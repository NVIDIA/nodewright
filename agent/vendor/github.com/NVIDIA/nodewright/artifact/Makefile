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
REPORTING ?= $(shell pwd)/reporting

##@ General
.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

## addlicense takes explicit paths, so enumerate tracked sources from artifact/ rather
## than letting it walk outside this component or rewrite untracked local files.
LICENSE_HOLDER ?= NVIDIA CORPORATION & AFFILIATES
LICENSE_TEMPLATE ?= ../scripts/license-header.tmpl
license_files = git ls-files -- '*.go' '*.py' '*.sh' '*.yaml' '*.yml' 'Dockerfile' '*.Dockerfile'

.PHONY: license-fmt
license-fmt: addlicense ## Add the Apache-2.0 header to any artifact source file missing one.
	$(license_files) | xargs $(ADDLICENSE) -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)

## addlicense -check accepts any license marker, so verify the required SPDX
## identifier separately for every non-generated tracked source.
.PHONY: license-header-check
license-header-check: addlicense ## Fail if any artifact source file is missing an Apache-2.0 header.
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

.PHONY: reporting
reporting: $(REPORTING)

$(REPORTING):
	mkdir -p $@

.PHONY: test
test: reporting ginkgo ## Run Ginkgo/Gomega unit tests and report coverage.
	$(GINKGO) run --cover --coverprofile=cover.out --output-dir=$(REPORTING) ./...
	$(GO) tool cover -func=$(REPORTING)/cover.out

.PHONY: lint
lint: golangci-lint license-header-check ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply fixes.
	$(GOLANGCI_LINT) run --fix ./...

##@ Clean
.PHONY: clean
clean: ## Remove local build artifacts.
	rm -rf $(LOCALBIN)
	rm -rf $(REPORTING)
