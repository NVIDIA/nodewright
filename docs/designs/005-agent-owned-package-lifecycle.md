# ADR-005: The agent owns the package lifecycle on a machine

## Status

**Proposed**, 2026-11-24. Answers #1084.

## Decision summary

The NodeWright agent takes over the sequencing of a package's lifecycle on one machine, so that any caller can apply a package correctly without reimplementing the operator's logic: the operator, a provisioning tool, an image build, a test harness, or a host with no Kubernetes. Decisions that need knowledge of the cluster (cordon, drain, the interruption budget, deployment policy, ordering, merging interrupts across packages) stay with the caller that orchestrates it.

The first step is an opt-in subcommand, `agent run`, that applies one lifecycle intent for one package. Phase 1 changes only the agent: the operator keeps its behavior, the per-stage CLI changes only to accept plain JSON interrupts (decision 7), and a later ADR decides how the operator adopts `agent run`.

This ADR relies on three others, each decided on its own: one Go module for the operator and the agent (so both import the `lifecycle` package), NodeWright names for on-host paths and environment variables (#437, #438; they ship in the same agent release), and testing the agent on real machines (where the VM tests live and how CI runs them).

## Context

The rules for a single step live in the agent: config validation, returncodes, idempotence flags, `-check` results, and performing an interrupt, including detecting a completed reboot. The order those steps run in lives only in the operator (`NodeState.NextStage`), which runs one Job per stage. So a package can be applied correctly only on a machine that is already a node with the operator installed. Provisioning before a node joins, standalone hosts and image builds have to reimplement the lifecycle or go without the package.

The same gap blocks testing. Package tests run step scripts in a container, and the chainsaw suites run on kind, so a reboot interrupt is never a real reboot and the agent's reboot handling is covered only by unit tests (#1022, #1038). The VM harness designed in NVIDIA/nodewright-packages#152 and #153 needs something to sequence stages and deliberately does not write a second sequencer.

Per-node sequencing and interrupt bookkeeping are also where recent operator bugs cluster (#868, #869, #870, #887, #1024, #1069). Moving them to the node may simplify the operator; the ADR for operator adoption decides.

## Decision

1. **`agent run` applies a whole intent.** It takes a package directory, a run spec and an intent (`install`, `upgrade`, `config` or `uninstall`), and runs the stages in the operator's current order and check pairing. A `config` run takes the changed configmap keys. It is dispatched before the per-stage parser, so `agent MODE ...` keeps its behavior apart from decision 7.

2. **The run spec is a new, separately versioned document.** `config.json` says what is in a package and does not change. The run spec says how to run it on this machine: the per-package fields of the NodeWright CR (`name`, `version`, `interrupt`, `configInterrupts`, `configMap`, `env`) with the same names and shapes, plus its own `schemaVersion`. An agent rejects a version it does not know.

3. **Each interrupt has an explicit owner.** With `--interrupt-owner=agent` (the default) the agent performs the interrupt through its existing interrupt code; after a reboot the caller re-invokes the same command and the run continues. With `--interrupt-owner=caller` the agent stops before the interrupt and exits with a distinct code; the caller merges it with other packages' or performs it by its own means and re-invokes with `--interrupt-done`. The operator would always use `caller`. Cordon and drain belong to the caller under either owner, and a caller that drains does so before starting a run whose plan includes an interrupt, as the operator drains before `apply` today.

4. **A run is identified by its spec, and resumes without doubling work.** A run is a package version, an intent and a digest of the run spec and changed keys. Re-running the same command resumes at the first incomplete stage and never re-runs a completed one, as the operator does today; a different intent or digest starts a new run. Each run gets its own resource ID, as each NodeWright generation does, so an earlier run's interrupt markers never complete a new run's interrupt, and an interrupt the package still owes (#887) carries over to the new run. The agent refuses to start a stage while a step from an earlier invocation is still running, and allows one run per package at a time. Steps must stay safe to re-run, the package contract since [ADR-003](003-package-execution-as-jobs.md).

5. **On-host state has the same shape as an operator-applied package.** The agent derives the copy, root and log directories from an owner name, and the resource ID from the owner name and the run. The default owner name is not a valid Kubernetes object name, so no NodeWright can share it, and an owner name cannot contain `_`, which separates the resource ID's fields.

6. **The stage graph lives in one `lifecycle` package.** It holds the stage vocabulary, the plan for each intent, config-interrupt matching, owed interrupts (#887) and interrupt merging, imports only the standard library, and replaces the agent's `stage` package. The agent uses it from phase 1; the operator switches to it in phase 2, proven by equivalence tests against its current functions. Phase 2 depends on the module ADR; if that is not accepted, how the operator shares `lifecycle` needs a decision of its own.

7. **Interrupts are plain JSON.** Everything `agent run` reads or writes shows an interrupt as plain JSON. The per-stage CLI also accepts plain JSON in `INTERRUPT_DATA`, alongside base64, so the operator can stop encoding without a flag day.

8. **The interface is a public, versioned contract.** The flags, run spec, run state, result file and exit codes are versioned with the agent and documented in `agent/README.md` by the implementing pull request. An agent from before this ADR rejects `agent run` with its flags as a usage error and silently does nothing for a call of two to four arguments, so callers check `agent --version` first and treat a missing result file as failure.

9. **The release waits for a real reboot.** The agent release that ships `agent run` waits until, on a real VM against its release candidate, an agent-owned reboot, a caller-owned reboot and a hard reset during `apply` each resume correctly without re-running completed stages.

### Phases

| Phase | Change | Ships as |
| ----- | ------ | -------- |
| 1 | `lifecycle`; `agent run` with both interrupt owners, run state and results; plain JSON in `INTERRUPT_DATA` | agent minor release, with the naming ADR's changes |
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
- **A separate shared Go module for `lifecycle`.** A third vendored module to share one package, not needed if the module ADR is accepted.
- **Run settings in `config.json`.** Mixes what a package is with how one machine runs it.

## Consequences

- Any caller can apply a package with its real lifecycle rules, and the VM harness can run whole lifecycles without a sequencer of its own.
- The agent's reboot path gets end-to-end coverage before the release that depends on it.
- The agent gains a public interface to keep compatible under `docs/operations/versioning.md`.
- Until phase 2, `lifecycle` and the operator hold the same graph twice.

## Open questions

1. Whether the agent should compute changed configmap keys itself from a digest in the run state, the first step towards a derived intent.
2. Whether runs should stream per-stage events for callers that want progress.
3. A read-only `verify` intent that re-runs `-check` stages, and whether every check is safe to re-run.
4. Whether steps should learn that the agent is stopping, for example through a deadline or a forwarded signal.

## References

- #194, #819, #820, #868, #869, #870, #887, #1022, #1024, #1038, #1046, #1069, #437, #438, #981
- [ADR-003: Package execution as Jobs](003-package-execution-as-jobs.md); [Interrupt flow](../architecture/interrupt-flow.md), [Lifecycle](../architecture/lifecycle.md)
- NVIDIA/nodewright-packages#152 and #153: the VM harness and first consumer of `agent run`
