#!/usr/bin/env bash
# Independent post-run verification: confirms via kubectl that the fleet
# ConfigMap shows the allocation reached Deleted (or was fully pruned, which
# only happens after Deleted+Released - see internal/operator/operator.go's
# delete(f.Pending, id)), AND, through a second EC2 client construction
# path, that no billable EC2 resource tagged to this scale set survives -
# the exact "independent client, not the adapter's own session" pattern
# aws_realcloud_test.go's newIndependentEC2Client and qualify-aws.yml's own
# "Independent post-run cleanup safety net" step both already use. This
# script never trusts the controller's own Observe()/Delete() success
# reports; it only trusts what kubectl and the AWS CLI themselves report.
#
# Safe to run standalone, any number of times, against a cluster that is
# still up - it only ever reads. tools/e2e/teardown.sh calls this before
# force-cleaning anything, then again after, to report before/after state.
#
# Required environment:
#   E2E_NAMESPACE, E2E_SCALE_SET_NAME, E2E_AWS_REGION
# Optional:
#   E2E_EVIDENCE_DIR    directory to write verify-<timestamp>.json
#
# Exit code is 0 only when zero leftover billable AWS resources are found
# for this scale set's owner tag (see internal/provider/aws.go's
# "runnerscout-owner" tag, whose value is the RunnerScaleSet's own name -
# see internal/operator.Config.Validate's "provider owner must match
# controller name" check for why that equality always holds). A cluster
# that was never brought up, or a RunnerScaleSet that never created an
# allocation, both still exit 0 - "never existed" and "existed and was
# cleaned up" are the same successful outcome for this check, exactly like
# qualify-aws.yml's own safety-net step treats "credentials were never
# configured" as nothing-to-verify rather than a failure.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
e2e_require_var E2E_SCALE_SET_NAME
e2e_require_var E2E_AWS_REGION

fleet_cm="${E2E_SCALE_SET_NAME}-fleet"
owner="$E2E_SCALE_SET_NAME"

e2e_log "kubectl-side check: fleet ConfigMap $fleet_cm allocation state"
fleet="$(kubectl -n "$E2E_NAMESPACE" get configmap "$fleet_cm" -o jsonpath='{.data.fleet}' 2>/dev/null || echo '{}')"
pending_summary="$(jq -c '.pending // {} | to_entries | map({id: .key, phase: .value.phase})' <<<"$fleet")"
e2e_log "pending allocations: $pending_summary"
unclean="$(jq -e '[.pending // {} | to_entries[] | select(.value.phase != "deleted")] | length' <<<"$fleet")"

e2e_log "AWS-side independent check: leftover instances/volumes/network-interfaces tagged runnerscout-owner=$owner"
# Each query below checks its own exit status explicitly (`if ! var=$(...)`)
# rather than a bare `var=$(...)` under set -e or a `2>/dev/null || true`
# fallback - either of those would make a real API/auth/network failure
# indistinguishable from "queried successfully, found nothing." An empty
# result must only ever mean the latter. Conflating the two would let a
# broken AWS CLI session report a false "confirmed clean" - see
# docs/e2e-qualification.background.md for the incident this guards against
# (found during this harness's own smoke testing: a bare `$(...)` inside
# teardown.sh's own fallback re-check swallowed a NoCredentials error into
# an empty, apparently-clean result).
# shellcheck disable=SC2016  # backticks are JMESPath literal syntax, not shell substitution - see qualify-aws.yml's identical use of this pattern.
if ! instances="$(aws ec2 describe-instances --region "$E2E_AWS_REGION" \
  --filters "Name=tag:runnerscout-owner,Values=$owner" \
  --query 'Reservations[].Instances[?State.Name!=`terminated`].InstanceId' --output text 2>&1)"; then
  e2e_die "AWS API call failed while verifying cleanup (describe-instances) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $instances"
fi
# shellcheck disable=SC2016
if ! volumes="$(aws ec2 describe-volumes --region "$E2E_AWS_REGION" \
  --filters "Name=tag:runnerscout-owner,Values=$owner" \
  --query 'Volumes[?State!=`deleted`].VolumeId' --output text 2>&1)"; then
  e2e_die "AWS API call failed while verifying cleanup (describe-volumes) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $volumes"
fi
if ! enis="$(aws ec2 describe-network-interfaces --region "$E2E_AWS_REGION" \
  --filters "Name=tag:runnerscout-owner,Values=$owner" \
  --query 'NetworkInterfaces[].NetworkInterfaceId' --output text 2>&1)"; then
  e2e_die "AWS API call failed while verifying cleanup (describe-network-interfaces) - this is NOT confirmed-clean, treat as an active leftover-resource risk: $enis"
fi

leftover=0
if [ -n "$instances" ]; then
  e2e_log "::warning:: leftover instance(s): $instances"
  leftover=1
fi
if [ -n "$volumes" ]; then
  e2e_log "::warning:: leftover volume(s): $volumes"
  leftover=1
fi
if [ -n "$enis" ]; then
  e2e_log "::warning:: leftover network interface(s): $enis"
  leftover=1
fi

if [ -n "${E2E_EVIDENCE_DIR:-}" ]; then
  mkdir -p "$E2E_EVIDENCE_DIR"
  jq -n --argjson pending "$pending_summary" --arg instances "$instances" --arg volumes "$volumes" \
    --arg enis "$enis" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{at: $at, pendingAllocations: $pending, leftoverInstances: $instances, leftoverVolumes: $volumes, leftoverNetworkInterfaces: $enis}' \
    > "$E2E_EVIDENCE_DIR/verify-$(date -u +%Y%m%dT%H%M%SZ).json"
fi

if [ "$leftover" -eq 1 ]; then
  e2e_die "independent AWS inventory found leftover billable resources for owner=$owner in $E2E_AWS_REGION - see warnings above"
fi
if [ "$unclean" -gt 0 ]; then
  e2e_log "::warning:: $unclean allocation(s) in the fleet ConfigMap have not reached phase=deleted yet (this alone is not a leftover-billable-resource failure if AWS-side inventory above is clean, but should not persist)"
fi
e2e_log "independent verification: zero leftover billable AWS resources for owner=$owner"
