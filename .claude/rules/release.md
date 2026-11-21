---
summary: "Per-component tags; CHANGELOG.md is generated, RELEASE_NOTES.md is hand-written and rare"
paths:
  - "**/CHANGELOG.md"
  - "**/RELEASE_NOTES.md"
  - "cliff.toml"
  - "scripts/release-*.sh"
  - "scripts/gen-changelog.sh"
  - ".github/workflows/*release*.y*ml"
  - "docs/contributing/release*.md"
---
# Releases

## Read before editing

- `docs/contributing/release-process.md`: the step-by-step release process, with release branches.
- `docs/contributing/releases.md`: release history.
- `docs/operations/versioning.md`: per-component semver, strictly enforced.

## Rules

- Components are released separately, and tags carry the component name: `operator/v0.15.0`, `cli/v0.3.0`, `agent/v6.4.0`, `chart/v0.15.0`. The operator Makefile computes the last tag with `git tag --list 'operator/*' --sort=-v:refname | head -1`; a plain `git log --tags` is confusing without knowing this.
- Each component has its own `CHANGELOG.md`: `agent/CHANGELOG.md`, `chart/CHANGELOG.md`, `operator/CHANGELOG.md` and `operator/cmd/cli/CHANGELOG.md`. Changelogs are generated from git history by `scripts/gen-changelog.sh` (`make changelog`) and are machine-owned: never hand-edit them.
- Hand-written notes go in the sibling `RELEASE_NOTES.md`, and only when a reader has to act or will be surprised (see the release-notes rule in `.claude/CLAUDE.md`).
- `make release-tag` and `make changelog` are interactive and need a terminal.
