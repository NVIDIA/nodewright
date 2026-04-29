# Uninstall Mixed Packages Test

## Purpose

Covers a Skyhook with two packages — one `uninstall.enabled: true`
(`pkg-remove`) and one `uninstall.enabled: false` (`pkg-keep`) — and
verifies two independent properties:

1. **Targeted uninstall**: flipping `apply: true` on `pkg-remove` runs an
   uninstall pod for that package only; `pkg-keep` is unaffected and
   remains at `stage=config, state=complete` on the node.
2. **State preservation on spec removal for `enabled: false` packages**:
   after `pkg-remove` is fully uninstalled, removing `pkg-keep` from the
   spec is allowed by the webhook, but per D2 semantics the node-state
   entry for `pkg-keep` must remain — its files are still on the host
   because no `uninstall.sh` ran. This preservation is what commit
   `66ba20bf` put in place.

## Test Scenario

1. Install both packages, wait for Skyhook `status: complete`.
2. Set `uninstall.apply: true` on `pkg-remove`. Assert:
   - An uninstall pod is created for `pkg-remove-2.1.4` with the
     `uninstall` / `uninstall-check` init containers.
   - Once it completes, `nodeState` contains **only** `pkg-keep|2.1.4` at
     `stage=config, state=complete`.
3. Remove `pkg-keep` from `spec.packages`. Assert:
   - `pkg-keep`'s node-state entry is **still present** (operator stops
     tracking but preserves the entry as a marker).
   - Skyhook is `status: complete` (no tracked packages remaining).

## Key Features Tested

- Per-package uninstall targeting (uninstall pod labeled for the specific
  package only)
- Non-interference: other packages in the Skyhook stay at their installed
  stage during an uninstall
- Webhook allows spec removal of `uninstall.enabled: false` packages
- `nodeState` preservation for `enabled: false` removal (D2 semantic: non-
  absent = files still on host)

## Files

- `chainsaw-test.yaml` — Main test: install both → uninstall one → remove the other from spec
- `skyhook.yaml` — Skyhook with `pkg-remove` (enabled: true) and `pkg-keep` (enabled: false)
- `update-uninstall-one.yaml` — Patch setting `uninstall.apply: true` on `pkg-remove`
- `update-remove-keep.yaml` — Patch removing `pkg-keep` from `spec.packages`
