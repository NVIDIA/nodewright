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
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/drain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ReadyConditionNodeListLimit caps condition message fan-out to avoid etcd object bloat and excess watch bandwidth on large rollouts.
	ReadyConditionNodeListLimit = 10

	SkyhookConditionReady                    = "Ready"
	SkyhookConditionTaintNotTolerable        = "TaintNotTolerable"
	SkyhookConditionNodesIgnored             = "NodesIgnored"
	SkyhookConditionApplyPackage             = "ApplyPackage"
	SkyhookConditionDeploymentPolicyNotFound = "DeploymentPolicyNotFound"
	SkyhookConditionBlocked                  = "Blocked"
	SkyhookConditionUninstallInProgress      = "UninstallInProgress"
	SkyhookConditionUninstallFailed          = "UninstallFailed"
	SkyhookConditionNodeStateMalformed       = "NodeStateMalformed"
	SkyhookConditionDeletionBlocked          = "DeletionBlocked"
	SkyhookConditionDrainBlocked             = "DrainBlocked"

	drainBlockedReasonPDB          = "PodDisruptionBudget"
	drainBlockedReasonUnmanagedPod = "UnmanagedPod"
	drainBlockedReasonEmptyDir     = "EmptyDirData"
	drainBlockedReasonMultiple     = "MultipleCauses"

	SkyhookReasonNonInterruptPodsRunning = "NonInterruptPodsRunning"

	skyhookReadyReasonNodesConverged = "NodesConverged"
	skyhookReadyReasonProgressing    = "Progressing"
	skyhookReadyReasonBlocked        = "Blocked"
	skyhookReadyReasonErroring       = "Erroring"
	skyhookReadyReasonPaused         = "Paused"
	skyhookReadyReasonWaiting        = "Waiting"
	skyhookReadyReasonDisabled       = "Disabled"
	skyhookReadyReasonUnknown        = "Unknown"

	LegacySkyhookConditionTransition = v1alpha1.METADATA_PREFIX + "/Transition"
)

func SkyhookReadyConditionReason(status v1alpha1.Status) string {
	switch status {
	case v1alpha1.StatusComplete:
		return skyhookReadyReasonNodesConverged
	case v1alpha1.StatusInProgress:
		return skyhookReadyReasonProgressing
	case v1alpha1.StatusBlocked:
		return skyhookReadyReasonBlocked
	case v1alpha1.StatusErroring:
		return skyhookReadyReasonErroring
	case v1alpha1.StatusPaused:
		return skyhookReadyReasonPaused
	case v1alpha1.StatusWaiting:
		return skyhookReadyReasonWaiting
	case v1alpha1.StatusDisabled:
		return skyhookReadyReasonDisabled
	default:
		return skyhookReadyReasonUnknown
	}
}

func LegacySkyhookConditionType(conditionType string) string {
	switch conditionType {
	case "":
		return ""
	case LegacySkyhookConditionTransition:
		return LegacySkyhookConditionTransition
	case SkyhookConditionTaintNotTolerable,
		SkyhookConditionNodesIgnored,
		SkyhookConditionApplyPackage,
		SkyhookConditionDeploymentPolicyNotFound:
		return fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, conditionType)
	}

	if strings.HasPrefix(conditionType, v1alpha1.METADATA_PREFIX+"/") {
		return conditionType
	}

	return fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, conditionType)
}

func AddSkyhookConditionWithLegacy(skyhook *Skyhook, condition metav1.Condition) bool {
	changed := AddSkyhookCondition(skyhook, condition)

	legacyType := LegacySkyhookConditionType(condition.Type)
	if legacyType == "" {
		return changed
	}

	legacyCondition := condition
	legacyCondition.Type = legacyType
	changed = AddSkyhookCondition(skyhook, legacyCondition) || changed
	return changed
}

func AddSkyhookCondition(skyhook *Skyhook, condition metav1.Condition) bool {
	condition = conditionWithStableTransitionTime(skyhook.Status.Conditions, condition)
	return addSkyhookCondition(skyhook, condition)
}

func AddSkyhookConditionRefreshingTransitionOnReasonOrMessage(skyhook *Skyhook, condition metav1.Condition) bool {
	condition = conditionWithStableTransitionTimeForReasonOrMessage(skyhook.Status.Conditions, condition)
	return addSkyhookCondition(skyhook, condition)
}

