# The NodeWright Custom Resource

The `NodeWright` custom resource is the declarative description of work you want
done on hosts: which nodes to touch, what to run on them, and how fast to roll it
out. This page is the field-by-field reference. Where a field has a guide of its
own, this page gives you the shape and the gotcha and links to that guide.

`NodeWright` is **cluster-scoped**. The install namespace determines only where
the operator and its package pods live, never which resources a client can see.

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
```

> **Legacy group.** The `skyhook.nvidia.com/v1alpha1 Skyhook` kind still exists,
> read-only, for a migration window. Field names and semantics are identical —
> everything on this page applies to both. See
> [Migration from Skyhook](../getting-started/migration.md).

---

## A complete example

Every field, in one resource. Almost nothing here is required — see the tables
below for what you can drop.

```yaml
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: gpu-tuning
  annotations:
    nodewright.nvidia.com/pause: "false"    # "true" stops this and everything after it
    nodewright.nvidia.com/disable: "false"  # "true" skips this one and keeps going
spec:
  # --- targeting: which nodes -------------------------------------------
  nodeSelectors:
    matchLabels:
      nvidia.com/gpu.present: "true"
    matchExpressions:
      - key: node-role.kubernetes.io/control-plane
        operator: DoesNotExist

  # --- rollout shape: how many at once ----------------------------------
  interruptionBudget:
    percent: 10          # or count: 5 — never both, and never with deploymentPolicy

  # deploymentPolicy: gpu-rollout          # mutually exclusive with interruptionBudget
  # deploymentPolicyOptions:
  #   resetBatchStateOnCompletion: true

  # --- ordering ---------------------------------------------------------
  priority: 200          # lower runs first
  sequencing: node       # or "all" for a cluster-wide barrier
  serial: false          # true = one package at a time per node

  # --- interrupt handling -----------------------------------------------
  podNonInterruptLabels:
    matchLabels:
      workload: training   # these pods must leave before drain starts

  drainConfig:
    disableEviction: false
    deleteEmptyDirData: true
    force: true
    ignoreDaemonSets: true
    timeout: 10m
    gracePeriod: 30s

  additionalTolerations:
    - key: nvidia.com/gpu
      operator: Exists
      effect: NoSchedule

  # --- gating new nodes -------------------------------------------------
  runtimeRequired: true
  autoTaintNewNodes: true

  # --- the work ---------------------------------------------------------
  packages:
    tuning:                                    # map key IS the package name
      version: 1.2.3
      image: ghcr.io/nvidia/skyhook-packages/tuning
      containerSHA: sha256:0000000000000000000000000000000000000000000000000000000000000000
      agentImageOverride: ghcr.io/nvidia/nodewright/agent:6.4.2
      gracefulShutdown: 5m
      stageTimeout: 30m
      env:
        - name: TUNING_PROFILE
          value: throughput
      resources:
        cpuRequest: 100m
        cpuLimit: 500m
        memoryRequest: 128Mi
        memoryLimit: 512Mi
      configMap:
        tuning.conf: |
          vm.swappiness=1
        network.properties: |
          net.core.somaxconn=1024
      configInterrupts:
        tuning.conf:
          type: service
          services: [containerd]
        "*.properties":                        # only '*' is supported
          type: restartAllServices
      interrupt:
        type: reboot
      uninstall:
        enabled: true
        apply: false

    dependent-package:
      version: 0.1.0
      image: ghcr.io/nvidia/skyhook-packages/dependent
      dependsOn:
        tuning: 1.2.3                          # name: version
