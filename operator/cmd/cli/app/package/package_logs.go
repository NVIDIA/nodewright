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
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// logsOptions holds the options for the logs command
type logsOptions struct {
	skyhookName string
	packageName string
	node        string
	stage       string
	follow      bool
	tail        int64
}

// BindToCmd binds the logs options to command flags
func (o *logsOptions) BindToCmd(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.skyhookName, "skyhook", "", "Name of the Skyhook CR (required)")
	cmd.Flags().StringVar(&o.node, "node", "", "Filter by node name")
	cmd.Flags().StringVar(&o.stage, "stage", "", "Filter by stage (apply, config, interrupt, post-interrupt)")
	cmd.Flags().BoolVarP(&o.follow, "follow", "f", false, "Follow log output")
	cmd.Flags().Int64Var(&o.tail, "tail", -1, "Number of lines to show from the end of logs (-1 for all)")

	_ = cmd.MarkFlagRequired("skyhook")
}

// NewLogsCmd creates the package logs command
func NewLogsCmd(ctx *cliContext.CLIContext) *cobra.Command {
	opts := &logsOptions{}

	cmd := &cobra.Command{
		Use:   "logs <package-name>",
		Short: "Retrieve logs from Skyhook package pods",
		Long: `Retrieve logs from pods running a Skyhook package.

This command finds pods by their Skyhook labels and retrieves their logs.
By default, it shows logs from the most relevant stage container.`,
		Example: `  # Get logs for a package
  kubectl skyhook package logs shellscript --skyhook gpu-init

  # Get logs for a package on a specific node
  kubectl skyhook package logs shellscript --skyhook gpu-init --node worker-1

  # Get logs from a specific stage
  kubectl skyhook package logs shellscript --skyhook gpu-init --stage apply

  # Follow logs in real-time
  kubectl skyhook package logs shellscript --skyhook gpu-init -f

  # Show last 100 lines
  kubectl skyhook package logs shellscript --skyhook gpu-init --tail 100`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.packageName = args[0]

			if opts.skyhookName == "" {
				return fmt.Errorf("--skyhook flag is required")
			}

			// Validate stage if provided
			if opts.stage != "" {
				validStages := map[string]bool{"apply": true, "config": true, "interrupt": true, "post-interrupt": true}
				if !validStages[opts.stage] {
					return fmt.Errorf("invalid stage %q: must be one of apply, config, interrupt, post-interrupt", opts.stage)
				}
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			namespace := ctx.GlobalFlags.Namespace()
			return runLogs(cmd.Context(), cmd.OutOrStdout(), kubeClient, opts, namespace)
		},
	}

	opts.BindToCmd(cmd)

	return cmd
}

func runLogs(ctx context.Context, out io.Writer, kubeClient *client.Client, opts *logsOptions, namespace string) error {
	// Build label selector for Skyhook pods
	labelSelector := fmt.Sprintf("%s/name=%s", v1alpha1.METADATA_PREFIX, opts.skyhookName)

	// List pods matching the selector
	podList, err := kubeClient.Kubernetes().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}

	if len(podList.Items) == 0 {
		_, _ = fmt.Fprintf(out, "No pods found for skyhook %q in namespace %q\n", opts.skyhookName, namespace)
		return nil
	}

	// Filter pods
	matchedPods := make([]corev1.Pod, 0, len(podList.Items))
	for _, pod := range podList.Items {
		// Filter by package if specified
		if opts.packageName != "" {
			packageLabel := pod.Labels[fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)]
			// Package label format is "package-name-version"
			if !strings.HasPrefix(packageLabel, opts.packageName+"-") && packageLabel != opts.packageName {
				continue
			}
		}

		// Filter by node if specified
		if opts.node != "" {
			matchedNodes, err := utils.MatchNodes([]string{opts.node}, []string{pod.Spec.NodeName})
			if err != nil {
				return fmt.Errorf("matching node: %w", err)
			}
			if len(matchedNodes) == 0 {
				continue
			}
		}

		matchedPods = append(matchedPods, pod)
	}

	if len(matchedPods) == 0 {
		_, _ = fmt.Fprintf(out, "No pods matched the specified filters\n")
		return nil
	}

	// Sort pods by creation time (newest first)
	sort.Slice(matchedPods, func(i, j int) bool {
		return matchedPods[i].CreationTimestamp.After(matchedPods[j].CreationTimestamp.Time)
	})

	// Get logs from matched pods
	for i, pod := range matchedPods {
		if i > 0 {
			_, _ = fmt.Fprintln(out, "\n---")
		}

		containers := getContainersToLog(&pod, opts)
		for i, containerName := range containers {
			// Add container header when showing multiple containers
			if len(containers) > 1 {
				if i > 0 {
					_, _ = fmt.Fprintln(out)
				}
				_, _ = fmt.Fprintf(out, "=== Container: %s ===\n", containerName)
			}
			if err := getContainerLogs(ctx, out, kubeClient, opts, &pod, containerName, namespace); err != nil {
				_, _ = fmt.Fprintf(out, "Error getting logs for %s/%s: %v\n", pod.Name, containerName, err)
			}
		}
	}

	return nil
}

