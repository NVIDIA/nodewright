# Example: rolling out a real package

The other examples use throwaway scripts. This one applies a package from the
[NodeWright package catalog](https://github.com/NVIDIA/nodewright-packages),
`copy-fail`, the way you would roll it out for real.

`copy-fail` is the temporary host mitigation for CVE-2026-31431. It blacklists the
`algif_aead` kernel module through `/etc/modprobe.d/disable-algif.conf` and makes a
best-effort `rmmod`. It does not reboot the node. See the
[package README](https://github.com/NVIDIA/nodewright-packages/tree/main/copy-fail)
for what it does and does not cover.

What makes the manifest production-shaped rather than a demo:

- **Selection is deliberate.** It names its nodes with a label and excludes the
  control plane with a `matchExpressions` rule. An empty `nodeSelectors` would match
  every node.
- **The rollout is bounded.** `interruptionBudget.percent: 25` caps how many nodes
  run at once. With no interrupt nothing is drained, but an unset budget still means
  100%.
- **There is a way back.** `uninstall.enabled: true` lets you remove the
  mitigation by flipping `apply` once your distribution ships a fixed kernel.

## Run it

Start on one node:

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=copy-fail
kubectl apply -f examples/copy-fail-mitigation/nodewright.yaml
kubectl get nodewright copy-fail-mitigation -w
```

Check the result on the node. From a [debug pod](../README.md#reading-files-on-a-node):

```bash
chroot /host cat /etc/modprobe.d/disable-algif.conf
chroot /host sh -c 'lsmod | grep algif_aead || echo "algif_aead not loaded"'
```

If `config-check` fails because the module is still in use, the node reads
`erroring` until it reboots. That is the package being honest that the node is
still exposed. Reboot on your own schedule, or set `ALLOW_LOADED_MODULE` (commented
in the manifest) to accept the risk until then.

Widen the rollout by labelling more nodes, or replace the label selector with one
that matches the fleet.

## Retire it

When fixed kernels are deployed:

```bash
kubectl patch nodewright copy-fail-mitigation --type=merge \
  -p '{"spec":{"packages":{"copy-fail":{"uninstall":{"apply":true}}}}}'
```

Wait for `complete`, then delete the NodeWright. See the
[`uninstall`](../uninstall/README.md) example.

## On read-only host filesystems

On nodes with a read-only root such as GKE Container-Optimized OS, the operator
needs a writable directory for the agent. The package README covers the Helm
values (`copyDirRoot`, `reapplyOnReboot`).
