package evidencejob

// The publication Job runs these POSIX shell steps in order (ADR-011). They
// are modelled on the ImageFactory steps attach-slsa and sign-files
// (artifactbuild ADR-0010): cosign v3 Sigstore bundles stored as OCI
// referrers, key based signing without transparency log.
//
// Environment (set by the controller, never secret except COSIGN_PASSWORD):
//
//	REPO              registry/path/<subject name> (no scheme)
//	DESCRIPTOR_TAG    <version>-<platform>
//	INDEX_TAG         <version>
//	SUBJECT_NAME, SUBJECT_VERSION, SUBJECT_PLATFORM, PROVIDER
//	BUNDLE_SHA256     hex digest of the evidence bundle
//	REGISTRY_CONFIG   docker config.json with push credentials (optional file)
//	COSIGN_KEY_PATH   cosign private key file
//	COSIGN_PASSWORD   cosign key password (from the key Secret, optional)
//	ORAS_EXTRA_FLAGS, COSIGN_EXTRA_FLAGS  test hooks (for example plain HTTP
//	                  registries); the controller never sets them.

const scriptHeader = `set -eu
umask 077
out=/work/out
fail() { echo "evidence publication: $*" >&2; printf '%s' "$*" > /dev/termination-log 2>/dev/null || true; exit 1; }
is_digest() { printf '%s' "$1" | grep -Eq '^sha256:[0-9a-f]{64}$'; }
`

const orasHeader = scriptHeader + `oras_auth() {
  if [ -s "${REGISTRY_CONFIG:-}" ]; then printf -- '--registry-config %s' "$REGISTRY_CONFIG"; fi
}
`

const cosignHeader = scriptHeader + `if [ -s "${REGISTRY_CONFIG:-}" ]; then
  export DOCKER_CONFIG="$(dirname "$REGISTRY_CONFIG")"
fi
test -s "$COSIGN_KEY_PATH" || fail "cosign key file is missing or empty"
cosign_flags="--yes --tlog-upload=false --use-signing-config=false --key $COSIGN_KEY_PATH ${COSIGN_EXTRA_FLAGS:-}"
`

// PrepareScript verifies and unpacks the evidence bundle (cosign image).
const PrepareScript = scriptHeader + `mkdir -p "$out" /work/home /work/tmp
echo "$BUNDLE_SHA256  /input/bundle.tar.gz" | sha256sum -c - >/dev/null || fail "evidence bundle digest mismatch"
tar xzf /input/bundle.tar.gz -C /work || fail "evidence bundle cannot be extracted"
test -s /work/evidence/sbom.cdx.json || fail "sbom.cdx.json missing in the evidence bundle"
test -s /work/evidence/trivy-report.json || fail "trivy-report.json missing in the evidence bundle"
cp /input/descriptor.json /input/provenance.json "$out/"
echo "evidence bundle sha256:$BUNDLE_SHA256 verified"
`

// PushDescriptorScript pushes the VM image descriptor (oras image).
const PushDescriptorScript = orasHeader + `cd "$out"
# shellcheck disable=SC2046
oras push $(oras_auth) ${ORAS_EXTRA_FLAGS:-} \
  --artifact-type ` + ArtifactType + ` \
  --annotation "org.opencontainers.image.title=$SUBJECT_NAME" \
  --annotation "org.opencontainers.image.version=$SUBJECT_VERSION" \
  --annotation "` + PlatformAnnotation + `=$SUBJECT_PLATFORM" \
  --annotation "io.tsigitops.vmimage.provider=$PROVIDER" \
  --format go-template='{{.digest}} {{.size}} {{.mediaType}}' \
  "$REPO:$DESCRIPTOR_TAG" descriptor.json:` + DescriptorMediaType + ` > push.out || fail "push VM image descriptor to $REPO:$DESCRIPTOR_TAG failed"
read -r digest size media < push.out || true
is_digest "$digest" || fail "oras push returned no digest"
printf '%s' "$digest" > descriptor.digest
printf '%s' "$size" > descriptor.size
printf '%s' "$media" > descriptor.mediatype
echo "descriptor $REPO@$digest"
`

// SignDescriptorScript signs the descriptor and attaches SLSA provenance,
// CycloneDX SBOM and Trivy report as attestations (cosign image).
const SignDescriptorScript = cosignHeader + `digest="$(cat "$out/descriptor.digest")"
is_digest "$digest" || fail "descriptor digest missing"
ref="$REPO@$digest"
# shellcheck disable=SC2086
cosign sign $cosign_flags "$ref" || fail "cosign sign $ref failed"
# shellcheck disable=SC2086
cosign attest $cosign_flags --type ` + PredicateProvenance + ` --predicate "$out/provenance.json" "$ref" || fail "attach SLSA provenance failed"
# shellcheck disable=SC2086
cosign attest $cosign_flags --type cyclonedx --predicate /work/evidence/sbom.cdx.json "$ref" || fail "attach CycloneDX SBOM failed"
# shellcheck disable=SC2086
cosign attest $cosign_flags --type ` + PredicateTrivy + ` --predicate /work/evidence/trivy-report.json "$ref" || fail "attach Trivy report failed"
echo "descriptor signed and attested"
`

