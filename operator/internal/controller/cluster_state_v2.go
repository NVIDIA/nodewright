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

package controller

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	k8staints "k8s.io/kubernetes/pkg/util/taints"
)

// tracks original objects
// useful for using patch api
// insert when u first see object
// later get a c
type ObjectTracker struct {
	objects map[string]client.Object
}

// GetOriginal will return prior tracked object if it exists, otherwise return nil
func (t *ObjectTracker) GetOriginal(obj client.Object) client.Object {
	key := fmt.Sprintf("%s|%s|%s-%s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), obj.GetUID())

	if obj, ok := t.objects[key]; ok {
		return obj
	}
	return nil
}

// Track when calling Track, make sure to pass in a DeepCopy to make sure to save to a copy
func (t *ObjectTracker) Track(obj client.Object) {

	key := fmt.Sprintf("%s|%s|%s-%s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName(), obj.GetUID())

	_, ok := t.objects[key]
	if !ok { // was never inserted, so add it, else dont care
		t.objects[key] = obj
		return
	}
}

type clusterState struct {
	tracker  ObjectTracker
	skyhooks []SkyhookNodes
}

func BuildState(skyhooks *v1alpha1.SkyhookList, nodes *corev1.NodeList, deploymentPolicies *v1alpha1.DeploymentPolicyList) (*clusterState, error) {

	ret := &clusterState{
		tracker:  ObjectTracker{objects: make(map[string]client.Object)},
		skyhooks: make([]SkyhookNodes, len(skyhooks.Items)),
		// nodes:    make(map[string][]*SkyhookNode),
	}

	for idx, skyhook := range skyhooks.Items {
		ret.tracker.Track(skyhook.DeepCopy())

		ret.skyhooks[idx] = &skyhookNodes{
			skyhook:      wrapper.NewSkyhookWrapper(&skyhook),
			nodes:        make([]wrapper.SkyhookNode, 0),
			compartments: make(map[string]*wrapper.Compartment),
		}
		for _, node := range nodes.Items {
			skyNode, err := wrapper.NewSkyhookNode(&node, &skyhook)
			if err != nil {
				return nil, err
			}

			selector, err := metav1.LabelSelectorAsSelector(&skyhook.Spec.NodeSelector)
			if err != nil {
				return nil, err
			}
			if selector.Matches(labels.Set(node.Labels)) { // note: if selector is empty, it selects all
				ret.tracker.Track(node.DeepCopy())
				ret.skyhooks[idx].AddNode(skyNode)
			}
		}

		// find deployment policy and all compartments + the default one
		// Handle backwards compatibility: if no deployment policy is set,
		// create a synthetic default compartment with FixedStrategy based on InterruptionBudget
		if skyhook.Spec.DeploymentPolicy == "" {
			// Load persisted batch state if it exists
			var defaultBatchState *v1alpha1.BatchProcessingState
			if skyhook.Status.CompartmentStatuses != nil {
				if status, exists := skyhook.Status.CompartmentStatuses[v1alpha1.DefaultCompartmentName]; exists && status.BatchState != nil {
					defaultBatchState = status.BatchState
				}
			}

			// Create the legacy default compartment
			nodeCount := len(ret.skyhooks[idx].GetNodes())
			legacyCompartment := createLegacyDefaultCompartment(skyhook.Spec, nodeCount)
			ret.skyhooks[idx].AddCompartment(v1alpha1.DefaultCompartmentName, wrapper.NewCompartmentWrapper(legacyCompartment, defaultBatchState))

			// Assign all nodes to the default compartment for backwards compatibility
			for _, node := range ret.skyhooks[idx].GetNodes() {
				if err := ret.skyhooks[idx].AddCompartmentNode(v1alpha1.DefaultCompartmentName, node); err != nil {
					return nil, fmt.Errorf("error adding node to default compartment: %w", err)
				}
			}
		} else {
			ret.initializeCompartmentsFromPolicy(idx, &skyhook, deploymentPolicies)
		}
	}

	// Sort by priority (ascending), then by name (ascending) if priorities are equal
	sort.Slice(ret.skyhooks, func(i, j int) bool {
		pi := ret.skyhooks[i].GetSkyhook().Spec.Priority
		pj := ret.skyhooks[j].GetSkyhook().Spec.Priority
		if pi != pj {
			return pi < pj
		}
		return ret.skyhooks[i].GetSkyhook().Name < ret.skyhooks[j].GetSkyhook().Name
	})

	for _, skyhook := range ret.skyhooks {
		sort.Slice(skyhook.GetNodes(), func(i, j int) bool {
			ti := skyhook.GetNodes()[i].GetNode().CreationTimestamp
			tj := skyhook.GetNodes()[j].GetNode().CreationTimestamp
			if !ti.Equal(&tj) {
				return ti.Before(&tj)
			}
			return skyhook.GetNodes()[i].GetNode().Name < skyhook.GetNodes()[j].GetNode().Name
		})
	}

	// Partition nodes into compartments for skyhooks with deployment policies
	if err := partitionNodesIntoCompartments(ret); err != nil {
		return nil, fmt.Errorf("partitioning nodes into compartments: %w", err)
	}

	return ret, nil
}

// initializeCompartmentsFromPolicy loads compartments from the specified DeploymentPolicy.
// It handles finding the policy, loading persisted batch states, creating compartments,
// and managing the DeploymentPolicyNotFound condition.
func (ret *clusterState) initializeCompartmentsFromPolicy(idx int, skyhook *v1alpha1.Skyhook, deploymentPolicies *v1alpha1.DeploymentPolicyList) {
	policyFound := false
	for _, deploymentPolicy := range deploymentPolicies.Items {
		if deploymentPolicy.Name == skyhook.Spec.DeploymentPolicy {
			policyFound = true
			// Store the deployment policy reference for auto-reset logic
			policyCopy := deploymentPolicy.DeepCopy()
			ret.skyhooks[idx].(*skyhookNodes).deploymentPolicy = policyCopy

			for _, compartment := range deploymentPolicy.Spec.Compartments {
				// Load persisted batch state from CompartmentStatuses if it exists
				var batchState *v1alpha1.BatchProcessingState
				if skyhook.Status.CompartmentStatuses != nil {
					if status, exists := skyhook.Status.CompartmentStatuses[compartment.Name]; exists && status.BatchState != nil {
						batchState = status.BatchState
					}
				}
				ret.skyhooks[idx].AddCompartment(compartment.Name, wrapper.NewCompartmentWrapper(&compartment, batchState))
			}
			// use policy default
			var defaultBatchState *v1alpha1.BatchProcessingState
			if skyhook.Status.CompartmentStatuses != nil {
				if status, exists := skyhook.Status.CompartmentStatuses[v1alpha1.DefaultCompartmentName]; exists && status.BatchState != nil {
					defaultBatchState = status.BatchState
				}
			}
			ret.skyhooks[idx].AddCompartment(v1alpha1.DefaultCompartmentName, wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:     v1alpha1.DefaultCompartmentName,
				Budget:   deploymentPolicy.Spec.Default.Budget,
				Strategy: deploymentPolicy.Spec.Default.Strategy,
			}, defaultBatchState))
			break
		}
	}

	// If deployment policy was specified but not found, mark it for error handling
	if !policyFound {
		// Set a condition to indicate the DeploymentPolicy is not found.
		// Note: The webhook also validates policy existence at creation/update time,
		// but this runtime check is needed to handle cases where:
		// 1. A policy is deleted after a Skyhook references it
		// 2. The webhook was bypassed or disabled
		// This provides defense-in-depth validation.
		wrapper.AddSkyhookConditionWithLegacy(ret.skyhooks[idx].GetSkyhook(), metav1.Condition{
			Type:               wrapper.SkyhookConditionDeploymentPolicyNotFound,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: skyhook.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "DeploymentPolicyNotFound",
			Message:            fmt.Sprintf("DeploymentPolicy %q not found", skyhook.Spec.DeploymentPolicy),
		})
	} else {
		// Policy found - clear any previous error condition if it exists
		wrapper.RemoveSkyhookConditionTypes(ret.skyhooks[idx].GetSkyhook(),
			wrapper.SkyhookConditionDeploymentPolicyNotFound,
			wrapper.LegacySkyhookConditionType(wrapper.SkyhookConditionDeploymentPolicyNotFound),
		)
	}
}

