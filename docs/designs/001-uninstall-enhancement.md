# ADR-001: Uninstall enhancement

## Status

**Accepted**, 2026-04-15 (#189). Recorded as an ADR on 2026-11-24 and condensed from the [original design](https://github.com/NVIDIA/nodewright/blob/882b699d/docs/designs/uninstall-enhancement.md), which has the full specification. The implementation departs from it in two places: an uninstall in progress is Stage `uninstall` with State `in_progress`, not a separate State, and a downgrade never runs uninstall (admission rejects downgrading an `enabled: true` package until it is uninstalled, and downgrading an `enabled: false` package leaves its node state as it is).

## Decision summary

Package uninstall becomes explicit. A package declares that it supports uninstall with `uninstall.enabled: true` in the NodeWright spec, and a user requests uninstall with `uninstall.apply: true` while the package stays in `spec.packages`, so the uninstall workload gets the same configMap, env and resources as apply. Removing a package key no longer triggers uninstall, and admission blocks removing an uninstall-capable package until it is gone from node state. Deleting a NodeWright runs the same uninstall for those packages before the finalizer is released; other packages are never uninstalled by the operator and keep their node metadata.

Capability discovery from container images or registries (OCI labels, layer inspection, ORAS) is out of scope.

## Context

Uninstall was triggered by deleting a package from `spec.packages`. In `HandleVersionChange` ([`nodewright_controller.go`](../../operator/internal/controller/nodewright_controller.go)), a package missing from the spec but present in node state moved to `StageUninstall`, and the controller built a synthetic `Package` with only `PackageRef` and `Image`. `createPodFromPackage` mounts ConfigMap volumes and env only from that struct, so uninstall ran without the package's `configMap` and `env`, which broke many packages' assumptions and made uninstall unusable.

Not every package supports uninstall, so the operator had no way to know when it was safe to remove a package's labels and annotations. `HandleFinalizer` uncordoned nodes and removed the finalizer on delete without running any uninstall. The webhook ([`nodewright_webhook.go`](../../operator/api/nodewright/v1alpha1/nodewright_webhook.go)) enforced no rule on removing packages, and `ValidateDelete` was a no-op.

## Decision

**One rule.** A package supports uninstall if and only if `spec.packages[name].uninstall.enabled == true`. Reconciliation, the finalizer, label cleanup and admission all use this rule.

**API.** Each package gains an `uninstall` block:

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | The package supports uninstall. Unset behaves like `false`; there is no nil/unset distinction beyond the CRD default. When `false`, the operator never runs uninstall pods for it and never strips its metadata or node state as if uninstall ran. |
| `apply` | `false` | When `true` and `enabled` is `true`, schedule uninstall while the package remains in the spec. |

**Admission.**

- Reject `uninstall.apply: true` unless `uninstall.enabled` is `true`.
- Reject removing a package key from `spec.packages` unless it is safe to remove: either `enabled` is `true` and no node's state holds the package at any version, or `enabled` is `false` (removal is allowed without an uninstall run).

**Trigger and reconciliation.** The removal-driven uninstall branch in `HandleVersionChange` is removed. Uninstall pods are built from the real `Package` in the spec, not a synthetic one. While uninstall runs, the state is `uninstall_in_progress`, distinct from `in_progress`. After a successful uninstall the spec stays unchanged; a package with `apply: true` that is absent from a node's state is uninstalled on that node, and no apply or config steps run for it.

**Failure and cancel.** A failing uninstall keeps being rescheduled until complete, as a failing install does. Setting `apply` to `false` or removing it, or pausing or stopping the package or NodeWright, halts scheduling of uninstall pods.

**Cleanup after a successful uninstall** (only when `enabled` is `true`): remove the package's node state entries; remove operator-managed keys scoped to that package, distinguishing per-package keys (for example the pod label `nodewright.nvidia.com/package`) from per-NodeWright ones in [`wrapper/node.go`](../../operator/internal/wrapper/node.go); zero out the package's metrics and stop reporting them. When all packages are uninstalled, remove the NodeWright labels and annotations the operator owns for rollout.

**Finalizer.** While a NodeWright is being deleted, any node still holding state for an `enabled: true` package gets the same uninstall orchestration as `uninstall.apply`, since the spec is still available. `enabled: false` packages are skipped and their node metadata is kept so users can see what was applied. Once that is done, the existing behavior runs (uncordon, metrics, remove finalizer) followed by NodeWright metadata cleanup. The condition `UninstallInProgress` is set when the finalizer triggers uninstall, and `UninstallFailed` reports failures during it.

**Version changes.** Upgrade and downgrade flows that use `StageUninstall` and `StageUpgrade` while the spec still names the package may remain, with a documented caveat: a downgrade's uninstall of the old version may see the new version's `configMap`, to be documented with workaround examples such as uninstalling before inducing a downgrade.

## Non-goals

- Discovering uninstall capability from images or registries; a separate design may cover it.
- Fixing the downgrade configMap caveat beyond documenting it.

## Consequences

- Breaking change: clusters that relied on removing a package from the spec to uninstall it must set `uninstall.enabled: true` and `uninstall.apply: true` before removing the key, subject to the admission rules.
- The chainsaw suites in [`k8s-tests/chainsaw/nodewright/`](../../k8s-tests/chainsaw/nodewright/) that cover removal-driven uninstall must move to `uninstall.apply`, explicit `uninstall.enabled` and admission.
- User docs must describe uninstall and the requirement to set `uninstall.enabled: true` for packages that support it.

## References

- #189
- [`operator/internal/controller/pod_controller.go`](../../operator/internal/controller/pod_controller.go): uninstall completion and node state
