#!/usr/bin/env bash
set -euo pipefail

# The platform-config-agent is a PlatformFactory release artifact. The VMImage
# passes only its immutable HTTPS location and SHA-256 through literal
# provisioner environment values. Registry or Git credentials are deliberately
# not accepted by this script.
: "${PLATFORM_CONFIG_AGENT_URL:?set an immutable HTTPS agent artifact URL}"
: "${PLATFORM_CONFIG_AGENT_SHA256:?set the 64-character SHA-256 of the agent binary}"

if [[ ! "${PLATFORM_CONFIG_AGENT_URL}" =~ ^https://[^/@]+(/|$) ]]; then
  printf 'PLATFORM_CONFIG_AGENT_URL must be an HTTPS URL without embedded credentials\n' >&2
  exit 1
fi
if [[ ! "${PLATFORM_CONFIG_AGENT_SHA256}" =~ ^[0-9a-f]{64}$ ]]; then
  printf 'PLATFORM_CONFIG_AGENT_SHA256 must be a lowercase 64-character digest\n' >&2
  exit 1
fi

work_dir="$(mktemp -d /tmp/platform-config-agent.XXXXXX)"
trap 'rm -rf "${work_dir}"' EXIT
artifact_path="${work_dir}/platform-config-agent"

curl \
  --proto '=https' \
  --tlsv1.2 \
  --fail \
  --location \
  --silent \
  --show-error \
  --output "${artifact_path}" \
  "${PLATFORM_CONFIG_AGENT_URL}"

printf '%s  %s\n' "${PLATFORM_CONFIG_AGENT_SHA256}" "${artifact_path}" | sha256sum --check --strict
sudo install -o root -g root -m 0755 "${artifact_path}" /usr/local/bin/platform-config-agent
sudo install -d -o root -g root -m 0700 /var/lib/platform-config-agent/incoming
sudo install -d -o root -g root -m 0700 /var/lib/platform-config-agent/backups

# Fail the image build if the downloaded artifact is not the expected CLI and
# record non-sensitive version/digest evidence in the image.
agent_version="$(/usr/local/bin/platform-config-agent --version)"
test -n "${agent_version}"
sudo install -d -o root -g root -m 0755 /etc/platform-config-agent
printf 'version=%s\nsha256=%s\n' "${agent_version}" "${PLATFORM_CONFIG_AGENT_SHA256}" | \
  sudo tee /etc/platform-config-agent/release >/dev/null
sudo chmod 0644 /etc/platform-config-agent/release
