# SPDX-License-Identifier: AGPL-3.0-or-later
#
# A private VPC: the ECS tasks and Aurora live in private subnets with no public IPs.
# Public subnets exist only to host the fck-nat egress instance. There is no NAT
# Gateway (an always-on ~$32/mo + data cost) — fck-nat is a single small instance that
# provides outbound-only egress for the private subnets at a fraction of the cost.

resource "aws_vpc" "this" {
  count                = local.use_existing_network ? 0 : 1
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.name }
}

resource "aws_internet_gateway" "this" {
  count  = local.use_existing_network ? 0 : 1
  vpc_id = aws_vpc.this[0].id
  tags   = { Name = local.name }
}

resource "aws_subnet" "public" {
  count                   = local.use_existing_network ? 0 : var.az_count
  vpc_id                  = aws_vpc.this[0].id
  cidr_block              = local.public_subnet_cidrs[count.index]
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true
  tags                    = { Name = "${local.name}-public-${local.azs[count.index]}", tier = "public" }
}

resource "aws_subnet" "private" {
  count             = local.use_existing_network ? 0 : var.az_count
  vpc_id            = aws_vpc.this[0].id
  cidr_block        = local.private_subnet_cidrs[count.index]
  availability_zone = local.azs[count.index]
  tags              = { Name = "${local.name}-private-${local.azs[count.index]}", tier = "private" }
}

resource "aws_route_table" "public" {
  count  = local.use_existing_network ? 0 : 1
  vpc_id = aws_vpc.this[0].id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this[0].id
  }
  tags = { Name = "${local.name}-public" }
}

resource "aws_route_table_association" "public" {
  count          = local.use_existing_network ? 0 : var.az_count
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

# One private route table per AZ so egress can be pinned to the fck-nat ENI. A single
# fck-nat instance serves all AZs (cross-AZ egress traffic is charged; acceptable for
# low-volume control-plane egress — connector calls, OTLP, package/DNS).
resource "aws_route_table" "private" {
  count  = local.use_existing_network ? 0 : var.az_count
  vpc_id = aws_vpc.this[0].id
  route {
    cidr_block           = "0.0.0.0/0"
    network_interface_id = aws_network_interface.fck_nat[0].id
  }
  tags = { Name = "${local.name}-private-${local.azs[count.index]}" }
}

resource "aws_route_table_association" "private" {
  count          = local.use_existing_network ? 0 : var.az_count
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}

# ---- fck-nat egress instance ---------------------------------------------------------

data "aws_ami" "fck_nat" {
  count       = !local.use_existing_network && var.fck_nat_ami_id == "" ? 1 : 0
  owners      = ["568608671756"] # fck-nat's publisher account
  most_recent = true
  filter {
    name   = "name"
    values = ["fck-nat-al2023-*-arm64-ebs"]
  }
  filter {
    name   = "architecture"
    values = ["arm64"]
  }
}

resource "aws_security_group" "fck_nat" {
  count       = local.use_existing_network ? 0 : 1
  name_prefix = "${local.name}-fck-nat-"
  description = "fck-nat egress: accept traffic from the VPC, allow all outbound"
  vpc_id      = aws_vpc.this[0].id

  ingress {
    description = "traffic from within the VPC to be NATed"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.vpc_cidr]
  }
  egress {
    description = "outbound to the internet"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle {
    create_before_destroy = true
  }
  tags = { Name = "${local.name}-fck-nat" }
}

# A dedicated ENI with source/dest check disabled, so it can forward (NAT) traffic and
# so the private route tables have a stable target across instance replacement.
resource "aws_network_interface" "fck_nat" {
  count             = local.use_existing_network ? 0 : 1
  subnet_id         = aws_subnet.public[0].id
  security_groups   = [aws_security_group.fck_nat[0].id]
  source_dest_check = false
  tags              = { Name = "${local.name}-fck-nat" }
}

resource "aws_instance" "fck_nat" {
  count         = local.use_existing_network ? 0 : 1
  ami           = var.fck_nat_ami_id != "" ? var.fck_nat_ami_id : data.aws_ami.fck_nat[0].id
  instance_type = var.fck_nat_instance_type

  network_interface {
    network_interface_id = aws_network_interface.fck_nat[0].id
    device_index         = 0
  }

  metadata_options {
    http_tokens   = "required" # IMDSv2 only
    http_endpoint = "enabled"
  }
  credit_specification {
    cpu_credits = "standard" # avoid surprise unlimited-burst charges on t4g
  }
  tags = { Name = "${local.name}-fck-nat" }
}
