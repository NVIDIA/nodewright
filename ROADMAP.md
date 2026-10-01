# NodeWright Roadmap

This roadmap tracks the remaining work toward NodeWright v1 (GA) and the enhancements planned beyond it. It is the cleaned, decision-bearing form of the v1 framework reviewed with the team on 2026-06-01.

Progress is marked inline: ✅ shipped on `main`, 🟡 partially shipped, 🔴 not started. Work merged only on a feature branch counts as 🟡, not ✅.

## Objectives

v1 is the first release we declare stable and production-ready. It is a promise, not a feature count: the API is stable, the core node-lifecycle use cases work end-to-end, it is safe to run unattended on a real fleet, and an outside team can adopt it without us in the room. We sort every candidate against four objectives, and an item only blocks v1 if it fails one of them.

| Objective | Definition |
|-----------|------------|
| **API Stability** | The CRDs graduate `v1alpha1 → v1` under the `nodewright` group, with a frozen schema, a conversion path, and a documented policy for making breaking changes after GA. |
| **Feature Completeness** | The core lifecycle is fully covered with no "fork the agent" gaps: packages as OCI artifacts, a Go agent, package-declared interrupts, and dynamic cordon/drain. |
| **Production Hardening** | Safe to run unattended: no unmaintained or unversioned third-party images, correct behavior under partial failure, verifiable provenance, and CI that is green without reruns. |
| **Adoption Readiness** | An outside team can install, author a package, deploy, and upgrade using only public docs and the CLI. |

## Scope

### 1. API Stability

