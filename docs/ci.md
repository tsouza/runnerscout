# Development CI and caches

Every Go job in CI and CodeQL uses checksum-pinned `actions/setup-go` with
`cache: true` and `cache-dependency-path: go.sum`. It restores and saves both
`GOMODCACHE` and `GOCACHE` across runs. The action includes operating system,
architecture, Go version and the dependency-file hash in its cache key. Changed
Go versions or dependencies therefore receive a new exact key. Tests still run;
`make verify` uses uncached test execution and checks candidate identity.

GitHub cache branch rules apply: PR runs can restore eligible base-branch caches,
but their saved caches are scoped to their merge reference. Main is populated by
successful push runs. There are no cloud credentials in these caches.

The runtime build uses Buildx and the GitHub Actions cache backend with the
`runnerscout-runtime-amd64` scope and `mode=max`. This preserves intermediate
layers, including `go mod download`, between workflow runs. The Dockerfile copies
`go.mod` and `go.sum` before application source so source changes retain the
module-download layer. The resulting image is loaded locally for smoke and Helm
tests; this job does not publish or deploy an image. Host Go caches and Docker
layers are separate caches.

Look for setup-go restore/save messages and Buildx cached steps in job logs to
verify actual hits. Cache misses must remain correct. Dependency updates are
weekly: Kubernetes modules are grouped, CI actions are grouped, and Docker base
images have their own update lane. Azure's complete Python lock is refreshed with
the official resolver and distribution hashes; incompatible forced overrides are
not used to conceal vulnerabilities.

Kubernetes modules move together. Keep kube-openapi compatible with the
structured-merge-diff major version required by apimachinery; updating it alone
to a different schema type breaks builds. Helm uses the maintained 3.x line
until the Helm 4 post-renderer interface is qualified. Dependency updates must
pass the existing build, behavior and security checks.

Required tests are identified by package and name. Integration checks retain Go
JSON events and reject missing, skipped or unfinished required tests even when
the Go process exits successfully. CI uploads integration evidence on failures
as well as successes.
