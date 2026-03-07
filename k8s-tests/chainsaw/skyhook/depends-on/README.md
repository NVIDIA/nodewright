# Depends On Test

## Purpose

Validates that package dependencies (dependsOn) work correctly, ensuring packages execute in the proper order.

## Test Scenario

1. Create a skyhook with three packages:
   - Package A (no dependencies)
   - Package B (no dependencies)
   - Package C (depends on A and B)
2. Verify that packages A and B complete before package C starts
3. Assert the final state shows all packages complete

## Key Features Tested

- Package dependency ordering
- Multiple dependencies on a single package
- Parallel execution of independent packages
- Sequential execution of dependent packages

## Files

- `chainsaw-test.yaml` - Main test configuration with all assertions inline (pods, skyhook status) for sequential ordering to verify dependency execution order
- `skyhook.yaml` - Skyhook with package dependencies
