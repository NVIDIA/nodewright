# NodeWright Examples

Manifests you can apply to a cluster to see NodeWright do something specific. Each
directory has its own README with the commands to run and what you should see.

If you have not used NodeWright before, start with [`simple/`](simple/), then work
down the table.

| Example | What it shows | Where to run it |
|---|---|---|
| [`simple/`](simple/) | The hello world: one package, no interrupt. Used by the [quickstart](../docs/getting-started/quickstart.md). | Any cluster |
| [`dependencies/`](dependencies/) | `dependsOn`: a diamond of packages, with the run order read back from the host | Any cluster |
| [`uninstall/`](uninstall/) | Reversing a package: `uninstall.enabled` and `uninstall.apply`, cancelling, and delete-time cleanup | Any cluster |
| [`ordering/`](ordering/) | `priority`, `sequencing: all` as a cluster-wide barrier, and `pause` as a brake | Any cluster |
| [`secrets/`](secrets/) | Passing a Kubernetes Secret to a package through `env`, without logging it | Any cluster |
| [`runtime-required/`](runtime-required/) | Gating workloads behind a node being ready, with the runtime-required taint | Any cluster |
| [`rollout/`](rollout/) | A `DeploymentPolicy`: canary, production, and default compartments, and a stopped compartment | Six or more workers; a kind config is included |
| [`interrupt-wait-for-pod/`](interrupt-wait-for-pod/) | `podNonInterruptLabels`: a reboot interrupt that waits for your workload to finish | Three nodes, EKS node group labels |
| [`config-interrupts/`](config-interrupts/) | Matching the interrupt to the change with `configInterrupts`, using the real `tuning` package | Disposable real nodes |
| [`copy-fail-mitigation/`](copy-fail-mitigation/) | A real package from the catalog, rolled out the production way | Real nodes |
| [`debug_pod.yaml`](debug_pod.yaml) | A privileged pod with the node's filesystem mounted, for looking at what a package did | Any cluster |

## Conventions

- **Nothing happens until you opt a node in.** Every example selects nodes by the
  label `nodewright.nvidia.com/example=<name>`, which no node carries by default.
  A NodeWright with an empty `nodeSelectors` matches *every* node in the cluster, so
  these examples never rely on that. Label one node and the blast radius is one node.
- **Packages come from [`nodewright-packages`](https://github.com/NVIDIA/nodewright-packages).**
  Most examples use the `shellscript` package, which runs the scripts you put in
  `configMap`. Its script names are fixed: `apply.sh`, `apply_check.sh`, `config.sh`,
  `config_check.sh`, `post_interrupt.sh`, `post_interrupt_check.sh`, `uninstall.sh`,
  and `uninstall_check.sh`. A script that is missing is skipped with a message and
  counts as success, so a misspelled key does nothing rather than failing.
- **Every step has a check.** A stage is done when its `*_check` script passes, not
  when its work script exits.
- **Scripts run on the host**, as root, in a chroot of the node's filesystem. The
  examples write only under `/etc/nodewright-example/` so cleanup is one `rm -rf`.
- **Clean up after yourself.** Deleting a NodeWright does not undo what its packages
  did to a host. Each README ends with a clean-up section, and says what stays behind.

The commands assume the operator is installed in the `nodewright` namespace, the
default for new installs. Installs that predate the rename are in `skyhook`.

## Reading files on a node

Several examples write a file to the host. To read it, run a debug pod pinned to the
node:

```bash
NODE=<node-name>
sed "s/{{ some hostname }}/$NODE/" examples/debug_pod.yaml | kubectl apply -f -
kubectl exec -ti debug-pod -- chroot /host cat /etc/nodewright-example/<path>
kubectl exec -ti debug-pod -- chroot /host bash     # or an interactive root shell
kubectl delete pod debug-pod
```

The pod is privileged and mounts the whole host filesystem. Delete it when you are done.

## Reading what NodeWright did

```bash
kubectl get nodewright <name> -w                         # overall status
kubectl nodewright node status <node>                    # every NodeWright on a node
kubectl nodewright package status <pkg> --nodewright <name> -o wide
kubectl nodewright package logs <pkg> --nodewright <name> --stage apply
kubectl get nodewright <name> -o jsonpath='{.status.conditions}'   # why nothing is happening
```

The authoritative record of each package is an annotation on the node,
`nodewright.nvidia.com/nodeState_<name>`, not the NodeWright's status. Status,
state, and stage are three different things; see
[Operator Status](../docs/architecture/operator-status.md).

## Adding an example

Keep it to one idea, safe to apply to a single node, and honest about what it leaves
on the host. Use the opt-in label convention above, give every YAML file the
license header (`make fmt` adds it), and link the docs page that explains the
feature. Check it with `kubectl apply --dry-run=server -f` against a cluster that has the
operator installed, so the admission webhook validates it: an example the webhook
rejects is worse than none.
