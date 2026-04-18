# Uninstall Interrupt Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix two critical bugs in the explicit-uninstall feature and remove the legacy "downgrade triggers uninstall pod" code path, by adding a new `StageUninstallInterrupt` stage that disambiguates the uninstall-cycle interrupt from the install-cycle interrupt.

**Architecture:** Introduce one new `Stage` enum value (`uninstall-interrupt`) to the CRD. `HandleCompletePod` transitions from uninstall → `StageUninstallInterrupt/InProgress` when the package has an interrupt; `ProcessInterrupt` fires the interrupt pod; `HandleUninstallRequests` switch detects `StageUninstallInterrupt/Complete` and calls `RemoveState`. Legacy downgrade code is deleted; webhook requires `oldPkg.IsUninstalling()` + absent-from-all-nodes before allowing a version downgrade.

**Tech Stack:** Go (controller-runtime, Ginkgo/Gomega, testify gomega), Kubernetes CRDs (kubebuilder), Kyverno Chainsaw (e2e tests).

**Spec reference:** `docs/superpowers/specs/2026-04-17-uninstall-interrupt-lifecycle-design.md`

**Working directory:** `/Users/ayuskauskas/git_repos/nvidia/nodewright`
**Branch:** `feat/uninstall-enhancement` (continuing the existing branch)
**Commit style:** Conventional Commits, DCO sign-off (`git commit -s`).

---

## Phase 1: Data Model — Add `StageUninstallInterrupt`

### Task 1: Add `StageUninstallInterrupt` constant to Stage enum

**Files:**
- Modify: `operator/api/v1alpha1/skyhook_types.go:707-725`

- [ ] **Step 1: Add constant to the `Stage` const block**

Open `operator/api/v1alpha1/skyhook_types.go`. Find the const block at line 707:

```go
const (
	StageUninstall     Stage = "uninstall"
	StageUpgrade       Stage = "upgrade"
	StageApply         Stage = "apply"
	StageInterrupt     Stage = "interrupt"
	StagePostInterrupt Stage = "post-interrupt"
	StageConfig        Stage = "config"
)
```

Change to:

```go
const (
	StageUninstall          Stage = "uninstall"
	StageUninstallInterrupt Stage = "uninstall-interrupt"
	StageUpgrade            Stage = "upgrade"
	StageApply              Stage = "apply"
	StageInterrupt          Stage = "interrupt"
	StagePostInterrupt      Stage = "post-interrupt"
	StageConfig             Stage = "config"
)
```

- [ ] **Step 2: Add to `Stages` slice**

Find the `Stages` var block at line 717:

```go
Stages = []Stage{
    StageUninstall,
    StageUpgrade,
    StageApply,
    StageInterrupt,
    StagePostInterrupt,
    StageConfig,
}
```

Change to:

```go
Stages = []Stage{
    StageUninstall,
    StageUninstallInterrupt,
    StageUpgrade,
    StageApply,
    StageInterrupt,
    StagePostInterrupt,
    StageConfig,
}
```

- [ ] **Step 3: Update CRD kubebuilder enum annotation**

Find the `PackageStatus.Stage` field (around line 682) with the kubebuilder marker:

```go
//+kubebuilder:validation:Enum=apply;interrupt;post-interrupt;config;uninstall;upgrade
Stage Stage `json:"stage"`
```

Change to:

```go
//+kubebuilder:validation:Enum=apply;interrupt;post-interrupt;config;uninstall;uninstall-interrupt;upgrade
Stage Stage `json:"stage"`
```

- [ ] **Step 4: Regenerate CRD manifests and deepcopy**

Run from `operator/` directory:

```bash
cd operator && make manifests && make generate
```

Expected: CRD YAML files in `operator/config/crd/` and `operator/internal/controller/crd/` are updated with the new enum value. `zz_generated.deepcopy.go` may also be regenerated.

- [ ] **Step 5: Verify it compiles**

```bash
cd operator && go build ./...
```

Expected: clean build with no errors.

- [ ] **Step 6: Commit**

```bash
cd /Users/ayuskauskas/git_repos/nvidia/nodewright
git add operator/api/v1alpha1/skyhook_types.go operator/api/v1alpha1/zz_generated.deepcopy.go operator/config/crd/ operator/internal/controller/crd/ 2>/dev/null || true
git add -A operator/config/crd/ operator/internal/controller/crd/
git commit -s -m "$(cat <<'EOF'
feat(api): add StageUninstallInterrupt to Stage enum

Introduces a new stage distinct from the install-cycle StageInterrupt so
HandleCompletePod and HandleUninstallRequests can unambiguously track the
uninstall-cycle interrupt phase.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2: Revert `NextStage` map change

### Task 2: Revert `StageUninstall → StageInterrupt` edit in NextStage

**Files:**
- Modify: `operator/api/v1alpha1/skyhook_types.go:550-578`
- Test: `operator/api/v1alpha1/skyhook_types_test.go`

- [ ] **Step 1: Write the failing test**

Append to `operator/api/v1alpha1/skyhook_types_test.go` inside the existing `Describe("Skyhook Types", ...)` block (find the closing of the Describe at the bottom of the file and add before it):

```go
It("NextStage returns nil for StageUninstall when package has interrupt", func() {
    pkg := &Package{
        PackageRef: PackageRef{Name: "my-pkg", Version: "1.0.0"},
        Interrupt:  &Interrupt{Type: REBOOT},
    }
    ns := NodeState{
        "my-pkg|1.0.0": PackageStatus{
            Name: "my-pkg", Version: "1.0.0", Stage: StageUninstall, State: StateComplete,
        },
    }
    interruptMap := map[string][]*Interrupt{}
    configMap := map[string][]string{}

    next := ns.NextStage(pkg, interruptMap, configMap)
    Expect(next).To(BeNil())
})

It("NextStage returns nil for StageUninstallInterrupt", func() {
    pkg := &Package{
        PackageRef: PackageRef{Name: "my-pkg", Version: "1.0.0"},
        Interrupt:  &Interrupt{Type: REBOOT},
    }
    ns := NodeState{
        "my-pkg|1.0.0": PackageStatus{
            Name: "my-pkg", Version: "1.0.0", Stage: StageUninstallInterrupt, State: StateComplete,
        },
    }
    interruptMap := map[string][]*Interrupt{}
    configMap := map[string][]string{}

    next := ns.NextStage(pkg, interruptMap, configMap)
    Expect(next).To(BeNil())
})
```

- [ ] **Step 2: Run tests to verify the first one fails**

```bash
cd operator && go test ./api/v1alpha1/ -run TestApi -v 2>&1 | grep -A 3 "NextStage returns nil for StageUninstall when"
```

Expected: `NextStage returns nil for StageUninstall when package has interrupt` FAILS because the current code maps `StageUninstall → StageInterrupt` in the interrupt map.

- [ ] **Step 3: Revert the NextStage interrupt map**

Open `operator/api/v1alpha1/skyhook_types.go`. Find the function `NextStage` around line 550. Locate the `if hasInterrupt` block with the `nextStage` map:

```go
if hasInterrupt := (*ns).HasInterrupt(*_package, interrupt, config); hasInterrupt {
    nextStage = map[Stage]Stage{
        StageUpgrade:   StageConfig,
        StageUninstall: StageInterrupt, // explicit uninstall → run interrupt after uninstall
        StageApply:     StageConfig,
        StageConfig:    StageInterrupt,
        StageInterrupt: StagePostInterrupt,
    }
}
```

Change to (remove the `StageUninstall` entry):

```go
if hasInterrupt := (*ns).HasInterrupt(*_package, interrupt, config); hasInterrupt {
    nextStage = map[Stage]Stage{
        StageUpgrade:   StageConfig,
        StageApply:     StageConfig,
        StageConfig:    StageInterrupt,
        StageInterrupt: StagePostInterrupt,
    }
}
```

Also verify the no-interrupt map (just above) does not have a `StageUninstall` entry (it shouldn't — original code):

```go
nextStage := map[Stage]Stage{
    StageUninstall: StageApply,
    StageApply:     StageConfig,
    StageUpgrade:   StageConfig,
}
```

**Keep the no-interrupt `StageUninstall: StageApply` entry as-is.** It's used today by upgrade/fresh-install scenarios where a prior uninstall state seed transitions to apply. It's benign; we leave it alone.

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd operator && go test ./api/v1alpha1/ -run TestApi -v 2>&1 | grep -E "(PASS|FAIL).*NextStage"
```

Expected: both new `NextStage` tests PASS.

Also run the full types test file to make sure nothing else regressed:

```bash
cd operator && go test ./api/v1alpha1/ -v 2>&1 | tail -20
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add operator/api/v1alpha1/skyhook_types.go operator/api/v1alpha1/skyhook_types_test.go
git commit -s -m "$(cat <<'EOF'
fix(api): revert NextStage mapping for StageUninstall

The StageUninstall → StageInterrupt mapping in the interrupt map was
added by the feat commit but is incorrect: NextStage is also used by
legacy downgrade and would route a downgraded package's new version
directly to interrupt instead of install. The uninstall cycle's
StageUninstall → StageUninstallInterrupt transition is driven by
HandleCompletePod, not NextStage.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3: Rewrite `HandleCompletePod` uninstall branch

### Task 3: HandleCompletePod — transition to StageUninstallInterrupt when has interrupt

**Files:**
- Modify: `operator/internal/controller/pod_controller.go:231-281`
- Test: `operator/internal/controller/pod_controller_test.go` (create if missing)

- [ ] **Step 1: Check if test file exists**

```bash
ls operator/internal/controller/pod_controller_test.go 2>&1
```

If it exists, review what's there. If not, we'll add to `skyhook_controller_test.go` instead (lots of existing test coverage there).

- [ ] **Step 2: Note the test coverage strategy**

`HandleCompletePod` is tightly coupled to `r.dal.GetSkyhook` and node-state mutations. The controller-test fixtures for this method require significant setup. Rather than stub a `t.Skip`, the behavioral coverage for this branch comes from two places:

1. **Chainsaw `explicit-uninstall`** exercises the has-interrupt path end-to-end.
2. **Chainsaw `uninstall-fix-config`** exercises the uninstall-pod retry path which ends in this branch.

No new Go unit test is added for this task. If later needed, `TestHandleCompletePod_UninstallBranch` can be added with a mock `r.dal` and mock `skyhookNode` — see `TestHandleUninstallRequests` in `skyhook_controller_test.go` for the fixture pattern.

- [ ] **Step 3: Rewrite the HandleCompletePod uninstall branch**

Open `operator/internal/controller/pod_controller.go`. Find the `else if packagePtr.Stage == v1alpha1.StageUninstall` branch starting around line 231. Replace the entire branch (lines 231-281) with:

```go
} else if packagePtr.Stage == v1alpha1.StageUninstall {
    skyhook, err := r.dal.GetSkyhook(ctx, packagePtr.Skyhook)
    if err != nil {
        return false, err
    }

    if skyhook != nil {
        _package, exists := skyhook.Spec.Packages[packagePtr.Name]

        if !exists {
            // Package removed from spec — clean up node state.
            if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
                return false, fmt.Errorf("error removing state for removed package: %w", err)
            }
            updated = true
            return updated, nil
        }

        if _package.Version != packagePtr.Version {
            // Defensive: webhook rejects version changes unless the package is
            // already fully uninstalled, so the uninstall pod shouldn't complete
            // for a version that's not in spec. Clean up defensively.
            if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
                return false, fmt.Errorf("error cleaning up stale uninstall: %w", err)
            }
            updated = true
            return updated, nil
        }

        // Same version in spec: explicit or finalizer-driven uninstall.
        if _package.HasInterrupt() {
            // Package has an interrupt — advance to the uninstall-interrupt stage
            // so ProcessInterrupt fires the interrupt pod on the next reconcile.
            if err = skyhookNode.Upsert(packagePtr.PackageRef, packagePtr.Image,
                v1alpha1.StateInProgress, v1alpha1.StageUninstallInterrupt, 0, packagePtr.ContainerSHA); err != nil {
                return false, fmt.Errorf("error transitioning to uninstall-interrupt: %w", err)
            }
        } else {
            // No interrupt — remove state immediately (absent = uninstalled per D2).
            if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
                return false, fmt.Errorf("error removing uninstalled package state: %w", err)
            }
            zeroOutSkyhookPackageMetrics(packagePtr.Skyhook, packagePtr.Name, packagePtr.Version)
        }
        updated = true
    }
}
```

Key changes from the original:
- The "DOWNGRADE" branch (seeding new version at `StageUninstall/Complete`) is **removed**.
- The "has interrupt" case Upserts `StageUninstallInterrupt/InProgress` instead of `StageUninstall/Complete` (the ambiguous state).

- [ ] **Step 4: Build and run existing tests**

```bash
cd operator && go build ./... && go test ./internal/controller/ -run TestHandleCompletePod -v 2>&1 | tail -20
```

Expected: build succeeds. Existing `TestHandleCompletePod` tests (if any in `pod_controller_test.go`) may need adjustment or will still pass if they didn't exercise the downgrade branch.

- [ ] **Step 5: Commit**

```bash
git add operator/internal/controller/pod_controller.go operator/internal/controller/skyhook_controller_test.go
git commit -s -m "$(cat <<'EOF'
feat(controller): transition uninstall to StageUninstallInterrupt on completion

HandleCompletePod's uninstall-pod-completion branch now advances to
StageUninstallInterrupt/InProgress for packages with an interrupt,
instead of the ambiguous StageUninstall/Complete. The legacy
downgrade branch (seeding new version at StageUninstall/Complete)
is removed — downgrades are now webhook-gated and never reach this
path with a version mismatch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4: Extend `r.Interrupt` and `createInterruptPodForPackage` with stage

