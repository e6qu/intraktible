# SPDX-License-Identifier: AGPL-3.0-or-later

terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source                = "hashicorp/aws"
      version               = ">= 5.60" # CloudFront VPC origins
      configuration_aliases = [aws.us_east_1]
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
