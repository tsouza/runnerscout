# Development CI and caches

Every Go job in CI and CodeQL uses checksum-pinned `actions/setup-go` with
`cache: true` and `cache-dependency-path: go.sum`. It restores and saves both
`GOMODCACHE` and `GOCACHE` across runs. The action includes operating system,
architecture, Go version and the dependency-file hash in its cache key. Changed
Go versions or dependencies therefore receive a new exact key. A hit on that
immutable key cannot save newly compiled objects.

The shared `go-build-cache` action additionally restores compiler output by OS,
architecture, resolved Go version and dependency hash, with a commit-specific
save key. Only a successful verifier job saves this cache; the other Go jobs
restore it. Tests still run: `make verify` uses uncached test execution and checks
candidate identity. Cache contents never substitute for acceptance evidence.

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

Look for setup-go and compiler-cache restore/save messages and Buildx cached steps to
verify actual hits. Cache misses must remain correct. Dependency updates are
weekly: Kubernetes modules are grouped, CI actions are grouped, and Docker base
images have their own update lane. Cloud provider SDKs are Go modules; dependency
updates must remain compatible and pass the existing checks.

Kubernetes modules move together. Keep kube-openapi compatible with the
structured-merge-diff major version required by apimachinery; updating it alone
to a different schema type breaks builds. Helm uses the maintained 3.x line
until the Helm 4 post-renderer interface is qualified. Dependency updates must
pass the existing build, behavior and security checks.

Required tests are identified by package and name. Integration checks retain Go
JSON events and reject missing, skipped or unfinished required tests even when
the Go process exits successfully. CI uploads integration evidence on failures
as well as successes.

Keep Kubernetes libraries, OpenAPI and structured-merge-diff compatible as a set.
Kubernetes 0.37 uses structured-merge-diff/v6; OpenAPI revisions requiring v7
cannot be substituted until Kubernetes supports them. Required CI validates
dependency updates before merge.

BuildKit retains Go compiler output within a reused builder. The Actions layer
cache retains the module-download layer; compiler cache mounts are local to the
builder and are not exported by that layer cache.

Local API qualification uses pinned Moto with explicit `client-token` discovery and
new-interface tagging extensions. Tests cover matching, unrelated and missing
tokens, and refusal after ownership tags change. The manifest records
both the image digest and extension hash. Ministack checks unsupported-image refusal;
Floci Azure exercises its VM REST API. These tests do not boot cloud guests or qualify
live execution. Ownership and cleanup requirements remain enforced by the adapter.
