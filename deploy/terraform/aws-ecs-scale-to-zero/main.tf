# SPDX-License-Identifier: AGPL-3.0-or-later

provider "aws" {
  region = var.region

  default_tags {
    tags = merge({
      "app"        = var.name
      "managed-by" = "terraform"
      "module"     = "aws-ecs-scale-to-zero"
    }, var.tags)
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name = var.name
  azs  = slice(data.aws_availability_zones.available.names, 0, var.az_count)

  # /20 public + /20 private subnets carved from the VPC CIDR, one pair per AZ.
  public_subnet_cidrs  = [for i in range(var.az_count) : cidrsubnet(var.vpc_cidr, 4, i)]
  private_subnet_cidrs = [for i in range(var.az_count) : cidrsubnet(var.vpc_cidr, 4, i + 8)]

  use_custom_domain = var.domain_name != ""
}
