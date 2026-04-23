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

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	readyConditionNodeListLimit = 10

	skyhookConditionReady                    = "Ready"
	skyhookConditionTaintNotTolerable        = "TaintNotTolerable"
	skyhookConditionNodesIgnored             = "NodesIgnored"
	skyhookConditionApplyPackage             = "ApplyPackage"
	skyhookConditionDeploymentPolicyNotFound = "DeploymentPolicyNotFound"

	skyhookReadyReasonNodesConverged = "NodesConverged"
	skyhookReadyReasonProgressing    = "Progressing"
	skyhookReadyReasonBlocked        = "Blocked"
	skyhookReadyReasonErroring       = "Erroring"
	skyhookReadyReasonPaused         = "Paused"
	skyhookReadyReasonWaiting        = "Waiting"
	skyhookReadyReasonDisabled       = "Disabled"
	skyhookReadyReasonUnknown        = "Unknown"

	legacySkyhookConditionTransition = v1alpha1.METADATA_PREFIX + "/Transition"
)

func skyhookReadyConditionReason(status v1alpha1.Status) string {
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

func legacySkyhookConditionType(conditionType string) string {
	switch conditionType {
	case skyhookConditionReady:
		return legacySkyhookConditionTransition
	case skyhookConditionTaintNotTolerable,
		skyhookConditionNodesIgnored,
		skyhookConditionApplyPackage,
		skyhookConditionDeploymentPolicyNotFound:
		return fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, conditionType)
	default:
		return ""
	}
}

func addSkyhookConditionWithLegacy(skyhook *wrapper.Skyhook, condition metav1.Condition) bool {
	changed := addSkyhookCondition(skyhook, condition)

	legacyType := legacySkyhookConditionType(condition.Type)
	if legacyType == "" {
		return changed
	}

	legacyCondition := condition
	legacyCondition.Type = legacyType
	changed = addSkyhookCondition(skyhook, legacyCondition) || changed
	return changed
}

func addSkyhookCondition(skyhook *wrapper.Skyhook, condition metav1.Condition) bool {
	condition = conditionWithStableTransitionTime(skyhook.Status.Conditions, condition)

	var existing metav1.Condition
	found := false
	for _, candidate := range skyhook.Status.Conditions {
		if candidate.Type == condition.Type {
			existing = candidate
			found = true
			break
		}
	}

	if found && skyhookConditionsEqual(existing, condition) {
		return false
	}

	skyhook.AddCondition(condition)
	return true
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

func removeSkyhookConditionTypes(skyhook *wrapper.Skyhook, conditionTypes ...string) bool {
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

func hasTrueSkyhookCondition(skyhook *wrapper.Skyhook, conditionTypes ...string) bool {
	for _, condition := range skyhook.Status.Conditions {
		for _, conditionType := range conditionTypes {
			if condition.Type == conditionType && condition.Status == metav1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

func skyhookReadyConditionMessage(s *skyhookNodes) string {
	nodeStatuses := make(map[string]v1alpha1.Status, len(s.skyhook.Status.NodeStatus))
	for nodeName, status := range s.skyhook.Status.NodeStatus {
		nodeStatuses[nodeName] = status
	}

	for _, node := range s.nodes {
		nodeName := node.GetNode().Name
		if _, ok := nodeStatuses[nodeName]; !ok {
			nodeStatuses[nodeName] = v1alpha1.StatusUnknown
		}
	}

	nodeNames := make([]string, 0, len(nodeStatuses))
	for nodeName := range nodeStatuses {
		nodeNames = append(nodeNames, nodeName)
	}
	sort.Strings(nodeNames)

	byStatus := make(map[v1alpha1.Status][]string, len(v1alpha1.Statuses))
	for _, nodeName := range nodeNames {
		status := nodeStatuses[nodeName]
		byStatus[status] = append(byStatus[status], nodeName)
	}

	complete := len(byStatus[v1alpha1.StatusComplete])
	total := len(nodeNames)
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
	displayNodes := nodes
	suffix := ""
	if len(nodes) > readyConditionNodeListLimit {
		displayNodes = nodes[:readyConditionNodeListLimit]
		suffix = fmt.Sprintf(", +%d more", len(nodes)-readyConditionNodeListLimit)
	}
	return fmt.Sprintf(" (%s%s)", strings.Join(displayNodes, ", "), suffix)
}
