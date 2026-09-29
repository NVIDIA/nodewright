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
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/preflight"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

const (
	nodeStateAnnotationPrefix  = v1alpha1.METADATA_PREFIX + "/nodeState_"
	statusAnnotationPrefix     = v1alpha1.METADATA_PREFIX + "/status_"
	cordonAnnotationPrefix     = v1alpha1.METADATA_PREFIX + "/cordon_"
	drainStartAnnotationPrefix = v1alpha1.METADATA_PREFIX + "/drainStart_"
	versionAnnotationPrefix    = v1alpha1.METADATA_PREFIX + "/version_"
	autoTaintAnnotationPrefix  = v1alpha1.METADATA_PREFIX + "/autoTaint_"
	statusLabelPrefix          = v1alpha1.METADATA_PREFIX + "/status_"
)

// resetOptions holds the options for the reset command
type resetOptions struct {
	confirm        bool
	skipBatchReset bool
	pkg            string // --package <name>[:<version>]
}

func resetAnnotationKeys(skyhookName string) []string {
	return []string{
		nodeStateAnnotationPrefix + skyhookName,
		statusAnnotationPrefix + skyhookName,
		cordonAnnotationPrefix + skyhookName,
		drainStartAnnotationPrefix + skyhookName,
		versionAnnotationPrefix + skyhookName,
		autoTaintAnnotationPrefix + skyhookName,
	}
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

// NewResetCmd creates the reset command
func NewResetCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &resetOptions{}

	cmd := &cobra.Command{
		Use:   "reset <nodewright-name>",
		Short: "Reset all nodes for a NodeWright",
		Long: `Reset all package state on all nodes for a specific NodeWright, forcing a complete re-run.

This command removes all NodeWright state from all nodes that have state for the
specified NodeWright, causing the operator to re-execute all packages from the beginning.

Unlike 'node reset' which resets specific nodes, 'nodewright reset' resets ALL nodes
that have state for the specified NodeWright.

By default, this command also resets the deployment policy batch state, allowing
the rollout to start fresh from batch 1. Use --skip-batch-reset to preserve the
existing batch state.`,
		Example: `  # Reset all nodes for gpu-init NodeWright
  kubectl nodewright reset gpu-init --confirm

  # Preview changes without applying (dry-run)
  kubectl nodewright reset gpu-init --dry-run

  # Reset nodes only, preserve batch state
  kubectl nodewright reset gpu-init --skip-batch-reset --confirm`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skyhookName := args[0]

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			if err := preflight.EnsureNodeWrightServed(kubeClient.Kubernetes().Discovery()); err != nil {
				return err
			}

			return runReset(cmd.Context(), cmd, kubeClient, skyhookName, opts, ctx)
		},
	}

	cmd.Flags().BoolVarP(&opts.confirm, "confirm", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&opts.skipBatchReset, "skip-batch-reset", false, "Skip resetting deployment policy batch state")
	cmd.Flags().StringVar(&opts.pkg, "package", "", "Reset only this package (format: <name> or <name>:<version>)")

	return cmd
}

