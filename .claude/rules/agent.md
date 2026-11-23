---
summary: "Separate vendored module, checked with make test lint in agent/; logs with slog, not logr"
paths:
  - "agent/**"
  - "containers/agent.Dockerfile"
---
# Agent

## Read before editing

- `agent/README.md`: the environment variables the agent reads and how it is run.
- `docs/user-guide/providing-secrets.md`: how packages consume secrets.
- `docs/architecture/interrupt-flow.md`: the interrupt sequence `agent/internal/interrupts` takes part in.

## What it is

`agent/` is a Go module (`github.com/NVIDIA/nodewright/agent`), vendored. It builds a single static binary that the operator injects alongside every package as the container entrypoint. It:

- Reads `/skyhook-package/config.json`, validated against the JSON schemas embedded from `agent/internal/config/schemas/`.
- Dispatches to the requested stage and step (`agent/internal/agent`; `agent/internal/step` and `agent/internal/interrupts` own per-step and per-interrupt behavior).
- Runs step scripts through `agent/internal/command`, inside a chroot of the host root mount, or against the agent's own filesystem for `on_host: false`.
- Writes completion flag files (`agent/internal/flags`) so later stages skip work already done. It writes both the legacy marker name and a fingerprint marker, so state is honored across `agent/v6.x` (Python) and `v7.x` (Go) in either direction. The one exception, the Go-only pending `node_restart` marker, is recorded in `agent/RELEASE_NOTES.md`.
- Gates interrupt re-runs on `SKYHOOK_RESOURCE_ID`, which is unique per package configuration.

## Rules

- The agent logs with `log/slog`, not the operator's `logr` (see `.claude/rules/go-style.md`).
- Run `make test lint` from `agent/` before pushing; it is exactly what the agent CI lanes run. Tooling is installed into `agent/bin/` by `agent/deps.mk`.
- Other targets in `agent/`: `make build` (binary to `agent/bin/agent`), `make fmt` (gofmt and license headers), `make generate-mocks` (required after editing a mocked interface), `make docker-build` (local image from `containers/agent.Dockerfile`).
