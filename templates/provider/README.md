# External Provider Starter

This template is a minimal external `PlatformProvider` implementation for VM
Image Builder. It runs a gRPC server that implements `api/provider/v1/provider.proto`
through the Go SDK in `pkg/provider/sdk`.

## Run Locally

```bash
go run ./cmd/provider --listen :9443
```

## Container

```bash
docker build -t ghcr.io/yourorg/imagebuilder-provider-example:dev .
```

## Implement

Replace the placeholder logic in `internal/provider/provider.go`:

- `ValidateConfig`: validate credentials and endpoint.
- `UploadArtifact`: stream the artifact to object/block storage.
- `RegisterImage`: register the uploaded artifact as a platform image.
- `DeleteArtifact`: remove temporary upload artifacts idempotently.
- `HealthCheck`: verify API reachability.
- `ReconcileRemoteBuild`: implement only when the provider can build directly
  on the target platform.

By default the template advertises only local upload/register support:

```go
BuildModes: []string{"local"}
```

Add `"remote"` only after `ReconcileRemoteBuild` is implemented and tested for
your platform.

Keep image references digest-pinned and signed when using
`PlatformProvider.spec.security.verifySignature`.

## mTLS

The template starts the SDK server with `sdk.ServerOptionsFromEnv()`. For local
development, leave TLS variables unset. In production, set
`PlatformProvider.spec.transport.tls.mode: Mutual`; the core operator injects:

```bash
PROVIDER_GRPC_TLS_MODE=Mutual
PROVIDER_GRPC_TLS_CERT_FILE=/var/run/imagebuilder/provider-tls/tls.crt
PROVIDER_GRPC_TLS_KEY_FILE=/var/run/imagebuilder/provider-tls/tls.key
PROVIDER_GRPC_TLS_CLIENT_CA_FILE=/var/run/imagebuilder/provider-client-ca/ca.crt
```

The SDK then requires and verifies the operator client certificate.
