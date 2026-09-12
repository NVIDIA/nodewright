# Interrupt Flow and Ordering

This document explains how NodeWright handles packages that require interrupts and the specific ordering of operations to ensure safe and reliable execution.

## Overview

When a package requires an interrupt (such as a reboot or service restart), NodeWright follows a specific sequence to ensure that workloads are safely evacuated from the node before any potentially disruptive operations occur.

## Interrupt Flow Sequence

### For packages WITH interrupts:

1. **Uninstall** (if downgrading) - Package uninstallation operations are executed.
2. **Cordon** - Node is marked as unschedulable to prevent new workloads from being scheduled
3. **Wait** - System waits for any conflicting workloads to naturally complete or be rescheduled
4. **Drain** - Remaining workloads are gracefully evicted from the node
5. **Apply** / **Upgrade** (if upgrading) - Package installation/upgrade operations are executed  
6. **Config** - Configuration and setup operations are performed
7. **Interrupt** - The actual interrupt operation (reboot, service restart, etc.) is executed. A node restart is complete only after a later agent invocation observes that the host boot ID changed.
8. **Post-Interrupt** - Any cleanup or verification operations after the interrupt

Before requesting a node restart, the agent writes a pending marker containing
the current host boot ID. If `reboot` exits successfully before shutdown reaches
the agent, the agent waits for up to 10 seconds for shutdown to terminate the
process. Remaining alive for the full window is reported as a failure because
the reboot was enqueued but did not take effect. After the node returns, the
next invocation compares the pending boot ID with the current value. A changed
boot ID promotes the pending marker to complete and allows post-interrupt work;
an unchanged boot ID removes the stale marker and retries the restart.

### For packages WITHOUT interrupts:

1. **Uninstall** (if downgrading) - Package uninstallation operations are executed.
2. **Apply** / **Upgrade** (if upgrading) - Package installation/upgrade operations are executed
3. **Config** - Configuration and setup operations are performed

## Why This Order Matters

The **uninstall → cordon → wait → drain → apply/upgrade → config → interrupt** sequence is critical for several reasons:

### Safety First

- Workloads are safely removed before any potentially disruptive operations
- Prevents data loss or service interruption for running applications
- Ensures the node is in a clean state before package operations begin

### Use Cases

This ordering is particularly important for scenarios such as:

- **Kernel module changes**: Unloading kernel modules while workloads are present could cause system instability
- **GPU mode switching**: Changing GPU from graphics to compute mode requires exclusive access
- **Driver updates**: Hardware driver changes need exclusive access to the hardware
- **System reboots**: Obviously require all workloads to be evacuated first

### Example Scenario

Consider a package that needs to unload a kernel module, perform some operations, and then reboot:

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: gpu-mode-switch
spec:
  packages:
    gpu-driver:
      version: "1.0.0"
      image: "example/gpu-driver"
      interrupt:
        type: "reboot"
