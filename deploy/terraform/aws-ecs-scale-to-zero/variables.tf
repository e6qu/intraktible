# SPDX-License-Identifier: AGPL-3.0-or-later

variable "name" {
  description = "Name prefix for all resources (DNS-safe, lowercase)."
  type        = string
  default     = "intraktible"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.name))
    error_message = "name must be lowercase alphanumeric/hyphen, 2-31 chars, starting with a letter."
  }
}

variable "region" {
  description = "AWS region for the regional resources (VPC, ECS, Aurora, API Gateway)."
  type        = string
  default     = "eu-west-1"
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC."
  type        = string
  default     = "10.42.0.0/16"
}

variable "az_count" {
  description = "Number of Availability Zones to spread private subnets across (>=2 for Aurora)."
  type        = number
  default     = 2

  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3 (Aurora requires a subnet group spanning >=2 AZs)."
  }
}

variable "container_image" {
  description = "Full multi-arch image reference for the intraktible backend (e.g. ghcr.io/e6qu/intraktible:main or a pinned :1.4.2, published by the release workflow). Tasks run arm64, which the multi-arch manifest covers."
  type        = string
}

variable "image_pull_secret_arn" {
  description = "Secrets Manager ARN of a {username,password} secret for pulling the image from a private registry (e.g. a private GHCR package). Empty means the image is public and no pull secret is used."
  type        = string
  default     = ""
}

variable "container_port" {
  description = "Port the backend listens on inside the task."
  type        = number
  default     = 8080
}

variable "task_cpu" {
  description = "Fargate task CPU units for the API service (256 = 0.25 vCPU)."
  type        = number
  default     = 512
}

variable "task_memory" {
  description = "Fargate task memory (MiB) for the API service."
  type        = number
  default     = 1024
}

variable "api_max_tasks" {
  description = "Maximum API tasks when scaled up under load (scales to 0 when idle)."
  type        = number
  default     = 4
}

variable "idle_scale_in_minutes" {
  description = "Minutes with no requests (per API Gateway CloudWatch metric) before the reaper scales the API service back to 0."
  type        = number
  default     = 20
}

variable "scheduler_mode" {
  description = "How the singleton timed-sweep scheduler runs. 'scheduled' = event-driven, scaled 0->1 by EventBridge on a window then reaped (true scale-to-zero; see README caveats). 'warm' = one always-on task (correct today, ~small fixed cost)."
  type        = string
  default     = "scheduled"

  validation {
    condition     = contains(["scheduled", "warm"], var.scheduler_mode)
    error_message = "scheduler_mode must be 'scheduled' or 'warm'."
  }
}

variable "scheduler_window_cron" {
  description = "EventBridge Scheduler cron (UTC) that wakes the scheduler service to run a sweep pass. Only used when scheduler_mode = 'scheduled'. Default: every 15 minutes."
  type        = string
  default     = "cron(0/15 * * * ? *)"
}

variable "scheduler_run_minutes" {
  description = "Minutes the scheduler service stays up per scheduled wake to run sweeps before being scaled back to 0. Only used when scheduler_mode = 'scheduled'. Must be <= 14 (Lambda timeout ceiling)."
  type        = number
  default     = 5

  validation {
    condition     = var.scheduler_run_minutes >= 1 && var.scheduler_run_minutes <= 14
    error_message = "scheduler_run_minutes must be between 1 and 14."
  }
}

variable "monitor_interval" {
  description = "INTRAKTIBLE_MONITOR_INTERVAL for the scheduler service (the timed-sweep cadence). Only the scheduler service sets it; the API service leaves it unset."
  type        = string
  default     = "1m"
}

variable "aurora_min_acu" {
  description = "Aurora Serverless v2 minimum capacity. 0 lets the cluster pause to zero when idle (resumes on the next connection)."
  type        = number
  default     = 0
}

variable "aurora_max_acu" {
  description = "Aurora Serverless v2 maximum capacity units."
  type        = number
  default     = 4
}

variable "aurora_seconds_until_auto_pause" {
  description = "Idle seconds before Aurora Serverless v2 scales to 0 ACU. Only effective when aurora_min_acu = 0. AWS allows 300-86400."
  type        = number
  default     = 300
}

variable "domain_name" {
  description = "Optional custom domain for the CloudFront distribution (e.g. app.example.com). Empty uses the default *.cloudfront.net domain."
  type        = string
  default     = ""
}

variable "acm_certificate_arn" {
  description = "ACM certificate ARN in us-east-1 for domain_name. Required when domain_name is set."
  type        = string
  default     = ""
}

variable "fck_nat_instance_type" {
  description = "Instance type for the fck-nat egress NAT (t4g.nano is ample for control-plane egress)."
  type        = string
  default     = "t4g.nano"
}

variable "fck_nat_ami_id" {
  description = "Override the fck-nat AMI. Empty resolves the latest published fck-nat AL2023 arm64 AMI."
  type        = string
  default     = ""
}

variable "connector_allow_private" {
  description = "Set INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE=1 (lets connectors reach private hosts). Leave false unless you understand the SSRF surface; the cloud metadata service stays blocked regardless."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Extra tags applied to every resource."
  type        = map(string)
  default     = {}
}
