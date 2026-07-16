# SPDX-License-Identifier: AGPL-3.0-or-later

terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60" # CloudFront VPC origins
    }
    archive = {
      source  = "hashicorp/archive"
      version = ">= 2.4"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.5"
    }
  }
}

# CloudFront is a global service; its ACM certificate MUST live in us-east-1. This
# aliased provider is used only for the CloudFront cert + distribution-scoped lookups.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}
