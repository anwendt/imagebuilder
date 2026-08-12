# Makefile — VM Image Builder Operator
# All targets are Apache 2.0 toolchain only.

BINARY_NAME    := imagebuilder-operator
BUILDER_BINARY := imagebuilder-builder
UPLOADER_BINARY := imagebuilder-uploader
PROVISIONER_BINARY := imagebuilder-provisioner
AWS_PROVIDER_BINARY := imagebuilder-provider-aws
VSPHERE_PROVIDER_BINARY := imagebuilder-provider-vsphere
AZURE_PROVIDER_BINARY := imagebuilder-provider-azure
OPENSTACK_PROVIDER_BINARY := imagebuilder-provider-openstack
IMAGE_TAG      ?= dev
REGISTRY       ?= ghcr.io/anwendt
OPERATOR_IMAGE := $(REGISTRY)/$(BINARY_NAME)
BUILDER_IMAGE := $(REGISTRY)/$(BUILDER_BINARY)
UPLOADER_IMAGE := $(REGISTRY)/$(UPLOADER_BINARY)
PROVISIONER_ANSIBLE_IMAGE := $(REGISTRY)/imagebuilder-provisioner-ansible
PROVISIONER_CHEF_IMAGE := $(REGISTRY)/imagebuilder-provisioner-chef
PROVISIONER_CUSTOM_IMAGE := $(REGISTRY)/imagebuilder-provisioner-custom
PROVISIONER_PUPPET_IMAGE := $(REGISTRY)/imagebuilder-provisioner-puppet
PROVISIONER_SALTSTACK_IMAGE := $(REGISTRY)/imagebuilder-provisioner-saltstack
AWS_PROVIDER_IMAGE := $(REGISTRY)/$(AWS_PROVIDER_BINARY)
VSPHERE_PROVIDER_IMAGE := $(REGISTRY)/$(VSPHERE_PROVIDER_BINARY)
AZURE_PROVIDER_IMAGE := $(REGISTRY)/$(AZURE_PROVIDER_BINARY)
OPENSTACK_PROVIDER_IMAGE := $(REGISTRY)/$(OPENSTACK_PROVIDER_BINARY)
CORE_IMAGE_PLATFORMS ?= linux/amd64,linux/arm64
PROVISIONER_IMAGE_PLATFORMS ?= linux/amd64,linux/arm64
AWS_PROVIDER_PLATFORMS ?= linux/amd64,linux/arm64
VSPHERE_PROVIDER_PLATFORMS ?= linux/amd64,linux/arm64
AZURE_PROVIDER_PLATFORMS ?= linux/amd64,linux/arm64
OPENSTACK_PROVIDER_PLATFORMS ?= linux/amd64,linux/arm64
OPERATOR_DIGEST ?=
BUILDER_DIGEST ?=
UPLOADER_DIGEST ?=
AWS_PROVIDER_DIGEST ?=
VSPHERE_PROVIDER_DIGEST ?=
AZURE_PROVIDER_DIGEST ?=
OPENSTACK_PROVIDER_DIGEST ?=
GO             := go
GOFLAGS        ?=
CGO_ENABLED    := 0
LOCALBIN       ?= $(CURDIR)/bin
API_PATHS      := ./api/...
GEN_PATHS      := ./api/...;./pkg/controller/...

# Tool versions
CONTROLLER_GEN_VERSION := v0.21.0
KUBEBUILDER_VERSION     := 3.15.0
GOSEC_VERSION           := v2.26.1
GOVULNCHECK_VERSION     := v1.3.0
STATICCHECK_VERSION     := 2026.1
GO_LICENSES_VERSION     := v1.6.0

