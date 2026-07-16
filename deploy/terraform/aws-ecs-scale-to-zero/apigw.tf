# SPDX-License-Identifier: AGPL-3.0-or-later
#
# API Gateway HTTP API is the request-priced edge into the private VPC — it replaces an
# ALB/NLB, both of which bill hourly and cannot scale to zero. A VPC Link reaches the ECS
# tasks privately through Cloud Map service discovery (no load balancer at all), so at
# idle the only edge cost is CloudFront + this API's per-request charge, i.e. ~$0.
#
#   CloudFront ──/v1,/healthz,/readyz──▶ HTTP API ──VPC Link──▶ Cloud Map ──▶ ECS tasks
#              └─POST /wake──▶ waker Lambda (scales the API service 0->1)

resource "aws_security_group" "vpc_link" {
  name_prefix = "${local.name}-vpclink-"
  description = "API Gateway VPC Link ENIs: egress to the ECS task port"
  vpc_id      = local.vpc_id

  egress {
    description = "to the app port on ECS tasks"
    from_port   = var.container_port
    to_port     = var.container_port
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }
  lifecycle {
    create_before_destroy = true
  }
  tags = { Name = "${local.name}-vpclink" }
}

resource "aws_apigatewayv2_vpc_link" "this" {
  name               = local.name
  subnet_ids         = local.private_subnet_ids
  security_group_ids = [aws_security_group.vpc_link.id]
}

resource "aws_apigatewayv2_api" "this" {
  name          = local.name
  protocol_type = "HTTP"
}

# Private integration: HTTP API -> VPC Link -> Cloud Map service (the ECS tasks). When
# the API service is at 0 tasks there are no registered instances, so this returns 503 —
# the front end treats that as "cold" and calls POST /wake.
resource "aws_apigatewayv2_integration" "app" {
  api_id             = aws_apigatewayv2_api.this.id
  integration_type   = "HTTP_PROXY"
  integration_method = "ANY"
  connection_type    = "VPC_LINK"
  connection_id      = aws_apigatewayv2_vpc_link.this.id
  integration_uri    = aws_service_discovery_service.api.arn
}

resource "aws_apigatewayv2_route" "default" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.app.id}"
}

# The wake endpoint: a more specific route than $default, so it overrides it.
resource "aws_apigatewayv2_integration" "waker" {
  api_id                 = aws_apigatewayv2_api.this.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.waker.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "wake" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "POST /wake"
  target    = "integrations/${aws_apigatewayv2_integration.waker.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.this.id
  name        = "$default"
  auto_deploy = true

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.apigw.arn
    format = jsonencode({
      requestId        = "$context.requestId"
      ip               = "$context.identity.sourceIp"
      method           = "$context.httpMethod"
      route            = "$context.routeKey"
      status           = "$context.status"
      responseLen      = "$context.responseLength"
      integrationError = "$context.integration.error"
    })
  }
}

resource "aws_cloudwatch_log_group" "apigw" {
  name              = "/apigw/${local.name}"
  retention_in_days = 30
}
