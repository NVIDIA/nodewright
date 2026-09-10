# Copyright (c) NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

include deps.mk

## Version of the operator
VERSION ?= $(GIT_TAG_LAST)
# Go toolchain version read from go.mod (the single source; also used by CI's
# setup-go go-version-file and the image build-arg). Prefer a toolchain
# directive over the go directive, matching how setup-go resolves the version.
GO_VERSION ?= $(shell awk '/^go /{go=$$2} /^toolchain /{tc=$$2; sub(/^go/,"",tc)} END{print (tc!=""?tc:go)}' $(CURDIR)/go.mod)
DEBIAN_VERSION ?= trixie
DISTROLESS_VERSION ?= 4.0.0

# Image URL to use all building/pushing image 
## TODO: update this to the correct image location
IMG_REPO ?= ghcr.io/nvidia/skyhook
IMG ?= $(IMG_REPO)/operator
LOCAL_REGISTRY ?= localhost:5005
LOCAL_OPERATOR_IMG ?= $(LOCAL_REGISTRY)/skyhook-operator:testing

PLATFORM := $(shell uname -s 2>/dev/null || echo unknown)
SKYHOOK_NAMESPACE ?= nodewright
METRICS_TEST_SERVICE_ACCOUNT ?= metrics-reader

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN := $(shell go env GOPATH)/bin
else
GOBIN := $(shell go env GOBIN)
endif

GIT_SHA 		:= $(shell git rev-parse --short HEAD)
GIT_TAG_LAST 	:= $(shell git tag --list 'operator*' --sort=-v:refname | head -n 1 | cut -d/ -f2)
CLI_TAG_LAST 	:= $(shell git tag --list 'cli/*' --sort=-v:refname | head -n 1 | cut -d/ -f2)

## GO Flags
GO_LDFLAGS  := -ldflags "-X github.com/NVIDIA/nodewright/operator/internal/version.GIT_SHA=$(GIT_SHA) \
	-X github.com/NVIDIA/nodewright/operator/internal/version.VERSION=$(VERSION)"
GOFLAGS 	:= -mod=vendor

# DOCKER_CMD defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
DOCKER_CMD ?= docker

ifdef CI
	DOCKER_CMD = docker
