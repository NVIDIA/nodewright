# Config Skyhook Test

## Purpose

Validates that configuration changes work correctly for the operator, including config interrupts and merging behavior.

## Test Scenario

1. Apply a simple skyhook definition and verify packages start applying
2. Update two packages before the skyhook finishes:
   - One with a package interrupt
   - One without a package interrupt
   - Both on a configmap key with a config interrupt defined
3. Assert that config interrupts are merged and run for both packages
4. Verify the package without an interrupt doesn't hang when config changes occur
5. Update the same two packages again on a key with a config interrupt
6. Assert that config, interrupt, and post-interrupt stages run correctly
7. Update one more time on a key without a config interrupt defined
8. Verify only the config step runs

## Key Features Tested

- Configuration changes during package execution
- Config interrupt merging
- Package interrupt and config interrupt interaction
- Handling packages without interrupts during config changes
- Post-interrupt stage execution

## Files

- `chainsaw-test.yaml` - Main test configuration with lifecycle assertions inline (nodes, skyhook status) for sequential ordering
- `skyhook.yaml` - Initial skyhook definition
- `update-while-running.yaml` - Config update applied while skyhook is still running
- `update.yaml` - Standard config update
- `update-no-interrupt.yaml` - Config update with no interrupt
- `update-glob.yaml` - Config update using glob-based package selection
- `assert-cm-deploy.yaml` - ConfigMap assertions for initial deploy phase
- `assert-cm-update-while-running.yaml` - ConfigMap assertions for update-while-running phase
- `assert-cm-update.yaml` - ConfigMap assertions for standard update phase
- `assert-cm-update-no-interrupt.yaml` - ConfigMap assertions for no-interrupt update phase
- `assert-cm-update-glob.yaml` - ConfigMap assertions for glob update phase
