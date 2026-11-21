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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
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

// nodeStatusOptions holds the options for the node status command
type nodeStatusOptions struct {
	skyhookName string
}

// BindToCmd binds the options to the command flags
func (o *nodeStatusOptions) BindToCmd(cmd *cobra.Command) {
	utils.RegisterNodeWrightNameFlag(cmd, &o.skyhookName, "Filter by NodeWright name", false)
}

// NewStatusCmd creates the node status command
func NewStatusCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &nodeStatusOptions{}

	cmd := &cobra.Command{
		Use:   "status [node-name...]",
		Short: "Show all NodeWright activity on specific node(s)",
		Long: `Show all NodeWright activity on specific node(s) by reading node annotations.

This command displays a summary of all NodeWright CRs that have activity on the 
specified node(s), including overall status and package completion counts.

If no node name is provided, all nodes with NodeWright annotations are shown.
Node names can be exact matches or regex patterns.`,
		Example: `  # Show all NodeWright activity on a specific node
  kubectl nodewright node status worker-1

  # Show NodeWright activity on multiple nodes
  kubectl nodewright node status worker-1 worker-2 worker-3

  # Show NodeWright activity on nodes matching a pattern
  kubectl nodewright node status "worker-.*"

  # Filter by specific NodeWright
  kubectl nodewright node status worker-1 --nodewright gpu-init

  # View all nodes with NodeWright activity
  kubectl nodewright node status

  # Output as JSON
  kubectl nodewright node status worker-1 -o json

  # Output with package details
  kubectl nodewright node status worker-1 -o wide`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			if err := preflight.EnsureNodeWrightServed(kubeClient.Kubernetes().Discovery()); err != nil {
				return err
			}

			return runNodeStatus(cmd.Context(), kubeClient, args, opts, ctx)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

// nodeSkyhookSummary represents a summary of NodeWright activity on a node
type nodeSkyhookSummary struct {
	NodeName              string                 `json:"nodeName"`
	SkyhookName           string                 `json:"skyhookName"`
	Status                string                 `json:"status"`
	PackagesComplete      int                    `json:"packagesComplete"`
	PackagesTotal         int                    `json:"packagesTotal"`
	Cordoned              bool                   `json:"cordoned"`
	RuntimeRequiredCordon bool                   `json:"runtimeRequiredCordon"`
	NodeStatus            string                 `json:"nodeStatus"`
	Bootstrapped          string                 `json:"bootstrapped"`
	BootstrappedAt        string                 `json:"bootstrappedAt,omitempty"`
	Packages              []nodeSkyhookPkgStatus `json:"packages,omitempty"`
}

// signalUnknown is what the node-level signals read on a node that does not carry them.
const signalUnknown = "unknown"

// nodeSignals reads a node's Status across its NodeWrights and whether it is bootstrapped. An
// operator that writes these sets the status label on every node a NodeWright counts on, so a node
// without it reads unknown, not as not bootstrapped: its operator may predate the signals.
func nodeSignals(labels map[string]string) (status, bootstrapped string) {
	status, labeled := labels[v1alpha1.NodeStatusLabel]
	switch {
	case labels[v1alpha1.BootstrappedLabel] == "true":
		bootstrapped = "true"
	case labeled:
		bootstrapped = "false"
	default:
		bootstrapped = signalUnknown
	}
	if !labeled {
		status = signalUnknown
	}
	return status, bootstrapped
}

// hasNodeSignals reports whether a node carries either node-level label.
func hasNodeSignals(labels map[string]string) bool {
	_, status := labels[v1alpha1.NodeStatusLabel]
	_, bootstrapped := labels[v1alpha1.BootstrappedLabel]
	return status || bootstrapped
}

// noNodeWright fills the per-NodeWright columns of a row that has no NodeWright.
const noNodeWright = "-"

// nodeOnlySummary is the one row a node gets when its package state, gone or not written yet,
// gives it no other row while it still carries the node-level labels, so they stay visible. A
// listing filtered by NodeWright leaves it out.
func nodeOnlySummary(node *corev1.Node, hasRows bool, skyhookFilter string) (nodeSkyhookSummary, bool) {
	if hasRows || skyhookFilter != "" || !hasNodeSignals(node.Labels) {
		return nodeSkyhookSummary{}, false
	}
	nodeStatus, bootstrapped := nodeSignals(node.Labels)
	return nodeSkyhookSummary{
		NodeName:              node.Name,
		Cordoned:              node.Spec.Unschedulable,
		RuntimeRequiredCordon: hasRuntimeRequiredCordonAnnotation(node.Annotations),
		NodeStatus:            nodeStatus,
		Bootstrapped:          bootstrapped,
		BootstrappedAt:        node.Annotations[v1alpha1.BootstrappedAtAnnotation],
	}, true
}

// hasRuntimeRequiredCordonAnnotation reports whether the node carries the persistent
// runtime-required cordon annotation.
func hasRuntimeRequiredCordonAnnotation(annotations map[string]string) bool {
	_, ok := annotations[v1alpha1.RuntimeRequiredCordonAnnotation]
	return ok
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

	// Collect status from all nodes with NodeWright annotations
	var summaries []nodeSkyhookSummary

	for _, node := range nodeList.Items {
		if !targetNodeSet[node.Name] {
			continue
		}
		nodeStatus, bootstrapped := nodeSignals(node.Labels)
		rows := len(summaries)

		// Find all NodeWright annotations on this node
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
				NodeName:              node.Name,
				SkyhookName:           skyhookName,
				Status:                status,
				PackagesComplete:      completeCount,
				PackagesTotal:         len(packages),
				Cordoned:              node.Spec.Unschedulable,
				RuntimeRequiredCordon: hasRuntimeRequiredCordonAnnotation(node.Annotations),
				NodeStatus:            nodeStatus,
				Bootstrapped:          bootstrapped,
				BootstrappedAt:        node.Annotations[v1alpha1.BootstrappedAtAnnotation],
				Packages:              packages,
			})
		}

		if summary, ok := nodeOnlySummary(&node, len(summaries) > rows, opts.skyhookName); ok {
			summaries = append(summaries, summary)
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
		_, _ = fmt.Fprintf(out, "No NodeWright activity found on specified nodes\n")
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
		Headers: []string{"NODE", "SKYHOOK", "STATUS", "PACKAGES", "CORDONED", "RUNTIME-REQUIRED-CORDON", "NODE-STATUS", "BOOTSTRAPPED"}, //nolint:goconst
		Extract: func(s nodeSkyhookSummary) []string {
			skyhookName, status, packages := s.SkyhookName, s.Status, fmt.Sprintf("%d/%d", s.PackagesComplete, s.PackagesTotal)
			if s.SkyhookName == "" {
				skyhookName, status, packages = noNodeWright, noNodeWright, noNodeWright
			}
			return []string{
				s.NodeName,
				skyhookName,
				status,
				packages,
				fmt.Sprintf("%t", s.Cordoned),
				fmt.Sprintf("%t", s.RuntimeRequiredCordon),
				s.NodeStatus,
				s.Bootstrapped,
			}
		},
	}
}

