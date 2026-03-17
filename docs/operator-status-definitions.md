# Operator Status, State, and Stage Definitions

This document provides concise definitions for the status, state, and stage concepts used throughout the Skyhook operator to track package operations and node lifecycle management.

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