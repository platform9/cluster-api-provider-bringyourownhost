#Ensure Make is run with bash shell as some syntax below is bash-specific
SHELL:=/usr/bin/env bash

# Define registries
STAGING_REGISTRY ?= gcr.io/k8s-staging-cluster-api

IMAGE_NAME ?= cluster-api-byoh-controller
TAG ?= dev
RELEASE_DIR := _dist

# Image URL to use all building/pushing image targets
IMG ?= ${STAGING_REGISTRY}/${IMAGE_NAME}:${TAG}
BYOH_BASE_IMG = byoh/node:e2e
BYOH_BASE_IMG_DEV = byoh/node:dev
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)

REPO_ROOT := $(shell pwd)
GINKGO_FOCUS  ?=
GINKGO_SKIP ?=
GINKGO_NODES  ?= 1
E2E_CONF_FILE  ?= ${REPO_ROOT}/test/e2e/config/provider.yaml
ARTIFACTS ?= ${REPO_ROOT}/_artifacts
SKIP_RESOURCE_CLEANUP ?= false
USE_EXISTING_CLUSTER ?= false
EXISTING_CLUSTER_BYOHOSTCONFIG_PATH ?=
GINKGO_NOCOLOR ?= false
GITHASH=$(shell git rev-parse --short HEAD 2>/dev/null || echo 'unknown')
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
	go vet ./...

GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
lint: golangci-lint
	${GOLANGCI_LINT} run
golangci-lint:
	[ -e ${GOLANGCI_LINT} ] || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell pwd)/bin v1.64.8

##@ Build

build: generate fmt vet ## Build manager binary.
	go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} .

docker-push: ## Push docker image with the manager.
	docker push ${IMG}

prepare-byoh-docker-host-image:
	docker build test/e2e -f test/e2e/BYOHDockerFile -t ${BYOH_BASE_IMG}

prepare-byoh-docker-host-image-dev:
	docker build test/e2e -f docs/BYOHDockerFileDev -t ${BYOH_BASE_IMG_DEV}

cluster-templates-v1beta1: kustomize ## Generate cluster templates for v1beta1
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/vm --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/docker --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml

##@ Test

# Run tests
test: $(GINKGO) generate fmt vet manifests test-coverage ## Run unit tests

test-coverage: prepare-byoh-docker-host-image ## Run test-coverage
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; $(GINKGO) --randomize-all -r --cover --coverprofile=cover.out --output-dir=. --skip-package=test .

agent-test: prepare-byoh-docker-host-image ## Run agent tests
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; $(GINKGO) --randomize-all -r $(HOST_AGENT_DIR) --coverprofile cover.out

controller-test: ## Run controller tests
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; $(GINKGO) --randomize-all controllers/infrastructure --coverprofile cover.out --vv

webhook-test: ## Run webhook tests
	source ./scripts/fetch_ext_bins.sh; fetch_tools; setup_envs; $(GINKGO) apis/infrastructure/v1beta1 --coverprofile cover.out

test-e2e: take-user-input docker-build prepare-byoh-docker-host-image $(GINKGO) cluster-templates-e2e ## Run the end-to-end tests
	$(GINKGO) -v -trace -tags=e2e -focus="$(GINKGO_FOCUS)" $(_SKIP_ARGS) -nodes=$(GINKGO_NODES) --noColor=$(GINKGO_NOCOLOR) $(GINKGO_ARGS) test/e2e -- \
	    -e2e.artifacts-folder="$(ARTIFACTS)" \
	    -e2e.config="$(E2E_CONF_FILE)" \
	    -e2e.skip-resource-cleanup=$(SKIP_RESOURCE_CLEANUP) -e2e.use-existing-cluster=$(USE_EXISTING_CLUSTER) \
		-e2e.existing-cluster-kubeconfig-path=$(EXISTING_CLUSTER_BYOHOSTCONFIG_PATH)

cluster-templates: kustomize cluster-templates-v1beta1

