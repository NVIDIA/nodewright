# Explicit Uninstall

NodeWright supports explicit, controlled uninstall of packages from nodes. This document covers the API, workflows, and migration guide.

## API Reference

### `Uninstall` struct

Added to each package entry in `spec.packages`:

```yaml
packages:
  my-package:
    version: "1.0.0"
    image: ghcr.io/example/pkg
    uninstall:
      enabled: true   # declares this package supports uninstall
      apply: false     # set to true to trigger uninstall
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Declares the package has uninstall scripts (`uninstall.sh`, `uninstall_check.sh`). When true, the operator runs uninstall pods before allowing package removal and during CR deletion cleanup. |
| `apply` | bool | `false` | Triggers the uninstall workflow on all target nodes. Only valid when `enabled` is true. Set to `false` to cancel a pending uninstall. |

## Workflows

### Explicit Uninstall (single package)

1. Set `uninstall.apply: true` on the package:

```yaml
packages:
  my-package:
    version: "1.0.0"
    image: ghcr.io/example/pkg
    uninstall:
      enabled: true
      apply: true      # triggers uninstall
```

2. The operator creates uninstall pods on each target node. These pods run with the **full package configuration** (ConfigMap, env, resources) — not a synthetic stub.

3. After uninstall completes on all nodes, the package is absent from node state (absent = uninstalled).

4. You may now safely remove the package from `spec.packages`. The webhook allows removal once the package is fully uninstalled.

### Uninstall Lifecycle

When the uninstall pod runs to completion, the controller advances the node through the following stages:

1. **`StageUninstall / InProgress`** — the uninstall pod runs `uninstall.sh` (and `uninstall-check.sh`) from the package's ConfigMap. If the script fails, the state becomes `StageUninstall / Erroring` and retries.

2. **`StageUninstallInterrupt / InProgress`** — reached only if the package has an `interrupt:` configured (e.g., `type: reboot`, `type: service`). The controller creates an interrupt pod using the existing interrupt mechanism. For `reboot`, the node reboots; for `service`, the service is restarted; etc.

3. **`StageUninstallInterrupt / Complete`** — the interrupt pod has completed. On the next reconcile, `HandleUninstallRequests` calls `RemoveState` and the package annotation disappears from the node (`absent = uninstalled` per D2 semantics).

If the package has no `interrupt:` configured, the flow is `StageUninstall / InProgress` → `RemoveState` (no uninstall-interrupt phase).

### Cancel Uninstall

Set `uninstall.apply: false` (or remove the uninstall block):

```yaml
packages:
  my-package:
    version: "1.0.0"
    image: ghcr.io/example/pkg
    uninstall:
      enabled: true
      apply: false     # cancels pending uninstall
