# Package Execution as Jobs design

This document specifies migrating package-stage execution from operator-managed raw Pods to `batch/v1` Jobs — one Job per (skyhook, package, version, stage, node). It is the design companion to [issue #223](https://github.com/NVIDIA/nodewright/issues/223); implementation is tracked by sub-issues #299–#305, each a separate PR to the `feature/package-as-jobs` branch.

Cordon/drain ownership, DeploymentPolicy semantics, and multi-node Job parallelism are **out of scope**. The migration makes **one deliberate, additive CRD change**: a per-package `stageTimeout` field. Fast ImagePullBackOff surfacing keeps its own fix in issue #306; the stage deadline here bounds that hang as a side effect but does not replace the precise fix.

## TL;DR

One `batch/v1` Job runs each package stage on each node. The Job controller owns the pod lifecycle we hand-roll today, and `kubectl logs job/<name>` works during and after a run. The lifecycle state machine — stages, Status/State derivation, interrupt/cordon/drain, DeploymentPolicy — is unchanged; three deliberate enhancements ride along (stage deadline, pause-as-stop, failure-log archive).

| Decision | Choice | Why |
| --- | --- | --- |
| Execution unit | one Job per (skyhook, package, version, stage, node) | Job controller owns pod lifecycle; stable `kubectl logs job/<name>` |
| Main container | exit-0 container replaces the forever-`pause` | a pod must reach `Succeeded` for its Job to complete |
| Retry substrate | `restartPolicy: Never` + a finite `backoffLimit` (interrupt Jobs keep `OnFailure` + unlimited) | each failed attempt survives as a full-log archive pod |
| Disruptions | `podFailurePolicy` ignores `DisruptionTarget` | eviction/preemption stay silent and self-heal, exactly as today |
| Completion marker | persisted `state-recorded` Job annotation | pods now linger, so delete-on-success can't be the marker |
| Stage bound | per-attempt `activeDeadlineSeconds` (new `stageTimeout` field), then time out on a spent retry budget | hung/unpullable stages surface as `erroring` instead of hanging forever |
| Pause | cascades to `spec.suspend` on unfinished Jobs | the Emergency Stop can finally stop a *running* stage |
| TTL | outcome-based, set at completion | failure logs outlive success logs |
| Finished-Job cleanup | deterministic names + a rerun predicate | the name is the claim; reruns and resets clear it |
| Upgrade | runtime dual-path, no migration shim | in-flight legacy pods finish under the old path for one minor |

Everything below is optional depth. The load-bearing invariants: node state is the source of truth (Jobs are executors, not state), completion is recorded exactly once via the `state-recorded` marker, and `erroring` is reserved for genuine step failures — never for disruptions.

## Problems

### Debugging requires racing the operator

The operator detects stage completion by reading init-container statuses, then **deletes the pod**. Logs for a completed stage are gone the moment the operator processes it; debugging requires catching the pod before deletion or knowing its generated name in advance.

### Hand-rolled lifecycle

Cleanup, completion tracking, "does this stage already run" checks, and stale-spec invalidation are all custom code spread across the controller and pod-controller. Kubernetes has a first-class run-to-completion primitive that carries most of this for free, plus ecosystem benefits (`kubectl logs`/`describe job`, k9s/ArgoCD/dashboard treatment of Jobs as completable work).

## Goals

1. **Stable, queryable execution records**: one Job per (skyhook, package, version, stage, node); `kubectl logs job/<name>` works during and after execution.
2. **Logs outlive completion**: finished work is retained for a configurable window; the logs that matter most for humans (failing steps) are retained the longest.
3. **No behavior change to the lifecycle state machine, with deliberate enhancements**: stage progression, Status/State derivation, interrupt/cordon/drain sequencing, DeploymentPolicy interactions, and agent-visible env are unchanged. A pod lost to *disruption* is silently re-executed with no `erroring` and no DeploymentPolicy impact, exactly as today. The enhancements: an attempt that runs past its **deadline** is killed and retried like any other failed attempt, and a stage whose **retry budget** is spent is surfaced as `erroring` and **timed out**; **pause becomes a true stop**; and each genuinely failing stage keeps a **full-log archive** of its most recent failed attempt.
4. **Failure evidence outlives failure**: the most recent failed attempt stays `kubectl logs`-queryable while the stage retries and after it times out.
5. **Safe upgrade**: an operator upgrade with in-flight raw pods completes those stages correctly, without double-executing them.

## Current behavior (baseline)

Facts about today's implementation that shape the design; symbols are named here so the design sections below can stay at altitude.

- **Package pods never terminate.** All work runs sequentially in `initContainers` (init-copy → `<stage>` → `<stage>-check`); the main container is a `pause` image that runs forever ([`createPodFromPackage`](../../operator/internal/controller/skyhook_controller.go)). The lingering Running pod doubles as the "stage in-flight, don't create another" marker ([`PodExists`](../../operator/internal/controller/skyhook_controller.go)).
- **The operator is the completion detector.** [`containerExitedSuccessfully`](../../operator/internal/controller/pod_controller.go) inspects init-container statuses; on success the operator updates node state and then **deletes the pod** — deletion is also the processed-once marker (the `DeletionTimestamp == nil` gate prevents double-processing). The write pair is not atomic today either (node Patch, then pod Delete); the Jobs design keeps the same crash window, not a larger one.
- **Retries are kubelet in-place restarts.** `restartPolicy: OnFailure` crash-loops a failing init container forever; the operator only reports `erroring` with restart counts — it never recreates a failing pod. A pod that *disappears* (evicted, deleted) produces no state write: node state stays `in_progress` and a later reconcile silently recreates it via the `PodExists` miss. Pods on a **deleted node** are cleaned up by kube-controller-manager's PodGC, not by the operator.
- **Pods are owned by the Skyhook CR**, carry `nodewright.nvidia.com/name` + `…/package` labels (interrupt pods add `…/interrupt: "True"`), and a JSON package annotation that round-trips (skyhook, package, version, stage, image).
- **Interrupt pods have their own name formula** (skyhook, stage, interrupt-type, node — no package/version), which dedupes the merged interrupt: one per node/stage/type, independent of which package's interrupt won.
- **Config updates delete erroring package pods directly** ([`HandleConfigUpdates`](../../operator/internal/controller/skyhook_controller.go)) so they are recreated with the updated configmap.
- **[`cluster_state_v2.go`](../../operator/internal/controller/cluster_state_v2.go) does not read pods.** Node annotations are the state store; pods are ephemeral executors.

## Design

### Job shape

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: <generateSafeName(63, skyhook, package, version, stage, node)>   # interrupt Jobs: skyhook, stage, interruptType, node
  namespace: <operator namespace>
  labels:
    nodewright.nvidia.com/name: <skyhook>            # existing
    nodewright.nvidia.com/package: <name>-<version>  # existing
    nodewright.nvidia.com/interrupt: "True"          # existing (interrupt Jobs only)
    nodewright.nvidia.com/stage: <stage>             # new
    nodewright.nvidia.com/node: <node>               # new (hashed for >63-char node names — see Labels)
    nodewright.nvidia.com/generation: "<n>"          # new: the SCR generation that produced this Job
  annotations:
    nodewright.nvidia.com/resource-id: <full resource id>   # provenance; too long for a label
  ownerReferences: [<Skyhook CR>]                 # cascade delete; pods are owned by the Job
spec:
  parallelism: 1
  completions: 1
  backoffLimit: <JOB_BACKOFF_LIMIT>               # retries after the first attempt (interrupt Jobs: unlimited)
  podFailurePolicy:                               # package Jobs only (the API forbids it with OnFailure)
    rules:
    - action: Ignore
      onPodConditions:
      - type: DisruptionTarget
  activeDeadlineSeconds: <stageTimeout>           # interrupt Jobs ONLY — package Jobs bound each attempt instead
  podReplacementPolicy: Failed                    # replace only after full termination — no hostPath overlap
  # ttlSecondsAfterFinished: unset at creation — set by the operator at completion (see TTL)
  template:
    metadata:
      labels: <same label set>                    # child pods inherit labels → existing CLI label queries keep working
      annotations:
        nodewright.nvidia.com/package: <same JSON package annotation>   # the in-flight erroring path reads it off the child pod
    spec:
      # identical to today's pod spec, with the three changes described below
      restartPolicy: Never                        # interrupt Jobs: OnFailure
      activeDeadlineSeconds: <stageTimeout>       # per attempt; omitted when the time bound is off
      tolerations:
      # existing tolerations, plus unbounded NoExecute for not-ready / unreachable
      - { key: node.kubernetes.io/not-ready,   operator: Exists, effect: NoExecute }
      - { key: node.kubernetes.io/unreachable, operator: Exists, effect: NoExecute }
      containers:
      - name: done                                # replaces the forever-running `pause` container
        image: <package image>                    # already pulled by init-copy, and guaranteed to have /bin/sh
        command: ["/bin/sh", "-c", "exit 0"]
```

Three things about this shape are non-obvious:

- **The main container changes** because a Job records completion only when its pod reaches `Succeeded`, which needs every container to terminate. It runs on the **package image**, not the agent image: the init-copy container already invokes `/bin/sh` from the package image, so that image is guaranteed to have a shell, whereas a minimal agent image may not — and an exit-0 container whose `/bin/sh` is missing `StartError`s, so the pod never succeeds and the Job hangs forever. The init-container chain stays — it is the only native run-to-completion sequencing primitive, and per-step container names are load-bearing for state reporting. The `pause` container's *other* job, being the in-flight marker, is taken over by the Job object, which outlives its pods.
- **`restartPolicy: Never` on package Jobs** makes each attempt a fresh pod, so a failure survives as a full-log archive pod instead of being destroyed by an in-place restart. Interrupt Jobs are the exception — they keep `OnFailure`, because a reboot interrupt kills its own pod by design; under `Never` every successful reboot would mint a spurious failed attempt, whereas in-place restart after the node returns is the proven recovery shape (the agent skips the already-done interrupt via its resource-id flag file).
- **The unbounded not-ready/unreachable tolerations** stop the taint manager from evicting a node-pinned pod when a reboot-class interrupt keeps the node NotReady past the default eviction timeout. These pods are node-bound host agents — running them anywhere else is meaningless, so eviction is never useful. A node that stays NotReady forever holds the Job Active exactly as today's raw pod would; a *removed* node is handled by the orphaned-node sweep.

`backoffLimit` is finite (`JOB_BACKOFF_LIMIT`, default 3): under `restartPolicy: Never` it counts Failed pods, and the `Ignore`-on-`DisruptionTarget` rule keeps disruptions out of that count. Counting an attempt and *timing the stage out* are then two different questions, and a Failed pod falls into one of three classes: an ignored disruption spends nothing; an attempt the kubelet refused to admit spends an attempt but is not the package's failure; a genuine step failure or per-attempt timeout spends an attempt and is the package's failure. Exhausting the budget therefore takes the Job terminal, but only the third class records the stage as timed out — see Retry and the failed-attempt archive. The retry loop lives inside the Job controller — attempt fails → the operator archives the Failed pod → the Job controller paces the next attempt under its own backoff — so the operator never drives recreation, and there is no operator-side retry counter to persist. `podReplacementPolicy: Failed` guarantees the replacement lands only after the previous pod has fully terminated, so two executors never overlap on the shared hostPath mounts.

Interrupt Jobs are the exception and keep an unbounded limit. Under `OnFailure` the limit counts container *restarts* rather than failed pods, so a finite budget would be spent by the in-place restart that *is* the reboot recovery.

### Completion and the processed-once marker

The pseudo-controller pattern is kept (issue #223 option A): a Jobs watch maps events into the single global reconcile queue as `job---<name>` requests, alongside the existing `pod---<name>` routing, so no second reconciler writes state and the serialization guarantee holds. The Jobs informer is namespace-scoped.

Because completed pods now linger, pod-deletion can no longer be the processed-once marker. A persisted Job annotation, `nodewright.nvidia.com/state-recorded: "true"`, replaces it. On each Job event:

- **`Complete`, not yet marked** → read the `Succeeded` child pod for the container name (selected by the Job's controller UID, not by name — a same-named prior Job's pod can still be terminating), record completion through the existing node-state path (upgrade/uninstall/interrupt special cases unchanged), then one Job Update sets `state-recorded` and the success TTL. If the child pod was already GC'd, fall back to the Job's own stage/node/interrupt labels. Already-marked Jobs are skipped, so duplicate events are harmless. The recorded attempt count now comes from the Job's failed-pod count — a user-visible improvement over today's usually-zero restart count.
- **`Failed`, on genuine evidence** → the stage is out of attempts (`BackoffLimitExceeded` with at least one retained archive that really failed) or hit the Job-level ceiling (`DeadlineExceeded`, which needs no archive — see Stage deadline, below). Record `erroring`, mark processed with the failure TTL, and **leave the Job in place as the timeout marker**.
- **`Failed`, without it** → every retained attempt was a pod the package never ran in, the kubelet-admission case below. No node-state write (matching today's silent disruption behavior); one Update carries the marker and failure TTL, and the sweep then clears the Job so the stage re-runs.
- **Node gone** → if the node `Get` returns NotFound, there is no state to record: mark the Job processed and stop, rather than error-looping on a node that will never return.
- **Active** → no terminal handling; the Pod watch keeps reporting in-flight `erroring` from failed-attempt evidence, and the operator prunes failed attempts to one archive.

The Pod watch is **evidence, not authority**: it may only update an entry that already exists at the pod's stage and is not yet complete. It must never create one. Under `restartPolicy: Never` a failing Job mints a fresh pod per attempt, indefinitely, so an unguarded write becomes a repeating one. If it could create an entry, a node-state reset would be undone by the very Job the reset is meant to clear: the resurrected entry makes the reset invisible to the not-in-node-state check in rule 1 below, so the stale Job is never invalidated, and the existence gate then blocks the package's new stage forever. The same guard also stops a retained failed-attempt archive pod (kept on purpose, below) from regressing a completion the Job path already recorded.

**Neither may the Job path create an entry**, for the same reason, even though it *is* the completion authority. Authority means it decides when a stage is done, not that it may write a package entry that node state says is gone. This bites on interrupt Jobs specifically: they are package-agnostic, so a completion is recorded on the strength of a *sibling* still sitting at (interrupt, `skipped`) — which says nothing about whether this Job's own package still has an entry. A rerun, reset or uninstall landing in that window would otherwise see the entry return at (interrupt, `complete`), where the rerun predicate keeps the Job and the stage never runs again. The promotion of skipped siblings happens either way; only the self-write is gated.

Recording a completion is two writes to two objects (node state, then the Job marker) and cannot be atomic; a crash between them re-serves the event. The re-processing path is guarded by per-transition postcondition checks so a re-served completion is only marked, not re-applied — detail in [Edge cases](#edge-cases-and-correctness-arguments). This is strictly better than today, which has the same two-write window with no guard.

### Retry and the failed-attempt archive

Retries are bounded by a budget rather than a clock, and the substrate moves from kubelet in-place restarts to Job-controller attempts, buying durable failure evidence:

- **Each attempt is a fresh pod.** A failing step fails its pod; the Job controller paces the next attempt (comparable to kubelet crash-loop backoff), pinned to the same node and gated so attempts never overlap on the hostPath mounts. The agent's host-side flag files carry across attempts, so each retry re-runs only init-copy and the failing step.
- **The archive (keep two).** Terminal-phase pods are not reaped by the Job controller, so failed attempts would accumulate; the operator prunes them, keeping the **first** genuine failure (most likely the root cause, before cascading errors obscure it) and the **most recent** — both carrying no `DisruptionTarget` condition, since a disruption casualty has no failure verdict and must not shadow a real one. Steady state is one running attempt plus up to two full-log archives — the first and latest real failures — queryable while the stage retries, after it times out, and through a pause.
- **Disruptions stay silent.** The `Ignore`-on-`DisruptionTarget` rule makes evictions, preemptions, taint-manager kills, and PodGC deletions count neither toward backoff nor as failed attempts — the replacement appears and nothing is recorded, matching today's invisible recreate. (Two honest deltas around hard node crashes are in [Edge cases](#edge-cases-and-correctness-arguments).)
- **Erroring evidence** still comes from pod observation, now triple-guarded: a Failed package pod marks the node `erroring` only if it carries no `DisruptionTarget` condition, its step container terminated with a real verdict, and its `DeletionTimestamp` is unset. The last guard is load-bearing — pause suspension, Job deletions, config updates, the sweeps, and a human's `kubectl delete pod` all kill pods without `DisruptionTarget`, and every one of them must stay silent. Full guard rules are in [Edge cases](#edge-cases-and-correctness-arguments).

A Failed *Job* therefore means the retry budget is spent — or, for an interrupt Job, that its whole-stage deadline fired; an individual *pod* failure only spends one attempt.

**Not every counted attempt is the package's fault.** These pods carry `spec.nodeName` rather than going through the scheduler, so kubelet admission is the only resource gate they face: a node at capacity or on its way back from a reboot can reject several node-pinned replacements in a row. Those pods are `Failed` with no container statuses and no `DisruptionTarget` for the `Ignore` rule to match, and no `podFailurePolicy` rule can absorb them (`onExitCodes` needs container statuses there are none of — the same argument that keeps ImagePullBackOff out of the policy). Under an unbounded limit they only cost an archive slot; under a finite one they can exhaust the budget in about a minute and time out a package that never ran a line of script. So `BackoffLimitExceeded` is believed only when a retained archive is a real failure — a step that exited nonzero, or an attempt killed by its own deadline. If none is, the Job is marked and swept, and the stage re-runs: the same invisible self-heal a vanished pod gets today. The archive pruner selects on the same predicate as this check, so a real failure with rejections either side of it is never pruned out from under the classifier — an earlier draft let the pruner use the looser Failed-and-not-disrupted test, which could leave the classifier reading only rejections and clearing a genuinely failing stage.

### Stage deadline: bounded attempts, timed-out Jobs

Today a repeatedly-failing stage churns forever and a *hung* stage (stuck script, unpullable image, dead registry) sits `in_progress` invisibly forever. A timeout is a failure like any other, so it is bounded per **attempt** and retried: the bound rides on `spec.template.spec.activeDeadlineSeconds`, and the finite `backoffLimit` is what finally gives up. Those two are the only bounds on a package Job — there is deliberately no Job-level deadline over the top of them (below).

**Configuration.** A new additive per-package field, following the existing `gracefulShutdown` duration pattern:

```yaml
spec:
  packages:
    mypackage:
      version: 1.0.0
      image: ghcr.io/example/pkg
      stageTimeout: 2h        # optional; bounds each stage Job for this package
```

Unset falls back to an operator default (`JOB_STAGE_TIMEOUT`, chart value `controllerManager.manager.env.jobStageTimeout`, default 1h). An explicit `0` removes the *time* bound — the builder omits the deadline, since a literal zero would insta-fail every Job — leaving `backoffLimit` as the only limit. Note what that does *not* bound: a retry budget is only spent by attempts that fail, so with `0` an attempt that hangs forever hangs forever, and one the kubelet never acknowledges never even reaches that. `0` means "this stage may take as long as it takes", not "this stage is bounded some other way". Webhook validation: non-negative. This is the only CRD change in the migration; it is additive and needs no migration shim (absent field = operator default).

**On expiry** the kubelet kills the pod and marks it `Failed` with pod-level reason `DeadlineExceeded`. It carries no `DisruptionTarget`, so the `Ignore` rule does not swallow it and it spends one attempt like any other failure; the Job creates a replacement. The pod is failed *in place* rather than deleted, so — unlike the Job-level deadline it replaces — a timed-out attempt survives as a full-log archive.

**Retries exhausted** (`BackoffLimitExceeded`, on genuine evidence) is what times a stage out. The operator records `erroring` and the Failed Job stays put: the finished-Job rules deliberately do not delete it while the package's entry sits at (same stage, `erroring`), so nothing recreates the stage and the churn stops. The timeout clears on any explicit signal — `package rerun`/`reset`, a config update, a spec change, or failure-TTL expiry — after which the still-`erroring` state drives one fresh attempt: a deliberate slow-retry cadence until a human intervenes.

**The budget bounds every failure class, not just timeouts.** A crash-looping package now times out after `backoffLimit + 1` attempts — about 70 seconds at the default, paced by the Job controller's 10s/20s/40s backoff — where an unbounded limit let it retry for the whole `stageTimeout`. Packages that ride out transient environment flakiness (a registry blip, a mount not up yet) lose that hour of self-healing, which is why the limit is an operator knob (`JOB_BACKOFF_LIMIT`, chart value `controllerManager.manager.env.jobBackoffLimit`, default 3) rather than a constant.

**No Job-level ceiling on package Jobs, and one case that leaves unbounded.** An earlier revision carried a second, Job-level `activeDeadlineSeconds` derived from the retry budget, to cover the case the per-attempt clock cannot see: that clock runs from `pod.Status.StartTime`, so a pod the kubelet never acknowledges never starts it, never fails, and never spends an attempt — the Job sits Active indefinitely. It was removed, because a second clock over the same work can only disagree with the first: derived from the budget it is redundant, and any value below the budget silently truncates it into a `DeadlineExceeded` that reads as a hang.

That leaves the never-acknowledged pod genuinely unbounded, and this is accepted rather than solved. Nothing the operator can set bounds a pod the kubelet has not accepted: `activeDeadlineSeconds` on either object is the only wall-clock the API offers, and the pod-level one is exactly the clock that never starts. The node-side condition (a kubelet that is up enough to be scheduled to but not up enough to admit) is the same one that makes such a node unusable generally, and it is visible: the stage sits `in_progress` with a Pending pod, `kubectl describe pod` names the admission problem, and `kubectl nodewright node status` shows the stage stuck. Treat it as node health, not stage health.

**A stage's bounds are fixed when its Job is created.** Editing `stageTimeout`, `JOB_STAGE_TIMEOUT` or `JOB_BACKOFF_LIMIT` changes what the *next* Job is built with; it does not reach a Job already running, and validation does not treat the difference as staleness, so nothing is replaced on the strength of it alone.

That is a deliberate limitation rather than an oversight, because Kubernetes offers no clean way to do better. The per-attempt bound lives on the Job's pod template, and a Job's template is immutable — so it cannot be patched, and even if it could, a template edit only reaches pods created *after* it. The running pod's own `activeDeadlineSeconds` is one of the few mutable pod-spec fields, but it may only be **decreased**, never increased — exactly backwards from the edit that motivates the change, since users raise a timeout when a stage needs longer. Applying an increase therefore means replacing the Job, which kills the in-flight attempt to grant it more time.

So the contract is: the new value applies at the package's next stage. To apply it to work already under way, clear the Job — `kubectl nodewright package rerun <package>`, or delete the Job — and the stage restarts under the new value. A *spec* change is different from a knob change: editing the package itself (image, config, resources) deletes even a timed-out Job, because that edit is the user fixing what broke.

**Interrupt Jobs keep the Job-level bound** and take no per-attempt one: a reboot interrupt's deadline has to span the reboot, and `StartTime` does not reset when the kubelet restarts its containers after the node returns, so a per-attempt clock sized for normal work would kill a legitimate reboot.

**Log visibility.** A per-attempt expiry terminates the pod's containers and marks the pod `Failed` with reason `DeadlineExceeded`; it does **not** delete the pod object, so a timed-out attempt survives as an ordinary failed-attempt archive with its logs intact, subject to the usual Job and pod GC. A Job-level deadline applies only to interrupt Jobs, where a reboot can remove the active pod while the host is shutting down. Those cases use the agent's host-side logs; the operator does not copy pod logs into Job annotations or require the pod-log subresource. On an **unreachable node** the Job can sit at `FailureTarget` without ever going terminal, so a stale `FailureTarget` is treated as erroring evidence too (state write only; the marker/TTL/timeout still wait for `Failed`), and a reboot interrupt that bricks its node still surfaces.

Deadline-`erroring` counts toward DeploymentPolicy failure thresholds like any `erroring` — new signal for hung stages, identical to today for crash loops.

### Pause cascades to Job suspension

Today the `nodewright.nvidia.com/pause` annotation (the CLI's **Emergency Stop**) only blocks *new* stage scheduling — an in-flight stage runs to completion, so pause cannot actually stop it. With Jobs, pause gets teeth: when the operator observes the annotation it sets `spec.suspend: true` on all of the Skyhook's unfinished Jobs, and clears it on resume. The annotation stays the user-facing primitive; suspension is the enforcement.

- **Suspension SIGTERMs the running pod** (honoring `gracefulShutdown`) and no pods start until resume. On resume the fresh pod re-runs the stage; the agent's flag files skip completed steps and re-run the interrupted one — the same recovery shape as an eviction or reboot mid-stage, which packages must already tolerate. This is the "no checkpointing" cost the earlier draft rejected `suspend` over, now accepted deliberately, because a pause that can't stop anything isn't an emergency stop.
- **The deadline pauses with the Job.** Suspension deletes the running pod, so the resumed attempt starts a fresh per-attempt clock — otherwise a "paused" stage could quietly hit its deadline and time out while the user believed everything was frozen. (Package Jobs carry no Job-level clock to keep ticking; an interrupt Job's is reset by suspension clearing the Job's start time.) Suspend-deletions are excluded from the failure count upstream (verified empirically), so pausing never spends the retry budget; `status.failed` is *not* reset either, so pausing to investigate a half-failed stage and resuming leaves the remaining budget unchanged.
- **Resume has an explicit owner and ordering.** Because the pause path only runs for paused Skyhooks and paused Skyhooks skip validation, un-suspension is a separate step for *non-paused* Skyhooks, ordered after validation: invalidate suspended Jobs whose spec changed while paused, *then* clear `suspend` on the survivors. Clearing first would let one stale-spec attempt launch before validation catches it.
- **Everything else is indifferent**: existence gating counts the suspended Job, completion ignores it (Suspended is not terminal), and node state stays `in_progress` (guard (c) of the erroring evidence makes that hold).
- **Interrupts already fired**: suspension can't un-ring a reboot; on resume the replacement pod skips the interrupt via the resource-id flag and completes.
- **Legacy pods can't suspend** — a pre-rename raw pod has no Job, so pause cannot stop one. The migration hold keeps the two execution models apart for anything *in flight*, but it deliberately treats a paused or disabled `Skyhook` as not-in-flight, so migrating never forces you to unpause one — which means such an object can cross the upgrade with a live raw pod, because neither pre-Jobs pause nor disable stopped a *running* pod. Unpausing or enabling it before that pod exits puts a Job alongside it on the same host `copyDir`; wait for the pod to be gone (see [Upgrade and compatibility](#upgrade-and-compatibility)). What stays version-dependent is pause's stop-strength *across operator versions* — a pre-Jobs operator lets an in-flight stage finish — which is the CLI-docs note.

`disable` is unchanged: it skips a Skyhook from processing but has never claimed to halt in-flight work.

### TTL: outcome-based, set at completion

`ttlSecondsAfterFinished` is mutable and applies to both `Complete` and `Failed` Jobs. Jobs are created with it unset; the operator sets it in the same Update that writes `state-recorded`:

| Outcome | Operator env | Chart value (`controllerManager.manager.env.*`) | Default |
| --- | --- | --- | --- |
| Succeeded | `JOB_TTL_SUCCEEDED` | `jobTtlSucceeded` | `1h` |
| Failed | `JOB_TTL_FAILED` | `jobTtlFailed` | `24h` |

Failure logs thus outlive success logs. If the operator is down when a Job finishes, the TTL is simply set on resync — level-triggered, no timer lost. Where failure logs actually live: the archive pod for a genuinely-failing step and the agent's host-side logs (`SKYHOOK_LOG_DIR`) for host-level interruptions.

Scale: succeeded pods are retained for the success window — roughly `nodes × packages × 3 stages` extra terminated pods at peak — plus at most two archived failed pods (the first and latest failures) per actively-failing stage. Keep `JOB_TTL_SUCCEEDED` short; note that kube-controller-manager's terminated-pod GC may reap retained *pods* (not Jobs) earlier on large clusters, which is exactly why the completion path has a no-child-pod fallback. Sizing guidance lives in [`operator_resources_at_scale.md`](../operations/resources-at-scale.md).

### Finished-Job lifecycle: naming and reruns

Job names are deterministic (same inputs as today's pod names), which makes creation idempotent — the name *is* the claim. But a finished Job keeps its name until TTL, so every "run this stage again" path must first clear the name. Five rules replace today's delete-on-success; "processed" means `state-recorded` is set, for both outcomes, and Job deletions are always **foreground** so a deterministic name frees only after its child pods are gone.

1. **Validation is scoped to unfinished Jobs.** The spec-mismatch / not-in-node-state / stage-mismatch checks apply only to unfinished Jobs; applying them to finished Jobs would delete every retained Job the moment the node progresses, gutting Goal 2.
2. **The rerun predicate.** A processed finished Job for stage `S` is deleted once node state no longer records `S` as done (entry absent, or progress at/before `S` without `S` complete — compared via the state machine's stage ordering, not string equality). **Timeout exception:** a `Failed` Job is *not* deleted while the entry sits at (`S`, `erroring`) — that pair is the timed-out state; every other value clears it, which is what sweeps away a failure the operator judged non-genuine and never wrote state for. **Unprocessed** finished Jobs are never deleted except by the two sweeps — for `Complete` that protects an unrecorded completion, and for `Failed` it keeps this sweep from racing the erroring write, a window a finite `backoffLimit` shrinks from an hour to seconds.
3. **`AlreadyExists` on create is not blindly benign.** GET the Job: unfinished and matching → another pass won the race, return. Timed out → return; the timeout is doing its job (this branch is what absorbs level-triggered recreate attempts on an erroring stage). Not yet marked → don't delete, whatever the outcome: a `Complete` one holds an unrecorded completion, and a `Failed` one has not yet had its chance to write `erroring`, so deleting it would take its retained attempts with it and restart the stage on a fresh budget. This path reaches that window first, because a finished Job does not satisfy the existence gate. Otherwise processed, or unfinished-but-mismatched → foreground-delete and requeue, without recording an `in_progress` that isn't happening.
4. **Orphaned-node sweep.** Jobs whose node no longer exists are deleted regardless of status — an unfinished one would otherwise churn PodGC↔replacement forever against the missing node, and a finished one has no node state left to claim it.
5. **Reboot-reset sweep.** When `REAPPLY_ON_REBOOT` resets a node's state, that node's Jobs for the Skyhook are deleted regardless of status, including unprocessed `Complete` ones — a completion from a previous boot must not land on freshly-reset state. Rule 2's protection of unprocessed completions yields to this sweep.

Two retention carve-outs follow and are accepted: a successful no-interrupt **uninstall** Job's completion *is* entry removal, so it satisfies the absent-entry branch immediately and gets no retention window; and because uninstall precedes apply in the ordering, starting an uninstall also clears the package's retained apply/config Jobs. Uninstall is the "remove all trace" operation, so prompt cleanup is coherent — and crash-loop *failures* of uninstall still retain logs (Active Job). The cost is that a *successful* uninstall's output is unrecoverable, since the Job and its pod are gone before the success TTL applies: a known limitation, tracked in [#443](https://github.com/NVIDIA/nodewright/issues/443) and deferred to a later release.

The existence gates move with these rules: the "is this stage in flight" check and the "wait until nothing is running" interrupt gate both count **unfinished Jobs only** (a retained Succeeded pod must not hold an interrupt hostage — node cordoned and drained — until its TTL expires), and config-update handling deletes the package's **unfinished or terminally failed** Job rather than its child pod (which would just trigger a same-config replacement) — a config change invalidates that failure history anyway, and the deterministic name has to be freed for the rerun.

### Labels

The full resource id is routinely over the 63-character label limit, so it rides as an **annotation**; a numeric `nodewright.nvidia.com/generation` **label** carries the SCR generation instead (same-generation reruns exist — reboots and erroring retries don't bump it, though config updates do; the annotation is the exact provenance). Node names are legal up to 253 characters, so the `…/node` label is the node name when it fits and a stable hash otherwise; the operator computes the same transform on lookup, and `spec.template.spec.nodeName` always carries the authoritative full name. The issue's query goals hold with valid label values:

```bash
kubectl get jobs -l nodewright.nvidia.com/name=gpu-init                              # all Jobs in one SCR
kubectl get jobs -l nodewright.nvidia.com/name=gpu-init,nodewright.nvidia.com/node=worker-7,nodewright.nvidia.com/generation=4
kubectl get jobs -l nodewright.nvidia.com/node=worker-7                              # everything touching a node
```

### RBAC

The operator gains `batch/jobs` (`get;list;watch;create;update;patch;delete`) as a **namespaced Role**, not on the ClusterRole: every Job the operator touches lives in its own namespace (the informer is scoped there and every list passes `InNamespace`). The `namespace=` field on the kubebuilder rbac markers makes controller-gen emit the Role; the binding is hand-written (controller-gen generates roles but never bindings), and both are mirrored into `chart/` templated on `.Release.Namespace`. Pod verbs stay cluster-wide: the operator still reads workload pods on any node for drain, reads child pods for restarts/container names, and drives legacy pods during the upgrade window.

## Upgrade and compatibility

No persisted schema change → **no migration shim**.

An earlier draft of this section specified four in-place accommodations — legacy-aware completion, existence gating that ORs in raw pods, a legacy sweep in validation, and direct deletion of legacy erroring pods on config update — so that Jobs and raw pods could run side by side for one minor release. **None of them were implemented, deliberately.** They were superseded by a stricter answer: don't let the two execution models overlap at all.

**The migration hold does that.** This change ships together with the `skyhook.nvidia.com` → `nodewright.nvidia.com` rename, so the operator that precedes it is always a pre-rename one, and its Skyhooks are visible as legacy `skyhook.nvidia.com` objects. `legacyMigrationHold` runs first in `Reconcile`, before anything else, and requeues while any legacy Skyhook is still mid-rollout. So a NodeWright reconcile cannot start a Job on a node the pre-rename operator may still be mutating: the operator waits for legacy work to finish, roll back, or be deleted. Legacy pods are never converted — they run to completion as pods, are relabelled by the converge, and are graceful-deleted by the prune once the rollback window elapses.

That the two ship together is load-bearing. Were the rename to land in an *earlier* release, the preceding operator would already be nodewright-native, no legacy Skyhook objects would exist, the hold would never fire, and its raw pods would carry `nodewright.nvidia.com/name` — which the legacy-workload sweep does not select. Then the accommodations above really would be required.

**One case the hold does not cover.** It treats a paused or disabled legacy Skyhook as not-in-flight, deliberately, so migration does not force a user to unpause or enable one. But neither pre-Jobs pause nor disable stopped a *running* pod — both only blocked new scheduling. A Skyhook paused or disabled just before the upgrade can therefore still have a live raw pod, and unpausing or enabling it on the new operator before that pod finishes puts a Job alongside it on the same host `copyDir`. Nothing goes wrong while it stays paused or disabled (the new operator creates no Jobs for it). Do not read the agent's flag files as making that safe: they make *re-execution* idempotent and are not a lock, so they neither serialize two executors nor order concurrent writes into the shared `copyDir`. **Both sequences are therefore unsupported rather than guarded** — the migration guide tells users to wait for a paused or disabled Skyhook's pre-upgrade pods to disappear before unpausing or enabling it, and leaving it paused or disabled is safe indefinitely. Guarding it in code instead would mean teaching the in-flight gate to count legacy raw pods so a Job is never created beside one; that was weighed and declined, because the whole point of shipping the rename and the Jobs migration together is that the two execution models do not have to coexist, and one narrow, documented, opt-in sequence did not justify carrying legacy-pod awareness into the gate for a release.

New stages after the upgrade are Jobs immediately; nothing is proactively deleted. Legacy pod names equal new Job names byte-for-byte, but they are different kinds and Job child pods get a suffix, so there are no API name conflicts.

CLI: child pods inherit the full label set, so `package logs/status/rerun` label queries work unchanged against both operator generations, and post-completion `package logs` gets *better*. No CLI code change is expected; verification is part of #305.

## Rejected alternatives

`restartPolicy: Never` vs `OnFailure` for package Jobs is the central decision and is kept in full below; the rest reduce to a line each.

| Rejected | Why not |
| --- | --- |
| `podFailurePolicy` for ImagePullBackOff | An unpullable image never starts a container, so no exit-code or condition rule can ever match. The design *does* set `podFailurePolicy`, but only the `Ignore`-on-`DisruptionTarget` rule. Fast surfacing stays #306; the deadline and retained attempt pods bound the exposure. |
| A separate JobReconciler with its own writes | A second reconciler doing read-modify-write against node state would race the global pass — per-controller concurrency limits don't serialize *across* controllers. Splitting the controller is its own design. |
| One long-lived Job (or JobSet) per rollout | `JobSet` targets coordinated parallel workloads, not a sequential per-stage lifecycle; a Job per stage keeps the state machine's granularity. Cross-stage grouping is served by labels. |
| Static TTL at creation | A single value can't distinguish success from failure, forcing a choice between losing failure logs early and retaining success pods too long. Mutability makes outcome-based TTL strictly better for one extra field in an Update the operator already makes. |
| `erroring` on disruption-failed Jobs | An earlier draft mapped Job `Failed` → `erroring`; that would count an invisible, self-healing vanished pod against DeploymentPolicy. `erroring` stays reserved for genuine step failures. |
| `backoffLimit: 0` with operator-driven recreation | With `OnFailure` it is broken outright (in-place restarts count toward the limit and kill the package on the first retry); with `Never` it moves the retry loop into operator code this migration is trying to delete. A finite-but-nonzero limit keeps retries and attempt accounting inside the Job controller, with no operator-side counter to persist. |
| A Job-level deadline as the *only* stage bound (the earlier draft) | `JobSpec.activeDeadlineSeconds` is terminal: exceeding it fails the Job permanently with no replacement pod, so the first expiry ended the stage. That makes a timeout the one failure class with no retry, and it deletes the running pod, leaving only a 16KB annotation where every other failure keeps a full-log archive. Moving the bound to the pod template makes a timeout an ordinary retryable failure. |
| A Job-level deadline *alongside* the per-attempt one (a later draft) | Two clocks over the same work can only disagree. Derived from the retry budget it says nothing the budget does not; set independently it can sit below the budget and truncate it into a `DeadlineExceeded` that reads as a hang. The one case it did cover — a pod the kubelet never acknowledges, so the per-attempt clock never starts — is left unbounded and documented as node health, since no operator-set field bounds a pod the kubelet has not accepted. |
| Random Job name suffixes | Suffixes break creation idempotency (a crash between Create and the state write leaves two Jobs racing) and turn "does this stage run" into a list-and-filter. Deterministic names + the rerun predicate keep the level-triggered property. |

### `restartPolicy: OnFailure` for package Jobs (the earlier draft)

Two earlier revisions kept `OnFailure` everywhere for maximal parity with today's kubelet in-place crash loops. It was superseded by `Never` + the archive once the stage deadline existed, because the deadline had already removed `OnFailure`'s one decisive advantage — retry-forever-in-place — while its costs remained: an in-place restart destroys the previous attempt's container, so only the immediately-previous logs are reachable and everything dies at the deadline; backoff accounting under `OnFailure` sums restarts (a subtle trap); `podFailurePolicy` is forbidden, so disruptions can't be declaratively ignored; and no archive is possible. The remaining real costs of `Never` were measured and accepted — per-attempt init-copy re-runs (completed steps skip via host flag files), slightly slower attempt pacing, attempts counted from the Job's failed-pod count, and one honest edge (hard node crashes mint an archived attempt, guarded from flapping node state). Interrupt Jobs keep `OnFailure`, where in-place restart is load-bearing for reboot survival.

## Edge cases and correctness arguments

These are the load-bearing safety arguments, collected so the sections above can describe the happy path. All are the product of adversarial review and are retained deliberately.

**The two-write crash window and postcondition guards.** Recording a completion is a node-state write followed by the Job `state-recorded` marker — two objects, not atomic, ordered state-first so a crash can never mark a Job whose state is still `in_progress` (which would permanently lose the completion). A crash *between* the writes re-serves the event, and the node-state transitions are not idempotent, so before re-applying, the operator checks whether the transition's outcome already holds:

- normal stage → the entry records the stage complete;
- upgrade → stale-version entries gone and the entry complete;
- uninstall without interrupt → the entry is absent;
- uninstall with interrupt → the entry is at `uninstall-interrupt` or beyond, or absent;
- interrupt (the path that also promotes skipped packages) → the winning package's entry is at `interrupt`-complete or beyond, *and* no spec package is still at (`interrupt`, `skipped`).

If the postcondition already holds, the Job is only marked, not re-processed. Keying dedup by Job UID inside the node-state entry was rejected: the entry's map shape has been stable since v0.7.5, and older CLIs rewrite the whole value, silently dropping fields they don't know.

**Hard node crashes / unexpected reboots (no `DisruptionTarget`).** Two honest deltas from today. First, a crash lands one archived, counted attempt, and whether it can *briefly* flap node state depends on what the kubelet recorded: a `ContainerStatusUnknown` shape is guarded out, but a plain `terminated{137, Error}` passes the guards and shows `erroring` until the replacement attempt completes (self-healing via flag files; `REAPPLY_ON_REBOOT` deployments reset the node anyway). Second, a rebooting node can transiently reject the node-pinned replacement at kubelet admission (`OutOfPods` etc.); those Failed pods have no container statuses (never `erroring`) but do count an attempt and churn the archive slot. This replaces a *worse* latent behavior today, where an admission-rejected raw pod satisfies the in-flight check forever and wedges the stage.

**Erroring guards, in full.** A Failed package pod marks the node `erroring` only if (a) it carries no `DisruptionTarget` condition; (b) its step container terminated with a real verdict — nonzero exit including `OOMKilled`; `ContainerStatusUnknown` (the kubelet-couldn't-tell artifact of a node crash) and kubelet admission rejections (which carry a status reason and no container statuses) are skipped; and (c) its `DeletionTimestamp` is unset. Guard (c) is what keeps pause suspension, the rule-2/3 and config-update deletions, the sweeps, and manual `kubectl delete pod` silent. A pod killed by its own per-attempt deadline is admitted by a separate check on the *pod-level* `DeadlineExceeded` reason, because guard (b) cannot see it: the stuck container may never have started, and has no exit code to judge. Today's `CrashLoopBackOff` waiting-state detection now applies only to interrupt Jobs (a `Never` pod fails once and goes terminal — it never enters `CrashLoopBackOff`).

**Two writers, one timeout predicate.** A timeout is the pair (terminal `Failed` Job, entry at (stage, `erroring`)) — nothing reads a stored verdict, so a lost classification cannot by itself lose a timeout. Two independent paths put the entry at `erroring`, using the same classification so they agree: the Pod watch writes it live, while the failed attempt still exists, and the terminal Job path writes it from the retained archives. Each covers the other's blind spot — the Pod watch survives terminated-pod GC reaping the archives, and the Job path survives the operator being down for the whole retry window. Both must miss to lose a timeout, and the consequence is bounded: the Job is swept, the stage re-runs, and the next cycle's evidence times it out. That is one extra retry cycle, not churn.

**Three writers, one annotation.** `nodewright.nvidia.com/nodeState_<name>` is a single JSON document covering every package, and the heavy pass, JobReconciler and PodReconciler are now three controllers with three workqueues all writing it. Before the Job and Pod watches became their own controllers they rode the heavy pass's queue at `MaxConcurrentReconciles: 1` and could not interleave; splitting them removed that guarantee.

The heavy pass cannot simply write the value it computed. Its result is built from a snapshot taken at cluster-state build time, so a completion recorded by JobReconcile mid-pass would be reverted — and never re-recorded, because the Job is already marked `state-recorded` by then. The stage would only recover by being torn down and re-run.

Locking the write does not fix this: the value is computed long before the write, so a lock serializes a stale value into place. What closes it is applying **only the entries the pass actually changed** — derived by diffing the pass's starting snapshot against its result — on top of whatever the annotation holds at write time, under an optimistic lock with retry. Entries the pass never touched keep whatever another writer put there. The patch target is rebuilt from the freshly read node rather than the pass's own object, so a strategic-merge diff cannot emit deletions for keys another writer added since the snapshot.

**Pruner accounting safety.** The archive pruner deletes the middle attempts by normal deletion only, never force-delete: the Job-tracking finalizer is what guarantees a failure is counted and policy-classified before the pod is removed. The archives it keeps — the first and most recent genuine failures — exclude `DisruptionTarget` casualties so an ignored disruption can neither shadow a real failure nor suppress the deadline snapshot.

**Replacement-policy gate off.** `podReplacementPolicy` is beta and on by default across the supported range; if a cluster disables the gate, operator-initiated recreates stay safe (foreground deletion frees the name only after children are gone), and only Job-controller replacements can then briefly overlap a terminating predecessor — accepted for that non-default configuration, since the agent's flag files keep re-execution idempotent.

## Validation

Chainsaw and envtest assert fixed shapes; this section records the hand-run pass over the flows a
user actually drives, which #305 gated the merge to `main` on. Run against
`feature/package-as-jobs` on kind v1.36.1 (podman), three nodes, with the operator built from the
branch and run against the cluster — the same shape `make e2e-tests` uses, since the branch
published no image. Every case reset first (all CRs, Jobs, pods, ConfigMaps, and per-CR node
annotations/labels/taints/cordons) with a baseline assertion before starting, and captured evidence
mid-flight as well as at the end. Two full passes were run and agreed case for case.

**17 cases, 16 pass, 1 fail.**

| Case | Result |
| --- | --- |
| single package | pass — apply then config Jobs `Complete 1/1`, child pods retained and logs still readable, `state-recorded` set at completion |
| multiple packages + `dependsOn` | pass — one Job per (package, stage); the dependency completed before its dependents started |
| erroring, retries exhausted | pass — `failed=4` at `backoffLimit: 3`, `BackoffLimitExceeded`, exactly two archive pods (middle attempts pruned), no churn; editing the package cleared the terminal Job and the stage re-ran |
| stage timeout | pass — attempt killed at its own deadline, pod `Failed`/`DeadlineExceeded`, archive logs readable, per-attempt bound only |
| multiple CRs on one node | pass — no tick ever held unfinished Jobs from both CRs; separate `nodeState_` keys |
| interrupt grouping | pass — one merged interrupt Job for two packages, node cordoned during and uncordoned after, `skipped` sibling promoted to `complete` |
| explicit uninstall | **fail** — entry removed correctly, but the successful uninstall Job and its pod were deleted immediately, losing the logs (#443) |
| CR deletion with `uninstall.enabled` | pass — finalizer ran an uninstall Job during deletion; no `nodewright.nvidia.com/*` metadata left behind, node uncordoned |
| TTL by outcome | pass — succeeded collected first while the failed one remained, node state survived collection, sub-minute TTL rejected at startup |
| kubelet-refused attempts | pass — spent budget, never marked `erroring`, self-healed via sweep and recreate |
| pause / disable | pass — pause set `spec.suspend` and killed the running pod, resume started a fresh one; adding disable while clearing pause left the Job suspended |
| config update mid-flight | pass — ConfigMap not swapped mid-stage; the config stage re-ran afterwards with a new `resource-id` |
| two nodes + interruption budget | pass — never more than one node cordoned, one interrupt Job per node |
| node deleted mid-run | pass — orphaned-node Job foreground-deleted within seconds; the surviving node completed |
| disruption casualty | pass — evicted attempt spent no retry budget and never marked `erroring` |
| upgrade → downgrade | pass — upgrade removed the superseded entry; downgrade kept both, as `uninstall.md` intends |
| unpullable image | pass — bounded by `stageTimeout` rather than hanging indefinitely; the baseline #306 improves on |

**CLI.** `package logs`, `package status`, `node status` and `package rerun` were exercised inside
the cases above against a Jobs operator and needed no code change: child pods inherit the full label
set, so the CLI's label queries resolve identically, and post-completion `package logs` gets better
because the Job is retained. The version-dependent stop-strength of `pause` is recorded in
[`docs/user-guide/cli.md`](../user-guide/cli.md)'s compatibility matrix.

**Upgrade.** The hold is what makes the upgrade safe, and it was validated separately, on the rename
pass: an upgrade started mid-rollout held without taking over the node (requeuing roughly every 20s,
writing no `nodewright.nvidia.com/*` annotations) and released when the in-flight legacy `Skyhook`
was deleted. The same pass confirmed the unsupported case — upgrading with an interrupt executing
leaves a **cordoned, stalled node** rather than corrupting state or double-executing.

### Not covered

- **The hold and the Jobs executor were never exercised in one build.** The hold was validated on
  pre-Jobs `main` (rename, raw pods) and the cases above on `feature/package-as-jobs`.
  `migration_hold.go` was byte-identical across the two, so the composition is argued rather than
  observed.
- **Operator restart across a completion** — the exactly-once crash window in
  [Edge cases](#edge-cases-and-correctness-arguments); the gap #433 exists for and the one most worth
  covering next.
- **Scale** — two nodes, one package at a time. Nothing here speaks to fleet behavior (#382).
- **Webhook validation** — disabled under the local-run setup, so rejection paths were not exercised.
- **Real reboots** — the test image does not reboot, so interrupt was exercised structurally only.

## References

- [`operator/internal/controller/skyhook_controller.go`](../../operator/internal/controller/skyhook_controller.go) — reconcile loop, `createPodFromPackage`, `PodExists`/`HasRunningPackages`, `ValidateRunningPackages`, `HandleConfigUpdates`, `UpdatePauseStatus`
- [`operator/internal/controller/pod_controller.go`](../../operator/internal/controller/pod_controller.go) — completion detection, in-flight erroring/restart reporting
- [`operator/internal/controller/cluster_state_v2.go`](../../operator/internal/controller/cluster_state_v2.go) — node-state snapshot, metrics
- [`operator/internal/controller/annotations.go`](../../operator/internal/controller/annotations.go) — package annotation round-trip
- [`operator/api/nodewright/v1alpha1/nodewright_types.go`](../../operator/api/nodewright/v1alpha1/nodewright_types.go) — `Package.stageTimeout`, webhook validation
- [issue #223](https://github.com/NVIDIA/nodewright/issues/223), sub-issues #299–#305; issue #306 (ImagePullBackOff surfacing)
