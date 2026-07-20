# SPDX-License-Identifier: AGPL-3.0-or-later
# Example variables. Copy to a private *.tfvars (git-ignored) and adjust.
#   terraform apply -var-file=my.tfvars

# Required: the multi-arch image the ECS task pulls. The release workflow publishes a
# 12-character commit-SHA manifest plus matching -arm64 and -amd64 images on every merge.
# Pin the immutable manifest; tasks run arm64, which the manifest covers.
container_image = "ghcr.io/e6qu/intraktible:0123456789ab"

# If the GHCR package is private, point this at a Secrets Manager {username,password}
# secret with a GHCR pull token (or make the package public / mirror to ECR).
# image_pull_secret_arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ghcr-pull-..."

region   = "eu-west-1"
name     = "intraktible"
az_count = 2

# To reuse an environment-level Amazon API Gateway VPC Link, set the explicit,
# plan-known ownership switch and supply both coordinates:
# create_api_gateway_vpc_link                     = false
# existing_api_gateway_vpc_link_id                = "vpclink-..."
# existing_api_gateway_vpc_link_security_group_id = "sg-..."

# Scale-to-zero tuning.
idle_scale_in_minutes = 20 # scale the API to 0 after this idle window
api_max_tasks         = 4  # burst ceiling under load
# Event-driven scheduler. Use "warm" for one always-on scheduler task instead.
scheduler_mode        = "scheduled"
scheduler_window_cron = "cron(0/15 * * * ? *)" # wake the scheduler every 15 minutes
scheduler_run_minutes = 5
monitor_interval      = "1m"

# Optional custom domain (needs an ACM cert in us-east-1 for CloudFront).
# domain_name         = "app.example.com"
# acm_certificate_arn = "arn:aws:acm:us-east-1:123456789012:certificate/..."
