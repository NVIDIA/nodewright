# ADR-005: The agent owns the package lifecycle on a machine

## Status

**Proposed**, 2026-11-24. Answers #1084.

## Decision summary

The NodeWright agent takes over the sequencing of a package's lifecycle on one machine, so that any caller can apply a package correctly without reimplementing the operator's logic: the operator, a provisioning tool, an image build, a test harness, or a host with no Kubernetes. Decisions that need knowledge of the cluster (cordon, drain, the interruption budget, deployment policy, ordering, merging interrupts across packages) stay with the caller that orchestrates it.

The first step is an opt-in subcommand, `agent run`, that applies one lifecycle intent for one package. Phase 1 changes only the agent: the operator keeps its behavior, the per-stage CLI changes only to accept plain JSON interrupts (decision 7) and ADR-006's schema v2, and a later ADR decides how the operator adopts `agent run`.

This ADR relies on [ADR-006](006-package-contract-v1.md) for a package's default settings and the NodeWright variable names, which ship in the same agent release, and on a later ADR, the module ADR, for how the operator and the agent share Go code so both import the `lifecycle` package. Where the VM tests live and how CI runs them is left to an issue filed with this ADR's implementation issues: decision 9 says what they must prove.

## Context

The rules for a single step live in the agent: config validation, returncodes, idempotence flags, `-check` results, and performing an interrupt, including detecting a completed reboot. The order those steps run in lives only in the operator (`NodeState.NextStage`), which runs one Job per stage. So a package can be applied correctly only on a machine that is already a node with the operator installed. Provisioning before a node joins, standalone hosts and image builds have to reimplement the lifecycle or go without the package.

The same gap blocks testing. Package tests run step scripts in a container, and the chainsaw suites run on kind, so a reboot interrupt is never a real reboot and the agent's reboot handling is covered only by unit tests (#1022, #1038). The VM harness designed in NVIDIA/nodewright-packages#152 and #153 needs something to sequence stages and deliberately does not write a second sequencer.

