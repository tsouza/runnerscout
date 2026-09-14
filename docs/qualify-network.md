# Qualification network provisioning: AWS, Azure and GCP

Part of [issue #89](https://github.com/tsouza/runnerscout/issues/89).
`.github/workflows/qualify.yml` and the E2E harness (`tools/e2e/`, issue
#80) both treat a per-provider network (AWS VPC/subnet/security group;
Azure resource group/VNet/subnet/NSG; GCP VPC network/subnetwork) as
pre-existing, pinned, operator-supplied input - neither one ever creates
that network itself. This is how that network actually gets created and
destroyed: OpenTofu modules under `tools/tofu/qualify-network/{aws,azure,gcp}/`,
driven by the single `.github/workflows/qualify-network.yml` workflow. See
[qualify-network.background.md](qualify-network.background.md) for why this
exists as a CI workflow using OpenTofu rather than local cloud CLI
invocations, and why apply/destroy uses a separate identity from
`qualify.yml`'s own qualification identities.

## What each module creates

| Provider | Module | Creates |
| --- | --- | --- |
| AWS | `tools/tofu/qualify-network/aws/` | VPC, public+private subnet pair, NAT Gateway, security group (no inbound, all outbound) |
| Azure | `tools/tofu/qualify-network/azure/` | Dedicated resource group, VNet, subnet, NSG (deny all inbound) |
| GCP | `tools/tofu/qualify-network/gcp/` | Custom-mode VPC network, subnetwork, firewall rules (443-only egress, implicit deny-all ingress) |

Each module's own `README.md` documents its exact resources, variables and
outputs. GCP's module does not create Cloud NAT - an instance launched into
its subnetwork has no external IP and therefore no real internet route yet;
this is a named, tracked gap (see that module's `main.tf` and `README.md`),
not something assumed to work.

## Provisioning identities

`qualify-network.yml` authenticates with a **separate** identity per
provider from the one `qualify.yml` uses - never
`AWS_QUALIFICATION_ROLE_ARN` / `AZURE_QUALIFICATION_CLIENT_ID`+
`AZURE_QUALIFICATION_TENANT_ID` / `GCP_QUALIFICATION_*`. See
[qualify-network.background.md](qualify-network.background.md) for why.

| Provider | Credential | Repository setting |
| --- | --- | --- |
| AWS | OIDC role assumption | `AWS_NETWORK_PROVISIONER_ROLE_ARN` (secret) |
| Azure | Workload Identity Federation | `AZURE_NETWORK_PROVISIONER_CLIENT_ID`, `AZURE_NETWORK_PROVISIONER_TENANT_ID`, `AZURE_NETWORK_PROVISIONER_SUBSCRIPTION_ID` (variables) |
| GCP | Workload Identity Federation | `GCP_NETWORK_PROVISIONER_PROJECT_ID`, `GCP_NETWORK_PROVISIONER_SERVICE_ACCOUNT`, `GCP_NETWORK_PROVISIONER_WORKLOAD_IDENTITY_PROVIDER` (variables) |

Each identity's role/service account needs create+delete permissions on
exactly the resource types its module creates (VPC/subnet/security-group
for AWS; resource group/VNet/subnet/NSG for Azure; VPC network/subnetwork/
firewall for GCP) - nothing broader. None of these repository
secrets/variables exist until an operator provisions the underlying cloud
identity and sets them; until then `qualify-network.yml` fails cleanly at
its own credential-presence check, before any cloud interaction.

## Running it

`workflow_dispatch` only, on `qualify-network.yml`:

| Input | Meaning |
| --- | --- |
| `provider` | `all` (default, fans out to all three as independent matrix jobs) or one of `aws`/`azure`/`gcp`. |
| `action` | `apply` (create the network) or `destroy` (tear it down). No separate confirmation input - dispatching this workflow with these inputs is itself the confirmation. |
| `apply_run_id` | Required only when `action: destroy` - the run ID of the prior `apply` dispatch whose OpenTofu state to restore before destroying (from that run's own URL). |
| `aws_region` | Required only when `provider` is `aws` or `all`. |
| `azure_location` | Required only when `provider` is `azure` or `all`. |
| `gcp_project` | Required only when `provider` is `gcp` or `all`. Must equal `vars.GCP_NETWORK_PROVISIONER_PROJECT_ID`. |
| `gcp_region` | Required only when `provider` is `gcp` or `all`. |

An `apply` run's outputs (the exact values to copy into `qualify.yml`'s
`workflow_dispatch` inputs or `tools/e2e/env*.example`) are printed in that
run's job summary, along with the `apply_run_id` value to use for a later
`destroy` of that same network.

## Apply before a run, destroy right after

This network is meant to be **applied immediately before** a
`qualify.yml`/E2E dispatch and **destroyed immediately after** - not applied
once and left running indefinitely. AWS's NAT Gateway bills hourly whether
or not anything is using it; provisioned and torn down around a single run
instead, its real cost is a few cents, not an ongoing monthly bill. Azure
and GCP's own resources don't bill hourly, but follow the same lifecycle
for consistency, so `qualify-network.yml`'s apply/destroy logic is identical
across all three providers.

## State

Each `apply` uploads the resulting OpenTofu state as a run-scoped GitHub
Actions artifact (`qualify-network-state-<provider>`, 90-day retention);
`destroy` downloads that same artifact from the run ID it's given
(`apply_run_id`) before running. No remote Terraform/OpenTofu backend is
used. Losing that artifact (past its retention window, or deleted) before
running `destroy` means the network must be torn down by hand through the
cloud console/CLI instead.
