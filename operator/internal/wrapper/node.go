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

package wrapper

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/graph"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// There are two interfaces: one for code that needs both a Skyhook and a Node,
// and one for code that only needs a Node (e.g. to avoid extra API calls).

// SkyhookNode wraps a Node with its associated Skyhook. Use it when you need
// full Skyhook spec and graph to drive sequencing, status, and conditions.
type SkyhookNode interface {
	SkyhookNodeOnly

	// GetSkyhook returns the Skyhook associated with this node, or nil if only a name was set.
	GetSkyhook() *Skyhook
	// GetComplete returns the list of package names that are complete on this node.
	GetComplete() []string
	// SetStatus updates the node's Skyhook status in annotations/labels and on the Skyhook; uncordons if Complete.
	SetStatus(status v1alpha1.Status)
	// IsComplete reports whether all packages for this Skyhook are complete on this node.
	IsComplete() bool
	// ProgressSkipped marks progress as skipped for sequencing (e.g. when dependencies are not run).
	ProgressSkipped()
	// IsPackageComplete reports whether the given package is complete on this node (considering interrupts and updates).
	IsPackageComplete(_package v1alpha1.Package) bool
	// RunNext returns the next package(s) that should run according to the dependency graph and current completion.
	RunNext() ([]*v1alpha1.Package, error)
	// NextStage returns the next stage for the given package given its current state and config.
	NextStage(_package *v1alpha1.Package) *v1alpha1.Stage
	// HasInterrupt reports whether the package has an interrupt (e.g. wait-for-input) that blocks progression.
	HasInterrupt(_package v1alpha1.Package) bool
	// UpdateCondition refreshes Skyhook-related node conditions (NotReady and Erroring) from current package state.
	UpdateCondition()
	// HasSkyhookAnnotations reports whether the node has any Skyhook operator annotations.
	HasSkyhookAnnotations() bool
}

// SkyhookNodeOnly wraps a Node with only a Skyhook name. Use it when you need
// node-level operations (state, taints, cordon, version) without loading the
// full Skyhook; helps reduce API calls and avoids stubbing full Skyhooks.
type SkyhookNodeOnly interface {
	// Status returns the current Skyhook status for this node from annotations, or StatusUnknown if unset.
	Status() v1alpha1.Status
	// SetStatus updates the node's Skyhook status in annotations/labels and on the Skyhook; uncordons if Complete.
	SetStatus(status v1alpha1.Status)
	// PackageStatus returns the status for the named package if present in node state.
	PackageStatus(name string) (*v1alpha1.PackageStatus, bool)
	// SetVersion writes the current operator version into the node's annotations for this Skyhook.
	SetVersion()
	// GetVersion returns the operator version stored in the node's annotations for this Skyhook.
	GetVersion() string
	// Migrate updates stored node state/annotations to the current schema when the operator version changes.
	Migrate(logger logr.Logger) error
	// State returns the persisted NodeState for this node (from memory or annotations).
	State() (v1alpha1.NodeState, error)
	// SetState persists the given NodeState to the node's annotations and in-memory state.
	SetState(state v1alpha1.NodeState) error
	// RemoveState removes persisted state for the given package ref and updates annotations.
	RemoveState(_package v1alpha1.PackageRef) error
	// Upsert creates or updates state for a package (image, state, stage, restarts, containerSHA) and persists it.
	Upsert(_package v1alpha1.PackageRef, image string, state v1alpha1.State, stage v1alpha1.Stage, restarts int32, containerSHA string) error
	// GetNode returns the underlying Kubernetes Node.
	GetNode() *corev1.Node
	// Taint adds a NoSchedule taint with the given key and the Skyhook name as value.
	Taint(key string)
	// RemoveTaint removes the taint with the given key from the node.
	RemoveTaint(key string)
	// Cordon marks the node unschedulable and records the cordon in annotations for this Skyhook.
	Cordon()
	// Uncordon marks the node schedulable and removes this Skyhook's cordon annotation if present.
	Uncordon()
	// Reset clears Skyhook-related state and annotations so the node can be reconfigured from scratch.
	Reset()
	// Changed reports whether the node has in-memory changes that need to be written back to the API.
	Changed() bool
}

var _ SkyhookNode = &skyhookNode{}

