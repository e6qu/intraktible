# SPDX-License-Identifier: AGPL-3.0-or-later

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60"
    }
  }
}

resource "terraform_data" "api_gateway_vpc_link" {
  input = "environment-level-link"
}

resource "terraform_data" "api_gateway_vpc_link_security_group" {
  input = "environment-level-link-security-group"
}

provider "aws" {
  region = "eu-west-1"
}

provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}

module "intraktible" {
  source = "../../.."

  providers = {
    aws           = aws
    aws.us_east_1 = aws.us_east_1
  }

  name                         = "intraktible-unknown-test"
  region                       = "eu-west-1"
  existing_vpc_id              = "vpc-00000000000000000"
  existing_private_subnet_ids  = ["subnet-00000000000000001", "subnet-00000000000000002"]
  existing_ecs_cluster_arn     = "arn:aws:ecs:eu-west-1:000000000000:cluster/dev"
  container_image              = "ghcr.io/e6qu/intraktible:0123456789ab"
  application_release_revision = "0123456789ab"
  database_url_secret_arn      = "arn:aws:secretsmanager:eu-west-1:000000000000:secret:intraktible-database"

  create_api_gateway_vpc_link                     = false
  existing_api_gateway_vpc_link_id                = terraform_data.api_gateway_vpc_link.id
  existing_api_gateway_vpc_link_security_group_id = terraform_data.api_gateway_vpc_link_security_group.id
}

output "creates_dedicated_vpc_link" {
  value = module.intraktible.creates_api_gateway_vpc_link
}
