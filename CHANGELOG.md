<!--
  SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
  SPDX-License-Identifier: Apache-2.0
-->

# Changelog

Each Skyhook component is versioned and released independently.
Full changelogs are maintained per component and published as [GitHub Releases](https://github.com/NVIDIA/skyhook/releases).

## Components

| Component | Changelog |
|---|---|
| Operator | [operator/CHANGELOG.md](operator/CHANGELOG.md) |
| Agent | [agent/CHANGELOG.md](agent/CHANGELOG.md) |
| Helm Chart | [chart/CHANGELOG.md](chart/CHANGELOG.md) |
| CLI | [operator/cmd/cli/CHANGELOG.md](operator/cmd/cli/CHANGELOG.md) |

## Generating

Regenerate a component changelog from git history:

```bash
make changelog COMPONENT=operator
```

Preview unreleased changes before tagging:

```bash
make changelog-preview COMPONENT=operator
```

Regenerate all component changelogs:

```bash
make changelog-all
```
