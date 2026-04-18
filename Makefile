# Makefile — VM Image Builder Operator
# All targets are Apache 2.0 toolchain only.

BINARY_NAME    := imagebuilder-operator
IMAGE_TAG      ?= dev
REGISTRY       ?= ghcr.io/yourorg
GO             := go
GOFLAGS        ?=

# Tool versions
CONTROLLER_GEN_VERSION := v0.15.0
KUBEBUILDER_VERSION     := 3.15.0

.PHONY: all build test lint generate manifests run docker-build license-check help

all: generate manifests build

## Build

build: ## Build the operator binary
	$(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME) ./cmd/operator/

run: generate manifests ## Run the operator locally (uses current kubeconfig context)
	$(GO) run ./cmd/operator/ \
		--leader-elect=false \
		--max-concurrent-builds=2

docker-build: ## Build the operator Docker image
	docker build -t $(REGISTRY)/$(BINARY_NAME):$(IMAGE_TAG) .

docker-push: ## Push the operator Docker image
	docker push $(REGISTRY)/$(BINARY_NAME):$(IMAGE_TAG)

## Code generation

generate: controller-gen ## Generate DeepCopy methods (run after changing api/v1alpha1/)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests: controller-gen ## Generate CRD YAMLs and RBAC manifests
	$(CONTROLLER_GEN) \
		crd rbac:roleName=imagebuilder-operator webhook \
		paths="./..." \
		output:crd:artifacts:config=config/crd \
		output:rbac:artifacts:config=config/rbac

proto: ## Generate Go code from proto files (requires protoc + protoc-gen-go-grpc)
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/provider/v1/provider.proto

## Testing

test: ## Run unit tests
	$(GO) test ./... -v -count=1

test-integration: ## Run integration tests (requires a running cluster)
	$(GO) test ./... -v -count=1 -tags=integration

lint: ## Run golangci-lint
	golangci-lint run ./...

## Compliance

license-check: ## Check all dependencies are Apache 2.0 / MIT compatible
	go-licenses check ./...

license-report: ## Generate NOTICE file
	go-licenses report ./... > NOTICE
	@echo "NOTICE file updated"

## Installation

install: manifests ## Install CRDs into the cluster
	kubectl apply -f config/crd/

uninstall: ## Remove CRDs from the cluster
	kubectl delete -f config/crd/ --ignore-not-found

deploy: manifests ## Deploy operator to the cluster
	kubectl apply -f config/

undeploy: ## Remove operator from the cluster
	kubectl delete -f config/ --ignore-not-found

## Tools

CONTROLLER_GEN := $(shell which controller-gen 2>/dev/null)
controller-gen:
ifeq ($(CONTROLLER_GEN),)
	$(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
	$(eval CONTROLLER_GEN := $(shell go env GOPATH)/bin/controller-gen)
endif

## Help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