```

**Flow:**

1. **Cordon**: Node becomes unschedulable
2. **Wait**: Any non-interrupt workloads are given time to complete
3. **Drain**: Remaining workloads are evicted
4. **Apply**: GPU driver package operations run (unload old module, install new)
5. **Config**: Configuration files are updated
6. **Interrupt**: System reboots to complete the driver change
7. **Post-Interrupt**: Verification that the new driver is loaded correctly

## Technical Implementation

The interrupt flow is managed by the `ProcessInterrupt` and `EnsureNodeIsReadyForInterrupt` functions in the NodeWright controller, which:

- Check for conflicting workloads using label selectors
- Coordinate the cordon and drain operations
- Ensure the node is ready before proceeding with package operations
- Handle the timing and sequencing of all stages

### Cordon Is Durable Before Drain Starts

The cordon and the drain are deliberately split across two reconcile passes. The
operator applies the cordon in memory and writes it to the API server at the end
of the pass, together with every other node it selected. Draining does not begin
until a later pass observes `spec.unschedulable` already set on the node.

This matters because eviction is what triggers a replacement pod. If the operator
evicted in the same pass that first cordoned the node, `spec.unschedulable` would
still be local to the controller, and the scheduler could place the replacement
back onto a node that is about to be interrupted.

The split costs one reconcile per drain cycle, not one per node: a single pass
still cordons every node it selected, so a batch of nodes is cordoned together
and then drained together on the following pass.

### Shared Cordon Ownership

Each NodeWright that cordons a node records ownership with a `nodewright.nvidia.com/cordon_<nodewright-name>` annotation. When that NodeWright completes, it removes only its own cordon annotation. The node is marked schedulable only after no `nodewright.nvidia.com/cordon_*` annotations remain, so one NodeWright cannot uncordon a node that another NodeWright is still preparing for interrupt work. Additionally, if a persistent cordon was previously applied from a NodeWright which set `runtimeRequiredCordonAfter` to true, the cordon will persist even after all `nodewright.nvidia.com/cordon_*` annotations are removed.

Other NodeWright annotations, such as `status_*`, `nodeState_*`, and `version_*`, do not keep a node cordoned. Only the `cordon_*` annotation family and `runtimeRequiredCordon` participate in shared cordon ownership.

### Orphaned Cordon Recovery

If a NodeWright is force-deleted in a way that bypasses finalizer cleanup, or cleanup fails after the node was cordoned, its `cordon_<nodewright-name>` annotation can be left behind. That stale annotation will keep the node unschedulable from the operator's point of view until it is removed.

Use `kubectl nodewright reset <nodewright-name> --confirm` to clear NodeWright metadata for affected nodes that still have `nodeState_<nodewright-name>` annotations, or `kubectl nodewright node reset <node-name> --nodewright <nodewright-name> --confirm` for a specific node. These reset commands remove the matching `cordon_<nodewright-name>` annotation, but they do not clear `spec.unschedulable`. After the stale cordon annotation is removed, if no other `nodewright.nvidia.com/cordon_*` annotations remain and no live NodeWright is expected to uncordon the node, check for the `runtimeRequiredCordon` annotation before running `kubectl uncordon <node-name>`. If the annotation is absent, uncordoning is a safe recovery step. If the annotation is present, it means a NodeWright intentionally applied a persistent cordon, and `kubectl uncordon` is not a recovery step unless removing the persistent cordon is intentional.

If the node only has a stale `cordon_<nodewright-name>` annotation and its `nodeState_<nodewright-name>` annotation has already been removed, `kubectl nodewright reset` will not discover the node. In that case, remove the orphaned annotation manually, then uncordon the node as above, subject to the same `runtimeRequiredCordon` check.

## Drain Configuration

Interrupt-enabled NodeWrights can tune drain behavior with `spec.drainConfig`.
Unset fields preserve the operator's existing behavior:

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: gpu-mode-switch
spec:
  drainConfig:
    disableEviction: false
    deleteEmptyDirData: true
    force: true
    ignoreDaemonSets: true
    timeout: 10m
    gracePeriod: 30s
```

The fields map to Kubernetes drain behavior:

- `disableEviction`: when `true`, pods are deleted directly instead of evicted. This bypasses PodDisruptionBudgets. The default is `false`, so the eviction API is used.
- `deleteEmptyDirData`: when `false`, pods with `emptyDir` volumes block drain. The default is `true`.
- `force`: when `false`, pods without a managing controller block drain. The default is `true`.
- `ignoreDaemonSets`: when `true`, DaemonSet-managed pods are skipped during drain. The default is `true`.
- `timeout`: bounds how long a node may spend draining, measured from drain start — the first pass that finds the node not yet drained, which is before any evict or delete is issued — until the node is drained. Unset or zero means no timeout, so a pod that never finishes terminating holds the node in `in_progress` indefinitely. When the timeout expires, the node is marked `erroring` and package stages do not proceed on that node.
- `gracePeriod`: overrides the grace period used for eviction or direct deletion. Unset uses each pod's own `terminationGracePeriodSeconds`.

