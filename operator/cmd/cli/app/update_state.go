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
	"maps"
	"slices"
	"sort"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/preflight"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

type updateStateOptions struct {
	nodes    []string
	selector string
	confirm  bool
	add      bool
}

// NewUpdateStateCmd creates the `update-state` command.
func NewUpdateStateCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &updateStateOptions{}

	cmd := &cobra.Command{
		Use:   "update-state <nodewright-name> <package> <version> <stage> <state>",
		Short: "Update the recorded state of a single package on NodeWright-managed nodes",
		Long: `Edit the per-node nodeState annotation for one package.

By default the command applies to every node that already has state for the
named NodeWright. Use --node or --selector to narrow the affected nodes.

This is an administrator escape hatch. It does not validate that the new
(stage, state) combination is reachable by the operator from the current
state, and it does not gate destructive stages (uninstall,
uninstall-interrupt) behind extra prompts. Pause the NodeWright CR before
running this command — otherwise the operator may overwrite the edit
immediately.

Use --add to create a fresh nodeState entry for nodes that do not yet have
one for this package. --add requires --node or --selector so the scope is
explicit.`,
		Example: `  # Mark pkg1@1.0 as complete on every tracked node
  kubectl nodewright update-state gpu-init pkg1 1.0 config complete --confirm

  # Same, but only on one node
  kubectl nodewright update-state gpu-init pkg1 1.0 config complete --node n1 --confirm

  # Create a fresh entry on selected nodes
  kubectl nodewright update-state gpu-init pkg1 1.0 apply in_progress --selector role=gpu --add --confirm`,
		Args: cobra.ExactArgs(5),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			if err := preflight.EnsureNodeWrightServed(kubeClient.Kubernetes().Discovery()); err != nil {
				return err
			}

			return runUpdateState(cmd.Context(), cmd, kubeClient, args, opts, ctx)
		},
	}

	cmd.Flags().StringArrayVar(&opts.nodes, "node", nil, "Limit the update to specific node(s); repeat for multiple")
	cmd.Flags().StringVarP(&opts.selector, "selector", "l", "", "Limit the update to nodes matching a label selector")
	cmd.Flags().BoolVarP(&opts.confirm, "confirm", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&opts.add, "add", false, "Create a fresh nodeState entry on nodes that do not yet have one (requires --node or --selector)")
	cmd.MarkFlagsMutuallyExclusive("node", "selector")

	return cmd
}

type updateStateTarget struct {
	nodeName string
	nextRaw  string
}

func validateUpdateStateArgs(args []string, opts *updateStateOptions) (v1alpha1.Stage, v1alpha1.State, error) {
	stage := v1alpha1.Stage(args[3])
	if !slices.Contains(v1alpha1.Stages, stage) {
		return "", "", fmt.Errorf("invalid stage %q (valid: %v)", args[3], v1alpha1.Stages)
	}
	state := v1alpha1.State(args[4])
	if !slices.Contains(v1alpha1.States, state) {
		return "", "", fmt.Errorf("invalid state %q (valid: %v)", args[4], v1alpha1.States)
	}
	if opts.add && len(opts.nodes) == 0 && opts.selector == "" {
		return "", "", fmt.Errorf("--add requires --node or --selector so the scope is explicit")
	}
	return stage, state, nil
}

func enumerateAddNodes(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, opts *updateStateOptions) ([]string, error) {
	listOpts := metav1.ListOptions{}
	if opts.selector != "" {
		listOpts.LabelSelector = opts.selector
	}
	nodes, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	existing := map[string]struct{}{}
	for _, n := range nodes.Items {
		existing[n.Name] = struct{}{}
	}
	var ordered []string
	if len(opts.nodes) > 0 {
		for _, n := range opts.nodes {
			if _, ok := existing[n]; !ok {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "node %q not found\n", n)
				continue
			}
			ordered = append(ordered, n)
		}
		return ordered, nil
	}
	for _, n := range nodes.Items {
		ordered = append(ordered, n.Name)
	}
	return ordered, nil
}

func enumerateUpdateNodes(cmd *cobra.Command, nodeStates map[string]v1alpha1.NodeState, opts *updateStateOptions, skyhookName string) []string {
	var ordered []string
	if len(opts.nodes) > 0 {
		for _, n := range opts.nodes {
			if _, ok := nodeStates[n]; !ok {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "node %q not found or has no state for NodeWright %q\n", n, skyhookName)
				continue
			}
			ordered = append(ordered, n)
		}
		return ordered
	}
	for n := range nodeStates {
		ordered = append(ordered, n)
	}
	return ordered
}

