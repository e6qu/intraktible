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

output "wake_endpoint" {
  description = "POST here (through CloudFront) to wake the backend from zero."
  value       = "${local.use_custom_domain ? "https://${var.domain_name}" : "https://${aws_cloudfront_distribution.this.domain_name}"}/wake"
}

output "ecs_cluster" {
  description = "ECS cluster name."
  value       = aws_ecs_cluster.this.name
}

output "api_service_name" {
  description = "API ECS service name (scales 0<->N)."
  value       = aws_ecs_service.api.name
}

output "scheduler_service_name" {
  description = "Scheduler ECS service name (singleton timed sweeps)."
  value       = aws_ecs_service.scheduler.name
}

output "db_endpoint" {
  description = "Database writer endpoint (Aurora Serverless v2 or the free-tier RDS instance)."
  value       = local.db_endpoint
}

output "db_dsn_secret_arn" {
  description = "Secrets Manager ARN of the composed Postgres DSN."
  value       = aws_secretsmanager_secret.db_dsn.arn
}

output "bootstrap_api_key_secret_arn" {
  description = "Secrets Manager ARN of the first-admin bootstrap API key (rotate after first use)."
  value       = aws_secretsmanager_secret.bootstrap_api_key.arn
}
