# Changelog

<!-- DO NOT EDIT. Generated from git commit history by scripts/gen-changelog.sh.
     Hand-authored behavior/upgrade notes live in RELEASE_NOTES.md (same directory). -->

All notable changes to this project will be documented in this file.

## [operator/v0.19.0] - 2026-08-31

### Bug Fixes

- *(operator)* Persist node cordon before draining
- *(operator)* Wait for evicted pods to terminate before interrupt
- *(changelog)* Promote RELEASE_NOTES.md's Unreleased heading on a release cut
- *(ci)* Derive CLI release install instructions from built artifacts
- *(licenses)* Disclose all vendored modules and make notices deterministic

### New Features

- Add runtimeRequiredCordonAfter to support persistent node cordons

### Other Tasks

- *(operator)* Cover pure-logic gaps in the nodewright API, wrapper, version and CLI utils
- *(deps)* Bump github.com/onsi/ginkgo/v2 in /operator
- *(deps)* Bump github.com/stretchr/testify in /operator
- *(deps)* Bump golang.org/x/mod
- *(operator)* Exclude generated deepcopy from reported coverage
- *(operator)* Run the operator-agent suite when the operator changes
- Retry transient registry and download failures
- *(makefile)* Record TIMEOUT decision for metrics review
- Update golangci version to latest
- *(deps)* Update go module directive to v1.27.0
- *(deps)* Update k8s.io/utils digest to cf1189d
- *(deps)* Update kubernetes


## [operator/v0.18.0] - 2026-08-17

### Bug Fixes

- *(operator)* Address drain config review feedback
- *(operator)* Address drain config review feedback
- *(operator)* Always emit drain timeout event
- *(api)* Preserve registry ports in package images
- *(api)* Reject inline tags and digests in package image
- *(controller)* Preserve shared Skyhook cordons
- *(operator)* Exempt package pods from drain by label as defense-in-depth
- *(operator)* Scope drain label exemption to the operator namespace
- *(operator)* Grant events.k8s.io RBAC so recorded events aren't rejected
- *(cli)* Correct stale examples in `version --help`
- *(operator)* Stop stranding a dependency-free package in the DAG walk
- *(operator)* Keep an unprocessed Failed Job on the AlreadyExists path too
- *(operator)* Say nodewright, not skyhook, in the Job failure event
- *(operator)* Judge every failed attempt with one predicate
- *(operator)* Drop the Job-level deadline ceiling on package Jobs
- *(operator)* Never resurrect a package entry on interrupt completion
- *(operator)* Merge node-state deltas instead of restamping a stale snapshot
- *(operator)* Never resume a disabled NodeWright's suspended Jobs
- *(operator)* Repair three Skyhook to NodeWright migration defects
- *(metrics)* Replace kube-rbac-proxy with controller-runtime auth
- *(operator)* Compare package resources semantically, not byte-wise
- *(operator)* Rename skyhook -> nodewright in event and condition text
- *(chart)* Template hardcoded skyhook-operator resource names
- *(operator)* Release the webhook bootstrap lease when it cannot be used
- *(chart)* Template the namespaced RBAC names
- *(operator)* Use the system namespace placeholder in the namespaced RBAC markers
- *(operator)* Rename skyhook -> nodewright in operator error and log text
- *(operator)* Address review findings in the Jobs execution path

### New Features