### Task 4: Add `stage` parameter to `createInterruptPodForPackage`

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:1916` (function), `:2282` (call site in `podMatchesPackage`)

- [ ] **Step 1: Change the function signature**

Open `operator/internal/controller/skyhook_controller.go`. Find `createInterruptPodForPackage` around line 1916. Current signature:

```go
func createInterruptPodForPackage(opts SkyhookOperatorOptions, _interrupt *v1alpha1.Interrupt,
    argEncode string, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string) *corev1.Pod {
```

Change to:

```go
func createInterruptPodForPackage(opts SkyhookOperatorOptions, _interrupt *v1alpha1.Interrupt,
    argEncode string, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string,
    stage v1alpha1.Stage) *corev1.Pod {
```

- [ ] **Step 2: Use `stage` in pod name generation**

Inside the function body, find the pod `Name:` assignment. It currently looks like:

```go
Name: generateSafeName(63, skyhook.Name, "interrupt", string(_interrupt.Type), nodeName),
```

Change to:

```go
Name: generateSafeName(63, skyhook.Name, string(stage), string(_interrupt.Type), nodeName),
```

This ensures install-cycle interrupt pods (`stage=interrupt`) and uninstall-cycle interrupt pods (`stage=uninstall-interrupt`) for the same package-version/node get distinct hashes.

- [ ] **Step 3: Update the call site in `podMatchesPackage` (line ~2282)**

Find the line:

```go
expectedPod = createInterruptPodForPackage(opts, &v1alpha1.Interrupt{}, "", _package, skyhook, "")
```

Change to (use the running pod's stage):

```go
expectedPod = createInterruptPodForPackage(opts, &v1alpha1.Interrupt{}, "", _package, skyhook, "", stage)
```

(The local `stage` variable is the parameter passed into `podMatchesPackage`.)

- [ ] **Step 4: Build to find other callers**

```bash
cd operator && go build ./... 2>&1
```

Expected: compiler errors pointing at the other call sites. Fix each by passing the appropriate stage.

- [ ] **Step 5: Update call site in `r.Interrupt` (line ~1716)**

In `r.Interrupt`, find:

```go
pod := createInterruptPodForPackage(r.opts, _interrupt, argEncode, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name)
```

The `r.Interrupt` function will gain a `stage` parameter in the next task. For now, use `v1alpha1.StageInterrupt` to preserve existing behavior:

```go
pod := createInterruptPodForPackage(r.opts, _interrupt, argEncode, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name, v1alpha1.StageInterrupt)
```

Build again to confirm clean:

```bash
cd operator && go build ./...
```

Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go
git commit -s -m "$(cat <<'EOF'
feat(controller): parameterize createInterruptPodForPackage on stage

The pod name now encodes the stage so install-cycle and uninstall-cycle
interrupt pods for the same package-version/node don't collide in
PodExists. Existing call sites pass StageInterrupt; the new
uninstall-cycle caller (added in a follow-up) will pass
StageUninstallInterrupt.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 5: Add `stage` parameter to `r.Interrupt` and propagate to both SetPackages and Upsert

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:1685` (function), `:2508`, `:2528` (call sites in `ProcessInterrupt`)

- [ ] **Step 1: Change the function signature**

Find `r.Interrupt` at line 1685. Current signature:

```go
func (r *SkyhookReconciler) Interrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode,
    _package *v1alpha1.Package, _interrupt *v1alpha1.Interrupt) error {
```

Change to:

```go
func (r *SkyhookReconciler) Interrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode,
    _package *v1alpha1.Package, _interrupt *v1alpha1.Interrupt, stage v1alpha1.Stage) error {
```

- [ ] **Step 2: Use `stage` in the `createInterruptPodForPackage` call**

Find (from Task 4, Step 5):

```go
pod := createInterruptPodForPackage(r.opts, _interrupt, argEncode, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name, v1alpha1.StageInterrupt)
```

Change the last argument to the new `stage` parameter:

```go
pod := createInterruptPodForPackage(r.opts, _interrupt, argEncode, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name, stage)
```

- [ ] **Step 3: Use `stage` in `SetPackages` (line ~1718)**

Find:

```go
if err := SetPackages(pod, skyhookNode.GetSkyhook().Skyhook, _package.Image, v1alpha1.StageInterrupt, _package); err != nil {
```

Change to:

```go
if err := SetPackages(pod, skyhookNode.GetSkyhook().Skyhook, _package.Image, stage, _package); err != nil {
```

- [ ] **Step 4: Use `stage` in the node-state `Upsert` (line ~1730)**

Find:

```go
_ = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, v1alpha1.StageInterrupt, 0, _package.ContainerSHA)
```

Change to:

```go
_ = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, stage, 0, _package.ContainerSHA)
```

- [ ] **Step 5: Update callers in `ProcessInterrupt`**

Lines 2508 and 2528 currently call:

```go
err := r.Interrupt(ctx, skyhookNode, _package, interrupt)
```

Change both to pass `StageInterrupt` for now (they're the install-cycle callers):

At line 2508 (race-recovery block, we'll extend this in Task 7):

```go
err := r.Interrupt(ctx, skyhookNode, _package, interrupt, v1alpha1.StageInterrupt)
```

At line 2528 (fire-interrupt block):

```go
err := r.Interrupt(ctx, skyhookNode, _package, interrupt, v1alpha1.StageInterrupt)
```

- [ ] **Step 6: Build and run existing tests**

```bash
cd operator && go build ./... && go test ./internal/controller/ -v 2>&1 | tail -30
```

Expected: clean build, existing tests pass (no behavior change yet for install-cycle interrupt).

- [ ] **Step 7: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go
git commit -s -m "$(cat <<'EOF'
feat(controller): parameterize r.Interrupt on stage

Adds a stage parameter to r.Interrupt and propagates it to both
SetPackages (pod annotation) and the node-state Upsert. Previously
the Upsert hardcoded StageInterrupt, which would clobber any other
caller's intent. Existing install-cycle callers pass StageInterrupt;
the uninstall-cycle caller will pass StageUninstallInterrupt in a
follow-up.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5: Extend `ProcessInterrupt` + add `ApplyPackage` guard

### Task 6: Extend ProcessInterrupt for uninstall-cycle interrupt

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:2485` (`ProcessInterrupt`)

- [ ] **Step 1: Extend race-recovery block (line ~2506)**

Find:

```go
if status != nil && (status.State == v1alpha1.StateInProgress || status.State == v1alpha1.StateErroring) && status.Stage == v1alpha1.StageInterrupt {
    err := r.Interrupt(ctx, skyhookNode, _package, interrupt, v1alpha1.StageInterrupt)
    if err != nil {
        return false, err
    }
}
```

Change to:

```go
if status != nil && (status.State == v1alpha1.StateInProgress || status.State == v1alpha1.StateErroring) &&
    (status.Stage == v1alpha1.StageInterrupt || status.Stage == v1alpha1.StageUninstallInterrupt) {
    err := r.Interrupt(ctx, skyhookNode, _package, interrupt, status.Stage)
    if err != nil {
        return false, err
    }
}
```

(Pass `status.Stage` so whichever stage the pod is stuck at gets recreated with the correct annotation.)

- [ ] **Step 2: Extend cordon + drain guard (line ~2515)**

Find:

```go
if stage == v1alpha1.StageApply {
    ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, _package)
    if err != nil {
        return false, err
    }
    if !ready {
        return false, nil
    }
}
```

Change to:

```go
if stage == v1alpha1.StageApply || stage == v1alpha1.StageUninstall {
    ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, _package)
    if err != nil {
        return false, err
    }
    if !ready {
        return false, nil
    }
}
```

(Note: per spec, the `StageUninstall` arm is currently unreachable because `NextStage(StageUninstall)` returns nil when State != Complete. Keeping it for symmetry/future-proofing. Most cordons happen via the `StageApply` arm on the very first interrupt-ful package action.)

- [ ] **Step 3: Add uninstall-cycle interrupt firing block**

After the existing `if stage == v1alpha1.StageInterrupt && runInterrupt { ... return false, nil }` block and the `if stage == v1alpha1.StageInterrupt && !runInterrupt { ... return false, nil }` block (around line 2536-2543), add a new block BEFORE the final "wait" condition:

```go
// Uninstall-cycle interrupt: HandleCompletePod set StageUninstallInterrupt/InProgress;
// fire the interrupt pod (idempotent — r.Interrupt bails if pod exists).
// Always runs — once uninstall has started, the interrupt must run to completion.
if status != nil && status.Stage == v1alpha1.StageUninstallInterrupt && status.State != v1alpha1.StateComplete {
    err := r.Interrupt(ctx, skyhookNode, _package, interrupt, v1alpha1.StageUninstallInterrupt)
    if err != nil {
        return false, err
    }
    return false, nil
}
```

- [ ] **Step 4: Extend the final wait condition (around line 2546)**

Find:

```go
if status != nil && status.Stage == v1alpha1.StageInterrupt && status.State != v1alpha1.StateComplete {
    return false, nil
}
```

Change to:

```go
if status != nil &&
    (status.Stage == v1alpha1.StageInterrupt || status.Stage == v1alpha1.StageUninstallInterrupt) &&
    status.State != v1alpha1.StateComplete {
    return false, nil
}
```

- [ ] **Step 5: Build and run existing tests**

```bash
cd operator && go build ./... && go test ./internal/controller/ -v 2>&1 | tail -20
```

Expected: clean build, existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go
git commit -s -m "$(cat <<'EOF'
feat(controller): extend ProcessInterrupt for uninstall-cycle

Adds stage-aware handling so ProcessInterrupt recognizes
StageUninstallInterrupt for race recovery, firing, and waiting.
Also extends the cordon+drain guard to cover the uninstall case.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 7: Add ApplyPackage guard for StageUninstallInterrupt

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:2574` (`ApplyPackage`)

- [ ] **Step 1: Extend the stage-resolution switch**

Find the switch around line 2588:

```go
if packageStatus, found := skyhookNode.PackageStatus(_package.GetUniqueName()); found {
    switch packageStatus.Stage {
    case v1alpha1.StageConfig, v1alpha1.StageUpgrade, v1alpha1.StageUninstall:
        stage = packageStatus.Stage
    }
}
```

Change to:

```go
if packageStatus, found := skyhookNode.PackageStatus(_package.GetUniqueName()); found {
    switch packageStatus.Stage {
    case v1alpha1.StageConfig, v1alpha1.StageUpgrade, v1alpha1.StageUninstall,
        v1alpha1.StageUninstallInterrupt:
        stage = packageStatus.Stage
    }
}
```

- [ ] **Step 2: Add an early-return for StageUninstallInterrupt**

Immediately after the `nextStage := skyhookNode.NextStage(_package)` block at line 2608-2611 (where `stage` gets its final value), add:

```go
// Uninstall-cycle interrupt pods are controller-created by ProcessInterrupt via
// r.Interrupt (type-based pod, not a stage-script pod). ApplyPackage has no
// work to do here.
if stage == v1alpha1.StageUninstallInterrupt {
    return nil
}
```

This should come **before** the `PodExists` check so we bail immediately.

- [ ] **Step 3: Build and run tests**

```bash
cd operator && go build ./... && go test ./internal/controller/ -v 2>&1 | tail -20
```

Expected: clean build, existing tests pass.

- [ ] **Step 4: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go
git commit -s -m "$(cat <<'EOF'
feat(controller): ApplyPackage bails for StageUninstallInterrupt

Uninstall-cycle interrupt pods are created by ProcessInterrupt via
r.Interrupt, not by createPodFromPackage. ApplyPackage now recognizes
the stage in its stage-resolution switch and returns early before
pod creation, ensuring the controller doesn't accidentally create a
stage-script pod for an interrupt stage.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 6: Broaden `IsUninstallInProgress` and related helpers

### Task 8: Rename `IsUninstallInProgress` to `IsUninstallCycleInProgress`, broaden it

**Files:**
- Modify: `operator/api/v1alpha1/skyhook_types.go:479-489`
- Modify callers: `operator/internal/controller/skyhook_controller.go:655, 916, 943, 994, 1062, 1403`
- Modify tests: `operator/api/v1alpha1/skyhook_types_test.go:638-653`

- [ ] **Step 1: Rewrite the helper function**

Open `operator/api/v1alpha1/skyhook_types.go`. Find `IsUninstallInProgress` at line 479:

```go
// IsUninstallInProgress returns true if the named package is at StageUninstall
// on this node (in_progress or erroring). This is the node-annotation-level
// answer to "has uninstall started?" — distinct from Package.IsUninstalling()
// which only answers "is uninstall requested in the spec?"
func (ns *NodeState) IsUninstallInProgress(uniqueName string) bool {
    if *ns == nil {
        return false
    }
    status, ok := (*ns)[uniqueName]
    return ok && status.Stage == StageUninstall
}
```

Replace with:

```go
// IsUninstallCycleInProgress returns true if the named package is anywhere in
// the uninstall cycle on this node — either the uninstall pod phase
// (StageUninstall) or the post-uninstall interrupt phase
// (StageUninstallInterrupt). This is the node-annotation-level answer to
// "has uninstall started?" — distinct from Package.IsUninstalling() which only
// answers "is uninstall requested in the spec?"
func (ns *NodeState) IsUninstallCycleInProgress(uniqueName string) bool {
    if *ns == nil {
        return false
    }
    status, ok := (*ns)[uniqueName]
    if !ok {
        return false
    }
    return status.Stage == StageUninstall || status.Stage == StageUninstallInterrupt
}
```

- [ ] **Step 2: Update the test in skyhook_types_test.go**

Find around line 638:

```go
It("Should detect IsUninstallInProgress from node state", func() {
    ns := NodeState{
        "pkg|1.0.0": PackageStatus{
            Name: "pkg", Version: "1.0.0", Stage: StageUninstall, State: StateInProgress,
        },
        "other|2.0.0": PackageStatus{
            Name: "other", Version: "2.0.0", Stage: StageConfig, State: StateComplete,
        },
    }
    Expect(ns.IsUninstallInProgress("pkg|1.0.0")).To(BeTrue())
    Expect(ns.IsUninstallInProgress("other|2.0.0")).To(BeFalse())
    Expect(ns.IsUninstallInProgress("missing|3.0.0")).To(BeFalse())

    var nilState NodeState
    Expect(nilState.IsUninstallInProgress("pkg|1.0.0")).To(BeFalse())
})
```

Replace with:

```go
It("Should detect IsUninstallCycleInProgress from node state", func() {
    ns := NodeState{
        "pkg|1.0.0": PackageStatus{
            Name: "pkg", Version: "1.0.0", Stage: StageUninstall, State: StateInProgress,
        },
        "other|2.0.0": PackageStatus{
            Name: "other", Version: "2.0.0", Stage: StageConfig, State: StateComplete,
        },
        "interrupting|1.5.0": PackageStatus{
            Name: "interrupting", Version: "1.5.0", Stage: StageUninstallInterrupt, State: StateInProgress,
        },
    }
    Expect(ns.IsUninstallCycleInProgress("pkg|1.0.0")).To(BeTrue())
    Expect(ns.IsUninstallCycleInProgress("other|2.0.0")).To(BeFalse())
    Expect(ns.IsUninstallCycleInProgress("interrupting|1.5.0")).To(BeTrue())
    Expect(ns.IsUninstallCycleInProgress("missing|3.0.0")).To(BeFalse())

    var nilState NodeState
    Expect(nilState.IsUninstallCycleInProgress("pkg|1.0.0")).To(BeFalse())
})
```

- [ ] **Step 3: Update all callers — search and replace**

Run from repo root:

```bash
grep -rln 'IsUninstallInProgress' operator/ | xargs sed -i.bak 's/IsUninstallInProgress/IsUninstallCycleInProgress/g'
find operator -name '*.bak' -delete
```

Expected: every `.IsUninstallInProgress(` call is now `.IsUninstallCycleInProgress(`.

- [ ] **Step 4: Build and run tests**

```bash
cd operator && go build ./... && go test ./... 2>&1 | tail -20
```

Expected: clean build, all tests pass.

- [ ] **Step 5: Add sibling guard to IsComplete**

Open `operator/api/v1alpha1/skyhook_types.go`. Find `IsComplete` around line 512. Locate the block:

```go
if len(activePackages) <= len(ns.GetComplete(activePackages, interrupt, config)) {
    // If a current spec package is still at StageUninstall then the node isn't complete.
    for _, pkg := range activePackages {
        if status, ok := (*ns)[pkg.GetUniqueName()]; ok && status.Stage == StageUninstall {
            return false
        }
    }
```

Extend the check:

```go
if len(activePackages) <= len(ns.GetComplete(activePackages, interrupt, config)) {
    // If a current spec package is still in the uninstall cycle the node isn't complete.
    for _, pkg := range activePackages {
        if status, ok := (*ns)[pkg.GetUniqueName()]; ok &&
            (status.Stage == StageUninstall || status.Stage == StageUninstallInterrupt) {
            return false
        }
    }
```

- [ ] **Step 6: Build and run tests again**

```bash
cd operator && go build ./... && go test ./... 2>&1 | tail -20
```

Expected: clean, all pass.

- [ ] **Step 7: Commit**

```bash
git add -A operator/
git commit -s -m "$(cat <<'EOF'
refactor(api): broaden IsUninstallInProgress to cover uninstall cycle

Renamed to IsUninstallCycleInProgress and extended to return true for
both StageUninstall and StageUninstallInterrupt. All callers updated.
Also extends NodeState.IsComplete's "cannot be complete if still
uninstalling" guard to cover the new stage.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 7: Rewrite `HandleUninstallRequests`

### Task 9: Rewrite the HandleUninstallRequests early-continue gate and switch

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:778-852`
- Test: `operator/internal/controller/skyhook_controller_test.go`

- [ ] **Step 1: Write the failing regression test for bug #1**

Append to `operator/internal/controller/skyhook_controller_test.go` (in the `TestHandleUninstallRequests` function, as a new `t.Run`):

```go
t.Run("should trigger uninstall for PostInterrupt/Complete package (bug #1 regression)", func(t *testing.T) {
    g := NewWithT(t)

    skyhook := &v1alpha1.Skyhook{
        Spec: v1alpha1.SkyhookSpec{
            Packages: v1alpha1.Packages{
                "my-pkg": v1alpha1.Package{
                    PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
                    Image:      "my-image",
                    Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
                    Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
                },
            },
        },
    }

    node := wrapperMock.NewMockSkyhookNode(t)
    node.EXPECT().State().Return(v1alpha1.NodeState{
        "my-pkg|1.0.0": v1alpha1.PackageStatus{
            Name: "my-pkg", Version: "1.0.0", Image: "my-image",
            Stage: v1alpha1.StagePostInterrupt, State: v1alpha1.StateComplete,
        },
    }, nil)
    // Expect uninstall trigger, NOT RemoveState
    node.EXPECT().Upsert(
        v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
        v1alpha1.StateInProgress, v1alpha1.StageUninstall, int32(0), "",
    ).Return(nil)
    node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

    sn := &skyhookNodes{
        skyhook: wrapper.NewSkyhookWrapper(skyhook),
        nodes:   []wrapper.SkyhookNode{node},
    }

    result, err := HandleUninstallRequests(sn)
    g.Expect(err).To(BeNil())
    g.Expect(result).To(HaveLen(1))
    g.Expect(result[0].Name).To(Equal("my-pkg"))
})

t.Run("should not trigger uninstall for StageInterrupt/InProgress (install mid-interrupt)", func(t *testing.T) {
    g := NewWithT(t)

    skyhook := &v1alpha1.Skyhook{
        Spec: v1alpha1.SkyhookSpec{
            Packages: v1alpha1.Packages{
                "my-pkg": v1alpha1.Package{
                    PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
                    Image:      "my-image",
                    Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
                    Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
                },
            },
        },
    }

    node := wrapperMock.NewMockSkyhookNode(t)
    node.EXPECT().State().Return(v1alpha1.NodeState{
        "my-pkg|1.0.0": v1alpha1.PackageStatus{
            Name: "my-pkg", Version: "1.0.0", Image: "my-image",
            Stage: v1alpha1.StageInterrupt, State: v1alpha1.StateInProgress,
        },
    }, nil)
    // No Upsert, no RemoveState — must wait for install interrupt to finish

    sn := &skyhookNodes{
        skyhook: wrapper.NewSkyhookWrapper(skyhook),
        nodes:   []wrapper.SkyhookNode{node},
    }

    result, err := HandleUninstallRequests(sn)
    g.Expect(err).To(BeNil())
    g.Expect(result).To(BeEmpty())
})

t.Run("should cleanup StageUninstallInterrupt/Complete", func(t *testing.T) {
    g := NewWithT(t)

    skyhook := &v1alpha1.Skyhook{
        Spec: v1alpha1.SkyhookSpec{
            Packages: v1alpha1.Packages{
                "my-pkg": v1alpha1.Package{
                    PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
                    Image:      "my-image",
                    Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
                    Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
                },
            },
        },
    }

    node := wrapperMock.NewMockSkyhookNode(t)
    node.EXPECT().State().Return(v1alpha1.NodeState{
        "my-pkg|1.0.0": v1alpha1.PackageStatus{
            Name: "my-pkg", Version: "1.0.0", Image: "my-image",
            Stage: v1alpha1.StageUninstallInterrupt, State: v1alpha1.StateComplete,
        },
    }, nil)
    node.EXPECT().RemoveState(
        v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
    ).Return(nil)
    node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

    sn := &skyhookNodes{
        skyhook: wrapper.NewSkyhookWrapper(skyhook),
        nodes:   []wrapper.SkyhookNode{node},
    }

    result, err := HandleUninstallRequests(sn)
    g.Expect(err).To(BeNil())
    g.Expect(result).To(BeEmpty())
})

t.Run("should cleanup StageUninstallInterrupt/Complete even when apply=false (cancel-strand)", func(t *testing.T) {
    g := NewWithT(t)

    // User flipped apply back to false AFTER interrupt completed.
    // Must still RemoveState — otherwise the node state is stranded.
    skyhook := &v1alpha1.Skyhook{
        Spec: v1alpha1.SkyhookSpec{
            Packages: v1alpha1.Packages{
                "my-pkg": v1alpha1.Package{
                    PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
                    Image:      "my-image",
                    Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
                    Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false}, // cancelled
                },
            },
        },
    }

    node := wrapperMock.NewMockSkyhookNode(t)
    node.EXPECT().State().Return(v1alpha1.NodeState{
        "my-pkg|1.0.0": v1alpha1.PackageStatus{
            Name: "my-pkg", Version: "1.0.0", Image: "my-image",
            Stage: v1alpha1.StageUninstallInterrupt, State: v1alpha1.StateComplete,
        },
    }, nil)
    node.EXPECT().RemoveState(
        v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
    ).Return(nil)
    node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

    sn := &skyhookNodes{
        skyhook: wrapper.NewSkyhookWrapper(skyhook),
        nodes:   []wrapper.SkyhookNode{node},
    }

    result, err := HandleUninstallRequests(sn)
    g.Expect(err).To(BeNil())
    g.Expect(result).To(BeEmpty())
})
```

- [ ] **Step 2: Run tests to see failures**

```bash
cd operator && go test ./internal/controller/ -run TestHandleUninstallRequests -v 2>&1 | tail -30
```

Expected: the new subtests FAIL because current code calls `RemoveState` for `StagePostInterrupt/Complete` (bug #1) and short-circuits on `!needsUninstall`.

- [ ] **Step 3: Rewrite `HandleUninstallRequests`**

Open `operator/internal/controller/skyhook_controller.go`. Find the function at line 778. Replace the entire function body (from the opening `{` through the final `}` at line 852) with:

```go
func HandleUninstallRequests(skyhook SkyhookNodes) ([]*v1alpha1.Package, error) {
	toUninstall := make([]*v1alpha1.Package, 0)
	beingDeleted := !skyhook.GetSkyhook().DeletionTimestamp.IsZero()
	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return nil, err
		}
		for name, pkg := range skyhook.GetSkyhook().Spec.Packages {
			status, exists := nodeState[pkg.GetUniqueName()]
			if !exists {
				continue // already uninstalled on this node (absent = done)
			}

			needsUninstall := pkg.IsUninstalling() || (beingDeleted && pkg.UninstallEnabled())
			inUninstallCycle := status.Stage == v1alpha1.StageUninstall ||
				status.Stage == v1alpha1.StageUninstallInterrupt
			if !needsUninstall && !inUninstallCycle {
				continue
			}

			// Handle packages progressing through the uninstall cycle.
			switch status.Stage {
			case v1alpha1.StageUninstall:
				// All states re-add so ApplyPackage / ProcessInterrupt sees the
				// package. StageUninstall/Complete is not used under the new rules
				// (HandleCompletePod transitions directly to either
				// StageUninstallInterrupt/InProgress or RemoveState), but we re-add
				// it defensively.
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

			// Install-cycle stages (Apply, Config, Interrupt, PostInterrupt, Upgrade):
			// only kick off uninstall from a terminal Complete state. Don't interrupt
			// an install mid-flight — wait for it.
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
		}
	}
	return toUninstall, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd operator && go test ./internal/controller/ -run TestHandleUninstallRequests -v 2>&1 | tail -40
```

Expected: all subtests PASS, including the new regression and cleanup cases.

- [ ] **Step 5: Run full test suite**

```bash
cd operator && go test ./... 2>&1 | tail -20
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go operator/internal/controller/skyhook_controller_test.go
git commit -s -m "$(cat <<'EOF'
fix(controller): HandleUninstallRequests — terminal state fixes

Rewrites the switch to handle StageUninstall and StageUninstallInterrupt
unambiguously. Removes the buggy StageInterrupt/StagePostInterrupt
branches which caused RemoveState to fire on a terminal install state.

Adds an inUninstallCycle gate so the function still drains
StageUninstallInterrupt/Complete even when the user has flipped
apply=false mid-cycle — otherwise the node state would be stranded.

Fixes the regression test case where an installed interrupt-ful package
has apply=true toggled: uninstall now actually runs.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 8: Remove legacy downgrade path

### Task 10: Remove downgrade branch from HandleVersionChange

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:1044-1150` (`HandleVersionChange`)
- Test: `operator/internal/controller/skyhook_controller_test.go`

- [ ] **Step 1: Write the failing test**

Append to `operator/internal/controller/skyhook_controller_test.go`:

```go
func TestHandleVersionChange_DowngradeIsNoOp(t *testing.T) {
    t.Run("downgrade with enabled=false leaves old state in node state", func(t *testing.T) {
        g := NewWithT(t)

        skyhook := &v1alpha1.Skyhook{
            Spec: v1alpha1.SkyhookSpec{
                Packages: v1alpha1.Packages{
                    "my-pkg": v1alpha1.Package{
                        PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
                        Image:      "my-image",
                        Uninstall:  &v1alpha1.Uninstall{Enabled: false, Apply: false},
                    },
                },
            },
        }

        node := wrapperMock.NewMockSkyhookNode(t)
        node.EXPECT().State().Return(v1alpha1.NodeState{
            "my-pkg|2.0.0": v1alpha1.PackageStatus{
                Name: "my-pkg", Version: "2.0.0", Image: "my-image",
                Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
            },
        }, nil)
        // No Upsert, no RemoveState for old version — old state is preserved.

        sn := &skyhookNodes{
            skyhook: wrapper.NewSkyhookWrapper(skyhook),
            nodes:   []wrapper.SkyhookNode{node},
        }

        result, err := HandleVersionChange(sn)
        g.Expect(err).To(BeNil())
        g.Expect(result).To(BeEmpty())
    })

    t.Run("upgrade still triggers StageUpgrade", func(t *testing.T) {
        g := NewWithT(t)

        skyhook := &v1alpha1.Skyhook{
            Spec: v1alpha1.SkyhookSpec{
                Packages: v1alpha1.Packages{
                    "my-pkg": v1alpha1.Package{
                        PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "2.0.0"},
                        Image:      "my-image",
                    },
                },
            },
        }

        node := wrapperMock.NewMockSkyhookNode(t)
        node.EXPECT().State().Return(v1alpha1.NodeState{
            "my-pkg|1.0.0": v1alpha1.PackageStatus{
                Name: "my-pkg", Version: "1.0.0", Image: "my-image",
                Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
            },
        }, nil)
        node.EXPECT().PackageStatus("my-pkg|2.0.0").Return(nil, false)
        node.EXPECT().Upsert(
            v1alpha1.PackageRef{Name: "my-pkg", Version: "2.0.0"}, "my-image",
            v1alpha1.StateInProgress, v1alpha1.StageUpgrade, int32(0), "",
        ).Return(nil)

        sn := &skyhookNodes{
            skyhook: wrapper.NewSkyhookWrapper(skyhook),
            nodes:   []wrapper.SkyhookNode{node},
        }

        _, err := HandleVersionChange(sn)
        g.Expect(err).To(BeNil())
    })
}
```

- [ ] **Step 2: Run tests to see failures**

```bash
cd operator && go test ./internal/controller/ -run TestHandleVersionChange_DowngradeIsNoOp -v 2>&1 | tail -30
```

Expected: the "downgrade is no-op" test FAILS because current code Upserts the old version to `StageUninstall/InProgress` (plus the synthetic-package block fires).

- [ ] **Step 3: Rewrite the downgrade branch**

Find the block in `HandleVersionChange` around line 1093-1148. The current structure:

```go
} else if exists && _package.Version != packageStatus.Version {
    versionChangeDetected = true
    comparison := version.Compare(_package.Version, packageStatus.Version)
    if comparison == -2 {
        return nil, errors.New("error comparing package versions: ...")
    }

    if comparison == 1 {
        // upgrade (keep)
        ...
    } else if comparison == -1 && packageStatus.Stage != v1alpha1.StageUninstall {
        // DOWNGRADE — remove this entire block
        err := node.Upsert(packageStatusRef, packageStatus.Image, v1alpha1.StateInProgress, v1alpha1.StageUninstall, 0, "")
        ...
        err = node.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateSkipped, v1alpha1.StageUninstall, 0, _package.ContainerSHA)
        ...
    }
}

// only need to create a feaux package for uninstall ...  <-- remove this block too
newPackageStatus, found := node.PackageStatus(packageStatusRef.GetUniqueName())
if !upgrade && found && newPackageStatus.Stage == v1alpha1.StageUninstall && newPackageStatus.State == v1alpha1.StateInProgress {
    // create fake package with the info we can salvage from the node state
    newPackage := &v1alpha1.Package{
        PackageRef: packageStatusRef,
        Image:      packageStatus.Image,
    }
    ...
    toUninstall = append(toUninstall, newPackage)
}
```

Replace the `} else if exists && _package.Version != packageStatus.Version {` block and the following synthetic-package block with:

```go
} else if exists && _package.Version != packageStatus.Version {
    versionChangeDetected = true
    comparison := version.Compare(_package.Version, packageStatus.Version)
    if comparison == -2 {
        return nil, errors.New("error comparing package versions: invalid version string provided enabling webhooks validates versions before being applied")
    }

    if comparison == 1 {
        // Upgrade path (unchanged).
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
    // Downgrade: no-op. The webhook rejects downgrades of enabled=true packages
    // unless the package is already fully uninstalled, and for enabled=false
    // packages the old-version state stays in node state per D2 semantics
    // (non-absent = "not cleanly uninstalled, just superseded").
}
```

Also **remove entirely** the synthetic-uninstall-package block that used to follow:

```go
// DELETE THIS ENTIRE BLOCK:
newPackageStatus, found := node.PackageStatus(packageStatusRef.GetUniqueName())
if !upgrade && found && newPackageStatus.Stage == v1alpha1.StageUninstall && newPackageStatus.State == v1alpha1.StateInProgress {
    newPackage := &v1alpha1.Package{
        PackageRef: packageStatusRef,
        Image:      packageStatus.Image,
    }
    found := false
    for _, uninstallPackage := range toUninstall {
        if reflect.DeepEqual(uninstallPackage, newPackage) {
            found = true
        }
    }
    if !found {
        toUninstall = append(toUninstall, newPackage)
    }
}
```

- [ ] **Step 4: Run tests**

```bash
cd operator && go test ./internal/controller/ -run TestHandleVersionChange -v 2>&1 | tail -30
```

Expected: new tests PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd operator && go test ./... 2>&1 | tail -30
```

Expected: all PASS. **Note:** some existing tests may have exercised the legacy downgrade path and will now fail. Audit any failures — if a test expected an old-version uninstall pod from a pure downgrade (no `apply=true`), that test is no longer valid under the new rules.

- [ ] **Step 6: Fix any broken pre-existing tests (if any)**

If tests in `TestHandleVersionChange` or similar fail, they were exercising the legacy behavior. Review each:
- If they tested downgrade triggering an uninstall pod: delete or rewrite to match new semantics.
- If they tested the feaux/synthetic package: delete.

- [ ] **Step 7: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go operator/internal/controller/skyhook_controller_test.go
git commit -s -m "$(cat <<'EOF'
refactor(controller): remove legacy downgrade-triggers-uninstall path

HandleVersionChange no longer auto-uninstalls the old version on
downgrade. Webhook gates downgrades of enabled=true packages (must be
fully uninstalled first); for enabled=false packages, the old version
entry stays in node state per D2 semantics. The synthetic-package
block for feaux uninstall pods is also removed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 9: Webhook changes

### Task 11: Rewrite downgrade check with semver.IsValid guard

**Files:**
- Modify: `operator/api/v1alpha1/skyhook_webhook.go:142-156`
- Test: `operator/api/v1alpha1/skyhook_types_test.go` (existing tests around line 745+)

- [ ] **Step 1: Write failing tests**

Append to `operator/api/v1alpha1/skyhook_types_test.go` inside the `Describe("Skyhook Types", ...)` block:

```go
It("Should reject downgrade when old apply=false", func() {
    oldSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v2.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    newSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v1.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    webhook := &SkyhookWebhook{}
    _, err := webhook.ValidateUpdate(ctx, oldSkyhook, newSkyhook)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("set uninstall.apply=true first"))
})

It("Should reject downgrade when node state still contains package", func() {
    oldSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v2.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: true},
                },
            },
        },
        Status: SkyhookStatus{
            NodeState: map[string]NodeState{
                "node-1": {
                    "my-pkg|v2.0.0": PackageStatus{
                        Name: "my-pkg", Version: "v2.0.0",
                        Stage: StageUninstall, State: StateInProgress,
                    },
                },
            },
        },
    }
    newSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v1.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: true},
                },
            },
        },
    }
    webhook := &SkyhookWebhook{}
    _, err := webhook.ValidateUpdate(ctx, oldSkyhook, newSkyhook)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("uninstall has not yet completed"))
})

