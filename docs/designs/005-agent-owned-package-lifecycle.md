# ADR-005: The agent owns the package lifecycle on a machine

## Status

**Proposed**, 2026-11-24. Answers #1084.

## Decision summary

The NodeWright agent takes over the sequencing of a package's lifecycle on one machine, so that anything can apply a package correctly without reimplementing the operator's logic: the operator, a provisioning tool, an image build, a test harness, or a host with no Kubernetes at all. The first step is an opt-in subcommand, `agent run`, that applies a whole lifecycle intent (`install`, `upgrade`, `config`, `uninstall`) for one package, in the same stage order and check pairing the operator uses today. Each interrupt is owned either by the agent, which performs it on the machine, or by the caller, to whom the agent hands it at a defined boundary. A run state file makes a run resumable across a reboot or a killed process, and a result file plus documented exit codes report per-stage outcomes.

The stage graph moves into one `lifecycle` package that the agent uses from the start and the operator switches to later with equivalence tests, so the two cannot drift. Decisions that need knowledge of the cluster stay with the caller that orchestrates it, on Kubernetes the operator: cordon, drain, the interruption budget, deployment policy, ordering, and merging interrupts across packages.

This ADR depends on three others, each decided on its own:

| ADR | What this ADR relies on it for |
| --- | --- |
| One Go module for the operator and the agent | a `lifecycle` package both import, with no shared-module setup |
| NodeWright names for on-host paths and environment variables (#437, #438) | the names `agent run` uses; the two ship in the same agent release |
| Testing the agent on real machines | where the agent's VM tests live and how CI runs them; this ADR sets only the release gate |

Phase 1 changes only the agent. Existing per-stage CLI forms keep their behavior, and the operator is untouched until a later ADR decides how it adopts `agent run`.

## Context

### Who decides what today

The rules for a single step live in the agent: `config.json` validation, `expected_config_files`, `root_dir`, returncodes, idempotence flags, `-check` results, `env:VAR` substitution, upgrade version variables, and performing an interrupt, including detecting a completed reboot through a `boot_id` marker. The order those steps run in lives only in the operator, which decides from node annotations what runs next and starts one Job at a time.

So nothing outside a cluster can run a package the way the operator does. A package is usable only on a machine that is already a Kubernetes node with the operator installed. Machines that are not nodes, or not yet (provisioning before a node joins, standalone hosts, image builds), have to reimplement the lifecycle or go without the package.

### Packages and the agent cannot be tested on a real machine

Package tests in NVIDIA/nodewright-packages run step scripts in a container, which cannot touch bootloaders, kernels, drivers or tuned profiles, and cannot reboot. The chainsaw suites here run on kind, whose nodes are containers, so a `reboot` interrupt is never a real reboot either. The agent's reboot handling (`boot_id` markers, the interrupt container restarting in place after the reboot) is covered by unit tests with an injected command runner (#1022, #1038) and nowhere end to end.

NVIDIA/nodewright-packages#152 and #153 design a Go test harness that boots a QEMU VM, stages a package and reboots it. To run a whole lifecycle with the real rules it needs something to sequence stages, and it deliberately does not write a second sequencer that would drift from the operator.

### The operator carries per-node logic it does not need

Per-node sequencing and interrupt bookkeeping are a large part of the operator's state machine, and they are where recent bugs cluster: #868, #869, #870, #887, #1024 (fixed in #1046) and #1069. Moving per-node sequencing to the node, and leaving the operator only the decisions that need knowledge of the cluster, may simplify the operator. The ADR for operator adoption decides whether it does.

### Current behavior

Facts on `main` at 882b699d that shape the decision.

- **Agent CLI.** `agent MODE [ROOT_MOUNT] COPY_DIR [INTERRUPT_DATA]` runs one mode, and `agent --version` prints the version (`agent/internal/agent/agent.go`). Modes are the eleven stages in `agent/internal/stage/stage.go`; `interrupt` is the only one without a `-check`. Exit codes are 0 (success), 1 (failure) and 2 (usage). An unrecognized mode with two to four arguments is logged and treated as a no-op with exit 0, so an agent from before this ADR exits 0 for `agent run DIR`; with more arguments it exits 2 for a usage error.
- **Identity and paths.** `SKYHOOK_RESOURCE_ID` is `<nodewright>-<uid>-<generation>_<package>_<version>` (`ResourceID()` in `operator/internal/wrapper/nodewright.go`); the agent parses package and version from its last two `_` fields and gates interrupt re-runs on it. Flags live under `<SKYHOOK_ROOT_DIR>/flags/<package>/<version>`, the version ledger at `<SKYHOOK_ROOT_DIR>/history/<package>.json`, interrupt markers under `<SKYHOOK_ROOT_DIR>/interrupts/flags/<resource ID>`, logs under `SKYHOOK_LOG_DIR`. The operator sets `SKYHOOK_ROOT_DIR=/var/lib/skyhook/<nodewright>`, `SKYHOOK_LOG_DIR=/var/log/skyhook/<nodewright>` and `COPY_RESOLV=false`.
- **Stage Jobs.** `createPodFromPackage` (`operator/internal/controller/job_builder.go`) builds one Job per stage: an init container copies `/skyhook-package` from the package image to the copy directory, then the agent runs the stage and its `-check` as consecutive init containers, with the host root at `/root`. Configmap keys are mounted at `/skyhook-package/configmaps/<key>` and node metadata at `/skyhook-package/node-metadata`.
- **Stage graph.** `NodeState.NextStage` (`operator/api/nodewright/v1alpha1/nodewright_types.go`, with a copy for the legacy group in `operator/api/v1alpha1/skyhook_types.go`) advances only from `complete`. Without an interrupt: `uninstall → apply → config`, `upgrade → config`. With one: `apply → config → interrupt → post-interrupt`, `upgrade → config → interrupt → post-interrupt`. An uninstall with an interrupt goes to `uninstall-interrupt`, then the package entry is removed. `interrupt → post-interrupt` is unconditional, so a package that reached `interrupt` always finishes. `HandleVersionChange` starts `upgrade` only when the spec version is greater than the installed one; a downgrade does nothing.
- **Config changes.** `HandleConfigUpdates` records the changed configmap keys and resets nodes to `config`. `GetConfigInterrupts` (`operator/internal/wrapper/nodewright.go`) matches the changed keys against `configInterrupts` with `filepath.Match`.
- **Owed interrupts.** Since #887, `PackageStatus.InterruptPending` records per node and package that a package declaring an interrupt has passed `apply` or `upgrade` without its interrupt running. It clears at `post-interrupt`, or when the interrupt completes. While config updates are recorded, `HasInterrupt` is true if a changed key matches a `configInterrupt` or the package still owes its own interrupt (`OwesOwnInterrupt`), so a config change cannot complete a package whose interrupt never ran.
- **Interrupts.** The operator encodes the CR interrupt (`reboot`, `service`, `restartAllServices`, `noop`) as base64 JSON and passes it as `INTERRUPT_DATA`. Across packages on a node, `fudgeInterruptWithPriority` merges interrupts (reboot wins, service lists union), working on a copy so neither the spec nor `configInterrupts` changes (#1046). It runs only the winning package's interrupt Job and marks the others `skipped`. For a package with an interrupt, `ProcessInterrupt` cordons and drains before `apply`, not only before the interrupt. After a reboot the interrupt container restarts in place, and a changed `boot_id` promotes the pending marker; the operator ignores a container record left by a replaced sandbox (#1069).
- **Package contents.** `config.json` describes what is inside a package and how each stage runs its steps. It carries `schema_version` (`v1` today), validated against the schemas embedded in `agent/internal/config/schemas/`. How a package is run on a given node (its interrupt, config interrupts, configmap and env) is not in the package; it comes from the CR.
- **Termination.** On `SIGTERM` the agent stops starting new steps but never signals the running one: each step runs in its own process group and is left to finish (`agent/internal/command/process.go`). In a pod, the grace period and then `SIGKILL` end it.
- **Modules.** `operator/` and `agent/` are separate vendored modules with no shared code; the interrupt wire format is duplicated on purpose. The #819 implementation proposed a shared `artifact/` module consumed through `replace` and vendoring, but it has not landed (#828 to #832 closed).

## Decision

### `agent run`

```text
agent run --package DIR --spec FILE --intent install|upgrade|config|uninstall
          [--changed KEY[,KEY...]] [--interrupt-owner agent|caller] [--interrupt-done]
          [--root DIR] [--name NAME] [--result FILE]
```

| Flag              | Default   | Meaning                                                                                                      |
| ----------------- | --------- | ------------------------------------------------------------------------------------------------------------ |
| `--package`       | required  | a directory laid out like `/skyhook-package` in a package image (`config.json`, `skyhook_dir/`, `root_dir/`) |
| `--spec`          | required  | the run spec (below)                                                                                         |
| `--intent`        | required  | which lifecycle to run                                                                                       |
| `--changed`       | none      | configmap keys that changed; required for `config`, matched against `configInterrupts`                       |
| `--interrupt-owner` | `agent` | who performs the interrupt: the agent, here, or the caller, after the agent stops at the boundary          |
| `--interrupt-done`  | off     | the caller performed the interrupt it was handed; continue after the boundary                              |
| `--root`          | `/`       | the host root, as `ROOT_MOUNT` today (`/root` inside an operator pod)                                        |
| `--name`          | `local`   | the owner name, standing in for the NodeWright name in paths and the resource ID; no `_`                     |
| `--result`        | none      | where to write the result file, in addition to the run state                                                 |

`run` is dispatched before the per-stage argument parser, so `agent MODE ...` keeps its exact behavior, including the unknown-mode no-op.

### Run spec

The run spec is a new document, separate from `config.json`. `config.json` says what is inside a package and how its stages run, and does not change. The run spec says how to run the package on this machine: the per-package fields of the NodeWright CR that matter on one machine, with the same names and shapes, so a caller can copy them from a CR:

```json
{
  "schemaVersion": "v1",
  "name": "nvidia-setup",
  "version": "0.8.0",
  "interrupt": { "type": "reboot" },
  "configInterrupts": { "kernel": { "type": "reboot" } },
  "configMap": { "service": "eks", "accelerator": "gb300" },
  "env": [{ "name": "EXAMPLE", "value": "1" }]
}
```

`name` and `version` must match `config.json`. `configMap` entries are written to `<copy dir>/configmaps/<key>`, where the operator's mounts put them. `env` is added to the environment of every step. If `--package` contains `node-metadata/`, it is used as is. Interrupts are plain JSON objects, as in the CR, never base64.

The run spec is versioned on its own, the way `config.json` is: `schemaVersion` names a JSON schema embedded in the agent, and an agent rejects a version it does not know with a usage error. A field added later is a new schema version. The spec type lives in `lifecycle`, so Go callers build it as a struct; JSON keeps the agent free of a YAML dependency. Packages declaring their own interrupts would change `config.json`, not the run spec, and belong to a later ADR.

### What the agent synthesizes

Everything the operator provides today, derived from `--name`, so a run has the same on-host shape as an operator-applied package:

| Today (operator)      | `agent run`                                                       |
| --------------------- | ----------------------------------------------------------------- |
| `SKYHOOK_RESOURCE_ID` | `<name>_<package>_<version>`                                      |
| copy directory        | `/var/lib/skyhook/<name>/<package>-<version>`                     |
| init-copy container   | the agent copies `--package` into the copy directory              |
| `SKYHOOK_ROOT_DIR`    | `/var/lib/skyhook/<name>`                                         |
| `SKYHOOK_LOG_DIR`     | `/var/log/skyhook/<name>`                                         |
| `COPY_RESOLV`         | `false`                                                           |
| `INTERRUPT_DATA`      | encoded from the spec's `interrupt` or matched `configInterrupts` |

The environment variables keep working as overrides, as they do for the per-stage CLI. The table shows today's names. The naming ADR (#437, #438) decides the NodeWright names and how the old ones keep being read, and it ships in the same agent release as `agent run`, so `agent run` never introduces a Skyhook-named path or variable of its own. Because `agent run` defaults to its own owner name (`--name local`), its on-host state never mixes with an operator-applied package's.

### The `lifecycle` package

One package, `lifecycle`, in the module the module ADR sets up for the operator and the agent. It imports only the standard library, so the agent binary gains no dependencies from it. It holds:

- the stage and intent vocabulary;
- `Plan(intent, interrupt) []Stage`, the ordered stages for an intent;
- `MatchConfigInterrupts(configInterrupts, changed)` and `MergeInterrupts(...)`, the rules of today's `GetConfigInterrupts` and `fudgeInterruptWithPriority`. Both are pure: they return new values and never change their inputs, which is the property #1046 restored;
- the run spec type and the interrupt encodings: plain JSON, and today's base64 JSON for the per-stage CLI.

The plans, taken from today's operator graph:

| Intent      | Without an interrupt                                 | With an interrupt, appended                             |
| ----------- | ---------------------------------------------------- | ------------------------------------------------------- |
| `install`   | `apply`, `apply-check`, `config`, `config-check`     | `interrupt`, `post-interrupt`, `post-interrupt-check`   |
| `upgrade`   | `upgrade`, `upgrade-check`, `config`, `config-check` | `interrupt`, `post-interrupt`, `post-interrupt-check`   |
| `config`    | `config`, `config-check`                             | the same, when the config interrupt rule below says so  |
| `uninstall` | `uninstall`, `uninstall-check`                       | `interrupt` (the operator's `uninstall-interrupt`)      |

A `config` run includes the interrupt when a changed key matches `configInterrupts`, or when the package still owes its own interrupt. That mirrors #887: the run state records `interruptOwed` when an `install` or `upgrade` run with an interrupt completes `apply` or `upgrade`, and clears it when the interrupt completes or when `post-interrupt` completes, as the operator clears `InterruptPending`. A new run for the same resource ID carries the flag over from the run it replaces. When both apply, the interrupts are merged with `MergeInterrupts`.

The agent's internal `stage` package moves into `lifecycle`. A second copy of the graph in the agent is the drift this ADR exists to prevent.

Downgrades are refused with a usage error, matching the operator, which does nothing on a downgrade. `upgrade` keeps today's version variables from the history ledger. Operator policies that are not per-step rules, such as `uninstall.enabled`, stay with the caller.

### Who owns the interrupt

The boundary is the point after the last stage before `interrupt`. Today the operator always owns what happens there. `agent run` makes the owner explicit with `--interrupt-owner`:

- **`agent`** performs the interrupt through the existing interrupt code path, with the same types, markers and resource ID. Service restarts and no-ops complete and the run continues. A reboot records the pending interrupt and the current `boot_id` in the run state, then reboots; the process ends with the host. The caller re-invokes the same command once the host is back. The existing marker logic sees a changed `boot_id`, marks the interrupt complete, and the run continues with `post-interrupt`.
- **`caller`** records the pending interrupt and the current `boot_id` in the run state and result, and exits with code 3 without performing it. The caller does what it needs (cordon, drain, merge with other packages, perform the interrupt by its own means or with `agent interrupt`) and re-invokes with `--interrupt-done`. The agent continues with `post-interrupt`. `--interrupt-done` is a usage error when the run state has no pending interrupt.

`agent` is the default because it is what a test or a host without an orchestrator wants. The operator would always pass `caller`.

Everywhere `agent run` shows an interrupt (the run spec, the run state, the result file), it is plain JSON such as `{"type": "reboot"}`. The per-stage CLI keeps accepting base64 JSON in `INTERRUPT_DATA` for the operators that send it, and from phase 1 also accepts plain JSON there, recognized by a leading `{`, so the operator can stop encoding later without a flag day.

### Run state and resume

The agent keeps a run state file at `<SKYHOOK_ROOT_DIR>/runs/<resource ID>.json`, rewritten atomically after each stage:

```json
{
  "resourceID": "local_nvidia-setup_0.8.0",
  "intent": "install",
  "specDigest": "sha256:...",
  "stages": [
    { "stage": "apply", "result": "complete", "started": "...", "finished": "..." },
    { "stage": "apply-check", "result": "complete", "started": "...", "finished": "..." }
  ],
  "interruptOwed": true,
  "pendingInterrupt": { "interrupt": { "type": "reboot" }, "bootID": "..." },
  "runningStep": null,
  "outcome": "interrupt-pending"
}
```

The spec digest covers the whole run spec, `configMap` contents included, and the `--changed` keys. Re-running the same command with the same intent and spec digest resumes: completed stages are skipped and the first incomplete stage runs, and re-running a completed run does nothing. That matches the operator, which never re-runs a completed stage, and keeps the per-step flags as the second line of idempotence inside a stage. A different intent or spec digest for the same resource ID starts a new run and archives the old state next to it. Only the latest run is current, so a key changed to one value, then another, then back again starts a new run each time.

A lock file per owner name and package, not per resource ID, stops two runs of the same package overlapping, across versions too: the versions share the package's history ledger and its state on the host. The operator gets the same exclusion today because it runs one stage per package and node at a time.

On `SIGTERM` the running step finishes, as today, and the run state records where it stopped. Outside a pod nothing kills a step whose agent was killed: the step keeps running in its own process group after the lock is released. So while a step runs, the run state records it in `runningStep` (stage, step, process group ID and start time), and a re-invocation that finds that process group still alive exits with code 4 instead of starting the stage a second time.

Only the agent writes a step's completion flag, after the step succeeds. A step that finishes while no agent is running therefore has no flag, and the next invocation runs it again. That is the package contract today: the operator re-runs a stage whose pod was lost ([ADR-003](003-package-execution-as-jobs.md)), so every step must be safe to run again, and `agent run` relies on the same contract rather than recording exit statuses from a process that outlives the agent.

### Results and exit codes

| Exit | Meaning                                                                                                    |
| ---- | ---------------------------------------------------------------------------------------------------------- |
| 0    | the intent completed                                                                                       |
| 1    | a stage failed; the result names the stage and step                                                        |
| 2    | usage error, including an unknown `schemaVersion`, a spec that does not match `config.json`, a downgrade, or a stray `--interrupt-done` |
| 3    | the interrupt was handed to the caller (`--interrupt-owner=caller`)                                        |
| 4    | a step started by an earlier invocation is still running                                                   |

Codes 0 to 2 keep the meaning they have for the per-stage CLI. The result file, written to `--result` and kept as the run state, is the run state at exit. Stdout keeps today's banner and per-step headers for each stage, so logs read as they do now.

An agent from before this ADR exits 0 for `agent run DIR` and 2 for the full flag form, having done nothing either way. A caller therefore treats a missing or unreadable result file as failure whatever the exit code, and checks `agent --version` before relying on `agent run`.

### Testing

`agent run` is tested at four levels:

| Level           | Where                        | Covers                                                                                                                                                                                                            |
| --------------- | ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lifecycle`     | the package's unit tests     | plans for every intent and interrupt combination; config-interrupt matching, owed interrupts and merging, with tables taken from the operator's current tests                                                      |
| Agent unit      | `agent/`, `make test`        | argument parsing and dispatch, run state transitions, resume, spec versions and digest changes, lock contention, a still-running step, exit codes, `--interrupt-done` misuse, the result file; the injected command runner from #1022 stands in for the host |
| Agent container | `agent/`, `make test`        | full `install`, `upgrade`, `config` and `uninstall` runs of a test package in a privileged container, with `noop` interrupts and caller-owned interrupts, where nothing reboots                                    |
| Real machine    | decided by the testing ADR   | everything that needs a real kernel, systemd and a reboot                                                                                                                                                         |

The agent release that ships `agent run` waits until these pass on a real VM, against its release candidate:

1. an agent-owned reboot: the host reboots once, and after re-invocation `post-interrupt` runs once and `apply` does not re-run;
2. a caller-owned reboot: exit 3 with the pending interrupt in the result, a reboot by the caller, then `--interrupt-done` and exit 0;
3. a hard reset during `apply`: re-invocation resumes at `apply`, and earlier stages do not re-run.

A reboot is the step no other level can test, and resuming across one is the main promise this ADR makes. Where those tests live, which other scenarios run, and how CI runs and gates them is the testing ADR's decision; the VM harness in NVIDIA/nodewright-packages (#152, #153) is where they are expected to run.

## Phases

| Phase | Change | Ships as | Depends on |
| ----- | ------ | -------- | ---------- |
| 1 | the `lifecycle` package; `agent run` with agent- and caller-owned interrupts, run state and results; plain JSON accepted in `INTERRUPT_DATA` | agent minor release (v7.1), with the naming ADR's agent changes | this ADR; the module ADR; the naming ADR; the release gate under Testing |
| 2 | the operator's `NextStage`, `IsPackageComplete`, `HasInterrupt`, `GetConfigInterrupts` and interrupt merging call `lifecycle`, with no behavior change | operator release | 1 |
| 3 | the operator runs packages through `agent run --interrupt-owner=caller`, behind an option | operator release | 1, 2, and its own ADR |
| Later | OCI reference input (`--package-ref`); intent derived from on-host state; a read-only `verify` intent for drift detection | agent releases | #819; 3 |

Phase 2 proves the shared graph against the operator's current behavior: table tests run the old functions and `lifecycle` over every intent, interrupt, owed-interrupt and state combination, and must agree before the old code is deleted. The legacy group's copy of `NextStage` moves with it.

### What the phase 3 ADR must answer

Phase 3 gets its own ADR. Its likely shape: behind an operator option, default off, the operator runs `agent run --interrupt-owner=caller` in Jobs instead of one Job per stage. For a package with an interrupt it still cordons and drains first. When a run exits 3, the operator merges pending interrupts across packages, waits for the node's other packages and runs one interrupt Job, all as today, then re-invokes with `--interrupt-done`. That ADR must answer:

1. **Status granularity.** Node state, `kubectl` status and the CLI show the current stage today; a Job that runs `agent run` covers several stages.
2. **How many Jobs.** One Job per stage (today), one per package run segment (up to the interrupt boundary, then after it), or one per node for the whole rollout. Fewer Jobs means fewer objects in etcd and less reconcile work; more Jobs means finer status, retries and timeouts. ADR-003 chose a Job per stage and rejected one long-lived Job per rollout to keep the state machine's granularity; with sequencing in the agent and per-stage results in its result file, that trade-off can be revisited.
3. **What the operator stops doing.** Which parts of `ApplyPackage`, `HandleCompletePod`, the skipped-interrupt promotion and `InterruptPending` go away.
4. **Stage timeouts.** `stageTimeout` bounds each stage today; with several stages per Job the agent may need to enforce it per stage.
5. **Migration.** Nodes mid-lifecycle when the option flips, including owed interrupts recorded in node state.

## Non-goals

- Changing the operator in phase 1.
- Decisions that need knowledge of the cluster: cordon, drain, interruption budget, deployment policy and batches, priority and `sequencing`, `runtimeRequired` taint removal, pause and disable. They stay with whatever orchestrates the machines.
- Several packages in one run, or `dependsOn` between packages. A caller that needs several packages runs them in order.
- Fetching packages. Phase 1 takes a directory; an OCI reference follows once package artifacts (#819) land.
- Restarting itself after a reboot. A caller re-invokes `agent run`; the agent installs no systemd unit or other host service.

## Alternatives

- **A sequencer in each caller.** Every tool that applies packages (a test harness, a provisioning tool) writes its own copy of the stage graph, which drifts from the operator's and gets the rules subtly wrong. Putting the lifecycle in the agent is what lets any caller invoke a package correctly.
- **The agent making cluster decisions.** The agent would call the Kubernetes API to cordon, drain and apply the budget itself. That ties the package runner to one orchestrator; keeping it modular, with those decisions in the caller, is what makes the agent usable with or without Kubernetes.
- **Changing the per-stage CLI or making multi-stage the default.** It breaks the operator contract; an opt-in subcommand ships without it.
- **Deriving the intent from on-host state in phase 1.** The natural end state, since the history ledger already records the installed version, but config-change detection needs configmap digests on the host and agreement with the operator's node state. Explicit intent comes first.
- **A systemd unit that resumes the run after a reboot.** It adds a host service the operator does not need and that tests do without; callers re-invoke instead.
- **Keeping the stage graph in the agent and copying it into the operator.** Smaller in phase 1, but it repeats the duplication the interrupt wire format already has, and phase 2 would have nothing to prove equivalence against.
- **A shared `lifecycle/` Go module, consumed through `replace` and vendoring.** It works with today's layout, as #819 proposed for `artifact/`, but adds a third vendored module and its setup to share one package. The module ADR removes the need.
- **Run settings in `config.json`.** It would mix what a package is with how one machine runs it, and change a contract every package already builds against.

## Consequences

- A package can be applied, with its real lifecycle rules, by any caller on any machine, and the VM harness can run whole lifecycles without a sequencer of its own.
- The agent's reboot path gets end-to-end coverage against a real kernel before the release that depends on it.
- `agent run`'s flags, run spec and its schema versions, run state, result file and exit codes become a public contract of the agent, versioned with it under `docs/operations/versioning.md`, and documented in `agent/README.md` when phase 1 ships.
- The per-stage CLI gains plain JSON in `INTERRUPT_DATA`; base64 stays until no supported operator sends it.
- Until phase 2, `lifecycle` and the operator hold the same graph twice. The phase 2 equivalence tests are what end that.

## Open questions

1. Whether `--changed` should become a configmap digest stored in the run state, letting the agent compute changed keys itself. This is the first step towards derived intent.
2. Whether the result file should also stream per-stage events (JSON lines on a file descriptor) for callers that want progress, which may settle phase 3's status question.
3. A read-only `verify` intent that re-runs the `-check` stages of an applied package. Phase 1 keeps the run state and result format able to carry it; whether every `-check` is safe to re-run, or packages opt in, is open. Preflight (#981) asks a similar question of checks.
4. Whether steps should learn that the agent is stopping, for example through a deadline in an environment variable or a forwarded signal. Today a step is never signalled, so a step that traps signals behaves the same as one that does not; forwarding a signal would change that for every package.

## References

- #194 (Go agent), #819 and #820 (package artifacts and the proposed `artifact/` module), #868, #869, #870, #887, #1024, #1046, #1069 (interrupt and batch issues), #1022 and #1038 (agent interrupt tests)
- #437 and #438 (Skyhook-named on-host paths and environment variables), #1056 (node metadata passed to the agent), #981 (preflight)
- [ADR-003: Package execution as Jobs](003-package-execution-as-jobs.md)
- [Interrupt flow](../architecture/interrupt-flow.md), [Lifecycle](../architecture/lifecycle.md), [Ordering](../architecture/ordering.md)
- NVIDIA/nodewright-packages#152 and #153: VM end-to-end tests for packages, where the release-gate scenarios are expected to run and whose agent executor is the first consumer of `agent run`
