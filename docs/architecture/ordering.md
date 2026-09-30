# Ordering of NodeWrights

## Priority

NodeWrights are applied in a repeatable and specific order based on their `priority` field. Each custom resource supports a `priority` field which is a non-zero positive integer. NodeWrights will be processed in order starting from 1. NodeWrights with the same `priority` are processed by sorting on their `metadata.name` field.

**NOTE**: Any NodeWright which does NOT provide a `priority` field will be assigned a priority value of 200.

---

## Sequencing

The `sequencing` field on each NodeWright controls how it gates the next priority level. This determines whether nodes progress independently or must synchronize.

### `sequencing: node` (default)

Per-node ordering. A node proceeds past this NodeWright independently once it completes on that node. Other nodes do not need to finish first.

```
Node A completes NodeWright 1 → Node A immediately starts NodeWright 2
Node B still on NodeWright 1  → Node B shows "waiting" on NodeWright 2
Node A completes NodeWright 2 → Node A is fully complete
Node B completes NodeWright 1 → Node B starts NodeWright 2
```

This prevents deadlocks where stuck or bad nodes block healthy nodes from progressing.

### `sequencing: all`

Global ordering. **ALL** nodes must complete this NodeWright before **ANY** node starts the next priority level. Use this when the next priority depends on every node being at the same stage (e.g., cluster-wide configuration that must be applied everywhere before proceeding).

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: cluster-config
spec:
  priority: 10
  sequencing: all   # all nodes must finish before priority 11+ starts
  ...
```

```
Node A completes cluster-config → Node A waits
Node B still on cluster-config  → both nodes blocked from priority 11
Node B completes cluster-config → both nodes start priority 11
```

When a NodeWright with `sequencing: all` is not yet globally complete, it shows `waiting` status at the NodeWright level. Individual nodes inherit this waiting state rather than being evaluated independently.

A node the operator cannot auto-taint is left out of every NodeWright until its taint lands, so it does not hold the gate meanwhile. See [Auto-tainting new nodes](../user-guide/runtime-required.md#auto-tainting-new-nodes).

### Mixing modes

Different NodeWrights can use different sequencing modes. A NodeWright's `sequencing` field determines how **it** gates the next priority:

```
Priority 1:  driver-install   (sequencing: node)   ← nodes progress independently
Priority 2:  cluster-config   (sequencing: all)    ← sync point: all must finish
Priority 3:  workload-setup   (sequencing: node)   ← resumes per-node after sync
```

In this example, fast nodes can install drivers independently, but all nodes must complete the cluster config before any node starts workload setup.

### Caution: Deadlock risks

**`sequencing: all` + `runtimeRequired: true`** — This combination can deadlock your cluster. With `runtimeRequired`, nodes are tainted until the NodeWright completes, preventing workloads from scheduling. With `sequencing: all`, every node must complete before any node moves to the next priority. If a single node fails (unhealthy, can't schedule pods, bad hardware), all nodes remain tainted and blocked indefinitely. New nodes joining the cluster with the same selector will also be tainted and must complete before the gate releases — if those nodes aren't healthy, the deadlock worsens.

**`sequencing: all` with unreliable packages** — Even without `runtimeRequired`, `sequencing: all` means one stuck node blocks all nodes from progressing to the next priority. If your package has a bug or a node has an issue that prevents completion, the entire rollout stalls. Prefer `sequencing: node` (the default) unless you have a strong reason to require cluster-wide synchronization.

**`runtimeRequired: true` with untested packages** — Since `runtimeRequired` leaves nodes tainted until the NodeWright completes, a broken package image or misconfigured package will leave nodes tainted and unable to run workloads. Always test packages on a small node group first before applying with `runtimeRequired` to your full cluster.

---

## Node Order Within a Rollout

The sections above cover ordering of NodeWrights relative to each other. This section covers ordering of **nodes** within a single NodeWright's rollout.

When a [DeploymentPolicy](../user-guide/deployment-policy.md) controls the batch rollout, each package pod receives a `SKYHOOK_NODE_ORDER` environment variable — a zero-indexed integer reflecting the node's position in the overall rollout order.

- The first batch's nodes are assigned `0, 1, 2, ...`
- The second batch continues from where the first left off (e.g., `3, 4, 5, ...`)
- Values are monotonically increasing across batches and never reused within a rollout
- Within a batch, nodes are sorted by name for deterministic tiebreaking

### Use case: kubeadm upgrades

The primary motivation is kubeadm-style Kubernetes upgrades where the first control-plane node must run `kubeadm upgrade apply` and all subsequent nodes run `kubeadm upgrade node`:

```bash
if [ "$SKYHOOK_NODE_ORDER" -eq 0 ]; then
    kubeadm upgrade apply v1.35.0
else
    kubeadm upgrade node
fi
```

### Scope

`SKYHOOK_NODE_ORDER` reflects rollout order within a single NodeWright only. Cross-NodeWright ordering is controlled by `priority` and `sequencing` (documented above). If a NodeWright is reset via `kubectl nodewright reset`, the node order restarts from `0`.

See [Batch Stickiness](../user-guide/deployment-policy.md#batch-stickiness) for details on how batches are kept intact during rollout.

---

## Flow Control Annotations

Two flow control features can be set in the annotations of each NodeWright:

- `nodewright.nvidia.com/disable`: bool. When `true`, skips this NodeWright from processing and continues with any others further down the priority order.
- `nodewright.nvidia.com/pause`: bool. When `true`, does NOT process this NodeWright and will NOT continue to process any NodeWrights after this one on that node. This effectively stops all application of NodeWrights starting with this one.

**NOTE**: `pause` was previously on the NodeWright spec and has been moved to annotations to be consistent with `disable` and to avoid incrementing the generation when toggling it.

---

## Recommended Priority Buckets

To coordinate work without explicit communication, we recommend bucketing NodeWrights by priority range:

| Range | Purpose | Examples |
|-------|---------|----------|
| 1–99 | Initialization and infrastructure | Security tools, monitoring agents |
| 100–199 | Configuration | SSH access, network settings |
| 200+ | User-level configuration | Workload tuning, application setup |

---

## Why

**Deterministic ordering** — Prior to priority ordering, NodeWrights ran in parallel with no deterministic order. This made debugging difficult since different nodes could receive updates in different sequences. Priority ordering ensures every node processes NodeWrights in the same order.

**Complex sequencing** — Some workflows require applying different sets of work to different node groups in a particular order. Priority ordering with `sequencing: all` enables cluster-wide synchronization points.

**Community coordination** — Priority buckets provide a shared convention so different teams can coordinate NodeWright ordering without direct communication.
