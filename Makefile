# Manager image. Repository defaults to the ghcr coordinate; the deploy target
# splits IMG into image.repository + image.tag for the Helm chart. Override IMG
# to build/deploy a locally-built image (e.g. e2e passes a kind-loaded tag).
IMG ?= ghcr.io/karlkfi/kube-headroom:latest

# Helm release + chart configuration.
# HELM_NAMESPACE is where the operator release (and its namespaced resources)
# land; it matches the e2e/runbook namespace. CHART_REGISTRY is the OCI
# destination for `helm push` (image and charts never share a coordinate).
HELM_NAMESPACE ?= kube-headroom-system
CRDS_RELEASE ?= kube-headroom-crds
OPERATOR_RELEASE ?= kube-headroom
CRDS_CHART ?= charts/kube-headroom-crds
OPERATOR_CHART ?= charts/kube-headroom
CHART_REGISTRY ?= oci://ghcr.io/karlkfi/charts
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: check
check: lint verify-generate verify-vendor verify-helm-sync helm-lint helm-template backlog-lint shellcheck doc-links test ## Run all fast pre-review checks (mirrors CI). Green here == green in CI.

.PHONY: vendor
vendor: ## Refresh go.mod/go.sum and the checked-in vendor/ tree.
	go mod tidy
	go mod vendor

.PHONY: verify-vendor
verify-vendor: vendor ## Fail if go.mod/go.sum or vendor/ are out of date.
	@git diff --exit-code -- go.mod go.sum vendor || { \
		echo "ERROR: vendored dependencies are out of date. Run 'make vendor' and commit the result."; \
		exit 1; \
	}

.PHONY: verify-generate
verify-generate: manifests generate ## Fail if generated manifests/code are out of date.
	@git diff --exit-code -- config api PROJECT || { \
		echo "ERROR: generated files are out of date. Run 'make manifests generate' and commit the result."; \
		exit 1; \
	}

.PHONY: backlog-lint
backlog-lint: ## Lint the docs/STATUS.md backlog.
	bash scripts/lint-backlog.sh docs/STATUS.md

.PHONY: govulncheck
govulncheck: govulncheck-tool ## Report known vulnerabilities in dependencies.
	"$(GOVULNCHECK)" ./...

