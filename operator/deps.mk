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
GOLANGCI_LINT_VERSION ?= v2.10.1
KUSTOMIZE_VERSION ?= v5.4.1
CONTROLLER_TOOLS_VERSION ?= v0.18.0
ENVTEST_K8S_VERSION ?= 1.35.0
GOCOVER_VERSION ?= v1.4.0
GINKGO_VERSION ?= v2.27.2
MOCKERY_VERSION ?= v3.5.0
CHAINSAW_VERSION ?= v0.2.14
HELM_VERSION ?= v3.18.5
HELMIFY_VERSION ?= v0.4.12
GO_LICENSES_VERSION ?= v1.6.0

## ctlptl (local cluster + registry management)
CTLPTL_VERSION ?= v0.9.3



.PHONY: install-deps
install-deps: golangci-lint kustomize controller-gen envtest gocover-cobertura ginkgo mockery chainsaw helm helmify go-licenses ctlptl ## Install all dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
golangci-lint: ## Download golangci locally if necessary. 
	@[ -f $(GOLANGCI_LINT) ] || { \
	set -e ;\
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell dirname $(GOLANGCI_LINT)) $(GOLANGCI_LINT_VERSION) ;\
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
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.22
	$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)

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
	$(LOCALBIN)/helm plugin list | grep cm-push > /dev/null || $(LOCALBIN)/helm plugin install https://github.com/chartmuseum/helm-push

.PHONY: helmify
helmify: $(LOCALBIN)  ## Download helmify locally if necessary.
	test -s $(LOCALBIN)/helmify || GOBIN=$(LOCALBIN) go install github.com/arttor/helmify/cmd/helmify@$(HELMIFY_VERSION)

.PHONY: go-licenses
go-licenses: $(LOCALBIN)  ## Download  go-licenses locally if necessary.
	test -s $(LOCALBIN)/go-licenses || GOBIN=$(LOCALBIN) go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
