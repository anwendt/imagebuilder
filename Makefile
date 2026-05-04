# Makefile — VM Image Builder Operator
# All targets are Apache 2.0 toolchain only.

BINARY_NAME    := imagebuilder-operator
BUILDER_BINARY := imagebuilder-builder
UPLOADER_BINARY := imagebuilder-uploader
AWS_PROVIDER_BINARY := imagebuilder-provider-aws
IMAGE_TAG      ?= dev
REGISTRY       ?= ghcr.io/anwendt
AWS_PROVIDER_IMAGE := $(REGISTRY)/$(AWS_PROVIDER_BINARY)
AWS_PROVIDER_PLATFORMS ?= linux/amd64,linux/arm64
AWS_PROVIDER_DIGEST ?=
GO             := go
GOFLAGS        ?=
CGO_ENABLED    := 0
LOCALBIN       ?= $(CURDIR)/bin
API_PATHS      := ./api/...
GEN_PATHS      := ./api/...;./pkg/controller/...

# Tool versions
CONTROLLER_GEN_VERSION := v0.19.0
KUBEBUILDER_VERSION     := 3.15.0
GOSEC_VERSION           := v2.22.10
GOVULNCHECK_VERSION     := v1.1.4
STATICCHECK_VERSION     := 2026.1
GO_LICENSES_VERSION     := v1.6.0

.PHONY: all build build-builder build-uploader build-provider-aws test test-race test-core-e2e test-manifests test-e2e test-e2e-aws lint vet gosec govulncheck staticcheck security-check generate manifests patch-webhook-manifest run docker-build docker-build-builder docker-build-uploader docker-build-provider-aws docker-build-provider-aws-multiarch docker-push-provider-aws docker-digest-provider-aws sign-provider-aws update-aws-provider-samples license-check help deploy-production deploy-observability deploy-policies helm-lint helm-template

all: generate manifests build

## Build

build: ## Build the operator binary (static, reproducible; ADR-004 CGO_ENABLED=0)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(BINARY_NAME) ./cmd/operator/

build-builder: ## Build the build-engine binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(BUILDER_BINARY) ./cmd/builder/

build-uploader: ## Build the upload/register binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(UPLOADER_BINARY) ./cmd/uploader/

build-provider-aws: ## Build the standalone AWS PlatformProvider binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(AWS_PROVIDER_BINARY) ./cmd/provider-aws/

run: generate manifests ## Run the operator locally (uses current kubeconfig context)
	$(GO) run ./cmd/operator/ \
		--leader-elect=false \
		--max-concurrent-builds=2 \
		--max-concurrent-builds-per-node=1

docker-build: ## Build the operator Docker image
	docker build -t $(REGISTRY)/$(BINARY_NAME):$(IMAGE_TAG) .

docker-build-builder: ## Build the build-engine Docker image
	docker build -f Dockerfile.builder -t $(REGISTRY)/$(BUILDER_BINARY):$(IMAGE_TAG) .

docker-build-uploader: ## Build the upload/register Docker image
	docker build -f Dockerfile.uploader -t $(REGISTRY)/$(UPLOADER_BINARY):$(IMAGE_TAG) .

docker-build-provider-aws: ## Build the standalone AWS PlatformProvider Docker image
	docker build -f Dockerfile.provider-aws -t $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) .

docker-build-provider-aws-multiarch: ## Build the standalone AWS PlatformProvider multi-arch image locally via buildx
	docker buildx build \
		--platform $(AWS_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-aws \
		-t $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) \
		.

docker-push-provider-aws: ## Build and push the standalone AWS PlatformProvider multi-arch image
	docker buildx build \
		--platform $(AWS_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-aws \
		-t $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) \
		--push \
		.

docker-digest-provider-aws: ## Print the pushed AWS PlatformProvider image digest
	docker buildx imagetools inspect $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

