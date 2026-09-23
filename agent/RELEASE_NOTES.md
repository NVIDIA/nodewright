# Release Notes

Human-authored highlights, behavior changes, and upgrade steps for the agent.
For the full commit-level log see CHANGELOG.md.

## Unreleased

### Breaking

- **The agent is now implemented in Go, and the Python implementation is
  removed.** The next release is `agent/v7.0.0`; `agent/v6.x` images are the
  Python agent and stay on GHCR. The operator-facing contract is unchanged:
  positional arguments, environment variables, exit codes, and the flag,
  history, interrupt-marker and log paths written on the host, as verified by
  the operator-agent chainsaw suite that ran against both implementations
  before the cutover. Both implementations honour each other's on-host state,
  so moving a node between `v6.x` and `v7.x` in either direction does not
  re-run completed steps or interrupts.

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
- **`SKYHOOK_AGENT_BUFFER_LIMIT` is no longer read or printed.** It only tuned
  the Python agent's stream reader and had no equivalent in Go.

### Upgrade and Rollback

- Rolling back is a matter of pinning the agent image back to `agent/v6.4.2`
  (the chart's `agent.tag` and `agent.digest`, or the per-package image in the
  NodeWright CR). One caveat: the Go agent records a started `node_restart` as
  a pending marker and confirms it by a changed host boot ID on its next run.
  The Python agent does not read that marker, so a node whose reboot the Go
  agent started but had not yet confirmed at the moment of rollback will be
  rebooted once more by the Python agent. Every other kind of in-flight state
  is picked up where it was left.
