/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package command

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("chroot command execution", func() {
	It("executes a command inside the target root", func() {
		if os.Geteuid() != 0 {
			Fail("chroot execution requires root privileges")
		}

		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, "bin"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "work"), 0o755)).To(Succeed())
		data, err := os.ReadFile(commandTestExecutable())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(root, "bin", "helper"), data, 0o700)).To(Succeed())
		cmd := NewCommand(
			"/bin/helper",
			WithArguments("-test.run=^TestCommand$", "--", "exit", "0"),
			WithWorkingDirectory("/work"),
		)

		result, err := NewRunner(WithChroot(root)).Run(context.Background(), cmd)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{ExitCode: SuccessExitCode}))
	})
})
