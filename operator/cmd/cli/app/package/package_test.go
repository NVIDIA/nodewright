/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package pkg

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
)

var _ = Describe("Package Command", func() {
	Describe("NewPackageCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewPackageCmd(ctx)

			Expect(cmd.Use).To(Equal("package"))
			Expect(cmd.Short).To(Equal("Manage Skyhook packages"))
			Expect(cmd.Long).To(ContainSubstring("package command group"))
		})

		It("should register all subcommands", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewPackageCmd(ctx)

			subcommands := make([]string, 0)
			for _, sub := range cmd.Commands() {
				subcommands = append(subcommands, sub.Name())
			}

			Expect(subcommands).To(ContainElement("rerun"))
			Expect(subcommands).To(ContainElement("status"))
			Expect(subcommands).To(ContainElement("logs"))
			Expect(subcommands).To(HaveLen(3))
		})

		It("should display help correctly", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewPackageCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetArgs([]string{"--help"})

			err := cmd.Execute()
			Expect(err).NotTo(HaveOccurred())

			helpText := output.String()
			Expect(helpText).To(ContainSubstring("package command group"))
			Expect(helpText).To(ContainSubstring("Available Commands"))
			Expect(helpText).To(ContainSubstring("rerun"))
			Expect(helpText).To(ContainSubstring("status"))
			Expect(helpText).To(ContainSubstring("logs"))
		})

		It("should include use cases in long description", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewPackageCmd(ctx)

			Expect(cmd.Long).To(ContainSubstring("Re-running a failed package"))
			Expect(cmd.Long).To(ContainSubstring("Query package status"))
			Expect(cmd.Long).To(ContainSubstring("Retrieve package logs"))
		})
	})
})
