# Runtime image

`make image` builds `runnerscout:development`. `make image-test` runs it without
network access as user 10001, with a read-only filesystem, dropped capabilities
and bounded CPU, memory and temporary storage. Checks exercise the controller
parser, identity, filesystem permissions and TLS trust store, and reject cloud
CLIs and Python. The test probe is mounted temporarily; it is not shipped.
Evidence stays under ignored `evidence/`.

AWS, Azure and GCP use native Go SDKs pinned in `go.mod` and `go.sum`. The build
and distroless runtime images are digest-pinned. No shell, Python interpreter,
cloud CLI or provider emulator is included. Helm qualification uses a separate
pinned Python image for its local GitHub protocol fixture.

The chart exposes `/healthz` and `/readyz`; readiness requires a working scale-set
session and reconciliation loop. A passing container check does not qualify live
GitHub-to-VM execution, cloud credentials, image vulnerability acceptance or
multiarchitecture execution. See [release requirements](releases.md).
