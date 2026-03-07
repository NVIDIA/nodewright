# Skyhook Upgrade Test

## Purpose

Tests the operator's ability to handle state migration during operator version upgrades.

## Test Scenario

1. Create bad state on the node before migration (simulating pre-upgrade state)
2. This triggers an infinite reconciliation loop: upgrade -> config -> upgrade -> config
3. Update the operator version
4. Verify the migration fixes the state and stops the loop
5. Assert the skyhook completes normally

## Key Features Tested

- State migration during operator upgrade
- Handling of legacy node state
- Breaking infinite reconciliation loops
- Version upgrade compatibility

## Files

- `chainsaw-test.yaml` - Main test configuration with all assertions inline (nodes, skyhook status) for sequential ordering through pre-migration and post-migration states
- `skyhook.yaml` - Skyhook definition

## Notes

- This test is **skipped** because it requires manual version updates while running
- This is a manual test that should be run when the operator is updated to a new version