func runReset(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, skyhookName string, opts *resetOptions, cliCtx *cliContext.CLIContext) error {
	// why: branch on --package BEFORE the empty-nodes early-return so the
	// per-package path can version-gate on the operator. Otherwise a `reset
	// --package` against an unsupported old operator that happens to have
	// zero stateful nodes would silently succeed instead of erroring.
	if opts.pkg != "" {
		nodeStates, err := utils.ListNodesWithSkyhookState(ctx, kubeClient.Kubernetes(), skyhookName, "")
		if err != nil {
			if nodeStates == nil {
				return fmt.Errorf("listing nodes: %w", err)
			}
			if cliCtx.GlobalFlags.Verbose {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
			}
		}
		return runPackageReset(ctx, cmd, kubeClient, skyhookName, nodeStates, opts, cliCtx)
	}

	nodeList, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	annotationKey := nodeStateAnnotationPrefix + skyhookName
	annotationKeys := resetAnnotationKeys(skyhookName)
	labelKeys := resetLabelKeys(skyhookName)
	nodesToReset := make([]string, 0)
	nodeStates := make(map[string]v1alpha1.NodeState)

	for _, node := range nodeList.Items {
		if !hasResettableMetadata(node.Annotations, node.Labels, annotationKeys, labelKeys) {
			continue
		}

		nodeState := v1alpha1.NodeState{}
		if annotation, ok := node.Annotations[annotationKey]; ok {
			if err := json.Unmarshal([]byte(annotation), &nodeState); err != nil {
				if cliCtx.GlobalFlags.Verbose {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: node %q has invalid node state annotation; resetting metadata with empty package state: %v\n", node.Name, err)
				}
			}
		}

		nodesToReset = append(nodesToReset, node.Name)
		nodeStates[node.Name] = nodeState
	}
	sort.Strings(nodesToReset)

	if len(nodesToReset) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No nodes have state for NodeWright %q\n", skyhookName)
		return nil
	}

	// Print summary
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "NodeWright: %s\n", skyhookName)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Nodes to reset (%d):\n", len(nodesToReset))
	for _, nodeName := range nodesToReset {
		nodeState := nodeStates[nodeName]
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s (%d packages)\n", nodeName, len(nodeState))
	}

	// Show batch reset info
	if !opts.skipBatchReset {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nBatch state will also be reset (use --skip-batch-reset to preserve)\n")
	}

	// Dry run check
	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	if !opts.confirm {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nThis will remove ALL package state for NodeWright %q on %d node(s).\n", skyhookName, len(nodesToReset))
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "All packages will re-run from the beginning.\n")
		if !opts.skipBatchReset {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Batch state will be reset to start from batch 1.\n")
		}
		ok, err := utils.ConfirmYN(cmd, "Continue? [y/N]: ")
		if err != nil {
			return err
		}
		if !ok {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Aborted\n")
			return nil
		}
	}

	// Apply changes - clear all skyhook-related annotations and labels
	successCount, updateErrors := resetNodeAnnotations(ctx, cmd, kubeClient, nodesToReset, skyhookName, cliCtx)

	// Print results
	if len(updateErrors) > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nErrors resetting some nodes:\n")
		for _, e := range updateErrors {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", e)
		}
	}

	if successCount > 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully reset %d node(s) for NodeWright %q\n", successCount, skyhookName)
	}

	// Reset batch state unless --skip-batch-reset is set
	if !opts.skipBatchReset {
		resetBatchStateForReset(ctx, cmd, kubeClient, skyhookName)
	}

	return nil
}

// resetNodeAnnotations removes all skyhook-related annotations and labels from nodes
func resetNodeAnnotations(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, nodesToReset []string, skyhookName string, cliCtx *cliContext.CLIContext) (int, []string) {
	var updateErrors []string
	successCount := 0

	for _, nodeName := range nodesToReset {
		annotationsToRemove := resetAnnotationKeys(skyhookName)
		labelsToRemove := resetLabelKeys(skyhookName)

		// Try to remove the main nodeState annotation first - this is the critical one
		mainAnnotationKey := nodeStateAnnotationPrefix + skyhookName
		if err := utils.RemoveNodeAnnotation(ctx, kubeClient.Kubernetes(), nodeName, mainAnnotationKey); err != nil {
			updateErrors = append(updateErrors, fmt.Sprintf("%s: failed to remove nodeState annotation: %v", nodeName, err))
			continue
		}

		// Remove other annotations (non-critical, so we don't fail if they don't exist)
		for _, annKey := range annotationsToRemove {
			if annKey == mainAnnotationKey {
				continue // Already removed
			}
			if err := utils.RemoveNodeAnnotation(ctx, kubeClient.Kubernetes(), nodeName, annKey); err != nil {
				if cliCtx.GlobalFlags.Verbose {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to remove annotation %q from node %q: %v\n", annKey, nodeName, err)
				}
			}
		}

		// Remove labels (non-critical, so we don't fail if they don't exist)
		for _, labelKey := range labelsToRemove {
			if err := utils.RemoveNodeLabel(ctx, kubeClient.Kubernetes(), nodeName, labelKey); err != nil {
				if cliCtx.GlobalFlags.Verbose {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to remove label %q from node %q: %v\n", labelKey, nodeName, err)
				}
			}
		}

		successCount++
	}

	return successCount, updateErrors
}

