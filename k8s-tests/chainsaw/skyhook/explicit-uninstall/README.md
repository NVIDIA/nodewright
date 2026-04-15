# Explicit Uninstall Test

## Purpose

End-to-end test for the explicit uninstall lifecycle: a user sets
`uninstall.apply: true` on an `uninstall.enabled: true` package, the operator
runs an uninstall pod (with the full package config — not a synthetic stub),
executes the post-uninstall interrupt, and removes the package from
`nodeState` (absent = uninstalled per D2). The package can then be removed
from spec without webhook rejection.

This is the canonical happy-path test for the feature; other uninstall tests
cover edge cases (cancel, mixed packages, finalizer-driven, failure
recovery).

## Test Scenario

1. Install the package with `uninstall.enabled: true`, wait for
   `stage=post-interrupt, state=complete`.
2. Flip `uninstall.apply: true`. Assert:
   - Uninstall pod created with the package's ConfigMap mounted as a volume
     and the user-supplied `MY_VAR=hello` env var on the uninstall and
     uninstall-check containers.
   - Stage advances to `uninstall-interrupt / in_progress`; node gets the
     unschedulable taint (drain).
   - An interrupt pod is created (package has `interrupt.type: reboot`).
   - After the full cycle, `nodeState_<name>` is `'{}'` (package absent).
   - Skyhook returns to `status: complete, completeNodes: 1/1`.
3. Remove the package from spec. Webhook permits the change because the
   package was fully uninstalled; Skyhook stays `status: complete` with
   `packageList: ""`.

## Key Features Tested

- Uninstall pod uses full package config (ConfigMap volume + env propagation)
- `StageUninstall` → `StageUninstallInterrupt` transition
- Interrupt pod creation and node cordon/drain during uninstall
- `RemoveState` on successful completion (absent = uninstalled)
- Skyhook status recovers to `complete` after uninstall
- Webhook allows spec removal once the package is fully uninstalled

## Files

- `chainsaw-test.yaml` — Main test: install → trigger-uninstall → remove-from-spec
- `skyhook.yaml` — Initial Skyhook, `uninstall.enabled: true`, `interrupt.type: reboot`, ConfigMap with `uninstall.sh` / `uninstall-check.sh`, env var `MY_VAR`
- `update-trigger-uninstall.yaml` — Patch setting `uninstall.apply: true`
- `update-remove-package.yaml` — Patch removing the package from `spec.packages`