// getAutoTaintNodes returns nodes that should be auto-tainted with the runtime-required taint.
// A node should be auto-tainted if:
// 1. It matches a Skyhook with RuntimeRequired=true AND AutoTaintNewNodes=true
// 2. It doesn't already have the runtime-required taint
// 3. It has no Skyhook annotations (it's a "new" node)
func (cs *clusterState) getAutoTaintNodes(taint corev1.Taint) []*corev1.Node {
	seen := make(map[types.UID]bool)
	result := make([]*corev1.Node, 0)
	for _, skyhook := range cs.skyhooks {
		if !skyhook.GetSkyhook().Spec.RuntimeRequired || !skyhook.GetSkyhook().Spec.AutoTaintNewNodes {
			continue
		}
		for _, nodeWrapper := range skyhook.GetNodes() {
			node := nodeWrapper.GetNode()
			if seen[node.UID] {
				continue
			}
			seen[node.UID] = true
			if k8staints.TaintExists(node.Spec.Taints, &taint) {
				continue
			}
			if nodeWrapper.HasSkyhookAnnotations() {
				continue
			}
			result = append(result, node)

		}
	}
	return result
}

// createLegacyDefaultCompartment creates a synthetic default compartment for backwards compatibility
// when no DeploymentPolicy is specified. It translates the legacy InterruptionBudget into a
// FixedStrategy compartment that behaves the same way.
func createLegacyDefaultCompartment(spec v1alpha1.SkyhookSpec, nodeCount int) *v1alpha1.Compartment {
	// Create a synthetic budget from InterruptionBudget
	// If InterruptionBudget is not set, default to 100% (all nodes at once)
	var budget v1alpha1.DeploymentBudget
	if spec.InterruptionBudget.Percent != nil {
		budget.Percent = spec.InterruptionBudget.Percent
	} else if spec.InterruptionBudget.Count != nil {
		budget.Count = spec.InterruptionBudget.Count
	} else {
		// Default to 100% for backwards compatibility (process all nodes at once)
		budget.Percent = ptr[int](100)
	}

	// Calculate the ceiling to maintain backwards-compatible batch size
	// This ensures the FixedStrategy processes the same number of nodes per batch
	// as the legacy InterruptionBudget behavior
	var initialBatch int
	if budget.Count != nil {
		// Count budget: use the count directly
		initialBatch = max(1, min(nodeCount, *budget.Count))
	} else if budget.Percent != nil {
		// Percent budget: calculate based on total nodes
		if nodeCount > 0 {
			limit := float64(*budget.Percent) / 100
			initialBatch = max(1, int(float64(nodeCount)*limit))
		} else {
			initialBatch = 1
		}
	} else {
		initialBatch = 1
	}

	// Create a FixedStrategy with InitialBatch matching the legacy ceiling behavior
	fixedStrategy := &v1alpha1.FixedStrategy{}
	fixedStrategy.Default()
	fixedStrategy.InitialBatch = &initialBatch

	return &v1alpha1.Compartment{
		Name:   v1alpha1.DefaultCompartmentName,
		Budget: budget,
		Strategy: &v1alpha1.DeploymentStrategy{
			Fixed: fixedStrategy,
		},
	}
}

func GetNextSkyhook(skyhooks []SkyhookNodes) SkyhookNodes {
	for _, skyhook := range skyhooks {
		if skyhook.IsComplete() || skyhook.IsDisabled() {
			continue
		}
		return skyhook
	}
	// Always return the last non disabled skyhook to handle any final state logic
	// for i := len(skyhooks) - 1; i >= 0; i-- {
	// 	if !skyhooks[i].IsDisabled() {
	// 		return skyhooks[i]
	// 	}
	// }
	return nil
}

// IsNodeReadyForSkyhook checks if a node has completed all higher-priority skyhooks.
// The check depends on each predecessor's sequencing mode:
// - sequencing: node (default) — checks per-node completion on that specific node
// - sequencing: all — checks global completion (all nodes must be done)
func IsNodeReadyForSkyhook(nodeName string, skyhook SkyhookNodes, allSkyhooks []SkyhookNodes) bool {
	targetPriority := skyhook.GetSkyhook().Spec.Priority
	targetName := skyhook.GetSkyhook().Name

	for _, other := range allSkyhooks {
		// Skip disabled skyhooks - they don't block
		if other.IsDisabled() {
			continue
		}

		otherPriority := other.GetSkyhook().Spec.Priority
		otherName := other.GetSkyhook().Name

		// Skip same or lower priority (higher number) skyhooks
		// For same priority, use name ordering (skip if other name >= target name)
		if otherPriority > targetPriority ||
			(otherPriority == targetPriority && otherName >= targetName) {
			continue
		}

		if other.GetSkyhook().Spec.IsPerNodeSequencing() {
			// Per-node: check if THIS node completed the predecessor
			_, nodeInOther := other.GetNode(nodeName)
			if nodeInOther != nil && !nodeInOther.IsComplete() {
				return false
			}
		} else {
			// Global (sequencing: all): predecessor must be globally complete
			if !other.IsComplete() {
				return false
			}
		}
	}
	return true
}

// isBlockedByGlobalPredecessor checks if any higher-priority skyhook with sequencing: all
// is not yet globally complete, which would block this skyhook.
func isBlockedByGlobalPredecessor(skyhook SkyhookNodes, allSkyhooks []SkyhookNodes) bool {
	targetPriority := skyhook.GetSkyhook().Spec.Priority
	targetName := skyhook.GetSkyhook().Name

	for _, other := range allSkyhooks {
		if other.IsDisabled() {
			continue
		}

		otherPriority := other.GetSkyhook().Spec.Priority
		otherName := other.GetSkyhook().Name

		if otherPriority > targetPriority ||
			(otherPriority == targetPriority && otherName >= targetName) {
			continue
		}

		// Only sequencing: all predecessors create skyhook-level waiting
		if !other.GetSkyhook().Spec.IsPerNodeSequencing() && !other.IsComplete() {
			return true
		}
	}
	return false
}

// SkyhookNodes wraps the skyhook and nodes that it pertains too
type SkyhookNodes interface {
	CollectNodeStatus() v1alpha1.Status
	GetSkyhook() *wrapper.Skyhook
	GetNodes() []wrapper.SkyhookNode
	GetNode(name string) (v1alpha1.Status, wrapper.SkyhookNode)
	AddNode(node wrapper.SkyhookNode)
	IsComplete() bool
	IsDisabled() bool
	IsPaused() bool
	HasUninstallWork() (bool, error)
	UpdateBlockedCondition() error
	UpdateUninstallConditions() error
	UpdateNodeStateMalformedCondition()
	NodeCount() int
	SetStatus(status v1alpha1.Status)
	Status() v1alpha1.Status
	GetPriorStatus() v1alpha1.Status
	// WasUpdated() bool
	UpdateCondition(logger logr.Logger) bool
	ReportState()
	Migrate(logger logr.Logger) error

	GetCompartments() map[string]*wrapper.Compartment
	AddCompartment(name string, compartment *wrapper.Compartment)
	AddCompartmentNode(name string, node wrapper.SkyhookNode) error
	AssignNodeToCompartment(node wrapper.SkyhookNode) (string, error)
	GetDeploymentPolicy() *v1alpha1.DeploymentPolicy
}

var _ SkyhookNodes = &skyhookNodes{}

// skyhookNodes impl's. SkyhookNodes
type skyhookNodes struct {
	skyhook          *wrapper.Skyhook
	nodes            []wrapper.SkyhookNode
	priorStatus      v1alpha1.Status
	compartments     map[string]*wrapper.Compartment
	deploymentPolicy *v1alpha1.DeploymentPolicy
}

func (s *skyhookNodes) GetPriorStatus() v1alpha1.Status {
	return s.priorStatus
}

