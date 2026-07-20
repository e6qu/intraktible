# SPDX-License-Identifier: AGPL-3.0-or-later

output "app_url" {
  description = "Public URL of the app."
  value       = local.use_custom_domain ? "https://${var.domain_name}" : "https://${aws_cloudfront_distribution.this.domain_name}"
}

output "cloudfront_domain_name" {
  description = "CloudFront distribution domain (point your DNS CNAME/ALIAS here when using a custom domain)."
  value       = aws_cloudfront_distribution.this.domain_name
}

output "cloudfront_distribution_id" {
  description = "CloudFront distribution ID (for cache invalidations after a site sync)."
  value       = aws_cloudfront_distribution.this.id
}

output "site_bucket" {
  description = "S3 bucket holding the dehydrated static site; sync the SvelteKit build here."
  value       = aws_s3_bucket.site.bucket
}

output "site_sync_command" {
  description = "Publish the built static site + wasm to S3 and invalidate CloudFront."
  value       = "aws s3 sync web/build s3://${aws_s3_bucket.site.bucket}/ --delete && aws cloudfront create-invalidation --distribution-id ${aws_cloudfront_distribution.this.id} --paths '/*'"
}

output "api_gateway_endpoint" {
  description = "Direct HTTP API endpoint (normally reached through CloudFront, not directly)."
  value       = aws_apigatewayv2_api.this.api_endpoint
}

output "api_gateway_vpc_link_id" {
  description = "Amazon API Gateway VPC Link used by the private application integration, whether reused or dedicated."
  value       = local.api_gateway_vpc_link_id
}

output "creates_api_gateway_vpc_link" {
  description = "Whether this module owns a dedicated Amazon API Gateway VPC Link and its security group."
  value       = var.create_api_gateway_vpc_link
}

output "api_gateway_vpc_link_security_group_id" {
  description = "Security group attached to the Amazon API Gateway VPC Link and admitted by the Intraktible task security group."
  value       = local.api_gateway_vpc_link_security_group_id
}

output "wake_endpoint" {
  description = "POST here (through CloudFront) to wake the backend from zero."
  value       = "${local.use_custom_domain ? "https://${var.domain_name}" : "https://${aws_cloudfront_distribution.this.domain_name}"}/wake"
}

output "ecs_cluster" {
  description = "ECS cluster name."
  value       = local.ecs_cluster_name
}

output "api_service_name" {
  description = "API ECS service name (scales 0<->N)."
  value       = aws_ecs_service.api.name
}

output "scheduler_service_name" {
  description = "Scheduler ECS service name (singleton timed sweeps)."
  value       = aws_ecs_service.scheduler.name
}

output "service_security_group_id" {
  description = "Security group attached to Intraktible Amazon ECS tasks."
  value       = aws_security_group.ecs_tasks.id
}

output "bootstrap_api_key_secret_arn" {
  description = "Secrets Manager ARN of the first-admin bootstrap API key (rotate after first use)."
  value       = aws_secretsmanager_secret.bootstrap_api_key.arn
}
