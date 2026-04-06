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

const nodeStateAnnotationPrefix = v1alpha1.METADATA_PREFIX + "/nodeState_"

// nodeStatusOptions holds the options for the node status command
type nodeStatusOptions struct {
	skyhookName string
}

// BindToCmd binds the options to the command flags
func (o *nodeStatusOptions) BindToCmd(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.skyhookName, "skyhook", "", "Filter by Skyhook name")
}

// NewStatusCmd creates the node status command
func NewStatusCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &nodeStatusOptions{}

	cmd := &cobra.Command{
		Use:   "status [node-name...]",
		Short: "Show all Skyhook activity on specific node(s)",
		Long: `Show all Skyhook activity on specific node(s) by reading node annotations.

This command displays a summary of all Skyhook CRs that have activity on the 
specified node(s), including overall status and package completion counts.

If no node name is provided, all nodes with Skyhook annotations are shown.
Node names can be exact matches or regex patterns.`,
		Example: `  # Show all Skyhook activity on a specific node
  kubectl skyhook node status worker-1

  # Show Skyhook activity on multiple nodes
  kubectl skyhook node status worker-1 worker-2 worker-3

  # Show Skyhook activity on nodes matching a pattern
  kubectl skyhook node status "worker-.*"

  # Filter by specific Skyhook
  kubectl skyhook node status worker-1 --skyhook gpu-init

  # View all nodes with Skyhook activity
  kubectl skyhook node status

  # Output as JSON
  kubectl skyhook node status worker-1 -o json

  # Output with package details
  kubectl skyhook node status worker-1 -o wide`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runNodeStatus(cmd.Context(), kubeClient, args, opts, ctx)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

// nodeSkyhookSummary represents a summary of Skyhook activity on a node
type nodeSkyhookSummary struct {
	NodeName         string                 `json:"nodeName"`
	SkyhookName      string                 `json:"skyhookName"`
	Status           string                 `json:"status"`
	PackagesComplete int                    `json:"packagesComplete"`
	PackagesTotal    int                    `json:"packagesTotal"`
	Packages         []nodeSkyhookPkgStatus `json:"packages,omitempty"`
}

// nodeSkyhookPkgStatus represents the status of a single package
type nodeSkyhookPkgStatus struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Stage    string `json:"stage"`
	State    string `json:"state"`
	Restarts int32  `json:"restarts"`
	Image    string `json:"image,omitempty"`
}

