# ADR-007: Ask whether a node needs the interrupt before cordoning it

## Status

**Proposed**, 2026-11-24. Answers #981, which has the full proposal, without its CRD field, `request` helper or late escalation.

## Decision summary

A package can opt in to a `needs-interrupt` stage: before a first apply or an upgrade, NodeWright asks the package on each node what this change needs there, and the package answers `live` (run the stages with no cordon) or an interrupt no more disruptive than the declared one, `noop` meaning cordon and drain only. The stage is read-only: its steps must not change the host, and the agent records nothing for it. An error, or no answer, takes today's path, and an answer above the declared interrupt stops the package before NodeWright cordons the node, so the stage never makes a rollout more disruptive than it is now.

The declared interrupt, the package's default or the CR's override of it, becomes the most a change can do, not what it will do: the limit an admin sets, and raises to consent to more. A package that needs a custom disruptive action runs it in its own steps and answers `noop`, so the node is drained around it; package-supplied interrupt scripts are not planned. A dry run, which reports what a rollout would do, is a separate concept and not decided here.

Until the operator can see which packages opt in, it runs the stage's Job for every package with an interrupt: one short Job per node per change, on an image the node pulls anyway, which is cheap next to a drain it can save.

## Context

A package's interrupt is fixed per package, so every change cordons, drains and interrupts every node it selects, whether or not the node needs it. The operator already decides per node for a config change: `NodeState.HasInterrupt` interrupts only when a changed key matches `configInterrupts` or the package still owes its interrupt (#887), and otherwise `NextStage` takes the path without an interrupt, with no cordon or drain. A first apply or an upgrade has no such decision. In `nvidia-setup`, a release that changes only the OKE path reboots every EKS node whose running kernel already matches, and on `aks-h100` every bump restarts containerd and kubelet even when nothing changed (#981).

A package's `-check` modes describe its target state, but they are optional and today run only straight after their stage, so nothing says they are safe or meaningful before it. A check that passes says the work is already done, so no interrupt is needed; a failing one says there is work, not whether that work needs the interrupt.

## Decision

1. **The package opts in, in `config.json` schema v2** ([ADR-006](006-package-contract-v1.md)). It either defines a `needs-interrupt` mode, whose steps answer the question directly, or declares that its check modes can run before their stages, which makes the checks the default answer. A package that does not opt in always takes today's path.

2. **The answer is `live` or an interrupt the declared one covers.** A `needs-interrupt` mode reports `live` or an interrupt in the CR's shape, with `noop` for drain only. The declared interrupt covers an answer that ranks at or below it, in the order interrupts are merged (`reboot`, `restartAllServices`, `service`, `noop`), with a `service` answer's services a subset of the declared ones. With the checks as the default, the answer is `live` only when every stage on the path that has steps also has its check mode, every one passes, and the running state is covered by `post-interrupt-check`, since a kernel installed but not yet booted passes `apply-check`; anything else is the declared interrupt.