It("Should allow downgrade when old apply=true AND package absent from all nodes", func() {
    oldSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v2.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: true},
                },
            },
        },
        Status: SkyhookStatus{
            NodeState: map[string]NodeState{
                "node-1": {}, // package absent = fully uninstalled per D2
            },
        },
    }
    newSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v1.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    webhook := &SkyhookWebhook{}
    _, err := webhook.ValidateUpdate(ctx, oldSkyhook, newSkyhook)
    Expect(err).ToNot(HaveOccurred())
})

It("Should allow upgrade regardless of apply setting", func() {
    oldSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v1.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    newSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "v2.0.0"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    webhook := &SkyhookWebhook{}
    _, err := webhook.ValidateUpdate(ctx, oldSkyhook, newSkyhook)
    Expect(err).ToNot(HaveOccurred())
})

It("Should skip downgrade check for invalid semver (defers to Validate)", func() {
    oldSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "not-a-semver"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    newSkyhook := &Skyhook{
        ObjectMeta: metav1.ObjectMeta{Name: "test"},
        Spec: SkyhookSpec{
            Packages: Packages{
                "my-pkg": Package{
                    PackageRef: PackageRef{Name: "my-pkg", Version: "also-invalid"},
                    Image:      "my-image",
                    Uninstall:  &Uninstall{Enabled: true, Apply: false},
                },
            },
        },
    }
    webhook := &SkyhookWebhook{}
    // Either pass (skipped) or fail on the separate Validate() check — but NOT
    // the "set uninstall.apply=true first" downgrade message.
    _, err := webhook.ValidateUpdate(ctx, oldSkyhook, newSkyhook)
    if err != nil {
        Expect(err.Error()).ToNot(ContainSubstring("set uninstall.apply=true first"))
        Expect(err.Error()).ToNot(ContainSubstring("uninstall has not yet completed"))
    }
})
```

The old tests from commit `1ff035d5` (around lines 748-807 in `skyhook_types_test.go`) use `ContainSubstring("uninstall.apply=true")` and `ContainSubstring("downgrad")` — those substrings appear in the new error messages too, so the old tests likely still pass. After running tests in Step 4, delete any that fail; keep any that still pass.

- [ ] **Step 2: Run tests to see failures**

```bash
cd operator && go test ./api/v1alpha1/ -v 2>&1 | tail -30
```

Expected: new tests FAIL (current code uses different rejection logic).

- [ ] **Step 3: Rewrite the webhook downgrade check**

Open `operator/api/v1alpha1/skyhook_webhook.go`. Find the block at line 142-156:

```go
// Reject downgrade of enabled packages without uninstall.apply=true.
// With explicit uninstall, downgrades require the user to uninstall first.
for name, oldPkg := range oldSkyhook.Spec.Packages {
    newPkg, exists := skyhook.Spec.Packages[name]
    if !exists || !oldPkg.UninstallEnabled() {
        continue
    }
    if newPkg.Version != oldPkg.Version && !newPkg.IsUninstalling() {
        if semver.Compare(newPkg.Version, oldPkg.Version) == -1 {
            return nil, fmt.Errorf(
                "package %q has uninstall.enabled=true; set uninstall.apply=true "+
                    "to uninstall before downgrading from %s to %s", name, oldPkg.Version, newPkg.Version)
        }
    }
}
```

Replace with:

```go
// Reject version downgrade unless the package has already been explicitly
// uninstalled on all nodes. The user must have flipped uninstall.apply=true
// on the old spec AND waited for the uninstall to complete (package absent
// from every tracked node's state) before changing the version.
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

