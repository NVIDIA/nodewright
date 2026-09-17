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

package v1alpha1

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/NVIDIA/nodewright/operator/internal/graph"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NodeWrightSpec defines the desired state of NodeWright
type NodeWrightSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Serial tells the operator if it allowed to run in packages in parallel. If true, the operator will run one package at a time.
	//+kubebuilder:default=false
	Serial bool `json:"serial,omitempty"`

	// PodNonInterruptLabels are a set of labels we want to monitor pods for whether they are interruptible.
	// Matching Pending or Running pods block pre-drain progress indefinitely; spec.drainConfig.timeout does not
	// apply to this barrier. A Blocked condition with reason NonInterruptPodsRunning is surfaced while this barrier holds.
	PodNonInterruptLabels metav1.LabelSelector `json:"podNonInterruptLabels,omitempty"`

	// NodeSelector are a set of labels we want to monitor nodes for applying packages too
	NodeSelector metav1.LabelSelector `json:"nodeSelectors,omitempty"`

	// DeploymentPolicy is the name of a DeploymentPolicy for rollout settings
	// +optional
	DeploymentPolicy string `json:"deploymentPolicy,omitempty"`

	// DeploymentPolicyOptions allows per-NodeWright overrides of DeploymentPolicy settings
	// +optional
	DeploymentPolicyOptions *DeploymentPolicyOptions `json:"deploymentPolicyOptions,omitempty"`

	// InterruptionBudget configures how many nodes that match node selectors that allowed to be interrupted at once.
	InterruptionBudget InterruptionBudget `json:"interruptionBudget,omitempty"`

	// DrainConfig tunes how nodes are drained before running interrupt packages.
	// If unset, the operator preserves its existing drain behavior.
	// +optional
	DrainConfig *DrainConfig `json:"drainConfig,omitempty"`

	// Packages are the DAG of packages to be applied to nodes.
	Packages Packages `json:"packages,omitempty"`

	// AdditionalTolerations adds tolerations to all packages
	AdditionalTolerations []corev1.Toleration `json:"additionalTolerations,omitempty"`

	// This NodeWright is required to have been completed before any workloads can start
	//+kubebuilder:default=false
	RuntimeRequired bool `json:"runtimeRequired,omitempty"`

	// RuntimeRequiredCordonAfter will result in the operator applying a persistent node cordon
	// after all runtime-required NodeWrights complete against a given node if the node currently
	// has the runtime-required taint applied. The operator will apply a runtimeRequiredCordon
	// annotation and mark the given node as unschedulable. This setting is only respected if
	// RuntimeRequired is also true.
	//+kubebuilder:default=false
	RuntimeRequiredCordonAfter bool `json:"runtimeRequiredCordonAfter,omitempty"`

	// AutoTaintNewNodes enables the operator to automatically apply the runtime-required taint
	// to new nodes that match this NodeWright's node selector. Only meaningful when RuntimeRequired is true.
	// A node is considered "new" if it has no nodewright.nvidia.com/* annotations.
	//+kubebuilder:default=false
	AutoTaintNewNodes bool `json:"autoTaintNewNodes,omitempty"`

	// Priority determines the order in which NodeWrights are applied. Lower values are applied first.
	//+kubebuilder:validation:Minimum=1
	//+kubebuilder:default=200
	Priority int `json:"priority,omitempty"`

	// Sequencing controls whether priority ordering is enforced globally or per-node.
	// "node" (default): a node can proceed past this NodeWright independently once it completes on that node.
	// "all": all nodes must complete this NodeWright before any node starts the next priority.
	//+kubebuilder:validation:Enum=all;node
	//+kubebuilder:default="node"
	Sequencing SequencingMode `json:"sequencing,omitempty"`
}

// SequencingMode controls whether priority ordering is enforced globally or per-node
type SequencingMode string

const (
	// SequencingNode allows each node to progress past this NodeWright independently
	// as soon as it completes on that node (per-node ordering)
	SequencingNode SequencingMode = "node"
	// SequencingAll requires all nodes to complete this NodeWright before any node
	// starts the next priority level (global ordering)
	SequencingAll SequencingMode = "all"
)

// IsPerNodeSequencing returns true if this NodeWright uses per-node priority ordering
func (spec *NodeWrightSpec) IsPerNodeSequencing() bool {
	return spec.Sequencing != SequencingAll
}

