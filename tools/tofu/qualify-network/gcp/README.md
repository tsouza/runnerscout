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
for a module this small and this rarely run, not an oversight. Since each
`apply` is meant to be matched by a `destroy` for the same run (see below),
the state file only needs to survive for the lifetime of that one
apply-then-destroy cycle - if you split `apply` and `destroy` across
different machines/CI jobs, make sure the state `apply` produced is passed
through to whichever job runs `destroy` (e.g. as a build artifact), or
`destroy` will find nothing to tear down and the resources will be
orphaned.

Copy the two outputs directly into whichever of the following you're
provisioning for:

- `qualify.yml`'s `workflow_dispatch` inputs: `network_name` → `gcp_network`,
  `subnetwork_name` → `gcp_subnetwork`.
- `tools/e2e/env-gcp.example` (copied to your own untracked env file):
  `network_name` → `E2E_GCP_NETWORK`, `subnetwork_name` → `E2E_GCP_SUBNETWORK`.

## Intended usage: apply before a run, destroy right after, run via CI with Workload Identity Federation

None of this module's resources (VPC network, subnetwork, firewall rules)
bill by the hour the way the sibling `../aws` module's NAT Gateway does, so
applying this once and leaving it standing indefinitely would not itself
cost anything. It is nonetheless meant to follow the same apply-before/
destroy-after-each-run lifecycle as the AWS and Azure siblings (see
`../aws/README.md`'s "Ephemeral" section for the AWS module, where that
lifecycle is cost-driven) - for consistency across all three providers, and
so a single future CI workflow can wrap `tofu apply` → dispatch
`qualify.yml`/the E2E harness with this module's outputs → `tofu destroy`
identically for every provider, rather than needing provider-specific
lifecycle handling. `qualify.yml`/`bring-up-gcp.sh` still treat
`gcp_network`/`gcp_subnetwork` as pinned, pre-existing identifiers for the
duration of one dispatch - "pinned" here means "fixed for that run," not
"created once and never rebuilt."

A future, separate piece of work (out of scope for this module) is expected
to add that CI workflow, authenticating via the same Workload Identity
Federation approach `qualify.yml` already uses for the qualification service
account (see
[docs/qualification-real-cloud.md](../../../../docs/qualification-real-cloud.md)'s
GCP "credentials" section) - never a downloaded service account key JSON.
Until that workflow exists, run this by hand as shown above, using your own
`gcloud auth application-default login` or an impersonated-service-account
ADC file, the same way `tools/e2e/env-gcp.example` documents for
`E2E_GCP_CREDENTIALS_FILE` - and run `tofu destroy` with the same `-var`
values once you're done with a given qualification/E2E run.

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
