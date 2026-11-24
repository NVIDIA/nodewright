# ADR-003: Package execution as Jobs

## Status

**Accepted**, 2026-07-10. Answers #223; implemented through #299 to #305. The design was kept current with the code, most recently in #1070, until it was recorded as an ADR on 2026-11-24 and condensed from the [original design](https://github.com/NVIDIA/nodewright/blob/882b699d/docs/designs/2026-07-10-package-execution-as-jobs.md), which has the Job shape, labels, edge cases and validation plan.

## Decision summary

Each package stage on each node runs as one `batch/v1` Job, replacing the raw Pods the operator created and deleted by hand. The Job controller owns the pod lifecycle and retries; `kubectl logs job/<name>` works during and after a run, and failure logs outlive success logs.

The lifecycle state machine does not change: stages, Status and State derivation, interrupt, cordon and drain sequencing, DeploymentPolicy, and the agent's env. Node state stays the source of truth; Jobs are executors. Three enhancements ride along: a per-attempt stage deadline (the one CRD change, an additive per-package `stageTimeout`), pause that stops a running stage, and a full-log archive of failed attempts.

Out of scope: cordon and drain ownership, DeploymentPolicy semantics, multi-node Job parallelism, and fast ImagePullBackOff surfacing, which stays with #306.

## Context

Package pods never terminated: work ran in `initContainers` and a forever-running `pause` main container made the Running pod the "stage in flight" marker (`PodExists` in `operator/internal/controller/nodewright_controller.go`). The operator detected completion from init-container statuses (`operator/internal/controller/pod_controller.go`), updated node state, then **deleted the pod**, which was also the processed-once marker. Two problems followed:

- **Debugging meant racing the operator.** A completed stage's logs vanished the moment the operator processed it.
- **The lifecycle was hand-rolled.** Cleanup, completion tracking, in-flight checks and stale-spec invalidation were custom code, duplicating a primitive Kubernetes already has and missing the tooling that treats Jobs as completable work.

Retries were kubelet in-place restarts under `restartPolicy: OnFailure`: a failing stage crash-looped forever, and a hung stage sat `in_progress` forever. Pause only blocked new scheduling, so the Emergency Stop could not stop a running stage.

## Decision

1. **One Job per (NodeWright, package, version, stage, node).** Interrupt Jobs are one per (NodeWright, stage, interrupt type, node), deduping the merged interrupt. Child pods inherit the existing labels.
2. **An exit-0 main container replaces `pause`.** A Job completes only when its pod reaches `Succeeded`. It runs on the package image, which init-copy already requires to have `/bin/sh`. The init-container chain stays.
3. **Package Jobs use `restartPolicy: Never` with a finite `backoffLimit`** (`JOB_BACKOFF_LIMIT`, default 3). Each attempt is a fresh pod and the retry loop stays in the Job controller. `podReplacementPolicy: Failed` keeps attempts from overlapping on the shared hostPath mounts.
4. **Disruptions stay silent and `erroring` means a genuine step failure.** A `podFailurePolicy` ignores `DisruptionTarget`, so eviction, preemption and PodGC spend no attempt and write no state. A Failed pod marks `erroring` only with a real verdict (nonzero exit or its own deadline) and no `DeletionTimestamp`, so pause and deletions stay silent. A Job that spends its budget only on kubelet admission rejections is swept and the stage re-runs.
5. **Every package step must be safe to run again.** A stage whose pod is lost to disruption, suspension or deletion is re-run; the agent's host flag files skip steps already done and re-run the interrupted one, which packages must tolerate. The flag files make re-execution idempotent; they are not a lock.
6. **Completion is recorded once, through a Job annotation.** Completed pods now linger, so deletion cannot be the marker. The operator writes node state, then sets `nodewright.nvidia.com/state-recorded` on the Job; after a crash between the two, a postcondition check marks the Job without re-applying the transition. Neither the Job path nor the Pod watch may create a node-state entry, so a lingering Job or pod cannot undo a reset, rerun or uninstall.
7. **The first and most recent genuine failed attempts are kept as archives.** The operator prunes the rest, so the likely root cause and the latest failure stay queryable while the stage retries and after it times out.
8. **A stage deadline bounds each attempt.** The bound is the pod template's `activeDeadlineSeconds`, from the new per-package `stageTimeout` (default `JOB_STAGE_TIMEOUT`, 1h; `0` removes the time bound). An expired attempt is an ordinary failed attempt. A spent budget times the stage out: the operator records `erroring` and the Failed Job stays as the marker until a rerun, reset, config update, spec change or failure-TTL expiry. Package Jobs carry no Job-level deadline. Bounds are fixed when the Job is created.
9. **Interrupt Jobs keep `OnFailure`, an unbounded `backoffLimit`, and a Job-level deadline.** A reboot interrupt kills its own pod, and in-place restart after the node returns is the recovery; it is bounded by its deadline alone. A non-reboot interrupt is bounded from the Pod watch: at its restart allowance the operator sets the Job's `backoffLimit` to 0.
10. **Pause suspends Jobs.** The pause annotation stays the user-facing primitive; the operator sets `spec.suspend` on the NodeWright's unfinished Jobs and clears it on resume, after validation. Suspension kills the running pod, restarts its attempt clock, and spends no retry budget.
11. **TTL is set at completion, by outcome.** Jobs are created without `ttlSecondsAfterFinished`; the update that writes `state-recorded` sets it (`JOB_TTL_SUCCEEDED` 1h, `JOB_TTL_FAILED` 24h), so failure logs outlive success logs.
12. **Names are deterministic and the name is the claim.** Creation is idempotent. A rerun predicate frees the name: a processed finished Job is deleted once node state no longer records its stage as done, unless it is a timed-out Job. Validation and the in-flight gates consider unfinished Jobs only, and Jobs on a deleted node or a node reset by `REAPPLY_ON_REBOOT` are swept regardless of status.