func outputNodeStatusTable(out io.Writer, summaries []nodeSkyhookSummary) error {
	return utils.OutputTable(out, nodeStatusTableConfig(), summaries)
}

// nodeStatusWideEntry represents a flattened entry for wide output (one row per package)
type nodeStatusWideEntry struct {
	NodeName              string
	SkyhookName           string
	Cordoned              bool
	RuntimeRequiredCordon bool
	NodeStatus            string
	Bootstrapped          string
	BootstrappedAt        string
	Package               nodeSkyhookPkgStatus
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
		WideHeaders: []string{"RESTARTS", "IMAGE", "CORDONED", "RUNTIME-REQUIRED-CORDON", "NODE-STATUS", "BOOTSTRAPPED", "BOOTSTRAPPED-AT"},
		WideExtract: func(e nodeStatusWideEntry) []string {
			restarts := fmt.Sprintf("%d", e.Package.Restarts)
			if e.SkyhookName == noNodeWright {
				restarts = noNodeWright
			}
			return []string{
				restarts,
				e.Package.Image,
				fmt.Sprintf("%t", e.Cordoned),
				fmt.Sprintf("%t", e.RuntimeRequiredCordon),
				e.NodeStatus,
				e.Bootstrapped,
				e.BootstrappedAt,
			}
		},
	}

	// Flatten summaries to per-package entries
	var entries []nodeStatusWideEntry
	for _, s := range summaries {
		skyhookName, packages := s.SkyhookName, s.Packages
		if s.SkyhookName == "" {
			skyhookName = noNodeWright
			packages = []nodeSkyhookPkgStatus{{Name: noNodeWright, Version: noNodeWright, Stage: noNodeWright, State: noNodeWright, Image: noNodeWright}}
		}
		for _, pkg := range packages {
			entries = append(entries, nodeStatusWideEntry{
				NodeName:              s.NodeName,
				SkyhookName:           skyhookName,
				Cordoned:              s.Cordoned,
				RuntimeRequiredCordon: s.RuntimeRequiredCordon,
				NodeStatus:            s.NodeStatus,
				Bootstrapped:          s.Bootstrapped,
				BootstrappedAt:        s.BootstrappedAt,
				Package:               pkg,
			})
		}
	}

	return utils.OutputWide(out, cfg, entries)
}
