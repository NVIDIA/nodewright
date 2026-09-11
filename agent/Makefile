# Copyright (c) NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#	 http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

VENV := ./venv/bin/
REGISTRY ?= ghcr.io
AGENT_IMAGE ?= nvidia/nodewright/agent
DOCKER_CMD ?= docker
PYTHON_VERSION ?= 3.13
DEBIAN_VERSION ?= trixie
DISTROLESS_VERSION ?= 4.0.1

.PHONY: all
all: venv test

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\n\033[1;31mUsage:\033[0m\n  make \033[3;1;36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1;31m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: venv
venv: ## Sets up a python venv at `./venv`
	python3 -m venv venv
	$(VENV)pip install hatch coverage
	$(VENV)hatch config set dirs.project "[\"$(shell pwd)\"]"

##@ Test
.PHONY: test
test: venv ## Test using hatch, prints coverage and outputs a report to coverage.xml
	$(VENV)hatch -p skyhook-agent test --cover-quiet --junit-xml=test-results.xml
	$(VENV)coverage report --show-missing --data-file=skyhook-agent/.coverage
	$(VENV)coverage xml --data-file=skyhook-agent/.coverage

## The agent target owns both the legacy Python sources and the Go rewrite.
## Enumerating tracked files avoids rewriting vendored dependencies or local virtualenvs.
LICENSE_HOLDER ?= NVIDIA CORPORATION & AFFILIATES
LICENSE_TEMPLATE ?= ../scripts/license-header.tmpl
ADDLICENSE ?= $(CURDIR)/go/bin/addlicense
license_files = git ls-files -- '*.go' '*.py' '*.sh' '*.yaml' '*.yml' 'Dockerfile' '*.Dockerfile' \
	| grep -vE '^(vendor|go/vendor)/'

.PHONY: addlicense
addlicense:
	$(MAKE) -C go addlicense

.PHONY: license-fmt
license-fmt: addlicense ## Add the Apache-2.0 header to tracked agent source files.
	$(license_files) | xargs $(ADDLICENSE) -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)

.PHONY: license-header-check
license-header-check: addlicense ## Fail if a tracked agent source file is missing an Apache-2.0 header.
	$(license_files) | xargs $(ADDLICENSE) -check -c "$(LICENSE_HOLDER)" -f $(LICENSE_TEMPLATE)
	@wrong=$$($(license_files) | while read -r f; do \
		grep -qE '^.{1,2} Code generated .* DO NOT EDIT\.$$' "$$f" && continue; \
		head -20 "$$f" | grep -q 'SPDX-License-Identifier: Apache-2.0' || echo "  $$f"; \
	done); \
	if [ -n "$$wrong" ]; then \
		echo "ERROR: header present but not Apache-2.0:"; echo "$$wrong"; exit 1; \
	fi

.PHONY: fmt
fmt: license-fmt ## Format agent source files.
	@echo "formattted"

##@ Build
.PHONY: build
build: ## Builds using hatch to `dist`
	$(VENV)hatch -p skyhook-agent version $(build_version)
	$(VENV)hatch -p skyhook-agent build -c

docker-setup:
	test ! $($(DOCKER_CMD) context ls | grep builder) || $(DOCKER_CMD) context create builder;
	$(DOCKER_CMD) buildx create --platform linux/amd64,linux/arm64 --use builder
	$(DOCKER_CMD) run --privileged --rm tonistiigi/binfmt --install amd64,arm64

GIT_SHA=$(shell git rev-parse --short HEAD)
ACTUAL_TAGS=$(shell echo "-t $(REGISTRY)/$(AGENT_IMAGE):$(shell date +%y.%m.%d-%H%M%S)-$(GIT_SHA) $(TAGS)" | tr A-Z a-z)
.PHONY: docker-build-only
docker-build-only:
	@echo "Building skyhook-agent $(DOCKER_CMD) image with tags: $(ACTUAL_TAGS)"
	$(DOCKER_CMD) buildx build $(BUILD_ARGS) --build-arg AGENT_VERSION=$(AGENT_VERSION) \
		--build-arg GIT_SHA=$(GIT_SHA) \
		--build-arg DISTROLESS_VERSION=$(DISTROLESS_VERSION) \
		--build-arg PYTHON_VERSION=$(PYTHON_VERSION) \
		--build-arg DEBIAN_VERSION=$(DEBIAN_VERSION) \
		--platform linux/amd64,linux/arm64 $(ACTUAL_TAGS) --metadata-file=metadata.json -f ../containers/agent.Dockerfile .

##@ Vendor
.PHONY: vendor
vendor: ## Uses Unearth to vendor all dependencies locally.
	python3 -m venv ./venv_vendor
	./venv_vendor/bin/pip install unearth toml
	dependencies=$(shell python -c 'import toml; print(" ".join(toml.loads(open("skyhook-agent/pyproject.toml","r").read())["project"]["dependencies"]))')
	rm -rf vendor
	mkdir -p vendor
	for dep in $(dependencies); do \
		./venv_vendor/bin/unearth --no-binary -d ./vendor $(dep) >> vendor/lock_file; \
	done

##@ Clean
.PHONY: clean
clean: ## Removes all pycache and venv directories from your local copy.
	rm -rf __pycache__
	rm -rf venv
	rm -rf venv_vendor
