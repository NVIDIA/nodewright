# Operator Status, State, Stage, and Condition Definitions

This document provides concise definitions for the status, state, stage, and condition concepts used throughout the Skyhook operator to track package operations and node lifecycle management.

## Key Relationships

- **Status** reflects the overall health and progress of nodes and the Skyhook resource
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

**Scope**: Applied to the overall Skyhook resource and individual nodes  
**Purpose**: High-level operational status indicating the current condition

| Status | Definition |
|--------|------------|
| `complete`    | All operations have finished successfully |
| `blocked`     | Operations are prevented from proceeding due to taint toleration issues |
| `waiting`     | Queued for execution but not yet started |
| `disabled`    | Execution is disabled but will continue for other Skyhooks |
| `paused`      | Execution is paused for this and all other Skyhooks supposed to be executed after this one |
| `in_progress` | Currently executing operations |
| `erroring`    | Experiencing failures or errors |
| `unknown`     | Status cannot be determined or is uninitialized |

## Conditions

Skyhook publishes Kubernetes conditions on `.status.conditions`. The `Ready` condition is the standard user-facing summary for `kubectl wait --for=condition=Ready`, while other condition types report specific operator states that can coexist with `Ready`.

### Ready Condition

`Ready` is a boolean projection of the Skyhook's multivalued `.status.status` field:

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

- `TaintNotTolerable`: selected nodes are skipped because their taints are not tolerated by the Skyhook
- `NodesIgnored`: selected nodes are skipped because they have the ignore label set
- `ApplyPackage`: the controller is applying a package to a node
- `DeploymentPolicyNotFound`: the referenced `DeploymentPolicy` is missing at reconcile time

These conditions complement, rather than replace, `.status.status` and `Ready`.

### Legacy Prefixed Condition Types

Canonical condition types are now the bare names above, such as `Ready` and `TaintNotTolerable`. During the one-release deprecation window, the operator also mirrors them to the legacy prefixed condition types for backward compatibility:

- `Ready` continues to be emitted as `skyhook.nvidia.com/Ready`
- the rollout-transition summary also remains available as `skyhook.nvidia.com/Transition`
- other bare condition types continue to be mirrored as `skyhook.nvidia.com/<Type>`

New consumers should read the canonical bare condition types now. Existing consumers of the prefixed condition types should migrate during the deprecation window.

## State

**Scope**: Applied to individual packages within a node  
**Purpose**: Current execution state of a specific package operation

| State | Definition |
|-------|------------|
| `complete`    | Package operation has finished successfully |
| `in_progress` | Package is actively running (pod has started) |
| `skipped`     | Package/stage was intentionally bypassed in the lifecycle |
| `erroring`    | Package operation is experiencing failures |
| `unknown`     | Package state cannot be determined or is uninitialized |

## Stage

**Scope**: Applied to individual packages  
**Purpose**: Indicates which phase of the package installation/management process is currently executing

| Stage | Definition |
|-------|------------|
| `uninstall` & `uninstall-check`           | Removal of the package |
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
uninstall → apply → config
upgrade → config
```

### With Interrupts:
When a package requires an interrupt, the node is first cordoned and drained before package operations begin:
```
uninstall (if downgrading) → cordon → wait → drain → apply → config → interrupt → post-interrupt
cordon → wait → drain → upgrade (if upgrading) → config → interrupt → post-interrupt
```

**Note**: The cordon, wait, and drain phases ensure that workloads are safely removed from the node before any package operations that require interrupts (such as reboots or kernel module changes) are executed.

## Skyhook Status Fields

The Skyhook resource's `.status` object includes fields that track batch rollout state. Two fields are particularly relevant for [batch stickiness](deployment_policy.md#batch-stickiness) and [node ordering](ordering_of_skyhooks.md#node-order-within-a-rollout):

| Field | Definition |
|-------|------------|
| `NodePriority` | Tracks which nodes are in the current active batch. A node stays in `NodePriority` from the time it is selected for a batch until it completes all packages. Prevents the controller from selecting new nodes while current batch nodes are between packages. |
| `NodeOrderOffset` | Cumulative count of nodes removed from `NodePriority`. Combined with a node's position in the sorted `NodePriority` map, this produces the monotonic `SKYHOOK_NODE_ORDER` value injected into package pods. |

Both fields are persisted in the CRD and survive controller restarts. They are cleared by `kubectl skyhook reset` and `kubectl skyhook deployment-policy reset`.