```

---

## `spec` reference

| Field | Type | Default | Description |
|---|---|---|---|
| [`nodeSelectors`](#specnodeselectors) | LabelSelector | `{}` — **all nodes** | Which nodes this resource applies to. |
| [`interruptionBudget`](#specinterruptionbudget) | object | 100% — **all matching nodes at once** | How many matching nodes may be worked on concurrently. |
| [`deploymentPolicy`](#specdeploymentpolicy-and-specdeploymentpolicyoptions) | string | none | Name of a `DeploymentPolicy` for compartment-based rollout. Mutually exclusive with `interruptionBudget`. |
| [`deploymentPolicyOptions`](#specdeploymentpolicy-and-specdeploymentpolicyoptions) | object | none | Per-resource overrides of the referenced policy. |
| [`priority`](#specpriority-and-specsequencing) | int ≥ 1 | `200` | Order across NodeWrights; lower runs first. |
| [`sequencing`](#specpriority-and-specsequencing) | `node` \| `all` | `node` | Whether priority is enforced per-node or cluster-wide. |
| [`serial`](#specserial) | bool | `false` | Run one package at a time per node instead of in parallel. |
| [`podNonInterruptLabels`](#specpodnoninterruptlabels) | LabelSelector | `{}` — **no pods** | Pods that must leave a node before drain begins. |
| [`drainConfig`](#specdrainconfig) | object | see below | Tunes how nodes are drained before interrupt stages. |
| [`additionalTolerations`](#specadditionaltolerations) | []Toleration | none | Tolerations added to every package pod. |
| [`runtimeRequired`](#specruntimerequired-and-specautotaintnewnodes) | bool | `false` | Node must complete this resource before workloads may run. |
| [`autoTaintNewNodes`](#specruntimerequired-and-specautotaintnewnodes) | bool | `false` | Operator applies the runtime-required taint to new matching nodes. Only meaningful with `runtimeRequired: true`. |
| [`packages`](#packages) | map[string]Package | none | The DAG of work to apply. |

---

## Targeting: which nodes get this

Three mechanisms decide whether a given node is worked on, and they compose in
this order:

1. **`nodeSelectors`** picks the candidate set out of the whole cluster.
2. **The deployment policy** (or `interruptionBudget`) decides which of those
   candidates are in the current batch.
3. **The ignore label** blocks individual nodes that were selected anyway.

### `spec.nodeSelectors`

A standard Kubernetes `LabelSelector` — `matchLabels`, `matchExpressions`, or
both — evaluated against **node** labels.

```yaml
spec:
  nodeSelectors:
    matchLabels:
      nvidia.com/gpu.present: "true"
    matchExpressions:
      - key: node-role.kubernetes.io/control-plane
        operator: DoesNotExist
```

**An empty or omitted `nodeSelectors` matches every node in the cluster.** This
is the single most common way to accidentally roll something out everywhere; it
is not an error and the webhook will not warn you.

Node labels are re-evaluated continuously. Relabelling a node into the selector
enrolls it; relabelling it out removes it from the resource's scope, which is a
different thing from uninstalling the packages it already received — the host
changes stay. Use [explicit uninstall](uninstall.md) to actually reverse work.

### The `nodewright.nvidia.com/ignore` label

Set on a **node**, this tells the operator to leave that node alone:

```bash
kubectl label node <node> nodewright.nvidia.com/ignore=true
```

The value must be exactly the string `"true"`. Any other value — including
`"True"` or `"1"` — is treated as not ignored.

What it does, precisely:

- The node still matches `nodeSelectors` and counts toward the population used
  to calculate the interruption-budget ceiling and batch size. It does **not**
  hold a concurrency slot, even if its package Job is still running. The label
  does not cancel an already-running Job.
- Its `NodePriority` entry and package state are preserved. Other eligible batch
  members finish before new nodes are selected; removing the ignore label makes
  the node eligible to resume in its sticky place.
- Ignoring a cordoned node does not uncordon it. After un-ignoring, normal
  processing resumes and releases this NodeWright's cordon on completion. For
  manual recovery, see [Orphaned Cordon Recovery](../architecture/interrupt-flow.md#orphaned-cordon-recovery);
  reset alone does not clear `spec.unschedulable`.
- An unfinished node's status is set to **`blocked`**, and the NodeWright gets a
  `NodesIgnored` condition set to `True` naming the ignored nodes (truncated to a
  count when the list is long).
- Because the node is blocked rather than complete, a NodeWright with
  `sequencing: all` will never finish while an ignored node is in scope, and
  everything behind it in priority order waits. Use `disable` on the NodeWright,
  or take the node out of `nodeSelectors`, if that is not what you want.

The label applies to **all** NodeWrights targeting that node — it is a property
of the node, not of any one resource. It is the right tool for quarantining a
node you are debugging by hand; it is the wrong tool for permanently excluding a
class of nodes, which belongs in `nodeSelectors`.

### `spec.deploymentPolicy` and `spec.deploymentPolicyOptions`

`deploymentPolicy` names a cluster-scoped `DeploymentPolicy` that replaces the
flat interruption budget with **compartments** — named node selectors, each with
its own ceiling and batch strategy (fixed, linear, or exponential), so a rollout
can advance cautiously and back off on failures.

```yaml
spec:
  deploymentPolicy: gpu-rollout
  deploymentPolicyOptions:
    resetBatchStateOnCompletion: true