func runNodeStatus(ctx context.Context, kubeClient *client.Client, nodePatterns []string, opts *nodeStatusOptions, cliCtx *cliContext.CLIContext) error {
	out := cliCtx.Config().OutputWriter
	// Get all nodes
	nodeList, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	// Collect all node names for pattern matching
	allNodeNames := make([]string, 0, len(nodeList.Items))
	for _, node := range nodeList.Items {
		allNodeNames = append(allNodeNames, node.Name)
	}

	// Filter nodes by pattern if specified
	var targetNodes []string
	if len(nodePatterns) > 0 {
		targetNodes, err = utils.MatchNodes(nodePatterns, allNodeNames)
		if err != nil {
			return fmt.Errorf("matching nodes: %w", err)
		}
		if len(targetNodes) == 0 {
			_, _ = fmt.Fprintf(out, "No nodes matched the specified patterns\n")
			return nil
		}
	} else {
		targetNodes = allNodeNames
	}

	targetNodeSet := make(map[string]bool)
	for _, n := range targetNodes {
		targetNodeSet[n] = true
	}

	// Collect status from all nodes with Skyhook annotations
	var summaries []nodeSkyhookSummary

	for _, node := range nodeList.Items {
		if !targetNodeSet[node.Name] {
			continue
		}

		// Find all Skyhook annotations on this node
		for annotationKey, annotationValue := range node.Annotations {
			if !strings.HasPrefix(annotationKey, nodeStateAnnotationPrefix) {
				continue
			}

			skyhookName := strings.TrimPrefix(annotationKey, nodeStateAnnotationPrefix)

			// Filter by skyhook name if specified
			if opts.skyhookName != "" && skyhookName != opts.skyhookName {
				continue
			}

			var nodeState v1alpha1.NodeState
			if err := json.Unmarshal([]byte(annotationValue), &nodeState); err != nil {
				if cliCtx.GlobalFlags.Verbose {
					_, _ = fmt.Fprintf(cliCtx.Config().ErrorWriter, "Warning: skipping node %q skyhook %q - invalid annotation: %v\n", node.Name, skyhookName, err)
				}
				continue // Skip invalid annotations
			}

			packages := make([]nodeSkyhookPkgStatus, 0, len(nodeState))
			completeCount := 0
			hasError := false
			hasInProgress := false

			for _, pkgStatus := range nodeState {
				packages = append(packages, nodeSkyhookPkgStatus{
					Name:     pkgStatus.Name,
					Version:  pkgStatus.Version,
					Stage:    string(pkgStatus.Stage),
					State:    string(pkgStatus.State),
					Restarts: pkgStatus.Restarts,
					Image:    pkgStatus.Image,
				})

				switch pkgStatus.State {
				case v1alpha1.StateComplete:
					completeCount++
				case v1alpha1.StateErroring:
					hasError = true
				case v1alpha1.StateInProgress:
					hasInProgress = true
				}
			}

			// Determine overall status
			status := string(v1alpha1.StateUnknown)
			if hasError {
				status = string(v1alpha1.StateErroring)
			} else if completeCount == len(packages) && len(packages) > 0 {
				status = string(v1alpha1.StateComplete)
			} else if hasInProgress || completeCount > 0 {
				status = string(v1alpha1.StateInProgress)
			}

			// Sort packages by name
			sort.Slice(packages, func(i, j int) bool {
				return packages[i].Name < packages[j].Name
			})

			summaries = append(summaries, nodeSkyhookSummary{
				NodeName:         node.Name,
				SkyhookName:      skyhookName,
				Status:           status,
				PackagesComplete: completeCount,
				PackagesTotal:    len(packages),
				Packages:         packages,
			})
		}
	}

	// Sort by node name, then skyhook name
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].NodeName != summaries[j].NodeName {
			return summaries[i].NodeName < summaries[j].NodeName
		}
		return summaries[i].SkyhookName < summaries[j].SkyhookName
	})

	if len(summaries) == 0 {
		_, _ = fmt.Fprintf(out, "No Skyhook activity found on specified nodes\n")
		return nil
	}

	// Output based on format
	switch cliCtx.GlobalFlags.OutputFormat {
	case utils.OutputFormatJSON:
		return utils.OutputJSON(out, summaries)
	case utils.OutputFormatYAML:
		return utils.OutputYAML(out, summaries)
	case utils.OutputFormatWide:
		return outputNodeStatusWide(out, summaries)
	default:
		return outputNodeStatusTable(out, summaries)
	}
}

// nodeStatusTableConfig returns the table configuration for node status output
func nodeStatusTableConfig() utils.TableConfig[nodeSkyhookSummary] {
	return utils.TableConfig[nodeSkyhookSummary]{
		Headers: []string{"NODE", "SKYHOOK", "STATUS", "PACKAGES"},
		Extract: func(s nodeSkyhookSummary) []string {
			return []string{
				s.NodeName,
				s.SkyhookName,
				s.Status,
				fmt.Sprintf("%d/%d", s.PackagesComplete, s.PackagesTotal),
			}
		},
	}
}

func outputNodeStatusTable(out io.Writer, summaries []nodeSkyhookSummary) error {
	return utils.OutputTable(out, nodeStatusTableConfig(), summaries)
}

// nodeStatusWideEntry represents a flattened entry for wide output (one row per package)
type nodeStatusWideEntry struct {
	NodeName    string
	SkyhookName string
	Package     nodeSkyhookPkgStatus
}

func outputNodeStatusWide(out io.Writer, summaries []nodeSkyhookSummary) error {
	// Wide output shows one row per package, not per summary
	cfg := utils.TableConfig[nodeStatusWideEntry]{
		Headers: []string{"NODE", "SKYHOOK", "PACKAGE", "VERSION", "STAGE", "STATE"},
		Extract: func(e nodeStatusWideEntry) []string {
			return []string{
				e.NodeName,
				e.SkyhookName,
				e.Package.Name,
				e.Package.Version,
				e.Package.Stage,
				e.Package.State,
			}
		},
		WideHeaders: []string{"RESTARTS", "IMAGE"},
		WideExtract: func(e nodeStatusWideEntry) []string {
			return []string{fmt.Sprintf("%d", e.Package.Restarts), e.Package.Image}
		},
	}

	// Flatten summaries to per-package entries
	var entries []nodeStatusWideEntry
	for _, s := range summaries {
		for _, pkg := range s.Packages {
			entries = append(entries, nodeStatusWideEntry{
				NodeName:    s.NodeName,
				SkyhookName: s.SkyhookName,
				Package:     pkg,
			})
		}
	}

	return utils.OutputWide(out, cfg, entries)
}
