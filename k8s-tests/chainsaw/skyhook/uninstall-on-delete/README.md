# Uninstall On Delete (Finalizer) Test

## Purpose

Covers finalizer-driven uninstall: when a Skyhook CR is deleted, the
finalizer runs an uninstall pod on every node for every
`uninstall.enabled: true` package, waits for completion, and only then
cleans up labels/annotations/conditions. Packages with
`uninstall.enabled: false` (or unset) are left alone — **and their
`nodeState` entry is preserved** after the CR is gone, because no
`uninstall.sh` ran so the files remain on the host (D2 semantic:
non-absent = files still on host). This preservation is what the fix in
`CleanupSCRMetadata` guarantees: the `nodeState_<skyhook>` annotation is
not deleted if it still contains entries.

## Test Scenario

1. Install a Skyhook with one `uninstall.enabled: true` package
   (`enabled-pkg`) and one `uninstall.enabled: false` package
   (`disabled-pkg`), wait for complete.
2. `kubectl delete skyhook --wait=false`. Assert:
   - An uninstall pod is created for `enabled-pkg-2.1.4` (init containers
     `enabled-pkg-init`, `enabled-pkg-uninstall`, `enabled-pkg-uninstallcheck`).
   - No uninstall pod is created for `disabled-pkg`.
3. Poll until the CR is gone (finalizer has completed).
4. Assert the node's `nodeState_uninstall-on-delete` annotation is **still
   present** and contains `disabled-pkg|2.1.4` at `stage=config,
   state=complete` — the preserved record of files remaining on the host.

## Key Features Tested

- Finalizer waits on uninstall completion before allowing CR teardown
- Only `uninstall.enabled: true` packages get an uninstall pod during CR
  deletion
- `CleanupSCRMetadata` preserves the `nodeState` annotation when it still
  contains packages that weren't uninstalled
- Status annotations, labels, and conditions are still cleaned (only
  `nodeState` and `version` are preserved alongside)

## Files

- `chainsaw-test.yaml` — Main test: install → delete CR → assert disabled-pkg state remains after CR is gone
- `skyhook.yaml` — Skyhook with `enabled-pkg` (uninstall.enabled: true) and `disabled-pkg` (uninstall.enabled: false)