Per-node sequencing and interrupt bookkeeping are also where recent operator bugs cluster (#868, #869, #870, #887, #1069). Moving them to the node may simplify the operator; the ADR for operator adoption decides.

## Decision

1. **`agent run` applies a whole intent.** It takes a package directory, an optional settings file and an intent (`install`, `upgrade`, `config` or `uninstall`, plus `needs-interrupt` from [ADR-007](007-needs-interrupt-before-cordon.md)), and runs the stages in the operator's current order and check pairing. A `config` run takes the changed configmap keys. It can also report the plan and the effective settings, including the interrupt, without running a step, so a caller knows before a run whether to drain. It is dispatched before the per-stage parser, so `agent MODE ...` keeps its behavior apart from decision 7.

2. **The caller's settings override the package's defaults.** The settings file holds the settings [ADR-006](006-package-contract-v1.md) makes overridable, with the NodeWright CR's field names, plus its own `schemaVersion`, and is merged over the package's defaults as ADR-006 decides; a caller with nothing to override passes none. An agent rejects a file version it does not know.

3. **Each interrupt has an explicit owner.** The agent performs it, and continues when the caller re-invokes the same command after a reboot, or the caller does, performing or merging it before telling the agent to continue; the operator would always own its interrupts. Cordon and drain stay with the caller, which drains before the first stage of a run whose plan includes an interrupt, as the operator does today.

4. **A run is identified by its spec, and resumes without doubling work.** A run is a package version, an intent and a digest of the effective settings and changed keys: re-running it resumes at the first incomplete stage, and a different intent or digest replaces it, finished or not, carrying over an interrupt the package still owes (#887). Each run gets its own resource ID, as each NodeWright generation does, so an earlier run's interrupt markers never complete a new run's interrupt. The plan report and the `needs-interrupt` intent never form or replace a run and write no run state. The agent allows one run per package at a time, never starts a stage while a step from an earlier invocation is still running, and relies on steps staying safe to re-run, the package contract since [ADR-003](003-package-execution-as-jobs.md).

5. **On-host state has the same shape as an operator-applied package.** The agent derives the copy, root and log directories from an owner name, under `/var/lib/skyhook/<owner>` and `/var/log/skyhook/<owner>` as the operator's are, and the resource ID from the owner name and the run. The default owner name is not a valid Kubernetes object name, so no NodeWright can share it, and an owner name cannot contain `_`, which separates the resource ID's fields.

6. **The stage graph lives in one `lifecycle` package.** It holds the stage vocabulary, the plan for each intent, config-interrupt matching, owed interrupts (#887) and interrupt merging, imports only the standard library, and replaces the agent's `stage` package. The agent uses it from phase 1; the operator switches to it in phase 2, proven by equivalence tests against its current functions. Phase 2 depends on the module ADR; if that is not accepted, how the operator shares `lifecycle` needs a decision of its own.

7. **Interrupts are plain JSON.** Everything `agent run` reads or writes shows an interrupt as plain JSON. The per-stage CLI also accepts plain JSON in `INTERRUPT_DATA`, alongside base64, so the operator can stop encoding without a flag day.

8. **The interface is a public, versioned contract.** The flags, settings file, run state, result file and exit codes are versioned with the agent and documented in `agent/README.md` by the implementing pull request. An agent from before this ADR rejects `agent run` with its flags as a usage error, and for a call of two to four arguments logs a warning and exits 0 without doing anything, so callers check `agent --version` first and treat a missing result file as failure.

9. **The release waits for a real reboot.** The agent release that ships `agent run` waits until, on a real VM against its release candidate, an agent-owned reboot, a caller-owned reboot and a hard reset during `apply` each resume correctly without re-running completed stages.

### Phases

| Phase | Change | Ships as |
| ----- | ------ | -------- |
| 1 | `lifecycle`; `agent run` with both interrupt owners, run state and results; plain JSON in `INTERRUPT_DATA` | agent minor release, with ADR-006's schema v2 |
| 2 | the operator's stage graph, completion, interrupt and merge logic call `lifecycle`, no behavior change | operator release |
| 3 | the operator runs packages through `agent run`, behind an option | operator release, with its own ADR |

The phase 3 ADR must settle status granularity, how many Jobs a rollout creates, what the operator stops doing, per-stage timeouts, and migrating nodes mid-lifecycle.

## Non-goals

- Changing the operator in phase 1.
- Cluster decisions: cordon, drain, budgets, deployment policy, ordering, pause and disable.
- Several packages in one run, or dependencies between packages.
- Fetching packages; phase 1 takes a directory, and an OCI reference follows #819.
- Restarting itself after a reboot; the caller re-invokes.

## Alternatives

- **A sequencer in each caller.** Every caller's copy of the stage graph drifts from the operator's.
- **The agent making cluster decisions.** Ties the package runner to one orchestrator.
- **Changing the per-stage CLI, or making multi-stage the default.** Breaks the operator contract; an opt-in subcommand does not.
- **Deriving the intent from on-host state now.** The likely end state, but config-change detection needs configmap digests on the host and agreement with the operator's node state.
- **A systemd unit that resumes after a reboot.** A host service the operator does not need.
- **Keeping the graph in the agent and copying it into the operator.** Repeats the duplication the interrupt wire format already has.
- **A separate shared Go module for `lifecycle`, like the `artifact` module #820 proposed.** A third vendored module to share one package; the module ADR weighs it against one module for both.

## Consequences

- Any caller can apply a package with its real lifecycle rules, and the VM harness can run whole lifecycles without a sequencer of its own.
- The agent's reboot path gets end-to-end coverage before the release that depends on it.
- The agent gains a public interface to keep compatible under `docs/operations/versioning.md`.
- Until phase 2, `lifecycle` and the operator hold the same graph twice.

## Open questions

1. Whether the agent should compute changed configmap keys itself from a digest in the run state, the first step towards a derived intent.
2. Whether runs should stream per-stage events for callers that want progress.
3. A read-only `verify` intent that re-runs the current version's `-check` stages for drift detection, building on the `needs-interrupt` intent of [ADR-007](007-needs-interrupt-before-cordon.md).
4. Whether steps should learn that the agent is stopping, for example through a deadline or a forwarded signal.

## References

- #194, #819, #820, #868, #869, #870, #887, #1022, #1038, #1069, #437, #438, #981
- [ADR-003: Package execution as Jobs](003-package-execution-as-jobs.md); [ADR-006: The package contract for v1](006-package-contract-v1.md); [Interrupt flow](../architecture/interrupt-flow.md), [Lifecycle](../architecture/lifecycle.md)
- NVIDIA/nodewright-packages#152 and #153: the VM harness and first consumer of `agent run`