3. **The operator follows the answer.** The declared interrupt is the one in effect: the package's default, or the CR's override, which wins ([ADR-006](006-package-contract-v1.md) decision 3). For a first apply or an upgrade of a package with an interrupt in effect, the operator runs the stage before cordoning the node; with none in effect the answer can only be `live`, so the stage is not run; config changes keep `configInterrupts`, and uninstall is never asked. The node is cordoned and drained if any of its packages answers an interrupt, and the answers are merged as declared interrupts are today; no opt-in, an error or no answer counts as the declared interrupt. An interrupt the version being replaced still owes (#887) carries over to the new version and counts as the declared interrupt whatever the answer; the new version's own debt is set from its answer, not when its apply or upgrade starts. The operator checks each answer against the declared interrupt, with one function next to the merge ranking so the two never disagree, and an answer the declared interrupt does not cover means the declaration is misconfigured for this version: the package goes `erroring` before the node is cordoned, naming both, and NodeWright does nothing else to the node. This extends the per-node decision config changes already have, through the same `HasInterrupt` and `NextStage` paths, behind the single gate [ADR-004](004-non-interruptible-workloads.md) decision 7 keeps for it.

4. **`needs-interrupt` is a read-only stage with a stored answer.** Its steps, and the checks it runs, must not change the host, and the agent records no completion flags or history for it, so a check run before its stage leaves the version an upgrade reads as previous unchanged. The operator writes the package's Stage as `needs-interrupt`, `in_progress`, before it creates the Job, so it joins the stage vocabulary and ADR-005's `lifecycle` package. The operator stores the answer in node state, since the pod is gone once its Job's TTL expires, tied to the version and spec it answered for and fixed for the rest of that run, with an event the CLI can show. It is cleared when that run ends or the package is uninstalled, so a reinstall asks again. ADR-004's ordering test uses only the current run's answer, and with none treats the node as needing its interrupt.

5. **The answer travels in the Job's termination message.** Only the kubelet can set it, from a file the container writes, so the package pod needs no API credentials; Tekton returns task results the same way. The agent writes it after the last step, with `terminationMessagePolicy: File` and never a fallback to logs, in the versioned envelope #871 defines for its preflight and with the operator's request ID echoed; the operator treats it as untrusted input and ignores a stale one.

6. **Off Kubernetes it is an `agent run` intent.** `agent run` with the `needs-interrupt` intent returns the same answer and records nothing a later run reads ([ADR-005](005-agent-owned-package-lifecycle.md)).

## Non-goals

- Package-supplied interrupt scripts. A custom action runs in the package's own steps under a `noop` answer, which drains the node first; a script would add only one run per node after all packages, which no package needs yet.
- A step asking for more interrupt partway through a run.
- A dry run reporting what a rollout would do, which is a different question from whether one node needs its interrupt.
- Drift detection, which can later run the same checks against the version already applied.
- Config changes and uninstall, which keep today's behavior.

## Alternatives

- **Ask every package by default with its existing checks.** Checks are optional and were never written to run before their stage, so a node could skip an interrupt its work needed.
- **#981's `conditional` field and `request` helper.** The opt-in lives in the package instead, so the CRD does not change, and a `needs-interrupt` mode reports its answer directly.
- **No limit on the answer.** An image bump could start rebooting nodes under an unchanged CR, and the CR would no longer state the worst case.
- **Run the declared interrupt when the answer is above it.** Leaves the node half-applied after a drain, as today.
- **Skip at interrupt time.** Saves the reboot but not the drain, since the node is already cordoned.
- **A semver rule on the CR**, such as patch bumps skipping the interrupt. Applies fleet-wide, so it cannot tell an EKS node from an OKE one.
- **Run package code in #871's preflight.** Breaks that stage's guarantee of running no package code.
- **An HTTP endpoint on the operator.** Needs a listener every node can reach and per-pod authentication, and loses an answer sent while the operator restarts.
- **Results read from a sidecar's logs**, as Tekton does for results over 4 KiB ([TEP-0127](https://github.com/tektoncd/community/blob/main/teps/0127-larger-results-via-sidecar-logs.md)). Needs an extra container per pod and cluster-wide `pods/log` access for the operator, and loses results whose logs are collected first; worth it only for large results.
- **The agent writing the answer to the API**, as a node annotation or a CR. Gives a privileged host pod API write access, widening what a compromised package can do; a dedicated CR is worth it only if answers outgrow the termination message's 4 KiB.
- **Gate the Job on a CR field.** Saves the Job for packages that never opt in, at the cost of a CRD change and a second place to declare what the package already says.

## Consequences

- Nodes that answer `live` are not drained or interrupted, which also keeps them clear of the non-interruptible workload barrier ([ADR-004](004-non-interruptible-workloads.md)).
- One short Job per node per change for every package with an interrupt, opted in or not, until the operator can read the opt-in without running the package (#819).
- An answer below the declared interrupt trusts the package: a `needs-interrupt` mode that is wrong runs work live that needed a drain, or leaves it unactivated until the next interrupt. The checks default only ever answers `live` or the declared interrupt, erring towards the interrupt.
- An answer above the declared interrupt stops the package on that node before NodeWright cordons it or runs a stage, which counts against the DeploymentPolicy's `failureThreshold`, so a CR that under-declares shows up in the first batch; raising the declared interrupt is how an admin consents.
- It covers what bring-your-own interrupts were for: [ADR-006](006-package-contract-v1.md) lets the package declare its interrupt, this stage lets each node choose within it before cordon over a channel that needs no API credentials, and custom actions run in the package's own steps under `noop`.

## Open questions

1. Whether a NodeWright can opt a package in to the stage or out of it.
2. One Job, or one per check mode.

## References

- #981, the proposal; #871, the artifact preflight and the termination-message channel; #819; #543; #887.
- Kubernetes, [Determine the Reason for Pod Failure](https://kubernetes.io/docs/tasks/debug/debug-application/determine-reason-pod-failure/): termination messages, `terminationMessagePolicy` and the 4096-byte limit.
- Tekton, [TEP-0127: Larger results via sidecar logs](https://github.com/tektoncd/community/blob/main/teps/0127-larger-results-via-sidecar-logs.md): results through termination messages by default, and the sidecar alternative for larger ones.
- [ADR-004](004-non-interruptible-workloads.md), [ADR-005](005-agent-owned-package-lifecycle.md), [ADR-006](006-package-contract-v1.md).
- `ROADMAP.md`: preflight stage and dynamic cordon/drain, package-declared interrupts, host drift detection.
