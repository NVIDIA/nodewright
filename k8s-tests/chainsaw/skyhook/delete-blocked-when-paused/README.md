# Delete Blocked When Paused Test

## Purpose

Verifies the Skyhook finalizer refuses to proceed with cleanup when the CR is
paused and at least one `uninstall.enabled: true` package is still tracked in
`nodeState`. `processSkyhooksPerNode` skips paused Skyhooks, so uninstall pods
never get created; without this guard, the finalizer would silently drop
on-host state the user explicitly asked to be cleaned.

The symmetric disabled-with-pending case is covered in
`../delete-blocked-when-disabled/`.

## Test Scenario

1. Install a Skyhook with one `uninstall.enabled: true` package, wait for
   `status: complete`.
2. Annotate the Skyhook with `skyhook.nvidia.com/pause=true`.
3. Issue `kubectl delete skyhook --wait=false`.
4. Assert the CR is still present (`deletionTimestamp != null`) with a
   `skyhook.nvidia.com/DeletionBlocked` condition
   (`status=True`, `reason=PausedWithPendingUninstall`).
5. Assert a Warning event was recorded on the Skyhook with
   `reason=DeletionBlocked`. Because Skyhook is cluster-scoped, events can
   land in a non-`default` namespace — a polling script checks events across
   all namespaces rather than a single-namespace resource assert.
6. Remove the pause annotation. The finalizer now drives uninstall to
   completion and the CR disappears.

## Key Features Tested

- Finalizer DeletionBlocked guard for paused + pending uninstall
- DeletionBlocked condition with `reason=PausedWithPendingUninstall`
- Warning event emission for operator visibility
- Condition cleared on unpause so the finalizer can proceed

## Files

- `chainsaw-test.yaml` — Main test: install → pause+delete+assert → unpause+drain
- `skyhook.yaml` — One `uninstall.enabled: true` package