```

Cancellation semantics depend on which stage the node is at when `apply` is flipped back to `false`:

| Stage at moment of cancel | Behavior |
|---|---|
| `StageUninstall / InProgress` or `Erroring` | Reset to install pipeline (`StageApply`). Package re-installs. |
| `StageUninstallInterrupt / *` | **Uncancellable.** The interrupt has fired and must run to completion. The uninstall completes even though `apply` is now false. |
| Uninstall already completed (node state absent) | Re-installs the package automatically. |

- The webhook emits a warning (not a rejection) on cancel.

### CR Deletion (Finalizer)

When a Skyhook CR is deleted (`kubectl delete skyhook my-skyhook`):

- **`enabled: true` packages**: The finalizer triggers uninstall pods, waits for completion on all nodes, then cleans up (uncordon nodes, remove SCR labels/annotations, remove finalizer).
- **`enabled: false` packages (or nil)**: No uninstall pods — state is cleaned up immediately. The package state remains on nodes so administrators can see what was previously applied.

#### Deletion edge cases

The finalizer handles a few edge cases where the normal "wait for uninstall to complete" path can't proceed. They surface as `skyhook.nvidia.com/DeletionBlocked` conditions (with a distinguishing `Reason`) or a Warning event:

| State at deletion | Outcome | Condition / Event |
|---|---|---|
| `nodeState` annotation unreadable on any node | **Blocked.** The finalizer cannot safely decide what to preserve or what still needs uninstalling. Repair the annotation (or delete it) on the affected node, then reconciliation proceeds. | `DeletionBlocked` / `Reason: MalformedNodeState` |
| Skyhook is **paused** AND at least one `uninstall.enabled=true` package is still tracked in `nodeState` | **Blocked.** A paused Skyhook can't drive uninstall (`processSkyhooksPerNode` short-circuits on pause). Unpause so uninstall can complete, then deletion proceeds. | `DeletionBlocked` / `Reason: PausedWithPendingUninstall` |
| Skyhook is **disabled** AND at least one `uninstall.enabled=true` package is still tracked in `nodeState` | **Blocked.** A disabled Skyhook also can't drive uninstall (`processSkyhooksPerNode` short-circuits on disable). `uninstall.enabled=true` is an explicit request to run uninstall scripts before the CR is removed — silently deleting would leave host-side state the user asked to be cleaned. Re-enable the Skyhook so uninstall can run. | `DeletionBlocked` / `Reason: DisabledWithPendingUninstall` |
| Paused or disabled, but no uninstall-enabled packages are tracked in `nodeState` (all packages are `uninstall.enabled=false`, or their uninstall already completed) | **Deletion proceeds** normally — pause/disable only matter when there is uninstall work to drive. `uninstall.enabled=false` packages are treated as complete in the finalizer, and their `nodeState` entries are preserved by `CleanupSCRMetadata` (D2 semantics: non-absent entry means files remain on host). | — |

Notes:

- `DeletionBlocked` is cleared automatically once the blocking condition is resolved (annotation repaired, Skyhook unpaused, or the pending work is no longer present).
- Forcing deletion of a blocked Skyhook requires manually removing the `skyhook.nvidia.com/skyhook` finalizer (`kubectl patch skyhook <name> --type=merge -p '{"metadata":{"finalizers":null}}'`). Doing this bypasses Phase 3 cleanup entirely: per-Skyhook labels/annotations/conditions are **not** removed and nodes are **not** uncordoned — the caller is responsible for any residual cleanup.

### Downgrade (version change)

Version downgrades are gated: the webhook rejects any downgrade unless the OLD spec already had `uninstall.apply: true` AND the package is absent from every tracked node's state (uninstall complete per D2). The rule: **to downgrade a package, first uninstall it.**

Upgrades have no such restriction and continue to work unchanged.

For packages with `uninstall.enabled: false`, downgrades are accepted without the uninstall gate — but the OLD version's state annotation is **preserved** in node state alongside the new version. This is intentional: without explicit uninstall, the old package's files on the node are not cleanly removed, and the persistent state annotation signals this to operators.

The legacy "downgrade triggers an uninstall pod for the old version" behavior has been removed.

## Webhook Validation Rules

| Rule | Action |
|------|--------|
| `apply: true` with `enabled: false` | **Rejected** — `apply: true` requires `uninstall.enabled: true` |
| Remove `enabled: true` package from spec without completing uninstall | **Rejected** — must uninstall first |
| Remove `enabled: false` (or nil) package from spec | **Allowed** — no uninstall needed |
| Version downgrade when old `apply: false` | **Rejected** — set `uninstall.apply: true` first, wait, then change version |
| Version downgrade when old `apply: true` but uninstall not yet complete on all nodes | **Rejected** — wait for uninstall to finish |
| Version downgrade when old `apply: true` AND package absent from all nodes | **Allowed** |
| Cancel (`apply: true` -> `false`) | **Warning** — nodes may need to re-install |

## DAG Dependency Interaction

If package A is being uninstalled and package B depends on A:
- B is **blocked** (cannot run) because A is no longer in the completed set
- Uninstall does **not** cascade — B remains installed
- A `nodewright.nvidia.com/Blocked` condition is set with a message indicating the broken dependency
- To resolve: either re-install A (cancel uninstall) or remove A from B's `dependsOn`

## Migration Guide

### Clusters using "remove from spec to uninstall"

The old behavior (removing a package from `spec.packages` triggers an uninstall pod) has been replaced:

- **`enabled: false` (default)**: Removing from spec is allowed, but no uninstall pod runs. The node-state annotation for the old package is **preserved** — its non-absence signals that the package's files may still be on the host (nothing ran `uninstall.sh`). The operator stops tracking the package but leaves the entry as a marker.
- **`enabled: true`**: Removing from spec is blocked by the webhook until `uninstall.apply: true` has been set and the uninstall has completed on all nodes.

To migrate to the explicit model:

1. Add `uninstall.enabled: true` to packages that need cleanup scripts run
2. Set `uninstall.apply: true` and wait for completion
3. Remove the package from spec

### Rollback safety

If the operator is rolled back to a version without explicit uninstall support:
- The `uninstall` field is preserved by Kubernetes but ignored by the old operator
- Packages at `StageUninstall` will be handled by the old version-change logic
- **Before rolling back**: remove `uninstall` config from all CRs to avoid packages stuck in `apply: true` state

## Troubleshooting

### Package stuck in uninstall

Check the uninstall pod logs:
```bash
kubectl logs -n skyhook <pod-name> -c <package>-uninstall
kubectl logs -n skyhook <pod-name> -c <package>-uninstallcheck
```

Check node state:
```bash
kubectl get nodes -l skyhook.nvidia.com/test-node=skyhooke2e -o jsonpath='{.items[*].metadata.annotations.skyhook\.nvidia\.com/nodeState_<skyhook-name>}' | jq
```

### Blocked dependency

Check the Skyhook conditions:
```bash
kubectl get skyhook <name> -o jsonpath='{.status.conditions}' | jq
```

Look for `nodewright.nvidia.com/Blocked` condition with the dependency chain message.

### Webhook rejection on package removal

If the webhook rejects removal of an `enabled: true` package:
1. Set `uninstall.apply: true` on the package
2. Wait for uninstall to complete (package absent from all node states)
3. Then remove the package from spec

## Known Issues

### CR deletion deadlocks when an install is stuck at `erroring`

**Symptom.** `kubectl delete skyhook <name>` hangs indefinitely. The Skyhook stays around with a `DeletionTimestamp` set and the `skyhook.nvidia.com/skyhook` finalizer attached. No uninstall pods are created for an `uninstall.enabled: true` package that's still tracked in `nodeState`.

**Why.** The finalizer drives uninstall through the same `HandleUninstallRequests` path as explicit uninstall. That path only transitions a package from an install stage (`apply`, `config`, `interrupt`, `post-interrupt`, `upgrade`) to `uninstall` when the package is in **`state: complete`** on the node. If the install never reached `complete` — e.g., `uninstall.sh` wasn't yet exercised because `apply.sh` is crash-looping in `state: erroring` — the uninstall trigger is skipped, so the finalizer's "wait for pending uninstall" phase never progresses.

**How to confirm.** Look for a node where the package is `state: erroring` at a non-uninstall stage:

```bash
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.name as $n
      | .metadata.annotations["skyhook.nvidia.com/nodeState_<skyhook-name>"]
      | fromjson
      | to_entries[]
      | select(.value.state == "erroring" and (.value.stage | test("uninstall") | not))
      | "\($n) \(.key) \(.value.stage)/\(.value.state)"'