```

Two constraints worth knowing before you reach for it:

- `deploymentPolicy` and `interruptionBudget` are **mutually exclusive** — the
  webhook rejects a resource that sets both.
- The named policy must already exist. The webhook rejects a reference to a
  missing policy on create, and a NodeWright whose policy is deleted out from
  under it goes `blocked`.

`deploymentPolicyOptions` currently carries a single override,
`resetBatchStateOnCompletion`, which wins over the policy's own setting for this
NodeWright only.

Full reference — compartments, strategy parameters, overlapping selectors,
tie-breaking, batch stickiness and reset:
**[Deployment Policy and Compartments](deployment-policy.md)**.

---

## Rollout control

### `spec.interruptionBudget`

How many matching nodes may be in progress at once. Exactly one of `percent` or
`count`:

```yaml
spec:
  interruptionBudget:
    percent: 10     # 0-100
    # count: 5      # setting both is rejected by the webhook
```

**Omitting `interruptionBudget` entirely means 100% — every matching node at
once.** Combined with an empty `nodeSelectors`, that is a whole-cluster
simultaneous rollout. Set a budget deliberately.

`percent` is computed against the number of nodes matching `nodeSelectors` and
floors to a minimum of one node, so a small percentage on a small cluster still
makes progress rather than stalling.

### `spec.serial`

```yaml
spec:
  serial: false
```

By default the operator starts every package whose dependencies are satisfied at
the same time on a given node. With `serial: true` it applies one package per
node per reconcile pass instead, re-queuing to pick up the next.

This is about packages **within** one node, not about how many nodes run at
once. **`serial` will not roll out one node at a time** — for that, set
`interruptionBudget.count: 1`, or give the NodeWright a `deploymentPolicy` whose
strategy has a batch size of 1 (a `fixed` strategy with `initialBatch: 1`; the
`linear` and `exponential` strategies grow the batch after a successful round).

Reach for `serial` when packages contend for the same host resource — a package
manager lock, a device — in a way `dependsOn` cannot express, and accept that a
node takes proportionally longer to converge.

### `spec.priority` and `spec.sequencing`

```yaml
spec:
  priority: 200     # minimum 1, lower runs first
  sequencing: node  # or "all"
```

`priority` orders NodeWrights against each other. `sequencing: node` (the
default) lets each node move past this resource independently as soon as it
completes there; `sequencing: all` holds every node at this priority until all
nodes finish, making it a cluster-wide barrier — and a deadlock risk if
misapplied.

Recommended priority buckets, node ordering within a rollout
(`SKYHOOK_NODE_ORDER`), mixing sequencing modes, and the deadlock cases:
**[Strict Ordering](../architecture/ordering.md)**.

### `spec.podNonInterruptLabels`

A `LabelSelector` over **pods**. Pods matching it are treated as
non-interruptible: they must complete or move off the node on their own before
the operator begins draining it.

```yaml
spec:
  podNonInterruptLabels:
    matchLabels:
      workload: training
