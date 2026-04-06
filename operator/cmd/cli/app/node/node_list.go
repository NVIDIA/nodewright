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

// nodeListOptions holds the options for the node list command
type nodeListOptions struct {
	skyhookName string
}

// BindToCmd binds the options to the command flags
func (o *nodeListOptions) BindToCmd(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.skyhookName, "skyhook", "", "Name of the Skyhook CR (required)")

	_ = cmd.MarkFlagRequired("skyhook")
}

// NewListCmd creates the node list command
func NewListCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &nodeListOptions{}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all nodes targeted by a Skyhook",
		Long: `List all nodes that have activity for a specific Skyhook.

This command shows all nodes that have Skyhook state annotations for the
specified Skyhook CR, along with a summary of package completion status.`,
		Example: `  # List all nodes targeted by gpu-init Skyhook
  kubectl skyhook node list --skyhook gpu-init

  # Output as JSON
  kubectl skyhook node list --skyhook gpu-init -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.skyhookName == "" {
				return fmt.Errorf("--skyhook flag is required")
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			return runNodeList(cmd.Context(), kubeClient, opts, ctx)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

// nodeListEntry represents a node in the list output
type nodeListEntry struct {
	NodeName         string `json:"nodeName"`
	Status           string `json:"status"`
	PackagesComplete int    `json:"packagesComplete"`
	PackagesTotal    int    `json:"packagesTotal"`
	Restarts         int32  `json:"restarts"`
}

func runNodeList(ctx context.Context, kubeClient *client.Client, opts *nodeListOptions, cliCtx *cliContext.CLIContext) error {
	out := cliCtx.Config().OutputWriter
	// Get all nodes
	nodeList, err := kubeClient.Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	annotationKey := nodeStateAnnotationPrefix + opts.skyhookName
	entries := make([]nodeListEntry, 0, len(nodeList.Items))

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

		completeCount := 0
		hasError := false
		hasInProgress := false
		var totalRestarts int32

		for _, pkgStatus := range nodeState {
			totalRestarts += pkgStatus.Restarts
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
		} else if completeCount == len(nodeState) && len(nodeState) > 0 {
			status = string(v1alpha1.StateComplete)
		} else if hasInProgress || completeCount > 0 {
			status = string(v1alpha1.StateInProgress)
		}

		entries = append(entries, nodeListEntry{
			NodeName:         node.Name,
			Status:           status,
			PackagesComplete: completeCount,
			PackagesTotal:    len(nodeState),
			Restarts:         totalRestarts,
		})
	}

	// Sort by node name
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].NodeName < entries[j].NodeName
	})

	if len(entries) == 0 {
		_, _ = fmt.Fprintf(out, "No nodes found for Skyhook %q\n", opts.skyhookName)
		return nil
	}

	// Output based on format
	output := nodeListOutput{SkyhookName: opts.skyhookName, Nodes: entries}
	switch cliCtx.GlobalFlags.OutputFormat {
	case utils.OutputFormatJSON:
		return utils.OutputJSON(out, output)
	case utils.OutputFormatYAML:
		return utils.OutputYAML(out, output)
	case utils.OutputFormatWide:
		return outputNodeListTableOrWide(out, opts.skyhookName, entries, true)
	default:
		return outputNodeListTableOrWide(out, opts.skyhookName, entries, false)
	}
}

// nodeListOutput is the structured output for JSON/YAML
type nodeListOutput struct {
	SkyhookName string          `json:"skyhookName" yaml:"skyhookName"`
	Nodes       []nodeListEntry `json:"nodes" yaml:"nodes"`
}

// nodeListTableConfig returns the table configuration for node list output
func nodeListTableConfig() utils.TableConfig[nodeListEntry] {
	return utils.TableConfig[nodeListEntry]{
		Headers: []string{"NODE", "STATUS", "PACKAGES"},
		Extract: func(e nodeListEntry) []string {
			status := e.Status
			if e.Status == string(v1alpha1.StateErroring) {
				status = strings.ToUpper(status)
			}
			return []string{e.NodeName, status, fmt.Sprintf("%d/%d", e.PackagesComplete, e.PackagesTotal)}
		},
		WideHeaders: []string{"RESTARTS"},
		WideExtract: func(e nodeListEntry) []string {
			return []string{fmt.Sprintf("%d", e.Restarts)}
		},
	}
}

func formatNodeListSummary(entries []nodeListEntry) string {
	totalNodes := len(entries)
	completeNodes := 0
	errorNodes := 0
	for _, e := range entries {
		switch e.Status {
		case string(v1alpha1.StateComplete):
			completeNodes++
		case string(v1alpha1.StateErroring):
			errorNodes++
		}
	}
	return fmt.Sprintf("Summary: %d nodes (%d complete, %d erroring, %d in progress)",
		totalNodes, completeNodes, errorNodes, totalNodes-completeNodes-errorNodes)
}

func outputNodeListTableOrWide(out io.Writer, skyhookName string, entries []nodeListEntry, wide bool) error {
	headerLine := fmt.Sprintf("Skyhook: %s\n\n%s", skyhookName, formatNodeListSummary(entries))
	if wide {
		return utils.OutputWideWithHeader(out, headerLine, nodeListTableConfig(), entries)
	}
	return utils.OutputTableWithHeader(out, headerLine, nodeListTableConfig(), entries)
}
