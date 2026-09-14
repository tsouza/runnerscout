# GCP qualification network (OpenTofu)

Creates the isolated GCP VPC network and subnetwork that
[`.github/workflows/qualify.yml`](../../../../.github/workflows/qualify.yml)'s
`provider: gcp` matrix branch and
[`tools/e2e/bring-up-gcp.sh`](../../../e2e/bring-up-gcp.sh) already expect as
pre-existing, pinned, operator-supplied input (`gcp_network`/`gcp_subnetwork`
and `E2E_GCP_NETWORK`/`E2E_GCP_SUBNETWORK` respectively). See
[docs/qualification-real-cloud.md](../../../../docs/qualification-real-cloud.md)'s
GCP "operator prerequisites" section for where this fits in the larger
qualification setup.

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

## Running it

```sh
cd tools/tofu/qualify-network/gcp
tofu init
tofu plan  -var project_id=<your-gcp-qualification-project> -var region=<e.g. us-central1>
tofu apply -var project_id=<your-gcp-qualification-project> -var region=<e.g. us-central1>
```

State is kept **local** (no remote backend) - this is a deliberate tradeoff
for a module this small and this rarely run, not an oversight. Keep the
resulting `terraform.tfstate` somewhere safe if you'll ever want to `tofu
plan`/`destroy` through this module again instead of managing the network by
hand from here on; losing it does not affect the already-created GCP
resources, only this module's ability to manage them going forward.

Copy the two outputs directly into whichever of the following you're
provisioning for:

- `qualify.yml`'s `workflow_dispatch` inputs: `network_name` → `gcp_network`,
  `subnetwork_name` → `gcp_subnetwork`.
- `tools/e2e/env-gcp.example` (copied to your own untracked env file):
  `network_name` → `E2E_GCP_NETWORK`, `subnetwork_name` → `E2E_GCP_SUBNETWORK`.

## Intended usage: one-time bootstrap, run via CI with Workload Identity Federation

This module is meant to be applied **once** per GCP qualification project,
not on every qualification run - the network and subnetwork it creates are
long-lived infrastructure, matching the fact that `qualify.yml` and
`bring-up-gcp.sh` both treat `gcp_network`/`gcp_subnetwork` as pinned,
pre-existing identifiers rather than something they provision themselves.
There is deliberately no auto-destroy, TTL, or other ephemeral-lifecycle
logic here.

A future, separate piece of work (out of scope for this module) is expected
to add a CI workflow that runs `tofu apply` for this module, authenticating
via the same Workload Identity Federation approach `qualify.yml` already
uses for the qualification service account (see
[docs/qualification-real-cloud.md](../../../../docs/qualification-real-cloud.md)'s
GCP "credentials" section) - never a downloaded service account key JSON.
Until that workflow exists, run this by hand as shown above, using your own
`gcloud auth application-default login` or an impersonated-service-account
ADC file, the same way `tools/e2e/env-gcp.example` documents for
`E2E_GCP_CREDENTIALS_FILE`.

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
