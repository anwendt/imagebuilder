# External Provider SDK

External providers are OCI images that implement the stable gRPC contract in
`api/provider/v1/provider.proto`. The Go SDK in `pkg/provider/sdk` removes most
of the gRPC boilerplate.

## Provider Interface

Implement `sdk.Provider`:

```go
type Provider interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    ValidateConfig(ctx context.Context, config Config) error
    UploadArtifact(ctx context.Context, artifact ArtifactInfo, body io.Reader, progress ProgressReporter) (UploadResult, error)
    RegisterImage(ctx context.Context, input RegisterInput) (ImageRef, error)
    DeleteArtifact(ctx context.Context, input DeleteInput) (bool, string, error)
    HealthCheck(ctx context.Context) (string, error)
}
```

## Starter Template

Copy `templates/provider` into a new repository:

```bash
cp -R templates/provider ../imagebuilder-provider-example
cd ../imagebuilder-provider-example
go mod edit -module github.com/yourorg/imagebuilder-provider-example
go mod tidy
```

Run locally:

```bash
go run ./cmd/provider --listen :9443
```

Build a container:

```bash
docker build -t ghcr.io/yourorg/imagebuilder-provider-example:dev .
```

## Capabilities

`Capabilities` is called during provider handshake. It must return:

- provider name
- provider version
- supported artifact formats
- supported OS families
- supported build modes

The SDK sets `protocol_version` to `v1`.

Providers that only support upload/register after a local build should return:

```go
BuildModes: []string{"local"}
```

Providers that can run a provider-owned build on the target platform may return:

```go
BuildModes: []string{"local", "remote"}
```

## ValidateConfig

ValidateConfig receives:

- `ProviderConfig` name
- decoded Secret data
- region
- endpoint
- insecure flag
- provider-specific `extra` map

Return an error when credentials or configuration are invalid. The SDK maps that
to `ValidateConfigResponse{valid:false}`.

## Remote-build error classification

`ReconcileRemoteBuild` errors are terminal by default. External providers must
mark errors as transient only when repeating the same idempotent request is
safe, for example throttling, temporary service unavailability, or a transport
timeout:

```go
return sdk.RemoteBuildResult{}, sdk.TransientError(err, 30*time.Second)
```

Pass a zero duration to use the core controller's exponential backoff. Use
`sdk.TerminalError(err)` to override generic timeout classification when a
timeout represents a completed, non-repeatable provider operation. The SDK
maps transient errors to gRPC `Unavailable`; the core persists retry count and
next retry time and stops retrying when the VMImage build timeout expires.

Do not mark invalid configuration, unsupported input, authentication or
authorization failures, missing source resources, or failed guest provisioner
execution as transient.

## UploadArtifact

The SDK exposes the streamed artifact as an `io.Reader`:

```go
func (p *Provider) UploadArtifact(ctx context.Context, artifact sdk.ArtifactInfo, body io.Reader, progress sdk.ProgressReporter) (sdk.UploadResult, error) {
    // stream body to object storage, datastore, or platform upload API
    return sdk.UploadResult{ProviderRef: "..."} , nil
}
```

Report progress when useful:

```go
progress.Report(ctx, sdk.Progress{
    BytesWritten: written,
    TotalBytes: artifact.TotalSizeBytes,
    Phase: "uploading",
})
```

The final `ProviderRef` is passed to `RegisterImage`.

## RegisterImage

Register the uploaded artifact as a platform-native image and return:

- ID
- name
- location
- tags

The ID must be non-empty.

## DeleteArtifact

DeleteArtifact must be idempotent. Return success when the artifact is already
gone.

## HealthCheck

HealthCheck should be cheap and fast. Prefer a lightweight API call such as
identity, region, or endpoint reachability check.

## Provider gRPC TLS

The starter template calls `sdk.ServerOptionsFromEnv()` before starting the
gRPC server. This keeps provider implementations independent from Kubernetes
mount paths while still supporting production mTLS.

For plaintext local development, leave TLS variables unset:

```bash
go run ./cmd/provider --listen :9443
```

For mTLS, the core operator injects these variables when
`PlatformProvider.spec.transport.tls.mode: Mutual` is configured:

```bash
PROVIDER_GRPC_TLS_MODE=Mutual
PROVIDER_GRPC_TLS_CERT_FILE=/var/run/imagebuilder/provider-tls/tls.crt
PROVIDER_GRPC_TLS_KEY_FILE=/var/run/imagebuilder/provider-tls/tls.key
PROVIDER_GRPC_TLS_CLIENT_CA_FILE=/var/run/imagebuilder/provider-client-ca/ca.crt
```

`ServerOptionsFromEnv()` configures the server with
`tls.RequireAndVerifyClientCert`. Providers that do not use the Go SDK must
implement the same behavior: require a verified operator client certificate and
serve a certificate whose DNS name matches `spec.transport.tls.serverName` or
the default provider Service DNS name.

## Remote Build

Remote build is optional. Implement `sdk.RemoteBuildProvider` only when the
provider can create the temporary VM/instance, provision it, shut it down,
capture the image, register it, and clean up provider-side resources.

```go
type RemoteBuildProvider interface {
    Provider
    ReconcileRemoteBuild(ctx context.Context, input RemoteBuildInput) (RemoteBuildResult, error)
}
```

The method must be idempotent for the same `BuildID` and `OperationRef`. Return
an `OperationRef` as soon as a provider-side operation exists, and return
`Done: true` only when the final image reference is available.

Progress example:

```go
return sdk.RemoteBuildResult{
    OperationRef: "provider-operation-id",
    Phase: "Registering",
    Message: "creating platform image",
    Done: false,
}, nil
```

Completion example:

```go
return sdk.RemoteBuildResult{
    OperationRef: "provider-operation-id",
    Phase: "Ready",
    Done: true,
    Hygiene: &sdk.RemoteHygieneResult{
        Status: "passed",
        Message: "final image hygiene checks passed",
        Checks: []string{"temporary-user-removed", "bootstrap-files-removed"},
        ResultRef: "provider-report-id",
    },
    Images: []sdk.RemoteImageRef{{
        Provider: "example",
        ProviderConfigName: input.ProviderConfigName,
        ImageRef: "platform-image-id",
        Location: input.Region,
        Format: input.Format,
    }},
}, nil
```

`Hygiene.Status` is provider-neutral and must be one of `passed`, `failed`, or
`unknown`. Return `failed` when bootstrap credentials, seed media, temporary
users, autologon material, or unattend/cloud-init residues remain in the final
image. `ResultRef` should reference a provider-side report or task ID, not raw
check output. Providers that cannot perform final hygiene checks must return
`unknown`, not `passed`.

Remote build providers must never put credentials, cloud-init data, scripts, or
temporary passwords in `Message`, `OperationRef`, image refs, logs, or status
metadata.

## Remote Build Cleanup

Remote build providers that advertise `build_modes: ["remote"]` should also
implement `sdk.RemoteBuildCleanupProvider`. The core calls this on VMImage
deletion, timeout, cancellation, or failed remote reconciliation.

```go
type RemoteBuildCleanupProvider interface {
    RemoteBuildProvider
    CleanupRemoteBuild(ctx context.Context, input RemoteBuildInput) (RemoteBuildCleanupResult, error)
}
```

Cleanup must be idempotent for the same `BuildID` and `OperationRef`. Providers
should remove temporary instances, disks, snapshots, uploads, locks, and partial
images, and treat already-deleted resources as success.

## Kubernetes Manifests

The template includes:

- `config/platformprovider.yaml`
- `config/providerconfig.yaml`

Provider images should be digest-pinned and signed:

```yaml
spec:
  package: ghcr.io/yourorg/imagebuilder-provider-example@sha256:...
  security:
    allowedRegistries:
      - ghcr.io/yourorg
    requireDigest: true
    verifySignature: true
  transport:
    tls:
      mode: Mutual
      serverName: provider-example.imagebuilder-system.svc
      caSecretRef:
        name: provider-grpc-ca
        namespace: imagebuilder-system
      clientCertificateSecretRef:
        name: operator-provider-client-tls
        namespace: imagebuilder-system
      serverCertificateSecretRef:
        name: provider-example-server-tls
        namespace: imagebuilder-system
```

## Compatibility

Do not change `api/provider/v1/provider.proto` in a breaking way. Breaking
changes require a new package such as `api/provider/v2`.