- [ ] **Step 4: Run tests**

```bash
cd operator && go test ./api/v1alpha1/ -v 2>&1 | tail -30
```

Expected: all new tests PASS. Old tests that referenced the previous error message will fail — delete them.

- [ ] **Step 5: Commit**

```bash
git add operator/api/v1alpha1/skyhook_webhook.go operator/api/v1alpha1/skyhook_types_test.go
git commit -s -m "$(cat <<'EOF'
feat(webhook): strict downgrade rule requires uninstall complete

Replaces the previous downgrade-rejection (which only blocked
enabled=true + apply=false). New rule: any version downgrade is
rejected unless the OLD spec already had uninstall.apply=true AND
the package is absent from all tracked nodes (uninstall complete
per D2 semantics). Adds a semver.IsValid guard before Compare so
invalid versions don't silently pass the check.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 10: Chainsaw test updates and new tests

### Task 12: Update explicit-uninstall chainsaw test

**Files:**
- Modify: `k8s-tests/chainsaw/skyhook/explicit-uninstall/chainsaw-test.yaml:113-134`

- [ ] **Step 1: Change the mid-flight stage assertion**

Open `k8s-tests/chainsaw/skyhook/explicit-uninstall/chainsaw-test.yaml`. Find the block around line 113:

```yaml
- assert:
    ## after uninstall pod completes, node should be cordoned for interrupt (reboot)
    resource:
      apiVersion: v1
      kind: Node
      metadata:
        labels:
          skyhook.nvidia.com/test-node: skyhooke2e
        annotations:
          ("skyhook.nvidia.com/nodeState_explicit-uninstall" && parse_json("skyhook.nvidia.com/nodeState_explicit-uninstall")):
            {
              "mypkg|2.1.4": {
                  "name": "mypkg",
                  "version": "2.1.4",
                  "image": "ghcr.io/nvidia/skyhook/agentless",
                  "stage": "interrupt",
                  "state": "in_progress"
              }
            }
      spec:
        taints:
        - effect: NoSchedule
          key: node.kubernetes.io/unschedulable