cluster-templates-e2e: kustomize
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/e2e --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/e2e/cluster-template.yaml

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
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0)

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
	docker run \
		--rm \
		-e CGO_ENABLED=0 \
		-e GOOS=$(GOOS) \
		-e GOARCH=$(GOARCH) \
		-v "$$(pwd):/workspace$(DOCKER_VOL_OPTS)" \
		-w /workspace \
		golang:1.20.7 \
		go build -buildvcs=false -a -ldflags "$(GOLDFLAGS)" \
		-o ./bin/$(notdir $(RELEASE_BINARY))-$(GOOS)-$(GOARCH) $(HOST_AGENT_DIR)


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
RPM_SRC_ROOT := $(PF9_BYOHOST_SRCDIR)/rpmsrc
DEB_SRC_ROOT := $(PF9_BYOHOST_SRCDIR)/debsrc
COMMON_SRC_ROOT := $(PF9_BYOHOST_SRCDIR)/common
PF9_BYOHOST_DEB_FILE := $(PF9_BYOHOST_SRCDIR)/debsrc/pf9-byohost-agent.deb
RPMBUILD_DIR := $(PF9_BYOHOST_SRCDIR)/rpmsrc
PF9_BYOHOST_RPM_FILE := $(PF9_BYOHOST_SRCDIR)/rpmsrc/pf9-byohost-agent.rpm

$(RPM_SRC_ROOT): | $(COMMON_SRC_ROOT)
	echo "make RPM_SRC_ROOT: $(RPM_SRC_ROOT)"
	cp -a $(COMMON_SRC_ROOT) $(RPM_SRC_ROOT)

$(PF9_BYOHOST_RPM_FILE): |$(RPM_SRC_ROOT)
	echo "make PF9_BYOHOST_RPM_FILE $(PF9_BYOHOST_RPM_FILE) "
	rpmbuild -bb \
	    --define "_topdir $(RPMBUILD_DIR)"  \
	    --define "_src_dir $(RPM_SRC_ROOT)"  \
	    --define "_githash $(GITHASH)" $(AGENT_SRC_DIR)/scripts/pf9-byohost.spec 
	./$(AGENT_SRC_DIR)/scripts/sign_packages.sh $(PF9_BYOHOST_RPM_FILE)
	md5sum $(PF9_BYOHOST_RPM_FILE) | cut -d' ' -f 1  > $(PF9_BYOHOST_RPM_FILE).md5

build-host-agent-rpm:  $(PF9_BYOHOST_RPM_FILE)
	echo "make agent-rpm pf9_byohost_rpm_file = $(PF9_BYOHOST_RPM_FILE)"

#########################################################################
$(COMMON_SRC_ROOT): build-host-agent-binary
	echo "Building COMMON_SRC_ROOT"
	mkdir -p $(COMMON_SRC_ROOT)
	echo "BUILDING COMMON_SRC_ROOT/binary COPING binary pf9-byoh-hostagent-linux-amd64"
	mkdir -p $(COMMON_SRC_ROOT)/binary
	cp $(RELEASE_DIR)/byoh-hostagent-linux-amd64 $(COMMON_SRC_ROOT)/binary/pf9-byoh-hostagent-linux-amd64
	echo "BUILDING dir for pf9-byohost-service , COPING service pf9-byoh-agent.service "
	mkdir -p $(COMMON_SRC_ROOT)/lib/systemd/system/
	cp $(AGENT_SRC_DIR)/service/pf9-byohostagent.service $(COMMON_SRC_ROOT)/lib/systemd/system/pf9-byohost-agent.service

$(DEB_SRC_ROOT): | $(COMMON_SRC_ROOT)
	cp -a  $(COMMON_SRC_ROOT) $(DEB_SRC_ROOT)

$(PF9_BYOHOST_DEB_FILE): $(DEB_SRC_ROOT)
	fpm -t deb -s dir -n pf9-byohost-agent \
	 --description "Platform9 Bring Your Own Host deb package" \
	 --license "Commercial" --architecture all --url "http://www.platform9.net" --vendor Platform9 \
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
