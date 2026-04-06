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

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/nodewright/operator/cmd/cli/app/deploymentpolicy"
	"github.com/NVIDIA/nodewright/operator/cmd/cli/app/node"
	pkg "github.com/NVIDIA/nodewright/operator/cmd/cli/app/package"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	internalVersion "github.com/NVIDIA/nodewright/operator/internal/version"
)

// ExitCode represents the exit code for the CLI
type ExitCode int

// Exit codes
const (
	ExitCodeSuccess ExitCode = 0
	ExitCodeError   ExitCode = 1
)

// ToInt converts the ExitCode to an integer
func (e ExitCode) ToInt() int {
	return int(e)
}

// NewSkyhookCommand creates the root skyhook command with all subcommands.
func NewSkyhookCommand(ctx *context.CLIContext) *cobra.Command {
	// skyhookCmd represents the root command
	skyhookCmd := &cobra.Command{
		Use:           "skyhook",
		Short:         "Skyhook SRE plugin",
		Long:          "kubectl-compatible helper for managing Skyhook deployments.",
		Version:       internalVersion.Summary(),
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return ctx.GlobalFlags.Validate()
		},
	}

	// Add global flags
	ctx.GlobalFlags.AddFlags(skyhookCmd.PersistentFlags())

	// Customize the version output template
	skyhookCmd.SetVersionTemplate("Skyhook plugin: {{.Version}}\n")

	// Add subcommands
	skyhookCmd.AddCommand(
		NewVersionCmd(ctx),
		pkg.NewPackageCmd(ctx),
		node.NewNodeCmd(ctx),
		deploymentpolicy.NewDeploymentPolicyCmd(ctx),
		NewPauseCmd(ctx),
		NewResumeCmd(ctx),
		NewDisableCmd(ctx),
		NewEnableCmd(ctx),
		NewResetCmd(ctx),
	)

	return skyhookCmd
}

// Execute runs the Skyhook CLI with the given context.
// If ctx is nil, a default context is created.
func Execute(config *context.CLIConfig) ExitCode {
	// create Context from config, if nil, create a default context
	ctx := context.NewCLIContext(config)

	// execute CLI with the created context
	if err := NewSkyhookCommand(ctx).Execute(); err != nil {
		// Log returned error and return error exit code
		_, _ = fmt.Fprintln(config.ErrorWriter, err.Error())
		return ExitCodeError
	}

	return ExitCodeSuccess
}
