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
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// resetOptions holds the options for the deployment-policy reset command
type resetOptions struct {
	confirm bool
}

// NewResetCmd creates the deployment-policy reset command.
func NewResetCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &resetOptions{}

	cmd := &cobra.Command{
		Use:   "reset <skyhook-name>",
		Short: "Reset batch state for a Skyhook",
		Long: `Reset the deployment policy batch processing state for a Skyhook.

This command resets the batch state for all compartments in the specified Skyhook,
allowing the rollout to start fresh from batch 1. This is useful when:
  - A rollout has completed and you want to start fresh
  - Batch processing is stuck and needs to be reset
  - You want to re-run a rollout with the same deployment policy

The batch state tracks:
  - Current batch number
  - Consecutive failures
  - Completed and failed node counts
  - Whether processing should stop

After reset, the next reconciliation will start from batch 1.`,
		Example: `  # Reset batch state for gpu-init Skyhook
  kubectl skyhook deployment-policy reset gpu-init --confirm

  # Preview changes without applying (dry-run)
  kubectl skyhook deployment-policy reset gpu-init --dry-run

  # Using the short alias
  kubectl skyhook dp reset gpu-init --confirm`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skyhookName := args[0]

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runDeploymentPolicyReset(cmd.Context(), cmd, kubeClient, skyhookName, opts, ctx)
		},
	}

	cmd.Flags().BoolVarP(&opts.confirm, "confirm", "y", false, "Skip confirmation prompt")

	return cmd
}

func runDeploymentPolicyReset(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, skyhookName string, opts *resetOptions, cliCtx *cliContext.CLIContext) error {
	// Get the Skyhook
	skyhook, err := utils.GetSkyhook(ctx, kubeClient.Dynamic(), skyhookName)
	if err != nil {
		return fmt.Errorf("getting skyhook %q: %w", skyhookName, err)
	}

	// Check if there's any batch state to reset
	compartmentCount := len(skyhook.Status.CompartmentStatuses)
	if compartmentCount == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook %q has no compartment statuses to reset\n", skyhookName)
		return nil
	}

	// Count compartments with batch state
	compartmentsWithBatchState := 0
	for _, cs := range skyhook.Status.CompartmentStatuses {
		if cs.BatchState != nil {
			compartmentsWithBatchState++
		}
	}

	// Print summary
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook: %s\n", skyhookName)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Compartments: %d total, %d with batch state\n", compartmentCount, compartmentsWithBatchState)

	if compartmentsWithBatchState > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nCurrent batch state:\n")
		for name, cs := range skyhook.Status.CompartmentStatuses {
			if cs.BatchState != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s: batch %d, %d completed, %d failed\n",
					name, cs.BatchState.CurrentBatch, cs.BatchState.CompletedNodes, cs.BatchState.FailedNodes)
			}
		}
	}

	// Dry run check
	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] Would reset batch state to:\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - CurrentBatch: 1\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - ConsecutiveFailures: 0\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - CompletedNodes: 0\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - FailedNodes: 0\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - ShouldStop: false\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	// Confirmation
	if !opts.confirm {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nThis will reset the batch state for Skyhook %q.\n", skyhookName)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "The next reconciliation will start from batch 1.\n")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Continue? [y/N]: ")

		reader := bufio.NewReader(cmd.InOrStdin())
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}

		response = strings.ToLower(strings.TrimSpace(response))
		if response != "y" && response != "yes" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Aborted\n")
			return nil
		}
	}

	// Reset batch state
	skyhook.ResetCompartmentBatchStates()

	// Patch the status
	if err := utils.PatchSkyhookStatus(ctx, kubeClient.Dynamic(), skyhookName, skyhook.Status); err != nil {
		return fmt.Errorf("patching skyhook status: %w", err)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully reset batch state for Skyhook %q\n", skyhookName)
	return nil
}

// ResetBatchStateForSkyhook resets the batch state for a Skyhook and patches the status.
// This is exported for use by the main reset command.
func ResetBatchStateForSkyhook(ctx context.Context, dynamicClient client.Client, skyhookName string) error {
	skyhook, err := utils.GetSkyhook(ctx, dynamicClient.Dynamic(), skyhookName)
	if err != nil {
		return fmt.Errorf("getting skyhook %q: %w", skyhookName, err)
	}

	// Reset batch state
	skyhook.ResetCompartmentBatchStates()

	// Patch the status
	if err := utils.PatchSkyhookStatus(ctx, dynamicClient.Dynamic(), skyhookName, skyhook.Status); err != nil {
		return fmt.Errorf("patching skyhook status: %w", err)
	}

	return nil
}
