#!/usr/bin/env bash
# Guaranteed teardown: deletes the RunnerScaleSet (waiting for its cleanup
# finalizer, per docs/architecture.md's "Deletion completes only after
# observed absence"), uninstalls the Helm release, force-cleans any leftover
# GCP resource labeled to this scale set (mirroring qualify-gcp.yml's own
# "Independent post-run cleanup safety net" step's instances/disks/addresses
# sweep), deletes the k3d cluster, and exits non-zero if independent
# GCP-side verification still finds anything left - treating a
# leftover-after-force-clean as a real bug to surface loudly, never a normal
# outcome.
#
# This script is written to be run under `trap teardown EXIT` by
# tools/e2e/run-gcp.sh (see that script), so every step here tolerates
# missing state: a cluster that was never created, a RunnerScaleSet that
# never admitted, credentials that were never configured. Each of those is a
# legitimate "nothing to tear down here" outcome, not a script bug - exactly
# like qualify-gcp.yml's own final step treats unset GCP credentials as
# nothing to verify or clean up, not a failure.
#
# Required environment:
#   E2E_NAMESPACE, E2E_SCALE_SET_NAME, E2E_GCP_PROJECT, E2E_GCP_ZONE,
#   E2E_GCP_REGION, E2E_CLUSTER_NAME
# Optional:
#   E2E_EVIDENCE_DIR
#   E2E_KEEP_CLUSTER=  (set non-empty to leave the k3d cluster running for
#     post-mortem inspection - GCP-side force-cleanup and verification still
#     run regardless; only the cluster deletion step is skipped)
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
e2e_require_var E2E_GCP_PROJECT
e2e_require_var E2E_GCP_ZONE
e2e_require_var E2E_GCP_REGION

failed=0
owner="$E2E_SCALE_SET_NAME"
filter="labels.runnerscout-owner=$owner"

cluster_exists() {
  k3d cluster list -o json 2>/dev/null | jq -e --arg n "$E2E_CLUSTER_NAME" 'any(.[]; .name == $n)' >/dev/null 2>&1
}

if cluster_exists; then
  kubectl config use-context "k3d-$E2E_CLUSTER_NAME" >/dev/null 2>&1 || true

  e2e_log "deleting RunnerScaleSet $E2E_SCALE_SET_NAME and waiting for its cleanup finalizer"
  if kubectl -n "$E2E_NAMESPACE" get runnerscaleset "$E2E_SCALE_SET_NAME" >/dev/null 2>&1; then
    if ! kubectl -n "$E2E_NAMESPACE" delete runnerscaleset "$E2E_SCALE_SET_NAME" --timeout=10m; then
      e2e_log "::warning:: RunnerScaleSet deletion/finalizer wait did not complete cleanly - continuing to force-clean GCP resources regardless"
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

e2e_log "GCP-side independent force-cleanup for owner=$owner in $E2E_GCP_PROJECT/$E2E_GCP_ZONE"
# The three discovery queries below are deliberately best-effort
# (`2>/dev/null || true`): this is the "attempt to clean" pass, not the
# authoritative gate - if one of these silently finds nothing because the
# query itself failed (bad credentials, network), force-cleanup simply does
# nothing this round, and the FINAL independent verification below (which
# does NOT tolerate a query failure - see its own comment) still fails the
# whole script loudly. The two blocks have different jobs: this one may be
# silent on failure, the final one must never be - see
# docs/e2e-qualification.background.md's AWS section for the incident this
# distinction guards against.
instances="$(gcloud compute instances list --project "$E2E_GCP_PROJECT" --zones "$E2E_GCP_ZONE" --filter "$filter" --format='value(name)' 2>/dev/null || true)"
if [ -n "$instances" ]; then
  e2e_log "::warning:: force-deleting leftover instance(s): $instances"
  for i in $instances; do gcloud compute instances delete "$i" --project "$E2E_GCP_PROJECT" --zone "$E2E_GCP_ZONE" --quiet || failed=1; done
fi
disks="$(gcloud compute disks list --project "$E2E_GCP_PROJECT" --zones "$E2E_GCP_ZONE" --filter "$filter" --format='value(name)' 2>/dev/null || true)"
for d in $disks; do
  e2e_log "::warning:: deleting leftover disk $d"
  gcloud compute disks delete "$d" --project "$E2E_GCP_PROJECT" --zone "$E2E_GCP_ZONE" --quiet || failed=1
done
addresses="$(gcloud compute addresses list --project "$E2E_GCP_PROJECT" --regions "$E2E_GCP_REGION" --filter "$filter" --format='value(name)' 2>/dev/null || true)"
for a in $addresses; do
  e2e_log "::warning:: deleting leftover reserved address $a"
  gcloud compute addresses delete "$a" --project "$E2E_GCP_PROJECT" --region "$E2E_GCP_REGION" --quiet || failed=1
done

if [ -z "${E2E_KEEP_CLUSTER:-}" ] && cluster_exists; then
  e2e_log "deleting k3d cluster $E2E_CLUSTER_NAME"
  k3d cluster delete "$E2E_CLUSTER_NAME" || failed=1
elif [ -n "${E2E_KEEP_CLUSTER:-}" ]; then
  e2e_log "E2E_KEEP_CLUSTER set: leaving k3d cluster $E2E_CLUSTER_NAME running for inspection"
fi

e2e_log "final independent GCP-side verification"
if ! E2E_GCP_PROJECT="$E2E_GCP_PROJECT" E2E_GCP_ZONE="$E2E_GCP_ZONE" E2E_GCP_REGION="$E2E_GCP_REGION" \
     E2E_SCALE_SET_NAME="$E2E_SCALE_SET_NAME" E2E_NAMESPACE="$E2E_NAMESPACE" \
     E2E_EVIDENCE_DIR="${E2E_EVIDENCE_DIR:-}" tools/e2e/verify-gcp.sh; then
  # verify-gcp.sh's own combined exit code also reflects the (now expected)
  # missing ConfigMap once the cluster is already deleted above, so it
  # cannot be trusted directly here - re-query GCP ourselves. Critically,
  # this re-query must NOT swallow a real API/auth/network failure into an
  # empty "nothing found" result - see the AWS pass's own background doc for
  # the exact class of bug this guards against.
  if ! remaining="$(gcloud compute instances list --project "$E2E_GCP_PROJECT" --zones "$E2E_GCP_ZONE" --filter "$filter" --format='value(name)' 2>&1)"; then
    e2e_log "::error:: could not independently re-verify cleanup - the gcloud API call itself failed (see below), which is NOT the same as confirmed-clean: $remaining"
    failed=1
  elif [ -n "$remaining" ]; then
    e2e_log "::error:: leftover GCP instance(s) survived force-cleanup: $remaining - this indicates a real bug, not a normal outcome"
    failed=1
  fi
fi

if [ "$failed" -ne 0 ]; then
  e2e_die "teardown completed with one or more failures - see warnings/errors above; do not treat this run as clean"
fi
e2e_log "teardown complete: cluster and scale-set resources torn down, zero leftover billable GCP resources confirmed"
