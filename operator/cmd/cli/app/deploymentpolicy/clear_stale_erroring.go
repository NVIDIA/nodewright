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

package deploymentpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
)

const (
	nodeStateAnnotationPrefix  = v1alpha1.METADATA_PREFIX + "/nodeState_"
	drainStartAnnotationPrefix = v1alpha1.METADATA_PREFIX + "/drainStart_"
	// statusKeyPrefix is the same for the node's status annotation and label
	statusKeyPrefix = v1alpha1.METADATA_PREFIX + "/status_"
)

// keptNode is an erroring node the command leaves alone, and why.
type keptNode struct {
	name   string
	reason string
}

// staleErroringPlan splits a NodeWright's erroring nodes into the ones whose status is stale and
// the ones that may have failed for a real reason.
type staleErroringPlan struct {
	clear []string
	keep  []keptNode
}

// isErroring reads the annotation, which is where the operator reads a node's status from.
func isErroring(node *corev1.Node, name string) bool {
	return node.Annotations[statusKeyPrefix+name] == string(v1alpha1.StatusErroring)
}

// keepReason returns why an erroring node must be left alone, or "" if its status is stale. The
// checks are the ones under Known Issues in docs/user-guide/deployment-policy.md; anything that
// cannot be read counts as a real failure.
func keepReason(node *corev1.Node, name string) string {
	if _, ok := node.Annotations[drainStartAnnotationPrefix+name]; ok {
		return "a drain was started on it (" + drainStartAnnotationPrefix + name + " is set)"
	}

	raw, ok := node.Annotations[nodeStateAnnotationPrefix+name]
	if !ok {
		return "it has no package state"
	}
	var state v1alpha1.NodeState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return fmt.Sprintf("its package state cannot be read: %v", err)
	}
	if len(state) == 0 {
		return "it has no package state"
	}

	packages := make([]string, 0, len(state))
	for key := range state {
		packages = append(packages, key)
	}
	sort.Strings(packages)
	for _, key := range packages {
		switch status := state[key]; status.State {
		case v1alpha1.StateErroring, v1alpha1.StateUnknown:
			return fmt.Sprintf("package %s is %s at stage %s", key, status.State, status.Stage)
		}
	}
	return ""
}

func planStaleErroring(ctx context.Context, kube kubernetes.Interface, name string) (*staleErroringPlan, error) {
	nodes, err := kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	plan := &staleErroringPlan{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if !isErroring(node, name) {
			continue
		}
		if reason := keepReason(node, name); reason != "" {
			plan.keep = append(plan.keep, keptNode{name: node.Name, reason: reason})
			continue
		}
		plan.clear = append(plan.clear, node.Name)
	}
	sort.Strings(plan.clear)
	sort.Slice(plan.keep, func(i, j int) bool { return plan.keep[i].name < plan.keep[j].name })
	return plan, nil
}

func (p *staleErroringPlan) print(out io.Writer, name string) {
	if len(p.clear) == 0 && len(p.keep) == 0 {
		_, _ = fmt.Fprintf(out, "\nNo node is erroring for NodeWright %q\n", name)
		return
	}
	if len(p.clear) > 0 {
		_, _ = fmt.Fprintf(out, "\nStale erroring nodes to set to waiting (%d):\n", len(p.clear))
		for _, node := range p.clear {
			_, _ = fmt.Fprintf(out, "  - %s\n", node)
		}
	}
	if len(p.keep) > 0 {
		_, _ = fmt.Fprintf(out, "\nErroring nodes left alone (%d):\n", len(p.keep))
		for _, node := range p.keep {
			_, _ = fmt.Fprintf(out, "  - %s: %s\n", node.name, node.reason)
		}
	}
}

// clearStaleErroring sets one node's status to waiting. The patch carries the resourceVersion of
// the node that was checked, so a write by the operator in between is a conflict and the node is
// read and checked again. Pausing cannot be used to keep the operator out: it overwrites every
// node's status with paused. It returns why the node was left alone, or "" once it is cleared.
func clearStaleErroring(ctx context.Context, kube kubernetes.Interface, nodeName, name string) (string, error) {
	key := statusKeyPrefix + name
	kept := ""
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		kept = ""
		node, err := kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !isErroring(node, name) {
			kept = fmt.Sprintf("its status is now %q", node.Annotations[key])
			return nil
		}
		if kept = keepReason(node, name); kept != "" {
			return nil
		}

		waiting := map[string]string{key: string(v1alpha1.StatusWaiting)}
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
			"resourceVersion": node.ResourceVersion,
			"annotations":     waiting,
			"labels":          waiting,
		}})
		if err != nil {
			return err
		}
		_, err = kube.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	})
	if err != nil {
		return "", fmt.Errorf("clearing status of node %q: %w", nodeName, err)
	}
	return kept, nil
}

// apply clears every planned node. Nodes that stopped being stale since the plan was made move to
// keep. It tries all nodes and returns the failures together.
func (p *staleErroringPlan) apply(ctx context.Context, cmd *cobra.Command, kube kubernetes.Interface, name string) error {
	var errs []error
	for _, nodeName := range p.clear {
		kept, err := clearStaleErroring(ctx, kube, nodeName, name)
		switch {
		case err != nil:
			errs = append(errs, err)
		case kept != "":
			p.keep = append(p.keep, keptNode{name: nodeName, reason: kept})
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Left node %s alone: %s\n", nodeName, kept)
		default:
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Set status of node %s to waiting\n", nodeName)
		}
	}
	return errors.Join(errs...)
}
