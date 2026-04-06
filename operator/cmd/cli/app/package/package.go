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
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/spf13/cobra"
)

func NewPackageCmd(ctx *context.CLIContext) *cobra.Command {
	// PackageCmd represents the package command that subcommands attach to
	packageCmd := &cobra.Command{
		Use:   "package",
		Short: "Manage Skyhook packages",
		Long: `The package command group provides functionality to manage, inspect,
and troubleshoot Skyhook packages across your cluster nodes.

Packages are the core unit of work in Skyhook, representing containerized
operations that are applied to nodes matching selector criteria.

Available operations:
  - Force re-execution of packages on specific nodes
  - Query package status across the cluster
  - Retrieve package logs for debugging
  - Inspect package state and progress

Common use cases:
  - Re-running a failed package after fixing the underlying issue
  - Forcing package updates after configuration changes
  - Debugging package failures by examining logs and state
  - Monitoring package rollout progress across nodes`,
	}

	packageCmd.AddCommand(
		NewRerunCmd(ctx),
		NewStatusCmd(ctx),
		NewLogsCmd(ctx),
	)

	return packageCmd
}
