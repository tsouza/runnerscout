# Azure qualification network (OpenTofu)

Provisions the minimal, isolated Azure network that a real-cloud Azure
qualification run needs: one dedicated resource group, one VNet, one
subnet, and one Network Security Group (deny-all-inbound) associated with
that subnet.

`.github/workflows/qualify.yml`'s `provider: azure` matrix branch runs this
module directly (`tofu apply` at the start of its job, `tofu destroy` at
the end, using a dedicated `AZURE_NETWORK_PROVISIONER_*` identity) - an
operator dispatching that workflow never touches this module or its
outputs by hand. `tools/e2e/bring-up-azure.sh` (a separate, local-runbook
E2E harness - see `docs/e2e-qualification.md`) still expects these same
identifiers as pre-existing, pinned, operator-supplied input, since that
harness was not changed by this module's introduction - an operator
preparing to run it should `tofu apply` this module by hand first (see
"Running it by hand" below) and copy its outputs into
`tools/e2e/env.example`.

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

## Running it by hand (for tools/e2e, not for qualify.yml)

`qualify.yml` never needs this - it runs `tofu apply`/`tofu destroy`
itself, per dispatch. Run this by hand only to prepare
`tools/e2e/bring-up-azure.sh`'s own pinned inputs:

```sh
cd tools/tofu/qualify-network/azure
tofu init
tofu plan -var location=eastus
tofu apply -var location=eastus
```

(`terraform` works identically in place of `tofu` - this module is plain
HCL with no OpenTofu-specific syntax.)

`location` has no default and must be passed explicitly - it should match
whatever region you intend to pin `tools/e2e/env.example`'s
`E2E_AZURE_REGION` to. See `variables.tf` for every other input and its
default.

After `apply`, copy the outputs directly into the equivalent `E2E_AZURE_*`
variables in `tools/e2e/env.example`:

| Output | Goes into |
| --- | --- |
| `resource_group_name` | `E2E_AZURE_RESOURCE_GROUP` |
| `subnet_id` | `E2E_AZURE_SUBNET_ID` |
| `nsg_id` | `E2E_AZURE_NSG_ID` |
| `vnet_id` | `E2E_AZURE_VNET_ID` |

Remember to `tofu destroy -var location=...` when you're done with the E2E
harness for now - unlike `qualify.yml`'s own automatic apply/destroy, a
by-hand `apply` here has no automatic teardown.

## CI credentials, using Workload Identity Federation

`qualify.yml` authenticates to run this module via Azure Workload Identity
Federation (OIDC) under `AZURE_NETWORK_PROVISIONER_CLIENT_ID`/
`AZURE_NETWORK_PROVISIONER_TENANT_ID`/`AZURE_NETWORK_PROVISIONER_SUBSCRIPTION_ID`
- repository variables, deliberately **separate** from the
`AZURE_QUALIFICATION_*` identity `qualify.yml` also uses, for the actual VM
lifecycle, and which has no permission to create or delete resource-group/
VNet/subnet/NSG resources by design. **Never** authenticate this module
with a client secret, in CI or by hand.

This module intentionally has no Azure AD app registration, federated
credential, or role assignment of its own - provisioning the identity it
runs under is a separate, human-driven bootstrap step, out of scope here.
See `docs/qualification-real-cloud.background.md` for what that identity's
minimum required permissions are.

## Apply before a run, destroy right after

None of this module's resources (resource group, VNet, subnet, NSG) bill by
the hour, so applying this once and leaving it standing indefinitely would
not itself cost anything. It is nonetheless meant to follow the same
apply-before/destroy-after-each-run lifecycle as the sibling `../aws`
module (where that lifecycle is cost-driven - `../aws/README.md`'s
"Ephemeral" section - since AWS's NAT Gateway bills hourly) - for
consistency across all three providers. `qualify.yml`'s own `provider:
azure` matrix branch does exactly this automatically, in the same job,
using this module's own local OpenTofu state - see that workflow for the
actual apply/destroy step sequence.

## State

This module uses local OpenTofu/Terraform state (a `terraform.tfstate` file
next to these `.tf` files, gitignored) - a remote state backend was
deliberately not built for it. `qualify.yml` never needs to move this state
anywhere: its `apply` and `destroy` steps run in the same job, on the same
runner filesystem, so the state file simply stays on disk between them. If
you run `apply`/`destroy` by hand (see "Running it by hand" above) from
different machines for the same network, you'd need to carry the state
file between them yourself, or `destroy` will find nothing to tear down
and the resources will be orphaned (billing/lingering until removed by
hand) - but the normal by-hand flow (apply, use it, destroy, all from one
machine) needs no such care. If instead you run `apply` more than once
from different machines without an intervening `destroy`, make sure you're
sharing the same state file, or you will create a second, duplicate
network instead of updating the first.