## Alternatives

- **`podFailurePolicy` for ImagePullBackOff:** an unpullable image never starts a container, so no rule can match; #306 owns fast surfacing.
- **One long-lived Job, or a JobSet, per rollout:** JobSet targets parallel work, not a sequential per-stage lifecycle.
- **Static TTL at creation:** one value cannot keep failure logs long without keeping success pods too long.
- **`erroring` on every Failed Job:** would count an invisible, self-healing disruption against DeploymentPolicy.
- **`backoffLimit: 0` with operator-driven recreation:** broken under `OnFailure`; under `Never` it moves the retry loop back into the operator.
- **A Job-level deadline as the only stage bound (earlier draft):** terminal on first expiry, so a timeout was the one failure with no retry, and it deleted the pod and its logs.
- **A Job-level deadline alongside the per-attempt one (later draft):** two clocks over the same work can only disagree; the one case it covered, a pod the kubelet never acknowledges, is left unbounded as node health.
- **Random Job name suffixes:** break creation idempotency and turn "does this stage run" into list-and-filter.
- **Pause that only blocks new scheduling (earlier draft):** the draft rejected `suspend` because a resumed stage re-runs without checkpointing; that cost was accepted, since a pause that cannot stop anything is not an emergency stop.
- **Keying completion dedup by Job UID in the node-state entry:** older CLIs rewrite the whole entry, dropping fields they do not know.
- **Locking the node-state write:** serializes a stale value into place; the heavy pass instead applies only the entries it changed, under an optimistic lock.
- **A pruner keeping any Failed, non-disrupted pod (earlier draft):** could leave only admission rejections and let the classifier clear a genuinely failing stage.
- **A `nodewright.nvidia.com/last-logs` annotation snapshotting failure logs:** removed in #479; it never fired for package Jobs, whose archive pod already holds the logs.
- **Dual-path upgrade accommodations (earlier draft):** let Jobs and raw pods coexist for a minor; superseded by the migration hold, below.
- **Counting legacy raw pods in the in-flight gate:** one narrow, documented, opt-in sequence did not justify legacy-pod awareness in the gate.

### `restartPolicy: OnFailure` for package Jobs (earlier draft)

Two earlier revisions kept `OnFailure` for parity with kubelet crash loops. The stage deadline removed its one advantage, retry forever in place, and its costs remained: each restart destroys the previous attempt's logs, backoff accounting sums restarts, `podFailurePolicy` is forbidden, and no archive is possible. `Never` was adopted, accepting per-attempt init-copy re-runs, slower pacing, and one hard-node-crash edge. Interrupt Jobs keep `OnFailure` for reboot survival.

## Consequences

- A hung or crash-looping stage surfaces as `erroring` and counts toward DeploymentPolicy thresholds.
- A crash-looping package times out after `backoffLimit + 1` attempts, about 70 seconds at the default, losing self-healing for transient faults; hence `JOB_BACKOFF_LIMIT` is a knob.
- A pod the kubelet never acknowledges is unbounded at any `stageTimeout`. A hard node crash can record one archived attempt and briefly show `erroring`.
- Raising `stageTimeout` or the operator knobs does not reach a running Job; the user clears it with `package rerun` or by deleting the Job.
- Retained Jobs and pods add a Jobs cache and up to `nodes × packages × stages` succeeded pods at peak.
- A successful uninstall's Job is deleted at once, so its logs are lost (#443); starting an uninstall also clears the package's retained apply and config Jobs. An interrupt Job ended at its Job-level deadline or restart limit keeps no log tail.
- The operator now depends on recent Job features (`podFailurePolicy`, `podReplacementPolicy`, suspend, TTL-after-finished); older clusters lose these silently.
- **Upgrade and compatibility.** No migration shim. This ships with the `skyhook.nvidia.com` to `nodewright.nvidia.com` rename, so `legacyMigrationHold` keeps Jobs and legacy raw pods from overlapping, which relies on the two shipping together; legacy pods are never converted. Unpausing or enabling a legacy `Skyhook` that still has a live raw pod is unsupported rather than guarded. The CLI needs no change; pause's stop strength is version-dependent.

## References

- #223; sub-issues #299 to #305; #306 (ImagePullBackOff); #443 (successful uninstall logs); #479 (`last-logs` removal); #788 (config-update deletion of config-stage Jobs)
- `operator/internal/controller/job_builder.go`, `operator/internal/controller/job_controller.go`, `operator/internal/controller/pod_controller.go`, `operator/internal/controller/nodewright_controller.go`, `operator/internal/controller/migration_hold.go`, `operator/api/nodewright/v1alpha1/nodewright_types.go`
- Current behavior: [lifecycle](../architecture/lifecycle.md), [operator status](../architecture/operator-status.md), [`stageTimeout`](../user-guide/custom-resource.md#stagetimeout), [Emergency Stop](../user-guide/cli.md#emergency-stop), [resources at scale](../operations/resources-at-scale.md), [Kubernetes support](../operations/kubernetes-support.md), [migration](../getting-started/migration.md)
