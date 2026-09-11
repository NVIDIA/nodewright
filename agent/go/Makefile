# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

include deps.mk

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

GO ?= go
BINARY_DIR ?= ./bin
AGENT_BINARY ?= $(BINARY_DIR)/agent
REPORTING ?= $(shell pwd)/reporting
COVERAGE_DATA_DIR ?= $(REPORTING)/coverdata
COVDATA = $(shell $(GO) tool -n covdata >/dev/null 2>&1 && echo "$(GO) tool covdata" || echo "$(GO) run $(shell $(GO) env GOROOT)/src/cmd/covdata")

.PHONY: all
all: build

##@ General
.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

## addlicense takes explicit paths, so enumerate tracked sources from agent/go rather
## than letting it walk outside this component or rewrite untracked local files.
LICENSE_HOLDER ?= NVIDIA CORPORATION & AFFILIATES
LICENSE_TEMPLATE ?= ../../scripts/license-header.tmpl
license_files = git ls-files -- '*.go' '*.py' '*.sh' '*.yaml' '*.yml' 'Dockerfile' '*.Dockerfile' \
	| grep -vE '^vendor/'

.PHONY: license-fmt
license-fmt: addlicense ## Add the Apache-2.0 header to any agent Go source file missing one.
	$(license_files) | xargs $(ADDLICENSE) -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)

## addlicense -check accepts any license marker, so verify the required SPDX
## identifier separately for every non-generated tracked source.
.PHONY: license-header-check
license-header-check: addlicense ## Fail if any agent Go source file is missing an Apache-2.0 header.
	$(license_files) | xargs $(ADDLICENSE) -check -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)
	@wrong=$$($(license_files) | while read -r f; do \
		grep -qE '^.{1,2} Code generated .* DO NOT EDIT\.$$' "$$f" && continue; \
		head -20 "$$f" | grep -q 'SPDX-License-Identifier: Apache-2.0' || echo "  $$f"; \
	done); \
	if [ -n "$$wrong" ]; then \
		echo "ERROR: header present but not Apache-2.0:"; echo "$$wrong"; exit 1; \
	fi

.PHONY: fmt
fmt: license-fmt ## Format go files and update license headers.
	$(GO) fmt ./...

.PHONY: generate-mocks
generate-mocks: mockery ## Generate interface mocks.
	$(MOCKERY) --config .mockery.yaml
	$(MAKE) fmt

.PHONY: vet
vet: ## Run go vet against code.
	$(GO) vet ./...

.PHONY: test
test: unit-tests ## Run unit tests and report coverage.
	$(MAKE) merge-coverage
	@test -f $(REPORTING)/cover.out || { echo "Coverage output was not generated" >&2; exit 1; }
	$(GO) tool cover -func=$(REPORTING)/cover.out

.PHONY: reporting
reporting: $(REPORTING)

$(REPORTING):
	mkdir -p $@

# Command and step tests re-execute covered test binaries. GOCOVERDIR prevents
# runtime warnings from those child processes.
.PHONY: unit-tests
unit-tests: reporting ginkgo ## Run Ginkgo/Gomega unit tests with coverage.
	rm -rf $(COVERAGE_DATA_DIR)
	mkdir -p $(COVERAGE_DATA_DIR)
	GOCOVERDIR=$(abspath $(COVERAGE_DATA_DIR)) $(GINKGO) run --cover --coverprofile=unit.coverprofile --output-dir=$(REPORTING) ./...

.PHONY: merge-coverage
merge-coverage: reporting ## Merge test coverage profiles.
	rm -f $(REPORTING)/cover.out $(REPORTING)/raw.coverprofile $(REPORTING)/temp-cover.out
	@if [ -d "$(COVERAGE_DATA_DIR)" ] && [ -n "$$(ls -A $(COVERAGE_DATA_DIR) 2>/dev/null)" ]; then \
		$(COVDATA) textfmt -i=$(abspath $(COVERAGE_DATA_DIR)) -o=$(REPORTING)/raw.coverprofile; \
	fi
	@if ls $(REPORTING)/*.coverprofile 1> /dev/null 2>&1; then \
		cat $(REPORTING)/*.coverprofile > $(REPORTING)/temp-cover.out; \
		echo "mode: set" > $(REPORTING)/cover.out; \
		tail -n +2 $(REPORTING)/temp-cover.out | sed '/mode: set/d' >> $(REPORTING)/cover.out; \
		rm -f $(REPORTING)/temp-cover.out; \
	else \
		echo "No coverage files found - skipping merge"; \
	fi

.PHONY: license-check
license-check: go-licenses ## Run go-licenses check against code.
	$(GO_LICENSES) check --allowed_licenses=MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,Zlib ./...

.PHONY: lint
lint: golangci-lint license-header-check ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply fixes.
	$(GOLANGCI_LINT) run --fix ./...

##@ Build
.PHONY: build
build: ## Build agent binary.
	mkdir -p $(BINARY_DIR)
	$(GO) build -o $(AGENT_BINARY) ./cmd/agent

##@ Clean
.PHONY: clean
clean: ## Remove local build artifacts.
	rm -rf $(BINARY_DIR)
	rm -rf $(LOCALBIN)
	rm -rf $(REPORTING)
