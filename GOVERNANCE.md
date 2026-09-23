<!--
  SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
  SPDX-License-Identifier: Apache-2.0
-->

# NodeWright Project Governance

This document describes how the NodeWright project (formerly Skyhook) is governed: the roles people hold, how decisions get made, and how maintainers join and leave. It is intentionally lightweight and will grow with the project. For the current roster and maintainer responsibilities, see [MAINTAINERS.md](MAINTAINERS.md).

## Project Scope

NodeWright is a Kubernetes-aware package manager for safely modifying and maintaining host infrastructure at scale. It coordinates the node lifecycle (cordon, drain, apply package, interrupt or reboot, uncordon) as controlled rollouts gated by interruption budgets and deployment policies.

In scope: the operator and its CRDs, the package agent, the `kubectl nodewright` CLI, the Helm chart, and the documentation and test infrastructure that support them.

Out of scope: the contents of individual packages (those live with their authors), cluster provisioning, and workload scheduling. NodeWright applies host changes; it does not decide what those changes should be.

## Roles

NodeWright uses three roles. Code ownership maps directly to [`.github/CODEOWNERS`](.github/CODEOWNERS).

### Contributors

Anyone who opens an issue, pull request, or discussion. Contributors follow the [Code of Conduct](CODE_OF_CONDUCT.md) and both sign off and sign their commits, `git commit -s -S` (see [CONTRIBUTING.md](CONTRIBUTING.md#developer-certificate-of-origin-and-commit-signing)). No special access is required to contribute.

### Code Owners

Trusted contributors who review and approve pull requests for the paths they own. Code ownership is declared per path in [`.github/CODEOWNERS`](.github/CODEOWNERS), which GitHub uses to request reviews automatically. Code owners keep their areas healthy and mentor contributors.

### Maintainers

Maintainers have merge rights, make and ratify project decisions, manage releases, and own this governance process, including adding and removing maintainers. Maintainers are also code owners. The current roster is in [MAINTAINERS.md](MAINTAINERS.md).

## Areas of Ownership

Path-level ownership is authoritative in [`.github/CODEOWNERS`](.github/CODEOWNERS). At a high level the project is organized into:

- **Operator** (`operator/`): the controller-manager, CRDs, reconcile loop, and the `kubectl nodewright` CLI under `operator/cmd/cli/`.
- **Agent** (`agent/`): the Go binary that runs inside every package container and executes lifecycle steps.
- **Chart and deployment surface** (`chart/`, `operator/config/`): the Helm chart and the kustomize manifests it mirrors.
- **Containers** (`containers/`): base images and container build tooling.
- **Documentation** (`docs/`, root project docs): domain concepts and behavioral contracts.
- **Tests and CI** (`k8s-tests/`, `.github/`, `scripts/`): chainsaw e2e suites, workflows, and release tooling.

Maintainers hold cross-cutting responsibility across all areas.

## Decision-Making

NodeWright decides by **lazy consensus**: a proposal (pull request, issue, or discussion) is accepted if no maintainer raises a blocking objection within a reasonable review window, at least five business days for non-trivial changes. Most day-to-day changes are merged through normal code-owner review under [`.github/CODEOWNERS`](.github/CODEOWNERS) and the process in [CONTRIBUTING.md](CONTRIBUTING.md).

When consensus is not reached:

- **Majority vote.** Any maintainer may call a vote. A proposal passes on a simple majority of non-emeritus maintainers; quorum is a simple majority of that same group.
- **Blocking objection (veto).** A maintainer may block a change by stating a concrete technical rationale and an actionable path to resolution. A blocked change proceeds only if a two-thirds supermajority of non-emeritus maintainers votes to override.
- **Supermajority decisions.** Adding or removing a maintainer, and changes to this document, require a two-thirds supermajority of non-emeritus maintainers.

Changes that alter a public contract (CRD field, CLI flag, annotation, metric, or lifecycle semantics) always require an explicit approval from at least one maintainer who did not author the change, regardless of the review window.

## Tie-Breaking

If a vote is tied, the **lead maintainer** has the casting vote. The lead maintainer is designated by the maintainer team and recorded in [MAINTAINERS.md](MAINTAINERS.md); the role exists to break deadlocks and carries no additional day-to-day authority. If the lead maintainer is the subject of, or is recused from, a decision, the remaining maintainers designate an acting lead for that decision.

## Adding and Removing Maintainers

### Adding

Maintainers are added on merit. An existing maintainer nominates a candidate based on sustained contributions, review quality, and domain expertise. The nominee is added when a two-thirds supermajority of non-emeritus maintainers approves.

### Removing and stepping down

A maintainer may step down at any time by opening a pull request that moves them to emeritus. A maintainer may also be removed by a two-thirds supermajority of non-emeritus maintainers, for sustained inactivity (see [Emeritus](#emeritus)) or for conduct that violates the [Code of Conduct](CODE_OF_CONDUCT.md). Removal for cause follows the Code of Conduct enforcement process.

## Emeritus

A maintainer is considered **inactive** after six months with no substantive contribution, review, or governance participation. Inactive maintainers are moved to the Emeritus list in [MAINTAINERS.md](MAINTAINERS.md) either voluntarily, or involuntarily by the same two-thirds supermajority of non-emeritus maintainers required for removal. Moving to emeritus removes merge rights and excludes the maintainer from quorum and vote counts. Emeritus maintainers are welcomed back through the normal onboarding process when they return to active participation.

## Deprecation and End-of-Life

Components are versioned and released independently under the rules in [`docs/operations/versioning.md`](docs/operations/versioning.md). Removing or breaking a public surface (CRD field, CLI command or flag, annotation, env var, or metric) requires a deprecation period announced in the relevant `CHANGELOG.md` and a migration path in `docs/` before the removal ships. The `skyhook.nvidia.com` to `nodewright.nvidia.com` transition documented in [`docs/getting-started/migration.md`](docs/getting-started/migration.md) is the reference example.

## Changing This Document

Amendments follow the supermajority rule above: a pull request plus approval from a two-thirds supermajority of non-emeritus maintainers.
