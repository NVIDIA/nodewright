# Example: a staged rollout with a DeploymentPolicy

An `interruptionBudget` caps how many nodes run at once. A `DeploymentPolicy`
goes further: it splits the fleet into **compartments**, each with its own batch
strategy and its own tolerance for failure, so one rollout can be cautious with
some nodes and quick with others.

This example sorts six workers into three groups:

| Group | Nodes | Rolls out | Stops when |
|---|---|---|---|
| `canary` | 1 | one at a time | the first batch fails |
| `production` | 3 | 1 node, then 2, growing by one per good batch | two batches in a row fail |
| default | 2 | two at a time | the first batch fails |

Nodes that match no compartment fall into the policy's `default`.

## Prerequisites

Six or more worker nodes. To make a throwaway cluster:

```bash
kind create cluster --name nodewright-examples --config examples/rollout/kind-config.yaml
```

and [install NodeWright](../../docs/getting-started/quickstart.md#1-install-the-operator).

## Run it

Opt every worker in, then sort them into compartments:

```bash
WORKERS=($(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o name | cut -d/ -f2))

for n in "${WORKERS[@]}"; do
  kubectl label node "$n" nodewright.nvidia.com/example=rollout
done
kubectl label node "${WORKERS[0]}" nodewright.nvidia.com/example-tier=canary
for n in "${WORKERS[@]:1:3}"; do
  kubectl label node "$n" nodewright.nvidia.com/example-tier=production
done
# the last two carry no tier label and fall into `default`
```

Create the policy before the NodeWright, because the webhook rejects a reference
to a policy that does not exist:

```bash
kubectl apply -f examples/rollout/deployment-policy.yaml
kubectl apply -f examples/rollout/nodewright.yaml
```

Watch the nodes move through in batches (each takes about 20 seconds):

```bash
kubectl get nodewright rollout-demo -o jsonpath='{.status.nodeStatus}' ; echo
watch -n 5 "kubectl get nodewright rollout-demo -o jsonpath='{.status.compartmentStatuses}'"
```

`status.compartmentStatuses` has one entry per compartment, including its batch
state. Compartments run independently, so canary and production move at the same
time.

## See a compartment stop

The package fails on any node that has `/etc/nodewright-example/rollout/fail`.
Make the canary node fail, then run the rollout again. From a
[debug pod on the canary node](../README.md#reading-files-on-a-node), run
`chroot /host sh -c 'mkdir -p /etc/nodewright-example/rollout && touch /etc/nodewright-example/rollout/fail'`.
A completed package is not re-run on its own, so reset the NodeWright to send it
through again:

```bash
kubectl nodewright reset rollout-demo --confirm
```

The canary node retries, then reads `erroring`. Its batch counts as failed, and
with `failureThreshold: 1` the **canary compartment stops**. The `production` and
default compartments keep going: stopping applies to the compartment that failed,
not the whole NodeWright.

A stopped compartment stays stopped until its batch state is reset. After
removing the marker file, `reset` clears the failed node and the batch state
together, and the rollout starts over:

```bash
kubectl nodewright reset rollout-demo --confirm
```

## Two things that surprise people

- **Once a compartment has a `strategy`, its `budget` no longer caps how many
  nodes run at once.** The strategy sets the batch size. To cap concurrency, use
  `fixed` with `initialBatch: N`. The budget still breaks ties for a node that
  matches several compartments.
- **`safetyLimit` defaults to 50.** Past half of a compartment, the rollout commits
  to finishing and stops enforcing `failureThreshold`. These manifests set it to
  100 so a failure stops a rollout at any point.

## Clean up

```bash
kubectl delete -f examples/rollout/nodewright.yaml
kubectl delete -f examples/rollout/deployment-policy.yaml
for n in "${WORKERS[@]}"; do
  kubectl label node "$n" nodewright.nvidia.com/example- nodewright.nvidia.com/example-tier-
done
kind delete cluster --name nodewright-examples   # if you made one
```

## Related

- [Deployment Policy and Compartments](../../docs/user-guide/deployment-policy.md):
  strategies, thresholds, overlapping selectors, batch state
- [Strict Ordering](../../docs/architecture/ordering.md#node-order-within-a-rollout):
  `SKYHOOK_NODE_ORDER`, the position of each node in a rollout
