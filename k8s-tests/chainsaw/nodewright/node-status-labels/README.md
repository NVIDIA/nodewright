# Node Status Labels Test

## Purpose

Validates the node-level signals: the `nodewright.nvidia.com/status` label, which reports a node's Status across every NodeWright that selects it, and the one-way `nodewright.nvidia.com/bootstrapped` label and `nodewright.nvidia.com/bootstrappedAt` annotation, set the first time that Status is `complete`.

## Test Scenario

1. Remove any `bootstrapped` marker an earlier run left on kind-worker2, and label it with the test label (`nodewright.nvidia.com/node-status-labels-test=true`)
2. Apply a NodeWright selecting the label and wait for it to complete
3. Assert the node reads `status=complete` and `bootstrapped=true`, has a `bootstrappedAt` annotation, and record that annotation
4. Pause the NodeWright and assert the node reads `status=paused` while still `bootstrapped=true`
5. Resume it and assert the node reads `status=complete` again with the same `bootstrappedAt`
6. Delete the NodeWright and assert the node no longer has the `status` label but keeps `bootstrapped` and `bootstrappedAt`

## Key Features Tested

- The node's Status across its NodeWrights, kept current as their Status changes
- `bootstrapped` and `bootstrappedAt` set once, and never cleared or rewritten
- The `status` label removed once no NodeWright selects the node

## Files

- `chainsaw-test.yaml` - Main test configuration, with the NodeWright and the node assertions inline

## Notes

- Uses kind-worker2, which no other test in the `core` pool selects while this test runs, so this NodeWright is the only one that counts on the node
- Uses dedicated label `nodewright.nvidia.com/node-status-labels-test=true`, removed at the end whether or not the assertions pass
