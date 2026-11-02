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

// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
// rename. This whole file is deleted with the legacy api/v1alpha1 package in the
// removal release (which is why the converters live here, not in the new package).
//
// This file holds plain, unit-testable converters from the legacy
// skyhook.nvidia.com API group (this package) to the new nodewright.nvidia.com
// API group. They live with the legacy types deliberately: the dependency
// points legacy -> new (disposable -> durable), so when this legacy package is
// removed the conversion goes with it and the new package never depends on it.
// These are ordinary functions, not controller-runtime Convertible/Hub
// implementations, so they can be reused by the mirror controller and the CLI.
//
// Conversion is explicit and field-by-field rather than a JSON round-trip. The
// legacy types are frozen but the NodeWright types will evolve during the
// migration bridge; a rename, removal, or retype of a NodeWright field must
// break the compiler here rather than silently drop the legacy value. For that
// reason this file deliberately avoids reflection, unsafe, and any catch-all:
// the compiler is the regression test for schema divergence.
//
// Where a leaf struct's underlying shape is provably only primitives and shared
// k8s value types, a Go struct conversion (nwv1.Leaf(in.Leaf)) is used. That is
// still fully compile-checked: it stops compiling the moment either side gains a
// field the other lacks. Structs that nest local named types are converted
// field-by-field instead. Reference, pointer, slice, and map fields are
// deep-copied so the converted object never aliases the source, because the
// mirror controller reads cache objects that must not be mutated.
package v1alpha1

import (
	"strings"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Convert_Skyhook_To_NodeWright converts a legacy Skyhook into a NodeWright.
// The result is a fresh object to be created (ResourceVersion and UID are
// cleared) carrying the NodeWright GVK. Spec and Status are converted
// field-by-field; see the package comment for why this is not a JSON copy.
func Convert_Skyhook_To_NodeWright(in *Skyhook, out *nwv1.NodeWright) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	prepareObjectMeta(&out.ObjectMeta)

	out.TypeMeta.APIVersion = nwv1.GroupVersion.String()
	out.TypeMeta.Kind = "NodeWright"

	convertSkyhookSpec(&in.Spec, &out.Spec)
	convertSkyhookStatus(&in.Status, &out.Status)

	return nil
}

// Convert_DeploymentPolicy_To_NodeWright converts a legacy DeploymentPolicy into
// a new-group DeploymentPolicy. The Kind name is unchanged; only the API group
// moves. DeploymentPolicy has no Status subresource, so only Spec is converted.
func Convert_DeploymentPolicy_To_NodeWright(in *DeploymentPolicy, out *nwv1.DeploymentPolicy) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	prepareObjectMeta(&out.ObjectMeta)

	out.TypeMeta.APIVersion = nwv1.GroupVersion.String()
	out.TypeMeta.Kind = "DeploymentPolicy"

	convertDeploymentPolicySpec(&in.Spec, &out.Spec)

	return nil
}

// prepareObjectMeta turns a deep-copied source ObjectMeta into one suitable for a
// fresh object to be created in the new group: it clears server-assigned fields
// (ResourceVersion, UID), drops the legacy finalizer (the new reconciler manages its
// own finalizer key, so an inherited one would deadlock deletion), and rewrites the
// skyhook.nvidia.com/ annotation/label prefix to nodewright.nvidia.com/ so that
// prefix-keyed controls (pause, disable, version) carry over.
func prepareObjectMeta(meta *metav1.ObjectMeta) {
	meta.ResourceVersion = ""
	meta.UID = ""
	meta.Finalizers = nil

	meta.Annotations = rekeyPrefix(meta.Annotations)
	meta.Labels = rekeyPrefix(meta.Labels)

	if len(meta.Annotations) == 0 {
		meta.Annotations = nil
	}
	if len(meta.Labels) == 0 {
		meta.Labels = nil
	}
}

// rekeyPrefix rewrites map keys carrying the legacy skyhook.nvidia.com/ prefix to the
// new nodewright.nvidia.com/ prefix, leaving all other keys untouched.
func rekeyPrefix(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	oldPrefix := METADATA_PREFIX + "/"
	newPrefix := nwv1.METADATA_PREFIX + "/"
	out := make(map[string]string, len(m))
	// First copy every non-legacy key (including any explicit nodewright.nvidia.com/*
	// entry). Rewriting a legacy key below must never clobber an explicit new-prefix
	// value, otherwise a map carrying both skyhook.nvidia.com/foo and
	// nodewright.nvidia.com/foo would resolve non-deterministically on map iteration
	// order. Explicit NodeWright metadata always wins.
	for k, v := range m {
		if !strings.HasPrefix(k, oldPrefix) {
			out[k] = v
		}
	}
	for k, v := range m {
		if !strings.HasPrefix(k, oldPrefix) {
			continue
		}
		newKey := newPrefix + strings.TrimPrefix(k, oldPrefix)
		if _, exists := out[newKey]; exists {
			continue
		}
		out[newKey] = v
	}
	return out
}

