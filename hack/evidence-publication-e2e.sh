#!/bin/sh
# ADR-011 evidence publication harness: runs the exact publication Job steps
# (pkg/controller/evidencejob) for two platforms against a throwaway local
# registry:3 container, publishes both into one OCI index concurrently and
# verifies signatures and attestations with cosign. Requires docker only.
#
#   hack/evidence-publication-e2e.sh
set -eu

ORAS_IMAGE="${ORAS_IMAGE:-ghcr.io/oras-project/oras:v1.3.0@sha256:6ce045ce069a89934d6666b8b49f9c4c0145201bd6de6dbe2aee267814c55468}"
COSIGN_IMAGE="${COSIGN_IMAGE:-cgr.dev/chainguard/cosign:latest-dev@sha256:a790a4f273f25d95e3a1df67a48b792f92a75ca230f31d2cea95371401fe40e1}"
NET=imagebuilder-evidence-e2e
REG=imagebuilder-evidence-registry
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'docker rm -f "$REG" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

mkdir -p "$WORK/scripts" "$WORK/keys"
EVIDENCEJOB_DUMP_DIR="$WORK/scripts" go test "$ROOT/pkg/controller/evidencejob" -run TestDumpScripts -count=1 >/dev/null
docker network create "$NET" >/dev/null
docker run -d --name "$REG" --network "$NET" registry:3 >/dev/null
chmod 0777 "$WORK/keys"
docker run --rm -v "$WORK/keys:/keys" -w /keys -e COSIGN_PASSWORD=e2e --entrypoint cosign "$COSIGN_IMAGE" generate-key-pair >/dev/null
chmod -R a+rX "$WORK"

prepare_platform() {
  platform="$1"
  dir="$WORK/$platform"
  mkdir -p "$dir/evidence-src/evidence" "$dir/input" "$dir/work"
  printf '{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"name":"bash","version":"5.2"}]}' > "$dir/evidence-src/evidence/sbom.cdx.json"
  printf '{"SchemaVersion":2,"Results":[]}' > "$dir/evidence-src/evidence/trivy-report.json"
  tar czf "$dir/input/bundle.tar.gz" -C "$dir/evidence-src" evidence
  printf '{"kind":"VMImageDescriptor","subject":{"platform":"%s"}}' "$platform" > "$dir/input/descriptor.json"
  printf '{"buildDefinition":{"buildType":"e2e"},"runDetails":{}}' > "$dir/input/provenance.json"
  : > "$dir/termination-log"
  chmod -R a+rwX "$dir"
}

step() {
  platform="$1"; image="$2"; script="$3"
  dir="$WORK/$platform"
  docker run --rm --network "$NET" --user 65532:65532 --read-only \
    -v "$dir/work:/work" -v "$dir/input:/input:ro" -v "$WORK/keys:/keys:ro" \
    -v "$dir/termination-log:/dev/termination-log" \
    -e HOME=/work/home -e TMPDIR=/work/tmp \
    -e REPO="$REG:5000/vmimages/ubuntu-2604-base" -e DESCRIPTOR_TAG="1.2.0-$platform" -e INDEX_TAG=1.2.0 \
    -e SUBJECT_NAME=ubuntu-2604-base -e SUBJECT_VERSION=1.2.0 -e SUBJECT_PLATFORM="$platform" -e PROVIDER=e2e \
    -e BUNDLE_SHA256="$(sha256sum "$dir/input/bundle.tar.gz" 2>/dev/null | cut -d' ' -f1 || shasum -a 256 "$dir/input/bundle.tar.gz" | cut -d' ' -f1)" \
    -e COSIGN_KEY_PATH=/keys/cosign.key -e COSIGN_PASSWORD=e2e \
    -e ORAS_EXTRA_FLAGS=--plain-http -e COSIGN_EXTRA_FLAGS=--allow-http-registry \
    --entrypoint /bin/sh "$image" -c "$(cat "$WORK/scripts/$script")"
}

publish() {
  platform="$1"
  prepare_platform "$platform"
  step "$platform" "$COSIGN_IMAGE" prepare.sh
  step "$platform" "$ORAS_IMAGE" push-descriptor.sh
  step "$platform" "$COSIGN_IMAGE" sign-descriptor.sh
  step "$platform" "$ORAS_IMAGE" publish-index.sh
  step "$platform" "$COSIGN_IMAGE" sign-index.sh
  step "$platform" "$ORAS_IMAGE" report.sh
}

publish azure > "$WORK/azure.log" 2>&1 &
p1=$!
publish vsphere-fci-c042 > "$WORK/vsphere.log" 2>&1 &
p2=$!
rc=0
wait "$p1" || rc=1
wait "$p2" || rc=1
if [ "$rc" -ne 0 ]; then cat "$WORK/azure.log" "$WORK/vsphere.log"; exit 1; fi

for platform in azure vsphere-fci-c042; do
  echo "$platform termination message: $(cat "$WORK/$platform/termination-log")"
done
index="$(docker run --rm --network "$NET" "$ORAS_IMAGE" manifest fetch --plain-http "$REG:5000/vmimages/ubuntu-2604-base:1.2.0")"
echo "$index"
for platform in azure vsphere-fci-c042; do
  printf '%s' "$index" | grep -qF "\"io.tsigitops.vmimage.platform\":\"$platform\"" || { echo "index lacks $platform"; exit 1; }
done
desc="$(sed -n 's/.*"descriptorDigest":"\([^"]*\)".*/\1/p' "$WORK/azure/termination-log")"
verify() {
  docker run --rm --network "$NET" -v "$WORK/keys:/keys:ro" -e HOME=/tmp --entrypoint cosign "$COSIGN_IMAGE" "$@" \
    --key /keys/cosign.pub --insecure-ignore-tlog --allow-http-registry "$REG:5000/vmimages/ubuntu-2604-base@$desc" >/dev/null
}
verify verify
verify verify-attestation --type https://slsa.dev/provenance/v1
verify verify-attestation --type cyclonedx
verify verify-attestation --type https://tsigitops.t-systems.net/attestations/trivy/v1
echo "evidence publication e2e passed"
