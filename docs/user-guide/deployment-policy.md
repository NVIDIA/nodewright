# Deployment Policy and Compartments

Deployment Policy provides fine-grained control over how NodeWright rolls out updates across your cluster by defining **compartments** — groups of nodes selected by labels — with different rollout strategies and budgets.

---

## Overview

A **DeploymentPolicy** is a Kubernetes Custom Resource that separates rollout configuration from the NodeWright Custom Resource, allowing you to:

- Reuse the same policy across multiple NodeWrights
- Apply different strategies to different node groups (e.g., production vs. test)
- Control rollout speed and safety with configurable thresholds

**Important**: DeploymentPolicy controls **all node updates** in a NodeWright rollout, not just interrupt handling.

---

## Basic Structure

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
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
- **Budget**: Concurrency ceiling when the compartment has no `strategy` (count or percent). See [Budgets](#budgets).
- **Strategy**: Rollout pattern (fixed, linear, or exponential)

### Budgets

Defines the ceiling for concurrent nodes **when the compartment has no `strategy`**:

- **Count**: Fixed number (e.g., `count: 3`)
- **Percent**: Percentage of matched nodes (e.g., `percent: 25`)

When a `strategy` is set, the strategy determines batch size and this ceiling is not applied to it. See [Concurrency and failure tolerance are separate](#concurrency-and-failure-tolerance-are-separate).

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

### Concurrency and failure tolerance are separate

These are two independent groups of settings, and they are frequently confused with each other:

| Goal | Fields |
|------|--------|
| Control **how many nodes upgrade at once** | `budget` (`count` or `percent`), `initialBatch`, and the strategy's growth (`delta` for linear, `growthFactor` for exponential) |
| Control **how much failure is tolerated** | `batchThreshold`, `failureThreshold`, `safetyLimit` |

`batchThreshold` is not a concurrency knob. It is the pass mark a batch has to hit to count as a success. `batchThreshold: 100` says "every node in the batch must succeed"; it says nothing about how many nodes run in parallel.

Which field actually sets concurrency depends on whether the compartment has a `strategy`:

| Compartment has | Batch size comes from |
|-----------------|-----------------------|
| `budget` only, no `strategy` | The budget ceiling |
| `budget` and a `strategy` | The strategy alone. **The budget does not cap the strategy's batch size.** |

This surprises people, so it is worth stating plainly: once you set a `strategy`, `budget` no longer limits how many nodes run at once. It still determines the ceiling reported in `status.compartmentStatuses` and still breaks ties when a node matches more than one compartment, but it does not clamp the batch. A strategy's batch size is bounded only by the nodes left to process (and, for `exponential`, by the compartment's total node count).

So to cap parallelism at a fixed number, use `fixed` with `initialBatch: N`, which upgrades up to `N` nodes at a time, fewer when fewer nodes remain. To ramp concurrency up over the course of the rollout, use `linear` or `exponential` and accept that it grows toward the size of the compartment. Use `budget` on its own, with no `strategy`, when you want a percentage-of-fleet ceiling and nothing more.

### Reference

| Field | Range | Default | Meaning |
|-------|-------|---------|---------|
| `initialBatch` | ≥ 1 | 1 | Nodes in the first batch |
| `delta` (linear only) | ≥ 1 | 1 | Batch size increase after a successful batch |
| `growthFactor` (exponential only) | ≥ 2 | 2 | Batch size multiplier after a successful batch |
| `batchThreshold` | 1-100 | 100 | Percentage of a batch's **non-blocked** nodes that must succeed for the batch to count as a success |
| `failureThreshold` | ≥ 1, nullable | none | Number of consecutive failed batches the compartment tolerates before it stops |
| `safetyLimit` | 1-100 | 50 | Point of no return: the percentage of the compartment **completed or failed** at or above which the rollout commits to finishing, so `failureThreshold` and batch slowdown stop applying |

When a parameter is omitted, the operator applies the default in the table above. `failureThreshold` is **nullable**: if you omit it, the rollout keeps going through any number of failed batches, still respecting `batchThreshold` for batch bookkeeping but never stopping on its own.

`batchThreshold` is evaluated with integer division, so it interacts with how many nodes are actually scored: a batch with 5 non-blocked nodes can only score 0, 20, 40, 60, 80, or 100. Any `batchThreshold` above 80 therefore behaves identically to 100 for that batch. Pick the threshold with your smallest expected batch in mind, remembering that blocked nodes drop out of the denominator and can make a batch smaller than you planned.

**`failureThreshold: 0` is invalid and the API server rejects it** with `spec...failureThreshold in body should be greater than or equal to 1`. Zero has no meaning for this field: it counts failed batches you are willing to tolerate, and the most permissive setting is not `0` but leaving the field out entirely. To stop as soon as one batch fails, use `failureThreshold: 1` together with `safetyLimit: 100`. `failureThreshold: 1` on its own only stops the rollout while it is below `safetyLimit`, which defaults to 50% progress; see [Safety Limit Behavior](#safety-limit-behavior).

### What counts as a failure

- A **node** counts as failed when its Status is `erroring` at the point the batch is evaluated. See [Operator Status](../architecture/operator-status.md) for how a node's Status is derived.
- A **batch** counts as failed when the percentage of its **non-blocked** nodes that completed successfully falls below `batchThreshold`. With `batchThreshold: 100`, one erroring node fails the whole batch.
- **Blocked nodes are excluded from the score, not counted against it.** The denominator is the batch's completed plus failed nodes, so a batch of 9 completed and 1 blocked scores 100%, not 90%. If every node in a batch is `blocked`, for example by a taint the NodeWright does not tolerate, the batch is not evaluated at all and the operator waits for those nodes to unblock.
- **Blocked nodes do hold back rollout progress.** The progress percentage that `safetyLimit` is compared against is completed plus failed nodes over every node in the compartment, and blocked nodes sit in the denominator without ever advancing the numerator. A compartment with enough permanently blocked nodes can therefore never reach its `safetyLimit`.
- A batch is evaluated **once it finishes**, not the moment a node starts erroring. Nodes already admitted to the batch run to completion first, so stopping happens at batch granularity and never mid-batch.
- Stopping applies to **the compartment that failed**, not the entire NodeWright. Other compartments continue rolling out.
- A stopped compartment stays stopped until its batch state is reset. See [Batch State Reset](#batch-state-reset).

### Safety Limit Behavior

`safetyLimit` is the point of no return. It is a rollout progress percentage: below it the operator is still willing to call the whole thing off, and at or above it the operator commits to finishing. We are this far in, most of the fleet is already on the new version, so keep going.

Progress is computed with integer division, as `(completed + failed) * 100 / total nodes in the compartment`, and the remainder is discarded. One processed node out of three is 33%, not 33.3%. On small compartments this shifts where the point of no return actually falls: with 3 nodes and the default `safetyLimit: 50`, one processed node scores 33% and the operator is still cautious, while two scores 66% and it has already committed.

The reasoning is that a half-rolled-out fleet is its own kind of problem. Early on, stopping is cheap and failures are the best evidence you have that the change is bad, so the operator treats them seriously. Late in the rollout, stopping leaves the cluster split across two versions indefinitely, which is often worse than finishing and dealing with the handful of nodes that failed.

**Below `safetyLimit`** (by default, fewer than 50% of the compartment's nodes completed or failed), the operator is cautious:

- Failed batches count toward `failureThreshold`
- For `linear` and `exponential`, batch sizes shrink after a failure: `linear` subtracts `delta`, `exponential` divides by `growthFactor`. `fixed` stays at `initialBatch`, since it never reacts to failures
- Hitting `failureThreshold` stops the compartment

**At or above `safetyLimit`**, the operator commits:

- `failureThreshold` is no longer enforced, so the rollout will not stop on its own
- For `linear` and `exponential`, batch sizes stop shrinking and keep growing as if every batch had passed. `fixed` stays at `initialBatch`, as it always does
- Failures are still recorded in status; they just no longer change what the rollout does

**If you do not want a point of no return, set `safetyLimit: 100`.** This is the most common surprise with these settings. At the default of 50, a rollout that is already past halfway runs to the end no matter what `failureThreshold` says, so `failureThreshold: 1` on its own means "stop at the first failed batch, but only during the first half of the rollout", not "stop at the first failure".

---

## Common Configurations

### Parallel rollout, no failures tolerated

Nodes upgrade in parallel with concurrency ramping up, and the compartment stops the moment any batch contains a failed node, at any point in the rollout.

```yaml
budget:
  percent: 25              # reported as the compartment ceiling; does NOT cap the ramp below
strategy:
  exponential:
    initialBatch: 1        # start with a single canary node
    growthFactor: 2        # 1 -> 2 -> 4 -> 8 ... grows toward the compartment size
    batchThreshold: 100    # every node in a batch must succeed
    failureThreshold: 1    # one failed batch stops the compartment
    safetyLimit: 100       # enforce failureThreshold for the entire rollout
```

Use `failureThreshold: 1` rather than `0`; see [Reference](#reference).

The exponential ramp here is deliberately unbounded: each successful batch doubles until the compartment is done. If you need a hard ceiling on concurrency, use the fixed-parallelism form below instead, because `budget` will not impose one while a `strategy` is set.

### Fixed parallelism, no failures tolerated

Same failure handling, but a bounded number of nodes at a time instead of a ramp. With a `strategy` set, `initialBatch` is what actually caps concurrency, so set it to the ceiling you want.

```yaml
budget:
  count: 10
strategy:
  fixed:
    initialBatch: 10       # the real concurrency cap: up to 10 nodes at a time
    batchThreshold: 100
    failureThreshold: 1
    safetyLimit: 100
```

### Tolerate some failures, slow down instead of stopping

Useful for large fleets where a few bad nodes should not halt the rollout.

```yaml
budget:
  percent: 20
strategy:
  linear:
    initialBatch: 5
    delta: 5               # grows 5 -> 10 -> 15 ...; budget does not cap this
    batchThreshold: 90     # a batch passes if 90% of its non-blocked nodes succeed
    safetyLimit: 100       # keep slowing down on failure for the whole rollout
    # failureThreshold omitted: never stop, just shrink batches on failure
```

`safetyLimit` gates the slowdown as well as `failureThreshold`. Leaving it at the default 50 means the rollout hits its point of no return halfway through and batch sizes stop shrinking on failure from there on.

### Excluding nodes from the rollout

Deployment Policy has no exclusion field. Exclude individual nodes with the `nodewright.nvidia.com/ignore` label, or scope the whole rollout with the NodeWright's `nodeSelectors`. See [Node Selection](custom-resource.md#the-nodewrightnvidiacomignore-label).

---

## Batch Stickiness

Nodes selected for a batch remain in that batch until every node has reached a definitive outcome — all packages complete, erroring, or blocked. The controller will not select new nodes for the next batch while the current batch has nodes still running between packages.

Batch membership is tracked via `NodePriority` in the NodeWright status. A node stays in `NodePriority` from the time it is picked for a batch until it completes all packages. This state is persisted in the CRD, so it survives controller restarts.

Each package pod also receives a `SKYHOOK_NODE_ORDER` environment variable reflecting the node's monotonic position in the rollout. See [Node Order Within a Rollout](../architecture/ordering.md#node-order-within-a-rollout) for details.

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

1. **Rollout completion** — When a NodeWright's status transitions to `Complete`
2. **Spec version change** — When a package version changes in the NodeWright spec

After reset, the next reconciliation starts from batch 1 with all counters cleared.

### Configuration

Auto-reset is controlled by two fields with a precedence hierarchy:

| Field | Location | Description |
|-------|----------|-------------|
| `spec.resetBatchStateOnCompletion` | DeploymentPolicy | Default setting for all NodeWrights using this policy |
| `spec.deploymentPolicyOptions.resetBatchStateOnCompletion` | NodeWright | Per-NodeWright override (takes precedence) |

**Precedence order** (highest to lowest):

1. NodeWright's `deploymentPolicyOptions.resetBatchStateOnCompletion`
2. DeploymentPolicy's `resetBatchStateOnCompletion`
3. Default: `true` (safe by default for new resources)

### Examples

**Enable auto-reset (default behavior for new policies)**:
```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: DeploymentPolicy
metadata:
  name: my-policy
spec:
  resetBatchStateOnCompletion: true  # Enabled by default
  default:
    budget:
      percent: 25
```

**Disable auto-reset for a specific NodeWright** (override the policy):
```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: my-nodewright
spec:
  deploymentPolicy: my-policy
  deploymentPolicyOptions:
    resetBatchStateOnCompletion: false  # Override: keep batch state across rollouts
```

**Disable auto-reset at the policy level**:
```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: DeploymentPolicy
metadata:
  name: preserve-state-policy
spec:
  resetBatchStateOnCompletion: false  # All NodeWrights using this policy keep batch state
```

### Manual Reset

You can also reset batch state manually using the CLI:

```bash
# Reset batch state for a specific NodeWright
kubectl nodewright deployment-policy reset my-nodewright --confirm

# Preview what would be reset (dry-run)
kubectl nodewright deployment-policy reset my-nodewright --dry-run

# The 'reset' command also resets batch state by default
kubectl nodewright reset my-nodewright --confirm

# To reset nodes only without resetting batch state
kubectl nodewright reset my-nodewright --skip-batch-reset --confirm
```

Both `reset` and `deployment-policy reset` also clear `NodeOrderOffset` and `NodePriority`, so the next rollout starts with fresh node ordering (`SKYHOOK_NODE_ORDER` begins at `0`).

See [CLI documentation](cli.md) for full command details.

---

## Using with NodeWrights

Reference a policy by name:

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: my-nodewright
spec:
  deploymentPolicy: my-policy  # References DeploymentPolicy
  deploymentPolicyOptions:     # Optional per-NodeWright overrides
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
- `deploymentPolicyOptions` allows per-NodeWright overrides of policy settings

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
apiVersion: nodewright.nvidia.com/v1alpha1
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
# 2. Update NodeWright
spec:
  deploymentPolicy: legacy-equivalent
  # Remove interruptionBudget field
```

---

## Monitoring

Deployment Policy rollout behavior is exposed via Prometheus metrics. See [Metrics documentation](../observability/metrics.md) for details.

---

## Examples

See `/operator/config/samples/deploymentpolicy_v1alpha1_deploymentpolicy.yaml` for a complete sample showing:

- Critical nodes (count=1, fixed strategy)
- Production nodes (count=3, linear strategy)
- Staging nodes (percent=33, exponential strategy)
- Test nodes (percent=50, fast exponential)

---
