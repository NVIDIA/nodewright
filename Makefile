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
DOCKER_CMD ?= docker

# Keep this digest in lockstep with renovate-version in the workflow so local
# and CI validation run the same Renovate build that creates update PRs.
RENOVATE_IMAGE := ghcr.io/renovatebot/renovate:44@sha256:e6b93e709ca64495ab9307350b260064276ee02d15c6886387fd2d42c926623b


.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Labels

.PHONY: labels
labels: ## Sync GitHub labels from .github/labels.yml (requires gh CLI with repo write access).
	python3 scripts/sync_labels.py

##@ Build

.PHONY: all
all: build ## Build all components.

.PHONY: build
build: ## Build operator and agent.
	$(MAKE) -C operator build
	$(MAKE) -C agent build

##@ Test

.PHONY: test
test: ## Run tests for operator and agent.
	$(MAKE) -C operator test
	$(MAKE) -C agent test

.PHONY: renovate-config-check
renovate-config-check: ## Validate the Renovate configuration with the pinned runner image.
	$(DOCKER_CMD) run --rm \
		-v "$(CURDIR):/repo:ro" \
		-w /repo \
		$(RENOVATE_IMAGE) \
		renovate-config-validator .github/renovate.json5

##@ Formatting

LICENSE_HOLDER ?= NVIDIA CORPORATION & AFFILIATES
LICENSE_TEMPLATE ?= scripts/license-header.tmpl
ADDLICENSE ?= $(CURDIR)/operator/bin/addlicense
license_files = git ls-files -- '*.go' '*.py' '*.sh' '*.yaml' '*.yml' 'Dockerfile' '*.Dockerfile' \
	| grep -vE '^(operator|agent|chart)/'

.PHONY: fmt
fmt: ## Run formatters for operator and agent.
	$(MAKE) -C operator fmt
	$(MAKE) -C agent fmt

.PHONY: license-fmt
license-fmt: ## Run license header formatting for all code.
	$(MAKE) -C operator license-fmt
	$(license_files) | xargs $(ADDLICENSE) -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)
	$(MAKE) -C agent license-fmt

.PHONY: license-header-check
license-header-check: ## Check license headers for all code.
	$(MAKE) -C operator license-header-check
	$(license_files) | xargs $(ADDLICENSE) -check -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)
	@wrong=$$($(license_files) | while read -r f; do \
		grep -qE '^.{1,2} Code generated .* DO NOT EDIT\.$$' "$$f" && continue; \
		head -20 "$$f" | grep -q 'SPDX-License-Identifier: Apache-2.0' || echo "  $$f"; \
	done); \
	if [ -n "$$wrong" ]; then \
		echo "ERROR: header present but not Apache-2.0:"; echo "$$wrong"; exit 1; \
	fi
	$(MAKE) -C agent license-header-check

##@ Docs

.PHONY: diagrams
diagrams: ## Regenerate the architecture diagram PNGs from docs/architecture/images/src.
	@bash docs/architecture/images/src/generate.sh

##@ Licenses

.PHONY: notices
notices: ## Regenerate operator/, agent/, and root THIRD_PARTY_NOTICES.md files.
	$(MAKE) -C operator go-licenses
	$(MAKE) -C agent/go go-licenses
	@python3 scripts/generate-notices.py all

.PHONY: notices-operator
notices-operator: ## Regenerate only operator/THIRD_PARTY_NOTICES.md.
	$(MAKE) -C operator go-licenses
	@python3 scripts/generate-notices.py operator

.PHONY: notices-agent
notices-agent: ## Regenerate only agent/THIRD_PARTY_NOTICES.md.
	$(MAKE) -C agent/go go-licenses
	@python3 scripts/generate-notices.py agent

.PHONY: notices-rollup
notices-rollup: ## Regenerate only the root THIRD_PARTY_NOTICES.md from component files.
	@python3 scripts/generate-notices.py rollup

.PHONY: notices-check
notices-check: notices ## Fail if the committed THIRD_PARTY_NOTICES.md files are stale.
	@git diff --exit-code -- THIRD_PARTY_NOTICES.md operator/THIRD_PARTY_NOTICES.md agent/THIRD_PARTY_NOTICES.md \
		|| { echo "ERROR: THIRD_PARTY_NOTICES.md is out of date. Run 'make notices' and commit the result."; exit 1; }

.PHONY: notices-test
notices-test: ## Run the unit tests for the notices generator's license completeness gate.
	@python3 scripts/generate-notices_test.py

##@ Changelog

# Changelogs are generated from git history by scripts/gen-changelog.sh, which
# takes release boundaries from `git tag` (a single `git-cliff --include-path`
# call drops most release sections in this monorepo). CHANGELOG.md is machine-
# owned; hand-authored notes live in the sibling RELEASE_NOTES.md.

.PHONY: changelog
changelog: ## Regenerate a CHANGELOG.md from git history. Interactive: prompts for component + action (regenerate or cut a release). Machine-owned; do not hand-edit.
	@bash scripts/gen-changelog.sh

.PHONY: release-tag
release-tag: ## Interactively cut a release tag: prompts for component + bump (+ optional RC), creates the tag, and optionally pushes it (push triggers the CI release).
	@bash scripts/release-tag.sh

##@ Clean

.PHONY: clean
clean: ## Clean build artifacts for operator and agent.
	$(MAKE) -C operator clean
	$(MAKE) -C agent clean
