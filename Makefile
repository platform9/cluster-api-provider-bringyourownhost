#Ensure Make is run with bash shell as some syntax below is bash-specific
SHELL:=/usr/bin/env bash

# GO_VERSION is derived from .tool-versions (the asdf-managed source of
# truth) so there is exactly one place in this Makefile that declares it.
GO_VERSION := $(shell awk '/^golang /{print $$2}' .tool-versions)
# Evil hack to fix "make lint". Otherwise it tries to re-install
export GOTOOLCHAIN := go$(GO_VERSION)

# Define registries
STAGING_REGISTRY ?= quay.io/platform9/cluster-api-provider-bringyourownhost

IMAGE_NAME ?= controller-manager
TAG ?= dev
RELEASE_DIR := _dist

# Image URL to use all building/pushing image targets
IMG ?= ${STAGING_REGISTRY}/${IMAGE_NAME}:${TAG}
BYOH_BASE_IMG = byoh/node:e2e
BYOH_BASE_IMG_DEV = byoh/node:dev
LINUX_VM_IMG = byoh/linux-test-runner:dev
PACKAGING_TEST_RPM_IMG = byoh/packaging-test-rocky:dev
PICT_IMG = byoh/pict:dev
PICT_DIR = $(REPO_ROOT)/test/e2e/pict
# Path to the podman machine's own socket, from inside the VM (not the macOS-side
# forwarding socket at /var/run/docker.sock). See the *-linux-vm targets below.
LINUX_VM_PODMAN_SOCK ?= /run/podman/podman.sock
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)

# GIT_VERSION is the single, predictable version string for every agent-side
# artifact built from a commit (agent bundle, byohctl) – Distinct from TAG
# above, which is the controller-manager image tag (still static "dev" by
# default); unifying that is separate follow-up work.
GIT_VERSION := $(shell git describe --abbrev=8 --dirty --tags --match='v*' 2>/dev/null || echo "v0.0.0-$(shell git rev-parse --short=8 HEAD)")

.PHONY: tag
tag: ## Print the predictable git-derived version used for agent/byohctl artifacts
	@echo $(GIT_VERSION)

REPO_ROOT := $(shell pwd)
# Real .git dir, outside REPO_ROOT for a linked worktree -- mounted below so git works in-container.
GIT_COMMON_DIR := $(shell git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
EXTRA_GIT_MOUNT := $(if $(GIT_COMMON_DIR),$(if $(filter $(REPO_ROOT)%,$(GIT_COMMON_DIR)),,-v "$(GIT_COMMON_DIR)":"$(GIT_COMMON_DIR)"),)
GINKGO_FOCUS  ?=
GINKGO_SKIP ?=
GINKGO_NODES  ?= 1
E2E_CONF_FILE  ?= ${REPO_ROOT}/test/e2e/config/provider.yaml
ARTIFACTS ?= ${REPO_ROOT}/_artifacts
SKIP_RESOURCE_CLEANUP ?= false
# SKIP_BUILD lets CI reuse an image/binary already obtained from a sibling
# workflow run instead of rebuilding it; unset by default so local dev and
# other CI paths keep rebuilding exactly as before.
SKIP_BUILD ?=
USE_EXISTING_CLUSTER ?= false
EXISTING_CLUSTER_BYOHOSTCONFIG_PATH ?=
GINKGO_NOCOLOR ?= false
GITHASH=$(shell git rev-parse --short HEAD 2>/dev/null || echo 'unknown')
BUILDNUM ?= $(if $(GITHUB_RUN_NUMBER),$(GITHUB_RUN_NUMBER),0)
TOOLS_DIR := $(REPO_ROOT)/hack/tools
BIN_DIR := bin
TOOLS_BIN_DIR := $(TOOLS_DIR)/$(BIN_DIR)
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
GINKGO_PKG := github.com/onsi/ginkgo/v2/ginkgo

BYOH_TEMPLATES := $(REPO_ROOT)/test/e2e/data/infrastructure-provider-byoh

LDFLAGS := -w -s $(shell hack/version.sh)
STATIC=-extldflags '-static'

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.DEFAULT_GOAL := help

all: build

HOST_AGENT_DIR ?= agent

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# https://linuxcommand.org/lc3_adv_awk.php

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

manifests: controller-gen yq ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) crd:crdVersions=v1 rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_bootstrapkubeconfigs.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byoclusters.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byoclustertemplates.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byohosts.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byomachines.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byomachinetemplates.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_k8sinstallerconfigs.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_k8sinstallerconfigtemplates.yaml


generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	GOOS=linux go vet ./...

GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
lint: golangci-lint
	${GOLANGCI_LINT} run
golangci-lint:
	$(call go-get-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2)

##@ Build

clean: ## Remove local build output (bin/, _dist/, build/)
	rm -rf bin $(RELEASE_DIR) build

build: generate fmt vet ## Build manager binary.
	go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

docker-build: ## Build docker image with the manager.
ifdef SKIP_BUILD
	@echo "SKIP_BUILD set; skipping docker build for ${IMG}"
else
	docker build --build-arg GO_VERSION=$(GO_VERSION) -t ${IMG} .
endif

docker-push: ## Push docker image with the manager.
	docker push ${IMG}

IMGPKG_VERSION ?= v0.43.1
BYOH_AGENT_BUNDLE_IMAGE ?= quay.io/platform9/cluster-api-provider-bringyourownhost/agent

# Note the module path is carvel.dev/imgpkg, not github.com/carvel-dev/imgpkg -- the GitHub org
# name and the Go module path diverged after a rename.
IMGPKG := $(shell pwd)/bin/imgpkg
imgpkg: ## Download imgpkg locally if necessary.
	$(call go-get-tool,$(IMGPKG),carvel.dev/imgpkg/cmd/imgpkg@$(IMGPKG_VERSION))

push-agent-bundle: imgpkg ## Push the built agent .deb bundle to the registry via imgpkg. Requires build-host-agent-deb to have already run.
	$(IMGPKG) push -f build/pf9-byohost/debsrc/ -i $(BYOH_AGENT_BUNDLE_IMAGE):$(TAG)

##@ Helm chart

# HELM_VERSION's release tarball is pinned by SHA256 (checked against the
# published helm-<version>-<os>-<arch>.tar.gz.sha256sum on get.helm.sh), the
# same reasoning as IMGPKG_SHA256 in cmd/byohctl/Makefile: a compromised
# upstream release can't swap the binary this Makefile executes without a
# second, reviewable commit to this repo.
HELM_VERSION ?= v3.21.3
HELM_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
HELM_ARCH_RAW := $(shell uname -m)
ifeq ($(HELM_ARCH_RAW),x86_64)
HELM_ARCH := amd64
else ifeq ($(HELM_ARCH_RAW),aarch64)
HELM_ARCH := arm64
else
HELM_ARCH := $(HELM_ARCH_RAW)
endif
# Only the platforms this repo's contributors and CI actually run on are
# pinned here (macOS/arm64 dev machines, ubuntu-22.04 linux/amd64 CI).
# Before using another platform: download its helm-<version>-<os>-<arch>.tar.gz,
# compute its SHA256, cross-check against get.helm.sh's published
# .sha256sum file, then add a HELM_SHA256_<os>_<arch> entry here.
HELM_SHA256_darwin_arm64 := 19879a848cad832b7a1ac24b767a481d20fb3b95ab53a220849649422ada144e
HELM_SHA256_linux_amd64 := 15e041a93a590dce8100f39385cd98c84a765c9e36aeeb9e2dc6ff9e4769e2e0
HELM_SHA256 := $(HELM_SHA256_$(HELM_OS)_$(HELM_ARCH))

