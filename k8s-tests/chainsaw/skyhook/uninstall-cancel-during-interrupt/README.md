# Cancel During Uninstall Interrupt Test

## Purpose

Covers the edge case the plain `uninstall-cancel` test explicitly does not:
flipping `uninstall.apply: true → false` while the package is at
`StageUninstallInterrupt`. That phase is uncancellable — the interrupt pod
has fired and must run to completion — so the controller must keep the
`skyhook.nvidia.com/UninstallInProgress` condition set until the cycle
actually exits on the node, even though the spec no longer requests
uninstall.

This guards the fix to `UpdateUninstallConditions` where the per-package
loop now consults `nodeState.IsUninstallCycleInProgress(...)` as the source
of truth (rather than the spec-only `pkg.IsUninstalling()` gate). Before
the fix, cancelling during `StageUninstallInterrupt` would cause the
condition to clear prematurely.

## Test Scenario

1. Install `mypkg:2.1.4` with `uninstall.enabled: true, apply: false`, a
   service interrupt (`services: [cron]`), and `SLEEP_LEN=5` in the package
   env (seconds the agentless image sleeps per step — widens the
   `StageUninstallInterrupt` window to a comfortable margin for the cancel
   patch to land and the condition assertion to run). Wait for
   `status: complete`.
2. Flip `uninstall.apply: true`. Wait until the node reaches
   `stage=uninstall-interrupt` on the package. Assert
   `UninstallInProgress` is `status=True, reason=UninstallInProgress`.
3. Flip `uninstall.apply: false` while still at `StageUninstallInterrupt`.
   Assert the node is still at `uninstall-interrupt` AND the
   `UninstallInProgress` condition remains `True` — proves the condition
   tracks the node cycle, not the spec apply flag.
4. Let the uncancellable interrupt finish. The package re-installs (because
   `apply=false` engaged the install pipeline after the cycle exited) and
   returns to `stage=post-interrupt, state=complete`. Assert the
   `UninstallInProgress` condition is absent.

## Key Features Tested

- `UpdateUninstallConditions` sources from `nodeState` (not spec) while a
  cycle is active
- `UninstallInProgress` stays `True` across an uncancellable cancel attempt
- After the cycle exits, the condition is cleared
- Package re-installs cleanly following a cancel-during-interrupt

## Teardown Note

A final `drain-uninstall-before-cleanup` step re-triggers uninstall and
waits for the package to be absent from `nodeState` before chainsaw's
built-in CR deletion. With an empty `nodeState`, the finalizer's Phase 2
scan finds no pending uninstall work and falls straight through to
Phase 3 cleanup (uncordon, remove `skyhook.nvidia.com/*` labels,
annotations, and conditions from the node) — fast enough for chainsaw's
default context deadline. Deleting the CR without this drain step would
kick off the finalizer-driven uninstall + interrupt cycle again and
leave shared-node annotations/labels stale until the controller caught
up.

## Files

- `chainsaw-test.yaml` — Main test: install → trigger → reach interrupt → cancel → assert condition sticks → wait for cycle exit → assert cleared → drain uninstall before teardown
- `skyhook.yaml` — Skyhook with `uninstall.enabled: true`, service interrupt, `SLEEP_LEN=5` (seconds per step)
- `update-trigger-uninstall.yaml` — Patch setting `uninstall.apply: true`
- `update-cancel-uninstall.yaml` — Patch setting `uninstall.apply: false`
