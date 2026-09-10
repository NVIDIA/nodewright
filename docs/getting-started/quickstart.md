# Quickstart

Install NodeWright and run a hello-world package on a single node. Should take
about five minutes.

This is the fast path. [Installation](installation.md) covers private registries,
image pull secrets, and uninstall in full.

## Prerequisites

- A Kubernetes cluster you can `kubectl` into (v1.30+)
- Helm 3.8+ (needed for native OCI support)
- A node you are willing to have NodeWright touch
- A clone of this repository, for the example manifest in step 3:

```bash
git clone https://github.com/NVIDIA/nodewright.git
cd nodewright
```

## 1. Install the operator

Pulling from a **private registry?** Create the image pull secret first, then add
`--set imagePullSecret=node-init-secret` to the command below — otherwise the
operator pod cannot pull and the wait in the next step times out. See
[Installation](installation.md#configure-image-pull-secrets-if-needed).

```bash
helm install nodewright oci://ghcr.io/nvidia/nodewright/charts/nodewright \
  --version <chart-version> \  # latest: https://github.com/NVIDIA/nodewright/releases?q=chart
  --namespace nodewright \
  --create-namespace
```

`--version` is required — GHCR doesn't support installing an OCI chart without
pinning one. Any recent chart release works for this quickstart. For a real
deployment, see [Installation](installation.md#install-nodewright) for more on
pinning intentionally.

Wait for it to come up:

```bash
kubectl wait --for=condition=Available deployment \
  -l control-plane=controller-manager -n nodewright --timeout=300s
```

## 2. Pick one node

The example targets nodes carrying a label that nothing has yet, so applying it
does nothing until you opt a node in. Pick one and label it:

```bash
kubectl get nodes
kubectl label node <node-name> nodewright.nvidia.com/quickstart=true
```

> **Why this way.** A NodeWright with an empty `nodeSelectors` matches **every
> node in the cluster**. Starting from a label you apply by hand keeps the blast
> radius at exactly one node while you are finding your feet.

## 3. Apply the hello world

```bash
kubectl apply -f examples/simple/scr.yaml
```

That is [`examples/simple/scr.yaml`](../../examples/simple/scr.yaml):

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: demo
spec:
  nodeSelectors:
    matchLabels:
      nodewright.nvidia.com/quickstart: "true"
  packages:
    hello-world:
      version: 1.1.0
      image: ghcr.io/nvidia/skyhook-packages/shellscript
      configMap:
        config.yaml: |-
          #!/bin/bash
          sleep 1
          echo "Hello, config!"
        config_check.yaml: |-
          #!/bin/bash
          sleep 1
          echo "Hello, config check!"
```

## 4. Watch it run

```bash
kubectl get nodewright demo -w
```

You are waiting for `status` to reach `complete`. While it works, the operator
creates a Job on your node for each stage:

```bash
kubectl get pods -n nodewright -l nodewright.nvidia.com/name=demo
```

To see the script's output, read the log of the container named for the stage:

```bash
kubectl logs -n nodewright <pod-name> -c hello-world-config
```

You should see `Hello, config!`. Finished Jobs are garbage-collected after a
while, so look reasonably promptly — or use
[`kubectl nodewright package logs`](../user-guide/cli.md), which retrieves them
for you.

The record of what ran lives on the **node**, not on the CR:

```bash
kubectl get node <node-name> \
  -o jsonpath='{.metadata.annotations.nodewright\.nvidia\.com/nodeState_demo}'
```

## What just happened

### The custom resource

Four parts of that manifest did all the work. Every field is covered in
[The NodeWright Custom Resource](../user-guide/custom-resource.md).

| Part | What it did |
|---|---|
| `nodeSelectors` | Chose which nodes to act on. Empty means *all* nodes — always set it deliberately. |
| `packages` | The unit of work. The map key (`hello-world`) is the package name. |
| `image` + `version` | `version` is the tag **and** the operator's ordering key, so it must be semver and the tag must not be inlined into `image`. |
| `configMap` | Configuration handed to the package. The `shellscript` package treats these keys as scripts, which is why no image build was needed. |

Two knobs you did not set are worth knowing before you go near a real cluster:
`interruptionBudget` defaults to **100%** — every matching node at once — and
`interrupt` is what makes a package cordon and drain a node before it runs.

### The lifecycle

The package moved through the install lane: **apply**, then **config**. Because
it declared no `interrupt`, it stopped there and the node was never cordoned or
drained. [Lifecycle of a NodeWright](../architecture/lifecycle.md) has the whole
picture.

The part worth internalising is what your two configMap keys were:

```text
config.yaml         →  the config stage's WORK step
config_check.yaml   →  the config stage's CHECK step
```

**Every work step has a paired check step, and the stage is not done until the
check passes.** That is not a naming convention — the operator runs them as
consecutive init containers in the same pod, so Kubernetes itself stops at the
first one that fails. `interrupt` is the sole exception, having no check step.

## Clean up

```bash
kubectl delete -f examples/simple/scr.yaml
kubectl label node <node-name> nodewright.nvidia.com/quickstart-
```

Deleting the CR does **not** undo what a package did to the host. This one only
echoed text, so there is nothing to undo — but for real packages that is the
distinction between removing a NodeWright and
[uninstalling](../user-guide/uninstall.md) its packages.

To remove the operator entirely:

```bash
helm uninstall nodewright --namespace nodewright
```

## Next steps

- [Overview](overview.md) — what NodeWright is for and when to reach for it
- [The NodeWright Custom Resource](../user-guide/custom-resource.md) — every field
- [Lifecycle of a NodeWright](../architecture/lifecycle.md) — how a rollout actually runs
- [Interrupt Flow](../architecture/interrupt-flow.md) — cordon, drain, reboot
- [Deployment Policy](../user-guide/deployment-policy.md) — shaping a rollout across many nodes
- [`examples/`](../../examples/) — including an interrupt example with real workloads