HELM = $(shell pwd)/bin/helm
BYOH_HELM_CHART_IMAGE ?= quay.io/platform9/cluster-api-provider-bringyourownhost

helm-binary: ## Download the pinned helm CLI locally if necessary.
	@if [ -z "$(HELM_SHA256)" ]; then \
		echo "error: no pinned helm SHA256 for platform $(HELM_OS)/$(HELM_ARCH); add a verified HELM_SHA256_$(HELM_OS)_$(HELM_ARCH) entry to the Makefile" >&2; \
		exit 1; \
	fi
	@[ -f $(HELM) ] || { \
	set -e ;\
	curl -fsSL -o /tmp/helm-download.tar.gz "https://get.helm.sh/helm-$(HELM_VERSION)-$(HELM_OS)-$(HELM_ARCH).tar.gz" ;\
	echo "$(HELM_SHA256)  /tmp/helm-download.tar.gz" | sha256sum -c - ;\
	rm -rf /tmp/helm-extract ;\
	mkdir -p /tmp/helm-extract $(shell pwd)/bin ;\
	tar -xzf /tmp/helm-download.tar.gz -C /tmp/helm-extract ;\
	install -m 0755 /tmp/helm-extract/$(HELM_OS)-$(HELM_ARCH)/helm $(HELM) ;\
	rm -rf /tmp/helm-download.tar.gz /tmp/helm-extract ;\
	}

helm: kustomize yq helm-binary ## Generate the byoh helm chart under charts/ (chart-generator/chart-generator.sh).
	PATH="$(shell pwd)/bin:$$PATH" VERSION=$(GIT_VERSION) chart-generator/chart-generator.sh -e $(IMG)

helm-package: helm ## Package the generated chart into a .tgz.
	PATH="$(shell pwd)/bin:$$PATH" helm package ./charts

helm-login: helm-binary ## Log in to the Quay OCI registry for helm push (reads QUAY_USERNAME/QUAY_TOKEN from the environment).
	echo "$$QUAY_TOKEN" | PATH="$(shell pwd)/bin:$$PATH" helm registry login --username "$$QUAY_USERNAME" --password-stdin quay.io

helm-push: helm-package ## Push the packaged helm chart to Quay via OCI.
	PATH="$(shell pwd)/bin:$$PATH" helm push byoh-chart-$(GIT_VERSION).tgz oci://$(BYOH_HELM_CHART_IMAGE)

prepare-byoh-docker-host-image:
	docker build test/e2e -f test/e2e/BYOHDockerFile -t ${BYOH_BASE_IMG}

build-packaging-test-image: ## Build the Rocky Linux image used by test-packaging
	docker build test/e2e/packaging -f test/e2e/packaging/RockyDockerFile -t $(PACKAGING_TEST_RPM_IMG)

test-packaging: build-packaging-test-image prepare-byoh-docker-host-image ## Run the pf9-byohost RPM/deb install/uninstall tests
	go test ./test/e2e/packaging/... -v -timeout 5m

prepare-byoh-docker-host-image-dev:
	docker build test/e2e -f docs/BYOHDockerFileDev -t ${BYOH_BASE_IMG_DEV}

cluster-templates-v1beta1: kustomize ## Generate cluster templates for v1beta1
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/vm --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/docker --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml

##@ PICT

pict-image: ## Build the PICT (pairwise combinatorial test generator) image from its Homebrew bottle
	docker build -f hack/docker/pict.Dockerfile -t $(PICT_IMG) hack/docker

# /o:1 (each value covered at least once) instead of PICT's default /o:2
# (every pair covered): each row is a full e2e run, and with only one
# real parameter besides Scenario today, pairwise coverage of exactly two
# parameters is the same thing as the full cross product -- no
# combinatorial saving over a naive nested loop, just more rows. Revisit
# once model.pict grows a third free parameter (e.g. OS, once a second
# real image exists), where pairwise actually starts saving cases.
PICT_OPTS = /o:1
generate-pict: pict-image ## Regenerate test/e2e/pict/generated-matrix.tsv from model.pict
	docker run --rm -v $(PICT_DIR):/var/pict:Z $(PICT_IMG) model.pict $(PICT_OPTS) > $(PICT_DIR)/generated-matrix.tsv

