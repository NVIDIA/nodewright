# Non-interruptible workloads

This document records the behavior and design decisions for a NodeWright rollout
that encounters a workload selected by `spec.podNonInterruptLabels`. It is
deliberately a design document, not a request to add new CRD fields in v1.

## Current behavior

The selector is a pre-drain barrier. A matching Pending or Running pod must
finish or leave the node before the operator starts the configured drain. The
wait is intentionally unbounded: `spec.drainConfig.timeout` does not apply,
because the drain clock has not started yet.

The operator cordons the node before checking the barrier. The cordon is
persisted on the first pass and remains in effect for the entire wait. The
operator does not evict or delete the matching pod. A node that is waiting is
reported through the `Blocked` condition with reason
`NonInterruptPodsRunning`, the affected nodes are included in the condition
message, and Warning events are emitted on the NodeWright and Node objects.

The waiting node remains in `status.nodePriority`, so it retains its active
batch position. With the default single compartment, that sticky membership
prevents a new batch from being selected until the node finishes. The rollout
therefore reports its normal progressing/blocked state rather than a distinct
"waiting for workload" phase. There is no duration, warning threshold, or
capacity override for this wait today.

## Decisions for v1 and the follow-up work

| Question | Decision | Rationale | Surface |
| --- | --- | --- | --- |
| When is the node cordoned? | Keep the current early cordon. | Cordon before the check prevents a replacement workload from arriving and restarting the wait indefinitely. The capacity cost is real, but changing timing without preflight would create a starvation race. | Fixed behavior for v1. Revisit with preflight work. |
| Does a waiting node consume rollout capacity? | No. A node blocked by a protected workload should leave the active batch slot while retaining resumable package state. | Other eligible nodes should make progress instead of waiting for an unrelated training job. The node can rejoin when the barrier clears. | Follow-up controller change; no new spec knob. |
| Does node selection prefer unblocked nodes? | Yes. Selection should skip nodes currently held by the barrier when another eligible node is available. | This is the operational counterpart to releasing the batch slot and avoids choosing a known blocker first. | Follow-up controller behavior. |
| Who declares a pod non-interruptible? | Keep the administrator-owned selector and add workload opt-in as a compatible follow-up. | Cluster administrators can protect an existing fleet today. A well-known pod annotation would let workload owners express intent without requiring a selector that knows another team's labels. | Selector remains fixed in v1; opt-in annotation is a follow-up API decision. |
| How is a long wait reported? | Add a start timestamp, duration metric, and a distinct condition reason; support a warning threshold that reports but never interrupts. | The current condition identifies the cause but not its age or operational cost. Reporting must not silently turn a protected workload into an interruptible one. | Follow-up status/metrics work; threshold is a future spec knob. |
| Is a parked rollout healthy? | Report it as deliberately waiting, distinct from an unexplained progressing stall, while keeping the NodeWright Ready condition false until the rollout can complete. | Operators need to distinguish an intentional protection from a controller failure without declaring incomplete work ready. | Follow-up condition/reason design. |
| How does this interact with preflight? | Preflight should decide whether an interrupt is needed before cordon and drain. If no interrupt is required, the non-interruptible barrier is not entered. | This removes unnecessary capacity loss and makes the early-cordon trade-off smaller. The current design remains the compatibility behavior until preflight lands. | Follow-up sequencing change. |

## Implementation slices

The decisions above should be implemented as separate, reviewable changes:

1. Release a protected node's batch slot and make selection prefer unblocked
   nodes, while preserving its package state and resumable priority entry.
2. Add a non-interruptible wait timestamp, duration metric, condition reason,
   and NodeWright/Node event transitions.
3. Define and document the workload opt-in annotation and its precedence with
   `podNonInterruptLabels`.
4. Add preflight interrupt determination and reconcile it with the cordon,
   batch, and status changes above.

Each slice should include focused controller tests. No timeout should be added
to the current barrier as a substitute: a timeout would violate the guarantee
that a protected workload is never interrupted by this feature.
