---
summary: "The CLI ships separately: keep it compatible with older operators and update it with any surface it reads"
paths:
  - "operator/cmd/cli/**"
  - "operator/internal/cli/**"
  - "operator/api/**"
  - "operator/internal/controller/annotations.go"
  - "operator/internal/controller/nodewright_controller.go"
  - "operator/internal/wrapper/**"
  - "docs/user-guide/cli.md"
---
# CLI and operator contract

## Read before editing

- `docs/user-guide/cli.md`: the CLI reference, including the compatibility matrix.
- `docs/operations/versioning.md`: per-component semver, which a compatibility break has to follow.

## The CLI

`kubectl nodewright` is a kubectl plugin built from `operator/cmd/cli/` (binary `kubectl-nodewright`, `make build-cli` from `operator/`). Subcommands under `operator/cmd/cli/app/`: `node`, `package`, `deploymentpolicy`, plus `reset`, `lifecycle` (pause, resume, disable, enable), `version` and `migrate`. Commands go through `operator/internal/cli/client` rather than calling the apiserver directly. Recent operators pause and disable through annotations (`nodewright.nvidia.com/pause`, `nodewright.nvidia.com/disable`), not spec fields; the legacy Skyhook uses the `skyhook.nvidia.com/*` equivalents.

## Keep the CLI and operator in sync

The CLI is a client of the operator. It reads and writes CRs, annotations (`nodewright.nvidia.com/pause`, `…/disable`, `…/nodeState_*` and others), status fields, and well-known label and finalizer names. When an operator change touches any of these (a renamed annotation, a new CRD field the CLI should surface, a changed status shape, a removed feature, a new lifecycle operation), update the CLI **in the same PR**, or say in the PR why the CLI is unaffected. Review rejects a contract change without one or the other.

## Stay compatible with older operators

The CLI is distributed separately, and users upgrade it and the operator on different schedules, so it must work against every supported operator version. When a feature depends on new operator behavior, detect it (`version`, CR schema presence, an annotation probe) and either fall back gracefully or fail with a clear error naming the minimum operator version. Never silently no-op. Every command with version-gated behavior appears in the compatibility matrix in `docs/user-guide/cli.md`, which is authoritative.

## When a break is unavoidable

1. Emit a runtime warning when the CLI detects an incompatible operator, naming the version it saw and the minimum it needs.
2. Update the compatibility matrix in `docs/user-guide/cli.md` and describe the break in prose.
3. Record the break with a migration note in `operator/cmd/cli/RELEASE_NOTES.md` (`CHANGELOG.md` is generated from commits).
4. Bump the CLI major or minor version as `docs/operations/versioning.md` requires.

Operators in production can lag the CLI by months. A CLI that misbehaves without explanation is worse than one that refuses to run.
