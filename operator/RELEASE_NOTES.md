# Release Notes

Human-authored highlights, behavior changes, and upgrade steps for the operator.
For the full commit-level log see CHANGELOG.md.

## Unreleased

## operator/v0.20.0 - 2026-10-06

### Breaking Changes

- **The operator refuses to start if the runtime-required taint key has a DNS prefix
  or is longer than 53 characters.** The key names the
  `nodewright.nvidia.com/autoTaint_<key>` marker, and such a key makes that an
  invalid annotation name. The config only appeared to work until a NodeWright
  enabled `autoTaintNewNodes`; then every auto-taint write was rejected and
  reconciliation stopped for all NodeWrights. **Before upgrading**, check
  `controllerManager.manager.env.runtimeRequiredTaint`: if its key contains a `/`
  (e.g. `example.com/runtime-required`) or exceeds 53 characters, switch to an
  unprefixed key such as the default `nodewright.nvidia.com`, and update the taint
  your provisioner stamps on new nodes to match.

### Behavior Changes

- **The operator no longer uncordons a node that something else cordoned.** If a node is already unschedulable when a NodeWright reaches its interrupt and no NodeWright holds a cordon on it, the NodeWright drains and interrupts under that cordon and leaves the node cordoned when it completes; previously it adopted the cordon and released it, which could hand a node back to workloads while a health checker such as NVSentinel still had it quarantined. **You will notice this if you cordon nodes yourself before a NodeWright runs on them:** `kubectl uncordon` them when you are done, as the operator will not. A cordon placed after the NodeWright's own is still released by the NodeWright. A reset with `kubectl nodewright` v0.4.0 or earlier strips the annotation the operator needs to release its own cordon, so uncordon nodes by hand after such a reset; newer CLIs keep it. **After upgrading, check for nodes this leaves cordoned.** A node that is already unschedulable with no `cordon_*` annotation, for example after a reset with an older CLI or a `REAPPLY_ON_REBOOT` reset whose reapply had no interrupt package, used to be released by the next interrupt run and now stays cordoned. List them with `kubectl get nodes -o json | jq -r '.items[] | select(.spec.unschedulable == true) | select([(.metadata.annotations // {}) | keys[] | select(startswith("nodewright.nvidia.com/cordon_") or . == "nodewright.nvidia.com/runtimeRequiredCordon")] | length == 0) | .metadata.name'` and uncordon any that nothing else is holding. See [interrupt-flow.md](../docs/architecture/interrupt-flow.md#cordons-nodewright-did-not-create).

- **The CycloneDX SBOM moved from the multi-platform index digest to each platform manifest digest, and an OpenVEX document is now published alongside it.** An SBOM describes exactly one root filesystem, so one attached to a multi-platform index described neither child truthfully, and a consumer who resolved `linux/amd64` and enumerated referrers on that manifest found nothing.

  **You will notice this if you verify release artifacts.** `cosign verify-attestation --type cyclonedx <index-digest>` was the documented command and now finds nothing on this release. Resolve the platform manifest and verify against that instead:

  ```bash
  INDEX=$(crane digest "$REPO:$TAG")
  PLATFORM_DIGEST=$(crane digest --platform linux/amd64 "$REPO@$INDEX")
  cosign verify-attestation --type cyclonedx ... "$REPO@$PLATFORM_DIGEST"
  cosign verify-attestation --type openvex   ... "$REPO@$PLATFORM_DIGEST"
  ```

  The signature and the SLSA provenance have not moved: both still verify against the index digest, which is what a tag resolves to and what you pull. Tags published before this release keep the old layout, with the SBOM on the index and no OpenVEX document. `SECURITY.md` carries the full recipe.

- **A node is marked `erroring` only when a stage fails, not on a failed attempt that its Job
  retries.** A single failed attempt used to mark the node `erroring`, which ended its batch as a
  failure and could stop the rollout even when the retry then succeeded; a reboot interrupt,
  which its own shutdown kills, always hit this. A failing attempt now shows on the package's
  state while the node reads `in_progress`, and the node reads `erroring` once the stage's Job
  runs out of retries or times out. Nodes already stuck need a one-time recovery: see "Known
  Issues" in `docs/user-guide/deployment-policy.md`.

- **A PodDisruptionBudget eviction rejection is now a wait state, not a reconcile
  error, and the refused eviction is retried once per node every 30 seconds.** An
  eviction refused with a 429 carrying a `DisruptionBudget` cause is recorded as a
  drain blocker, and the pass carries on with the remaining nodes. Any other eviction
  failure, including a 429 without that cause, is still an error. In v0.19.0 the
  refusal was a reconcile error, so retries followed controller-runtime's exponential
  backoff, growing toward roughly 1000 seconds apart. Now the reconcile keeps its
  2-second cadence, and only the refused node's eviction waits 30 seconds between
  attempts. A budget that regains headroom is therefore picked up within 30 seconds
  rather than after the backoff, and `spec.drainConfig.timeout` fires on time. A
  `spec.drainConfig` change made mid-drain (`disableEviction`, `force`,
  `deleteEmptyDirData`) takes effect at the node's next attempt, up to 30 seconds
  later. A throttled pass attempts the eviction instead of skipping when none of the
  blockers recorded for its NodeWright on the node is still blocking, either because
  none were recorded or because the refused pods have left the node or begun
  terminating. The retry timer is held in
  memory, so an operator restart retries at once.

- **`spec.drainConfig.timeout` now fires for drains blocked by a PodDisruptionBudget.**
  The PDB rejection used to return an error before node state was saved, so the
  drain start time was never persisted and the timeout could never elapse: a
  PDB-blocked node retried forever. The timeout now applies as it already did to
  other blockers. When it elapses the operator records a `DrainTimeout` Warning
  event, marks the node and NodeWright `erroring`, and leaves the node cordoned
  without evicting further.

  **Review this before upgrading.** If you set `spec.drainConfig.timeout` and drain
  workloads whose PDB routinely allows no disruptions, nodes that used to retry
  forever will start erroring with `DrainTimeout`. Check the PDBs on drained
  workloads and the configured timeout first. Recovery is described in
  [docs/architecture/interrupt-flow.md](../docs/architecture/interrupt-flow.md#recovering-from-a-drain-timeout).

- **A new `DrainBlocked` condition on the NodeWright names what is holding a drain.**
  It is set while any selected node has a drain that cannot make progress. Its reason
  is `PodDisruptionBudget`, `UnmanagedPod`, `EmptyDirData`, or `MultipleCauses` when
  more than one kind of blocker is present, and its message lists the blocked nodes
  and blocking pods, quoting the apiserver's PDB message verbatim. Each blocked node
  also carries a `nodewright.nvidia.com/drainBlocked_<name>` annotation recording its
  blocking pods for that NodeWright. Both cover the drain before an uninstall as well
  as before an apply, and both clear once the blocker is gone. Unmanaged and
  `emptyDir` pods were already wait states; they are now visible without reading
  operator logs. See
  [docs/architecture/interrupt-flow.md](../docs/architecture/interrupt-flow.md#drainblocked-condition).

- **A failing interrupt other than a reboot now marks its node `erroring` after `jobBackoffLimit` restarts instead of at its stage deadline.** Interrupt Jobs restart the interrupt in place with no retry limit, because a reboot interrupt's own shutdown kills its container and the restart after the node returns completes the stage. The operator now counts the container restarts of every other interrupt type (`service`, `restartAllServices`, `noop`): when one fails again with its restart count at `jobBackoffLimit`, the same budget a package stage gets, the operator fails the interrupt Job, the node reads `erroring`, and its DeploymentPolicy batch is evaluated and counted toward `failureThreshold`. The count is the kubelet's restart count, so restarts that were not failures, such as an unplanned node reboot, spend the budget too. With the default `jobBackoffLimit` of 3 that is four failed runs and about 30 seconds of kubelet restart backoff, where it used to take the full `jobStageTimeout` (1h by default) and never happened at all for an interrupt Job with no deadline. The ended Job carries the `nodewright.nvidia.com/restart-limit-exceeded` annotation, and a `Warning` event with reason `Interrupt` and action `RestartLimitExceeded` is emitted on the Node and the NodeWright. The budget is fixed when the interrupt's Job is created, so interrupt Jobs already running when you upgrade keep the old behavior. **Reboot interrupts are unchanged**: a reboot's shutdown can kill the agent before it takes effect, so its restarts are never counted, and a failing reboot still holds its node cordoned, drained and `in_progress` until its stage deadline (`stageTimeout`, or `jobStageTimeout`), indefinitely if it has none; its package's State reads `erroring` meanwhile, but the node's Status does not. Give every package that declares a reboot interrupt a finite `stageTimeout` that covers a reboot. See "A failing interrupt" in `docs/user-guide/custom-resource.md`.

### Bug Fixes

- **`spec.serial: true` now applies one package per node per reconcile pass, as
  documented, instead of one package per pass across the whole NodeWright.**
  Previously the pass stopped after the first selected node and returned without
  saving, so only that node advanced until it finished, serial rollouts
  effectively ran one node at a time, and the node changes the pass had made in
  memory, such as a package's `in_progress` state, a cordon or a drain start,
  were discarded. Nodes in the same batch now progress together, and every pass
  saves its node changes. To control how many nodes a batch admits, use
  `interruptionBudget.count` or a `deploymentPolicy` strategy, not `serial`.

- **A `Blocked` status condition (reason `NonInterruptPodsRunning`) and a Warning event are
  now surfaced when `spec.podNonInterruptLabels` blocks node drain.** Previously,
  the operator held the node in `Ready=False` / `Progressing` with no condition or
  event indicating why or which pods were causing the hold. When nodes with packages
  requiring interrupt are held at this barrier:
  - A `Blocked` condition (status `True`, reason `NonInterruptPodsRunning`) is set on the
    NodeWright once per reconcile pass. Its message identifies the blocked nodes using standard
    node list formatting: up to 10 nodes (`wrapper.ReadyConditionNodeListLimit`) are listed by
    name (e.g. `1 node blocked by non-interrupt pods (node-a). Waiting.` or `2 nodes blocked by non-interrupt pods (node-a, node-b). Waiting.`).
    If more than 10 nodes are blocked, the full list is logged at info level once per pass and the
    condition message is summarized with `(list truncated; see controller logs)`.
  - A Warning event with reason `Drain` (`EventsReasonSkyhookDrain`) is emitted on the NodeWright
    object on transition from unblocked to blocked, reporting the held nodes.
  - A supplementary Warning event is also emitted on each affected Node object detailing the
    namespace-qualified blocking pods and package being held.
  - If another `Blocked` reason is already active (such as `DependencyUninstalled`), the
    `NonInterruptPodsRunning` condition and NodeWright Warning event are deferred and will only appear
    once that other condition clears. Once all matching non-interrupt pods finish or
    terminate, the `NonInterruptPodsRunning` condition is removed and drain proceeds,
    preserving any unrelated `Blocked` condition that may also be active.
- **Adding and removing the finalizer from a natively authored NodeWright no
  longer rewrites its spec.** Both paths now use optimistic, metadata-only merge
  patches, preserving concurrent finalizer changes and user-authored resource
  quantity strings such as `4000m` and `8192Mi`. The former full-object update
  also wrote the package map key back into an omitted `spec.packages.*.name`
  field and unintentionally bumped `metadata.generation`; raw and unstructured
  clients will now continue to see that field omitted. Automation should
  compare `observedGeneration` to the object's current `metadata.generation`
  instead of relying on historical fixed values, which may now be lower. This
  does not change legacy `skyhook.nvidia.com` mirror synchronization, which
  still writes its converted target spec.

## operator/v0.19.0 - 2026-08-31

### Bug Fixes

- **Drain now completes when the evicted pods have terminated, not when their
  evictions were accepted.** A pod carrying a `deletionTimestamp` was classified as
  ignorable, so one pass evicted and the next — two seconds later — saw everything
  terminating and reported the node drained. A workload with a 30s
  `terminationGracePeriodSeconds` was roughly 2s into shutdown when the interrupt
  fired. Terminating pods now block drain, matching `kubectl drain`. The exclusions
  (DaemonSet, `kube-system`, mirror/static, unschedulable-tolerating, and the
  operator's own package pods) still apply while a pod terminates, so drain only ever
  waits on pods it selected for eviction or deletion.

  **Expect interrupts to start later than they used to** — by roughly the longest
  `terminationGracePeriodSeconds` among the pods on the node. `spec.drainConfig.timeout`
  now measures time-to-drain rather than time-to-accept-evictions, and it still has no
  default: a pod that can never finish terminating (stuck finalizer, unresponsive
  kubelet) holds the node in `in_progress` until it is cleared. Set a `timeout` if you
  need that wait bounded.

### New Features

- **New `spec.runtimeRequiredCordonAfter`** applies a persistent cordon at the
  same time the runtime-required taint is removed, for any NodeWright that sets
  both `runtimeRequired: true` and `runtimeRequiredCordonAfter: true`. The node
  is marked with a `runtimeRequiredCordon` annotation so the cordon survives
  later NodeWright interrupts. Releasing it requires removing the annotation
  and setting `unschedulable` to false in the same patch. If the node is
  uncordoned externally without removing the annotation, the operator removes
  it automatically. `kubectl nodewright node status` reports the cordon via new
  `CORDONED` and `RUNTIME-REQUIRED-CORDON` columns. See
  [docs/user-guide/runtime-required.md](../docs/user-guide/runtime-required.md#what-happens-when-the-taint-is-removed).

## operator/v0.18.0 - 2026-08-17

### Other Changes

- **The operator finds its webhook configurations by label, not by name.** It now
  selects `Validating`/`MutatingWebhookConfiguration` objects carrying
  `nodewright.nvidia.com/webhook-config` whose `clientConfig` dials its own webhook
  Service, and injects the caBundle into every webhook of every match. Scoping on the
  Service rather than just the label matters because the configurations are
  cluster-scoped: the caBundle only signs that one Service's certificate, so writing it
  into a configuration belonging to another install would break that install's admission. Previously the two names
  were compiled-in constants, which made renaming them in the chart a hard error on
  the running operator: it stayed un-Ready, kept the webhook bootstrap lease, and
  wedged the rolling update (see
  [docs/designs/webhook-bootstrap-lease.md](../docs/designs/webhook-bootstrap-lease.md)).
  The chart applies the label; a chart that does not is unsupported. The
  now-vestigial `webhookValidatingWebhookConfiguration` /
  `webhookMutatingWebhookConfiguration` builders were removed with the constants,
  since the chart has owned creation of these objects for several releases and the
  operator only ever patched their caBundle.

- **`WEBHOOK_SERVICE_NAME` and `WEBHOOK_SECRET_NAME` are now set by the chart.**
  Both env vars already existed but nothing passed them, so the operator always fell
  back to its built-in defaults and silently ignored `webhook.serviceName` /
  `webhook.secretName` in `values.yaml`.

- **The webhook serving certificate is reminted when the webhook Service is renamed.**
  `Secret/webhook-cert` is operator-owned, so it survives a chart upgrade. The
  operator reminted only on expiry or a cert-on-disk mismatch, so a renamed Service
  left a year-valid certificate carrying the old SAN and admission failed closed with
  `x509: certificate is valid for <old>..., not <new>`. It now also remints when the
  Service recorded on the Secret differs from the configured `WEBHOOK_SERVICE_NAME`.

- **The documented install namespace for new deployments is now `nodewright`, not
  `skyhook`.** The kustomize overlay moved from `skyhook-operator-system` to
  `nodewright-operator-system`, and the operator's `NAMESPACE` env default (used only
  when nothing sets it, such as a bare binary or `make run`) moved from `skyhook` to
  `nodewright`.

  **Existing installs need no action, and there is no deadline.** Namespaces cannot be
  renamed in place and Helm cannot move a release between namespaces, so an install in
  `skyhook` stays supported indefinitely. The chart sources the namespace from
  `.Release.Namespace` throughout and installs into any namespace, so `helm upgrade`
  against a `skyhook`-namespace release is unaffected. The chart always sets `NAMESPACE`
  explicitly, so the default change is invisible to chart users.

  `kubectl nodewright` discovers the operator's namespace rather than assuming one, so
  it keeps working against a `skyhook`-namespace install. See
  [docs/getting-started/migration.md](../docs/getting-started/migration.md#install-namespace-skyhook---nodewright).

### Deprecations

- **The `skyhook_*` metrics are deprecated in favour of `nodewright_*`, and the
  `skyhook_name` series label in favour of `nodewright_name`.** Both sets are now
  published side by side.

  **No action is required at upgrade time, and nothing breaks.** Metric names and
  label keys are the identifiers users bake into Grafana dashboards, Prometheus
  alerting rules, and recording rules, so the rename is a dual-publish rather than
  an in-place swap: every `skyhook_*` series continues to be exported with the same
  value and the same remaining labels as its `nodewright_*` twin.

  Migrating a query is a two-token swap, for example
  `skyhook_node_status_count{skyhook_name="x"}` becomes
  `nodewright_node_status_count{nodewright_name="x"}`. The `skyhook_*` help text in
  `/metrics` names its replacement and the removal release, so the exposition itself
  documents the mapping.

  **The legacy set is removed in operator v0.20.0**, the same release that removes
  the legacy `skyhook.nvidia.com` API group, so there is one deadline rather than
  two. Update dashboards and alerts before then. See
  [docs/observability/metrics.md](../docs/observability/metrics.md) and the
  [migration guide](../docs/getting-started/migration.md#metrics).

  Note this roughly doubles the operator's exported series count for the duration of
  the window; see [docs/operations/resources-at-scale.md](../docs/operations/resources-at-scale.md).
  If you have already migrated your dashboards, or never consumed the legacy names,
  set `PUBLISH_LEGACY_METRICS=false` (chart:
  `controllerManager.manager.env.publishLegacyMetrics`) to drop the deprecated half
  immediately. It defaults to `true`, so upgrading without setting it keeps the
  compatibility window.

### Breaking Changes

- **The default runtime-required taint key moves from `skyhook.nvidia.com` to
  `nodewright.nvidia.com`.** `RUNTIME_REQUIRED_TAINT` now defaults to
  `nodewright.nvidia.com=runtime-required:NoSchedule`.

  **This is a coordinated change, not a silent default bump.** The taint key is a
  contract with infrastructure the operator cannot see: cluster autoscaler and
  Karpenter node pools, machine/node templates, `--register-with-taints` kubelet
  arguments, and tolerations on your own workloads all name it. A node that comes
  up carrying a key the operator does not recognise is never untainted, and sits
  unschedulable — a cluster-down failure mode for anyone using
  `autoTaintNewNodes`, not a cosmetic break.

  For the deprecation window the operator therefore recognises **both** keys. It
  **applies** only the configured taint, but **tolerates** and **removes** the
  legacy `skyhook.nvidia.com=runtime-required:NoSchedule` taint as well, and
  treats a node already carrying either key as gated so `autoTaintNewNodes` does
  not stamp a second taint. Existing taints on existing nodes are not rewritten.
  A cluster whose provisioner still applies the legacy key keeps working with no
  change on your side.

  **What you must do:** update the taint key in your autoscaler / node-pool /
  machine-template configuration, and in any workload tolerations that name it,
  before operator v0.20.0, after which the legacy key is neither tolerated nor
  removed. To stay on the old key in the meantime, set it explicitly via
  `controllerManager.manager.env.runtimeRequiredTaint`. See
  [docs/user-guide/runtime-required.md](../docs/user-guide/runtime-required.md#taint-key-rename-skyhooknvidiacom---nodewrightnvidiacom).

- **The primary CRD is renamed from `Skyhook` (`skyhook.nvidia.com/v1alpha1`) to
  `NodeWright` (`nodewright.nvidia.com/v1alpha1`), and `DeploymentPolicy` moves to
  the same `nodewright.nvidia.com` group.** This is the operator side of the
  Skyhook to NodeWright rename.
  - **Migration is automatic for the common case.** The transition release serves
    both API groups. A mirror controller imports every existing `skyhook.nvidia.com`
    `Skyhook`/`DeploymentPolicy` into its `nodewright.nvidia.com` equivalent and
    reconciles the NodeWright as the source of truth. Per-node package state is
    **copied** to the new prefix (`skyhook.nvidia.com/*` node annotations, labels,
    and conditions gain `nodewright.nvidia.com/*` equivalents) while the legacy
    keys are **kept** for a rollback window, so **packages are not re-run** and
    in-progress rollouts keep their position. The legacy copies are pruned only
    after `LEGACY_CLEANUP_DELAY` (default 24h) has elapsed.
  - **Legacy `Skyhook`/`DeploymentPolicy` objects become read-only once migrated:**
    the admission webhook rejects spec and `pause`/`disable` edits (deletions and
    identical re-applies are still allowed) and emits a deprecation warning.
    Operate the mirrored `NodeWright` instead; to move your manifests forward,
    change `apiVersion` to `nodewright.nvidia.com/v1alpha1` (and, for `Skyhook`,
    `kind` to `NodeWright`) and re-apply.
  - **Upgrade during a quiet window:** perform the upgrade only when every Skyhook
    is `complete` with no nodes in progress. In-flight stages resume idempotently
    (no double reboot), but upgrading idle avoids even the benign package-pod
    restart.
  - The legacy `skyhook.nvidia.com` group remains available for a two-minor-release
    migration window spanning v0.18.x and v0.19.x, and is removed in operator v0.20.0.
  - **See [docs/getting-started/migration.md](../docs/getting-started/migration.md)** for the
    full migration guide, the pre-upgrade check, and a verification checklist.

### Bug Fixes

- **The migration now touches only the keys the operator owns, not the whole
  `skyhook.nvidia.com/` prefix.** The metadata prefix is the product's domain name,
  so users legitimately carry their own node labels and annotations under it. The
  migration previously copied *every* prefixed key to `nodewright.nvidia.com/*` and
  then deleted every prefixed key at prune time, which silently duplicated user data
  into the operator's namespace and destroyed the originals 24h after an upgrade. It
  now migrates only the operator's own keys (`nodeState_`, `status_`, `version_`,
  `cordon_`, `drainStart_`, `autoTaint_` annotations; the `status_` and
  operator-defined `ignore` labels; the `<name>/NotReady` and `<name>/Erroring`
  conditions) and leaves everything else under the prefix untouched.

- **Deleting a migrated legacy `Skyhook` no longer cascade-deletes the
  `NodeWright`'s ConfigMaps.** The migration relabelled the pre-rename ConfigMaps but
  left their `ownerReferences` pointing at the legacy `Skyhook`, so the documented
  "delete the old CRs" step garbage-collected the package and per-node metadata
  ConfigMaps out from under the live `NodeWright`. The operator recreated the package
  ConfigMap, but a package pod scheduled in that window would reference a missing
  one. The converge now re-parents these ConfigMaps onto the `NodeWright`.

- **Upgrading from a pre-rename operator no longer wedges on the package
  ConfigMap.** The migration relabelled only the per-node metadata ConfigMaps
  (`skyhook.nvidia.com/skyhook-node-meta`) and missed the package ConfigMaps,
  which carry `skyhook.nvidia.com/name`. Because the post-rename reconciler lists
  package ConfigMaps by the new `nodewright.nvidia.com/name` label, it could not
  see the existing one, tried to create it, and failed permanently with
  `configmaps "<name>-<package>-<version>" already exists` - leaving the
  `NodeWright` stuck short of `complete` behind an exponential backoff. Both
  legacy label keys now converge (and both are dropped at prune time).

- **Package `image` must be a bare registry/repository reference; inline tags
  and digests are now rejected.** `image` carries neither a tag nor a digest:
  `version` supplies the tag (and is the operator's ordering key for
  upgrade/downgrade detection) and `containerSHA` pins the exact bytes the
  kubelet pulls.
  - An inline digest (`image: repo@sha256:...`) was previously accepted but
    never deployable: with `containerSHA` unset the operator appended the
    version tag to an image that already had a digest, producing an unpullable
    reference like `repo@sha256:...:1.2.2`. It is now rejected with a message
    pointing at `containerSHA`.
  - **Behavior change:** an inline *tag* (`image: repo:1.2.2`) was previously
    absorbed silently as a leftover migration off the pre-semver scheme. That
    migration is complete and has been removed, so an inline tag is now rejected
    the same way a digest is, even when it matches `version`. If your manifests
    still embed a tag in `image`, drop it (the package `version` becomes the
    tag). Existing stored CRs are unaffected: their tags were already stripped
    on write by the old migration, so only re-applying a manifest that still
    carries an inline tag will be rejected.
  - `image` is also validated by the CRD schema (apiserver-enforced) to be
    non-empty with no whitespace, and `containerSHA`, when set, must be a
    well-formed digest (`sha256:` followed by 64 lowercase hex characters).

### New Features

- **Package stages now execute as `batch/v1` Jobs**, one per (NodeWright,
  package, stage, node), instead of operator-managed raw pods. The lifecycle
  state machine is unchanged — stages, Status/State derivation, interrupt /
  cordon / drain sequencing and DeploymentPolicy all behave as before — but the
  executor is now a Job, so `kubectl logs job/<name>` works during and after a
  run and finished work is retained instead of deleted on completion.
- **Failure logs survive the failure.** Each genuinely failing stage keeps up to
  two full-log archive pods (its first failure and its most recent), including an
  attempt killed by its own `stageTimeout` — the deadline fails the pod in place
  rather than deleting it, so its logs are read like any other failed attempt's.
  An interrupt Job, which keeps a whole-stage deadline, is the case where the pod
  *is* deleted; there the operator falls back to a best-effort ~16KiB log tail in
  the Job's `nodewright.nvidia.com/last-logs` annotation. Three limits are worth
  knowing: an attempt with no container logs (an unpullable image, say) archives
  nothing to read; an attempt the kubelet refused to admit is not a genuine
  failure and is not archived at all; and the `last-logs` annotation is written
  only while a Job passes through `FailureTarget`, so it can be absent when that
  condition is never emitted.
- **New per-package `stageTimeout`** bounds the wall-clock runtime of one attempt
  at each of a package's stages. A hung stage (stuck script, unpullable image,
  dead registry) is killed and retried instead of sitting `in_progress`
  indefinitely. Unset uses the operator default; `"0"` removes the time bound.
- **`pause` is now a true stop.** The pause annotation cascades to `spec.suspend`
  on the NodeWright's unfinished Jobs, so the stage that is *currently executing*
  is stopped rather than being allowed to finish. Resuming re-runs that stage
  from the start of its current phase; the agent skips already-completed steps.
  `disable` is unchanged and still does not stop in-flight work.
- Add `spec.drainConfig` so interrupt drains can tune eviction, direct deletion,
  emptyDir handling, unmanaged-pod handling, DaemonSet skipping, timeout, and
  grace-period behavior.

### Changed

- **A failing stage now gives up after a bounded number of retries.** Package
  stage Jobs run with `backoffLimit` = `JOB_BACKOFF_LIMIT` (default 3), so a
  stage runs at most four times before it is surfaced as `erroring` and left
  alone until a `package rerun`/`reset`, a config or spec change, or the failed
  Job's TTL expires. **This shortens self-healing from up to an hour to roughly
  70 seconds** for a package that previously rode out transient environment
  problems (a registry blip, a mount not up yet) by crash-looping until it
  succeeded. If you depend on that behavior, raise `JOB_BACKOFF_LIMIT` (chart
  value `controllerManager.manager.env.jobBackoffLimit`).
  - Not every failed attempt counts against the package: pods lost to eviction,
    preemption or node shutdown are ignored, and attempts the kubelet refused to
    admit spend the budget but never mark a package `erroring` on their own.
- **Finished Jobs are retained and then cleaned up on a timer**, controlled by
  `JOB_TTL_SUCCEEDED` (default 1h) and `JOB_TTL_FAILED` (default 24h) — failure
  logs outlive success logs. Both have a hard minimum of one minute; the
  operator refuses to start below it, so `"0"` is not a way to disable retention.
  - **A successful `uninstall` stage is the one exception and keeps no logs.**
    A successful no-interrupt uninstall's completion *is* the removal of the
    package's node-state entry, so its Job is eligible for rerun cleanup the
    moment it finishes and never serves out `JOB_TTL_SUCCEEDED`. Starting an
    uninstall also clears that package's retained apply/config Jobs. Failed
    uninstalls are retained normally. A successful uninstall's output is
    therefore unrecoverable — a known limitation, tracked in #443 and deferred
    to a later release.
- Editing `stageTimeout`, `JOB_STAGE_TIMEOUT` or `JOB_BACKOFF_LIMIT` applies to
  the *next* stage Job, not one already running (a Job's pod template is
  immutable). To apply a new value to work already under way, clear that Job
  with `kubectl nodewright package rerun <package>`. Editing the **package
  itself** does clear a stage that has already given up, so fixing a broken
  image or config takes effect without waiting for the failure TTL.
- **Metrics now use controller-runtime's built-in authentication and
  authorization filter.** The operator serves TLS metrics directly on `:8443`
  with controller-runtime's in-memory self-signed certificate. The manager
  performs TokenReview and SubjectAccessReview calls in process; the metrics
  reader role and `/metrics` authorization contract are unchanged. Scrapers
  must disable certificate verification because the per-pod certificate has no
  stable, published CA.

- Align the default interrupt drain pod filter with Kubernetes drain semantics:
  already-terminating pods and mirror/static pods are skipped, unschedulable
  tolerations use Kubernetes `ToleratesTaint` matching, and DaemonSet pods are
  identified from the controller owner reference instead of the previous owner
  reference count heuristic.

### Upgrade notes

- **Upgrade with no package work in flight.** Jobs and the pre-rename raw-pod
  executor are never allowed to run side by side, so there is no dual path and
  nothing is converted: because this release also carries the rename, every
  operator that precedes it is a pre-rename one, and `legacyMigrationHold`
  withholds all `NodeWright` reconciliation while any legacy `Skyhook` is still
  rolling out. The new operator therefore cannot start a Job on a node the
  pre-rename operator may still be mutating. Pre-upgrade package pods run to
  completion as pods and are removed by the prune once `LEGACY_CLEANUP_DELAY`
  elapses. This is the same quiet-window prerequisite the rename carries above —
  see [docs/getting-started/migration.md](../docs/getting-started/migration.md).
  - **If you upgrade anyway with a rollout in flight**, the failure mode is a
    **stuck, cordoned node** rather than corruption or double execution: the hold
    protects the node, the legacy `Skyhook` stays frozen at its stage, and
    nothing progresses until you either roll back and let the rollout finish or
    delete the legacy `Skyhook`.
  - **Do not unpause or enable a migrated `NodeWright` until its pre-upgrade
    package pods are gone.** The hold treats a paused or disabled `Skyhook` as
    not-in-flight, deliberately, so migrating never forces you to unpause one —
    but neither pre-Jobs pause nor disable stopped a *running* pod, so one of
    those objects can cross the upgrade with a live raw pod. Unpausing or
    enabling it before that pod exits puts a Job alongside it on the same host
    `copyDir`; the agent's flag files make re-execution idempotent but are not a
    lock, so that sequence is documented as unsupported rather than guarded.
    Leaving the object paused or disabled is safe indefinitely.

## operator/v0.17.0 - 2026-06-12

### Bug Fixes

- **Reapply-on-reboot dropped on busy nodes.** With `REAPPLY_ON_REBOOT=true`, a
  reboot of a node under heavy controller churn (frequent pod/label/annotation
  updates) could be detected and then silently lost: the per-node state reset
  was persisted with a full `Update` that lost an optimistic-concurrency race,
  yet the node's boot id was advanced anyway, marking the reboot handled. The
  node kept its stale `complete` state and the package was never reapplied
  (`unknown -> complete`, no pod). The reset now persists via a strategic-merge
  `Patch` (not resourceVersion-gated, like the rest of the reconcile), and the
  boot id is advanced only after that write succeeds, so a failed reset leaves
  the reboot pending to be retried. Also fixes `Reset()` deleting the cordon
  annotation with a key missing the Skyhook name.

## operator/v0.16.1 - 2026-05-22

### Bug Fixes

- **Webhook serving-cert bootstrap deadlock on major upgrade.** During an
  upgrade from older versions, an old-version leader holding the main
  reconcile lease could deadlock the new webhook's serving-cert bootstrap
  and prevent the new controller from coming up. The bootstrap now runs
  under its own dedicated `ctrl.Manager` with a separate leader-election
  lease (`nodewright-webhook-bootstrap.nvidia.com`), so it no longer
  contends with the main reconcile lease and the upgrade proceeds even
  while the old leader still holds the primary lease (#243).

## operator/v0.16.0 - 2026-05-19 — Explicit Uninstall

Introduces an opt-in declarative uninstall workflow and reworks how downgrades
and CR deletion behave. Affects the Operator, Webhook, and CRD.

### New Features

- Add a standard `Ready` condition to Skyhook status for native Kubernetes wait and GitOps health tooling.

### New behavior

- **`uninstall.enabled` / `uninstall.apply` on each package.** Setting
  `apply: true` (requires `enabled: true`) triggers an uninstall pod on every
  target node, running `uninstall.sh` / `uninstall-check.sh` from the package's
  ConfigMap (or the agentless equivalent) with the full package configuration
  (env, resources, volumes).
- **Interrupt after uninstall.** Packages with an `interrupt:` block (reboot,
  service restart, etc.) now run that interrupt *after* the uninstall pod
  completes, via a new `StageUninstallInterrupt` stage on `PackageStatus`. The
  new stage is distinct from the install-cycle `StageInterrupt` so the two can
  never be confused.
- **Finalizer-driven cleanup on CR delete.** Deleting a `Skyhook` CR now blocks
  on uninstall completion for every `enabled: true` package before the
  finalizer clears. Uncordon, labels, annotations, and per-node ConfigMaps are
  cleaned up automatically.
- **`UninstallInProgress` and `UninstallFailed` status conditions** report the
  state of in-flight uninstall work.
- **`Blocked` status condition** is emitted when a package depends on another
  package that is currently uninstalling (DAG dependency safety).
- **Spec-change pod recreation.** Editing an explicit-uninstall package's
  ConfigMap or env while the uninstall pod is failing causes the operator to
  recreate the pod with the new config — fixes can be rolled forward without
  manual pod deletion, even on a CR that is being deleted.

### Removed / changed behavior

- **Removing a package from `spec.packages` no longer triggers an uninstall.**
  For `enabled: false` (or unset) packages, the package's entry is **left in
  the node state annotation** (`skyhook.nvidia.com/nodeState_<name>`) — no
  uninstall pod runs and nothing on the node is cleaned up, so the persistent
  state entry signals to operators that the package's files are still on the
  node. For `enabled: true` packages, the webhook now **rejects** removal
  until the package has been explicitly uninstalled on all nodes.
- **Downgrades are gated.** The webhook rejects a version downgrade unless the
  OLD spec already had `uninstall.apply: true` AND the package is absent from
  every tracked node's state. The old "downgrade auto-triggers an uninstall
  pod" path is removed. For `enabled: false` packages, downgrades are accepted
  but the old version's node-state entry is preserved (D2 semantics: absent =
  cleanly uninstalled; non-absent = not cleanly uninstalled, just superseded).
  Upgrades are unchanged.
- **`apply: true` with `enabled: false`** is rejected by the webhook.

### Deprecations

- Deprecated prefixed Skyhook status condition types such as `skyhook.nvidia.com/Ready`, `skyhook.nvidia.com/Transition`, and `skyhook.nvidia.com/TaintNotTolerable`; bare condition types such as `Ready` and `TaintNotTolerable` are now emitted alongside the legacy names for one release.

### Migration

See [`docs/user-guide/uninstall.md`](../docs/user-guide/uninstall.md) for the API reference, workflow
examples, cancellation semantics, webhook rules, and migration guidance from
the previous remove-from-spec behavior.
