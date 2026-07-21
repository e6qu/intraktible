# SPDX-License-Identifier: AGPL-3.0-or-later

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = {
      names = ["eu-west-1a", "eu-west-1b", "eu-west-1c"]
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
}

mock_provider "aws" {
  alias = "us_east_1"
}

mock_provider "archive" {}
mock_provider "random" {}

variables {
  name                         = "intraktible-test"
  region                       = "eu-west-1"
  existing_vpc_id              = "vpc-00000000000000000"
  existing_private_subnet_ids  = ["subnet-00000000000000001", "subnet-00000000000000002"]
  existing_ecs_cluster_arn     = "arn:aws:ecs:eu-west-1:000000000000:cluster/dev"
  container_image              = "ghcr.io/e6qu/intraktible:0123456789ab"
  application_release_revision = "0123456789ab"
  database_url_secret_arn      = "arn:aws:secretsmanager:eu-west-1:000000000000:secret:intraktible-database"
}

run "reject_mutable_application_release_revision" {
  command = plan

  variables {
    application_release_revision = "latest"
  }

  expect_failures = [var.application_release_revision]
}

run "reject_short_application_release_revision" {
  command = plan

  variables {
    application_release_revision = "0123456789a"
  }

  expect_failures = [var.application_release_revision]
}

run "reject_long_application_release_revision" {
  command = plan

  variables {
    application_release_revision = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"
  }

  expect_failures = [var.application_release_revision]
}

run "reject_uppercase_application_release_revision" {
  command = plan

  variables {
    application_release_revision = "ABCDEF012345"
  }

  expect_failures = [var.application_release_revision]
}

run "inject_application_release_revision_into_shared_task_environment" {
  command = plan

  assert {
    condition = one([
      for item in local.base_environment : item.value
      if item.name == "APPLICATION_RELEASE_REVISION"
    ]) == "0123456789ab"
    error_message = "Every application task must receive the exact immutable application release revision through its shared environment."
  }
}

run "accept_maximum_length_application_release_revision" {
  command = plan

  variables {
    application_release_revision = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}

run "accept_application_release_digest" {
  command = plan

  variables {
    application_release_revision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }

  assert {
    condition = one([
      for item in local.base_environment : item.value
      if item.name == "APPLICATION_RELEASE_REVISION"
    ]) == "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    error_message = "Every application task must receive the exact immutable SHA-256 application release digest through its shared environment."
  }
}

run "accept_exact_shauth_logout_bridge" {
  command = plan

  variables {
    domain_name                   = "intraktible.example.test"
    acm_certificate_arn           = "arn:aws:acm:us-east-1:000000000000:certificate/00000000-0000-0000-0000-000000000000"
    oidc_provider_name            = "shauth"
    oidc_issuer                   = "https://auth.example.test"
    oidc_client_id                = "intraktible"
    oidc_client_secret            = "test-only-secret"
    oidc_redirect_url             = "https://intraktible.example.test/v1/auth/oidc/shauth/callback"
    oidc_post_logout_redirect_url = "https://intraktible.example.test/auth/shauth/logout/complete"
    oidc_org                      = "e6qu"
    oidc_workspace                = "dev"
  }
}

run "reject_signed_out_page_as_shauth_logout_bridge" {
  command = plan

  variables {
    domain_name                   = "intraktible.example.test"
    acm_certificate_arn           = "arn:aws:acm:us-east-1:000000000000:certificate/00000000-0000-0000-0000-000000000000"
    oidc_provider_name            = "shauth"
    oidc_issuer                   = "https://auth.example.test"
    oidc_client_id                = "intraktible"
    oidc_client_secret            = "test-only-secret"
    oidc_redirect_url             = "https://intraktible.example.test/v1/auth/oidc/shauth/callback"
    oidc_post_logout_redirect_url = "https://intraktible.example.test/v1/auth/signed-out"
    oidc_org                      = "e6qu"
    oidc_workspace                = "dev"
  }

  expect_failures = [check.oidc_coordinates]
}

run "create_dedicated_vpc_link_by_default" {
  command = plan

  assert {
    condition     = length(aws_security_group.dedicated_vpc_link) == 1
    error_message = "The standalone module must create one dedicated Amazon API Gateway VPC Link security group."
  }

  assert {
    condition     = length(aws_apigatewayv2_vpc_link.dedicated) == 1
    error_message = "The standalone module must create one dedicated Amazon API Gateway VPC Link."
  }
}

run "reuse_existing_vpc_link" {
  command = plan

  variables {
    create_api_gateway_vpc_link                     = false
    existing_api_gateway_vpc_link_id                = "vpclink-0123456789abcdef0"
    existing_api_gateway_vpc_link_security_group_id = "sg-0123456789abcdef0"
  }

  assert {
    condition     = length(aws_security_group.dedicated_vpc_link) == 0
    error_message = "Reusing an Amazon API Gateway VPC Link must not create a dedicated security group."
  }

  assert {
    condition     = length(aws_apigatewayv2_vpc_link.dedicated) == 0
    error_message = "Reusing an Amazon API Gateway VPC Link must not create another link."
  }

  assert {
    condition     = aws_apigatewayv2_integration.app.connection_id == "vpclink-0123456789abcdef0"
    error_message = "The API integration must use the supplied Amazon API Gateway VPC Link."
  }

  assert {
    condition     = toset(tolist(aws_security_group.ecs_tasks.ingress)[0].security_groups) == toset(["sg-0123456789abcdef0"])
    error_message = "The task security group must admit the supplied Amazon API Gateway VPC Link security group."
  }

  assert {
    condition     = output.api_gateway_vpc_link_id == "vpclink-0123456789abcdef0" && output.api_gateway_vpc_link_security_group_id == "sg-0123456789abcdef0"
    error_message = "The module outputs must expose the reused Amazon API Gateway VPC Link coordinates."
  }
}

run "reject_vpc_link_without_security_group" {
  command = plan

  variables {
    create_api_gateway_vpc_link      = false
    existing_api_gateway_vpc_link_id = "vpclink-0123456789abcdef0"
  }

  expect_failures = [aws_apigatewayv2_integration.app]
}

run "reject_security_group_without_vpc_link" {
  command = plan

  variables {
    create_api_gateway_vpc_link                     = false
    existing_api_gateway_vpc_link_security_group_id = "sg-0123456789abcdef0"
  }

  expect_failures = [aws_apigatewayv2_integration.app]
}

run "reject_reuse_without_coordinates" {
  command = plan

  variables {
    create_api_gateway_vpc_link = false
  }

  expect_failures = [aws_apigatewayv2_integration.app]
}

run "reject_existing_coordinates_while_creating" {
  command = plan

  variables {
    existing_api_gateway_vpc_link_id                = "vpclink-0123456789abcdef0"
    existing_api_gateway_vpc_link_security_group_id = "sg-0123456789abcdef0"
  }

  expect_failures = [aws_apigatewayv2_integration.app]
}

run "accept_resource_derived_shared_coordinates" {
  command = plan

  module {
    source = "./tests/fixtures/shared-unknown"
  }

  assert {
    condition     = output.creates_dedicated_vpc_link == false
    error_message = "Resource-derived unknown shared coordinates must leave dedicated Amazon API Gateway VPC Link ownership disabled at plan time."
  }
}
