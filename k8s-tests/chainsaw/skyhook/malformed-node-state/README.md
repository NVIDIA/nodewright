# Malformed Node State Test

## Purpose

Verifies that a corrupted `nodeState_<skyhook>` annotation surfaces as a
dedicated user-visible `NodeStateMalformed` condition on the Skyhook (not a
silent reconcile error), and that repairing the annotation clears the
condition on the next reconcile.

Two controller changes make this work:
1. `NewSkyhookNodeOnly` (in `internal/wrapper/node.go`) no longer aborts
   construction on a parse failure — it returns a wrapper whose cached state
   is empty so downstream `State()` callers re-encounter the error. Without
   this, `BuildState` would abort the entire reconcile before any condition
   could be set, deadlocking the operator on the malformed annotation.
2. `UpdateNodeStateMalformedCondition` (new, in
   `internal/controller/cluster_state_v2.go`) iterates the Skyhook's nodes,
   collects the names of any whose nodeState annotation cannot be parsed,
   and sets `NodeStateMalformed` with `Reason=ParseError`
   and a message listing them. The condition is **stage-agnostic** —
   malformed state affects install, upgrade, uninstall, and finalizer
   decisions, so it deserves its own signal rather than being folded into
   `UninstallFailed`. Node names longer than 10 characters are truncated to
   the first 10 chars plus `"..."` so the message stays compact on
   clusters with DNS-style node names.

## Test Scenario

1. Install a minimal Skyhook (one package, no uninstall configured). Wait
   for `status: complete`.
2. Save the valid `nodeState_<skyhook>` annotation to
   `/tmp/malformed-node-state-backup.json`, then overwrite it on the test
   node with invalid JSON (`{not-valid-json`).
3. Assert the Skyhook has condition
   `NodeStateMalformed` with `status=True`,
   `reason=ParseError`, and a message containing
   `nodeState annotation cannot be parsed on 1 node(s)`.
4. Restore the saved annotation.
5. Assert the `NodeStateMalformed` condition is absent
   (`length(conditions[?type == '...NodeStateMalformed']) == 0`) and the
   Skyhook is `status: complete`.

No uninstall is required — that's the point of the dedicated condition.

## Key Features Tested

- `NewSkyhookNodeOnly` tolerates malformed state annotations — reconcile
  proceeds far enough to surface the problem instead of dead-looping
- `UpdateNodeStateMalformedCondition` lists every affected node
- Truncation: node names longer than 10 characters appear as `<first10>...`
- `Reason=ParseError` (distinct from `UninstallFailed` / `UninstallPodFailing`)
- Condition cleared automatically once every node's state is parseable again

## Files

- `chainsaw-test.yaml` — Main test: install → corrupt → assert condition → repair → assert condition gone
- `skyhook.yaml` — Minimal Skyhook with one package (`mypkg:2.1.4`, no uninstall block)
