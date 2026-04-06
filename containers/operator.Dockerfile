# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Build the manager binary
ARG GO_VERSION
ARG DEBIAN_VERSION
ARG DISTROLESS_VERSION

FROM golang:${GO_VERSION}-${DEBIAN_VERSION} as builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG GIT_SHA
ARG GO_VERSION

WORKDIR /workspace

COPY ./ ./

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -mod=vendor \
    -ldflags "-X github.com/NVIDIA/nodewright/operator/internal/version.GIT_SHA=${GIT_SHA}\
    -X github.com/NVIDIA/nodewright/operator/internal/version.VERSION=${VERSION}" \
    -a -o manager cmd/manager/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless/tree/main/base for more 
FROM nvcr.io/nvidia/distroless/go:v${DISTROLESS_VERSION}

ARG DISTROLESS_VERSION
ARG GO_VERSION
ARG VERSION
ARG GIT_SHA

## https://github.com/opencontainers/image-spec/blob/main/annotations.md
LABEL org.opencontainers.image.base.name="nvcr.io/nvidia/distroless/go:v${DISTROLESS_VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="skyhook-operator" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      go.version="${GO_VERSION}" \
      distroless.version="${DISTROLESS_VERSION}"

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

EXPOSE 8080 8081

ENTRYPOINT ["/manager"]
