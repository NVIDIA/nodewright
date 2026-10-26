# Example: package dependencies

Packages in one NodeWright form a graph. `dependsOn` says which packages must be
complete before another one starts, and everything with no unmet dependency runs
in parallel.

This example builds a diamond: `base`, then `service-a` and `service-b` together,
then `report`. Each package appends to a log on the host, so afterwards you can
read off exactly what ran when.

## Run it

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=dependencies
kubectl apply -f examples/dependencies/nodewright.yaml
kubectl get nodewright dependencies-demo -w
```

Wait for `complete`, then read the `report` package's output, which is the log:

```bash
kubectl nodewright package logs report --nodewright dependencies-demo --stage apply
```

```text
--- order in which the packages ran on <node-name> ---
12:00:01 base: start
12:00:06 base: done
12:00:09 service-a: start
12:00:09 service-b: start
12:00:19 service-a: done
12:00:19 service-b: done
```

Your timestamps will differ. What matters:

- Nothing starts before `base: done`.
- `service-a` and `service-b` start together, because neither depends on the other.
- `report` runs only after both are done.

## Things to try

- **Break the graph.** Change `report` to depend on a package that is not defined,
  or make `base` depend on `report`. The webhook rejects both when you apply: the
  graph has to be a valid DAG.
- **Serialize it.** Add `serial: true` under `spec`. Packages that were running in
  parallel now run one at a time per node. That is for packages that fight over
  the same host resource; it does not roll out one node at a time.

## Clean up

```bash
kubectl delete -f examples/dependencies/nodewright.yaml
kubectl label node "$NODE" nodewright.nvidia.com/example-
```

Deleting the NodeWright does not undo what its packages did. The log stays on the
host; remove it from a [debug pod](../README.md#reading-files-on-a-node) with
`chroot /host rm -rf /etc/nodewright-example/dependencies`.

## Related

- [The NodeWright Custom Resource: `dependsOn`](../../docs/user-guide/custom-resource.md#dependson)
- [Lifecycle of a NodeWright](../../docs/architecture/lifecycle.md)
