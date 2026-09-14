#!/usr/bin/env bash
# Bring up a local k3d cluster running the real runnerscout controller,
# wired to one real Azure ProviderConfig/RunnerClass/RunnerScaleSet/
# CapacityCatalog/NetworkProfile resource graph - the Azure sibling of
# tools/e2e/bring-up.sh (AWS), modeled closely on examples/multicloud,
# Azure-only, real network identifiers as inputs, and a real,
# already-registered GitHub scale set ID as an input (never hardcoded). See
# docs/e2e-qualification.md for the full harness this is one stage of, and
# cross-check every safety property claimed in comments below against
# tools/e2e/lib.sh, which this script sources for its two mandatory gates.
#
# This script itself never dispatches a GitHub workflow and never registers
# a scale set - see tools/e2e/register-scale-set.sh (run this first,
# cloud-agnostic, shared with the AWS piece) and
# tools/e2e/dispatch-and-wait-azure.sh (run this after). Splitting bring-up
# from dispatch keeps this script idempotent-safe to re-run (helm upgrade
# --install, kubectl apply are all convergent) without ever re-registering
# or re-dispatching anything by accident.
#
# Azure-specific pre-flight (network/identifier cross-validation, Workload
# Identity Federation credentials, one-resource-per-call teardown
# elsewhere) follows the exact pattern qualify-azure.yml/
# docs/qualification-real-cloud.md's Azure section already established and
# reviewed - see tools/e2e/lib.sh's e2e_refresh_azure_catalog_inputs for the
# pre-flight itself.
#
# Required environment (see docs/e2e-qualification.md):
#   E2E_CONFIRM_REAL_SPEND, E2E_MAX_RUNTIME_MINUTES   tools/e2e/lib.sh gates
#   E2E_GITHUB_URL, E2E_SCALE_SET_NAME, E2E_REGISTERED_SCALE_SET_ID
#   E2E_GITHUB_AUTH_MODE                              pat|app
#     pat:  E2E_GITHUB_TOKEN_FILE
#     app:  E2E_GITHUB_APP_CLIENT_ID, E2E_GITHUB_APP_INSTALLATION_ID,
#           E2E_GITHUB_APP_KEY_FILE
#   E2E_AZURE_REGION, E2E_AZURE_SUBSCRIPTION_ID, E2E_AZURE_RESOURCE_GROUP,
#   E2E_AZURE_VNET_ID, E2E_AZURE_SUBNET_ID, E2E_AZURE_NSG_ID,
#   E2E_AZURE_IMAGE_ID, E2E_AZURE_VM_SIZE, E2E_AZURE_ZONE,
#   E2E_AZURE_ARCHITECTURE
#   Azure Workload Identity Federation only (see docs/qualification-real-cloud.md's
#   Azure section for why there is no client-secret fallback here):
#     E2E_AZURE_CLIENT_ID, E2E_AZURE_TENANT_ID, E2E_AZURE_FEDERATED_TOKEN_FILE
# Optional (defaults shown):
#   E2E_NAMESPACE=runnerscout  E2E_CLUSTER_NAME=runnerscout-e2e
#   E2E_IMAGE=runnerscout:e2e  E2E_AZURE_OFFERING_CPU=2
#   E2E_AZURE_OFFERING_MEMORY_MIB=4096  E2E_PROVISIONING_SECONDS=300
#   E2E_MAX_LIFETIME_SECONDS=1800  E2E_AZURE_MAX_PRICE_MICROS=200000
#   E2E_SKIP_IMAGE_BUILD=  (set non-empty to reuse an already-built/imported
#     E2E_IMAGE instead of building from source - used by the local
#     mechanics smoke test, which never needs real Azure/GitHub
#     credentials or an authenticated `az` CLI session)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/e2e/lib.sh
source tools/e2e/lib.sh

e2e_require_confirmation
e2e_require_ceiling

E2E_NAMESPACE="${E2E_NAMESPACE:-runnerscout}"
E2E_CLUSTER_NAME="${E2E_CLUSTER_NAME:-runnerscout-e2e}"
E2E_IMAGE="${E2E_IMAGE:-runnerscout:e2e}"
export E2E_NAMESPACE
e2e_default_catalog_vars

for v in E2E_GITHUB_URL E2E_SCALE_SET_NAME E2E_REGISTERED_SCALE_SET_ID E2E_GITHUB_AUTH_MODE \
         E2E_AZURE_REGION E2E_AZURE_SUBSCRIPTION_ID E2E_AZURE_RESOURCE_GROUP E2E_AZURE_VNET_ID \
         E2E_AZURE_SUBNET_ID E2E_AZURE_NSG_ID E2E_AZURE_IMAGE_ID E2E_AZURE_VM_SIZE \
         E2E_AZURE_ZONE E2E_AZURE_ARCHITECTURE; do
  e2e_require_var "$v"