```

This is a barrier that runs *before* the configurable drain — it is not a drain
exclusion. The operator will wait indefinitely for a matching pod that never
finishes, so pair it with `drainConfig.timeout` if you need a bound.

**Note the asymmetry with `nodeSelectors`:** an empty `podNonInterruptLabels` is
special-cased to mean *no pods are protected*, not *all of them*. The two fields
are both `LabelSelector`s and both default to `{}`, but leaving this one empty is
the safe outcome, where leaving `nodeSelectors` empty is the dangerous one.

Sequence details: **[Interrupt Flow](../architecture/interrupt-flow.md)**.

### `spec.drainConfig`

Tunes the drain that precedes interrupt stages. Only meaningful for packages that
declare an `interrupt`.

| Field | Type | Default | Description |
|---|---|---|---|
| `disableEviction` | bool | `false` | Delete pods directly instead of evicting. **Bypasses PodDisruptionBudgets.** |
| `deleteEmptyDirData` | bool | `true` | When `false`, pods with `emptyDir` volumes block drain. |
| `force` | bool | `true` | When `false`, pods with no managing controller block drain. |
| `ignoreDaemonSets` | bool | `true` | Skip DaemonSet-managed pods. |
| `timeout` | duration | none | Bounds the whole drain. Unset or `0` means **no timeout**. |
| `gracePeriod` | duration | pod's own | Overrides `terminationGracePeriodSeconds` for evict/delete. |

Drain waits for selected pods to actually disappear, not merely for their
evictions to be accepted — so workload termination grace periods are on the
critical path of every interrupt, and a pod stuck terminating holds the node in
`in_progress` forever unless `timeout` is set. On timeout the node goes
`erroring` and no package stages run on it.

Which pods the operator never drains, and how to recover from a drain timeout:
**[Interrupt Flow → Drain Configuration](../architecture/interrupt-flow.md#drain-configuration)**.

### `spec.additionalTolerations`

Standard Kubernetes tolerations added to **every** package pod this resource
creates.

```yaml
spec:
  additionalTolerations:
    - key: nvidia.com/gpu
      operator: Exists
      effect: NoSchedule
```

Package pods must tolerate every taint on a target node or they will not
schedule. A node whose taints are not tolerated is marked `blocked` and the
NodeWright reports a `TaintNotTolerable` condition — that condition is usually
the answer when a node is selected but nothing happens on it. The operator adds a
set of default tolerations on your behalf; these are additive to those.

See **[Taints](taints.md)** for the defaults and the common symptoms.

### `spec.runtimeRequired` and `spec.autoTaintNewNodes`

```yaml
spec:
  runtimeRequired: true
  autoTaintNewNodes: true
