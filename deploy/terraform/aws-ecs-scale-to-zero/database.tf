# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The PostgreSQL database the backend uses as BOTH the event log (--log=postgres) and the
# projection store (--store=postgres), so N stateless API tasks share one ordered log and
# durable read models. Two engines, selected by var.db_serverless:
#
#   true (default): Aurora PostgreSQL Serverless v2, min 0 ACU — the cluster scales to zero
#                   when idle and resumes on the next connection (the "database scaled to
#                   zero" half of the design). Requires a standard/paid account; AWS Free
#                   Plan accounts cannot create Aurora via Terraform.
#   false:          a single free-tier RDS instance (db.t4g.micro). Always-on but
#                   free-tier-eligible; the compute + edge still scale to zero. Use this on
#                   a Free Plan account.

locals {
  db_name     = "intraktible"
  db_username = "intraktible"
  # The writer endpoint hostname, from whichever engine is active (one() is empty-safe).
  db_endpoint = var.db_serverless ? one(aws_rds_cluster.this[*].endpoint) : one(aws_db_instance.this[*].address)
}

resource "aws_db_subnet_group" "this" {
  name       = "${local.name}-db"
  subnet_ids = aws_subnet.private[*].id
  tags       = { Name = "${local.name}-db" }
}

resource "aws_security_group" "db" {
  name_prefix = "${local.name}-db-"
  description = "Database: accept Postgres only from the ECS task security group"
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

# ---- Aurora Serverless v2 (db_serverless = true) -------------------------------------

resource "aws_rds_cluster" "this" {
  count              = var.db_serverless ? 1 : 0
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
  storage_encrypted      = true

  serverlessv2_scaling_configuration {
    min_capacity             = var.aurora_min_acu
    max_capacity             = var.aurora_max_acu
    seconds_until_auto_pause = var.aurora_min_acu == 0 ? var.aurora_seconds_until_auto_pause : null
  }

  backup_retention_period      = var.db_backup_retention_days
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
  count              = var.db_serverless ? 1 : 0
  identifier         = "${local.name}-0"
  cluster_identifier = aws_rds_cluster.this[0].id
  engine             = aws_rds_cluster.this[0].engine
  engine_version     = aws_rds_cluster.this[0].engine_version
  instance_class     = "db.serverless"

  performance_insights_enabled = true
  # A resuming-from-zero cluster is not reachable for a few seconds; the backend retries
  # its first connection, and /readyz gates traffic until projections catch up.
}

# ---- Free-tier single RDS instance (db_serverless = false) ---------------------------
# A db.t4g.micro PostgreSQL instance: free-tier-eligible and always-on (it does not pause,
# but is effectively free on a Free Plan and avoids Aurora's resume-from-zero latency).

resource "aws_db_instance" "this" {
  count      = var.db_serverless ? 0 : 1
  identifier = local.name

  engine         = "postgres"
  engine_version = var.postgres_version
  instance_class = var.db_instance_class

  allocated_storage = var.db_allocated_storage
  storage_type      = "gp2"
  storage_encrypted = true

  db_name  = local.db_name
  username = local.db_username
  password = random_password.db.result
  port     = 5432

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]
  publicly_accessible    = false
  multi_az               = false

  backup_retention_period = var.db_backup_retention_days
  copy_tags_to_snapshot   = true
  deletion_protection     = false # a free-tier demo DB; keep teardown simple
  skip_final_snapshot     = true
  apply_immediately       = false

  # Performance Insights is not free-tier-eligible; leave it off on the free-tier path.
  performance_insights_enabled = false

  lifecycle {
    ignore_changes = [password]
  }
}
