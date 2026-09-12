# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/runnerscout ./cmd/runnerscout

FROM public.ecr.aws/aws-cli/aws-cli@sha256:e8467f2c319f9bc9a1471808a69949a76915e9c95eaf4a09ece9f9e85fd32747 AS aws
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli@sha256:2f2d80f4be75ffb2dc99d2048bd67248b830d1e52eb6a09ac1fa4b05e6e53a91 AS gcloud
FROM python:3.13-slim-trixie@sha256:9d2e5553305c7c7b0097999bb17187c69b921ccd6bc9d40e4bb5ebe652c00285 AS runtime
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
COPY docker/azure-cli-requirements.txt /tmp/azure-cli-requirements.txt
RUN python -m pip install --no-cache-dir --require-hashes -r /tmp/azure-cli-requirements.txt && rm /tmp/azure-cli-requirements.txt
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
    AZURE_CONFIG_DIR=/tmp/azure \
    PYTHONDONTWRITEBYTECODE=1 \
    AWS_EC2_METADATA_DISABLED=true \
    AZURE_CORE_COLLECT_TELEMETRY=false \
    CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/runnerscout"]
