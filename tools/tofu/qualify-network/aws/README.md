# AWS qualification network (OpenTofu)

Provisions the minimal, isolated AWS network that
`.github/workflows/qualify.yml`'s `provider: aws` matrix branch and
`tools/e2e/bring-up.sh` both expect as pre-existing, pinned,
operator-supplied input: one VPC, a public/private subnet pair, a NAT
Gateway, and one security group (no inbound, all outbound) attached to the
private subnet.

## What this creates

- `aws_vpc` - a small, isolated VPC (`10.90.0.0/16` by default).
- `aws_internet_gateway` - attached to the VPC.
- `aws_subnet` (public, `10.90.0.0/24` by default) - hosts only the NAT
  Gateway. No qualification instance is ever launched here.
- `aws_subnet` (private, `10.90.1.0/24` by default) - where qualify.yml /
  tools/e2e actually launch the qualification instance (`subnet_id`
  output). Routes outbound traffic through the NAT Gateway.
- `aws_eip` + `aws_nat_gateway` - gives the private subnet's instances real
  outbound internet reachability. See "Why a NAT Gateway" below for why
  this could not be a cheaper Internet-Gateway-only design.
- Route tables and associations wiring the above together.
- `aws_security_group` - attached to the private subnet, with a single
  `aws_vpc_security_group_egress_rule` allowing all outbound traffic and
  zero ingress rules (no inbound access of any kind).

Every resource is tagged `runnerscout-purpose = "qualification-network"`
(plus a resource-specific `Name`) so it is identifiable as this repo's own
qualification infrastructure, never mistaken for production infra.

## Why a NAT Gateway, unlike the sibling Azure module

The sibling `../azure` module needs no NAT-equivalent, because an Azure VM
with no public IP still gets the platform's implicit "default outbound
access." AWS has no equivalent: `internal/provider/aws.go`'s `createAWS`
hard-codes `AssociatePublicIpAddress: aws.Bool(false)` unconditionally, so
no instance this codebase's AWS adapter ever creates gets a public IPv4
address, regardless of the subnet's own `map_public_ip_on_launch` setting.
An Internet Gateway alone cannot route return traffic to an instance with
no public IP, so a NAT Gateway - and the resulting hourly NAT Gateway cost
- is treated here as necessary, not optional, for `tools/e2e/bring-up.sh`'s
real GitHub Actions runner registration/job-polling use of this network to
actually work. See `main.tf`'s header comment for the full reasoning,
including why `qualify.yml`'s own AWS lifecycle test does not itself
require this (it feeds a synthetic, non-functional GitHub token and never
depends on real connectivity succeeding).

## Running it

```sh
cd tools/tofu/qualify-network/aws
tofu init
tofu plan -var region=us-east-1
tofu apply -var region=us-east-1
```

(`terraform` works identically in place of `tofu` - this module is plain
HCL with no OpenTofu-specific syntax.)

`region` has no default and must be passed explicitly - it should match
whatever region you intend to pin `qualify.yml`'s `aws_region` /
`tools/e2e/env.example`'s `E2E_AWS_REGION` to. See `variables.tf` for every
other input and its default.

After `apply`, copy the outputs directly into `qualify.yml`'s
`workflow_dispatch` inputs (and the equivalent `E2E_AWS_*` variables in
`tools/e2e/env.example`):

| Output | Goes into |
| --- | --- |
| `vpc_id` | `aws_vpc_id` / `E2E_AWS_VPC_ID` |
| `subnet_id` | `aws_subnet_id` / `E2E_AWS_SUBNET_ID` |
| `security_group_id` | `aws_security_group_id` / `E2E_AWS_SECURITY_GROUP_ID` |
| `availability_zone` | not a separate input - both qualify.yml and tools/e2e already derive an instance's AZ from its pinned subnet - but useful context when picking an AMI/instance type known to be available there. |

## Intended to run via CI, using OIDC credentials

This module is meant to be run from a future CI workflow (not built here -
that is separate, out-of-scope work) authenticated via AWS OIDC role
assumption, the same credential model `docs/qualification-real-cloud.md`'s
AWS section already establishes for the qualification workflow itself.
**Never** authenticate this module with local static AWS access keys. Until
that workflow exists, run it locally with whatever OIDC-derived or
short-lived credentials you already use.

This module intentionally has no IAM role, policy, or OIDC provider
resource of its own - provisioning the identity this module runs under is a
separate, human-driven bootstrap step, out of scope here.

## One-time bootstrap, long-lived result

Run this once. The VPC/subnets/NAT Gateway/security group it creates are
meant to live indefinitely and be reused by every future qualification/E2E
run - this module has no destroy-on-idle, TTL, or other ephemeral lifecycle
logic, and none should be added. Tearing it down is a deliberate, manual
`tofu destroy` if this qualification network is ever retired.

## State

This module uses local OpenTofu/Terraform state (a `terraform.tfstate` file
next to these `.tf` files, gitignored). It is run rarely, by hand or from a
single CI identity, so a remote state backend was deliberately not built
for it. If you run `apply` more than once from different machines, make
sure you're sharing the same state file, or you will create a second,
duplicate network instead of updating the first.
