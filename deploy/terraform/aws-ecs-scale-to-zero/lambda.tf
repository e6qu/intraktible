# SPDX-License-Identifier: AGPL-3.0-or-later

data "archive_file" "waker" {
  type        = "zip"
  source_dir  = "${path.module}/lambda/waker"
  output_path = "${path.module}/.build/waker.zip"
}

data "archive_file" "controller" {
  type        = "zip"
  source_dir  = "${path.module}/lambda/controller"
  output_path = "${path.module}/.build/controller.zip"
}

resource "aws_lambda_function" "waker" {
  function_name    = "${local.name}-waker"
  role             = aws_iam_role.lambda.arn
  runtime          = "python3.12"
  handler          = "index.handler"
  filename         = data.archive_file.waker.output_path
  source_code_hash = data.archive_file.waker.output_base64sha256
  timeout          = 10
  memory_size      = 128

  environment {
    variables = {
      CLUSTER     = local.ecs_cluster_name
      API_SERVICE = aws_ecs_service.api.name
      WAKE_TO     = "1"
    }
  }
}

resource "aws_lambda_function" "controller" {
  function_name    = "${local.name}-controller"
  role             = aws_iam_role.lambda.arn
  runtime          = "python3.12"
  handler          = "index.handler"
  filename         = data.archive_file.controller.output_path
  source_code_hash = data.archive_file.controller.output_base64sha256
  timeout          = var.scheduler_run_minutes * 60 + 90 # must outlast the sweep sleep
  memory_size      = 128

  environment {
    variables = {
      CLUSTER               = local.ecs_cluster_name
      API_SERVICE           = aws_ecs_service.api.name
      SCHEDULER_SERVICE     = aws_ecs_service.scheduler.name
      API_ID                = aws_apigatewayv2_api.this.id
      API_STAGE             = "$default"
      IDLE_MINUTES          = tostring(var.idle_scale_in_minutes)
      SCHEDULER_RUN_MINUTES = tostring(var.scheduler_run_minutes)
    }
  }
}

# API Gateway is allowed to invoke the waker (the POST /wake route integration).
resource "aws_lambda_permission" "waker_apigw" {
  statement_id  = "AllowApiGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.waker.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.this.execution_arn}/*/*"
}
