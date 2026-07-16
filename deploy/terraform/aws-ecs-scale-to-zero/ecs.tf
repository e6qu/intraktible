# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Two ECS Fargate services on one cluster, both able to scale to zero:
#   - api:       the stateless read/write surface. Woken 0->1 by the waker Lambda on a
#                /wake request, scaled out 1->N by CPU target-tracking under load, and
#                scaled back to 0 by the reaper when the edge sees no traffic. Registered
#                in Cloud Map so API Gateway's VPC Link reaches it with no load balancer.
#   - scheduler: the singleton timed-sweep runner (monitor/drift alerts, timed deploy
#                activation, SLA breach). Runs the SAME image with INTRAKTIBLE_MONITOR_
#                INTERVAL set. In 'scheduled' mode it sits at 0 and is woken briefly on a
#                cron to avoid continuous compute; in 'warm' mode it stays 1.
#                It is NOT in Cloud Map — nothing routes HTTP to it.

resource "aws_ecs_cluster" "this" {
  count = local.use_existing_cluster ? 0 : 1
  name  = local.name
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_cloudwatch_log_group" "app" {
  name              = "/ecs/${local.name}"
  retention_in_days = 30
}

resource "aws_security_group" "ecs_tasks" {
  name_prefix = "${local.name}-ecs-"
  description = "ECS tasks: accept the app port from the API Gateway VPC Link, all egress"
  vpc_id      = local.vpc_id

  ingress {
    description     = "app port from the API Gateway VPC Link"
    from_port       = var.container_port
    to_port         = var.container_port
    protocol        = "tcp"
    security_groups = [aws_security_group.vpc_link.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle {
    create_before_destroy = true
  }
  tags = { Name = "${local.name}-ecs" }
}

# ---- Cloud Map service discovery (the API Gateway VPC Link integration target) -------

resource "aws_service_discovery_private_dns_namespace" "this" {
  name        = "${local.name}.internal"
  description = "Service discovery for intraktible ECS tasks"
  vpc         = local.vpc_id
}

resource "aws_service_discovery_service" "api" {
  name = "api"

  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.this.id
    routing_policy = "MULTIVALUE"
    # SRV so the registered record carries the task's port — API Gateway's HTTP_PROXY
    # VPC Link integration needs the port, which A records don't provide.
    dns_records {
      type = "SRV"
      ttl  = 15
    }
  }
  # ECS registers/deregisters task instances on the task lifecycle; no Cloud Map active
  # health check (SRV private records don't support one).
}

# ---- Task definitions ----------------------------------------------------------------

locals {
  base_environment = concat([
    { name = "INTRAKTIBLE_ENV", value = "production" },
    { name = "INTRAKTIBLE_SECURE_COOKIES", value = "1" },
    { name = "INTRAKTIBLE_TRUST_PROXY", value = "1" }, # X-Forwarded-Proto from CloudFront/API GW
    ], var.connector_allow_private ? [
    { name = "INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE", value = "1" }
  ] : [])

  app_secrets = [
    { name = "INTRAKTIBLE_POSTGRES_DSN", valueFrom = var.database_url_secret_arn },
    { name = "INTRAKTIBLE_ENCRYPTION_KEY", valueFrom = aws_secretsmanager_secret.encryption_key.arn },
    { name = "INTRAKTIBLE_BOOTSTRAP_API_KEY", valueFrom = aws_secretsmanager_secret.bootstrap_api_key.arn },
  ]

  oidc_environment = var.oidc_provider_name == "" ? [] : [
    { name = "INTRAKTIBLE_OIDC_PROVIDERS", value = var.oidc_provider_name },
    { name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_ISSUER", value = var.oidc_issuer },
    { name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_CLIENT_ID", value = var.oidc_client_id },
    { name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_REDIRECT_URL", value = var.oidc_redirect_url },
    { name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_ORG", value = var.oidc_org },
    { name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_WORKSPACE", value = var.oidc_workspace },
    { name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_DEFAULT_ROLE", value = var.oidc_default_role },
  ]
  oidc_secrets = var.oidc_provider_name == "" ? [] : [{ name = "INTRAKTIBLE_OIDC_${upper(var.oidc_provider_name)}_CLIENT_SECRET", valueFrom = aws_secretsmanager_secret.oidc_client[0].arn }]

  # The image's ENTRYPOINT hard-codes --store=sqlite; override it for the networked
  # Postgres log + store that lets N stateless tasks share one ordered log.
  app_command = ["serve", "--addr=:${var.container_port}", "--log=postgres", "--store=postgres"]

  # Pull credentials for a private registry (e.g. a private GHCR package). Empty when the
  # image is public. Merged into each container definition below.
  pull_credentials = var.image_pull_secret_arn != "" ? {
    repositoryCredentials = { credentialsParameter = var.image_pull_secret_arn }
  } : {}

  log_options = {
    "awslogs-group"         = aws_cloudwatch_log_group.app.name
    "awslogs-region"        = var.region
    "awslogs-stream-prefix" = "app"
  }
}

resource "aws_ecs_task_definition" "api" {
  family                   = "${local.name}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64" # Graviton/arm64 tasks (cheaper); the image is multi-arch
  }

  container_definitions = jsonencode([merge({
    name        = "app"
    image       = var.container_image
    essential   = true
    entryPoint  = ["/intraktible"]
    command     = local.app_command
    environment = concat(local.base_environment, local.oidc_environment)
    secrets     = concat(local.app_secrets, local.oidc_secrets)
    portMappings = [{
      containerPort = var.container_port
      protocol      = "tcp"
    }]
    logConfiguration = {
      logDriver = "awslogs"
      options   = local.log_options
    }
  }, local.pull_credentials)])
}

resource "aws_ecs_task_definition" "scheduler" {
  family                   = "${local.name}-scheduler"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64" # Graviton/arm64 tasks (cheaper); the image is multi-arch
  }

  container_definitions = jsonencode([merge({
    name       = "app"
    image      = var.container_image
    essential  = true
    entryPoint = ["/intraktible"]
    command    = local.app_command
    environment = concat(local.base_environment, local.oidc_environment, [
      { name = "INTRAKTIBLE_MONITOR_INTERVAL", value = var.monitor_interval },
    ])
    secrets = concat(local.app_secrets, local.oidc_secrets)
    logConfiguration = {
      logDriver = "awslogs"
      options   = local.log_options
    }
  }, local.pull_credentials)])
}

# ---- Services ------------------------------------------------------------------------

resource "aws_ecs_service" "api" {
  name                   = "${local.name}-api"
  cluster                = local.ecs_cluster_arn
  task_definition        = aws_ecs_task_definition.api.arn
  desired_count          = var.api_always_on ? 1 : 0
  launch_type            = "FARGATE"
  enable_execute_command = true

  network_configuration {
    subnets          = local.private_subnet_ids
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = false
  }

  service_registries {
    registry_arn   = aws_service_discovery_service.api.arn
    container_name = "app"
    container_port = var.container_port
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  # The task definition references secret ARNs, which does not create a Terraform
  # dependency on their values. Starting before AWSCURRENT exists makes Fargate fail
  # task initialization, so the service waits for every injected secret version.
  depends_on = [
    aws_secretsmanager_secret_version.bootstrap_api_key,
    aws_secretsmanager_secret_version.encryption_key,
    aws_secretsmanager_secret_version.oidc_client,
  ]

  lifecycle {
    # desired_count is owned at runtime by the waker/reaper Lambdas and CPU autoscaling.
    # In always-on mode the reaper is absent and the initial desired count stays at one.
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "scheduler" {
  name            = "${local.name}-scheduler"
  cluster         = local.ecs_cluster_arn
  task_definition = aws_ecs_task_definition.scheduler.arn
  desired_count   = var.scheduler_mode == "warm" ? 1 : 0
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = local.private_subnet_ids
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = false
  }

  depends_on = [
    aws_secretsmanager_secret_version.bootstrap_api_key,
    aws_secretsmanager_secret_version.encryption_key,
    aws_secretsmanager_secret_version.oidc_client,
  ]

  lifecycle {
    # In 'scheduled' mode the control Lambda toggles desired_count 0<->1 on a cron.
    ignore_changes = [desired_count]
  }
}

# ---- Autoscaling: CPU target-tracking scales the API service OUT only ----------------
# Scale-in is disabled here so a freshly-woken task (idle CPU, no traffic yet) is never
# reaped mid-wake. The 0<->1 transitions are owned by the waker (up) and reaper (down).

resource "aws_appautoscaling_target" "api" {
  service_namespace  = "ecs"
  resource_id        = "service/${local.ecs_cluster_name}/${aws_ecs_service.api.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  min_capacity       = var.api_always_on ? 1 : 0
  max_capacity       = var.api_max_tasks
}

resource "aws_appautoscaling_policy" "api_cpu" {
  name               = "${local.name}-api-cpu"
  service_namespace  = aws_appautoscaling_target.api.service_namespace
  resource_id        = aws_appautoscaling_target.api.resource_id
  scalable_dimension = aws_appautoscaling_target.api.scalable_dimension
  policy_type        = "TargetTrackingScaling"

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value       = 60
    scale_in_cooldown  = 60
    scale_out_cooldown = 30
    disable_scale_in   = true
  }
}