func (s *skyhookNodes) GetNodes() []wrapper.SkyhookNode {
	return s.nodes
}

func (s *skyhookNodes) AddNode(node wrapper.SkyhookNode) {
	s.nodes = append(s.nodes, node)
}

func (s *skyhookNodes) GetSkyhook() *wrapper.Skyhook {
	return s.skyhook
}

func (s *skyhookNodes) NodeCount() int {
	return len(s.nodes)
}

// func (s *skyhookNodes) WasUpdated() bool {
// 	return s.skyhook.WasUpdated()
// }

func (s *skyhookNodes) IsComplete() bool {
	for _, node := range s.nodes {
		if !node.IsComplete() {
			return false
		}
	}

	return true
}

func (s *skyhookNodes) IsDisabled() bool {
	return s.skyhook.IsDisabled()
}

func (s *skyhookNodes) IsPaused() bool {
	return s.skyhook.IsPaused()
}

// HasUninstallWork returns true if the skyhook has any packages that need uninstall
// processing:
//   - explicitly requested (IsUninstalling), OR
//   - already in progress on any node (StageUninstall in node annotations), OR
//   - CR is being deleted and an enabled package is still in node state (finalizer-driven)
//
// An error is returned if any node's state annotation cannot be read. Callers
// must surface the error — silently skipping would let this report "no work"
// when there really is pending uninstall we just can't see, which would allow
// a Skyhook to appear complete or a finalizer to drop prematurely.
func (s *skyhookNodes) HasUninstallWork() (bool, error) {
	beingDeleted := !s.skyhook.DeletionTimestamp.IsZero()
	for _, pkg := range s.skyhook.Spec.Packages {
		if pkg.IsUninstalling() {
			return true, nil
		}
	}
	for _, node := range s.nodes {
		nodeState, err := node.State()
		if err != nil {
			return false, fmt.Errorf("node %s: reading state: %w", node.GetNode().Name, err)
		}
		for _, pkg := range s.skyhook.Spec.Packages {
			if nodeState.IsUninstallCycleInProgress(pkg.GetUniqueName()) {
				return true, nil
			}
			// Finalizer case: CR deleting, package enabled, still present on node
			if beingDeleted && pkg.UninstallEnabled() && !nodeState.IsUninstalled(pkg.GetUniqueName()) {
				return true, nil
			}
		}
	}
	return false, nil
}

// UpdateBlockedCondition sets or clears the Blocked condition based on whether
// any package's dependency is being uninstalled. The DAG naturally prevents the
// dependent from running; this condition informs the user.
// Uses node annotations (StageUninstall) as source of truth, not the spec's apply flag.
//
// Returns an error if any node's state annotation cannot be read; callers must
// surface it so an unreadable node doesn't silently mask a blocked dependency.
func (s *skyhookNodes) UpdateBlockedCondition() error {
	var blockedMsgs []string

	// Build a set of packages that are being uninstalled on any node (source of truth)
	uninstallingOnAnyNode := make(map[string]bool)
	for _, node := range s.nodes {
		nodeState, err := node.State()
		if err != nil {
			return fmt.Errorf("node %s: reading state: %w", node.GetNode().Name, err)
		}
		for _, pkg := range s.skyhook.Spec.Packages {
			if nodeState.IsUninstallCycleInProgress(pkg.GetUniqueName()) {
				uninstallingOnAnyNode[pkg.Name] = true
			}
		}
	}

	for bName, bPkg := range s.skyhook.Spec.Packages {
		for depName := range bPkg.DependsOn {
			if uninstallingOnAnyNode[depName] {
				blockedMsgs = append(blockedMsgs, fmt.Sprintf(
					"package %s is blocked: dependency %s is being uninstalled", bName, depName))
			}
		}
	}

	sort.Strings(blockedMsgs) // deterministic order to avoid unnecessary status writes

	condType := fmt.Sprintf("%s/Blocked", v1alpha1.METADATA_PREFIX)
	if len(blockedMsgs) > 0 {
		s.skyhook.AddCondition(metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: s.skyhook.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "DependencyUninstalled",
			Message:            strings.Join(blockedMsgs, "; "),
		})
	} else {
		// Clear the condition if no packages are blocked
		s.skyhook.RemoveCondition(condType)
	}

	return nil
}

// UpdateUninstallConditions sets or clears UninstallInProgress and UninstallFailed
// conditions based on node annotations. Works for both explicit (apply=true) and
// finalizer-driven (beingDeleted + enabled) uninstall.
//
// State-read errors (e.g. malformed JSON in the nodeState annotation) are
// surfaced by UpdateNodeStateMalformedCondition, not here — they're
// stage-agnostic and shouldn't be conflated under "UninstallFailed". This
// function silently skips nodes with unreadable state so the uninstall
// conditions reflect whatever readable nodes show.
func (s *skyhookNodes) UpdateUninstallConditions() error {
	beingDeleted := !s.skyhook.DeletionTimestamp.IsZero()
	inProgress := false
	hasErrors := false

	for _, node := range s.nodes {
		nodeState, err := node.State()
		if err != nil {
			continue
		}
		for _, pkg := range s.skyhook.Spec.Packages {
			// nodeState is the source of truth: if a cycle is already in
			// progress on this node we must surface it even when the spec no
			// longer requests uninstall. For example, a user flipping
			// apply=true → false while the package is at StageUninstallInterrupt
			// cannot cancel the cycle (the interrupt has fired and must run to
			// completion), so UninstallInProgress / UninstallFailed must track
			// the node until the cycle actually exits.
			cycleInProgress := nodeState.IsUninstallCycleInProgress(pkg.GetUniqueName())
			if !cycleInProgress && !pkg.IsUninstalling() && (!beingDeleted || !pkg.UninstallEnabled()) {
				continue
			}
			if cycleInProgress {
				inProgress = true
				status := nodeState[pkg.GetUniqueName()]
				if status.State == v1alpha1.StateErroring {
					hasErrors = true
				}
			}
		}
	}

	inProgressType := fmt.Sprintf("%s/UninstallInProgress", v1alpha1.METADATA_PREFIX)
	failedType := fmt.Sprintf("%s/UninstallFailed", v1alpha1.METADATA_PREFIX)

	if inProgress {
		s.skyhook.AddCondition(metav1.Condition{
			Type:               inProgressType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: s.skyhook.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "UninstallInProgress",
			Message:            "One or more packages are being uninstalled",
		})
	} else {
		s.skyhook.RemoveCondition(inProgressType)
	}

	if hasErrors {
		s.skyhook.AddCondition(metav1.Condition{
			Type:               failedType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: s.skyhook.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "UninstallPodFailing",
			Message:            "One or more uninstall pods are failing",
		})
	} else {
		s.skyhook.RemoveCondition(failedType)
	}

	return nil
}

// UpdateNodeStateMalformedCondition sets or clears a
// `skyhook.nvidia.com/NodeStateMalformed` condition listing the nodes whose
// `nodeState_<skyhook>` annotation cannot be parsed for this Skyhook. Unlike
// UninstallFailed, this condition is stage-agnostic — malformed state
// affects every lifecycle decision (install, upgrade, uninstall, finalizer)
// so it deserves its own user-visible signal.
//
// Node names longer than 10 characters are truncated to the first 10 chars
// plus "..." to keep the condition message compact across large clusters.
func (s *skyhookNodes) UpdateNodeStateMalformedCondition() {
	var badNodes []string
	for _, node := range s.nodes {
		if _, err := node.State(); err != nil {
			badNodes = append(badNodes, node.GetNode().Name)
		}
	}

	condType := fmt.Sprintf("%s/NodeStateMalformed", v1alpha1.METADATA_PREFIX)

	if len(badNodes) == 0 {
		s.skyhook.RemoveCondition(condType)
		return
	}

	sort.Strings(badNodes) // deterministic order so the condition doesn't churn
	truncated := make([]string, len(badNodes))
	for i, n := range badNodes {
		truncated[i] = truncateNodeName(n)
	}

	s.skyhook.AddCondition(metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: s.skyhook.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "ParseError",
		Message: fmt.Sprintf("nodeState annotation cannot be parsed on %d node(s): %s",
			len(badNodes), strings.Join(truncated, ", ")),
	})
}

