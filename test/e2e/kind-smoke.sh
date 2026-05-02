#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-imagebuilder-e2e}"
NAMESPACE="${NAMESPACE:-imagebuilder-system}"
WEBHOOK_SECRET="${WEBHOOK_SECRET:-imagebuilder-webhook-server-cert}"
OPERATOR_IMAGE="${OPERATOR_IMAGE:-ghcr.io/anwendt/imagebuilder-operator:dev}"
SKIP_OPERATOR_IMAGE_BUILD="${SKIP_OPERATOR_IMAGE_BUILD:-false}"

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

kind get clusters | grep -qx "${CLUSTER_NAME}" || kind create cluster --name "${CLUSTER_NAME}"
kubectl config use-context "kind-${CLUSTER_NAME}"
wait_for_cluster_ready
ensure_operator_image

kubectl apply -f config/crd/
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
ensure_webhook_cert_secret
kubectl apply -f config/deploy/operator.yaml

kubectl -n "${NAMESPACE}" rollout status deploy/imagebuilder-operator --timeout=120s
kubectl api-resources --api-group=imagebuilder.io | grep -q vmimages
kubectl apply --dry-run=server -f config/samples/vmimage-ubuntu-aws-vsphere.yaml >/dev/null
