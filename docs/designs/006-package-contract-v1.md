# ADR-006: The package contract for v1

## Status

**Proposed**, 2026-11-24. Answers #437 and #438.

## Decision summary

A package declares everything about itself in one manifest, `config.json` schema v2: what it is and the steps it runs, which callers never change, and default settings for how it runs (its interrupt, config interrupts, `configMap` and `env`), which a caller can override. On Kubernetes the NodeWright CR's per-package fields are the override, with the names and shapes they have today; off Kubernetes `agent run` takes the same fields ([ADR-005](005-agent-owned-package-lifecycle.md)). Cluster concerns stay on the CR and never appear in a package.

Schema v2 is also the boundary for the NodeWright names: a v2 package's steps get `NODEWRIGHT_*` variables, and a v1 package's keep the `SKYHOOK_*` ones. The paths keep their names: the package payload stays at `/skyhook-package`, and the host state directories stay where they are.

v1 packages keep working with no change.

## Context

A package is described in two places. `config.json` in the package image holds its identity (`package_name`, `package_version`), its steps per mode, and `expected_config_files`, the configmap keys without which the agent fails. The NodeWright CR holds, per package, how it runs (`interrupt`, `configInterrupts`, `configMap`, `env`) next to cluster concerns (`image`, `containerSHA`, `agentImageOverride`, `dependsOn`, `resources`, `gracefulShutdown`, `stageTimeout`, `uninstall`). The package author knows which interrupt a kernel package needs and which keys need a service restart, but cannot say so: every CR that uses the package restates it, and a CR that gets it wrong rolls out a kernel without the reboot. `config.json` has a per-step `requires_interrupt` that the agent parses but never acts on.

The two halves also disagree on style: `config.json` is snake_case, the CR camelCase.

The names are the other half of the contract. Every package image reads `/skyhook-package/config.json`, and step scripts read up to 14 `SKYHOOK_*` variables. A package has no way to say which names it expects, so a rename breaks every existing package at once (#437, #438). Separately, the operator keeps flags, history and logs on every node under `/var/lib/skyhook` and `/var/log/skyhook` (`COPY_DIR_ROOT`, `AGENT_LOG_ROOT`), and log collectors on user clusters watch those paths.

## Decision

1. **One manifest per package.** `config.json` schema v2 holds identity and steps, which no caller overrides, and a `settings` section with the package's defaults: `interrupt`, `configInterrupts`, `configMap` and `env`, with the CR's names and shapes. Schema v2 uses camelCase throughout.

2. **Only settings can be overridden.** A caller supplies a partial `settings`: on Kubernetes the CR's per-package fields, off Kubernetes the file `agent run` takes. Identity and steps cannot be overridden, and cluster concerns are never part of a package.

3. **Overrides merge by field.** `interrupt` is replaced whole. `configInterrupts`, `configMap` and `env` merge by key, and an override's value for a key replaces the default whole. An override can remove an interrupt the package declares. A step's own `env` stays part of the step.

4. **A package lists the configmap keys it reads.** Each key in `settings.configMap` has a default or is marked required, which replaces `expected_config_files`. `requires_interrupt` is dropped.

5. **Interrupt defaults must be readable before the package runs.** A caller drains and merges interrupts before `apply`, so it needs the effective interrupt without running the package. The agent applies `configMap` and `env` defaults itself at run time. The operator honors a package's interrupt defaults only once it can read them from the package's metadata without running it (#820). Until then the CR stays the only source of interrupts on Kubernetes, and the agent logs a warning when a package declares an interrupt that the effective settings do not include.

6. **The schema version selects the variable names.** A v2 package's steps get `NODEWRIGHT_*` variables; a v1 package's get the `SKYHOOK_*` variables they have today. The agent never exports both, because the schema version already says which names the package's scripts read.

7. **The paths keep their names and are documented as stable.** The payload stays at `/skyhook-package`: the operator mounts configmaps and node metadata inside it before it knows a package's schema version, so the path cannot vary by version. The host state directories `/var/lib/skyhook` and `/var/log/skyhook` stay too: moving them strands completed work on every node and breaks users' log collection, and needs its own decision with a migration.

8. **v1 stays supported.** The agent reads both schema versions. A v1 package has no defaults, so the CR or the `agent run` caller supplies every setting, as today.

## Non-goals

- Cluster concerns in a package: placement, `dependsOn`, resources, timeouts, budgets.
- Overriding steps, return codes or idempotence.
- Package artifact metadata itself, which #820 defines.
- Renaming the payload path or the host state directories (decision 7).

## Alternatives

- **Keep `config.json` and the CR apart and add a run spec for `agent run`.** A third shape, and the package author still cannot declare defaults.
- **Merge, and let a caller override anything.** Overriding steps breaks what the author guarantees, and completion flags stop matching the work they record.
- **Export both `SKYHOOK_*` and `NODEWRIGHT_*` to every package.** Needs a deprecation window and a way to tell which name a script read; the schema version settles it per package for free.
- **Move a v2 package's payload to `/nodewright-package`.** The operator mounts into the payload directory before it can read the schema version, so it would mount into both or probe the image first.
- **Rename the host directories now with a dual read.** Completed work survives, but log collectors and the operator's configuration break without a migration plan.

## Consequences

- A package works correctly with an empty override, so `agent run` callers and new CRs restate nothing the author knows.
- Package authors move to schema v2 to get defaults and the NodeWright names, one package at a time.
- On Kubernetes, interrupt defaults wait for #820; `configMap` and `env` defaults work as soon as the agent ships v2.
- The agent maintains two schema versions and two sets of names until v1 is retired by a later decision.

## Open questions

1. How a CR override removes an interrupt the package declares, since an absent field and a removed one look the same.
2. Whether `resources` belongs in a package's defaults: the author knows what a package needs, but resources are a cluster concern.
3. When schema v1 is retired.

## References

- #437, #438: the names; #820: package artifact metadata; #310: the rename.
- [ADR-005](005-agent-owned-package-lifecycle.md): `agent run`, which takes the override off Kubernetes.
- [`agent/internal/config/schemas/v1/`](../../agent/internal/config/schemas/v1/): schema v1.
