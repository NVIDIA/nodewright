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
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
	"github.com/NVIDIA/nodewright/operator/internal/version"
)

// NewVersionCmd creates the version command.
func NewVersionCmd(ctx *cliContext.CLIContext) *cobra.Command {
	var timeout time.Duration
	var clientOnly bool

	// versionCmd represents the version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show plugin and Skyhook operator versions",
		Long: `Display version information for the Skyhook plugin and the Skyhook operator running in the cluster.

		The plugin version is always shown. By default, the command also queries the cluster
		to discover the Skyhook operator version. Use --client-only to skip the cluster query.`,
		Example: `  # Show both plugin and operator versions
		skyhook version
		kubectl skyhook version

		# Show only the plugin version (no cluster query)
		skyhook version --client-only

		# Query operator in a specific namespace
		skyhook version -n skyhook-system`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook plugin:\t%s\n", version.Summary())

			if clientOnly {
				return nil
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			cmdCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			opVersion, err := utils.DiscoverOperatorVersion(cmdCtx, kubeClient.Kubernetes(), ctx.GlobalFlags.Namespace())
			if err != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook operator:\tunknown (%v)\n", err)
				return nil
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook operator:\t%s\n", opVersion)
			return nil
		},
	}

	versionCmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Time limit for contacting the Kubernetes API")
	versionCmd.Flags().BoolVar(&clientOnly, "client-only", false, "Only print the plugin version without querying the cluster")

	return versionCmd
}
