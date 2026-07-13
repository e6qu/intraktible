# SPDX-License-Identifier: AGPL-3.0-or-later
# Example variables. Copy to a private *.tfvars (git-ignored) and adjust.
#   terraform apply -var-file=my.tfvars

# Required: the multi-arch image the ECS task pulls. The release workflow publishes
# ghcr.io/e6qu/intraktible on every merge to main (:main, :sha-<short>) and version tags
# (:1.4.2, :1.4, :1) — no :latest. Pin a version in production; :main is the rolling tip.
# Tasks run arm64, which the multi-arch manifest covers.
container_image = "ghcr.io/e6qu/intraktible:main"

# If the GHCR package is private, point this at a Secrets Manager {username,password}
# secret with a GHCR pull token (or make the package public / mirror to ECR).
# image_pull_secret_arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:ghcr-pull-..."

region   = "eu-west-1"
name     = "intraktible"
az_count = 2

# Scale-to-zero tuning.
idle_scale_in_minutes = 20 # scale the API to 0 after this idle window
api_max_tasks         = 4  # burst ceiling under load
aurora_min_acu        = 0  # 0 = pause the database to zero when idle
aurora_max_acu        = 4

# Event-driven scheduler (keeps Aurora paused between sweeps). Use "warm" for one
# always-on scheduler task instead (correct, but the database no longer pauses).
scheduler_mode        = "scheduled"
scheduler_window_cron = "cron(0/15 * * * ? *)" # wake the scheduler every 15 minutes
scheduler_run_minutes = 5
monitor_interval      = "1m"

# Optional custom domain (needs an ACM cert in us-east-1 for CloudFront).
# domain_name         = "app.example.com"
# acm_certificate_arn = "arn:aws:acm:us-east-1:123456789012:certificate/..."
