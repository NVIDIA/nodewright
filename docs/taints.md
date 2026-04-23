Just like any other scheduler Skyhook will not schedule packages on selected nodes when there are taints that the package does not explicitly tolerate. These nodes however are not just ignored as it is assumed that the user wanted their packages on these nodes due to their selection of nodeSelectors. In this case the following will happen:

On the Skyhook Custom Resource containing the package(s) targeting the affected nodes:

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

The legacy `skyhook.nvidia.com/TaintNotTolerable` condition type is retained during the deprecation window for existing consumers.

Metrics:
 * skyhook_node_status_count status=blocked

# Default tolerations

The following taints are always tolerated by Skyhook

 * Runtime Required
 * Cordon taint: `node.kubernetes.io/unschedulable`


# Common Symptoms

The following are common ways a user might know they have taint problem:

1. A Skyhook Custom Resource has status as `unkown` (Operator < v0.9) or `blocked` (Operator >= v0.9)
2. A Skyhook Custom Resource is sitting with incomplete nodes.

# Solutions

This can be solved in a few different ways:
 1. Remove the problem taint(s) from the node(s)
 2. Change the `nodeSelectors` for the Skyhook Custom Resources to avoid the nodes
 3. Set the `additionalTolerations` on the Skyhook Custom Resources to enable toleration of the taints. An example doing this is included below.

 ```yaml
 apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  labels:
    app.kubernetes.io/part-of: skyhook-operator
    app.kubernetes.io/created-by: skyhook-operator
  name: taint-scheduling
spec:
  nodeSelectors:
    matchLabels:
      skyhook.nvidia.com/test-node: skyhooke2e
  additionalTolerations:
    - key: nvidia.com/gpu
      effect: NoSchedule
  packages: ...
```
