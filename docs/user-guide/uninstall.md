# Explicit Uninstall

NodeWright supports explicit, controlled uninstall of packages from nodes. This document covers the API, workflows, and migration guide.

> `kubectl` examples below use `nodewright`, the default install namespace for new installs. Substitute your own if you installed elsewhere; installs predating the namespace rename are in `skyhook`.

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
| `enabled` | bool | `false` | Declares the package has uninstall scripts (`uninstall.sh`, `uninstall_check.sh`). When true, the operator runs uninstall pods before allowing package removal and during CR deletion cleanup, unless its package artifact declares uninstall unsupported (see [Uninstall support declared by a package artifact](#uninstall-support-declared-by-a-package-artifact)). |
| `apply` | bool | `false` | Triggers the uninstall workflow on all target nodes. Only valid when `enabled` is true. Set to `false` to cancel a pending uninstall. |

## Uninstall support declared by a package artifact

With the package artifacts option on (`PACKAGE_ARTIFACTS`, chart value `controllerManager.manager.env.packageArtifacts`), the operator reads the `uninstall` field of a package artifact's [metadata](package-artifact-spec.md#metadata), which declares uninstall `supported` or `unsupported`. `uninstall.enabled` stays the opt-in, and the declaration is a ceiling on it: an artifact declaring uninstall supported runs no uninstall for a package that has not opted in, and one declaring it unsupported keeps an opted-in package from uninstalling. The operator decides on every reconcile pass, and only for a package whose uninstall is requested (`uninstall.apply`, or the deletion of the NodeWright of an `uninstall.enabled` package) or under way on some node.

### Precedence

| Decision | `enabled: false` | `enabled: true`, `apply: true` | `enabled: true`, NodeWright deleted |
|---|---|---|---|
| **spec**: the option is off, the package is a container image, or its artifact declares uninstall supported | No uninstall | The uninstall runs, as [Explicit Uninstall](#explicit-uninstall-single-package) describes | The finalizer runs the uninstall and waits for it, as [CR Deletion](#cr-deletion-finalizer) describes |
| **unsupported**: an accepted artifact declares uninstall unsupported | No uninstall | The uninstall never starts, with an `UninstallUnsupported` Warning and condition. The package stays installed and the NodeWright can complete | The deletion does not wait for it, and keeps its node state as for an `enabled: false` package |
| **deferred**: the option is on and the reference cannot be inspected, even one naming a container image, or the artifact is rejected | No uninstall | The uninstall does not start until an inspection succeeds, then follows what the artifact declares, with an `UninstallDeferred` Warning meanwhile. The operator retries within 5 minutes | The deletion waits for it. A rejected artifact also gives `DeletionBlocked`, reason `PackageArtifactRefused` |

A decision holds back only an uninstall that has not started on a node. One already under way is never undone; [Stalled uninstall](#stalled-uninstall) describes what happens when its artifact turns unsupported.

### Deferred uninstall

The operator never uses the spec as a guess at what an artifact declares. It defers the uninstall when the artifact is [rejected](../architecture/lifecycle.md#what-the-inspection-decides), or when the reference could not be inspected and the operator holds no earlier answer for it (see [Caching](../architecture/lifecycle.md#caching)): for example, the registry is unreachable, refuses the operator's credentials, has no such reference, or timed out. The uninstall starts once an inspection succeeds. To unblock it instead:

- Give the operator access to the registry through its image pull Secret (`IMAGE_PULL_SECRET`, chart value `imagePullSecret`); see [Registry credentials](../architecture/lifecycle.md#registry-credentials).
- Set `uninstall.enabled: false` (with `uninstall.apply: false`) to give up the uninstall.
- Turn `PACKAGE_ARTIFACTS` off, so every uninstall follows the spec again.

While the NodeWright is deleted, the finalizer waits for a deferred package.

### Stalled uninstall

An artifact can turn unsupported while an uninstall cycle is under way, for example when its tag is re-pushed; the operator sees a re-pushed tag within 5 minutes. On each node:

- An uninstall Job that already runs finishes.
- A node at the `uninstall` stage with no Job gets none, with a `PackageArtifactUninstallUnsupported` Warning. It is neither cordoned nor drained for it, and one already cordoned stays cordoned.
- A node at the `uninstall-interrupt` stage completes it.

The `UninstallUnsupported` condition lists the package as `(stalled)`. It reads node state, so a node still running its Job counts until the Job finishes. The cycle resumes once the artifact declares uninstall supported again. It is never cancelled automatically: cancel it with `uninstall.apply: false` or `uninstall.enabled: false`, which resets the entry to the `apply` stage as [Cancel Uninstall](#cancel-uninstall) describes. While the NodeWright is deleted, only `uninstall.enabled: false` cancels it, and until then the deletion is blocked with `DeletionBlocked`, reason `PackageArtifactRefused`, which names the package.

### Conditions and events

- **`UninstallUnsupported` condition**, reason `ArtifactUninstallUnsupported`: lists each package whose artifact declares uninstall unsupported, as `(not started)` while its requested uninstall is held back, or as `(stalled)` while a node has it at the `uninstall` stage, such as `1 package artifact declares uninstall unsupported: tuning:1.0.0 (not started)`. It lists up to 10 and counts the rest, then gives the remedy. It has no `nodewright.nvidia.com/` prefixed mirror, and clears once `uninstall.apply` or `uninstall.enabled` is false, the artifact changes to one declaring uninstall supported, or the option is off.
- **`UninstallUnsupported` Warning**: the uninstall of a package was not started because its artifact, named by digest, declares uninstall unsupported. Recorded per package.
- **`UninstallDeferred` Warning**: the uninstall of a package was deferred. Recorded per package, naming the reference and why: the error class of the failed inspection (such as `timeout`, `unauthorized`, `not-found` or `transport`) or the reason the artifact is rejected, followed by the remedies.
- **`PackageArtifactUninstallUnsupported` Warning**: a node of a stalled cycle got no Job at its `uninstall` stage.
- **`UninstallCapability` Normal event**: what an accepted artifact declares about uninstall, recorded the first time the operator sees it, once per package and digest per NodeWright for the life of the operator process, so possibly once more after a restart. For an artifact declaring uninstall supported on a package with `enabled: false`, it adds that setting `uninstall.enabled` to true opts the package in.
- **`DeletionBlocked` condition, reason `PackageArtifactRefused`**: the deletion waits on an `uninstall.enabled` package that gets no Job at its stage, from a stalled cycle or a rejected artifact.
- **`PackageArtifactRejected` condition**: names each package whose artifact is rejected, which defers its uninstall.

See [Other Condition Types](../architecture/operator-status.md#other-condition-types).

### Removing a package whose artifact declares uninstall unsupported

Admission never reads a package artifact and makes no registry request. While `uninstall.enabled` is true, the webhook therefore rejects removing the package until its uninstall completes on every node, which never happens. To remove it:

1. Set `uninstall.enabled: false` (and `uninstall.apply: false`).
2. In a later update, remove the package from `spec.packages`.

Its node-state entry stays on each node, marking that its files may remain on the host (D2 semantics, as in the [Migration Guide](#migration-guide)).

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

1. The operator creates uninstall pods on each target node. These pods run with the **full package configuration** (ConfigMap, env, resources) — not a synthetic stub. The uninstall of a package whose artifact declares uninstall unsupported, or whose artifact the operator cannot inspect or rejects, does not start (see [Precedence](#precedence)).

2. After uninstall completes on all nodes, the package is absent from node state (absent = uninstalled).

3. You may now safely remove the package from `spec.packages`. The webhook allows removal once the package is fully uninstalled.

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

When a NodeWright CR is deleted (`kubectl delete nodewright my-nodewright`):

- **`enabled: true` packages**: The finalizer triggers uninstall pods, waits for completion on all nodes (except for a package whose artifact declares uninstall unsupported and that has no uninstall cycle under way; see [Precedence](#precedence)), then removes this NodeWright's own cordon ownership and metadata. A node is uncordoned only once no `cordon_*` ownership annotations remain (other NodeWrights sharing the node may still hold it); the CR finalizer is removed last. Note that if a persistent cordon was previously applied from a NodeWright which set `runtimeRequiredCordonAfter` to true, the cordon will persist even after all `cordon_*` ownership annotations are removed.
- **`enabled: false` packages (or nil)**: No uninstall pods — state is cleaned up immediately. The package state remains on nodes so administrators can see what was previously applied.

#### Deletion edge cases

The finalizer handles a few edge cases where the normal "wait for uninstall to complete" path can't proceed. They surface as `DeletionBlocked` conditions (with a distinguishing `Reason`) or a Warning event:

| State at deletion | Outcome | Condition / Event |
|---|---|---|
| `nodeState` annotation unreadable on any node | **Blocked.** The finalizer cannot safely decide what to preserve or what still needs uninstalling. Repair the annotation (or delete it) on the affected node, then reconciliation proceeds. | `DeletionBlocked` / `Reason: MalformedNodeState` |
| NodeWright is **paused** AND at least one `uninstall.enabled=true` package is still tracked in `nodeState` | **Blocked.** A paused NodeWright can't drive uninstall (`processSkyhooksPerNode` short-circuits on pause). Unpause so uninstall can complete, then deletion proceeds. | `DeletionBlocked` / `Reason: PausedWithPendingUninstall` |
| NodeWright is **disabled** AND at least one `uninstall.enabled=true` package is still tracked in `nodeState` | **Blocked.** A disabled NodeWright also can't drive uninstall (`processSkyhooksPerNode` short-circuits on disable). `uninstall.enabled=true` is an explicit request to run uninstall scripts before the CR is removed — silently deleting would leave host-side state the user asked to be cleaned. Re-enable the NodeWright so uninstall can run. | `DeletionBlocked` / `Reason: DisabledWithPendingUninstall` |
| An `uninstall.enabled=true` package gets no Job at its stage on some node, from a [stalled cycle](#stalled-uninstall) or a rejected package artifact | **Blocked.** Its uninstall can never complete. Correct the artifact, or set `uninstall.enabled: false` on the package to delete the NodeWright without uninstalling it. | `DeletionBlocked` / `Reason: PackageArtifactRefused` |
| Paused or disabled, but no uninstall-enabled packages are pending: none is tracked in `nodeState` (all packages are `uninstall.enabled=false`, or their uninstall already completed), or each one tracked has an artifact declaring uninstall unsupported and no uninstall cycle under way | **Deletion proceeds** normally — pause/disable only matter when there is uninstall work to drive. `uninstall.enabled=false` packages, and those whose artifact declares uninstall unsupported with no cycle under way, are treated as complete in the finalizer, and their `nodeState` entries are preserved by `CleanupSCRMetadata` (D2 semantics: non-absent entry means files remain on host). | — |

Notes:

- `DeletionBlocked` is cleared automatically once the blocking condition is resolved (annotation repaired, NodeWright unpaused, or the pending work is no longer present).
- Forcing deletion of a blocked NodeWright requires manually removing the `nodewright.nvidia.com/nodewright` finalizer (`kubectl patch nodewright <name> --type=merge -p '{"metadata":{"finalizers":null}}'`). Doing this bypasses Phase 3 cleanup entirely: per-NodeWright labels/annotations/conditions are **not** removed and nodes are **not** uncordoned — the caller is responsible for any residual cleanup.

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

Admission reads the NodeWright alone and never a package artifact, so these rules ignore what an artifact declares; see [Removing a package whose artifact declares uninstall unsupported](#removing-a-package-whose-artifact-declares-uninstall-unsupported).

## DAG Dependency Interaction

If package A is being uninstalled and package B depends on A:

- B is **blocked** (cannot run) because A is no longer in the completed set
- Uninstall does **not** cascade — B remains installed
- A `Blocked` condition is set with a message indicating the broken dependency
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
kubectl logs -n nodewright <pod-name> -c <package>-uninstall
kubectl logs -n nodewright <pod-name> -c <package>-uninstallcheck
```

Check node state:
```bash
kubectl get nodes -l nodewright.nvidia.com/test-node=skyhooke2e -o jsonpath='{.items[*].metadata.annotations.nodewright\.nvidia\.com/nodeState_<name>}' | jq
```

A node at the `uninstall` stage with no uninstall pod, while the `UninstallUnsupported` condition lists the package as `(stalled)`, is waiting on an artifact that declares uninstall unsupported; see [Stalled uninstall](#stalled-uninstall).

### Blocked dependency

Check the NodeWright conditions:
```bash
kubectl get nodewright <name> -o jsonpath='{.status.conditions}' | jq
```

Look for `Blocked` condition with the dependency chain message.

### Webhook rejection on package removal

If the webhook rejects removal of an `enabled: true` package:

1. Set `uninstall.apply: true` on the package
2. Wait for uninstall to complete (package absent from all node states)
3. Then remove the package from spec

If the package's artifact declares uninstall unsupported, the uninstall never completes; see [Removing a package whose artifact declares uninstall unsupported](#removing-a-package-whose-artifact-declares-uninstall-unsupported).

## Known Issues

### CR deletion deadlocks when an install is stuck at `erroring`

**Symptom.** `kubectl delete nodewright <name>` hangs indefinitely. The NodeWright stays around with a `DeletionTimestamp` set and the `nodewright.nvidia.com/nodewright` finalizer attached. No uninstall pods are created for an `uninstall.enabled: true` package that's still tracked in `nodeState`.

**Why.** The finalizer drives uninstall through the same `HandleUninstallRequests` path as explicit uninstall. That path only transitions a package from an install stage (`apply`, `config`, `interrupt`, `post-interrupt`, `upgrade`) to `uninstall` when the package is in **`state: complete`** on the node. If the install never reached `complete` — e.g., `uninstall.sh` wasn't yet exercised because `apply.sh` is crash-looping in `state: erroring` — the uninstall trigger is skipped, so the finalizer's "wait for pending uninstall" phase never progresses.

**How to confirm.** Look for a node where the package is `state: erroring` at a non-uninstall stage:

```bash
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.name as $n
      | .metadata.annotations["nodewright.nvidia.com/nodeState_<name>"]
      | fromjson
      | to_entries[]
      | select(.value.state == "erroring" and (.value.stage | test("uninstall") | not))
      | "\($n) \(.key) \(.value.stage)/\(.value.state)"'
```

Any rows returned are nodes the finalizer is waiting on.

**Workarounds (pick one; they have different blast radius).**

1. **Fix the underlying install.** Inspect `kubectl logs -n nodewright <pod> -c <pkg>-apply` and correct the script, config, or environment so the install completes. Once the node reaches `stage: config` / `state: complete` (or `post-interrupt/complete` if the package has an interrupt), the finalizer's next reconcile will transition it to `uninstall` and proceed.

2. **Reset the affected node's NodeWright state.** Use the CLI:

    ```bash
    kubectl nodewright node reset <node-name> --nodewright <name> --confirm
    ```

    This clears the per-NodeWright `nodeState` annotation on that node. With the entry gone, the finalizer's "is anything still tracked" check turns false and Phase 3 cleanup runs. **Caveat:** `uninstall.sh` does **not** run — anything the install script wrote to the host is left in place. Prefer this only when you know the install didn't actually modify host state, or when you're willing to clean up out-of-band.

3. **Strip the finalizer (last resort).** Bypasses the finalizer entirely:

    ```bash
    kubectl patch nodewright <name> --type=merge -p '{"metadata":{"finalizers":null}}'
    ```

    Same caveat as above, plus Phase 3 cleanup is **skipped**: node cordons, per-NodeWright labels/annotations, and conditions are **not** removed. You'll need to run `kubectl nodewright node reset` on each affected node (or hand-remove the residual keys) afterward. With the NodeWright gone, that removes its `cordon_<name>` annotation but leaves the node unschedulable, and no later NodeWright run releases a cordon no NodeWright holds, so uncordon the node yourself as described in [Orphaned Cordon Recovery](../architecture/interrupt-flow.md#orphaned-cordon-recovery).

**Long-term fix.** Tracked as a design gap: the finalizer should be able to drive uninstall from an install-erroring state (either after N retries, or via an explicit "give up on install" CR annotation). Until that lands, the workarounds above are the only options.

### Node stuck at `uninstall-interrupt` after removing `interrupt` from spec

**Rare.** Requires a specific sequence: the uninstall pod has already finished, the node has transitioned to `stage: uninstall-interrupt` / `state: in_progress`, the interrupt pod is **not currently running** (never fired, was manually deleted, or the kubelet evicted it), and the user then edits the package to remove the `interrupt:` block.

**Symptom.** The node's `nodeState` entry for the package is pinned at `stage: uninstall-interrupt` / `state: in_progress`. No new pod is created, no state transition occurs, and the NodeWright never returns to `complete`. Reconciles are a no-op for this package.

**Why.** Once the uninstall pod succeeds, `HandleCompletePod` commits the node to `stage: uninstall-interrupt` only when `package.HasInterrupt()` was true at that moment. The next reconcile's `ProcessInterrupt` re-checks `HasInterrupt` from the *current* spec to decide whether to (re-)create the interrupt pod — if the user has since removed the `interrupt:` block, the check fails and no pod is spawned. `ApplyPackage` short-circuits `stage == uninstall-interrupt` to a no-op (the interrupt machinery is supposed to drive it), and `HandleUninstallRequests` only calls `RemoveState` once `state == complete` — which will never happen without a pod to succeed. The node is permanently stranded.

**How to confirm.**

```bash
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.name as $n
      | .metadata.annotations["nodewright.nvidia.com/nodeState_<name>"]
      | fromjson
      | to_entries[]
      | select(.value.stage == "uninstall-interrupt" and .value.state != "complete")
      | "\($n) \(.key) \(.value.state)"'
```

And verify no interrupt pod exists for the package:

```bash
kubectl get pods -n nodewright -l nodewright.nvidia.com/name=<name>,nodewright.nvidia.com/package=<pkg>-<ver>,nodewright.nvidia.com/interrupt=True
```

An entry from the first command with no rows from the second confirms the stranded state.

**Workarounds.**

1. **Re-add the interrupt to the spec.** Put the `interrupt:` block back on the package. `ProcessInterrupt` will re-fire the pod; once it completes, the node advances to `stage: uninstall-interrupt` / `state: complete` and `HandleUninstallRequests` calls `RemoveState` on the next reconcile. You can then remove the `interrupt:` block safely.

2. **Reset the affected node.**

    ```bash
    kubectl nodewright node reset <node-name> --nodewright <name> --confirm
    ```

    Same caveat as the install-erroring case: any pending uninstall script does **not** run, and host-side state written by earlier lifecycle steps stays put.

**Long-term fix.** Tracked as a design gap: once `stage: uninstall-interrupt` is committed the controller should drive it to completion regardless of whether the spec still declares an interrupt — the decision was made when the uninstall pod finished and should not be revocable by a later spec edit.

### `uninstall.apply: true` on a package that was never installed is a silent no-op

**Symptom.** A package with `uninstall.enabled: true` / `uninstall.apply: true` is in the spec of a newly-applied (or extended) NodeWright. Reconcile runs, no uninstall pod spawns, and the package is **never installed** either. The NodeWright looks idle for that package — no events, no error condition, nothing in the status to explain the silence.

**Why.** The reconciler treats `IsUninstalling() && absent from nodeState` as the terminal "uninstalled" state (per D2). A brand-new package is also absent from nodeState, which collides with that signal: `shouldSkipApplyForUninstall` returns true (apply requested + absent), so the install pipeline is skipped. The package is interpreted as "already uninstalled, nothing to do." The webhook only validates `apply: true` requires `enabled: true` — it has no way to tell "never-installed" from "fully-uninstalled" at admission time.

**Most common trigger.** Copy-pasting a working NodeWright YAML from one cluster to another and forgetting to flip `apply` back to `false` before applying. Also reachable by applying a NodeWright with `apply: true` set in a manifest generated by a tool that tracks "last known good" config.

**How to confirm.** Package is in spec with `apply: true, enabled: true`, but no entry in any node's `nodeState_<name>` annotation:

```bash
kubectl get nodewright <name> -o jsonpath='{.spec.packages.<pkg>.uninstall}'
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | "\(.metadata.name): \(.metadata.annotations["nodewright.nvidia.com/nodeState_<name>"] // "<no state>")"'
```

If every node returns `<no state>` (or a state map that doesn't contain the package's `name|version` key), the package was never installed.

**Workaround.** Flip `apply` back to `false`:

```yaml
uninstall:
  enabled: true
  apply: false
```

Re-apply the NodeWright. The install pipeline will engage on the next reconcile.

**Long-term fix.** Either emit an admission warning for `apply: true` on a package where the webhook sees no node state, or raise an explicit NodeWright condition (`Skipped: apply=true on never-installed package`) so the silence is surfaced in `kubectl describe`.

### Changing `version` while `uninstall.apply: true` is in flight leaves the new version uninstalled

**Symptom.** A package is being uninstalled (`apply: true`) across the fleet. Before the uninstall finishes on all nodes, the user bumps the package's `version`. Reconcile proceeds, old-version state is cleaned up, but the new version **never installs**. The package sits in terminal "uninstalled" state indefinitely.

**Why.** The webhook only rejects *downgrades* during active uninstall; upgrades are allowed. When `HandleCompletePod` sees an uninstall-pod finish for a version that's no longer in spec, its defensive branch removes the old-version state. On the next reconcile, the new version is in spec but absent from nodeState — `shouldSkipApplyForUninstall` sees `apply: true` + absent and treats the package as uninstalled. The install pipeline skips it.

**How to confirm.** Package in spec has the new version, every node's nodeState annotation either lacks the package entirely or has the package at the *old* version, and `uninstall.apply` is still `true`:

```bash
kubectl get nodewright <name> -o jsonpath='{.spec.packages.<pkg>.version}'
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.annotations["nodewright.nvidia.com/nodeState_<name>"] // "{}" | fromjson | keys'
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

### `CleanupSCRMetadata` can over-delete annotations when a NodeWright name collides with a taint-key suffix

**Unlikely, operational.** During CR-delete cleanup (`HandleFinalizer` Phase 3), `CleanupSCRMetadata` removes any `nodewright.nvidia.com/*` annotation or label whose key ends with `_<name>`. That suffix-match also catches the `nodewright.nvidia.com/autoTaint_<taintKey>` annotation written by `AutoTaintNewNodes` — **but only if the NodeWright's name exactly equals the taint key's value**. In practice both sides are user-chosen strings; a collision requires a NodeWright named to match a taint key (e.g., a NodeWright literally named `runtime-required` in a cluster where the runtime-required taint uses that string).

**Impact if it happens.** The `autoTaint_*` annotation is removed when the NodeWright is deleted, losing the audit trail of which nodes were auto-tainted. The taint itself is a separate concern (managed by `HandleAutoTaint`) and is not affected. In real clusters this is almost never reachable because NodeWright names tend to be descriptive (`gpu-drivers`, `kernel-tune`) while taint keys tend to be namespaced (`nvidia.com/gpu`, `skyhook.nvidia.com`).

**Avoidance.** Don't name NodeWrights to exactly match a taint key in use. If you have a clash, either rename the NodeWright or disable `AutoTaintNewNodes` on it before deletion.

**Long-term fix.** Replace the suffix match in `CleanupSCRMetadata` with an explicit list of cleanup keys (`status_`, `nodeState_`, `cordon_`, `version_`) so unrelated keys with a coincidentally-matching suffix are never touched.

### Clearing the finalizer mid-uninstall and recreating the NodeWright can run a stray apply Job

**Rare; requires manually removing the `nodewright.nvidia.com/nodewright` finalizer while an uninstall Job is still running.** Under normal deletion the finalizer holds the CR until the uninstall Job completes, so this path isn't reachable. `kubectl delete --force --grace-period=0` does **not** reach it either: `--force` skips a pod's graceful termination and has no effect on finalizers, so the CR just sits in `Terminating` and the operator still drives uninstall to completion. Only clearing the finalizer yourself (see [Deletion edge cases](#deletion-edge-cases)) lets the CR vanish mid-uninstall.

**Symptom.** After clearing the finalizer on a NodeWright whose uninstall Job was mid-run, then recreating a NodeWright with the same name and `uninstall.apply: true`, one of the affected nodes briefly runs an **apply** Job for the package before the controller transitions it back to uninstall. This reaches **only packages without an interrupt**: `NextStage`'s with-interrupt map deliberately omits `uninstall`, so a package that interrupts has no next stage from there and never produces the stray apply Job. The end state is correct (the package eventually uninstalls), but operators see one unexpected install cycle.

**Why.** When the uninstall Job completes, `HandleCompletePod` looks up the parent NodeWright via `dal.GetSkyhook`; if the CR is gone, it returns `(nil, nil)` and the function exits without writing the usual "remove state" or "advance to `uninstall-interrupt`" outcome, leaving `updated` false. Its caller `recordJobCompletion` then falls through to its default `Upsert(state=Complete, stage=pkg.Stage)`, which the entry still satisfies: `entryOpenAtStage` only requires an entry present at the Job's own stage and not yet complete, and a mid-run uninstall sits at `stage: uninstall` / `state: in_progress`. So `stage: uninstall` / `state: complete` is persisted on the node annotation. Recreating the NodeWright surfaces that orphaned annotation. `HandleUninstallRequests`'s `StageUninstall` branch re-adds the package to `toUninstall` regardless of state. `ApplyPackage` then reads `packageStatus.Stage = uninstall` and calls `NextStage`, which (for a no-interrupt package at `state: complete`) maps `uninstall → apply` per `NodeState.NextStage` — so an apply Job is created. The apply Job completes, the node moves to `stage: apply` / `state: complete`, the next reconcile takes the install-cycle branch in `HandleUninstallRequests`, and Upserts the package back to `stage: uninstall` / `state: in_progress`. Self-corrects within one extra apply cycle.

**How to confirm.** After the finalizer removal + recreate, look for the orphaned terminal-uninstall entry **before** the controller has had time to re-trigger:

```bash
kubectl get nodes -l <selector> -o json \
  | jq -r '.items[] | .metadata.name as $n
      | .metadata.annotations["nodewright.nvidia.com/nodeState_<name>"]
      | fromjson
      | to_entries[]
      | select(.value.stage == "uninstall" and .value.state == "complete")
      | "\($n) \(.key)"'
```

Rows for a package **without** an interrupt are nodes the controller will run an unwanted apply Job on before retriggering uninstall. The query cannot tell the two apart, so check the package's `interrupt:` block in the spec before acting on a row.

**Avoidance.** Don't clear the finalizer on a NodeWright with active uninstall Jobs. Let it drive uninstall to completion, or use the documented workarounds for blocked-finalizer cases (`kubectl nodewright node reset`, then plain `kubectl delete`).

**Workaround if already in this state.** Before recreating the NodeWright, run `kubectl nodewright node reset <node-name> --nodewright <name> --confirm` on each affected node to clear the orphaned annotation. Recreating it with `uninstall.apply: true` still set then produces no spurious apply Job, because an absent entry reads as terminal-uninstalled: `IsComplete` drops the package from the active set, so it is neither re-applied nor uninstalled again. That is the end state you want if the package should stay gone. To reinstall it instead, recreate with `uninstall.apply: false` — per [Cancel Uninstall](#cancel-uninstall), an absent entry re-installs automatically.

**Long-term fix.** In `HandleUninstallRequests`, special-case `stage: uninstall` / `state: complete`: call `RemoveState` (mirroring the existing `uninstall-interrupt / complete` branch) and skip the `toUninstall` append. The current "re-add defensively" comment predates the realisation that `NextStage` re-maps `uninstall → apply` for completed packages without an interrupt; the safe handling is to treat a completed uninstall as terminal-uninstalled per D2.
