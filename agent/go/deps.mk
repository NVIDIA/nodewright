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

GOLANGCI_LINT_VERSION ?= v2.13.1
GINKGO_VERSION ?= v2.32.1
MOCKERY_VERSION ?= v3.7.0
# Mockery interprets MOCKERY_VERSION as its boolean version configuration.
unexport MOCKERY_VERSION
ADDLICENSE_VERSION ?= v1.2.0
GO_LICENSES_VERSION ?= v1.6.0

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO = $(LOCALBIN)/ginkgo
MOCKERY = $(LOCALBIN)/mockery
ADDLICENSE = $(LOCALBIN)/addlicense
GO_LICENSES = $(LOCALBIN)/go-licenses

.PHONY: install-deps
install-deps: golangci-lint ginkgo mockery addlicense go-licenses ## Install all dependencies.

.PHONY: go-licenses
go-licenses: $(LOCALBIN) ## Download go-licenses locally if necessary.
	@test -x $(GO_LICENSES) \
		&& go version -m $(GO_LICENSES) | awk '$$1 == "mod" && $$2 == "github.com/google/go-licenses" && $$3 == "$(GO_LICENSES_VERSION)" { found = 1 } END { exit !found }' \
		|| GOBIN=$(LOCALBIN) go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)

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

.PHONY: mockery
mockery: $(LOCALBIN) ## Download Mockery locally if necessary.
	@test -x $(MOCKERY) \
		&& [ "$$($(MOCKERY) version)" = "$(MOCKERY_VERSION)" ] \
		|| GOBIN=$(LOCALBIN) go install github.com/vektra/mockery/v3@$(MOCKERY_VERSION)

.PHONY: addlicense
addlicense: $(LOCALBIN) ## Download addlicense locally if necessary.
	@test -x $(ADDLICENSE) \
		&& go version -m $(ADDLICENSE) | awk '$$1 == "mod" && $$2 == "github.com/google/addlicense" && $$3 == "$(ADDLICENSE_VERSION)" { found = 1 } END { exit !found }' \
		|| GOBIN=$(LOCALBIN) go install github.com/google/addlicense@$(ADDLICENSE_VERSION)
