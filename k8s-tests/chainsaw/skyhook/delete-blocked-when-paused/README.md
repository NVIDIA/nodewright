# Delete Blocked When Paused Test

## Purpose

Verifies that the Skyhook finalizer refuses to proceed with cleanup while the
CR is paused or disabled. `processSkyhooksPerNode` skips paused/disabled
Skyhooks, so uninstall pods never get created; without this guard, the
finalizer would block deletion indefinitely with no user-visible signal.

## Test Scenario

1. Install a Skyhook with one `uninstall.enabled: true` package, wait for
   `status: complete`.
2. Annotate the Skyhook with `skyhook.nvidia.com/pause=true`.
3. Issue `kubectl delete skyhook --wait=false`.
4. Assert the CR is still present (`deletionTimestamp != null`) with a
   `skyhook.nvidia.com/DeletionBlocked` condition
   (`status=True`, `reason=PausedOrDisabled`).
5. Assert a Warning event was recorded on the Skyhook with
   `reason=DeletionBlocked`. Because Skyhook is cluster-scoped, events can
   land in a non-`default` namespace — a polling script checks events across
   all namespaces rather than a single-namespace resource assert.
6. Remove the pause annotation. The finalizer now drives uninstall to
   completion and the CR disappears.

## Key Features Tested

- Finalizer DeletionBlocked guard (controller-level)
- DeletionBlocked condition with `reason=PausedOrDisabled`
- Warning event emission for operator visibility
- Condition cleared on unpause so the finalizer can proceed
- Symmetric behavior for `disable=true` (same guard predicate)

## Files

- `chainsaw-test.yaml` — Main test: install → pause+delete+assert → unpause+drain
- `skyhook.yaml` — One `uninstall.enabled: true` package
