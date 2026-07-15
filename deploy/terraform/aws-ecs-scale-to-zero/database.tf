# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Aurora PostgreSQL Serverless v2 with a minimum capacity of 0 ACU: when idle the cluster
# scales to zero (no compute billed, storage only) and resumes on the next connection.
# This is the "database scaled to zero" half of the design. The backend uses it as BOTH
# the event log (--log=postgres) and the projection store (--store=postgres), so N
# stateless API tasks share one ordered log and durable read models.

locals {
  db_name     = "intraktible"
  db_username = "intraktible"
}

resource "aws_db_subnet_group" "this" {
  name       = "${local.name}-db"
  subnet_ids = aws_subnet.private[*].id
  tags       = { Name = "${local.name}-db" }
}

resource "aws_security_group" "db" {
  name_prefix = "${local.name}-db-"
  description = "Aurora: accept Postgres only from the ECS task security group"
  vpc_id      = aws_vpc.this.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle {
    create_before_destroy = true
  }
  tags = { Name = "${local.name}-db" }
}

# Ingress is a separate rule so the ECS SG and DB SG can reference each other without a
# cycle (the ECS SG is defined in ecs.tf).
resource "aws_security_group_rule" "db_from_ecs" {
  type                     = "ingress"
  description              = "Postgres from ECS tasks"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  security_group_id        = aws_security_group.db.id
  source_security_group_id = aws_security_group.ecs_tasks.id
}

resource "aws_rds_cluster" "this" {
  cluster_identifier = local.name
  engine             = "aurora-postgresql"
  engine_mode        = "provisioned" # Serverless v2 runs under the provisioned engine
  engine_version     = "16.6"
  database_name      = local.db_name
  master_username    = local.db_username
  master_password    = random_password.db.result
  port               = 5432

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]

  storage_encrypted = true

  serverlessv2_scaling_configuration {
    min_capacity             = var.aurora_min_acu
    max_capacity             = var.aurora_max_acu
    seconds_until_auto_pause = var.aurora_min_acu == 0 ? var.aurora_seconds_until_auto_pause : null
  }

  backup_retention_period      = var.aurora_backup_retention_days
  preferred_backup_window      = "03:00-04:00"
  copy_tags_to_snapshot        = true
  deletion_protection          = true
  skip_final_snapshot          = false
  final_snapshot_identifier    = "${local.name}-final"
  apply_immediately            = false
  performance_insights_enabled = true

  lifecycle {
    ignore_changes = [master_password] # rotate via Secrets Manager, not by replacing the cluster
  }
}

resource "aws_rds_cluster_instance" "this" {
  identifier         = "${local.name}-0"
  cluster_identifier = aws_rds_cluster.this.id
  engine             = aws_rds_cluster.this.engine
  engine_version     = aws_rds_cluster.this.engine_version
  instance_class     = "db.serverless"

  performance_insights_enabled = true
  # A resuming-from-zero cluster is not reachable for a few seconds; the backend retries
  # its first connection, and /readyz gates traffic until projections catch up.
}
