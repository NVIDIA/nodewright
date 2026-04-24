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
