# Uninstall Upgrade Skyhook Test

## Purpose

Validates that uninstall and upgrade modes work correctly when packages are removed or have their versions changed.

## Test Scenario

1. Apply a skyhook with multiple packages and wait for completion
2. Update the skyhook:
   - Remove one package (should trigger uninstall)
   - Downgrade another package version (should uninstall old, install new)
   - Upgrade another package version (should run upgrade stage)
3. Verify:
   - Removed package is uninstalled
   - Downgraded package: old version uninstalled, new version installed
   - Upgraded package: runs in upgrade mode before completing
4. Update again to remove all remaining packages
5. Verify all packages are uninstalled successfully
6. Assert pod resource requests and limits are set correctly from defaults

## Key Features Tested

- Package uninstall when removed from spec
- Package version downgrade handling
- Package version upgrade handling
- ConfigMap changes during version changes (version overrides config)
- Pod resource management
- Complete package removal

## Files

- `chainsaw-test.yaml` - Main test configuration with lifecycle assertions inline (nodes, pods, skyhook status) for sequential ordering
- `skyhook.yaml` - Initial skyhook with packages
- `update.yaml` - Update with version changes
- `update-no-packages.yaml` - Final update removing all packages
- `assert-cm-install.yaml` - ConfigMap assertions after initial install (parallel)
- `assert-cm-update.yaml` - ConfigMap assertions after upgrade (parallel)
