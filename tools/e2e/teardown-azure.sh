#!/usr/bin/env bash
# Guaranteed teardown: deletes the RunnerScaleSet (waiting for its cleanup
# finalizer, per docs/architecture.md's "Deletion completes only after
# observed absence"), uninstalls the Helm release, force-cleans any leftover
# Azure resource tagged to this scale set, deletes the k3d cluster, and
# exits non-zero if independent Azure-side verification still finds
# anything left - the Azure sibling of tools/e2e/teardown.sh (AWS), and
# mirroring qualify-azure.yml's own "Independent post-run cleanup safety
# net" step almost verbatim for the Azure-specific half (delete VMs first,
# then whatever remains - Azure's deleteAzure removes one dependent
# resource per call, unlike AWS's cascading terminate-instances, see
# docs/qualification-real-cloud.background.md's Azure section), which
# treats a leftover-after-force-clean as a real bug to surface loudly,
# never a normal outcome.
#
# This script is written to be run under `trap teardown EXIT` by
# tools/e2e/run-azure.sh (see that script), so every step here tolerates
# missing state: a cluster that was never created, a RunnerScaleSet that
# never admitted, credentials that were never configured. Each of those is
# a legitimate "nothing to tear down here" outcome, not a script bug -
# exactly like qualify-azure.yml's own final step treats unset Azure
# credentials as nothing to verify or clean up, not a failure.
#
# Required environment:
#   E2E_NAMESPACE, E2E_SCALE_SET_NAME, E2E_AZURE_RESOURCE_GROUP, E2E_CLUSTER_NAME
# Optional:
#   E2E_EVIDENCE_DIR
#   E2E_KEEP_CLUSTER=  (set non-empty to leave the k3d cluster running for
#     post-mortem inspection - Azure-side force-cleanup and verification
#     still run regardless; only the cluster deletion step is skipped)
set -uo pipefail  # deliberately not -e: every step below must still attempt
                  # to run even if an earlier one fails - see the header
                  # comment above. Failures are tracked in $failed and
                  # surfaced at the end instead.
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh
set +e  # lib.sh sets -e for scripts that want it; this script deliberately
        # does not (see the header comment above) - restated here, after the
        # source, so lib.sh's own "set -euo pipefail" does not silently win.

E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
E2E_CLUSTER_NAME="${E2E_CLUSTER_NAME:-runnerscout-e2e}"
e2e_require_var E2E_SCALE_SET_NAME
e2e_require_var E2E_AZURE_RESOURCE_GROUP

failed=0
owner="$E2E_SCALE_SET_NAME"

cluster_exists() {
  k3d cluster list -o json 2>/dev/null | jq -e --arg n "$E2E_CLUSTER_NAME" 'any(.[]; .name == $n)' >/dev/null 2>&1
}

if cluster_exists; then
  kubectl config use-context "k3d-$E2E_CLUSTER_NAME" >/dev/null 2>&1 || true

  e2e_log "deleting RunnerScaleSet $E2E_SCALE_SET_NAME and waiting for its cleanup finalizer"
  if kubectl -n "$E2E_NAMESPACE" get runnerscaleset "$E2E_SCALE_SET_NAME" >/dev/null 2>&1; then
    if ! kubectl -n "$E2E_NAMESPACE" delete runnerscaleset "$E2E_SCALE_SET_NAME" --timeout=10m; then
      e2e_log "::warning:: RunnerScaleSet deletion/finalizer wait did not complete cleanly - continuing to force-clean Azure resources regardless"
      failed=1
    fi
  else
    e2e_log "RunnerScaleSet $E2E_SCALE_SET_NAME already absent"
  fi

  e2e_log "uninstalling the runnerscout Helm release"
  helm uninstall runnerscout --namespace "$E2E_NAMESPACE" --timeout 2m 2>&1 | while read -r line; do e2e_log "$line"; done
else
  e2e_log "k3d cluster $E2E_CLUSTER_NAME not present - skipping Kubernetes-side teardown"
fi

