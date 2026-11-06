# Non-interruptible workloads design

This document decides how NodeWright should behave when a rollout meets a workload that must not be interrupted and may run for weeks, and what an administrator should be able to control. It is the design deliverable of [#543](https://github.com/NVIDIA/nodewright/issues/543). It changes no code.

**Status: proposed.** Every decision below is a proposal for maintainers to accept or overrule in review. A decision that is overruled changes the follow-up issues at the end, not the facts in [Current behavior](#current-behavior).

`spec.podNonInterruptLabels` holding a node until the workload finishes is intended behavior, and nothing here proposes a timeout that interrupts a protected workload.

## Terms

Two sets of nodes are easy to confuse, and the proposals below depend on the difference.

- A **protected node** is a selected node that has an interrupt package waiting on a drain and at least one `Running` or `Pending` pod that matches `podNonInterruptLabels`. It is the set `updateDrainBlockedCondition` lists today, whether or not the node has been picked for a batch.
- A **held node** is a protected node that has been picked into a batch, so the barrier has run on it. It is cordoned, it carries the Warning event on the Node, and under decision 5 it carries `holdStart_<name>`. Every held node is protected. Most protected nodes are not held.

In this document "held" always means held at this barrier. It never means the runtime-required hold described in [runtime-required.md](../user-guide/runtime-required.md), which uses the same word for an unrelated taint.

## TL;DR

| Question | Proposed decision | Fixed or knob |
| --- | --- | --- |
| 1. When is the node cordoned? | Keep cordoning when the node's turn comes. Do not defer until the barrier clears. | Fixed. A knob is possible later without breaking anything. |
| 2. Does a held node keep consuming rollout capacity? | On a first apply, a node that is likely to be protected when a batch is formed is not picked while anything else is selectable, so it takes no slot. A node that becomes protected after it is picked, a node on a version bump, and a protected node picked because nothing else was left keep their slots. | Fixed (a fix, not a knob) |
| 3. Should selection prefer unblocked nodes? | Yes. When a batch is formed, nodes that are likely to be protected are picked only if nothing else is selectable. | Fixed |
| 4. Who declares a pod non-interruptible? | Add a pod annotation that coexists with the selector. A NodeWright must opt in to honoring it. | Knob, off by default |
| 5. How is a long wait reported? | A `holdStart_<name>` node annotation, a stable start time in the condition, and two per-NodeWright metrics counted over held nodes. No `warnAfter` knob. | Fixed |
| 6. Should a parked rollout read as healthy? | Keep node status `waiting` and add no new Status value. Name the held nodes in the `Ready` message. | Fixed |
| 7. How does this interact with preflight? | The barrier and the cordon stay behind one gate that a preflight can short-circuit. A node that needs no interrupt is never protected. | Fixed |

Questions 2 and 3 are one change. On a first apply, because of them, the cost of a long hold falls from "the whole rollout stops" to "the last nodes wait". The cases it does not reach are listed under [What B does not fix](#2-should-a-waiting-node-keep-consuming-rollout-capacity).

## Current behavior

Verified on 2026-11-03 against `main` at `5f33c2ae`, on a three-worker kind cluster (Kubernetes 1.37.0). The cluster ran a NodeWright with `podNonInterruptLabels`, `interruptionBudget.count: 1` and one interrupt package, with a protected pod on one worker. That is a first apply with a single package. The behaviors marked "from the code" were read, not run.

- **The barrier runs before apply.** `ProcessInterrupt` calls `EnsureNodeIsReadyForInterrupt` for a package that has an interrupt at its `apply` (or `uninstall`) stage. No work has run for that package yet. `RunSkyhookPackages` visits a NodeWright's packages in a loop, so an earlier package may already have run on the node; the test used a single package.
- **The cordon comes first.** `EnsureNodeIsReadyForInterrupt` calls `Cordon()` before `HasNonInterruptWork`. The held node had `spec.unschedulable=true` and the `nodewright.nvidia.com/cordon_<name>` annotation on the first pass.
- **The wait has no bound.** The barrier returns before `DrainNode`, so `StartDrain` is never called and `spec.drainConfig.timeout` has no clock to measure. A matching pod that is `Pending` or `Running` holds the node.
- **A held node keeps its batch slot.** It was in `status.nodePriority`, so `Compartment.GetNodesForNextBatch` returned it every pass as a sticky member of the batch in flight and never reached `createNewBatch`. The other two nodes stayed `waiting` and no Jobs were created.
- **The node status depends on the path.** On a first apply `in_progress` is set in `DrainNode` (after `StartDrain`) and in package dispatch, both after the barrier, so the held node's `status_<name>` annotation read `waiting`. On a version bump, from the code, `HandleVersionChange` sets every node with a changed package to `in_progress` before the barrier and nothing moves it back, so a held node reads `in_progress`.
- **`Ready` follows the roll-up of all nodes.** It read `False` with reason `Waiting` and message `0/3 nodes complete, 3 waiting (...)` in the test. From the code, `CollectNodeStatus` returns `in_progress` when any node is in progress, so `Ready` reads `Progressing` next to a running batch peer or a running node in another compartment. After the pod was deleted, the held node went `in_progress`, its Jobs ran, and `Ready` became `Progressing`.
- **Some reporting exists, with limits.** A `Blocked` condition with reason `NonInterruptPodsRunning` lists the protected nodes, picked or not. With `count: 1` and protected pods on ten nodes it reads `10 nodes blocked` while one node is cordoned. A NodeWright has one `Blocked` condition and `updateDrainBlockedCondition` returns early while another reason, such as `DependencyUninstalled`, holds it, so a node can stay held without that condition being published. A Warning event with reason `Drain` is emitted on the Node whenever the barrier holds it, and on the NodeWright by the same condition updater that the early return skips.
- **No duration is recorded.** Nothing stores when the hold began. Two timestamps approximate it: the node's entry in `status.nodePriority` (when it was picked) and the `Blocked` condition's `lastTransitionTime`, which stays fixed while the condition is true and may predate a given node's hold.

Not tested on a cluster: an `interruptionBudget` above 1, more than one compartment, an upgrade, and a protected pod that appears after the node is already cordoned.

## Goals and non-goals

Goals:

1. A protected node does not stop unrelated nodes from rolling out.
2. An administrator can tell, from the cluster, that a node is held, by what, and for how long.
3. The cost of a hold is bounded to the nodes that are actually held.

Non-goals:

- A timeout, eviction, or any other way of interrupting a protected workload. The wait stays unbounded.
- A change to `spec.drainConfig`. It governs the drain that starts after the barrier clears.
- Implementing any of this. The work is cut into follow-up issues at the end.

## 1. When is the node cordoned?

**Today:** the first time the barrier is evaluated for the node, which is the first pass on which the node is selected.

**Options.**

- **A. Cordon when the node's turn comes (today).** A cordon stops the scheduler from placing new pods on the node, so for workloads the scheduler places, which is the usual case for training jobs, the wait is bounded by the pods already there. A pod that bypasses the scheduler, such as a DaemonSet pod or one with `spec.nodeName` set, can still land on a cordoned node and extend the wait. The cordon does not help for those, and neither would deferring it.
- **B. Defer the cordon until the barrier clears.** The node stays schedulable during the wait, which saves capacity. A new protected workload can land on it and restart the wait, so a cluster that keeps scheduling training jobs onto that node may never converge.

**Proposed: A, as fixed behavior.**

The assumption behind A is that the protected workloads are placed by the scheduler. If they are not, the selector or the pod annotation (decision 4) is still how they are declared, but the wait is no longer bounded by the pods already there, and the cordon is not what bounds it.

The capacity cost the issue describes is real, but it comes mostly from the whole rollout being held, not from the cordon itself. On a first apply, under decisions 2 and 3 a protected node is picked only once nothing else is selectable, so the cordon lands at the end of the rollout instead of at the start, and the cost shrinks to the final wait for the nodes that are still held. On a version bump decisions 2 and 3 do not apply (see below), so every protected node is still cordoned on the first pass there. B trades that for a starvation risk.

No knob yet, because nothing shows the right answer varies by workload. If it does, a field that defers the cordon is additive: an optional enum that defaults to today's behavior can be added later without breaking an existing NodeWright. This matters because the CRD surface is being frozen for v1, and an optional field is the cheaper kind of change to add afterwards than a wrong one is to remove.

## 2. Should a waiting node keep consuming rollout capacity?

**Today:** yes. A picked node stays in `status.nodePriority` until it completes, so a held node is a sticky member of its compartment's current batch. With nothing `in_progress`, `GetNodesForNextBatch` returns the sticky nodes and never starts a new batch. (#936, open, changes this for a node whose stage timed out: it leaves `nodePriority` once its failure is terminal, and `GetNodesForNextBatch` gains a `retryBatch` path. Neither touches a held node.)

**Options.**

- **A. Release a held node's slot** so the batch can admit others while it waits. The batch size no longer bounds the number of cordoned nodes, because each released slot lets another node be picked and cordoned. A guard is needed to stop half-applied nodes piling up, and it has to be based on whether any package has already run on the node, not on `wrapper.HoldsBatchSlot`, which is false for a node between packages. The release would also sit next to #936's gate, which keeps a node's slot as the only brake when a failed batch cannot stop the compartment.
- **B. Do not pick a protected node while anything else is selectable.** A node that is protected when it would be picked never takes a slot, so nothing is released and the budget keeps its meaning.

**Proposed: B, as fixed behavior.** This is a fix, not a knob. It removes the stall in the common case without changing what a slot means, and it leaves #936's brake alone.

**Mechanism.** It is the same change as decision 3, described there. The sticky set and the in-progress set are untouched: a node already in the batch in flight keeps its slot.

**What B does not fix.** Decision 3 acts only where `createNewBatch` forms a new batch, so a node that gets a slot some other way, or that is not recognized as protected at that moment, keeps today's behavior:

- **A version bump.** `HandleVersionChange` sets every node with a changed package `in_progress` before selection, and `GetNodesForNextBatch` returns in-progress nodes without reaching `createNewBatch`. Every protected node is therefore selected and cordoned on the first pass, which is the opposite of what decision 1 relies on. Version bumps are out of scope for this design. Covering them would need the pick gate to run before nodes are marked, which is a larger change and a separate issue.
- **A pod that lands after the pick.** The node keeps its slot and parks its batch. Releasing the slot is option A with its pile-up risk, so this design does not.
- **A protected node picked because nothing else was left.** It is sticky until it completes, and `GetNodesForNextBatch` returns the sticky nodes without reaching `createNewBatch`, so no new batch starts while it is held. With a budget of 2 and two protected nodes P1 (a three-week job) and P2 (a one-day job) picked together, P2 finishes and P1 stays sticky. A node added later, or one whose workload ended after the pick, waits behind P1 for three weeks, although it is not held. Decision 3 keeps this to the final batch, but not to zero. Follow-up 1 should either accept it or let a new batch start past a picked protected node, which is option A again.

A node whose interrupt package depends on a package without one is covered: the ordering test in decision 3 looks at every package still to run, so the node is recognized as likely protected before the first package runs.

If the remaining stalls matter in practice, the extension is option A with a cap on released nodes, and #936 should merge first.

**Costs and open points.**

- **Progress accounting.** A protected node is neither completed nor failed. Like an ignored or blocked node today, it counts in the compartment's total, so a compartment with enough protected nodes can fail to reach `safetyLimit` (see [Deployment Policy](../user-guide/deployment-policy.md)). The existing documentation of that limit should name protected nodes too.
- **Batch completion.** `Compartment.IsBatchComplete` reads "no node is `in_progress`", and `EvaluateCurrentBatch` returns early until it does. This design does not change that: a held node does not block batch completion, and it counts as neither completed nor failed, so it adds nothing to the deltas that `EvaluateCurrentBatch` and the progressive strategies (`linear`, `exponential`) read. The batch does not advance on its account, and its sticky membership keeps a new batch from starting. This was read from `EvaluateCurrentBatch`, not run, and I did not trace how each strategy grows its batch. The implementation issue should add a test for a held node with a checkpoint and each strategy.
- **Cost of the check.** Evaluating whether a node is protected for every candidate on every pass is a cached pod lookup per node, indexed by node name. `updateDrainBlockedCondition` already does this per node through `HasNonInterruptWork`. It should be measured against [resources at scale](../operations/resources-at-scale.md) before it ships.
- **A protected node as the last pick.** When only protected nodes are left, they are picked up to the batch size, so up to that many nodes are cordoned at once. That is the final wait for the nodes that exist now. It is not free of cost for others: see the third bullet under "What B does not fix".
- **Related fixes.** Issues #721 and #722 change the sticky cap in the same code, and #936 changes the batch and retry paths. This change should land with or after them.

**A workaround that exists today, from reading the code and not tested on a cluster.** `currentBatchNodes` skips a node that is not eligible, and `ignore=true` makes it ineligible. Labelling a held node `nodewright.nvidia.com/ignore=true` should therefore free its slot, including in the case B does not fix. The node stays cordoned. This is a manual override, not a design.

## 3. Should node selection prefer unblocked nodes?

**Proposed: yes, as fixed behavior, part of the same change as decision 2.**

`createNewBatch` picks by node status in a fixed order (`waiting`, `unknown`, `blocked`, `erroring`) and then by position. The change is a second pass: candidates that are not likely protected are taken first, in that order. Likely-protected nodes are taken only if that leaves the batch empty, and then up to the batch size. They are never used to fill room beside other nodes, because a picked protected node is sticky and would hold the batch.

**The ordering test is wider than the term "protected".** The set `updateDrainBlockedCondition` lists uses `nodeNeedsInterruptDrain`, which looks only at the packages `RunNext` offers now. For a package A with no interrupt and a package B with one that depends on A, that test is false until A completes, so a node running a training job would be picked in normal order, run A, and be cordoned at B's barrier. The ordering test therefore asks whether the node has a matching pod and any package still to run on it has an interrupt, whether or not that package is runnable yet. The Blocked condition and the definition of a held node keep the narrower test. The compartment needs this as one more predicate from the picker, next to the eligibility predicate it already takes.

When only protected nodes are left, they are picked. Excluding them outright would leave nothing to cordon and the rollout would never finish. The rollout is then waiting on protected workloads and nothing else, which is the intended outcome.

Ordering relative to #936's `retryBatch`: a node that failed and is due a retry is not protected by that fact, so it sorts ahead of protected nodes like any other candidate. The implementation issue should confirm that against #936 once it merges.

## 4. Who declares a pod non-interruptible?

**Today:** a cluster administrator writes a selector in the NodeWright and guesses at the labels of workloads owned by other teams. `HasNonInterruptWork` matches pods in every namespace.

**Options.**

- **A. Selector only (today).**
- **B. A well-known pod annotation, `nodewright.nvidia.com/non-interruptible: "true"`, that a workload sets on itself.** The people who know whether a job matters make the call.
- **C. The annotation, honored only when a NodeWright opts in.**

**Proposed: C.** The selector and the annotation both count, so existing NodeWrights are unchanged.

The opt-in is not a security boundary. A tenant that copies a NodeWright's `podNonInterruptLabels` onto its pod can already hold a node indefinitely, because the selector is not restricted by namespace, so the annotation grants nothing the selector does not. The opt-in is there for predictability: an administrator decides whether pods may declare themselves protected, and a NodeWright written before the annotation existed does not change behavior when a workload starts carrying it.

**Knob.** One optional field on the spec, off by default, whose exact name and shape are left to the implementation issue.

**Open point, for both the selector and the annotation.** Whether the protection should be restricted by namespace, so that a pod in an arbitrary namespace cannot hold a node. That is a separate decision from this one, because it changes the selector's existing behavior, and it should be taken in its own issue.

## 5. How is a long wait reported?

**Today:** a condition and events, with no start time and no metric.

**Proposed, all fixed behavior:**

- **A start timestamp.** A `nodewright.nvidia.com/holdStart_<name>` node annotation, written the first time the barrier holds the node, mirroring `drainStart_<name>`. It is level-triggered: a held node with no annotation gets one on the next pass, so an operator restart loses nothing.
- **It is cleared on every pass for a node that is not held**, not only inside the barrier, as `drainBlocked_` is cleared in `UpdateDrainBlockedCondition` (`cluster_state_v2.go`). The barrier does not run for a node whose next package version drops its interrupt, so clearing it there alone would leave a stale start time, and a later version that brings the interrupt back would reuse it and report weeks at once.
- **The key is `holdStart_<name>`, chosen for its length.** An annotation name segment is limited to 63 characters, and the NodeWright name is part of it. A longer prefix such as `nonInterruptStart_` (18) would leave 45 characters for the name, which is less than `drainBlocked_` (13, leaving 50) and `nodeState_` (10, leaving 53) leave today, and would reject names those keys accept. `holdStart_` is 10 characters, the same as `nodeState_`, so it adds no new failure case. I did not find a cap on the NodeWright name, so any name that fits `nodeState_` today fits this key. "Hold" here means held at this barrier, as defined in Terms.
- **The places that name `drainStart_` need the new key.** Reset in `cmd/cli/app/reset.go` and `cmd/cli/app/node/node_reset.go`, `rebootResetAlreadyApplied` in `nodewright_controller.go`, and the status display in `cmd/cli/app/node/node_status.go`. An older CLI's reset will not strip the new key. The per-pass clearing removes it once the node is seen not held, but a node that is still held straight after such a reset keeps the old start time, so its age is over-reported until the hold ends. That is a known limit for older CLIs.
- **The condition carries a stable value, not a duration, and is best-effort.** The `Blocked`/`NonInterruptPodsRunning` message gains the oldest hold's start time, and omits it while no node is held (the condition lists protected nodes, so it can be present with no hold). It must not carry an elapsed time. A message that changes every pass writes NodeWright status on every pass, which is the same churn [#867](https://github.com/NVIDIA/nodewright/pull/867) had to remove from `ImagePullFailed`.
- **Two metrics per NodeWright, not per node:** the number of held nodes and the age in seconds of the oldest hold. Both are counted over held nodes, the ones that carry the annotation, not over protected nodes. A per-node series would add a label that grows with the cluster. They are new `nodewright_` metrics only, with no `skyhook_` twin, since the legacy set is being removed (see [metrics](../observability/metrics.md)).
- **CLI.** `kubectl nodewright node` output should show the hold and its start time, read from the annotation. Against an older operator that does not write it, the CLI shows nothing and does not fail, per the CLI compatibility rules.

**Which signal to rely on.** The `Blocked` condition shares one slot with other reasons, so it can be absent while a node is held, and it lists protected nodes, which are more than the held ones (see [Current behavior](#current-behavior)). The node annotation and the two metrics do not depend on that slot and describe held nodes, so they are the signals to alert on. The condition stays as it is, with its existing precedence. A separate condition type, as `DrainBlocked` is separate from `Blocked`, would remove the limit, but it would add a second condition for a state the first already names and duplicate a surface users already read. It is the alternative if the precedence turns out to hide holds in practice.

**No `warnAfter` knob.** A threshold that reports without ever interrupting is expressible as an alert on the age metric. A field would duplicate a Prometheus rule and add a spec field that cannot be removed once the CRD freezes.

## 6. Should a parked rollout read as healthy?

**Today:** on a first apply with nothing else running, `Ready` is `False` with reason `Waiting`, the same as a rollout waiting for another NodeWright's priority. Otherwise it can read `Progressing` with a node held. The `Blocked` condition tells a hold apart unless another `Blocked` reason holds its single slot.

**Options.**

- **A. A new node Status for a held node.** Rejected. Status is a closed vocabulary that the CLI, the `node_status_count` metric and the DeploymentPolicy logic all read, so a new value spreads further than the problem warrants.
- **B. Reuse `blocked`.** `blocked` currently means the operator will not touch the node (ignored or untolerated taint), and `createNewBatch` orders `Blocked` after `Waiting`. Reusing it for a hold would change selection as a side effect. This is the option most worth a maintainer's opinion.
- **C. Keep the node status as it is and name the held nodes in the `Ready` message.** For example `0/3 nodes complete, 2 waiting (...), 1 held by non-interrupt pods (...)`.

**Proposed: C.** It changes no vocabulary, and the message is stable between passes because it lists nodes and not durations. The clause is added whatever the reason reads, so it works when `Ready` says `Progressing`. A hold is identifiable through the message, which people read and which does not depend on the `Blocked` slot, and through the annotation and metrics of decision 5, which machines should read. B is the alternative if maintainers want the node status to carry it.

## 7. How does this interact with preflight?

The issue describes preflight and dynamic cordon/drain work that would decide whether an interrupt is needed before anything is cordoned. That work is not described anywhere in this repository, so this section is written against the description in #543 and not against a specification.

**Proposed:** keep the cordon, the barrier and the drain behind the single gate they sit behind today, `EnsureNodeIsReadyForInterrupt`, so a preflight can short-circuit the whole sequence for a package that does not need to interrupt. A node that needs no interrupt must never be protected, however many protected pods it runs. The definition of a protected node already requires this, because it asks `nodeNeedsInterruptDrain` first. Whatever a preflight adds to that question applies to protection without a second change. The ordering test in decision 3 runs at pick time, before the cordon, so a preflight that decides a node needs no interrupt has to be consulted by that test as well, which keeps the node neither deprioritized nor cordoned. Where the preflight itself runs is left to its own design.

The decision-1 default (cordon when the node's turn comes) is also what a preflight wants: a node that is skipped is never cordoned at all.

## Follow-up issues

To be filed once these decisions are accepted. They are not filed yet.

1. **Pick likely-protected nodes last, and only when nothing else is left** (decisions 2 and 3). The change that matters most. Sequence it with #721 and #722, and with #936, which changes the batch and retry paths it touches. This design never releases a held node's slot, so #936's `CanStopOnFailure` brake is untouched. Includes the batch-completion check above and the documentation of protected nodes under `safetyLimit`.
2. **Record when a hold began and report it** (decision 5): the `holdStart_<name>` annotation with its per-pass clearing, the stable start time in the condition, and the two metrics, and the CLI and operator places that enumerate `drainStart_`.
3. **Show a hold in the CLI** (decision 5). Feature-detected against older operators.
4. **Honor a pod annotation when a NodeWright opts in** (decision 4). Needs the field's name settled first. The namespace question is its own issue.
5. **Name held nodes in the `Ready` message** (decision 6). Small, and independent of the others.

## Out of scope

- A timeout that interrupts a protected workload, in any form.
- Releasing the slot of a node that becomes protected after it is picked (decision 2, option A), unless the remaining stall proves to matter.
- Changes to `spec.drainConfig`, PodDisruptionBudget handling and the other drain blockers, which the `DrainBlocked` condition reports.
- A deferred-cordon knob (decision 1), unless evidence shows the right answer varies by workload.
