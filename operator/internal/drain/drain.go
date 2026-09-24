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

package drain

import (
	"fmt"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MirrorPodAnnotationKey = "kubernetes.io/config.mirror"

	ReasonPhase                   = "phase"
	ReasonTerminating             = "terminating"
	ReasonSkyhookPackage          = "skyhook-package"
	ReasonUnschedulableToleration = "unschedulable-toleration"
	ReasonDaemonSet               = "daemonset"
	ReasonKubeSystem              = "kube-system"
	ReasonMirrorPod               = "mirror-pod"
	ReasonUnmanaged               = "unmanaged"
	ReasonEmptyDir                = "emptydir"
	ReasonEviction                = "eviction"
	ReasonDelete                  = "delete"
)

type Action string

const (
	ActionIgnore Action = "ignore"
	ActionBlock  Action = "block"
	ActionEvict  Action = "evict"
	ActionDelete Action = "delete"
)

type Options struct {
	DisableEviction    bool
	DeleteEmptyDirData bool
	Force              bool
	IgnoreDaemonSets   bool
	GracePeriodSeconds *int64
	// PackageNamespace is the namespace the operator creates package pods in.
	// It is operator identity, not user configuration, so callers set it
	// directly rather than through DrainConfig. When empty, the label-based
	// package-pod exemption is disabled.
	PackageNamespace string
}

type BlockReason string

const (
	BlockReasonPodDisruptionBudget BlockReason = "PodDisruptionBudget"
	BlockReasonUnmanagedPod        BlockReason = "UnmanagedPod"
	BlockReasonEmptyDirData        BlockReason = "EmptyDirData"
)

// BlockedPod is one pod currently preventing a node's drain from completing,
// with enough context to render a DrainBlocked condition message.
type BlockedPod struct {
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	Reason    BlockReason `json:"reason"`
	// Detail is apiserver-generated prose (e.g. the PDB cause message) and is
	// copied verbatim — it is not a stable contract, so never parsed.
	Detail string `json:"detail,omitempty"`
}

// DrainResult is what DrainNode reports back: whether the node is fully
// drained, and — if not — which pods are blocking it and why.
type DrainResult struct {
	Ready   bool
	Blocked []BlockedPod
}

func DefaultOptions() Options {
	return Options{
		DeleteEmptyDirData: true,
		Force:              true,
		IgnoreDaemonSets:   true,
	}
}

func OptionsFromConfig(config *v1alpha1.DrainConfig) Options {
	options := DefaultOptions()
	if config == nil {
		return options
	}

	if config.DisableEviction != nil {
		options.DisableEviction = *config.DisableEviction
	}
	if config.DeleteEmptyDirData != nil {
		options.DeleteEmptyDirData = *config.DeleteEmptyDirData
	}
	if config.Force != nil {
		options.Force = *config.Force
	}
	if config.IgnoreDaemonSets != nil {
		options.IgnoreDaemonSets = *config.IgnoreDaemonSets
	}
	if config.GracePeriod != nil {
		seconds := int64(config.GracePeriod.Duration / time.Second)
		if config.GracePeriod.Duration%time.Second != 0 {
			seconds++
		}
		options.GracePeriodSeconds = &seconds
	}

	return options
}

func TimedOut(startedAt *metav1.Time, timeout *metav1.Duration, now time.Time) bool {
	if startedAt == nil || timeout == nil || timeout.Duration == 0 {
		return false
	}
	return !now.Before(startedAt.Add(timeout.Duration))
}

func (o Options) DeleteOptions() []client.DeleteOption {
	if o.GracePeriodSeconds == nil {
		return nil
	}
	return []client.DeleteOption{client.GracePeriodSeconds(*o.GracePeriodSeconds)}
}

func (o Options) EvictionDeleteOptions() *metav1.DeleteOptions {
	if o.GracePeriodSeconds == nil {
		return nil
	}
	return &metav1.DeleteOptions{GracePeriodSeconds: o.GracePeriodSeconds}
}