done
case "$E2E_GITHUB_AUTH_MODE" in
  pat) e2e_require_var E2E_GITHUB_TOKEN_FILE ;;
  app) e2e_require_var E2E_GITHUB_APP_CLIENT_ID; e2e_require_var E2E_GITHUB_APP_INSTALLATION_ID; e2e_require_var E2E_GITHUB_APP_KEY_FILE ;;
  *) e2e_die "E2E_GITHUB_AUTH_MODE must be 'pat' or 'app'" ;;
esac
if [ -z "${E2E_SKIP_IMAGE_BUILD:-}" ]; then
  e2e_require_var E2E_AZURE_CLIENT_ID
  e2e_require_var E2E_AZURE_TENANT_ID
  e2e_require_var E2E_AZURE_FEDERATED_TOKEN_FILE
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

render() {
  e2e_render "$1" "$work_dir/$1"
}

# A fresh, ephemeral SSH key pair, generated unconditionally (real run or
# mechanics smoke test) and discarded the moment its public half is
# encoded. internal/provider.Command.Validate requires
# Config.SSHPublicKey to start with "ssh-" for every Azure deployment
# (azure.go's linuxConfiguration), but nothing in this harness (a synthetic,
# non-functional Bootstrap JIT token placeholder, exactly like every other
# qualification piece) ever logs into the VM over SSH - see
# docs/e2e-qualification.background.md's Azure section for why generating a
# throwaway key here is strictly less operational surface than asking an
# operator to provision, store and rotate a real one for a value nothing
# downstream ever verifies.
e2e_log "generating a fresh, throwaway SSH key pair for the Azure ProviderConfig"
ssh-keygen -t ed25519 -N '' -C 'runnerscout-e2e-azure' -f "$work_dir/e2e-azure-ssh" -q
export E2E_AZURE_SSH_PUBLIC_KEY
E2E_AZURE_SSH_PUBLIC_KEY="$(cat "$work_dir/e2e-azure-ssh.pub")"
rm -f "$work_dir/e2e-azure-ssh"

e2e_log "cross-validating pinned Azure identifiers (read-only)"
if [ -z "${E2E_SKIP_IMAGE_BUILD:-}" ]; then
  e2e_refresh_azure_catalog_inputs
else
  e2e_log "E2E_SKIP_IMAGE_BUILD set: mechanics smoke test mode, skipping real Azure calls; using placeholder catalog values"
  export E2E_AZURE_SUBNET_CIDR="10.98.0.0/24"
  export E2E_AZURE_PRICE_MICROS="10000"
  observed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  export E2E_AZURE_PRICE_OBSERVED_AT="$observed_at"
fi

if ! k3d cluster list -o json | jq -e --arg n "$E2E_CLUSTER_NAME" 'any(.[]; .name == $n)' >/dev/null; then
  e2e_log "creating k3d cluster $E2E_CLUSTER_NAME"
  # charts/runnerscout/Chart.yaml pins kubeVersion: ">=1.37.0-0 <1.38.0-0" -
  # k3d's own default k3s image trails that (see docs/operations.md's
  # kindest/node:v1.37.0 pin for the same reason, for kind-based
  # integration testing). E2E_K3D_IMAGE lets an operator point at a
  # rancher/k3s tag that actually satisfies the chart's floor; left unset,
  # k3d's own default is used and helm install fails loudly with an
  # unambiguous kubeVersion mismatch rather than silently proceeding on an
  # incompatible cluster.
  create_args=("$E2E_CLUSTER_NAME" --wait)
  if [ -n "${E2E_K3D_IMAGE:-}" ]; then
    create_args+=(--image "$E2E_K3D_IMAGE")
  fi
  k3d cluster create "${create_args[@]}"
else
  e2e_log "reusing existing k3d cluster $E2E_CLUSTER_NAME"
fi
kubectl config use-context "k3d-$E2E_CLUSTER_NAME" >/dev/null

if [ -z "${E2E_SKIP_IMAGE_BUILD:-}" ]; then
  e2e_log "building controller image $E2E_IMAGE"
  docker build --tag "$E2E_IMAGE" .
fi
e2e_log "importing $E2E_IMAGE into k3d cluster $E2E_CLUSTER_NAME"
k3d image import "$E2E_IMAGE" -c "$E2E_CLUSTER_NAME"

e2e_log "installing CRDs"
kubectl apply --server-side -f charts/runnerscout/crds/

kubectl create namespace "$E2E_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

