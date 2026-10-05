# Release Notes

Human-authored highlights, behavior changes, and upgrade steps for the agent.
For the full commit-level log see CHANGELOG.md.

## Unreleased

## agent/v7.0.0 - 2026-10-05

### Breaking

- **The agent is now implemented in Go, and the Python implementation is
  removed.** The next release is `agent/v7.0.0`; `agent/v6.x` images are the
  Python agent and stay on GHCR. The operator-facing contract is unchanged:
  positional arguments, environment variables, exit codes, and the flag,
  history, interrupt-marker and log directories written on the host, as
  verified by the operator-agent chainsaw suite that ran against both
  implementations before the cutover. Two log file names inside those
  directories do change; see Behavior Changes. Both implementations honour
  each other's on-host state,
  so moving a node between `v6.x` and `v7.x` in either direction does not
  re-run completed steps or interrupts, with the single `node_restart`
  exception described under Upgrade and Rollback below.

### Behavior Changes

- **`on_host: false` now takes effect.** The Python agent printed the flag but
  ran every step inside the host chroot regardless. The Go agent runs an
  `on_host: false` step inside the agent container instead, against the host
  paths reached through the root mount, with `STEP_ROOT` and `SKYHOOK_DIR`
  resolved accordingly. Packages that set `on_host: false` and relied on it
  being ignored will see their step run where the flag said it should.
- **Host log files no longer prefix each line with `[out]`/`[err]` and a
  timestamp.** Step and interrupt output is written to the log file as the
  script produced it. Anything parsing those files for the prefix must stop
  expecting it.
- **Step stderr lands in the same host log file as stdout, and log files are
  mode 0600.** The Python agent wrote stdout to `<step>-<timestamp>.log` (0644)
  and stderr to a sibling `<step>-<timestamp>.log.err`. Neither agent reaps the
  zero-byte `.log.err` files a `v6.x` node already carries; remove them by hand
  if the clutter matters.
- **Interrupt logs are one file per interrupt run, and are reaped.** The
  directory is unchanged,
  `<SKYHOOK_LOG_DIR>/<package name in the NodeWright>/<version>/interrupts/`,
  but the file is now `<type>-<timestamp>.log` for the whole interrupt, kept to
  the newest five like step logs. The Python agent wrote
  `<type>_<index>-<timestamp>.log` per operation and never removed any; those
  names do not match the new reaping pattern, so they stay on a `v6.x` node
  until removed by hand.
- **`SKYHOOK_AGENT_BUFFER_LIMIT` is no longer read or printed.** It only tuned
  the Python agent's stream reader and had no equivalent in Go.
- **The CycloneDX SBOM moved from the multi-platform index digest to each platform manifest digest, and an OpenVEX document is now published alongside it.** An SBOM describes exactly one root filesystem, so one attached to a multi-platform index described neither child truthfully, and a consumer who resolved `linux/amd64` and enumerated referrers on that manifest found nothing.

  **You will notice this if you verify release artifacts.** `cosign verify-attestation --type cyclonedx <index-digest>` was the documented command and now finds nothing on this release. Resolve the platform manifest and verify against that instead:

  ```bash
  INDEX=$(crane digest "$REPO:$TAG")
  PLATFORM_DIGEST=$(crane digest --platform linux/amd64 "$REPO@$INDEX")
  cosign verify-attestation --type cyclonedx ... "$REPO@$PLATFORM_DIGEST"
  cosign verify-attestation --type openvex   ... "$REPO@$PLATFORM_DIGEST"
  ```

  The signature and the SLSA provenance have not moved: both still verify against the index digest, which is what a tag resolves to and what you pull. Tags published before this release keep the old layout, with the SBOM on the index and no OpenVEX document. `SECURITY.md` carries the full recipe.

### Upgrade and Rollback

- Rolling back is a matter of pinning the agent image back to image tag
  `v6.4.2` (`ghcr.io/nvidia/nodewright/agent:v6.4.2`, through the chart's
  `agent.tag` and `agent.digest` or the per-package image in the NodeWright CR;
  the git tag `agent/v6.4.2` is not a valid image tag). One caveat: the Go
  agent records a started `node_restart` as a pending marker and confirms it by
  a changed host boot ID on its next run.
  The Python agent does not read that marker, so a node whose reboot the Go
  agent started but had not yet confirmed at the moment of rollback will be
  rebooted once more by the Python agent. Every other kind of in-flight state
  is picked up where it was left.