```

Any rows returned are nodes the finalizer is waiting on.

**Workarounds (pick one; they have different blast radius).**

1. **Fix the underlying install.** Inspect `kubectl logs -n skyhook <pod> -c <pkg>-apply` and correct the script, config, or environment so the install completes. Once the node reaches `stage: config` / `state: complete` (or `post-interrupt/complete` if the package has an interrupt), the finalizer's next reconcile will transition it to `uninstall` and proceed.

2. **Reset the affected node's Skyhook state.** Use the CLI:

    ```bash
    kubectl skyhook reset <skyhook-name> --node <node-name> --confirm
    ```

    This clears the per-skyhook `nodeState` annotation on that node. With the entry gone, the finalizer's "is anything still tracked" check turns false and Phase 3 cleanup runs. **Caveat:** `uninstall.sh` does **not** run — anything the install script wrote to the host is left in place. Prefer this only when you know the install didn't actually modify host state, or when you're willing to clean up out-of-band.

3. **Strip the finalizer (last resort).** Bypasses the finalizer entirely:

    ```bash
    kubectl patch skyhook <name> --type=merge -p '{"metadata":{"finalizers":null}}'
    ```

    Same caveat as above, plus Phase 3 cleanup is **skipped**: node cordons, per-skyhook labels/annotations, and conditions are **not** removed. You'll need to run `kubectl skyhook reset` on each affected node (or hand-remove the residual keys) afterward.

**Long-term fix.** Tracked as a design gap: the finalizer should be able to drive uninstall from an install-erroring state (either after N retries, or via an explicit "give up on install" CR annotation). Until that lands, the workarounds above are the only options.

### Node stuck at `uninstall-interrupt` after removing `interrupt` from spec

**Rare.** Requires a specific sequence: the uninstall pod has already finished, the node has transitioned to `stage: uninstall-interrupt` / `state: in_progress`, the interrupt pod is **not currently running** (never fired, was manually deleted, or the kubelet evicted it), and the user then edits the package to remove the `interrupt:` block.

**Symptom.** The node's `nodeState` entry for the package is pinned at `stage: uninstall-interrupt` / `state: in_progress`. No new pod is created, no state transition occurs, and the Skyhook never returns to `complete`. Reconciles are a no-op for this package.

**Why.** Once the uninstall pod succeeds, `HandleCompletePod` commits the node to `stage: uninstall-interrupt` only when `package.HasInterrupt()` was true at that moment. The next reconcile's `ProcessInterrupt` re-checks `HasInterrupt` from the *current* spec to decide whether to (re-)create the interrupt pod — if the user has since removed the `interrupt:` block, the check fails and no pod is spawned. `ApplyPackage` short-circuits `stage == uninstall-interrupt` to a no-op (the interrupt machinery is supposed to drive it), and `HandleUninstallRequests` only calls `RemoveState` once `state == complete` — which will never happen without a pod to succeed. The node is permanently stranded.

**How to confirm.**

```bash
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.name as $n
      | .metadata.annotations["skyhook.nvidia.com/nodeState_<skyhook-name>"]
      | fromjson
      | to_entries[]
      | select(.value.stage == "uninstall-interrupt" and .value.state != "complete")
      | "\($n) \(.key) \(.value.state)"'