// NewSkyhookNodeOnly most of use cases for the wrapper just needs name, so this stub is for making helpers for those use cases,
// should help reduce calls to api, and not leak stubbed skyhooks with just name set
func NewSkyhookNodeOnly(node *corev1.Node, skyhookName string) (SkyhookNodeOnly, error) {
	ret := &skyhookNode{
		Node:        node,
		skyhookName: skyhookName,
	}
	state, err := ret.State()
	if err != nil {
		return nil, fmt.Errorf("error creating skyhookNode: %w", err)
	}
	ret.nodeState = state
	return ret, nil
}

// Convert upgrades a SkyhookNodeOnly to a full SkyhookNode when a Skyhook object is available.
func Convert(node SkyhookNodeOnly, skyhook *v1alpha1.Skyhook) (SkyhookNode, error) {
	ret := node.(*skyhookNode)
	ret.skyhook = &Skyhook{Skyhook: skyhook}

	graph, err := skyhook.Spec.BuildGraph()
	if err != nil {
		return nil, err
	}

	ret.graph = graph

	return ret, nil
}

// NewSkyhookNode creates a full SkyhookNode from a Node and a Skyhook (node + graph + name).
func NewSkyhookNode(node *corev1.Node, skyhook *v1alpha1.Skyhook) (SkyhookNode, error) {

	t, err := NewSkyhookNodeOnly(node, skyhook.Name)
	if err != nil {
		return nil, err
	}

	return Convert(t, skyhook)
}

type skyhookNode struct {
	*corev1.Node
	skyhookName string
	skyhook     *Skyhook
	nodeState   v1alpha1.NodeState
	graph       graph.DependencyGraph[*v1alpha1.Package]
	updated     bool
}

// GetSkyhook returns the Skyhook associated with this node, or nil if only a name was set.
func (node *skyhookNode) GetSkyhook() *Skyhook {
	return node.skyhook
}

// GetNode returns the underlying Kubernetes Node.
func (node *skyhookNode) GetNode() *corev1.Node {
	return node.Node
}

// SetStatus updates the node's Skyhook status in annotations/labels and on the Skyhook status; also uncordons if status is Complete.
func (node *skyhookNode) SetStatus(status v1alpha1.Status) {

	s, ok := node.Annotations[fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if !ok || s != string(status) {
		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		node.updated = true
		node.Annotations[fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)] = string(status)
		node.Labels[fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)] = string(status)
	}

	if status == v1alpha1.StatusComplete {
		node.Uncordon()
	}

	if node.skyhook != nil {
		node.skyhook.SetNodeStatus(node.Node.Name, status)
		node.skyhook.SetNodeState(node.Node.Name, node.nodeState)
	}
}

