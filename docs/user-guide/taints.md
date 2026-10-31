Just like any other scheduler NodeWright will not schedule packages on selected nodes when there are taints that the package does not explicitly tolerate. These nodes however are not just ignored as it is assumed that the user wanted their packages on these nodes due to their selection of nodeSelectors. In this case the following will happen:

On the NodeWright Custom Resource containing the package(s) targeting the affected nodes:

```yaml
status:
    status: blocked
    nodeStatus:
        [node name]: blocked
    conditions:
      - reason: TaintNotTolerable
        status: "True"
        type: TaintNotTolerable
        message: Node [X, Y, Z, ...] has taints that are not tolerable. Skipping.
```

The legacy prefixed `nodewright.nvidia.com/TaintNotTolerable` condition type is retained for one release to allow existing consumers time to migrate.

Metrics:

 * nodewright_node_status_count status=blocked

# Default tolerations

The following taints are always tolerated by NodeWright

 * Runtime Required (`controllerManager.manager.env.runtimeRequiredTaint`, default `nodewright.nvidia.com=runtime-required:NoSchedule`)
 * Runtime Required, legacy key: `skyhook.nvidia.com=runtime-required:NoSchedule`. Tolerated and removed for the rename deprecation window, never applied. See [runtime_required.md](runtime-required.md#taint-key-rename-skyhooknvidiacom---nodewrightnvidiacom).
 * Cordon taint: `node.kubernetes.io/unschedulable`

A current-batch node with package State `in_progress` or `erroring` retains its
slot when Kubernetes adds
`node.kubernetes.io/not-ready` or `node.kubernetes.io/unreachable` taints with
`NoSchedule` or `NoExecute` effects. A reboot therefore does not let the next batch
start early. The exemption follows the slot, not batch membership: a node that was
picked and then displaced by another node before any stage started is blocked like
a fresh node. These taints block fresh admission unless `additionalTolerations`
explicitly tolerates them. Other untolerated taints and ignore labels still block
nodes, including current batch members.

An interrupt Job that retries keeps the node's slot, even when a failed attempt leaves the package State `erroring`.
After a stage timeout, the operator releases the slot once no other package Job for that NodeWright is running or retrying there.
The node then loses its lifecycle-taint exemption and can become `blocked`, but its erroring package entry still counts toward batch failure.
Once the node returns and the timeout clears, it can join a later batch.
See [Deployment Policy](deployment-policy.md#batch-stickiness) for timeout and recovery rules.

Package and interrupt pods tolerate both lifecycle keys with `NoExecute`
indefinitely by default. Explicit matching `additionalTolerations` entries come
first, so a finite `tolerationSeconds` still controls eviction. Pod eviction and
batch membership are separate: retaining a slot does not change the Pod's timeout.

# Common Symptoms

The following are common ways a user might know they have taint problem:

1. A NodeWright Custom Resource has status as `unknown` (Operator < v0.9) or `blocked` (Operator >= v0.9)
2. A NodeWright Custom Resource is sitting with incomplete nodes.

# Solutions

This can be solved in a few different ways:

 1. Remove the problem taint(s) from the node(s)
 2. Change the `nodeSelectors` for the NodeWright Custom Resources to avoid the nodes
 3. Set the `additionalTolerations` on the NodeWright Custom Resources to enable toleration of the taints. An example doing this is included below.

 ```yaml
 apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  labels:
    app.kubernetes.io/part-of: skyhook-operator
    app.kubernetes.io/created-by: skyhook-operator
  name: taint-scheduling
spec:
  nodeSelectors:
    matchLabels:
      nodewright.nvidia.com/test-node: skyhooke2e
  additionalTolerations:
    - key: nvidia.com/gpu
      effect: NoSchedule
  packages: ...
```
