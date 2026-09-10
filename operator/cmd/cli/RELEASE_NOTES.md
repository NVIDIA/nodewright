# Release Notes

Human-authored highlights, behavior changes, and upgrade steps for the CLI.
For the full commit-level log see CHANGELOG.md.

## Unreleased

## cli/v0.4.0 - 2026-08-31

### Changed

- **`node status` now reports two cordon columns, `CORDONED` and
  `RUNTIME-REQUIRED-CORDON`.** They appear in the default table, in `-o wide`, and
  as the `cordoned` and `runtimeRequiredCordon` fields in `-o json` / `-o yaml`.
  `CORDONED` is the node's `spec.unschedulable`, whatever applied it.
  `RUNTIME-REQUIRED-CORDON` is the presence of the
  `nodewright.nvidia.com/runtimeRequiredCordon` annotation, which the operator sets
  when a `runtimeRequired` NodeWright also sets `spec.runtimeRequiredCordonAfter:
  true`. Read together they distinguish a cordon the operator will release itself
  from one that persists across later NodeWright interrupts and
  `kubectl nodewright reset` until an external actor clears it. See
  [docs/user-guide/cli.md](../../../docs/user-guide/cli.md#node-status-columns) for
  the columns and
  [docs/user-guide/runtime-required.md](../../../docs/user-guide/runtime-required.md#what-happens-when-the-taint-is-removed)
  for how to release the persistent cordon.

  **If you parse the table output positionally, check your parser.** Both columns
  are appended, so the existing columns keep their positions, but anything reading
  the last field now gets a boolean. JSON and YAML consumers are unaffected — the
  fields are additive.

  No operator version gate applies. An operator without
  `spec.runtimeRequiredCordonAfter` never sets the annotation, so
  `RUNTIME-REQUIRED-CORDON` reads `false` everywhere, which is accurate rather than
  degraded: no such cordon can exist.

## cli/v0.3.0 - 2026-08-17

### Breaking Changes

- The plugin is now invoked as `kubectl nodewright` (binary `kubectl-nodewright`)
  instead of `kubectl skyhook`. Reinstall the plugin under the new name; update any
  scripts or aliases that call `kubectl skyhook`.
- The CLI now targets the `nodewright.nvidia.com` API group and the `NodeWright`
  kind, and it reads and writes `nodewright.nvidia.com/*` node annotations. It no
  longer works against operators that only serve the legacy `skyhook.nvidia.com`
  group and `Skyhook` kind. Migration: upgrade the operator to a
  NodeWright-capable version before using this CLI; an older Skyhook-only
  operator is unsupported.

### Changed

- **The `--namespace` default moved from `skyhook` to `nodewright`, and the CLI now
  discovers the operator's namespace instead of assuming it.** The documented install
  namespace moved with the rename, but a namespace cannot be renamed in place, so
  installs that predate the rename legitimately stay in `skyhook` forever. A blind
  default change would have answered "not found" for every one of them.

  With no `--namespace`, the CLI now checks `nodewright`, then `skyhook` (printing a
  one-line note to stderr when the legacy namespace is what answered), then sweeps
  cluster-wide for an operator in some other namespace, and only then falls back to
  `nodewright` so the command's own lookup reports the specific miss. An explicit
  `--namespace` is always used verbatim, with no discovery and no note.

  Within the set of operators this CLI supports at all (NodeWright-capable ones, per
  the Breaking Changes above), the namespace change introduces **no** new
  incompatibility: the namespace is only used to locate the operator Deployment and
  package pods, and the `NodeWright` and `DeploymentPolicy` CRDs are cluster-scoped,
  so no command changes which CRs it sees. This says nothing about Skyhook-only
  operators, which remain unsupported for unrelated reasons. See
  [docs/user-guide/cli.md](../../../docs/user-guide/cli.md#namespace-resolution) for the full resolution
  order and the situation matrix.

  The cluster-wide sweep needs cluster-scoped Deployment list permission. Users
  without it lose only that last discovery step; the `nodewright`/`skyhook` probes
  and explicit `--namespace` are unaffected.

- Commands that read or write `NodeWright` resources now run a preflight check on
  the served API groups. When the cluster serves only the legacy
  `skyhook.nvidia.com` group and not `nodewright.nvidia.com`, they fail fast with
  a clear, actionable error naming both groups and telling you to upgrade to a
  NodeWright-capable operator, instead of a confusing `NotFound`. `version` does
  not run the preflight.

- `reset` and `node reset` now select nodes with any resettable Skyhook
  metadata, including status, cordon, and drain-start metadata. Nodes with a
  malformed `nodeState` annotation are reset with an empty package state instead
  of being skipped.
