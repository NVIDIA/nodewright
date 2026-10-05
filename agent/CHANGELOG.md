# Changelog

<!-- DO NOT EDIT. Generated from git commit history by scripts/gen-changelog.sh.
     Hand-authored behavior/upgrade notes live in RELEASE_NOTES.md (same directory). -->

All notable changes to this project will be documented in this file.

## [agent/v7.0.0] - 2026-10-05

### Bug Fixes

- *(changelog)* Promote RELEASE_NOTES.md's Unreleased heading on a release cut
- *(licenses)* Disclose all vendored modules and make notices deterministic
- *(agent/go)* Add dependency-license coverage (merge gate, notices, vendoring comment)
- *(licenses)* Resolve notices tags across all refs, not just reachable ones
- *(agent)* Let the running step finish on SIGTERM so gracefulShutdown holds
- *(agent)* Drop SKYHOOK_AGENT_BUFFER_LIMIT, which the Go agent never used

### New Features

- *(changelog)* Tag-range generator, split CHANGELOG/RELEASE_NOTES, release-tag helper
- *(agent)* Port enums and steps for agent go rewrite
- *(agent)* Embedded json schema and step validation for agent go rewrite
- *(agent)* Add filesystem flag and log helpers
- *(agent)* Add package history store
- *(agent)* Add commands and command runners with chroot for go rewrite
- *(agent)* Added Run() to the Step interface for agent go rewrite
- *(agent)* Added Run() to Interrupt interface for agent go rewrite
- *(agent)* Complete Go agent orchestration and entrypoint
- *(agent)* Add pre-cutover Go container validation
- *(ci)* Attest SBOM and VEX to each platform manifest
- *(agent)* Cut over to the Go agent and remove the Python implementation

### Other Tasks

- Update go to latest
- *(deps)* Bump golang.org/x/net from 0.49.0 to 0.55.0 in /agent/go
- Bump Go to 1.26.5 and centralize the Go version in go.mod
- *(deps)* Bump github.com/onsi/ginkgo/v2 in /agent/go
- *(deps)* Bump github.com/onsi/gomega in /agent/go
- *(deps)* Bump github.com/santhosh-tekuri/jsonschema/v6 in /agent/go
- Standardize license header tooling
- *(agent)* Centralize rooted host filesystem operations
- *(agent)* Unify regular and upgrade step execution
- *(agent)* Add generated interface mocks
- *(deps)* Bump github.com/onsi/ginkgo/v2 in /agent/go
- *(deps)* Bump github.com/stretchr/testify in /agent/go
- Update golangci version to latest in ci
- *(deps)* Update go module directive to v1.27.0
- *(deps)* Update k8s.io/utils digest to cf1189d
- *(deps)* Update kubernetes
- *(deps)* Update module github.com/onsi/gomega to v1.43.0
- *(deps)* Update go module directive to v1.27.1
- *(deps)* Update module github.com/onsi/ginkgo/v2 to v2.32.2
- *(deps)* Update module github.com/onsi/gomega to v1.43.1
- *(deps)* Update module github.com/onsi/ginkgo/v2 to v2.33.0
- *(deps)* Update module github.com/onsi/gomega to v1.44.0
- *(agent)* Assert what gracefulShutdown guarantees in sigterm_grace


## [agent/v6.4.2] - 2026-05-19

### Bug Fixes

- *(agent)* Bootstrap copy dir before interrupts

### New Features

- *(agent)* Bootstrap agent go rewrite
- *(agent)* Port interrupts for agent go rewrite
- Add make notices for third-party license aggregation

### Other Tasks

- Update project to follow the OSS template
- *(agent)* Use pytest style for interrupt bootstrap coverage
- Parallelize e2e tests by pool and add merge gates

## [agent/v6.4.1] - 2026-03-04

### Bug Fixes

- *(agent)* Set the cwd to the copy dir so we don't write to temp

## [agent/v6.4.0] - 2026-02-06

### Bug Fixes

- *(agent)* Hotfix for interrupts in agent v6.3.0

### New Features

- *(agent)* Add env var SKYHOOK_AGENT_WRITE_LOGS to be able to toggle log files on disk
- *(ci)* Auto-update distroless base images and fix operator version

### Other Tasks

- Update python version for agent
- Update build distro and go version

## [agent/v6.3.1] - 2025-08-25

### Bug Fixes

- *(agent)* Hotfix for interrupts in agent v6.3.0

## [agent/v6.3.0] - 2025-07-30

### Bug Fixes

- *(agent)* Chdir to / and make env var setting more robust for host machine

### New Features

- Fix agent for distroless and have scr name in flag/history/log

### Other Tasks

- Update license header format
- Fix up headers after merge

## [agent/v6.2.0] - 2025-06-05

### Bug Fixes

- *(agent)* Container has to run as root so it can do the chroot

### New Features

- *(agent)* Change to distroless and use python for chroot and chmod
- Add gracefully shutdown support
- Remove cert manager

### Other Tasks

- Clean up extra newlines from license formatting

## [agent/v6.1.5] - 2025-03-26

### New Features

- Print configuration at the start of agent container
- Change to common license formatter and update all code with that format

### Other Tasks

- *(agent)* Add notice file and remove extra license file

## [agent/v6.1.4] - 2025-02-28

## [agent/v0.0.1] - 2025-02-14

### Bug Fixes

- *(agent)* Change COPY_RESOLVE to COPY_RESOLV

### New Features

- *(agent)* Add container build ci on commit with attestation
- *(agent/ci)* Add unittest and coverage report job
- *(agent/ci)* Add unittest reporting to agent MRs
- *(ci/githubi/agent)* Organize each container build into its own workflow for clarity and appropriate filters on file changes

### Other Tasks

- Move e2e to k8s-tests, restore docker build command in agent

<!-- Generated by git-cliff -->
