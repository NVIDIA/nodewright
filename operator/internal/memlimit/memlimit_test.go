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

package memlimit

import (
	"math"
	"os"
	"path/filepath"
	"runtime/debug"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Configure", func() {
	const gib = int64(1) << 30

	var root string

	writeCgroupFile := func(name, content string) {
		path := filepath.Join(root, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
	}

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		// Configure changes process-wide runtime state; put back whatever the suite started with.
		DeferCleanup(debug.SetMemoryLimit, debug.SetMemoryLimit(-1))
		GinkgoT().Setenv(GoMemLimitEnv, "")
		debug.SetMemoryLimit(math.MaxInt64)
	})

	It("sets a fraction of the cgroup v2 limit", func() {
		writeCgroupFile("memory.max", "4294967296\n")

		result, err := Configure(root, 0.9)

		Expect(err).NotTo(HaveOccurred())
		// 0.9 × 4GiB is 3865470566.4 bytes; the fraction rounds down.
		Expect(result).To(Equal(Result{Source: SourceCgroupV2, ContainerLimit: 4 * gib, SoftLimit: 3865470566}))
		Expect(debug.SetMemoryLimit(-1)).To(Equal(result.SoftLimit))
	})

	It("sets a fraction of the cgroup v1 limit", func() {
		writeCgroupFile("memory/memory.limit_in_bytes", "2147483648\n")

		result, err := Configure(root, 0.5)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Source: SourceCgroupV1, ContainerLimit: 2 * gib, SoftLimit: gib}))
		Expect(debug.SetMemoryLimit(-1)).To(Equal(gib))
	})

	It("prefers cgroup v2 when both files are present", func() {
		writeCgroupFile("memory.max", "1073741824\n")
		writeCgroupFile("memory/memory.limit_in_bytes", "2147483648\n")

		result, err := Configure(root, 1)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Source).To(Equal(SourceCgroupV2))
		Expect(result.SoftLimit).To(Equal(gib))
	})

	DescribeTable("leaves the runtime alone when the container has no limit",
		func(files map[string]string) {
			for name, content := range files {
				writeCgroupFile(name, content)
			}

			result, err := Configure(root, 0.9)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(Result{Source: SourceNone, SoftLimit: math.MaxInt64}))
			Expect(debug.SetMemoryLimit(-1)).To(Equal(int64(math.MaxInt64)))
		},
		Entry("cgroup v2 reports max", map[string]string{"memory.max": "max\n"}),
		Entry("cgroup v1 reports the page counter maximum", map[string]string{"memory/memory.limit_in_bytes": "9223372036854771712\n"}),
		Entry("no cgroup memory files", map[string]string{}),
	)

	It("leaves an explicit GOMEMLIMIT untouched", func() {
		writeCgroupFile("memory.max", "4294967296\n")
		GinkgoT().Setenv(GoMemLimitEnv, "3GiB")
		// The runtime parses GOMEMLIMIT once at startup, so stand in for that here.
		debug.SetMemoryLimit(3 * gib)

		result, err := Configure(root, 0.9)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Source: SourceGoMemLimit, SoftLimit: 3 * gib}))
		Expect(debug.SetMemoryLimit(-1)).To(Equal(3 * gib))
	})

	It("rejects an invalid fraction even when GOMEMLIMIT is set", func() {
		GinkgoT().Setenv(GoMemLimitEnv, "3GiB")

		_, err := Configure(root, 1.5)

		Expect(err).To(MatchError(ContainSubstring("memory limit fraction")))
	})

	DescribeTable("rejects a fraction outside (0, 1]",
		func(fraction float64) {
			writeCgroupFile("memory.max", "4294967296\n")

			_, err := Configure(root, fraction)

			Expect(err).To(MatchError(ContainSubstring("memory limit fraction")))
			Expect(debug.SetMemoryLimit(-1)).To(Equal(int64(math.MaxInt64)))
		},
		Entry("zero", 0.0),
		Entry("negative", -0.5),
		Entry("above one", 1.1),
		Entry("NaN", math.NaN()),
		Entry("small enough to round the soft limit down to zero", 1e-12),
	)

	DescribeTable("fails on a malformed limit",
		func(name, content string) {
			writeCgroupFile(name, content)

			_, err := Configure(root, 0.9)

			Expect(err).To(MatchError(ContainSubstring("parsing " + name)))
			Expect(debug.SetMemoryLimit(-1)).To(Equal(int64(math.MaxInt64)))
		},
		Entry("cgroup v2 garbage", "memory.max", "lots\n"),
		Entry("cgroup v1 zero", "memory/memory.limit_in_bytes", "0\n"),
	)
})
