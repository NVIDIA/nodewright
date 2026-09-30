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

package step

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("shared step execution in a chroot", func() {
	It("executes a command inside the mounted root", func() {
		if os.Geteuid() != 0 {
			Fail("chroot execution requires root privileges")
		}

		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, "steps"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "package"), 0o755)).To(Succeed())
		testExecutable, err := filepath.Abs(os.Args[0])
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(testExecutable)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(root, "steps", "step-helper"), data, 0o700)).To(Succeed())
		config, err := execution.NewConfig(
			execution.WithRootMount(root),
			execution.WithStepRoot("/steps"),
			execution.WithSkyhookDir("/package"),
			execution.WithRunOutput(io.Discard, io.Discard),
		)
		Expect(err).NotTo(HaveOccurred())
		value := NewRegularStep(
			"step-helper",
			WithArguments([]string{"-test.run=^TestStep$", "--", "exit", "0"}),
		)
		status, err := value.Run(context.Background(), config)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("adds execute bits without clearing package-provided permissions", func() {
		if os.Geteuid() != 0 {
			Fail("chroot execution requires root privileges")
		}

		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, "steps"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "package"), 0o755)).To(Succeed())
		testExecutable, err := filepath.Abs(os.Args[0])
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(testExecutable)
		Expect(err).NotTo(HaveOccurred())
		executable := filepath.Join(root, "steps", "step-helper")
		Expect(os.WriteFile(executable, data, 0o640)).To(Succeed())
		config, err := execution.NewConfig(
			execution.WithRootMount(root),
			execution.WithStepRoot("/steps"),
			execution.WithSkyhookDir("/package"),
			execution.WithRunOutput(io.Discard, io.Discard),
		)
		Expect(err).NotTo(HaveOccurred())
		value := NewRegularStep(
			"step-helper",
			WithArguments([]string{"-test.run=^TestStep$", "--", "exit", "0"}),
		)

		status, err := value.Run(context.Background(), config)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		info, err := os.Stat(executable)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o751)))
	})
})