// BuildGraph turns packages in the a graph of dependencies
func (spec *NodeWrightSpec) BuildGraph() (graph.DependencyGraph[*Package], error) {
	dependencyGraph := graph.New[*Package]()
	for _, _package := range spec.Packages {

		deps := make([]string, 0)
		for dep, ver := range _package.DependsOn {
			if ver == "" {
				return nil, fmt.Errorf("DependsOn version is empty for [%s]", dep)
			}
			deps = append(deps, fmt.Sprintf("%s|%s", dep, ver))
		}

		err := dependencyGraph.Add(_package.GetUniqueName(), &_package, deps...)
		if err != nil {
			return nil, fmt.Errorf("error building graph from packages: %w", err)
		}
	}

	// return dependencyGraph, nil
	return dependencyGraph, nil
}

// Packages are set of packages to apply
type Packages map[string]Package

// Names sets the names on packages if not set
func (f Packages) Names() {
	for k := range f {
		m := f[k]
		if m.Name == "" {
			m.Name = k
			f[k] = m
		}
	}
}

// splitImageReference splits a container image reference into its repository,
// tag, and digest. tag and digest are nil when their separator (":" or "@") is
// absent, and non-nil (possibly pointing at "") when the separator is present,
// so callers can distinguish "no tag" from an empty "repo:". A trailing
// "@digest" is separated first, and a colon before the final "/" is treated as
// a registry port rather than a tag separator (e.g. "localhost:5000/org/pkg"
// has no tag).
func splitImageReference(image string) (repository string, tag, digest *string) {
	repository = image
	if index := strings.LastIndex(repository, "@"); index >= 0 {
		d := repository[index+1:]
		digest = &d
		repository = repository[:index]
	}

	lastSlash := strings.LastIndex(repository, "/")
	if lastColon := strings.LastIndex(repository, ":"); lastColon > lastSlash {
		t := repository[lastColon+1:]
		tag = &t
		repository = repository[:lastColon]
	}

	return repository, tag, digest
}

func (f *Packages) UnmarshalJSON(data []byte) error {

	var ret map[string]Package
	err := json.Unmarshal(data, &ret)
	if err != nil {
		return fmt.Errorf("unmarshalling packages: %w", err)
	}

	*f = Packages(ret)
	f.Names()
	return nil
}

type InterruptionBudget struct {
	// Percent of nodes that match node selectors that allowed to be interrupted at once.
	// Percent and count are mutually exclusive settings
	//+kubebuilder:validation:Minimum=0
	//+kubebuilder:validation:Maximum=100
	//+nullable
	Percent *int `json:"percent,omitempty"`

	// Count is number of nodes that match node selectors that allowed to be interrupted at once.
	// Percent and count are mutually exclusive settings
	//+kubebuilder:validation:Minimum=0
	//+nullable
	Count *int `json:"count,omitempty"`
}

func (i *InterruptionBudget) Validate() error {
	if i.Count != nil && i.Percent != nil {
		return errors.New("error InterruptionBudget is not valid, both percent and count can not be set at the same time")
	}
	return nil
}

type DrainConfig struct {
	// DisableEviction bypasses the eviction API and deletes pods directly.
	// This bypasses PodDisruptionBudgets.
	// +optional
	//+kubebuilder:default=false
	//+nullable
	DisableEviction *bool `json:"disableEviction,omitempty"`

	// DeleteEmptyDirData allows draining pods that use emptyDir volumes.
	// Defaults to true to preserve the operator's existing behavior.
	// +optional
	//+kubebuilder:default=true
	//+nullable
	DeleteEmptyDirData *bool `json:"deleteEmptyDirData,omitempty"`

	// Force allows draining pods not managed by a controller.
	// Defaults to true to preserve the operator's existing behavior.
	// +optional
	//+kubebuilder:default=true
	//+nullable
	Force *bool `json:"force,omitempty"`

	// IgnoreDaemonSets skips DaemonSet-managed pods during drain.
	// Defaults to true to preserve the operator's existing behavior.
	// +optional
	//+kubebuilder:default=true
	//+nullable
	IgnoreDaemonSets *bool `json:"ignoreDaemonSets,omitempty"`

	// Timeout bounds how long the operator waits for a node to drain.
	// Zero or unset means no timeout.
	// +optional
	//+nullable
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// GracePeriod overrides the grace period used on pod eviction/delete.
	// Unset uses each pod's own terminationGracePeriodSeconds.
	// +optional
	//+nullable
	GracePeriod *metav1.Duration `json:"gracePeriod,omitempty"`
}

func (d *DrainConfig) Validate() error {
	if d == nil {
		return nil
	}

	if d.Timeout != nil && d.Timeout.Duration < 0 {
		return errors.New("drainConfig.timeout must be greater than or equal to 0")
	}

	if d.GracePeriod != nil && d.GracePeriod.Duration < 0 {
		return errors.New("drainConfig.gracePeriod must be greater than or equal to 0")
	}

	return nil
}

