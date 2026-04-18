# Uninstall interrupt lifecycle — design spec

**Date:** 2026-04-17
**Branch:** `feat/uninstall-enhancement`

## Problem

Two critical bugs exist on the `feat/uninstall-enhancement` branch:

1. **Premature `RemoveState`**. `HandleUninstallRequests` matches `status.Stage == StagePostInterrupt && status.State == Complete` as part of the uninstall-interrupt cycle — but that is also the terminal state of an *install* with an interrupt. When a user sets `uninstall.apply=true` on a fully-installed interrupt-ful package, the switch fires `RemoveState` on the very first reconcile, skipping the uninstall pod entirely. Confirmed via a Go unit test: for `PackageStatus{Stage: StagePostInterrupt, State: Complete}` + `Uninstall{Enabled: true, Apply: true}`, the code calls `RemoveState` and returns an empty work list. The chainsaw `explicit-uninstall` test's assertion that an `mypkg-uninstall` init container exists cannot succeed.

2. **`NextStage` map change breaks legacy downgrade**. `StageUninstall → StageInterrupt` in the interrupt map affects the legacy downgrade path (not only explicit uninstall): `HandleCompletePod` seeds the new version at `StageUninstall/Complete`, and `NextStage` now routes that to an interrupt pod instead of an apply pod, rebooting the node without installing.

The root cause of both bugs is that the uninstall cycle reuses install's `StageInterrupt` and `StagePostInterrupt` stages, so handler code cannot tell which lifecycle a stage belongs to.

## Scope

- Fix bug #1 by introducing a dedicated uninstall-cycle interrupt stage.
- Remove the legacy "downgrade triggers uninstall pod" path entirely. Downgrades are permitted only after an explicit uninstall has completed (see webhook section).
- Preserve old-version entries in node state after an `enabled=false` downgrade (D2 semantics: absent-from-state = cleanly uninstalled; non-absent signals the opposite).
- Apply remaining cleanup items from the code review: `semver.IsValid` guard on downgrade check, fix misplaced comment delimiters, collapse duplicated if-blocks in the uninstall switch.

Out of scope:
- Agent-side changes. The uninstall cycle reuses the existing interrupt-pod mechanism (controller-created, type-based: reboot / service / etc.). No new agent `Mode` values, no new scripts.
- Post-interrupt phase for uninstall. The cycle ends at `StageUninstallInterrupt/Complete` → `RemoveState`.

## State machine

**Explicit uninstall, no interrupt:**

```
Terminal install state → user sets apply=true
  → HandleUninstallRequests: Upsert(StageUninstall, InProgress)
  → uninstall pod runs (uninstall.sh + uninstall-check.sh)
  → HandleCompletePod: no interrupt → RemoveState → done
```

**Explicit uninstall, with interrupt:**

```
Terminal install state → user sets apply=true
  → HandleUninstallRequests: Upsert(StageUninstall, InProgress)
  → uninstall pod runs
  → HandleCompletePod: has interrupt → Upsert(StageUninstallInterrupt, InProgress)
  → ProcessInterrupt fires interrupt pod (reboot/service/etc.)
  → interrupt pod completes → default Upsert leaves StageUninstallInterrupt/Complete
  → HandleUninstallRequests switch: StageUninstallInterrupt/Complete → RemoveState → done
```

**Cancellation** (`apply` goes true → false):

| Stage at moment of cancel | Behavior |
|---|---|
| `StageUninstall/InProgress` or `Erroring` | `HandleCancelledUninstalls` resets to `StageApply` (existing) |
| `StageUninstallInterrupt/*` | uncancellable — interrupt fired, must run to completion |

## Data model

Add one value to the `Stage` enum (`operator/api/v1alpha1/skyhook_types.go`):

```go
StageUninstallInterrupt Stage = "uninstall-interrupt"
```

Append to the `Stages` slice so metrics loops pick it up.

