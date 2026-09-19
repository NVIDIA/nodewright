# PR: fix(version): prevent panic on empty or invalid version string in Compare

## Related Issue
Fixes #644

## Changes Proposed
* **`operator/internal/version/version.go`**: Validated inputs in `Compare(version1, version2 string) int` using `IsValid(version1)` and `IsValid(version2)` prior to accessing slice indices `version1[0]` and `version2[0]`. If either version is empty or invalid semver, `Compare` now returns `-2`. This prevents runtime index-out-of-range panics when reconciling nodes with missing or empty version state annotations and fulfills the error contract expected by `skyhook_controller.go` (`if comparison == -2`).
* **`operator/internal/version/version_test.go`**: Added table-driven test cases verifying that `Compare` returns `-2` when given an empty string on the left, an empty string on the right, empty strings on both sides, or invalid semver strings (e.g. `"dev"`).

## How Has This Been Tested?
Please describe the tests that you ran to verify your changes.
* [x] Unit test passed: Added table entries to `version_test.go` verifying that `Compare` returns `-2` for empty strings on either or both sides, as well as invalid semver inputs, alongside existing ordering test cases.
* [x] Reconcile error path verified: Verified that `operator/internal/controller/skyhook_controller.go` line 1968 receives `-2` on invalid/empty version annotations and properly returns an error instead of panicking.

## Checklist
- [x] My code follows the style guidelines of this project
- [x] I have performed a self-review of my own code
- [x] I have commented my code, particularly in hard-to-understand areas
- [x] My changes generate no new warnings