type PackageRef struct {
	// Name of the package. Do not set unless you know what your doing. Comes from map key.
	//+optional
	//+kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]{0,41}[a-z]$`
	//+kubebuilder:validation:MaxLength=43
	Name string `json:"name"`
	// Version is the version of the package
	//+kubebuilder:validation:Required
	Version string `json:"version"`
}

func (p *PackageRef) GetUniqueName() string {
	return fmt.Sprintf("%s|%s", p.Name, p.Version)
}

type ResourceRequirements struct {
	CPURequest    resource.Quantity `json:"cpuRequest,omitempty"`
	CPULimit      resource.Quantity `json:"cpuLimit,omitempty"`
	MemoryRequest resource.Quantity `json:"memoryRequest,omitempty"`
	MemoryLimit   resource.Quantity `json:"memoryLimit,omitempty"`
}

// Uninstall configures explicit uninstall support for a package.
type Uninstall struct {
	// Enabled declares this package supports uninstall (has uninstall.sh/uninstall_check.sh).
	// When true, the operator will run uninstall pods before allowing package removal
	// and during CR deletion cleanup.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Apply triggers the uninstall workflow on all target nodes.
	// Only valid when Enabled is true (webhook rejects apply=true with enabled=false).
	// Set to false (or remove) to cancel a pending uninstall.
	// +kubebuilder:default=false
	Apply bool `json:"apply"`
}

// Package is a container that contains the skyhook agent plus some work to do, plus any dependencies to be run first.
type Package struct {
	PackageRef `json:",inline"`

	// Image is the container image to run, given as a registry/repository reference with
	// no tag or digest (e.g. "ghcr.io/nvidia/skyhook-packages/shellscript" or a ported
	// registry like "localhost:5000/org/pkg"). The version field supplies the tag, and
	// containerSHA pins an exact digest. The webhook rejects any inline tag or
	// "@sha256:..." digest embedded in image, and image must be non-empty with no
	// whitespace.
	//+kubebuilder:example="ghcr.io/nvidia/skyhook-packages/shellscript"
	//+kubebuilder:validation:Required
	//+kubebuilder:validation:Pattern=`^\S+$`
	Image string `json:"image"`

	// ContainerSHA is the SHA256 digest of the container image for verification purposes.
	// When specified, this will be used instead of the version tag to pull the exact image.
	// Format: sha256:<64 lowercase hex chars>
	//+kubebuilder:example="sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	//+kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	//+optional
	ContainerSHA string `json:"containerSHA,omitempty"`

	// Agent Image Override is the container image to override at the package level. Full qualified image with tag.
	// This overrides the image provided via ENV to the operator.
	//+kubebuilder:example="alpine:3.21.0"
	AgentImageOverride string `json:"agentImageOverride,omitempty"`

	// Interrupt if supplied is the type of interrupt
	//+optional
	Interrupt *Interrupt `json:"interrupt,omitempty"`

	// DependsOn is a map of name:version of dependencies.
	// NOTE: we need to deal with version
	//+optional
	DependsOn map[string]string `json:"dependsOn,omitempty"`

	// ConfigInterrupts is a map for whether an interrupt is needed for a configmap key
	// +optional
	ConfigInterrupts map[string]Interrupt `json:"configInterrupts,omitempty"`

	// ConfigMap contains the configuration data.
	// Each key must consist of alphanumeric characters, '-', '_' or '.'.
	// Values must be UTF-8 byte sequences.
	// The keys stored in Data must not overlap with the keys in
	// the BinaryData field, this is enforced during validation process.
	// +optional
	ConfigMap map[string]string `json:"configMap,omitempty"`

	// Env are the environment variables for the package
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources lets you set the cpu and memory limits and requests for this package.
	// More info: https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
	Resources *ResourceRequirements `json:"resources,omitempty"`

	// GracefulShutdown is the graceful shutdown timeout for the package, if not set, uses k8s default
	//+optional
	GracefulShutdown *metav1.Duration `json:"gracefulShutdown,omitempty"`

	// StageTimeout bounds the wall-clock runtime of one attempt at each of this package's
	// stages (the stage Job pod's activeDeadlineSeconds). An attempt that runs past it is
	// killed and retried like any other failed attempt, and the package surfaces as erroring
	// once the operator's retry budget (JOB_BACKOFF_LIMIT) is spent. Interrupt stages are the
	// exception: their attempt must span a reboot, so it bounds the whole stage instead.
	// Unset uses the operator default (JOB_STAGE_TIMEOUT); "0" removes the time bound for this
	// package, leaving the retry budget as its only limit. Note what that does NOT bound: the
	// budget is only spent by attempts that fail, so with "0" an attempt that hangs hangs
	// forever. There is also no bound on a pod the kubelet never acknowledges — the attempt clock
	// runs from the pod's start time, which such a pod never gets — so that case is unbounded at
	// any stageTimeout. It surfaces as a stage stuck in_progress with a Pending pod, and is node
	// health rather than stage health.
	//
	// The value is fixed when a stage's Job is created. Editing it does not change a Job already
	// running, because the bound lives on that Job's pod template and a Job's template is
	// immutable. To apply a new value to work already under way, clear that Job — `kubectl
	// nodewright package rerun <package>`, or deleting the Job directly — and the stage restarts
	// under the new value. Otherwise it takes effect at the package's next stage.
	//+optional
	StageTimeout *metav1.Duration `json:"stageTimeout,omitempty"`

	// Uninstall configures explicit uninstall support for this package.
	// +optional
	Uninstall *Uninstall `json:"uninstall,omitempty"`
}

