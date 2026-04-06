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

package node

import (
	"bytes"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
)

func TestNode(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Node CLI Tests Suite")
}

var _ = Describe("Node Command", func() {
	Describe("NewNodeCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewNodeCmd(ctx)

			Expect(cmd.Use).To(Equal("node"))
			Expect(cmd.Short).To(Equal("Manage Skyhook nodes"))
			Expect(cmd.Long).To(ContainSubstring("node command group"))
		})

		It("should register all subcommands", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewNodeCmd(ctx)

			subcommands := make([]string, 0)
			for _, sub := range cmd.Commands() {
				subcommands = append(subcommands, sub.Name())
			}

			Expect(subcommands).To(ContainElement("list"))
			Expect(subcommands).To(ContainElement("status"))
			Expect(subcommands).To(ContainElement("reset"))
			Expect(subcommands).To(ContainElement("ignore"))
			Expect(subcommands).To(ContainElement("unignore"))
			Expect(subcommands).To(HaveLen(5))
		})

		It("should display help correctly", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewNodeCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetArgs([]string{"--help"})

			err := cmd.Execute()
			Expect(err).NotTo(HaveOccurred())

			helpText := output.String()
			Expect(helpText).To(ContainSubstring("node command group"))
			Expect(helpText).To(ContainSubstring("Available Commands"))
			Expect(helpText).To(ContainSubstring("list"))
			Expect(helpText).To(ContainSubstring("status"))
			Expect(helpText).To(ContainSubstring("reset"))
			Expect(helpText).To(ContainSubstring("ignore"))
			Expect(helpText).To(ContainSubstring("unignore"))
		})

		It("should include use cases in long description", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewNodeCmd(ctx)

			Expect(cmd.Long).To(ContainSubstring("List all nodes targeted by a Skyhook"))
			Expect(cmd.Long).To(ContainSubstring("Query all Skyhook activity"))
			Expect(cmd.Long).To(ContainSubstring("Reset all package state"))
			Expect(cmd.Long).To(ContainSubstring("Ignore/unignore nodes"))
		})
	})
})
