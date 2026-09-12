# Dependency refresh — 2026-09-12

The current candidate targets Go 1.27.1, Kubernetes libraries v0.37.0,
kind v0.33.0, Kubernetes v1.37.0 and Helm v3.22.0. Go build/test imports were
refreshed with `go get -u -t ./...` and `go mod tidy`; `go.mod` and `go.sum`
record exact versions. A build rejected the latest kube-openapi before the
compatible pin was restored. No failed attempt has been discarded.

The core Kubernetes modules move together. kube-openapi remains at the upstream
Kubernetes v0.37.0 dependency, `v0.0.0-20260721132016-d427ff9ee9ad`: the newer
September revision returns merge-diff v7 schema types, incompatible with the v6
API expected by apimachinery v0.37.0. This is a concrete build constraint.
Helm uses the newest maintained 3.x release; Helm 4.3 is a separate major-version
qualification target because its post-renderer plugin interface differs.

GitHub Actions use current release commits: checkout v7.0.1, setup-go v7.0.0,
upload-artifact v7.0.1, CodeQL v4.38.0, setup-buildx v4.3.0 and build-push v7.3.0.
All are pinned by full commit ID. The latest actions/scaleset is still v0.4.0.
PyYAML is current at 6.0.3, Trivy at 0.74.0 and govulncheck at 1.8.0. The old govulncheck 1.1.4 crashed parsing Go 1.27 syntax; 1.8.0 completed with no vulnerabilities found.

Registry checks found the existing AWS CLI, GCloud stable and Python 3.13
runtime digests still current for their selected channels. Azure CLI 2.90.0 is
the latest release. A fresh official pip resolution on the pinned Python base
returned the same 149 distributions as the hash-locked requirements. The Python
runtime remains on its supported 3.13 line; 3.14 compatibility is not qualified.
Azure CLI's MSAL pin constrains cryptography below available security fixes.
Do not force unsupported replacements or describe the image as security accepted.
The image scan status remains separately recorded in runtime-security.json.

Go caching is explicit in every workflow job. Buildx caches intermediate runtime
layers across runs. See [CI cache behavior](../docs/ci.md). GitHub's cache API
already showed populated main-branch and PR Go caches before this change;
new-toolchain cache restoration must be observed in fresh CI logs.

The update is a development candidate. Previous Helm and image evidence belongs
to its recorded source/tool versions. Fresh checks must pass before merging it;
no dependency refresh establishes real GitHub-to-cloud-VM qualification.
