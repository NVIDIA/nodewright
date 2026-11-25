# ADR-007: Ask whether a node needs the interrupt before cordoning it

## Status

**Proposed**, 2026-11-24. Answers #981 for whether a package's declared interrupt runs; #981 has the full proposal. Its per-node interrupt levels are dynamic interrupts, a different decision for a different ADR.

## Decision summary

A package can opt in to a `needs-interrupt` stage: before a first apply or an upgrade, NodeWright asks the package on each node how much of the declared path this change needs there, and the package answers `live` (run the stages with no cordon), `drain` (cordon and drain, but no interrupt) or `interrupt` (today's path). An error, or no answer, is `interrupt`, so the stage never makes a rollout more disruptive than it is now.

The declared interrupt becomes the most a change can do, not what it will do. The answer decides how much of it runs, never which interrupt: interrupts stay as declared. Letting the package choose per node which interrupt its work needs is dynamic interrupts and gets its own ADR. A dry run, which reports what a rollout would do, is a separate concept and not decided here.

Until the operator can see which packages opt in, it runs the stage's Job for every package with an interrupt: one short Job per node per change, on an image the node pulls anyway, which is cheap next to a drain it can save.

## Context

A package's interrupt is fixed per package, so every change cordons, drains and interrupts every node it selects, whether or not the node needs it. The operator already decides per node for a config change: `NodeState.HasInterrupt` interrupts only when a changed key matches `configInterrupts` or the package still owes its interrupt (#887), and otherwise `NextStage` takes the path without an interrupt, with no cordon or drain. A first apply or an upgrade has no such decision. In `nvidia-setup`, a release that changes only the OKE path reboots every EKS node whose running kernel already matches, and on `aks-h100` every bump restarts containerd and kubelet even when nothing changed (#981).

A package's `-check` modes describe its target state, but they are optional and today run only straight after their stage, so nothing says they are safe or meaningful before it. A check that passes says the work is already done, so no interrupt is needed; a failing one says there is work, not whether that work needs the interrupt.

## Decision

1. **The package opts in, in `config.json` schema v2** ([ADR-006](006-package-contract-v1.md)). It either defines a `needs-interrupt` mode, whose steps answer the question directly, or declares that its check modes can run before their stages, which makes the checks the default answer. A package that does not opt in always takes today's path.

2. **The answer is one of `live`, `drain` or `interrupt`.** A `needs-interrupt` mode reports one of the three. With the checks as the default, the answer is `live` only when every stage on the path that has steps also has its check mode, every one passes, and the running state is covered by `post-interrupt-check`, since a kernel installed but not yet booted passes `apply-check`; anything else is `interrupt`. A package that still owes its interrupt (#887) answers `interrupt`.

3. **The operator follows the answer.** For a first apply or an upgrade of a package with an interrupt, the operator runs the stage before cordoning the node; config changes keep `configInterrupts`, and uninstall is never asked. The node is cordoned and drained if any of its packages answers `drain` or `interrupt`, its merged interrupt covers only the packages that answer `interrupt`, and no opt-in, an error or no answer counts as `interrupt`. This extends the per-node decision config changes already have, through the same `HasInterrupt` and `NextStage` paths, behind the single gate [ADR-004](004-non-interruptible-workloads.md) decision 7 keeps for it.

4. **`needs-interrupt` is a stage with a stored answer.** The operator writes the package's Stage as `needs-interrupt`, `in_progress`, before it creates the Job, so it joins the stage vocabulary and ADR-005's `lifecycle` package. The agent returns the answer in the Job's termination message, in the versioned envelope #871 defines for its preflight, with the operator's request ID echoed so a stale answer is ignored, and the operator stores it in node state, tied to the version and spec it answered for and fixed for the rest of that run, with an event the CLI can show. ADR-004's ordering test uses a stored answer for the current version when there is one, and otherwise treats the node as needing its interrupt.

5. **Off Kubernetes it is an `agent run` intent.** `agent run` with the `needs-interrupt` intent returns the same answer and records nothing a later run reads ([ADR-005](005-agent-owned-package-lifecycle.md)).

## Non-goals

- Dynamic interrupts: the package choosing per node which interrupt its work needs, such as a service restart instead of a reboot. That needs the package to say what its work costs and the operator to merge those answers across packages before cordon, so it is a different ADR; it can reuse this stage's channel.
- A dry run reporting what a rollout would do, which is a different question from whether one node needs its interrupt.
- Drift detection, which can later run the same checks against the version already applied.
- Config changes and uninstall, which keep today's behavior.

## Alternatives

- **Ask every package by default with its existing checks.** Checks are optional and were never written to run before their stage, so a node could skip an interrupt its work needed.
- **#981's ceiling and `request` helper.** Makes interrupts dynamic, needs a CRD field and a way for packages to declare costs; that is the dynamic interrupts ADR's scope.
- **Skip at interrupt time.** Saves the reboot but not the drain, since the node is already cordoned.
- **A semver rule on the CR**, such as patch bumps skipping the interrupt. Applies fleet-wide, so it cannot tell an EKS node from an OKE one.
- **Run package code in #871's preflight.** Breaks that stage's guarantee of running no package code.
- **Gate the Job on a CR field.** Saves the Job for packages that never opt in, at the cost of a CRD change and a second place to declare what the package already says.

## Consequences

- Nodes that answer `live` are not drained or interrupted, which also keeps them clear of the non-interruptible workload barrier ([ADR-004](004-non-interruptible-workloads.md)).
- One short Job per node per change for every package with an interrupt, opted in or not, until the operator can read the opt-in without running the package (#819).
- `live` and `drain` trust the package: a `needs-interrupt` mode that is wrong runs work live that needed a drain, or leaves it unactivated until the next interrupt. The checks default only ever answers `live` or `interrupt`, erring towards the interrupt.
- It is the path to bring-your-own interrupts: [ADR-006](006-package-contract-v1.md) lets the package declare its interrupt, this stage lets each node answer before cordon over a channel that needs no API credentials, and the dynamic interrupts ADR can widen the answer to name which interrupt, merged as declared interrupts are today.

## Open questions

1. Whether a NodeWright can turn the stage off for a package that opts in.
2. One Job, or one per check mode.

## References

- #981, the proposal; #871, the artifact preflight and the termination-message channel; #819; #543; #887.
- [ADR-004](004-non-interruptible-workloads.md), [ADR-005](005-agent-owned-package-lifecycle.md), [ADR-006](006-package-contract-v1.md).
- `ROADMAP.md`: preflight stage and dynamic cordon/drain, bring-your-own interrupts, host drift detection.
