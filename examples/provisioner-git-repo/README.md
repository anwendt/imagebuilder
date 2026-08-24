# Example Git Provisioner Repository

This directory shows how a separate image customization repository can be
structured when `VMImage.spec.provisioners[].source.git.path` points to a
directory.

The image builder clones the repository, checks out the requested `ref`, reads
regular files under `scripts/ubuntu`, sorts them lexicographically by relative
path, and runs each file as a separate `shell` provisioner step.

```text
image-scripts/
└── scripts/
    └── ubuntu/
        ├── 10-basic-tools.sh
        ├── 20-hardening.sh
        ├── 30-monitoring.sh
        ├── 40-platform-config-agent.sh
        └── 90-evidence.sh
```

Use numeric prefixes to make execution order explicit. Keep scripts
idempotent: a retry should be safe, packages should not be reconfigured
unnecessarily, and service changes should tolerate already-applied state.

For production `VMImage` manifests, pin `source.git.ref` to an immutable commit
SHA instead of a mutable branch.

`40-platform-config-agent.sh` demonstrates the production handoff required by
PlatformFactory ADR-0040. The `shell` provisioner must receive the immutable
agent artifact URL and its SHA-256 as literal, non-sensitive environment
values. The script rejects credentials, non-HTTPS transport, mutable content
that fails checksum verification, and an artifact that does not expose the
expected CLI.

```yaml
provisioners:
  - type: shell
    source:
      git:
        url: https://github.com/yourorg/image-scripts.git
        ref: 7f6e5d4c3b2a190817263544536271809abcdef0
        path: scripts/ubuntu/40-platform-config-agent.sh
    env:
      - name: PLATFORM_CONFIG_AGENT_URL
        value: https://packages.example.net/platform-config-agent/1.0.0/linux-amd64
      - name: PLATFORM_CONFIG_AGENT_SHA256
        value: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

The release pipeline must replace the example URL and digest. Do not pass a
token or password through `env`; protected registries need an approved
build-time workload identity or an internal artifact mirror.

`90-evidence.sh` is used as a separate final provisioner with type `evidence`,
not as another `shell` entry in the directory expansion. It creates an SPDX
SBOM, scans the final guest filesystem with Trivy, signs SLSA provenance with
Cosign, publishes the documents through ORAS, and returns only immutable OCI
references to the provider. See the VMImage authoring guide for the complete
manifest and identity requirements.
