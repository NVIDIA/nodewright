# Upgrade Test

## Purpose

Covers the package **upgrade path** in isolation: a version bump must run
the `upgrade` stage (not `apply`), swap the ConfigMap to the new version,
fire the interrupt cycle, and return the Skyhook to `complete` with the
package at `stage=post-interrupt, state=complete`.

Narrowed from the original combined uninstall/downgrade/upgrade test —
uninstall and downgrade scenarios have their own dedicated tests on this
branch (`explicit-uninstall`, `uninstall-on-delete`, `uninstall-mixed-packages`,
`uninstall-cancel`, `uninstall-fix-config`, `downgrade-after-uninstall`,
`downgrade-enabled-false-preserves-state`), so this test focuses only on
upgrade.

## Test Scenario

1. Reset node state and install the package at `nullptr:2.0.0` with a
   service-type interrupt. Assert the full install cycle (apply →
   post-interrupt → complete) via `assert-install.yaml`.
2. Update the spec to `nullptr:2.0.1` with a changed ConfigMap. Assert:
   - Node enters `in_progress` with the new version's nodeState key
     (`nullptr|2.0.1`) at `stage=upgrade, state=in_progress` — proves the
     upgrade stage is running, not apply.
   - The package pod's `upgrade` and `upgrade-check` init containers are
     created with the expected args (`upgrade` / `upgrade-check`).
   - Node returns to `complete` with the new version at
     `stage=post-interrupt, state=complete`.
   - Skyhook status reaches `complete` with `packageList: nullptr:2.0.1`
     and `observedGeneration: 4`.
   - A new ConfigMap `...-nullptr-2.0.1` exists with the updated content
     (per-version ConfigMap naming).

## Key Features Tested

- `upgrade` lifecycle stage fires on version bump (vs. `apply` on first install)
- Pod init containers use the upgrade / upgrade-check args
- Per-version ConfigMap creation (new version gets a fresh ConfigMap with
  current spec content)
- Full interrupt cycle post-upgrade (cordon, interrupt pod, post-interrupt)
- Skyhook status recovers to `complete` after the upgrade finishes

## Files

- `chainsaw-test.yaml` — Main test: initial install → upgrade, with inline
  node/pod/skyhook assertions for sequential ordering
- `skyhook.yaml` — Initial Skyhook with `nullptr:2.0.0`
- `update.yaml` — Patch bumping to `nullptr:2.0.1` with changed ConfigMap
- `assert-install.yaml` — Initial install assertions (node lifecycle, skyhook
  status, ConfigMap) asserted in parallel
- `assert-cm-update.yaml` — New ConfigMap assertion after upgrade