// resetBatchStateForReset resets the batch state for a NodeWright if dynamic client is available
func resetBatchStateForReset(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, skyhookName string) {
	if kubeClient.Dynamic() == nil {
		return
	}

	skyhook, err := utils.GetSkyhook(ctx, kubeClient.Dynamic(), skyhookName)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to get skyhook for batch reset: %v\n", err)
		return
	}

	if len(skyhook.Status.CompartmentStatuses) == 0 {
		// No compartments — use raw patch to clear omitempty fields explicitly
		if err := utils.PatchSkyhookStatusRaw(ctx, kubeClient.Dynamic(), skyhookName,
			[]byte(`{"status":{"nodeOrderOffset":0,"nodePriority":null}}`)); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to reset node ordering: %v\n", err)
		}
		return
	}

	skyhook.ResetCompartmentBatchStates()
	if err := utils.PatchBatchStates(ctx, kubeClient.Dynamic(), skyhookName, skyhook.Status.CompartmentStatuses); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to reset batch state: %v\n", err)
		return
	}
	// Raw patch to clear the node-ordering fields, which are omitempty too
	if err := utils.PatchSkyhookStatusRaw(ctx, kubeClient.Dynamic(), skyhookName,
		[]byte(`{"status":{"nodeOrderOffset":0,"nodePriority":null}}`)); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to reset node ordering: %v\n", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Batch state reset for NodeWright %q\n", skyhookName)
}

func splitPackageRef(s string) (name, version string, hasVersion bool, err error) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		name, version = s[:i], s[i+1:]
		hasVersion = true
	} else {
		name = s
	}
	if name == "" {
		return "", "", false, fmt.Errorf("--package: name must not be empty")
	}
	if hasVersion && version == "" {
		return "", "", false, fmt.Errorf("--package: version must not be empty after %q", ":")
	}
	return name, version, hasVersion, nil
}

// why: --package reset removes a single package's state. Resetting batch
// state (which restarts the entire rollout from batch 1) would be wildly
// disproportionate to a one-package recovery, so this path deliberately
// skips it regardless of --skip-batch-reset.
func runPackageReset(
	ctx context.Context,
	cmd *cobra.Command,
	kubeClient *client.Client,
	skyhookName string,
	nodeStates map[string]v1alpha1.NodeState,
	opts *resetOptions,
	cliCtx *cliContext.CLIContext,
) error {
	name, version, hasVersion, err := splitPackageRef(opts.pkg)
	if err != nil {
		return err
	}

	skyhook, err := utils.GetSkyhook(ctx, kubeClient.Dynamic(), skyhookName)
	if err != nil {
		return fmt.Errorf("fetching NodeWright %q: %w", skyhookName, err)
	}
	if err := utils.CheckNodeStateOperatorVersion(ctx, cmd, kubeClient.Kubernetes(), cliCtx.ResolveNamespace(ctx, cmd, kubeClient.Kubernetes()), skyhook); err != nil {
		return err
	}

	annotationKey := nodeStateAnnotationPrefix + skyhookName

	type target struct {
		nodeName  string
		remaining v1alpha1.NodeState
		emptied   bool
	}
	var targets []target
	nodeNames := make([]string, 0, len(nodeStates))
	for n := range nodeStates {
		nodeNames = append(nodeNames, n)
	}
	sort.Strings(nodeNames)

	for _, nodeName := range nodeNames {
		ns := nodeStates[nodeName]
		next := v1alpha1.NodeState{}
		matched := false
		for key, status := range ns {
			if status.Name != name {
				next[key] = status
				continue
			}
			if hasVersion && status.Version != version {
				next[key] = status
				continue
			}
			matched = true
		}
		if !matched {
			continue
		}
		targets = append(targets, target{
			nodeName:  nodeName,
			remaining: next,
			emptied:   len(next) == 0,
		})
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "NodeWright: %s\n", skyhookName)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Package filter: %s\n", opts.pkg)

	if len(targets) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no matching package on any node\n")
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Nodes with a matching package (%d):\n", len(targets))
	for _, t := range targets {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", t.nodeName)
	}

	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	if !opts.confirm {
		ok, err := utils.ConfirmYN(cmd, fmt.Sprintf("\nThis will remove the %q entry from %d node(s).\nContinue? [y/N]: ", opts.pkg, len(targets)))
		if err != nil {
			return err
		}
		if !ok {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Aborted\n")
			return nil
		}
	}

	var firstErr error
	success := 0
	for _, t := range targets {
		if t.emptied {
			if err := utils.RemoveNodeAnnotation(ctx, kubeClient.Kubernetes(), t.nodeName, annotationKey); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			success++
			continue
		}
		payload, err := json.Marshal(t.remaining)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("marshaling node state for %q: %w", t.nodeName, err)
			}
			continue
		}
		if err := utils.SetNodeAnnotation(ctx, kubeClient.Kubernetes(), t.nodeName, annotationKey, string(payload)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success++
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully updated %d node(s)\n", success)
	return firstErr
}
