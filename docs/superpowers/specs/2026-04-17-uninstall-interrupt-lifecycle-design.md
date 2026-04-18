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

Replaces the current switch. Each case matches exactly one lifecycle phase.

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

### `r.Interrupt` (operator/internal/controller/skyhook_controller.go:1685)

Add a `stage` parameter so the pod's package annotation reflects which cycle the interrupt belongs to:

```go
func (r *SkyhookReconciler) Interrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode,
    _package *v1alpha1.Package, _interrupt *v1alpha1.Interrupt, stage v1alpha1.Stage) error {
    ...
    if err := SetPackages(pod, skyhookNode.GetSkyhook().Skyhook, _package.Image, stage, _package); err != nil { ... }
    ...
}
```

Existing callers pass `v1alpha1.StageInterrupt`. The new uninstall-cycle caller passes `v1alpha1.StageUninstallInterrupt`.

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

- Audit every caller of `r.Interrupt` to pass the new `stage` argument.
- Audit `generateSafeName` / pod-name generation for uninstall-interrupt pods — stage name is part of the pod name, so long names need truncation check.
- Confirm `podMatchesPackage` treats `StageUninstallInterrupt` correctly (interrupt-labeled pod path).
- Confirm `hasUninstallWork` / `updateUninstallConditions` recognize the new stage when deciding the `UninstallInProgress` / `UninstallFailed` conditions.
- Verify `versionChangeDetected` side-effects: the flag is still set on downgrade under the new code. If downstream behavior (e.g., status resets, pod invalidation) assumes a state change accompanied it, adjust so downgrade doesn't trip it inappropriately.
- Verify `ValidateRunningPackages` still invalidates explicit-uninstall pods correctly when spec changes — the fix from commit `eb52ba77` uses `Stage == StageUninstall` and version-in-spec matching; confirm it works for `StageUninstallInterrupt` pods too (or that these don't need invalidation since they're controller-created).
- Docs: update `docs/uninstall.md` to describe the new stage, the cancellation semantics, and the downgrade rule.
