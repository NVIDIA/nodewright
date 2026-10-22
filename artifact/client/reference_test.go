// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
)

var sha = digest.Digest("sha256:" + strings.Repeat("ab", 32))

var _ = Describe("Reference", func() {
	DescribeTable("NewReference normalises as the kubelet does",
		func(image, version, containerSHA string, want Reference) {
			Expect(NewReference(image, version, containerSHA)).To(Equal(want))
		},
		Entry("a containerSHA wins and the version is not validated", "ghcr.io/org/app", "not a tag", string(sha),
			Reference{Registry: "ghcr.io", Repository: "org/app", Digest: sha}),
		Entry("a bare name is an official Docker Hub image", "alpine", "3.20", "",
			Reference{Registry: "docker.io", Repository: "library/alpine", Tag: "3.20"}),
		Entry("a lower-case first segment is a Docker Hub user", "user/app", "1.0", "",
			Reference{Registry: "docker.io", Repository: "user/app", Tag: "1.0"}),
		Entry("docker.io gains library/", "docker.io/alpine", "1.0", "",
			Reference{Registry: "docker.io", Repository: "library/alpine", Tag: "1.0"}),
		Entry("index.docker.io is docker.io", "index.docker.io/user/app", "1.0", "",
			Reference{Registry: "docker.io", Repository: "user/app", Tag: "1.0"}),
		Entry("a bare localhost is an image name", "localhost", "1.0", "",
			Reference{Registry: "docker.io", Repository: "library/localhost", Tag: "1.0"}),
		Entry("localhost before a slash is a registry", "localhost/app", "1.0", "",
			Reference{Registry: "localhost", Repository: "app", Tag: "1.0"}),
		Entry("a registry with a port", "localhost:5000/org/app", "1.0", "",
			Reference{Registry: "localhost:5000", Repository: "org/app", Tag: "1.0"}),
		Entry("an upper-case first segment is a registry", "Registry/app", "1.0", "",
			Reference{Registry: "Registry", Repository: "app", Tag: "1.0"}),
		Entry("a nested repository", "ghcr.io/nvidia/skyhook-packages/shellscript", "1.2.3", "",
			Reference{Registry: "ghcr.io", Repository: "nvidia/skyhook-packages/shellscript", Tag: "1.2.3"}),
	)

	DescribeTable("NewReference rejects",
		func(image, version, containerSHA string) {
			_, err := NewReference(image, version, containerSHA)
			Expect(err).To(MatchError(ErrInvalidReference))
		},
		Entry("an empty image", "", "1.0", ""),
		Entry("whitespace", "ghcr.io/org/my app", "1.0", ""),
		Entry("an inline digest", "ghcr.io/org/app@"+string(sha), "1.0", ""),
		Entry("an empty @ separator", "ghcr.io/org/app@", "1.0", ""),
		Entry("an inline tag", "ghcr.io/org/app:1.0", "1.0", ""),
		Entry("an empty : separator", "ghcr.io/org/app:", "1.0", ""),
		Entry("a tag after a port", "localhost:5000/org/app:1.0", "1.0", ""),
		Entry("upper case in the repository", "ghcr.io/org/App", "1.0", ""),
		Entry("a 64-hex image", strings.Repeat("ab", 32), "1.0", ""),
		Entry("an invalid registry host", "my_registry.io/app", "1.0", ""),
		Entry("an upper-case containerSHA", "ghcr.io/org/app", "1.0", strings.ToUpper(string(sha))),
		Entry("a non-sha256 containerSHA", "ghcr.io/org/app", "1.0", "sha512:"+strings.Repeat("ab", 64)),
		Entry("an empty version without containerSHA", "ghcr.io/org/app", "", ""),
		Entry("an invalid tag without containerSHA", "ghcr.io/org/app", "1.0+build", ""),
	)

	DescribeTable("names Docker Hub docker.io but calls its API host",
		func(image, name, apiHost string) {
			ref, err := NewReference(image, "1.0", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(ref.Name()).To(Equal(name))
			Expect(ref.String()).To(Equal(name + ":1.0"))
			Expect(ref.APIHost()).To(Equal(apiHost))
		},
		Entry("Docker Hub", "alpine", "docker.io/library/alpine", "registry-1.docker.io"),
		Entry("legacy Docker Hub", "index.docker.io/user/app", "docker.io/user/app", "registry-1.docker.io"),
		Entry("another registry", "ghcr.io/org/app", "ghcr.io/org/app", "ghcr.io"),
		Entry("a registry with a port", "localhost:5000/app", "localhost:5000/app", "localhost:5000"),
	)

	DescribeTable("ParsePinned round-trips the String of a digest Reference",
		func(image string) {
			ref, err := NewReference(image, "", string(sha))
			Expect(err).NotTo(HaveOccurred())
			Expect(ref.String()).To(HaveSuffix("@" + string(sha)))
			Expect(ParsePinned(ref.String())).To(Equal(ref))
		},
		Entry("Docker Hub", "alpine"),
		Entry("a registry with a port", "localhost:5000/org/app"),
		Entry("a nested repository", "ghcr.io/nvidia/skyhook-packages/shellscript"),
	)

	DescribeTable("ParseReference round-trips the String of a Reference",
		func(image, version, containerSHA string) {
			ref, err := NewReference(image, version, containerSHA)
			Expect(err).NotTo(HaveOccurred())
			Expect(ParseReference(ref.String())).To(Equal(ref))
		},
		Entry("a tag on Docker Hub", "alpine", "3.20", ""),
		Entry("a tag on a registry with a port", "localhost:5000/org/app", "1.0", ""),
		Entry("a digest", "ghcr.io/nvidia/skyhook-packages/shellscript", "", string(sha)),
	)

	DescribeTable("ParseReference rejects",
		func(s string) {
			_, err := ParseReference(s)
			Expect(err).To(MatchError(ErrInvalidReference))
		},
		Entry("no tag or digest", "ghcr.io/org/app"),
		Entry("a port and no tag", "localhost:5000/app"),
		Entry("an empty tag", "ghcr.io/org/app:"),
		Entry("an invalid tag", "ghcr.io/org/app:-1"),
		Entry("a tag beside a digest", "ghcr.io/org/app:1.0@"+string(sha)),
	)

	It("ParsePinned normalises the name", func() {
		Expect(ParsePinned("alpine@" + string(sha))).To(Equal(Reference{Registry: "docker.io", Repository: "library/alpine", Digest: sha}))
	})

	DescribeTable("ParsePinned rejects",
		func(s string) {
			_, err := ParsePinned(s)
			Expect(err).To(MatchError(ErrInvalidReference))
		},
		Entry("a tag", "ghcr.io/org/app:1.0"),
		Entry("a tag beside a digest", "ghcr.io/org/app:1.0@"+string(sha)),
		Entry("a missing digest", "ghcr.io/org/app"),
		Entry("an empty digest", "ghcr.io/org/app@"),
		Entry("a short digest", "ghcr.io/org/app@sha256:abcd"),
		Entry("an upper-case digest", "ghcr.io/org/app@"+strings.ToUpper(string(sha))),
		Entry("an md5 digest", "ghcr.io/org/app@md5:"+strings.Repeat("ab", 16)),
	)
})
