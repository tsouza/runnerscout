#!/usr/bin/env bash
# Register (or, with --delete, remove) one real GitHub Actions runner scale
# set via cmd/e2e-register-scale-set - see that program's own header comment
# for why registration lives in a separate binary from cmd/runnerscout.
#
# Idempotent-safe: cmd/e2e-register-scale-set always looks up the named scale
# set before creating or deleting one, so running this script twice with the
# same E2E_SCALE_SET_NAME never creates a duplicate and never errors on an
# already-absent name.
#
# Required environment (see docs/e2e-qualification.md for the full harness):
#   E2E_CONFIRM_REAL_SPEND   exact phrase, see tools/e2e/lib.sh
#   E2E_GITHUB_URL           https://github.com/<owner>/<repo-or-org>
#   E2E_SCALE_SET_NAME       scale set name == runs-on label
# Exactly one auth mode:
#   E2E_GITHUB_TOKEN_FILE                                     (PAT), or
#   E2E_GITHUB_APP_CLIENT_ID + E2E_GITHUB_APP_INSTALLATION_ID
#     + E2E_GITHUB_APP_KEY_FILE                                (GitHub App)
# Optional:
#   E2E_RUNNER_GROUP          GitHub runner group name (default "Default")
#   E2E_EVIDENCE_DIR          directory to write registration-evidence.json
#
# Usage: tools/e2e/register-scale-set.sh [--delete]
# On success (register mode), the scale set's numeric ID is printed alone on
# stdout - capture it with SCALE_SET_ID=$(tools/e2e/register-scale-set.sh).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

e2e_require_confirmation
e2e_require_var E2E_GITHUB_URL
e2e_require_var E2E_SCALE_SET_NAME

args=(
  -github-url "$E2E_GITHUB_URL"
  -name "$E2E_SCALE_SET_NAME"
  -runner-group "${E2E_RUNNER_GROUP:-Default}"
)

if [ -n "${E2E_GITHUB_TOKEN_FILE:-}" ]; then
  args+=(-github-token-file "$E2E_GITHUB_TOKEN_FILE")
elif [ -n "${E2E_GITHUB_APP_CLIENT_ID:-}" ]; then
  e2e_require_var E2E_GITHUB_APP_INSTALLATION_ID
  e2e_require_var E2E_GITHUB_APP_KEY_FILE
  args+=(
    -github-app-client-id "$E2E_GITHUB_APP_CLIENT_ID"
    -github-app-installation-id "$E2E_GITHUB_APP_INSTALLATION_ID"
    -github-app-key-file "$E2E_GITHUB_APP_KEY_FILE"
  )
else
  e2e_die "set either E2E_GITHUB_TOKEN_FILE or the E2E_GITHUB_APP_* trio"
fi

if [ "${1:-}" = "--delete" ]; then
  args+=(-delete)
fi

if [ -n "${E2E_EVIDENCE_DIR:-}" ]; then
  mkdir -p "$E2E_EVIDENCE_DIR"
  args+=(-evidence-file "$E2E_EVIDENCE_DIR/registration-evidence.json")
fi

e2e_log "resolving/registering scale set '$E2E_SCALE_SET_NAME' at $E2E_GITHUB_URL"
go run ./cmd/e2e-register-scale-set "${args[@]}"
