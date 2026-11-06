<!--
  SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
  SPDX-License-Identifier: Apache-2.0
-->

# Releasing NodeWright

What a consumer or new contributor needs to know: how versions are numbered, when releases happen, where artifacts are published, and what support an older version gets.

This page is a summary. The step-by-step runbook maintainers follow is [`docs/contributing/release-process.md`](docs/contributing/release-process.md); the versioning rules in full are in [`docs/operations/versioning.md`](docs/operations/versioning.md).

## Versioning

NodeWright ships four independently versioned components, each following [Semantic Versioning](https://semver.org/). Tags are always prefixed with the component name, because the components release on their own cycles:

| Component | Tag | Published to |
| --- | --- | --- |
| Operator | `operator/vX.Y.Z` | `ghcr.io/nvidia/nodewright/operator` |
| Agent | `agent/vX.Y.Z` | `ghcr.io/nvidia/nodewright/agent` |
| Helm chart | `chart/vX.Y.Z` | `oci://ghcr.io/nvidia/nodewright/charts/nodewright` |
| CLI | `cli/vX.Y.Z` | `ghcr.io/nvidia/nodewright/cli` (OCI), plus binaries and `checksums.txt` on the GitHub Release |

Only two tag shapes are accepted per component. Anything else is rejected by the release workflow so the format stays predictable:

- `<component>/vX.Y.Z` — final release
- `<component>/vX.Y.Z-rc.N` — release candidate, published as a GitHub pre-release

The dot in `-rc.N` is required: it keeps `git tag --sort=v:refname` ordering correct and matches the SemVer pre-release convention.

### What a version bump means

Operator, agent, and CLI follow SemVer directly — MAJOR for breaking changes, MINOR for new backward-compatible functionality, PATCH for fixes. The chart versions its own template and configuration surface:

- **PATCH** — bug fixes, documentation
- **MINOR** — new features, new configuration options
- **MAJOR** — breaking changes to the chart, or to its compatibility with the agent or operator

The chart carries two versions: `version` tracks the chart itself, and `appVersion` names the operator version that chart release is built around. `appVersion` is the *fallback* operator image tag, used only when both `image.tag` and `image.digest` are omitted from values. The shipped `chart/values.yaml` sets both explicitly, and when a digest is present the digest is what determines the image pulled.

Removing or breaking a public surface — a CRD field, CLI command or flag, annotation, env var, or metric — requires a deprecation period and a migration path in `docs/` before the removal ships. See [Deprecation and End-of-Life](GOVERNANCE.md#deprecation-and-end-of-life).

**Before upgrading, read the component's `RELEASE_NOTES.md`.** That is where breaking changes and the steps to migrate are written down. `CHANGELOG.md` is generated from commit history — it will tell you *what* changed, not what you have to do about it.

## Cadence

Releases are driven by readiness, not by a calendar — there is no fixed release date. The operator drives the cycle; most releases exist because a set of operator features or fixes is ready to ship.

The shape of a release line:

1. At feature freeze a `release/vX.Y.x` branch is cut from `main`.
2. Release candidates (`-rc.1`, `-rc.2`, …) are tagged on that branch until validation is clean.
3. The final `vX.Y.0` is tagged right after the last good RC, with no code changes in between: only the changelog cut lands after the RC, plus the version bump for the chart.
4. Patches for that line (`vX.Y.1`, `vX.Y.2`, …) land on `main` first, are cherry-picked onto the same release branch, and are tagged there.

Operator and chart tags live on the release branch, never on `main`. The operator is tagged first and the chart after it, because the chart pins the operator image by digest. The chart is tagged on every release because `Chart.yaml` moves with it.

The agent and the CLI follow the same shape on their own branch families, `release/agent/vX.Y.x` and `release/cli/vX.Y.x`, because each is versioned and released on its own schedule. The CLI publishes through its own workflow ([`.github/workflows/cli-release.yaml`](.github/workflows/cli-release.yaml), triggered by `cli/*` tags) instead of the shared release workflow.

Little of this is enforced by tooling: `scripts/release-tag.sh` tags whatever `HEAD` points at and only warns when that is not a release branch of the right family, and the release workflows publish any matching tag push regardless of the branch it came from. Check what branch you are on before tagging.

## Where artifacts are published

Since `v0.16.0`, NodeWright is distributed through **GitHub Container Registry** (`ghcr.io`). That release was also the first to publish the Helm chart as an OCI artifact, which removes the `helm repo add` step; Helm 3.8+ pulls from `oci://` URLs directly:

```bash
helm install nodewright oci://ghcr.io/nvidia/nodewright/charts/nodewright --version v0.16.0
```

Releases are also staged to NGC, from where NVIDIA's internal release process promotes them to the public `nvcr.io/nvidia` catalog. The chart's image-pull defaults point at `ghcr.io`. See [Distribution: ghcr.io, staged to NGC](docs/contributing/release-process.md#distribution-ghcrio-staged-to-ngc).

**Artifacts are always built and published by CI, never from a maintainer's machine.** Pushing a `<component>/v*` tag runs that component's workflows: `operator-ci.yaml` and `agent-ci.yaml` test, build and publish the multi-platform images, `cli-release.yaml` builds and publishes the CLI, and [`.github/workflows/release.yml`](.github/workflows/release.yml) creates the GitHub Release and, for a chart tag, publishes the chart. Every artifact is signed with [cosign](https://github.com/sigstore/cosign) and carries SLSA build provenance and a CycloneDX SBOM attestation generated by [syft](https://github.com/anchore/syft); the images and the CLI also carry an OpenVEX document. See [Verify release signatures and attestations](docs/contributing/release-process.md#verify-release-signatures-and-attestations).

Only maintainers can cut a release, since pushing a tag requires write access. See [MAINTAINERS.md](MAINTAINERS.md).

### Where every release has lived

Artifacts have moved over the project's history: NGC and GHCR in parallel, then GHCR only, then the Skyhook to NodeWright rename, which moved every GitHub-hosted path. Older locations are frozen rather than deleted. [Artifact Locations](docs/operations/artifact-locations.md) lists every location each component has been published to, with the versions each one holds, so you can find an older release or update a pinned reference.

## Release notes and changelogs

Each component keeps two files side by side:

| File | Owner | Contents |
| --- | --- | --- |
| `CHANGELOG.md` | machine-generated | every release and commit, derived from git history |
| `RELEASE_NOTES.md` | human-authored | behavior changes, breaking changes, upgrade steps, highlights |

`CHANGELOG.md` carries a `DO NOT EDIT` banner and is regenerated by `scripts/gen-changelog.sh` — never hand-edit it. Curated prose belongs in the sibling `RELEASE_NOTES.md`, organized under `## <component>/<version>` headings that match the changelog.

On a tag push, the release workflow assembles the GitHub Release body from the commit range for that version and **prepends the matching `RELEASE_NOTES.md` entry** if one exists. So anything a user must read before upgrading — a breaking change, a migration step, an action required at upgrade time — needs to be written under its version heading in `RELEASE_NOTES.md` *before* the tag is pushed.

Release-notes scoping is deliberately asymmetric: a stable release's notes cover everything since the previous **stable** tag, so the stable release page is complete even after several RCs. An RC's notes cover only the delta since the prior tag, which is what you want while iterating.

## Supported versions and backports

Security fixes are released against the **latest minor of each component**. Critical fixes may be backported to the most recent prior release branch (`release/vX.Y.x`) at the maintainers' discretion.

Ordinary bug fixes follow the same path as any patch: they land on `main` first, then are cherry-picked onto the active release branch. A fix that cannot exist on `main` — a chart version bump for one line, or a backport that needs re-implementing for an older line — is opened as a PR against the release branch directly, but that is the exception.

For vulnerability reporting, see [SECURITY.md](SECURITY.md). Do not open a public issue for a security problem.