type Decision struct {
	Action Action
	Reason string
}

func (d Decision) BlocksDrain() bool {
	return d.Action != ActionIgnore
}

func (d Decision) RequiresAction() bool {
	return d.Action == ActionEvict || d.Action == ActionDelete
}

func DecidePod(pod *corev1.Pod, options Options) Decision {
	if pod == nil {
		return Decision{Action: ActionIgnore, Reason: ReasonPhase}
	}

	switch pod.Status.Phase {
	case corev1.PodRunning, corev1.PodPending:
	default:
		return Decision{Action: ActionIgnore, Reason: ReasonPhase}
	}

	// Package pods normally dodge drain via toleratesUnschedulable below, but
	// some admission controllers rewrite or strip pod tolerations, so the
	// operator's own in-flight pods are also recognized by the labels stamped
	// on every package pod. The check is scoped to the operator's namespace:
	// the labels alone could be copied onto any pod, while creating pods in
	// the operator's namespace requires RBAC there.
	if isSkyhookPackagePod(pod, options.PackageNamespace) {
		return Decision{Action: ActionIgnore, Reason: ReasonSkyhookPackage}
	}

	if toleratesUnschedulable(pod) {
		return Decision{Action: ActionIgnore, Reason: ReasonUnschedulableToleration}
	}

	controllerRef := metav1.GetControllerOf(pod)
	if options.IgnoreDaemonSets && controllerRef != nil && controllerRef.Kind == "DaemonSet" {
		return Decision{Action: ActionIgnore, Reason: ReasonDaemonSet}
	}

	if pod.Namespace == "kube-system" {
		return Decision{Action: ActionIgnore, Reason: ReasonKubeSystem}
	}

	if isMirrorPod(pod) {
		return Decision{Action: ActionIgnore, Reason: ReasonMirrorPod}
	}

	// A pod that has already accepted its eviction still holds the node's resources
	// until the kubelet finishes killing it, so it blocks rather than being ignored:
	// drain is done when the workloads are gone, not when the evictions were accepted.
	// This sits below the exemptions above so that drain only ever waits on pods it
	// would have selected for eviction or deletion itself, which is what kubectl drain
	// waits for.
	if pod.DeletionTimestamp != nil {
		return Decision{Action: ActionBlock, Reason: ReasonTerminating}
	}

	if controllerRef == nil && !options.Force {
		return Decision{Action: ActionBlock, Reason: ReasonUnmanaged}
	}

	if hasEmptyDir(pod) && !options.DeleteEmptyDirData {
		return Decision{Action: ActionBlock, Reason: ReasonEmptyDir}
	}

	if options.DisableEviction {
		return Decision{Action: ActionDelete, Reason: ReasonDelete}
	}

	return Decision{Action: ActionEvict, Reason: ReasonEviction}
}

func isSkyhookPackagePod(pod *corev1.Pod, packageNamespace string) bool {
	if packageNamespace == "" || pod.Namespace != packageNamespace {
		return false
	}
	_, hasName := pod.Labels[fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX)]
	_, hasPackage := pod.Labels[fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)]
	return hasName && hasPackage
}

func toleratesUnschedulable(pod *corev1.Pod) bool {
	unschedulableTaint := corev1.Taint{
		Key:    corev1.TaintNodeUnschedulable,
		Effect: corev1.TaintEffectNoSchedule,
	}
	for _, toleration := range pod.Spec.Tolerations {
		if toleration.ToleratesTaint(klog.Background(), &unschedulableTaint, false) {
			return true
		}
	}
	return false
}

func isMirrorPod(pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		return false
	}
	_, ok := pod.Annotations[MirrorPodAnnotationKey]
	return ok
}

func hasEmptyDir(pod *corev1.Pod) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil {
			return true
		}
	}
	return false
}
