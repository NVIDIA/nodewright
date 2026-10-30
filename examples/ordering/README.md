# Example: running NodeWrights in a fixed order

When several NodeWrights target the same nodes, `priority` decides the order and
`sequencing` decides how strictly it is held. This example has three:

| NodeWright | `priority` | `sequencing` | Meaning |
|---|---|---|---|
| `ordering-infra` | 10 | `node` (default) | Each node moves on as soon as it finishes here |
| `ordering-config` | 100 | `all` | A cluster-wide barrier: **no** node starts priority 200 until **every** node has finished this one |
| `ordering-workload` | 200 | `node` | Runs last |

Lower numbers run first. NodeWrights with the same priority run in name order.
The numbers follow the [recommended buckets](../../docs/architecture/ordering.md#recommended-priority-buckets):
1-99 infrastructure, 100-199 configuration, 200 and up user-level work.

## Run it

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=ordering
kubectl apply -f examples/ordering/nodewrights.yaml
kubectl get nodewright
```

`ordering-config` carries a `nodewright.nvidia.com/pause: "true"` annotation, so the
run stops there. `ordering-infra` completes, `ordering-config` reads `paused`, and
`ordering-workload` waits behind it. **Pause stops the paused NodeWright and
everything after it** on that node, which is what makes it a safe brake.

Release it:

```bash
kubectl annotate nodewright ordering-config nodewright.nvidia.com/pause=false --overwrite
kubectl get nodewright -w
```

Now `ordering-config` runs, then `ordering-workload`. The last package prints the
log of what ran, in order:

```bash
kubectl nodewright package logs workload --nodewright ordering-workload --stage apply
```

```text
--- order in which the NodeWrights ran on <node-name> ---
12:00:01 10  ordering-infra
12:00:30 100 ordering-config
12:00:41 200 ordering-workload
```

Pausing is a flag on the resource, so toggling it does not change the spec's
generation. `disable` is the sibling: it skips a NodeWright and lets the ones
after it carry on.

## Use `sequencing: all` sparingly

A barrier is only as fast as its slowest node. If one node never finishes the
`all` NodeWright, every node waits at it indefinitely. That is the right trade when
the next stage truly needs the whole fleet in the same state, and a trap
otherwise. Never combine it with `runtimeRequired: true`: a single unhealthy node
would keep every node tainted.

## Clean up

```bash
kubectl delete -f examples/ordering/nodewrights.yaml
kubectl label node "$NODE" nodewright.nvidia.com/example-
```

The log stays on the host. Remove it from a
[debug pod](../README.md#reading-files-on-a-node) with
`chroot /host rm -rf /etc/nodewright-example/ordering`.

## Related

- [Strict Ordering](../../docs/architecture/ordering.md): priority, sequencing,
  pause and disable, and the deadlock cases
