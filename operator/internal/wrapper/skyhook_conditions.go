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
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
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
	switch {
	case conditionType == "":
		return ""
	case strings.HasPrefix(conditionType, v1alpha1.METADATA_PREFIX+"/"):
		return ""
	default:
		return fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, conditionType)
	}
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
	return addOrUpdateSkyhookCondition(skyhook, condition)
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

func skyhookConditionsEqual(left, right metav1.Condition) bool {
	return left.Type == right.Type &&
		left.Status == right.Status &&
		left.ObservedGeneration == right.ObservedGeneration &&
		left.LastTransitionTime.Equal(&right.LastTransitionTime) &&
		left.Reason == right.Reason &&
		left.Message == right.Message
}

func addOrUpdateSkyhookCondition(skyhook *Skyhook, condition metav1.Condition) bool {
	if skyhook.Status.Conditions == nil {
		skyhook.Status.Conditions = make([]metav1.Condition, 0)
	}

	for i, existing := range skyhook.Status.Conditions {
		if existing.Type != condition.Type {
			continue
		}

		if skyhookConditionsEqual(existing, condition) {
			return false
		}

		skyhook.Status.Conditions[i] = condition
		skyhook.Updated = true
		return true
	}

	skyhook.Status.Conditions = append(skyhook.Status.Conditions, condition)
	skyhook.Updated = true
	return true
}

func RemoveSkyhookConditionTypes(skyhook *Skyhook, conditionTypes ...string) bool {
	if len(skyhook.Status.Conditions) == 0 {
		return false
	}

	remove := make(map[string]struct{}, len(conditionTypes))
	for _, conditionType := range conditionTypes {
		remove[conditionType] = struct{}{}
	}

	conditions := skyhook.Status.Conditions[:0]
	changed := false
	for _, condition := range skyhook.Status.Conditions {
		if _, ok := remove[condition.Type]; ok {
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
	parts := []string{fmt.Sprintf("%d/%d nodes complete%s", complete, total, formatNodeList(byStatus[v1alpha1.StatusComplete]))}

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
		parts = append(parts, fmt.Sprintf("%d %s%s", len(nodes), nodeProgressStatusLabel(status), formatNodeList(nodes)))
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

func formatNodeList(nodes []string) string {
	if len(nodes) == 0 {
		return ""
	}
	if len(nodes) > ReadyConditionNodeListLimit {
		return " (list truncated; see controller logs)"
	}
	return fmt.Sprintf(" (%s)", strings.Join(nodes, ", "))
}
