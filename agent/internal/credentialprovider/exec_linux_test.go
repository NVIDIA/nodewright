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

package credentialprovider

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ExecRunner with a root", func() {
	It("runs a plugin at its host path inside the root", func() {
		if os.Geteuid() != 0 {
			Fail("chroot execution requires root privileges")
		}
		root := GinkgoT().TempDir()
		executable, err := filepath.Abs(os.Args[0])
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(executable)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(root, testBinDir), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, testBinDir, "plugin"), data, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "marker"), []byte("inside the root"), 0o600)).To(Succeed())

		stdout, err := ExecRunner(root)(context.Background(), Command{
			Path: filepath.Join(testBinDir, "plugin"),
			Env:  []string{pluginHelperEnv + "=/marker"},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(string(stdout)).To(Equal("inside the root"))
	})
})
