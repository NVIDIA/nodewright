# What it is

Runtime required is a special mode that packages can be run in. This mode is for when a set of Packages must complete before any other workloads are allowed to run on the node.

## How to use it

## Pre-requisites

1. The node MUST carry the runtime-required taint. There are two ways to get there, and only the first requires the node to join already tainted:
    1. **Pre-taint at provisioning (recommended).** The node joins the cluster already carrying the taint, via the kubelet `--register-with-taints` flag or your provisioner. Nothing can schedule on the node before the taint exists.
    1. **`autoTaintNewNodes: true` (fallback).** The operator applies the taint itself to new nodes matching the NodeWright's selector, so the node does **not** have to join with it. Use this when you cannot control tainting at provisioning time, and note the race window described in [Auto-tainting new nodes](#auto-tainting-new-nodes).
1. The operator MUST recognise that taint. It recognises two:
    1. the taint set as the chart value `controllerManager.manager.env.runtimeRequiredTaint`, which defaults to `nodewright.nvidia.com=runtime-required:NoSchedule`. Its key must have no DNS prefix and be at most 53 characters, because the operator names the `nodewright.nvidia.com/autoTaint_<key>` marker after it; the operator refuses to start otherwise.
    1. for the deprecation window only, the legacy `skyhook.nvidia.com=runtime-required:NoSchedule` taint, whatever the chart value is

Recognition matters for both paths: a taint the operator does not recognise is never removed, so the node stays unschedulable. `autoTaintNewNodes` applies only the configured taint, never the legacy one.