**Rename to the `nodewright.nvidia.com` API group (required).** ✅ **Shipped** ([#349](https://github.com/NVIDIA/nodewright/pull/349)). The primary CRD is now `nodewright.nvidia.com/v1alpha1 NodeWright`, `DeploymentPolicy` moved group, the on-node annotation/label/finalizer prefix moved, and the kubectl plugin is `kubectl nodewright` (binary `kubectl-nodewright`). The conversion path ships as a rollback-safe migration bridge: a one-way mirror controller that pre-creates a new-group object per legacy object, new-group admission webhooks, deprecation warnings and read-only enforcement on the legacy group, a runtime migration hold while any legacy `Skyhook` is non-complete, and deferred cleanup of legacy-labeled pods and ConfigMaps gated by `LEGACY_CLEANUP_DELAY`. Documented in [`docs/getting-started/migration.md`](docs/getting-started/migration.md).

*Remaining* ([#310](https://github.com/NVIDIA/nodewright/issues/310)): the `skyhook` namespace and the agent image path, the Go identifiers still named `Skyhook` ([#375](https://github.com/NVIDIA/nodewright/issues/375)), and the removal release that drops the legacy CRDs behind a chart pre-upgrade safety hook.

**Graduate the CRDs to `v1`.** 🔴 Lock the NodeWright and DeploymentPolicy schemas and ship a conversion webhook so existing clusters migrate in place.

**Freeze the package contract.** What the operator and CLI can know about a package changes fundamentally once packages are OCI artifacts, so the package contract is part of the surface we are freezing. Dependency handling is validation only: there is no resolver and no auto-install of dependencies in v1. Schema-affecting work:

- ✅ Configurable node-drain options ([#259](https://github.com/NVIDIA/nodewright/issues/259)): `deleteEmptyDirData`, `ignoreDaemonSets`, and `gracePeriod` are on the spec and validated.
- ✅ Package image references are locked to a bare `registry/repository` form; inline tags and digests are rejected at admission ([#282](https://github.com/NVIDIA/nodewright/issues/282), a breaking change taken deliberately before the freeze).
- 🟡 Admission-webhook validation of the `dependsOn` DAG: missing targets and malformed versions are rejected today; explicit cycle rejection is not yet covered (`graph.Valid()` only reports unresolved placeholders).
- 🔴 Package-declared interrupts and the new preflight stage.

**Feature-release process.** 🔴 A defined way to introduce and graduate features after v1 so new work lands without destabilizing the frozen surface. The likely shape is a maturity path every feature flows through (off-by-default or opt-in before it becomes default), with feature flags as one mechanism for opting in or out. The process is the deliverable, not just the flags; because the flags surface is most likely a CRD field or operator config, its shape is part of what we freeze. This is the "how do we add things safely after the freeze" counterpart to the versioning policy below.

**Versioning and compatibility policy.** 🟡 Defining how we change the API safely after v1 is itself a v1 deliverable. The per-component semver policy is documented in [`docs/operations/versioning.md`](docs/operations/versioning.md), and the group rename proved out a working deprecation channel (legacy-group admission warnings ahead of read-only enforcement). Still to do: the per-surface major-bump policy written down as policy rather than practice, and compatibility tests wired into CI so unintended breakage is caught.

**Acceptance:** `v1.0.0` ships the `nodewright.nvidia.com/v1` group with a frozen, documented schema and package contract, a conversion path from `v1alpha1`, and a CI-enforced compatibility policy that gates any breaking change behind a major-version bump.

### 2. Feature Completeness

**The shared core: Go agent and OCI/ORAS packages.** 🟡 The Go agent rewrite is complete; moving packages to OCI artifacts ([#194](https://github.com/NVIDIA/nodewright/issues/194), [#214](https://github.com/NVIDIA/nodewright/issues/214)–[#222](https://github.com/NVIDIA/nodewright/issues/222)) is the keystone of v1. Before the rewrite the Python agent was a black box the operator could only run and watch. Now that the agent is Go, once packages are OCI artifacts, package-handling becomes one shared Go library that all three binaries use, each in its own role: the operator reads a package to drive orchestration (its steps, interrupts, uninstall support, schema, dependencies) without executing it; the CLI uses the same library to validate a package locally before it is applied; the agent streams and applies the layers.

The Go agent shipped with the cutover ([#222](https://github.com/NVIDIA/nodewright/issues/222)) and lives at `agent/`: module bootstrap and pure-data types ([#213](https://github.com/NVIDIA/nodewright/issues/213)), the config loader with embedded JSON schemas and step validation ([#214](https://github.com/NVIDIA/nodewright/issues/214)), filesystem/path/log helpers and the package history store ([#215](https://github.com/NVIDIA/nodewright/issues/215)), the `chroot-exec` subcommand and command runners ([#216](https://github.com/NVIDIA/nodewright/issues/216)), `tee` streaming and `run_step` ([#217](https://github.com/NVIDIA/nodewright/issues/217)), `do_interrupt` including the NodeRestart `-15` special case ([#218](https://github.com/NVIDIA/nodewright/issues/218)), and `Run()` on both the Step and Interrupt interfaces.

The entrypoint and CLI parsing ([#219](https://github.com/NVIDIA/nodewright/issues/219)), the container build and CI ([#220](https://github.com/NVIDIA/nodewright/issues/220)), the `operator-agent` chainsaw suite against the Go image ([#221](https://github.com/NVIDIA/nodewright/issues/221)) and the cutover itself ([#222](https://github.com/NVIDIA/nodewright/issues/222)) have all landed. 🔴 The OCI/ORAS package format itself ([#194](https://github.com/NVIDIA/nodewright/issues/194)) has not started.

The cutover landed ahead of GA, so v1 ships on the Go agent rather than swapping the runtime out from under users afterwards.

**Bring-your-own, discovered interrupts.** 🔴 Interrupts stop being a fixed enum the operator and agent both hardcode (NoOp, NodeRestart, ServiceRestart). A package declares its own interrupt, and the operator discovers what a package needs by reading it. The package becomes the source of truth; the operator orchestrates whatever it finds. Interrupts configured in the spec do not go away: they become an **override** of the discovered behavior, so an operator can force or suppress an interrupt when they need to deviate from what the package reports.

**Preflight stage and dynamic cordon/drain.** 🔴 A new `preflight` lifecycle stage runs before cordon/drain, where each package reports whether it actually needs to interrupt this time. Cordon and drain become dynamic: the operator skips them when nothing requires an interrupt, rather than cordoning and draining because a package might. Because preflight evaluates what would happen without doing it, running it in report-only mode is the natural substrate for dry-run.

**The CLI as an operations tool.** 🟡 The operational verbs shipped: `kubectl nodewright update-state` for fine-grained node-state annotation edits and `reset --package` for targeted reset ([#257](https://github.com/NVIDIA/nodewright/issues/257)), both gated behind an API-served preflight check so they fail loudly against an operator that cannot support them. 🔴 Client-side package validation still depends on the shared library that reads packages: validate a package, show "what will this do", and diff versions before anything touches a node.

**Acceptance:** a package distributed as a signed OCI artifact is inspected by the operator and applied by the Go agent end-to-end, and a node is cordoned and drained only when a package's preflight reports that an interrupt is required.

### 3. Production Hardening

**Correctness under partial failure.** 🟡 Shipped:

- ✅ ConfigMap keys mount as subPaths so they no longer clobber files baked into the package image ([#208](https://github.com/NVIDIA/nodewright/issues/208)).
- ✅ A deferred owned-ConfigMap sync now requeues instead of leaving a stale `Status.ConfigUpdates` entry driving a spurious interrupt ([#245](https://github.com/NVIDIA/nodewright/issues/245)).
- ✅ The runtime-required taint is re-applied on reboot when `autoTaintNewNodes=true`, covering the `applyOnReboot` + `runtimeRequired` + `autoTaintNewNodes` combination ([#180](https://github.com/NVIDIA/nodewright/issues/180)).

Adjacent partial-failure fixes that landed alongside these: level-triggered promotion of packages orphaned at `interrupt`/`skipped` ([#270](https://github.com/NVIDIA/nodewright/issues/270)), drain no longer evicting the operator's own package pods when `additionalTolerations` contains a wildcard ([#296](https://github.com/NVIDIA/nodewright/issues/296), [#297](https://github.com/NVIDIA/nodewright/issues/297)), preserved shared cordons, missing `events.k8s.io` RBAC that was silently dropping every recorded event ([#308](https://github.com/NVIDIA/nodewright/issues/308)), and an uninstall pod that was dropping the package ConfigMap and env ([#186](https://github.com/NVIDIA/nodewright/issues/186)).

🔴 Still open: surfacing ImagePullBackOff / ErrImagePull as an explicit `erroring` state instead of a silent hang ([#306](https://github.com/NVIDIA/nodewright/issues/306)).

**Run package execution as Jobs.** 🟡 Migrate package execution from raw Pods to Kubernetes Jobs ([#223](https://github.com/NVIDIA/nodewright/issues/223)) so retry and backoff use native semantics. In progress on the `feature/package-as-jobs` branch, not yet merged to `main`: the design doc ([#299](https://github.com/NVIDIA/nodewright/issues/299)), package annotation helpers generalized to `client.Object` ([#300](https://github.com/NVIDIA/nodewright/issues/300)), Job builders with stage-timeout and TTL options ([#301](https://github.com/NVIDIA/nodewright/issues/301)), dal Job accessors, the Job event mapper, the `jobMatchesPackage` staleness check and completion recording ([#302](https://github.com/NVIDIA/nodewright/issues/302)), and the first part of the execution swap ([#303](https://github.com/NVIDIA/nodewright/issues/303)). Remaining: the rest of #303 (wiring, RBAC, chart), chainsaw e2e assertions ([#304](https://github.com/NVIDIA/nodewright/issues/304)), and release notes plus upgrade verification ([#305](https://github.com/NVIDIA/nodewright/issues/305)). Follow-ups surfaced by the work and not yet scheduled: per-node workqueue keying ([#382](https://github.com/NVIDIA/nodewright/issues/382)), treating a stage timeout as retryable rather than an immediate park ([#373](https://github.com/NVIDIA/nodewright/issues/373)), and exposing Job execution options through the chart ([#372](https://github.com/NVIDIA/nodewright/issues/372)).

**Verifiable provenance.** 🟡 Build-time provenance ships: keyless cosign signatures, SLSA build provenance attestations, and SBOM attestations on releases, with a verification step in the release workflow ([#224](https://github.com/NVIDIA/nodewright/issues/224)). 🔴 v1 adds the runtime half: the operator and CLI verify a package's attestation via the OCI referrers API before pull or apply. This lands together with the OCI package work rather than as a separate path.

**Release and CI reliability.** 🟡 RC tags no longer steal release notes from the stable release; the changelog tooling now generates from an explicit tag range and splits `CHANGELOG` from `RELEASE_NOTES` ([#246](https://github.com/NVIDIA/nodewright/issues/246)). Kubernetes test versions are centralized on a single source ([#238](https://github.com/NVIDIA/nodewright/issues/238)), the local kind cluster and registry run through ctlptl so helm e2e tests hit the locally-built operator image ([#209](https://github.com/NVIDIA/nodewright/issues/209)), and CI gained CodeQL, actionlint, commit linting, DCO, and a merge-conflict check. 🔴 False-positive flakiness that forces reruns is not eliminated; a known flaky unit test is still open ([#357](https://github.com/NVIDIA/nodewright/issues/357)).

**Multi-tenant node access control (design-first).** 🔴 The operator is a privileged deputy: at apply time, node mutations run with the operator's credentials, so RBAC on the namespaced NodeWright CR does not constrain which cluster-scoped nodes a tenant can target. Close this with admission-time authorization keyed on the requester's identity. This needs a design before implementation, and its v1 gating depends on whether multi-tenant operation is a v1 use case.

**Acceptance:** the operator runs unattended on a real fleet with no unmaintained or unversioned third-party images; package execution retries through Jobs; error states such as image-pull failures and stale-status drift surface as status and events rather than hangs; every package is provenance-verified before apply; and CI passes without reruns.

### 4. Adoption Readiness

**Documentation and examples.** 🟡 A completeness pass over install, upgrade, and day-2 operations so a new adopter is never blocked on tribal knowledge. That sweep has not happened as a deliberate exercise, but docs landed alongside other work: the Skyhook-to-NodeWright migration guide, governance/maintainers/security policy, a local-development guide, a docs-wide rename pass, and a fix for stale `skyhook.nvidia.com` annotation and label references ([#374](https://github.com/NVIDIA/nodewright/issues/374)).

**CLI distribution and compatibility.** 🟡 The CLI is distributed independently of the operator and must work against any supported operator version. The compatibility matrix in [`docs/user-guide/cli.md`](docs/user-guide/cli.md) is current, and every cluster-backed command runs an API-served preflight so version-gated commands fail loudly rather than silently no-op. Keeping the matrix current is ongoing work, not a one-time deliverable.

**Project hygiene.** ✅ **Shipped** ([#260](https://github.com/NVIDIA/nodewright/issues/260)). Issue and PR-management automation is in place: triage, labeler, welcome, lock-threads, inactive-PR reminders, stale handling, `/assign` self-service issue assignment, PR author auto-assignment, and Dependabot.

**Install and upgrade UX.** 🟡 Review the install and upgrade flow end-to-end so adoption does not require hand-holding. Two upgrade-path breakages found and fixed in passing: the immutable Deployment selector that blocked the `skyhook-operator` to `nodewright` chart upgrade ([#285](https://github.com/NVIDIA/nodewright/issues/285)) and the PDB replicas type mismatch ([#314](https://github.com/NVIDIA/nodewright/issues/314)). The deliberate end-to-end review has not happened.

**Acceptance:** an outside team installs NodeWright, authors and deploys a package, and upgrades across a release using only public docs and the CLI, without contacting the maintainers.

## Revision History

| Date | Change |
|------|--------|
| 2026-06-02 | Initial roadmap drafted from the v1 framework and the 2026-06-01 team review |
| 2026-07-29 | Progress pass: marked delivered work inline (✅ / 🟡 / 🔴) against `main` as of `c71b7569`. Notable completions since the draft: the `nodewright.nvidia.com` API rename with its migration bridge, configurable drain options, the CLI's `update-state` and targeted `reset --package`, the `bitnami/kubectl` migration, three partial-failure fixes (#180, #208, #245), the RC-tag release-notes fix, build-time provenance, and the issue/PR automation. |
