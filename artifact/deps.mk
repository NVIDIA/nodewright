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

## this makefile is for installing deps and controlling the versioning
## its included in the main makefile, but its a lot to look at these
## plus ci can watch this file to know to build a new build image

# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.13.1
## The ginkgo CLI and library are cut from the same tag and must never drift
## apart, so the CLI takes its version from go.mod rather than a pin here:
## Renovate's custom manager does not read this file, but its gomod manager
## does bump go.mod.
GINKGO_VERSION ?= $(shell awk '$$1 == "github.com/onsi/ginkgo/v2" { print $$2 }' $(CURDIR)/go.mod)
# renovate: datasource=go depName=github.com/google/addlicense
ADDLICENSE_VERSION ?= v1.2.0

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO = $(LOCALBIN)/ginkgo
ADDLICENSE = $(LOCALBIN)/addlicense

.PHONY: install-deps
install-deps: golangci-lint ginkgo addlicense ## Install all dependencies.

.PHONY: golangci-lint
golangci-lint: $(LOCALBIN) ## Download golangci-lint locally if necessary.
	@test -x $(GOLANGCI_LINT) \
		&& [ "$$($(GOLANGCI_LINT) version | awk '{sub(/^v/, "", $$4); print $$4}')" = "$(patsubst v%,%,$(GOLANGCI_LINT_VERSION))" ] \
		|| { \
	set -e ;\
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh | sh -s -- -b $(shell dirname $(GOLANGCI_LINT)) $(GOLANGCI_LINT_VERSION) ;\
	}

.PHONY: ginkgo
ginkgo: $(LOCALBIN) ## Download ginkgo locally if necessary.
	@test -x $(GINKGO) \
		&& [ "$$($(GINKGO) version)" = "Ginkgo Version $(patsubst v%,%,$(GINKGO_VERSION))" ] \
		|| GOBIN=$(LOCALBIN) go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

.PHONY: addlicense
addlicense: $(LOCALBIN) ## Download addlicense locally if necessary.
	@test -x $(ADDLICENSE) \
		&& go version -m $(ADDLICENSE) | awk '$$1 == "mod" && $$2 == "github.com/google/addlicense" && $$3 == "$(ADDLICENSE_VERSION)" { found = 1 } END { exit !found }' \
		|| GOBIN=$(LOCALBIN) go install github.com/google/addlicense@$(ADDLICENSE_VERSION)