```

`runtimeRequired: true` declares that this NodeWright must be complete on a node
before general workloads may run there. Nodes are expected to join the cluster
already carrying the runtime-required taint; the operator removes it only once
the work is done.

`autoTaintNewNodes` lets the operator apply that taint itself to **new** matching
nodes — where "new" means a node carrying no `nodewright.nvidia.com/*`
annotations. It is a one-way judgement: once the operator has touched a node, it
is never "new" again, so this does not retroactively protect existing nodes and
does not re-taint a node you have reset by hand.

Prerequisites, the taint key rename, and what `runtimeRequired` will *not* do:
**[Runtime Required](runtime-required.md)**.

---

## Packages

`spec.packages` is a map of package name to package definition, forming a DAG via
`dependsOn`.

```yaml
spec:
  packages:
    tuning:              # the map key is the package name
      version: 1.2.3
      image: ghcr.io/nvidia/skyhook-packages/tuning
```

**The map key is the name.** Do not also set `name:` inside the package body —
the webhook rejects it. Names must match `^[a-z][-a-z0-9]{0,41}[a-z]$` (max 43
characters).

| Field | Type | Required | Description |
|---|---|---|---|
| [`version`](#version-image-and-containersha) | string | ✓ | Semver. Drives upgrade/downgrade detection and supplies the image tag. |
| [`image`](#version-image-and-containersha) | string | ✓ | Bare `registry/repository` reference — **no tag, no digest**. |
| [`containerSHA`](#version-image-and-containersha) | string | | `sha256:<64 hex>`. Pins the exact image bytes. |
| [`interrupt`](#interrupt) | object | | Interrupt to run after this package applies. |
| [`dependsOn`](#dependson) | map[string]string | | `name: version` of packages that must complete first. |
| [`configMap`](#configmap-and-configinterrupts) | map[string]string | | Configuration delivered to the package. |
| [`configInterrupts`](#configmap-and-configinterrupts) | map[string]Interrupt | | Interrupt to run when a given config key changes. Key may be a `*` glob. |
| [`env`](#env) | []EnvVar | | Environment variables for the package container. |
| [`agentImageOverride`](#agentimageoverride) | string | | Fully-qualified agent image for this package only. |
| [`resources`](#resources) | object | | CPU/memory requests and limits. All four or none. |
| [`gracefulShutdown`](#gracefulshutdown) | duration | | Pod `terminationGracePeriodSeconds` for this package. |
| [`stageTimeout`](#stagetimeout) | duration | | Bounds one attempt at each stage. |
| [`uninstall`](#uninstall) | object | | Explicit uninstall support. |

### `version`, `image`, and `containerSHA`

These three are one mechanism, split across three fields, and the split is
enforced:

```yaml
version: 1.2.3
image: ghcr.io/nvidia/skyhook-packages/tuning
containerSHA: sha256:0000…0000
```

- **`image` must be a bare repository reference.** An inline tag
  (`…/tuning:1.2.3`) or digest (`…/tuning@sha256:…`) is **rejected** by the
  webhook, not silently stripped. A colon before the final `/` is read as a
  registry port, so `localhost:5000/org/pkg` is fine.
- **`version` supplies the tag** and is the operator's ordering key — it is how
  upgrade, downgrade, and fresh-apply are told apart, which is why it cannot live
  in the image string. It must be valid semver;
  [versioning is strictly enforced](../operations/versioning.md).
- **`containerSHA` pins the bytes.** When set, the kubelet pulls that digest
  instead of the version tag. `version` still drives lifecycle decisions, so
  bumping the SHA without bumping the version does not trigger an upgrade.

Downgrades are restricted: for a package with `uninstall.enabled: true`, the
webhook rejects a version decrease unless the package was explicitly uninstalled
first. See [Uninstall](uninstall.md#downgrade-version-change).

### `dependsOn`

```yaml
dependent-package:
  version: 0.1.0
  image: ghcr.io/nvidia/skyhook-packages/dependent
  dependsOn:
    tuning: 1.2.3
```

A map of package name to version. Both parts matter — the version string may not
be empty. The operator builds a dependency graph across the whole `packages` map
and refuses the resource if it is not a valid DAG (cycles, or a dependency on a
package that is not defined). Packages with no unmet dependencies run in
parallel unless [`serial`](#specserial) says otherwise.

### `configMap` and `configInterrupts`

`configMap` is the configuration handed to the package. The operator copies this
map verbatim into a Kubernetes ConfigMap, so the keys are **ConfigMap keys**:
alphanumerics, `-`, `_`, and `.` only, with values that are valid UTF-8.

**A key cannot contain `/`.** Keys are filenames, not paths — `sysctl.d/99.conf`
is rejected by the apiserver when the operator creates the ConfigMap. Where the
file lands on the host is the package's business, not the key's.

```yaml
configMap:
  tuning.conf: |
    vm.swappiness=1
  network.properties: |
    net.core.somaxconn=1024
configInterrupts:
  tuning.conf:
    type: service
    services: [containerd]
  "*.properties":
    type: restartAllServices
```

`configInterrupts` maps a config key to the interrupt that should run **when that
specific key changes** — so editing a value that only needs a service bounce does
not cost you a node reboot, while the key that does need one still gets it.

- A key may be a literal config key or a glob. **Only `*` is supported** as a
  metacharacter.
- Every entry must match at least one key actually present in `configMap`. A
  literal key that does not exist, or a glob that matches nothing, is rejected by
  the webhook — this catches the typo that would otherwise silently skip an
  interrupt.

For secrets, do **not** put them in `configMap`. See
[Providing Secrets to Packages](providing-secrets.md).

### `interrupt`

```yaml
interrupt:
  type: service
  services: [containerd, kubelet]
```

| `type` | What runs on the host |
|---|---|
| `reboot` | `reboot` |
| `service` | `systemctl daemon-reload`, then `systemctl restart <s>` per entry in `services` |
| `restartAllServices` | `service procps force-reload` |
| `noop` | Nothing — the package's own scripts are expected to have handled it |

`services` is only meaningful for `type: service`.

Declaring any interrupt changes the package's lifecycle: the node is cordoned and
drained first, and the stages become
**Apply → Config → Interrupt → Post-Interrupt**.

**Interrupts coalesce.** When several packages become due on the same node in the
same pass, the operator merges their interrupts into a single one, ranked
`reboot` > `restartAllServices` > `service` > `noop`. Two `service` interrupts at
the same rank merge their `services` lists; a `reboot` anywhere in the set wins
outright and the node reboots once instead of restarting services and then
rebooting. So the interrupt a node actually experiences may be stronger than the
one this package declares.

Full sequence and rationale: **[Interrupt Flow](../architecture/interrupt-flow.md)**.

### `env`

```yaml
env:
  - name: TUNING_PROFILE
    value: throughput
```

Standard Kubernetes `EnvVar` entries — including `valueFrom` for secret and
configmap references — added to the package container. The operator appends its
own agent configuration variables (`SKYHOOK_RESOURCE_ID`, node order, and
friends) after yours; see [`agent/README.md`](../../agent/README.md) for those
names, and avoid reusing them.

### `agentImageOverride`

```yaml
agentImageOverride: ghcr.io/nvidia/nodewright/agent:6.4.2
```

Overrides, for this package only, the agent image the operator would otherwise
inject from its own environment. Unlike `image`, this is a **fully-qualified
reference including the tag**. Useful for pinning one package to an older agent
during an agent upgrade; not something to set routinely.

### `resources`

```yaml
resources:
  cpuRequest: 100m
  cpuLimit: 500m
  memoryRequest: 128Mi
  memoryLimit: 512Mi
```

**All four fields or none.** Setting a subset is rejected. Limits must be greater
than or equal to their matching request, and all values must be positive. Without
an override, packages inherit the namespace `LimitRange` defaults.

See **[Resource Management](../operations/resource-management.md)**.

### `gracefulShutdown`

```yaml
gracefulShutdown: 5m
```

Sets the package pod's `terminationGracePeriodSeconds`. Unset uses the Kubernetes
default (30s). Raise it for a package whose scripts must not be killed
mid-operation; note that this is how long the node will wait when the pod is
being torn down.

### `stageTimeout`

```yaml
stageTimeout: 30m
```

Bounds the wall-clock runtime of **one attempt** at each of this package's
stages. An attempt that overruns is killed and retried like any other failure;
the package surfaces as `erroring` once the operator's retry budget
(`JOB_BACKOFF_LIMIT`) is spent. Unset uses the operator default
(`JOB_STAGE_TIMEOUT`).

Four things about this field are easy to get wrong:

- **It bounds an attempt, not the stage.** Total time spent on a stage is roughly
  `stageTimeout × retries`, not `stageTimeout`.
- **Interrupt stages are the exception.** Their attempt must span a reboot, so
  there the value bounds the whole stage instead.
- **`0` removes the time bound**, leaving the retry budget as the only limit —
  and the budget is only spent by attempts that *fail*. An attempt that hangs
  never fails, so with `0` it hangs forever.
- **It is fixed when the stage's Job is created.** The bound lives on the Job's
  pod template, which Kubernetes makes immutable, so editing the field does not
  affect work already running. To apply a new value now, clear the Job —
  `kubectl nodewright package rerun <package>`, or delete the Job — otherwise it
  takes effect at the package's next stage.

One case no `stageTimeout` covers: a pod the kubelet never acknowledges. The
attempt clock runs from the pod's start time, which such a pod never gets, so it
is unbounded at any value. It shows up as a stage stuck `in_progress` with a
`Pending` pod, and it is node health rather than stage health.

### `uninstall`

```yaml
uninstall:
  enabled: true    # this package ships uninstall.sh / uninstall_check.sh
  apply: false     # flip to true to actually uninstall it everywhere
```

`enabled` declares the capability; `apply` triggers the workflow on all target
nodes. Setting `apply: true` without `enabled: true` is rejected. Setting `apply`
back to `false` cancels a pending uninstall (the webhook warns rather than
rejects).

Removing an `enabled: true` package from `spec.packages` before its uninstall has
completed is **rejected** — uninstall first, then remove.

Workflows, finalizer-driven cleanup on CR deletion, DAG interaction, and known
issues: **[Uninstall](uninstall.md)**.

---

## Controlling a live rollout

Two annotations on the NodeWright change flow without editing the spec — which
means toggling them does not bump `metadata.generation`:

| Annotation | Effect |
|---|---|
| `nodewright.nvidia.com/disable` | Skip this NodeWright. Nodes continue on to NodeWrights further down the priority order. |
| `nodewright.nvidia.com/pause` | Stop at this NodeWright. Nothing after it in priority order proceeds on that node either. |

```bash
kubectl annotate nodewright gpu-tuning nodewright.nvidia.com/pause=true --overwrite
# or, equivalently
kubectl nodewright lifecycle pause gpu-tuning
```

`pause` was once a spec field and has moved to annotations. Note the CLI is
version-gated on this — see the compatibility matrix in
**[CLI Reference](cli.md)**.

To exclude a *node* rather than a NodeWright, use
[the ignore label](#the-nodewrightnvidiacomignore-label).

---

## Validation rules

The admission webhook rejects a `NodeWright` that violates any of these. Most are
worth knowing before you hit them:

### Resource level

- `deploymentPolicy` and `interruptionBudget` are both set.
- `deploymentPolicy` names a policy that does not exist.
- `interruptionBudget` sets both `percent` and `count`.
- `drainConfig.timeout` or `drainConfig.gracePeriod` is negative.
- `nodeSelectors` or `podNonInterruptLabels` is not a valid label selector.
- `priority` is less than 1, or `sequencing` is not `node` or `all`.

### Package level

- `name` is set inside the package body (it comes from the map key).
- A package name does not match `^[a-z][-a-z0-9]{0,41}[a-z]$`.
- `version` is not valid semver.
- `image` contains an inline tag or digest, is empty, or contains whitespace.
- `containerSHA` is not `sha256:` followed by 64 lowercase hex characters.
- `interrupt.type` is not one of `service`, `reboot`, `noop`,
  `restartAllServices`.
- A `configInterrupts` key matches no key in `configMap`.
- `resources` sets some but not all four fields; a limit is below its request; or
  any value is not positive.
- `stageTimeout` is negative.
- `uninstall.apply: true` with `uninstall.enabled: false`.
- The `dependsOn` graph is not a valid DAG, or a `dependsOn` version is empty.

### Update only

- A package with `uninstall.enabled: true` is removed from the spec before its
  uninstall completed.
- A package with `uninstall.enabled: true` is downgraded without an explicit,
  completed uninstall first.

---

## What about `status`?

`status` is not a knob — it is the operator's published observation of the world,
and the operator re-derives it from the cluster on every reconcile rather than
reading it back as truth. Editing it does not change behavior.

It is worth reading, though: `status.nodeState` and `status.nodeStatus` are how
you find out what a rollout is actually doing, and the `conditions` list carries
`NodesIgnored`, `TaintNotTolerable`, and `DeploymentPolicyNotFound` — usually the
fastest answer to "why is nothing happening on this node".

Per-node execution state is persisted as annotations on the **Node**
(`nodewright.nvidia.com/nodeState_<name>`), not on the NodeWright.

For the Status / State / Stage vocabulary and what each value means:
**[Operator Status Definitions](../architecture/operator-status.md)**.

---

## See also

- [Deployment Policy and Compartments](deployment-policy.md)
- [Interrupt Flow](../architecture/interrupt-flow.md)
- [Strict Ordering](../architecture/ordering.md)
- [Runtime Required](runtime-required.md)
- [Taints](taints.md)
- [Uninstall](uninstall.md)
- [Providing Secrets to Packages](providing-secrets.md)
- [Resource Management](../operations/resource-management.md)
- [Versioning](../operations/versioning.md)
- [CLI Reference](cli.md)
