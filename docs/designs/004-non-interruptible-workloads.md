# ADR-004: Non-interruptible workloads

## Status

**Proposed**, 2026-11-19 (#962). Answers #543. Merged as a proposal: each decision is accepted or overruled in its follow-up issue. Recorded as an ADR on 2026-11-24 and condensed from the [original proposal](https://github.com/NVIDIA/nodewright/blob/882b699d/docs/designs/non-interruptible-workloads.md), which has the full analysis of current behavior and of each option.

## Decision summary

A node whose pods match `spec.podNonInterruptLabels` is held until those pods finish, however long that takes. That is intended behavior, and this ADR adds no timeout or other way of interrupting a protected workload.

It decides the cost and visibility of the wait. On a first apply, nodes likely to be protected are picked last, so a long hold delays the last nodes instead of stopping the rollout. A pod can declare itself non-interruptible with an annotation that a NodeWright opts in to honoring. A hold gets a start time, two metrics and a clause in the `Ready` message. Cordon timing, the node Status vocabulary and `spec.drainConfig` do not change, and version bumps keep today's behavior.

## Context

- A **protected node** is a selected node with an interrupt package waiting on a drain and at least one `Running` or `Pending` pod matching `podNonInterruptLabels` (or, under decision 4, the opt-in annotation).
- A **held node** is a protected node picked into a batch, so the barrier has run on it. Most protected nodes are not held. Unrelated to the runtime-required hold.

Behavior on `main` (verified 2026-11-03 at `5f33c2ae`):

- `EnsureNodeIsReadyForInterrupt` in `operator/internal/controller/nodewright_controller.go` cordons the node, then waits on `HasNonInterruptWork` before draining, with no bound.
- A held node stays in `status.nodePriority`, so `GetNodesForNextBatch` in `operator/internal/wrapper/compartment.go` returns it as a sticky batch member and never reaches `createNewBatch`. With one node per batch, the compartment stops for as long as the workload runs.
- Neither node status (`waiting` or `in_progress`) nor `Ready` identifies a hold. The `Blocked` condition with reason `NonInterruptPodsRunning` lists protected nodes, picked or not, and is absent while another `Blocked` reason holds its slot. Nothing records when a hold began.
- On a version bump, `HandleVersionChange` marks every changed node `in_progress` before selection.
- The selector is written by an administrator guessing at other teams' labels, and matches pods in every namespace.

Details: [interrupt-flow.md](../architecture/interrupt-flow.md#what-a-held-node-costs-the-rollout). Goals: a hold does not stop unrelated nodes, is visible with its cause and age, and costs only the nodes actually held.

## Decision

1. **Cordon timing.** Keep cordoning when the node's turn comes, as fixed behavior. A deferring field can be added later as an optional enum without breaking existing NodeWrights.
2. **Rollout capacity.** On a first apply, a node likely to be protected is not picked while anything else is selectable, so it takes no batch slot. A node already in the batch in flight keeps its slot, and a held node's slot is never released. Follow-up 1.
3. **Selection order.** `createNewBatch` gains a second pass: nodes not likely protected are taken first, in today's status order; likely-protected nodes only if that leaves the batch empty, then up to the batch size. "Likely protected" means a matching pod and an interrupt in any package still to run on the node, runnable or not. Fixed, one change with decision 2. Follow-up 1.
4. **Who declares a pod non-interruptible.** Add the pod annotation `nodewright.nvidia.com/non-interruptible: "true"`, honored alongside the selector only when a NodeWright opts in through one optional spec field, off by default. The field's name is left to the follow-up. Follow-up 4.
5. **Reporting a long wait.** A `nodewright.nvidia.com/holdStart_<name>` node annotation, written level-triggered when the barrier first holds the node and cleared on every pass for a node not held, including nodes outside the selector. The `NonInterruptPodsRunning` message gains the oldest hold's start time, never an elapsed time. Two per-NodeWright `nodewright_` metrics over held nodes: their count and the oldest hold's age. `kubectl nodewright node` shows the hold. No `warnAfter` knob. Follow-ups 2 and 3.
6. **Health of a parked rollout.** Add no node Status value; name the held nodes in the `Ready` message whatever its reason, for example `1 held by non-interrupt pods (...)`. Follow-up 5.
7. **Preflight.** Keep the cordon, barrier and drain behind the single gate `EnsureNodeIsReadyForInterrupt`, so a preflight can short-circuit them. A node that needs no interrupt is never protected, and decision 3's ordering test must consult the preflight too.

## Non-goals

- A timeout that interrupts a protected workload, in any form.
- Releasing the slot of a node that becomes protected after it is picked, unless the remaining stall proves to matter.
- Changes to `spec.drainConfig`, PodDisruptionBudget handling and other drain blockers.
- A deferred-cordon knob, unless evidence shows the right answer varies by workload.
- Restricting the selector or the annotation by namespace, which needs its own issue.

## Alternatives

- **1, defer the cordon until the barrier clears:** a new protected workload can land on the schedulable node and restart the wait, so the rollout may never converge.
- **2, release a held node's slot:** the batch size no longer bounds cordoned nodes, half-applied nodes can pile up, and it would sit beside #936's slot brake.
- **3, exclude protected nodes outright:** with only protected nodes left, the rollout would never finish.
- **4, selector only:** leaves the call with an administrator guessing at other teams' labels, not with the people who know whether a job matters.
- **4, annotation always honored:** a NodeWright written before the annotation would change behavior when a workload starts carrying it.
- **5, a `warnAfter` field:** duplicates an alert on the age metric and adds a spec field that cannot be removed once the CRD freezes.
- **5, a separate condition type:** a second condition for a state `Blocked` already names; the fallback if the shared slot hides holds in practice.
- **5, per-node metrics:** a label that grows with the cluster.
- **5, a longer key such as `nonInterruptStart_`:** rejects NodeWright names that fit `nodeState_` today.
- **6, a new node Status:** a closed vocabulary that the CLI, the `node_status_count` metric and DeploymentPolicy logic all read.
- **6, reuse `blocked`:** it means the operator will not touch the node, and `createNewBatch` orders it after `waiting`, so selection would change. Flagged as the option most worth a maintainer's opinion.

## Consequences

Known gaps that remain, besides version bumps:

- A pod that lands after the pick keeps the node's slot and parks its batch.
- Protected nodes picked together because nothing else was left stay sticky, so a later node waits behind the longest. Follow-up 1 should accept this or reopen slot release.
- The barrier is not re-run at the interrupt stage, and a protected pod appearing between the barrier and the drain can be evicted. Follow-up 4 should state the guarantee as "pods present when the barrier last ran" or add re-checks.
- With the opt-in enabled, an annotated pod can hold a node on a NodeWright with no `podNonInterruptLabels`, which today protects nothing.
- An older CLI's reset does not strip `holdStart_<name>`, so a node still held after it over-reports its age.

Follow-up issues, to be filed once accepted and linked from #543:

1. Pick likely-protected nodes last (decisions 2, 3). Sequence with #721, #722 and #936; covers priority readiness, a batch-completion test per progressive strategy, a check against [resources at scale](../operations/resources-at-scale.md), and protected nodes under `safetyLimit`.
2. Record and report hold start (decision 5), including every place that names `drainStart_`.
3. Show a hold in the CLI, feature-detected against older operators (decision 5).
4. Honor the pod annotation on opt-in (decision 4), including the drain race.
5. Name held nodes in the `Ready` message (decision 6).

## References

- #543, the question; #962, the original proposal with the full option analysis.
- #936, #721, #722: the same batch and sticky-slot code.
- #867: the status churn decision 5 avoids by carrying no elapsed time.
