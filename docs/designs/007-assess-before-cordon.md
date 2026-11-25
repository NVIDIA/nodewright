# ADR-007: Assess what each node needs before cordoning it

## Status

**Proposed**, 2026-11-24. Answers #981, which has the full proposal: the outcomes table, the package author guide, a worked example and the testing plan.

## Decision summary

Before NodeWright cordons a node for a change, it runs the package's own checks there, from the new version, to find out what this change needs on this node: nothing, live work, or work behind a drain and a given interrupt. A node that needs nothing is never touched, one that needs only live work is never drained, and every node still ends up recorded at the new version.

It is opt-in per package interrupt. The declared interrupt becomes a ceiling: the most disruptive thing the package may do, with each node's answer at or below it. Without the opt-in, nothing changes.

The new stage is called assess. It is separate from the artifact preflight of #871, which runs no package code.

## Context

A package's interrupt is fixed per package, so every change interrupts every node it selects, whether or not the node needs it. A version bump runs `upgrade → config → interrupt` unconditionally unless only config keys changed (`NodeState.HasInterrupt`), and the node is cordoned and drained before `upgrade`. In `nvidia-setup`, a release that changes only the OKE path reboots every EKS node whose running kernel already matches, and on `aks-h100` every bump restarts containerd and kubelet even when nothing changed (#981). Splitting images per platform narrows this but does not fix it: within a platform, a release that changes nothing on a node still drains and interrupts it.

The package already says what its target state is. `apply-check`, `upgrade-check` and `config-check` confirm the configured state, and `post-interrupt-check` the running state; today each runs only straight after its stage.

## Decision

1. **Assess runs the path's checks before the change.** For a first apply, a version bump or a config change, the node's work starts with an assess Job that runs, standalone and from the new version, every check mode the package defines for that path. It runs per node inside its batch, just before that node's work, so the answer is fresh, and it never cordons.

2. **A failing check can say what fixing it costs.** Check and step scripts get a `request <level> [services…]` helper. The answer is the highest level requested by a failing check, ranked `none < noop < service < restartAllServices < reboot`, with service lists merged. Every check passing answers `skip`; a failure that requests nothing answers the ceiling.

3. **Opt in with a ceiling.** An interrupt marked `conditional` makes its type the ceiling: the answer may be that type or anything below it, decided per node. Without `conditional`, behavior is as today and assess never runs. The ceiling is the effective interrupt after [ADR-006](006-package-contract-v1.md)'s merge. A conditional interrupt cannot be combined with `configInterrupts`, which admission rejects, so two mechanisms never decide the same interrupt.

4. **The answer decides the path.** `skip` runs nothing and records the version. `none` runs the stages live, with no cordon or drain. `noop` and above cordon, drain, run the stages and the interrupt of that type, merged across packages as today. The checks that ran in assess are not re-run after a `skip`; when stages do run, their checks follow them as today.

5. **Anything unclear fails before the drain.** The agent reports one outcome: answered, unsupported (the package has no checks for the path, so the ceiling applies), or invalid (a malformed request). A request above the ceiling, an invalid request, or no readable result puts the package in `erroring` before the node is cordoned. A missing result never counts as "no checks", so a crashed or old agent cannot silently take the ceiling path.

6. **The answer is stored on the node.** The agent returns it in the Job's termination message, the channel #871 also uses, and the operator stores it in the package's node state, from which `HasInterrupt` decides per node. The answer and its outcome are recorded as an event, so "why didn't this node reboot?" has an answer the CLI can show.

7. **A step can escalate late, within the ceiling.** A step that finds more work than assess predicted (an upgrade brought a new kernel) can request an interrupt up to the ceiling, and the operator then cordons, drains and interrupts before marking the node complete.

8. **Checks become part of the package contract.** A check must be safe to run on its own, before its stage as well as after it, and a failing check before its stage is normal. The agent runs assess through an `assess` mode in the per-stage CLI and an `assess` intent in `agent run` ([ADR-005](005-agent-owned-package-lifecycle.md)).

## Non-goals

- Uninstall: a conditional uninstall takes the ceiling path.
- Dry runs and drift detection. Both reuse assess later: a dry run is assess on demand, and drift is a node that no longer answers `skip` for the version it has.
- Per-step costs declared in `config.json`, which the operator could read without running anything but which cannot express a package that branches by platform.

## Alternatives

- **A separate `interrupt-plan` mode** that predicts the cost. A second description of the package to keep in step with apply, which needs verifying after a `skip` and cannot serve drift detection.
- **Skip at interrupt time.** Saves the reboot but not the drain, since the node is already cordoned.
- **A semver rule on the CR**, such as patch bumps skipping the interrupt. Release engineers would encode reboot necessity in version numbers, fleet-wide, with no way to differ by platform.
- **Run package code in #871's preflight.** Breaks that stage's guarantee of running no package code, and depends on package artifacts.
- **Clamp a request above the ceiling.** Silently skips something the package needs.

## Consequences

- Nodes that need nothing are not drained or interrupted, which also keeps them clear of the non-interruptible workload barrier ([ADR-004](004-non-interruptible-workloads.md)).
- One short Job per node per change; the image is pulled either way.
- Package checks carry more weight: an inaccurate check either costs an unneeded interrupt or, if it passes wrongly, skips needed work. A check broken outright reads as "needs the ceiling", the safe direction, and still fails its stage afterwards.
- A real reboot from a late escalation cannot run on kind and falls to the real-machine tests, as today.

## Open questions

1. A request that a non-conditional CR does not allow (a step calls `request reboot` under no interrupt): ignore it, fail, or warn with an event. #981 leans towards a warning.
2. Whether a package's declared interrupt should apply to a CR that declares none, or only once the CR opts in: ADR-006 makes the package's interrupt a default, and #981 worries an image bump could then start rebooting nodes under an unchanged CR.
3. The Status for a node that escalated late and waits for an interrupt slot.
4. One Job or one per check mode, and whether `post-interrupt-check` runs for a path that would not interrupt.
5. The names: `conditional`, `assess`, and `skip` against `none` against `noop`.

## References

- #981, the proposal; #871, the artifact preflight and the termination-message channel; #819; #543.
- [ADR-004](004-non-interruptible-workloads.md), [ADR-005](005-agent-owned-package-lifecycle.md), [ADR-006](006-package-contract-v1.md).
- `ROADMAP.md`: preflight stage and dynamic cordon/drain, bring-your-own interrupts, host drift detection.