##@ Test

# Run tests
test: $(GINKGO) generate fmt vet manifests test-coverage cmd-test ## Run unit tests

test-coverage: $(GINKGO) prepare-byoh-docker-host-image ## Run test-coverage
	find . -name "*.test" -not -path "./cmd/*" -delete
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; CGO_ENABLED=0 $(GINKGO) --randomize-all -r --cover --coverprofile=cover.out --output-dir=. --skip-package=test,cmd .

cmd-test: ## Run cmd/byohctl tests (separate Go module)
	cd cmd && go test ./...

agent-test: $(GINKGO) prepare-byoh-docker-host-image ## Run agent tests
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; CGO_ENABLED=0 $(GINKGO) --randomize-all -r --coverprofile cover.out $(HOST_AGENT_DIR)

controller-test: $(GINKGO) ## Run controller tests
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; $(GINKGO) --randomize-all --coverprofile cover.out --vv controllers/infrastructure

webhook-test: $(GINKGO) ## Run webhook tests
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; $(GINKGO) --coverprofile cover.out apis/infrastructure/v1beta1

test-e2e: take-user-input docker-build prepare-byoh-docker-host-image $(GINKGO) cluster-templates-e2e ## Run the end-to-end tests
	$(GINKGO) -v -trace -tags=e2e -focus="$(GINKGO_FOCUS)" $(_SKIP_ARGS) -nodes=$(GINKGO_NODES) --noColor=$(GINKGO_NOCOLOR) $(GINKGO_ARGS) test/e2e -- \
	    -e2e.artifacts-folder="$(ARTIFACTS)" \
	    -e2e.config="$(E2E_CONF_FILE)" \
	    -e2e.skip-resource-cleanup=$(SKIP_RESOURCE_CLEANUP) -e2e.use-existing-cluster=$(USE_EXISTING_CLUSTER) \
		-e2e.existing-cluster-kubeconfig-path=$(EXISTING_CLUSTER_BYOHOSTCONFIG_PATH)

cluster-templates: kustomize cluster-templates-v1beta1

cluster-templates-e2e: kustomize
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/e2e --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/e2e/cluster-template.yaml

linux-vm-image: ## Build the Linux container image used by the *-linux-vm targets
	docker build --build-arg GO_VERSION=$(GO_VERSION) -f hack/docker/linux-test-runner.Dockerfile -t $(LINUX_VM_IMG) .

# Run any other Makefile target inside the Linux VM container (macOS) by
# appending -linux-vm, e.g. `make agent-test-linux-vm`, `make
# prepare-byoh-docker-host-image-linux-vm`, `make build-host-agent-rpm-linux-vm`.
# LINUX_VM=1 tells host-agent-binary (see below) it's already inside the
# wrapper container and shouldn't nest another docker run. -i keeps stdin
# open so an interactive prompt (e.g. test-e2e's take-user-input) still
# reaches the container: answer it yourself, or run e.g.
# `yes | make test-e2e-linux-vm` to auto-confirm, same as with plain `make test-e2e`.
%-linux-vm: linux-vm-image
	docker run --rm -i --network host --security-opt label=disable \
		-v "$(REPO_ROOT)":/workspace \
		$(EXTRA_GIT_MOUNT) \
		-v $(LINUX_VM_PODMAN_SOCK):/var/run/docker.sock \
		-e CONTAINER_HOST=unix:///var/run/docker.sock \
		-e LINUX_VM=1 \
		-e GINKGO_FOCUS -e GINKGO_SKIP -e GINKGO_NODES -e SKIP_RESOURCE_CLEANUP -e USE_EXISTING_CLUSTER \
		-e BYOH_AGENT_BUNDLE_URL \
		-e E2E_OS_IMAGE -e E2E_K8S_VERSION_FROM -e E2E_K8S_VERSION_TO -e IP_FAMILY \
		-w /workspace \
		$(LINUX_VM_IMG) \
		make $*


