## Copyright (c) NVIDIA CORPORATION.  All rights reserved.

## Licensed under the Apache License, Version 2.0 (the "License");
## you may not use this file except in compliance with the License.
## You may obtain a copy of the License at

##     http://www.apache.org/licenses/LICENSE-2.0

## Unless required by applicable law or agreed to in writing, software
## distributed under the License is distributed on an "AS IS" BASIS,
## WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
## See the License for the specific language governing permissions and
## limitations under the License.

## this makefile is for installing deps and controlling the versioning
## its included in the main makefile, but its a lot to look at these
## plus ci can wait this file to know to build a new build image

## Location to install dependencies to
LOCALBIN ?= $(CURDIR)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

YQ ?= $(LOCALBIN)/yq
VERSIONS ?= $(CURDIR)/versions.sh

ENVTEST_K8S_VERSION ?= $(shell YQ="$(YQ)" $(VERSIONS) --get envtest.k8s.version)
KIND_VERSION ?= $(shell YQ="$(YQ)" $(VERSIONS) --get kind.nodeImage)
KIND_NODE_IMAGE_VERSION ?= $(KIND_VERSION)
KIND_BINARY_VERSION ?= $(shell YQ="$(YQ)" $(VERSIONS) --get kind.binary)
CI_KIND_NODE_IMAGE_VERSIONS_JSON ?= $(shell YQ="$(YQ)" $(VERSIONS) --get ci.kindNodeImages -o=json -I=0)
CI_PRIMARY_KIND_NODE_IMAGE_VERSION ?= $(shell YQ="$(YQ)" $(VERSIONS) --get ci.primaryKindNodeImage)

UNAMEO 	?=$(shell uname -o | tr A-Z a-z)
ifndef OS
	ifeq ($(findstring linux,$(UNAMEO)),linux)
		OS=linux
	else
		OS=darwin
	endif
endif
UNAMEM ?=$(shell uname -m | tr A-Z a-z)
ifndef ARCH
	ifeq ($(UNAMEM),x86_64)
		ARCH=amd64
	else ifeq ($(UNAMEM),aarch64)
		ARCH=arm64
	else
		ARCH=$(UNAMEM)
	endif
endif

## versions
GOLANGCI_LINT_VERSION ?= v2.13.1
KUSTOMIZE_VERSION ?= v5.4.1
CONTROLLER_TOOLS_VERSION ?= v0.21.0
GOCOVER_VERSION ?= v1.4.0
GINKGO_VERSION ?= v2.28.1
MOCKERY_VERSION ?= v3.7.0
CHAINSAW_VERSION ?= v0.2.15
HELM_VERSION ?= v4.1.4
HELMIFY_VERSION ?= v0.4.12
GO_LICENSES_VERSION ?= v2.0.1
ADDLICENSE_VERSION ?= v1.2.0
GOVULNCHECK_VERSION ?= v1.3.0
YQ_VERSION ?= v4.44.3

## ctlptl (local cluster + registry management)
CTLPTL_VERSION ?= v0.9.4



.PHONY: install-deps
install-deps: golangci-lint kustomize controller-gen envtest gocover-cobertura ginkgo mockery chainsaw helm helmify go-licenses addlicense govulncheck ctlptl yq ## Install all dependencies

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
golangci-lint: ## Download golangci locally if necessary. 
	@[ -f $(GOLANGCI_LINT) ] || { \
	set -e ;\
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh | sh -s -- -b $(shell dirname $(GOLANGCI_LINT)) $(GOLANGCI_LINT_VERSION) ;\
	}


KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOCOVER_COBERTURA ?= $(LOCALBIN)/gocover-cobertura
GINKGO ?= $(LOCALBIN)/ginkgo
MOCKERY ?= $(LOCALBIN)/mockery
CHAINSAW ?= $(LOCALBIN)/chainsaw
HELMIFY ?= $(LOCALBIN)/helmify
HELM ?= $(LOCALBIN)/helm
CTLPTL ?= $(LOCALBIN)/ctlptl
CTLPTL_OS = $(if $(filter darwin,$(OS)),mac,$(OS))
CTLPTL_ARCH = $(if $(filter amd64,$(ARCH)),x86_64,$(ARCH))
CTLPTL_VERSION_NO_V = $(patsubst v%,%,$(CTLPTL_VERSION))

