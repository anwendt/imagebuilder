# Git-Backed Provisioner Scripts

`VMImage` provisioners can load their content from a Git repository instead of
embedding long scripts directly in the manifest. This is useful when image
customization is maintained by another team, reviewed in a separate repository,
or shared across multiple image definitions.

## Repository Layout

For shell provisioners, point `spec.provisioners[].source.git.path` either to a
single script file or to a directory. When the path is a directory, every
regular file below it is loaded and executed as a separate provisioner step in
lexicographic order.

```text
image-scripts/
└── scripts/
    └── ubuntu/
        ├── 10-basic-tools.sh
        ├── 20-hardening.sh
        └── 30-monitoring.sh
```

The repository includes a concrete example under
`examples/provisioner-git-repo/`.

## Example Scripts

`10-basic-tools.sh` installs basic packages:

```bash
#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  jq \
  lsb-release \
  unzip

apt-get clean
rm -rf /var/lib/apt/lists/*
```

`20-hardening.sh` applies SSH and sysctl settings:

```bash
#!/usr/bin/env bash
set -euo pipefail

install -d -m 0755 /etc/ssh/sshd_config.d
cat >/etc/ssh/sshd_config.d/90-imagebuilder-hardening.conf <<'EOF'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
X11Forwarding no
ClientAliveInterval 300
ClientAliveCountMax 2
EOF

install -d -m 0755 /etc/sysctl.d
cat >/etc/sysctl.d/90-imagebuilder-hardening.conf <<'EOF'
net.ipv4.ip_forward = 0
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.rp_filter = 1
net.ipv4.conf.default.rp_filter = 1
EOF

sysctl --system

if command -v systemctl >/dev/null 2>&1; then
  systemctl reload ssh || systemctl reload sshd || true
fi
```

`30-monitoring.sh` installs and enables node exporter:

```bash
#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends prometheus-node-exporter

if command -v systemctl >/dev/null 2>&1; then
  systemctl enable prometheus-node-exporter
fi

apt-get clean
rm -rf /var/lib/apt/lists/*
```

## Public Repository VMImage

Use an immutable commit SHA for `ref` in production. Branch names such as `main`
are convenient for development, but they make image builds less reproducible.

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: VMImage
metadata:
  name: ubuntu-24-04-git-provisioners
  namespace: imagebuilder-system
spec:
  os:
    family: linux
    distribution: ubuntu
    version: "24.04"
    arch: amd64
  source:
    type: cloud-image
    url: https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img
    checksum: sha256:replace-with-published-checksum
  provisioners:
    - type: shell
      source:
        git:
          url: https://github.com/yourorg/image-scripts.git
          ref: 7f6e5d4c3b2a190817263544536271809abcdef0
          path: scripts/ubuntu
  targets:
    - providerConfigRef:
        name: aws-eu-central-1
      format: ami
  build:
    timeout: 2h
    guestAccess:
      protocol: ssh
      host: 127.0.0.1
      hostPort: 2222
      credentials:
        generate:
          sshKey: true
        injection:
          method: cloud-init
```

With the `scripts/ubuntu` directory above, the effective provisioner sequence is:

1. `10-basic-tools.sh`
2. `20-hardening.sh`
3. `30-monitoring.sh`

Each file is expanded into a separate `shell` step with the file content copied
into `inline` at build time.

## Private Repository Authentication

Put Git credentials in a Kubernetes Secret in the same namespace as the
`VMImage`. Do not put tokens or passwords in the Git URL.

Token authentication:

```bash
kubectl create secret generic private-git \
  --namespace imagebuilder-system \
  --from-literal=token="${GIT_READ_TOKEN}"
```

```yaml
provisioners:
  - type: shell
    source:
      git:
        url: https://github.com/yourorg/private-image-scripts.git
        ref: 7f6e5d4c3b2a190817263544536271809abcdef0
        path: scripts/ubuntu
        auth:
          secretRef:
            name: private-git
            tokenKey: token
```

Basic authentication:

```bash
kubectl create secret generic private-git-basic \
  --namespace imagebuilder-system \
  --from-literal=username="${GIT_READ_USERNAME}" \
  --from-literal=password="${GIT_READ_PASSWORD}"
```

```yaml
provisioners:
  - type: shell
    source:
      git:
        url: https://github.com/yourorg/private-image-scripts.git
        ref: 7f6e5d4c3b2a190817263544536271809abcdef0
        path: scripts/ubuntu
        auth:
          secretRef:
            name: private-git-basic
            usernameKey: username
            passwordKey: password
```

The controller mounts the Secret read-only into the build pod and passes
controller-managed file paths to the builder. Runtime credential path fields are
reserved for that internal handoff and are rejected in user-authored manifests.

## Rules And Limits

- `source.git.url` must be HTTPS.
- Raw IP hosts, loopback, private, and link-local addresses are rejected.
- `source.git.ref` is required.
- `source.git.path` must be a relative path inside the repository and may not
  contain `..`.
- A single script file may be at most 1 MiB.
- All expanded scripts together may be at most 10 MiB.
- Directory expansion is supported for in-process provisioners such as `shell`
  and `powershell`.
- Init-container provisioners such as Ansible, Chef, Puppet, SaltStack,
  `custom`, and third-party OCI provisioners must resolve to a single file per
  provisioner entry.
