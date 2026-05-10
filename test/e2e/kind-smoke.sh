#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-imagebuilder-e2e}"
NAMESPACE="${NAMESPACE:-imagebuilder-system}"
WEBHOOK_SECRET="${WEBHOOK_SECRET:-imagebuilder-webhook-server-cert}"
OPERATOR_IMAGE="${OPERATOR_IMAGE:-ghcr.io/anwendt/imagebuilder-operator:dev}"
SKIP_OPERATOR_IMAGE_BUILD="${SKIP_OPERATOR_IMAGE_BUILD:-false}"
SMOKE_INSTALL_MODE="${SMOKE_INSTALL_MODE:-static}"

tmpdir=""
cleanup() {
  if [[ -n "${tmpdir}" ]]; then
    rm -rf "${tmpdir}"
  fi
}
trap cleanup EXIT

ensure_webhook_cert_secret() {
  if kubectl -n "${NAMESPACE}" get secret "${WEBHOOK_SECRET}" >/dev/null 2>&1; then
    return
  fi

  tmpdir="$(mktemp -d)"
  openssl req \
    -x509 \
    -nodes \
    -newkey rsa:2048 \
    -keyout "${tmpdir}/tls.key" \
    -out "${tmpdir}/tls.crt" \
    -days 1 \
    -subj "/CN=imagebuilder-webhook-service.${NAMESPACE}.svc" \
    -addext "subjectAltName=DNS:imagebuilder-webhook-service.${NAMESPACE}.svc,DNS:imagebuilder-webhook-service.${NAMESPACE}.svc.cluster.local" \
    >/dev/null 2>&1

  kubectl -n "${NAMESPACE}" create secret tls "${WEBHOOK_SECRET}" \
    --cert="${tmpdir}/tls.crt" \
    --key="${tmpdir}/tls.key"
}

wait_for_cluster_ready() {
  kubectl wait --for=condition=Ready nodes --all --timeout=180s

  for _ in {1..30}; do
    if kubectl get --raw=/readyz >/dev/null 2>&1; then
      return
    fi
    sleep 2
  done

  kubectl get --raw=/readyz
}

ensure_operator_image() {
  if [[ "${SKIP_OPERATOR_IMAGE_BUILD}" != "true" ]]; then
    docker build -t "${OPERATOR_IMAGE}" .
  elif ! docker image inspect "${OPERATOR_IMAGE}" >/dev/null 2>&1; then
    echo "SKIP_OPERATOR_IMAGE_BUILD=true but ${OPERATOR_IMAGE} is not present locally" >&2
    exit 1
  fi

  kind load docker-image "${OPERATOR_IMAGE}" --name "${CLUSTER_NAME}"
}

split_image_ref() {
  local image="$1"

  if [[ "${image}" == *@sha256:* ]]; then
    IMAGE_REPOSITORY="${image%@sha256:*}"
    IMAGE_TAG=""
    IMAGE_DIGEST="sha256:${image##*@sha256:}"
    return
  fi

  local last_segment="${image##*/}"
  if [[ "${last_segment}" == *:* ]]; then
    IMAGE_REPOSITORY="${image%:*}"
    IMAGE_TAG="${image##*:}"
  else
    IMAGE_REPOSITORY="${image}"
    IMAGE_TAG="latest"
  fi
  IMAGE_DIGEST=""
}

assert_rendered_production_helm_invariants() {
  local rendered="${tmpdir}/helm-production.yaml"

  helm template imagebuilder charts/imagebuilder \
    --namespace "${NAMESPACE}" \
    --include-crds \
    >"${rendered}"

  grep -q "failurePolicy: Fail" "${rendered}"
  grep -q "kind: NetworkPolicy" "${rendered}"
  grep -q "kind: ResourceQuota" "${rendered}"
  grep -q "kind: LimitRange" "${rendered}"
  grep -q "kind: ClusterPolicy" "${rendered}"
  grep -q -- "--require-provider-mtls=true" "${rendered}"
  grep -q -- "--require-provider-digest=true" "${rendered}"
  grep -q -- "--require-provider-signature=true" "${rendered}"
}

install_with_static_manifests() {
  kubectl apply -f config/crd/
  kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
  ensure_webhook_cert_secret
  kubectl apply -f config/deploy/operator.yaml
}

install_with_helm() {
  command -v helm >/dev/null 2>&1 || {
    echo "SMOKE_INSTALL_MODE=helm requires helm" >&2
    exit 1
  }

  tmpdir="${tmpdir:-$(mktemp -d)}"
  assert_rendered_production_helm_invariants
  split_image_ref "${OPERATOR_IMAGE}"

  kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
  ensure_webhook_cert_secret

  local ca_bundle
  ca_bundle="$(kubectl -n "${NAMESPACE}" get secret "${WEBHOOK_SECRET}" -o jsonpath='{.data.tls\.crt}')"

  # kind smoke clusters normally do not include cert-manager or Kyverno CRDs.
  # The render check above keeps production policy coverage; this install path
  # disables only those external-CRD resources so the operator can roll out.
  local helm_args=(
    upgrade --install imagebuilder charts/imagebuilder
    --namespace "${NAMESPACE}"
    --create-namespace
    --skip-schema-validation
    --set-string "image.repository=${IMAGE_REPOSITORY}"
    --set "image.pullPolicy=IfNotPresent"
    --set "webhook.certManager.enabled=false"
    --set-string "webhook.caBundle=${ca_bundle}"
    --set "imageSignaturePolicy.enabled=false"
  )
  if [[ -n "${IMAGE_TAG}" ]]; then
    helm_args+=(--set-string "image.tag=${IMAGE_TAG}")
  fi
  if [[ -n "${IMAGE_DIGEST}" ]]; then
    helm_args+=(--set-string "image.digest=${IMAGE_DIGEST}")
  fi

  helm "${helm_args[@]}"
}

kind get clusters | grep -qx "${CLUSTER_NAME}" || kind create cluster --name "${CLUSTER_NAME}"
kubectl config use-context "kind-${CLUSTER_NAME}"
wait_for_cluster_ready
ensure_operator_image

case "${SMOKE_INSTALL_MODE}" in
  static)
    install_with_static_manifests
    ;;
  helm)
    install_with_helm
    ;;
  *)
    echo "unsupported SMOKE_INSTALL_MODE=${SMOKE_INSTALL_MODE}; expected static or helm" >&2
    exit 1
    ;;
esac

kubectl -n "${NAMESPACE}" rollout status deploy/imagebuilder-operator --timeout=120s
kubectl api-resources --api-group=imagebuilder.io | grep -q vmimages
kubectl apply --dry-run=server -f config/samples/vmimage-ubuntu-aws-vsphere.yaml >/dev/null
