# Runtime Required Delete Test

## Purpose

Validates that deleting a runtime-required NodeWright removes the runtime-required taint from a node it never completed on, when no other runtime-required NodeWright selects that node. Without this, nothing would ever remove the taint and the node would stay `NoSchedule`.

## Test Scenario

1. Label kind-worker2 with the test label (`nodewright.nvidia.com/runtime-required-delete-test=true`)
2. Add the runtime-required taint and a blocking taint (`test-block-delete=true:NoSchedule`) to kind-worker2
3. Apply a runtime-required NodeWright selecting the label. It does not tolerate the blocking taint, so the node is `blocked` and never completes
4. Assert kind-worker2 is `blocked` and still has the runtime-required taint
5. Delete the NodeWright
6. Assert kind-worker2 no longer has the runtime-required taint and still has the blocking taint

## Key Features Tested

- Taint removal on deletion of a runtime-required NodeWright that never completed on the node
- Only runtime-required taints are removed; other taints stay

## Files

- `chainsaw-test.yaml` - Main test configuration, with the NodeWright and the node assertions inline

## Notes

- Uses dedicated label `nodewright.nvidia.com/runtime-required-delete-test=true` to avoid conflicts with other tests
- The final step removes the test taints and label from kind-worker2 whether or not the assertions pass
