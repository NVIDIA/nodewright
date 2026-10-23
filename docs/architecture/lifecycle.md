# Lifecycle of a NodeWright

This page follows a `NodeWright` from `kubectl apply` to `complete`, and then
through change and deletion. It is the "what happens, in what order" companion to
[The NodeWright Custom Resource](../user-guide/custom-resource.md), which
describes the fields themselves.

The through-line is the pairing that shows up at every level: **every unit of
work has a check that decides whether it actually worked.** A stage is not done
because its script exited zero — it is done because its check said so.

---

## Three lifecycles, not one

"Lifecycle" means three different things depending on what you are looking at,
and they nest:

| Scope | Question it answers | Where it is recorded |
|---|---|---|
| **The resource** | Is this NodeWright rolling out, paused, blocked, complete? | `status.status` on the CR |
| **A node** | Has this node finished everything this NodeWright asks of it? | `status.nodeStatus[<node>]` |
| **A package on a node** | Which stage is this package in, and did it succeed? | Annotation on the **Node** |

The vocabulary for these — Status, State, Stage — is deliberately similar and
easy to conflate. If you have not read
[Operator Status Definitions](operator-status.md), read it before this page; the
rest of this document uses those three words precisely.

---

## The cluster-level arc

![Lifecycle of a NodeWright in a cluster](images/cr-lifecycle.png)

### 1. Admission

