# Deployment Policy and Compartments

Deployment Policy provides fine-grained control over how Skyhook rolls out updates across your cluster by defining **compartments** — groups of nodes selected by labels — with different rollout strategies and budgets.

---

## Overview

A **DeploymentPolicy** is a Kubernetes Custom Resource that separates rollout configuration from the Skyhook Custom Resource, allowing you to:
- Reuse the same policy across multiple Skyhooks
- Apply different strategies to different node groups (e.g., production vs. test)
- Control rollout speed and safety with configurable thresholds

**Important**: DeploymentPolicy controls **all node updates** in a Skyhook rollout, not just interrupt handling.

---

## Basic Structure

```yaml
apiVersion: skyhook.nvidia.com/v1alpha1
kind: DeploymentPolicy
metadata:
  name: my-policy
spec:
  # Reset batch state automatically when rollout completes or spec version changes
  resetBatchStateOnCompletion: true  # default: true
  # Default applies to nodes that don't match any compartment
  default:
    budget:
      percent: 100  # or count: N
    strategy:
      fixed:        # or linear, exponential
        initialBatch: 1
        batchThreshold: 100
        safetyLimit: 50
  # Compartments define specific node groups
  compartments:
  - name: production
    selector:
      matchLabels:
        env: production
    budget:
      percent: 25  # Scales with cluster size
    strategy:
      exponential:
        initialBatch: 1
        growthFactor: 2
        batchThreshold: 100
        failureThreshold: 1
        safetyLimit: 50
```

---

## Core Concepts

### Compartments
A named group of nodes selected by labels with:
- **Selector**: Kubernetes `LabelSelector` to match nodes
- **Budget**: Maximum nodes in progress at once (count or percent)
- **Strategy**: Rollout pattern (fixed, linear, or exponential)

### Budgets
Defines the ceiling for concurrent nodes:
- **Count**: Fixed number (e.g., `count: 3`)
- **Percent**: Percentage of matched nodes (e.g., `percent: 25`)

**Rounding for Percent**: `ceiling = max(1, int(matched_nodes × percent / 100))`
- Always rounds **down**
- Minimum is **1** (unless 0 nodes match)

**Examples**:
| Matched Nodes | Percent | Ceiling |
|---------------|---------|---------|
| 10 | 25% | 2 |
| 10 | 30% | 3 |
| 5 | 10% | 1 (rounds down from 0.5, then max(1, 0)) |
| 100 | 1% | 1 |

---

## Rollout Strategies

### Fixed Strategy
Constant batch size throughout the rollout.

```yaml
strategy:
  fixed:
    initialBatch: 5      # Always process 5 nodes
    batchThreshold: 100  # Require 100% success
    failureThreshold: 3  # Stop after 3 consecutive failures
    safetyLimit: 50      # Apply failure threshold only below 50% progress
```

**Use when**: You want predictable, safe rollouts.

---

### Linear Strategy
Increases by delta on success, decreases on failure.

```yaml
strategy:
  linear:
    initialBatch: 1
    delta: 1             # Increase by 1 each success
    batchThreshold: 100
    failureThreshold: 3
    safetyLimit: 50
```

**Progression** (delta=1): `1 → 2 → 3 → 4 → 5`

**Use when**: You want gradual ramp-up with slowdown on failures.

---

### Exponential Strategy
Multiplies by growth factor on success, divides on failure.

```yaml
strategy:
  exponential:
    initialBatch: 1
    growthFactor: 2      # Double on success
    batchThreshold: 100
    failureThreshold: 2
    safetyLimit: 50
```

**Progression** (factor=2): `1 → 2 → 4 → 8 → 16`

**Use when**: You want fast rollouts in large clusters with high confidence.

---

## Strategy Parameters

All strategies share these parameters:

- **`initialBatch`** (≥1): Starting number of nodes (default: 1)
- **`batchThreshold`** (1-100): Minimum success percentage to continue (default: 100)
- **`failureThreshold`** (≥1, optional): Max consecutive failures before stopping (default: none/unlimited)
- **`safetyLimit`** (1-100): Progress threshold for failure handling (default: 50)

