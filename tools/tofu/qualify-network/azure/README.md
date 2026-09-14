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

## Apply before a run, destroy right after

None of this module's resources (resource group, VNet, subnet, NSG) bill by
the hour, so applying this once and leaving it standing indefinitely would
not itself cost anything. It is nonetheless meant to follow the same
apply-before/destroy-after-each-run lifecycle as the sibling `../aws` module
(where that lifecycle is cost-driven - `../aws/README.md`'s "Ephemeral"
section - since AWS's NAT Gateway bills hourly) - for consistency across all
three providers, and so a single future CI workflow can wrap `tofu apply` →
dispatch `qualify.yml`/the E2E harness with this module's outputs → `tofu
destroy` identically for every provider, rather than needing
provider-specific lifecycle handling. `qualify.yml`/`bring-up-azure.sh`
still treat `azure_resource_group`/`azure_subnet_id`/`azure_nsg_id` as
pinned, pre-existing identifiers for the duration of one dispatch - "pinned"
here means "fixed for that run," not "created once and never rebuilt."
`tofu destroy -var location=...` (using the same `location` value `apply`
was given) tears down every resource this module created.

## State

This module uses local OpenTofu/Terraform state (a `terraform.tfstate` file
next to these `.tf` files, gitignored). Because the network is meant to be
created and destroyed within a single run (see above) rather than persisted
across separate invocations, the state file only needs to survive for the
lifetime of that one apply-then-destroy cycle - a remote state backend was
deliberately not built for it. If you split `apply` and `destroy` across
different machines/CI jobs, make sure the state file `apply` produced is
passed through to whichever job runs `destroy` (e.g. as a build artifact),
or `destroy` will find nothing to tear down and the resources will be
orphaned (billing/lingering until removed by hand). If instead you run
`apply` more than once from different machines without an intervening
`destroy`, make sure you're sharing the same state file, or you will create
a second, duplicate network instead of
updating the first.