define WARNING
#####################################################################################################

** WARNING **
These tests modify system settings - and do **NOT** revert them at the end of the test.
A list of changes can be found below. We **highly** recommend running these tests in a VM. 

Running e2e tests locally will change the following host config
- enable the kernel modules: overlay & bridge network filter
- create a systemwide config that will enable those modules at boot time
- enable ipv4 & ipv6 forwarding
- create a systemwide config that will enable the forwarding at boot time
- reload the sysctl with the applied config changes so the changes can take effect without restarting
- disable unattended OS updates

#####################################################################################################
endef
export WARNING

.PHONY: take-user-input
take-user-input:
	@echo "$$WARNING"
	@read -p "Do you want to proceed [Y/n]?" REPLY; \
	if [[ $$REPLY = "Y" || $$REPLY = "y" ]]; then echo starting e2e test; exit 0 ; else echo aborting; exit 1; fi
	


$(GINKGO): # Build ginkgo from tools folder.
	cd $(TOOLS_DIR); GOBIN=$(TOOLS_BIN_DIR) go install $(GINKGO_PKG)

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image gcr.io/k8s-staging-cluster-api/cluster-api-byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

publish-infra-yaml:kustomize # Generate infrastructure-components.yaml for the provider
	cd config/manager && $(KUSTOMIZE) edit set image gcr.io/k8s-staging-cluster-api/cluster-api-byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default > infrastructure-components.yaml

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0)

KUSTOMIZE = $(shell pwd)/bin/kustomize
kustomize: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v4@v4.5.2)

YQ = $(shell pwd)/bin/yq
yq: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(YQ),github.com/mikefarah/yq/v4@v4.31.1)

host-agent-binaries: ## Builds the binaries for the host-agent
	RELEASE_BINARY=./byoh-hostagent GOOS=linux GOARCH=amd64 GOLDFLAGS="$(LDFLAGS) $(STATIC)" \
	HOST_AGENT_DIR=./$(HOST_AGENT_DIR) $(MAKE) host-agent-binary

host-agent-binary: $(RELEASE_DIR)
ifdef SKIP_BUILD
	@echo "SKIP_BUILD set; skipping host-agent binary build"
else ifdef LINUX_VM
	# Already running inside the *-linux-vm wrapper container.
	# Nested call would need a bind-mount source
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
	go build -buildvcs=false -a -ldflags "$(GOLDFLAGS)" \
	-o ./bin/$(notdir $(RELEASE_BINARY))-$(GOOS)-$(GOARCH) $(HOST_AGENT_DIR)
else
	docker run \
		--rm \
		--user "$$(id -u):$$(id -g)" \
		-e CGO_ENABLED=0 \
		-e GOOS=$(GOOS) \
		-e GOARCH=$(GOARCH) \
		-e GOCACHE=/tmp/go-build-cache \
		-e GOPATH=/tmp/go \
		-v "$$(pwd):/workspace$(DOCKER_VOL_OPTS)" \
		-w /workspace \
		golang:$(GO_VERSION) \
		go build -buildvcs=false -a -ldflags "$(GOLDFLAGS)" \
		-o ./bin/$(notdir $(RELEASE_BINARY))-$(GOOS)-$(GOARCH) $(HOST_AGENT_DIR)
endif


##@ Release

$(RELEASE_DIR):
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)

build-release-artifacts: build-cluster-templates build-infra-yaml build-metadata-yaml build-host-agent-binary ## Builds release artifacts

