# One-time bootstrap for the isolated GCP network that .github/workflows/
# qualify.yml's `provider: gcp` matrix branch and tools/e2e/bring-up-gcp.sh
# both already expect as PRE-EXISTING, pinned, operator-supplied input
# (gcp_network/gcp_subnetwork and E2E_GCP_NETWORK/E2E_GCP_SUBNETWORK,
# respectively - neither workflow nor harness ever creates this network
# itself; see docs/qualification-real-cloud.md's GCP "operator
# prerequisites" section, item 1: "Provisioned an isolated VPC network and
# subnetwork dedicated to qualification (never a production network)").
#
# See README.md for how to run this and why it is a one-time, long-lived
# bootstrap rather than something re-applied per qualification run.
#
# Naming: both the network and subnetwork are named "runners" to match the
# pre-existing worked example in tools/e2e/env-gcp.example
# (E2E_GCP_NETWORK='runners' / E2E_GCP_SUBNETWORK='runners']) - an operator
# who applies this module and then copies its outputs into that example (or
# into qualify.yml's workflow_dispatch inputs) gets identifiers that already
# match what the repo's own documented example expects, with nothing to
# rename.
#
# Identification: GCP's compute API does not support labels on
# google_compute_network/google_compute_subnetwork/google_compute_firewall
# at all (verified against the provider's own argument reference for each
# resource - none of the three accept a `labels` block; labels are a
# per-resource-type GCP feature, and networks/subnetworks/firewall rules are
# simply not among the resource kinds that carry them). Each resource's
# `description` field - which all three DO support - carries the same
# "this is runnerscout's own qualification infra, not guessed-at
# production infra" identification a label would otherwise provide.

resource "google_compute_network" "qualify" {
  project     = var.project_id
  name        = "runners"
  description = "runnerscout qualification-network (tools/tofu/qualify-network/gcp) - isolated VPC for real-cloud-spend qualify.yml/e2e GCP runs, never production traffic"

  # Explicit custom mode: GCP's auto-mode default subnetworks are chosen by
  # GCP (one per region, at a fixed, non-negotiable CIDR) rather than
  # pinned by this repo, which conflicts with this repo's "nothing
  # auto-discovered, everything pinned" qualification philosophy (see
  # docs/qualification-real-cloud.md - "Nothing is auto-discovered: the
  # image and every network identifier are operator-supplied"). A
  # custom-mode network starts with zero subnetworks, so the one below is
  # the only one that will ever exist here.
  auto_create_subnetworks = false

  # routing_mode left at its provider default (REGIONAL): this network only
  # ever holds one region's subnetwork, so global dynamic routing has no
  # effect either way; REGIONAL is also GCP's own default for a reason -
  # global routing has cost/scope implications this single-subnet network
  # never needs.
}

resource "google_compute_subnetwork" "qualify" {
  project       = var.project_id
  name          = "runners"
  description   = "runnerscout qualification-network (tools/tofu/qualify-network/gcp) - isolated subnetwork for real-cloud-spend qualify.yml/e2e GCP runs, never production traffic"
  network       = google_compute_network.qualify.id
  region        = var.region
  ip_cidr_range = "10.92.0.0/24" # 254 usable addresses - far more than one ephemeral qualification VM at a time will ever need.

  # No private_google_access, no secondary ranges, no flow logs: this
  # subnetwork exists to hold one ephemeral qualification/e2e VM at a time
  # (internal/provider/gcp_sdk.go's createGCP), which reads no Google API
  # from inside the guest - only the metadata server, which is reachable
  # regardless of this setting. Adding it would be unused surface area, not
  # a safety property.
}