Extend the CRD kubebuilder validation marker on `PackageStatus.Stage`:

```go
//+kubebuilder:validation:Enum=apply;interrupt;post-interrupt;config;uninstall;uninstall-interrupt;upgrade
```

Run `make manifests` to regenerate CRD YAML.

No `PackageStatus` struct field changes. No migration concern — existing in-flight state can only be at legacy stages, which the new code paths still handle.

## Controller changes

### `HandleCompletePod` (operator/internal/controller/pod_controller.go)

The uninstall-pod completion branch (currently lines 231-281) is rewritten. The legacy "DOWNGRADE" branch that seeded the new version at `StageUninstall/Complete` is removed. The "has interrupt" case jumps straight to `StageUninstallInterrupt/InProgress` instead of the ambiguous `StageUninstall/Complete`.

```go
} else if packagePtr.Stage == v1alpha1.StageUninstall {
    skyhook, err := r.dal.GetSkyhook(ctx, packagePtr.Skyhook)
    if err != nil { return false, err }
    if skyhook == nil { return updated, nil }

    _package, exists := skyhook.Spec.Packages[packagePtr.Name]
    if !exists {
        // Package removed from spec — clean up.
        if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
            return false, fmt.Errorf("error removing state for removed package: %w", err)
        }
        updated = true
        return updated, nil
    }

    if _package.Version != packagePtr.Version {
        // Defensive: webhook rejects version changes unless the package is already
        // fully uninstalled, so the uninstall pod shouldn't complete for a version
        // that's not in spec. Clean up defensively.
        if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
            return false, fmt.Errorf("error cleaning up stale uninstall: %w", err)
        }
        updated = true
        return updated, nil
    }

    // Same version: explicit or finalizer-driven uninstall.
    if _package.HasInterrupt() {
        if err = skyhookNode.Upsert(packagePtr.PackageRef, packagePtr.Image,
            v1alpha1.StateInProgress, v1alpha1.StageUninstallInterrupt, 0, packagePtr.ContainerSHA); err != nil {
            return false, fmt.Errorf("error transitioning to uninstall-interrupt: %w", err)
        }
    } else {
        if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
            return false, fmt.Errorf("error removing state after uninstall: %w", err)
        }
        zeroOutSkyhookPackageMetrics(packagePtr.Skyhook, packagePtr.Name, packagePtr.Version)
    }
    updated = true
}
```

### `HandleUninstallRequests` switch (operator/internal/controller/skyhook_controller.go)

Two changes to the function: (a) relax the early-continue gate so uninstall-cycle state can be drained even after the user flips `apply` back to false, and (b) replace the switch.

**Early-continue gate (line 787 area)** — `!needsUninstall` alone is not enough to skip; a package mid-uninstall-cycle must still reach the switch so cleanup happens:

```go
needsUninstall := pkg.IsUninstalling() || (beingDeleted && pkg.UninstallEnabled())
inUninstallCycle := exists &&
    (status.Stage == v1alpha1.StageUninstall || status.Stage == v1alpha1.StageUninstallInterrupt)
if !needsUninstall && !inUninstallCycle {
    continue
}
```

