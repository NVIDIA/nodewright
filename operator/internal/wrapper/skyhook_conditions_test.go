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
	"time"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Skyhook condition helpers", func() {
	DescribeTable("skyhookConditionsEqual", func(mutate func(*metav1.Condition), expected bool) {
		left := metav1.Condition{
			Type:               SkyhookConditionReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 7,
			LastTransitionTime: metav1.NewTime(time.Unix(123, 0)),
			Reason:             "NodesConverged",
			Message:            "All nodes converged",
		}
		right := left
		if mutate != nil {
			mutate(&right)
		}

		Expect(skyhookConditionsEqual(left, right)).To(Equal(expected))
	},
		Entry("returns true for identical conditions", nil, true),
		Entry("returns false when type differs", func(condition *metav1.Condition) {
			condition.Type = SkyhookConditionNodesIgnored
		}, false),
		Entry("returns false when status differs", func(condition *metav1.Condition) {
			condition.Status = metav1.ConditionFalse
		}, false),
		Entry("returns false when observed generation differs", func(condition *metav1.Condition) {
			condition.ObservedGeneration = 8
		}, false),
		Entry("returns false when transition time differs", func(condition *metav1.Condition) {
			condition.LastTransitionTime = metav1.NewTime(time.Unix(456, 0))
		}, false),
		Entry("returns false when reason differs", func(condition *metav1.Condition) {
			condition.Reason = "Progressing"
		}, false),
		Entry("returns false when message differs", func(condition *metav1.Condition) {
			condition.Message = "Still converging"
		}, false),
	)

	DescribeTable("conditionWithStableTransitionTime", func(lastTransitionTime metav1.Time, assertTransitionTime func(updated metav1.Condition, existingTime metav1.Time)) {
		existingTime := metav1.NewTime(time.Unix(789, 0))
		updated := conditionWithStableTransitionTime([]metav1.Condition{
			{
				Type:               SkyhookConditionReady,
				Status:             metav1.ConditionFalse,
				LastTransitionTime: existingTime,
			},
		}, metav1.Condition{
			Type:               SkyhookConditionReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: lastTransitionTime,
		})

		assertTransitionTime(updated, existingTime)
	},
		Entry("keeps the provided transition time when status changes", metav1.NewTime(time.Unix(999, 0)), func(updated metav1.Condition, existingTime metav1.Time) {
			Expect(updated.LastTransitionTime).To(Equal(metav1.NewTime(time.Unix(999, 0))))
			Expect(updated.LastTransitionTime).NotTo(Equal(existingTime))
		}),
		Entry("sets a fresh transition time when status changes and none is provided", metav1.Time{}, func(updated metav1.Condition, existingTime metav1.Time) {
			Expect(updated.LastTransitionTime.IsZero()).To(BeFalse())
			Expect(updated.LastTransitionTime).NotTo(Equal(existingTime))
		}),
	)

	DescribeTable("removeSkyhookConditionTypes", func(existing []metav1.Condition, conditionTypes []string, expectedTypes []string, changed bool) {
		skyhook := &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					Conditions: existing,
				},
			},
		}

		Expect(RemoveSkyhookConditionTypes(skyhook, conditionTypes...)).To(Equal(changed))

		actualTypes := make([]string, 0, len(skyhook.Status.Conditions))
		for _, condition := range skyhook.Status.Conditions {
			actualTypes = append(actualTypes, condition.Type)
		}
		Expect(actualTypes).To(Equal(expectedTypes))
		Expect(skyhook.Updated).To(Equal(changed))
	},
		Entry("returns false when there are no conditions", nil, []string{SkyhookConditionReady}, []string{}, false),
		Entry("returns false when no condition types match", []metav1.Condition{
			{Type: SkyhookConditionReady},
		}, []string{SkyhookConditionNodesIgnored}, []string{SkyhookConditionReady}, false),
		Entry("removes multiple matching condition types", []metav1.Condition{
			{Type: SkyhookConditionReady},
			{Type: SkyhookConditionNodesIgnored},
			{Type: SkyhookConditionApplyPackage},
			{Type: SkyhookConditionTaintNotTolerable},
		}, []string{SkyhookConditionNodesIgnored, SkyhookConditionApplyPackage}, []string{SkyhookConditionReady, SkyhookConditionTaintNotTolerable}, true),
	)

	DescribeTable("hasTrueSkyhookCondition", func(conditions []metav1.Condition, conditionTypes []string, expected bool) {
		skyhook := &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					Conditions: conditions,
				},
			},
		}

		Expect(HasTrueSkyhookCondition(skyhook, conditionTypes...)).To(Equal(expected))
	},
		Entry("returns false for a false condition", []metav1.Condition{
			{Type: SkyhookConditionReady, Status: metav1.ConditionFalse},
		}, []string{SkyhookConditionReady}, false),
		Entry("returns false for an unknown condition", []metav1.Condition{
			{Type: SkyhookConditionReady, Status: metav1.ConditionUnknown},
		}, []string{SkyhookConditionReady}, false),
		Entry("returns true when one of multiple requested condition types is true", []metav1.Condition{
			{Type: SkyhookConditionNodesIgnored, Status: metav1.ConditionFalse},
			{Type: SkyhookConditionApplyPackage, Status: metav1.ConditionTrue},
		}, []string{SkyhookConditionNodesIgnored, SkyhookConditionApplyPackage}, true),
	)

	DescribeTable("legacySkyhookConditionType", func(conditionType, expected string) {
		Expect(LegacySkyhookConditionType(conditionType)).To(Equal(expected))
	},
		Entry("maps Ready to the legacy metadata prefix", SkyhookConditionReady, fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionReady)),
		Entry("maps TaintNotTolerable to the legacy metadata prefix", SkyhookConditionTaintNotTolerable, fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionTaintNotTolerable)),
		Entry("maps NodesIgnored to the legacy metadata prefix", SkyhookConditionNodesIgnored, fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionNodesIgnored)),
		Entry("maps ApplyPackage to the legacy metadata prefix", SkyhookConditionApplyPackage, fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionApplyPackage)),
		Entry("maps DeploymentPolicyNotFound to the legacy metadata prefix", SkyhookConditionDeploymentPolicyNotFound, fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionDeploymentPolicyNotFound)),
		Entry("keeps already-prefixed condition types unchanged", fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionReady), fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, SkyhookConditionReady)),
		Entry("mirrors unknown condition types by default", "SomeNewCondition", fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, "SomeNewCondition")),
	)

	It("truncates formatted node lists beyond the display limit", func() {
		nodes := make([]string, 0, ReadyConditionNodeListLimit+2)
		for i := 1; i <= ReadyConditionNodeListLimit+2; i++ {
			nodes = append(nodes, fmt.Sprintf("node-%02d", i))
		}

		Expect(formatNodeList(nodes)).To(Equal(" (list truncated; see controller logs)"))
	})
})