```

And verify no interrupt pod exists for the package:

```bash
kubectl get pods -n skyhook -l skyhook.nvidia.com/name=<skyhook-name>,skyhook.nvidia.com/package=<pkg>-<ver>,skyhook.nvidia.com/interrupt=True
```

An entry from the first command with no rows from the second confirms the stranded state.

**Workarounds.**

1. **Re-add the interrupt to the spec.** Put the `interrupt:` block back on the package. `ProcessInterrupt` will re-fire the pod; once it completes, the node advances to `stage: uninstall-interrupt` / `state: complete` and `HandleUninstallRequests` calls `RemoveState` on the next reconcile. You can then remove the `interrupt:` block safely.

2. **Reset the affected node.**

    ```bash
    kubectl skyhook reset <skyhook-name> --node <node-name> --confirm
    ```

    Same caveat as the install-erroring case: any pending uninstall script does **not** run, and host-side state written by earlier lifecycle steps stays put.

**Long-term fix.** Tracked as a design gap: once `stage: uninstall-interrupt` is committed the controller should drive it to completion regardless of whether the spec still declares an interrupt — the decision was made when the uninstall pod finished and should not be revocable by a later spec edit.

### `uninstall.apply: true` on a package that was never installed is a silent no-op

**Symptom.** A package with `uninstall.enabled: true` / `uninstall.apply: true` is in the spec of a newly-applied (or extended) Skyhook. Reconcile runs, no uninstall pod spawns, and the package is **never installed** either. The Skyhook looks idle for that package — no events, no error condition, nothing in the status to explain the silence.

**Why.** The reconciler treats `IsUninstalling() && absent from nodeState` as the terminal "uninstalled" state (per D2). A brand-new package is also absent from nodeState, which collides with that signal: `shouldSkipApplyForUninstall` returns true (apply requested + absent), so the install pipeline is skipped. The package is interpreted as "already uninstalled, nothing to do." The webhook only validates `apply: true` requires `enabled: true` — it has no way to tell "never-installed" from "fully-uninstalled" at admission time.

**Most common trigger.** Copy-pasting a working Skyhook YAML from one cluster to another and forgetting to flip `apply` back to `false` before applying. Also reachable by applying a Skyhook with `apply: true` set in a manifest generated by a tool that tracks "last known good" config.

**How to confirm.** Package is in spec with `apply: true, enabled: true`, but no entry in any node's `nodeState_<skyhook-name>` annotation:

```bash
kubectl get skyhook <name> -o jsonpath='{.spec.packages.<pkg>.uninstall}'
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | "\(.metadata.name): \(.metadata.annotations["skyhook.nvidia.com/nodeState_<skyhook-name>"] // "<no state>")"'
```

If every node returns `<no state>` (or a state map that doesn't contain the package's `name|version` key), the package was never installed.

**Workaround.** Flip `apply` back to `false`:

```yaml
uninstall:
  enabled: true
  apply: false