```

Change `"stage": "interrupt"` to `"stage": "uninstall-interrupt"`:

```yaml
- assert:
    ## after uninstall pod completes, node should be cordoned for interrupt (reboot)
    resource:
      apiVersion: v1
      kind: Node
      metadata:
        labels:
          skyhook.nvidia.com/test-node: skyhooke2e
        annotations:
          ("skyhook.nvidia.com/nodeState_explicit-uninstall" && parse_json("skyhook.nvidia.com/nodeState_explicit-uninstall")):
            {
              "mypkg|2.1.4": {
                  "name": "mypkg",
                  "version": "2.1.4",
                  "image": "ghcr.io/nvidia/skyhook/agentless",
                  "stage": "uninstall-interrupt",
                  "state": "in_progress"
              }
            }
      spec:
        taints:
        - effect: NoSchedule
          key: node.kubernetes.io/unschedulable
```

- [ ] **Step 2: Commit**

```bash
git add k8s-tests/chainsaw/skyhook/explicit-uninstall/chainsaw-test.yaml
git commit -s -m "$(cat <<'EOF'
test(chainsaw): update explicit-uninstall to assert uninstall-interrupt stage

The mid-flight stage is now uninstall-interrupt (distinct from the
install-cycle interrupt) after the controller refactor.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 13: Update helm webhook test for new rejection message