build-cluster-templates: $(RELEASE_DIR) cluster-templates
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml $(RELEASE_DIR)/cluster-template-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template-topology-docker.yaml $(RELEASE_DIR)/cluster-template-topology-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/clusterclass-quickstart-docker.yaml $(RELEASE_DIR)/clusterclass-quickstart-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml $(RELEASE_DIR)/cluster-template.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template-topology.yaml $(RELEASE_DIR)/cluster-template-topology.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/clusterclass-quickstart.yaml $(RELEASE_DIR)/clusterclass-quickstart.yaml


build-infra-yaml:kustomize ## Generate infrastructure-components.yaml for the provider
	cd config/manager && $(KUSTOMIZE) edit set image gcr.io/k8s-staging-cluster-api/cluster-api-byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default > $(RELEASE_DIR)/infrastructure-components.yaml

build-metadata-yaml:
	cp metadata.yaml $(RELEASE_DIR)/metadata.yaml

build-host-agent-binary: host-agent-binaries
	cp bin/byoh-hostagent-linux-amd64 $(RELEASE_DIR)/byoh-hostagent-linux-amd64


##########################################################################

BUILD_DIR=$(shell pwd)/build
$(BUILD_DIR):
	mkdir -p $@

PF9_BYOHOST_SRCDIR := $(BUILD_DIR)/pf9-byohost
$(PF9_BYOHOST_SRCDIR):
	echo "make PF9_BYOHOST_SRCDIR $(PF9_BYOHOST_SRCDIR)"
	rm -fr $@
	mkdir -p $@

AGENT_SRC_DIR := $(REPO_ROOT)
BYOHCTL_DIR := $(REPO_ROOT)/cmd/byohctl
RPM_SRC_ROOT := $(PF9_BYOHOST_SRCDIR)/rpmsrc
DEB_SRC_ROOT := $(PF9_BYOHOST_SRCDIR)/debsrc

# See docs/proposals/pf9-byohost-deb-arch-support-adr.md (§2) for why this defaults to
# HELM_ARCH rather than a hardcoded amd64.
PACKAGE_GOARCH ?= $(HELM_ARCH)
COMMON_SRC_ROOT := $(PF9_BYOHOST_SRCDIR)/common
PF9_BYOHOST_DEB_FILE := $(PF9_BYOHOST_SRCDIR)/debsrc/pf9-byohost-agent.deb
RPMBUILD_DIR := $(PF9_BYOHOST_SRCDIR)/rpmbuild
PF9_BYOHOST_RPM_FILE := $(RPMBUILD_DIR)/RPMS/$(HELM_ARCH_RAW)/pf9-byohost-1.0-$(BUILDNUM).git$(GITHASH).$(HELM_ARCH_RAW).rpm

$(RPM_SRC_ROOT): | $(COMMON_SRC_ROOT)
	echo "make RPM_SRC_ROOT: $(RPM_SRC_ROOT)"
	cp -a $(COMMON_SRC_ROOT) $(RPM_SRC_ROOT)

$(PF9_BYOHOST_RPM_FILE): |$(RPM_SRC_ROOT)
	echo "make PF9_BYOHOST_RPM_FILE $(PF9_BYOHOST_RPM_FILE) "
	rpmbuild -bb \
	    --define "_topdir $(RPMBUILD_DIR)"  \
	    --define "_src_dir $(RPM_SRC_ROOT)"  \
	    --define "_scripts_dir $(AGENT_SRC_DIR)/scripts"  \
	    --define "_githash $(GITHASH)" \
	    --define "_buildnum $(BUILDNUM)" $(AGENT_SRC_DIR)/scripts/pf9-byohost.spec
	$(AGENT_SRC_DIR)/scripts/sign_packages.sh $(PF9_BYOHOST_RPM_FILE)
	md5sum $(PF9_BYOHOST_RPM_FILE) | cut -d' ' -f 1  > $(PF9_BYOHOST_RPM_FILE).md5

build-host-agent-rpm:  $(PF9_BYOHOST_RPM_FILE)
	echo "make agent-rpm pf9_byohost_rpm_file = $(PF9_BYOHOST_RPM_FILE)"

