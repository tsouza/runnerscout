# AWS qualification network (OpenTofu)

Provisions the minimal, isolated AWS network that a real-cloud AWS
qualification run needs: one VPC, a public/private subnet pair, a NAT
Gateway, and one security group (no inbound, all outbound) attached to the
private subnet.

`.github/workflows/qualify.yml`'s `provider: aws` matrix branch runs this
module directly (`tofu apply` at the start of its job, `tofu destroy` at
the end, using a dedicated `AWS_NETWORK_PROVISIONER_ROLE_ARN` identity) -
an operator dispatching that workflow never touches this module or its
outputs by hand. `tools/e2e/bring-up.sh` (a separate, local-runbook E2E
harness - see `docs/e2e-qualification.md`) still expects these same
identifiers as pre-existing, pinned, operator-supplied input, since that
harness was not changed by this module's introduction - an operator
preparing to run it should `tofu apply` this module by hand first (see
"Running it by hand" below) and copy its outputs into
`tools/e2e/env.example`.

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

## Running it by hand (for tools/e2e, not for qualify.yml)

`qualify.yml` never needs this - it runs `tofu apply`/`tofu destroy`
itself, per dispatch. Run this by hand only to prepare
`tools/e2e/bring-up.sh`'s own pinned inputs:

```sh
cd tools/tofu/qualify-network/aws
tofu init
tofu plan -var region=us-east-1
tofu apply -var region=us-east-1
```

(`terraform` works identically in place of `tofu` - this module is plain
HCL with no OpenTofu-specific syntax.)

`region` has no default and must be passed explicitly - it should match
whatever region you intend to pin `tools/e2e/env.example`'s
`E2E_AWS_REGION` to. See `variables.tf` for every other input and its
default.

After `apply`, copy the outputs directly into the equivalent `E2E_AWS_*`
variables in `tools/e2e/env.example`:

| Output | Goes into |
| --- | --- |
| `vpc_id` | `E2E_AWS_VPC_ID` |
| `subnet_id` | `E2E_AWS_SUBNET_ID` |
| `security_group_id` | `E2E_AWS_SECURITY_GROUP_ID` |
| `availability_zone` | not a separate input - `tools/e2e` already derives an instance's AZ from its pinned subnet - but useful context when picking an AMI/instance type known to be available there. |

Remember to `tofu destroy -var region=...` when you're done with the E2E
harness for now - unlike `qualify.yml`'s own automatic apply/destroy, a
by-hand `apply` here has no automatic teardown.

## CI credentials, using OIDC

`qualify.yml` authenticates to run this module via AWS OIDC role
assumption under `AWS_NETWORK_PROVISIONER_ROLE_ARN` - a repository secret,
deliberately **separate** from `AWS_QUALIFICATION_ROLE_ARN` (which
`qualify.yml` also uses, for the actual VM lifecycle, and which has no
permission to create or delete VPC/subnet/NAT-Gateway/security-group
resources by design). **Never** authenticate this module with local static
AWS access keys, in CI or by hand.

This module intentionally has no IAM role, policy, or OIDC provider
resource of its own - provisioning the identity it runs under is a
separate, human-driven bootstrap step, out of scope here. See
`docs/qualification-real-cloud.background.md` for what that identity's
minimum required permissions are.

## Ephemeral: apply before a run, destroy right after

Unlike a truly free network (nothing here costs money by itself except the
NAT Gateway), this module is meant to be **applied immediately before** a
qualification run and **destroyed immediately after** - not applied once
and left running indefinitely. The NAT Gateway bills hourly (~$0.045/hr
plus data processing) whether or not anything is using it, so leaving it
up permanently would turn a bounded, cheap qualification run into an
unbounded, ongoing monthly cost. Provisioned and torn down around a single
run instead, its real cost is a few cents, not tens of dollars a month.
`qualify.yml`'s own `provider: aws` matrix branch does exactly this
automatically, in the same job, using this module's own local OpenTofu
state - see that workflow for the actual apply/destroy step sequence.

## State

This module uses local OpenTofu/Terraform state (a `terraform.tfstate` file
next to these `.tf` files, gitignored) - a remote state backend was
deliberately not built for it. `qualify.yml` never needs to move this state
anywhere: its `apply` and `destroy` steps run in the same job, on the same
runner filesystem, so the state file simply stays on disk between them. If
you run `apply`/`destroy` by hand (see "Running it by hand" above) from
different machines for the same network, you'd need to carry the state file
between them yourself, or `destroy` will find nothing to tear down and the
resources will be orphaned (billing until removed by hand) - but the normal
by-hand flow (apply, use it, destroy, all from one machine) needs no such
care.
