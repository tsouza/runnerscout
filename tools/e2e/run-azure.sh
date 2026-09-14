#!/usr/bin/env bash
# Top-level orchestrator for the full real Azure end-to-end qualification
# (issue #80): bring-up -> dispatch-and-wait -> guaranteed teardown, with
# teardown always attempted via `trap ... EXIT`, exactly mirroring
# qualify-azure.yml's `if: always()` final step and tools/e2e/run.sh's (AWS)
# identical orchestration shape - the local-runbook equivalent of a
# workflow-level guarantee, since no GitHub Actions runtime is enforcing
# this for us here. See docs/e2e-qualification.md for the full harness, its
# safety bounds, and every environment variable each stage reads; see
# docs/e2e-qualification.background.md for why this harness is a local
# runbook rather than a GitHub Actions workflow.
#
# This script never registers a scale set itself - run
# tools/e2e/register-scale-set.sh first (cloud-agnostic, shared with the
# AWS piece - see cmd/e2e-register-scale-set's own header comment) and
# export its printed ID as E2E_REGISTERED_SCALE_SET_ID. Keeping registration
# a separate, explicit step means a single `run-azure.sh` invocation can
# never silently create a new GitHub-side scale set as a side effect of an
# unrelated retry.
#
# Usage: tools/e2e/run-azure.sh
# All configuration is environment variables - see tools/e2e/env.example,
# copy it to an untracked file (e.g. tools/e2e/.env, already .gitignore'd)
# and `source` it before running this script.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

e2e_require_confirmation
e2e_require_ceiling
e2e_require_var E2E_REGISTERED_SCALE_SET_ID

E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
E2E_CLUSTER_NAME="${E2E_CLUSTER_NAME:-runnerscout-e2e}"
export E2E_NAMESPACE E2E_CLUSTER_NAME

teardown_ran=0
run_teardown() {
  if [ "$teardown_ran" -eq 1 ]; then
    return 0
  fi
  teardown_ran=1
  e2e_log "running guaranteed teardown (trap on exit, status so far: $1)"
  # set +e around teardown: a non-zero exit from teardown-azure.sh must not
  # short-circuit the EXIT trap itself, and must not be silently swallowed
  # either - captured and reported below.
  set +e
  tools/e2e/teardown-azure.sh
  teardown_status=$?
  set -e
  if [ "$teardown_status" -ne 0 ]; then
    e2e_log "::error:: teardown reported failures (exit $teardown_status) - see its own output above; this requires human follow-up"
  fi
  return "$teardown_status"
}
trap 'run_teardown "trap on exit $?"' EXIT

e2e_log "=== stage 1/2: bring-up ==="
tools/e2e/bring-up-azure.sh

e2e_log "=== stage 2/2: dispatch and wait ==="
tools/e2e/dispatch-and-wait-azure.sh

e2e_log "=== full run succeeded; guaranteed teardown follows via EXIT trap ==="
