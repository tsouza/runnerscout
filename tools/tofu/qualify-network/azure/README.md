# Azure qualification network (OpenTofu)

Provisions the minimal, isolated Azure network that
`.github/workflows/qualify.yml`'s `provider: azure` matrix branch and
`tools/e2e/bring-up-azure.sh` both expect as pre-existing, pinned,
operator-supplied input: one dedicated resource group, one VNet, one subnet,
and one Network Security Group (deny-all-inbound) associated with that
subnet.

## What this creates

- `azurerm_resource_group` - a dedicated resource group (`runnerscout-qualify-network`
  by default), never a production one.
- `azurerm_virtual_network` - a small, isolated VNet (`10.91.0.0/16` by
  default).
- `azurerm_subnet` - a single subnet within it (`10.91.1.0/24` by default).
- `azurerm_network_security_group` - associated with that subnet, with an
  explicit `DenyAllInbound` rule and no custom outbound rule (see `main.tf`
  for why).

Every resource is tagged `purpose = "qualification-network"` so it is
identifiable as this repo's own qualification infrastructure, never
mistaken for production infra.

## Running it

```sh
cd tools/tofu/qualify-network/azure
tofu init
tofu plan -var location=eastus
tofu apply -var location=eastus
```

(`terraform` works identically in place of `tofu` - this module is plain
HCL with no OpenTofu-specific syntax.)

`location` has no default and must be passed explicitly - it should match
whatever region you intend to pin `qualify.yml`'s `azure_region` /
`tools/e2e/env.example`'s `E2E_AZURE_REGION` to. See `variables.tf` for every
other input and its default.

After `apply`, copy the outputs directly into `qualify.yml`'s
`workflow_dispatch` inputs (and the equivalent `E2E_AZURE_*` variables in
`tools/e2e/env.example`):

| Output | Goes into |
| --- | --- |
| `resource_group_name` | `azure_resource_group` / `E2E_AZURE_RESOURCE_GROUP` |
| `subnet_id` | `azure_subnet_id` / `E2E_AZURE_SUBNET_ID` |
| `nsg_id` | `azure_nsg_id` / `E2E_AZURE_NSG_ID` |
| `vnet_id` | `E2E_AZURE_VNET_ID` (qualify.yml derives this itself from `subnet_id`, so it is not a separate workflow input there) |

## Intended to run via CI, using Workload Identity Federation

This module is meant to be run from a future CI workflow (not built here -
that is separate, out-of-scope work) authenticated via Azure Workload
Identity Federation (OIDC), the same credential model
`docs/qualification-real-cloud.md`'s Azure section already establishes for
the qualification workflow itself. **Never** authenticate this module with a
client secret. Until that workflow exists, run it locally with whatever
`az login` / `ARM_*` environment credentials you already use, or with a
short-lived OIDC-derived credential you generate by hand.

This module intentionally has no Azure AD app registration, federated
credential, or role assignment of its own - provisioning the identity this
module runs under is a separate, human-driven bootstrap step, out of scope
here.

## One-time bootstrap, long-lived result

Run this once. The resource group/VNet/subnet/NSG it creates are meant to
live indefinitely and be reused by every future qualification/E2E run - this
module has no destroy-on-idle, TTL, or other ephemeral lifecycle logic, and
none should be added. Tearing it down is a deliberate, manual `tofu destroy`
if this qualification network is ever retired.

## State

This module uses local OpenTofu/Terraform state (a `terraform.tfstate` file
next to these `.tf` files, gitignored). It is run rarely, by hand or from a
single CI identity, so a remote state backend was deliberately not built for
it - see the PR this module was introduced in for the reasoning. If you run
`apply` more than once from different machines, make sure you're sharing the
same state file, or you will create a second, duplicate network instead of
updating the first.
