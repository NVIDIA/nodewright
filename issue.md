# [BUG]: version.Compare panics on empty or invalid version strings, crashing operator reconcile loop

## Description
In `operator/internal/version/version.go`, the `Compare(version1, version2 string) int` function indexes `version1[0]` and `version2[0]` directly to detect or prepend the `v` prefix without checking whether either string is empty (`""`). When either input is empty, Go triggers an immediate out-of-bounds panic: `panic: runtime error: index out of range [0] with length 0`.

This function is called in the primary reconcile loop (`operator/internal/controller/skyhook_controller.go:1967`) when comparing the desired package version from the CR spec with the version observed on the node (`packageStatus.Version`). While the spec version is validated by the admission webhook, `packageStatus.Version` is read from the `nodewright.nvidia.com/nodeState_<name>` annotation on the Node object. Because Node annotations are not subject to the CRD schema validation by the Kubernetes API server, an empty or malformed stored version causes the operator to panic and enter a `CrashLoopBackOff`, degrading all reconciliation across the entire cluster.

Furthermore, `skyhook_controller.go` expects `version.Compare` to return `-2` when an invalid version string is provided so that it can return an explicit reconciliation error (`error comparing package versions: invalid version string provided...`). However, `Compare` previously delegated directly to `semver.Compare`, which only returns `-1`, `0`, or `1` and treats invalid versions as less than valid versions, silently taking the downgrade path rather than flagging the error.

## Steps to Reproduce
1. Deploy the NodeWright operator controller manager in a Kubernetes cluster.
2. Create and apply a NodeWright/Skyhook custom resource specifying a package with version `1.0.0`.
3. Manually or via CLI tooling set the node state annotation (`nodewright.nvidia.com/nodeState_<name>`) on a target node to an object containing an empty version string (`"version": ""`).
4. Trigger reconciliation of the NodeWright resource.
5. Observe the operator controller pod crashing with a runtime panic in `version.Compare`.

## Expected Behavior
`version.Compare` should handle empty and invalid version strings safely without panicking. When either version string is empty or invalid semver, `Compare` should return `-2`, allowing the caller in `skyhook_controller.go` to handle the error gracefully and report a descriptive reconciliation error rather than crashing the controller.

## Actual Behavior / Error Logs
```text
panic: runtime error: index out of range [0] with length 0

goroutine 65 [running]:
github.com/NVIDIA/nodewright/operator/internal/version.Compare({0x0, 0x0}, {0x103d865, 0x6})
	/workspace/operator/internal/version/version.go:44 +0x24
github.com/NVIDIA/nodewright/operator/internal/controller.(*SkyhookReconciler).reconcileSkyhook(0xc000620000, {0x145a820, 0xc00021a300}, 0xc0004bc000, 0xc0003b0180)
	/workspace/operator/internal/controller/skyhook_controller.go:1967 +0x1485
github.com/NVIDIA/nodewright/operator/internal/controller.(*SkyhookReconciler).Reconcile(0xc000620000, {0x145a820, 0xc00021a300}, {0xc0005400c0, 0xe})
	/workspace/operator/internal/controller/skyhook_controller.go:412 +0x3d0
```

## Proposed Fix
1. Guard `version.Compare` against empty and invalid semver inputs by checking `if !IsValid(version1) || !IsValid(version2)`.
2. Return `-2` if either string fails validation. Since `IsValid` already checks `if version == "" { return false }`, this prevents indexing empty strings and properly satisfies the caller's contract in `skyhook_controller.go` (`if comparison == -2`).
3. Add unit test entries in `operator/internal/version/version_test.go` covering empty strings on the left, right, both sides, and invalid semver inputs.
