# Example: matching the interrupt to the change

An interrupt is the disruptive step after a package applies: a service restart or
a reboot. It means the node is cordoned and drained first, so you want the
smallest one that does the job.

This example uses the real [`tuning`](https://github.com/NVIDIA/nodewright-packages/tree/main/tuning)
package to change kernel and ulimit settings, and gives each setting the interrupt
it actually needs:

| configMap key | Changes | Interrupt |
|---|---|---|
| `sysctl.conf` | kernel parameters | `restartAllServices` |
| `ulimit.conf` | per-process limits | none: no `configInterrupts` entry |
| `grub.conf` (commented out) | kernel command line | `reboot` |

`interrupt` is what runs when the package is first applied. `configInterrupts`
takes over for later edits, and runs only the interrupt for the key that changed.

> **This changes real host settings and restarts services.** Use disposable nodes.

## Run it

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=config-interrupts
kubectl apply -f examples/config-interrupts/nodewright.yaml
kubectl get nodewright config-interrupts-demo -w
```

Because the package declares an interrupt, the node is cordoned and drained
before it runs. In another terminal:

```bash
kubectl get node "$NODE" -w
kubectl nodewright package status tuning --nodewright config-interrupts-demo -o wide
```

You will see the package move through `apply`, `config`, `interrupt`, and
`post-interrupt`, and the node uncordon when it finishes.

## Change one setting at a time

**Edit `ulimit.conf`.** Change `nofile=1048576` to another value and re-apply. The
package re-runs `config` and finishes. The node is never cordoned or drained,
because no `configInterrupts` entry matches `ulimit.conf`.

```bash
kubectl patch nodewright config-interrupts-demo --type=merge \
  -p '{"spec":{"packages":{"tuning":{"configMap":{"ulimit.conf":"nofile=524288"}}}}}'
```

**Edit `sysctl.conf`.** Change `vm.max_map_count`. This time the node is cordoned,
drained, and services are restarted, but it is not rebooted.

**Add `grub.conf`.** Uncomment `grub.conf` in both places and re-apply. Now the node
reboots.

> A `reboot` anywhere wins. If several packages become due on one node in the same
> pass, their interrupts merge into one, ranked `reboot` over `restartAllServices`
> over `service` over `noop`. The node reboots once instead of restarting services
> first.

## Related knobs, set in the manifest

- `interruptionBudget.count: 1` means only one node is ever cordoned and drained at
  a time.
- `drainConfig.timeout` bounds a drain that would otherwise wait forever on a pod
  that will not leave. When it expires the node goes `erroring`.
- To keep specific pods from being evicted, see
  [`interrupt-wait-for-pod`](../interrupt-wait-for-pod/demo.md).

## Clean up

`tuning` writes drop-in files, so it can take them out again. Uninstall first, then
delete, otherwise the settings stay on the host:

```bash
kubectl patch nodewright config-interrupts-demo --type=merge \
  -p '{"spec":{"packages":{"tuning":{"uninstall":{"apply":true}}}}}'
kubectl get nodewright config-interrupts-demo -w   # wait for complete
kubectl delete -f examples/config-interrupts/nodewright.yaml
kubectl label node "$NODE" nodewright.nvidia.com/example-
```

See the [`uninstall`](../uninstall/README.md) example for how that workflow works.

## Related

- [Interrupt Flow](../../docs/architecture/interrupt-flow.md): cordon, drain,
  interrupt, uncordon, in full
- [The NodeWright Custom Resource: `configInterrupts`](../../docs/user-guide/custom-resource.md#configmap-and-configinterrupts)
