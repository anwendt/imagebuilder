# Security Guide

## Security Model

The core design uses Kubernetes boundaries:

- operator as control plane
- build and upload as short-lived Jobs
- providers as independent Deployments
- credentials through Kubernetes Secrets
- generated guest credentials in memory-backed volumes and local files

## Supply Chain Policy

Provider images support policy through `PlatformProvider.spec.security`:

```yaml
spec:
  package: ghcr.io/yourorg/provider@sha256:...
  security:
    allowedRegistries:
      - ghcr.io/yourorg
    requireDigest: true
    verifySignature: true
```

Provisioner images support policy through
`VMImage.spec.build.security.provisionerImages`:

```yaml
build:
  security:
    provisionerImages:
      allowedRegistries:
        - ghcr.io/yourorg
      requireDigest: true
      verifySignature: true
```

The core validates registry allow-lists and digest pinning. Cryptographic
signature verification should be enforced by cluster admission policy. A Kyverno
example is provided in `config/policy/kyverno-image-signatures.yaml`.

## Runtime Hardening

The operator manifests enforce:

- non-root execution
- runtime default seccomp
- read-only root filesystem
- dropped Linux capabilities
- namespace Pod Security Admission labels
- no service account token in build pods

Build pods additionally:

- use read-only mounted Secrets
- write generated credentials with restrictive file modes
- use memory-backed storage for generated guest credentials
- mount `/dev/kvm` only when explicitly enabled

## Guest Credential Handling

Generated SSH keys and WinRM passwords are written to workspace files, not
environment variables. The builder injects them through cloud-init or
autounattend and sanitizes them before final image conversion.

For Windows ISO builds, Cloudbase-Init is installed only from an explicitly
configured MSI path in the unattended media configuration. The core writes
Cloudbase-Init configuration files without embedding generated WinRM passwords;
temporary WinRM credentials remain in the generated Autounattend bootstrap and
are rotated before image conversion.

Final image hygiene checks fail the build if bootstrap residue remains.

## NetworkPolicy

`config/policy/networkpolicies.yaml` includes:

- default deny
- operator ingress for metrics/webhook
- operator egress to API server, DNS, and provider gRPC on TCP/50051
- provider ingress only from operator pods on TCP/50051
- provider egress to HTTPS and DNS only
- build/upload egress to HTTPS and DNS

These policies are enabled by default in the Helm chart. Provider gRPC over
ClusterIP is considered inside the local namespace trust boundary only when
these NetworkPolicies are enforced by the cluster CNI. If a provider endpoint is
exposed across namespaces, clusters, or non-cluster networks, use mTLS with
provider certificate identity verification in addition to NetworkPolicy/firewall
rules.

Adapt egress policies if providers require platform-specific private endpoints.

## Provider gRPC mTLS

`PlatformProvider.spec.transport.tls.mode: Mutual` enables mutual TLS for the
operator-to-provider gRPC channel. The operator:

- verifies the provider server certificate with `caSecretRef`
- presents the client certificate from `clientCertificateSecretRef`
- mounts `serverCertificateSecretRef` into the provider pod as read-only files
- rejects incomplete references before creating or updating provider workloads

Example:

```yaml
spec:
  transport:
    tls:
      mode: Mutual
      serverName: provider-aws.imagebuilder-system.svc
      caSecretRef:
        name: provider-grpc-ca
        namespace: imagebuilder-system
      clientCertificateSecretRef:
        name: operator-provider-client-tls
        namespace: imagebuilder-system
      serverCertificateSecretRef:
        name: provider-aws-server-tls
        namespace: imagebuilder-system
```

Secrets default to Kubernetes TLS key names: `ca.crt`, `tls.crt`, and `tls.key`.
Use `caKey`, `certKey`, or `keyKey` only when the Secret uses non-standard keys.
The Go provider SDK reads the injected `PROVIDER_GRPC_TLS_*` environment
variables and configures `RequireAndVerifyClientCert`.

## Webhook TLS

Webhook TLS uses cert-manager:

- `config/certmanager/webhook-certificate.yaml`
- `config/webhook/manifests.yaml`

The webhook configuration uses CA injection:

```yaml
cert-manager.io/inject-ca-from: imagebuilder-system/imagebuilder-webhook-serving-cert
```

Admission is configured fail-closed for production. Both validating webhooks use
`failurePolicy: Fail`; if the webhook backend is unavailable, Kubernetes rejects
`VMImage` and `ProviderConfig` writes. This matters because several security
rules are cross-field validations that cannot be fully represented by CRD schema
alone, such as OS/protocol combinations, Windows/cloud-init injection mismatch,
provider-native source references, and insecure TLS settings.

The Helm chart keeps these defaults enforced through `values.schema.json`:

- `webhook.enabled` must remain `true`
- `webhook.failurePolicy` must remain `Fail`

## Secrets

Never place secrets in:

- provisioner inline scripts
- Git-backed provisioner scripts
- provisioner args
- non-secret ConfigMaps
- image metadata
- logs

Use Secret references and mounted files instead.

## Git-Backed Provisioners

Git-backed provisioner sources are treated as executable supply-chain input:

- use `spec.provisioners[].source.git.url`, `ref`, and `path`
- use HTTPS URLs only
- pin `ref` to an immutable commit SHA for production
- keep `path` relative to the repository
- store credentials in Secrets, not in URLs or scripts
- use scoped, short-lived tokens where the Git host supports them

Private repositories use `spec.provisioners[].source.git.auth.secretRef`.
Token-based auth reads `token` by default. Basic auth reads `username` and
`password` by default. Local build pods mount these Secrets read-only under
`/credentials/git/<n>` and the builder receives only the mounted file paths.
Remote builds resolve the Secret in the VMImage namespace and forward the
credential material only in the in-memory provider request; it is not serialized
into VMImage spec or status.

When `path` is a directory, regular files are expanded in lexicographic order.
Runtime clone/checkout paths repeat SSRF validation and fail closed if the host
cannot be resolved safely.

## License Boundary

The project does not use Packer. LGPL components must be executed only through
external processes or sockets. They must not be statically or dynamically linked
into Apache-2.0 core binaries.
