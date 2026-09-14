#!/usr/bin/env bash
# Independent post-run verification: confirms via kubectl that the fleet
# ConfigMap shows the allocation reached Deleted (or was fully pruned, which
# only happens after Deleted+Released - see internal/operator/operator.go's
# delete(f.Pending, id)), AND, through a second, independent Azure client
# construction path (the `az` CLI, authenticated however the operator's own
# shell/session resolves it - see docs/e2e-qualification.background.md's
# Azure section), that no billable Azure resource tagged to this scale
# set's owner survives. This is the tag-based sweep pattern
# qualify-azure.yml's own "Independent post-run cleanup safety net" step and
# azure_realcloud_test.go's azureIndependentLeftovers both already use -
# reused here, not reinvented, per this piece's own scoping instructions.
#
# Unlike the AWS piece's three separately-typed EC2 describe calls
# (instances/volumes/network-interfaces), one generic `az resource list`
# tag query already returns every resource type in the resource group
# (VM, NIC, disk, or anything else a future template might add) - matching
# how qualify-azure.yml's own safety net and azureIndependentLeftovers both
# do exactly one tag-based `az resource list`/`az resource show` sweep
# rather than one call per resource kind.
#
# This script never trusts the controller's own Observe()/Delete() success
# reports; it only trusts what kubectl and the `az` CLI themselves report.
#
# Safe to run standalone, any number of times, against a cluster that is
# still up - it only ever reads. tools/e2e/teardown-azure.sh calls this
# before force-cleaning anything, then again after, to report before/after
# state.
#
# Required environment:
#   E2E_NAMESPACE, E2E_SCALE_SET_NAME, E2E_AZURE_RESOURCE_GROUP
# Optional:
#   E2E_EVIDENCE_DIR    directory to write verify-<timestamp>.json
#
# Exit code is 0 only when zero leftover billable Azure resources are found
# for this scale set's owner tag (see internal/provider/azure.go's
# "runnerscout-owner" tag, whose value is the RunnerScaleSet's own name -
# see internal/operator.Config.Validate's "provider owner must match
# controller name" check for why that equality always holds). A cluster
# that was never brought up, or a RunnerScaleSet that never created an
# allocation, both still exit 0 - "never existed" and "existed and was
# cleaned up" are the same successful outcome for this check, exactly like
# qualify-azure.yml's own safety-net step treats "credentials were never
# configured" as nothing-to-verify rather than a failure.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
e2e_require_var E2E_SCALE_SET_NAME
e2e_require_var E2E_AZURE_RESOURCE_GROUP

fleet_cm="${E2E_SCALE_SET_NAME}-fleet"
owner="$E2E_SCALE_SET_NAME"

e2e_log "kubectl-side check: fleet ConfigMap $fleet_cm allocation state"
fleet="$(kubectl -n "$E2E_NAMESPACE" get configmap "$fleet_cm" -o jsonpath='{.data.fleet}' 2>/dev/null || echo '{}')"
pending_summary="$(jq -c '.pending // {} | to_entries | map({id: .key, phase: .value.phase})' <<<"$fleet")"
e2e_log "pending allocations: $pending_summary"
unclean="$(jq -e '[.pending // {} | to_entries[] | select(.value.phase != "deleted")] | length' <<<"$fleet")"

e2e_log "Azure-side independent check: leftover resources tagged runnerscout-owner=$owner in resource group $E2E_AZURE_RESOURCE_GROUP"
# Explicit exit-status check (`if ! var=$(...)`), never a bare `var=$(...)`
# under set -e or a `2>/dev/null || true` fallback - either of those would
# make a real API/auth/network failure indistinguishable from "queried
# successfully, found nothing." An empty result must only ever mean the
# latter. This is the exact same class of bug this harness's AWS piece
# found and fixed in its own verify.sh/teardown.sh during local smoke
# testing (see docs/e2e-qualification.background.md) - applied here
# proactively rather than re-discovered.
if ! leftover_ids="$(az resource list --resource-group "$E2E_AZURE_RESOURCE_GROUP" --tag runnerscout-owner="$owner" --query '[].id' -o tsv 2>&1)"; then
  e2e_die "az CLI call failed while verifying cleanup (resource list) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $leftover_ids"
fi

leftover=0
if [ -n "$leftover_ids" ]; then
  e2e_log "::warning:: leftover Azure resource(s): $leftover_ids"
  leftover=1
fi

if [ -n "${E2E_EVIDENCE_DIR:-}" ]; then
  mkdir -p "$E2E_EVIDENCE_DIR"
  jq -n --argjson pending "$pending_summary" --arg leftover "$leftover_ids" \
    --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{at: $at, pendingAllocations: $pending, leftoverAzureResources: $leftover}' \
    > "$E2E_EVIDENCE_DIR/verify-$(date -u +%Y%m%dT%H%M%SZ).json"
fi

if [ "$leftover" -eq 1 ]; then
  e2e_die "independent Azure inventory found leftover billable resources for owner=$owner in resource group $E2E_AZURE_RESOURCE_GROUP - see warnings above"
fi
if [ "$unclean" -gt 0 ]; then
  e2e_log "::warning:: $unclean allocation(s) in the fleet ConfigMap have not reached phase=deleted yet (this alone is not a leftover-billable-resource failure if Azure-side inventory above is clean, but should not persist)"
fi
e2e_log "independent verification: zero leftover billable Azure resources for owner=$owner"
