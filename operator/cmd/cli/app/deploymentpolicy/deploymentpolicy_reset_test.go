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

package deploymentpolicy

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
)

var _ = Describe("DeploymentPolicy Reset Command", func() {
	Describe("NewResetCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			Expect(cmd.Use).To(Equal("reset <skyhook-name>"))
			Expect(cmd.Short).To(ContainSubstring("Reset batch state"))
		})

		It("should require exactly one argument", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			err := cmd.Args(cmd, []string{})
			Expect(err).To(HaveOccurred())

			err = cmd.Args(cmd, []string{"skyhook1", "skyhook2"})
			Expect(err).To(HaveOccurred())

			err = cmd.Args(cmd, []string{"skyhook1"})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should have confirm flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			confirmFlag := cmd.Flags().Lookup("confirm")
			Expect(confirmFlag).NotTo(BeNil())
			Expect(confirmFlag.Shorthand).To(Equal("y"))
		})

		It("should include examples in help", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			Expect(cmd.Example).To(ContainSubstring("kubectl skyhook deployment-policy reset"))
			Expect(cmd.Example).To(ContainSubstring("--dry-run"))
			Expect(cmd.Example).To(ContainSubstring("dp reset"))
		})

		It("should have detailed long description", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			Expect(cmd.Long).To(ContainSubstring("batch processing state"))
			Expect(cmd.Long).To(ContainSubstring("start fresh from batch 1"))
			Expect(cmd.Long).To(ContainSubstring("rollout has completed"))
		})

		It("should display help correctly", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetArgs([]string{"--help"})

			err := cmd.Execute()
			Expect(err).NotTo(HaveOccurred())

			helpText := output.String()
			Expect(helpText).To(ContainSubstring("Reset batch state"))
			Expect(helpText).To(ContainSubstring("--confirm"))
			Expect(helpText).To(ContainSubstring("-y"))
		})
	})
})
