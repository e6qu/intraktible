# SPDX-License-Identifier: AGPL-3.0-or-later
# Example variables. Copy to a private *.tfvars (git-ignored) and adjust.
#   terraform apply -var-file=my.tfvars

# Required: the backend image the ECS task pulls (build/push the repo Dockerfile first).
container_image = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/intraktible:latest"

region   = "eu-west-1"
name     = "intraktible"
az_count = 2

# Scale-to-zero tuning.
idle_scale_in_minutes = 20         # scale the API to 0 after this idle window
api_max_tasks         = 4          # burst ceiling under load
aurora_min_acu        = 0          # 0 = pause the database to zero when idle
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