// convertSkyhookSpec converts every field of SkyhookSpec into NodeWrightSpec.
func convertSkyhookSpec(in *SkyhookSpec, out *nwv1.NodeWrightSpec) {
	out.Serial = in.Serial
	out.RuntimeRequired = in.RuntimeRequired
	out.AutoTaintNewNodes = in.AutoTaintNewNodes
	out.Priority = in.Priority
	out.DeploymentPolicy = in.DeploymentPolicy
	out.Sequencing = nwv1.SequencingMode(in.Sequencing)

	out.PodNonInterruptLabels = *in.PodNonInterruptLabels.DeepCopy()
	out.NodeSelector = *in.NodeSelector.DeepCopy()

	out.DeploymentPolicyOptions = convertDeploymentPolicyOptions(in.DeploymentPolicyOptions)
	out.InterruptionBudget = convertInterruptionBudget(in.InterruptionBudget)
	out.DrainConfig = convertDrainConfig(in.DrainConfig)
	out.Packages = convertPackages(in.Packages)
	out.AdditionalTolerations = convertTolerations(in.AdditionalTolerations)
}

func convertDrainConfig(in *DrainConfig) *nwv1.DrainConfig {
	if in == nil {
		return nil
	}
	return &nwv1.DrainConfig{
		DisableEviction:    copyBoolPtr(in.DisableEviction),
		DeleteEmptyDirData: copyBoolPtr(in.DeleteEmptyDirData),
		Force:              copyBoolPtr(in.Force),
		IgnoreDaemonSets:   copyBoolPtr(in.IgnoreDaemonSets),
		Timeout:            convertDurationPtr(in.Timeout),
		GracePeriod:        convertDurationPtr(in.GracePeriod),
	}
}

func copyBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func convertTolerations(in []corev1.Toleration) []corev1.Toleration {
	if in == nil {
		return nil
	}
	out := make([]corev1.Toleration, len(in))
	for i := range in {
		in[i].DeepCopyInto(&out[i])
	}
	return out
}

func convertEnvVars(in []corev1.EnvVar) []corev1.EnvVar {
	if in == nil {
		return nil
	}
	out := make([]corev1.EnvVar, len(in))
	for i := range in {
		in[i].DeepCopyInto(&out[i])
	}
	return out
}

func convertInterruptionBudget(in InterruptionBudget) nwv1.InterruptionBudget {
	out := nwv1.InterruptionBudget{}
	if in.Percent != nil {
		v := *in.Percent
		out.Percent = &v
	}
	if in.Count != nil {
		v := *in.Count
		out.Count = &v
	}
	return out
}

func convertDeploymentPolicyOptions(in *DeploymentPolicyOptions) *nwv1.DeploymentPolicyOptions {
	if in == nil {
		return nil
	}
	out := &nwv1.DeploymentPolicyOptions{}
	if in.ResetBatchStateOnCompletion != nil {
		v := *in.ResetBatchStateOnCompletion
		out.ResetBatchStateOnCompletion = &v
	}
	return out
}

func convertPackages(in Packages) nwv1.Packages {
	if in == nil {
		return nil
	}
	out := make(nwv1.Packages, len(in))
	for k, v := range in {
		out[k] = convertPackage(v)
	}
	return out
}

func convertPackage(in Package) nwv1.Package {
	out := nwv1.Package{
		PackageRef:         nwv1.PackageRef(in.PackageRef),
		Image:              in.Image,
		ContainerSHA:       in.ContainerSHA,
		AgentImageOverride: in.AgentImageOverride,
	}

	out.Interrupt = convertInterruptPtr(in.Interrupt)
	out.DependsOn = copyStringMap(in.DependsOn)

	if in.ConfigInterrupts != nil {
		out.ConfigInterrupts = make(map[string]nwv1.Interrupt, len(in.ConfigInterrupts))
		for k, v := range in.ConfigInterrupts {
			out.ConfigInterrupts[k] = convertInterrupt(v)
		}
	}

	out.ConfigMap = copyStringMap(in.ConfigMap)
	out.Env = convertEnvVars(in.Env)
	out.Resources = convertResourceRequirements(in.Resources)
	out.GracefulShutdown = convertDurationPtr(in.GracefulShutdown)
	out.Uninstall = convertUninstall(in.Uninstall)

	return out
}

