# Migrating from Skyhook to NodeWright

> **STATUS: DRAFT.** This guide describes the in-progress `skyhook.nvidia.com` -> `nodewright.nvidia.com`
> API rename. It is written against the mirror-based upgrade flow. The rename ships in operator v0.18.0
> and the legacy group is removed in **v0.21.0**; treat those two as firm, since the deprecation windows
> below are the deadline users plan against.
>
> **BREAKING CHANGE.** The primary CRD moves group and Kind: `skyhook.nvidia.com/v1alpha1 Skyhook` ->
> `nodewright.nvidia.com/v1alpha1 NodeWright`. `DeploymentPolicy` moves group (same Kind name). The
> on-node annotation/label/finalizer prefix moves `skyhook.nvidia.com/* -> nodewright.nvidia.com/*`, and
> the kubectl plugin is renamed `kubectl skyhook -> kubectl nodewright`.
>
> **Scope of the current release.** This guide is written against the full migration flow, and most of it
> has shipped. Available **now**: the `nodewright.nvidia.com` API group and CRDs, the mirror controller
> that pre-creates a `NodeWright`/`DeploymentPolicy` for each legacy object, new-group admission webhooks,
> legacy deprecation warnings, the primary reconciler reconciling `NodeWright`, the on-node
> annotation/label/condition **copy** to the `nodewright.nvidia.com/*` prefix on upgrade (so packages are
> **not** re-run), the deferred rollback-safe cleanup of the legacy-labeled package pods and per-node
> ConfigMaps (gated by `LEGACY_CLEANUP_DELAY`), the runtime migration hold that waits while any legacy
> `Skyhook` is non-complete, the renamed CLI plugin (binary `kubectl-nodewright`), and the Helm chart shipping both groups'
> CRDs and RBAC. **Not yet available** (planned for v0.21.0, do not rely on it today): the
> chart's pre-upgrade safety hook that refuses to drop the legacy CRD while legacy objects remain. Sections
> below flag this inline.

## TL;DR

