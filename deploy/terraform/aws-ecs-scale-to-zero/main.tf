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
  # Managed-DNS mode: the module owns the ACM cert + Route53 alias records for domain_name.
  manage_dns           = var.domain_name != "" && var.route53_zone_id != ""
  use_existing_network = var.existing_vpc_id != ""
  use_existing_cluster = var.existing_ecs_cluster_arn != ""
  vpc_id               = local.use_existing_network ? var.existing_vpc_id : aws_vpc.this[0].id
  private_subnet_ids   = local.use_existing_network ? var.existing_private_subnet_ids : aws_subnet.private[*].id
  ecs_cluster_arn      = local.use_existing_cluster ? var.existing_ecs_cluster_arn : aws_ecs_cluster.this[0].arn
  ecs_cluster_name     = local.use_existing_cluster ? element(split("/", var.existing_ecs_cluster_arn), 1) : aws_ecs_cluster.this[0].name
}

check "shared_network_coordinates" {
  assert {
    condition     = (var.existing_vpc_id == "") == (length(var.existing_private_subnet_ids) == 0)
    error_message = "existing_vpc_id and existing_private_subnet_ids must be supplied together."
  }
}

check "oidc_coordinates" {
  assert {
    condition     = var.oidc_provider_name == "" || (var.oidc_issuer != "" && var.oidc_client_id != "" && var.oidc_client_secret != "" && var.oidc_redirect_url != "" && var.oidc_post_logout_redirect_url != "" && var.oidc_org != "" && var.oidc_workspace != "")
    error_message = "An OIDC provider requires issuer, client ID, client secret, redirect URL, app-origin post-logout redirect URL, organization, and workspace."
  }
  assert {
    condition     = var.oidc_provider_name == "" || var.domain_name == "" || (var.oidc_redirect_url == "https://${var.domain_name}/v1/auth/oidc/${var.oidc_provider_name}/callback" && var.oidc_post_logout_redirect_url == "https://${var.domain_name}/v1/auth/signed-out")
    error_message = "OIDC redirect coordinates must use the module domain and the standard callback and signed-out routes."
  }
}

check "embedded_ui_requires_always_on_api" {
  assert {
    condition     = !var.serve_embedded_ui_from_api || var.api_always_on
    error_message = "serve_embedded_ui_from_api requires api_always_on because the UI is served by the API task."
  }
}
