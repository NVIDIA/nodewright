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

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

GO ?= go

# Every Go module under tools/, relative to this Makefile.
MODULES ?= . tests

## This module has no third-party dependencies and no go.sum, which is what lets
## the release job run `go run ./cmd/openvex bind` and `go run ./cmd/openvex
## validate` with a toolchain and no module download. Keep it that way: adding
## one turns every release into a fetch.
##
## tests/ is a module of its own so the release-policy tests can read workflow
## YAML with a real parser without that dependency reaching this module. A
## nested module is invisible to `./...`, so every Go target below has to name
## it; `make test` from here still runs everything, which is what CI calls.
##
## There are deliberately no license targets here. The ROOT Makefile's
## license_files glob excludes only operator|agent|chart, so tools/ is already
## covered repo-wide. A target here would double-cover it.

.PHONY: all
all: test

##@ General
.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Format go files in every module.
	@for module in $(MODULES); do \
		(cd $$module && $(GO) fmt ./...); \
	done

.PHONY: fmt-check
fmt-check: ## Fail if any go file is not gofmt-clean.
	@# gofmt walks the directory tree rather than the module graph, so this one
	@# invocation already covers tests/. vet and the unit tests do not, and name
	@# it explicitly below.
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "::error::gofmt reports unformatted files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet against every module.
	@for module in $(MODULES); do \
		(cd $$module && $(GO) vet ./...); \
	done

.PHONY: unit-tests
unit-tests: ## Run the unit tests in every module.
	@for module in $(MODULES); do \
		(cd $$module && $(GO) test ./... -count=1); \
	done

.PHONY: test
test: fmt-check vet unit-tests ## Run formatting, vet and unit tests.
