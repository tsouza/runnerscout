# Minimal, isolated AWS network for this repo's real-cloud AWS
# qualification/E2E lanes (.github/workflows/qualify.yml's `provider: aws`
# matrix branch, and tools/e2e/bring-up.sh). Both already treat the VPC /
# subnet / security group as pre-existing, pinned, operator-supplied input -
# this module is what actually provisions that input, once.
#
# What runs here, and why this is NOT a simple "public subnet + Internet
# Gateway" network, unlike the sibling Azure module in this same directory:
#
#   - qualify.yml's AWS branch drives internal/provider/aws_realcloud_test.go,
#     which creates one real Spot instance through the production adapter,
#     confirms it reaches EC2's real "running" state, observes its Spot
#     price, and deletes it. Its own header comment is explicit that it
#     feeds a synthetic, non-functional placeholder in place of a real
#     GitHub JIT token, so the AMI's cloud-init runner-registration step is
#     *expected* to fail harmlessly inside the guest - this lane never
#     depends on real inbound OR outbound connectivity succeeding; it only
#     qualifies the EC2 control-plane lifecycle (create/observe/price/
#     delete).
#   - tools/e2e/bring-up.sh's harness goes further and expects a real
#     self-hosted GitHub Actions runner to register and pick up a job. A
#     self-hosted runner is pull-only by design: it makes outbound HTTPS
#     (443) connections to GitHub to register itself and long-poll for
#     work, and never accepts any inbound connection from GitHub or
#     anywhere else. Nothing in internal/provider/aws.go's createAWS (the
#     RunInstances call) or aws_inventory.go opens, expects, or checks any
#     inbound port either.
#
#   - Unlike Azure (see that sibling module's own main.tf), a "give the
#     subnet map_public_ip_on_launch = true and rely on an Internet
#     Gateway" design does NOT work here: internal/provider/aws.go's
#     createAWS hard-codes `AssociatePublicIpAddress: aws.Bool(false)` on
#     every instance's network interface (unconditionally, for every
#     allocation this codebase ever creates), which overrides whatever the
#     subnet's own map_public_ip_on_launch default is. No instance this
#     codebase's AWS adapter creates will ever receive a public IPv4
#     address. An Internet Gateway alone cannot route return traffic to an
#     instance that has no public IP - IGW routing is a 1:1 NAT keyed on
#     that public IP, and AWS (unlike Azure, which grants VMs implicit
#     "default outbound access" even with no public IP) has no equivalent
#     fallback. So a NAT Gateway - translating the private subnet's
#     outbound traffic through one Elastic IP it owns - is the only way for
#     an instance launched here to reach GitHub at all, and this module
#     accepts the resulting hourly NAT Gateway cost as necessary, not
#     optional, for tools/e2e/bring-up.sh's real registration/job-polling
#     use of this same network to actually work. (qualify.yml's own AWS
#     lifecycle test does not itself require this - see above - but the
#     network is shared with tools/e2e, which does.) That hourly cost is
#     bounded to a few cents per real-cloud run, not an ongoing monthly
#     bill: this module is meant to be applied immediately before a run and
#     destroyed immediately after (see README.md's "Ephemeral" section),
#     never left standing indefinitely.
#
# So this network needs zero inbound access and only outbound connectivity
# (443 to GitHub, plus whatever the guest OS itself needs - package repos,
# time sync, DNS - which is why the security group below is not narrowed to
# port 443 only; see that resource's own comment).

provider "aws" {
  region = var.region
}

locals {
  name_prefix = var.name_prefix

  common_tags = merge(var.tags, {
    "runnerscout-purpose" = "qualification-network"
  })
}

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "qualify" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-vpc"
  })
}

resource "aws_internet_gateway" "qualify" {
  vpc_id = aws_vpc.qualify.id

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-igw"
  })
}

# Public subnet: exists only to host the NAT Gateway's Elastic IP-backed
# ENI. No qualification/E2E instance is ever launched into this subnet -
# they are launched into the private subnet below, and never receive a
# public IP regardless (see this file's header comment), so there would be
# no benefit to launching them here instead.
resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.qualify.id
  cidr_block              = var.public_subnet_cidr
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = true

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-public-subnet"
  })
}

# Private subnet: where qualify.yml / tools/e2e actually launch the
# qualification instance (this module's `subnet_id` output). Routes
# outbound traffic through the NAT Gateway in the public subnet above -
# never directly through the Internet Gateway - since these instances never
# hold a public IP.
resource "aws_subnet" "private" {
  vpc_id                  = aws_vpc.qualify.id
  cidr_block              = var.private_subnet_cidr
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = false

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-private-subnet"
  })
}

resource "aws_eip" "nat" {
  domain = "vpc"

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-nat-eip"
  })

  depends_on = [aws_internet_gateway.qualify]
}

resource "aws_nat_gateway" "qualify" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public.id

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-nat"
  })

  depends_on = [aws_internet_gateway.qualify]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.qualify.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.qualify.id
  }

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-public-rt"
  })
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.qualify.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.qualify.id
  }

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-private-rt"
  })
}

resource "aws_route_table_association" "private" {
  subnet_id      = aws_subnet.private.id
  route_table_id = aws_route_table.private.id
}

# No inline ingress/egress blocks: the AWS provider's aws_security_group
# resource revokes the default "allow all outbound" rule AWS's own API
# would otherwise create for a new VPC security group, unless egress is
# declared - so the explicit aws_vpc_security_group_egress_rule below is
# what actually grants outbound access, not an assumed default. Zero
# ingress rules of any kind are declared, so this security group denies all
# inbound traffic - exactly what a self-hosted GitHub Actions runner needs:
# it only ever makes outbound connections (443 to GitHub; plus whatever
# else the guest OS needs, which is why outbound is not narrowed to 443
# only - see this file's header comment).
resource "aws_security_group" "qualify" {
  name        = "${local.name_prefix}-sg"
  description = "runnerscout qualification network: no inbound, all outbound. Self-hosted GitHub Actions runners only ever make outbound connections."
  vpc_id      = aws_vpc.qualify.id

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-sg"
  })
}

resource "aws_vpc_security_group_egress_rule" "allow_all_outbound" {
  security_group_id = aws_security_group.qualify.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = "Allow all outbound IPv4 traffic. No inbound rule exists on this security group."

  tags = merge(local.common_tags, {
    Name = "${local.name_prefix}-allow-all-outbound"
  })
}