func convertInterruptPtr(in *Interrupt) *nwv1.Interrupt {
	if in == nil {
		return nil
	}
	out := convertInterrupt(*in)
	return &out
}

func convertInterrupt(in Interrupt) nwv1.Interrupt {
	out := nwv1.Interrupt{
		Type: nwv1.InterruptType(in.Type),
	}
	if in.Services != nil {
		out.Services = make([]string, len(in.Services))
		copy(out.Services, in.Services)
	}
	return out
}

func convertResourceRequirements(in *ResourceRequirements) *nwv1.ResourceRequirements {
	if in == nil {
		return nil
	}
	// resource.Quantity carries internal pointer state; DeepCopy each field so
	// the converted object cannot mutate the source.
	return &nwv1.ResourceRequirements{
		CPURequest:    in.CPURequest.DeepCopy(),
		CPULimit:      in.CPULimit.DeepCopy(),
		MemoryRequest: in.MemoryRequest.DeepCopy(),
		MemoryLimit:   in.MemoryLimit.DeepCopy(),
	}
}

func convertDurationPtr(in *metav1.Duration) *metav1.Duration {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func convertUninstall(in *Uninstall) *nwv1.Uninstall {
	if in == nil {
		return nil
	}
	out := nwv1.Uninstall(*in)
	return &out
}

func convertSkyhookStatus(in *SkyhookStatus, out *nwv1.NodeWrightStatus) {
	out.ObservedGeneration = in.ObservedGeneration
	out.Status = nwv1.Status(in.Status)
	out.NodeOrderOffset = in.NodeOrderOffset
	out.NodesInProgress = in.NodesInProgress
	out.CompleteNodes = in.CompleteNodes
	out.PackageList = in.PackageList

	out.NodeState = convertNodeStateMap(in.NodeState)
	out.NodeStatus = convertStatusMap(in.NodeStatus)
	out.Conditions = convertConditions(in.Conditions)
	out.NodeBootIds = copyStringMap(in.NodeBootIds)
	out.NodePriority = convertTimeMap(in.NodePriority)
	out.ConfigUpdates = convertStringSliceMap(in.ConfigUpdates)
	out.CompartmentStatuses = convertCompartmentStatuses(in.CompartmentStatuses)
}

func convertNodeStateMap(in map[string]NodeState) map[string]nwv1.NodeState {
	if in == nil {
		return nil
	}
	out := make(map[string]nwv1.NodeState, len(in))
	for k, v := range in {
		out[k] = convertNodeState(v)
	}
	return out
}

func convertNodeState(in NodeState) nwv1.NodeState {
	if in == nil {
		return nil
	}
	out := make(nwv1.NodeState, len(in))
	for k, v := range in {
		out[k] = convertPackageStatus(v)
	}
	return out
}

func convertPackageStatus(in PackageStatus) nwv1.PackageStatus {
	// Not a struct conversion: Stage and State are distinct named scalar types
	// across the two groups, so the fields must be converted individually.
	return nwv1.PackageStatus{
		Name:         in.Name,
		Version:      in.Version,
		Image:        in.Image,
		ContainerSHA: in.ContainerSHA,
		Stage:        nwv1.Stage(in.Stage),
		State:        nwv1.State(in.State),
		Restarts:     in.Restarts,

		InterruptPending: in.InterruptPending,
	}
}

func convertStatusMap(in map[string]Status) map[string]nwv1.Status {
	if in == nil {
		return nil
	}
	out := make(map[string]nwv1.Status, len(in))
	for k, v := range in {
		out[k] = nwv1.Status(v)
	}
	return out
}

func convertConditions(in []metav1.Condition) []metav1.Condition {
	if in == nil {
		return nil
	}
	out := make([]metav1.Condition, len(in))
	for i := range in {
		in[i].DeepCopyInto(&out[i])
	}
	return out
}

func convertTimeMap(in map[string]metav1.Time) map[string]metav1.Time {
	if in == nil {
		return nil
	}
	// metav1.Time is a value type; copying the map entries is a full copy.
	out := make(map[string]metav1.Time, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func convertStringSliceMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		if v == nil {
			out[k] = nil
			continue
		}
		s := make([]string, len(v))
		copy(s, v)
		out[k] = s
	}
	return out
}

func convertCompartmentStatuses(in map[string]CompartmentStatus) map[string]nwv1.CompartmentStatus {
	if in == nil {
		return nil
	}
	out := make(map[string]nwv1.CompartmentStatus, len(in))
	for k, v := range in {
		out[k] = convertCompartmentStatus(v)
	}
	return out
}

func convertCompartmentStatus(in CompartmentStatus) nwv1.CompartmentStatus {
	return nwv1.CompartmentStatus{
		Matched:         in.Matched,
		Ceiling:         in.Ceiling,
		InProgress:      in.InProgress,
		Completed:       in.Completed,
		ProgressPercent: in.ProgressPercent,
		BatchState:      convertBatchProcessingState(in.BatchState),
	}
}

func convertBatchProcessingState(in *BatchProcessingState) *nwv1.BatchProcessingState {
	if in == nil {
		return nil
	}
	out := nwv1.BatchProcessingState(*in)
	return &out
}

func convertDeploymentPolicySpec(in *DeploymentPolicySpec, out *nwv1.DeploymentPolicySpec) {
	out.Default = convertPolicyDefault(in.Default)
	out.Compartments = convertCompartments(in.Compartments)
	if in.ResetBatchStateOnCompletion != nil {
		v := *in.ResetBatchStateOnCompletion
		out.ResetBatchStateOnCompletion = &v
	}
}

func convertPolicyDefault(in PolicyDefault) nwv1.PolicyDefault {
	return nwv1.PolicyDefault{
		Budget:   convertDeploymentBudget(in.Budget),
		Strategy: convertDeploymentStrategy(in.Strategy),
	}
}

func convertCompartments(in []Compartment) []nwv1.Compartment {
	if in == nil {
		return nil
	}
	out := make([]nwv1.Compartment, len(in))
	for i := range in {
		out[i] = convertCompartment(in[i])
	}
	return out
}

func convertCompartment(in Compartment) nwv1.Compartment {
	return nwv1.Compartment{
		Name:     in.Name,
		Selector: *in.Selector.DeepCopy(),
		Budget:   convertDeploymentBudget(in.Budget),
		Strategy: convertDeploymentStrategy(in.Strategy),
	}
}

func convertDeploymentBudget(in DeploymentBudget) nwv1.DeploymentBudget {
	out := nwv1.DeploymentBudget{}
	if in.Percent != nil {
		v := *in.Percent
		out.Percent = &v
	}
	if in.Count != nil {
		v := *in.Count
		out.Count = &v
	}
	return out
}

func convertDeploymentStrategy(in *DeploymentStrategy) *nwv1.DeploymentStrategy {
	if in == nil {
		return nil
	}
	return &nwv1.DeploymentStrategy{
		Fixed:       convertFixedStrategy(in.Fixed),
		Linear:      convertLinearStrategy(in.Linear),
		Exponential: convertExponentialStrategy(in.Exponential),
	}
}

func convertFixedStrategy(in *FixedStrategy) *nwv1.FixedStrategy {
	if in == nil {
		return nil
	}
	out := &nwv1.FixedStrategy{}
	out.InitialBatch = copyIntPtr(in.InitialBatch)
	out.BatchThreshold = copyIntPtr(in.BatchThreshold)
	out.FailureThreshold = copyIntPtr(in.FailureThreshold)
	out.SafetyLimit = copyIntPtr(in.SafetyLimit)
	return out
}

func convertLinearStrategy(in *LinearStrategy) *nwv1.LinearStrategy {
	if in == nil {
		return nil
	}
	out := &nwv1.LinearStrategy{}
	out.InitialBatch = copyIntPtr(in.InitialBatch)
	out.Delta = copyIntPtr(in.Delta)
	out.BatchThreshold = copyIntPtr(in.BatchThreshold)
	out.FailureThreshold = copyIntPtr(in.FailureThreshold)
	out.SafetyLimit = copyIntPtr(in.SafetyLimit)
	return out
}

func convertExponentialStrategy(in *ExponentialStrategy) *nwv1.ExponentialStrategy {
	if in == nil {
		return nil
	}
	out := &nwv1.ExponentialStrategy{}
	out.InitialBatch = copyIntPtr(in.InitialBatch)
	out.GrowthFactor = copyIntPtr(in.GrowthFactor)
	out.BatchThreshold = copyIntPtr(in.BatchThreshold)
	out.FailureThreshold = copyIntPtr(in.FailureThreshold)
	out.SafetyLimit = copyIntPtr(in.SafetyLimit)
	return out
}

func copyIntPtr(in *int) *int {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
