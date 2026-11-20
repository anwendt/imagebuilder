#!/bin/sh
# Reference evidence collector for Ubuntu guests (ADR-011, publisher=controller).
#
# Runs as the single `type: evidence` provisioner. It only COLLECTS evidence:
# it writes a CycloneDX SBOM and a Trivy JSON report into the outbox
# /var/lib/imagebuilder/evidence and nothing else. The provider retrieves and
# deletes the outbox; the imagebuilder controller publishes and signs it. The
# script needs no registry or signing credentials and must not receive any.
#
# Tools are downloaded from the upstream GitHub releases, pinned by sha256, and
# removed afterwards together with all caches. Route the downloads through the
# build proxy with literal provisioner env (HTTPS_PROXY/NO_PROXY), and point
# TRIVY_DB_REPOSITORY / TRIVY_JAVA_DB_REPOSITORY at a mirror when the guest has
# no direct access to ghcr.io. Override SYFT_*/TRIVY_* to use other approved
# releases; always pin the sha256 of the release archive.
#
# Inputs set by the provider (non-secret):
#   IMAGEBUILDER_EVIDENCE_OUTBOX            outbox directory
#   IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY  e.g. HIGH,CRITICAL (fixable findings fail the build here already)
#   IMAGEBUILDER_EVIDENCE_SUBJECT_*         subject name/version/platform (informational)
set -eu

OUTBOX="${IMAGEBUILDER_EVIDENCE_OUTBOX:-/var/lib/imagebuilder/evidence}"
FAIL_ON="${IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY:-}"

SYFT_VERSION="${SYFT_VERSION:-1.57.0}"
SYFT_SHA256_AMD64="${SYFT_SHA256_AMD64:-9c077a7d43029b3b13e98a8db720661e700c4d58db6352153a06395ae4a2a704}"
SYFT_SHA256_ARM64="${SYFT_SHA256_ARM64:-410cbfa0ae0f63246bc0057843760b1de2833cd23919e025e368fd7681308cdb}"
TRIVY_VERSION="${TRIVY_VERSION:-0.76.1}"
TRIVY_SHA256_AMD64="${TRIVY_SHA256_AMD64:-c7cbe3137fb8a48a17574bede3dd6beb16742d1e9772929940fc90082c39d9c0}"
TRIVY_SHA256_ARM64="${TRIVY_SHA256_ARM64:-e174dff2a6dba5e3ae41f8d0537a6324405bd08132497340304c6b20e0f16a1f}"

case "$(dpkg --print-architecture)" in
  amd64) syft_arch=amd64; trivy_arch=64bit; syft_sha="$SYFT_SHA256_AMD64"; trivy_sha="$TRIVY_SHA256_AMD64" ;;
  arm64) syft_arch=arm64; trivy_arch=ARM64; syft_sha="$SYFT_SHA256_ARM64"; trivy_sha="$TRIVY_SHA256_ARM64" ;;
  *) echo "unsupported architecture $(dpkg --print-architecture)" >&2; exit 2 ;;
esac

work="$(mktemp -d /var/tmp/imagebuilder-evidence-tools.XXXXXX)"
cleanup() { rm -rf "$work"; }
trap cleanup EXIT INT TERM
umask 077
# Keep every cache inside $work so nothing remains in the image.
export HOME="$work/home" XDG_CACHE_HOME="$work/cache" TMPDIR="$work/tmp"
export TRIVY_CACHE_DIR="$work/trivy-cache" SYFT_CHECK_FOR_APP_UPDATE=false
# Package SBOM only: per-file components would multiply the size and the
# compressed outbox must stay below 900 KiB.
export SYFT_FILE_METADATA_SELECTION=none SYFT_FILE_CONTENT_SKIP_FILES_ABOVE_SIZE=0
mkdir -p "$HOME" "$XDG_CACHE_HOME" "$TMPDIR"

fetch() { # url sha256 file
  curl --fail --silent --show-error --location --retry 3 --output "$3" "$1"
  echo "$2  $3" | sha256sum -c - >/dev/null || { echo "sha256 mismatch for $1" >&2; exit 3; }
}

fetch "https://github.com/anchore/syft/releases/download/v${SYFT_VERSION}/syft_${SYFT_VERSION}_linux_${syft_arch}.tar.gz" "$syft_sha" "$work/syft.tgz"
fetch "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${trivy_arch}.tar.gz" "$trivy_sha" "$work/trivy.tgz"
tar xzf "$work/syft.tgz" -C "$work" syft
tar xzf "$work/trivy.tgz" -C "$work" trivy

rm -rf "$OUTBOX"
install -d -m 0700 "$OUTBOX"

# Pseudo filesystems, temporary data and the outbox itself are not part of the image.
"$work/syft" scan dir:/ --quiet \
  --exclude './proc/**' --exclude './sys/**' --exclude './dev/**' --exclude './run/**' \
  --exclude './tmp/**' --exclude './var/tmp/**' --exclude './var/lib/imagebuilder/**' \
  --output "cyclonedx-json=$OUTBOX/sbom.cdx.json"

skip_dirs=/proc,/sys,/dev,/run,/tmp,/var/tmp,/var/lib/imagebuilder
"$work/trivy" rootfs --quiet --scanners vuln --skip-dirs "$skip_dirs" \
  --format json --output "$OUTBOX/trivy-report.json" /

if [ -n "$FAIL_ON" ]; then
  # Same rule as the controller gate: only fixable findings block.
  "$work/trivy" rootfs --quiet --scanners vuln --skip-dirs "$skip_dirs" --skip-db-update \
    --ignore-unfixed --severity "$FAIL_ON" --exit-code 1 --format table / || {
    echo "fixable vulnerabilities with severity $FAIL_ON found" >&2
    exit 1
  }
fi

printf '{"collector":"imagebuilder-reference-ubuntu","syft":"%s","trivy":"%s","subject":"%s:%s-%s"}\n' \
  "$SYFT_VERSION" "$TRIVY_VERSION" "${IMAGEBUILDER_EVIDENCE_SUBJECT_NAME:-}" \
  "${IMAGEBUILDER_EVIDENCE_SUBJECT_VERSION:-}" "${IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM:-}" > "$OUTBOX/collector.json"

ls -l "$OUTBOX"
