# Changelog

All notable changes to this project will be documented in this file.

## Unreleased

### New Features

- Add a standard `Ready` condition to Skyhook status for native Kubernetes wait and GitOps health tooling.

### Deprecations

- Deprecated prefixed Skyhook status condition types such as `skyhook.nvidia.com/Ready`, `skyhook.nvidia.com/Transition`, and `skyhook.nvidia.com/TaintNotTolerable`; bare condition types such as `Ready` and `TaintNotTolerable` are now emitted alongside the legacy names for one release.

## [operator/v0.15.0] - 2026-04-06

### Bug Fixes

- Batch stickiness — nodes in NodePriority finish all packages before new nodes are picked
- Change skyhook/operator to nodewright/operator for coverage

### New Features

- Add SKYHOOK_NODE_ORDER env var for monotonic node ordering

### Other Tasks

- Update project to follow the OSS template

## [operator/v0.14.0] - 2026-03-10

### Bug Fixes

- Resolve webhook caBundle deadlock during helm upgrade
- Webhook controller dropped CREATE/UPDATE operations for DeploymentPolicy validating rules 
- Working reducing flapping tests, large tests refactor

### New Features

- AutoTaintNewNodes
- Add sequencing: node or all

### Other Tasks

- *(chart)* Update versions
- Update go, linter, fix linter errors
- Update k8s version, fix chainsaw install

## [operator/v0.12.0] - 2026-02-06

### Bug Fixes

- Release ci process
- Make imagePullSecret optional to prevent kubelet errors

### New Features

- Add cli doc for backwards compatibly and warnings
- Add new printer columns
- *(operator)* Implement per-node priority ordering
- *(agent/operator)* Add integration chainsaw tests for agent for reaping logs and not writing logs
- *(ci)* Auto-update distroless base images and fix operator version
- *(chart)* Add automatic Skyhook resource cleanup on helm uninstall
- *(deployment-policy)* Add batch state reset with auto-reset, CLI, and config

### Other Tasks

- Update build distro and go version

## [operator/v0.11.1] - 2026-01-12

### Bug Fixes

- *(chart)* Add missing rbac for deploymentpolicies
- Cleanup cli code 
- Update gocover
- Gitlint version to support 1.25 go
- Un namespace policies
- Bad webhook rules
- Unknown to waiting status
- Bug in uncordon logic

### New Features

- Add support for ignoring nodes via label 
- *(cli)* Add package and node management commands with lifecycle controls 
- Add webhook support for validation policies exist
- *(ci)* Make ci coverage include new deployment policies suite

### Other Tasks

- *(deps)* Bump k8s.io/kubernetes from 1.34.1 to 1.34.2 in /operator
- *(cli)* Restructure CLI to cmd/cli/app pattern and consolidate lifecycle commands 
- Consolidate BuildState and compartment batch selection logic 
- Add GoReleaser workflow for CLI releases 
- Update golang to latest and k8s to latest

## [operator/v0.10.0] - 2025-12-01

### Bug Fixes

- Deadlock if reboot pods are missing, adds them back
- Migration bug, and units from new defaults
- Miscellaneous fixes to project structure
- Helm tests, seem like they need more time in this env
- Race bug running more then one pod at a time
- Helm e2e tests were broken
- Depends on not waiting for completed tasks to continue
- Depends on not walking the graph correctly in partial stages
- Volume names getting longer than DNS_LABEL
- Update tests to not set limits everywhere anymore
- How we compare interrupt pods
- Reviews
- *(operator)* Change minimum to be 1 due to 0 being considered an 'unset' value for golang
- *(operator)* Lint issue
- *(operator)* Pod reconciler wasn't updating restarts in node state
- *(operator)* License adding
- *(operator)* Make metrics binding disabled by default
- *(operator/Makefile)* Fix license-check?
- *(operator/ci)* Invalidate cache and use 1.23.9?
- *(ci)* Kind k8s version matrix was incorrect
- *(operator)* Clean up nodes that no longer exist from status
- *(chart)* Resolve kubernetes security scan violations for compliance 
- Handle edge cases in compartment-based deployment rollouts 

### New Features

- Change to common license formatter and update all code with that format
- Add gracefully shutdown support
- Remove cert manager
- Change how limits are manged to a use a limitrange via helm
- *(operator)* Add strict ordering of skyhooks along with documentation
- *(operator)* Initial metrics
- *(operator)* Add testing for metrics in k8s-tests
- *(chart)* Enable scraping of metrics by prometheus
- *(operator)* Add a metric for taint scheduling
- *(operator)* Update k8s sdk version
- Fix agent for distroless and have scr name in flag/history/log 
- *(operator)* Added disabled, paused, waiting, and blocked statuses for skyhooks and nodes 
- *(operator)* Added comprehensive status and state metrics 
- *(operator)* Added turn key grafana dashboards with new metrics 
- *(operator)* Changed interrupt order 
- Add package configuration to node config map 
- Add glob support for config interrupts 
- *(crd)* Add deployment policy 
- Add DeploymentPolicy validation and defaults with tests 
- Add compartment-based node assignment 
- Resolve overlaps in compartments 
- Implement deployment strategies with compartment-based batching 
- Add backwards compatability for rollouts 
- Compartment status 
- *(operator)* Update k8s version to 1.34.0 
- Add metrics for compartments 
- Add container sha as optional field to package 
- Add e2e tests for deployment policy 
- Make failureThreshold nullable and skip defaulting 
- *(plugin)* Setup basic structure 

### Other Tasks

- Version update for security
- *(deps)* Bump golang.org/x/net from 0.33.0 to 0.36.0 in /operator
- Clean up extra newlines from license formatting
- *(deps)* Bump golang.org/x/net from 0.36.0 to 0.38.0
- Update license header format
- Fix up headers after merge
- *(operator)* Update go and container versions
- *(operator)* Update go import paths to fix importing another project
- Bump helm version and go version
- *(deps)* Bump k8s.io/kubernetes from 1.33.2 to 1.33.4 in /operator

## [operator/v0.0.0] - 2025-02-14

### Bug Fixes

- Random little things in logs when running tests
- Add miss license and fix some license tooling
- Remove interrupt timeout which was flawed by design

### New Features

- *(agent/ci)* Add unittest and coverage report job
- *(agentless)* Add agentless build to agent build workflow 
- *(ci/github/operator)* Add ci to build operator container to github 
- *(operator/ci)* Add unit and end to end test workflows 

### Other Tasks

- Begin reorg
- Update module name to point at github
- Merge pull request #5 from NVIDIA/update-module-name

chore: update module name to point at github
- *(helm)* Added docs for the helm chart

<!-- Generated by git-cliff -->
