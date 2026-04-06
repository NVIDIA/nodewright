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

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build ## Build all components.

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Labels

.PHONY: labels
labels: ## Sync GitHub labels from .github/labels.yml (requires gh CLI with repo write access).
	python3 scripts/sync_labels.py

##@ Build

.PHONY: build
build: ## Build operator and agent.
	$(MAKE) -C operator build
	$(MAKE) -C agent build

##@ Test

.PHONY: test
test: ## Run tests for operator and agent.
	$(MAKE) -C operator test
	$(MAKE) -C agent test

##@ Formatting

.PHONY: fmt
fmt: ## Run formatters for operator and agent.
	$(MAKE) -C operator fmt
	$(MAKE) -C agent fmt

.PHONY: license-fmt
license-fmt: ## Run license header formatting for all code.
	python3 scripts/format_license.py --root-dir . --license-file LICENSE

##@ Changelog

# CLI code lives under operator/cmd/cli/, so we map the include path accordingly.
define changelog_include_path
$(if $(filter cli,$(1)),operator/cmd/cli,$(1))
endef

define changelog_output
$(if $(filter cli,$(1)),operator/cmd/cli/CHANGELOG.md,$(1)/CHANGELOG.md)
endef

.PHONY: changelog
changelog: ## Generate/update CHANGELOG.md for a component. Usage: make changelog COMPONENT=operator
	@if [ -z "$(COMPONENT)" ]; then \
		echo "ERROR: COMPONENT is required. Usage: make changelog COMPONENT=operator|chart|cli"; \
		exit 1; \
	fi
	git-cliff \
		--include-path "$(call changelog_include_path,$(COMPONENT))/**" \
		--tag-pattern "$(COMPONENT)/.*" \
		-o $(call changelog_output,$(COMPONENT))
	@echo "Updated $(call changelog_output,$(COMPONENT))"

.PHONY: changelog-preview
changelog-preview: ## Preview unreleased changes for a component. Usage: make changelog-preview COMPONENT=operator
	@if [ -z "$(COMPONENT)" ]; then \
		echo "ERROR: COMPONENT is required. Usage: make changelog-preview COMPONENT=operator|chart|cli"; \
		exit 1; \
	fi
	git-cliff \
		--include-path "$(call changelog_include_path,$(COMPONENT))/**" \
		--tag-pattern "$(COMPONENT)/.*" \
		--unreleased \
		--strip header

COMPONENTS := operator agent chart cli

.PHONY: changelog-all
changelog-all: $(COMPONENTS:%=changelog-%) ## Generate CHANGELOGs for all components.

.PHONY: $(COMPONENTS:%=changelog-%)
$(COMPONENTS:%=changelog-%):
	$(MAKE) changelog COMPONENT=$(@:changelog-%=%)

##@ Clean

.PHONY: clean
clean: ## Clean build artifacts for operator and agent.
	$(MAKE) -C operator clean
	$(MAKE) -C agent clean
