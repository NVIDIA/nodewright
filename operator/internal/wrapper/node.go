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
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/drain"
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
	// ProgressSkipped promotes any package skipped during interrupt sequencing to complete and persists it.
	ProgressSkipped() error
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
	// CleanupSCRMetadata removes all operator-managed annotations, labels, and node conditions for this Skyhook.
	CleanupSCRMetadata()
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
	// PruneLegacyMetadata removes any remaining legacy skyhook.nvidia.com-prefixed node
	// metadata (annotations, labels, conditions) after the rollback window elapses, and
	// marks the node changed if it removed anything. MIGRATION-SHIM: remove with the
	// legacy skyhook.nvidia.com group.
	PruneLegacyMetadata() bool
	// State returns the persisted NodeState for this node (from memory or annotations).
	State() (v1alpha1.NodeState, error)
	ReloadState() error
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
	// Cordon marks the node unschedulable and records the cordon in annotations for this
	// Skyhook. It reports whether this call changed the node, which means the cordon has
	// not reached the API server yet.
	Cordon() bool
	// StartDrain records when draining started for this Skyhook on this node.
	StartDrain(startedAt metav1.Time)
	// DrainStartedAt returns when draining started for this Skyhook on this node.
	DrainStartedAt() (*metav1.Time, error)
	// ClearDrainStart removes the drain start marker for this Skyhook on this node.
	ClearDrainStart()
	// SetDrainBlocked persists this pass's drain-blocker findings for this Skyhook on
	// this node, replacing any previously recorded findings. Nil or empty clears it.
	SetDrainBlocked(blocked []drain.BlockedPod) error
	// DrainBlocked returns the drain-blocker findings last persisted for this Skyhook on
	// this node.
	DrainBlocked() ([]drain.BlockedPod, error)
	// Uncordon marks the node schedulable and removes this Skyhook's cordon annotation if present.
	Uncordon()
	// Reset clears Skyhook-related state and annotations so the node can be reconfigured from scratch.
	Reset()
	// Changed reports whether the node has in-memory changes that need to be written back to the API.
	Changed() bool
}

var _ SkyhookNode = &skyhookNode{}

const (
	cordonAnnotationPrefix = v1alpha1.METADATA_PREFIX + "/cordon_"

	// The node-condition types UpdateCondition writes, as the trailing segment of
	// "<prefix>/<skyhookName>/<type>". Named because the 0.18.0 migration shim has to
	// recognise exactly this set when deciding which conditions are the operator's to
	// migrate; a new type added here without updating that shim would silently stop
	// being carried across the rename.
	conditionTypeNotReady = "NotReady"
	conditionTypeErroring = "Erroring"
	cordonAnnotationValue = "true"
)

// NewSkyhookNodeOnly most of use cases for the wrapper just needs name, so this stub is for making helpers for those use cases,
// should help reduce calls to api, and not leak stubbed skyhooks with just name set.
//
// A parse failure on the nodeState annotation (malformed JSON) does NOT abort
// construction: the wrapper is returned with nodeState left uncached so
// subsequent State() calls re-encounter the error. Aborting here would dead-
// lock the reconciler (BuildState → main Reconcile return with error → requeue,
// forever) and prevent the controller from ever reaching
// UpdateUninstallConditions, which is where the failure is supposed to
// surface as a user-visible UninstallFailed/NodeStateUnreadable condition.
func NewSkyhookNodeOnly(node *corev1.Node, skyhookName string) (SkyhookNodeOnly, error) {
	ret := &skyhookNode{
		Node:        node,
		skyhookName: skyhookName,
	}
	state, err := ret.State()
	if err != nil {
		return ret, nil
	}
	ret.nodeState = state
	return ret, nil
}

// Convert upgrades a SkyhookNodeOnly to a full SkyhookNode when a Skyhook object is available.
func Convert(node SkyhookNodeOnly, skyhook *v1alpha1.NodeWright) (SkyhookNode, error) {
	ret := node.(*skyhookNode)
	ret.skyhook = &Skyhook{NodeWright: skyhook}

	graph, err := skyhook.Spec.BuildGraph()
	if err != nil {
		return nil, err
	}

	ret.graph = graph

	return ret, nil
}