**Files:**
- Modify: `k8s-tests/chainsaw/helm/helm-webhook-test/chainsaw-test.yaml`

- [ ] **Step 1: Update expected error message**

Open `k8s-tests/chainsaw/helm/helm-webhook-test/chainsaw-test.yaml`. Find the test block that applies `update-downgrade-enabled-pkg.yaml` and greps for `uninstall.apply=true`. Change the grep to match the new error text:

Find:

```yaml
if ! grep -q "uninstall.apply=true" err.txt; then
```

Change to:

```yaml
if ! grep -q "set uninstall.apply=true first" err.txt; then
```

- [ ] **Step 2: Commit**

```bash
git add k8s-tests/chainsaw/helm/helm-webhook-test/chainsaw-test.yaml
git commit -s -m "$(cat <<'EOF'
test(chainsaw): update helm-webhook downgrade-reject message

New webhook error text is "set uninstall.apply=true first" rather
than just "uninstall.apply=true".

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 14: Add downgrade-after-uninstall chainsaw test

**Files:**
- Create: `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/chainsaw-test.yaml`
- Create: `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/skyhook.yaml`
- Create: `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/update-trigger-uninstall.yaml`
- Create: `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/update-downgrade.yaml`

- [ ] **Step 1: Create the initial skyhook yaml**

Create `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/skyhook.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: downgrade-after-uninstall
spec:
  nodeSelectors:
    matchLabels:
      skyhook.nvidia.com/test-node: skyhooke2e
  interruptionBudget:
    count: 1
  packages:
    mypkg:
      version: "2.1.4"
      image: ghcr.io/nvidia/skyhook/agentless
      uninstall:
        enabled: true
        apply: false
```

- [ ] **Step 2: Create trigger-uninstall update**

Create `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/update-trigger-uninstall.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: downgrade-after-uninstall
spec:
  nodeSelectors:
    matchLabels:
      skyhook.nvidia.com/test-node: skyhooke2e
  interruptionBudget:
    count: 1
  packages:
    mypkg:
      version: "2.1.4"
      image: ghcr.io/nvidia/skyhook/agentless
      uninstall:
        enabled: true
        apply: true
```

- [ ] **Step 3: Create the downgrade update**

Create `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/update-downgrade.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: downgrade-after-uninstall
spec:
  nodeSelectors:
    matchLabels:
      skyhook.nvidia.com/test-node: skyhooke2e
  interruptionBudget:
    count: 1
  packages:
    mypkg:
      version: "1.2.3"
      image: ghcr.io/nvidia/skyhook/agentless
      uninstall:
        enabled: true
        apply: false
```

- [ ] **Step 4: Create the chainsaw test**

Create `k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/chainsaw-test.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# yaml-language-server: $schema=https://raw.githubusercontent.com/kyverno/chainsaw/main/.schemas/json/test-chainsaw-v1alpha1.json
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: downgrade-after-uninstall
spec:
  timeouts:
    assert: 240s
  catch:
    - get:
        apiVersion: v1
        kind: Node
        selector: skyhook.nvidia.com/test-node=skyhooke2e
        format: yaml
    - get:
        apiVersion: skyhook.nvidia.com/v1alpha1
        kind: Skyhook
        name: downgrade-after-uninstall
        format: yaml
  steps:
  - name: install-v2
    description: Install the package at v2.1.4 and wait for complete
    try:
    - script:
        content: |
          ../skyhook-cli reset downgrade-after-uninstall --confirm 2>/dev/null || true
    - create:
        file: skyhook.yaml
    - assert:
        resource:
          apiVersion: skyhook.nvidia.com/v1alpha1
          kind: Skyhook
          metadata:
            name: downgrade-after-uninstall
          status:
            status: complete

  - name: uninstall
    description: Flip apply=true, wait for package absent from node state
    try:
    - update:
        file: update-trigger-uninstall.yaml
    - assert:
        resource:
          apiVersion: v1
          kind: Node
          metadata:
            labels:
              skyhook.nvidia.com/test-node: skyhooke2e
            annotations:
              skyhook.nvidia.com/nodeState_downgrade-after-uninstall: '{}'

  - name: downgrade
    description: Change version to 1.2.3 — should be accepted and install fresh
    try:
    - update:
        file: update-downgrade.yaml
    - assert:
        resource:
          apiVersion: v1
          kind: Node
          metadata:
            labels:
              skyhook.nvidia.com/test-node: skyhooke2e
            annotations:
              ("skyhook.nvidia.com/nodeState_downgrade-after-uninstall" && parse_json("skyhook.nvidia.com/nodeState_downgrade-after-uninstall")):
                {
                  "mypkg|1.2.3": {
                      "name": "mypkg",
                      "version": "1.2.3",
                      "image": "ghcr.io/nvidia/skyhook/agentless",
                      "stage": "config",
                      "state": "complete"
                  }
                }
    - assert:
        resource:
          apiVersion: skyhook.nvidia.com/v1alpha1
          kind: Skyhook
          metadata:
            name: downgrade-after-uninstall
          status:
            status: complete
```

- [ ] **Step 5: Commit**

```bash
git add k8s-tests/chainsaw/skyhook/downgrade-after-uninstall/
git commit -s -m "$(cat <<'EOF'
test(chainsaw): add downgrade-after-uninstall test

Exercises the happy path for downgrading an enabled=true package:
install v2.1.4, flip apply=true and wait for package absent, then
downgrade to v1.2.3 with apply=false. Asserts the webhook accepts
the downgrade and the new version installs cleanly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 15: Add downgrade-enabled-false-preserves-state chainsaw test

**Files:**
- Create: `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/chainsaw-test.yaml`
- Create: `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/skyhook.yaml`
- Create: `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/update-downgrade.yaml`

- [ ] **Step 1: Create skyhook.yaml (install at 2.0.0 with enabled=false)**

Create `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/skyhook.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: preserve-downgrade-state
spec:
  nodeSelectors:
    matchLabels:
      skyhook.nvidia.com/test-node: skyhooke2e
  interruptionBudget:
    count: 1
  packages:
    mypkg:
      version: "2.1.4"
      image: ghcr.io/nvidia/skyhook/agentless
      uninstall:
        enabled: false
        apply: false
```

- [ ] **Step 2: Create update-downgrade.yaml**

Create `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/update-downgrade.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: preserve-downgrade-state
spec:
  nodeSelectors:
    matchLabels:
      skyhook.nvidia.com/test-node: skyhooke2e
  interruptionBudget:
    count: 1
  packages:
    mypkg:
      version: "1.2.3"
      image: ghcr.io/nvidia/skyhook/agentless
      uninstall:
        enabled: false
        apply: false
```

- [ ] **Step 3: Create the chainsaw test**

Create `k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/chainsaw-test.yaml`:

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# yaml-language-server: $schema=https://raw.githubusercontent.com/kyverno/chainsaw/main/.schemas/json/test-chainsaw-v1alpha1.json
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: preserve-downgrade-state
spec:
  timeouts:
    assert: 240s
  catch:
    - get:
        apiVersion: v1
        kind: Node
        selector: skyhook.nvidia.com/test-node=skyhooke2e
        format: yaml
    - get:
        apiVersion: skyhook.nvidia.com/v1alpha1
        kind: Skyhook
        name: preserve-downgrade-state
        format: yaml
  steps:
  - name: install-v2
    description: Install the package at v2.1.4 with enabled=false
    try:
    - script:
        content: |
          ../skyhook-cli reset preserve-downgrade-state --confirm 2>/dev/null || true
    - create:
        file: skyhook.yaml
    - assert:
        resource:
          apiVersion: skyhook.nvidia.com/v1alpha1
          kind: Skyhook
          metadata:
            name: preserve-downgrade-state
          status:
            status: complete

  - name: downgrade
    description: Downgrade to v1.2.3 — old v2.1.4 state should persist alongside the new install
    try:
    - update:
        file: update-downgrade.yaml
    - assert:
        ## Both versions present in node state; new version reached config/complete
        resource:
          apiVersion: v1
          kind: Node
          metadata:
            labels:
              skyhook.nvidia.com/test-node: skyhooke2e
            annotations:
              ("skyhook.nvidia.com/nodeState_preserve-downgrade-state" && parse_json("skyhook.nvidia.com/nodeState_preserve-downgrade-state")):
                {
                  "mypkg|2.1.4": {
                      "name": "mypkg",
                      "version": "2.1.4",
                      "image": "ghcr.io/nvidia/skyhook/agentless",
                      "stage": "config",
                      "state": "complete"
                  },
                  "mypkg|1.2.3": {
                      "name": "mypkg",
                      "version": "1.2.3",
                      "image": "ghcr.io/nvidia/skyhook/agentless",
                      "stage": "config",
                      "state": "complete"
                  }
                }
    - assert:
        resource:
          apiVersion: skyhook.nvidia.com/v1alpha1
          kind: Skyhook
          metadata:
            name: preserve-downgrade-state
          status:
            status: complete
```

- [ ] **Step 4: Commit**

```bash
git add k8s-tests/chainsaw/skyhook/downgrade-enabled-false-preserves-state/
git commit -s -m "$(cat <<'EOF'
test(chainsaw): add downgrade-enabled-false-preserves-state test