The validating webhook runs before the object is ever stored. It rejects
malformed specs outright — an invalid semver, a tag embedded in `image`, a
`configInterrupts` key matching nothing, a `dependsOn` cycle, a
`deploymentPolicy` that does not exist. The full list is in the
[validation rules](../user-guide/custom-resource.md#validation-rules).

This is the only synchronous feedback you get. Everything after admission is
asynchronous and surfaces through status and conditions.

### 2. Reconcile

The operator is **level-triggered**: each reconcile looks at the current state of
the cluster and works out what to do from scratch. It does not remember the
previous pass, and it does not assume any particular event triggered this one. A
reconcile is queued by changes to the CR, to matching Nodes, or to the package
pods the operator owns.

Each pass builds a snapshot of what packages exist on which nodes and what stage
each is in, then compares that against the spec.

### 3. Node selection

`nodeSelectors` picks the candidate nodes. The interruption budget — or the
compartments of a [DeploymentPolicy](../user-guide/deployment-policy.md) — decides
how many of those may be worked on at once. Nodes carrying the
`nodewright.nvidia.com/ignore` label are selected into the batch and then
skipped as `blocked`, which means **they still consume a slot**.

### 4. Per-node gating

Before any package runs on a node, the node must clear:

- **Priority and sequencing** — higher-priority NodeWrights must have finished
  first, per node or cluster-wide depending on `sequencing`. See
  [Strict Ordering](ordering.md).
- **Taint toleration** — package pods must tolerate every taint on the node, or
  the node is marked `blocked` with a `TaintNotTolerable` condition.
- **Package artifacts** — with the option on, the package's reference must
  resolve to a digest the operator has inspected and can run. A package whose
  artifact is rejected or whose reference the operator cannot read marks the
  node `blocked`; see [What the pass decides](#what-the-pass-decides).
- **Cordon and drain** — only for packages that declare an `interrupt`. See
  [Interrupt Flow](interrupt-flow.md). No node is cordoned or drained for a
  package that gets no Job, such as a
  [package artifact](#package-artifacts) pending inspection or rejected.

### 5. Completion

When every package reaches its terminal stage on a node, the node is uncordoned
(unless the cordon was not NodeWright's; see
[Interrupt Flow](interrupt-flow.md#cordons-nodewright-did-not-create)) and, if `runtimeRequired` is set, the runtime-required taint is removed. When
every selected node is done, the resource reports `complete`.

---

## Package stages on a node

Within a node, packages run in dependency order — `dependsOn` forms a DAG, and
packages with no unmet dependencies run in parallel unless `serial: true`.

Each package then walks its own stage machine. Which stage it *enters* depends on
what changed:

![Package stages on one node](images/package-stages.png)

```text
  install lane
    first install     apply ──▶ config ──▶ [ interrupt ──▶ post-interrupt ] ──▶ complete
    version bump    upgrade ──▶ config ──▶ [ interrupt ──▶ post-interrupt ] ──▶ complete

  uninstall lane
    uninstall.apply   uninstall ──▶ [ uninstall-interrupt ] ──▶ state removed

  [ ] = only when the package declares an interrupt
```

Install and uninstall are separate lanes, not one loop. Two things about the
uninstall lane are worth knowing:

- **An explicit uninstall ends by deleting the package's state, not by
  re-applying it.** When the uninstall finishes, the package's entry is removed
  from the node's state annotation — `absent` *is* how the operator records
  "uninstalled". While `uninstall.apply` stays `true`, every later reconcile sees
  the package as already gone and skips it, so it does not come back.
- **A package with an interrupt routes through a distinct `uninstall-interrupt`
  stage** rather than reusing the install-side interrupt stage. Once it reaches
  that stage the uninstall is no longer cancellable — the interrupt has fired and
  must run to completion.

A package does return in two cases, and neither is the steady state above:

- **Cancel.** Setting `uninstall.apply` back to `false` re-enters the install
  pipeline — from `apply` if the uninstall already completed, or by resetting the
  in-flight uninstall if it has not yet reached `uninstall-interrupt`.
- **Downgrade.** A downgrade runs no uninstall. For a package with
  `uninstall.enabled`, the webhook rejects it until the package has been
  uninstalled from every node, and the new version installs fresh once
  `uninstall.apply` is `false`. Node state is keyed by `name|version`, so as far
  as the operator is concerned those are two different packages: a removal
  followed by a fresh install, not a package coming back. For a package without
  `uninstall.enabled`, the new version applies and the old version's entry stays
  in node state, a marker that its files were never removed.

Removing a package from the spec is a third thing again, and it is not an
uninstall — see [Uninstall](../user-guide/uninstall.md) for how the two differ
and why the webhook blocks the unsafe ordering.

A package only advances when its current stage has completed — which raises the
question of what a stage actually *is*, and what decides that it finished.

---

## What runs a stage: Job, Pod, containers, scripts

One **Job** per package, per stage, per node — not a bare pod. Four layers nest,
and each owns something distinct:

![Job to Pod to stage containers to scripts](images/job-nesting.png)

| Layer | What it owns |
|---|---|
| **Job** (`batch/v1`) | The retry budget. `parallelism: 1`, `completions: 1`, and a `backoffLimit` that decides when a stage gives up. |
| **Pod** | Placement. Pinned to one node by name, and the thing that actually mounts the host. |
| **Containers** | Ordering. `initContainers` run in sequence: copy, then work, then check. A [package artifact](#the-artifact-stage-pod) has no copy. |
| **Scripts** | The real work, executed on the host through a chroot. |

Several Job-level settings exist to stop Kubernetes from doing something
reasonable-but-wrong to a node-pinned host agent:

- **`podReplacementPolicy: Failed`** — a replacement pod is only created once the
  previous one has fully terminated, so two executors never overlap on the shared
  hostPath mounts.
- **A `podFailurePolicy` that ignores `DisruptionTarget`** — an eviction or
  preemption is not the package's fault, so it does not burn a retry.
- **Unbounded `not-ready` / `unreachable` tolerations** — without them the taint
  manager evicts the pod when a reboot-class interrupt takes the node `NotReady`
  past the default 300s, replacing a pod for a node that is coming back.
- **`ttlSecondsAfterFinished` set at completion, by outcome** — failed Jobs are
  kept longer than successful ones, so failure logs are still there when you go
  looking.

Interrupt Jobs invert two of these: `restartPolicy: OnFailure` and an effectively
unbounded `backoffLimit`, because under `OnFailure` the limit counts container
restarts — and the in-place restart *is* the reboot recovery.

The operator bounds a failing interrupt other than a reboot itself instead: once the interrupt fails again after `JOB_BACKOFF_LIMIT` restarts, it sets the Job's `backoffLimit` to 0 so the Job fails. A reboot is bounded only by the stage deadline; see [A failing interrupt](../user-guide/custom-resource.md#a-failing-interrupt).

---

## Inside a stage: the work step and its check step

This is the mechanism that makes the rest of the lifecycle trustworthy.

### The work and check containers

![Inside one stage: the work step and its check step](images/stage-pod-anatomy.png)

Within the stage pod, the steps are `initContainers`, which matters: Kubernetes runs
init containers strictly in order and stops the pod at the first one that fails.

```text
┌─ stage pod ─────────────────────────────────────────────────────────┐
│                                                                     │
│  initContainers        run in order · halt on the first failure     │
│                                                                     │
│    1  <package>-init             copies the package onto the host   │
│           │                                                         │
│           ▼                                                         │
│    2  <package>-<stage>          WORK    agent runs <stage>         │
│           │                                                         │
│           ▼                                                         │
│    3  <package>-<stage>check     CHECK   agent runs <stage>-check   │
│                                                                     │
│  containers                                                         │
│    4  done                       exits 0 so the pod can succeed     │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

Because the check is the container *after* the work container, two properties
fall out for free:

- **The check never runs if the work step failed.** Kubernetes will not start
  init container 3 if init container 2 exited non-zero.
- **The stage is not complete until the check passes.** The pod has not finished
  its init sequence until step 3 succeeds, so the operator never sees the stage
  as done.

The pod's restart policy is `OnFailure`, so a failing work or check step retries
rather than wedging. It surfaces as `erroring` once the operator's retry budget
is spent.

The `done` container is there because a Job completes only when its pod reaches
`Succeeded`, which needs every container to exit. What it runs depends on the Job:

- **A stage Job for a package container image** runs the package image with
  `/bin/sh -c exit 0`. The copy container has already pulled that image and run
  its shell.
- **An [artifact stage Job](#the-artifact-stage-pod), and every interrupt Job
  while [package artifacts](#package-artifacts) are on,** runs the agent image
  with `--version`, because an artifact is not a runnable image and the
  distroless agent image has no shell. Interrupt Jobs take it whatever the
  package is, because a pass resolves nothing for an interrupt stage, so an
  interrupt Job never gets the package image of an artifact.

Only the Go agent answers `--version`. So before turning package artifacts on,
the operator's agent image and every package's `agentImageOverride` must be the
Go agent, `agent/v7.0.0` or later; with the Python agent (v6.x) those Jobs never
complete.

### The artifact stage pod

A package whose [package artifact](#package-artifacts) the operator accepted runs
in a stage pod that differs from the one above in its init containers:

- **There is no `<package>-init` container**, so nothing runs the package as an
  image.
- **The work and check containers get `NODEWRIGHT_PACKAGE_REF`**, the artifact
  pinned to the digest the pass [resolved](#resolution-and-the-cache):
  `registry/repository@sha256:<digest>`.
- **With [pull Secrets](#registry-credentials) set, they also get
  `NODEWRIGHT_REGISTRY_AUTH_FILES`**, the paths of each Secret's
  `.dockerconfigjson`, in order, each mounted read-only from its own volume. The
  volumes are optional, so a missing Secret or key leaves no file, which the agent
  passes over, and the pod still starts.

The agent pulls the artifact, lays its payload out into the same copy directory
the `<package>-init` container would have filled, keeping what that directory
already holds beyond the payload as the container's `cp -r` does, and overlays
the `configmaps/` and `node-metadata/` files mounted in the pod on it.
`SKYHOOK_DIR`, `STEP_ROOT` and every step path are therefore the same as for a
container-image package (see
[Side-loading](#side-loading-what-a-script-can-actually-see)), and a file a step
wrote into the copy directory in an earlier stage is still there. That agent
behavior is the contract that the agent release with artifact support
implements; the operator only sets the two variables, and never on an interrupt
Job. The [package artifact specification](../user-guide/package-artifact-spec.md)
defines what the payload is and how it is laid out.

### The pairing

The check mode is derived from the stage name by appending `-check`:

```text
uninstall        →  uninstall-check
upgrade          →  upgrade-check
apply            →  apply-check
config           →  config-check
post-interrupt   →  post-interrupt-check

interrupt        →  (none)
```

> **Note the two spellings.** The agent *mode* is `<stage>-check`
> (`apply-check`), but the *container name* is `<package>-<stage>check` with no
> hyphen (`tuning-applycheck`). When you are reading `kubectl get pod -o yaml` or
> hunting through logs, the container you want is the one ending in `check`.

### `interrupt` is the exception

`interrupt` is the one work step with no check step. This is not an oversight and
it is not a gap you can fill from a package:

- The agent lists `interrupt` in `NON_STEP_MODES` and has no `INTERRUPT_CHECK`
  mode to dispatch to.
- The operator builds interrupt pods from a **different template** — a single
  init container running the interrupt, with no check container to add.

The reason is that an interrupt is not a script whose success you can inspect
afterward from the same process. A `reboot` interrupt ends the node; nothing
survives to run a check. What plays the verifying role instead is
**`post-interrupt`**, which runs after the node comes back and *does* have a
`post-interrupt-check`. If you need to assert that an interrupt achieved
something, assert it there.

### What "the check passed" actually means

The check step is not a single exit code. A package may define several check
steps for a stage, and the agent evaluates them together:

- **Any failing check fails the whole stage.** There is no partial credit.
- **Not having run every check counts as failure.** If the number of results does
  not match the number of check steps defined, the stage fails rather than
  passing by default.
- **Success is recorded on the host.** The agent writes a
  `<mode>_ALL_CHECKED` flag file so a re-run can skip work already proven done.

That last point is what makes stages cheap to re-run — which matters, because a
level-triggered operator re-runs things.

### Idempotence, and where it does not apply

Most stages are skipped when their flag file already exists. Three are not:
`config`, `uninstall`, and `upgrade` deliberately **ignore** the flag and run
again. Their inputs change in ways a flag cannot capture — a rewritten
`configMap` key, a version transition with a specific from/to — so re-running is
the correct behavior, and a package's scripts for those stages must tolerate
being invoked more than once.

---

## Side-loading: what a script can actually see

A package's scripts never talk to the Kubernetes API. Everything they need is put
in front of them as files, in two moves.

### 1. Three volumes mount into the pod

| Mounted | At | Contents |
|---|---|---|
| `hostPath: /` | `/root` | The node's real filesystem, with `HostToContainer` mount propagation |
| ConfigMap `<nodewright>-<node>-metadata` | `/skyhook-package/node-metadata` | `annotations.json`, `labels.json`, `packages.json` |
| ConfigMap `<nodewright>-<package>-<version>` | `/skyhook-package/configmaps/<key>` | One `subPath` mount per key |

The **node-metadata ConfigMap is generated per node**, and it is how a script
learns anything about where it is running: the node's labels and annotations
serialized to JSON, plus the operator's own view of the package set. The operator
rewrites it when that metadata changes.

The **package ConfigMap is mounted one key at a time**, which is deliberate and
worth understanding before changing it:

> Mounting the whole ConfigMap as a directory would **replace** everything at
> `/skyhook-package/configmaps`, hiding any files the package image baked in
> there. Per-key `subPath` mounts **overlay** individual files on top of the image
> content instead. The trade-off is that `subPath` mounts do not receive live
> ConfigMap updates — harmless here, because a stage pod is recreated on every
> stage and every version bump anyway.

This is also why `configMap` keys cannot contain `/`: each key becomes one
mount path and one filename.

### 2. The init container copies it onto the host

```text
   /skyhook-package/*                    (in the container)
          │
          │  init container:  cp -r  →  /root/$SKYHOOK_DIR
          ▼
   /var/lib/skyhook/<nodewright>/<package>-<version>-<uid>-<generation>/
          ├─ skyhook_dir/...             the step scripts
          ├─ configmaps/...              your configMap keys, as plain files
          └─ node-metadata/...           annotations.json · labels.json · packages.json
```

A package artifact has no copy step: the agent itself lays the payload out at
the same path, merged over what the directory already holds as `cp -r` merges,
with the same `configmaps/` and `node-metadata/` (see
[The artifact stage pod](#the-artifact-stage-pod)), so everything below holds for
it too.

The agent then chroots into `/root` — the host's real root — and runs the step
script out of that copy directory. So by the time a script executes:

- It runs **on the host**, not in the container. The container is a delivery
  mechanism, not the execution environment.
- Its configuration is an ordinary file on disk. A script reads
  `configmaps/<key>` the way it would read any config file, with no awareness
  that Kubernetes was involved.
- The copy directory is keyed by **UID and generation**, so an edited CR produces
  a fresh directory rather than a half-updated one, and two generations never
  share a path.

Two host-absolute paths are exported into every step's environment so a package
never has to hardcode the layout above: **`SKYHOOK_DIR`** (the copy directory
itself) and **`STEP_ROOT`** (its `skyhook_dir/` subdirectory, where the scripts
live). Both are reserved by the agent. The rest of the agent's environment is
documented in [`agent/README.md`](../../agent/README.md).

---

## Package artifacts

A package can be published as an OCI package artifact instead of a container
image; the [package artifact specification](../user-guide/package-artifact-spec.md)
defines the format. Running one is opt-in, through the operator's
`PACKAGE_ARTIFACTS` environment variable (chart value
`controllerManager.manager.env.packageArtifacts`). It is off by default, and
while it is off the operator makes no registry request, reads no cache, and
every Job is unchanged, exactly as the rest of this page describes. Turning it
on requires the Go agent; see [the `done` container](#the-work-and-check-containers).

With it on, before the operator builds a stage Job of a package, it resolves the
package's reference (image plus `version`, or image plus `containerSHA`) to the
manifest digest it names, and builds the Job from what that digest holds: an
artifact, an image, or an artifact it rejects. It learns that by inspecting the
digest once, reading the manifest and, for an artifact, its metadata blob, never
a layer, and keeps it in a [cache](#resolution-and-the-cache) keyed by digest.

### Registry credentials

The operator's pull Secrets are the comma-separated names in
`IMAGE_PULL_SECRETS` (chart value `imagePullSecrets`), after the one name in
`IMAGE_PULL_SECRET` (chart value `imagePullSecret`), each taken once. Every
package pod lists them, in that order, in its `imagePullSecrets`, so the kubelet
pulls package and agent images with them as well as with the node's own
credentials.

To resolve or inspect a reference, the operator reads each Secret through its
namespace-scoped Secret cache at every request, so a rotated or added Secret
applies to the next one. Like the kubelet with a pod's pull Secrets, it tries
each credential that names the registry in turn, in the Secrets' order, and
reads anonymously only when none does. A Secret that cannot be read, has a type
other than `kubernetes.io/dockerconfigjson`, or lacks the `.dockerconfigjson` key
gives a `PackageArtifactPullSecret` Warning and is passed over. An
[artifact stage pod](#the-artifact-stage-pod) mounts the same Secrets, and the
agent pulls the payload with them alone, so the operator and the agent read with
the same credentials.

Neither the operator nor the agent uses the node's Docker configs or the
kubelet's credential provider plugins. While the option is on, the operator
resolves every package this way, a container-image package included, so a
package in a registry that the nodes reach only through their own identity, such
as ECR with the instance role, needs its credential in a pull Secret, whatever
the package is. Without one the operator cannot read the reference, and the
package is [blocked](#what-the-pass-decides). The kubelet still pulls a
container-image package with every credential the node has.

### Resolution and the cache

A pass resolves a package's reference when some unfinished node would run a
stage Job of the package next and no Job of it runs there yet, and when the pass
decides the package's [uninstall](../user-guide/uninstall.md#uninstall-support-declared-by-a-package-artifact).
It resolves nothing for a package whose Job runs, for an interrupt stage, next or
not yet complete, or for a paused or disabled NodeWright that is not being
deleted. A reference costs:

- **Nothing for a `containerSHA`**, which is the digest.
- **One manifest request for a tag** with the pull Secrets, bounded by 5
  seconds, never retried. The digest it names answers for that tag for 30
  seconds, whatever number of packages, NodeWrights and passes name it, so a
  tag costs at most one request per 30 seconds although an unfinished
  NodeWright's pass runs every 2 seconds. The pass resolves the references of
  every NodeWright at once: the registry hosts at once, and on each host one
  reference first, then the rest 8 at a time until one of them times out and the
  rest at once, so a host whose repositories take the connection and never
  answer costs a pass about three timeouts, not one per tag. A digest a tag resolved to answers until those 30 seconds are over, and
  the pass runs again by then, so a push to the tag is picked up within 30
  seconds even on a NodeWright with nothing else to do.

The operator then looks the digest up in its cache: one file per manifest
digest, `sha256/<hex>.json`, under `PACKAGE_ARTIFACT_CACHE_DIR`
(`/var/cache/nodewright/package-artifacts`), which the deployment mounts from a
memory-backed `emptyDir` of 64Mi (chart value
`controllerManager.manager.packageArtifactCacheSizeLimit`). An entry records
whether the digest is an artifact, with what it declares about uninstall, an
image, or a rejected artifact, with the reason, and the repository it was read
at. A digest's content never changes, so an entry never goes stale. Each
reconcile, once it has listed the NodeWrights, first removes the entry of every
digest no NodeWright names: one a package names by `containerSHA`, or the one
its tag resolved to last, whatever the age of that resolution. A tag not
resolved since the operator started, as after a container restart, keeps the
entries of its repository until it is. The cache therefore holds about what the
cluster's NodeWrights use. An entry takes at least one memory page,
so 64Mi holds about 16,000 digests with 4KiB pages, and about 1,000 with the
64KiB pages of some arm64 kernels; a write that fails, as one does once the
volume is full, empties the cache, which inspecting fills again. The cache outlives a
container restart but not the pod or a change of leader, and an empty one is
filled again by inspecting. With the option on, the operator does not start
unless it can write the directory.

A digest the cache lacks is queued for the operator's inspector, a few workers
on the leader that run outside the reconcile pass, each inspection bounded by 10
seconds. Until its entry is written, normally within seconds,
the package is pending: it gets no Job, its node is not cordoned or drained for
it, and the pass runs again 2 seconds later.

What follows from resolving at each stage:

- **Every stage Job runs the digest its pass resolved.** An
  [artifact stage Job](#the-artifact-stage-pod) is pinned through
  `NODEWRIGHT_PACKAGE_REF`; the stage Job of a container image runs its package
  image as `registry/repository@sha256:<hex>`, in its copy and `done`
  containers.
- **A re-pushed tag reaches every stage built more than 30 seconds after the
  push.** Nodes of one
  rollout, or the stages of one node, can therefore run different digests of one
  version; a Job already built keeps its digest (see [Staleness](#staleness)).
  Set `containerSHA` to pin a version to one digest.
- **A restart or a change of leader only resolves and inspects again.** Until a
  digest is cached again its package is pending, so a package that was blocked
  is briefly pending instead: its nodes and the `Blocked` condition clear and
  return once the digest is inspected, and the Warnings recorded once are
  recorded again, once per operator run. A registry outage at that moment holds
  packages, as below, and breaks none.

### What the pass decides

| What the pass found | The stage gets |
|---|---|
| A container image | The unchanged Job, with its package image pinned to the digest |
| An artifact the operator accepts | The [artifact stage Job](#the-artifact-stage-pod), pinned to the digest |
| A digest the cache lacks | No Job until it is inspected: the package is pending |
| A reference that could not be read for now: a timeout, a registry that could not be reached, or one that answered a 5xx, 429 or 408 | No Job: the package waits, and the first pass 30 seconds later reads the reference again. When no connection to the registry host could be made, no reference on that host is contacted for those 30 seconds either, so a black-holed registry costs one timeout per 30 seconds, whatever number of references it serves; a failure once connected holds back only its own reference, so the healthy repositories of the host are still read. A registry the operator can never reach, such as one its egress rules deny, looks the same. A `PackageArtifactInspectionFailed` Warning names the reference and the error class once per outage |
| A reference that could not be read for a reason that does not pass by itself: the registry refused every credential (`unauthorized`), has no such reference (`not-found`), served content that does not match its digest (`digest-mismatch`), presented a certificate the operator does not trust (`tls`), speaks plain HTTP (`plain-http`), has a host name that does not resolve (`no-such-host`), or answered another 4xx about the request itself (`refused`) | No Job: the package is blocked. A `PackageArtifactInspectionFailed` Warning names the reference and the error class. The reference is read again after 5 minutes, then after twice as long for each further failure of the same class in a row, up to an hour, so a pull Secret fixed later, or a push that comes later, is picked up without a change to the spec. The package stays blocked while it is read again |
| A reference that does not parse | No Job: the package is blocked, as `invalid-reference`, until the spec changes |
| An invalid artifact, one of an unsupported form or version, or one declaring host files or an interrupt | No Job: the package is blocked, with a `PackageArtifactRejected` Warning naming the digest and the reason. The digest stays rejected; a corrected push under the same tag names another digest, which a pass picks up within 30 seconds |
| At the `uninstall` stage, an artifact whose metadata declares `"uninstall": "unsupported"` | No Job, and a `PackageArtifactUninstallUnsupported` Warning |

There is no fallback: a package whose reference the operator cannot read never
runs as a container image in its place.

A package that gets no Job does not advance on that node, its node is not
cordoned or drained for it, its interrupt never holds back another package's,
and the pass moves on to the next package. Interrupt stages are never held: the
pass resolves nothing for them, and while the option is on every interrupt
Job's `done` container is the agent.

A blocked package is reported in two places:

- **The `Blocked` condition**, reason `PackageArtifactUnusable`, lists each
  blocked package with its reason or error class, such as
  `2 packages blocked: tuning:1.0.0 (unauthorized), net:2.0.0 (host-files). A package artifact in a private registry needs its pull Secret in imagePullSecrets.`
  It lists up to 10 and counts the rest, and is removed once no spec package is
  blocked, or the option is off. A paused or disabled NodeWright resolves
  nothing, so it keeps the condition as the last pass that ran left it. `Blocked` carries one reason at a time: a
  `DependencyUninstalled` one keeps the condition, and this one replaces a
  `NonInterruptPodsRunning` one (see
  [Other Condition Types](operator-status.md#other-condition-types)).
- **The node's Status is `blocked`** while its next stage needs a blocked
  package, and the NodeWright's is `blocked` once every unfinished node is. A
  blocked node never counts as a failure of its batch.

Deleting a NodeWright waits for the uninstall of each `uninstall.enabled`
package, which a package refused its Job never finishes. So while one gets no
Job at its stage on some node, because it is blocked or its artifact declares
uninstall unsupported, the deletion is blocked with the `DeletionBlocked`
condition, reason `PackageArtifactRefused`, which names it. Correct its artifact
or its pull Secret, or set its `uninstall.enabled` to `false` to delete the
NodeWright without uninstalling it. A package that is pending, or whose
registry cannot be read for now, only waits.

Every Warning is recorded on the NodeWright, each class under its own reason,
and logged by the operator. `PackageArtifactInspectionFailed` is recorded once
for each outage of a reference, and once for each class of other failure in a
row. `PackageArtifactRejected`, and the Normal `UninstallCapability` event, are
recorded when the inspector caches a digest, so once for each digest while the
cache keeps it. `PackageArtifactUninstallUnsupported` is recorded once, when the
`UninstallUnsupported` condition first lists the package, and
`PackageArtifactPullSecret` once for each problem of each Secret, until the
Secret can be read again. The per-package Warnings carry the package's
`name:version` as their action, and `PackageArtifactPullSecret` carries the
problem and the Secret, such as `SecretUnreadable:registry-credentials` (or
`SecretWrongType`, `SecretMissingKey`). Events with the same reason and action
on one object fold into a single series, so every package, class and Secret
keeps its own series instead of being hidden behind the first.

### Staleness

Deciding whether an existing Job is stale reads neither the cache nor a
registry:

- **A re-pushed tag never replaces a Job.** A stage Job pinned to a digest of the
  package's repository matches the package whatever its tag names now.
- **A `containerSHA` pins content.** A Job pinned to another digest is replaced.
- **Another repository replaces a Job**, as any change of `image` does.
- **The option and what a pass resolves make no Job stale.** A Job built while
  the option was off is kept after it is turned on, and an artifact Job after
  it is turned off.

---

## Where the state lives

Per-package state is persisted as an annotation on the **Node**, not on the
NodeWright:

```text
nodewright.nvidia.com/nodeState_<nodewright-name>
  └─ { "<package>|<version>": { "stage": "config", "state": "complete", … } }
```

This is deliberate. The node is the thing being modified, so the record of what
was done to it travels with it. It also means the operator can rebuild its whole
picture from the cluster after a restart or a leader-election change — there is
no reconciler-held memory to lose.

The CR's `status` is a *published observation* of those annotations, re-derived
each pass. Editing status changes nothing; clearing a node's annotation genuinely
does reset that node's work.

---

## Change

A NodeWright is not finished when it reports `complete` — it is converged. Change
the spec and it converges again:

| What you change | What happens |
|---|---|
| Package `version` increases | Package re-enters at `upgrade` |
| Package `version` decreases | With `uninstall.enabled`, rejected unless the package was explicitly uninstalled first; without it, the new version applies |
| A `configMap` key | Nothing happens until every node is complete or one is erroring at `config`, `interrupt` or `post-interrupt`. Then every node at `config`, `interrupt`, `post-interrupt` or complete re-enters at `config`, and its unfinished Jobs are cleared; `configInterrupts` decides whether that costs an interrupt. Nodes at `uninstall`, `apply` or `upgrade`, or without the package, are not reset and pick up the new ConfigMap when they reach `config` |
| `nodeSelectors` | Newly matching nodes are enrolled; newly excluded nodes leave scope — their host changes remain |
| `image` or `containerSHA` only | No stage change — `version` is the ordering key. With package artifacts, the version's next stage Jobs resolve the new reference (see [Resolution and the cache](#resolution-and-the-cache)) |

The `pause` and `disable` annotations stop the rollout without touching the spec,
so toggling them does not bump `metadata.generation`.

---

## Deletion

Deleting a NodeWright does not delete the changes it made to your hosts. The
`nodewright.nvidia.com/nodewright` finalizer holds the object open while the
operator:

1. Runs the uninstall workflow for every package with `uninstall.enabled: true`
2. Cleans its metadata off the nodes — the status labels, annotations and
   conditions it owns, and the cordons it holds. The `nodeState_<name>` and
   `version_<name>` annotations are kept as long as they still record packages
   whose files remain on the host (a non-absent entry means "installed"; see
   [CR Deletion in the uninstall guide](../user-guide/uninstall.md#cr-deletion-finalizer)),
   and removed once nothing remains.
   The runtime-required taint is not touched here; only the completion path in
   [runtime-required](../user-guide/runtime-required.md#when-is-the-runtime-required-taint-removed-from-a-node)
   removes it.
3. Releases the finalizer, at which point the object disappears

Packages without `uninstall.enabled` are simply forgotten, not reversed. Their
host changes stay. The failure modes here — including a deletion that deadlocks
on a package stuck at `erroring` — are catalogued in
[Uninstall](../user-guide/uninstall.md).

---

## See also

- [The NodeWright Custom Resource](../user-guide/custom-resource.md) — every field
- [Operator Status Definitions](operator-status.md) — Status / State / Stage
- [Interrupt Flow](interrupt-flow.md) — cordon, drain, interrupt
- [Strict Ordering](ordering.md) — priority and sequencing across NodeWrights
- [Uninstall](../user-guide/uninstall.md) — explicit uninstall and CR deletion
- [Package Artifact Specification](../user-guide/package-artifact-spec.md) — the package artifact format
- [Deployment Policy](../user-guide/deployment-policy.md) — compartments and batching
