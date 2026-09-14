#!/usr/bin/env bash
# Dispatch one real workflow_dispatch job against the target test repository
# and poll both GitHub's own run status and this cluster's fleet ConfigMap
# until the job completes, the allocation is cleaned up, or the hard
# E2E_MAX_RUNTIME_MINUTES ceiling elapses - whichever comes first.
#
# Run this only after tools/e2e/bring-up-gcp.sh reports its RunnerScaleSet
# Ready. This script uses the operator's own `gh` session against
# E2E_TARGET_REPO - deliberately NOT this repository's gh-tsouza wrapper
# (see AGENTS.md): gh-tsouza is scoped to this coordinating session's own
# operations against tsouza/runnerscout, while E2E_TARGET_REPO is an
# operator-chosen, unrelated test repository whose credentials/account this
# harness has no opinion about. Run `gh auth status` yourself first.
#
# Identical GitHub dispatch/poll logic to tools/e2e/dispatch-and-wait.sh (the
# AWS equivalent) - that part of the harness is genuinely cloud-agnostic.
# The only GCP-specific piece is the CapacityCatalog refresh immediately
# below: it re-validates the pinned GCP identifiers (no real price
# observation exists to refresh - see manifests/capacity-catalog-gcp.yaml.tmpl)
# and re-renders/re-applies the catalog with a fresh observedAt timestamp.
#
# Required environment (see docs/e2e-qualification.md):
#   E2E_CONFIRM_REAL_SPEND, E2E_MAX_RUNTIME_MINUTES   tools/e2e/lib.sh gates
#   E2E_NAMESPACE, E2E_SCALE_SET_NAME                 must match bring-up-gcp.sh
#   E2E_GCP_PROJECT, E2E_GCP_REGION, E2E_GCP_ZONE, E2E_GCP_NETWORK,
#   E2E_GCP_SUBNETWORK, E2E_GCP_IMAGE, E2E_GCP_MACHINE_TYPE   for the catalog refresh
#   E2E_TARGET_REPO      owner/repo containing the workflow to dispatch
#   E2E_TARGET_WORKFLOW  workflow file name or id, e.g. e2e-trivial.yml
# Optional:
#   E2E_TARGET_REF=main
#   E2E_EVIDENCE_DIR    directory to write dispatch-run.json
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

e2e_require_confirmation
e2e_require_ceiling
E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
E2E_TARGET_REF="${E2E_TARGET_REF:-main}"
export E2E_NAMESPACE
e2e_default_gcp_catalog_vars
# Same full set of resource-graph inputs bring-up-gcp.sh requires for the
# CapacityCatalog it renders - this script re-renders that same manifest
# (see manifests/capacity-catalog-gcp.yaml.tmpl's header comment for why) as
# a separate process, so it needs every variable that manifest references,
# not only the ones this script's own GCP cross-validation logic touches.
for v in E2E_SCALE_SET_NAME E2E_GCP_PROJECT E2E_GCP_REGION E2E_GCP_ZONE \
         E2E_GCP_NETWORK E2E_GCP_SUBNETWORK E2E_GCP_IMAGE E2E_GCP_MACHINE_TYPE \
         E2E_TARGET_REPO E2E_TARGET_WORKFLOW; do
  e2e_require_var "$v"
done

deadline="$(e2e_deadline_epoch)"
fleet_cm="${E2E_SCALE_SET_NAME}-fleet"

fleet_json() {
  kubectl -n "$E2E_NAMESPACE" get configmap "$fleet_cm" -o jsonpath='{.data.fleet}' 2>/dev/null || echo '{}'
}

e2e_log "re-validating pinned GCP identifiers and refreshing the CapacityCatalog's observedAt before dispatch (price stays static, see manifests/capacity-catalog-gcp.yaml.tmpl)"
e2e_refresh_gcp_catalog_inputs
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
e2e_render capacity-catalog-gcp.yaml.tmpl "$work_dir/capacity-catalog-gcp.yaml"
kubectl apply -n "$E2E_NAMESPACE" -f "$work_dir/capacity-catalog-gcp.yaml"

