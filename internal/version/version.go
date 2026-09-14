// Package version holds this binary's build-time version string.
package version

// Version identifies this build to GitHub's Actions Runner Scale Set API
// (via scaleset.SystemInfo) and anywhere else this binary reports itself.
// Overridden at build time via
// `-ldflags "-X github.com/tsouza/runnerscout/internal/version.Version=vX.Y.Z"`
// (see Dockerfile's VERSION build arg). The zero-value default identifies
// an unversioned build - a local `go build`/`docker build` with no
// VERSION arg, never a real tagged release.
var Version = "development"
