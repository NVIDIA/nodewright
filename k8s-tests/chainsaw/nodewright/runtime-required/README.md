# Runtime Required Test

## Purpose

Validates per-node runtime-required behavior where each node's taint is removed independently when that node completes all runtime-required nodewrights.

## Test Scenario

The test explicitly validates node isolation by blocking one node while the other completes:

### Phase 1: Setup with Blocked Node

1. Label both worker nodes with the test label (`nodewright.nvidia.com/runtime-required-test=true`)
2. Add runtime-required taints to both nodes
3. **Add blocking taint to kind-worker2** only (`test-block=true:NoSchedule`)
4. Apply a runtime-required nodewright (does NOT tolerate the blocking taint)
5. Result: Only kind-worker can run; kind-worker2 is blocked

### Phase 2: Assert Node Isolation (Sequential Assertions)

1. Assert kind-worker completes with runtime-required taint removed
2. Assert kind-worker2 is still blocked with runtime-required taint STILL present
   - This sequential assertion proves per-node taint removal

### Phase 3: Unblock Second Node

1. Remove the blocking taint from kind-worker2

### Phase 4: Assert Node2 Completion

1. Assert kind-worker2 completes with runtime-required taint removed

### Phase 5: Final Validation

1. Assert both nodes complete (uses label selector for both nodes)

## Key Features Tested

- Per-node taint removal for runtime-required nodewrights
- Node isolation (one slow node doesn't block others)
- Multi-node runtime-required behavior
- Taint is removed based on individual node completion, not global nodewright completion
- Deprecation-window handling of the **legacy** `skyhook.nvidia.com=runtime-required:NoSchedule` taint key

## Files

- `chainsaw-test.yaml` - Main test configuration with all assertions inline (pods, nodes, nodewright status) for sequential ordering through the multi-phase flow; the nodewright resource is also defined inline

## Notes

- Uses dedicated label `nodewright.nvidia.com/runtime-required-test=true` to avoid conflicts with other tests
- Tests both worker nodes independently
- **Deliberately taints the nodes with the legacy `skyhook.nvidia.com` key**, not the current default
  `nodewright.nvidia.com`. This is the e2e coverage for the rename deprecation window: it proves the
  operator still tolerates and removes the legacy taint on a cluster whose provisioner has not migrated.
  The `auto-taint-new-nodes` suite covers the current key end to end. Do not "fix" this to
  `nodewright.nvidia.com`; when the legacy key is dropped in operator v0.21.0, delete this coverage
  along with it.