.PHONY: all build build-builder build-uploader build-provisioner build-provider-aws build-provider-vsphere build-provider-azure build-provider-openstack test test-race test-core-e2e test-manifests test-vsphere-simulator test-e2e test-e2e-production test-e2e-aws test-e2e-aws-tomcat test-e2e-aws-ubuntu24 test-e2e-azure test-e2e-azure-tomcat test-e2e-azure-ubuntu24 test-e2e-azure-tomcat-prep test-e2e-azure-tomcat-run-clean test-e2e-azure-tomcat-cleanup test-e2e-vsphere test-e2e-vsphere-tomcat test-e2e-vsphere-ubuntu24 test-e2e-openstack test-e2e-openstack-tomcat test-e2e-open-telekom-cloud-ubuntu24 test-e2e-windows-cloudbase lint vet verify-deps gosec govulncheck staticcheck security-check generate manifests patch-webhook-manifest run docker-build docker-build-builder docker-build-uploader docker-build-provisioner-ansible docker-build-provisioner-chef docker-build-provisioner-custom docker-build-provisioner-puppet docker-build-provisioner-saltstack docker-build-provisioners docker-build-provider-aws docker-build-provider-vsphere docker-build-provider-azure docker-build-provider-openstack docker-build-core-multiarch docker-build-provisioners-multiarch docker-build-provider-aws-multiarch docker-build-provider-vsphere-multiarch docker-build-provider-azure-multiarch docker-build-provider-openstack-multiarch docker-push-core docker-push-builder docker-push-uploader docker-push-provisioners docker-push-provider-aws docker-push-provider-vsphere docker-push-provider-azure docker-push-provider-openstack docker-digest-core docker-digest-builder docker-digest-uploader docker-digest-provider-aws docker-digest-provider-vsphere docker-digest-provider-azure docker-digest-provider-openstack sign-core sign-builder sign-uploader sign-provider-aws sign-provider-vsphere sign-provider-azure sign-provider-openstack update-provider-samples update-aws-provider-samples update-vsphere-provider-samples update-azure-provider-samples update-openstack-provider-samples license-check help deploy-production deploy-observability deploy-policies helm-lint helm-template

all: generate manifests build

## Build

build: ## Build the operator binary (static, reproducible; ADR-004 CGO_ENABLED=0)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(BINARY_NAME) ./cmd/operator/

build-builder: ## Build the build-engine binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(BUILDER_BINARY) ./cmd/builder/

build-uploader: ## Build the upload/register binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(UPLOADER_BINARY) ./cmd/uploader/

build-provisioner: ## Build the generic ADR-003 provisioner runner binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(PROVISIONER_BINARY) ./cmd/provisioner/

build-provider-aws: ## Build the standalone AWS PlatformProvider binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(AWS_PROVIDER_BINARY) ./cmd/provider-aws/

build-provider-vsphere: ## Build the standalone vSphere PlatformProvider binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(VSPHERE_PROVIDER_BINARY) ./cmd/provider-vsphere/

build-provider-azure: ## Build the standalone Azure PlatformProvider binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(AZURE_PROVIDER_BINARY) ./cmd/provider-azure/

build-provider-openstack: ## Build the standalone OpenStack PlatformProvider binary
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -o bin/$(OPENSTACK_PROVIDER_BINARY) ./cmd/provider-openstack/

run: generate manifests ## Run the operator locally (uses current kubeconfig context)
	$(GO) run ./cmd/operator/ \
		--leader-elect=false \
		--max-concurrent-builds=2

docker-build: ## Build the operator Docker image
	docker build -t $(OPERATOR_IMAGE):$(IMAGE_TAG) .

docker-build-builder: ## Build the build-engine Docker image
	docker build -f Dockerfile.builder -t $(BUILDER_IMAGE):$(IMAGE_TAG) .

docker-build-uploader: ## Build the upload/register Docker image
	docker build -f Dockerfile.uploader -t $(UPLOADER_IMAGE):$(IMAGE_TAG) .

docker-build-provisioner-ansible: ## Build the Ansible provisioner image
	docker build -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=ansible -t $(PROVISIONER_ANSIBLE_IMAGE):$(IMAGE_TAG) .

docker-build-provisioner-chef: ## Build the Chef provisioner image
	docker build -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=chef -t $(PROVISIONER_CHEF_IMAGE):$(IMAGE_TAG) .

docker-build-provisioner-custom: ## Build the custom provisioner image
	docker build -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=custom -t $(PROVISIONER_CUSTOM_IMAGE):$(IMAGE_TAG) .

