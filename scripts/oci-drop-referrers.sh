#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Removes referrers from a subject's referrers fallback index, so a referrer
# manifest can then be deleted without leaving a dangling descriptor behind.
#
# Usage: scripts/oci-drop-referrers.sh <repository> <subject sha256:digest> <referrer sha256:digest>...
#
# A registry without the OCI referrers API (GHCR is one) lists a subject's
# referrers in an ordinary index tagged sha256-<subject hex>. Deleting a
# referrer's manifest does not edit that index, so clients that enumerate
# referrers, such as `oras discover`, `oras cp -r` and cosign, keep finding a
# descriptor for a manifest that no longer exists and fail on it. This rewrites
# the index without the given digests and re-checks it. A registry that serves
# the referrers API natively has no such tag, and the script leaves it alone.
# Already-removed digests are skipped, so a re-run is safe.
set -euo pipefail

if [ "$#" -lt 3 ]; then
    echo "usage: $0 <repository> <subject sha256:digest> <referrer sha256:digest>..." >&2
    exit 2
fi
repository="$1"
subject="$2"
shift 2
for digest in "${subject}" "$@"; do
    if ! [[ "${digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
        echo "digest must be sha256:<64 hex>, got '${digest}'" >&2
        exit 2
    fi
done
drop="$(printf '%s\n' "$@" | jq -R . | jq -s .)"
tag="sha256-${subject#sha256:}"

# A fetch failure is only "no fallback index" when the tag is really absent;
# anything else (a timeout, an auth error) must stop the run rather than be
# read as a registry that needs no rewrite.
if ! index="$(timeout --foreground 120s oras manifest fetch "${repository}:${tag}" 2>/dev/null)"; then
    tags="$(timeout --foreground 300s oras repo tags "${repository}")"
    if grep -qxF "${tag}" <<<"${tags}"; then
        echo "could not fetch ${repository}:${tag}" >&2
        exit 1
    fi
    echo "${repository} has no referrers fallback index for ${subject}; nothing to rewrite"
    exit 0
fi

remaining="$(jq --argjson drop "${drop}" '[.manifests[]? | select(.digest as $d | $drop | index($d) | not)]' <<<"${index}")"
if [ "$(jq 'length' <<<"${remaining}")" -eq "$(jq '.manifests | length' <<<"${index}")" ]; then
    echo "${repository}:${tag} already lists none of the given referrers"
    exit 0
fi

rewritten="$(mktemp)"
trap 'rm -f "${rewritten}"' EXIT
jq --argjson manifests "${remaining}" '.manifests = $manifests' <<<"${index}" >"${rewritten}"
timeout --foreground 120s oras manifest push \
    --media-type "$(jq -r '.mediaType // "application/vnd.oci.image.index.v1+json"' <<<"${index}")" \
    "${repository}:${tag}" "${rewritten}" >/dev/null

after="$(timeout --foreground 120s oras manifest fetch "${repository}:${tag}")"
if jq -e --argjson drop "${drop}" '[.manifests[]?.digest] | any(. as $d | $drop | index($d))' <<<"${after}" >/dev/null; then
    echo "${repository}:${tag} still lists a dropped referrer after the rewrite" >&2
    exit 1
fi
echo "rewrote ${repository}:${tag} without: $*"
