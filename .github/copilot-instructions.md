<!--
  SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
  SPDX-License-Identifier: Apache-2.0
-->

# Copilot Instructions for Skyhook (NodeWright)

GitHub Copilot should follow this project's canonical coding-agent rules. Those
rules (repository architecture, the Status / State / Stage vocabulary, the
level-triggered reconcile pattern, error/context/logging conventions, required
`make` targets, commit signing, and the pull request process) live in one place
and are mirrored for every tool that reads this directory.

Do not duplicate that content here. Read the canonical sources directly:

- **[AGENTS.md](../AGENTS.md)** is the canonical agent guide (a symlink to
  `.claude/CLAUDE.md`); treat it as authoritative for code style, architecture,
  and the working rules.
- **[CONTRIBUTING.md](../CONTRIBUTING.md)** covers DCO sign-off, commit signing,
  the AI-assisted contributions policy, and how to open a PR, including what
  "ready to open" means: run the tests and linters locally, say in the pull
  request which ones you ran and which you could not, and disclose AI assistance.
- **[docs/README.md](../docs/README.md)** indexes the domain docs (interrupt
  flow, deployment policy, versioning, and the behavioral contracts that most
  bugs in this codebase come from ignoring).

When in doubt, prefer the existing patterns in the package you are editing over
introducing new ones, and keep `docs/` in sync with any behavior change in the
same PR.
