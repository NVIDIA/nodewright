# Downgrade With `uninstall.enabled: false` Preserves State Test

## Purpose

Covers the complement of `downgrade-after-uninstall`. For packages with
`uninstall.enabled: false` (or unset), the webhook accepts version downgrades
without requiring a prior uninstall — no `uninstall.sh` was ever declared, so
the gate doesn't apply. However, the old version's `nodeState` entry must be
**preserved alongside** the new one: D2 semantics say a non-absent entry
signals "files still on host," and without explicit uninstall, the old
version's files are still there.

## Test Scenario

1. Install the package at v2.1.4 with `uninstall.enabled: false`, wait for
   complete.
2. Update the spec to v1.2.3 (downgrade). The webhook accepts without the
   uninstall gate.
3. Assert the node's `nodeState_<name>` annotation contains **both** entries
   — `mypkg|2.1.4` (old, preserved) and `mypkg|1.2.3` (new, complete).
4. Assert the Skyhook returns to `status: complete`.

## Key Features Tested

- Downgrade allowed when `uninstall.enabled: false` without the uninstall
  gate
- Old version's `nodeState` entry preserved (not overwritten) alongside the
  new version — operator signal that old files remain on host
- New version installs cleanly to `stage=config, state=complete`

## Files

- `chainsaw-test.yaml` — Main test: install v2 → downgrade to v1 → assert both versions tracked
- `skyhook.yaml` — Initial v2.1.4 Skyhook, `uninstall.enabled: false`
- `update-downgrade.yaml` — Patch changing version to v1.2.3