docker-build-provisioner-puppet: ## Build the Puppet provisioner image
	docker build -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=puppet -t $(PROVISIONER_PUPPET_IMAGE):$(IMAGE_TAG) .

docker-build-provisioner-saltstack: ## Build the SaltStack provisioner image
	docker build -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=saltstack -t $(PROVISIONER_SALTSTACK_IMAGE):$(IMAGE_TAG) .

docker-build-provisioners: docker-build-provisioner-ansible docker-build-provisioner-chef docker-build-provisioner-custom docker-build-provisioner-puppet docker-build-provisioner-saltstack ## Build all provisioner images

docker-build-provider-aws: ## Build the standalone AWS PlatformProvider Docker image
	docker build -f Dockerfile.provider-aws -t $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) .

docker-build-provider-vsphere: ## Build the standalone vSphere PlatformProvider Docker image
	docker build -f Dockerfile.provider-vsphere -t $(VSPHERE_PROVIDER_IMAGE):$(IMAGE_TAG) .

docker-build-provider-azure: ## Build the standalone Azure PlatformProvider Docker image
	docker build -f Dockerfile.provider-azure -t $(AZURE_PROVIDER_IMAGE):$(IMAGE_TAG) .

docker-build-provider-openstack: ## Build the standalone OpenStack PlatformProvider Docker image
	docker build -f Dockerfile.provider-openstack -t $(OPENSTACK_PROVIDER_IMAGE):$(IMAGE_TAG) .

docker-build-core-multiarch: ## Build operator, builder, and uploader multi-arch images locally via buildx
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -t $(OPERATOR_IMAGE):$(IMAGE_TAG) .
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -f Dockerfile.builder -t $(BUILDER_IMAGE):$(IMAGE_TAG) .
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -f Dockerfile.uploader -t $(UPLOADER_IMAGE):$(IMAGE_TAG) .

docker-build-provisioners-multiarch: ## Build provisioner multi-arch images locally via buildx
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=ansible -t $(PROVISIONER_ANSIBLE_IMAGE):$(IMAGE_TAG) .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=chef -t $(PROVISIONER_CHEF_IMAGE):$(IMAGE_TAG) .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=custom -t $(PROVISIONER_CUSTOM_IMAGE):$(IMAGE_TAG) .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=puppet -t $(PROVISIONER_PUPPET_IMAGE):$(IMAGE_TAG) .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=saltstack -t $(PROVISIONER_SALTSTACK_IMAGE):$(IMAGE_TAG) .

docker-build-provider-aws-multiarch: ## Build the standalone AWS PlatformProvider multi-arch image locally via buildx
	docker buildx build \
		--platform $(AWS_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-aws \
		-t $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) \
		.

