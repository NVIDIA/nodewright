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

package version

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestVersion(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Version Suite")
}

var _ = Describe("version", func() {

	// GIT_SHA and VERSION are set by ldflags at build time, so every spec that reads
	// them has to put back whatever this binary was built with.
	BeforeEach(func() {
		sha, ver := GIT_SHA, VERSION
		DeferCleanup(func() {
			GIT_SHA, VERSION = sha, ver
		})
	})

	Describe("IsValid", func() {
		DescribeTable("accepts semver with or without the v prefix",
			func(version string, expected bool) {
				Expect(IsValid(version)).To(Equal(expected))
			},
			Entry("empty", "", false),
			Entry("with v prefix", "v1.2.3", true),
			Entry("without v prefix", "1.2.3", true),
			Entry("major.minor only", "0.15", true),
			Entry("prerelease", "v1.2.3-rc.1", true),
			Entry("not a version", "dev", false),
			Entry("trailing junk", "1.2.3.4", false),
		)
	})

	Describe("Compare", func() {
		DescribeTable("orders versions regardless of the v prefix",
			func(left, right string, expected int) {
				Expect(Compare(left, right)).To(Equal(expected))
			},
			Entry("equal with prefix", "v1.2.3", "v1.2.3", 0),
			Entry("equal with the prefix missing on the left", "1.2.3", "v1.2.3", 0),
			Entry("equal with the prefix missing on the right", "v1.2.3", "1.2.3", 0),
			Entry("equal with the prefix missing on both", "1.2.3", "1.2.3", 0),
			Entry("left is older", "v1.2.3", "v1.3.0", -1),
			Entry("left is newer", "v2.0.0", "v1.9.9", 1),
			Entry("prerelease sorts before its release", "v1.2.3-rc.1", "v1.2.3", -1),
			Entry("empty on the right returns -2", "v1.2.3", "", -2),
			Entry("empty on the left returns -2", "", "v1.2.3", -2),
			Entry("empty on both sides returns -2", "", "", -2),
			Entry("invalid on the left returns -2", "dev", "v1.2.3", -2),
			Entry("invalid on the right returns -2", "v1.2.3", "dev", -2),
		)
	})

	Describe("MajorMinor", func() {
		DescribeTable("reduces a version to major.minor",
			func(version, expected string) {
				Expect(MajorMinor(version)).To(Equal(expected))
			},
			Entry("empty", "", ""),
			Entry("with v prefix", "v1.2.3", "v1.2"),
			Entry("without v prefix", "1.2.3", "v1.2"),
			Entry("already major.minor", "v0.15", "v0.15"),
			Entry("prerelease is dropped", "v1.2.3-rc.1", "v1.2"),
			Entry("not a version", "dev", ""),
		)
	})

	Describe("GetVersion", func() {
		It("should return the version the binary was built with", func() {
			VERSION = "v0.15.0"
			Expect(GetVersion()).To(Equal("v0.15.0"))
		})
	})

	Describe("Summary", func() {
		It("should combine version and git sha when both are known", func() {
			VERSION, GIT_SHA = "v0.15.0", "abc1234"
			Expect(Summary()).To(Equal("v0.15.0 (abc1234)"))
		})

		It("should return the version alone when the sha is unknown", func() {
			VERSION, GIT_SHA = "v0.15.0", "unknown"
			Expect(Summary()).To(Equal("v0.15.0"))
		})

		It("should return the version alone when the sha is empty", func() {
			VERSION, GIT_SHA = "v0.15.0", ""
			Expect(Summary()).To(Equal("v0.15.0"))
		})
	})
})
