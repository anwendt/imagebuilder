# Dockerfile for the VM image build-engine container.
#
# The builder runs as a Kubernetes Job main container and writes only to the
# mounted /workspace volume. It does not need Kubernetes API credentials.

FROM golang:1.26.3-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN apk add --no-cache ca-certificates

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -extldflags=-static" \
      -o /workspace/imagebuilder-builder \
      ./cmd/builder

FROM debian:12-slim@sha256:67b30a61dc87758f0caf819646104f29ecbda97d920aaf5edc834128ac8493d3

RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates genisoimage openssh-client qemu-system-x86 qemu-utils \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --home-dir /workspace --shell /usr/sbin/nologin nonroot

COPY --from=builder /workspace/imagebuilder-builder /imagebuilder-builder

USER 65532:65532
WORKDIR /workspace

ENTRYPOINT ["/imagebuilder-builder"]
