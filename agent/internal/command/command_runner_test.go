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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("commandRunner", func() {
	It("executes against the host filesystem without a chroot", func() {
		workingDirectory := GinkgoT().TempDir()
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		cmd := helperCommand(
			"inspect",
			"first",
			"second",
			WithWorkingDirectory(workingDirectory),
			WithEnvironment(map[string]string{"NODEWRIGHT_VALUE": "configured"}),
			WithStdout(stdout),
			WithStderr(stderr),
		)

		runner := NewRunner()
		_, isCommandRunner := runner.(*commandRunner)
		Expect(isCommandRunner).To(BeTrue())

		result, err := runner.Run(context.Background(), cmd)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{ExitCode: SuccessExitCode}))
		Expect(stdout.String()).To(Equal(fmt.Sprintf(
			"arguments=first,second\nenvironment=configured\nworking-directory=%s\n",
			workingDirectory,
		)))
		Expect(stderr.String()).To(Equal("helper-stderr\n"))
	})

	It("applies requested executable permissions", func() {
		executable := filepath.Join(GinkgoT().TempDir(), "helper")
		data, err := os.ReadFile(commandTestExecutable())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(executable, data, 0o600)).To(Succeed())

		cmd := NewCommand(
			executable,
			WithArguments("-test.run=^TestCommand$", "--", "exit", "0"),
			WithPermissions(0o700),
		)
		result, err := NewRunner().Run(context.Background(), cmd)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitCode).To(Equal(SuccessExitCode))
		info, err := os.Stat(executable)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	It("adds required executable permissions without clearing existing bits", func() {
		executable := filepath.Join(GinkgoT().TempDir(), "helper")
		data, err := os.ReadFile(commandTestExecutable())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(executable, data, 0o640)).To(Succeed())

		cmd := NewCommand(
			executable,
			WithArguments("-test.run=^TestCommand$", "--", "exit", "0"),
			WithRequiredPermissions(0o111),
		)
		result, err := NewRunner().Run(context.Background(), cmd)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitCode).To(Equal(SuccessExitCode))
		info, err := os.Stat(executable)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o751)))
	})

	It("strips privilege bits when adding executable permissions", func() {
		executable := filepath.Join(GinkgoT().TempDir(), "helper")
		data, err := os.ReadFile(commandTestExecutable())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(executable, data, 0o640)).To(Succeed())
		Expect(os.Chmod(executable, 0o640|os.ModeSetuid)).To(Succeed())
		before, err := os.Stat(executable)
		Expect(err).NotTo(HaveOccurred())
		if before.Mode()&os.ModeSetuid == 0 {
			Skip("test filesystem does not preserve setuid mode bits")
		}

		cmd := NewCommand(
			executable,
			WithArguments("-test.run=^TestCommand$", "--", "exit", "0"),
			WithRequiredPermissions(0o111),
		)
		result, err := NewRunner().Run(context.Background(), cmd)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitCode).To(Equal(SuccessExitCode))
		after, err := os.Stat(executable)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Mode() & os.ModeSetuid).To(BeZero())
		Expect(after.Mode().Perm()).To(Equal(os.FileMode(0o751)))
	})
})
