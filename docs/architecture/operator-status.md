# Operator Status, State, Stage, and Condition Definitions

This document provides concise definitions for the status, state, stage, and condition concepts used throughout the NodeWright operator to track package operations and node lifecycle management.

## Key Relationships

- **Status** reflects the overall health and progress of nodes and the NodeWright resource
- **State** tracks the execution status of individual package operations
- **Stage** defines the specific lifecycle phase a package is currently in

- A node's Status is derived from the collective States of its packages
- Stages progress sequentially, with State indicating success/failure at each stage
- All stages except for interrupts include validation checks that must succeed for progression

## Usage in Operations

- **Monitoring**: Use Status for high-level health checks and dashboards
- **Debugging**: Examine State and Stage for detailed package-level troubleshooting  
- **Automation**: State transitions trigger the next appropriate Stage in the lifecycle
- **Scheduling**: Status values like `blocked` and `paused` control operation scheduling and dependencies 

## Status

**Scope**: Applied to the overall NodeWright resource and individual nodes  
**Purpose**: High-level operational status indicating the current condition

| Status | Definition |
|--------|------------|
| `complete`    | All operations have finished successfully |
| `blocked`     | Operations are prevented from proceeding due to taint toleration issues or the `nodewright.nvidia.com/ignore` label. A node blocked this way reports `blocked` even while ordering would otherwise hold it at `waiting`, and returns to `waiting` once the taint or label is removed |
| `waiting`     | Queued for execution but not yet started |
| `disabled`    | Execution is disabled but will continue for other NodeWrights |
| `paused`      | Execution is paused for this and all other NodeWrights supposed to be executed after this one |
| `in_progress` | Currently executing operations |
| `erroring`    | A stage failed, because its Job ran out of retries or timed out, or a drain timed out. An attempt that fails and is still being retried shows only on the package's State (below), so a package whose retry succeeds never marks the node `erroring`. A failing interrupt's node reads `in_progress` until the interrupt fails again with its container's restart count at `JOB_BACKOFF_LIMIT`, or, for a reboot, whose restarts are never counted, until its Job's deadline; see [A failing interrupt](../user-guide/custom-resource.md#a-failing-interrupt) |
| `unknown`     | Status cannot be determined or is uninitialized |

## Conditions

NodeWright publishes Kubernetes conditions on `.status.conditions`. The `Ready` condition is the standard user-facing summary for `kubectl wait --for=condition=Ready`, while other condition types report specific operator states that can coexist with `Ready`.

### Ready Condition

`Ready` is a boolean projection of the NodeWright's multivalued `.status.status` field:

| `.status.status` | `Ready.status` | `Ready.reason` | Meaning |
|------------------|----------------|----------------|---------|
| `complete` | `True` | `NodesConverged` | All targeted nodes have completed successfully |
| `in_progress` | `False` | `Progressing` | Work is actively running on at least one node |
| `blocked` | `False` | `Blocked` | Progress is blocked, such as by taint toleration or ignored nodes |
| `erroring` | `False` | `Erroring` | One or more nodes are failing |
| `paused` | `False` | `Paused` | Processing is paused by annotation |
| `waiting` | `False` | `Waiting` | Nodes are waiting for ordering or batch admission |
| `disabled` | `False` | `Disabled` | Processing is disabled by annotation |
| `unknown` | `False` | `Unknown` | The operator cannot yet determine a stable state |

`.status.status` remains the canonical rollout summary used by the operator's scheduling and state machine. `Ready` exists to expose that state in Kubernetes condition form for standard tooling, while `.status.nodeStatus` remains the authoritative per-node source of truth.

### Ready Condition Message

The `Ready.message` field summarizes node progress by status:

- It always starts with `<complete>/<total> nodes complete`.
- It appends one segment for each non-empty node-status bucket, such as `<count> in progress`, `<count> blocked`, or `<count> erroring`.
- When a bucket has a short node list, the node names are included in sorted order: `2 blocked (node-a, node-b)`.
- When a bucket exceeds the message cap, the node names are dropped and the segment becomes `(list truncated; see controller logs)`.

Example for a small rollout:

```text
1/3 nodes complete (node-a), 1 in progress (node-b), 1 blocked (node-c)
```

Example for a large rollout:

```text
0/800 nodes complete, 800 in progress (list truncated; see controller logs)
```

The truncation cap exists to keep condition payloads bounded for etcd object size and watch bandwidth. When truncation happens, the controller logs the full per-status node lists at `Info`, and `.status.nodeStatus` still contains the complete per-node view.

### Other Condition Types

The operator also sets additional condition types that may be useful for troubleshooting:

- `Blocked`: rollout progress is blocked on one or more nodes. Reasons include:
  - `NonInterruptPodsRunning`: node drain is held because pods matching `spec.podNonInterruptLabels` are still running or pending on selected nodes. A held node keeps its slot in the compartment's current batch, and can read `waiting` or `in_progress` depending on what has already run on it (see [interrupt-flow.md](interrupt-flow.md#what-a-held-node-costs-the-rollout))
  - `DependencyUninstalled`: a package that still has work to do depends on a package that is being or has been uninstalled. A dependent that is itself being uninstalled, by `uninstall.apply` or by the NodeWright's deletion with `uninstall.enabled`, is not reported
- `TaintNotTolerable`: selected nodes are skipped because their taints are not tolerated by the NodeWright
- `NodesIgnored`: selected nodes are skipped because they have the ignore label set
- `ApplyPackage`: the controller is applying a package to a node
- `DeploymentPolicyNotFound`: the referenced `DeploymentPolicy` is missing at reconcile time
- `ImagePullFailed`: one or more package pods have been unable to pull their image (`ErrImagePull`/`ImagePullBackOff`) for more than 2 minutes, measured from the pod's creation, from the last init container that finished, or from the end of the container's previous run when it restarts and pulls again. The message names the nodes (past 10 the list is logged by the controller instead) and the package and container of the first node's failing pod, the lowest package name when several fail; the kubelet's pull error is in the `Warning` event emitted when the condition first appears, since it changes on every retry. The kubelet keeps retrying the pull, so this is deliberately not a package State or node Status: a package or node reads `in_progress` throughout, and the condition clears by itself once no pod is waiting on a failed pull. If the pull never recovers, the stage's deadline (`stageTimeout`) fails it and marks the node `erroring`. With `stageTimeout: 0`, or no `stageTimeout` and `JOB_STAGE_TIMEOUT` set to `0`, there is no deadline: the node stays `in_progress` and this condition is the only signal
- `DrainBlocked`: one or more selected nodes have a drain that cannot currently make progress. Reasons: `PodDisruptionBudget`, `UnmanagedPod`, `EmptyDirData`, or `MultipleCauses` when more than one kind of blocker is present. Independent of `Blocked` — a NodeWright can be both dependency-blocked and drain-blocked at once.

These conditions complement, rather than replace, `.status.status` and `Ready`.

### Legacy Prefixed Condition Types

Canonical condition types are now the bare names above, such as `Ready` and `TaintNotTolerable`. During the one-release deprecation window, the operator also mirrors them to the legacy prefixed condition types for backward compatibility:

- `Ready` continues to be emitted as `nodewright.nvidia.com/Ready`
- the rollout-transition summary also remains available as `nodewright.nvidia.com/Transition`
- other bare condition types continue to be mirrored as `nodewright.nvidia.com/<Type>`

New consumers should read the canonical bare condition types now. Existing consumers of the prefixed condition types should migrate during the deprecation window.

## Node Conditions

Separate from the CR conditions above, the operator also writes two conditions onto each targeted **Node's** `.status.conditions` — not the NodeWright's. They show up in `kubectl describe node` and are the usual hook for node-health tooling.

`<name>` is the NodeWright's `metadata.name`. A node targeted by several NodeWrights carries one pair per CR.

| Condition type | Status / Reason | Message |
|---|---|---|
| `nodewright.nvidia.com/<name>/NotReady` | `True` / `Incomplete` until the node is complete for this NodeWright; then `False` / `Complete` | `NodeWright <name> Ready` |
| `nodewright.nvidia.com/<name>/Erroring` | `True` / `Package(s) Erroring or Unknown` if any package State on the node is `erroring` or `unknown`; otherwise `False` / `Not Erroring` | `Package Erroring or Unknown for <name>` |

**Polarity is inverted from the usual `Ready` convention: `False` on both means healthy.** They only reflect this NodeWright's own packages — `False` on both says nothing about the Node's standard `Ready`/`DiskPressure`/`MemoryPressure`/etc. conditions, which readers must still check separately for overall node health.

- The operator recomputes both conditions on every reconcile pass, and patches them onto the Node status subresource only when they change.
- They are written and removed per `<name>`, independent of the CR condition types above.
- Phase 3 cleanup currently does not remove them: it drops the conditions in memory but saves the Node through a plain `Patch` against the main resource, which the apiserver ignores for `.status`. This leaves them on every node a deleted NodeWright targeted, frozen at their last values, on **any** delete — not only when the finalizer is bypassed. Tracked as [#983](https://github.com/NVIDIA/nodewright/issues/983). Neither the operator-side node reset nor `kubectl nodewright node reset` touches them either; the CLI has no condition handling for these.
- A Node that a label or `spec.nodeSelector` change **deselects** (without deleting the NodeWright) is also skipped: cluster-state no longer tracks it, so it gets no further condition updates or Phase 3 cleanup, and both conditions stay frozen at their last values even while the NodeWright still exists. The same manual removal below applies to these nodes.
- During the rename migration window, the legacy `skyhook.nvidia.com/<name>/{NotReady,Erroring}` conditions are copied to the new prefix and pruned after `LEGACY_CLEANUP_DELAY` — see [Rollback window](../getting-started/migration.md#rollback-window-legacy_cleanup_delay).

### Removing leftover Node conditions by hand

Because Phase 3 cleanup doesn't remove these conditions on delete ([#983](https://github.com/NVIDIA/nodewright/issues/983)), both conditions are left on every node a deleted NodeWright had targeted, with their last values, since nothing updates them once the NodeWright is gone. The same thing happens to a node that's merely **deselected** by a label or `spec.nodeSelector` change while the NodeWright still exists — it stops getting condition updates but keeps its last values. During the migration window a node may also carry the legacy `skyhook.nvidia.com/<name>/{NotReady,Erroring}` copies.

Node `.status.conditions` merges on `type`, so a strategic-merge `$patch: delete` removes a condition by type in one step — no index lookup needed, and it's a no-op if the type is already absent, so the same command is safe to run for both prefixes and to loop over nodes:

```bash
kubectl patch node <node-name> --subresource=status -p '{"status":{"conditions":[
  {"type":"nodewright.nvidia.com/<name>/NotReady","$patch":"delete"},
  {"type":"nodewright.nvidia.com/<name>/Erroring","$patch":"delete"},
  {"type":"skyhook.nvidia.com/<name>/NotReady","$patch":"delete"},
  {"type":"skyhook.nvidia.com/<name>/Erroring","$patch":"delete"}
]}}'
```

To find which nodes still carry stale conditions for a given `<name>`:

```bash
kubectl get nodes -o json | jq -r '.items[] | select(any(.status.conditions[]?; .type | test("^(nodewright|skyhook)\\.nvidia\\.com/<name>/"))) | .metadata.name'
```

See [Uninstall and Finalizer Behavior](../user-guide/uninstall.md) for the surrounding cleanup steps.

## State

**Scope**: Applied to individual packages within a node  
**Purpose**: Current execution state of a specific package operation

| State | Definition |
|-------|------------|
| `complete`    | Package operation has finished successfully |
| `in_progress` | Package is actively running (pod has started) |
| `skipped`     | Package/stage was intentionally bypassed in the lifecycle |
| `erroring`    | Package operation is experiencing failures. While the stage still has retries left this is in-flight evidence and clears on its own if an attempt succeeds; once the retry budget (`JOB_BACKOFF_LIMIT`) is spent the stage is **timed out** here and will not retry until a `package rerun`/`reset`, a change to what the stage's Job runs (the package's `version`, `image`, `containerSHA`, `agentImageOverride`, `env` or `resources`, but for an interrupt only `version` or `agentImageOverride`), or a config change while the package is `erroring` at `config`, `interrupt` or `post-interrupt`, or the failed Job's TTL expiry. An interrupt stage's budget is its container's restart count, which also counts restarts that were not failures, and a reboot interrupt has none, so it times out only at its Job's deadline; see [A failing interrupt](../user-guide/custom-resource.md#a-failing-interrupt) |
| `unknown`     | Package state cannot be determined or is uninitialized |

## Stage

**Scope**: Applied to individual packages  
**Purpose**: Indicates which phase of the package installation/management process is currently executing

| Stage | Definition |
|-------|------------|
| `uninstall` & `uninstall-check`           | Removal of the package |
| `uninstall-interrupt`                     | Execution of the package's interrupt after its uninstall, for a package with an interrupt |
| `upgrade`   & `upgrade-check`             | Package version update operations |
| `apply`     & `apply-check`               | Initial installation/deployment of the package |
| `config`    & `config-check`              | Configuration and setup operations |
| `interrupt`                               | Execution of interrupt operations (e.g., reboots, service restarts) |
| `post-interrupt` & `post-interrupt-check` | Operations that run after interrupt completion |

**NOTE**: All stages except for interrupts include validation checks that must succeed for progression

## Stage Flow

The typical stage progression depends on whether the package has interrupts:

### Without Interrupts:

```
apply → config
upgrade → config
uninstall
```

### With Interrupts:

When a package requires an interrupt, the node is first cordoned and drained before package operations begin:
```
cordon → wait → drain → apply → config → interrupt → post-interrupt
cordon → wait → drain → upgrade (if upgrading) → config → interrupt → post-interrupt
cordon → wait → drain → uninstall → uninstall-interrupt
```

**Note**: The cordon, wait, and drain phases ensure that workloads are safely removed from the node before any package operations that require interrupts (such as reboots or kernel module changes) are executed.

## NodeWright Status Fields

The NodeWright resource's `.status` object includes fields that track batch rollout state. Two fields are particularly relevant for [batch stickiness](../user-guide/deployment-policy.md#batch-stickiness) and [node ordering](ordering.md#node-order-within-a-rollout):

| Field | Definition |
|-------|------------|
| `NodePriority` | Tracks which nodes are in the current active batch. A node stays in `NodePriority` from the time it is selected for a batch until it completes all packages. Ignored nodes and nodes with untolerated taints keep their entries for resumption but are excluded from selection. Eligible batch members finish before new nodes are selected. |
| `NodeOrderOffset` | Cumulative count of nodes removed from `NodePriority`. Combined with a node's position in the sorted `NodePriority` map, this produces the monotonic `SKYHOOK_NODE_ORDER` value injected into package pods. |

Both fields are persisted in the CRD and survive controller restarts. They are cleared by `kubectl nodewright reset`; `kubectl nodewright deployment-policy reset` keeps them.
