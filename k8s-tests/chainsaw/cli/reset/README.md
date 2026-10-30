# CLI Reset Test

## Purpose

Validates the nodewright reset command for resetting all nodes in a NodeWright.

## Test Scenario

1. Create a nodewright and wait for it to complete
2. Disable the nodewright to prevent re-processing after reset
3. Test reset command:
   - Run `nodewright reset <name>`
   - Assert all nodes are reset to initial state
4. Verify node annotations are cleared
5. Seed `status.configUpdates` before the reset, and verify the reset clears it

## Key Features Tested

- `nodewright reset` - Resets all nodes for a NodeWright
- Node state cleanup
- Annotation removal
- Recorded config changes (`status.configUpdates`) cleared

## Files

- `chainsaw-test.yaml` - Main test configuration
- `nodewright.yaml` - Test nodewright
- `assert-nodewright-complete.yaml` - Initial completion assertion
- `assert-nodewright-disabled.yaml` - Disabled state assertion
- `assert-nodes-reset.yaml` - Reset state assertion