### Default Values

When strategy parameters are not specified, the operator applies these defaults:
- `initialBatch`: 1
- `batchThreshold`: 100
- `safetyLimit`: 50
- `failureThreshold`: **none** (rollout never stops due to consecutive failures)

**Note**: `failureThreshold` is **nullable**. If omitted, the rollout will continue despite consecutive failures, only respecting batch success thresholds but never stopping the entire rollout.

### Safety Limit Behavior

**Before safetyLimit** (e.g., < 50% progress):
- Failures count toward `failureThreshold` (if set)
- Batch sizes slow down (linear/exponential)
- Reaching `failureThreshold` stops the rollout (if set)

**After safetyLimit** (e.g., ≥ 50% progress):
- Rollout continues despite failures
- Batch sizes don't slow down
- `failureThreshold` is ignored (rollout assumed "safe enough" to complete)

**Rationale**: Early failures indicate a problem. Late failures are less critical since most nodes are updated.

---

## Batch Stickiness

Nodes selected for a batch remain in that batch until every node has reached a definitive outcome — all packages complete, erroring, or blocked. The controller will not select new nodes for the next batch while the current batch has nodes still running between packages.

Batch membership is tracked via `NodePriority` in the Skyhook status. A node stays in `NodePriority` from the time it is picked for a batch until it completes all packages. This state is persisted in the CRD, so it survives controller restarts.

Each package pod also receives a `SKYHOOK_NODE_ORDER` environment variable reflecting the node's monotonic position in the rollout. See [Node Order Within a Rollout](ordering_of_skyhooks.md#node-order-within-a-rollout) for details.

---

## Selectors and Node Matching

Compartments use standard Kubernetes label selectors:

### Match Labels
```yaml
selector:
  matchLabels:
    env: production
    tier: frontend
```

---

## Overlapping Selectors

When a node matches **multiple compartments**, the operator uses a **safety heuristic** to choose the safest one.

### Tie-Breaking Algorithm (3 levels)

1. **Strategy Safety**: Prefer safer strategies
   - **Fixed** (safest) > **Linear** > **Exponential** (least safe)

2. **Effective Ceiling**: If strategies are the same, prefer smaller ceiling
   - Smaller ceiling = fewer nodes at risk

3. **Lexicographic**: If still tied, alphabetically by compartment name
   - Ensures deterministic behavior

### Example

```yaml
compartments:
- name: us-west
  selector:
    matchLabels:
      region: us-west
  budget:
    count: 20         # Ceiling = 20
  strategy:
    exponential: {}

- name: production
  selector:
    matchLabels:
      env: production
  budget:
    count: 10         # Ceiling = 10 (smaller)
  strategy:
    linear: {}

- name: critical
  selector:
    matchLabels:
      priority: critical
  budget:
    count: 3
  strategy:
    fixed: {}         # Fixed (safest)
```

**Node with labels** `region=us-west, env=production, priority=critical`:
- Matches all three compartments
- **Winner**: `critical` (fixed strategy is safest)

**Node with labels** `region=us-west, env=production`:
- Matches `us-west` (exponential) and `production` (linear)
- **Winner**: `production` (linear is safer than exponential)

---

## Batch State Reset

When using progressive rollout strategies (linear, exponential), the operator tracks batch processing state per compartment — current batch number, consecutive failures, completed/failed node counts, etc. This state persists across reconciliations so the rollout can scale up progressively.

However, when a rollout **completes** or a **spec version changes**, you typically want the next rollout to start fresh from batch 1 rather than continuing with scaled-up batch sizes. Batch state reset handles this automatically.

### Auto-Reset Triggers

Batch state is automatically reset when **either** of these events occurs (if configured):

1. **Rollout completion** — When a Skyhook's status transitions to `Complete`
2. **Spec version change** — When a package version changes in the Skyhook spec

After reset, the next reconciliation starts from batch 1 with all counters cleared.

### Configuration

Auto-reset is controlled by two fields with a precedence hierarchy:

| Field | Location | Description |
|-------|----------|-------------|
| `spec.resetBatchStateOnCompletion` | DeploymentPolicy | Default setting for all Skyhooks using this policy |
| `spec.deploymentPolicyOptions.resetBatchStateOnCompletion` | Skyhook | Per-Skyhook override (takes precedence) |

**Precedence order** (highest to lowest):
1. Skyhook's `deploymentPolicyOptions.resetBatchStateOnCompletion`
2. DeploymentPolicy's `resetBatchStateOnCompletion`
3. Default: `true` (safe by default for new resources)

### Examples

**Enable auto-reset (default behavior for new policies)**:
```yaml
apiVersion: skyhook.nvidia.com/v1alpha1
kind: DeploymentPolicy
metadata:
  name: my-policy
spec:
  resetBatchStateOnCompletion: true  # Enabled by default
  default:
    budget:
      percent: 25
```

**Disable auto-reset for a specific Skyhook** (override the policy):
```yaml
apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: my-skyhook
spec:
  deploymentPolicy: my-policy
  deploymentPolicyOptions:
    resetBatchStateOnCompletion: false  # Override: keep batch state across rollouts
```

**Disable auto-reset at the policy level**:
```yaml
apiVersion: skyhook.nvidia.com/v1alpha1
kind: DeploymentPolicy
metadata:
  name: preserve-state-policy
spec:
  resetBatchStateOnCompletion: false  # All Skyhooks using this policy keep batch state
```

### Manual Reset

You can also reset batch state manually using the CLI:

```bash
# Reset batch state for a specific Skyhook
kubectl skyhook deployment-policy reset my-skyhook --confirm

# Preview what would be reset (dry-run)
kubectl skyhook deployment-policy reset my-skyhook --dry-run

# The 'reset' command also resets batch state by default
kubectl skyhook reset my-skyhook --confirm

# To reset nodes only without resetting batch state
kubectl skyhook reset my-skyhook --skip-batch-reset --confirm
```

Both `reset` and `deployment-policy reset` also clear `NodeOrderOffset` and `NodePriority`, so the next rollout starts with fresh node ordering (`SKYHOOK_NODE_ORDER` begins at `0`).

See [CLI documentation](cli.md) for full command details.

---

## Using with Skyhooks

Reference a policy by name:

```yaml
apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: my-skyhook
spec:
  deploymentPolicy: my-policy  # References DeploymentPolicy
  deploymentPolicyOptions:     # Optional per-Skyhook overrides
    resetBatchStateOnCompletion: true
  nodeSelectors:
    matchLabels:
      workload: gpu
  packages:
    # ...
```

**Behavior**:
- DeploymentPolicy is **cluster-scoped** (not namespaced)
- Each node is assigned to a compartment based on selectors
- Nodes not matching any compartment use the `default` settings
- `deploymentPolicyOptions` allows per-Skyhook overrides of policy settings

---

## Migration from InterruptionBudget

The legacy `interruptionBudget` field is still supported but **DeploymentPolicy is recommended**.

### Before
```yaml
spec:
  interruptionBudget:
    percent: 25
```

### After
```yaml
# 1. Create DeploymentPolicy
apiVersion: skyhook.nvidia.com/v1alpha1
kind: DeploymentPolicy
metadata:
  name: legacy-equivalent
spec:
  default:
    budget:
      percent: 25
    strategy:
      fixed:
        initialBatch: 1
        batchThreshold: 100
        safetyLimit: 50
```

```yaml
# 2. Update Skyhook
spec:
  deploymentPolicy: legacy-equivalent
  # Remove interruptionBudget field
```

---

## Monitoring

Deployment Policy rollout behavior is exposed via Prometheus metrics. See [Metrics documentation](metrics/README.md) for details.

---

## Examples

See `/operator/config/samples/deploymentpolicy_v1alpha1_deploymentpolicy.yaml` for a complete sample showing:
- Critical nodes (count=1, fixed strategy)
- Production nodes (count=3, linear strategy)
- Staging nodes (percent=33, exponential strategy)
- Test nodes (percent=50, fast exponential)

---