# --- Firewall: what's actually necessary, and why -------------------------
#
# Every VPC network - custom-mode included - always carries two IMPLIED
# firewall rules that cannot be deleted, only overridden by a
# higher-priority (lower-numbered) explicit rule (see
# https://cloud.google.com/firewall/docs/firewalls#default_firewall_rules):
#   - an implied ALLOW-egress rule (all protocols/ports, destination
#     0.0.0.0/0), and
#   - an implied DENY-ingress rule (all protocols/ports, source 0.0.0.0/0).
# A newly created custom-mode network has these two implied rules and
# NOTHING else - unlike GCP's auto-created "default" network, which is
# additionally pre-populated with several explicit convenience rules
# (default-allow-internal/-ssh/-rdp/-icmp) that a custom-mode network never
# gets for free. So the premise that this network starts with "no firewall
# rules at all" is only half right: it starts with no INGRESS access (good,
# matches what's needed below) but already permits ALL egress, not none.
#
# What the workload actually needs (internal/provider/gcp_sdk.go's
# createGCP, cross-checked against gcp_realcloud_test.go): the launched
# instance runs a GitHub Actions self-hosted runner's registration/startup
# flow, which only ever initiates outbound HTTPS (443) connections (to
# GitHub's Actions/API/codeload endpoints) - it never accepts any inbound
# connection, and createGCP never attaches an external IP (ForceSendFields
# on AccessConfigs), a reserved address, or any instance network tag/service
# account the instance could be targeted by. So:
#
#   - No explicit ingress rule is created. The implied deny-ingress rule
#     already provides exactly the desired "zero inbound" property; adding
#     an explicit rule that denies the exact same thing the implied rule
#     already denies would be pure ceremony, not an added safety property.
#   - An explicit egress rule IS added, narrowing the implied allow-all-ports
#     egress down to only the port the workload actually uses (443). This is
#     a deliberate least-privilege tightening, not a fix for a missing
#     default - the implied rule would already "work" for this workload,
#     but it also permits every other outbound port, which this dedicated
#     qualification network has no reason to allow.
#   - No target_tags/target_service_accounts are set on either rule: they
#     apply network-wide. createGCP's own Instance struct never sets a tag
#     and explicitly force-sends an empty ServiceAccounts list, so a
#     tag/service-account-scoped rule would silently match nothing. Since
#     this network is dedicated solely to qualification/e2e instances (never
#     shared with unrelated infrastructure), a network-wide rule is exactly
#     as scoped as this network itself.
#
# Known limitation (out of this module's scope - see README.md): because
# createGCP never assigns an external IP, an instance launched into this
# subnetwork has no actual route to the internet yet, even though this
# egress rule permits port 443 - reaching the real internet additionally
# requires Cloud NAT (a Cloud Router + Cloud NAT gateway), which this module
# deliberately does not create (out of scope per this module's task: it
# creates network/subnetwork/firewall resources only). This has no effect on
# .github/workflows/qualify.yml's own real-cloud test, which never depends
# on the guest reaching GitHub (its Bootstrap fixture is a synthetic,
# non-functional token; see gcp_realcloud_test.go's header). It does mean
# tools/e2e/bring-up-gcp.sh's runner cannot actually register with a real
# GitHub instance against this network until Cloud NAT is separately
# provisioned - tracked as a gap for whoever wires up that harness's GCP
# credentials/network, not silently assumed to work.

# Narrower than the AWS/Azure siblings' own qualify-network egress
# reasoning, which leaves all outbound ports open specifically because a
# guest OS needs more than 443 (package repos, time sync, DNS) and
# wrongly narrowing that would turn a network bug into a mysterious
# runner-registration failure. This module accepts that same risk for GCP
# on the strength of the "known limitation" note above: this network has
# no route to the internet at all yet (no Cloud NAT), so this rule
# doesn't currently gate anything real - if/when Cloud NAT is wired up
# for tools/e2e/bring-up-gcp.sh, revisit whether 443-only still holds for
# a real guest boot (apt/NTP), the same way AWS/Azure already do.
resource "google_compute_firewall" "allow_egress_https" {
  project     = var.project_id
  name        = "runners-allow-egress-https"
  description = "runnerscout qualification-network - allow the runner's only real outbound need (HTTPS/443) before deny_egress_all below closes everything else"
  network     = google_compute_network.qualify.id
  direction   = "EGRESS"
  priority    = 900 # Lower than deny_egress_all below, so this specific allow is matched first for port 443.

  destination_ranges = ["0.0.0.0/0"]

  allow {
    protocol = "tcp"
    ports    = ["443"]
  }
}

resource "google_compute_firewall" "deny_egress_all" {
  project     = var.project_id
  name        = "runners-deny-egress-all"
  description = "runnerscout qualification-network - narrow the implied allow-all-egress default down to only what allow_egress_https permits above"
  network     = google_compute_network.qualify.id
  direction   = "EGRESS"
  priority    = 65534 # Just ahead of the implied allow-egress rule (65535, uneditable) - catches every port this network doesn't explicitly allow above.

  destination_ranges = ["0.0.0.0/0"]

  deny {
    protocol = "all"
  }
}
