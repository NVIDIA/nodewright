# ADR-007: Assess whether a node needs the change before cordoning it

## Status

**Proposed**, 2026-11-24. Answers the skip case of #981, which has the full proposal. Its per-node interrupt levels are bring-your-own, dynamic interrupts, a different decision for a different ADR.

## Decision summary

A package can opt in to being assessed: before a change, NodeWright asks the package on each node whether the node already matches the new version. A node that matches is recorded at the new version without being cordoned, drained or interrupted. Any other answer, or none, takes today's path with the declared interrupt, so assessing never makes a rollout more disruptive than it is now.

Assess answers "is there work on this node?", not "what does the work cost?": interrupts stay as declared. Making them dynamic, so the package decides per node which interrupt its work needs, is bring-your-own interrupts and gets its own ADR. A dry run, which reports what a rollout would do, is a separate concept and not decided here.

## Context

A package's interrupt is fixed per package, so every change cordons, drains and interrupts every node it selects, whether or not the node needs it. In `nvidia-setup`, a release that changes only the OKE path reboots every EKS node whose running kernel already matches, and on `aks-h100` every bump restarts containerd and kubelet even when nothing changed (#981).

A package's `-check` modes describe its target state, but they are optional and today run only straight after their stage, so nothing says they are safe or meaningful before it. A check that passes says the work is done; a failing one says there is work, not whether that work needs a drain.

## Decision

1. **Assess is opt-in by the package.** A package opts in in `config.json`, either by declaring that its check modes can run before their stages, or by defining an `assess` mode of its own, whose steps can call the check scripts or do something else. A package that does not opt in is never assessed.

2. **Skip only when the node provably matches.** Assess runs, from the new version, the path's check modes or the `assess` mode. The node matches only when every step passes. A path with no checks does not match, and a package with an interrupt matches only if its assessment covers the running state (`post-interrupt-check`, or its `assess` mode), since a kernel installed but not yet booted passes `apply-check`.

3. **A match skips the node; anything else is today's path.** Before cordoning a node for a package with an interrupt, the operator runs an assess Job there. A match records the new version and completes the package with no cordon, drain or interrupt. No match, no opt-in, an error or no answer at all takes today's path with the declared interrupt.

4. **The answer is stored on the node.** The agent returns it in the Job's termination message, the channel #871 also uses, and the operator stores it in the package's node state and records an event, so the CLI can say why a node was or was not touched. Off Kubernetes a caller gets the same answer from `agent run` with an `assess` intent ([ADR-005](005-agent-owned-package-lifecycle.md)).

## Non-goals

- Bring-your-own, dynamic interrupts: the package deciding per node which interrupt its work needs, such as a service restart instead of a reboot, or no drain for live work. That needs the package to say what its work costs and the operator to merge those answers across packages before cordon, so it is a different ADR; it can reuse this ADR's channel.
- A dry run reporting what a rollout would do, which is a different question from whether one node needs the change.
- Drift detection, which can later run the same assessment against the version already applied.
- Uninstall, which is never assessed.

## Alternatives

- **Assess every package by default with its existing checks.** Checks are optional and were never written to run before their stage, so a package could be skipped without its work being done.
- **#981's ceiling and `request` helper.** Makes interrupts dynamic, needs a CRD field and a way for packages to declare costs; that is the bring-your-own interrupts ADR's scope.
- **Skip at interrupt time.** Saves the reboot but not the drain, since the node is already cordoned.
- **A semver rule on the CR**, such as patch bumps skipping the interrupt. Applies fleet-wide, so it cannot tell an EKS node from an OKE one.
- **Run package code in #871's preflight.** Breaks that stage's guarantee of running no package code.

## Consequences

- Nodes that already match are not drained or interrupted, which also keeps them clear of the non-interruptible workload barrier ([ADR-004](004-non-interruptible-workloads.md)).
- One short Job per node per change for every package with an interrupt, opted in or not, until the operator can read the opt-in without running the package (#819).
- A skip trusts the package's assessment: one that passes wrongly skips needed work until the next change.

## Open questions

1. Whether a NodeWright can turn skipping off for a package that opts in.
2. One Job, or one per check mode.

## References

- #981, the proposal; #871, the artifact preflight and the termination-message channel; #819; #543.
- [ADR-004](004-non-interruptible-workloads.md), [ADR-005](005-agent-owned-package-lifecycle.md), [ADR-006](006-package-contract-v1.md).
- `ROADMAP.md`: preflight stage and dynamic cordon/drain, bring-your-own interrupts, host drift detection.