docker-build-provider-vsphere-multiarch: ## Build the standalone vSphere PlatformProvider multi-arch image locally via buildx
	docker buildx build \
		--platform $(VSPHERE_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-vsphere \
		-t $(VSPHERE_PROVIDER_IMAGE):$(IMAGE_TAG) \
		.

docker-build-provider-azure-multiarch: ## Build the standalone Azure PlatformProvider multi-arch image locally via buildx
	docker buildx build \
		--platform $(AZURE_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-azure \
		-t $(AZURE_PROVIDER_IMAGE):$(IMAGE_TAG) \
		.

docker-build-provider-openstack-multiarch: ## Build the standalone OpenStack PlatformProvider multi-arch image locally via buildx
	docker buildx build \
		--platform $(OPENSTACK_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-openstack \
		-t $(OPENSTACK_PROVIDER_IMAGE):$(IMAGE_TAG) \
		.

docker-push-provider-aws: ## Build and push the standalone AWS PlatformProvider multi-arch image
	docker buildx build \
		--platform $(AWS_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-aws \
		-t $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) \
		--push \
		.

docker-push-provider-vsphere: ## Build and push the standalone vSphere PlatformProvider multi-arch image
	docker buildx build \
		--platform $(VSPHERE_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-vsphere \
		-t $(VSPHERE_PROVIDER_IMAGE):$(IMAGE_TAG) \
		--push \
		.

docker-push-provider-azure: ## Build and push the standalone Azure PlatformProvider multi-arch image
	docker buildx build \
		--platform $(AZURE_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-azure \
		-t $(AZURE_PROVIDER_IMAGE):$(IMAGE_TAG) \
		--push \
		.

docker-push-provider-openstack: ## Build and push the standalone OpenStack PlatformProvider multi-arch image
	docker buildx build \
		--platform $(OPENSTACK_PROVIDER_PLATFORMS) \
		-f Dockerfile.provider-openstack \
		-t $(OPENSTACK_PROVIDER_IMAGE):$(IMAGE_TAG) \
		--push \
		.

docker-push-core: ## Build and push operator, builder, and uploader multi-arch images
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -t $(OPERATOR_IMAGE):$(IMAGE_TAG) --push .
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -f Dockerfile.builder -t $(BUILDER_IMAGE):$(IMAGE_TAG) --push .
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -f Dockerfile.uploader -t $(UPLOADER_IMAGE):$(IMAGE_TAG) --push .

docker-push-provisioners: ## Build and push provisioner multi-arch images
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=ansible -t $(PROVISIONER_ANSIBLE_IMAGE):$(IMAGE_TAG) --push .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=chef -t $(PROVISIONER_CHEF_IMAGE):$(IMAGE_TAG) --push .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=custom -t $(PROVISIONER_CUSTOM_IMAGE):$(IMAGE_TAG) --push .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=puppet -t $(PROVISIONER_PUPPET_IMAGE):$(IMAGE_TAG) --push .
	docker buildx build --platform $(PROVISIONER_IMAGE_PLATFORMS) -f Dockerfile.provisioner --build-arg PROVISIONER_TYPE=saltstack -t $(PROVISIONER_SALTSTACK_IMAGE):$(IMAGE_TAG) --push .

docker-push-builder: ## Build and push the build-engine multi-arch image
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -f Dockerfile.builder -t $(BUILDER_IMAGE):$(IMAGE_TAG) --push .

docker-push-uploader: ## Build and push the upload/register multi-arch image
	docker buildx build --platform $(CORE_IMAGE_PLATFORMS) -f Dockerfile.uploader -t $(UPLOADER_IMAGE):$(IMAGE_TAG) --push .

docker-digest-core: ## Print pushed operator, builder, and uploader image digests
	@printf "operator "
	@docker buildx imagetools inspect $(OPERATOR_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'
	@printf "builder "
	@docker buildx imagetools inspect $(BUILDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'
	@printf "uploader "
	@docker buildx imagetools inspect $(UPLOADER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

docker-digest-builder: ## Print the pushed build-engine image digest
	docker buildx imagetools inspect $(BUILDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

docker-digest-uploader: ## Print the pushed upload/register image digest
	docker buildx imagetools inspect $(UPLOADER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

docker-digest-provider-aws: ## Print the pushed AWS PlatformProvider image digest
	docker buildx imagetools inspect $(AWS_PROVIDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

docker-digest-provider-vsphere: ## Print the pushed vSphere PlatformProvider image digest
	docker buildx imagetools inspect $(VSPHERE_PROVIDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

docker-digest-provider-azure: ## Print the pushed Azure PlatformProvider image digest
	docker buildx imagetools inspect $(AZURE_PROVIDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

docker-digest-provider-openstack: ## Print the pushed OpenStack PlatformProvider image digest
	docker buildx imagetools inspect $(OPENSTACK_PROVIDER_IMAGE):$(IMAGE_TAG) --format '{{.Manifest.Digest}}'

sign-provider-aws: ## Sign the pushed AWS PlatformProvider image by digest with cosign
	test -n "$(AWS_PROVIDER_DIGEST)" || (echo "AWS_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(AWS_PROVIDER_IMAGE)@$(AWS_PROVIDER_DIGEST)

sign-provider-vsphere: ## Sign the pushed vSphere PlatformProvider image by digest with cosign
	test -n "$(VSPHERE_PROVIDER_DIGEST)" || (echo "VSPHERE_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(VSPHERE_PROVIDER_IMAGE)@$(VSPHERE_PROVIDER_DIGEST)

sign-provider-azure: ## Sign the pushed Azure PlatformProvider image by digest with cosign
	test -n "$(AZURE_PROVIDER_DIGEST)" || (echo "AZURE_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(AZURE_PROVIDER_IMAGE)@$(AZURE_PROVIDER_DIGEST)

sign-provider-openstack: ## Sign the pushed OpenStack PlatformProvider image by digest with cosign
	test -n "$(OPENSTACK_PROVIDER_DIGEST)" || (echo "OPENSTACK_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(OPENSTACK_PROVIDER_IMAGE)@$(OPENSTACK_PROVIDER_DIGEST)

sign-core: ## Sign the pushed operator, builder, and uploader images by digest with cosign
	test -n "$(OPERATOR_DIGEST)" || (echo "OPERATOR_DIGEST is required, e.g. sha256:..." && exit 1)
	test -n "$(BUILDER_DIGEST)" || (echo "BUILDER_DIGEST is required, e.g. sha256:..." && exit 1)
	test -n "$(UPLOADER_DIGEST)" || (echo "UPLOADER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(OPERATOR_IMAGE)@$(OPERATOR_DIGEST)
	cosign sign $(BUILDER_IMAGE)@$(BUILDER_DIGEST)
	cosign sign $(UPLOADER_IMAGE)@$(UPLOADER_DIGEST)

sign-builder: ## Sign the pushed build-engine image by digest with cosign
	test -n "$(BUILDER_DIGEST)" || (echo "BUILDER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(BUILDER_IMAGE)@$(BUILDER_DIGEST)

sign-uploader: ## Sign the pushed upload/register image by digest with cosign
	test -n "$(UPLOADER_DIGEST)" || (echo "UPLOADER_DIGEST is required, e.g. sha256:..." && exit 1)
	cosign sign $(UPLOADER_IMAGE)@$(UPLOADER_DIGEST)

update-provider-samples: ## Replace PlatformProvider digest placeholders in samples (PROVIDER=aws|vsphere|azure|openstack)
	test -n "$(PROVIDER)" || (echo "PROVIDER is required, e.g. aws, vsphere, azure, or openstack" && exit 1)
	test -n "$(PROVIDER_IMAGE)" || (echo "PROVIDER_IMAGE is required, e.g. ghcr.io/anwendt/imagebuilder-provider-aws" && exit 1)
	test -n "$(PROVIDER_DIGEST)" || (echo "PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	hack/update-provider-digest.sh "$(PROVIDER)" "$(PROVIDER_IMAGE)" "$(PROVIDER_DIGEST)"

update-aws-provider-samples: ## Replace AWS PlatformProvider digest placeholders in samples
	test -n "$(AWS_PROVIDER_DIGEST)" || (echo "AWS_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	hack/update-provider-digest.sh aws "$(AWS_PROVIDER_IMAGE)" "$(AWS_PROVIDER_DIGEST)"

update-vsphere-provider-samples: ## Replace vSphere PlatformProvider digest placeholders in samples
	test -n "$(VSPHERE_PROVIDER_DIGEST)" || (echo "VSPHERE_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	hack/update-provider-digest.sh vsphere "$(VSPHERE_PROVIDER_IMAGE)" "$(VSPHERE_PROVIDER_DIGEST)"

update-azure-provider-samples: ## Replace Azure PlatformProvider digest placeholders in samples
	test -n "$(AZURE_PROVIDER_DIGEST)" || (echo "AZURE_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	hack/update-provider-digest.sh azure "$(AZURE_PROVIDER_IMAGE)" "$(AZURE_PROVIDER_DIGEST)"

update-openstack-provider-samples: ## Replace OpenStack PlatformProvider digest placeholders in samples
	test -n "$(OPENSTACK_PROVIDER_DIGEST)" || (echo "OPENSTACK_PROVIDER_DIGEST is required, e.g. sha256:..." && exit 1)
	hack/update-provider-digest.sh openstack "$(OPENSTACK_PROVIDER_IMAGE)" "$(OPENSTACK_PROVIDER_DIGEST)"

docker-push: ## Push the operator Docker image
	docker push $(OPERATOR_IMAGE):$(IMAGE_TAG)

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

test-e2e-production: ## Run kind-based Helm production smoke test (requires kind + kubectl + helm)
	SMOKE_INSTALL_MODE=helm test/e2e/kind-smoke.sh

test-e2e-aws: ## Run opt-in real AWS remote build E2E test (requires AWS_E2E=1 and AWS_E2E_* env)
	AWS_E2E=1 $(GO) test ./plugins/aws -run TestAWSRemoteBuild_E2E -count=1 -v

test-e2e-aws-tomcat: ## Run opt-in real AWS remote build E2E with a tar-based Apache Tomcat workload
	AWS_E2E=1 AWS_E2E_WORKLOAD=tomcat $(GO) test ./plugins/aws -run TestAWSRemoteBuild_E2E -count=1 -v -timeout=75m

test-e2e-aws-ubuntu24: ## Run opt-in real AWS remote build E2E from Ubuntu 24.04 latest Marketplace source
	AWS_E2E=1 AWS_E2E_WORKLOAD=ubuntu24 $(GO) test ./plugins/aws -run TestAWSRemoteBuildUbuntuLatest_E2E -count=1 -v -timeout=75m

test-e2e-azure: ## Run opt-in real Azure provider E2E test (requires AZURE_E2E=1 and AZURE_E2E_* env)
	AZURE_E2E=1 $(GO) test ./plugins/azure -run TestAzureProviderLive_E2E -count=1 -v -timeout=60m

test-e2e-azure-tomcat: ## Run opt-in real Azure remote build E2E with a tar-based Apache Tomcat workload
	AZURE_E2E=1 AZURE_E2E_WORKLOAD=tomcat $(GO) test ./plugins/azure -run TestAzureRemoteBuildTomcat_E2E -count=1 -v -timeout=90m

test-e2e-azure-ubuntu24: ## Run opt-in real Azure remote build E2E from Ubuntu 24.04 latest Marketplace source
	AZURE_E2E=1 AZURE_E2E_WORKLOAD=ubuntu24 $(GO) test ./plugins/azure -run TestAzureRemoteBuildUbuntuLatest_E2E -count=1 -v -timeout=90m

test-e2e-azure-tomcat-prep: ## Prepare Azure resources for the Tomcat E2E helper
	test/e2e/azure-tomcat-e2e.sh prep

test-e2e-azure-tomcat-run-clean: ## Run Azure Tomcat E2E and clean test resources without deleting the resource group
	test/e2e/azure-tomcat-e2e.sh run-clean

test-e2e-azure-tomcat-cleanup: ## Clean Azure Tomcat E2E resources without deleting the resource group
	test/e2e/azure-tomcat-e2e.sh cleanup

test-e2e-vsphere: ## Run opt-in real vSphere provider E2E test (requires VSPHERE_E2E=1 and VSPHERE_E2E_* env)
	VSPHERE_E2E=1 $(GO) test ./plugins/vsphere -run TestVSphereProviderLive_E2E -count=1 -v -timeout=60m

test-e2e-vsphere-tomcat: ## Run opt-in real vSphere remote build E2E with a tar-based Apache Tomcat workload
	VSPHERE_E2E=1 VSPHERE_E2E_WORKLOAD=tomcat $(GO) test ./plugins/vsphere -run TestVSphereRemoteBuildTomcat_E2E -count=1 -v -timeout=90m

test-e2e-vsphere-ubuntu24: ## Run opt-in real vSphere E2E from Ubuntu 24.04 latest marketplace mapping
	VSPHERE_E2E=1 VSPHERE_E2E_WORKLOAD=ubuntu24 $(GO) test ./plugins/vsphere -run TestVSphereRemoteBuildUbuntuLatest_E2E -count=1 -v -timeout=90m

test-e2e-openstack: ## Run opt-in real OpenStack remote build E2E test (requires OPENSTACK_E2E=1 and OPENSTACK_E2E_* env)
	OPENSTACK_E2E=1 $(GO) test ./plugins/openstack -run TestOpenStackRemoteBuild_E2E -count=1 -v -timeout=60m

test-e2e-openstack-tomcat: ## Run opt-in real OpenStack remote build E2E with a tar-based Apache Tomcat workload
	OPENSTACK_E2E=1 OPENSTACK_E2E_WORKLOAD=tomcat $(GO) test ./plugins/openstack -run TestOpenStackRemoteBuild_E2E -count=1 -v -timeout=75m

test-e2e-open-telekom-cloud-ubuntu24: ## Run opt-in real Open Telekom Cloud E2E from Ubuntu 24.04 latest Glance source
	OPENSTACK_E2E=1 OPENSTACK_E2E_WORKLOAD=ubuntu24 $(GO) test ./plugins/openstack -run TestOpenTelekomCloudUbuntuLatest_E2E -count=1 -v -timeout=75m

test-e2e-windows-cloudbase: ## Run opt-in live Windows ISO Cloudbase-Init/Sysprep E2E test (requires IMAGEBUILDER_WINDOWS_E2E=1 and local QEMU)
	IMAGEBUILDER_WINDOWS_E2E=1 $(GO) test ./pkg/builder -run TestQEMUISOBackend_WindowsCloudbaseInitSysprep_E2E -count=1 -v -timeout=4h

test-vsphere-simulator: ## Run opt-in vSphere provider test against govmomi simulator
	IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS=1 $(GO) test ./plugins/vsphere -run TestGovmomiClientUploadCleanupWithSimulator -count=1 -v

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet (OSSF-Q-04, CERT-CON-04)
	$(GO) vet ./...

verify-deps: ## Verify downloaded Go modules against go.sum (SC-018)
	$(GO) mod verify

gosec: ## Run gosec SAST scanner (AS-060, CERT-MSC-04, REQ-010)
	GOBIN=$(LOCALBIN) $(GO) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	$(LOCALBIN)/gosec -exclude-generated $$($(GO) list -f '{{.Dir}}' ./... | grep -v '/templates/')

govulncheck: ## Scan for known CVEs in Go module graph (AS-033, OSSF-S-06)
	GOBIN=$(LOCALBIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(LOCALBIN)/govulncheck ./...

staticcheck: ## Run staticcheck static analyser (CERT-ERR-01, SAMM-I-SB-03)
	GOBIN=$(LOCALBIN) $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	$(LOCALBIN)/staticcheck ./...

security-check: vet verify-deps gosec staticcheck govulncheck license-check ## Run all security gates (REQ-008, REQ-010)

## Compliance

license-check: ## Check all dependencies are Apache 2.0 / MIT compatible
	GOBIN=$(LOCALBIN) $(GO) install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
	$(LOCALBIN)/go-licenses check ./...

license-report: ## Generate NOTICE file (run before each release; LR-006)
	GOBIN=$(LOCALBIN) $(GO) install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
	@{ \
	  printf 'VM Image Builder — Third-Party Software Notices\n'; \
	  printf 'License: Apache License 2.0  (https://www.apache.org/licenses/LICENSE-2.0)\n'; \
	  printf 'Generated by go-licenses. Format: package,license-url,license-identifier\n\n'; \
	  $(LOCALBIN)/go-licenses report ./...; \
	} > NOTICE
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

helm-template-dev: ## Render Helm chart with development values (requires helm)
	helm template imagebuilder charts/imagebuilder --namespace imagebuilder-system --include-crds -f charts/imagebuilder/values-development.yaml

helm-package: ## Package Helm chart into dist/ (requires helm)
	mkdir -p dist
	helm package charts/imagebuilder --destination dist

helm-push: helm-package ## Push Helm chart to OCI registry, e.g. HELM_REGISTRY=oci://ghcr.io/anwendt/charts
	test -n "$(HELM_REGISTRY)" || (echo "HELM_REGISTRY is required, e.g. oci://ghcr.io/anwendt/charts" && exit 1)
	helm push dist/imagebuilder-*.tgz $(HELM_REGISTRY)

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
