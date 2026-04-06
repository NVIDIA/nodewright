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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
)

func TestDeploymentPolicy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DeploymentPolicy CLI Tests Suite")
}

var _ = Describe("DeploymentPolicy Command", func() {
	Describe("NewDeploymentPolicyCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewDeploymentPolicyCmd(ctx)

			Expect(cmd.Use).To(Equal("deployment-policy"))
			Expect(cmd.Short).To(Equal("Manage deployment policy state"))
			Expect(cmd.Long).To(ContainSubstring("deployment-policy command group"))
		})

		It("should have dp alias", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewDeploymentPolicyCmd(ctx)

			Expect(cmd.Aliases).To(ContainElement("dp"))
		})

		It("should register reset subcommand", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewDeploymentPolicyCmd(ctx)

			subcommands := make([]string, 0)
			for _, sub := range cmd.Commands() {
				subcommands = append(subcommands, sub.Name())
			}

			Expect(subcommands).To(ContainElement("reset"))
			Expect(subcommands).To(HaveLen(1))
		})

		It("should display help correctly", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewDeploymentPolicyCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetArgs([]string{"--help"})

			err := cmd.Execute()
			Expect(err).NotTo(HaveOccurred())

			helpText := output.String()
			Expect(helpText).To(ContainSubstring("deployment-policy command group"))
			Expect(helpText).To(ContainSubstring("Available Commands"))
			Expect(helpText).To(ContainSubstring("reset"))
		})

		It("should include use cases in long description", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewDeploymentPolicyCmd(ctx)

			Expect(cmd.Long).To(ContainSubstring("Reset batch state"))
			Expect(cmd.Long).To(ContainSubstring("start a rollout fresh"))
		})
	})
})