The operator also skips pods that tolerate the `node.kubernetes.io/unschedulable`
taint, mirror/static pods, pods in `kube-system`, and its own package pods —
identified by the `nodewright.nvidia.com/name` and `nodewright.nvidia.com/package`
labels stamped on every package pod, so they remain exempt even if an admission
controller rewrites or strips their tolerations. The label exemption only applies
to pods in the operator's own namespace, so workloads elsewhere cannot opt out
of drain by copying the labels. These exclusions are not user-configurable.

Compared to earlier releases, the default drain filter now follows Kubernetes
matching more closely: the unschedulable toleration check uses Kubernetes
`ToleratesTaint` semantics, DaemonSet pods are identified from the controller
owner reference, and mirror/static pods are ignored.

`podNonInterruptLabels` remains a pre-drain barrier. Matching pods must finish
or move away before the operator starts the configurable drain step. The node is cordoned
before entering this barrier and remains cordoned throughout the wait so no replacement
workloads can schedule on it. This wait is unbounded and drain timeout does not apply.
While this barrier holds, administrators will see a `Blocked` condition with reason
`NonInterruptPodsRunning` reporting which nodes are held, along with Warning events on
both the NodeWright and affected Node objects detailing the hold.

### When Drain Is Complete

Drain is complete once the pods it selected have actually left the node, not
once their evictions were accepted. A pod that has an eviction or delete in
flight keeps a `deletionTimestamp` while the kubelet works through its
`terminationGracePeriodSeconds`, and the operator treats it as still present:
the node stays in `in_progress`, no further evict or delete is issued for it,
and the interrupt does not run until the pod object is gone. This matches
`kubectl drain`, which waits for the pods it selected to disappear.

Only pods the operator selected for eviction or deletion are waited on. The
exclusions above still apply while a pod is terminating, so a DaemonSet pod
mid-rollout, a `kube-system` pod, or one of the operator's own package pods never
holds up a drain.

Because drain now waits out termination, a workload's
`terminationGracePeriodSeconds` is on the critical path for every interrupt, and
a pod that cannot finish terminating — a stuck finalizer, an unresponsive
kubelet — blocks the node's interrupt until it is cleared. Set
`spec.drainConfig.timeout` to bound that wait; there is no default.

### Recovering From a Drain Timeout

When `spec.drainConfig.timeout` expires, the operator records a `DrainTimeout`
warning event, marks the node and NodeWright `erroring`, and leaves the node
cordoned. The operator stops issuing further evict/delete actions while the
blocking condition remains, so package stages do not proceed on that node.

To recover, remove the underlying blocker first, such as a PDB with zero allowed
disruptions, an unmanaged pod when `force: false`, or an `emptyDir` pod when
`deleteEmptyDirData: false`. Then reset the failed rollout metadata:

```bash
kubectl nodewright reset <nodewright-name> --confirm
```

For a single node, use:

```bash
kubectl nodewright node reset <node-name> --nodewright <nodewright-name> --confirm
```

If the blocker clears after the timeout without a reset, a later reconcile can
observe the node as drained and continue from current cluster state. Reset is
still the recommended recovery workflow in production because it explicitly
clears the `erroring` status, drain-start metadata, cordon metadata, and batch
state before retrying. If the blocker is still present after reset, the drain
will time out again.

## Best Practices

- Always test interrupt-enabled packages in non-production environments first
- Use appropriate `podNonInterruptLabels` selectors to identify important workloads that should block interrupts
- Consider the impact of node cordoning on cluster capacity
- Monitor package logs during interrupt operations for troubleshooting
- Use Grafana dashboards to monitor interrupt operations and track package state transitions across your cluster (see [docs/observability/metrics.md](../observability/metrics.md) for dashboard setup and configuration)
