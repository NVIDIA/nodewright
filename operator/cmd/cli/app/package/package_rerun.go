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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

var skyhookGVR = schema.GroupVersionResource{
	Group:    v1alpha1.GroupVersion.Group,
	Version:  v1alpha1.GroupVersion.Version,
	Resource: "skyhooks",
}

// nodeStateAnnotationKey returns the annotation key for node state
func nodeStateAnnotationKey(skyhookName string) string {
	return fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, skyhookName)
}

// rerunOptions holds the options for the rerun command
type rerunOptions struct {
	skyhookName string
	nodes       []string
	stage       string
	confirm     bool
}

// BindToCmd binds the rerun options to command flags
func (o *rerunOptions) BindToCmd(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.skyhookName, "skyhook", "", "Name of the Skyhook CR (required)")
	cmd.Flags().StringArrayVar(&o.nodes, "node", nil, "Node name or regex pattern (can be specified multiple times, required)")
	cmd.Flags().StringVar(&o.stage, "stage", "", "Re-run from specific stage (apply, config, interrupt, post-interrupt)")
	cmd.Flags().BoolVarP(&o.confirm, "confirm", "y", false, "Skip confirmation prompt")

	_ = cmd.MarkFlagRequired("skyhook")
	_ = cmd.MarkFlagRequired("node")
}

