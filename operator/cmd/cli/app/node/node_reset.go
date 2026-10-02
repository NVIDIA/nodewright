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

package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/preflight"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// nodeResetOptions holds the options for the node reset command
type nodeResetOptions struct {
	skyhookName string
	confirm     bool
}

func resetAnnotationKeys(skyhookName string, keepCordon bool) []string {
	keys := []string{
		nodeStateAnnotationPrefix + skyhookName,
		statusAnnotationPrefix + skyhookName,
		drainStartAnnotationPrefix + skyhookName,
		versionAnnotationPrefix + skyhookName,
		autoTaintAnnotationPrefix + skyhookName,
	}
	if !keepCordon {
		keys = append(keys, cordonAnnotationPrefix+skyhookName)
	}
	return keys
}

func resetLabelKeys(skyhookName string) []string {
	return []string{
		statusLabelPrefix + skyhookName,
	}
}

func hasResettableMetadata(annotations, labels map[string]string, annotationKeys, labelKeys []string) bool {
	for _, annotationKey := range annotationKeys {
		if _, ok := annotations[annotationKey]; ok {
			return true
		}
	}
	for _, labelKey := range labelKeys {
		if _, ok := labels[labelKey]; ok {
			return true
		}
	}
	return false
}

// BindToCmd binds the options to the command flags
func (o *nodeResetOptions) BindToCmd(cmd *cobra.Command) {
	utils.RegisterNodeWrightNameFlag(cmd, &o.skyhookName, "Name of the NodeWright CR (required)", true)
	cmd.Flags().BoolVarP(&o.confirm, "confirm", "y", false, "Skip confirmation prompt")
}

// NewResetCmd creates the node reset command
func NewResetCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &nodeResetOptions{}

	cmd := &cobra.Command{
		Use:   "reset <node-name...>",
		Short: "Reset all package state on node(s) for a NodeWright",
		Long: `Reset all package state on node(s) for a specific NodeWright, forcing a complete re-run.

This command removes all NodeWright state from the specified node(s), causing
the operator to re-execute all packages from the beginning.

The node is never uncordoned. While the NodeWright exists its cordon annotation is kept
so the re-run can release the cordon; once the NodeWright is gone the annotation is
removed and the node must be uncordoned by hand.

Unlike 'package rerun' which resets a single package, 'node reset' clears
ALL package state for a NodeWright on the specified node(s).

Node names can be exact matches or regex patterns.`,
		Example: `  # Reset all packages on worker-1 for gpu-init NodeWright
  kubectl nodewright node reset worker-1 --nodewright gpu-init --confirm

  # Reset multiple nodes
  kubectl nodewright node reset worker-1 worker-2 worker-3 --nodewright gpu-init --confirm

  # Reset all nodes matching a pattern
  kubectl nodewright node reset "gpu-node-.*" --nodewright gpu-init --confirm

  # Preview changes without applying (dry-run)
  kubectl nodewright node reset worker-1 --nodewright gpu-init --dry-run`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.skyhookName == "" {
				return fmt.Errorf("--nodewright flag is required")
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			if err := preflight.EnsureNodeWrightServed(kubeClient.Kubernetes().Discovery()); err != nil {
				return err
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

	// Find nodes that have the specified NodeWright annotation
	annotationKey := nodeStateAnnotationPrefix + opts.skyhookName
	annotationKeys := resetAnnotationKeys(opts.skyhookName, utils.KeepCordonOnReset(ctx, cmd, kubeClient.Dynamic(), opts.skyhookName))
	labelKeys := resetLabelKeys(opts.skyhookName)
	nodesToReset := make([]string, 0, len(matchedNodes))
	nodeStates := make(map[string]v1alpha1.NodeState)

	for _, nodeName := range matchedNodes {
		idx := nodeMap[nodeName]
		node := &nodeList.Items[idx]

		if !hasResettableMetadata(node.Annotations, node.Labels, annotationKeys, labelKeys) {
			continue
		}

		nodeState := v1alpha1.NodeState{}
		if annotation, ok := node.Annotations[annotationKey]; ok {
			if err := json.Unmarshal([]byte(annotation), &nodeState); err != nil {
				if cliCtx.GlobalFlags.Verbose {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: node %q has invalid node state annotation; resetting metadata with empty package state: %v\n", nodeName, err)
				}
			}
		}

		nodesToReset = append(nodesToReset, nodeName)
		nodeStates[nodeName] = nodeState
	}

	if len(nodesToReset) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No nodes have state for NodeWright %q\n", opts.skyhookName)
		return nil
	}

	// Print summary
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "NodeWright: %s\n", opts.skyhookName)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nThis will remove ALL package state for NodeWright %q on these nodes.\n", opts.skyhookName)
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
		nodeHasError := false
		for _, annotationKey := range annotationKeys {
			if err := utils.RemoveNodeAnnotation(ctx, kubeClient.Kubernetes(), nodeName, annotationKey); err != nil {
				updateErrors = append(updateErrors, fmt.Sprintf("%s: %v", nodeName, err))
				nodeHasError = true
				break
			}
		}
		if nodeHasError {
			continue
		}
		for _, labelKey := range labelKeys {
			if err := utils.RemoveNodeLabel(ctx, kubeClient.Kubernetes(), nodeName, labelKey); err != nil && cliCtx.GlobalFlags.Verbose {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to remove label %q from node %q: %v\n", labelKey, nodeName, err)
			}
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully reset %d node(s) for NodeWright %q\n", successCount, opts.skyhookName)
	}

	return nil
}
