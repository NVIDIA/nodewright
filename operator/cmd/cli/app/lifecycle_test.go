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

package app

import (
	"github.com/spf13/cobra"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
)

var _ = Describe("Lifecycle Commands", func() {
	type lifecycleTestCase struct {
		name           string
		cmdFactory     func(*context.CLIContext) *cobra.Command
		expectedUse    string
		expectedVerb   string
		hasConfirmFlag bool
	}

	testCases := []lifecycleTestCase{
		{
			name:           "Pause",
			cmdFactory:     NewPauseCmd,
			expectedUse:    "pause <skyhook-name>",
			expectedVerb:   "Pause",
			hasConfirmFlag: true,
		},
		{
			name:           "Resume",
			cmdFactory:     NewResumeCmd,
			expectedUse:    "resume <skyhook-name>",
			expectedVerb:   "Resume",
			hasConfirmFlag: true,
		},
		{
			name:           "Disable",
			cmdFactory:     NewDisableCmd,
			expectedUse:    "disable <skyhook-name>",
			expectedVerb:   "Disable",
			hasConfirmFlag: true,
		},
		{
			name:           "Enable",
			cmdFactory:     NewEnableCmd,
			expectedUse:    "enable <skyhook-name>",
			expectedVerb:   "Enable",
			hasConfirmFlag: true,
		},
	}

	for _, tc := range testCases {
		Describe(tc.name+" Command", func() {
			It("should create command with correct properties", func() {
				ctx := context.NewCLIContext(nil)
				cmd := tc.cmdFactory(ctx)

				Expect(cmd.Use).To(Equal(tc.expectedUse))
				Expect(cmd.Short).To(ContainSubstring(tc.expectedVerb))
			})

			It("should handle confirm flag correctly", func() {
				ctx := context.NewCLIContext(nil)
				cmd := tc.cmdFactory(ctx)

				confirmFlag := cmd.Flags().Lookup("confirm")
				if tc.hasConfirmFlag {
					Expect(confirmFlag).NotTo(BeNil())
					Expect(confirmFlag.Shorthand).To(Equal("y"))
					Expect(confirmFlag.DefValue).To(Equal("false"))
				} else {
					Expect(confirmFlag).To(BeNil())
				}
			})

			It("should require exactly one argument", func() {
				ctx := context.NewCLIContext(nil)
				cmd := tc.cmdFactory(ctx)

				err := cmd.Args(cmd, []string{})
				Expect(err).To(HaveOccurred())

				err = cmd.Args(cmd, []string{"skyhook1"})
				Expect(err).NotTo(HaveOccurred())
			})

			It("should have examples in help", func() {
				ctx := context.NewCLIContext(nil)
				cmd := tc.cmdFactory(ctx)

				Expect(cmd.Example).To(ContainSubstring("kubectl skyhook"))
			})
		})
	}
})