endif

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
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: clean
clean: ## Clears out the local build folder
	chmod -R +w $(LOCALBIN)/* ## some binaries permissions are r+x
	rm -rf $(LOCALBIN)/*
	rm -rf $(REPORTING)/*


define util_confirm_code
$(eval confirm := $(shell read -p "⚠ Are you sure? [y/n] > " -r; echo $$REPLY))
$(if $(filter y Y,$(confirm)),1)
endef
#NOTE: We must call this one to remove \n or any other spaces
util_confirm_ask = $(strip $(util_confirm_code))

.PHONY: confirm
confirm:
	$(if $(util_confirm_ask), echo "User said yes", echo "User said no"; exit 1)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	$(MAKE) license-fmt ## fix up license headers

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object paths="./..."
	$(MAKE) license-fmt ## fix up license headers

.PHONY: generate-mocks
generate-mocks: mockery ## Generate code for interface mocking
	$(MOCKERY) --config .mockery.yaml
	$(MAKE) fmt ## fix up license headers

# go-licenses v2 refuses to resolve standard-library packages ("does not have
# module info") instead of skipping them as v1 did, so both targets below have
# to ignore them explicitly. --ignore matches by PREFIX, so this passes full
# stdlib package paths: reducing them to first path segments yields the bare
# token `go` (from go/ast), which then also matches every module under
# go.opentelemetry.io, golang.org/x, google.golang.org and gopkg.in.
GO_LICENSES_IGNORE = $$(go list std | paste -sd, -),$$(go list -m)

license-report: go-licenses ## Run run license report
	$(LOCALBIN)/go-licenses report ./... --ignore=$(GO_LICENSES_IGNORE) > $(REPORTING)/licenses.csv
	@echo "report at $(REPORTING)/licenses.csv"

.PHONY: license-check
license-check: go-licenses ## Run go-licenses check against code.
	$(LOCALBIN)/go-licenses check --allowed_licenses=MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,Zlib --ignore=$(GO_LICENSES_IGNORE) ./...

.PHONY: vulncheck
vulncheck: govulncheck ## Run govulncheck against code; fails on any reachable vulnerability.
	GOFLAGS="$(GOFLAGS)" $(GOVULNCHECK) -mode source ./...

.PHONY: vulncheck-all
vulncheck-all: govulncheck ## Run govulncheck in verbose mode, including module-level vulns not reachable from this code.
	GOFLAGS="$(GOFLAGS)" $(GOVULNCHECK) -mode source -show verbose ./...

## addlicense takes explicit paths, so enumerate only tracked operator sources and
## leave repository-wide formatting to the root Makefile. vendor/ carries upstream headers.
LICENSE_HOLDER ?= NVIDIA CORPORATION & AFFILIATES
LICENSE_TEMPLATE ?= ../scripts/license-header.tmpl
license_files = git ls-files -- '*.go' '*.py' '*.sh' '*.yaml' '*.yml' 'Dockerfile' '*.Dockerfile' \
	| grep -vE '^vendor/'

.PHONY: license-fmt
license-fmt: addlicense ## Add the Apache-2.0 header to any source file missing one.
	$(license_files) | xargs $(ADDLICENSE) -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)

## addlicense -check only asserts that *some* license marker is present, so an MIT or
## malformed header would pass. The old format_license.py replaced a wrong header
## outright; assert the SPDX identifier explicitly to keep that guarantee.
.PHONY: license-header-check
license-header-check: addlicense ## Fail if any tracked source file is missing an Apache-2.0 header.
	$(license_files) | xargs $(ADDLICENSE) -check -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)
	@wrong=$$($(license_files) | while read -r f; do \
		grep -qE '^.{1,2} Code generated .* DO NOT EDIT\.$$' "$$f" && continue; \
		head -20 "$$f" | grep -q 'SPDX-License-Identifier: Apache-2.0' || echo "  $$f"; \
	done); \
	if [ -n "$$wrong" ]; then \
		echo "ERROR: header present but not Apache-2.0:"; echo "$$wrong"; exit 1; \
	fi

.PHONY: fmt
fmt: license-fmt ## Run go fmt against code.
	go fmt $(GOFLAGS) ./...


.PHONY: vet
vet: ## Run go vet against code.
	go vet $(GOFLAGS) ./...

REPORTING ?= $(shell pwd)/reporting
.PHONY: reporting
reporting: $(REPORTING)
$(REPORTING):
	mkdir -p $@

CTLPTL_CONFIG_TEMPLATE ?= config/local-dev/ctlptl-config.yaml
CTLPTL_CONFIG_RENDERED ?= $(REPORTING)/ctlptl-config.yaml

.PHONY: print-k8s-test-versions-github-output
print-k8s-test-versions-github-output: ## Print Kubernetes test versions in GitHub Actions output format.
	@$(MAKE) --no-print-directory yq >/dev/null
	@YQ="$(YQ)" $(VERSIONS) --print

## Docker Hub resets connections often enough that an unretried inspect fails CI
## with "not published" for an image that is published. RETRY only wraps reads.
RETRY ?= ../scripts/retry.sh

.PHONY: validate-kind-node-image
validate-kind-node-image: yq ## Check that the configured kindest/node image tag exists before creating a cluster.
	@echo "Validating kindest/node:v$(KIND_NODE_IMAGE_VERSION)"
	@if command -v $(DOCKER_CMD) >/dev/null 2>&1; then \
		if [ "$(DOCKER_CMD)" = "podman" ]; then \
			$(RETRY) -- $(DOCKER_CMD) manifest inspect docker://kindest/node:v$(KIND_NODE_IMAGE_VERSION) >/dev/null; \
		else \
			$(RETRY) -- $(DOCKER_CMD) manifest inspect kindest/node:v$(KIND_NODE_IMAGE_VERSION) >/dev/null; \
		fi; \
	elif command -v docker >/dev/null 2>&1; then \
		$(RETRY) -- docker manifest inspect kindest/node:v$(KIND_NODE_IMAGE_VERSION) >/dev/null; \
	elif command -v podman >/dev/null 2>&1; then \
		$(RETRY) -- podman manifest inspect docker://kindest/node:v$(KIND_NODE_IMAGE_VERSION) >/dev/null; \
	else \
		curl -fsSL --retry 3 --retry-delay 5 --retry-all-errors https://hub.docker.com/v2/repositories/kindest/node/tags/v$(KIND_NODE_IMAGE_VERSION) >/dev/null; \
	fi || { \
		echo "kindest/node:v$(KIND_NODE_IMAGE_VERSION) is not published or cannot be inspected."; \
		echo "Update kind.nodeImage in versions.yaml, or override KIND_NODE_IMAGE_VERSION with a published kindest/node tag."; \
		exit 1; \
	}

.PHONY: render-ctlptl-config
render-ctlptl-config: reporting yq ## Render local ctlptl config using KIND_NODE_IMAGE_VERSION.
	sed 's|@@KIND_NODE_IMAGE_VERSION@@|$(KIND_NODE_IMAGE_VERSION)|g' $(CTLPTL_CONFIG_TEMPLATE) > $(CTLPTL_CONFIG_RENDERED)
	@echo "$(CTLPTL_CONFIG_RENDERED)"

.PHONY: test
test:: reporting manifests generate fmt vet lint build-cli unit-tests e2e-tests cli-e2e-tests helm-tests operator-agent-tests ## Run all tests.

ifndef CI
## we double define test so we can do thing different if in ci vs local
test:: merge-coverage
	echo "Total Code Coverage: $(shell go tool cover -func $(REPORTING)/cover.out | grep total | awk '{print $$NF}')"
	go tool cover -html=$(REPORTING)/cover.out
else
test:: merge-coverage gocover-cobertura
	echo "Total Code Coverage: $(shell go tool cover -func $(REPORTING)/cover.out | grep total | awk '{print $$NF}')"
	$(GOCOVER_COBERTURA) < $(REPORTING)/cover.out > $(REPORTING)/coverage.xml
endif

.PHONY: watch-tests
watch-tests: ## watch unit tests and auto run on changes.
	$(GINKGO) watch $(GOFLAGS) -p -vv ./...

.PHONY: unit-test
unit-tests: reporting manifests generate envtest ginkgo kill ## Run unit tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) $(GOFLAGS) --cover --coverprofile=unit.coverprofile --output-dir=$(REPORTING) -vv --trace --junit-report=$(REPORTING)/unit.xml --keep-going --timeout=180s ./...

## exec time is for things like scripts and commands, if we new more of these, might want to switch to a config file
## https://kyverno.github.io/chainsaw/latest/reference/commands/chainsaw_test/
## https://kyverno.github.io/chainsaw/latest/configuration/file/
# CHAINSAW_ARGS: --exec-timeout 30s is the fallback for any test that doesn't set
# spec.timeouts.exec. metrics_test.py's own TIMEOUT env var (also 30s default) can
# hit the same pod/container-startup cliff (#397) if a check races an in-flight
# package transition. Decision (#496): don't export TIMEOUT globally here — every
# metrics_test.py call outside simple-nodewright is already gated behind a native
# chainsaw `assert:` that waits for the same transition first, so a global bump
# would only slow down genuine failures across the whole suite for no benefit.
# Bump TIMEOUT per-script only where a metrics_test.py call is the *first* thing
# checking a transition (see nodewright/simple-nodewright for the pattern).
CHAINSAW_ARGS:=--exec-timeout 30s --parallel 1

# POOL_NON_DEFAULT lists every pool except the default (core). Adding a new
# pool: append it here AND to the matrix in .github/workflows/operator-ci.yaml.
POOL_NON_DEFAULT := interrupt uninstall lifecycle
POOL_NOTIN_SELECTOR := pool notin ($(shell echo $(POOL_NON_DEFAULT) | tr ' ' ','))

e2e-tests: chainsaw install run build-cli ensure-test-symlinks prepare-metrics-test-access ## Run end to end tests. Set POOL=<name> to run a single pool; unset runs all.
	## requires a cluster to be running with access
	## locally use kind to create clusters
	## in ci, the plan current is to have a real cluster, and create a node pool for testing
ifeq ($(POOL),core)
	SKYHOOK_NAMESPACE=$(SKYHOOK_NAMESPACE) METRICS_TEST_SERVICE_ACCOUNT=$(METRICS_TEST_SERVICE_ACCOUNT) $(CHAINSAW) test --test-dir ../k8s-tests/chainsaw/nodewright --selector '$(POOL_NOTIN_SELECTOR)' $(CHAINSAW_ARGS)
else ifneq ($(POOL),)
	SKYHOOK_NAMESPACE=$(SKYHOOK_NAMESPACE) METRICS_TEST_SERVICE_ACCOUNT=$(METRICS_TEST_SERVICE_ACCOUNT) $(CHAINSAW) test --test-dir ../k8s-tests/chainsaw/nodewright --selector 'pool=$(POOL)' $(CHAINSAW_ARGS)
else
	SKYHOOK_NAMESPACE=$(SKYHOOK_NAMESPACE) METRICS_TEST_SERVICE_ACCOUNT=$(METRICS_TEST_SERVICE_ACCOUNT) $(CHAINSAW) test --test-dir ../k8s-tests/chainsaw/nodewright $(CHAINSAW_ARGS)
endif
	$(MAKE) kill
	go tool covdata textfmt -i=$(REPORTING)/int -o reporting/int.coverprofile

cli-e2e-tests: chainsaw install run build-cli-with-coverage ensure-test-symlinks ## Run CLI end to end tests with coverage.
	## Tests the kubectl-nodewright CLI commands against a real cluster
	## Requires nodes labeled with skyhook.nvidia.com/test-node=skyhooke2e
	@mkdir -p $(REPORTING)/cli-cover
	@# Ensure bin/nodewright exists (build-cli-with-coverage only creates bin/nodewright-cover)
	@if [ ! -f bin/nodewright ]; then \
		if [ -f bin/nodewright-cover ]; then \
			cp bin/nodewright-cover bin/nodewright; \
		else \
			echo "Error: bin/nodewright-cover not found. Run build-cli-with-coverage first."; \
			exit 1; \
		fi; \
	fi
	@# Replace CLI binary with a wrapper that sets GOCOVERDIR for coverage collection
	@mv bin/nodewright bin/nodewright.orig 2>/dev/null || true
	@echo '#!/bin/bash' > bin/nodewright
	@echo '# Resolve actual script location (handles symlinks)' >> bin/nodewright
	@echo 'if [ -L "$${BASH_SOURCE[0]}" ]; then' >> bin/nodewright
	@echo '  SCRIPT_DIR="$$(cd "$$(dirname "$$(readlink -f "$${BASH_SOURCE[0]}")")" && pwd)"' >> bin/nodewright
	@echo 'else' >> bin/nodewright
	@echo '  SCRIPT_DIR="$$(cd "$$(dirname "$${BASH_SOURCE[0]}")" && pwd)"' >> bin/nodewright
	@echo 'fi' >> bin/nodewright
	@echo 'export GOCOVERDIR="$$SCRIPT_DIR/../reporting/cli-cover"' >> bin/nodewright
	@echo 'exec "$$SCRIPT_DIR/nodewright-cover" "$$@"' >> bin/nodewright
	@chmod +x bin/nodewright
	@# Update symlinks to point to the new wrapper script
	@$(MAKE) ensure-test-symlinks
	@# Run the CLI e2e tests, then ALWAYS stop the operator and restore the real CLI
	@# binary even when chainsaw fails: otherwise a failed run leaves bin/nodewright as
	@# the coverage wrapper and the operator still running. The chainsaw exit status is
	@# preserved so the target still fails.
	@status=0; \
		$(CHAINSAW) test --test-dir ../k8s-tests/chainsaw/cli $(CHAINSAW_ARGS) || status=$$?; \
		$(MAKE) kill; \
		mv bin/nodewright.orig bin/nodewright 2>/dev/null || true; \
		exit $$status
	@# Extract coverage from operator running in cluster
	go tool covdata textfmt -i=$(REPORTING)/int -o reporting/cli-int.coverprofile
	@# Extract coverage from CLI binary if any was generated
	@if [ -d "$(REPORTING)/cli-cover" ] && [ -n "$$(ls -A $(REPORTING)/cli-cover 2>/dev/null)" ]; then \
		go tool covdata textfmt -i=$(REPORTING)/cli-cover -o=$(REPORTING)/cli-e2e.coverprofile; \
		echo "✅ CLI coverage collected"; \
	else \
		echo "ℹ️  No CLI coverage data (this is OK - CLI is covered by unit tests)"; \
	fi


helm-tests: helm chainsaw ctlptl ensure-test-symlinks push-local-image
	## Here we need to run the operator so that the old CRD can deleted along with
	## any leftover SCRs. Without this the SCRs may have finalizers which rely on
	## the operator and will cause the deletion and tests to hang.
	$(MAKE) run
	$(MAKE) uninstall ignore-not-found=true
	$(MAKE) kill
	LOCAL_OPERATOR_IMG=$(LOCAL_OPERATOR_IMG) $(CHAINSAW) test --test-dir ../k8s-tests/chainsaw/helm $(CHAINSAW_ARGS)



.PHONY: push-local-image
push-local-image: docker-build ## Tag and push the locally-built operator image to the local ctlptl registry
	$(DOCKER_CMD) tag $(IMG):testing $(LOCAL_OPERATOR_IMG)
	$(DOCKER_CMD) push $(LOCAL_OPERATOR_IMG)



.PHONY: rollout-local
rollout-local: push-local-image ## push locally-built operator and restart the deployment
	@# control-plane=controller-manager is the only label both install paths agree on:
	@# the chart renders app.kubernetes.io/name=nodewright while the kustomize overlay
	@# renders app.kubernetes.io/name=deployment (namePrefix renames resources, not labels).
	$(KUBECTL) -n $(SKYHOOK_NAMESPACE) rollout restart deployment -l control-plane=controller-manager
	$(KUBECTL) -n $(SKYHOOK_NAMESPACE) rollout status deployment -l control-plane=controller-manager --timeout=60s


.PHONY: delete-kind-cluster
delete-kind-cluster: ctlptl render-ctlptl-config ## delete the local ctlptl kind cluster and registry
	$(CTLPTL) delete -f $(CTLPTL_CONFIG_RENDERED)

## build-cli because the suite shells out to bin/nodewright to reset prior state, the same
## reason e2e-tests declares it. Three of the four tests hide a missing binary behind
## `|| true`, so without this only reap_old_logs fails, and it fails as exit 127 rather than
## anything that points at the cause.
operator-agent-tests: chainsaw install build-cli ## Run operator agent tests.
	@if [ -z "$(AGENT_IMAGE)" ]; then \
		echo "Error: AGENT_IMAGE is not set. Please set it to the agent image to test against."; \
		echo "Example: AGENT_IMAGE=ghcr.io/nvidia/nodewright/agent:v6.4.2 make operator-agent-tests"; \
		exit 1; \
	fi
	@echo "Running operator-agent tests with AGENT_IMAGE=$(AGENT_IMAGE)"
	../k8s-tests/operator-agent/setup.sh kind-worker setup
	$(MAKE) run
	$(CHAINSAW) test --test-dir ../k8s-tests/operator-agent $(CHAINSAW_ARGS)
	$(MAKE) kill
	## ../k8s-tests/operator-agent/setup.sh kind-worker teardown
	go tool covdata textfmt -i=$(REPORTING)/int -o reporting/int.coverprofile

.PHONY: migration-test
migration-test: helm push-local-image ## Run the Skyhook->NodeWright upgrade migration test (needs a fresh kind cluster).
	## Requires a CLEAN cluster: the test refuses to run if nodewright CRDs already
	## exist, since a pre-rename baseline is the whole point. Run
	## `make create-kind-cluster` first, and note push-local-image must come AFTER
	## cluster creation because ctlptl recreates the local registry with the cluster.
	LOCAL_OPERATOR_IMG=$(LOCAL_OPERATOR_IMG) HELM=$(HELM) KUBECTL=$(KUBECTL) \
		../k8s-tests/migration/run.sh

deployment-policy-tests: chainsaw install run ensure-test-symlinks prepare-metrics-test-access ## Run all deployment policy E2E tests (requires 15-node cluster).
	## requires a 15-node cluster to be running with access
	## use 'make create-deployment-policy-cluster' to create the cluster
	SKYHOOK_NAMESPACE=$(SKYHOOK_NAMESPACE) METRICS_TEST_SERVICE_ACCOUNT=$(METRICS_TEST_SERVICE_ACCOUNT) $(CHAINSAW) test --test-dir ../k8s-tests/chainsaw/deployment-policy $(CHAINSAW_ARGS)
	$(MAKE) kill
	go tool covdata textfmt -i=$(REPORTING)/int -o reporting/int.coverprofile

.PHONY: create-deployment-policy-cluster
create-deployment-policy-cluster: validate-kind-node-image ## Create a 15-node Kind cluster for deployment policy tests.
	@echo "🔧 Creating 15-node Kind cluster for deployment policy tests..."
	@# Fix for Podman: increase kernel keyring quota
	@if command -v podman >/dev/null 2>&1; then \
		echo "📝 Detected Podman - increasing kernel keyring quota..."; \
		if [ "$$(uname)" = "Darwin" ]; then \
			podman machine ssh sudo sysctl -w kernel.keys.maxkeys=20000 2>/dev/null || true; \
			podman machine ssh sudo sysctl -w kernel.keys.maxbytes=400000 2>/dev/null || true; \
		else \
			sudo sysctl -w kernel.keys.maxkeys=20000 2>/dev/null || true; \
			sudo sysctl -w kernel.keys.maxbytes=400000 2>/dev/null || true; \
		fi; \
	fi
	kind delete cluster --name skyhook-dp-test 2>/dev/null || true
	kind create cluster --name skyhook-dp-test --config ../k8s-tests/chainsaw/deployment-policy/kind-config.yaml --image=kindest/node:v$(KIND_NODE_IMAGE_VERSION)

.PHONY: delete-deployment-policy-cluster
delete-deployment-policy-cluster: ## Delete the 15-node Kind cluster for deployment policy tests.
	kind delete cluster --name skyhook-dp-test 2>/dev/null || true
	kubectl config use-context kind-kind || true

ifeq ($(DOCKER_CMD),docker)
DOCKER_AUTH_FILE=${HOME}/.docker/config.json
else
DOCKER_AUTH_FILE=${HOME}/.config/containers/auth.json
endif

setup-kind-cluster: ## setup kind cluster with local docker creds and nodewright namespace
	## sets you local $(DOCKER_CMD) creds into a secret in kind in the nodewright namespace
	## Dual-labelled during the NodeWright rename: the skyhook and deployment-policy
	## pools select nodewright.nvidia.com/test-node; the not-yet-migrated cli pool still
	## selects skyhook.nvidia.com/test-node. Drop the skyhook label once the cli pool moves.
	$(KUBECTL) label node/kind-worker skyhook.nvidia.com/test-node=skyhooke2e --overwrite
	$(KUBECTL) label node/kind-worker nodewright.nvidia.com/test-node=skyhooke2e --overwrite
	$(KUBECTL) create namespace $(SKYHOOK_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@if [ -f "$(DOCKER_AUTH_FILE)" ]; then \
		$(KUBECTL) create secret generic node-init-secret --type=kubernetes.io/dockerconfigjson -n $(SKYHOOK_NAMESPACE) \
			--from-file=.dockerconfigjson=$(DOCKER_AUTH_FILE) --dry-run=client -o yaml | $(KUBECTL) apply -f -; \
	else \
		echo "Skipping node-init-secret creation; docker auth file $(DOCKER_AUTH_FILE) not found"; \
	fi

.PHONY: prepare-metrics-test-access
prepare-metrics-test-access:
	$(KUBECTL) -n $(SKYHOOK_NAMESPACE) create serviceaccount $(METRICS_TEST_SERVICE_ACCOUNT) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) create clusterrole metrics-test-reader --verb=get --non-resource-url=/metrics \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) create clusterrolebinding metrics-test-reader-access --clusterrole=metrics-test-reader \
		--serviceaccount=$(SKYHOOK_NAMESPACE):$(METRICS_TEST_SERVICE_ACCOUNT) --dry-run=client -o yaml | $(KUBECTL) apply -f -


create-kind-cluster: ctlptl validate-kind-node-image render-ctlptl-config ## creates or updates a kind cluster.
	$(CTLPTL) delete -f $(CTLPTL_CONFIG_RENDERED) || true
	$(CTLPTL) apply -f $(CTLPTL_CONFIG_RENDERED)
	$(MAKE) setup-kind-cluster

podman-create-machine: ## creates a podman machine
	podman machine stop podman-machine-default || true
	podman machine rm -f podman-machine-default || true
	podman machine init --cpus=6 -m=12288 --disk-size=300 podman-machine-default
	podman machine start podman-machine-default

podman-restart:
	podman machine stop && podman machine start

colima-create-machine:
	colima delete -f
	colima start --runtime docker --vm-type vz --cpu 6 --memory 12 --disk 100

	colima ssh -- sudo sysctl -w kernel.keys.maxkeys=20000
	colima ssh -- sudo sysctl -w kernel.keys.maxbytes=400000                 
	colima ssh -- sudo sysctl -w fs.inotify.max_user_watches=524288
	colima ssh -- sudo sysctl -w fs.inotify.max_user_instances=512

sedrp := sed -r -i
ifeq ($(PLATFORM),Darwin)
	sedrp = sed -r -i ''
endif

# Paths whose statements are dropped before coverage totals are reported.
# THIS IS THE ONLY DEFINITION — the Coveralls upload job calls filter-coverage
# rather than repeating the pattern in YAML, because Coveralls itself has no
# exclusion mechanism for Go profiles (no .coveralls.yml key, no action input),
# so whatever is uploaded is what gets counted.
#
# Generated code only. controller-gen writes zz_generated.deepcopy.go and no one
# should ever hand-write a test for DeepCopyInto, yet it is 916 statements —
# a quarter of the repo's uncovered code — which drags the headline number down
# by roughly 4 points and hides the coverage of code people actually write.
#
# Do not add hand-written code here to make a number look better. If something
# is genuinely untestable, that is worth a comment at the call site, not a
# silent exclusion.
COVERAGE_EXCLUDE ?= zz_generated\.deepcopy\.go

.PHONY: merge-coverage
merge-coverage: ## Merge per-suite coverage profiles into reporting/cover.out.
	@if ls $(REPORTING)/*.coverprofile 1> /dev/null 2>&1; then \
		cat $(REPORTING)/*.coverprofile > reporting/temp-cover.out; \
		echo "mode: set" > $(REPORTING)/cover.out; \
		tail -n +2 $(REPORTING)/temp-cover.out | sed  '/mode: set/d' >> $(REPORTING)/cover.out; \
		$(sedrp) 's|^/.*nodewright/operator/(.*)$$|github\.com/NVIDIA/nodewright/operator/\1|g' $(REPORTING)/cover.out; \
	else \
		echo "No coverage files found - skipping merge"; \
	fi
	@$(MAKE) --no-print-directory filter-coverage

# The guard below is deliberately paranoid, because every way this can go wrong
# ends in the same place: an empty profile that replaces a good one and reports
# 0% to Coveralls, with nothing in the log to say the pattern was at fault.
#   - an empty COVERAGE_EXCLUDE makes `grep -v -E ''` match every line (exit 1)
#   - a pattern that matches everything does the same (exit 1)
#   - an invalid ERE truncates the redirect target *before* grep reports the
#     error (exit 2), so the output file is already destroyed by then
# Hence: refuse on any exit above 1, and refuse on an empty result whatever the
# exit status, leaving cover.out untouched in both cases.
.PHONY: filter-coverage
filter-coverage: ## Drop $(COVERAGE_EXCLUDE) paths from reporting/cover.out.
	@if [ ! -f $(REPORTING)/cover.out ]; then exit 0; fi; \
	if [ -z '$(COVERAGE_EXCLUDE)' ]; then \
		echo "coverage: COVERAGE_EXCLUDE is empty, reporting the unfiltered profile"; \
		exit 0; \
	fi; \
	before=$$(wc -l < $(REPORTING)/cover.out | tr -d '[:space:]'); \
	if grep -v -E '$(COVERAGE_EXCLUDE)' $(REPORTING)/cover.out > $(REPORTING)/cover.filtered.out; then \
		status=0; \
	else \
		status=$$?; \
	fi; \
	if [ $$status -gt 1 ] || [ ! -s $(REPORTING)/cover.filtered.out ]; then \
		rm -f $(REPORTING)/cover.filtered.out; \
		echo "ERROR: COVERAGE_EXCLUDE='$(COVERAGE_EXCLUDE)' produced no usable profile (grep exit $$status)." >&2; \
		echo "       $(REPORTING)/cover.out left unchanged." >&2; \
		exit 1; \
	fi; \
	mv $(REPORTING)/cover.filtered.out $(REPORTING)/cover.out; \
	after=$$(wc -l < $(REPORTING)/cover.out | tr -d '[:space:]'); \
	echo "coverage: excluded $$((before - after)) of $$before profile blocks matching '$(COVERAGE_EXCLUDE)'"

.PHONY: lint
lint: golangci-lint license-header-check ## Run golangci-lint linter & yamllint
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

##@ Build

.PHONY: build
build: build-manager build-cli ## Build manager and CLI binaries.

.PHONY: build-manager
build-manager: manifests generate fmt vet lint ## Build manager binary.
	go build $(GOFLAGS) $(GO_LDFLAGS) -o bin/manager cmd/manager/main.go

.PHONY: ensure-test-symlinks
ensure-test-symlinks:  ## Ensure symlinks exist in test directories (idempotent).
	@# Create symlinks in test directories for easy access
	@# This target is idempotent and can be called even if binaries are cached
	@# Note: symlinks will be created even if bin/nodewright doesn't exist yet (it will be created by build targets)
	@for dir in cli deployment-policy nodewright helm; do \
		ln -sf ../../../operator/bin/nodewright ../k8s-tests/chainsaw/$$dir/nodewright-cli 2>/dev/null || true; \
	done

.PHONY: build-cli
CLI_VERSION ?= $(CLI_TAG_LAST)
CLI_LDFLAGS := -ldflags "-X github.com/NVIDIA/nodewright/operator/internal/version.VERSION=$(CLI_VERSION) -X github.com/NVIDIA/nodewright/operator/internal/version.GIT_SHA=$(GIT_SHA)"
build-cli: ensure-test-symlinks ## Build CLI binary.
	go build $(GOFLAGS) $(CLI_LDFLAGS) -o bin/nodewright cmd/cli/main.go
CLI_DIST_DIR ?= dist
cli-release-build: ## Build CLI binaries for all platforms (used by CI release workflow).
	@echo "Building CLI $(CLI_VERSION) for all platforms..."
	@mkdir -p $(CLI_DIST_DIR)
	@# Build for each platform
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			echo "  Building $$os/$$arch..."; \
			ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(GOFLAGS) \
				-ldflags "-X github.com/NVIDIA/nodewright/operator/internal/version.VERSION=$(CLI_VERSION) -X github.com/NVIDIA/nodewright/operator/internal/version.GIT_SHA=$(GIT_SHA)" \
				-o $(CLI_DIST_DIR)/nodewright_$(CLI_VERSION)_$${os}_$${arch}$${ext} cmd/cli/main.go; \
		done; \
	done
	@# Create archives (rename binary to just "nodewright" inside archive)
	@echo "Creating archives..."
	@# Keep these as ';'-separated commands. Chaining them with '&&' looks
	@# equivalent but defeats .SHELLFLAGS=-ec: set -e exempts every command in
	@# an && list except the last, so a failed cp or a tar that dies on a full
	@# disk would let the loop run on and the recipe exit 0, publishing a
	@# release with a missing or truncated archive.
	@cd $(CLI_DIST_DIR) && for os in linux darwin; do \
		for arch in amd64 arm64; do \
			cp nodewright_$(CLI_VERSION)_$${os}_$${arch} nodewright; \
			tar -czf nodewright_$(CLI_VERSION)_$${os}_$${arch}.tar.gz nodewright; \
			rm nodewright; \
		done; \
	done
	@cd $(CLI_DIST_DIR) && for arch in amd64 arm64; do \
		cp nodewright_$(CLI_VERSION)_windows_$${arch}.exe nodewright.exe; \
		zip -q nodewright_$(CLI_VERSION)_windows_$${arch}.zip nodewright.exe; \
		rm nodewright.exe; \
	done
	@# Generate checksums
	@echo "Generating checksums..."
	@cd $(CLI_DIST_DIR) && shasum -a 256 *.tar.gz *.zip > checksums.txt
	@echo "Done! Artifacts in $(CLI_DIST_DIR)/"

build-cli-with-coverage: ensure-test-symlinks ## Build CLI binary with coverage instrumentation.
	go build $(GOFLAGS) -cover $(CLI_LDFLAGS) -o bin/nodewright-cover cmd/cli/main.go
	@# Ensure bin/nodewright exists (copy from cover binary if not cached) so symlinks work
	@if [ ! -f bin/nodewright ]; then \
		cp bin/nodewright-cover bin/nodewright; \
	fi

.PHONY: run
ENABLE_WEBHOOKS?=false
BACKGROUND?=true
AGENT_IMAGE?=$(IMG_REPO)/agentless:6.2.0
LOG_LEVEL?=info
ifndef CI
run: manifests generate reporting install kill ## Run a controller from your host.
else
run: manifests generate fmt vet lint reporting install kill ## Run a controller from your host.
endif
	mkdir -p $(REPORTING)/int
	rm -rf $(REPORTING)/int/*
	go build $(GOFLAGS) -cover $(GO_LDFLAGS) -o $(LOCALBIN)/manager cmd/manager/main.go
ifeq ($(BACKGROUND),true)
	LOG_LEVEL=$(LOG_LEVEL) AGENT_IMAGE=$(AGENT_IMAGE) ENABLE_WEBHOOKS=$(ENABLE_WEBHOOKS) GOCOVERDIR=$(REPORTING)/int nohup $(LOCALBIN)/manager > $(REPORTING)/int/std.out & echo "$$!" > $(REPORTING)/int/run.PID
else 
	LOG_LEVEL=$(LOG_LEVEL) AGENT_IMAGE=$(AGENT_IMAGE) ENABLE_WEBHOOKS=$(ENABLE_WEBHOOKS) GOCOVERDIR=$(REPORTING)/int $(LOCALBIN)/manager > $(REPORTING)/int/std.out
endif

kill:
	! test -s $(REPORTING)/int/run.PID || kill -15 $(shell cat $(REPORTING)/int/run.PID) || rm $(REPORTING)/int/run.PID
	# some times it gets strange, so try to clean that up with a backup pgrep kill
	-pgrep ^manager$$ ; [ "$$?" -ne "0" ] || (echo "kill failed, manager still running"; kill -15 $(shell pgrep ^manager$$))


# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(DOCKER_CMD) build -f ../containers/operator.Dockerfile \
	--build-arg VERSION=$(VERSION) \
	--build-arg GIT_SHA=$(GIT_SHA) \
	--build-arg GO_VERSION=$(GO_VERSION) \
	--build-arg DEBIAN_VERSION=$(DEBIAN_VERSION) \
	--build-arg DISTROLESS_VERSION=$(DISTROLESS_VERSION) \
	-t $(IMG):testing .

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

create-namespace: ## Create the namespace in the K8s cluster specified in ~/.kube/config.
	$(KUBECTL) create namespace $(SKYHOOK_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -

.PHONY: install
install: manifests kustomize create-namespace license-fmt ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: install-helm-chart
install-helm-chart: helm create-namespace ## Install helm chart into the K8s cluster specified in ~/.kube/config.
	$(HELM) install skyhook-operator ../chart -n $(SKYHOOK_NAMESPACE) 

.PHONY: uninstall-helm-chart
uninstall-helm-chart: helm ## Uninstall helm chart from the K8s cluster specified in ~/.kube/config.
	$(HELM) uninstall skyhook-operator -n $(SKYHOOK_NAMESPACE)

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}:latest
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

generate-helm: confirm manifests kustomize helm helmify ## Generates new helm chart using helmify. Be-careful, this can break things, it overwrites files, make sure to look at you git diff.
	$(KUSTOMIZE) build config/default | $(HELMIFY) -original-name

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -


.PHONY: metrics
metrics:
	helm repo add grafana https://grafana.github.io/helm-charts
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update

	../docs/observability/generate-dashboards.sh
	$(KUBECTL) apply -f ../docs/observability/grafana-dashboards-configmap.yaml

	helm upgrade --install prometheus prometheus-community/prometheus -f ../docs/observability/prometheus_values.yaml
	helm upgrade --install grafana grafana/grafana -f ../docs/observability/grafana_values.yaml
	echo "kubectl port-forward svc/prometheus-server 9090:80"

.PHONY: metrics-undeploy
metrics-undeploy:
	helm uninstall prometheus
	helm uninstall grafana

	$(KUBECTL) delete configmap grafana-dashboards
	rm -rf ../docs/observability/grafana-dashboards-configmap.yaml

.PHONY: grafana-password
grafana-password:
	kubectl get secret --namespace default grafana -o jsonpath="{.data.admin-password}" | base64 --decode ; echo

.PHONY: grafana-connect
grafana-connect:
	kubectl port-forward svc/grafana 8080:80

##@ Build Dependencies

## Tool Binaries
KUBECTL ?= kubectl
