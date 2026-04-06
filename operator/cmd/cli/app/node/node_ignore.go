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
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

const labelValueTrue = "true"

// NewIgnoreCmd creates the node ignore command
func NewIgnoreCmd(ctx *cliContext.CLIContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ignore <node-name...>",
		Short: "Ignore node(s) from Skyhook processing",
		Long: `Ignore node(s) from all Skyhook processing by setting the ignore label.

When a node is ignored, Skyhook will skip it during package execution.
This is useful for maintenance windows or debugging.

Node names can be exact matches or regex patterns.`,
		Example: `  # Ignore a single node
  kubectl skyhook node ignore worker-1

  # Ignore multiple nodes
  kubectl skyhook node ignore worker-1 worker-2 worker-3

  # Ignore all nodes matching a pattern
  kubectl skyhook node ignore "worker-.*"

  # Ignore GPU nodes for maintenance
  kubectl skyhook node ignore "gpu-node-[0-9]+"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runIgnore(cmd.Context(), cmd, kubeClient, args, ctx, true)
		},
	}

	return cmd
}

// NewUnignoreCmd creates the node unignore command
func NewUnignoreCmd(ctx *cliContext.CLIContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unignore <node-name...>",
		Short: "Remove ignore label from node(s)",
		Long: `Remove the ignore label from node(s), re-enabling Skyhook processing.

After unignoring, Skyhook will resume package execution on these nodes.

Node names can be exact matches or regex patterns.`,
		Example: `  # Unignore a single node
  kubectl skyhook node unignore worker-1

  # Unignore multiple nodes
  kubectl skyhook node unignore worker-1 worker-2 worker-3

  # Unignore all nodes matching a pattern
  kubectl skyhook node unignore "gpu-node-[0-9]+"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runIgnore(cmd.Context(), cmd, kubeClient, args, ctx, false)
		},
	}

	return cmd
}

func runIgnore(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, nodePatterns []string, cliCtx *cliContext.CLIContext, ignore bool) error {
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

	action := "Ignoring"
	if !ignore {
		action = "Unignoring"
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %d node(s):\n", action, len(matchedNodes))
	for _, nodeName := range matchedNodes {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", nodeName)
	}

	// Dry run check
	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	// Apply changes
	var updateErrors []string
	successCount := 0

	for _, nodeName := range matchedNodes {
		idx := nodeMap[nodeName]
		node := &nodeList.Items[idx]

		var err error
		if ignore {
			// Check if already ignored
			if val, ok := node.Labels[utils.NodeIgnoreLabel]; ok && val == labelValueTrue {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s: already ignored\n", nodeName)
				continue
			}
			err = utils.SetNodeLabel(ctx, kubeClient.Kubernetes(), nodeName, utils.NodeIgnoreLabel, labelValueTrue)
		} else {
			// Check if not ignored
			if val, ok := node.Labels[utils.NodeIgnoreLabel]; !ok || val != labelValueTrue {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s: not ignored\n", nodeName)
				continue
			}
			err = utils.RemoveNodeLabel(ctx, kubeClient.Kubernetes(), nodeName, utils.NodeIgnoreLabel)
		}

		if err != nil {
			updateErrors = append(updateErrors, fmt.Sprintf("%s: %v", nodeName, err))
			continue
		}
		successCount++
	}

	// Print results
	if len(updateErrors) > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nErrors updating some nodes:\n")
		for _, e := range updateErrors {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", e)
		}
	}

	if successCount > 0 {
		verb := "ignored"
		if !ignore {
			verb = "unignored"
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully %s %d node(s)\n", verb, successCount)
	}

	return nil
}