// NewSkyhookNode creates a full SkyhookNode from a Node and a Skyhook (node + graph + name).
func NewSkyhookNode(node *corev1.Node, skyhook *v1alpha1.NodeWright) (SkyhookNode, error) {

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

func (node *skyhookNode) drainStartAnnotationKey() string {
	return fmt.Sprintf("%s/drainStart_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)
}

func (node *skyhookNode) drainBlockedAnnotationKey() string {
	return fmt.Sprintf("%s/drainBlocked_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)
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

// ReloadState re-parses the node-state annotation into the cache. Callers use it after replacing
// the underlying Node with one the apiserver returned, so the wrapper answers from what actually
// landed rather than from the value the caller computed.
//
// It re-seeds rather than nils the cache, which is load-bearing and was got wrong once: IsComplete,
// NextStage, GetComplete and PackageStatus read node.nodeState DIRECTLY rather than through
// State(), so a nil cache reads as "no package has any state". A node that just completed then
// reports incomplete, its MarkComplete event never fires, and the rollout stalls. Upsert is worse
// still — NodeState.Upsert allocates a fresh map over a nil one, so the next write would drop every
// other package's entry.
func (node *skyhookNode) ReloadState() error {
	node.nodeState = nil // force State() past its cache check
	state, err := node.State()
	if err != nil {
		return fmt.Errorf("reloading node state for %s: %w", node.Name, err)
	}
	node.nodeState = state
	return nil
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
	if from == "" {
		// A node with no new-prefix version annotation is either brand new or was
		// last written by the pre-rename operator (skyhook.nvidia.com/* keys). Re-key
		// any legacy node metadata to the nodewright prefix BEFORE we trust
		// GetVersion()/State() (both read the new prefix), otherwise a renamed node
		// looks fresh and every package re-runs. No-op on a genuinely fresh node.
		// MIGRATION-SHIM: remove this block with the legacy skyhook.nvidia.com group.
		if err := migrateNodePrefixToNodeWright(node, logger); err != nil {
			return err
		}
		from = node.GetVersion()
	}
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

// MIGRATION-SHIM: remove with the legacy skyhook.nvidia.com group.
func (node *skyhookNode) PruneLegacyMetadata() bool {
	return pruneLegacyNodePrefix(node)
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

// ProgressSkipped promotes any package that was skipped during interrupt sequencing
// to complete and persists the result.
func (node *skyhookNode) ProgressSkipped() error {
	if node.nodeState.ProgressSkipped(node.skyhook.Spec.Packages, node.skyhook.GetConfigInterrupts(), node.skyhook.GetConfigUpdates()) {
		node.skyhook.Updated = true
		// Persist the promotion to the nodeState annotation, exactly as Upsert/RemoveState do.
		// Without SetState the promoted state lives only in node.nodeState; it never reaches
		// the annotation the Node patch diffs against, so the promotion is silently dropped
		// unless another package's Upsert happens to re-serialize the whole map.
		return node.SetState(node.nodeState)
	}
	return nil
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
// nodewright.nvidia.com/ prefix, indicating it has been previously touched by the NodeWright operator.
func (node *skyhookNode) HasSkyhookAnnotations() bool {
	for key := range node.Annotations {
		if strings.HasPrefix(key, v1alpha1.METADATA_PREFIX+"/") {
			return true
		}
	}
	return false
}

// Cordon marks the node unschedulable and records the cordon in annotations for this Skyhook.
// It reports whether this call changed the node, which means the cordon is still only in
// memory and has not reached the API server yet.
func (node *skyhookNode) Cordon() bool {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	_, ok := node.Annotations[cordonAnnotationKey(node.skyhookName)]
	if !node.Spec.Unschedulable || !ok {
		node.Spec.Unschedulable = true
		node.Annotations[cordonAnnotationKey(node.skyhookName)] = cordonAnnotationValue
		node.updated = true
		return true
	}
	return false
}

// StartDrain records when draining started for this Skyhook on this node.
func (node *skyhookNode) StartDrain(startedAt metav1.Time) {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	key := node.drainStartAnnotationKey()
	if _, ok := node.Annotations[key]; ok {
		return
	}

	node.Annotations[key] = startedAt.Time.Format(time.RFC3339Nano)
	node.updated = true
}

// DrainStartedAt returns when draining started for this Skyhook on this node.
func (node *skyhookNode) DrainStartedAt() (*metav1.Time, error) {
	if node.Annotations == nil {
		return nil, nil
	}

	value, ok := node.Annotations[node.drainStartAnnotationKey()]
	if !ok {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("error parsing drain start annotation: %w", err)
	}

	startedAt := metav1.NewTime(parsed)
	return &startedAt, nil
}

// ClearDrainStart removes the drain start marker for this Skyhook on this node.
func (node *skyhookNode) ClearDrainStart() {
	if node.Annotations == nil {
		return
	}

	key := node.drainStartAnnotationKey()
	if _, ok := node.Annotations[key]; !ok {
		return
	}

	delete(node.Annotations, key)
	node.updated = true
}

// SetDrainBlocked persists this pass's drain-blocker findings (PDB rejections, unmanaged
// pods, emptyDir pods) for this Skyhook on this node, replacing whatever was recorded
// previously. Nil or empty blocked clears the annotation. Unlike the per-package
// nodeState annotation, this always sets the whole value: there is exactly one DrainNode
// call's findings to record per pass, never several to merge.
//
// Persisting here — at the moment the caller learns the result — rather than accumulating
// in a caller-local slice is what lets the DrainBlocked condition be level-triggered:
// UpdateDrainBlockedCondition rebuilds it from this annotation on every reconcile pass,
// including passes that skip RunSkyhookPackages entirely (paused, disabled, complete
// Skyhooks) or that return from it early (an error, or spec.serial stopping after the
// first node). Without persisting immediately, all three of those paths would either
// never update the condition or wrongly clear it for nodes the pass never reached.
func (node *skyhookNode) SetDrainBlocked(blocked []drain.BlockedPod) error {
	key := node.drainBlockedAnnotationKey()

	if len(blocked) == 0 {
		if node.Annotations == nil {
			return nil
		}
		if _, ok := node.Annotations[key]; !ok {
			return nil
		}
		delete(node.Annotations, key)
		node.updated = true
		return nil
	}

	data, err := json.Marshal(blocked)
	if err != nil {
		return fmt.Errorf("error marshalling drain blocked pods: %w", err)
	}

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	if existing, ok := node.Annotations[key]; !ok || existing != string(data) {
		node.Annotations[key] = string(data)
		node.updated = true
	}

	return nil
}

// DrainBlocked returns the drain-blocker findings last persisted for this Skyhook on this
// node by SetDrainBlocked, or nil if none are recorded.
func (node *skyhookNode) DrainBlocked() ([]drain.BlockedPod, error) {
	if node.Annotations == nil {
		return nil, nil
	}

	value, ok := node.Annotations[node.drainBlockedAnnotationKey()]
	if !ok {
		return nil, nil
	}

	var blocked []drain.BlockedPod
	if err := json.Unmarshal([]byte(value), &blocked); err != nil {
		return nil, fmt.Errorf("error unmarshalling drain blocked pods: %w", err)
	}

	return blocked, nil
}

// Uncordon marks the node schedulable and removes this Skyhook's cordon annotation if present.
func (node *skyhookNode) Uncordon() {

	// if we hold a cordon remove it, also we dont want to remove a cordon if we dont have one...
	_, ok := node.Annotations[cordonAnnotationKey(node.skyhookName)]
	if ok {
		delete(node.Annotations, cordonAnnotationKey(node.skyhookName))
		// Multiple Skyhooks can cordon the same node; only mark it schedulable
		// after every Skyhook-owned cordon has been released.
		if !hasSkyhookCordon(node.Annotations) {
			node.Spec.Unschedulable = false
		}
		node.updated = true
	}
}

func cordonAnnotationKey(skyhookName string) string {
	return cordonAnnotationPrefix + skyhookName
}

func hasSkyhookCordon(annotations map[string]string) bool {
	for key := range annotations {
		if strings.HasPrefix(key, cordonAnnotationPrefix) {
			return true
		}
	}
	// If a non-runtime-required Skyhook with an interrupt runs after all runtime-required Skyhooks and one of the
	// runtime-required Skyhooks sets runtimeRequiredCordonAfter (and the runtime-required taint still exists),
	// preserve the persistent cordon. Note that any runtime-required Skyhooks that run initially are free to
	// add and remove the interrupt cordon because the persistent cordon is only applied when the runtime-required
	// taint is removed.
	_, ok := annotations[v1alpha1.RuntimeRequiredCordonAnnotation]
	return ok
}

// Reset clears Skyhook-related state and annotations so the node can be reconfigured from scratch.
func (node *skyhookNode) Reset() {

	delete(node.skyhook.Status.NodeState, node.Name)
	delete(node.skyhook.Status.NodeStatus, node.Name)
	node.skyhook.Status.Status = v1alpha1.StatusUnknown
	node.skyhook.Updated = true

	delete(node.Annotations, cordonAnnotationKey(node.skyhookName))
	delete(node.Annotations, node.drainStartAnnotationKey())
	delete(node.Annotations, node.drainBlockedAnnotationKey())
	delete(node.Annotations, fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, node.skyhookName))
	delete(node.Annotations, fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhookName))
	delete(node.Annotations, fmt.Sprintf("%s/version_%s", v1alpha1.METADATA_PREFIX, node.skyhookName))

	delete(node.Labels, fmt.Sprintf("%s/status_%s", v1alpha1.METADATA_PREFIX, node.skyhookName))

	// We just wiped the nodeState annotation; invalidate the in-memory cache so a later
	// State() read in this reconcile doesn't serve the stale (pre-reset) map.
	node.nodeState = nil
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
		Type:               corev1.NodeConditionType(fmt.Sprintf("%s/%s/%s", v1alpha1.METADATA_PREFIX, node.skyhookName, conditionTypeNotReady)),
		Status:             condStatus,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
		Reason:             readyReason,
		Message:            fmt.Sprintf("NodeWright %s Ready", node.skyhookName),
	}

	errorCond := corev1.NodeCondition{
		Type:               corev1.NodeConditionType(fmt.Sprintf("%s/%s/%s", v1alpha1.METADATA_PREFIX, node.skyhookName, conditionTypeErroring)),
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
			if condition.Reason != cond.Reason || condition.Message != cond.Message {
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

// CleanupSCRMetadata removes all operator-managed annotations and labels for this
// Skyhook from the node, plus any node conditions set by this Skyhook.
//
// The nodeState annotation is preserved if it still records packages that were
// not uninstalled (D2 semantics: non-absent entry = files remain on host). The
// version annotation is preserved alongside it so a future operator can still
// interpret the retained state schema via Migrate.
func (node *skyhookNode) CleanupSCRMetadata() {
	prefix := fmt.Sprintf("%s/", v1alpha1.METADATA_PREFIX)
	suffix := fmt.Sprintf("_%s", node.skyhookName)
	nodeStateKey := fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)
	versionKey := fmt.Sprintf("%s/version_%s", v1alpha1.METADATA_PREFIX, node.skyhookName)

	// Preserve only when we actually parsed a non-empty state. A decode error
	// or an empty map both mean there's nothing meaningful to keep, so the
	// annotation (and its companion version annotation) should be wiped.
	state, err := node.State()
	preserveNodeState := err == nil && len(state) > 0

	for key := range node.Annotations {
		if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) {
			if preserveNodeState && (key == nodeStateKey || key == versionKey) {
				continue
			}
			delete(node.Annotations, key)
			node.updated = true
		}
	}
	// If we wiped the nodeState annotation, invalidate the in-memory cache so
	// any subsequent State() read in this reconcile doesn't serve the stale
	// map that was populated before the wipe.
	if !preserveNodeState {
		node.nodeState = nil
	}
	for key := range node.Labels {
		if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) {
			delete(node.Labels, key)
			node.updated = true
		}
	}

	// Remove node conditions set by this Skyhook
	condPrefix := corev1.NodeConditionType(fmt.Sprintf("%s/%s/", v1alpha1.METADATA_PREFIX, node.skyhookName))
	filtered := make([]corev1.NodeCondition, 0, len(node.Node.Status.Conditions))
	for _, c := range node.Node.Status.Conditions {
		if !strings.HasPrefix(string(c.Type), string(condPrefix)) {
			filtered = append(filtered, c)
		} else {
			node.updated = true
		}
	}
	node.Node.Status.Conditions = filtered
}