```

Re-apply the Skyhook. The install pipeline will engage on the next reconcile.

**Long-term fix.** Either emit an admission warning for `apply: true` on a package where the webhook sees no node state, or raise an explicit Skyhook condition (`Skipped: apply=true on never-installed package`) so the silence is surfaced in `kubectl describe`.

### Changing `version` while `uninstall.apply: true` is in flight leaves the new version uninstalled

**Symptom.** A package is being uninstalled (`apply: true`) across the fleet. Before the uninstall finishes on all nodes, the user bumps the package's `version`. Reconcile proceeds, old-version state is cleaned up, but the new version **never installs**. The package sits in terminal "uninstalled" state indefinitely.

**Why.** The webhook only rejects *downgrades* during active uninstall; upgrades are allowed. When `HandleCompletePod` sees an uninstall-pod finish for a version that's no longer in spec, its defensive branch removes the old-version state. On the next reconcile, the new version is in spec but absent from nodeState — `shouldSkipApplyForUninstall` sees `apply: true` + absent and treats the package as uninstalled. The install pipeline skips it.

**How to confirm.** Package in spec has the new version, every node's nodeState annotation either lacks the package entirely or has the package at the *old* version, and `uninstall.apply` is still `true`:

```bash
kubectl get skyhook <name> -o jsonpath='{.spec.packages.<pkg>.version}'
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.annotations["skyhook.nvidia.com/nodeState_<skyhook-name>"] // "{}" | fromjson | keys'
```

If spec shows the new version and no node state references the new `name|version` key, the package is stranded.

**Workaround.** Flip `apply: false` to re-engage the install pipeline with the new version. Once the new version is complete on all nodes, you may set `apply: true` again if you actually want to uninstall.

**Long-term fix.** Either reject version changes at the webhook while `apply: true`, or auto-reset `apply: false` on version change so the user's intent ("install the new version") wins.

### Cancel during `stage: uninstall` may briefly "uninstall then re-install" on a node

**Symptom.** A user sets `apply: true`, an uninstall pod starts on one or more nodes, then the user flips `apply: false` to cancel. For nodes where the uninstall pod was mid-run, observable behavior is: the uninstall pod finishes successfully, the package briefly disappears from `nodeState`, and then the install pipeline re-engages and reinstalls the package.

**Why.** `HandleCancelledUninstalls` transitions the nodeState from `stage: uninstall` back to `stage: apply` but does not kill the already-running uninstall pod. If the pod finishes before the next reconcile deletes it via `ValidateRunningPackages`, `HandleCompletePod` honors the pod's reported stage (`uninstall`) and calls `RemoveState` — erasing the freshly-reset `stage: apply` entry. The next reconcile sees the package absent from nodeState with `apply: false` and schedules a fresh install.

**User-visible impact.** The end state is correct — the package is installed — but the *path* is "complete the uninstall that was cancelled, then reinstall," not "resume install from where it was." Operators watching closely will see the package briefly disappear from `nodeState` and then re-appear, which can look alarming. If the package writes state that isn't idempotent across an uninstall + reinstall cycle, the user should prefer **not** cancelling mid-pod.

**Workaround / guidance.** If you need to cancel in-flight: either (a) accept the "uninstall then reinstall" path, or (b) wait for the uninstall pod to finish and the node to be absent from state, then flip `apply: false` — `RunNext` will reinstall cleanly without the intermediate erase.

**Long-term fix.** `HandleCancelledUninstalls` should delete any in-flight uninstall pod when it resets the stage, instead of leaving cleanup to `ValidateRunningPackages`.

### `CleanupSCRMetadata` can over-delete annotations when a Skyhook name collides with a taint-key suffix

**Unlikely, operational.** During CR-delete cleanup (`HandleFinalizer` Phase 3), `CleanupSCRMetadata` removes any `skyhook.nvidia.com/*` annotation or label whose key ends with `_<skyhookName>`. That suffix-match also catches the `skyhook.nvidia.com/autoTaint_<taintKey>` annotation written by `AutoTaintNewNodes` — **but only if the Skyhook's name exactly equals the taint key's value**. In practice both sides are user-chosen strings; a collision requires a Skyhook named to match a taint key (e.g., a Skyhook literally named `runtime-required` in a cluster where the runtime-required taint uses that string).

**Impact if it happens.** The `autoTaint_*` annotation is removed when the Skyhook is deleted, losing the audit trail of which nodes were auto-tainted. The taint itself is a separate concern (managed by `HandleAutoTaint`) and is not affected. In real clusters this is almost never reachable because Skyhook names tend to be descriptive (`gpu-drivers`, `kernel-tune`) while taint keys tend to be namespaced (`nvidia.com/gpu`, `skyhook.nvidia.com`).

**Avoidance.** Don't name Skyhooks to exactly match a taint key in use. If you have a clash, either rename the Skyhook or disable `AutoTaintNewNodes` on it before deletion.

**Long-term fix.** Replace the suffix match in `CleanupSCRMetadata` with an explicit list of cleanup keys (`status_`, `nodeState_`, `cordon_`, `version_`) so unrelated keys with a coincidentally-matching suffix are never touched.

### Force-deleting a Skyhook mid-uninstall and recreating it can run a stray apply pod

**Rare; requires `kubectl delete --force --grace-period=0` on a Skyhook with an active uninstall pod.** Under normal deletion the finalizer holds the CR until the uninstall pod completes, so this path isn't reachable. Force-delete bypasses the finalizer.

**Symptom.** After force-deleting a Skyhook whose uninstall pod was mid-run, then recreating a Skyhook with the same name and `uninstall.apply: true`, one of the affected nodes briefly runs an **apply** pod for the package before the controller transitions it back to uninstall. The end state is correct (the package eventually uninstalls), but operators see one unexpected install cycle.

**Why.** When the uninstall pod completes, `HandleCompletePod` looks up the parent Skyhook via `dal.GetSkyhook`; if the CR is gone, it returns `(nil, nil)` and the function exits without writing the usual "remove state" or "advance to `uninstall-interrupt`" outcome. The caller `UpdateNodeState` then falls through to its default `Upsert(state=Complete, stage=packagePtr.Stage)` — persisting `stage: uninstall` / `state: complete` on the node annotation. Recreating the Skyhook surfaces that orphaned annotation. `HandleUninstallRequests`'s `StageUninstall` branch re-adds the package to `toUninstall` regardless of state. `ApplyPackage` then reads `packageStatus.Stage = uninstall` and calls `NextStage`, which (for a no-interrupt package at `state: complete`) maps `uninstall → apply` per `NodeState.NextStage` — so an apply pod is created. The apply pod completes, the node moves to `stage: apply` / `state: complete`, the next reconcile takes the install-cycle branch in `HandleUninstallRequests`, and Upserts the package back to `stage: uninstall` / `state: in_progress`. Self-corrects within one extra apply cycle.

**How to confirm.** After the force-delete + recreate, look for the orphaned terminal-uninstall entry **before** the controller has had time to re-trigger:

```bash
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.name as $n
      | .metadata.annotations["skyhook.nvidia.com/nodeState_<skyhook-name>"]
      | fromjson
      | to_entries[]
      | select(.value.stage == "uninstall" and .value.state == "complete")
      | "\($n) \(.key)"'
```

Any rows are nodes the controller will run an unwanted apply pod on before retriggering uninstall.

**Avoidance.** Don't `--force --grace-period=0` a Skyhook with active uninstall pods. Let the finalizer drive uninstall to completion, or use the documented workarounds for blocked-finalizer cases (`kubectl skyhook reset`, then plain `kubectl delete`).

**Workaround if already in this state.** Before recreating the Skyhook, run `kubectl skyhook reset <skyhook-name> --node <node-name> --confirm` on each affected node to clear the orphaned annotation. Then recreate the Skyhook normally — the install pipeline engages cleanly with no spurious apply pod.

**Long-term fix.** In `HandleUninstallRequests`, special-case `stage: uninstall` / `state: complete`: call `RemoveState` (mirroring the existing `uninstall-interrupt / complete` branch) and skip the `toUninstall` append. The current "re-add defensively" comment predates the realisation that `NextStage` re-maps `uninstall → apply` for completed packages without an interrupt; the safe handling is to treat a completed uninstall as terminal-uninstalled per D2.
