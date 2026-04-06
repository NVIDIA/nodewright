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
	"bytes"
	"os"
	"testing"

	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

func TestCLISmoke(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI Test Suite")
}

var _ = Describe("Skyhook CLI Tests", func() {
	var rootCmd *cobra.Command
	var testCtx *context.CLIContext
	var output *bytes.Buffer

	BeforeEach(func() {
		config := context.NewCLIConfig()
		testCtx = context.NewCLIContext(config)
		rootCmd = NewSkyhookCommand(testCtx)

		output = &bytes.Buffer{}
		rootCmd.SetOut(output)
		rootCmd.SetErr(output)
	})

	Describe("Command Structure", func() {
		It("should have all main commands registered", func() {
			commandNames := getCommandNames(rootCmd)
			Expect(commandNames).To(ContainElement("package"))
		})

		It("should have complete subcommand structure", func() {
			// Verify package subcommands
			packageCmd := findCommand(rootCmd, "package")
			Expect(packageCmd).NotTo(BeNil())
			packageSubs := getCommandNames(packageCmd)
			expectedPackageCommands := []string{
				"rerun <package-name>",
				"status <package-name>",
				"logs <package-name>",
			}
			for _, expected := range expectedPackageCommands {
				Expect(packageSubs).To(ContainElement(expected))
			}

			// Verify node subcommands
			nodeCmd := findCommand(rootCmd, "node")
			Expect(nodeCmd).NotTo(BeNil())
			nodeSubs := getCommandNames(nodeCmd)
			expectedNodeCommands := []string{
				"list",
				"status [node-name...]",
				"reset <node-name...>",
				"ignore <node-name...>",
				"unignore <node-name...>",
			}
			for _, expected := range expectedNodeCommands {
				Expect(nodeSubs).To(ContainElement(expected))
			}
		})
	})

	Describe("Help and Version", func() {
		It("should display help correctly", func() {
			rootCmd.SetArgs([]string{"--help"})
			err := rootCmd.Execute()
			Expect(err).NotTo(HaveOccurred())
			helpText := output.String()
			Expect(helpText).To(ContainSubstring("kubectl-compatible helper for managing Skyhook deployments."))
			Expect(helpText).To(ContainSubstring("Available Commands:"))
		})

		It("should display version correctly", func() {
			rootCmd.SetArgs([]string{"--version"})
			err := rootCmd.Execute()
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Skyhook plugin: "))
		})
	})

	Describe("Global Flags", func() {
		It("should have all flags bound with correct properties", func() {
			persistentFlags := rootCmd.PersistentFlags()

			// Verify all flags exist with correct shortcuts
			Expect(persistentFlags.Lookup("verbose")).NotTo(BeNil())
			Expect(persistentFlags.Lookup("dry-run")).NotTo(BeNil())
		})

		It("should bind flags to config.Flags struct", func() {
			pflags := rootCmd.PersistentFlags()

			// Set flags and verify they update config.Flags
			Expect(pflags.Set("verbose", "true")).To(Succeed())
			Expect(pflags.Set("dry-run", "true")).To(Succeed())
			Expect(testCtx.GlobalFlags.Verbose).To(BeTrue())
			Expect(testCtx.GlobalFlags.DryRun).To(BeTrue())
		})

		It("should validate output format via PersistentPreRunE", func() {
			// Invalid format should fail - need fresh command since flags persist
			freshConfig := context.NewCLIConfig()
			freshCtx := context.NewCLIContext(freshConfig)
			freshCmd := NewSkyhookCommand(freshCtx)
			freshCmd.SetArgs([]string{"--output", "invalid", "version"})
			err := freshCmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid output format"))
		})
	})

	Describe("Error Handling", func() {
		It("should handle invalid commands and arguments", func() {
			rootCmd.SetArgs([]string{"invalid-command"})
			err := rootCmd.Execute()
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("ToInt", func() {
		It("should convert ExitCode to int", func() {
			Expect(ExitCodeSuccess.ToInt()).To(Equal(0))
			Expect(ExitCodeError.ToInt()).To(Equal(1))
		})
	})

	Describe("Execute Function", func() {
		var originalArgs []string

		BeforeEach(func() {
			originalArgs = os.Args
		})

		AfterEach(func() {
			os.Args = originalArgs
		})

		It("should execute successfully and return correct exit code", func() {
			os.Args = []string{"skyhook", "--help"}
			exitCode := Execute(context.NewCLIConfig())
			Expect(exitCode).To(Equal(ExitCodeSuccess))
		})

		It("should return error exit code if command execution fails", func() {
			os.Args = []string{"skyhook", "invalid-command"}
			errBuf := &bytes.Buffer{}
			config := context.NewCLIConfig(context.WithErrorWriter(errBuf))
			exitCode := Execute(config)
			Expect(exitCode).To(Equal(ExitCodeError))
			Expect(errBuf.String()).To(ContainSubstring("unknown command"))
		})
	})

	Describe("ExtractImageTag", func() {
		It("should extract tag from simple image reference", func() {
			tag := utils.ExtractImageTag("ghcr.io/nvidia/skyhook/operator:v1.2.3")
			Expect(tag).To(Equal("v1.2.3"))
		})

		It("should extract tag from image with digest", func() {
			tag := utils.ExtractImageTag("ghcr.io/nvidia/skyhook/operator:v1.2.3@sha256:abc123")
			Expect(tag).To(Equal("v1.2.3"))
		})

		It("should return empty for image without tag", func() {
			tag := utils.ExtractImageTag("ghcr.io/nvidia/skyhook/operator")
			Expect(tag).To(BeEmpty())
		})

		It("should handle image with only digest", func() {
			tag := utils.ExtractImageTag("ghcr.io/nvidia/skyhook/operator@sha256:abc123")
			Expect(tag).To(BeEmpty())
		})

		It("should extract latest tag", func() {
			tag := utils.ExtractImageTag("nginx:latest")
			Expect(tag).To(Equal("latest"))
		})

		It("should handle image with port in registry", func() {
			tag := utils.ExtractImageTag("localhost:5000/myimage:v1.0")
			Expect(tag).To(Equal("v1.0"))
		})
	})

	Describe("NewVersionCmd", func() {
		var versionCmd *cobra.Command

		BeforeEach(func() {
			testCtx := context.NewCLIContext(nil)
			versionCmd = NewVersionCmd(testCtx)
		})

		It("should have correct command metadata", func() {
			Expect(versionCmd.Use).To(Equal("version"))
			Expect(versionCmd.Short).To(ContainSubstring("version"))
		})

		It("should have timeout flag with default", func() {
			timeoutFlag := versionCmd.Flags().Lookup("timeout")
			Expect(timeoutFlag).NotTo(BeNil())
			Expect(timeoutFlag.DefValue).To(Equal("10s"))
		})

		It("should have client-only flag", func() {
			clientOnlyFlag := versionCmd.Flags().Lookup("client-only")
			Expect(clientOnlyFlag).NotTo(BeNil())
			Expect(clientOnlyFlag.DefValue).To(Equal("false"))
		})

		It("should show plugin version with --client-only", func() {
			output := &bytes.Buffer{}
			versionCmd.SetOut(output)
			versionCmd.SetArgs([]string{"--client-only"})

			err := versionCmd.Execute()
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Skyhook plugin:"))
		})
	})

	Describe("defaultNamespace constant", func() {
		It("should be set to skyhook", func() {
			Expect(context.DefaultNamespace).To(Equal("skyhook"))
		})
	})
})

func findCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, cmd := range parent.Commands() {
		if cmd.Use == name || cmd.Name() == name {
			return cmd
		}
	}
	return nil
}

func getCommandNames(cmd *cobra.Command) []string {
	commands := cmd.Commands()
	names := make([]string, len(commands))
	for i, cmd := range commands {
		names[i] = cmd.Use
	}
	return names
}