// PublishIndexScript merges the descriptor into the OCI index of the logical
// image version (oras image). Every entry is written on its own line so the
// merge works without a JSON processor; an existing tag in another format is
// never overwritten. Concurrent writers are handled by retrying until the own
// entry is still present after a settle delay.
const PublishIndexScript = orasHeader + `cd "$out"
digest="$(cat descriptor.digest)"; size="$(cat descriptor.size)"; media="$(cat descriptor.mediatype)"
is_digest "$digest" || fail "descriptor digest missing"
index_ref="$REPO:$INDEX_TAG"
entry="{\"mediaType\":\"$media\",\"digest\":\"$digest\",\"size\":$size,\"artifactType\":\"` + ArtifactType + `\",\"annotations\":{\"` + PlatformAnnotation + `\":\"$SUBJECT_PLATFORM\",\"org.opencontainers.image.version\":\"$SUBJECT_VERSION\"}}"
platform_key="\"` + PlatformAnnotation + `\":\"$SUBJECT_PLATFORM\""
attempt=0
while :; do
  attempt=$((attempt + 1))
  : > others
  # shellcheck disable=SC2046
  if oras manifest fetch $(oras_auth) ${ORAS_EXTRA_FLAGS:-} "$index_ref" > current.json 2> fetch.err; then
    grep -q '"artifactType":"` + IndexArtifactType + `"' current.json || fail "$index_ref exists and is not an imagebuilder VM image index; refusing to overwrite it"
    grep '^{"mediaType"' current.json | sed 's/,$//' | grep -vF "$platform_key" > others || true
  elif ! grep -qiE 'not ?found|unknown' fetch.err; then
    fail "fetch $index_ref failed: $(tail -c 300 fetch.err)"
  fi
  {
    printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","artifactType":"` + IndexArtifactType + `","manifests":[\n'
    { cat others; printf '%s\n' "$entry"; } | sed '$!s/$/,/'
    printf '],"annotations":{"org.opencontainers.image.title":"%s","org.opencontainers.image.version":"%s"}}\n' "$SUBJECT_NAME" "$SUBJECT_VERSION"
  } > index.json
  # shellcheck disable=SC2046
  oras manifest push $(oras_auth) ${ORAS_EXTRA_FLAGS:-} --media-type application/vnd.oci.image.index.v1+json "$index_ref" index.json >/dev/null || fail "push $index_ref failed"
  sleep $((2 + attempt * 3))
  # shellcheck disable=SC2046
  oras manifest fetch $(oras_auth) ${ORAS_EXTRA_FLAGS:-} "$index_ref" > check.json || fail "fetch $index_ref after push failed"
  if grep -qF "\"digest\":\"$digest\"" check.json; then
    printf 'sha256:%s' "$(sha256sum check.json | cut -d' ' -f1)" > index.digest
    break
  fi
  [ "$attempt" -lt 6 ] || fail "own entry missing in $index_ref after $attempt attempts (concurrent writers)"
  echo "own entry missing in $index_ref after a concurrent update; retrying"
done
echo "index $REPO@$(cat index.digest)"
`

// SignIndexScript signs the index version that contains the descriptor.
const SignIndexScript = cosignHeader + `digest="$(cat "$out/index.digest")"
is_digest "$digest" || fail "index digest missing"
# shellcheck disable=SC2086
cosign sign $cosign_flags "$REPO@$digest" || fail "cosign sign index $REPO@$digest failed"
echo "index signed"
`

// ReportScript discovers the referrers and writes the exact digests as
// termination message for the controller (oras image).
const ReportScript = orasHeader + `cd "$out"
digest="$(cat descriptor.digest)"; index="$(cat index.digest)"
referrer() {
  subject="$1"; predicate="$2"
  # shellcheck disable=SC2046
  for r in $(oras discover $(oras_auth) ${ORAS_EXTRA_FLAGS:-} --format go-template='{{range .referrers}}{{.digest}} {{end}}' "$REPO@$subject"); do
    # shellcheck disable=SC2046
    if oras manifest fetch $(oras_auth) ${ORAS_EXTRA_FLAGS:-} "$REPO@$r" | grep -qF "\"dev.sigstore.bundle.predicateType\":\"$predicate\""; then
      printf '%s' "$r"; return 0
    fi
  done
  return 0
}
sig="$(referrer "$digest" ` + PredicateSignature + `)"
prov="$(referrer "$digest" ` + PredicateProvenance + `)"
sbom="$(referrer "$digest" ` + PredicateCycloneDX + `)"
vuln="$(referrer "$digest" ` + PredicateTrivy + `)"
isig="$(referrer "$index" ` + PredicateSignature + `)"
for d in "$sig" "$prov" "$sbom" "$vuln" "$isig"; do is_digest "$d" || fail "referrer discovery incomplete (signature/provenance/sbom/vulnerability report/index signature)"; done
printf '{"descriptorDigest":"%s","indexDigest":"%s","signatureDigest":"%s","provenanceDigest":"%s","sbomDigest":"%s","vulnerabilityReportDigest":"%s","indexSignatureDigest":"%s"}' \
  "$digest" "$index" "$sig" "$prov" "$sbom" "$vuln" "$isig" > /dev/termination-log
cat /dev/termination-log; echo
`