e2e_log "creating GitHub and Azure credential Secrets"
case "$E2E_GITHUB_AUTH_MODE" in
  pat)
    kubectl -n "$E2E_NAMESPACE" create secret generic github --from-file=token="$E2E_GITHUB_TOKEN_FILE" --dry-run=client -o yaml | kubectl apply -f -
    ;;
  app)
    kubectl -n "$E2E_NAMESPACE" create secret generic github --from-file=privateKey="$E2E_GITHUB_APP_KEY_FILE" --dry-run=client -o yaml | kubectl apply -f -
    ;;
esac
# azure-identity carries the exact three keys
# provider-config-azure.yaml.tmpl's credentialEnvironment references:
# client-id/tenant-id (plain literal values) and token-file (the in-pod path
# where the fourth key, "token" - the actual federated JWT content, mounted
# via credentialSecrets - will appear once the whole Secret is mounted as a
# volume, exactly mirroring how aws-credentials' "path"/"credentials" pair
# works for the AWS piece).
if [ -n "${E2E_SKIP_IMAGE_BUILD:-}" ] && { [ -z "${E2E_AZURE_CLIENT_ID:-}" ] || [ -z "${E2E_AZURE_TENANT_ID:-}" ] || [ -z "${E2E_AZURE_FEDERATED_TOKEN_FILE:-}" ]; }; then
  E2E_AZURE_CLIENT_ID="${E2E_AZURE_CLIENT_ID:-00000000-0000-0000-0000-000000000000}"
  E2E_AZURE_TENANT_ID="${E2E_AZURE_TENANT_ID:-00000000-0000-0000-0000-000000000000}"
  echo 'placeholder-federated-token' > "$work_dir/azure-token-placeholder"
  E2E_AZURE_FEDERATED_TOKEN_FILE="$work_dir/azure-token-placeholder"
fi
kubectl -n "$E2E_NAMESPACE" create secret generic azure-identity \
  --from-literal=client-id="$E2E_AZURE_CLIENT_ID" \
  --from-literal=tenant-id="$E2E_AZURE_TENANT_ID" \
  --from-file=token="$E2E_AZURE_FEDERATED_TOKEN_FILE" \
  --from-literal=token-file=/etc/runnerscout/providers/azure-identity/token \
  --dry-run=client -o yaml | kubectl apply -f -

e2e_log "rendering and applying the resource graph"
render provider-config-azure.yaml.tmpl
render network-profile-azure.yaml.tmpl
render capacity-catalog-azure.yaml.tmpl
render runner-class-azure.yaml.tmpl
render "runner-scaleset-azure-${E2E_GITHUB_AUTH_MODE}.yaml.tmpl"
kubectl apply -n "$E2E_NAMESPACE" \
  -f "$work_dir/provider-config-azure.yaml.tmpl" \
  -f "$work_dir/network-profile-azure.yaml.tmpl" \
  -f "$work_dir/capacity-catalog-azure.yaml.tmpl" \
  -f "$work_dir/runner-class-azure.yaml.tmpl" \
  -f "$work_dir/runner-scaleset-azure-${E2E_GITHUB_AUTH_MODE}.yaml.tmpl"

e2e_log "installing/upgrading the runnerscout Helm chart (CRD-driven mode)"
helm upgrade --install runnerscout charts/runnerscout --namespace "$E2E_NAMESPACE" \
  --set image.repository="${E2E_IMAGE%%:*}" \
  --set image.tag="${E2E_IMAGE#*:}" \
  --set crd.scaleSetName="$E2E_SCALE_SET_NAME" \
  --set crd.secretNames[0]=github \
  --set crd.secretNames[1]=azure-identity \
  --set credentialSecrets[0].name=azure-identity

if [ -n "${E2E_SKIP_IMAGE_BUILD:-}" ]; then
  e2e_log "mechanics smoke test: cluster/CRDs/chart installed; not waiting for a real Ready condition (no real GitHub/Azure credentials configured)"
  exit 0
fi

e2e_log "waiting for RunnerScaleSet $E2E_SCALE_SET_NAME to become Ready"
deadline="$(e2e_deadline_epoch)"
while true; do
  status="$(kubectl -n "$E2E_NAMESPACE" get runnerscaleset "$E2E_SCALE_SET_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  if [ "$status" = "True" ]; then
    e2e_log "RunnerScaleSet $E2E_SCALE_SET_NAME is Ready"
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    kubectl -n "$E2E_NAMESPACE" get runnerscaleset "$E2E_SCALE_SET_NAME" -o yaml >&2 || true
    e2e_die "RunnerScaleSet $E2E_SCALE_SET_NAME did not become Ready within E2E_MAX_RUNTIME_MINUTES=$E2E_MAX_RUNTIME_MINUTES minutes"
  fi
  sleep 5
done
e2e_log "bring-up complete: cluster=$E2E_CLUSTER_NAME namespace=$E2E_NAMESPACE scale-set=$E2E_SCALE_SET_NAME"
