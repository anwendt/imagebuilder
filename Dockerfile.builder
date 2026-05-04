# Dockerfile for the VM image build-engine container.
#
# The builder runs as a Kubernetes Job main container and writes only to the
# mounted /workspace volume. It does not need Kubernetes API credentials.

FROM golang:1.26.2-alpine AS builder

RUN apk add --no-cache ca-certificates

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags="-s -w -extldflags=-static" \
      -o /workspace/imagebuilder-builder \
      ./cmd/builder

FROM debian:12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates genisoimage openssh-client qemu-system-x86 qemu-utils \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --home-dir /workspace --shell /usr/sbin/nologin nonroot

COPY --from=builder /workspace/imagebuilder-builder /imagebuilder-builder

USER 65532:65532
WORKDIR /workspace

ENTRYPOINT ["/imagebuilder-builder"]
