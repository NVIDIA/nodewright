# Example: keep workloads off a node until it is ready

Some node setup has to finish before anything else runs there: a driver, a
security agent, a kernel setting. `runtimeRequired: true` makes that a rule. The
node carries a taint that blocks ordinary pods, and the operator lifts it only
once the NodeWright is complete **on that node**.

Package pods tolerate the taint automatically. Your workloads do not, which is the point.

## How it works

1. The node has the runtime-required taint, `nodewright.nvidia.com=runtime-required:NoSchedule`
   by default.
2. A NodeWright with `runtimeRequired: true` targets the node and runs its packages.
3. When they finish on that node, the operator removes the taint.
4. Pods that were waiting get scheduled.

The taint is per node. A slow node does not hold back a healthy one.

## Run it

Pick one node, label it, and taint it by hand. Pre-tainting is how you would do it
in production, by having the node join the cluster already tainted:

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=runtime-required
kubectl taint node "$NODE" nodewright.nvidia.com=runtime-required:NoSchedule
```

Start a workload pinned to that node. It has no toleration:

```bash
kubectl apply -f examples/runtime-required/workload.yaml
kubectl get pod runtime-required-demo
```

It is `Pending`. `kubectl describe pod runtime-required-demo` shows why: the node
has a taint the pod does not tolerate.

Now apply the NodeWright:

```bash
kubectl apply -f examples/runtime-required/nodewright.yaml
kubectl get nodewright runtime-required-demo -w
```

The package takes about 45 seconds. When it completes, the taint comes off and the
pod starts:

```bash
kubectl get node "$NODE" -o jsonpath='{.spec.taints}' ; echo
kubectl get pod runtime-required-demo
```

## What it will not do

- **It will not taint a node for you** unless you also set `autoTaintNewNodes: true`.
  Without a taint on the node, `runtimeRequired` gates nothing.
- **`autoTaintNewNodes` is a fallback, not a replacement for pre-tainting.** There is a
  short window between a node joining and the operator tainting it. It also treats a
  node as new only if no NodeWright has ever touched it, and `kubectl nodewright reset`
  does not make it new again.
- **A broken package leaves the node tainted.** Test packages on a few nodes before
  setting `runtimeRequired` across a fleet.
- **Do not combine it with `sequencing: all`.** One unhealthy node would hold the
  whole fleet tainted. See [Strict Ordering](../../docs/architecture/ordering.md#caution-deadlock-risks).

## Clean up

```bash
kubectl delete -f examples/runtime-required/workload.yaml
kubectl delete -f examples/runtime-required/nodewright.yaml
kubectl label node "$NODE" nodewright.nvidia.com/example-
kubectl taint node "$NODE" nodewright.nvidia.com=runtime-required:NoSchedule-
```

The last command matters if you clean up before the package finished: with no
NodeWright left to remove it, the taint stays and the node stays unschedulable.
It reports `not found` if the operator already removed the taint, which is fine.

## Related

- [Runtime Required](../../docs/user-guide/runtime-required.md): auto-tainting,
  the taint key rename from `skyhook.nvidia.com`, and `runtimeRequiredCordonAfter`
- [Taints](../../docs/user-guide/taints.md): how package pods tolerate taints, and
  what `blocked` means