e2e_log "Azure-side independent force-cleanup for owner=$owner in resource group $E2E_AZURE_RESOURCE_GROUP"
if az account show >/dev/null 2>&1; then
  # Best-effort discovery (`2>/dev/null || true`): this is the "attempt to
  # clean" pass, not the authoritative gate - see the final independent
  # verification below, which does NOT tolerate a query failure. The two
  # blocks have different jobs: this one may be silent on failure, the
  # final one must never be - same discipline as tools/e2e/teardown.sh's
  # (AWS) identical split, and the same bug class its own background doc
  # describes finding and fixing.
  vm_ids="$(az resource list --resource-group "$E2E_AZURE_RESOURCE_GROUP" --tag runnerscout-owner="$owner" --resource-type Microsoft.Compute/virtualMachines --query '[].id' -o tsv 2>/dev/null || true)"
  if [ -n "$vm_ids" ]; then
    e2e_log "::warning:: force-deleting leftover VM(s) first (Azure's deleteAzure order: VM, then NIC, then disk): $vm_ids"
    for id in $vm_ids; do
      az resource delete --ids "$id" --verbose || failed=1
    done
  fi
  remaining_ids="$(az resource list --resource-group "$E2E_AZURE_RESOURCE_GROUP" --tag runnerscout-owner="$owner" --query '[].id' -o tsv 2>/dev/null || true)"
  if [ -n "$remaining_ids" ]; then
    e2e_log "::warning:: force-deleting remaining leftover resource(s): $remaining_ids"
    for id in $remaining_ids; do
      az resource delete --ids "$id" --verbose || failed=1
    done
  fi
else
  e2e_log "no authenticated az CLI session - skipping Azure-side force-cleanup (nothing was ever created without one)"
fi

if [ -z "${E2E_KEEP_CLUSTER:-}" ] && cluster_exists; then
  e2e_log "deleting k3d cluster $E2E_CLUSTER_NAME"
  k3d cluster delete "$E2E_CLUSTER_NAME" || failed=1
elif [ -n "${E2E_KEEP_CLUSTER:-}" ]; then
  e2e_log "E2E_KEEP_CLUSTER set: leaving k3d cluster $E2E_CLUSTER_NAME running for inspection"
fi

e2e_log "final independent Azure-side verification"
if ! E2E_AZURE_RESOURCE_GROUP="$E2E_AZURE_RESOURCE_GROUP" E2E_SCALE_SET_NAME="$E2E_SCALE_SET_NAME" E2E_NAMESPACE="$E2E_NAMESPACE" \
     E2E_EVIDENCE_DIR="${E2E_EVIDENCE_DIR:-}" tools/e2e/verify-azure.sh; then
  # verify-azure.sh's own combined exit code also reflects the (now
  # expected) missing ConfigMap once the cluster is already deleted above,
  # so it cannot be trusted directly here - re-query Azure ourselves.
  # Critically, this re-query must NOT swallow a real API/auth/network
  # failure into an empty "nothing found" result - see
  # tools/e2e/teardown.sh's (AWS) identical re-check and
  # docs/e2e-qualification.background.md for the real bug this exact
  # pattern already caught once in this harness's own AWS piece. "Could not
  # verify" and "confirmed clean" must never look the same here either.
  if ! az account show >/dev/null 2>&1; then
    e2e_log "no authenticated az CLI session for final re-verification either - nothing further to independently confirm"
  elif ! remaining="$(az resource list --resource-group "$E2E_AZURE_RESOURCE_GROUP" --tag runnerscout-owner="$owner" --query '[].id' -o tsv 2>&1)"; then
    e2e_log "::error:: could not independently re-verify cleanup - the az CLI call itself failed (see below), which is NOT the same as confirmed-clean: $remaining"
    failed=1
  elif [ -n "$remaining" ]; then
    e2e_log "::error:: leftover Azure resource(s) survived force-cleanup: $remaining - this indicates a real bug, not a normal outcome"
    failed=1
  fi
fi

if [ "$failed" -ne 0 ]; then
  e2e_die "teardown completed with one or more failures - see warnings/errors above; do not treat this run as clean"
fi
e2e_log "teardown complete: cluster and scale-set resources torn down, zero leftover billable Azure resources confirmed"
