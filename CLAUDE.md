# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

NodeWright (formerly Skyhook) is a Kubernetes-aware package manager that enables cluster administrators to safely modify and maintain underlying hosts at scale. It's an NVIDIA open-source project with three main components:

1. **Operator** (`operator/`) — Go Kubernetes controller (controller-runtime) that reconciles Skyhook and DeploymentPolicy CRDs
2. **Agent** (`agent/`) — Python agent that runs in init containers within package pods, executing lifecycle stages (Uninstall → Apply → Config → Interrupt → Post-Interrupt)
3. **CLI** (`operator/cmd/cli/`) — kubectl plugin (`kubectl skyhook`) for inspecting status and managing nodes

## Build & Test Commands

### Root-level (both components)
```bash
make build          # Build operator + agent
make test           # Run all tests
make fmt            # Run formatters (includes license headers)
make license-fmt    # Add/fix license headers only
make clean          # Clean build artifacts
```

### Operator (Go)
```bash
cd operator
make build              # Build manager + CLI binaries
make build-manager      # Build operator binary only
make build-cli          # Build kubectl-skyhook CLI only
make unit-tests         # Run unit tests (Ginkgo/Gomega, needs envtest)
make e2e-tests          # Run e2e tests (needs Kind cluster)
make test               # Run all tests (unit + e2e + helm + cli + coverage)
make watch-tests        # Auto-run unit tests on file changes
make lint               # golangci-lint + license check
make lint-fix           # golangci-lint with auto-fix
make fmt                # gofmt + license headers
make manifests          # Regenerate CRDs, RBAC, webhooks via controller-gen
make generate           # Regenerate DeepCopy methods
make generate-mocks     # Regenerate interface mocks via mockery
make install            # Install CRDs into current cluster
make create-kind-cluster        # Create Kind cluster for testing
make docker-build               # Build operator container image
```

### Agent (Python)
```bash
cd agent
make venv           # Create Python venv with hatch + coverage
make test           # Run pytest with coverage (via hatch)
make build          # Build wheel with hatch
make fmt            # License headers
make vendor         # Vendor dependencies with Unearth
make docker-build-only  # Build agent container image
make clean          # Remove venv and pycache
```

### K8s Integration Tests
```bash
# Chainsaw-based e2e tests (from operator dir, needs running cluster)
cd operator
make e2e-tests                  # Skyhook e2e tests
make cli-e2e-tests              # CLI e2e tests
make helm-tests                 # Helm chart tests
make deployment-policy-tests    # Needs 15-node cluster
make operator-agent-tests AGENT_IMAGE=<image>  # Operator-agent integration
```

## Architecture

### CRD Types (operator/api/v1alpha1/)
- **Skyhook** — Declares packages to install on nodes, with node selectors, interrupt budgets, dependencies, and version constraints
- **DeploymentPolicy** — Controls compartmentalized rollouts across node groups

### Operator Reconciliation (operator/internal/controller/)
The operator watches Skyhook CRs, schedules package pods on matching nodes, manages lifecycle stages, handles node cordoning/draining for interrupts, and respects interruption budgets. It uses a DAL layer (`internal/dal/`) and dependency graph (`internal/graph/`).

### Agent Lifecycle (agent/skyhook-agent/src/skyhook_agent/)
The agent runs inside package containers and executes steps in lifecycle stages. It validates package configs against JSON schemas (`schemas/`), manages chroot execution for host-level changes, and handles interrupts (systemd restart, reboot).

### Key Environment Variables (operator)
- `AGENT_IMAGE` — Agent container image (required for operator-agent tests)
- `ENABLE_WEBHOOKS` — Enable/disable webhook validation (default: false for local dev)
- `LOG_LEVEL` — Operator log level
- `KIND_VERSION` — Kubernetes version for Kind clusters (default: 1.35.0)
- `DOCKER_CMD` — Container tool: `podman` locally, `docker` in CI

## Development Conventions

- **Commits**: [Conventional Commits](https://www.conventionalcommits.org/) format, DCO sign-off required (`git commit -s`)
- **Go style**: gofmt, golangci-lint
- **Python style**: Black, ruff
- **Testing**: Ginkgo/Gomega for Go, pytest for Python, Chainsaw for K8s e2e
- **Versioning**: Strict semantic versioning; tags follow `component/vX.Y.Z` pattern (e.g., `operator/v1.2.3`)
- **License headers**: Required on all source files; run `make license-fmt` or `make fmt` to apply
- **Dependencies**: Go uses vendoring (`-mod=vendor`), Python agent vendors dependencies locally
- **Changelogs**: Generated via git-cliff (`make changelog COMPONENT=operator|agent|chart|cli`)