func conditionWithStableTransitionTime(conditions []metav1.Condition, condition metav1.Condition) metav1.Condition {
	if condition.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = metav1.Now()
	}

	for _, existing := range conditions {
		if existing.Type == condition.Type && existing.Status == condition.Status {
			condition.LastTransitionTime = existing.LastTransitionTime
			break
		}
	}

	return condition
}

func conditionWithStableTransitionTimeForReasonOrMessage(conditions []metav1.Condition, condition metav1.Condition) metav1.Condition {
	if condition.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = metav1.Now()
	}

	for _, existing := range conditions {
		if existing.Type == condition.Type &&
			existing.Status == condition.Status &&
			existing.Reason == condition.Reason &&
			existing.Message == condition.Message {
			condition.LastTransitionTime = existing.LastTransitionTime
			break
		}
	}

	return condition
}

func skyhookConditionsEqual(left, right metav1.Condition) bool {
	return left.Type == right.Type &&
		left.Status == right.Status &&
		left.ObservedGeneration == right.ObservedGeneration &&
		left.LastTransitionTime.Equal(&right.LastTransitionTime) &&
		left.Reason == right.Reason &&
		left.Message == right.Message
}

func addSkyhookCondition(skyhook *Skyhook, condition metav1.Condition) bool {
	conditions, changed := addOrUpdateSkyhookCondition(skyhook.Status.Conditions, condition)
	if !changed {
		return false
	}

	skyhook.Status.Conditions = conditions
	skyhook.Updated = true
	return true
}

func addOrUpdateSkyhookCondition(conditions []metav1.Condition, condition metav1.Condition) ([]metav1.Condition, bool) {
	if conditions == nil {
		conditions = make([]metav1.Condition, 0)
	}

	for i, existing := range conditions {
		if existing.Type != condition.Type {
			continue
		}

		if skyhookConditionsEqual(existing, condition) {
			return conditions, false
		}

		conditions[i] = condition
		return conditions, true
	}

	conditions = append(conditions, condition)
	return conditions, true
}

// removeSkyhookConditions removes any condition matching predicate.
// Returns true if the condition slice was modified.
func removeSkyhookConditions(skyhook *Skyhook, shouldRemove func(metav1.Condition) bool) bool {
	if len(skyhook.Status.Conditions) == 0 {
		return false
	}

	conditions := skyhook.Status.Conditions[:0]
	changed := false
	for _, condition := range skyhook.Status.Conditions {
		if shouldRemove(condition) {
			changed = true
			continue
		}
		conditions = append(conditions, condition)
	}

	if changed {
		skyhook.Status.Conditions = conditions
		skyhook.Updated = true
	}

	return changed
}

func RemoveSkyhookConditionTypes(skyhook *Skyhook, conditionTypes ...string) bool {
	remove := make(map[string]struct{}, len(conditionTypes))
	for _, conditionType := range conditionTypes {
		remove[conditionType] = struct{}{}
	}

	return removeSkyhookConditions(skyhook, func(condition metav1.Condition) bool {
		_, ok := remove[condition.Type]
		return ok
	})
}

// RemoveSkyhookConditionTypeAndReason removes any condition matching both conditionType and reason.
// Returns true if the condition slice was modified.
func RemoveSkyhookConditionTypeAndReason(skyhook *Skyhook, conditionType, reason string) bool {
	return removeSkyhookConditions(skyhook, func(condition metav1.Condition) bool {
		return condition.Type == conditionType && condition.Reason == reason
	})
}

