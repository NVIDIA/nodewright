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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// statusOptions holds the options for the status command
type statusOptions struct {
	skyhookName string
	packageName string
	nodes       []string
}

// BindToCmd binds the status options to command flags
func (o *statusOptions) BindToCmd(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.skyhookName, "skyhook", "", "Name of the Skyhook CR (required)")
	cmd.Flags().StringArrayVar(&o.nodes, "node", nil, "Node name or regex pattern (can be specified multiple times)")

	_ = cmd.MarkFlagRequired("skyhook")
}

// NewStatusCmd creates the package status command
func NewStatusCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &statusOptions{}

	cmd := &cobra.Command{
		Use:   "status <package-name>",
		Short: "Query package status across nodes",
		Long: `Query the status of a package across cluster nodes by reading node annotations.

This command displays the current state, stage, and version of a package
on each node that has Skyhook state annotations.

The output can be filtered by:
  - Skyhook name (required flag)
  - Node name patterns (optional, supports regex)`,
		Example: `  # View package status on all nodes
  kubectl skyhook package status shellscript --skyhook gpu-init

  # View package status on specific nodes
  kubectl skyhook package status shellscript --skyhook gpu-init --node worker-1 --node worker-2

  # View package status on nodes matching a regex pattern
  kubectl skyhook package status shellscript --skyhook gpu-init --node "worker-.*"

  # Output as JSON
  kubectl skyhook package status shellscript --skyhook gpu-init -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.packageName = args[0]

			if opts.skyhookName == "" {
				return fmt.Errorf("--skyhook flag is required")
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runStatus(cmd.Context(), kubeClient, opts, ctx)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

// nodePackageStatus represents the status of a package on a node
type nodePackageStatus struct {
	// Embed the API PackageStatus for consistency and to avoid drift
	v1alpha1.PackageStatus

	// NodeName is the name of the node (not in PackageStatus)
	NodeName string `json:"nodeName"`
}

// newNodePackageStatus creates a nodePackageStatus from API types
func newNodePackageStatus(nodeName string, pkgStatus v1alpha1.PackageStatus) nodePackageStatus {
	return nodePackageStatus{
		PackageStatus: pkgStatus,
		NodeName:      nodeName,
	}
}

func runStatus(ctx context.Context, kubeClient *client.Client, opts *statusOptions, cliCtx *cliContext.CLIContext) error {
	out := cliCtx.Config().OutputWriter
	// Get the Skyhook CR to validate it exists and get package info
	skyhookUnstructured, err := kubeClient.Dynamic().Resource(skyhookGVR).Get(ctx, opts.skyhookName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting skyhook %q: %w", opts.skyhookName, err)
	}

	skyhook, err := utils.UnstructuredToSkyhook(skyhookUnstructured)
	if err != nil {
		return fmt.Errorf("parsing skyhook: %w", err)
	}

	// Get all nodes
	annotationKey := nodeStateAnnotationKey(opts.skyhookName)
	nodeList, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	// Collect status from all nodes with the annotation
	var statuses []nodePackageStatus
	allNodes := make([]string, 0, len(nodeList.Items))

	for _, node := range nodeList.Items {
		annotation, ok := node.Annotations[annotationKey]
		if !ok {
			continue
		}

		var nodeState v1alpha1.NodeState
		if err := json.Unmarshal([]byte(annotation), &nodeState); err != nil {
			if cliCtx.GlobalFlags.Verbose {
				_, _ = fmt.Fprintf(cliCtx.Config().ErrorWriter, "Warning: skipping node %q - invalid annotation: %v\n", node.Name, err)
			}
			continue
		}

		allNodes = append(allNodes, node.Name)

		for _, pkgStatus := range nodeState {
			statuses = append(statuses, newNodePackageStatus(node.Name, pkgStatus))
		}
	}

	// Filter by node patterns if specified
	if len(opts.nodes) > 0 {
		matchedNodes, err := utils.MatchNodes(opts.nodes, allNodes)
		if err != nil {
			return fmt.Errorf("matching nodes: %w", err)
		}
		matchedSet := make(map[string]bool)
		for _, n := range matchedNodes {
			matchedSet[n] = true
		}

		var filtered []nodePackageStatus
		for _, s := range statuses {
			if matchedSet[s.NodeName] {
				filtered = append(filtered, s)
			}
		}
		statuses = filtered
	}

	// Filter by package name if specified
	if opts.packageName != "" {
		var filtered []nodePackageStatus
		for _, s := range statuses {
			if s.Name == opts.packageName {
				filtered = append(filtered, s)
			}
		}
		statuses = filtered
	}

	// Sort by node name, then package name
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].NodeName != statuses[j].NodeName {
			return statuses[i].NodeName < statuses[j].NodeName
		}
		return statuses[i].Name < statuses[j].Name
	})

	if len(statuses) == 0 {
		_, _ = fmt.Fprintf(out, "No package status found for skyhook %q\n", opts.skyhookName)
		return nil
	}

	// Output based on format
	switch cliCtx.GlobalFlags.OutputFormat {
	case utils.OutputFormatJSON:
		return utils.OutputJSON(out, statuses)
	case utils.OutputFormatYAML:
		return utils.OutputYAML(out, statuses)
	case utils.OutputFormatWide:
		return outputPackageStatusTableOrWide(out, skyhook, statuses, true)
	default:
		return outputPackageStatusTableOrWide(out, skyhook, statuses, false)
	}
}

// packageStatusTableConfig returns the table configuration for package status output
func packageStatusTableConfig() utils.TableConfig[nodePackageStatus] {
	return utils.TableConfig[nodePackageStatus]{
		Headers: []string{"NODE", "PACKAGE", "VERSION", "STAGE", "STATE"},
		Extract: func(s nodePackageStatus) []string {
			return []string{
				s.NodeName,
				s.Name,
				s.Version,
				string(s.Stage),
				string(s.State),
			}
		},
		WideHeaders: []string{"RESTARTS", "IMAGE"},
		WideExtract: func(s nodePackageStatus) []string {
			return []string{fmt.Sprintf("%d", s.Restarts), s.Image}
		},
	}
}

func outputPackageStatusTableOrWide(out io.Writer, skyhook *v1alpha1.Skyhook, statuses []nodePackageStatus, wide bool) error {
	headerLine := fmt.Sprintf("Skyhook: %s\nPackages: %s", skyhook.Name, formatPackageList(skyhook))
	if wide {
		return utils.OutputWideWithHeader(out, headerLine, packageStatusTableConfig(), statuses)
	}
	return utils.OutputTableWithHeader(out, headerLine, packageStatusTableConfig(), statuses)
}

func formatPackageList(skyhook *v1alpha1.Skyhook) string {
	packages := make([]string, 0, len(skyhook.Spec.Packages))
	for name, pkg := range skyhook.Spec.Packages {
		packages = append(packages, fmt.Sprintf("%s:%s", name, pkg.Version))
	}
	sort.Strings(packages)
	return strings.Join(packages, ", ")
}