sign-provider-aws: ## Sign the pushed AWS PlatformProvider image by digest with cosign
	test -n "$(AWS_PROVIDER_DIGEST)" || (echo "AWS_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(AWS_PROVIDER_IMAGE)@$(AWS_PROVIDER_DIGEST)

update-aws-provider-samples: ## Replace AWS PlatformProvider digest placeholders in samples
	test -n "$(AWS_PROVIDER_DIGEST)" || (echo "AWS_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	hack/update-aws-provider-digest.sh "$(AWS_PROVIDER_IMAGE)" "$(AWS_PROVIDER_DIGEST)"

docker-push: ## Push the operator Docker image
	docker push $(REGISTRY)/$(BINARY_NAME):$(IMAGE_TAG)

## Code generation

generate: controller-gen ## Generate DeepCopy methods (run after changing api/v1alpha1/)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="$(API_PATHS)"

manifests: controller-gen ## Generate CRD YAMLs and RBAC manifests
	$(CONTROLLER_GEN) \
		crd rbac:roleName=imagebuilder-operator webhook \
		paths="$(GEN_PATHS)" \
		output:crd:artifacts:config=config/crd \
		output:rbac:artifacts:config=config/rbac
	$(MAKE) patch-webhook-manifest

patch-webhook-manifest: ## Keep generated webhook manifest aligned with production cert-manager deployment
	perl -0pi -e 's/metadata:\n  name: validating-webhook-configuration/metadata:\n  annotations:\n    cert-manager.io\/inject-ca-from: imagebuilder-system\/imagebuilder-webhook-serving-cert\n  name: imagebuilder-validating-webhook-configuration/' config/webhook/manifests.yaml

proto: ## Generate Go code from proto files (requires protoc + protoc-gen-go + protoc-gen-go-grpc)
	protoc \
		--proto_path=api/provider/v1 \
		--go_out=api/provider/v1 --go_opt=paths=source_relative \
		--go-grpc_out=api/provider/v1 --go-grpc_opt=paths=source_relative \
		api/provider/v1/provider.proto
	perl -0pi -e 's/(\/\/ \tprotoc\s+)v[0-9.]+/$${1}normalized/g; s/(\/\/ - protoc\s+)v[0-9.]+/$${1}normalized/g' api/provider/v1/provider.pb.go api/provider/v1/provider_grpc.pb.go

## Testing

test: ## Run unit tests with race detector (CERT-CON-02)
	$(GO) test ./... -v -count=1 -race

test-integration: ## Run integration tests (requires a running cluster or envtest)
	$(GO) test ./... -v -count=1 -race -tags=integration

test-core-e2e: ## Run deterministic mocked-provider core E2E flows
	$(GO) test ./pkg/controller/vmimage -run TestCoreE2E -count=1 -v

test-manifests: ## Validate deployment and Helm chart invariants
	$(GO) test ./test/manifests -count=1 -v

test-e2e: ## Run kind-based smoke test (requires kind + kubectl)
	test/e2e/kind-smoke.sh

test-e2e-aws: ## Run opt-in real AWS remote build E2E test (requires AWS_E2E=1 and AWS_E2E_* env)
	AWS_E2E=1 $(GO) test ./plugins/aws -run TestAWSRemoteBuild_E2E -count=1 -v

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet (OSSF-Q-04, CERT-CON-04)
	$(GO) vet ./...

gosec: ## Run gosec SAST scanner (AS-060, CERT-MSC-04, REQ-010)
	@which gosec > /dev/null 2>&1 || $(GO) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	gosec -exclude-generated $$($(GO) list -f '{{.Dir}}' ./... | grep -v '/templates/')

govulncheck: ## Scan for known CVEs in Go module graph (AS-033, OSSF-S-06)
	@which govulncheck > /dev/null 2>&1 || $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	govulncheck ./...

staticcheck: ## Run staticcheck static analyser (CERT-ERR-01, SAMM-I-SB-03)
	@which staticcheck > /dev/null 2>&1 || $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	staticcheck ./...

security-check: vet gosec staticcheck govulncheck license-check ## Run all security gates (REQ-008, REQ-010)

## Compliance

license-check: ## Check all dependencies are Apache 2.0 / MIT compatible
	@which go-licenses > /dev/null 2>&1 || $(GO) install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
	go-licenses check ./...

license-report: ## Generate NOTICE file
	@which go-licenses > /dev/null 2>&1 || $(GO) install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
	go-licenses report ./... > NOTICE
	@echo "NOTICE file updated"

## Installation

install: manifests ## Install CRDs into the cluster
	kubectl apply -f config/crd/

uninstall: ## Remove CRDs from the cluster
	kubectl delete -f config/crd/ --ignore-not-found

deploy: manifests ## Deploy operator to the cluster
	kubectl apply -f config/crd/
	kubectl apply -f config/deploy/operator.yaml
	kubectl apply -f config/certmanager/webhook-certificate.yaml
	kubectl apply -f config/webhook/manifests.yaml

deploy-observability: ## Deploy Prometheus Operator resources (requires monitoring.coreos.com CRDs)
	kubectl apply -f config/deploy/servicemonitor.yaml
	kubectl apply -f config/deploy/prometheusrule.yaml

deploy-policies: ## Deploy optional hardening policies (requires matching admission controllers)
	kubectl apply -f config/policy/networkpolicies.yaml
	kubectl apply -f config/policy/kyverno-image-signatures.yaml

deploy-production: manifests ## Deploy operator with cert-manager webhook TLS and hardening examples
	kubectl apply -f config/crd/
	kubectl apply -f config/deploy/operator.yaml
	kubectl apply -f config/policy/networkpolicies.yaml
	kubectl apply -f config/certmanager/webhook-certificate.yaml
	kubectl apply -f config/webhook/manifests.yaml

helm-lint: ## Lint Helm chart (requires helm)
	helm lint charts/imagebuilder

helm-template: ## Render Helm chart including CRDs (requires helm)
	helm template imagebuilder charts/imagebuilder --namespace imagebuilder-system --include-crds

undeploy: ## Remove operator from the cluster
	kubectl delete -f config/webhook/manifests.yaml --ignore-not-found
	kubectl delete -f config/certmanager/webhook-certificate.yaml --ignore-not-found
	kubectl delete -f config/deploy/operator.yaml --ignore-not-found
	kubectl delete -f config/crd/ --ignore-not-found

## Tools

CONTROLLER_GEN := $(LOCALBIN)/controller-gen
controller-gen:
	test -s $(CONTROLLER_GEN) || GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

## Help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
