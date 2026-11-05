#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Prints the release branch a component's tag is cut from, for example
#   operator/v0.20.0       -> release/v0.20.x
#   agent/v7.0.0-rc.2      -> release/agent/v7.0.x
#   cli/v0.5.1             -> release/cli/v0.5.x
#
# The branch families are the ones scripts/release-tag.sh enforces when a tag is
# cut. The vulnerability scan files a released image's findings under this
# branch, so they read as "what this release line ships" rather than as findings
# on main.
set -euo pipefail

tag="${1:?usage: release-branch.sh <component>/vX.Y.Z[-suffix]}"
component="${tag%%/*}"
version="${tag#*/}"

if [[ ! "${version}" =~ ^v([0-9]+)\.([0-9]+)\.[0-9]+ ]]; then
  echo "release-branch.sh: ${tag} is not <component>/vX.Y.Z" >&2
  exit 1
fi
line="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.x"

case "${component}" in
  agent | cli) echo "release/${component}/${line}" ;;
  operator | chart) echo "release/${line}" ;;
  *)
    echo "release-branch.sh: unknown component ${component}" >&2
    exit 1
    ;;
esac
