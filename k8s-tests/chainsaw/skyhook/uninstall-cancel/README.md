# Uninstall Cancel Test

## Purpose

Verifies cancellation semantics: flipping `uninstall.apply: true` → `false`
while the package is mid-uninstall (at `StageUninstall`) resets the package
back into the install pipeline, and the node returns to `complete` with the
package re-installed. The webhook permits the transition with a warning, not
a rejection.

Cancellation is **uncancellable** once the package has reached
`StageUninstallInterrupt` — that edge case is not covered here; this test
targets the reversible window.

## Test Scenario

1. Install the package with `uninstall.enabled: true`, wait for complete.
2. Flip `uninstall.apply: true`. Wait for the Skyhook label to show
   `status_<name>: in_progress` (uninstall has started).
3. Sleep 15s so the uninstall pod has time to be running (keeping the test
   in the cancellable window before the interrupt phase).
4. Flip `uninstall.apply: false` via the cancel patch.
5. Assert the node returns to `status_<name>: complete` and `nodeState`
   shows the package back at `stage=config, state=complete`.
6. Assert the Skyhook is `status: complete`.

## Key Features Tested

- `HandleCancelledUninstalls` path: StageUninstall → reset to install
  pipeline
- Package re-installs cleanly after cancel
- Skyhook returns to `complete` without manual intervention
- Webhook accepts the `apply: true` → `false` transition (warning, not
  rejection)

## Files

- `chainsaw-test.yaml` — Main test: install → trigger → cancel → reinstall
- `skyhook.yaml` — Initial Skyhook, `uninstall.enabled: true`
- `update-trigger-uninstall.yaml` — Patch setting `uninstall.apply: true`
- `update-cancel-uninstall.yaml` — Patch setting `uninstall.apply: false`
