# Uninstall Fix Config Test

## Purpose

Verifies the operator's recovery path when an `uninstall.sh` is broken: the
uninstall pod errors, the Skyhook gets an `UninstallFailed` condition, and
fixing the script in the spec causes the operator to invalidate the failing
pod and recreate it with the corrected config. Once the corrected script
runs, the package is uninstalled cleanly and the failure condition clears.

## Test Scenario

1. Install the package with a deliberately broken `uninstall.sh` in its
   ConfigMap, wait for complete (the broken script only runs on uninstall,
   not install).
2. Flip `uninstall.apply: true`. Assert:
   - Node state shows `stage=uninstall, state=erroring` (the pod is
     restarting — ignore the `restarts` count, only assert stable fields).
   - Skyhook has condition `UninstallFailed`
     (`status=True, reason=UninstallPodFailing`).
3. Apply the fix patch (corrected `uninstall.sh` in the ConfigMap). The
   operator detects the spec change, invalidates the failing pod, and
   recreates it.
4. Assert `nodeState_<name>: '{}'` (package absent — uninstall succeeded)
   and Skyhook is `status: complete` (the `UninstallFailed` condition is
   cleared).

## Key Features Tested

- `UninstallFailed` condition surfaced on pod failure
- Pod restart behavior while in `erroring` state
- ConfigMap change triggers invalidation + recreation of the failing pod
- `UninstallFailed` condition cleared once the uninstall succeeds
- Operator recovers to `status: complete` without manual pod cleanup

## Files

- `chainsaw-test.yaml` — Main test: install → trigger failing uninstall → fix → succeed
- `skyhook.yaml` — Initial Skyhook with a broken `uninstall.sh`
- `update-trigger-uninstall.yaml` — Patch setting `uninstall.apply: true`
- `update-fix-uninstall.yaml` — Patch replacing the broken script with a working one