// NewRerunCmd creates the package rerun command
func NewRerunCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &rerunOptions{}

	cmd := &cobra.Command{
		Use:   "rerun <package-name>",
		Short: "Force a package to re-run on specific node(s)",
		Long: `Force a package to re-run on specific node(s) by removing its state
from the Skyhook status, causing the operator to re-execute the package.

Nodes can be specified using exact names or regex patterns. Multiple
--node flags can be combined to target different nodes.`,
		Example: `  # Re-run the shellscript package on worker-1 (from beginning)
  kubectl skyhook package rerun shellscript --skyhook gpu-init --node worker-1

  # Re-run only the config stage
  kubectl skyhook package rerun shellscript --skyhook gpu-init --node worker-1 --stage config

  # Re-run on all nodes matching a regex pattern
  kubectl skyhook package rerun shellscript --skyhook gpu-init --node "worker-.*"

  # Skip confirmation prompt
  kubectl skyhook package rerun shellscript --skyhook gpu-init --node worker-1 --confirm

  # Preview changes without applying
  kubectl skyhook package rerun shellscript --skyhook gpu-init --node worker-1 --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.skyhookName = cmd.Flag("skyhook").Value.String()
			packageName := args[0]

			// Validate required flags
			if opts.skyhookName == "" {
				return fmt.Errorf("--skyhook flag is required")
			}
			if len(opts.nodes) == 0 {
				return fmt.Errorf("at least one --node flag is required")
			}

			// Validate stage if provided
			if opts.stage != "" {
				validStages := map[string]bool{
					"apply":          true,
					"config":         true,
					"interrupt":      true,
					"post-interrupt": true,
				}
				if !validStages[opts.stage] {
					return fmt.Errorf("invalid stage %q: must be one of apply, config, interrupt, post-interrupt", opts.stage)
				}
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return rerunPackage(cmd.Context(), cmd, kubeClient, packageName, opts, ctx)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

func rerunPackage(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, packageName string, opts *rerunOptions, cliCtx *cliContext.CLIContext) error {
	// Get the Skyhook CR to find the package spec
	skyhookUnstructured, err := kubeClient.Dynamic().Resource(skyhookGVR).Get(ctx, opts.skyhookName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting skyhook %q: %w", opts.skyhookName, err)
	}

	// Convert to Skyhook type to access spec
	skyhook, err := utils.UnstructuredToSkyhook(skyhookUnstructured)
	if err != nil {
		return fmt.Errorf("parsing skyhook: %w", err)
	}

	// Find the package in the spec
	pkg, found := skyhook.Spec.Packages[packageName]
	if !found {
		return fmt.Errorf("package %q not found in skyhook %q", packageName, opts.skyhookName)
	}

	// Get the unique key for the package (name|version)
	packageKey := pkg.GetUniqueName()

	// Collect node states
	annotationKey := nodeStateAnnotationKey(opts.skyhookName)
	nodeStates, allNodes, err := collectNodeStates(ctx, kubeClient, annotationKey, cliCtx)
	if err != nil {
		return err
	}

	// Match nodes based on patterns
	matchedNodes, err := utils.MatchNodes(opts.nodes, allNodes)
	if err != nil {
		return fmt.Errorf("matching nodes: %w", err)
	}

	if len(matchedNodes) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No nodes matched the specified patterns\n")
		return nil
	}

	// Filter to only nodes that have this package in their state
	nodesToUpdate := filterNodesWithPackage(matchedNodes, nodeStates, packageKey)

	if len(nodesToUpdate) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Package %q (version %s) has no state on matched nodes\n", packageName, pkg.Version)
		return nil
	}

	// Display what will be changed
	printRerunSummary(cmd, packageName, pkg.Version, opts, nodesToUpdate, nodeStates, packageKey)

	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	// Confirmation prompt
	confirmed, err := promptConfirmation(cmd, opts)
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Aborted\n")
		return nil
	}

	// Update each node's annotation to trigger re-run
	successCount, updateErrors := updateNodeAnnotations(ctx, kubeClient, nodesToUpdate, nodeStates, packageKey, annotationKey, opts)

	printRerunResults(cmd, packageName, successCount, updateErrors)

	return nil
}

// collectNodeStates gathers node states from annotations
func collectNodeStates(ctx context.Context, kubeClient *client.Client, annotationKey string, cliCtx *cliContext.CLIContext) (map[string]v1alpha1.NodeState, []string, error) {
	nodeList, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("listing nodes: %w", err)
	}

	allNodes := make([]string, 0, len(nodeList.Items))
	nodeStates := make(map[string]v1alpha1.NodeState)
	for _, node := range nodeList.Items {
		if annotation, ok := node.Annotations[annotationKey]; ok {
			var nodeState v1alpha1.NodeState
			if err := json.Unmarshal([]byte(annotation), &nodeState); err != nil {
				if cliCtx.GlobalFlags.Verbose {
					_, _ = fmt.Fprintf(cliCtx.Config().ErrorWriter, "Warning: skipping node %q - invalid annotation: %v\n", node.Name, err)
				}
				continue // skip nodes with invalid annotation
			}
			allNodes = append(allNodes, node.Name)
			nodeStates[node.Name] = nodeState
		}
	}
	return nodeStates, allNodes, nil
}

// filterNodesWithPackage returns nodes that have the specified package in their state
func filterNodesWithPackage(matchedNodes []string, nodeStates map[string]v1alpha1.NodeState, packageKey string) []string {
	nodesToUpdate := make([]string, 0, len(matchedNodes))
	for _, nodeName := range matchedNodes {
		if nodeState, ok := nodeStates[nodeName]; ok {
			if _, hasPackage := nodeState[packageKey]; hasPackage {
				nodesToUpdate = append(nodesToUpdate, nodeName)
			}
		}
	}
	return nodesToUpdate
}

// printRerunSummary displays what will be changed
func printRerunSummary(cmd *cobra.Command, packageName, version string, opts *rerunOptions, nodesToUpdate []string, nodeStates map[string]v1alpha1.NodeState, packageKey string) {
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Package: %s (version %s)\n", packageName, version)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook: %s\n", opts.skyhookName)
	if opts.stage != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Re-run from stage: %s\n", opts.stage)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Nodes to reset (%d):\n", len(nodesToUpdate))
	for _, nodeName := range nodesToUpdate {
		nodeState := nodeStates[nodeName]
		pkgStatus := nodeState[packageKey]
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s (current state: %s, stage: %s)\n", nodeName, pkgStatus.State, pkgStatus.Stage)
	}
}

// promptConfirmation asks user to confirm the operation
func promptConfirmation(cmd *cobra.Command, opts *rerunOptions) (bool, error) {
	if opts.confirm {
		return true, nil
	}

	if opts.stage != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nThis will reset package state to re-run from the '%s' stage.\n", opts.stage)
	} else {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nThis will remove package state from node annotations and cause the package to re-run from the beginning.\n")
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Continue? [y/N]: ")

	reader := bufio.NewReader(cmd.InOrStdin())
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}

	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes", nil
}

// updateNodeAnnotations updates each node's annotation to trigger re-run
func updateNodeAnnotations(
	ctx context.Context,
	kubeClient *client.Client,
	nodesToUpdate []string,
	nodeStates map[string]v1alpha1.NodeState,
	packageKey, annotationKey string,
	opts *rerunOptions,
) (int, []string) {
	var updateErrors []string
	successCount := 0

	for _, nodeName := range nodesToUpdate {
		nodeState := nodeStates[nodeName]

		if opts.stage != "" {
			pkgStatus := nodeState[packageKey]
			pkgStatus.Stage = v1alpha1.Stage(opts.stage)
			pkgStatus.State = v1alpha1.StateInProgress
			nodeState[packageKey] = pkgStatus
		} else {
			delete(nodeState, packageKey)
		}

		if err := applyNodeStateUpdate(ctx, kubeClient, nodeName, nodeState, annotationKey); err != nil {
			updateErrors = append(updateErrors, fmt.Sprintf("%s: %v", nodeName, err))
			continue
		}
		successCount++
	}

	return successCount, updateErrors
}

// applyNodeStateUpdate applies the updated state to a single node using patch
func applyNodeStateUpdate(ctx context.Context, kubeClient *client.Client, nodeName string, nodeState v1alpha1.NodeState, annotationKey string) error {
	if len(nodeState) == 0 {
		return utils.RemoveNodeAnnotation(ctx, kubeClient.Kubernetes(), nodeName, annotationKey)
	}

	newAnnotation, err := json.Marshal(nodeState)
	if err != nil {
		return err
	}
	return utils.SetNodeAnnotation(ctx, kubeClient.Kubernetes(), nodeName, annotationKey, string(newAnnotation))
}

// printRerunResults displays the operation results
func printRerunResults(cmd *cobra.Command, packageName string, successCount int, updateErrors []string) {
	if len(updateErrors) > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nErrors updating some nodes:\n")
		for _, e := range updateErrors {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", e)
		}
	}

	if successCount > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully reset package %q on %d node(s)\n", packageName, successCount)
	}
}