// Status returns the current Skyhook status for this node from annotations, or StatusUnknown if unset.
func (node *skyhookNode) Status() v1alpha1.Status {
	status, ok := node.Annotations[fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if !ok {
		return v1alpha1.StatusUnknown
	}
	return v1alpha1.GetStatus(status)
}

// State returns the persisted NodeState for this node (from memory or annotations).
func (node *skyhookNode) State() (v1alpha1.NodeState, error) {

	if node.nodeState != nil {
		return node.nodeState, nil
	}

	if node == nil {
		return nil, nil
	}
	s, ok := node.Annotations[fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if !ok {
		return nil, nil
	}

	ret := v1alpha1.NodeState{}
	err := json.Unmarshal([]byte(s), &ret)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling node state: %w", err)
	}

	return ret, nil
}

// PackageStatus returns the status for the named package if present in node state.
func (node *skyhookNode) PackageStatus(name string) (*v1alpha1.PackageStatus, bool) {
	packageStatus := node.nodeState.Get(name)
	if packageStatus != nil {
		return packageStatus, true
	}

	return nil, false
}

// SetVersion writes the current operator version into the node's annotations for this Skyhook.
func (node *skyhookNode) SetVersion() {

	current := node.GetVersion()
	if current == version.VERSION { // if has not changed, do nothing and not set updated
		return
	}

	if version.VERSION == "" { // was not compiled with version, so do nothing
		return
	}

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	node.Annotations[fmt.Sprintf("%s/version_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)] = version.VERSION
	node.updated = true
}

// GetVersion returns the operator version stored in the node's annotations for this Skyhook.
func (node *skyhookNode) GetVersion() string {
	version, ok := node.Annotations[fmt.Sprintf("%s/version_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if !ok {
		return ""
	}
	return version
}

// Migrate updates stored node state/annotations to the current schema when the operator version changes.
func (node *skyhookNode) Migrate(logger logr.Logger) error {

	from := node.GetVersion()
	to := version.VERSION

	if from == to { // already migrated
		return nil
	}

	mm := version.MajorMinor(from)
	switch mm {
	// because there was a bug in versioning, this same migration needs to be run for more then just the v0.5 releases
	// empty string is for before versioning was added
	case "", "v0.5", "v0.6", "v0.7":
		err := migrateNodeTo_0_5_0(node, logger)
		if err != nil {
			return err
		}
		node.SetVersion()
		return nil
	}

	return nil
}

// SetState persists the given NodeState to the node's annotations and in-memory state.
func (node *skyhookNode) SetState(state v1alpha1.NodeState) error {
	if node == nil || state == nil {
		return nil
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("error marshalling node state: %w", err)
	}

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	s, ok := node.Annotations[fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if !ok || s != string(data) {
		node.Annotations[fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)] = string(data)
		node.nodeState = state
		node.updated = true
	}

	return nil
}

// RemoveState removes persisted state for the given package ref and updates annotations.
func (node *skyhookNode) RemoveState(_package v1alpha1.PackageRef) error {
	changed := node.nodeState.RemoveState(_package)
	if changed {
		return node.SetState(node.nodeState)
	}

	return nil
}

// Upsert creates or updates state for a package (image, state, stage, restarts, containerSHA) and persists it.
func (node *skyhookNode) Upsert(_package v1alpha1.PackageRef, image string, state v1alpha1.State, stage v1alpha1.Stage, restarts int32, containerSHA string) error {
	changed := node.nodeState.Upsert(_package, image, state, stage, restarts, containerSHA)
	if changed {
		if node.skyhook != nil {
			node.skyhook.Updated = true
		}

		return node.SetState(node.nodeState)
	}
	return nil
}

// IsPackageComplete reports whether the given package is complete on this node (considering interrupts and updates).
func (node *skyhookNode) IsPackageComplete(_package v1alpha1.Package) bool {
	return node.nodeState.IsPackageComplete(_package, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates())
}

// IsComplete reports whether all packages for this Skyhook are complete on this node.
func (node *skyhookNode) IsComplete() bool {
	return node.nodeState.IsComplete(node.skyhook.Spec.Packages, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates())
}

// GetComplete returns the list of package names that are complete on this node.
func (node *skyhookNode) GetComplete() []string {
	return node.nodeState.GetComplete(node.skyhook.Spec.Packages, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates())
}

// ProgressSkipped marks progress as skipped for sequencing (e.g. when dependencies are not run).
func (node *skyhookNode) ProgressSkipped() {
	if node.nodeState.ProgressSkipped(node.skyhook.Spec.Packages, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates()) {
		node.skyhook.Updated = true
		node.updated = true
	}
}

// RunNext returns the next package(s) that should run according to the dependency graph and current completion.
func (node *skyhookNode) RunNext() ([]*v1alpha1.Package, error) {
	complete := node.GetComplete()

	// Get next available nodes based on completed dependencies
	next, err := node.graph.Next(complete...)
	if err != nil {
		return nil, err
	}

	toRun := node.graph.Get(next...)

	// Sort for deterministic ordering
	sort.Slice(toRun, func(i, j int) bool {
		return toRun[i].Name < toRun[j].Name
	})

	return toRun, nil
}

// NextStage returns the next stage for the given package given its current state and config.
func (node *skyhookNode) NextStage(_package *v1alpha1.Package) *v1alpha1.Stage {
	return node.nodeState.NextStage(_package, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates())
}

// Changed reports whether the node has in-memory changes that need to be written back to the API.
func (node *skyhookNode) Changed() bool {
	return node.updated
}

// HasInterrupt reports whether the package has an interrupt (e.g. wait-for-input) that blocks progression.
func (node *skyhookNode) HasInterrupt(_package v1alpha1.Package) bool {
	return node.nodeState.HasInterrupt(_package, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates())
}

// Taint adds a NoSchedule taint with the given key and the Skyhook name as value.
func (node *skyhookNode) Taint(key string) {

	// dont add it if it exists already, dups will error
	for _, t := range node.Spec.Taints {
		if t.Key == key {
			return
		}
	}

	if node.Spec.Taints == nil {
		node.Spec.Taints = make([]corev1.Taint, 0)
	}

	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    key,
		Value:  node.GetSkyhook().Name,
		Effect: corev1.TaintEffectNoSchedule,
	})
	node.updated = true
}

// RemoveTaint removes the taint with the given key from the node.
func (node *skyhookNode) RemoveTaint(key string) {

	if len(node.Spec.Taints) == 0 {
		return
	}

	temp := node.Spec.Taints[:0]
	for _, t := range node.Spec.Taints {
		if t.Key != key {
			temp = append(temp, t)
		}
	}

	if len(temp) < len(node.Spec.Taints) {
		node.Spec.Taints = temp
		node.updated = true
	}
}

// HasSkyhookAnnotations returns true if the node has any annotation with the
// skyhook.nvidia.com/ prefix, indicating it has been previously touched by the Skyhook operator.
func (node *skyhookNode) HasSkyhookAnnotations() bool {
	for key := range node.Annotations {
		if strings.HasPrefix(key, v1alpha1.METADATA_PREFIX+"/") {
			return true
		}
	}
	return false
}

// Cordon marks the node unschedulable and records the cordon in annotations for this Skyhook.
func (node *skyhookNode) Cordon() {
	_, ok := node.Annotations[fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if !node.Spec.Unschedulable || !ok {
		node.Spec.Unschedulable = true
		node.Annotations[fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)] = "true"
		node.updated = true
	}
}

// Uncordon marks the node schedulable and removes this Skyhook's cordon annotation if present.
func (node *skyhookNode) Uncordon() {

	// if we hold a cordon remove it, also we dont want to remove a cordon if we dont have one...
	_, ok := node.Annotations[fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)]
	if ok {
		node.Spec.Unschedulable = false
		delete(node.Annotations, fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, node.skyhookName))
		node.updated = true
	}
}

