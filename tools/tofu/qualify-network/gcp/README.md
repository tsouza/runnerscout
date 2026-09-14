# GCP qualification network (OpenTofu)

Creates the isolated GCP VPC network and subnetwork that a real-cloud GCP
qualification run needs.

[`.github/workflows/qualify.yml`](../../../../.github/workflows/qualify.yml)'s
`provider: gcp` matrix branch runs this module directly (`tofu apply` at
the start of its job, `tofu destroy` at the end, using a dedicated
`GCP_NETWORK_PROVISIONER_*` identity) - an operator dispatching that
workflow never touches this module or its outputs by hand.
[`tools/e2e/bring-up-gcp.sh`](../../../e2e/bring-up-gcp.sh) (a separate,
local-runbook E2E harness - see
[docs/e2e-qualification.md](../../../../docs/e2e-qualification.md)) still
expects `E2E_GCP_NETWORK`/`E2E_GCP_SUBNETWORK` as pre-existing, pinned,
operator-supplied input, since that harness was not changed by this
module's introduction - an operator preparing to run it should `tofu
apply` this module by hand first (see "Running it by hand" below) and copy
its outputs into `tools/e2e/env-gcp.example`.

## What this creates

- One custom-mode VPC network (`runners`).
- One subnetwork (`runners`) in that network, in the region you choose, at
  `10.92.0.0/24`.
- Two firewall rules narrowing the network to the one thing a launched
  qualification/e2e runner instance actually does (outbound HTTPS) - see the
  detailed reasoning in `main.tf`'s comments, including why no ingress rule
  is needed at all.

It does **not** create any GCP service account, IAM binding, Workload
Identity Pool/Provider, or Cloud NAT/Router - those are out of scope for this
module; see `main.tf`'s "Known limitation" comment for what Cloud NAT's
absence means in practice.

## Running it by hand (for tools/e2e, not for qualify.yml)

`qualify.yml` never needs this - it runs `tofu apply`/`tofu destroy`
itself, per dispatch. Run this by hand only to prepare
`tools/e2e/bring-up-gcp.sh`'s own pinned inputs:

```sh
cd tools/tofu/qualify-network/gcp
tofu init
tofu plan  -var project_id=<your-gcp-qualification-project> -var region=<e.g. us-central1>
tofu apply -var project_id=<your-gcp-qualification-project> -var region=<e.g. us-central1>
```

Authenticate with your own `gcloud auth application-default login` or an
impersonated-service-account ADC file, the same way
`tools/e2e/env-gcp.example` documents for `E2E_GCP_CREDENTIALS_FILE` -
never a downloaded service account key JSON.

State is kept **local** (no remote backend) - this is a deliberate tradeoff
for a module this small and this rarely run, not an oversight. `qualify.yml`
never needs to move this state anywhere (its own `apply`/`destroy` run in
the same job, on the same runner filesystem). If you run `apply`/`destroy`
by hand from different machines for the same network, you'd need to carry
the state file between them yourself, or `destroy` will find nothing to
tear down and the resources will be orphaned.

Copy the two outputs into `tools/e2e/env-gcp.example` (copied to your own
untracked env file): `network_name` → `E2E_GCP_NETWORK`, `subnetwork_name`
→ `E2E_GCP_SUBNETWORK`. Run `tofu destroy` with the same `-var` values once
you're done with a given E2E run - unlike `qualify.yml`'s own automatic
apply/destroy, a by-hand `apply` here has no automatic teardown.

## CI credentials, using Workload Identity Federation

`qualify.yml` authenticates to run this module via Workload Identity
Federation under `GCP_NETWORK_PROVISIONER_PROJECT_ID`/
`GCP_NETWORK_PROVISIONER_SERVICE_ACCOUNT`/
`GCP_NETWORK_PROVISIONER_WORKLOAD_IDENTITY_PROVIDER` - repository
variables, deliberately **separate** from the `GCP_QUALIFICATION_*`
identity `qualify.yml` also uses, for the actual VM lifecycle, and which
has no permission to create or delete network/subnetwork/firewall
resources by design. Never a downloaded service account key JSON, in CI or
by hand.

This module intentionally has no GCP service account, IAM binding, or
Workload Identity Pool/Provider of its own - provisioning the identity it
runs under is a separate, human-driven bootstrap step, out of scope here.
See `docs/qualification-real-cloud.background.md` for what that identity's
minimum required permissions are.

## Apply before a run, destroy right after

None of this module's resources (VPC network, subnetwork, firewall rules)
bill by the hour the way the sibling `../aws` module's NAT Gateway does, so
applying this once and leaving it standing indefinitely would not itself
cost anything. It is nonetheless meant to follow the same apply-before/
destroy-after-each-run lifecycle as the AWS and Azure siblings (see
`../aws/README.md`'s "Ephemeral" section for the AWS module, where that
lifecycle is cost-driven) - for consistency across all three providers.
`qualify.yml`'s own `provider: gcp` matrix branch does exactly this
automatically, in the same job - see that workflow for the actual
apply/destroy step sequence.

## Judgment calls worth knowing about

- **Labels vs. `description`**: the task this module was built for asked for
  a `purpose = "qualification-network"` label on every resource. GCP's
  Compute Engine API does not support labels on VPC networks, subnetworks,
  or firewall rules at all (verified against the `google` provider's own
  argument reference for `google_compute_network`/`google_compute_subnetwork`/
  `google_compute_firewall` - none of the three accept a `labels` argument;
  labeling is a per-resource-type GCP feature these three simply aren't
  part of). Each resource's `description` field carries the same
  "this is runnerscout's own infra" identification instead.
- **Firewall rules**: see `main.tf`'s inline reasoning. Short version: every
  VPC network - custom-mode included - already has an implied allow-all
  egress rule and an implied deny-all ingress rule that cannot be deleted,
  only overridden by a higher-priority explicit rule
  ([GCP docs](https://cloud.google.com/firewall/docs/firewalls#default_firewall_rules)).
  No explicit ingress rule is added (the implied deny already provides
  "zero inbound"); an explicit egress rule *is* added to narrow the implied
  allow-all-ports egress down to just port 443, the only outbound traffic
  a launched runner instance actually initiates.
- **No Cloud NAT**: `internal/provider/gcp_sdk.go`'s `createGCP` never
  assigns an external IP to the instances it launches, so even with the
  443-only egress rule above, an instance in this subnetwork has no actual
  route to the public internet yet - GCP requires either an external IP or
  Cloud NAT for that. This is out of scope for this module (task scope was
  network/subnetwork/firewall only). It has no effect on `qualify.yml`'s own
  real-cloud test, which never depends on the guest reaching GitHub (its
  `Bootstrap` is a synthetic, non-functional fixture token - see
  `gcp_realcloud_test.go`'s header comment). It does mean a real runner
  registration through `tools/e2e/bring-up-gcp.sh` cannot actually reach
  GitHub against this network until Cloud NAT is separately provisioned -
  worth flagging to whoever builds that out, not something to assume works.
