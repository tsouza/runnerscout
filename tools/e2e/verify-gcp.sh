#!/usr/bin/env bash
# Independent post-run verification: confirms via kubectl that the fleet
# ConfigMap shows the allocation reached Deleted (or was fully pruned, which
# only happens after Deleted+Released - see internal/operator/operator.go's
# delete(f.Pending, id)), AND, through a second Compute Engine client
# construction path (the operator's own ambient `gcloud`/credential session,
# never internal/provider/gcp_credentials.go's newGCPSDK), that no billable
# GCE resource tagged to this scale set survives - the exact
# "independent client, not the adapter's own session" pattern
# qualify-gcp.yml's own "Independent post-run cleanup safety net" step
# already uses. This script never trusts the controller's own
# Observe()/Delete() success reports; it only trusts what kubectl and the
# gcloud CLI themselves report.
#
# Filters on labels.runnerscout-owner only (never also
# labels.runnerscout-operation): internal/provider/gcp_sdk.go's gcpLabels
# sets runnerscout-owner to the RunnerScaleSet's own name (see
# internal/operator.Config.Validate's "provider owner must match controller
# name" invariant, the same reasoning tools/e2e/verify.sh's AWS equivalent
# documents for its own tag:runnerscout-owner-only filter) - unlike
# qualify-gcp.yml's own safety-net step, this harness has no single
# well-known allocation id to also filter on, since the controller assigns
# allocation ids dynamically as real placement happens.
#
# Safe to run standalone, any number of times, against a cluster that is
# still up - it only ever reads. tools/e2e/teardown-gcp.sh calls this before
# force-cleaning anything, then again after, to report before/after state.
#
# Required environment:
#   E2E_NAMESPACE, E2E_SCALE_SET_NAME, E2E_GCP_PROJECT, E2E_GCP_ZONE, E2E_GCP_REGION
# Optional:
#   E2E_EVIDENCE_DIR    directory to write verify-<timestamp>.json
#
# Exit code is 0 only when zero leftover billable GCE resources are found
# for this scale set's owner label. A cluster that was never brought up, or
# a RunnerScaleSet that never created an allocation, both still exit 0 -
# "never existed" and "existed and was cleaned up" are the same successful
# outcome for this check, exactly like qualify-gcp.yml's own safety-net step
# treats "credentials were never configured" as nothing-to-verify rather
# than a failure.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
e2e_require_var E2E_SCALE_SET_NAME
e2e_require_var E2E_GCP_PROJECT
e2e_require_var E2E_GCP_ZONE
e2e_require_var E2E_GCP_REGION

fleet_cm="${E2E_SCALE_SET_NAME}-fleet"
owner="$E2E_SCALE_SET_NAME"
filter="labels.runnerscout-owner=$owner"

e2e_log "kubectl-side check: fleet ConfigMap $fleet_cm allocation state"
fleet="$(kubectl -n "$E2E_NAMESPACE" get configmap "$fleet_cm" -o jsonpath='{.data.fleet}' 2>/dev/null || echo '{}')"
pending_summary="$(jq -c '.pending // {} | to_entries | map({id: .key, phase: .value.phase})' <<<"$fleet")"
e2e_log "pending allocations: $pending_summary"
unclean="$(jq -e '[.pending // {} | to_entries[] | select(.value.phase != "deleted")] | length' <<<"$fleet")"

e2e_log "GCP-side independent check: leftover instances/disks/addresses labeled runnerscout-owner=$owner"
# Each query below checks its own exit status explicitly (`if ! var=$(...)`)
# rather than a bare `var=$(...)` under set -e or a `2>/dev/null || true`
# fallback - either would make a real API/auth/network failure
# indistinguishable from "queried successfully, found nothing." An empty
# result must only ever mean the latter. Conflating the two would let a
# broken gcloud CLI session report a false "confirmed clean" - see
# docs/e2e-qualification.background.md's AWS section for the incident this
# guards against (found during that pass's own smoke testing: a bare
# `$(...)` inside its teardown script's own fallback re-check swallowed a
# credentials error into an empty, apparently-clean result). The same
# discipline is applied here from the start, not discovered by repeating
# that mistake a second time.
if ! instances="$(gcloud compute instances list --project "$E2E_GCP_PROJECT" --zones "$E2E_GCP_ZONE" --filter "$filter" --format='value(name)' 2>&1)"; then
  e2e_die "gcloud API call failed while verifying cleanup (instances list) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $instances"
fi
if ! disks="$(gcloud compute disks list --project "$E2E_GCP_PROJECT" --zones "$E2E_GCP_ZONE" --filter "$filter" --format='value(name)' 2>&1)"; then
  e2e_die "gcloud API call failed while verifying cleanup (disks list) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $disks"
fi
if ! addresses="$(gcloud compute addresses list --project "$E2E_GCP_PROJECT" --regions "$E2E_GCP_REGION" --filter "$filter" --format='value(name)' 2>&1)"; then
  e2e_die "gcloud API call failed while verifying cleanup (addresses list) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $addresses"
fi

leftover=0
if [ -n "$instances" ]; then
  e2e_log "::warning:: leftover instance(s): $instances"
  leftover=1
fi
if [ -n "$disks" ]; then
  e2e_log "::warning:: leftover disk(s): $disks"
  leftover=1
fi
if [ -n "$addresses" ]; then
  e2e_log "::warning:: leftover reserved address(es): $addresses"
  leftover=1
fi

if [ -n "${E2E_EVIDENCE_DIR:-}" ]; then
  mkdir -p "$E2E_EVIDENCE_DIR"
  jq -n --argjson pending "$pending_summary" --arg instances "$instances" --arg disks "$disks" \
    --arg addresses "$addresses" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{at: $at, pendingAllocations: $pending, leftoverInstances: $instances, leftoverDisks: $disks, leftoverAddresses: $addresses}' \
    > "$E2E_EVIDENCE_DIR/verify-$(date -u +%Y%m%dT%H%M%SZ).json"
fi

if [ "$leftover" -eq 1 ]; then
  e2e_die "independent GCP inventory found leftover billable resources for owner=$owner in $E2E_GCP_PROJECT/$E2E_GCP_ZONE - see warnings above"
fi
if [ "$unclean" -gt 0 ]; then
  e2e_log "::warning:: $unclean allocation(s) in the fleet ConfigMap have not reached phase=deleted yet (this alone is not a leftover-billable-resource failure if GCP-side inventory above is clean, but should not persist)"
fi
e2e_log "independent verification: zero leftover billable GCP resources for owner=$owner"
