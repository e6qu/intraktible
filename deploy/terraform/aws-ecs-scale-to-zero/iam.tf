# SPDX-License-Identifier: AGPL-3.0-or-later

data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Execution role: pulls the image, ships logs, and reads the injected secrets.
resource "aws_iam_role" "task_execution" {
  name               = "${local.name}-task-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "task_execution_managed" {
  role       = aws_iam_role.task_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "task_execution_secrets" {
  statement {
    sid     = "ReadInjectedSecrets"
    actions = ["secretsmanager:GetSecretValue"]
    resources = concat([
      aws_secretsmanager_secret.db_dsn.arn,
      aws_secretsmanager_secret.encryption_key.arn,
      aws_secretsmanager_secret.bootstrap_api_key.arn,
    ], var.image_pull_secret_arn != "" ? [var.image_pull_secret_arn] : [])
  }
}

resource "aws_iam_role_policy" "task_execution_secrets" {
  name   = "read-secrets"
  role   = aws_iam_role.task_execution.id
  policy = data.aws_iam_policy_document.task_execution_secrets.json
}

# Task role: the app itself. It reaches Postgres over the network (no IAM), so it needs
# only the SSM channel for `aws ecs execute-command` debugging.
resource "aws_iam_role" "task" {
  name               = "${local.name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

data "aws_iam_policy_document" "task_exec_command" {
  statement {
    sid = "ECSExecSSMChannel"
    actions = [
      "ssmmessages:CreateControlChannel",
      "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel",
      "ssmmessages:OpenDataChannel",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "task_exec_command" {
  name   = "ecs-exec"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_exec_command.json
}

# ---- Lambda role (shared by the waker and controller) --------------------------------

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${local.name}-lambda-control"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "lambda_logs" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "lambda_control" {
  statement {
    sid       = "ScaleServices"
    actions   = ["ecs:UpdateService", "ecs:DescribeServices"]
    resources = [aws_ecs_service.api.id, aws_ecs_service.scheduler.id]
  }
  statement {
    sid       = "ReadEdgeTraffic"
    actions   = ["cloudwatch:GetMetricData", "cloudwatch:GetMetricStatistics"]
    resources = ["*"] # GetMetricData does not support resource-level scoping
  }
}

resource "aws_iam_role_policy" "lambda_control" {
  name   = "control"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_control.json
}
