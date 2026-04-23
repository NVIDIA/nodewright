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
| Skyhook is **paused** AND at least one `uninstall.enabled=true` package is still tracked in `nodeState` | **Blocked.** A paused Skyhook can't drive uninstall (`processSkyhooksPerNode` short-circuits on pause), and "paused" is a temporary "resume later" signal — deletion waits until the Skyhook is unpaused and uninstall completes. | `DeletionBlocked` / `Reason: PausedWithPendingUninstall` |
| Skyhook is **disabled** AND at least one `uninstall.enabled=true` package is still tracked in `nodeState` | **Deletion proceeds.** "Disabled" is the explicit "shut this off" signal — uninstall pods do not run, per-Skyhook labels/annotations/conditions are cleaned up and nodes are uncordoned, but the `nodeState` annotation (and its companion `version` annotation) are **preserved** so host-side remnants remain traceable (D2 semantics). | Warning event `DeletedWhileDisabled` |
| Paused or disabled, but no uninstall-enabled packages are tracked in `nodeState` (e.g., all packages have `uninstall.enabled=false`, or their uninstall already completed) | **Deletion proceeds** normally — pause/disable only matter when there is uninstall work to drive. | — |

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
