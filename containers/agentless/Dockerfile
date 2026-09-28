# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
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

# The digest must stay the multi-arch index digest, not a per-platform one:
# .github/workflows/agentless-container.yaml builds linux/amd64 + linux/arm64
# from this file, and a platform-specific digest breaks the arm64 leg.
# Dependabot (docker ecosystem, /containers/**) bumps tag and digest together.
FROM docker.io/library/busybox:1.38.0@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e

## might need a better mock than empty folders existing
RUN mkdir -p /skyhook_package/skyhook_dir /skyhook_package/root_dir

ENV EXIT_CODE=0

COPY ./entrypoint.sh /

ENTRYPOINT ["/entrypoint.sh"]