- *(operator)* Add configurable drain behavior
- Rename the Skyhook API to NodeWright with a rollback-safe migration bridge
- *(operator)* Job builders and stage-timeout/TTL options
- *(operator)* Dal Job accessors and Job event mapper (#302 part 1)
- *(operator)* JobMatchesPackage staleness check (#302 part 2a)
- *(operator)* JobReconcile completion recording and dal.GetPodLogTail (#302 part 2b)
- *(operator)* Swap package/interrupt execution to Jobs (#303 part 1)
- *(operator)* Cascade pause to Job suspension (#303 part 2)
- *(chart)* Expose Job TTL/stage-timeout options via the chart
- *(operator)* Treat a stage timeout as a retryable failure
- *(operator)* Let a spec change clear a timed-out stage
- *(cli)* Default install namespace to nodewright, discover it at runtime
- *(operator)* Default runtime-required taint key to nodewright.nvidia.com
- *(operator)* Publish nodewright_* metrics alongside deprecated skyhook_*

### Other Tasks

- Update change log for release
- *(operator)* Bump k8s dependencies to v0.36.2/v1.36.2
- *(operator)* Centralize Kubernetes test versions
- Bump Go to 1.26.5 and centralize the Go version in go.mod
- *(deps)* Bump github.com/sethvargo/go-envconfig in /operator
- *(deps)* Bump github.com/spf13/pflag in /operator
- *(deps)* Bump sigs.k8s.io/controller-runtime
- *(deps)* Bump github.com/onsi/ginkgo/v2 in /operator
- *(deps)* Bump go.uber.org/zap from 1.27.1 to 1.28.0 in /operator
- *(deps)* Bump github.com/onsi/gomega in /operator
- *(deps)* Bump the golang-x group in /operator with 2 updates
- *(tests)* Rename k8s-tests skyhook files and dirs to nodewright
- *(deps)* Bump the kubernetes group in /operator with 5 updates
- *(deps)* Bump github.com/prometheus/client_golang in /operator
- *(deps)* Bump github.com/go-logr/logr in /operator
- *(deps)* Bump github.com/sethvargo/go-envconfig in /operator
- *(operator)* Replace format_license.py with google/addlicense
- *(deps)* Bump github.com/prometheus/client_golang in /operator
- *(controller)* Fix flaky config sync gate test via cache-sync wait
- *(operator)* Package annotation helpers accept any client.Object
- *(operator)* Describe backoffLimit as retries and fix the deadline-expiry text
- *(operator)* Separate the retry budget from the parking decision
- *(operator)* Document the two-writer invariant behind the park
- *(operator)* StageTimeout applies to the next Job, not one already running
- *(operator)* Restore license headers stripped by the generators
- *(operator)* Say what stageTimeout 0 does not bound
- *(cli)* Stop overselling disable in the docs and the command help
- *(design)* Record that the migration hold replaced the raw-pod accommodations
- *(cli)* Synchronize the pause and enable help, and stop calling the overlap safe
- *(cli)* Fix the garbled sentence in the enable help
- Standardize license header tooling
- *(deps)* Bump go.opentelemetry.io/otel/sdk in /operator
- *(deps)* Bump github.com/google/cel-go in /operator
- *(deps)* Bump google.golang.org/grpc in /operator
- *(operator)* Correct where a timed-out stage's logs actually live
- *(operator)* Close webhook-cache race in DeploymentPolicy webhook specs
- *(operator)* Wait on the webhook cache when deleting a DeploymentPolicy
- Repoint tooling and prose at the moved docs/ paths
- Update changelog for release

## [operator/v0.17.0] - 2026-06-12

### Bug Fixes

- Deadlock in webhook controller when upgrading from old versions
- Close StageInterrupt trap + harden core e2e pool
- *(operator)* Requeue when an owned ConfigMap sync is deferred
- *(operator)* Persist reapply-on-reboot reset before advancing boot id
- *(operator)* Mount configMap keys as subPaths to preserve image defaults
- *(operator)* Promote skipped interrupt packages from the reconcile loop

### New Features

- *(cli)* Update-state + targeted reset --package
- *(changelog)* Tag-range generator, split CHANGELOG/RELEASE_NOTES, release-tag helper
- *(controller)* Re-apply runtime-required taint on reboot when autoTaintNewNodes=true

### Other Tasks

- *(chart)* Bump to v0.16.1 with operator webhook cert deadlock fix
- *(controller)* Collapse Skyhook and Node reconciles onto a glo…
- Update go to latest
- Update change log for release

## [operator/v0.16.1] - 2026-05-22

### Other Tasks

- Cherry pick again

## [operator/v0.16.0] - 2026-05-19

### New Features

- *(operator)* Expose standard Skyhook Ready condition
- Add explicit uninstall to support uninstalls that require the config information
- Add make notices for third-party license aggregation

### Other Tasks

- *(chart)* Version bump
- Update go version to 1.26.2
- Run helm tests with ctlptl registry
- Update go and libs to latest
- Parallelize e2e tests by pool and add merge gates

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

- Webhook controller dropped CREATE/UPDATE operations for DeploymentPolicy validating rules
- Working reducing flapping tests, large tests refactor

### New Features

- Add sequencing: node or all

## [operator/v0.13.0] - 2026-03-03

### Bug Fixes

- Resolve webhook caBundle deadlock during helm upgrade

### New Features

- AutoTaintNewNodes

### Other Tasks

- Update go, linter, fix linter errors
- Update k8s version, fix chainsaw install

## [operator/v0.12.1] - 2026-02-10

### Bug Fixes

- Resolve webhook caBundle deadlock during helm upgrade

### Other Tasks

- *(chart)* Update versions

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

- Unknown to waiting status
- Bug in uncordon logic

## [operator/v0.11.0] - 2025-12-24

### Bug Fixes

- *(chart)* Add missing rbac for deploymentpolicies
- Cleanup cli code
- Update gocover
- Gitlint version to support 1.25 go
- Un namespace policies
- Bad webhook rules

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

- *(chart)* Resolve kubernetes security scan violations for compliance
- Handle edge cases in compartment-based deployment rollouts

### New Features

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

- Bump helm version and go version
- *(deps)* Bump k8s.io/kubernetes from 1.33.2 to 1.33.4 in /operator

## [operator/v0.9.0] - 2025-08-08

### Bug Fixes

- *(operator)* Lint issue
- *(operator)* Pod reconciler wasn't updating restarts in node state
- *(operator)* License adding
- *(operator)* Make metrics binding disabled by default
- *(operator/Makefile)* Fix license-check?
- *(operator/ci)* Invalidate cache and use 1.23.9?
- *(ci)* Kind k8s version matrix was incorrect
- *(operator)* Clean up nodes that no longer exist from status

### New Features

- *(operator)* Initial metrics
- *(operator)* Add testing for metrics in k8s-tests
- *(chart)* Enable scraping of metrics by prometheus
- *(operator)* Add a metric for taint scheduling
- *(operator)* Update k8s sdk version
- Fix agent for distroless and have scr name in flag/history/log
- *(operator)* Added disabled, paused, waiting, and blocked statuses for skyhooks and nodes
- *(operator)* Added comprehensive status and state metrics

### Other Tasks

- Update license header format
- Fix up headers after merge
- *(operator)* Update go and container versions
- *(operator)* Update go import paths to fix importing another project

## [operator/v0.8.0] - 2025-06-06

### Bug Fixes

- Race bug running more then one pod at a time
- Helm e2e tests were broken
- Depends on not waiting for completed tasks to continue
- Depends on not walking the graph correctly in partial stages
- Volume names getting longer than DNS_LABEL
- Update tests to not set limits everywhere anymore
- How we compare interrupt pods
- Reviews
- *(operator)* Change minimum to be 1 due to 0 being considered an 'unset' value for golang

### New Features

- Add gracefully shutdown support
- Remove cert manager
- Change how limits are manged to a use a limitrange via helm
- *(operator)* Add strict ordering of skyhooks along with documentation

### Other Tasks

- Clean up extra newlines from license formatting
- *(deps)* Bump golang.org/x/net from 0.36.0 to 0.38.0

## [operator/v0.7.6] - 2025-03-19

### Bug Fixes

- Miscellaneous fixes to project structure
- Helm tests, seem like they need more time in this env

### New Features

- Change to common license formatter and update all code with that format

### Other Tasks

- *(deps)* Bump golang.org/x/net from 0.33.0 to 0.36.0 in /operator

## [operator/v0.7.5] - 2025-02-28

### Bug Fixes

- Deadlock if reboot pods are missing, adds them back
- Migration bug, and units from new defaults

### Other Tasks

- Version update for security

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

- Update module name to point at github
- *(helm)* Added docs for the helm chart

<!-- Generated by git-cliff -->
