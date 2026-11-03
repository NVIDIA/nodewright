# NodeWright Roadmap

NodeWright runs in production today. This roadmap tracks the work toward v1 (GA), the release where we commit to stability for everyone else, and the larger features planned alongside it. It covers features and API, not bug fixes. Each item links one tracking issue that holds the detail, so this page changes only when direction or status does.

Status: ✅ shipped on `main`, 🟡 in progress or partly shipped, 🔴 not started. Work that is only on a branch or in review is 🟡 at most.

## Big bets

- **Packages as OCI artifacts.** A package becomes a signed OCI artifact that the operator can inspect without running it, the CLI can validate before anything touches a node, and the agent applies with no package-image init container.
- **The agent owns the package lifecycle, with or without Kubernetes.** The same package, with the same lifecycle rules, applies on any machine the agent runs on, whether or not it is a Kubernetes node yet. On a cluster the operator keeps only the decisions that need cluster knowledge.
- **Host drift detection.** NodeWright knows that the host still matches what was applied, not only that it was applied once.

## At a glance

### Required for v1

| Objective | Item | Status | Tracking |
|-----------|------|--------|----------|
| API Stability | Finish the NodeWright rename and remove the legacy group | 🟡 | [#310](https://github.com/NVIDIA/nodewright/issues/310) |
| API Stability | Graduate the CRDs to `v1` | 🔴 | to file |
| API Stability | Freeze the package contract | 🟡 | to file |
| API Stability | Feature-release process | 🔴 | to file |
| API Stability | Versioning and compatibility policy | 🟡 | to file |
| Feature Completeness | Packages as OCI artifacts | 🟡 | [#819](https://github.com/NVIDIA/nodewright/issues/819) |
| Feature Completeness | Bring-your-own, discovered interrupts | 🔴 | to file |
| Feature Completeness | Preflight stage and dynamic cordon/drain | 🔴 | to file |
| Feature Completeness | Client-side package validation in the CLI | 🔴 | to file |
| Production Hardening | Runtime provenance verification | 🔴 | [#819](https://github.com/NVIDIA/nodewright/issues/819) |
| Production Hardening | Release gating on vulnerability findings | 🟡 | to file |
| Production Hardening | Multi-tenant node access control (design first) | 🔴 | to file |
| Adoption Readiness | Documentation completeness pass | 🟡 | to file |
| Adoption Readiness | Install, upgrade and cluster bootstrap | 🟡 | to file |

### Planned, not required for v1

| Item | Status | Tracking |
|------|--------|----------|
| The agent owns the package lifecycle, with or without Kubernetes | 🔴 design | to file |
| Host drift detection | 🔴 | to file |
| Real-machine end-to-end tests | 🔴 design | [NVIDIA/nodewright-packages#152](https://github.com/NVIDIA/nodewright-packages/issues/152) |

## v1 objectives

v1 is a promise, not a feature count. We sort every candidate against four objectives, and an item only blocks v1 if it fails one of them.

| Objective | What it means | Done when |
|-----------|---------------|-----------|
| **API Stability** | The CRDs graduate `v1alpha1 → v1` under the `nodewright` group, with a frozen schema and package contract, a conversion path, and a documented policy for breaking changes after GA. | `v1.0.0` ships the `nodewright.nvidia.com/v1` group with a conversion path from `v1alpha1` and a CI-enforced compatibility policy. |
| **Feature Completeness** | The core lifecycle is covered with no "fork the agent" gaps: packages as OCI artifacts, a Go agent, package-declared interrupts, and dynamic cordon/drain. | A signed OCI package is inspected by the operator and applied by the agent end to end, and a node is cordoned and drained only when a package reports it needs an interrupt. |
| **Production Hardening** | No unmaintained or unversioned third-party images, provenance verified from build to apply, and node access that holds on shared clusters. | Every package is provenance-verified before apply, releases gate on vulnerability findings, and the multi-tenant decision is made and implemented. |
| **Adoption Readiness** | An outside team can install, author a package, deploy, and upgrade using only public docs and the CLI. | An outside team does all of that across a release without contacting the maintainers. |

## Required for v1

### API Stability

**Finish the NodeWright rename and remove the legacy group.** 🟡 The `nodewright.nvidia.com` API group, the `kubectl nodewright` plugin and a rollback-safe migration bridge from the legacy `Skyhook` group have shipped, documented in [`docs/getting-started/migration.md`](docs/getting-started/migration.md). Left: the Go identifiers still named `Skyhook`, and the release that removes the legacy group, planned for v0.21.0. Tracking: [#310](https://github.com/NVIDIA/nodewright/issues/310).

**Graduate the CRDs to `v1`.** 🔴 Lock the NodeWright and DeploymentPolicy schemas and ship a conversion webhook so existing clusters migrate in place. The status shape is part of the schema, so it is settled first: on large clusters the per-node maps in NodeWright status grow with the fleet, so they become optional and reboot boot IDs move to a Node annotation.

**Freeze the package contract.** 🟡 Once packages are OCI artifacts, what the operator and CLI can know about a package changes, so the package contract is part of what we freeze. Drain options and bare image references are already settled. Still to decide before the freeze: rejecting `dependsOn` cycles at admission, how packages declare interrupts, the fate of the skyhook-named on-host paths and `SKYHOOK_*` variables that packages depend on, the node-state annotation format the CLI reads, and whether `-check` stages must be safe to re-run for drift detection. Dependency handling stays validation only: no resolver and no auto-install in v1.

**Feature-release process.** 🔴 A maturity path every new feature flows through after v1 (off by default or opt-in before it becomes default), so new work lands without destabilizing the frozen surface. The process is the deliverable, not just the flags, and the shape of the flags is part of what we freeze. Package artifacts and `agent run` are both opt-in and can serve as the first worked examples.

**Versioning and compatibility policy.** 🟡 The per-component semver policy is documented in [`docs/operations/versioning.md`](docs/operations/versioning.md), each component releases from its own branch family, and the rename proved out a deprecation channel. Left: the per-surface major-bump policy written as policy, and compatibility tests in CI.

### Feature Completeness

**Packages as OCI artifacts.** 🟡 The keystone of v1. With the agent now in Go, package handling becomes one shared library used three ways: the operator reads a package to drive orchestration without executing it, the CLI validates it locally, and the agent streams and applies it. The Go agent shipped as agent v7.0.0. The artifact design and implementation are in review, behind an option that is off by default. Tracking: [#819](https://github.com/NVIDIA/nodewright/issues/819) (under [#194](https://github.com/NVIDIA/nodewright/issues/194)).

**Bring-your-own, discovered interrupts.** 🔴 Interrupts stop being a fixed list the operator and agent both hardcode. A package declares the interrupt it needs, and the operator discovers it by reading the package. Interrupts set in the spec remain as an override, to force or suppress one. The artifact metadata is where the declaration will live.

**Preflight stage and dynamic cordon/drain.** 🔴 A `preflight` stage runs before cordon and drain, where each package reports whether it needs to interrupt this time. The operator cordons and drains only when something does. Run in report-only mode, preflight is also the basis for a dry run. How NodeWright behaves around long-running workloads that cannot be interrupted is designed alongside it.

**Client-side package validation in the CLI.** 🔴 The operational CLI verbs have shipped (`update-state`, targeted `reset --package`). Next, using the shared package library: validate a package, show what it will do, and diff versions before anything touches a node.

### Production Hardening

**Runtime provenance verification.** 🔴 Build-time provenance ships: signatures, SLSA provenance, and SBOM and VEX attestations on every platform image, plus vulnerability scanning. v1 adds the runtime half: the operator and CLI verify a package's attestation before pull or apply. It lands with package artifacts. Tracking: [#819](https://github.com/NVIDIA/nodewright/issues/819).

**Release gating on vulnerability findings.** 🟡 Released images are already scanned, with OpenVEX statements for findings that do not apply. Next, a release fails on an unaddressed finding, and `main` is scanned nightly so the state is known before a release is cut.

**Multi-tenant node access control (design first).** 🔴 Node changes run with the operator's credentials, so RBAC on the namespaced NodeWright resource does not limit which nodes a tenant can target. Close this with admission-time authorization keyed on the requester. Whether it gates v1 depends on whether multi-tenant operation is a v1 use case.

### Adoption Readiness

**Documentation completeness pass.** 🟡 The docs are restructured by audience, with a docs site, a quickstart, references for the custom resource and the lifecycle, and examples. The deliberate pass over install, upgrade and day-2 operations has not happened yet.

**Install, upgrade and cluster bootstrap.** 🟡 Review install and upgrade end to end so adoption needs no hand-holding. Cluster bootstrap is the next gap: tainting a node at registration and labelling nodes when NodeWright has finished with them. The CLI is distributed independently, as a signed OCI artifact too, and works across supported operator versions; keeping that compatibility matrix current is ongoing.

## Planned, not required for v1

**The agent owns the package lifecycle, with or without Kubernetes.** 🔴 Design in progress. Today only the operator can apply a package: the agent runs one stage per invocation, and the operator sequences the stages, one Job per stage. The main goal is to let the same package, with the same rules, be applied on machines that are not Kubernetes nodes, or not yet. Moving per-node sequencing into the agent does that, and leaves the operator only the cluster-wide decisions: cordon, drain, the interruption budget, batches, priority, and merging interrupts across packages. That boundary is also a candidate simplification of the operator. Three phases, each shipping on its own:

1. An opt-in `agent run` that applies a whole lifecycle for one package on one machine, performing interrupts or pausing for the caller at each one, and resuming across a reboot. The operator is unchanged.
2. The operator and the agent share one stage graph, with no behavior change.
3. The operator runs packages through `agent run`, behind an option and under its own design.

Phases 1 and 2 can land before GA.

**Host drift detection.** 🔴 Today nothing looks at a node again after a package completes, until the spec changes or, with `applyOnReboot`, the node reboots, so a hand-edited file or a kernel changed by something else goes unnoticed. Packages already define "applied" through their `-check` stages, so the agent can re-run those checks without applying anything, on a schedule or on demand. On a cluster the operator reports drift as a node condition and event, and each NodeWright chooses to only report or to re-apply through the normal lifecycle. Without Kubernetes, `agent run` runs the same checks. Builds on the agent owning the lifecycle; the contract question is settled before the freeze.

**Real-machine end-to-end tests.** 🔴 Design in progress. Container tests cannot reboot, change a kernel or bootloader, or load a driver, which is most of what packages do. A Go library that boots throwaway VMs runs package steps first, then whole lifecycles through `agent run`, in about five minutes per pull request on hosted amd64 runners. It is importable, so this repository can test the agent's reboot path the same way.

## Shipped

Highlights since the v1 framework was drafted; release notes have the detail.

- ✅ The `nodewright.nvidia.com` API group, the `kubectl nodewright` plugin, and the migration bridge ([#349](https://github.com/NVIDIA/nodewright/issues/349))
- ✅ The Go agent replaces the Python agent: agent v7.0.0 ([#222](https://github.com/NVIDIA/nodewright/issues/222))
- ✅ Package execution as Kubernetes Jobs: operator v0.18.0 ([#223](https://github.com/NVIDIA/nodewright/issues/223))
- ✅ Configurable drain options ([#259](https://github.com/NVIDIA/nodewright/issues/259)) and image references locked to `registry/repository` ([#282](https://github.com/NVIDIA/nodewright/issues/282))
- ✅ CLI operations: `update-state` and targeted `reset --package` ([#257](https://github.com/NVIDIA/nodewright/issues/257)); the CLI as a signed OCI artifact
- ✅ Image-pull failures ([#306](https://github.com/NVIDIA/nodewright/issues/306)) and drain blockers reported as conditions
- ✅ Signed releases with SLSA provenance, per-platform SBOM and VEX, and vulnerability scanning ([#224](https://github.com/NVIDIA/nodewright/issues/224))
- ✅ Issue and PR automation ([#260](https://github.com/NVIDIA/nodewright/issues/260)), and a docs restructure with a quickstart and references

## Revision History

| Date | Change |
|------|--------|
| 2026-06-02 | Initial roadmap drafted from the v1 framework and the 2026-06-01 team review |
| 2026-07-29 | Progress pass against `main` as of `c71b7569` |
| 2026-11-02 | Progress pass against `main` as of `76237437`. Restructured around big bets, an at-a-glance table and one tracking issue per item, with features and API only. Added the agent owning the package lifecycle, host drift detection, real-machine end-to-end tests, release vulnerability gating, and cluster bootstrap. |