// truncateNodeName shortens node names longer than 10 characters to the
// first 10 characters plus "..." so condition messages stay compact on
// clusters with long DNS-style node names (e.g. ip-10-0-1-234.us-west-2...).
func truncateNodeName(name string) string {
	const maxLen = 10
	if len(name) <= maxLen {
		return name
	}
	return name[:maxLen] + "..."
}

func (s *skyhookNodes) Status() v1alpha1.Status {
	return s.skyhook.Status.Status
}

func (s *skyhookNodes) SetStatus(status v1alpha1.Status) {
	s.priorStatus = s.skyhook.Status.Status
	oldStatus := s.skyhook.Status.Status

	s.skyhook.SetStatus(status)

	// Auto-reset batch state when transitioning to Complete (if configured)
	// We must reset BOTH the persisted status AND the in-memory compartment wrapper's BatchState
	// Otherwise updateCompartmentStatuses will overwrite the reset with stale values
	if oldStatus != v1alpha1.StatusComplete && status == v1alpha1.StatusComplete {
		resetSkyhookBatchState(s)
	}
}

// CollectNodeStatus collects all the nodes current status
func (s *skyhookNodes) CollectNodeStatus() v1alpha1.Status {
	complete := 0
	erroring := 0
	blocked := 0
	unknown := 0
	waiting := 0
	inProgress := 0

	for _, node := range s.nodes {
		if node.IsComplete() {
			complete += 1
			continue
		}
		switch node.Status() {
		case v1alpha1.StatusInProgress:
			inProgress += 1
		case v1alpha1.StatusErroring:
			erroring += 1
		case v1alpha1.StatusBlocked:
			blocked += 1
		case v1alpha1.StatusUnknown:
			unknown += 1
		case v1alpha1.StatusWaiting:
			waiting += 1
		}
	}

	remaining := len(s.nodes) - complete

	// all need to be complete to be considered complete
	if complete == len(s.nodes) {
		return v1alpha1.StatusComplete
	}

	// if any node is in progress, show progress (per-node ordering: some nodes making progress)
	if inProgress > 0 {
		return v1alpha1.StatusInProgress
	}

	// if all remaining nodes are erroring, the skyhook is erroring
	if erroring > 0 && erroring == remaining {
		return v1alpha1.StatusErroring
	}

	// if all remaining nodes are blocked, the skyhook is blocked
	if blocked > 0 && blocked == remaining {
		return v1alpha1.StatusBlocked
	}

	// if all remaining nodes are unknown, the skyhook is unknown
	if unknown > 0 && unknown == remaining {
		return v1alpha1.StatusUnknown
	}

	// if all remaining nodes are waiting, the skyhook is waiting
	if waiting > 0 && waiting == remaining {
		return v1alpha1.StatusWaiting
	}

	// mixed states - default to unknown
	return v1alpha1.StatusUnknown
}

// Pick will grab node if exists
func (s *skyhookNodes) GetNode(name string) (v1alpha1.Status, wrapper.SkyhookNode) {

	for _, node := range s.nodes {
		if node.GetNode().Name == name {
			return node.Status(), node
		}
	}
	return v1alpha1.StatusUnknown, nil
}

func (s *skyhookNodes) GetDeploymentPolicy() *v1alpha1.DeploymentPolicy {
	return s.deploymentPolicy
}

// resetSkyhookBatchState resets all compartment batch states to fresh values if configured.
// This is used when transitioning to Complete or when a version change is detected.
func resetSkyhookBatchState(skyhook SkyhookNodes) {
	if !skyhook.GetSkyhook().Skyhook.ShouldResetBatchStateOnCompletion(skyhook.GetDeploymentPolicy()) {
		return
	}

	// Reset persisted compartment statuses via the canonical API method
	if skyhook.GetSkyhook().Skyhook.ResetCompartmentBatchStates() {
		skyhook.GetSkyhook().Updated = true
	}

	// Reset in-memory compartment wrapper batch states to stay in sync
	for _, compartment := range skyhook.GetCompartments() {
		compartment.BatchState = v1alpha1.BatchProcessingState{
			CurrentBatch: 1,
		}
	}
}

func (s *skyhookNodes) UpdateCondition(logger logr.Logger) bool {
	skyhookStatus := s.Status()
	readyStatus := metav1.ConditionFalse
	if s.IsComplete() {
		readyStatus = metav1.ConditionTrue
	}

	nodeStatuses := make(map[string]v1alpha1.Status, len(s.skyhook.Status.NodeStatus))
	for nodeName, status := range s.skyhook.Status.NodeStatus {
		nodeStatuses[nodeName] = status
	}

	nodeNames := make([]string, 0, len(s.nodes))
	for _, node := range s.nodes {
		nodeNames = append(nodeNames, node.GetNode().Name)
	}
	sort.Strings(nodeNames)
	byStatus := wrapper.SkyhookReadyConditionStatusGroups(nodeStatuses, nodeNames)

	if wrapper.SkyhookReadyConditionMessageTruncated(byStatus) {
		logger.WithName("skyhook-ready-condition").Info(
			"Ready condition message truncated; full per-status node lists",
			"skyhook", s.skyhook.Name,
			"complete", byStatus[v1alpha1.StatusComplete],
			"inProgress", byStatus[v1alpha1.StatusInProgress],
			"blocked", byStatus[v1alpha1.StatusBlocked],
			"erroring", byStatus[v1alpha1.StatusErroring],
			"waiting", byStatus[v1alpha1.StatusWaiting],
			"paused", byStatus[v1alpha1.StatusPaused],
			"disabled", byStatus[v1alpha1.StatusDisabled],
			"unknown", byStatus[v1alpha1.StatusUnknown],
		)
	}

	readyCondition := metav1.Condition{
		Type:               wrapper.SkyhookConditionReady,
		Status:             readyStatus,
		ObservedGeneration: s.skyhook.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             wrapper.SkyhookReadyConditionReason(skyhookStatus),
		Message:            wrapper.SkyhookReadyConditionMessage(nodeStatuses, nodeNames),
	}

	changed := wrapper.AddSkyhookConditionWithLegacy(s.skyhook, readyCondition)

	legacyMessage := readyCondition.Message
	if s.priorStatus != "" && s.priorStatus != skyhookStatus {
		legacyMessage = fmt.Sprintf("Transitioned [%s] -> [%s]", s.priorStatus, skyhookStatus)
	}
	legacyTransitionCondition := readyCondition
	legacyTransitionCondition.Type = wrapper.LegacySkyhookConditionTransition
	legacyTransitionReason := string(skyhookStatus)
	if legacyTransitionReason == "" {
		legacyTransitionReason = string(v1alpha1.StatusUnknown)
	}
	legacyTransitionCondition.Reason = legacyTransitionReason
	legacyTransitionCondition.Message = legacyMessage
	changed = wrapper.AddSkyhookConditionRefreshingTransitionOnReasonOrMessage(s.skyhook, legacyTransitionCondition) || changed

	return changed
}

type NodePicker struct {
	logger                    logr.Logger
	priorityNodes             map[string]time.Time
	runtimeRequiredToleration corev1.Toleration
}

func NewNodePicker(logger logr.Logger, runtimeRequiredToleration corev1.Toleration) *NodePicker {
	return &NodePicker{
		logger:                    logger,
		priorityNodes:             make(map[string]time.Time),
		runtimeRequiredToleration: runtimeRequiredToleration,
	}
}

// primeAndPruneNodes add current priority from skyhook status, and check time removing old ones
func (s *NodePicker) primeAndPruneNodes(skyhook SkyhookNodes) {

	pruneCompletedNodePriorities(skyhook)
	for n, t := range skyhook.GetSkyhook().Status.NodePriority {
		s.priorityNodes[n] = t.Time
	}
}

func pruneCompletedNodePriorities(skyhook SkyhookNodes) bool {
	changed := false
	for n := range skyhook.GetSkyhook().Status.NodePriority {
		if nodeStatus, _ := skyhook.GetNode(n); nodeStatus == v1alpha1.StatusComplete {
			skyhook.GetSkyhook().RemoveNodePriority(n)
			changed = true
		}
	}
	return changed
}

