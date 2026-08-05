# Dockerfile — imagebuilder operator
#
# Multi-stage build (AS-055, AS-056, AS-057, AS-058, AS-059):
#   Stage 1: Build — compile the operator binary with Go
#   Stage 2: Runtime — distroless "nonroot" image, no shell, no package manager
#
# Security properties:
#   - Distroless base (AS-056): no shell → no exec-based attack pivot
#   - Non-root UID 65532 (AS-057, AS-059): operator runs as "nonroot"
#   - Read-only binary layer (AS-058): scratch-equivalent runtime
#   - CGO disabled (AS-055): no dynamic linking → no ld.so attack surface
#   - No secrets in build args or layers (AS-059)

# ---------------------------------------------------------------------------
# Stage 1 — Builder
# ---------------------------------------------------------------------------
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Install ca-certificates so TLS works when fetching modules.
RUN apk add --no-cache ca-certificates git

WORKDIR /workspace

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build.
COPY . .

# Build flags:
#   -trimpath        — remove local FS paths from binary (AS-059)
#   -ldflags "-s -w" — strip debug info to reduce attack surface and image size
#   CGO_ENABLED=0    — static binary, no dynamic C libraries (AS-055)
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -extldflags=-static" \
      -o /workspace/manager \
      ./cmd/operator

# ---------------------------------------------------------------------------
# Stage 2 — Runtime (distroless nonroot)
# ---------------------------------------------------------------------------
# gcr.io/distroless/static-debian12:nonroot provides:
#   - ca-certificates (HTTPS to Kubernetes API server)
#   - tzdata
#   - no shell, no package manager, no libc  (AS-056)
#   - USER=nonroot (UID 65532)               (AS-057, AS-059)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:a9329520abc449e3b14d5bc3a6ffae065bdde0f02667fa10880c49b35c109fd1

# Copy the compiled binary from the builder stage.
COPY --from=builder /workspace/manager /manager

# Distroless "nonroot" sets USER 65532 by default; make it explicit.
USER 65532:65532

# The operator listens on 8080 (metrics) and 9443 (webhook).
# Expose is documentation-only — actual port binding is via Kubernetes Service.
EXPOSE 8080 9443

ENTRYPOINT ["/manager"]
