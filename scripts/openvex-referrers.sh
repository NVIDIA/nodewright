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

# Prints the digest of every OpenVEX attestation attached to one manifest, one
# per line.
#
# Usage: scripts/openvex-referrers.sh <repository> <sha256:digest>
#
# Each attestation is identified by the predicate type inside its signed
# payload, not by the referrer's `dev.sigstore.bundle.predicateType`
# annotation: cosign through v3.0.6 wrote that annotation as cosign/sign/v1 on
# every attestation, so on releases cut before the v3.1.3 pin an SBOM and a VEX
# document are indistinguishable by annotation (see
# docs/contributing/release-process.md). Referrers that are not DSSE-wrapped
# attestations, such as the index signature, carry no payload and are skipped.
set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <repository> <sha256:digest>" >&2
    exit 2
fi
repository="$1"
digest="$2"
if ! [[ "${digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    echo "digest must be sha256:<64 hex>, got '${digest}'" >&2
    exit 2
fi

referrers="$(timeout --foreground 120s oras discover --format json "${repository}@${digest}" |
    jq -r '.referrers[]?.digest')"

for referrer in ${referrers}; do
    layer="$(timeout --foreground 120s oras manifest fetch "${repository}@${referrer}" |
        jq -r '.layers[0].digest // empty')"
    if [ -z "${layer}" ]; then
        continue
    fi
    payload="$(timeout --foreground 120s oras blob fetch --output - "${repository}@${layer}" |
        jq -r '.dsseEnvelope.payload // empty')"
    if [ -z "${payload}" ]; then
        continue
    fi
    predicate="$(printf '%s' "${payload}" | base64 -d | jq -r '.predicateType // empty')"
    if [ "${predicate}" = "https://openvex.dev/ns" ]; then
        echo "${referrer}"
    fi
done