.PHONY: shellcheck
shellcheck: ## Lint shell scripts and git hooks (skips locally if shellcheck is absent; CI runs it strictly).
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck not installed; skipping (CI enforces it)."; exit 0; }
	shellcheck --severity=warning scripts/*.sh .githooks/*

.PHONY: doc-links
doc-links: ## Check Markdown for broken relative links/anchors (offline, no external deps).
	python3 scripts/check-doc-links.py

.PHONY: plan-hygiene
plan-hygiene: ## Check plan docs are STATUS-referenced or archived, and unreferenced by Go code.
	bash scripts/lint-plan-hygiene.sh

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# kubectl kuberc is disabled by default for test isolation; enable with:
# - KUBECTL_KUBERC=true
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= kube-headroom-test-e2e
# Pin the node image to a Kubernetes >= 1.35 release: in-place pod resize is GA
# only from 1.35, and Headroom's e2e scenarios (design §10) depend on it. The
# digest makes the cluster shape reproducible across kind versions. Bump both
# the tag and the digest together from the kind release notes.
KIND_NODE_IMAGE ?= kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f
# The e2e cluster is control-plane + one worker so slack scenarios run on a node
# free of control-plane static pods; see test/e2e/kind-config.yaml.
KIND_CONFIG ?= test/e2e/kind-config.yaml

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)' (k8s >= 1.35)..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) --image $(KIND_NODE_IMAGE) --config $(KIND_CONFIG) ;; \
	esac

.PHONY: test-e2e
# CertManager is required: config/default deploys the Q6 birth-limit webhook,
# whose serving cert (webhook-server-cert) is issued by cert-manager. The suite
# installs it in BeforeSuite unless CERT_MANAGER_INSTALL_SKIP=true.
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) \
		go test -tags=e2e ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Helm

.PHONY: helm-sync
helm-sync: ## Sync kubebuilder-generated manifests (config/**) into the Helm charts.
	bash scripts/helm-sync.sh

.PHONY: verify-helm-sync
verify-helm-sync: manifests generate helm-sync ## Fail if the charts are out of sync with the generated manifests.
	@git diff --exit-code -- charts || { \
		echo "ERROR: charts are out of sync with config/**. Run 'make manifests generate helm-sync' and commit the result."; \
		exit 1; \
	}

.PHONY: helm-lint
helm-lint: helm ## Lint both Helm charts.
	"$(HELM)" lint $(CRDS_CHART) $(OPERATOR_CHART)

.PHONY: helm-template
helm-template: helm ## Render both charts across the values toggle matrix and validate them with kubeconform.
	@if command -v kubeconform >/dev/null 2>&1; then \
		"$(HELM)" template $(CRDS_RELEASE) $(CRDS_CHART) | kubeconform -strict -ignore-missing-schemas -summary; \
	else \
		echo "kubeconform not installed; rendering only (CI validates with kubeconform)."; \
		"$(HELM)" template $(CRDS_RELEASE) $(CRDS_CHART) >/dev/null; \
	fi
	@HELM="$(HELM)" OPERATOR_CHART="$(OPERATOR_CHART)" OPERATOR_RELEASE="$(OPERATOR_RELEASE)" \
		HELM_NAMESPACE="$(HELM_NAMESPACE)" bash scripts/helm-render-matrix.sh

.PHONY: helm-package
helm-package: helm ## Package both charts into dist/. Set CHART_VERSION=x.y.z to stamp the release version/appVersion.
	mkdir -p dist
	"$(HELM)" package $(CRDS_CHART) $(OPERATOR_CHART) -d dist \
		$(if $(CHART_VERSION),--version $(CHART_VERSION) --app-version $(CHART_VERSION),)

.PHONY: helm-push
helm-push: helm-package ## Push both packaged charts to the OCI registry (needs `helm registry login`).
	@for tgz in dist/$(CRDS_RELEASE)-*.tgz dist/$(OPERATOR_RELEASE)-*.tgz; do \
		echo "Pushing $$tgz -> $(CHART_REGISTRY)"; \
		"$(HELM)" push "$$tgz" "$(CHART_REGISTRY)"; \
	done

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name kube-headroom-builder
	$(CONTAINER_TOOL) buildx use kube-headroom-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm kube-headroom-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate helm-sync helm ## Render a consolidated YAML (CRD + operator) into dist/install.yaml.
	mkdir -p dist
	@repo="$$(printf '%s' '$(IMG)' | sed -E 's/(.*):[^:/]+$$/\1/')"; \
	 tag="$$(printf '%s' '$(IMG)' | sed -E 's/.*:([^:/]+)$$/\1/')"; \
	 { "$(HELM)" template $(CRDS_RELEASE) $(CRDS_CHART) \
		--namespace $(HELM_NAMESPACE); \
	   "$(HELM)" template $(OPERATOR_RELEASE) $(OPERATOR_CHART) \
		--namespace $(HELM_NAMESPACE) \
		--set-string image.repository="$$repo" \
		--set-string image.tag="$$tag"; } > dist/install.yaml

##@ Deployment

# The deploy path is Helm-native (Q21): the CRD and operator install as two
# charts. `make install`/`deploy` drive `helm upgrade --install` so they are
# idempotent and re-runnable; `uninstall`/`undeploy` are `helm uninstall`. The
# CRD carries resource-policy: keep, so `make uninstall` leaves live
# HeadroomConfigs intact.

.PHONY: install
install: helm ## Install/upgrade the HeadroomConfig CRD chart into the current kubecontext.
	"$(HELM)" upgrade --install $(CRDS_RELEASE) $(CRDS_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace

.PHONY: uninstall
uninstall: helm ## Uninstall the CRD chart. resource-policy: keep leaves live HeadroomConfigs and the CRD in place.
	"$(HELM)" uninstall $(CRDS_RELEASE) --namespace $(HELM_NAMESPACE) --ignore-not-found

.PHONY: deploy
deploy: helm ## Deploy/upgrade the operator to the current kubecontext (override IMG to set the image).
	@repo="$$(printf '%s' '$(IMG)' | sed -E 's/(.*):[^:/]+$$/\1/')"; \
	 tag="$$(printf '%s' '$(IMG)' | sed -E 's/.*:([^:/]+)$$/\1/')"; \
	 "$(HELM)" upgrade --install $(OPERATOR_RELEASE) $(OPERATOR_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		--set-string image.repository="$$repo" \
		--set-string image.tag="$$tag"

.PHONY: undeploy
undeploy: helm ## Undeploy the operator from the current kubecontext.
	"$(HELM)" uninstall $(OPERATOR_RELEASE) --namespace $(HELM_NAMESPACE) --ignore-not-found

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Location of the tools submodule (tools/go.mod) that pins build-tool versions.
TOOLS_DIR ?= tools

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
HELM ?= $(LOCALBIN)/helm

## Tool Versions
## controller-gen, kustomize, golangci-lint and govulncheck are pinned in the
## tools submodule (tools/go.mod); these read the pinned version from there so
## there is a single source of truth. Override on the command line if needed.
KUSTOMIZE_VERSION ?= $(call toolver,sigs.k8s.io/kustomize/kustomize/v5)
CONTROLLER_TOOLS_VERSION ?= $(call toolver,sigs.k8s.io/controller-tools)
GOVULNCHECK_VERSION ?= $(call toolver,golang.org/x/vuln)
HELM_VERSION ?= $(call toolver,helm.sh/helm/v4)

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= $(call toolver,github.com/golangci/golangci-lint/v2)
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Build kustomize from the tools submodule if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-build-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Build controller-gen from the tools submodule if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-build-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Build golangci-lint from the tools submodule if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-build-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

.PHONY: govulncheck-tool
govulncheck-tool: $(GOVULNCHECK) ## Build govulncheck from the tools submodule if necessary.
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-build-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

.PHONY: helm
helm: $(HELM) ## Build helm from the tools submodule if necessary.
$(HELM): $(LOCALBIN)
	$(call go-build-tool,$(HELM),helm.sh/helm/v4/cmd/helm,$(HELM_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

# toolver reads a tool's pinned version from the tools submodule (tools/go.mod),
# so the Makefile and tools/go.mod never drift.
# $1 - module path of the tool (e.g. sigs.k8s.io/controller-tools)
define toolver
$(shell go -C $(TOOLS_DIR) list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

# go-build-tool builds a tool from the tools submodule (tools/go.mod pins the
# version) into LOCALBIN, version-stamped so a version bump in tools/go.mod
# rebuilds it. Mirrors go-install-tool but sources the pinned version from the
# submodule instead of a floating '@version'.
# $1 - target path with name of binary
# $2 - package import path, built from within the tools module
# $3 - resolved version, used to version-stamp the binary
define go-build-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
echo "Building $(2)@$(3)" ;\
rm -f "$(1)" ;\
go -C "$(TOOLS_DIR)" build -o "$(1)-$(3)" "$(2)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