1. **Upgrade the operator.** It automatically mirrors every existing `Skyhook` into a `NodeWright` (and
   each legacy `DeploymentPolicy` into a new-group `DeploymentPolicy`). You do **not** touch your CRs for
   this step. On upgrade the operator copies the per-node state (annotations, conditions) to the
   `nodewright.nvidia.com/*` prefix, so the migrated `NodeWright` picks up live node lifecycle position and
   **packages are not re-run**. The old `skyhook.nvidia.com/*` state is kept for a rollback window and
   pruned later (see [Rollback window](#rollback-window-legacy_cleanup_delay)).
2. **Rename your CRs** (in git for GitOps, or by re-applying for Helm/manual): change `apiVersion` and
   `kind`, keep the same `metadata.name`. This is a mechanical group/kind swap with no change to the spec
   body; see the sed one-liner in [the CLI reference](../user-guide/cli.md#migrating-manifests-to-nodewright).
3. **Delete the old `Skyhook`/`DeploymentPolicy` CRs**, but only after confirming the corresponding
   `NodeWright` exists and is being reconciled (see [safe deletion](#safe-deletion-ordering)). Delete the
   legacy `DeploymentPolicy` objects too. The mirrored `NodeWright` objects are the real, reconciled
   resources and are never deleted by this step.

**Recommended runbook: migrate on a quiesced cluster.** In order: **quiesce** (no `Skyhook` mid-rollout:
none `in_progress`/`erroring`/`blocked`/`waiting`; `complete`, `paused`, and `disabled` are fine to leave
as-is) -> **upgrade** the
operator -> **rename** the CRs atomically (steps 2 and 3 as one change: a rename = delete old + add new
together) -> **delete** the legacy CRs promptly. The sharp edges this guide warns about (a wedged migration
hold if a `Skyhook` is not `complete`, and rejected writes once the legacy CRs go read-only) all come from
deviating from that steady state.

## What the operator does automatically

On upgrade, a one-way, level-triggered **mirror controller** runs:

- For each legacy `Skyhook`/`DeploymentPolicy`, it creates the equivalent `nodewright.nvidia.com` object,
  stamped `nodewright.nvidia.com/mirrored-from: <legacy-name>`.
- It is **read-only on your legacy objects** (never writes to them), so GitOps tools see **no drift** on
  the CRs you still manage.
- It **backs off** if a `NodeWright` of that name already exists that it did not create (a user/GitOps
  managed object always wins).
- It **never deletes** a `NodeWright`, even when the legacy object is deleted; once mirrored, the
  `NodeWright` holds live workload state.

Legacy `Skyhook`/`DeploymentPolicy` writes return a deprecation warning pointing here.

**The legacy `Skyhook`/`DeploymentPolicy` is read-only once migrated; operate the `NodeWright`.** The
post-rename operator's admission webhook **rejects** any spec or `pause`/`disable` change to a legacy
object. Deletions, finalizer edits, and identical re-applies are still allowed, so a steady-state GitOps
sync does not break; only a real edit is rejected. Run every spec and lifecycle change (`pause`, `disable`,
`resume`, `enable`) against the new object with `kubectl nodewright …` or by editing the `NodeWright` /
nodewright `DeploymentPolicy` directly. A divergent edit to a legacy CR (including via Argo) surfacing as a
rejection is deliberate: it forces the rename rather than letting the legacy object drift from the object
the operator actually reconciles.

Alongside the mirror, the operator adopts the per-node state under the new prefix. It **copies** the legacy
per-node metadata (`nodeState_`, `status_`, `version_`, `cordon_` annotations, status labels, and node
conditions) to the `nodewright.nvidia.com/*` prefix and adds a `nodewright.nvidia.com` label to the
per-node ConfigMaps, so the migrated `NodeWright` picks up live lifecycle position and does **not** re-run
package stages. It does this **additively**: the legacy `skyhook.nvidia.com/*` copies, the pre-rename
package pods, and the legacy ConfigMap labels are **kept** so a rollback stays possible, and are pruned
later, not on upgrade (see [Rollback window](#rollback-window-legacy_cleanup_delay)).

Checking migration status: a `Skyhook` is migrated when a `NodeWright` of the same name with the
`nodewright.nvidia.com/mirrored-from` stamp exists:

```bash
kubectl get nodewrights.nodewright.nvidia.com -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.annotations.nodewright\.nvidia\.com/mirrored-from}{"\n"}{end}'
```

---

## Argo CD (GitOps)

The key property: because the operator **pre-creates** the `NodeWright`, when your git later declares it,
Argo finds it already present and **adopts** it (applies its tracking label, shows in-sync) rather than
creating a duplicate. This assumes Argo's default `label` resource-tracking method; if you run with
`annotation` or `annotation+label` tracking (`application.resourceTrackingMethod` in `argocd-cm`), the same
adoption still applies but via the tracking annotation. Orphaned-resource monitoring on the `AppProject`
is separate: it only controls warnings about untracked objects and does not affect adoption.

1. **Upgrade the operator** through its own Argo Application (bump the chart/image). The mirror imports
   your existing `Skyhook`s into `NodeWright`s. At this point the `NodeWright`s exist but are not part of
   any Argo Application (untracked) - Argo neither prunes nor flags them, and your `Skyhook` Application
   stays in-sync because the mirror never writes to your `Skyhook`s.

2. **Rename the CRs in git as a single commit.** For each `Skyhook`, change:
   - `apiVersion: skyhook.nvidia.com/v1alpha1` -> `nodewright.nvidia.com/v1alpha1`
   - `kind: Skyhook` -> `kind: NodeWright`
   - keep `metadata.name` identical.

   Same for `DeploymentPolicy` (only `apiVersion` changes; `kind` stays `DeploymentPolicy`). This is a
   mechanical group/kind swap; see the sed one-liner in
   [the CLI reference](../user-guide/cli.md#migrating-manifests-to-nodewright). The commit should **remove** the old
   `Skyhook` manifest and **add** the `NodeWright` manifest so Argo prunes the old and adopts the new in
   one sync.

3. **Sync.** Argo adopts the existing `NodeWright` (in-sync, no re-create) and prunes the `Skyhook`. Once
   the `Skyhook` is gone, the mirror has nothing left to import for it.

**Do not** leave the old `Skyhook` in git after adding the `NodeWright`: in that window both the mirror
and Argo write the `NodeWright`, and editing it via git could be stomped by the mirror re-importing the
stale `Skyhook`. Renaming in one commit avoids this entirely.

If you keep CRDs in a separate Argo Application: during the transition it should track both CRDs. The
operator Helm chart ships both groups' CRDs and RBAC, so an operator upgrade installs the
`nodewright.nvidia.com` CRDs for you. Do not remove the `skyhook.nvidia.com` CRD until every `Skyhook` is
migrated and deleted (removing the CRD cascade-deletes any remaining `Skyhook`s). See
[Removal](#removal-operator-v0210).

---

## Helm (`helm upgrade`)

Helm here refers to installing the **operator** via its chart. Your `Skyhook`/`NodeWright` **instances**
are usually authored separately (not part of the operator chart); adjust to your setup.

1. **`helm upgrade` the operator chart.** The mirror imports your existing `Skyhook`s into `NodeWright`s.
   The chart ships the CRDs and RBAC for **both** groups, so the upgrade installs the
   `nodewright.nvidia.com` CRDs for you. Per-node state is migrated on upgrade (see above), so packages are
   not re-run.

2. **Update your CR manifests to `NodeWright`** wherever you keep them, and apply. This is a mechanical
   group/kind swap with no change to the spec body:
   ```bash
   sed -e '/^ *apiVersion:/ s|skyhook\.nvidia\.com/|nodewright.nvidia.com/|' \
       -e 's|^\( *kind: *\)Skyhook[[:space:]]*$|\1NodeWright|' my-skyhook.yaml > my-nodewright.yaml
   kubectl apply -f my-nodewright.yaml
   ```
   Because the operator already created the `NodeWright` via the mirror, this apply is a no-op adoption of
   the existing object (same name, same spec) rather than a fresh create.

   Rewrite `apiVersion` and `kind` only. A blanket `s|skyhook\.nvidia\.com/|...|g` also rewrites
   `nodeSelectors` and `podNonInterruptLabels` keys, which name **your** node and pod labels rather than
   anything the operator owns. Rewriting them points the CR at a key nothing carries, so it matches nothing,
   and it turns this step into a real spec change instead of the no-op adoption described above. See
   [the CLI reference](../user-guide/cli.md#migrating-manifests-to-nodewright) for what else to rename by hand.

3. **Delete the old CRs** once you have confirmed the `NodeWright` exists and reconciles (see
   [safe deletion](#safe-deletion-ordering)):
   ```bash
   kubectl delete skyhook.skyhook.nvidia.com <name>
   kubectl delete deploymentpolicy.skyhook.nvidia.com <name>   # legacy DeploymentPolicies too
   ```
   The `NodeWright` remains (the mirror never deletes it).

**Planned, not yet in this release:** at the **removal** release the chart is intended to add a pre-upgrade
safety hook (`legacy.blockUpgradeIfPresent`) that **aborts the upgrade** if any legacy
`Skyhook`/`DeploymentPolicy` still exist, so you cannot accidentally drop the old CRD (and cascade-delete
live objects) before migrating. That hook is not implemented yet; until it ships, verify manually that no
legacy objects remain before removing the legacy CRD.

---

## Manual / kubectl

1. Upgrade the operator (apply the new manifests / image, or `helm upgrade` the chart, which ships both
   groups' CRDs and RBAC). The mirror imports your `Skyhook`s and the per-node state is migrated on
   upgrade.
2. Convert your CR manifests by hand (change `apiVersion`/`kind`, keep `metadata.name`) and apply them.
   The mirror has already created the `NodeWright` objects, so this apply is a no-op adoption:
   ```bash
   sed -e '/^ *apiVersion:/ s|skyhook\.nvidia\.com/|nodewright.nvidia.com/|' \
       -e 's|^\( *kind: *\)Skyhook[[:space:]]*$|\1NodeWright|' my-skyhook.yaml > my-nodewright.yaml
   kubectl apply -f my-nodewright.yaml             # adopts the mirrored object
   ```
   As above, rewrite `apiVersion` and `kind` only; leave `nodeSelectors` and `podNonInterruptLabels` keys
   alone.
3. Delete the old CRs only after confirming the `NodeWright` reconciles (see
   [safe deletion](#safe-deletion-ordering)); delete legacy `DeploymentPolicy` objects too:
   ```bash
   kubectl delete skyhooks.skyhook.nvidia.com --all
   kubectl delete deploymentpolicies.skyhook.nvidia.com --all
   ```

---

## Safe deletion ordering

Delete a legacy `Skyhook` (or `DeploymentPolicy`) **only after** you have confirmed its `NodeWright`
counterpart exists and is being reconciled. The mirror never deletes a `NodeWright`, but deleting the
legacy object before the new one is live loses your intent. For each object:

1. Confirm the mirrored `NodeWright` exists and carries the `nodewright.nvidia.com/mirrored-from` stamp
   (see the status check above).
2. Confirm it is reconciling (its `.status` is populated and it is not erroring).
3. Then delete the legacy `Skyhook`/`DeploymentPolicy`.

A legacy `Skyhook` created by the pre-rename operator carries the old `skyhook.nvidia.com/skyhook`
finalizer, so `kubectl delete` briefly parks the object in `Terminating` until the mirror strips that
finalizer (the post-rename operator manages a finalizer only on `NodeWright`). This is automatic and
usually completes in seconds; you do not need to remove the finalizer by hand. It also means deletion
only completes while the operator is running, so delete legacy objects **before** you scale the operator
down for the final removal step.

Delete legacy `DeploymentPolicy` objects as part of the same pass: they move group too, and a stale legacy
`DeploymentPolicy` left behind will keep emitting deprecation warnings and block eventual removal of the
`skyhook.nvidia.com` CRD.

**Ordering within the pass matters:** the legacy `DeploymentPolicy` admission webhook rejects deletion
while any legacy `Skyhook` still references it. Delete the referencing `Skyhook`s **first**, then the
`DeploymentPolicy`. The Helm and Manual command sequences above already delete `skyhooks` before
`deploymentpolicies` for this reason.

---

## Prerequisite: all Skyhooks must be complete

**Perform the operator upgrade only when every `Skyhook` is `complete` with no nodes in progress**, i.e.
no package is mid-rollout. This is a **requirement**, not just a recommendation: the migration adds
`nodewright.nvidia.com/*` labels to the operator's package and per-node ConfigMaps so the post-rename
operator adopts them instead of trying to recreate them, and the flow assumes there is no in-flight
package work to disrupt. The pre-rename package pods are **not** renamed or touched on upgrade; they are
left in place for the rollback window and deleted at prune time. Upgrading against idle Skyhooks avoids
handing a migrated `NodeWright` a stage that was mid-rollout.

Check before upgrading:

```bash
kubectl get skyhooks.skyhook.nvidia.com -o custom-columns=NAME:.metadata.name,STATUS:.status.status,INPROGRESS:.status.nodesInProgress
```

Proceed only when no Skyhook is actively rolling out. **`paused` and `disabled` Skyhooks are fine to leave
as-is** and do **not** need to be enabled or unpaused first: the mirror carries their `paused`/`disabled`
state onto the `NodeWright`, which then does not roll out, so they migrate in that state.

> [!WARNING]
> **Do not unpause or enable a migrated `NodeWright` until its pre-upgrade package pods are gone.**
> Pause and disable on the pre-rename operator both blocked *new* scheduling but neither stopped a pod
> that was already running, so a Skyhook paused or disabled shortly before the upgrade can still have
> one. Unpausing or enabling while that pod runs is **unsupported**: the new operator's in-flight gate
> counts Jobs, not raw pods, so it can start a stage Job alongside the running pod. Both mount the host
> root and share one package directory, and nothing serializes their writes.
>
> Leaving it paused or disabled is safe indefinitely — the new operator creates no Jobs for it.
> Check before unpausing or enabling, and wait for the list to come back empty. Query every
> namespace — a false empty here is the dangerous answer, and the pre-rename operator was not
> necessarily installed in `skyhook`:
>
> ```bash
> kubectl get pods -A -l skyhook.nvidia.com/name=<skyhook-name>
> ```
>
> These pods are not reaped by the new operator; they finish on their own and are cleaned up by the
> legacy sweep once the rollback window (`LEGACY_CLEANUP_DELAY`) elapses.

The runtime hold only waits on Skyhooks whose rollout is genuinely in flight (`in_progress`, `erroring`, `blocked`,
`waiting`, `unknown`). Finish, roll back, or delete those before upgrading (they show up in the `STATUS`
column above). If you have already upgraded and one is wedging the migration, **delete** the offending
legacy `Skyhook` (its status cannot advance without the old operator, and it is read-only) or roll back to
the pre-rename operator; the hold clears once it is gone or complete.

The operator enforces this at runtime as a fail-safe. On startup, while any legacy `Skyhook` is still
actively rolling out (see the statuses above), the operator
**holds**: it does not take over any node, requeues every 20 seconds, and logs a warning naming the
in-flight Skyhooks. This prevents the new operator from double-driving a node the pre-rename operator may
still be mutating. It is a stop, not an auto-resume: because a legacy `Skyhook`'s status is frozen once the
operator stops reconciling that kind, an in-flight hold clears only when those Skyhooks are finished (roll
back to the pre-rename operator, let the rollout complete, then upgrade again) or deleted. A `complete`,
`paused`, or `disabled` Skyhook, and a legacy `Skyhook` created *after* the upgrade (with no status,
mirrored into a `NodeWright`), do not trigger the hold.

## Rollback window (`LEGACY_CLEANUP_DELAY`)

The migration hold above keeps rollback safe while a rollout was *in flight* at upgrade; the rollback
window keeps it safe *after* a Skyhook has finished migrating. When the operator adopts a node it
**copies** the legacy `skyhook.nvidia.com/*` annotations, labels, and conditions to the
`nodewright.nvidia.com/*` prefix but **keeps the legacy copies**, and it leaves the pre-rename package pods
in place. So a rolled-back pre-rename operator still finds its state and resumes cleanly, and a bad upgrade
stays recoverable even after migration completes.

The legacy copies (and the old package pods, and the legacy ConfigMap labels) are pruned only once
`LEGACY_CLEANUP_DELAY` has elapsed since the Skyhook was adopted, tracked by a
`nodewright.nvidia.com/legacy-migrated-at` timestamp on the `NodeWright`. The delay defaults to **24h**
(`controllerManager.manager.env.legacyCleanupDelay` in the chart). Set it to `0` to prune immediately (no
rollback window), or longer to keep the door open longer. Pruning is the **point of no return**: after it
runs, rolling back to the pre-rename operator would re-apply every package from scratch.

**Your own `skyhook.nvidia.com/*` node keys are not touched.** The metadata prefix is the product's domain
name, so you may well have your own labels or annotations under it (a `skyhook.nvidia.com/pool` feeding a
`nodeSelectors`, say). The migration only copies and later prunes the keys the operator itself writes:
the `nodeState_`, `status_`, `version_`, `cordon_`, `drainStart_`, and `autoTaint_` annotations, the
`status_` label, the operator-defined `ignore` label, and the `<name>/NotReady` and `<name>/Erroring`
conditions. Anything else under the prefix is yours and is left exactly as it is, on both halves.

The runtime-required **taint** key is not part of this copy-and-prune flow, and taints already on nodes
are never rewritten. Its default did move in the rename, from
`skyhook.nvidia.com=runtime-required:NoSchedule` to `nodewright.nvidia.com=runtime-required:NoSchedule`.
The operator applies only the configured key, but tolerates and removes **both** for the deprecation
window, so nodes stamped with the legacy key by an autoscaler or node template are not stranded
unschedulable. Update your provisioning config before operator v0.21.0; see
[runtime_required.md](../user-guide/runtime-required.md#taint-key-rename-skyhooknvidiacom---nodewrightnvidiacom).

This is transition-only behavior and is removed together with the `skyhook.nvidia.com` group in
operator v0.21.0.

## Install namespace (`skyhook` -> `nodewright`)

The documented install namespace for **new** deployments is now `nodewright`. The kustomize
overlay moved from `skyhook-operator-system` to `nodewright-operator-system` to match, and the
operator's own `NAMESPACE` default (used only when nothing sets it, such as a bare binary or
`make run`) moved from `skyhook` to `nodewright`.

**There is nothing to migrate, and no deadline.** Kubernetes namespaces cannot be renamed in
place, and Helm cannot move a release between namespaces, so moving an existing install would
mean deleting and recreating every namespaced object the operator owns: a real outage in
exchange for a cosmetic name. **Staying in `skyhook` remains fully supported and correct.** The
chart takes the namespace from `.Release.Namespace` throughout and installs cleanly into any
namespace, so `helm upgrade` against an existing `skyhook`-namespace release is unaffected by
this change.

What this does change:

- **New installs.** The README and chart docs now use `--namespace nodewright --create-namespace`.
  Substitute your own namespace freely; nothing depends on the name.
- **`kubectl nodewright`.** With no `--namespace`, the CLI discovers the operator's namespace
  rather than assuming one, so it keeps working against a `skyhook`-namespace install. It checks
  `nodewright`, then `skyhook` (printing a one-line note), then sweeps cluster-wide. See
  [the CLI reference](../user-guide/cli.md#namespace-resolution).

If you do want to move an existing install, treat it as an uninstall and reinstall, not a
migration:

1. Confirm every NodeWright is `complete` with no nodes in progress.
2. `helm uninstall <release> -n skyhook`. The chart's pre-delete hook removes the NodeWright and
   DeploymentPolicy CRs; on-node package state (the `nodewright.nvidia.com/*` node annotations)
   is **not** removed by this, so it survives.
3. `helm install <release> oci://ghcr.io/nvidia/nodewright/charts/nodewright -n nodewright --create-namespace`.
4. Re-apply your NodeWright CRs. Because the per-node state annotations survived, packages are
   not re-run.

There is no reason to do this on a running cluster unless you have an external requirement on
the namespace name.

## Metrics

Every operator metric moved from the `skyhook_` prefix to `nodewright_`, and the shared series label moved
from `skyhook_name` to `nodewright_name`. **No action is required at upgrade time.** Both sets are
published side by side with identical values and identical remaining labels, so existing dashboards,
alerting rules, and recording rules keep working across the window.

Migrating a query is a two-token swap:

```promql
# before
skyhook_node_status_count{skyhook_name="my-nodewright", status="complete"}

# after
nodewright_node_status_count{nodewright_name="my-nodewright", status="complete"}
```

The legacy series are removed in **v0.21.0** along with the legacy API group, so update dashboards and
alerts before then. `skyhook_*` help text in `/metrics` names its replacement and the removal release, so
`curl`ing the operator's metrics endpoint tells you what each one becomes.

Dual-publishing roughly doubles the exported series count. Once your dashboards and alerts are migrated,
set `PUBLISH_LEGACY_METRICS=false` (chart: `controllerManager.manager.env.publishLegacyMetrics`) to drop
the deprecated half early rather than waiting for v0.21.0. The full metric reference and the opt-out are in
[docs/observability/metrics.md](../observability/metrics.md).

## Downstream consumers (e.g. aicr)

Projects that ship or depend on Skyhook CRs need a coordinated update. For example
[aicr](https://github.com/NVIDIA/aicr) (Argo-based) references Skyhook in several places; the equivalent
changes for any such consumer are:

- **CRs you deploy** (e.g. aicr's `recipe.yaml` `Skyhook`): rename `apiVersion` + `kind` to
  `nodewright.nvidia.com/v1alpha1` / `NodeWright` (keep the name). Because the operator pre-creates the
  `NodeWright`, Argo adopts it on next sync.
- **CRD readiness waits / assertions** (e.g. chainsaw `assert-crds.yaml`, waits on
  `skyhooks.skyhook.nvidia.com` Established): point at `nodewrights.nodewright.nvidia.com`.
- **Cleanup tooling** referencing the `skyhook.nvidia.com` group: add/switch to `nodewright.nvidia.com`.

These land as a **companion PR in the consumer repo**, timed with (or shortly after) the operator upgrade.

## Verification checklist

- `kubectl get nodewrights.nodewright.nvidia.com` lists a `NodeWright` for each former `Skyhook`.
- Node state carried over: a node's annotations are copied under `nodewright.nvidia.com/` (the legacy
  `skyhook.nvidia.com/` keys remain during the rollback window) and no package re-ran.
- No drift reported by Argo on your (soon-to-be-deleted) `Skyhook` objects during the transition.
- After you delete the old CRs, the `NodeWright` objects remain and reconcile normally.
- Legacy `Skyhook`/`DeploymentPolicy` writes emit the deprecation warning.

## Removal (operator v0.21.0)

The legacy `skyhook.nvidia.com` group is kept for a three-minor-release migration window and is removed in
**operator v0.21.0**. The rename ships in v0.18.0, so the window spans v0.18.x, v0.19.x and v0.20.x.

This is the single date every transition-only behavior keys off: the legacy API group, the per-node
metadata prune, the legacy runtime-required taint key (see
[runtime_required.md](../user-guide/runtime-required.md#deprecation-window)), and the legacy `skyhook_*` metric names
(see [Metrics](#metrics) below) all end together in v0.21.0. In the
removal release:

- The legacy admission webhook flips from warning to **denying** writes.
- The legacy `skyhook_*` metric series stop being published.
- The legacy CRDs are removed from the chart/manifests, sequenced behind a preflight that refuses removal
  while any legacy objects remain (the Helm/Argo pre-upgrade hook, planned above).
- Removing the `skyhook.nvidia.com` CRD cascade-deletes any remaining `Skyhook`s, so **migrate before
  operator v0.21.0**.

## FAQ

**Will my nodes reboot during the upgrade?** No (unless a package interrupt was mid-flight, and even then
the agent skips an already-completed interrupt). Per-node state is migrated and legacy workloads are
cleaned up, so packages are not re-run; still upgrade against idle, `complete` Skyhooks as required by the
prerequisite above.

**Does the operator write to my `Skyhook` objects?** No. The mirror is read-only on the source, so GitOps
tools see no drift. "Migrated" is derived from the existence of the stamped `NodeWright`.

**What if I create a new `Skyhook` after upgrading?** It still works (the CRD is served during the
transition) and gets mirrored, but you will get a deprecation warning. Prefer creating `NodeWright`
directly.

**Can I roll the operator back?** Yes, within the rollback window. The operator keeps the legacy node
state, package pods, and `Skyhook` CRs during the transition, so rolling back to a version that reconciles
`Skyhook` resumes cleanly. Two things close the door: the per-node prune after `LEGACY_CLEANUP_DELAY`
(default 24h; see [Rollback window](#rollback-window-legacy_cleanup_delay)), after which a rolled-back
operator sees fresh nodes and re-applies packages; and deleting the `Skyhook`s, which strands the
`NodeWright`s. Keep the `Skyhook`s (and raise `LEGACY_CLEANUP_DELAY`) until you are confident.

Pass the target revision explicitly: `helm rollback <release> <revision> -n <namespace> --wait`. With no
revision, `helm rollback` targets the previous *deployed* revision, which after a failed attempt is the
release you were trying to leave rather than your baseline, so it is easy to run twice and conclude the
rollback worked.

The rollback also has to hand the operator's webhook plumbing back across the resource-name rename, since
the two releases name the webhook Service and configurations differently. That is automatic: the pod being
rolled away notices that the webhook configurations now dial a Service it does not serve, releases the
webhook bootstrap lease so the pod being rolled back to can take over, and the serving certificate it
minted already carries a SAN for the pre-rename Service name. Expect the rollout to take up to about
ninety seconds longer than a normal one while that handover happens.

**Rolling back from an operator that predates that handover** wedges with no Ready operator pod
([#469](https://github.com/NVIDIA/nodewright/issues/469)). Clear it by hand:

```bash
kubectl -n <namespace> delete deploy <fullname>-controller-manager
kubectl -n <namespace> delete secret webhook-cert
helm rollback <release> <revision> -n <namespace> --wait
```
