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
        └── 30-monitoring.sh
```

Use numeric prefixes to make execution order explicit. Keep scripts
idempotent: a retry should be safe, packages should not be reconfigured
unnecessarily, and service changes should tolerate already-applied state.

For production `VMImage` manifests, pin `source.git.ref` to an immutable commit
SHA instead of a mutable branch.

