# Multicloud runner class

This example connects all six CRDs: three ProviderConfigs, a RunnerClass,
RunnerScaleSet, CapacityCatalog, NetworkProfile and CapacityBudget. `runs-on:
build` selects the GitHub scale set; RunnerScout chooses eligible AWS, Azure
or GCP capacity, bounded by `budget.yaml`'s $5.00/day ceiling - see
[capacity-budget.md](../../docs/capacity-budget.md).

Replace account/project IDs, network IDs, SSH public key, image IDs, GitHub URL,
App identity and scale-set ID. The GitHub scale set must be named `build` and have
no other controller. Images must supply Linux amd64 and the declared Docker
capability. The catalog contains **illustrative expired prices**, not cloud quotes;
all enumeration flags are false and admission is suspended.

Create the namespace and schemas, then supply existing credentials through files:

```sh
kubectl create namespace runnerscout
kubectl apply --server-side -f charts/runnerscout/crds/
kubectl -n runnerscout create secret generic github --from-file=privateKey=./github-app.pem
kubectl -n runnerscout create secret generic aws-credentials \
  --from-file=credentials=./aws-credentials \
  --from-literal=path=/etc/runnerscout/providers/aws-credentials/credentials
kubectl -n runnerscout create secret generic gcp-credentials \
  --from-file=credentials.json=./gcp-credentials.json \
  --from-literal=path=/etc/runnerscout/providers/gcp-credentials/credentials.json
kubectl -n runnerscout create secret generic azure-identity \
  --from-literal=client-id=REPLACE_WITH_CLIENT_ID \
  --from-literal=tenant-id=REPLACE_WITH_TENANT_ID \
  --from-literal=token-file=/var/run/secrets/azure/tokens/azure-identity-token
```

The AWS file needs a `[runnerscout]` profile. The GCP file may use an approved
external-account configuration or service-account credential. Azure uses a
federated identity: configure the workload-identity webhook and federation for
the chart's ServiceAccount, and set the same client ID in `values.yaml`. The
webhook supplies the projected token; this example does not create federation.
File references contain mounted paths; private credential contents stay in Secrets.
For EKS IRSA or GKE workload identity, use the platform's ServiceAccount binding
and remove that provider's explicit credential references and file mount.

```sh
kubectl apply -k examples/multicloud
helm upgrade --install runnerscout charts/runnerscout --namespace runnerscout \
  --values examples/multicloud/values.yaml
helm test runnerscout --namespace runnerscout --timeout 2m
```

Set an actual development image in `values.yaml`. The suspended controller is
intentionally unready, so this initial install omits `--wait`. `helm test` checks
the real CRD/Secret snapshot through Kubernetes; it does not authenticate to GitHub
or provision a VM.

Before enabling admission, populate a fresh, finite, authoritative price snapshot,
verify image capabilities and private-network egress, and allocate a cloud budget.
Mark a provider complete only when its allowed pools were fully enumerated; never
refresh an illustrative quote by changing only its timestamp. Prices are USD
microdollars per compute hour; the example limit is $0.10/hour before other costs.
Set `spec.suspend: false` on `build`, then wait for its `Ready` condition. To permit
global on-demand fallback, explicitly set the class's `placement.allowOnDemand`
to true; it still requires definitive exhaustion of every eligible spot pool.

Retries remain disabled and this example stays in `separate` networking mode -
not because either is unimplemented (both are functionally complete, see
[architecture.md](../../docs/architecture.md) and
[networking-peer-model.md](../../docs/networking-peer-model.md)), but because
of this example's own configuration: enabling retries requires PAT
authentication (this example uses App auth), and a wireguard-mode
`NetworkProfile` accepts exactly one `NetworkMapping` (this example maps all
three clouds). `network.yaml` maps existing private networks in each cloud;
it does not join them or create gateways. The [workflow](../workflow.yml) exercises
multiple dependent jobs, cached Go dependencies and a Docker build on this class.

Delete the RunnerScaleSet and wait for cleanup **before** uninstalling Helm:

```sh
kubectl -n runnerscout delete runnerscaleset build --timeout=10m
helm uninstall runnerscout --namespace runnerscout --timeout 2m
```

The uninstall hook refuses unresolved ownership or cleanup. Keep the controller
and required cloud credentials available until deletion completes. Helm retains
CRD schemas and durable state; deleting those records is not a cleanup mechanism.
