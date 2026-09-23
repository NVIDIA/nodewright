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

var _ = Describe("GetVersion", func() {
	BeforeEach(func() {
		version, gitSHA := Version, GitSHA
		DeferCleanup(func() {
			Version, GitSHA = version, gitSHA
		})
	})

	It("returns the semantic version when it is available", func() {
		Version, GitSHA = "6.5.0", "abc1234"

		Expect(GetVersion()).To(Equal("6.5.0"))
	})

	It("falls back to the git SHA", func() {
		Version, GitSHA = "", "abc1234"

		Expect(GetVersion()).To(Equal("abc1234"))
	})

	It("returns unknown when no build metadata is available", func() {
		Version, GitSHA = "", ""

		Expect(GetVersion()).To(Equal("unknown"))
	})
})
