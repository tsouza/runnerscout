# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY api/ api/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/runnerscout ./cmd/runnerscout

FROM public.ecr.aws/aws-cli/aws-cli@sha256:e8467f2c319f9bc9a1471808a69949a76915e9c95eaf4a09ece9f9e85fd32747 AS aws
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli@sha256:147cb346b1bdc3256085fc2c1ee98c6f3628bd5d99eba3fb3ac8287fa088a8d9 AS gcloud
FROM python:3.14-slim-trixie@sha256:cad9a2c871761c413caa6fdd6441c783451e740a48aaeba60ae62a8b53525ef6 AS runtime
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
COPY --from=aws /usr/local/aws-cli /usr/local/aws-cli
RUN ln -s /usr/local/aws-cli/v2/current/bin/aws /usr/local/bin/aws
COPY --from=gcloud /usr/lib/google-cloud-sdk /opt/google-cloud-sdk
# Use the pinned system Python; remove the unused interpreter and its libraries.
RUN rm -rf /opt/google-cloud-sdk/platform/bundledpythonunix
COPY --from=build /out/runnerscout /usr/local/bin/runnerscout
ENV PATH="/opt/google-cloud-sdk/bin:${PATH}" \
    HOME=/tmp/home \
    CLOUDSDK_CONFIG=/tmp/gcloud \
    CLOUDSDK_PYTHON=/usr/local/bin/python3 \
    \
    PYTHONDONTWRITEBYTECODE=1 \
    AWS_EC2_METADATA_DISABLED=true \
    \
    CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/runnerscout"]