.PHONY: $(LOCALBIN) kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || GOBIN=$(LOCALBIN) GO111MODULE=on go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: $(LOCALBIN) controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: $(LOCALBIN) envtest
envtest: $(ENVTEST) yq ## Download envtest-setup locally if necessary.
	$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.22

.PHONY: $(LOCALBIN) gocover-cobertura
gocover-cobertura: ## Download gocover-cobertura locally if necessary.
	test -s $(LOCALBIN)/gocover-cobertura || GOBIN=$(LOCALBIN) go install github.com/boumenot/gocover-cobertura@$(GOCOVER_VERSION)

.PHONY: ginkgo
ginkgo: $(LOCALBIN)  ## Download ginkgo locally if necessary.
	test -s $(LOCALBIN)/ginkgo || GOBIN=$(LOCALBIN) go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

.PHONY: mockery
mockery: $(LOCALBIN)  ## Download mockery locally if necessary.
	test -s $(LOCALBIN)/mockery ||  GOBIN=$(LOCALBIN) go install github.com/vektra/mockery/v3@$(MOCKERY_VERSION)

.PHONY: chainsaw
chainsaw: $(LOCALBIN)  ## Download chainsaw binary if necessary.
	test -s $(LOCALBIN)/chainsaw || curl -sSfL https://github.com/kyverno/chainsaw/releases/download/$(CHAINSAW_VERSION)/chainsaw_$(OS)_$(ARCH).tar.gz | \
		tar --no-same-owner -zxv -C $(LOCALBIN) chainsaw

.PHONY: ctlptl
ctlptl: $(LOCALBIN) ## Download ctlptl binary if necessary.
	test -s $(LOCALBIN)/ctlptl || curl -sSfL \
	    https://github.com/tilt-dev/ctlptl/releases/download/$(CTLPTL_VERSION)/ctlptl.$(CTLPTL_VERSION_NO_V).$(CTLPTL_OS).$(CTLPTL_ARCH).tar.gz | \
	    tar --no-same-owner -zxv -C $(LOCALBIN) ctlptl

.PHONY: helm
helm: $(LOCALBIN) ## Download helm locally if necessary.
	test -s $(LOCALBIN)/helm || curl -s -L https://get.helm.sh/helm-$(HELM_VERSION)-$(OS)-$(ARCH).tar.gz |\
		tar --no-same-owner --strip-components=1 -zxv -C $(LOCALBIN) $(OS)-$(ARCH)/helm

.PHONY: helmify
helmify: $(LOCALBIN)  ## Download helmify locally if necessary.
	test -s $(LOCALBIN)/helmify || GOBIN=$(LOCALBIN) go install github.com/arttor/helmify/cmd/helmify@$(HELMIFY_VERSION)

.PHONY: go-licenses
go-licenses: $(LOCALBIN)  ## Download  go-licenses locally if necessary.
	@# Version-checked rather than the bare `test -s` the neighbouring tools use.
	@# v1 picks one license at random from a multi-license file while v2 reports
	@# the full set deterministically, so a stale v1 binary produces notices that
	@# fail `make notices-check` in CI with nothing on screen to explain why.
	@if ! go version -m $(LOCALBIN)/go-licenses 2>/dev/null | grep -q "go-licenses/v2[[:space:]]*$(GO_LICENSES_VERSION)"; then \
		GOBIN=$(LOCALBIN) go install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION); \
	fi

ADDLICENSE ?= $(LOCALBIN)/addlicense
.PHONY: addlicense
addlicense: $(LOCALBIN)  ## Download addlicense locally if necessary.
	test -s $(ADDLICENSE) || GOBIN=$(LOCALBIN) go install github.com/google/addlicense@$(ADDLICENSE_VERSION)

GOVULNCHECK ?= $(LOCALBIN)/govulncheck
.PHONY: govulncheck
govulncheck: $(LOCALBIN) ## Download govulncheck locally if necessary.
	test -s $(GOVULNCHECK) || GOBIN=$(LOCALBIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: yq
yq: $(YQ) ## Download yq locally if necessary.
$(YQ): $(LOCALBIN)
	test -s $(YQ) || GOBIN=$(LOCALBIN) go install github.com/mikefarah/yq/v4@$(YQ_VERSION)
