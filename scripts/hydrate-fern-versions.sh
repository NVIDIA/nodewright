#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Materialize the frozen docs content each registered Fern version points at.
#
# fern/versions/vX.Y.Z.yml is committed, but the pages it names live under
# fern/versions/vX.Y.Z-content/ and are NOT — they are extracted from the
# matching chart/vX.Y.Z tag on every build so a released version keeps serving
# the docs as they were when it shipped. Anything that runs the Fern CLI over
# the registry has to call this first or the nav manifest dangles and the CLI
# fails with a bare ENOENT on a path that exists in no commit.
#
# Requires tags to be present: pair with fetch-tags (or fetch-depth: 0) on
# actions/checkout. Run from the repository root. Safe to re-run.

set -euo pipefail

shopt -s nullglob

for version_file in fern/versions/v*.yml; do
	version=$(basename "$version_file" .yml)
	# Registry files are named by the stripped version; the tag they were cut
	# from still carries the chart/ prefix.
	tag="chart/${version}"

	if ! git show-ref --verify --quiet "refs/tags/${tag}"; then
		echo "::error::Tag ${tag} not found — cannot load frozen docs content for ${version}"
		exit 1
	fi

	mkdir -p "fern/versions/${version}-content"
	git archive "refs/tags/${tag}" -- docs/ \
		| tar -x --strip-components=1 -C "fern/versions/${version}-content"

	# Escape {, }, < for MDX — but only outside fenced code blocks and inline code spans
	find "fern/versions/${version}-content" -name '*.md' -print0 | while IFS= read -r -d '' f; do
		awk '
			/^````*/ || /^~~~~*/ { fence = !fence; print; next }
			fence { print; next }
			{
				n = split($0, p, "`")
				out = ""
				for (i = 1; i <= n; i++) {
					if (i % 2 == 1) {
						gsub(/{/, "\\{", p[i])
						gsub(/}/, "\\}", p[i])
						gsub(/</, "\\&lt;", p[i])
					}
					out = out p[i]
					if (i < n) out = out "`"
				}
				print out
			}
		' "$f" > "${f}.tmp" && mv "${f}.tmp" "$f"
	done

	echo "Extracted docs from ${version}"
done