// upsertPick updates or inserts the node priority for a given name in the Skyhook object.
// If the node priority already exists, it updates the priority with the current time.
// If the node priority does not exist, it inserts a new priority with the current time.
// The updated Skyhook object is marked as "Updated".
//
// Parameters:
// - name: The name of the node.
// - skyhook: The Skyhook object to update.
func (s *NodePicker) upsertPick(name string, skyhook *wrapper.Skyhook) {

	if skyhook.Status.NodePriority == nil {
		skyhook.Status.NodePriority = make(map[string]metav1.Time)
	}

	if t, ok := skyhook.Status.NodePriority[name]; ok { // check if exists before inserting
		s.priorityNodes[name] = t.Time
		return
	}

	now := time.Now()
	s.priorityNodes[name] = now

	skyhook.Status.NodePriority[name] = metav1.NewTime(now)
	skyhook.Updated = true
}

func CheckTaintToleration(logger logr.Logger, tolerations []corev1.Toleration, taints []corev1.Taint) bool {
	// Must tolerate all taints.
	all_tolerated := true
	for _, taint := range taints {
		tolerated := false
		for _, toleration := range tolerations {
			if toleration.ToleratesTaint(logger.WithName("CheckTaintToleration"), &taint, false) {
				tolerated = true
				break
			}
		}
		all_tolerated = all_tolerated && tolerated
	}
	return all_tolerated
}

