# syntax=docker/dockerfile:1
# The binary is built by goreleaser (see .goreleaser.yml's builds: section
# and its own comment for why), never by this file - $TARGETPLATFORM (e.g.
# "linux/amd64") is a BuildKit-populated global build arg; goreleaser's
# dockers_v2 stages the binary it built for that exact platform at
# $TARGETPLATFORM/runnerscout in the build context automatically. `make
# image` and ci.yml's runtime-image job reproduce that same staging via
# `goreleaser build --single-target` before invoking a plain `docker build`,
# so this file works identically from all three call sites.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99 AS runtime
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/runnerscout /usr/local/bin/runnerscout
ENV HOME=/tmp/home
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/runnerscout"]
