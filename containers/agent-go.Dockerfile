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

ARG GO_VERSION
ARG DEBIAN_VERSION
ARG DISTROLESS_VERSION
ARG DISTROLESS_DIGEST_SUFFIX=

FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS builder

ARG TARGETOS
ARG TARGETARCH
ARG AGENT_VERSION
ARG GIT_SHA

WORKDIR /workspace
COPY ./ ./

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -mod=vendor \
    -trimpath \
    -ldflags "-s -w \
    -X github.com/NVIDIA/nodewright/agent/internal/version.Version=${AGENT_VERSION} \
    -X github.com/NVIDIA/nodewright/agent/internal/version.GitSHA=${GIT_SHA}" \
    -o /out/agent ./cmd/agent

FROM nvcr.io/nvidia/distroless/static:v${DISTROLESS_VERSION}${DISTROLESS_DIGEST_SUFFIX}

ARG GO_VERSION
ARG DISTROLESS_VERSION
ARG DISTROLESS_DIGEST_SUFFIX
ARG AGENT_VERSION
ARG GIT_SHA

LABEL org.opencontainers.image.base.name="nvcr.io/nvidia/distroless/static:v${DISTROLESS_VERSION}${DISTROLESS_DIGEST_SUFFIX}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="nodewright-agent-go" \
      org.opencontainers.image.version="${AGENT_VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      go.version="${GO_VERSION}" \
      distroless.version="${DISTROLESS_VERSION}"

COPY --from=builder /out/agent /usr/local/bin/agent

# Run as root so the agent can chroot into the host filesystem.
USER 0:0

ENTRYPOINT ["/usr/local/bin/agent"]