func (f *Package) HasInterrupt() bool {
	return f.Interrupt != nil
}

// UninstallEnabled returns true if this package has uninstall support enabled.
func (f *Package) UninstallEnabled() bool {
	return f != nil && f.Uninstall != nil && f.Uninstall.Enabled
}

// IsUninstalling returns true if this package is actively being uninstalled
// (both enabled and apply must be true).
func (f *Package) IsUninstalling() bool {
	return f.UninstallEnabled() && f.Uninstall.Apply
}

type InterruptType string
type Interrupt struct {
	// Type of interrupt. Reboot, Service, All Services, or Noop
	//+kubebuilder:validation:Required
	//+kubebuilder:validation:Enum=service;reboot;noop;restartAllServices
	Type InterruptType `json:"type"`
	// List of systemd services to restart
	//+optional
	Services []string `json:"services,omitempty"`
}

// ToArgs base64 encoded json of self
func (i *Interrupt) ToArgs() (string, error) {

	// HACK: choosing to do it this way so the CRD interface is not tied to the agent
	clone := i.DeepCopy() // make copy as to not alter this

	switch clone.Type { // update type to match what the agent is expecting
	case REBOOT:
		clone.Type = InterruptType("node_restart")
	case SERVICE:
		clone.Type = InterruptType("service_restart")
	case NOOP:
		clone.Type = InterruptType("no_op")
	case RESTART_ALL_SERVICES:
		clone.Type = InterruptType("restart_all_services")
	}

	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

const (
	REBOOT               InterruptType = "reboot"
	SERVICE              InterruptType = "service"
	NOOP                 InterruptType = "noop"
	RESTART_ALL_SERVICES InterruptType = "restartAllServices"
)

// CompartmentStatus tracks the detailed state of a compartment
type CompartmentStatus struct {
	// Matched is the number of nodes that match this compartment's selector
	Matched int `json:"matched"`

	// Ceiling is the maximum number of nodes that can be in progress at once
	Ceiling int `json:"ceiling"`

	// InProgress is the number of nodes currently in progress
	InProgress int `json:"inProgress"`

	// Completed is the number of nodes that have completed successfully
	Completed int `json:"completed"`

	// ProgressPercent is the percentage of nodes completed (0-100)
	ProgressPercent int `json:"progressPercent"`

	// BatchState tracks the batch processing state for this compartment
	// +optional
	BatchState *BatchProcessingState `json:"batchState,omitempty"`
}

// NodeWrightStatus defines the observed state of NodeWright
type NodeWrightStatus struct {

	// observedGeneration represents the .metadata.generation that the condition was set based upon.
	// For instance, if .metadata.generation is currently 12, but the .status.observedGeneration is 9, then status is out of date
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NodeState is the detailed state of each node
	NodeState map[string]NodeState `json:"nodeState,omitempty"`

	// NodeStatus tracks by node the status of the node
	NodeStatus map[string]Status `json:"nodeStatus,omitempty"`

	//Status is the roll of this instance of NodeWright and all nodes status.
	//+kubebuilder:validation:Enum=unknown;complete;blocked;waiting;disabled;paused;in_progress;erroring
	Status Status `json:"status,omitempty"`

	// Represents the observations of a NodeWright's current state.
	// Known .status.conditions.type are: "Available", "Progressing", and "Degraded" // TODO
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// NodeBootIds tracks the boot ids of nodes for triggering on reboot
	NodeBootIds map[string]string `json:"nodeBootIds,omitempty"`

	// NodePriority tracks what nodes we are working on. This is makes the interrupts budgets sticky.
	NodePriority map[string]metav1.Time `json:"nodePriority,omitempty"`

	// NodeOrderOffset tracks the cumulative count of nodes removed from NodePriority.
	// Used with NodePriority to compute monotonic SKYHOOK_NODE_ORDER across batches.
	NodeOrderOffset int `json:"nodeOrderOffset,omitempty"`

	// ConfigUpdates tracks config updates
	ConfigUpdates map[string][]string `json:"configUpdates,omitempty"`

	// CompartmentStatuses tracks the detailed status of each compartment
	// +optional
	CompartmentStatuses map[string]CompartmentStatus `json:"compartmentStatuses,omitempty"`

	// +kubebuilder:example=3
	// +kubebuilder:default=0
	// NodesInProgress displays the number of nodes that are currently in progress and is
	// only used for printer columns.
	NodesInProgress int `json:"nodesInProgress,omitempty"`

	// +kubebuilder:example="3/5"
	// +kubebuilder:default="0/0"
	// CompleteNodes is a string that displays the amount of nodes that are complete
	// out of the total nodes the NodeWright is being applied to and is only used for
	// a printer column.
	CompleteNodes string `json:"completeNodes,omitempty"`

	// +kubebuilder:example="dexter,spencer,foobar"
	// +kubebuilder:default=""
	// PackageList is a comma separated list of package names from the NodeWright spec and
	// is only used for a printer column.
	PackageList string `json:"packageList,omitempty"`
}

type NodeState map[string]PackageStatus

// Upsert adds or updates specified state for package in the node state
func (ns *NodeState) Upsert(_package PackageRef, image string, state State, stage Stage, restarts int32, containerSHA string) bool {

	if *ns == nil {
		*ns = make(map[string]PackageStatus)
	}

	status := PackageStatus{
		Name:         _package.Name,
		Version:      _package.Version,
		State:        state,
		Image:        image,
		Stage:        stage,
		Restarts:     restarts,
		ContainerSHA: containerSHA,
	}

	existing, ok := (*ns)[_package.GetUniqueName()]
	if ok && status.Equal(&existing) {
		return false
	}

	(*ns)[_package.GetUniqueName()] = status

	return true
}

// RemoveState removes specified package from Node State
func (ns *NodeState) RemoveState(_package PackageRef) bool {
	if *ns == nil {
		return false
	}

	_, ok := (*ns)[_package.GetUniqueName()]
	if ok {
		delete(*ns, _package.GetUniqueName())
		return true
	}

	return false
}

// IsUninstallCycleInProgress returns true if the named package is anywhere in
// the uninstall cycle on this node — either the uninstall pod phase
// (StageUninstall) or the post-uninstall interrupt phase
// (StageUninstallInterrupt). This is the node-annotation-level answer to
// "has uninstall started?" — distinct from Package.IsUninstalling() which only
// answers "is uninstall requested in the spec?"
func (ns *NodeState) IsUninstallCycleInProgress(uniqueName string) bool {
	if *ns == nil {
		return false
	}
	status, ok := (*ns)[uniqueName]
	if !ok {
		return false
	}
	return status.Stage == StageUninstall || status.Stage == StageUninstallInterrupt
}

// IsUninstalled returns true if the named package is absent from this node's
// state, meaning uninstall has completed (absent = uninstalled per D2).
func (ns *NodeState) IsUninstalled(uniqueName string) bool {
	if *ns == nil {
		return true
	}
	_, ok := (*ns)[uniqueName]
	return !ok
}

func (ns *NodeState) Get(name string) *PackageStatus {
	if s, ok := (*ns)[name]; ok {
		return &s
	}
	return nil
}

// IsComplete checks if the number of complete frames is equal to total packages,
// and that the set of packages contain the same packages.
// Packages that are being uninstalled (IsUninstalling) and are absent from node
// state are treated as "done" — they don't block completion.
func (ns *NodeState) IsComplete(packages Packages, interrupt map[string][]*Interrupt, config map[string][]string) bool {
	// Build the set of "active" packages: exclude those where uninstall is
	// requested and has completed (absent from node state = uninstalled).
	activePackages := make(Packages)
	for name, pkg := range packages {
		if pkg.IsUninstalling() && ns.IsUninstalled(pkg.GetUniqueName()) {
			continue // uninstall completed — don't require for completion
		}
		activePackages[name] = pkg
	}

	if len(activePackages) <= len(ns.GetComplete(activePackages, interrupt, config)) {
		// If a current spec package is still in the uninstall cycle the node isn't complete.
		for _, pkg := range activePackages {
			if ns.IsUninstallCycleInProgress(pkg.GetUniqueName()) {
				return false
			}
		}

		return ns.Contains(activePackages)
	}

	return false
}

// HasInterrupt determines whether package has an interrupt based off of config updates and interrupts passed to it
func (ns *NodeState) HasInterrupt(_package Package, interrupt map[string][]*Interrupt, config map[string][]string) bool {
	var hasInterrupt bool

	if len(config[_package.Name]) > 0 {
		hasInterrupt = len(interrupt[_package.Name]) > 0
	} else {
		hasInterrupt = _package.HasInterrupt()
	}

	return hasInterrupt
}

func (ns *NodeState) NextStage(_package *Package, interrupt map[string][]*Interrupt, config map[string][]string) *Stage {

	state, ok := (*ns)[_package.GetUniqueName()]
	if !ok || state.State != StateComplete {
		return nil
	}

	// StageInterrupt → StagePostInterrupt is unconditional even in the
	// no-interrupt map: if a package has reached StageInterrupt at any point,
	// the dynamic HasInterrupt(config) signal may have decayed by now
	// (Status.ConfigUpdates can be cleared or never persist due to a 409 on the
	// spec patch), but the package still needs to escape. The with-interrupt
	// map deliberately omits StageUninstall → StageApply (PR #200) so that
	// with-interrupt uninstalls route via StageUninstallInterrupt instead — do
	// not collapse the two maps into one.
	nextStage := map[Stage]Stage{
		StageUninstall: StageApply,
		StageApply:     StageConfig,
		StageUpgrade:   StageConfig,
		StageInterrupt: StagePostInterrupt,
	}

	if hasInterrupt := (*ns).HasInterrupt(*_package, interrupt, config); hasInterrupt {
		nextStage = map[Stage]Stage{
			StageUpgrade:   StageConfig,
			StageApply:     StageConfig,
			StageConfig:    StageInterrupt,
			StageInterrupt: StagePostInterrupt,
		}
	}

	if next, exists := nextStage[state.Stage]; exists {
		return &next
	}

	return nil
}

// Contains return true if node state contains the same packages as the spec including versions
func (ns *NodeState) Contains(packages Packages) bool {

	if len(*ns) < len(packages) { // same as above, ns can be longer, but not shorter
		return false
	}

	for _, v := range packages {
		v2, ok := (*ns)[v.GetUniqueName()]
		if !ok {
			return false
		}
		if v2.Version != v.Version {
			return false
		}
	}

	return true
}

func (left *NodeState) Equal(right *NodeState) bool {
	return reflect.DeepEqual(left, right)
}

// GetComplete returns a list of packages that are complete
func (ns *NodeState) GetComplete(packages Packages, interrupt map[string][]*Interrupt, config map[string][]string) []string {

	ret := make([]string, 0)

	for _, _package := range packages {
		if ns.IsPackageComplete(_package, interrupt, config) {
			ret = append(ret, _package.GetUniqueName())
		}
	}

	sort.Strings(ret)

	return ret
}

// IsPackageComplete reports whether a package has reached a terminal lifecycle
// state on this node. Two terminal cases:
//
//   - StagePostInterrupt: unconditionally terminal. Reaching it required the
//     operator to once decide HasInterrupt was true; gating the terminal check
//     on the still-true-now signal is redundant and would trap packages whose
//     Status.ConfigUpdates decayed (clearing or never persisting due to a 409
//     on the spec patch) between entering the interrupt cycle and reaching
//     post-interrupt.
//   - StageConfig with no interrupt currently in scope: terminal because the
//     package has no interrupt cycle to enter from here.
func (ns *NodeState) IsPackageComplete(_package Package, interrupt map[string][]*Interrupt, config map[string][]string) bool {

	packageStatus, found := (*ns)[_package.GetUniqueName()]
	if !found || _package.Version != packageStatus.Version || packageStatus.State != StateComplete {
		return false
	}

	if packageStatus.Stage == StagePostInterrupt {
		return true
	}
	if packageStatus.Stage == StageConfig && !(*ns).HasInterrupt(_package, interrupt, config) {
		return true
	}
	return false
}

// ProgressSkipped checks if a package is skipped and should be progressed to complete.
// Promotion is gated on Stage alone: being at StageInterrupt with StateSkipped means
// the operator already decided this package had an interrupt to schedule (see
// ProcessInterrupt). The dynamic HasInterrupt(config) signal can decay before this
// runs — Status.ConfigUpdates can be cleared or never persisted due to a 409 on the
// spec patch — so gating promotion on it can trap the package permanently.
func (ns *NodeState) ProgressSkipped(packages Packages, interrupt map[string][]*Interrupt, config map[string][]string) bool {
	ret := false
	for _, s := range *ns {
		f, ok := packages[s.Name]
		if !ok {
			continue
		}

		if s.Stage == StageInterrupt && s.State == StateSkipped {
			s.State = StateComplete
			(*ns)[f.GetUniqueName()] = s
			ret = true
		}
	}
	return ret
}

type PackageStatus struct {
	// Name is the name of the package
	//+kubebuilder:validation:Required
	Name string `json:"name"`

	// Version is the version of the package
	//+kubebuilder:validation:Required
	Version string `json:"version"`

	// Image for the package
	//+kubebuilder:validation:Required
	Image string `json:"image"`

	// ContainerSHA is the SHA256 digest that was actually deployed
	//+optional
	ContainerSHA string `json:"containerSHA,omitempty"`

	// Stage is where in the package install process is currently for a node.
	// these stages encapsulate checks. Both Apply and PostInterrupt also run checks,
	// these are all or nothing, meaning both need to be successful in order to transition
	//+kubebuilder:validation:Required
	//+kubebuilder:validation:Enum=apply;interrupt;post-interrupt;config;uninstall;uninstall-interrupt;upgrade
	Stage Stage `json:"stage"`

	// State is the current state of this package
	//+kubebuilder:validation:Required
	//+kubebuilder:validation:Enum=complete;in_progress;skipped;erroring;unknown
	State State `json:"state,omitempty"`

	// Restarts are the number of times a package restarted
	Restarts int32 `json:"restarts,omitempty"`
}

// Equal checks name, version, image, containerSHA, stage, state, and restarts
func (left *PackageStatus) Equal(right *PackageStatus) bool {
	return left.Name == right.Name &&
		left.Version == right.Version &&
		left.Image == right.Image &&
		left.ContainerSHA == right.ContainerSHA &&
		left.Stage == right.Stage &&
		left.State == right.State &&
		left.Restarts == right.Restarts
}

// IsInterruptStage reports whether the package is in one of the two interrupt-phase
// Stages (interrupt or uninstall-interrupt). Nil-safe so it preserves the existing
// `status != nil &&` guards at call sites: a nil/absent status is in no Stage.
func (s *PackageStatus) IsInterruptStage() bool {
	if s == nil {
		return false
	}
	return s.Stage == StageInterrupt || s.Stage == StageUninstallInterrupt
}

// IsActive reports whether the package's execution State is still live (in-progress or
// erroring), i.e. started but not yet terminal. Nil-safe.
func (s *PackageStatus) IsActive() bool {
	if s == nil {
		return false
	}
	return s.State == StateInProgress || s.State == StateErroring
}

// IsSkipped reports whether the package's execution State is skipped (a higher-priority
// interrupt won the node's single interrupt slot). Nil-safe.
func (s *PackageStatus) IsSkipped() bool {
	if s == nil {
		return false
	}
	return s.State == StateSkipped
}

type Stage string

const (
	StageUninstall          Stage = "uninstall"
	StageUninstallInterrupt Stage = "uninstall-interrupt"
	StageUpgrade            Stage = "upgrade"
	StageApply              Stage = "apply"
	StageInterrupt          Stage = "interrupt"
	StagePostInterrupt      Stage = "post-interrupt"
	StageConfig             Stage = "config"
)

var (
	Stages = []Stage{
		StageUninstall,
		StageUninstallInterrupt,
		StageUpgrade,
		StageApply,
		StageInterrupt,
		StagePostInterrupt,
		StageConfig,
	}
)

type State string

const (
	METADATA_PREFIX string = "nodewright.nvidia.com"

	// The RuntimeRequiredCordonAnnotation annotation and cordon are applied to a node when RuntimeRequiredCordonAfter
	// is true, a node completes all runtime-required NodeWrights, and the runtime-required taint is present.
	RuntimeRequiredCordonAnnotation = METADATA_PREFIX + "/runtimeRequiredCordon"

	StateComplete   State = "complete"
	StateInProgress State = "in_progress" // this means its actually running, pod started
	StateSkipped    State = "skipped"     // this means this package, stage are skipped mostly for some parts of the lifecycle
	StateErroring   State = "erroring"
	StateUnknown    State = "unknown"
)

var (
	States = []State{
		StateComplete,
		StateInProgress,
		StateSkipped,
		StateErroring,
		StateUnknown,
	}
)

type Status string

var (
	Statuses = []Status{
		StatusComplete,
		StatusBlocked,
		StatusWaiting,
		StatusDisabled,
		StatusPaused,
		StatusInProgress,
		StatusErroring,
		StatusUnknown,
	}
)

const (
	StatusComplete   Status = "complete"
	StatusBlocked    Status = "blocked"
	StatusWaiting    Status = "waiting"
	StatusDisabled   Status = "disabled"
	StatusPaused     Status = "paused"
	StatusInProgress Status = "in_progress"
	StatusErroring   Status = "erroring"
	StatusUnknown    Status = "unknown"
)

func GetStatus(s string) Status {
	switch Status(s) {
	case StatusComplete:
		return StatusComplete
	case StatusBlocked:
		return StatusBlocked
	case StatusWaiting:
		return StatusWaiting
	case StatusDisabled:
		return StatusDisabled
	case StatusPaused:
		return StatusPaused
	case StatusInProgress:
		return StatusInProgress
	case StatusErroring:
		return StatusErroring
	default:
		return StatusUnknown
	}
}

func StateToStatus(s State) Status {
	switch s {
	case StateComplete:
		return StatusComplete
	case StateErroring:
		return StatusErroring
	case StateInProgress:
		return StatusInProgress
	default:
		return StatusUnknown
	}
}

//+kubebuilder:object:root=true
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.status"
//+kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=".spec.priority"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:printcolumn:name="Nodes In-Progress",type=integer,JSONPath=".status.nodesInProgress"
//+kubebuilder:printcolumn:name="Complete Nodes",type=string,JSONPath=".status.completeNodes"
//+kubebuilder:printcolumn:name="Packages",type=string,JSONPath=".status.packageList"
//+kubebuilder:printcolumn:name="Policy",type=string,JSONPath=".spec.deploymentPolicy"
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// NodeWright is the Schema for the nodewrights API
type NodeWright struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeWrightSpec   `json:"spec,omitempty"`
	Status NodeWrightStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NodeWrightList contains a list of NodeWright
type NodeWrightList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeWright `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeWright{}, &NodeWrightList{})
}

// WasUpdated returns true if this instance of NodeWright has been updated
func (s *NodeWright) WasUpdated() bool {
	return s.Generation > 1 && s.Generation > s.Status.ObservedGeneration
}

func (s *NodeWright) IsPaused() bool {
	if s.Annotations == nil {
		return false
	}
	if val, ok := s.Annotations[fmt.Sprintf("%s/pause", METADATA_PREFIX)]; ok {
		return val == "true" //nolint:goconst
	}
	return false
}

func (s *NodeWright) IsDisabled() bool {
	if s.Annotations == nil {
		return false
	}
	if val, ok := s.Annotations[fmt.Sprintf("%s/disable", METADATA_PREFIX)]; ok {
		return val == "true"
	}
	return false
}

// ShouldResetBatchStateOnCompletion determines whether batch state should be reset
// based on the NodeWright's deployment policy options and the referenced DeploymentPolicy.
// The NodeWright's deploymentPolicyOptions takes precedence over the DeploymentPolicy setting.
// Returns false by default if neither is set.
func (s *NodeWright) ShouldResetBatchStateOnCompletion(policy *DeploymentPolicy) bool {
	// Check NodeWright-level override first (highest precedence)
	if s.Spec.DeploymentPolicyOptions != nil && s.Spec.DeploymentPolicyOptions.ResetBatchStateOnCompletion != nil {
		return *s.Spec.DeploymentPolicyOptions.ResetBatchStateOnCompletion
	}

	// Check DeploymentPolicy setting
	if policy != nil && policy.Spec.ResetBatchStateOnCompletion != nil {
		return *policy.Spec.ResetBatchStateOnCompletion
	}

	// Default to false if neither is set
	return false
}

// ResetCompartmentBatchStates resets all compartment batch states to fresh values.
// Returns true if any compartments were reset, false if there was nothing to reset.
func (s *NodeWright) ResetCompartmentBatchStates() bool {
	if len(s.Status.CompartmentStatuses) == 0 {
		return false
	}

	for name, cs := range s.Status.CompartmentStatuses {
		cs.BatchState = &BatchProcessingState{
			CurrentBatch:        1,
			ConsecutiveFailures: 0,
			CompletedNodes:      0,
			FailedNodes:         0,
			ShouldStop:          false,
			LastBatchSize:       0,
			LastBatchFailed:     false,
		}
		s.Status.CompartmentStatuses[name] = cs
	}
	return true
}
