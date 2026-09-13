# Runtime image

`make image` builds `runnerscout:development`. `make image-test` runs it without
network access as a non-root user, with a read-only filesystem, dropped
capabilities and bounded CPU, memory and temporary storage. Checks exercise the
controller parser and AWS CLI and verify that Azure/GCP Python libraries and
the Google Cloud CLI are absent. Evidence stays under ignored `evidence/`.

Azure and GCP use native Go SDKs pinned in `go.mod` and `go.sum`. AWS uses a
digest-pinned CLI with isolated credential files and caches. The build and runtime
base images are digest-pinned; Debian security updates are installed at build time.
No provider emulator is included in the controller image.

The chart exposes `/healthz` and `/readyz`; readiness requires a working scale-set
session and reconciliation loop. A passing container check does not qualify live
GitHub-to-VM execution, cloud credentials, image vulnerability acceptance or
multiarchitecture execution. See [release requirements](releases.md).