// Reset clears Skyhook-related state and annotations so the node can be reconfigured from scratch.
func (node *skyhookNode) Reset() {

	delete(node.skyhook.Status.NodeState, node.Name)
	delete(node.skyhook.Status.NodeStatus, node.Name)
	node.skyhook.Status.Status = v1alpha1.StatusUnknown
	node.skyhook.Updated = true

	delete(node.Annotations, fmt.Sprintf("%s/cordon_", v1alpha1.METADATA_PREFIX))
	delete(node.Annotations, fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, node.skyhook.Name))
	delete(node.Annotations, fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhook.Name))

	delete(node.Labels, fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhook.Name))
	node.updated = true
}

// UpdateCondition refreshes Skyhook-related node conditions (NotReady and Erroring) from current package state.
func (node *skyhookNode) UpdateCondition() {
	readyReason, errorReason := "Incomplete", "Not Erroring"
	errorCondFound, condFound := false, false

	if node.Node.Status.Conditions == nil {
		node.Node.Status.Conditions = make([]corev1.NodeCondition, 0)
	}

	errorStatus, condStatus := corev1.ConditionFalse, corev1.ConditionTrue
	if node.IsComplete() {
		readyReason = "Complete"
		condStatus = corev1.ConditionFalse
	}

	for _, packageStatus := range node.nodeState {
		switch packageStatus.State {
		case v1alpha1.StateErroring, v1alpha1.StateUnknown:
			errorReason = "Package(s) Erroring or Unknown"
			errorStatus = corev1.ConditionTrue
		}
	}

	cond := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(fmt.Sprintf("%s/%s/NotReady", v1alpha1.METADATA_PREFIX, node.skyhookName)),
		Status:             condStatus,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             readyReason,
		Message:            fmt.Sprintf("Skyhook %s Ready", node.skyhookName),
	}

	errorCond := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(fmt.Sprintf("%s/%s/Erroring", v1alpha1.METADATA_PREFIX, node.skyhookName)),
		Status:             errorStatus,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             errorReason,
		Message:            fmt.Sprintf("Package Erroring or Unknown for %s", node.skyhookName),
	}

	for i, condition := range node.Node.Status.Conditions {
		switch condition.Type {
		case errorCond.Type:
			errorCondFound = true
			if condition.Reason != errorCond.Reason && condition.Message == errorCond.Message {
				node.Node.Status.Conditions[i] = errorCond // update it with the new condition
				node.updated = true
			}
		case cond.Type:
			condFound = true
			if condition.Reason != cond.Reason && condition.Message == cond.Message {
				node.Node.Status.Conditions[i] = cond // update it with the new condition
				node.updated = true
			}
		}
	}

	if !errorCondFound {
		node.Node.Status.Conditions = append([]corev1.NodeCondition{errorCond}, node.Node.Status.Conditions...)
		node.updated = true
	}
	if !condFound {
		node.Node.Status.Conditions = append([]corev1.NodeCondition{cond}, node.Node.Status.Conditions...)
		node.updated = true
	}
}