Without this, user flipping `apply: true → false` after the uninstall pod finished strands the node at `StageUninstallInterrupt/Complete` forever (the switch's `RemoveState` path is never reached).

**Replaced switch.** Each case matches exactly one lifecycle phase; fall-through handles install-cycle terminal states triggering a fresh uninstall:

```go
switch status.Stage {
case v1alpha1.StageUninstall:
    // Uninstall pod running or retrying. All states re-add so ApplyPackage
    // sees the package. StageUninstall/Complete is not used under new rules
    // (HandleCompletePod transitions directly to StageUninstallInterrupt or
    // RemoveState), but we re-add it defensively.
    p := skyhook.GetSkyhook().Spec.Packages[name]
    toUninstall = appendIfNotPresent(toUninstall, &p)
    continue

case v1alpha1.StageUninstallInterrupt:
    if status.State == v1alpha1.StateComplete {
        if err := node.RemoveState(pkg.PackageRef); err != nil {
            return nil, fmt.Errorf("error removing state after uninstall interrupt for %s: %w", name, err)
        }
        zeroOutSkyhookPackageMetrics(skyhook.GetSkyhook().Name, pkg.Name, pkg.Version)
        node.SetStatus(v1alpha1.StatusInProgress)
    } else {
        p := skyhook.GetSkyhook().Spec.Packages[name]
        toUninstall = appendIfNotPresent(toUninstall, &p)
    }
    continue
}

// Install-cycle stages (Apply, Config, Interrupt, PostInterrupt, Upgrade): only
// kick off uninstall from a terminal Complete state. Don't interrupt an install
// mid-flight — wait for it.
if status.State != v1alpha1.StateComplete {
    continue
}
if err := node.Upsert(pkg.PackageRef, pkg.Image,
    v1alpha1.StateInProgress, v1alpha1.StageUninstall, 0, pkg.ContainerSHA); err != nil {
    return nil, fmt.Errorf("error triggering uninstall for %s: %w", name, err)
}
node.SetStatus(v1alpha1.StatusInProgress)
p := skyhook.GetSkyhook().Spec.Packages[name]
toUninstall = appendIfNotPresent(toUninstall, &p)
```

The previous `case StageInterrupt:` and `case StagePostInterrupt:` branches are gone. If the user flips `apply=true` mid-install-interrupt, `status.State != Complete` guards the fallthrough — reconcile requeues until install finishes, then triggers uninstall.

### `NextStage` (operator/api/v1alpha1/skyhook_types.go)

Revert the feat commit's `StageUninstall → StageInterrupt` edit. `NextStage` should have no entry for `StageUninstall` or `StageUninstallInterrupt`:

```go
// no-interrupt map (unchanged from pre-feat)
nextStage := map[Stage]Stage{
    StageApply:   StageConfig,
    StageUpgrade: StageConfig,
}

if hasInterrupt := (*ns).HasInterrupt(*_package, interrupt, config); hasInterrupt {
    nextStage = map[Stage]Stage{
        StageUpgrade:   StageConfig,
        StageApply:     StageConfig,
        StageConfig:    StageInterrupt,
        StageInterrupt: StagePostInterrupt,
    }
}
```

Uninstall-cycle transitions are driven by `HandleCompletePod` and `HandleUninstallRequests`, not by the `NextStage` map.

### `r.Interrupt` (operator/internal/controller/skyhook_controller.go:1685) and `createInterruptPodForPackage` (line 1916)

Three coordinated changes to support two interrupt cycles per package-version on the same node:

**1. `r.Interrupt` takes a `stage` parameter.** Propagate it to BOTH the `SetPackages` call (line 1718) AND the `Upsert` call that marks the pod in-progress (line 1730). Missing the Upsert leaves the node annotation at `StageInterrupt` even when we intended `StageUninstallInterrupt`, breaking every downstream check.

```go
func (r *SkyhookReconciler) Interrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode,
    _package *v1alpha1.Package, _interrupt *v1alpha1.Interrupt, stage v1alpha1.Stage) error {
    ...
    if err := SetPackages(pod, skyhookNode.GetSkyhook().Skyhook, _package.Image, stage, _package); err != nil { ... }
    ...
    _ = skyhookNode.Upsert(_package.PackageRef, _package.Image,
        v1alpha1.StateInProgress, stage, 0, _package.ContainerSHA)  // was: hardcoded StageInterrupt
    ...
}
```

Existing callers pass `v1alpha1.StageInterrupt`. The new uninstall-cycle caller passes `v1alpha1.StageUninstallInterrupt`.

**2. `createInterruptPodForPackage` encodes stage in pod name.** Currently the pod name is `generateSafeName(63, skyhook.Name, "interrupt", string(_interrupt.Type), nodeName)` — the same for install-cycle and uninstall-cycle interrupt pods with the same interrupt type. Under normal sequencing (install's interrupt pod is torn down before uninstall begins) this is fine, but during controller restart + stale pod, or fast reconcile races, `r.Interrupt`'s `PodExists` short-circuit at line 1696-1703 would see a lingering install-cycle pod and refuse to create the uninstall-cycle one.

Change:

```go
func createInterruptPodForPackage(opts SkyhookOperatorOptions, _interrupt *v1alpha1.Interrupt,
    argEncode string, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage) *corev1.Pod {
    ...
    // Include stage so install-cycle and uninstall-cycle interrupt pods don't collide.
    Name: generateSafeName(63, skyhook.Name, string(stage), string(_interrupt.Type), nodeName),
    ...
}
```

Every caller of `createInterruptPodForPackage` passes its stage through (`r.Interrupt`, `podMatchesPackage`).

**3. `podMatchesPackage`'s `createInterruptPodForPackage` call** (line 2282) passes the pod's own running stage, so the expected/actual comparison stays accurate.

### `ProcessInterrupt` (operator/internal/controller/skyhook_controller.go:2485)

Extend the three existing blocks to be stage-aware.

Race recovery (pod missing after reboot):

```go
if status != nil && (status.State == StateInProgress || status.State == StateErroring) &&
    (status.Stage == StageInterrupt || status.Stage == StageUninstallInterrupt) {
    if err := r.Interrupt(ctx, skyhookNode, _package, interrupt, status.Stage); err != nil {
        return false, err
    }
}
```

Cordon + drain: extend the guard so it also fires when we're about to run the uninstall pod on a node that's not yet cordoned:

```go
if stage == v1alpha1.StageApply || stage == v1alpha1.StageUninstall {
    ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, _package)
    if err != nil { return false, err }
    if !ready { return false, nil }
}
```

Fire uninstall-cycle interrupt (parallel to the install-cycle block):

```go
// Install-cycle interrupt (existing)
if stage == StageInterrupt && runInterrupt {
    if err := r.Interrupt(ctx, skyhookNode, _package, interrupt, StageInterrupt); err != nil {
        return false, err
    }
    return false, nil
}

// Uninstall-cycle interrupt (new) — always runs (no budget skip once uninstall
// has started). HandleCompletePod has already set StageUninstallInterrupt/InProgress.
if status != nil && status.Stage == StageUninstallInterrupt && status.State != StateComplete {
    if err := r.Interrupt(ctx, skyhookNode, _package, interrupt, StageUninstallInterrupt); err != nil {
        return false, err
    }
    return false, nil
}
```

Extend the wait condition:

```go
if status != nil && (status.Stage == StageInterrupt || status.Stage == StageUninstallInterrupt) &&
    status.State != StateComplete {
    return false, nil
}
```

**Dead-code cleanup.** The `if stage == StageUninstall` arm added to the cordon guard is not reachable under the proposed code: `NextStage` has no entry for `StageUninstall` after the revert, so when `ProcessInterrupt` is called for a package at `StageUninstall/InProgress`, `nextStage` is nil and the local `stage` variable stays at `StageApply` — the cordon guard fires via the `StageApply` arm. Keep `|| stage == v1alpha1.StageUninstall` for symmetry / future-proofing, but the spec notes it's currently a no-op.

### `ApplyPackage` guard (operator/internal/controller/skyhook_controller.go:2574)

Three defensive changes so `ApplyPackage` can't create a stage-script pod for an uninstall-cycle interrupt stage.

**1. Stage-resolution switch at line 2588** — extend so `StageUninstallInterrupt` is recognized:

```go
if packageStatus, found := skyhookNode.PackageStatus(_package.GetUniqueName()); found {
    switch packageStatus.Stage {
    case v1alpha1.StageConfig, v1alpha1.StageUpgrade, v1alpha1.StageUninstall,
         v1alpha1.StageUninstallInterrupt:
        stage = packageStatus.Stage
    }
}
```

**2. Early bail for interrupt-stage packages.** `StageUninstallInterrupt` pods are controller-created via `r.Interrupt` (through `ProcessInterrupt`), not via `createPodFromPackage`. If `ProcessInterrupt` returned `true` for this stage (shouldn't, but defense in depth), `ApplyPackage` should bail before calling `createPodFromPackage`:

```go
// After stage resolution, before PodExists check:
if stage == v1alpha1.StageUninstallInterrupt {
    // Interrupt pods for the uninstall cycle are managed entirely by
    // ProcessInterrupt + r.Interrupt. Nothing to apply here.
    return nil
}
```

**3. Filter in `RunSkyhookPackages`** (line 655): the existing `IsUninstallInProgress` check skips install-apply for packages mid-uninstall. Extend it to cover the uninstall-interrupt phase — see the "helpers" section below.

### Helpers — cover `StageUninstallInterrupt` everywhere

Rename `IsUninstallInProgress` to `IsUninstallCycleInProgress` (or add a new helper with that name), returning true for BOTH `StageUninstall` and `StageUninstallInterrupt`. Update every caller:

- `RunSkyhookPackages` line 655 (install-filter)
- `HandleVersionChange` line 1062 (skip-version-change filter)
- `hasUninstallWork` line 916
- `updateUninstallConditions` (checks nodes for in-progress uninstall to emit the `UninstallInProgress` condition)

Callers that specifically want "uninstall pod running" (not the interrupt) can keep the narrower check inline.

Also add a sibling guard to `NodeState.IsComplete` (skyhook_types.go line 526) — defense in depth: the current guard treats `StageUninstall` as "cannot be complete"; add `|| status.Stage == StageUninstallInterrupt` with the same reasoning.

### `HandleVersionChange` (operator/internal/controller/skyhook_controller.go:1044)

Remove the downgrade branch at lines 1113-1124 and the synthetic-uninstall-package block at 1128-1148. Upgrade branch stays. Downgrades become a no-op here — old-version entries persist in node state, and `RunNext` picks up the new (absent) version for fresh install.

```go
} else if exists && _package.Version != packageStatus.Version {
    versionChangeDetected = true
    comparison := version.Compare(_package.Version, packageStatus.Version)
    if comparison == -2 {
        return nil, errors.New("error comparing package versions: ...")
    }

    if comparison == 1 {
        // Upgrade path (existing, unchanged).
        _packageStatus, found := node.PackageStatus(_package.GetUniqueName())
        if found && _packageStatus.Stage == v1alpha1.StageUpgrade {
            continue
        }
        if err := node.Upsert(_package.PackageRef, _package.Image,
            v1alpha1.StateInProgress, v1alpha1.StageUpgrade, 0, _package.ContainerSHA); err != nil {
            return nil, fmt.Errorf("error updating node status: %w", err)
        }
        upgrade = true
    }
    // Downgrade: no-op. Webhook has either rejected the update, or the old
    // version is already absent from node state (fully uninstalled). If the
    // old version entry still exists here (enabled=false downgrade), we
    // intentionally leave it — D2 semantics.
}
```

## Webhook changes (operator/api/v1alpha1/skyhook_webhook.go)

Replace the downgrade check (lines 144-156) with the stricter rule.

```go
// Reject version downgrade unless the package has already been explicitly
// uninstalled on all nodes.
for name, oldPkg := range oldSkyhook.Spec.Packages {
    newPkg, exists := skyhook.Spec.Packages[name]
    if !exists {
        continue
    }
    if newPkg.Version == oldPkg.Version {
        continue
    }
    if !semver.IsValid(newPkg.Version) || !semver.IsValid(oldPkg.Version) {
        continue // not comparable; Validate() rejects invalid formats separately
    }
    if semver.Compare(newPkg.Version, oldPkg.Version) != -1 {
        continue // upgrade or equal — allowed
    }

    // Downgrade. Required:
    //   (a) OLD spec already had uninstall.apply=true, AND
    //   (b) package absent from all nodes (uninstall complete per D2).
    if !oldPkg.IsUninstalling() {
        return nil, fmt.Errorf(
            "package %q: downgrade not allowed — set uninstall.apply=true first, "+
                "wait for uninstall to complete, then change version", name)
    }
    if !isPackageFullyUninstalled(oldSkyhook, name) {
        return nil, fmt.Errorf(
            "package %q: downgrade not allowed — uninstall has not yet completed "+
                "on all nodes. Wait for the uninstall to finish, then change version", name)
    }
}
```

Other existing checks in `ValidateUpdate` (removal of enabled packages, cancel warning) are unchanged.

## Cleanup items (from review)

- Fix `/` → `//` comment typos at skyhook_controller.go lines 513, 775.
- The `HandleUninstallRequests` switch's `StageUninstall` case is already simplified in the new design (single add-to-list, no duplicated if-blocks).
- `semver.IsValid` guard is included in the new webhook check.

## Test plan

### Go unit tests

**`HandleUninstallRequests`** — add cases for every new switch branch:

| Setup | Expected |
|---|---|
| `StagePostInterrupt/Complete` + `apply=true` + has interrupt | `Upsert(StageUninstall, InProgress)`; package added to list. **Regression test for bug #1.** |
| `StagePostInterrupt/Complete` + `apply=true` + no interrupt | `Upsert(StageUninstall, InProgress)`. |
| `StageInterrupt/InProgress` + `apply=true` (install mid-interrupt) | no Upsert, no RemoveState. |
| `StagePostInterrupt/InProgress` + `apply=true` | no Upsert. |
| `StageUninstallInterrupt/InProgress` | added to pod list, no state change. |
| `StageUninstallInterrupt/Complete` | `RemoveState` called; metrics zeroed. |
| `StageUninstallInterrupt/Complete` + `apply=false` (cancel after uninstall-pod done) | `RemoveState` called — early-continue gate must not skip uninstall-cycle cleanup. |
| `StageUninstallInterrupt/InProgress` + `apply=false` (cancel mid-interrupt) | added to pod list, no state change (uncancellable; interrupt proceeds). |
| `StageUninstall/Complete` (defensive) | added to pod list. |

**`HandleCompletePod`** — uninstall pod completion branch:

| Setup | Expected |
|---|---|
| Spec same version + has interrupt | `Upsert(StageUninstallInterrupt, InProgress)`. |
| Spec same version + no interrupt | `RemoveState`; metrics zeroed. |
| Spec version differs | `RemoveState` (defensive). |
| Package missing from spec | `RemoveState`. |

**`HandleVersionChange`**:

| Setup | Expected |
|---|---|
| Downgrade, `enabled=false` | no Upsert for old, no synthetic package added. Old state preserved. |
| Upgrade | existing Upsert to `StageUpgrade/InProgress`. |

**Webhook `ValidateUpdate` (skyhook_types_test.go or skyhook_webhook_test.go)**:

| Setup | Expected |
|---|---|
| Downgrade + old `apply=false` | reject with "set uninstall.apply=true first" |
| Downgrade + old `apply=true` + node state still contains package | reject with "uninstall has not yet completed" |
| Downgrade + old `apply=true` + node state empty of package | allow |
| Upgrade + any apply setting | allow |
| Invalid semver in old or new | skip check; separate Validate() path handles |

**`ProcessInterrupt`** (add test if not present):

| Setup | Expected |
|---|---|
| `status.Stage=StageUninstallInterrupt/InProgress` | `r.Interrupt(..., StageUninstallInterrupt)` called; returns `false`. |
| Race recovery: `StageUninstallInterrupt/InProgress` + pod missing | `r.Interrupt` called. |

**`r.Interrupt`**:

| Setup | Expected |
|---|---|
| Called with `stage=StageUninstallInterrupt` | Created pod annotation shows `stage: "uninstall-interrupt"`. `Upsert` writes `StageUninstallInterrupt`, not `StageInterrupt`. |
| Pod name for install-cycle vs uninstall-cycle interrupt on same package-version/node | Names differ (stage in the hash). No collision. |

**`ApplyPackage`**:

| Setup | Expected |
|---|---|
| `packageStatus.Stage == StageUninstallInterrupt` | Returns `nil` without calling `createPodFromPackage`. |

### Chainsaw tests

**Update:**
- `k8s-tests/chainsaw/skyhook/explicit-uninstall/chainsaw-test.yaml` — change the mid-flight assertion from `stage: "interrupt"` to `stage: "uninstall-interrupt"`. Rest of the flow is the e2e happy path and stays.
- `k8s-tests/chainsaw/helm/helm-webhook-test/` — verify `update-downgrade-enabled-pkg.yaml` still covers the reject path; update expected error message to match new webhook copy.

**Add:**
- `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/` — install v2.1.4 with `enabled=true apply=false`; flip `apply=true`; wait for package absent from node state; update spec to v1.0.0 `apply=false`; assert v1.0.0 installs cleanly.
- `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/` — install v2.0.0 with `enabled=false`; downgrade to v1.0.0; assert node state contains both `mypkg|2.0.0` (preserved per D2) and `mypkg|1.0.0/stage=config/state=complete`.

**Remove:**
- `uninstall-upgrade-skyhook/` (already deleted in working tree).
- Any other chainsaw test that exercises the legacy "downgrade triggers uninstall pod" path — audit during implementation. Candidates to inspect: `interrupt-grouping`, `config-skyhook`, `depends-on`, `cleanup-pods` (each uses multiple versions; most likely upgrade-only, but verify each).

**Unchanged:**
- `uninstall-fix-config/`, `uninstall-finalizer-fix/`, `uninstall-cancel/`, `uninstall-mixed-packages/`, `uninstall-on-delete/` — orthogonal to this change. Verify no implicit dependence on the removed downgrade path.

## Open items for implementation plan

These are deferred to the implementation plan, not yet folded into the spec:

- `isPackageFullyUninstalled` returns `false` when `NodeState` is empty — rejects downgrade on a CR whose node selector matches zero nodes. Pre-existing issue the spec inherits by adding a second call site. Decision needed: flip to `true` (empty = nothing to uninstall = fully uninstalled) or keep current behavior and document.
- `enabled: true → false` flip mid-uninstall: currently not webhook-rejected. Cancellation via `HandleCancelledUninstalls` only resets `StageUninstall`, not `StageUninstallInterrupt`. If the uninstall pod completed before the flip, `HandleCompletePod` would still transition to `StageUninstallInterrupt` and reboot. Decision needed: webhook-reject the flip while uninstall in progress, or guard in `HandleCompletePod`.
- Cancellation warning text in `ValidateUpdate` (line 161) is misleading once `StageUninstallInterrupt` exists. Consider refining the warning, or gating cancel via webhook rejection when any node is at `StageUninstallInterrupt`.
- `versionChangeDetected` side-effects: the flag is still set on downgrade under the new code. Audit downstream behavior (status resets, pod invalidation) for side-effects that assumed an accompanying state change.
- Verify `ValidateRunningPackages` still invalidates explicit-uninstall pods correctly when spec changes. The fix from commit `eb52ba77` uses `Stage == StageUninstall` and version-in-spec matching — for `StageUninstallInterrupt` pods, the existing stage-mismatch check at line 2449 correctly invalidates stale install-cycle interrupt pods when node state progresses, so no additional change needed there. Verify this during implementation.
- Docs: update `docs/uninstall.md` to describe the new stage, the cancellation semantics, and the downgrade rule.
