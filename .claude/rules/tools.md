---
summary: "Stdlib-only module with stdlib testing, so a release never downloads a module"
paths:
  - "tools/**"
---
# Release and repository tooling (`tools/`)

`tools/` is a Go module (`github.com/NVIDIA/nodewright/tools`) holding:

- `tools/cmd/openvex`: binds `.openvex.json` to a platform digest and validates it against the OpenVEX v0.2.0 contract.
- `tools/cmd/agentrules`: validates the rule files in `.claude/rules/` and keeps the index in `.claude/CLAUDE.md` in step with them (`make agent-rules-index`, `make agent-rules-check` from the repository root).
- `tools/tests/releasepolicy`: a nested module pinning the release evidence policy.

## Rules

- **Stdlib only.** The release job runs `go run ./cmd/openvex` with a toolchain and no module download, so a dependency would turn every release into a fetch. The module has no `go.sum`; keep it that way.
- **Tests use stdlib `testing`**, not Ginkgo.
- The policy tests live in their own nested module (`tools/tests/`) precisely so they can parse YAML with a real library without that dependency reaching the release path. A nested module is invisible to `./...`, so every Go target in `tools/Makefile` names it.
- `make test` from `tools/` runs fmt-check, vet and the unit tests in both modules; it is what CI runs. There are no license targets in `tools/Makefile` because the root Makefile's `license-fmt` already covers `tools/`.
