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
  name                        = "intraktible-test"
  region                      = "eu-west-1"
  existing_vpc_id             = "vpc-00000000000000000"
  existing_private_subnet_ids = ["subnet-00000000000000001", "subnet-00000000000000002"]
  existing_ecs_cluster_arn    = "arn:aws:ecs:eu-west-1:000000000000:cluster/dev"
  container_image             = "ghcr.io/e6qu/intraktible:0123456789ab"
  database_url_secret_arn     = "arn:aws:secretsmanager:eu-west-1:000000000000:secret:intraktible-database"
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