func HasTrueSkyhookCondition(skyhook *Skyhook, conditionTypes ...string) bool {
	for _, condition := range skyhook.Status.Conditions {
		for _, conditionType := range conditionTypes {
			if condition.Type == conditionType && condition.Status == metav1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

func SkyhookReadyConditionStatusGroups(nodeStatuses map[string]v1alpha1.Status, sortedNodeNames []string) map[v1alpha1.Status][]string {
	byStatus := make(map[v1alpha1.Status][]string, len(v1alpha1.Statuses))
	for _, nodeName := range sortedNodeNames {
		status, ok := nodeStatuses[nodeName]
		if !ok {
			status = v1alpha1.StatusUnknown
		}
		byStatus[status] = append(byStatus[status], nodeName)
	}

	return byStatus
}

func SkyhookReadyConditionMessage(nodeStatuses map[string]v1alpha1.Status, sortedNodeNames []string) string {
	return skyhookReadyConditionMessageFromStatusGroups(
		SkyhookReadyConditionStatusGroups(nodeStatuses, sortedNodeNames),
		len(sortedNodeNames),
	)
}

func SkyhookReadyConditionMessageTruncated(byStatus map[v1alpha1.Status][]string) bool {
	for _, nodes := range byStatus {
		if len(nodes) > ReadyConditionNodeListLimit {
			return true
		}
	}
	return false
}

func skyhookReadyConditionMessageFromStatusGroups(byStatus map[v1alpha1.Status][]string, total int) string {
	complete := len(byStatus[v1alpha1.StatusComplete])
	parts := []string{fmt.Sprintf("%d/%d nodes complete%s", complete, total, FormatNodeList(byStatus[v1alpha1.StatusComplete]))}

	for _, status := range []v1alpha1.Status{
		v1alpha1.StatusInProgress,
		v1alpha1.StatusBlocked,
		v1alpha1.StatusErroring,
		v1alpha1.StatusWaiting,
		v1alpha1.StatusPaused,
		v1alpha1.StatusDisabled,
		v1alpha1.StatusUnknown,
	} {
		nodes := byStatus[status]
		if len(nodes) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s%s", len(nodes), nodeProgressStatusLabel(status), FormatNodeList(nodes)))
	}

	return strings.Join(parts, ", ")
}

func nodeProgressStatusLabel(status v1alpha1.Status) string {
	switch status {
	case v1alpha1.StatusInProgress:
		return "in progress"
	default:
		return string(status)
	}
}

func FormatNodeList(nodes []string) string {
	if len(nodes) == 0 {
		return ""
	}
	if len(nodes) > ReadyConditionNodeListLimit {
		return " (list truncated; see controller logs)"
	}
	return fmt.Sprintf(" (%s)", strings.Join(nodes, ", "))
}

// DrainBlockedNode is one node's drain blockers for the DrainBlocked condition
// message builder below — kept independent of the controller package's
// nodeDrainBlock so wrapper has no import cycle back to controller.
type DrainBlockedNode struct {
	NodeName string
	Blocked  []drain.BlockedPod
}

// DrainBlockedConditionReason picks the condition Reason from the set of block
// reasons observed this pass. MultipleCauses covers both "one node has two kinds
// of blocker" and "different nodes are blocked for different reasons".
func DrainBlockedConditionReason(nodes []DrainBlockedNode) string {
	seen := make(map[drain.BlockReason]struct{})
	for _, n := range nodes {
		for _, b := range n.Blocked {
			seen[b.Reason] = struct{}{}
		}
	}
	if len(seen) != 1 {
		return drainBlockedReasonMultiple
	}
	for reason := range seen {
		switch reason {
		case drain.BlockReasonPodDisruptionBudget:
			return drainBlockedReasonPDB
		case drain.BlockReasonUnmanagedPod:
			return drainBlockedReasonUnmanagedPod
		case drain.BlockReasonEmptyDirData:
			return drainBlockedReasonEmptyDir
		}
	}
	return drainBlockedReasonMultiple
}

// DrainBlockedConditionMessage renders the aggregate DrainBlocked message: a
// "N/total nodes blocked draining (names)" summary line — following the same
// truncation idiom as the Ready condition — followed by one "<ns>/<pod> on
// <node>: <verbatim detail>" line per blocked pod that carries a Detail (PDB
// cases only; Detail is apiserver prose and is never altered).
func DrainBlockedConditionMessage(nodes []DrainBlockedNode, totalSelected int) string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.NodeName)
	}
	sort.Strings(names)

	lines := []string{fmt.Sprintf("%d/%d nodes blocked draining%s", len(nodes), totalSelected, formatNodeList(names))}

	for _, n := range nodes {
		for _, b := range n.Blocked {
			if b.Detail == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s/%s on %s: %s", b.Namespace, b.Name, n.NodeName, b.Detail))
		}
	}

	return strings.Join(lines, "; ")
}
