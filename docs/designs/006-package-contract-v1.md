# ADR-006: The package contract for v1

## Status

**Proposed**, 2026-11-24. Answers #437 and #438.

## Decision summary

A package declares everything about itself in one manifest, `config.json` schema v2: what it is and the steps it runs, which callers never change, and default settings for how it runs, which a caller can override. The settings are the ones the package author knows: its interrupt and config interrupts, `configMap` and `env`, `dependsOn`, its stage timeout and graceful shutdown, and whether it supports uninstall. On Kubernetes the NodeWright CR's per-package fields are the override; off Kubernetes `agent run` takes them in a file ([ADR-005](005-agent-owned-package-lifecycle.md)). Cluster concerns (image, resources, placement, budgets) stay on the CR and never appear in a package.

Steps of a v2 package see `NODEWRIGHT_*` variables; a v1 package's steps keep the `SKYHOOK_*` ones. The paths keep their names. v1 packages keep working with no change.

## Context

A package is described in two places. `config.json` in the package image holds its identity (`package_name`, `package_version`), `root_dir` (files the agent copies onto the host), its steps per mode, and `expected_config_files`, the configmap keys without which the agent fails. The NodeWright CR holds, per package, settings the package author knows (`interrupt`, `configInterrupts`, `configMap`, `env`, `dependsOn`, `stageTimeout`, `gracefulShutdown`, `uninstall`) next to cluster concerns (`image`, `containerSHA`, `agentImageOverride`, `resources`). The author knows which interrupt a kernel package needs, which keys need a service restart and how long a driver build takes, but cannot say so: every CR that uses the package restates it, and a CR that gets it wrong rolls out a kernel without the reboot. `config.json` has a per-step `requires_interrupt` that the agent parses but never acts on.

The two halves also disagree on style: `config.json` is snake_case, the CR camelCase.

The names are the other half of the contract. Every package image reads `/skyhook-package/config.json`. Steps see five `SKYHOOK_*` variables: `SKYHOOK_DIR`, set by the agent, and `SKYHOOK_RESOURCE_ID`, `SKYHOOK_NODE_ORDER`, `SKYHOOK_ROOT_DIR` and `SKYHOOK_LOG_DIR`, set by the operator on the Job before it knows anything about the package. A rename breaks every existing package at once (#437, #438). The operator keeps flags, history and logs on every node under `/var/lib/skyhook` and `/var/log/skyhook` (`COPY_DIR_ROOT`, `AGENT_LOG_ROOT`), log collectors on user clusters watch those paths, and the agent falls back to `/etc/skyhook` when no state root is given.

## Decision

1. **One manifest per package.** `config.json` schema v2 holds identity, `root_dir` and steps, which no caller overrides, and a `settings` section with the package's defaults for the settings listed above, with the CR's field names. Schema v2 uses camelCase throughout.

2. **Only settings can be overridden.** A caller supplies a partial `settings`: on Kubernetes the CR's per-package fields, off Kubernetes the `agent run` settings file. Cluster concerns are never part of a package.

3. **Overrides merge by field.** `interrupt`, `stageTimeout` and `gracefulShutdown` are replaced whole; `configInterrupts`, `configMap`, `dependsOn` and `env` merge by key (by `name` for `env`), the override's value winning. An override can remove an interrupt the package declares.

4. **Shapes follow the CR where the agent can honor them.** `configMap` is the CR's map of key to contents, and a `requiredConfig` list of keys the caller must supply replaces `expected_config_files`. `env` in a package or the settings file takes only `name` and `value`; `valueFrom` stays a CR-only override that Kubernetes resolves. The package's uninstall support is the default for the CR's `uninstall.enabled`; `uninstall.apply` stays a user action. `requires_interrupt` is dropped.

5. **A caller reads the defaults it acts on before a run.** The interrupt, config interrupts, `dependsOn`, timeouts and uninstall support shape what a caller does before or around a run, so it reads them without running the package: `agent run` reports them (ADR-005 decision 1), and the operator reads them from a package artifact it inspects (#819, #820). The agent applies `configMap` and `env` defaults itself. A legacy image cannot be inspected, so on Kubernetes the CR stays the only source of those fields for it, as today.

6. **Steps of a v2 package see NodeWright variable names.** The operator keeps passing `SKYHOOK_*` variables to the agent, since it sets them before it knows the schema version. For a v2 package the agent gives its steps the five step-visible variables as `NODEWRIGHT_*` and removes the `SKYHOOK_*` names; a v1 package's steps see what they see today.

7. **The paths keep their names and are documented as stable.** The payload stays at `/skyhook-package`: the operator mounts configmaps and node metadata inside it before it can know a legacy image's schema version, and package build files and step scripts hard-code it. `/var/lib/skyhook`, `/var/log/skyhook` and the `/etc/skyhook` fallback stay too: moving them strands completed work on every node and breaks users' log collection.

8. **v1 stays supported.** The agent reads both schema versions. A v1 package has no defaults, so the CR or the `agent run` caller supplies every setting, as today.

## Non-goals

- Cluster concerns in a package: image, resources, placement, budgets.
- Overriding identity, `root_dir`, steps, return codes or idempotence.
- Package artifacts and their inspection, which #819 and #820 define.
- Renaming the paths, or the variables the operator passes to the agent.

## Alternatives

- **Keep `config.json` and the CR apart and add a run spec for `agent run`.** A third shape, and the package author still cannot declare defaults.
- **Merge, and let a caller override anything.** Overriding steps breaks what the author guarantees, and completion flags stop matching the work they record.
- **Give every package both `SKYHOOK_*` and `NODEWRIGHT_*`.** Needs a deprecation window and a way to tell which name a script read; the schema version settles it per package.
- **Move a v2 package's payload to `/nodewright-package`.** The operator mounts into the payload directory before it can read a legacy image's schema version, and existing build files would all change.
- **Rename the host directories now with a dual read.** Completed work survives, but log collectors and the operator's configuration break without a migration plan.

## Consequences

- An `agent run` caller restates nothing the package author knows.
- On Kubernetes, CRs keep restating the settings the operator acts on before a run until it inspects packages, which covers artifacts only (#819); `configMap` and `env` defaults work as soon as the agent ships v2.
- A v2 package needs an agent that reads v2; an older agent fails its stage, so a package that must run on older agents stays v1.
- The agent maintains two schema versions and two sets of step variable names until a later decision retires v1.

## Open questions

1. How a CR override removes an interrupt the package declares, since an absent field and a removed one look the same.
2. Whether a CR can enable uninstall for a package that declares it unsupported.
3. When schema v1 is retired.

## References

- #437, #438: the names; #819, #820: package artifacts and their metadata; #310: the rename.
- [ADR-001](001-uninstall-enhancement.md): `uninstall.enabled`; [ADR-005](005-agent-owned-package-lifecycle.md): `agent run`, which takes the override off Kubernetes.
- [`agent/internal/config/schemas/v1/`](../../agent/internal/config/schemas/v1/): schema v1.
