# Runtime image

`make image` builds `runnerscout:development`. `make image-test` runs it without
network access as a non-root user, with a read-only filesystem, dropped
capabilities and bounded CPU, memory and temporary storage. Checks exercise the
controller parser, AWS CLI and Google Cloud CLI and confirm the Azure Python
authentication stack is absent. Evidence is retained under ignored `evidence/`.

Azure uses Microsoft's Go identity, deployments and resource-management SDKs.
Authentication uses a configured federated workload identity, environment
credential or managed identity. It never invokes Azure CLI or falls back to a
developer login. GitHub and cloud credentials are external to the image.

Go, Python, AWS CLI and Google Cloud CLI base stages are digest-pinned. The image
uses system Python for gcloud and removes gcloud's bundled interpreter. Debian
updates are resolved at build time; a reproducible release requires an immutable
package snapshot. SDK versions and checksums are locked in `go.mod` and `go.sum`.

`/healthz` reports process liveness. `/readyz` requires leadership, a scale-set
session and successful latest reconciliation. These endpoints do not promise VM
capacity or establish successful GitHub job execution.

A runtime smoke test or emulator pass does not qualify live cloud behavior.
Release acceptance requires the complete architecture/provider matrix and fresh
image vulnerability results; dependency removal alone is not security acceptance.