func buildUpdateStateTarget(
	cmd *cobra.Command,
	name string,
	nodeStates map[string]v1alpha1.NodeState,
	pkg v1alpha1.Package,
	stage v1alpha1.Stage,
	state v1alpha1.State,
	addMode bool,
) (updateStateTarget, bool) {
	uniqueName := pkg.GetUniqueName()
	// why: nodeStates[name] returns a shared map reference; mutating it would
	// silently mutate the caller's snapshot. Clone before mutation so this
	// helper stays safe even if it's ever called twice for the same node.
	ns := maps.Clone(nodeStates[name])
	if ns == nil {
		ns = v1alpha1.NodeState{}
	}
	entry, hasEntry := ns[uniqueName]

	if addMode {
		if hasEntry {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "node %q: entry already exists for %q; skipping (use without --add to update)\n", name, uniqueName)
			return updateStateTarget{}, false
		}
		ns[uniqueName] = v1alpha1.PackageStatus{
			Name:    pkg.Name,
			Version: pkg.Version,
			Image:   pkg.Image,
			Stage:   stage,
			State:   state,

			InterruptPending: pkg.HasInterrupt() && (stage == v1alpha1.StageApply || stage == v1alpha1.StageUpgrade ||
				(stage == v1alpha1.StageInterrupt && state != v1alpha1.StateComplete)),
		}
	} else {
		if !hasEntry {
			for _, ps := range ns {
				if ps.Name == pkg.Name && ps.Version != pkg.Version {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "node %q: has %s at version %q; skipping (target was %q)\n", name, pkg.Name, ps.Version, pkg.Version)
					break
				}
			}
			return updateStateTarget{}, false
		}
		entry.Stage = stage
		entry.State = state
		switch stage {
		case v1alpha1.StageApply, v1alpha1.StageUpgrade:
			entry.InterruptPending = pkg.HasInterrupt() // a fresh apply owes the package's own interrupt again
		case v1alpha1.StagePostInterrupt:
			entry.InterruptPending = false
		case v1alpha1.StageInterrupt:
			// an unfinished interrupt is still owed, so the operator re-runs it rather than apply
			entry.InterruptPending = state != v1alpha1.StateComplete && pkg.HasInterrupt()
		}
		ns[uniqueName] = entry
	}

	payload, err := json.Marshal(ns)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "node %q: marshaling annotation: %v\n", name, err)
		return updateStateTarget{}, false
	}
	return updateStateTarget{nodeName: name, nextRaw: string(payload)}, true
}

func runUpdateState(ctx context.Context, cmd *cobra.Command, kubeClient *client.Client, args []string, opts *updateStateOptions, cliCtx *cliContext.CLIContext) error {
	skyhookName, packageName, packageVersion := args[0], args[1], args[2]

	stage, state, err := validateUpdateStateArgs(args, opts)
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

	pkg, ok := skyhook.Spec.Packages[packageName]
	if !ok || pkg.Version != packageVersion {
		return fmt.Errorf("package %q (version %q) not found in spec of NodeWright %q", packageName, packageVersion, skyhookName)
	}

	nodeStates, err := utils.ListNodesWithSkyhookState(ctx, kubeClient.Kubernetes(), skyhookName, opts.selector)
	if err != nil {
		// nil map signals a list-from-apiserver failure (e.g. RBAC denied,
		// unreachable apiserver); the helper returns an initialized map
		// even when only parse failures occurred, so we use map identity
		// to distinguish hard failures from per-node parse warnings.
		if nodeStates == nil {
			return fmt.Errorf("listing nodes: %w", err)
		}
		if cliCtx.GlobalFlags.Verbose {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
		}
	}

	var orderedNodes []string
	if opts.add {
		orderedNodes, err = enumerateAddNodes(ctx, cmd, kubeClient, opts)
		if err != nil {
			return err
		}
	} else {
		orderedNodes = enumerateUpdateNodes(cmd, nodeStates, opts, skyhookName)
	}
	sort.Strings(orderedNodes)

	var targets []updateStateTarget
	for _, name := range orderedNodes {
		t, ok := buildUpdateStateTarget(cmd, name, nodeStates, pkg, stage, state, opts.add)
		if !ok {
			continue
		}
		targets = append(targets, t)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "NodeWright: %s\n", skyhookName)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Package: %s@%s -> stage=%s state=%s", packageName, packageVersion, stage, state)
	if opts.add {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), " (add)")
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout())

	if len(targets) == 0 {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no matching nodes\n")
		return nil
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Nodes to update (%d):\n", len(targets))
	for _, t := range targets {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", t.nodeName)
	}

	if cliCtx.GlobalFlags.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[dry-run] No changes applied\n")
		return nil
	}

	if !opts.confirm {
		ok, err := utils.ConfirmYN(cmd, "\nContinue? [y/N]: ")
		if err != nil {
			return err
		}
		if !ok {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Aborted\n")
			return nil
		}
	}

	annotationKey := nodeStateAnnotationPrefix + skyhookName
	var firstErr error
	success := 0
	for _, t := range targets {
		if err := utils.SetNodeAnnotation(ctx, kubeClient.Kubernetes(), t.nodeName, annotationKey, t.nextRaw); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  - %s: %v\n", t.nodeName, err)
			continue
		}
		success++
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessfully updated %d node(s)\n", success)
	return firstErr
}
