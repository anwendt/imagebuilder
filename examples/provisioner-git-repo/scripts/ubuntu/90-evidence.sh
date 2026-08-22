#!/usr/bin/env bash
set -euo pipefail

# This script is intentionally the final provisioner. The Azure provider injects
# the IMAGEBUILDER_* policy variables below. Registry authentication and the
# Cosign KMS key reference must be prepared through workload identity or a prior
# centrally managed provisioner; no registry password or private key belongs in
# the VMImage manifest.

required_commands=(base64 cosign jq oras sha256sum syft trivy)
for command_name in "${required_commands[@]}"; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    printf 'required evidence command is unavailable: %s\n' "${command_name}" >&2
    exit 1
  fi
done

: "${IMAGEBUILDER_BUILD_ID:?provider must inject IMAGEBUILDER_BUILD_ID}"
: "${IMAGEBUILDER_IMAGE_NAME:?provider must inject IMAGEBUILDER_IMAGE_NAME}"
: "${IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY:?provider must inject the evidence repository}"
: "${IMAGEBUILDER_EVIDENCE_COSIGN_KEY_REF:?set to a Cosign KMS URI such as azurekms://vault/key}"

repository="${IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY#oci://}"
severity_csv="${IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY:-HIGH,CRITICAL}"
source_ref="${IMAGEBUILDER_SOURCE_REF:-unknown}"
image_slug="$(printf '%s' "${IMAGEBUILDER_IMAGE_NAME}" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9._-' '-')"
build_slug="$(printf '%s' "${IMAGEBUILDER_BUILD_ID}" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9._-' '-')"
work_dir="$(mktemp -d /tmp/imagebuilder-evidence.XXXXXX)"
trap 'rm -rf "${work_dir}"' EXIT

sbom_path="${work_dir}/sbom.spdx.json"
vulnerability_path="${work_dir}/vulnerability-report.json"
provenance_path="${work_dir}/provenance.json"
bundle_path="${work_dir}/provenance.bundle.json"
result_path="${work_dir}/result.json"

syft dir:/ --output "spdx-json=${sbom_path}"
trivy rootfs \
  --format json \
  --output "${vulnerability_path}" \
  --severity "${severity_csv}" \
  --scanners vuln \
  /

severity_json="$(printf '%s' "${severity_csv}" | jq -Rc 'split(",") | map(ascii_upcase)')"
blocked_findings="$(jq --argjson blocked "${severity_json}" '
  [.Results[]?.Vulnerabilities[]? | select(.Severity as $severity | $blocked | index($severity))] | length
' "${vulnerability_path}")"

sbom_digest="$(sha256sum "${sbom_path}" | awk '{print $1}')"
vulnerability_digest="$(sha256sum "${vulnerability_path}" | awk '{print $1}')"
subject_digest="$(printf '%s\n%s\n%s\n%s\n' \
  "${IMAGEBUILDER_BUILD_ID}" "${source_ref}" "${sbom_digest}" "${vulnerability_digest}" |
  sha256sum | awk '{print $1}')"
generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

jq -n \
  --arg image "${IMAGEBUILDER_IMAGE_NAME}" \
  --arg build_id "${IMAGEBUILDER_BUILD_ID}" \
  --arg source_ref "${source_ref}" \
  --arg subject_digest "${subject_digest}" \
  --arg sbom_digest "${sbom_digest}" \
  --arg vulnerability_digest "${vulnerability_digest}" \
  --arg generated_at "${generated_at}" \
  '{
    _type: "https://in-toto.io/Statement/v1",
    subject: [{name: $image, digest: {sha256: $subject_digest}}],
    predicateType: "https://slsa.dev/provenance/v1",
    predicate: {
      buildDefinition: {
        buildType: "https://imagebuilder.io/buildtypes/azure-remote-v1",
        externalParameters: {buildId: $build_id, sourceRef: $source_ref},
        internalParameters: {sbomSha256: $sbom_digest, vulnerabilityReportSha256: $vulnerability_digest},
        resolvedDependencies: []
      },
      runDetails: {
        builder: {id: "https://imagebuilder.io/providers/azure"},
        metadata: {invocationId: $build_id, startedOn: $generated_at, finishedOn: $generated_at}
      }
    }
  }' >"${provenance_path}"

COSIGN_EXPERIMENTAL=1 cosign sign-blob \
  --yes \
  --key "${IMAGEBUILDER_EVIDENCE_COSIGN_KEY_REF}" \
  --bundle "${bundle_path}" \
  "${provenance_path}" >/dev/null

publish_document() {
  local kind="$1"
  local file_path="$2"
  local media_type="$3"
  local artifact_type="$4"
  local target_repository="${repository}/${image_slug}/${build_slug}/${kind}"
  local mutable_push_ref="${target_repository}:v1"
  local descriptor_digest

  oras push \
    --artifact-type "${artifact_type}" \
    "${mutable_push_ref}" \
    "${file_path}:${media_type}" >/dev/null
  descriptor_digest="$(oras manifest fetch --descriptor "${mutable_push_ref}" | jq -er '.digest')"
  if [[ ! "${descriptor_digest}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    printf 'registry returned an invalid descriptor digest for %s: %s\n' "${kind}" "${descriptor_digest}" >&2
    exit 1
  fi
  printf 'oci://%s@%s' "${target_repository}" "${descriptor_digest}"
}

sbom_ref="$(publish_document \
  sbom "${sbom_path}" application/spdx+json application/vnd.imagebuilder.sbom.v1)"
vulnerability_ref="$(publish_document \
  vulnerability "${vulnerability_path}" application/vnd.aquasec.trivy.report+json application/vnd.imagebuilder.vulnerability-report.v1)"
provenance_ref="$(publish_document \
  provenance "${provenance_path}" application/vnd.in-toto+json application/vnd.imagebuilder.provenance.v1)"
signature_ref="$(publish_document \
  signature "${bundle_path}" application/vnd.dev.sigstore.bundle+json application/vnd.imagebuilder.signature.v1)"

status="passed"
message="SBOM, vulnerability report, signed provenance, and signature bundle published"
if (( blocked_findings > 0 )); then
  status="failed"
  message="${blocked_findings} vulnerability finding(s) matched the blocking severity policy"
fi

jq -n \
  --arg status "${status}" \
  --arg message "${message}" \
  --arg sbom_ref "${sbom_ref}" \
  --arg vulnerability_ref "${vulnerability_ref}" \
  --arg provenance_ref "${provenance_ref}" \
  --arg signature_ref "${signature_ref}" \
  '{
    status: $status,
    message: $message,
    sbomRef: $sbom_ref,
    vulnerabilityReportRef: $vulnerability_ref,
    provenanceRef: $provenance_ref,
    signatureRef: $signature_ref
  }' >"${result_path}"

printf 'IMAGEBUILDER_EVIDENCE_V1=%s\n' "$(base64 -w0 "${result_path}")"
