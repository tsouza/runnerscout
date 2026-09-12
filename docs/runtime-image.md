# Runtime image qualification

`make image` builds `runnerscout:development`. `make image-test` runs the image
without networking under a non-root UID, read-only root filesystem, dropped
capabilities, no-new-privileges and bounded memory/CPU/tmpfs. It checks the real
controller configuration parser and all three cloud CLIs. Raw output and image
identity are retained under ignored evidence directories.

Go, Python, AWS CLI and Google Cloud CLI base stages are digest-pinned. Azure CLI
2.90.0 and 148 transitive Python distributions are version/hash-locked. Debian
security updates are currently resolved at build time; an immutable package
snapshot is still required for a fully reproducible release image. The current
AWS runtime stage uses AWS CLI 2.36.44; the independent Ministack emulator lane
retains its previously qualified CLI version.

The image uses system Python for Google Cloud CLI and removes its unused bundled
interpreter and dependencies. Azure and gcloud write configuration only under
/tmp by default; named provider Secret paths or workload identity remain operator
configuration. Telemetry is disabled. No credentials are built into the image.

`/healthz` reports process liveness. `/readyz` requires leadership, a scale-set
session and a successful latest reconciliation. Session failure, failed
reconciliation and leader exit clear readiness. These endpoints neither promise
available VM capacity nor prove a successful GitHub job.

Initial amd64 image execution passed. Arm64 execution, real Helm lifecycle tests
and image vulnerability acceptance remain pending. The first Trivy 0.74.0 scan
found known vulnerabilities; its evidence is retained, not suppressed. Supported
OS/AWS/Go fixes passed a rebuild and all five restricted-container checks. The
fresh scan found zero critical and 48 high findings; image security acceptance
remains open. Azure CLI 2.90.0 pins MSAL 1.36.0, which requires
cryptography <49; current fixes require cryptography 49/50. The dependency
constraints must be resolved and authenticated Azure behavior verified before
claiming release acceptance. Do not force-install incompatible packages merely
to obtain a green scan.