#########################################################################
# Independent of build-host-agent-binary/RELEASE_DIR, which stay pinned to amd64 for the release artifact.
# byohctl reuses the same PACKAGE_GOARCH so both binaries in the package always match one arch.
build-byohctl-binary:
	$(MAKE) -C $(BYOHCTL_DIR) build GOARCH=$(PACKAGE_GOARCH)

$(COMMON_SRC_ROOT): build-byohctl-binary
	echo "Building COMMON_SRC_ROOT"
	mkdir -p $(COMMON_SRC_ROOT)
	echo "BUILDING COMMON_SRC_ROOT/binary for GOARCH=$(PACKAGE_GOARCH)"
	RELEASE_BINARY=./byoh-hostagent GOOS=linux GOARCH=$(PACKAGE_GOARCH) GOLDFLAGS="$(LDFLAGS) $(STATIC)" \
	HOST_AGENT_DIR=./$(HOST_AGENT_DIR) $(MAKE) host-agent-binary
	mkdir -p $(COMMON_SRC_ROOT)/binary
	cp bin/byoh-hostagent-linux-$(PACKAGE_GOARCH) $(COMMON_SRC_ROOT)/binary/pf9-byoh-hostagent
	echo "BUILDING dir for pf9-byohost-service , COPING service pf9-byoh-agent.service "
	mkdir -p $(COMMON_SRC_ROOT)/etc/systemd/system/
	cp $(AGENT_SRC_DIR)/service/pf9-byohostagent.service $(COMMON_SRC_ROOT)/etc/systemd/system/pf9-byohost-agent.service
	echo "BUILDING COMMON_SRC_ROOT/usr/bin COPING binary byohctl"
	mkdir -p $(COMMON_SRC_ROOT)/usr/bin
	cp $(BYOHCTL_DIR)/bin/byohctl $(COMMON_SRC_ROOT)/usr/bin/byohctl
	chmod +x $(COMMON_SRC_ROOT)/usr/bin/byohctl

$(DEB_SRC_ROOT): | $(COMMON_SRC_ROOT)
	cp -a  $(COMMON_SRC_ROOT) $(DEB_SRC_ROOT)

$(PF9_BYOHOST_DEB_FILE): $(DEB_SRC_ROOT)
	fpm -t deb -s dir -n pf9-byohost-agent \
	 --description "Platform9 Bring Your Own Host deb package" \
	 --license "Commercial" --architecture $(PACKAGE_GOARCH) --url "http://www.platform9.net" --vendor Platform9 \
	 -d socat -d ethtool -d ebtables -d conntrack \
	 --after-install $(AGENT_SRC_DIR)/scripts/pf9-byohost-agent-after-install.sh \
	 --before-remove $(AGENT_SRC_DIR)/scripts/pf9-byohost-agent-before-remove.sh \
	 --after-remove $(AGENT_SRC_DIR)/scripts/pf9-byohost-agent-after-remove.sh \
	 -p $(PF9_BYOHOST_DEB_FILE) \
	 -C $(DEB_SRC_ROOT)/ .
	$(AGENT_SRC_DIR)/sign_packages_deb.sh $(PF9_BYOHOST_DEB_FILE)
	md5sum $(PF9_BYOHOST_DEB_FILE) | cut -d' ' -f 1 > $(PF9_BYOHOST_DEB_FILE).md5

build-host-agent-deb: $(PF9_BYOHOST_DEB_FILE)

########################################################################

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

.PHONY: ci
ci: ## Push a ci-<tag> git tag to force every CI check (including publish steps) to run against the current commit, bypassing the normal main-only gate.
	@TAG_NAME="ci-$$(make --no-print-directory tag)" && \
	echo "Creating git tag: $$TAG_NAME" && \
	git tag "$$TAG_NAME" && \
	git push origin "$$TAG_NAME"