func (np *NodePicker) SelectNodes(s SkyhookNodes) []wrapper.SkyhookNode {

	np.primeAndPruneNodes(s)

	// Straight from skyhook_controller CreatePodForPackage
	tolerations := append([]corev1.Toleration{ // tolerate all cordon
		{
			Key:      TaintUnschedulable,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}, s.GetSkyhook().Spec.AdditionalTolerations...)

	if s.GetSkyhook().Spec.RuntimeRequired {
		tolerations = append(tolerations, np.runtimeRequiredToleration)
	}

	// All skyhooks now use compartments (with a default 100% compartment if none specified)
	compartments := s.GetCompartments()
	return np.selectNodesWithCompartments(s, compartments, tolerations)
}

// CheckNodeIgnoreLabel checks if a node has the ignore label set to true
func CheckNodeIgnoreLabel(node wrapper.SkyhookNode) bool {
	ignoreLabel := fmt.Sprintf("%s/ignore", v1alpha1.METADATA_PREFIX)
	if val, ok := node.GetNode().Labels[ignoreLabel]; ok && val == "true" {
		return true
	}
	return false
}

// selectNodesWithCompartments selects nodes using compartment-based batch processing
func (np *NodePicker) selectNodesWithCompartments(s SkyhookNodes, compartments map[string]*wrapper.Compartment, tolerations []corev1.Toleration) []wrapper.SkyhookNode {
	selectedNodes := make([]wrapper.SkyhookNode, 0)
	nodesWithTaintTolerationIssue := make([]string, 0)
	ignoredNodes := make([]string, 0)

	// First, check ALL nodes for taint and ignore issues to set the conditions correctly
	// This ensures the conditions reflect the true state even when no batch is being processed
	for _, compartment := range compartments {
		for _, node := range compartment.GetNodes() {
			if !CheckTaintToleration(np.logger, tolerations, node.GetNode().Spec.Taints) {
				nodesWithTaintTolerationIssue = append(nodesWithTaintTolerationIssue, node.GetNode().Name)
			}
			if CheckNodeIgnoreLabel(node) {
				ignoredNodes = append(ignoredNodes, node.GetNode().Name)
			}
		}
	}

	// Process each compartment according to its strategy
	for _, compartment := range compartments {
		batchNodes := compartment.GetNodesForNextBatch()

		for _, node := range batchNodes {
			// Check if node is ignored
			if CheckNodeIgnoreLabel(node) {
				node.SetStatus(v1alpha1.StatusBlocked)
				continue
			}
			// Check taint toleration
			if CheckTaintToleration(np.logger, tolerations, node.GetNode().Spec.Taints) {
				selectedNodes = append(selectedNodes, node)
				np.upsertPick(node.GetNode().GetName(), s.GetSkyhook())
			} else {
				node.SetStatus(v1alpha1.StatusBlocked)
			}
		}
	}

	// Add condition about taint toleration issues
	sort.Strings(nodesWithTaintTolerationIssue)
	np.updateTaintToleranceCondition(s, nodesWithTaintTolerationIssue)
	// Add condition about ignored nodes
	sort.Strings(ignoredNodes)
	np.updateIgnoredNodesCondition(s, ignoredNodes)

	return selectedNodes
}

// updateTaintToleranceCondition updates the taint tolerance condition on the skyhook
func (np *NodePicker) updateTaintToleranceCondition(s SkyhookNodes, nodesWithTaintTolerationIssue []string) {
	if len(nodesWithTaintTolerationIssue) > 0 {
		message := fmt.Sprintf("Node [%s] has taints that are not tolerable. Skipping.", strings.Join(nodesWithTaintTolerationIssue, ", "))
		if len(nodesWithTaintTolerationIssue) > wrapper.ReadyConditionNodeListLimit {
			np.logger.Info("Condition message truncated for nodes with taint toleration issues", "skyhook", s.GetSkyhook().Name, "nodes", nodesWithTaintTolerationIssue)
			message = fmt.Sprintf("%d nodes have taints that are not tolerable. Skipping.", len(nodesWithTaintTolerationIssue))
		}

		wrapper.AddSkyhookConditionWithLegacy(s.GetSkyhook(), metav1.Condition{
			Type:               wrapper.SkyhookConditionTaintNotTolerable,
			Status:             metav1.ConditionTrue,
			Reason:             "TaintNotTolerable",
			Message:            message,
			ObservedGeneration: s.GetSkyhook().Generation,
			LastTransitionTime: metav1.Now(),
		})
	} else {
		wrapper.AddSkyhookConditionWithLegacy(s.GetSkyhook(), metav1.Condition{
			Type:               wrapper.SkyhookConditionTaintNotTolerable,
			Status:             metav1.ConditionFalse,
			Reason:             "TaintNotTolerable",
			Message:            "All nodes have tolerable taints.",
			ObservedGeneration: s.GetSkyhook().Generation,
			LastTransitionTime: metav1.Now(),
		})
	}
}

// updateIgnoredNodesCondition updates the ignored nodes condition on the skyhook
func (np *NodePicker) updateIgnoredNodesCondition(s SkyhookNodes, ignoredNodes []string) {
	if len(ignoredNodes) > 0 {
		message := fmt.Sprintf("Node [%s] has ignore label set. Skipping.", strings.Join(ignoredNodes, ", "))
		if len(ignoredNodes) > wrapper.ReadyConditionNodeListLimit {
			np.logger.Info("Condition message truncated for ignored nodes", "skyhook", s.GetSkyhook().Name, "nodes", ignoredNodes)
			message = fmt.Sprintf("%d nodes have ignore label set. Skipping.", len(ignoredNodes))
		}

		wrapper.AddSkyhookConditionWithLegacy(s.GetSkyhook(), metav1.Condition{
			Type:               wrapper.SkyhookConditionNodesIgnored,
			Status:             metav1.ConditionTrue,
			Reason:             "NodesIgnored",
			Message:            message,
			ObservedGeneration: s.GetSkyhook().Generation,
			LastTransitionTime: metav1.Now(),
		})
	} else {
		wrapper.AddSkyhookConditionWithLegacy(s.GetSkyhook(), metav1.Condition{
			Type:               wrapper.SkyhookConditionNodesIgnored,
			Status:             metav1.ConditionFalse,
			Reason:             "NodesIgnored",
			Message:            "No nodes have ignore label set.",
			ObservedGeneration: s.GetSkyhook().Generation,
			LastTransitionTime: metav1.Now(),
		})
	}
}

// for node/package source of true, its on the node (we true to reflect this on the skyhook status)
// for SCR true, we need to look at all nodes and compare state to current SCR. This should be reflected in the SCR too.

// IntrospectSkyhook checks the current state of nodes, and SCR if they are in a bad mix, update to be correct
func IntrospectSkyhook(skyhook SkyhookNodes, allSkyhooks []SkyhookNodes, logger logr.Logger) bool {
	change := false

	scrStatus := skyhook.Status()
	collectNodeStatus := skyhook.CollectNodeStatus()

	// Check if deployment policy is missing - this should block the skyhook
	hasMissingPolicy := wrapper.HasTrueSkyhookCondition(skyhook.GetSkyhook(), wrapper.SkyhookConditionDeploymentPolicyNotFound)

	// override the node status if the skyhook is in a skyhook controlled state. (e.g. disabled, paused, blocked)
	// Note: Waiting status is now handled per-node in IntrospectNode using IsNodeReadyForSkyhook
	if collectNodeStatus != v1alpha1.StatusComplete {
		switch {
		case skyhook.IsDisabled():
			collectNodeStatus = v1alpha1.StatusDisabled

		case skyhook.IsPaused():
			collectNodeStatus = v1alpha1.StatusPaused

		case hasMissingPolicy:
			collectNodeStatus = v1alpha1.StatusBlocked

		default:
			// Check if any higher-priority skyhook with sequencing: all blocks this skyhook globally
			if isBlockedByGlobalPredecessor(skyhook, allSkyhooks) {
				collectNodeStatus = v1alpha1.StatusWaiting
			}
			// Per-node waiting (for sequencing: node predecessors) is handled in IntrospectNode
		}
	} else if hasMissingPolicy {
		// Even if all nodes are complete, if policy is missing, we should still be blocked
		collectNodeStatus = v1alpha1.StatusBlocked
	}

	if scrStatus != collectNodeStatus {
		skyhook.SetStatus(collectNodeStatus)
	}

	for _, node := range skyhook.GetNodes() {
		if IntrospectNode(node, skyhook, allSkyhooks) {
			change = true
		}
	}

	if pruneCompletedNodePriorities(skyhook) {
		change = true
	}

	// Evaluate completed batches for compartments with deployment policies
	if evaluateCompletedBatches(skyhook) {
		change = true
	}

	skyhook.UpdateCondition(logger)
	if skyhook.GetSkyhook().Updated {
		change = true
	}
	return change
}

// evaluateCompletedBatches checks if any compartment batches are complete and evaluates them
func evaluateCompletedBatches(skyhook SkyhookNodes) bool {
	compartments := skyhook.GetCompartments()
	if len(compartments) == 0 {
		return false // No compartments to evaluate
	}

	// Skip batch evaluation when skyhook is Complete - this prevents overwriting
	// the batch state that was just reset by SetStatus transitioning to Complete
	if skyhook.Status() == v1alpha1.StatusComplete {
		return false
	}

	changed := false
	for _, compartment := range compartments {
		if isComplete, successCount, failureCount := compartment.EvaluateCurrentBatch(); isComplete {
			batchSize := successCount + failureCount

			// Count blocked nodes to determine if we should skip batch evaluation
			blockedCount := 0
			for _, node := range compartment.GetNodes() {
				if node.Status() == v1alpha1.StatusBlocked {
					blockedCount++
				}
			}

			// If batchSize is 0 but batch is complete, check if all nodes are blocked
			// If all nodes are blocked, don't advance the batch - wait for them to become unblocked
			// Blocked nodes are not failures, they're just temporarily unable to proceed
			if batchSize == 0 {
				// If all nodes in the compartment are blocked, skip batch evaluation
				// The batch will be re-evaluated when nodes become unblocked
				if blockedCount > 0 && blockedCount == len(compartment.GetNodes()) {
					continue // Skip this compartment - all nodes blocked, wait for them to unblock
				}
				// If some nodes are blocked but not all, use blocked count as batch size
				// This handles mixed batches (some blocked, some completed/failed)
				if blockedCount > 0 {
					batchSize = blockedCount
				} else if compartment.GetBatchState().LastBatchSize > 0 {
					batchSize = compartment.GetBatchState().LastBatchSize
				}
			}

			// If batch has blocked nodes but no successes/failures, don't treat as failure
			// Blocked nodes should not increment consecutive failures
			// Only evaluate if we have actual progress (successes or failures)
			if batchSize > 0 && successCount == 0 && failureCount == 0 && blockedCount == batchSize {
				// All nodes in batch are blocked - skip evaluation to avoid false failures
				continue
			}

			// Update the compartment's batch state using strategy logic
			compartment.EvaluateAndUpdateBatchState(batchSize, successCount, failureCount)

			// Persist the updated batch state to the skyhook status immediately
			if skyhook.GetSkyhook().Status.CompartmentStatuses == nil {
				skyhook.GetSkyhook().Status.CompartmentStatuses = make(map[string]v1alpha1.CompartmentStatus)
			}
			// Build and persist the compartment status with the updated batch state
			newStatus := buildCompartmentStatus(compartment)
			skyhook.GetSkyhook().Status.CompartmentStatuses[compartment.GetName()] = newStatus

			skyhook.GetSkyhook().Updated = true
			changed = true
		}
	}

	return changed
}

func IntrospectNode(node wrapper.SkyhookNode, skyhook SkyhookNodes, allSkyhooks []SkyhookNodes) bool {
	skyhookStatus := skyhook.Status()

	nodeStatus := node.Status()
	node.SetStatus(nodeStatus)

	// Check if skyhook status should override node status (for disabled, paused)
	// Note: Waiting is now handled per-node below
	if skyhookStatus == v1alpha1.StatusDisabled || skyhookStatus == v1alpha1.StatusPaused {
		if nodeStatus != skyhookStatus {
			node.SetStatus(skyhookStatus)
		}
		return node.Changed()
	}

	// Check per-node priority: if this node is waiting on higher-priority skyhooks
	// Skip when skyhook is already globally waiting (sequencing: all) — nodes inherit via isSkyhookControlledNodeStatus
	if !node.IsComplete() && skyhookStatus != v1alpha1.StatusWaiting && !IsNodeReadyForSkyhook(node.GetNode().Name, skyhook, allSkyhooks) {
		if nodeStatus != v1alpha1.StatusWaiting {
			node.SetStatus(v1alpha1.StatusWaiting)
		}
		return node.Changed()
	}

	// need to move node out of Skyhook controlled status
	if isSkyhookControlledNodeStatus(nodeStatus) {
		if node.IsComplete() {
			node.SetStatus(v1alpha1.StatusComplete)
		} else {
			// In normal operation, all nodes are in at least the default compartment
			// If compartments exist, node is waiting for its batch; otherwise Unknown (error state)
			compartments := skyhook.GetCompartments()
			if len(compartments) > 0 {
				node.SetStatus(v1alpha1.StatusWaiting)
			} else {
				// No compartments exist (error state, e.g., deployment policy missing)
				node.SetStatus(v1alpha1.StatusUnknown)
			}
		}
		return node.Changed()
	}

	// For normal node state transitions
	if nodeStatus != v1alpha1.StatusComplete && node.IsComplete() {
		node.SetStatus(v1alpha1.StatusComplete)
	}

	if nodeStatus == v1alpha1.StatusComplete && !node.IsComplete() {
		node.SetStatus(v1alpha1.StatusUnknown)
	}

	// If node is Unknown and not complete, check if it's waiting for its batch
	// In normal operation, all nodes are in at least the default compartment
	if nodeStatus == v1alpha1.StatusUnknown && !node.IsComplete() {
		compartments := skyhook.GetCompartments()
		if len(compartments) > 0 {
			node.SetStatus(v1alpha1.StatusWaiting)
			return node.Changed()
		}
	}

	return node.Changed()
}

func isSkyhookControlledNodeStatus(status v1alpha1.Status) bool {
	return status == v1alpha1.StatusDisabled ||
		status == v1alpha1.StatusPaused ||
		status == v1alpha1.StatusWaiting
}

func UpdateSkyhookPauseStatus(skyhook SkyhookNodes, logger logr.Logger) bool {
	changed := false
	if skyhook.IsPaused() {
		if skyhook.Status() != v1alpha1.StatusPaused {
			skyhook.SetStatus(v1alpha1.StatusPaused)
			changed = true
		}

		for _, node := range skyhook.GetNodes() {
			if node.Status() != v1alpha1.StatusPaused {
				node.SetStatus(v1alpha1.StatusPaused)
				changed = true
			}
		}

		if skyhook.UpdateCondition(logger) {
			changed = true
		}
	}

	return changed
}

// compartmentStatusEqual compares two CompartmentStatus for equality
func compartmentStatusEqual(a, b v1alpha1.CompartmentStatus) bool {
	if a.Matched != b.Matched || a.Ceiling != b.Ceiling || a.InProgress != b.InProgress ||
		a.Completed != b.Completed || a.ProgressPercent != b.ProgressPercent {
		return false
	}

	// Compare BatchState if present
	if (a.BatchState == nil) != (b.BatchState == nil) {
		return false
	}
	if a.BatchState != nil && b.BatchState != nil {
		return *a.BatchState == *b.BatchState
	}
	return true
}

// buildCompartmentStatus creates a CompartmentStatus for a given compartment
func buildCompartmentStatus(compartment *wrapper.Compartment) v1alpha1.CompartmentStatus {
	matched := len(compartment.GetNodes())
	ceiling := wrapper.CalculateCeiling(compartment.Budget, matched)

	// Count inProgress and completed nodes
	inProgress := 0
	completed := 0
	for _, node := range compartment.GetNodes() {
		if node.Status() == v1alpha1.StatusInProgress {
			inProgress++
		}
		if node.IsComplete() {
			completed++
		}
	}

	// Calculate progress percentage
	progressPercent := 0
	if matched > 0 {
		progressPercent = (completed * 100) / matched
	}

	// Get batch state
	batchState := compartment.GetBatchState()

	// Copy batch state for status
	var batchStateCopy *v1alpha1.BatchProcessingState
	if compartment.Strategy != nil {
		batchStateCopy = &v1alpha1.BatchProcessingState{
			CurrentBatch:        batchState.CurrentBatch,
			ConsecutiveFailures: batchState.ConsecutiveFailures,
			CompletedNodes:      batchState.CompletedNodes,
			FailedNodes:         batchState.FailedNodes,
			ShouldStop:          batchState.ShouldStop,
			LastBatchSize:       batchState.LastBatchSize,
			LastBatchFailed:     batchState.LastBatchFailed,
		}
	}

	return v1alpha1.CompartmentStatus{
		Matched:         matched,
		Ceiling:         ceiling,
		InProgress:      inProgress,
		Completed:       completed,
		ProgressPercent: progressPercent,
		BatchState:      batchStateCopy,
	}
}

// ReportState collects the current state of the skyhook and reports it to the skyhook status for printer columns
func (skyhook *skyhookNodes) ReportState() {
	CleanupRemovedNodes(skyhook)

	nodeCount := len(skyhook.nodes)
	skyhookName := skyhook.GetSkyhook().Name

	// Initialize status and metrics maps
	nodeStatusCounts := make(map[v1alpha1.Status]int, len(v1alpha1.Statuses))
	for _, status := range v1alpha1.Statuses {
		nodeStatusCounts[status] = 0
	}

	packageRestarts := make(map[string]map[string]int32)
	packageStateStageCounts := make(map[string]map[string]map[v1alpha1.State]map[v1alpha1.Stage]int)

	// Collect node and package stats
	for _, node := range skyhook.nodes {
		nodeStatusCounts[node.Status()]++

		for _, _package := range node.GetSkyhook().Spec.Packages {
			packageStatus, found := node.PackageStatus(_package.GetUniqueName())
			if !found {
				continue
			}

			// Nested map initialization
			if packageStateStageCounts[_package.Name] == nil {
				packageStateStageCounts[_package.Name] = make(map[string]map[v1alpha1.State]map[v1alpha1.Stage]int)
			}
			if packageStateStageCounts[_package.Name][_package.Version] == nil {
				packageStateStageCounts[_package.Name][_package.Version] = make(map[v1alpha1.State]map[v1alpha1.Stage]int)
			}
			if packageStateStageCounts[_package.Name][_package.Version][packageStatus.State] == nil {
				packageStateStageCounts[_package.Name][_package.Version][packageStatus.State] = make(map[v1alpha1.Stage]int)
			}
			packageStateStageCounts[_package.Name][_package.Version][packageStatus.State][packageStatus.Stage]++

			if packageRestarts[_package.Name] == nil {
				packageRestarts[_package.Name] = make(map[string]int32)
			}
			packageRestarts[_package.Name][_package.Version] += packageStatus.Restarts
		}
	}

	// Update compartment statuses
	updateCompartmentStatuses(skyhook)

	// Clean up stale compartment statuses
	cleanupStaleCompartmentStatuses(skyhook)

	// Set all metrics
	setAllMetrics(skyhookName, skyhook, nodeStatusCounts, packageStateStageCounts, packageRestarts, nodeCount)

	// Set current count of completed nodes
	completeNodes := fmt.Sprintf("%d/%d", nodeStatusCounts[v1alpha1.StatusComplete], nodeCount)
	if completeNodes != skyhook.skyhook.GetCompleteNodes() {
		skyhook.skyhook.SetCompleteNodes(completeNodes)
		skyhook.skyhook.Updated = true
	}

	// Update nodes in progress count if changed
	inProgress := nodeStatusCounts[v1alpha1.StatusInProgress] + nodeStatusCounts[v1alpha1.StatusErroring]
	if skyhook.skyhook.GetNodesInProgress() != inProgress {
		skyhook.skyhook.SetNodesInProgress(inProgress)
	}

	// Get and set sorted package list
	packageNames := make([]string, 0, len(skyhook.skyhook.Spec.Packages))
	for _, _package := range skyhook.skyhook.Spec.Packages {
		packageNames = append(packageNames, fmt.Sprintf("%s:%s", _package.Name, _package.Version))
	}
	sort.Strings(packageNames)
	packageList := strings.Join(packageNames, ",")
	if packageList != skyhook.skyhook.GetPackageList() {
		skyhook.skyhook.SetPackageList(packageList)
		skyhook.skyhook.Updated = true
	}
}

// Migrate is for tracking versions of the operator. If the version changes, we need to update the state of
// the skyhook and nodes to be valid for the new version. The pattern here is to check the versions if they match a version
// matrix we have 3 places to handle changes. Here and in the skyhook and node wrappers. The mirgrate function is called to compare
// version and then actual work are in the migration files prefixed with zz.migration and the version number.
func (skyhook *skyhookNodes) Migrate(logger logr.Logger) error {

	for _, node := range skyhook.nodes {
		if node.GetVersion() == version.VERSION {
			continue // already up to date
		}
		if err := node.Migrate(logger); err != nil {
			return fmt.Errorf("error migrating node [%s]: %w", node.GetNode().Name, err)
		}
	}

	from := skyhook.skyhook.GetVersion()
	to := version.VERSION

	if from == to {
		return nil
	}

	if err := skyhook.skyhook.Migrate(logger); err != nil {
		return fmt.Errorf("error migrating skyhook [%s]: %w", skyhook.skyhook.Name, err)
	}

	if from == "" { // before this was a thing v0.4.0 and before
		err := migrateSkyhookNodesTo_0_5_0(skyhook, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func (skyhook *skyhookNodes) GetCompartments() map[string]*wrapper.Compartment {
	return skyhook.compartments
}

func (skyhook *skyhookNodes) AddCompartment(name string, compartment *wrapper.Compartment) {
	skyhook.compartments[name] = compartment
}

func (skyhook *skyhookNodes) AddCompartmentNode(name string, node wrapper.SkyhookNode) error {
	compartment, ok := skyhook.compartments[name]
	if !ok {
		return fmt.Errorf("compartment %q not found for skyhook %q - missing deployment policy", name, skyhook.skyhook.Name)
	}
	compartment.AddNode(node)
	return nil
}

// compartmentMatch represents a compartment that matches a node
type compartmentMatch struct {
	name         string
	strategyType v1alpha1.StrategyType
	capacity     int
}

// countMatchingNodes counts how many nodes from allNodes match the given selector
func (skyhook *skyhookNodes) countMatchingNodes(selector metav1.LabelSelector) (int, error) {
	labelSelector, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, node := range skyhook.nodes {
		if labelSelector.Matches(labels.Set(node.GetNode().Labels)) {
			count++
		}
	}
	return count, nil
}

// AssignNodeToCompartment assigns a single node to the appropriate compartment using overlap resolution.
// When a node matches multiple compartments, it resolves using:
// 1. Strategy safety order: Fixed is safer than Linear, which is safer than Exponential
// 2. Tie-break on same strategy: Choose compartment with smaller effective ceiling (window)
// 3. Final tie-break: Lexicographically by compartment name for determinism
// Assignments are recalculated fresh on every reconcile based on current cluster state.
func (skyhook *skyhookNodes) AssignNodeToCompartment(node wrapper.SkyhookNode) (string, error) {
	nodeLabels := labels.Set(node.GetNode().Labels)

	matches := []compartmentMatch{}

	// Collect all matching compartments (excluding default)
	for _, compartment := range skyhook.compartments {
		// Skip the default compartment - it's a fallback
		if compartment.Name == v1alpha1.DefaultCompartmentName {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(&compartment.Selector)
		if err != nil {
			return "", fmt.Errorf("invalid selector for compartment %s: %w", compartment.Name, err)
		}

		if selector.Matches(nodeLabels) {
			// Count how many nodes in total match this compartment's selector
			matchedCount, err := skyhook.countMatchingNodes(compartment.Selector)
			if err != nil {
				return "", fmt.Errorf("error counting matching nodes for compartment %s: %w", compartment.Name, err)
			}

			// Ensure at least 1 node for capacity calculation
			if matchedCount == 0 {
				matchedCount = 1
			}

			stratType := wrapper.GetStrategyType(compartment.Strategy)
			capacity := wrapper.CalculateCeiling(compartment.Budget, matchedCount)

			matches = append(matches, compartmentMatch{
				name:         compartment.Name,
				strategyType: stratType,
				capacity:     capacity,
			})
		}
	}

	// No matches - assign to default
	if len(matches) == 0 {
		return v1alpha1.DefaultCompartmentName, nil
	}

	// Single match - return it
	if len(matches) == 1 {
		return matches[0].name, nil
	}

	// Multiple matches - apply overlap resolution
	// Sort matches using the safety heuristic
	sort.Slice(matches, func(i, j int) bool {
		// 1. Strategy safety order: Fixed > Linear > Exponential
		if matches[i].strategyType != matches[j].strategyType {
			return wrapper.StrategyIsSafer(matches[i].strategyType, matches[j].strategyType)
		}

		// 2. Tie-break on same strategy: smaller window (capacity)
		if matches[i].capacity != matches[j].capacity {
			return matches[i].capacity < matches[j].capacity
		}

		// 3. Final tie-break: lexicographically by name for determinism
		return matches[i].name < matches[j].name
	})

	// Return the safest compartment
	return matches[0].name, nil
}

// updateCompartmentStatuses updates compartment statuses for all current compartments
func updateCompartmentStatuses(skyhook *skyhookNodes) {
	if len(skyhook.compartments) == 0 {
		return
	}
	if skyhook.skyhook.Status.CompartmentStatuses == nil {
		skyhook.skyhook.Status.CompartmentStatuses = make(map[string]v1alpha1.CompartmentStatus)
	}

	for name, compartment := range skyhook.compartments {
		newStatus := buildCompartmentStatus(compartment)
		if existing, ok := skyhook.skyhook.Status.CompartmentStatuses[name]; !ok || !compartmentStatusEqual(existing, newStatus) {
			skyhook.skyhook.Status.CompartmentStatuses[name] = newStatus
			skyhook.skyhook.Updated = true
		}
	}
}

// cleanupStaleCompartmentStatuses removes compartment statuses that are no longer in the policy
func cleanupStaleCompartmentStatuses(skyhook *skyhookNodes) {
	if skyhook.skyhook.Status.CompartmentStatuses == nil {
		return
	}
	for compartmentName := range skyhook.skyhook.Status.CompartmentStatuses {
		if _, exists := skyhook.compartments[compartmentName]; !exists {
			delete(skyhook.skyhook.Status.CompartmentStatuses, compartmentName)
			skyhook.skyhook.Updated = true
		}
	}
}

// setAllMetrics sets all metrics for the skyhook
func setAllMetrics(
	skyhookName string,
	skyhook *skyhookNodes,
	nodeStatusCounts map[v1alpha1.Status]int,
	packageStateStageCounts map[string]map[string]map[v1alpha1.State]map[v1alpha1.Stage]int,
	packageRestarts map[string]map[string]int32,
	nodeCount int,
) {
	// reset metrics to zero
	ResetSkyhookMetricsToZero(skyhook)

	// Set skyhook status metrics
	SetSkyhookStatusMetrics(skyhookName, skyhook.Status(), true)

	// Set target count and node status metrics
	SetNodeTargetCountMetrics(skyhookName, float64(nodeCount))
	for status, count := range nodeStatusCounts {
		SetNodeStatusMetrics(skyhookName, status, float64(count))
	}

	// Set package state and stage metrics
	for _package, versions := range packageStateStageCounts {
		for version, states := range versions {
			for state, stages := range states {
				for stage, count := range stages {
					SetPackageStateMetrics(skyhookName, _package, version, state, float64(count))
					SetPackageStageMetrics(skyhookName, _package, version, stage, float64(count))
				}
			}
		}
	}

	// Set package restarts metrics
	for _package, versions := range packageRestarts {
		for version, restarts := range versions {
			SetPackageRestartsMetrics(skyhookName, _package, version, restarts)
		}
	}

	// Set rollout metrics for each compartment
	if len(skyhook.compartments) > 0 {
		policyName := skyhook.GetSkyhook().Spec.DeploymentPolicy
		if policyName == "" {
			policyName = LegacyPolicyName
		}

		for name, compartment := range skyhook.compartments {
			if status, ok := skyhook.skyhook.Status.CompartmentStatuses[name]; ok {
				strategy := getStrategyType(compartment)
				SetRolloutMetrics(skyhookName, policyName, name, strategy, status)
			}
		}
	}
}

// cleanupNodeMap removes nodes from the given map that no longer exist in currentNodes
// Returns false if nodeMap is nil, otherwise returns true if any nodes were removed
func cleanupNodeMap[T any](nodeMap map[string]T, currentNodes map[string]struct{}) bool {
	if nodeMap == nil {
		return false
	}

	change := false
	for nodeName := range nodeMap {
		if _, ok := currentNodes[nodeName]; !ok {
			delete(nodeMap, nodeName)
			change = true
		}
	}
	return change
}

// CleanupRemovedNodes removes nodes from the Skyhook status that no longer exist in the cluster
// or no longer match the node selector. This ensures that only nodes that exist in the cluster
// are tracked in the status section of the Custom Resource.
func CleanupRemovedNodes(skyhook SkyhookNodes) {
	// Get all current node names from the cluster using struct{} for O(1) lookup
	currentNodeNames := make(map[string]struct{})
	for _, node := range skyhook.GetNodes() {
		currentNodeNames[node.GetNode().Name] = struct{}{}
	}

	status := skyhook.GetSkyhook().Status

	// NodePriority needs special handling to track offset
	for name := range status.NodePriority {
		if _, ok := currentNodeNames[name]; !ok {
			skyhook.GetSkyhook().RemoveNodePriority(name)
		}
	}

	// Check and remove nodes from remaining status maps
	change := cleanupNodeMap(status.NodeState, currentNodeNames)
	change = cleanupNodeMap(status.NodeStatus, currentNodeNames) || change
	change = cleanupNodeMap(status.NodeBootIds, currentNodeNames) || change

	// Only set Updated flag if there were changes
	if change {
		skyhook.GetSkyhook().Updated = true
	}
}