So during the deprecation window the node taint and the chart value do **not** have to match: a cluster whose nodes still join with the legacy key keeps working without touching the chart, and a cluster on the new key works out of the box. You only need to set `runtimeRequiredTaint` explicitly if your nodes join with some **other** key. See [Taint key rename](#taint-key-rename-skyhooknvidiacom---nodewrightnvidiacom) for what to migrate and by when.

## Taint key rename: `skyhook.nvidia.com` -> `nodewright.nvidia.com`

The default runtime-required taint key moved from `skyhook.nvidia.com` to `nodewright.nvidia.com` as part of the Skyhook -> NodeWright rename. **This is a coordinated change, not a cosmetic default bump**: the taint key is named by infrastructure the operator cannot see, and a node carrying a key the operator does **not** recognise is never untainted, so it sits unschedulable. The deprecation window below exists precisely so that moving the default does not create that situation: during the window the legacy key is still recognised, so it is not a "wrong" key.

During the deprecation window the operator:

- **applies** only the configured taint (`runtimeRequiredTaint`, defaulting to the `nodewright.nvidia.com` key)
- **tolerates** both the configured taint and the legacy `skyhook.nvidia.com=runtime-required:NoSchedule` taint, so package pods still schedule onto legacy-tainted nodes
- **removes** both from a node once every `runtimeRequired: true` NodeWright targeting it has completed on it
- treats a node that already carries either taint as already gated, so `autoTaintNewNodes` does not stamp a second taint on it

That means a cluster whose provisioner still applies the legacy key keeps working with no change, and a cluster on the new key works immediately.

### What you need to update

Anywhere outside the operator that names the taint key:

- **Cluster autoscaler / Karpenter node pool definitions** and any `--register-with-taints` kubelet arguments, so new nodes come up carrying the new key
- **Machine or node templates** in your provisioning stack
- **Tolerations on your own workloads** that were written against the legacy key

If you cannot change your provisioning config yet, pin the old behavior explicitly instead of relying on the deprecation window:

```yaml
controllerManager:
  manager:
    env:
      runtimeRequiredTaint: skyhook.nvidia.com=runtime-required:NoSchedule
```

### Deprecation window

Legacy-key toleration and removal ship as a transition-only shim and are removed together with the legacy `skyhook.nvidia.com` API group in **operator v0.20.0** (see [nodewright-migration.md](../getting-started/migration.md#removal-operator-v0200)). The rename ships in v0.18.0, so you have the v0.18.x and v0.19.x lines to migrate.

**Migrate your provisioning config before v0.20.0.** From that release on, a node carrying only the legacy taint is never untainted by the operator and package pods do not tolerate it, so such nodes stay unschedulable.

## Required NodeWrights

Once the pre-requisites are satisfied any NodeWright Custom Resource (CR) may be marked with `runtimeRequired: true`. This flag indicates that all packages within this CR must complete
before the nodes that it targets are considered available for general use.

## Auto-tainting new nodes

**Recommended:** The preferred approach is to taint nodes as they are added to the cluster (e.g., via your infrastructure provisioner or node bootstrap configuration). Pre-tainting eliminates any race condition between a node becoming schedulable and the operator applying the taint.

If you cannot control node tainting at provisioning time, `autoTaintNewNodes` provides a fallback. Note that there is a small window between when a node joins the cluster and when the operator's reconcile loop applies the taint, during which workloads could theoretically be scheduled on the node.

To enable, set `autoTaintNewNodes: true` alongside `runtimeRequired: true` on the NodeWright CR:

```yaml
spec:
  runtimeRequired: true
  autoTaintNewNodes: true
```

When enabled, the operator automatically applies the runtime-required taint to nodes that:

1. Match the NodeWright's node selector
2. Do not already have the runtime-required taint
3. Have no `nodewright.nvidia.com/*` annotations (i.e., have never been touched by the NodeWright operator)

A node is considered "new" if it has no NodeWright annotations. This works for both initial cluster setup (day 0) and nodes joining an existing cluster (day 2+). Nodes that have already been processed by NodeWright (and had their taint removed after completion) will not be re-tainted because they retain their NodeWright annotations.

If the operator cannot taint a new node, for example because an admission policy denies the patch, it records a Warning event on the node (reason `AutoTaint`), leaves that node out of reconciliation for every NodeWright, and retries the taint on every later reconcile until it lands. Until then the node is treated as if it had not joined: no NodeWright runs packages on it or counts it in its status, so a NodeWright can report `complete` without it and a `sequencing: all` gate can open without it. The event and the operator log are where the node shows. Every other node carries on, and workloads can still schedule onto the node because it has no taint yet. Once the taint lands, the node is handled like any other new node.

### "New" means never touched, and it is one-way

This is deliberately **not** "new to this NodeWright". The check is for *any* `nodewright.nvidia.com/*` annotation, so once **any** NodeWright has touched a node, auto-taint never considers it new again. Two consequences that surprise people:

- **A different NodeWright does not make it new again.** If NodeWright A has run on a node, and runtime-required NodeWright B later selects that same node for the first time, B does **not** auto-taint it. The node is not new, even though B has never run there.
- **`kubectl nodewright reset` does not make it new again.** Reset clears a NodeWright's package state so its packages re-run, but the node keeps other `nodewright.nvidia.com/*` annotations, including the node-scoped `autoTaint_<taintKey>` marker. So the packages re-run **without** the runtime-required taint being re-applied.

That second point is the important one: **reset is not a way to re-gate a node.** If you need the runtime-required taint back on a node that NodeWright has already handled, do it explicitly:

```bash
# Re-apply the taint yourself, then reset:
kubectl taint node <node> nodewright.nvidia.com=runtime-required:NoSchedule
kubectl nodewright reset <nodewright-name> --confirm
```

Returning a node to genuinely "new" is a deliberate human action, not something the operator or the CLI does for you. It means removing the NodeWright annotations from the node yourself:

```bash
# Inspect what is keeping the node from being "new"
kubectl get node <node> -o jsonpath='{range .metadata.annotations}{...}' \
  | tr ',' '\n' | grep nodewright.nvidia.com

# Remove them (this discards NodeWright's record of the node). The marker is
# autoTaint_<key> for your runtimeRequiredTaint key; the default key is shown.
kubectl annotate node <node> nodewright.nvidia.com/autoTaint_nodewright.nvidia.com-
```

This is intentional. Auto-taint exists to gate nodes arriving in the cluster, typically from an autoscaler; it is not a general re-gating mechanism, and it is not a substitute for pre-tainting at provisioning. If you need the gate to hold reliably across resets, reboots, and re-runs, **pre-taint at provisioning** as recommended above rather than relying on `autoTaintNewNodes`.

**Exception: reboot with `REAPPLY_ON_REBOOT=true`.** When the operator is configured with `REAPPLY_ON_REBOOT=true` and a NodeWright has both `runtimeRequired: true` and `autoTaintNewNodes: true`, a node whose boot ID changes is treated as new for taint purposes. The runtime-required taint is re-applied before the state reset, and the reboot is not recorded as handled until both writes succeed, ensuring no workloads can schedule on the rebooted node before NodeWright finishes re-applying. If the taint cannot be re-applied, the operator records a Warning event on the node (reason `AutoTaint`) and retries on every later reconcile. Until the taint lands, that NodeWright keeps its state and Jobs from before the reboot, and no NodeWright starts a new stage on the node: any other NodeWright on it is reset as usual, but re-applies only once the taint is back. The node keeps its slot in its batch, so the interruption budget still counts it, and workloads can still schedule onto it while the taint is missing. Every other node carries on. The taint is removed again by the normal completion path once all runtime-required NodeWrights finish on that node.

## What runtimeRequired: true will NOT do

1. Without `autoTaintNewNodes: true`, it will NOT add the taint to any nodes targeted by a CR with `runtimeRequired: true`

## Details

## When is the runtime-required taint removed from a node

The taint is removed from a node when all CRs with `runtimeRequired: true` that target that node are complete **on that specific node**.

**Important**: Taint removal is per-node, not per-NodeWright. This means:

- Node A's taint is removed when all runtime-required NodeWrights complete on Node A
- Node A does NOT wait for Node B to complete those same NodeWrights
- If Node B is stuck or failing, Node A can still have its taint removed and become available

This per-node behavior prevents deadlocks where a few bad nodes would block all other healthy nodes from becoming available.

## What happens when the taint is removed

- If `runtimeRequiredCordonAfter` is false on all `runtimeRequired` NodeWrights, the node is eligible for general workload scheduling (pods without the runtime-required toleration can now be scheduled on it), unless another NodeWright's `cordon_*` annotation is still holding it.
- If `runtimeRequiredCordonAfter` is true on one or more `runtimeRequired` NodeWrights, the node will be cordoned at the same time the runtime-required taint is removed. The operator will also set the `nodewright.nvidia.com/runtimeRequiredCordon` annotation on each cordoned node. Note that this setting is only respected on a given NodeWright if `runtimeRequired` is also true. The cordon is only applied to nodes that currently hold the runtime-required taint. Recall that the runtime-required taint could be applied externally of NodeWright for new nodes or via the autoTaintNewNodes feature described above. As a result, when the operator is configured with `REAPPLY_ON_REBOOT=true` and a NodeWright has `runtimeRequired: true`, `autoTaintNewNodes: true`, and `runtimeRequiredCordonAfter: true`, the cordon would be re-applied after the reboot and re-run complete.

```yaml
spec:
  runtimeRequired: true
  runtimeRequiredCordonAfter: true
```

An external actor can clear the node cordon by setting `unschedulable` to false and removing the annotation. If a node is uncordoned but the external actor does not remove the `runtimeRequiredCordon` annotation, the operator will automatically remove the annotation from the node.

```bash
kubectl patch node <node-name> --type=merge \
  -p '{"metadata":{"annotations":{"nodewright.nvidia.com/runtimeRequiredCordon":null}},"spec":{"unschedulable":false}}'
```

Note that a cordon applied via `runtimeRequiredCordonAfter` will persist after running the reset CLI command: `kubectl nodewright reset`

`kubectl nodewright node status` reports `CORDONED` and `RUNTIME-REQUIRED-CORDON` for each node, so you can tell whether a cordoned node needs an external actor to uncordon it, as described above.

## Why would you use runtime required

This is useful when you want to gate other work behind the successful completion of some set of NodeWright Packages. This can be for security reasons or for scheduling.

**NOTE:** **Package pods** need no additional toleration: NodeWright stamps them with a toleration for the configured (`runtimeRequiredTaint`) taint, and for the deprecation window the legacy `skyhook.nvidia.com` taint as well.

This does **not** cover the operator's own controller-manager pod, which carries only the tolerations you give it via `controllerManager.tolerations`. If the operator itself has to schedule onto nodes carrying the runtime-required taint, add that toleration to the chart values yourself, or the operator will not schedule there.
