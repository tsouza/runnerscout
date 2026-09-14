# Real-cloud qualification: AWS, Azure and GCP

Part of [issue #3](https://github.com/tsouza/runnerscout/issues/3)'s remaining
"Final real-cloud qualification" scope, plus [issue #2](https://github.com/tsouza/runnerscout/issues/2)'s
related "real provider pricing requires later qualification" ask. This covers
the AWS, Azure and GCP provider adapters — see each provider's own "Known
gaps" section below for what remains out of scope even after a passing run.

All three providers are matrix branches of the single
`.github/workflows/qualify.yml` workflow (a `provider` input selects `all`
or one of `aws`/`azure`/`gcp`), and share one safety philosophy -
`workflow_dispatch`-only, an exact-phrase spend confirmation checked before
any cloud credential is configured, a hard-coded runtime ceiling no input
can raise, pinned operator-supplied network identifiers cross-validated
before any create call, and a two-layer teardown guarantee (the Go test's
own `t.Cleanup` plus an `if: always()` step that independently re-derives
everything and re-queries the cloud API) - adapted to each cloud's real API
shapes rather than copied blindly. See
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for the reasoning behind each design choice, including where and why the
three providers' steps genuinely differ.

## AWS

`.github/workflows/qualify.yml`'s `provider: aws` matrix branch drives the
production `internal/provider` AWS adapter (`aws.go`, `aws_sdk.go`, `aws_inventory.go`,
`internal/prices/aws.go`) against real AWS infrastructure, through
`internal/provider/aws_realcloud_test.go` (built only with `-tags
realcloud`, never part of any other build or test path). One run:

1. Creates one real, billed EC2 Spot Instance via the adapter's own
   `CreateWithResources`.
2. Independently confirms it reaches EC2's real "running" state, using a
   second EC2 client built through the standard AWS SDK default credential
   chain — never the adapter's own session — so this confirmation never
   depends on the exact code path under qualification also certifying its
   own success.
3. Confirms the adapter's `Observe` reports the running instance as not
   interrupted.
4. Observes a real Spot price for the same pinned instance type/AZ via
   `internal/prices.AWSSpotClient` (through `Command.AWS.SpotPrices()`).
5. Deletes the instance via the adapter's own `Delete`, then independently
   re-queries the AWS API (the same second EC2 client) for leftover
   volumes/network interfaces owned by this run's allocation tags — never
   just trusting `Delete`'s or `Observe`'s own report of success.
6. Uploads everything observed (creation receipt, running-state
   confirmation, observation results, the price quote, the independent
   post-delete inventory query) as a workflow artifact, at
   `evidence/qualify-aws-<run>-<attempt>/manifest.json`.

### AWS trigger and required inputs

`workflow_dispatch` only — no schedule, no push, no pull_request, on
`qualify.yml` with `provider: aws` (or `all`). `max_runtime_minutes` is
required at the trigger level and shared across every selected provider;
every other input below is `required: false` at the trigger level (GitHub
Actions cannot make an input conditionally required on another input's
value) but is enforced as required for AWS by the workflow's own first
AWS-specific step, before any AWS credential is configured:

| Input | Meaning |
| --- | --- |
| `aws_region` | Region to qualify against - also where the qualification network (see below) is created and destroyed. |
| `aws_ami_id` | Pinned AMI. Must be `available`, EBS-backed, and match `aws_architecture`. |
| `aws_instance_type` | Keep this cheap — it directly bounds real spend alongside `max_runtime_minutes`. |
| `aws_architecture` | `amd64` or `arm64`; must match `aws_ami_id`'s real architecture. |
| `max_runtime_minutes` | Plain integer, 1–20. Bounds the create/observe/price phase. |

The AMI/instance type/architecture are auto-discovered from nowhere -
operator-supplied, per issue #3's "pinned images/networks" requirement. The
**network** is a different matter (issue #89): `aws_vpc_id`/`aws_subnet_id`/
`aws_security_group_id` are not workflow inputs at all - this workflow
provisions its own isolated VPC/subnet/security group via OpenTofu at the
start of the job and destroys it at the end (see "Network provisioning"
below), rather than requiring an operator to pin a pre-existing one. The
instance's Availability Zone is not a separate input either — it is read
directly from that freshly-created subnet's own AZ output.

### AWS hard bounds

- `max_runtime_minutes` is checked against a hard-coded workflow ceiling of
  **20** before anything else runs (before Go setup, before AWS credentials
  are even configured). No input can raise this ceiling.
- The job's own `timeout-minutes: 45` is a second, independent ceiling that
  does not derive from the input at all.
- Deletion runs on its own fixed 5-minute budget, independent of
  `max_runtime_minutes` — a run that spent its whole create/observe/price
  budget just confirming the VM works still gets a full, unhurried
  teardown attempt.
- `concurrency: { group: qualify-aws, cancel-in-progress: false }` — only
  one real-cloud run at a time; a second dispatch queues rather than races
  or cancels an in-flight one.

### AWS guaranteed cleanup

Two independent layers, either one alone sufficient for the common case:

1. The Go test's own `t.Cleanup`, registered immediately once a real
   instance exists, running on its own fresh 5-minute context regardless of
   how much of the create/observe/price budget was already spent.
2. The workflow's own final step, gated `if: always()` — the authoritative
   backstop, since it runs even if the job was killed by its own timeout or
   cancelled before the Go process's `t.Cleanup` could run. It re-derives
   everything it needs (region, allocation id, owner tag) directly from
   job inputs/env, never from a prior step's output, so it still works when
   every earlier step failed. It independently re-queries for any instance/
   volume/network-interface still tagged to this run and force-cleans
   anything found, then fails the job loudly (that condition is a real bug,
   never a normal outcome).

### AWS credentials

OIDC role assumption is preferred, matching this repo's existing GHCR/
cosign OIDC pattern (`release-build.yml`). A static-key fallback is also
supported:

- **OIDC** (preferred): set the `AWS_QUALIFICATION_ROLE_ARN` repository
  secret to an IAM role ARN (a role ARN is not sensitive on its own, but it
  is stored as a secret rather than a variable to match how it was actually
  provisioned in this repository). The workflow assumes it via
  `aws-actions/configure-aws-credentials`'s `role-to-assume`, using the
  job's own GitHub Actions OIDC identity (`id-token: write`, granted only
  to this job).
- **Static keys** (fallback): set the `AWS_QUALIFICATION_ACCESS_KEY_ID` and
  `AWS_QUALIFICATION_SECRET_ACCESS_KEY` repository secrets. Used only when
  `AWS_QUALIFICATION_ROLE_ARN` is unset.

Both paths converge on the same `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/
`AWS_SESSION_TOKEN` environment variables for the rest of the job — see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this made supporting both trivial instead of a deferred follow-up.

`AWS_QUALIFICATION_ROLE_ARN` is the only one of these that exists in this
repository today (confirmed via `gh secret list`); the static-key secrets do
not. Referencing the static-key secrets by name is inert until an operator
deliberately provisions them — the OIDC path above is what a dispatch
actually uses today.

#### AWS minimum IAM permissions

The assumed role or IAM user needs, scoped to the pinned region/account:
`sts:GetCallerIdentity`; `ec2:DescribeImages`, `ec2:DescribeInstances`,
`ec2:DescribeVolumes`, `ec2:DescribeNetworkInterfaces`,
`ec2:DescribeSpotPriceHistory`; `ec2:RunInstances`, `ec2:CreateTags`,
`ec2:TerminateInstances`, `ec2:DeleteVolume`, `ec2:DeleteNetworkInterface`.
No IAM or VPC-creation permissions are needed — this identity only ever
launches into and deletes from a network a *separate* identity created
(see "Network provisioning" below), never creating or modifying the
network itself.

### Network provisioning (issue #89)

Unlike the compute side above, this workflow's qualification network is
not an operator-pinned input at all: `tools/tofu/qualify-network/aws/`
(an OpenTofu module) is applied at the start of this provider's job and
destroyed at the end of the same job, creating a fresh, isolated VPC,
subnet, NAT Gateway and security group exclusively for that one run. See
that module's own `README.md` for exactly what it creates, and
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this replaced an earlier, separately-exposed provision/destroy
workflow.

This uses a **deliberately separate** identity from
`AWS_QUALIFICATION_ROLE_ARN` above: the `AWS_NETWORK_PROVISIONER_ROLE_ARN`
repository secret, an OIDC role scoped to exactly
`ec2:CreateVpc`/`DeleteVpc`, `ec2:CreateInternetGateway`/
`DeleteInternetGateway`, `ec2:CreateSubnet`/`DeleteSubnet`,
`ec2:AllocateAddress`/`ReleaseAddress`, `ec2:CreateNatGateway`/
`DeleteNatGateway`, `ec2:CreateRouteTable`/`DeleteRouteTable`+route/
association actions, `ec2:CreateSecurityGroup`/`DeleteSecurityGroup`+
security-group-rule actions, plus the matching `Describe*`/tagging actions
- nothing the qualification identity above also needs, and nothing that
identity is granted.

### AWS operator prerequisites

Before dispatching this workflow, an operator must have already:

1. Chosen a pinned, EBS-backed AMI and confirmed its real architecture.
2. Chosen a cheap instance type consistent with that architecture.
3. Set either `AWS_QUALIFICATION_ROLE_ARN` (preferred) or the static-key
   secret pair above, granting the minimum permissions listed.
4. Set `AWS_NETWORK_PROVISIONER_ROLE_ARN`, granting the separate network-
   provisioning permissions listed above.
5. Confirmed the `AWSServiceRoleForEC2Spot` service-linked role already
   exists in the target AWS account (`aws iam get-role --role-name
   AWSServiceRoleForEC2Spot`) - a one-time, account-level prerequisite
   for *any* identity to ever request a Spot Instance in that account,
   unrelated to this workflow's own IAM setup. If it doesn't exist yet, an
   operator with `iam:CreateServiceLinkedRole` (never the qualification
   identity itself - see
   [qualification-real-cloud.background.md](qualification-real-cloud.background.md))
   must run `aws iam create-service-linked-role --aws-service-name
   spot.amazonaws.com` once, directly.

### What AWS qualifies — and what it honestly does not

Qualified, for real, by a passing run:

- Real EC2 Spot creation, a real transition to "running", real Spot price
  observation, real deletion, and independently re-verified cleanup — all
  through the exact production adapter code path.

Named gaps, not covered by this piece:

- **Real GitHub Actions job execution** (issue #3's "ordinary runs-on,
  actual VM job execution"). The VM boots with a synthetic, non-functional
  placeholder in place of a real GitHub JIT registration token — the AMI's
  cloud-init runner-registration step is expected to fail harmlessly inside
  the guest. Confirming a real runner registers and executes a real
  workflow job needs a live scale set and a real ephemeral registration
  token — a separate, larger qualification piece this workflow does not
  attempt.
- **A real Spot interruption.** AWS provides no API to force one on demand.
  This workflow can only confirm the adapter correctly reports "not
  interrupted" against a real, healthy running instance; the
  `Server.SpotInstanceTermination` detection logic itself is covered
  separately, against synthesized state payloads, by the existing unit
  test suite.

## Azure

`.github/workflows/qualify.yml`'s `provider: azure` matrix branch drives the
production `internal/provider` Azure adapter (`azure.go`, `azure_sdk.go`,
`azure_inventory.go`, `azure_image.go`, `azure_binding.go`,
`internal/prices/azure.go`) against real Azure infrastructure, through
`internal/provider/azure_realcloud_test.go` (built only with `-tags
realcloud`, alongside `aws_realcloud_test.go`, never part of any other
build or test path). One run:

1. Creates one real, billed Azure Spot Virtual Machine — plus its dependent
   network interface and OS disk, provisioned together in one ARM template
   deployment — via the adapter's own `CreateWithResources`.
2. Independently confirms it reaches a real `PowerState/running` state,
   using a typed `armcompute.VirtualMachinesClient` built through Azure's
   plain `DefaultAzureCredential` chain — never the adapter's own session —
   so this confirmation never depends on the exact code path under
   qualification also certifying its own success. This typed client is
   necessary here specifically: the generic ARM resource client the
   production adapter itself uses for every other read cannot report a
   VM's real runtime power state at all, only static ARM properties.
3. Confirms the adapter's `Observe` reports the running VM as not
   interrupted.
4. Observes a real Spot price for the same pinned VM size/region via
   `internal/prices.AzureSpotClient` (through `Command.Azure.SpotPrices()`)
   — a public, unauthenticated endpoint, unlike AWS's.
5. Deletes the VM by driving the adapter's own `Observe`/`Delete` in a
   bounded loop (Azure's `Delete` tears down one dependent resource per
   call, not all three at once — see
   [qualification-real-cloud.background.md](qualification-real-cloud.background.md)),
   then independently re-queries the ARM API (the same independent client)
   for any resource still tagged to this run's allocation — never just
   trusting `Delete`'s or `Observe`'s own report of success.
6. Uploads everything observed as a workflow artifact, at
   `evidence/qualify-azure-<run>-<attempt>/manifest.json`.

### Azure trigger and required inputs

`workflow_dispatch` only — no schedule, no push, no pull_request, on
`qualify.yml` with `provider: azure` (or `all`). As with AWS, only
`max_runtime_minutes` is required at the trigger
level; every other input below is enforced as required for Azure by the
workflow's own first Azure-specific step, before any Azure credential is
configured:

| Input | Meaning |
| --- | --- |
| `azure_region` | Region to qualify against (e.g. `eastus`) - also where the qualification network (see below) is created and destroyed. |
| `azure_subscription_id` | Pinned Azure subscription GUID. |
| `azure_image_id` | Pinned managed-image resource ID (`Microsoft.Compute/images/...`). Must be an available, generalized Linux image with only an OS disk. |
| `azure_vm_size` | Keep this cheap — it directly bounds real spend alongside `max_runtime_minutes`. |
| `azure_availability_zone` | `1`, `2` or `3`. Must be supported by both `azure_region` and `azure_vm_size`. |
| `azure_disk_controller_type` | Optional, defaults to `unset`. Leave `unset` for Azure's own default controller-type inference (correct for most VM sizes). Set to `NVMe` or `SCSI` only if `azure_vm_size` requires a specific controller type - see "Choosing a compatible `azure_vm_size`" below. |
| `max_runtime_minutes` | Plain integer, 1–20. Bounds the create/observe/price phase. |

The image/VM size/subscription are auto-discovered from nowhere -
operator-supplied, per issue #3's "pinned images/networks" requirement.
Unlike AWS, Azure has no per-resource "architecture" concept to validate —
the managed-image API this adapter requires has no architecture field at
all (see `azure_image.go`) — and the Availability Zone is not derivable
from any other input, so it is its own required input here. The
**network** is a different matter (issue #89): `azure_resource_group`/
`azure_subnet_id`/`azure_nsg_id` are not workflow inputs at all - this
workflow provisions its own isolated resource group/VNet/subnet/NSG via
OpenTofu at the start of the job and destroys it at the end (see "Network
provisioning" below).

### Choosing a compatible `azure_vm_size`

A classic managed image (`Microsoft.Compute/images/...`, what
`azure_image_id` must point to - see above) carries no disk-controller-type
metadata of its own, unlike a Compute Gallery image. Most Azure VM size
families let Azure infer a working controller type from the image anyway,
but some newer families (e.g. the `Dv7`/`Ev7`/`Fv7` generations) only
support `NVMe` and cannot boot such an image without `azure_disk_controller_type`
set explicitly to `NVMe`. Confidential-computing families (`DCasv6`/`ECasv6`
and similar) have a separate, unrelated incompatibility: they require an
explicit `securityProfile.securityType` this workflow's VM deployment
template does not set, and are not supported by `azure_disk_controller_type`
or any other input here. Before picking `azure_vm_size`, confirm both:

- Its supported disk controller type(s):
  `az vm list-skus --location <region> --size <size> --resource-type
  virtualMachines --query "[0].capabilities[?name=='DiskControllerTypes']"`.
  If the result is `NVMe` only, set `azure_disk_controller_type=NVMe`; if it
  includes `SCSI` (alone or alongside `NVMe`), leave `azure_disk_controller_type`
  `unset`.
- It isn't a confidential-computing family (`DC*`/`EC*`) - those need a
  `securityProfile` change this workflow doesn't make, regardless of
  `azure_disk_controller_type`.
- Its Spot/low-priority core quota headroom in the target subscription/region
  (`az vm list-usage --location <region> --query "[?name.value=='lowPriorityCores']"`)
  covers the size's own vCPU count - a subscription's default quota can be
  quite low (single digits), which rules out larger sizes (memory-optimized
  `M`-series, for example) even when they're otherwise controller-compatible.

### Azure hard bounds

- `max_runtime_minutes` is checked against a hard-coded workflow ceiling of
  **20** before anything else runs (before Go setup, before any Azure
  credential is even configured) — the same ceiling AWS uses. No input can
  raise this ceiling.
- The job's own `timeout-minutes: 45` is a second, independent ceiling that
  does not derive from the input at all.
- Deletion runs on its own fixed 5-minute budget, independent of
  `max_runtime_minutes`.
- `concurrency: { group: qualify-azure, cancel-in-progress: false }` — only
  one real-cloud Azure run at a time; a second dispatch queues rather than
  races or cancels an in-flight one.

### Azure guaranteed cleanup

Two independent layers, either one alone sufficient for the common case:

1. The Go test's own `t.Cleanup`, registered immediately once a real VM
   exists, running on its own fresh 5-minute context. It drives the
   adapter's own `Observe`/`Delete` to completion (Azure's `Delete` removes
   one dependent resource per call — see "What it does" above), then
   independently re-queries for anything still tagged to this run.
2. The workflow's own final step, gated `if: always()` — the authoritative
   backstop, since it runs even if the job was killed by its own timeout or
   cancelled before the Go process's `t.Cleanup` could run. It re-derives
   everything it needs (resource group, allocation id, owner tag) directly
   from job inputs/env, never from a prior step's output, reuses the `az`
   CLI session `azure/login` already established earlier in the same job,
   independently re-queries for anything still tagged to this run's
   allocation across the whole resource group, force-cleans anything found
   (VMs first, then whatever remains), and fails the job loudly (that
   condition is a real bug, never a normal outcome).

### Azure credentials

Azure Workload Identity Federation (OIDC) is the **only** supported path —
there is no client-secret fallback here, unlike AWS's static-key fallback
(see [qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why). Set the `AZURE_QUALIFICATION_CLIENT_ID` and
`AZURE_QUALIFICATION_TENANT_ID` repository variables (not secrets — a
client ID and tenant ID are not sensitive on their own, matching
`AWS_QUALIFICATION_ROLE_ARN`'s and the `GCP_QUALIFICATION_*` identifiers'
own reasoning) to an Azure AD app registration's client ID and tenant ID,
with a federated credential configured to trust this repository's GitHub
Actions OIDC issuer (`https://token.actions.githubusercontent.com`) for
`qualify.yml`'s `workflow_dispatch` runs, audience
`api://AzureADTokenExchange`.

Two independent consumers of this same trust relationship are used side by
side in the workflow: `azure/login` authenticates the `az` CLI used by this
workflow's own pre-flight and cleanup steps, and a separately-fetched
GitHub OIDC token is written to its own file and exposed as
`AZURE_FEDERATED_TOKEN_FILE`/`AZURE_CLIENT_ID`/`AZURE_TENANT_ID` for the Go
test step — the exact environment variable shape
`internal/provider/credentials.go`'s `azureCredential` already resolves via
`azidentity.NewWorkloadIdentityCredential` when `NewCommand` is given a
`nil` environment map.

Both `AZURE_QUALIFICATION_CLIENT_ID` and `AZURE_QUALIFICATION_TENANT_ID`
already exist in this repository today (confirmed via `gh-tsouza variable
list`), alongside `AZURE_QUALIFICATION_SUBSCRIPTION_ID`.

#### Azure minimum RBAC permissions

The federated identity needs, scoped to the pinned subscription (or
resource group, if narrower):
`Microsoft.Resources/subscriptions/resourceGroups/read`;
`Microsoft.Resources/deployments/read`,
`Microsoft.Resources/deployments/write`,
`Microsoft.Resources/deployments/delete`,
`Microsoft.Resources/deployments/operations/read`,
`Microsoft.Resources/deployments/exportTemplate/action`;
`Microsoft.Compute/virtualMachines/read`,
`Microsoft.Compute/virtualMachines/write`,
`Microsoft.Compute/virtualMachines/delete`;
`Microsoft.Compute/disks/read`, `Microsoft.Compute/disks/write`,
`Microsoft.Compute/disks/delete`; `Microsoft.Compute/images/read`;
`Microsoft.Compute/locations/*/read`; `Microsoft.Network/locations/*/read`;
`Microsoft.Network/networkInterfaces/read`,
`Microsoft.Network/networkInterfaces/write`,
`Microsoft.Network/networkInterfaces/delete`,
`Microsoft.Network/networkInterfaces/join/action`;
`Microsoft.Network/virtualNetworks/read`,
`Microsoft.Network/virtualNetworks/subnets/read`,
`Microsoft.Network/virtualNetworks/subnets/join/action`;
`Microsoft.Network/networkSecurityGroups/read`,
`Microsoft.Network/networkSecurityGroups/join/action`. No subscription-wide,
resource-group-creation or billing permissions are needed — this identity
only ever reads and attaches to a resource group/VNet/subnet/NSG a
*separate* identity created (see "Network provisioning" below), never
creating or modifying the network itself.

### Network provisioning (issue #89)

Unlike the compute side above, this workflow's qualification network is
not an operator-pinned input at all: `tools/tofu/qualify-network/azure/`
(an OpenTofu module) is applied at the start of this provider's job and
destroyed at the end of the same job, creating a fresh, isolated resource
group, VNet, subnet and network security group exclusively for that one
run. See that module's own `README.md` for exactly what it creates, and
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this replaced an earlier, separately-exposed provision/destroy
workflow.

This uses a **deliberately separate** identity from
`AZURE_QUALIFICATION_CLIENT_ID` above: the `AZURE_NETWORK_PROVISIONER_CLIENT_ID`/
`AZURE_NETWORK_PROVISIONER_TENANT_ID`/`AZURE_NETWORK_PROVISIONER_SUBSCRIPTION_ID`
repository variables, backing an Azure AD app registration and federated
credential with its own custom role scoped to exactly
`Microsoft.Resources/subscriptions/resourceGroups/read`,
`Microsoft.Resources/subscriptions/resourceGroups/write`,
`Microsoft.Resources/subscriptions/resourceGroups/delete`;
`Microsoft.Network/virtualNetworks/read`,
`Microsoft.Network/virtualNetworks/write`,
`Microsoft.Network/virtualNetworks/delete`;
`Microsoft.Network/virtualNetworks/subnets/read`,
`Microsoft.Network/virtualNetworks/subnets/write`,
`Microsoft.Network/virtualNetworks/subnets/delete`,
`Microsoft.Network/virtualNetworks/subnets/join/action`;
`Microsoft.Network/networkSecurityGroups/read`,
`Microsoft.Network/networkSecurityGroups/write`,
`Microsoft.Network/networkSecurityGroups/delete`,
`Microsoft.Network/networkSecurityGroups/join/action`;
`Microsoft.Network/networkSecurityGroups/securityRules/read`,
`Microsoft.Network/networkSecurityGroups/securityRules/write`,
`Microsoft.Network/networkSecurityGroups/securityRules/delete`;
`Microsoft.Network/locations/*/read` - nothing the qualification identity
above also needs, and nothing that identity is granted.

### Azure operator prerequisites

Before dispatching this workflow, an operator must have already:

1. Chosen a pinned, generalized Linux managed image with only an OS disk,
   and confirmed it is available in the target region.
2. Chosen a cheap VM size available in the target region and Availability
   Zone.
3. Registered an Azure AD app registration with a federated credential
   trusting this repository's GitHub Actions OIDC issuer, granted the
   minimum RBAC permissions above, and set `AZURE_QUALIFICATION_CLIENT_ID`/
   `AZURE_QUALIFICATION_TENANT_ID`/`AZURE_QUALIFICATION_SUBSCRIPTION_ID`.
4. Set `AZURE_NETWORK_PROVISIONER_CLIENT_ID`/
   `AZURE_NETWORK_PROVISIONER_TENANT_ID`/
   `AZURE_NETWORK_PROVISIONER_SUBSCRIPTION_ID`, granting the separate
   network-provisioning permissions listed above.

### What Azure qualifies — and what it honestly does not

Qualified, for real, by a passing run:

- Real Azure Spot VM creation (with its dependent NIC and OS disk), a real
  transition to `PowerState/running`, real Spot price observation, real
  deletion, and independently re-verified cleanup — all through the exact
  production adapter code path.

Named gaps, not covered by this piece:

- **Real GitHub Actions job execution** (issue #3's "ordinary runs-on,
  actual VM job execution"). The VM boots with a synthetic, non-functional
  placeholder in place of a real GitHub JIT registration token — the
  image's cloud-init runner-registration step is expected to fail
  harmlessly inside the guest. Confirming a real runner registers and
  executes a real workflow job needs a live scale set and a real ephemeral
  registration token — a separate, larger qualification piece this
  workflow does not attempt.
- **A real Spot eviction.** Azure's generally available Compute API
  provides no way to force one on demand either (only the separate,
  unintegrated Azure Chaos Studio service can). This workflow can only
  confirm the adapter correctly reports "not interrupted" against a real,
  healthy running instance; `azureConfirmedPreemption`'s Event Grid/Storage
  Queue-fed detection logic itself is covered separately, against
  synthesized payloads, by the existing unit test suite.

## GCP

`.github/workflows/qualify.yml`'s `provider: gcp` matrix branch drives the
production `internal/provider` GCP adapter (`gcp_sdk.go`, `gcp_credentials.go`) against
real GCP infrastructure, through `internal/provider/gcp_realcloud_test.go`
(built only with `-tags realcloud`, alongside — never replacing —
`aws_realcloud_test.go`). One run:

1. Creates one real, billed Compute Engine Spot instance (plus its boot
   disk) via the adapter's own `CreateWithResources`.
2. Independently confirms it reaches Compute Engine's real "RUNNING" status,
   using a second `compute.Service` client built through
   `google.golang.org/api/compute/v1`'s own default-credential resolution —
   never the adapter's own `newGCPSDK`/`gcpFileCredential` construction — so
   this confirmation never depends on the exact code path under
   qualification also certifying its own success.
3. Confirms the adapter's `Observe` reports the running instance as not
   interrupted.
4. Deletes the instance via the adapter's own `Delete`, then independently
   re-queries the GCP API (the same second client) for a leftover boot disk
   or any reserved address owned by this run's allocation labels — never
   just trusting `Delete`'s or `Observe`'s own report of success.
5. Uploads everything observed (creation receipt, running-state
   confirmation, observation results, the independent post-delete inventory
   query) as a workflow artifact, at
   `evidence/qualify-gcp-<run>-<attempt>/manifest.json`.

Real Spot **price** observation is not attempted — see "What GCP qualifies
— and what it honestly does not" below.

### GCP trigger and required inputs

`workflow_dispatch` only — no schedule, no push, no pull_request, on
`qualify.yml` with `provider: gcp` (or `all`). As with AWS and Azure, only
`max_runtime_minutes` is required at the trigger
level; every other input below is enforced as required for GCP by the
workflow's own first GCP-specific step, before any GCP credential is
configured:

| Input | Meaning |
| --- | --- |
| `gcp_project` | Project to qualify against. Must equal `vars.GCP_QUALIFICATION_PROJECT_ID` (checked before any credential is configured). Also where the qualification network (see below) is created and destroyed. |
| `gcp_region` | Region to qualify against. |
| `gcp_zone` | Zone to launch into. Must belong to `gcp_region` (checked before any create call). |
| `gcp_image` | Pinned boot image, as an exact image self-link — never a rolling `.../images/family/...` reference, which is not a pinned identifier. Resolved (existence-checked) before any create call. |
| `gcp_machine_type` | Keep this cheap — it directly bounds real spend alongside `max_runtime_minutes`. |
| `max_runtime_minutes` | Plain integer, 1–20. Bounds the create/observe phase. |

The image/machine type/project are auto-discovered from nowhere -
operator-supplied, per issue #3's "pinned images/networks" requirement.
Unlike AWS, GCP zones are not derivable from a subnetwork (a GCP subnetwork
is regional, not zonal, so any zone in the region is valid for it) — `gcp_zone`
is therefore its own required input, cross-checked against `gcp_region`
directly rather than read from another pinned identifier. The **network**
is a different matter (issue #89): `gcp_network`/`gcp_subnetwork` are not
workflow inputs at all - this workflow provisions its own isolated VPC
network/subnetwork via OpenTofu at the start of the job and destroys it at
the end (see "Network provisioning" below).

There is no `architecture` input: `internal/provider/gcp_sdk.go`'s
`createGCP` never reads `Offering.Architecture` at all (unlike AWS's
`createAWS`, which cross-validates the AMI's real architecture against the
input before creating) — adding an input the adapter does not consult would
document a safety property this piece does not actually have. See
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this is named as a real adapter-level gap rather than papered over.

### GCP hard bounds

- `max_runtime_minutes` is checked against a hard-coded workflow ceiling of
  **20** before anything else runs (before Go setup, before GCP credentials
  are even configured). No input can raise this ceiling.
- The job's own `timeout-minutes: 45` is a second, independent ceiling that
  does not derive from the input at all.
- Deletion runs on its own fixed 5-minute budget, independent of
  `max_runtime_minutes` — a run that spent its whole create/observe budget
  just confirming the VM works still gets a full, unhurried teardown
  attempt.
- `concurrency: { group: qualify-gcp, cancel-in-progress: false }` — only
  one real-cloud run at a time; a second dispatch queues rather than races
  or cancels an in-flight one.

### GCP guaranteed cleanup

Two independent layers, either one alone sufficient for the common case:

1. The Go test's own `t.Cleanup`, registered immediately once a real
   instance exists, running on its own fresh 5-minute context regardless of
   how much of the create/observe budget was already spent.
2. The workflow's own final step, gated `if: always()` — the authoritative
   backstop, since it runs even if the job was killed by its own timeout or
   cancelled before the Go process's `t.Cleanup` could run. It re-derives
   everything it needs (project, zone, region, allocation id, owner label)
   directly from job inputs/env, never from a prior step's output, so it
   still works when every earlier step failed. It independently re-queries
   for any instance/disk/reserved-address still labeled to this run and
   force-cleans anything found, then fails the job loudly (that condition is
   a real bug, never a normal outcome).

### GCP credentials

Workload Identity Federation only — deliberately no service account key
fallback (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this genuinely differs from AWS's OIDC-preferred/static-key-fallback
pair). Three repository **variables** (not secrets — none of these values
are sensitive on their own; GCP's WIF trust policy is what actually gates
access) already exist in this repository:

- `GCP_QUALIFICATION_PROJECT_ID` — the project the qualification identity is
  provisioned in.
- `GCP_QUALIFICATION_SERVICE_ACCOUNT` — the service account email the
  workflow impersonates.
- `GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER` — the full WIF provider
  resource name.

`google-github-actions/auth` assumes this identity via
`workload_identity_provider`/`service_account`, using the job's own GitHub
Actions OIDC identity (`id-token: write`, granted only to this job), and
exports `GOOGLE_APPLICATION_CREDENTIALS`/
`CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE` pointing at the generated
credential file — exactly the environment shape
`internal/provider/gcp_credentials.go`'s `gcpCredential` already reads from
a nil environment map, so no branching is needed anywhere below that step.

#### GCP minimum IAM permissions

The impersonated service account needs, scoped to the pinned
project/region/zone: `compute.zones.get`, `compute.images.get`,
`compute.images.useReadOnly`, `compute.machineTypes.get`,
`compute.instances.get`, `compute.instances.list`, `compute.disks.get`,
`compute.disks.list`, `compute.addresses.list`, `compute.zoneOperations.get`,
`compute.zoneOperations.list`; `compute.instances.create`,
`compute.instances.delete`, `compute.disks.create`, `compute.disks.delete`,
`compute.instances.setLabels`, `compute.disks.setLabels`,
`compute.subnetworks.use`. `compute.instances.setLabels`/
`compute.disks.setLabels` are both needed even though `gcp_sdk.go`'s
`createGCP` never calls a distinct `setLabels` API method - it sets labels
as part of the `instances.create`/`disks.create` insert calls themselves,
but GCE's own IAM checks still gate that on the `setLabels` permission
(confirmed by an actual real-cloud dispatch: `compute.disks.setLabels` was
missing and the create failed with a live "Required
'compute.disks.setLabels' permission" error - see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
"What real dispatches found" section). No project-creation, IAM,
VPC-creation or billing permissions are needed — this identity only ever
launches into a network/subnetwork a *separate* identity created (see
"Network provisioning" below), never creating or modifying the network
itself.

### Network provisioning (issue #89)

Unlike the compute side above, this workflow's qualification network is
not an operator-pinned input at all: `tools/tofu/qualify-network/gcp/`
(an OpenTofu module) is applied at the start of this provider's job and
destroyed at the end of the same job, creating a fresh, isolated VPC
network and subnetwork exclusively for that one run. See that module's own
`README.md` for exactly what it creates, and
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this replaced an earlier, separately-exposed provision/destroy
workflow.

This uses a **deliberately separate** identity from
`GCP_QUALIFICATION_*` above: the `GCP_NETWORK_PROVISIONER_PROJECT_ID`/
`GCP_NETWORK_PROVISIONER_SERVICE_ACCOUNT`/
`GCP_NETWORK_PROVISIONER_WORKLOAD_IDENTITY_PROVIDER` repository variables,
backing a service account with its own custom role scoped to exactly
`compute.networks.create`/`compute.networks.delete`/`compute.networks.get`/
`compute.networks.list`, `compute.subnetworks.create`/
`compute.subnetworks.delete`/`compute.subnetworks.get`/
`compute.subnetworks.list`/`compute.subnetworks.use`,
`compute.firewalls.create`/`compute.firewalls.delete`/
`compute.firewalls.get`/`compute.firewalls.list`, plus
`compute.regions.get`/`compute.regions.list`/`compute.zones.get`/
`compute.zones.list`/`compute.regionOperations.get`/
`compute.globalOperations.get` for OpenTofu's own polling - nothing the
qualification identity above also needs, and nothing that identity is
granted.

### GCP operator prerequisites

Before dispatching this workflow, an operator must have already:

1. Chosen a pinned boot image self-link.
2. Chosen a cheap machine type.
3. Provisioned the Workload Identity Federation pool/provider, the
   qualification service account, and set the three repository variables
   above, granting the minimum permissions listed.
4. Provisioned the separate network-provisioner service account and set
   `GCP_NETWORK_PROVISIONER_PROJECT_ID`/
   `GCP_NETWORK_PROVISIONER_SERVICE_ACCOUNT`/
   `GCP_NETWORK_PROVISIONER_WORKLOAD_IDENTITY_PROVIDER`, granting the
   separate network-provisioning permissions listed above.

### What GCP qualifies — and what it honestly does not

Qualified, for real, by a passing run:

- Real Compute Engine Spot creation, a real transition to "RUNNING", real
  deletion, and independently re-verified cleanup — all through the exact
  production adapter code path.

Named gaps, not covered by this piece:

- **Real GitHub Actions job execution** (issue #3's "ordinary runs-on,
  actual VM job execution"). The VM boots with a synthetic, non-functional
  placeholder in place of a real GitHub JIT registration token — the
  image's startup-script runner-registration step is expected to fail
  harmlessly inside the guest. Confirming a real runner registers and
  executes a real workflow job needs a live scale set and a real ephemeral
  registration token — a separate, larger qualification piece this workflow
  does not attempt.
- **Real GCP Spot price observation** (issue #2). No `internal/prices` GCP
  client exists: Compute Engine's public pricing surface splits Core/RAM
  SKUs with no structured machine-type field, no zone-level pricing and no
  request-side filter, so a GCP price client was assessed as not honestly
  buildable without guessing — see
  [prices-gcp.md](prices-gcp.md) and
  [prices-gcp.background.md](prices-gcp.background.md).
  This is a named, tracked gap, not a silently skipped step.
- **A real Spot preemption.** GCP provides no supported, on-demand API to
  force one. This workflow can only confirm the adapter correctly reports
  "not interrupted" against a real, healthy running instance; the
  `gcpConfirmedPreemption` detection logic itself is covered separately,
  against synthesized operation payloads, by the existing unit test suite.
- **Architecture/image compatibility.** `createGCP` never cross-validates
  `gcp_machine_type` against `gcp_image`'s real architecture the way AWS's
  adapter does for `aws_instance_type`/`aws_ami_id` — an operator error here
  fails at VM boot, not at this workflow's pre-flight.

See [qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for the reasoning behind these design choices.
