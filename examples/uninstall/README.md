# Example: explicit uninstall

Deleting a NodeWright, or relabelling a node out of its selector, leaves the
host as the packages left it, unless the package has opted in to uninstall (below),
in which case deleting the NodeWright runs the uninstall first. Reversing a change
is otherwise a separate, deliberate step: uninstall.

A package opts in with `uninstall.enabled: true`, which declares that it ships
`uninstall` and `uninstall_check` steps. Setting `uninstall.apply: true` then runs
them on every target node.

This example's package writes a marker file on the host and removes it again.

## Run it

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=uninstall
kubectl apply -f examples/uninstall/nodewright.yaml
kubectl get nodewright uninstall-demo -w
```

Once it is `complete`, the node's record of the package shows it installed:

```bash
kubectl get node "$NODE" \
  -o jsonpath='{.metadata.annotations.nodewright\.nvidia\.com/nodeState_uninstall-demo}'
```

The file is on the host. [Check from a debug pod](../README.md#reading-files-on-a-node):
`chroot /host cat /etc/nodewright-example/uninstall/marker`.

## Uninstall it

Flip `apply` to `true`:

```bash
kubectl patch nodewright uninstall-demo --type=merge \
  -p '{"spec":{"packages":{"marker":{"uninstall":{"apply":true}}}}}'
```

The operator runs `uninstall.sh`, then `uninstall_check.sh`, which fails unless the
file is gone. When the check passes, the package disappears from the node's
`nodeState_uninstall-demo` annotation. **Absent means uninstalled**: there is no
"uninstalled" state to look for.

```bash
kubectl nodewright package status marker --nodewright uninstall-demo
```

Now it is safe to remove the package from the spec. The webhook would have
rejected that before the uninstall finished, which is what stops you from
orphaning files on a host.

## Things to try

- **Cancel.** Patch `apply` back to `false` while the uninstall is running. The
  package installs again. (A package that declares an `interrupt` can no longer be
  cancelled once its uninstall interrupt has fired.)
- **Uninstall on delete.** Reinstall, then `kubectl delete` the NodeWright. A
  finalizer runs the uninstall on every node before the resource goes away.
- **Downgrade.** Lower `version` while `apply` is `false`. The webhook rejects it:
  to downgrade a package with `uninstall.enabled`, uninstall it first.

## Clean up

```bash
kubectl delete -f examples/uninstall/nodewright.yaml
kubectl label node "$NODE" nodewright.nvidia.com/example-
```

## Related

- [Explicit Uninstall](../../docs/user-guide/uninstall.md): every workflow, the
  deletion edge cases, and how uninstall interacts with `dependsOn`
- [Lifecycle of a NodeWright](../../docs/architecture/lifecycle.md): the uninstall lane
