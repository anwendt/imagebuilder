# External Provider SDK

External providers are OCI images that implement the stable gRPC contract in
`api/provider/v1/provider.proto`. The Go SDK in `pkg/provider/sdk` removes most
of the gRPC boilerplate.

## Provider identity and selection

The provider name returned by `GetCapabilities()` is the logical API identity
and must match `ProviderConfig.spec.provider`. The `PlatformProvider` resource
selecting the external implementation must use that same value as
`metadata.name`. This explicit resource-name match takes precedence over a
same-named built-in provider. If no matching `PlatformProvider` exists, the
built-in implementation remains the fallback.

Built-in and external implementations are stored separately, so an external
provider may advertise `aws`, `azure`, `gcp`, `openstack`, or `vsphere` without
overwriting the built-in. An unhealthy selected external implementation causes
a clear reconciliation failure rather than a silent built-in fallback.

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

Reference-provider behavior:

| Provider | Direct target streaming |
|---|---|
| AWS | gRPC reader to S3 `PutObject` or bounded 64 MiB multipart parts |
| Azure | Sequential page-aligned chunks to Azure Page Blob Storage |
| OpenStack | gRPC reader directly to Glance image-data upload |
| vSphere | VMDK reader directly to the datastore HTTP upload API |

vSphere OVA/OVF is intentionally spooled because OVF/template and Content
Library registration reopen the archive and inspect its descriptor, manifest,
and referenced disks. Providers with similar random-access requirements may
retain a bounded-lifecycle spool fallback, but direct streaming is required
when the target SDK accepts an `io.Reader`.

The reference providers wrap streams with `sdk.NewValidatingReader`. It checks
the declared byte length and SHA-256 during target upload and rejects truncated,
overlong, partially consumed, or checksum-mismatched streams. Progress callbacks
are emitted as the target platform consumes bytes rather than after a local
spool completes.

Report progress when useful:

```go
progress.Report(ctx, sdk.Progress{
    BytesWritten: written,
    TotalBytes: artifact.TotalSizeBytes,
    Phase: "uploading",
})
```

The final `ProviderRef` is passed to `RegisterImage`.

### Upload sessions and retries

Local upload Jobs persist `upload-sessions.json` atomically on the workspace
PVC. Each target has a deterministic idempotency key scoped to the build,
provider configuration, format, checksum, and size. A replacement Pod restores
the session before it calls the provider. Completed upload and registration
phases are restored from that checkpoint instead of being repeated.

The additive v1 gRPC fields implement a session handshake:

1. the client sends a metadata-only `UploadChunk` with `idempotency_key`, the
  previous `session_token`, and `resume_offset`;
2. the provider returns `UploadProgress{phase:"session"}` with its opaque token,
  authoritative `committed_offset`, and `resume_mode`;
3. the client atomically persists that acknowledgement before sending bytes;
4. every subsequent chunk offset is checked exactly, and EOF without a final
  chunk or a size mismatch fails closed.

`resume_mode` has two deliberately distinct values:

- `restart`: the session is idempotent but retransmits from offset zero;
- `offset`: the provider durably stores partial data and resumes from the
  acknowledged byte offset.

The SDK server provides `restart` compatibility for existing `sdk.Provider`
implementations. Providers must implement `sdk.ResumableProvider` and return
`UploadResumeMode: "offset"` only when `PrepareUpload` can reconstruct durable
backend state. AWS persists S3 multipart upload IDs and ETags, Azure persists
Page Blob offsets, and GCP persists the JSON API resumable session URI.
OpenStack and vSphere retain honest safe-restart semantics.

Resume preparation must treat the backend as authoritative. The reference AWS
provider rebuilds part state with `ListParts`; Azure and GCP rotate checkpoints
whose backend session no longer exists. GCP also requires resumable session URIs
and redirects to match the configured upload origin.

Upload Jobs use a bounded `backoffLimit` of three. The uploader exits with code
75 only for errors classified as transient (transport failures, throttling,
deadlines, or temporary service failures). A Pod failure policy fails the Job
immediately for terminal exit code 1; exit code 75 and infrastructure failures
consume the bounded retry budget.

## RegisterImage

Register the uploaded artifact as a platform-native image and return:

- ID
- name
- location
- tags

The ID must be non-empty.

`RegisterRequest.idempotency_key` is stable across uploader Pod retries. A
provider must return the previously-created image for the same key instead of
creating a duplicate after a lost response. Reference providers persist or
derive this identity through native tags, labels, deterministic resources,
Glance image IDs, and vSphere annotations.

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
