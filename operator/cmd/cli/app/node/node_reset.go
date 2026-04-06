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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// nodeResetOptions holds the options for the node reset command
type nodeResetOptions struct {
	skyhookName string
	confirm     bool
}

// BindToCmd binds the options to the command flags
func (o *nodeResetOptions) BindToCmd(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.skyhookName, "skyhook", "", "Name of the Skyhook CR (required)")
	cmd.Flags().BoolVarP(&o.confirm, "confirm", "y", false, "Skip confirmation prompt")

	_ = cmd.MarkFlagRequired("skyhook")
}

// NewResetCmd creates the node reset command
func NewResetCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &nodeResetOptions{}

	cmd := &cobra.Command{
		Use:   "reset <node-name...>",
		Short: "Reset all package state on node(s) for a Skyhook",
		Long: `Reset all package state on node(s) for a specific Skyhook, forcing a complete re-run.

This command removes all Skyhook state from the specified node(s), causing
the operator to re-execute all packages from the beginning.

Unlike 'package rerun' which resets a single package, 'node reset' clears
ALL package state for a Skyhook on the specified node(s).

Node names can be exact matches or regex patterns.`,
		Example: `  # Reset all packages on worker-1 for gpu-init Skyhook
  kubectl skyhook node reset worker-1 --skyhook gpu-init --confirm

  # Reset multiple nodes
  kubectl skyhook node reset worker-1 worker-2 worker-3 --skyhook gpu-init --confirm

  # Reset all nodes matching a pattern
  kubectl skyhook node reset "gpu-node-.*" --skyhook gpu-init --confirm

  # Preview changes without applying (dry-run)
  kubectl skyhook node reset worker-1 --skyhook gpu-init --dry-run`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.skyhookName == "" {
				return fmt.Errorf("--skyhook flag is required")
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runNodeReset(cmd.Context(), cmd, kubeClient, args, opts, ctx)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

func runNodeReset(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, nodePatterns []string, opts *nodeResetOptions, cliCtx *cliContext.CLIContext) error {
	// Get all nodes
	nodeList, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	// Collect all node names for pattern matching
	allNodeNames := make([]string, 0, len(nodeList.Items))
	nodeMap := make(map[string]int) // node name -> index in nodeList.Items
	for i, node := range nodeList.Items {
		allNodeNames = append(allNodeNames, node.Name)
		nodeMap[node.Name] = i
	}

	// Match nodes
	matchedNodes, err := utils.MatchNodes(nodePatterns, allNodeNames)
	if err != nil {
		return fmt.Errorf("matching nodes: %w", err)
	}

	if len(matchedNodes) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No nodes matched the specified patterns\n")
		return nil
	}

	// Find nodes that have the specified Skyhook annotation
	annotationKey := nodeStateAnnotationPrefix + opts.skyhookName
	nodesToReset := make([]string, 0, len(matchedNodes))
	nodeStates := make(map[string]v1alpha1.NodeState)

	for _, nodeName := range matchedNodes {
		idx := nodeMap[nodeName]
		node := &nodeList.Items[idx]

		annotation, ok := node.Annotations[annotationKey]
		if !ok {
			continue
		}

		var nodeState v1alpha1.NodeState
		if err := json.Unmarshal([]byte(annotation), &nodeState); err != nil {
			if cliCtx.GlobalFlags.Verbose {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: skipping node %q - invalid annotation: %v\n", nodeName, err)
			}
			continue
		}

		nodesToReset = append(nodesToReset, nodeName)
		nodeStates[nodeName] = nodeState
	}

	if len(nodesToReset) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No nodes have state for Skyhook %q\n", opts.skyhookName)
		return nil
	}

	// Print summary
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook: %s\n", opts.skyhookName)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Nodes to reset (%d):\n", len(nodesToReset))
	for _, nodeName := range nodesToReset {
		nodeState := nodeStates[nodeName]
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s (%d packages)\n", nodeName, len(nodeState))
	}

	// Dry run check
	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	// Confirmation
	if !opts.confirm {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nThis will remove ALL package state for Skyhook %q on these nodes.\n", opts.skyhookName)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "All packages will re-run from the beginning.\n")
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

	// Apply changes
	var updateErrors []string
	successCount := 0

	for _, nodeName := range nodesToReset {
		if err := utils.RemoveNodeAnnotation(ctx, kubeClient.Kubernetes(), nodeName, annotationKey); err != nil {
			updateErrors = append(updateErrors, fmt.Sprintf("%s: %v", nodeName, err))
			continue
		}
		successCount++
	}

	// Print results
	if len(updateErrors) > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nErrors resetting some nodes:\n")
		for _, e := range updateErrors {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", e)
		}
	}

	if successCount > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully reset %d node(s) for Skyhook %q\n", successCount, opts.skyhookName)
	}

	return nil
}