before_pending="$(fleet_json | jq -r '.pending // {} | keys | sort | join(",")')"
dispatch_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
# shellcheck disable=SC2153  # E2E_TARGET_REPO is a real, required env var (e2e_require_var above) - not a typo of E2E_TARGET_REF.
e2e_log "dispatching $E2E_TARGET_WORKFLOW on $E2E_TARGET_REPO@$E2E_TARGET_REF"
gh workflow run "$E2E_TARGET_WORKFLOW" --repo "$E2E_TARGET_REPO" --ref "$E2E_TARGET_REF"

e2e_log "locating the run GitHub just created"
run_id=""
while [ -z "$run_id" ]; do
  run_id="$(gh run list --repo "$E2E_TARGET_REPO" --workflow "$E2E_TARGET_WORKFLOW" --limit 5 \
    --json databaseId,createdAt --jq "map(select(.createdAt >= \"$dispatch_time\")) | sort_by(.createdAt) | .[0].databaseId // empty")"
  if [ -z "$run_id" ]; then
    [ "$(date +%s)" -lt "$deadline" ] || e2e_die "no run appeared for $E2E_TARGET_WORKFLOW within E2E_MAX_RUNTIME_MINUTES=$E2E_MAX_RUNTIME_MINUTES minutes"
    sleep 5
  fi
done
run_url="$(gh run view "$run_id" --repo "$E2E_TARGET_REPO" --json url --jq .url)"
e2e_log "tracking run $run_id: $run_url"

allocation_id=""
run_status=""
run_conclusion=""
while true; do
  read -r run_status run_conclusion < <(gh run view "$run_id" --repo "$E2E_TARGET_REPO" --json status,conclusion --jq '[.status, (.conclusion // "")] | @tsv')

  if [ -z "$allocation_id" ]; then
    after_pending="$(fleet_json | jq -r '.pending // {} | keys | sort | join(",")')"
    allocation_id="$(comm -13 <(tr ',' '\n' <<<"$before_pending" | sort -u) <(tr ',' '\n' <<<"$after_pending" | sort -u) | head -n1)"
    if [ -n "$allocation_id" ]; then
      e2e_log "observed new allocation $allocation_id in the fleet ConfigMap"
    fi
  fi
  if [ -n "$allocation_id" ]; then
    phase="$(fleet_json | jq -r --arg id "$allocation_id" '.pending[$id].phase // "deleted-or-pruned"')"
    e2e_log "run=$run_status allocation=$allocation_id phase=$phase"
  else
    e2e_log "run=$run_status (no allocation observed yet)"
  fi

  if [ "$run_status" = "completed" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    e2e_die "run $run_id did not complete within E2E_MAX_RUNTIME_MINUTES=$E2E_MAX_RUNTIME_MINUTES minutes (last status=$run_status)"
  fi
  sleep 10
done

e2e_log "run $run_id completed with conclusion=$run_conclusion"

if [ -n "$allocation_id" ]; then
  e2e_log "waiting for allocation $allocation_id to reach deleted/pruned state"
  while true; do
    phase="$(fleet_json | jq -r --arg id "$allocation_id" '.pending[$id].phase // "deleted-or-pruned"')"
    if [ "$phase" = "deleted-or-pruned" ] || [ "$phase" = "deleted" ]; then
      break
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      e2e_log "::warning:: allocation $allocation_id still in phase=$phase at the runtime ceiling - tools/e2e/verify-gcp.sh and tools/e2e/teardown-gcp.sh will still independently confirm/force cloud-side cleanup"
      break
    fi
    sleep 5
  done
fi

if [ -n "${E2E_EVIDENCE_DIR:-}" ]; then
  mkdir -p "$E2E_EVIDENCE_DIR"
  jq -n --arg run_id "$run_id" --arg run_url "$run_url" --arg conclusion "$run_conclusion" \
    --arg allocation_id "${allocation_id:-}" --arg dispatch_time "$dispatch_time" \
    '{runID: $run_id, runURL: $run_url, conclusion: $conclusion, allocationID: $allocation_id, dispatchedAt: $dispatch_time}' \
    > "$E2E_EVIDENCE_DIR/dispatch-run.json"
fi

[ "$run_conclusion" = "success" ] || e2e_die "run $run_id concluded '$run_conclusion', not 'success' - see $run_url"
e2e_log "dispatch-and-wait complete: run $run_id succeeded"