Asserts that downgrading an enabled=false package preserves the old
version entry in node state (D2 semantics: non-absent = not cleanly
uninstalled) while installing the new version alongside.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 16: Audit existing chainsaw tests for legacy-downgrade dependence

**Files to inspect:**
- `k8s-tests/chainsaw/skyhook/interrupt-grouping/`
- `k8s-tests/chainsaw/skyhook/config-skyhook/`
- `k8s-tests/chainsaw/skyhook/depends-on/`
- `k8s-tests/chainsaw/skyhook/cleanup-pods/`
- `k8s-tests/chainsaw/skyhook/simple-skyhook/`
- `k8s-tests/chainsaw/skyhook/simple-update-skyhook/`
- `k8s-tests/chainsaw/skyhook/interrupt/`
- `k8s-tests/chainsaw/skyhook/delete-skyhook/`
- `k8s-tests/chainsaw/skyhook/validate-packages/`

- [ ] **Step 1: Identify tests that exercise version downgrade (not upgrade)**

For each directory, examine the `skyhook.yaml` initial version and `update*.yaml` target version(s). If ANY update yaml changes a version from higher to lower (SemVer), it's a downgrade that may no longer work under the new rules.

```bash
for d in k8s-tests/chainsaw/skyhook/*/; do
    echo "=== $d ==="
    for f in "$d"*.yaml; do
        grep -H 'version:' "$f" 2>/dev/null | head -5
    done
done | less
```

- [ ] **Step 2: For each test that does a downgrade without explicit uninstall first**

Options:
- **Delete the test** if it exists only to exercise the legacy behavior.
- **Rewrite** to first flip `apply=true`, wait for uninstall, then downgrade (the new supported flow).
- **Switch to upgrade** if the test's intent was "change version" and direction didn't matter.

Make the judgment per test. Record the decision in the commit message.

- [ ] **Step 3: Delete the already-removed uninstall-upgrade-skyhook directory**

The working tree shows these files as deleted. Run:

```bash
git rm -rf k8s-tests/chainsaw/skyhook/uninstall-upgrade-skyhook/ 2>/dev/null || true
```

(They're already marked deleted in working tree; this stages the deletion.)

- [ ] **Step 4: Commit audit changes**

```bash
git add -A k8s-tests/chainsaw/skyhook/
git commit -s -m "$(cat <<'EOF'
test(chainsaw): remove legacy downgrade-triggers-uninstall paths

Removes uninstall-upgrade-skyhook test and adjusts other tests that
depended on the now-removed auto-uninstall-on-downgrade behavior.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 11: Cleanup typos and docs

### Task 17: Fix `/` → `//` comment typos

**Files:**
- Modify: `operator/internal/controller/skyhook_controller.go:513, 775`
- Modify: `operator/api/v1alpha1/skyhook_types.go` (search for `^/ ` pattern)

- [ ] **Step 1: Grep for the typos**

```bash
grep -n '^/ ' operator/internal/controller/skyhook_controller.go operator/api/v1alpha1/skyhook_types.go
```

Expected: lines starting with `/ ` (single slash + space) that should be `// ` (double slash + space).

- [ ] **Step 2: Fix each occurrence**

For each line found, change `/ ` at the start to `// `:

Example — line 513 in skyhook_controller.go might be:
```go
/ HandleUninstallRequests should ...
```
Change to:
```go
// HandleUninstallRequests should ...
```

Repeat for every line the grep finds.

- [ ] **Step 3: Verify no more typos**

```bash
grep -n '^/ ' operator/internal/controller/skyhook_controller.go operator/api/v1alpha1/skyhook_types.go
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add operator/internal/controller/skyhook_controller.go operator/api/v1alpha1/skyhook_types.go
git commit -s -m "$(cat <<'EOF'
style: fix stray '/' comment delimiters

A few comments were written with a single slash instead of two,
making them syntactically invalid (ignored by the compiler but
noisy to read). Fixes each occurrence to //.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 18: Update docs/uninstall.md

**Files:**
- Modify: `docs/uninstall.md`

- [ ] **Step 1: Read the existing docs**

```bash
cat docs/uninstall.md
```

- [ ] **Step 2: Add a section describing the new lifecycle**

At an appropriate place in `docs/uninstall.md` (after the introduction, before any existing "Lifecycle" section — or add a new "Lifecycle" section if none exists), insert:

```markdown
## Uninstall lifecycle

When `uninstall.apply=true` is set on a package that is fully installed on a node, the controller advances the node state through the following stages:

1. **`StageUninstall / InProgress`** — the controller creates a pod that runs `uninstall.sh` (and `uninstall-check.sh`) from the package's configmap. If the script fails, the state becomes `StageUninstall / Erroring` and retries.

2. **`StageUninstallInterrupt / InProgress`** — reached only if the package has an `interrupt:` configured (e.g., `type: reboot`, `type: service`). The controller creates an interrupt pod using the existing interrupt mechanism. For `reboot`, the node reboots; for `service`, the service is restarted; etc.

3. **`StageUninstallInterrupt / Complete`** — the interrupt pod has completed. On the next reconcile, `HandleUninstallRequests` calls `RemoveState` and the package annotation disappears from the node (`absent = uninstalled` per D2 semantics).

If the package has no `interrupt:` configured, the flow is `StageUninstall / InProgress` → `RemoveState` (no uninstall-interrupt phase).

### Cancellation

Setting `uninstall.apply=true → false` cancels the uninstall **only if the node is still at `StageUninstall`**. Once the uninstall pod has completed and the node has advanced to `StageUninstallInterrupt`, the cycle cannot be cancelled — the interrupt must run to completion.

### Downgrades

Version downgrades are only accepted if the old spec already had `uninstall.apply=true` AND the package is absent from every tracked node's state (uninstall complete per D2). The user-facing rule: "to downgrade a package, first uninstall it." Upgrades have no such restriction.

For packages with `uninstall.enabled=false`, downgrades are accepted without the uninstall gate — but the old version's state annotation is **preserved** in node state. This is intentional: without explicit uninstall, the package's files on the node are not cleanly removed, and the persistent state annotation signals this to operators.
```

- [ ] **Step 3: Commit**

```bash
git add docs/uninstall.md
git commit -s -m "$(cat <<'EOF'
docs: document uninstall-interrupt stage and downgrade rule

Describes the uninstall lifecycle (including the new
StageUninstallInterrupt phase), cancellation semantics, and the
stricter downgrade rule for enabled=true packages.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 12: Final verification

### Task 19: Run full lint and test suite

- [ ] **Step 1: Run lint**

```bash
cd operator && make lint 2>&1 | tail -30
```

Expected: no errors. Fix any lint issues found.

- [ ] **Step 2: Run unit tests**

```bash
cd operator && make unit-tests 2>&1 | tail -30
```

Expected: all pass.

- [ ] **Step 3: Run full test**

```bash
cd /Users/ayuskauskas/git_repos/nvidia/nodewright && make test 2>&1 | tail -50
```

Expected: all pass.

- [ ] **Step 4: If any tests fail, investigate**

For each failure:
- Is it a real regression? → fix.
- Is it a test that exercised the legacy downgrade path? → delete or rewrite.
- Commit fixes as separate commits with appropriate messages.

### Task 20: Verify build artifact

- [ ] **Step 1: Build operator and CLI**

```bash
cd operator && make build
```

Expected: clean build.

- [ ] **Step 2: Build agent (for completeness — no agent changes in this plan but verify nothing broke)**

```bash
cd agent && make build 2>&1 | tail -10
```

Expected: clean build.

### Task 21: Review git log for tidiness

- [ ] **Step 1: Review commit series**

```bash
git log --oneline 249eb264..HEAD
```

Expected: a clean series of commits, each with a descriptive conventional-commit message. If any commits are "WIP" or confused, consider rebasing or keeping as-is depending on merge policy.

- [ ] **Step 2: Verify DCO sign-off on every commit**

```bash
git log 249eb264..HEAD --format='%H %s%n  %(trailers:key=Signed-off-by)%n'
```

Expected: every commit has a `Signed-off-by:` trailer. If any is missing, amend that commit with `git commit --amend -s --no-edit` (only if it's the most recent) or address before merging.

---

## Summary of deferred items (from spec's "Open items")

These were deliberately left for a follow-up plan rather than this one:

1. **`isPackageFullyUninstalled` zero-node-selector behavior** — decision whether empty `NodeState` should evaluate as "nothing to uninstall = fully uninstalled" or remain "cannot verify uninstall."
2. **`enabled: true → false` flip mid-uninstall** — webhook rejection OR `HandleCompletePod` guard.
3. **Cancellation warning text refinement** — the current `ValidateUpdate` warning is misleading once `StageUninstallInterrupt` exists.
4. **`versionChangeDetected` side-effects** — audit whether the flag still being set on downgrade causes inappropriate downstream behavior.

These should be addressed in a follow-up ticket / PR if they surface in practice.

---

## Self-review checklist

Before declaring the plan complete:

- [ ] Every task references exact file paths and line numbers.
- [ ] Every code change is shown in full (not "similar to above").
- [ ] Every test has concrete code.
- [ ] Every commit message is written out.
- [ ] No "TODO", "TBD", or placeholder text.
- [ ] Phases are independently commitable — each phase's artifacts compile and tests (local to that phase) pass.
- [ ] The bug #1 regression test from Phase 7 would have caught the original bug.
