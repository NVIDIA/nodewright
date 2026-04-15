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

- Nodes where uninstall was in progress are reset to the install pipeline (`StageApply`).
- Nodes where uninstall already completed will re-install the package automatically.
- The webhook emits a warning (not a rejection) on cancel.

### CR Deletion (Finalizer)

When a Skyhook CR is deleted (`kubectl delete skyhook my-skyhook`):

- **`enabled: true` packages**: The finalizer triggers uninstall pods, waits for completion on all nodes, then cleans up (uncordon nodes, remove SCR labels/annotations, remove finalizer).
- **`enabled: false` packages (or nil)**: No uninstall pods — state is cleaned up immediately. The package state remains on nodes so administrators can see what was previously applied.

### Downgrade (version change)

Downgrades continue to work as before through the version-change detection in `HandleVersionChange`. This is separate from explicit uninstall — downgrades uninstall the old version and install the new version in one flow.

## Webhook Validation Rules

| Rule | Action |
|------|--------|
| `apply: true` with `enabled: false` | **Rejected** — apply requires enabled |
| Remove `enabled: true` package from spec without completing uninstall | **Rejected** — must uninstall first |
| Remove `enabled: false` (or nil) package from spec | **Allowed** — no uninstall needed |
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

- **`enabled: false` (default)**: Removing from spec calls `RemoveState` directly. No uninstall pod runs. Package state is cleaned from node annotations immediately.
- **`enabled: true`**: Removing from spec is blocked by the webhook until uninstall completes.

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
