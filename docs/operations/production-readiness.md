# Production Readiness

This checklist describes the repository-supported production path for the core
operator, build jobs, upload jobs, and platform providers.

## Release Images

Publish the core runtime images from a release tag or the `Core Images Release`
workflow:

```bash
make docker-push-core REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
make docker-digest-core REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
OPERATOR_DIGEST=sha256:<digest> \
BUILDER_DIGEST=sha256:<digest> \
UPLOADER_DIGEST=sha256:<digest> \
make sign-core REGISTRY=ghcr.io/anwendt
```

Publish providers independently:

```bash
make docker-push-provider-aws REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
AWS_PROVIDER_DIGEST=sha256:<digest> make sign-provider-aws REGISTRY=ghcr.io/anwendt
AWS_PROVIDER_DIGEST=sha256:<digest> make update-aws-provider-samples REGISTRY=ghcr.io/anwendt

make docker-push-provider-vsphere REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
VSPHERE_PROVIDER_DIGEST=sha256:<digest> make sign-provider-vsphere REGISTRY=ghcr.io/anwendt
VSPHERE_PROVIDER_DIGEST=sha256:<digest> make update-vsphere-provider-samples REGISTRY=ghcr.io/anwendt

make docker-push-provider-openstack REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
OPENSTACK_PROVIDER_DIGEST=sha256:<digest> make sign-provider-openstack REGISTRY=ghcr.io/anwendt
OPENSTACK_PROVIDER_DIGEST=sha256:<digest> make update-openstack-provider-samples REGISTRY=ghcr.io/anwendt
```

The release workflows publish multi-arch images, generate provenance and SBOM
attestations, sign pushed digests with keyless Cosign, and print immutable image
references in the workflow summary.

## Helm Values

Use Helm for production deployment. Set immutable digests for every image and
keep the production policy flags enabled:

```yaml
image:
  repository: ghcr.io/anwendt/imagebuilder-operator
  tag: v0.5.0
  digest: sha256:<operator-digest>

builderImage:
  repository: ghcr.io/anwendt/imagebuilder-builder
  tag: v0.5.0
  digest: sha256:<builder-digest>

uploaderImage:
  repository: ghcr.io/anwendt/imagebuilder-uploader
  tag: v0.5.0
  digest: sha256:<uploader-digest>

providerSecurity:
  requireMTLS: true
  requireDigest: true
  requireSignature: true
  allowedRegistries:
    - ghcr.io/anwendt

imageSignaturePolicy:
  enabled: true

networkPolicy:
  enabled: true
  workloadNamespaces:
    - imagebuilder-tenant-a
    - imagebuilder-tenant-b
```

The default chart values are production-oriented. The repository also ships
`charts/imagebuilder/values-development.yaml` for local clusters; do not use
that profile for production because it disables webhooks, network policies,
namespace guardrails, and strict provider package policies.

`imageSignaturePolicy.enabled` defaults to `true` and must remain true while
`providerSecurity.requireSignature` is enabled. Install Kyverno first. The
operator validates both the enforcing `ClusterPolicy` and an active fail-closed
Kyverno validating webhook before creating provider Deployments.

Provider `PlatformProvider` resources must also use digest-pinned package
references and mTLS:

```yaml
spec:
  package: ghcr.io/anwendt/imagebuilder-provider-vsphere@sha256:<digest>
  transport:
    tls:
      mode: Mutual
  security:
    requireDigest: true
    verifySignature: true
```

## Runtime Controls

- Keep the validating webhook enabled with `failurePolicy: Fail`.
- Keep namespace guardrails enabled so `VMImage` and `ProviderConfig` resources
  cannot cross tenant namespaces unless explicitly allowed.
- Configure `networkPolicy.workloadNamespaces` for every namespace where
  `VMImage` build and upload Jobs run.
- Use provider mTLS for every provider outside the operator Pod trust boundary.
- Prefer workload identity, IRSA, or assumed roles over static cloud keys.
- Configure cache TTL and retention policies for the source cache PVC.
- Run at least two operator replicas with leader election enabled.

## Validation Gates

CI now treats the following as required gates:

- full Go tests, deterministic core E2E tests, and manifest invariants;
- Go module integrity verification with `go mod verify`;
- generated API, CRD, RBAC, webhook, and protobuf artifacts;
- `go vet`, `staticcheck`, `gosec`, `govulncheck`, and license checks;
- gitleaks secret scanning;
- Helm lint/render;
- digest-pinned Dockerfile base images and commit-SHA-pinned GitHub Actions;
- Trivy scans for operator, builder, uploader, AWS provider, Azure provider,
  vSphere provider, and OpenStack provider.
  The blocking gate fails on fixable `HIGH` and `CRITICAL` findings; the SARIF
  upload job still reports the full scanner output for review;
- govmomi simulator integration test for vSphere upload/register/cleanup.
- opt-in live provider E2E workflows for AWS, OpenStack, and local
  Windows Cloudbase-Init/Sysprep validation. Azure and vSphere live provider
  tests are available through `make test-e2e-azure` and `make test-e2e-vsphere`.

Before approving a production rollout, also run one real provider smoke test per
target environment. The simulator validates code paths, not vCenter inventory,
networking, datastore capacity, IAM policy, or organizational admission policy.

For a local production-install smoke test, render the production Helm defaults
and roll out the operator into kind with:

```bash
make test-e2e-production
```

The production render keeps webhook, NetworkPolicy, namespace guardrail, provider
mTLS, digest, signature, and image policy invariants enabled. The kind install
path disables only cert-manager and Kyverno resources because those external CRDs
are not present in a default kind cluster.

## Residual Operational Tasks

These are environment-specific and cannot be completed solely in this repo:

- install cert-manager, Kyverno or an equivalent image policy engine, and
  Prometheus Operator CRDs where those chart features are enabled;
- configure Cosign trust policy for your registry and GitHub OIDC issuer;
- provision cloud and vSphere service accounts with least-privilege roles;
- define backup and retention for generated artifacts, source cache PVCs, and
  provider-specific image registries;
- define incident runbooks for failed builds, leaked credentials, and image
  revocation.