func getContainersToLog(pod *corev1.Pod, opts *logsOptions) []string {
	// If stage is specified, look for container matching that stage
	if opts.stage != "" {
		containerName := fmt.Sprintf("%s-%s", opts.packageName, opts.stage)
		// Verify the container exists in the pod
		for _, c := range pod.Spec.InitContainers {
			if c.Name == containerName {
				return []string{containerName}
			}
		}
		for _, c := range pod.Spec.Containers {
			if c.Name == containerName {
				return []string{containerName}
			}
		}
		// Container not found, return empty (will show error)
		return nil
	}

	// Default: return all init containers that have run or are running
	containers := make([]string, 0, len(pod.Status.InitContainerStatuses))

	for _, cs := range pod.Status.InitContainerStatuses {
		// Skip containers that haven't started yet
		if cs.State.Waiting != nil {
			continue
		}
		// Skip the copy/init container (usually first one, named *-init)
		if strings.HasSuffix(cs.Name, "-init") {
			continue
		}
		containers = append(containers, cs.Name)
	}

	// If we found init containers, return them
	if len(containers) > 0 {
		return containers
	}

	// No package execution containers found, try any init container that has run
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Running != nil || cs.State.Terminated != nil {
			containers = append(containers, cs.Name)
		}
	}

	if len(containers) > 0 {
		return containers
	}

	// Fallback: if nothing has run yet, show the first init container (will show waiting message)
	if len(pod.Spec.InitContainers) > 0 {
		return []string{pod.Spec.InitContainers[0].Name}
	}

	return nil
}

func getContainerLogs(ctx context.Context, out io.Writer, kubeClient *client.Client, opts *logsOptions, pod *corev1.Pod, containerName, namespace string) error {
	// Check container status first
	containerStatus := getContainerStatus(pod, containerName)
	statusStr := ""
	if containerStatus != nil {
		if containerStatus.State.Waiting != nil {
			reason := containerStatus.State.Waiting.Reason
			if reason == "" {
				reason = "Waiting"
			}
			statusStr = fmt.Sprintf(" [%s]", reason)
		} else if containerStatus.State.Running != nil {
			statusStr = " [Running]"
		} else if containerStatus.State.Terminated != nil {
			exitCode := containerStatus.State.Terminated.ExitCode
			if exitCode == 0 {
				statusStr = " [Completed]"
			} else {
				statusStr = fmt.Sprintf(" [Exit: %d]", exitCode)
			}
		}
	}

	// Print header
	_, _ = fmt.Fprintf(out, "==> Pod: %s, Container: %s%s, Node: %s <==\n", pod.Name, containerName, statusStr, pod.Spec.NodeName)

	// If container is waiting, show a helpful message instead of an error
	if containerStatus != nil && containerStatus.State.Waiting != nil {
		reason := containerStatus.State.Waiting.Reason
		message := containerStatus.State.Waiting.Message
		if message == "" {
			message = "Container has not started yet"
		}
		_, _ = fmt.Fprintf(out, "(no logs available: %s - %s)\n", reason, message)
		return nil
	}

	// Build log options
	logOpts := &corev1.PodLogOptions{
		Container: containerName,
		Follow:    opts.follow,
	}

	if opts.tail >= 0 {
		logOpts.TailLines = &opts.tail
	}

	// Get logs
	req := kubeClient.Kubernetes().CoreV1().Pods(namespace).GetLogs(pod.Name, logOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("opening log stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	// Copy logs to output
	_, err = io.Copy(out, stream)
	if err != nil && err != context.Canceled {
		return fmt.Errorf("reading logs: %w", err)
	}

	return nil
}

// getContainerStatus finds the status of a container by name
func getContainerStatus(pod *corev1.Pod, containerName string) *corev1.ContainerStatus {
	// Check init containers first
	for i := range pod.Status.InitContainerStatuses {
		if pod.Status.InitContainerStatuses[i].Name == containerName {
			return &pod.Status.InitContainerStatuses[i]
		}
	}
	// Then regular containers
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == containerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}
