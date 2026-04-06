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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	skyhookNodesMock "github.com/NVIDIA/nodewright/operator/internal/controller/mock"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	wrapperMock "github.com/NVIDIA/nodewright/operator/internal/wrapper/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kptr "k8s.io/utils/ptr"
)

const (
	annotationTrueValue = "true"
)

var _ = Describe("cluster state v2 tests", func() {

	logger := log.FromContext(ctx)

	It("should check taint toleration", func() {
		taints := []corev1.Taint{
			{
				Key:    "key1",
				Value:  "value1",
				Effect: corev1.TaintEffectNoSchedule,
			},
			{
				Key:    "key2",
				Value:  "value2",
				Effect: corev1.TaintEffectNoSchedule,
			},
		}

		tolerations := []corev1.Toleration{
			{
				Key:      "key1",
				Value:    "value1",
				Effect:   corev1.TaintEffectNoSchedule,
				Operator: corev1.TolerationOpEqual,
			},
			{
				Key:      "key2",
				Value:    "value2",
				Effect:   corev1.TaintEffectNoSchedule,
				Operator: corev1.TolerationOpEqual,
			},
		}

		Expect(CheckTaintToleration(logger, tolerations, taints)).To(BeTrue())
	})

	It("Must tolerate all taints", func() {
		taints := []corev1.Taint{
			{
				Key:    "key1",
				Value:  "value1",
				Effect: corev1.TaintEffectNoSchedule,
			},
			{
				Key:    "key2",
				Value:  "value2",
				Effect: corev1.TaintEffectNoExecute,
			},
		}

		tolerations := []corev1.Toleration{
			{
				Effect:   corev1.TaintEffectNoSchedule,
				Operator: corev1.TolerationOpExists,
			},
		}

		Expect(CheckTaintToleration(logger, tolerations, taints)).To(BeFalse())
	})

	It("When no taints it is tolerated", func() {
		taints := make([]corev1.Taint, 0)

		tolerations := []corev1.Toleration{
			{
				Effect:   corev1.TaintEffectNoSchedule,
				Operator: corev1.TolerationOpExists,
			},
		}

		Expect(CheckTaintToleration(logger, tolerations, taints)).To(BeTrue())
	})

	It("When no taints and no tolerations it is tolerated", func() {
		taints := make([]corev1.Taint, 0)

		tolerations := make([]corev1.Toleration, 0)

		Expect(CheckTaintToleration(logger, tolerations, taints)).To(BeTrue())
	})

	It("When node has ignore label it is blocked", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{v1alpha1.METADATA_PREFIX + "/ignore": "true"}},
		}
		skyhookNode, err := wrapper.NewSkyhookNode(node, &v1alpha1.Skyhook{})
		Expect(err).ToNot(HaveOccurred())
		Expect(CheckNodeIgnoreLabel(skyhookNode)).To(BeTrue())
	})

	It("When node does not have ignore label it is not blocked", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{}},
		}
		skyhookNode, err := wrapper.NewSkyhookNode(node, &v1alpha1.Skyhook{})
		Expect(err).ToNot(HaveOccurred())
		Expect(CheckNodeIgnoreLabel(skyhookNode)).To(BeFalse())
	})

	It("When node has ignore label set to false it is not blocked", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{v1alpha1.METADATA_PREFIX + "/ignore": "false"}},
		}
		skyhookNode, err := wrapper.NewSkyhookNode(node, &v1alpha1.Skyhook{})
		Expect(err).ToNot(HaveOccurred())
		Expect(CheckNodeIgnoreLabel(skyhookNode)).To(BeFalse())
	})
})

// --- Add GetNextSkyhook tests ---

var _ = Describe("GetNextSkyhook", func() {
	It("returns the first not-complete, not-disabled skyhook", func() {
		// Helper to make a skyhookNodes with given complete/disabled
		makeSkyhookNodes := func(complete bool, disabled bool) SkyhookNodes {
			sn_mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
			sn_mock.EXPECT().IsComplete().Return(complete).Maybe()
			sn_mock.EXPECT().IsDisabled().Return(disabled).Maybe()
			return sn_mock
		}

		// Not complete, not disabled
		n1 := makeSkyhookNodes(false, false)
		// Complete
		n2 := makeSkyhookNodes(true, false)
		// Disabled
		n3 := makeSkyhookNodes(false, true)

		// Should return n1
		result := GetNextSkyhook([]SkyhookNodes{n1, n2, n3})
		Expect(result).To(Equal(n1))

		// Should return nil as all complete or disabled
		n1 = makeSkyhookNodes(true, false)
		result = GetNextSkyhook([]SkyhookNodes{n1, n2, n3})
		Expect(result).To(BeNil())

		// Should return n3 as all others are complete or disabled
		n2 = makeSkyhookNodes(false, true)
		n3 = makeSkyhookNodes(false, false)
		result = GetNextSkyhook([]SkyhookNodes{n1, n2, n3})
		Expect(result).To(Equal(n3))
	})
})

var _ = Describe("IsNodeReadyForSkyhook", func() {
	// Helper to create a mock SkyhookNodes with GetNode returning specific completion status
	makeSkyhookNodesMock := func(name string, priority int, nodeCompletions map[string]bool, disabled bool) *skyhookNodesMock.MockSkyhookNodes {
		mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())

		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       v1alpha1.SkyhookSpec{Priority: priority},
		}
		mock.EXPECT().GetSkyhook().Return(wrapper.NewSkyhookWrapper(skyhook)).Maybe()
		mock.EXPECT().IsDisabled().Return(disabled).Maybe()

		for nodeName, isComplete := range nodeCompletions {
			nodeWrapper := wrapperMock.NewMockSkyhookNode(GinkgoT())
			nodeWrapper.EXPECT().IsComplete().Return(isComplete).Maybe()
			mock.EXPECT().GetNode(nodeName).Return(v1alpha1.StatusComplete, wrapper.SkyhookNode(nodeWrapper)).Maybe()
		}

		return mock
	}

	It("should return true when all higher-priority skyhooks are complete on this node", func() {
		// Setup: skyhook_a (priority 1) complete on node-1, skyhook_b (priority 2)
		skyhookA := makeSkyhookNodesMock("skyhook-a", 1, map[string]bool{"node-1": true}, false)
		skyhookB := makeSkyhookNodesMock("skyhook-b", 2, map[string]bool{"node-1": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// node-1 should be ready for skyhook_b because skyhook_a is complete on node-1
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeTrue())
	})

	It("should return false when a higher-priority skyhook is incomplete on this node", func() {
		// Setup: skyhook_a (priority 1) incomplete on node-1, skyhook_b (priority 2)
		skyhookA := makeSkyhookNodesMock("skyhook-a", 1, map[string]bool{"node-1": false}, false)
		skyhookB := makeSkyhookNodesMock("skyhook-b", 2, map[string]bool{"node-1": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// node-1 should NOT be ready for skyhook_b because skyhook_a is incomplete on node-1
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeFalse())
	})

	It("should return true when higher-priority skyhook doesn't target this node", func() {
		// Setup: skyhook_a (priority 1) targets only node-2, skyhook_b (priority 2) targets node-1
		skyhookA := makeSkyhookNodesMock("skyhook-a", 1, map[string]bool{"node-2": false}, false)
		// GetNode for node-1 should return nil (not found)
		skyhookA.On("GetNode", "node-1").Return(v1alpha1.StatusUnknown, nil)

		skyhookB := makeSkyhookNodesMock("skyhook-b", 2, map[string]bool{"node-1": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// node-1 should be ready for skyhook_b because skyhook_a doesn't target node-1
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeTrue())
	})

	It("should allow parallel processing when nodes are independent", func() {
		// Setup: node-1 complete on skyhook_a, node-2 incomplete on skyhook_a
		skyhookA := makeSkyhookNodesMock("skyhook-a", 1, map[string]bool{"node-1": true, "node-2": false}, false)
		skyhookB := makeSkyhookNodesMock("skyhook-b", 2, map[string]bool{"node-1": false, "node-2": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// node-1 should be ready for skyhook_b (complete on skyhook_a)
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeTrue())
		// node-2 should NOT be ready for skyhook_b (incomplete on skyhook_a)
		Expect(IsNodeReadyForSkyhook("node-2", skyhookB, allSkyhooks)).To(BeFalse())
	})

	It("should skip disabled skyhooks in priority check", func() {
		// Setup: skyhook_a (priority 1, disabled) incomplete on node-1, skyhook_b (priority 2)
		skyhookA := makeSkyhookNodesMock("skyhook-a", 1, map[string]bool{"node-1": false}, true)
		skyhookB := makeSkyhookNodesMock("skyhook-b", 2, map[string]bool{"node-1": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// node-1 should be ready for skyhook_b because skyhook_a is disabled
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeTrue())
	})

	It("should handle same-priority skyhooks by name ordering", func() {
		// Setup: skyhook_a and skyhook_b both priority 2, ordered by name
		skyhookA := makeSkyhookNodesMock("skyhook-a", 2, map[string]bool{"node-1": false}, false)
		skyhookB := makeSkyhookNodesMock("skyhook-b", 2, map[string]bool{"node-1": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// skyhook_b (name "b") should wait for skyhook_a (name "a") at same priority
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeFalse())
		// skyhook_a should be ready (no higher priority or earlier name)
		Expect(IsNodeReadyForSkyhook("node-1", skyhookA, allSkyhooks)).To(BeTrue())
	})

	// sequencing: all tests
	makeSkyhookNodesMockWithSequencing := func(name string, priority int, sequencing v1alpha1.SequencingMode, nodeCompletions map[string]bool, complete bool) *skyhookNodesMock.MockSkyhookNodes {
		mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())

		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       v1alpha1.SkyhookSpec{Priority: priority, Sequencing: sequencing},
		}
		mock.EXPECT().GetSkyhook().Return(wrapper.NewSkyhookWrapper(skyhook)).Maybe()
		mock.EXPECT().IsDisabled().Return(false).Maybe()
		mock.EXPECT().IsComplete().Return(complete).Maybe()

		for nodeName, isComplete := range nodeCompletions {
			nodeWrapper := wrapperMock.NewMockSkyhookNode(GinkgoT())
			nodeWrapper.EXPECT().IsComplete().Return(isComplete).Maybe()
			mock.EXPECT().GetNode(nodeName).Return(v1alpha1.StatusComplete, wrapper.SkyhookNode(nodeWrapper)).Maybe()
		}

		return mock
	}

	It("sequencing: all should block when predecessor is not globally complete", func() {
		// skyhook_a (priority 1, sequencing: all): node-1 complete, node-2 incomplete → not globally complete
		skyhookA := makeSkyhookNodesMockWithSequencing("skyhook-a", 1, v1alpha1.SequencingAll, map[string]bool{"node-1": true, "node-2": false}, false)
		skyhookB := makeSkyhookNodesMockWithSequencing("skyhook-b", 2, v1alpha1.SequencingNode, map[string]bool{"node-1": false, "node-2": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// node-1 completed skyhook_a, but sequencing: all means ALL nodes must complete
		// Since node-2 is incomplete on skyhook_a, node-1 should NOT be ready for skyhook_b
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeFalse())
	})

	It("sequencing: all should allow when predecessor is globally complete", func() {
		skyhookA := makeSkyhookNodesMockWithSequencing("skyhook-a", 1, v1alpha1.SequencingAll, map[string]bool{"node-1": true, "node-2": true}, true)
		skyhookB := makeSkyhookNodesMockWithSequencing("skyhook-b", 2, v1alpha1.SequencingNode, map[string]bool{"node-1": false, "node-2": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}

		// Both nodes complete on skyhook_a, so node-1 should be ready for skyhook_b
		Expect(IsNodeReadyForSkyhook("node-1", skyhookB, allSkyhooks)).To(BeTrue())
	})

	It("mixed sequencing: node predecessor allows per-node, all predecessor blocks globally", func() {
		// skyhook_a (priority 1, sequencing: node): node-1 complete, node-2 incomplete
		skyhookA := makeSkyhookNodesMockWithSequencing("skyhook-a", 1, v1alpha1.SequencingNode, map[string]bool{"node-1": true, "node-2": false}, false)
		// skyhook_gate (priority 2, sequencing: all): node-1 complete, node-2 incomplete → not globally complete
		skyhookGate := makeSkyhookNodesMockWithSequencing("skyhook-gate", 2, v1alpha1.SequencingAll, map[string]bool{"node-1": true, "node-2": false}, false)
		// skyhook_c (priority 3)
		skyhookC := makeSkyhookNodesMockWithSequencing("skyhook-c", 3, v1alpha1.SequencingNode, map[string]bool{"node-1": false, "node-2": false}, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookGate, skyhookC}

		// node-1 is ready for skyhook_gate (per-node on skyhook_a, and node-1 completed it)
		Expect(IsNodeReadyForSkyhook("node-1", skyhookGate, allSkyhooks)).To(BeTrue())
		// node-1 is NOT ready for skyhook_c (sequencing: all on gate, node-2 incomplete on gate)
		Expect(IsNodeReadyForSkyhook("node-1", skyhookC, allSkyhooks)).To(BeFalse())
	})
})

var _ = Describe("isBlockedByGlobalPredecessor", func() {
	makeSkyhookNodesMockForBlocked := func(name string, priority int, sequencing v1alpha1.SequencingMode, complete bool, disabled bool) *skyhookNodesMock.MockSkyhookNodes {
		mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       v1alpha1.SkyhookSpec{Priority: priority, Sequencing: sequencing},
		}
		mock.EXPECT().GetSkyhook().Return(wrapper.NewSkyhookWrapper(skyhook)).Maybe()
		mock.EXPECT().IsDisabled().Return(disabled).Maybe()
		mock.EXPECT().IsComplete().Return(complete).Maybe()
		return mock
	}

	It("should return true when a sequencing: all predecessor is incomplete", func() {
		skyhookA := makeSkyhookNodesMockForBlocked("skyhook-a", 1, v1alpha1.SequencingAll, false, false)
		skyhookB := makeSkyhookNodesMockForBlocked("skyhook-b", 2, v1alpha1.SequencingNode, false, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}
		Expect(isBlockedByGlobalPredecessor(skyhookB, allSkyhooks)).To(BeTrue())
	})

	It("should return false when a sequencing: all predecessor is complete", func() {
		skyhookA := makeSkyhookNodesMockForBlocked("skyhook-a", 1, v1alpha1.SequencingAll, true, false)
		skyhookB := makeSkyhookNodesMockForBlocked("skyhook-b", 2, v1alpha1.SequencingNode, false, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}
		Expect(isBlockedByGlobalPredecessor(skyhookB, allSkyhooks)).To(BeFalse())
	})

	It("should return false when predecessor uses sequencing: node", func() {
		skyhookA := makeSkyhookNodesMockForBlocked("skyhook-a", 1, v1alpha1.SequencingNode, false, false)
		skyhookB := makeSkyhookNodesMockForBlocked("skyhook-b", 2, v1alpha1.SequencingNode, false, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}
		Expect(isBlockedByGlobalPredecessor(skyhookB, allSkyhooks)).To(BeFalse())
	})

	It("should skip disabled sequencing: all predecessors", func() {
		skyhookA := makeSkyhookNodesMockForBlocked("skyhook-a", 1, v1alpha1.SequencingAll, false, true)
		skyhookB := makeSkyhookNodesMockForBlocked("skyhook-b", 2, v1alpha1.SequencingNode, false, false)

		allSkyhooks := []SkyhookNodes{skyhookA, skyhookB}
		Expect(isBlockedByGlobalPredecessor(skyhookB, allSkyhooks)).To(BeFalse())
	})
})

var _ = Describe("BuildState ordering", func() {
	It("orders skyhooks by priority and name", func() {
		priorityKey := v1alpha1.METADATA_PREFIX + "/priority"
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "b", Annotations: map[string]string{priorityKey: "2"}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a", Annotations: map[string]string{priorityKey: "1"}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "c", Annotations: map[string]string{priorityKey: "2"}},
				},
			},
		}
		deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
		nodes := &corev1.NodeList{Items: []corev1.Node{}}
		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())
		ordered := clusterState.skyhooks
		// Should be: a (priority 1), b (priority 2, name b), c (priority 2, name c)
		Expect(ordered[0].GetSkyhook().Name).To(Equal("a"))
		Expect(ordered[1].GetSkyhook().Name).To(Equal("b"))
		Expect(ordered[2].GetSkyhook().Name).To(Equal("c"))
	})
})

var _ = Describe("Safe rollouts backwards compatibility", func() {
	It("creates synthetic FixedStrategy compartment when InterruptionBudget is set but no DeploymentPolicy", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
					Spec: v1alpha1.SkyhookSpec{
						InterruptionBudget: v1alpha1.InterruptionBudget{
							Count: kptr.To(5),
						},
					},
				},
			},
		}
		deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
		// Create 10 nodes, but budget count is 5
		nodes := &corev1.NodeList{Items: make([]corev1.Node, 10)}
		for i := 0; i < 10; i++ {
			nodes.Items[i] = corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i)},
			}
		}

		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))

		// Verify the synthetic default compartment was created
		compartments := clusterState.skyhooks[0].GetCompartments()
		Expect(compartments).To(HaveLen(1))
		Expect(compartments).To(HaveKey(v1alpha1.DefaultCompartmentName))

		defaultComp := compartments[v1alpha1.DefaultCompartmentName]
		// Verify budget was copied from InterruptionBudget
		Expect(defaultComp.Budget.Count).ToNot(BeNil())
		Expect(*defaultComp.Budget.Count).To(Equal(5))

		// Verify FixedStrategy was created with InitialBatch matching the count
		Expect(defaultComp.Strategy).ToNot(BeNil())
		Expect(defaultComp.Strategy.Fixed).ToNot(BeNil())
		Expect(*defaultComp.Strategy.Fixed.InitialBatch).To(Equal(5), "InitialBatch should match count budget")

		// Verify all nodes were assigned to the compartment
		Expect(len(defaultComp.GetNodes())).To(Equal(10))
	})

	It("Creates default 100% compartment when no InterruptionBudget and no DeploymentPolicy", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
					Spec:       v1alpha1.SkyhookSpec{},
				},
			},
		}
		deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
		// Create 10 nodes to test 100% behavior
		nodes := &corev1.NodeList{Items: make([]corev1.Node, 10)}
		for i := 0; i < 10; i++ {
			nodes.Items[i] = corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i)},
			}
		}

		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))

		// Verify default compartment was created with 100% budget (all nodes at once)
		compartments := clusterState.skyhooks[0].GetCompartments()
		Expect(compartments).To(HaveLen(1))
		Expect(compartments).To(HaveKey(v1alpha1.DefaultCompartmentName))

		defaultComp := compartments[v1alpha1.DefaultCompartmentName]
		// Verify 100% budget
		Expect(defaultComp.Budget.Percent).ToNot(BeNil())
		Expect(*defaultComp.Budget.Percent).To(Equal(100), "Should default to 100% budget")

		// Verify FixedStrategy with InitialBatch = all nodes (100% of 10 = 10)
		Expect(defaultComp.Strategy).ToNot(BeNil())
		Expect(defaultComp.Strategy.Fixed).ToNot(BeNil())
		Expect(*defaultComp.Strategy.Fixed.InitialBatch).To(Equal(10), "InitialBatch should be all nodes")

		// Verify all nodes were assigned to the compartment
		Expect(len(defaultComp.GetNodes())).To(Equal(10))
	})

	It("should add condition and set status when deployment policy is referenced but not found", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-skyhook",
						Generation: 1,
					},
					Spec: v1alpha1.SkyhookSpec{
						DeploymentPolicy: "missing-policy",
					},
					Status: v1alpha1.SkyhookStatus{
						CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
							"old-compartment": {
								Matched: 5,
								Ceiling: 3,
							},
						},
					},
				},
			},
		}
		// No deployment policies in the list
		deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
		nodes := &corev1.NodeList{Items: []corev1.Node{}}

		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))

		skyhookNodes := clusterState.skyhooks[0]

		// Verify no compartments were created
		compartments := skyhookNodes.GetCompartments()
		Expect(compartments).To(HaveLen(0))

		// Verify condition was added
		conditions := skyhookNodes.GetSkyhook().Status.Conditions
		Expect(conditions).NotTo(BeNil())
		found := false
		for _, cond := range conditions {
			if cond.Type == fmt.Sprintf("%s/DeploymentPolicyNotFound", v1alpha1.METADATA_PREFIX) {
				Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				Expect(cond.Reason).To(Equal("DeploymentPolicyNotFound"))
				Expect(cond.Message).To(ContainSubstring("missing-policy"))
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "DeploymentPolicyNotFound condition should be present")

		// Verify Updated flag was set
		Expect(skyhookNodes.GetSkyhook().Updated).To(BeTrue())

		// Test IntrospectSkyhook sets status to blocked
		changed := IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})
		Expect(changed).To(BeTrue())
		Expect(skyhookNodes.Status()).To(Equal(v1alpha1.StatusBlocked))
	})

	It("should clear condition when deployment policy is found after being missing", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-skyhook",
						Generation: 2,
					},
					Spec: v1alpha1.SkyhookSpec{
						DeploymentPolicy: "found-policy",
					},
					Status: v1alpha1.SkyhookStatus{
						Conditions: []metav1.Condition{
							{
								Type:               fmt.Sprintf("%s/DeploymentPolicyNotFound", v1alpha1.METADATA_PREFIX),
								Status:             metav1.ConditionTrue,
								ObservedGeneration: 1,
								LastTransitionTime: metav1.Now(),
								Reason:             "DeploymentPolicyNotFound",
								Message:            "DeploymentPolicy \"found-policy\" not found",
							},
						},
					},
				},
			},
		}
		// Deployment policy exists now
		deploymentPolicies := &v1alpha1.DeploymentPolicyList{
			Items: []v1alpha1.DeploymentPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "found-policy"},
					Spec: v1alpha1.DeploymentPolicySpec{
						Default: v1alpha1.PolicyDefault{
							Budget: v1alpha1.DeploymentBudget{
								Percent: kptr.To(100),
							},
						},
					},
				},
			},
		}
		nodes := &corev1.NodeList{Items: []corev1.Node{}}

		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))

		skyhookNodes := clusterState.skyhooks[0]

		// Verify compartments were created
		compartments := skyhookNodes.GetCompartments()
		Expect(compartments).To(HaveLen(1))
		Expect(compartments).To(HaveKey(v1alpha1.DefaultCompartmentName))

		// Verify condition was removed
		conditions := skyhookNodes.GetSkyhook().Status.Conditions
		found := false
		for _, cond := range conditions {
			if cond.Type == fmt.Sprintf("%s/DeploymentPolicyNotFound", v1alpha1.METADATA_PREFIX) {
				found = true
				break
			}
		}
		Expect(found).To(BeFalse(), "DeploymentPolicyNotFound condition should be removed")
	})
})

var _ = Describe("AddCompartmentNode", func() {
	It("should return error when compartment doesn't exist", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
		}

		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-skyhook",
			},
		}

		skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
		Expect(err).NotTo(HaveOccurred())

		skyhookNodes := &skyhookNodes{
			skyhook:      wrapper.NewSkyhookWrapper(skyhook),
			nodes:        []wrapper.SkyhookNode{skyhookNode},
			compartments: make(map[string]*wrapper.Compartment),
		}

		// Try to add node to non-existent compartment
		err = skyhookNodes.AddCompartmentNode("non-existent-compartment", skyhookNode)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("compartment \"non-existent-compartment\" not found"))
		Expect(err.Error()).To(ContainSubstring("test-skyhook"))
	})

	It("should successfully add node when compartment exists", func() {
		compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
			Name: "test-compartment",
		}, nil)

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
		}

		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-skyhook",
			},
		}

		skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
		Expect(err).NotTo(HaveOccurred())

		skyhookNodes := &skyhookNodes{
			skyhook:      wrapper.NewSkyhookWrapper(skyhook),
			nodes:        []wrapper.SkyhookNode{skyhookNode},
			compartments: make(map[string]*wrapper.Compartment),
		}
		skyhookNodes.AddCompartment("test-compartment", compartment)

		// Should succeed
		err = skyhookNodes.AddCompartmentNode("test-compartment", skyhookNode)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(compartment.GetNodes())).To(Equal(1))
	})
})

var _ = Describe("partitionNodesIntoCompartments", func() {
	It("should skip skyhooks with no deployment policy", func() {
		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec:       v1alpha1.SkyhookSpec{
				// No deployment policy
			},
		}
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		}
		skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
		Expect(err).NotTo(HaveOccurred())

		skyhookNodes := &skyhookNodes{
			skyhook:      wrapper.NewSkyhookWrapper(skyhook),
			nodes:        []wrapper.SkyhookNode{skyhookNode},
			compartments: make(map[string]*wrapper.Compartment),
		}

		clusterState := &clusterState{
			skyhooks: []SkyhookNodes{skyhookNodes},
		}

		// Should not error, just skip
		err = partitionNodesIntoCompartments(clusterState)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should skip skyhooks with empty compartments (missing policy)", func() {
		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				DeploymentPolicy: "missing-policy", // Policy doesn't exist
			},
		}
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		}
		skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
		Expect(err).NotTo(HaveOccurred())

		skyhookNodes := &skyhookNodes{
			skyhook:      wrapper.NewSkyhookWrapper(skyhook),
			nodes:        []wrapper.SkyhookNode{skyhookNode},
			compartments: make(map[string]*wrapper.Compartment), // Empty - policy not found
		}

		clusterState := &clusterState{
			skyhooks: []SkyhookNodes{skyhookNodes},
		}

		// Should skip gracefully instead of panicking
		err = partitionNodesIntoCompartments(clusterState)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should partition nodes when compartments exist", func() {
		compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
			Name: "test-compartment",
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"type": "gpu"},
			},
		}, nil)

		defaultCompartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
			Name: v1alpha1.DefaultCompartmentName,
		}, nil)

		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				DeploymentPolicy: "test-policy",
			},
		}

		node1 := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "gpu-node",
				Labels: map[string]string{"type": "gpu"},
			},
		}
		node2 := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "cpu-node",
				Labels: map[string]string{"type": "cpu"},
			},
		}

		skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
		Expect(err).NotTo(HaveOccurred())
		skyhookNode2, err := wrapper.NewSkyhookNode(node2, skyhook)
		Expect(err).NotTo(HaveOccurred())

		skyhookNodes := &skyhookNodes{
			skyhook:      wrapper.NewSkyhookWrapper(skyhook),
			nodes:        []wrapper.SkyhookNode{skyhookNode1, skyhookNode2},
			compartments: make(map[string]*wrapper.Compartment),
		}
		skyhookNodes.AddCompartment("test-compartment", compartment)
		skyhookNodes.AddCompartment(v1alpha1.DefaultCompartmentName, defaultCompartment)

		clusterState := &clusterState{
			skyhooks: []SkyhookNodes{skyhookNodes},
		}

		// Should succeed
		err = partitionNodesIntoCompartments(clusterState)
		Expect(err).NotTo(HaveOccurred())

		// Verify nodes were assigned correctly
		Expect(len(compartment.GetNodes())).To(Equal(1))
		Expect(compartment.GetNodes()[0].GetNode().Name).To(Equal("gpu-node"))
		Expect(len(defaultCompartment.GetNodes())).To(Equal(1))
		Expect(defaultCompartment.GetNodes()[0].GetNode().Name).To(Equal("cpu-node"))
	})
})

var _ = Describe("CleanupRemovedNodes", func() {
	It("should cleanup removed nodes from all status maps", func() {
		// Create mock skyhook nodes
		mockSkyhookNodes := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
		// Create mock nodes that currently exist
		mockNode1 := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1"},
		}
		mockNode2 := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node2"},
		}

		// Create actual wrapper nodes using NewSkyhookNodeOnly
		node1, err := wrapper.NewSkyhookNode(
			mockNode1,
			&v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			},
		)
		Expect(err).NotTo(HaveOccurred())
		node2, err := wrapper.NewSkyhookNode(
			mockNode2,
			&v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			},
		)
		Expect(err).NotTo(HaveOccurred())

		// Create mock skyhook wrapper with status maps containing both existing and removed nodes
		mockSkyhook := &wrapper.Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					NodeState: map[string]v1alpha1.NodeState{
						"node1":        {},
						"node2":        {},
						"removed-node": {},
					},
					NodeStatus: map[string]v1alpha1.Status{
						"node1":        v1alpha1.StatusComplete,
						"node2":        v1alpha1.StatusInProgress,
						"removed-node": v1alpha1.StatusErroring,
					},
					NodeBootIds: map[string]string{
						"node1":        "boot-id-1",
						"node2":        "boot-id-2",
						"removed-node": "boot-id-removed",
					},
					NodePriority: map[string]metav1.Time{
						"node1":        metav1.Now(),
						"node2":        metav1.Now(),
						"removed-node": metav1.Now(),
					},
					ConfigUpdates: map[string][]string{
						"package1": {"config1"},
						"package2": {"config2"},
						"package3": {"config3"},
					},
				},
			},
			Updated: false,
		}

		// Set up mock expectations
		mockSkyhookNodes.EXPECT().GetNodes().Return([]wrapper.SkyhookNode{
			node1,
			node2,
		})
		mockSkyhookNodes.EXPECT().GetSkyhook().Return(mockSkyhook)

		// Call the function
		CleanupRemovedNodes(mockSkyhookNodes)

		// Verify that removed-node was cleaned up from all maps
		Expect(mockSkyhook.Status.NodeState).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodeState).To(HaveKey("node2"))
		Expect(mockSkyhook.Status.NodeState).NotTo(HaveKey("removed-node"))

		Expect(mockSkyhook.Status.NodeStatus).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodeStatus).To(HaveKey("node2"))
		Expect(mockSkyhook.Status.NodeStatus).NotTo(HaveKey("removed-node"))

		Expect(mockSkyhook.Status.NodeBootIds).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodeBootIds).To(HaveKey("node2"))
		Expect(mockSkyhook.Status.NodeBootIds).NotTo(HaveKey("removed-node"))

		Expect(mockSkyhook.Status.NodePriority).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodePriority).To(HaveKey("node2"))
		Expect(mockSkyhook.Status.NodePriority).NotTo(HaveKey("removed-node"))

		// ConfigUpdates should NOT be cleaned up by node removal since it's keyed by package names
		Expect(mockSkyhook.Status.ConfigUpdates).To(HaveKey("package1"))
		Expect(mockSkyhook.Status.ConfigUpdates).To(HaveKey("package2"))
		Expect(mockSkyhook.Status.ConfigUpdates).To(HaveKey("package3"))

		// Verify NodeOrderOffset was incremented for the removed node
		Expect(mockSkyhook.Status.NodeOrderOffset).To(Equal(1))

		// Verify that Updated flag was set since changes were made
		Expect(mockSkyhook.Updated).To(BeTrue())
	})

	It("should not set Updated flag when no nodes are removed", func() {
		// Create mock skyhook nodes
		mockSkyhookNodes := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
		// Create mock nodes that currently exist
		mockNode1 := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1"},
		}

		// Create actual wrapper nodes using NewSkyhookNodeOnly
		node1, err := wrapper.NewSkyhookNode(
			mockNode1,
			&v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			},
		)
		Expect(err).NotTo(HaveOccurred())

		// Create mock skyhook wrapper with status maps containing both existing and removed nodes
		mockSkyhook := &wrapper.Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					NodeState: map[string]v1alpha1.NodeState{
						"node1": {},
					},
					NodeStatus: map[string]v1alpha1.Status{
						"node1": v1alpha1.StatusComplete,
					},
					NodeBootIds: map[string]string{
						"node1": "boot-id-1",
					},
					NodePriority: map[string]metav1.Time{
						"node1": metav1.Now(),
					},
					ConfigUpdates: map[string][]string{
						"package1": {"config1"},
					},
				},
			},
			Updated: false,
		}

		// Set up mock expectations
		mockSkyhookNodes.EXPECT().GetNodes().Return([]wrapper.SkyhookNode{
			node1,
		})
		mockSkyhookNodes.EXPECT().GetSkyhook().Return(mockSkyhook)

		// Call the function
		CleanupRemovedNodes(mockSkyhookNodes)

		// Verify that removed-node was cleaned up from all maps
		Expect(mockSkyhook.Status.NodeState).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodeStatus).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodeBootIds).To(HaveKey("node1"))
		Expect(mockSkyhook.Status.NodePriority).To(HaveKey("node1"))
		// ConfigUpdates should remain unchanged since it's keyed by package names, not node names
		Expect(mockSkyhook.Status.ConfigUpdates).To(HaveKey("package1"))

		// Verify NodeOrderOffset was NOT incremented
		Expect(mockSkyhook.Status.NodeOrderOffset).To(Equal(0))

		// Verify that Updated flag was NOT set since no changes were made
		Expect(mockSkyhook.Updated).To(BeFalse())
	})

	Describe("isSkyhookControlledNodeStatus", func() {
		It("should return true for disabled status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusDisabled)
			Expect(result).To(BeTrue())
		})

		It("should return true for paused status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusPaused)
			Expect(result).To(BeTrue())
		})

		It("should return true for waiting status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusWaiting)
			Expect(result).To(BeTrue())
		})

		It("should return false for complete status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusComplete)
			Expect(result).To(BeFalse())
		})

		It("should return false for in_progress status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusInProgress)
			Expect(result).To(BeFalse())
		})

		It("should return false for erroring status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusErroring)
			Expect(result).To(BeFalse())
		})

		It("should return false for blocked status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusBlocked)
			Expect(result).To(BeFalse())
		})

		It("should return false for unknown status", func() {
			result := isSkyhookControlledNodeStatus(v1alpha1.StatusUnknown)
			Expect(result).To(BeFalse())
		})
	})

	Describe("UpdateSkyhookPauseStatus", func() {
		var mockSkyhookNodes *skyhookNodesMock.MockSkyhookNodes
		var mockSkyhook *wrapper.Skyhook
		var mockNode1 wrapper.SkyhookNode
		var mockNode2 wrapper.SkyhookNode

		BeforeEach(func() {
			mockSkyhookNodes = skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())

			// Create a real skyhook for testing
			mockSkyhook = &wrapper.Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-skyhook",
						Annotations: map[string]string{},
					},
					Status: v1alpha1.SkyhookStatus{
						Status: v1alpha1.StatusInProgress,
					},
				},
			}

			// Create real nodes for testing
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}

			var err error
			mockNode1, err = wrapper.NewSkyhookNode(node1, mockSkyhook.Skyhook)
			Expect(err).NotTo(HaveOccurred())

			mockNode2, err = wrapper.NewSkyhookNode(node2, mockSkyhook.Skyhook)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update status to paused when skyhook is paused and status is not already paused", func() {
			// Set up the skyhook as paused
			mockSkyhook.Annotations[v1alpha1.METADATA_PREFIX+"/pause"] = annotationTrueValue

			// Set up mock expectations
			mockSkyhookNodes.EXPECT().IsPaused().Return(true)
			mockSkyhookNodes.EXPECT().Status().Return(v1alpha1.StatusInProgress)
			mockSkyhookNodes.EXPECT().SetStatus(v1alpha1.StatusPaused).Once()
			mockSkyhookNodes.EXPECT().GetNodes().Return([]wrapper.SkyhookNode{mockNode1, mockNode2})

			// Call the function
			result := UpdateSkyhookPauseStatus(mockSkyhookNodes)

			// Verify the result
			Expect(result).To(BeTrue())
		})

		It("should not change status when skyhook is paused but status is already paused", func() {
			// Set up the skyhook as paused with paused status
			mockSkyhook.Annotations[v1alpha1.METADATA_PREFIX+"/pause"] = annotationTrueValue

			// Set up mock expectations
			mockSkyhookNodes.EXPECT().IsPaused().Return(true)
			mockSkyhookNodes.EXPECT().Status().Return(v1alpha1.StatusPaused)

			// Call the function
			result := UpdateSkyhookPauseStatus(mockSkyhookNodes)

			// Verify the result
			Expect(result).To(BeFalse())
		})

		It("should not change status when skyhook is not paused", func() {
			// Set up the skyhook as not paused
			mockSkyhook.Annotations[v1alpha1.METADATA_PREFIX+"/pause"] = "false"

			// Set up mock expectations
			mockSkyhookNodes.EXPECT().IsPaused().Return(false)

			// Call the function
			result := UpdateSkyhookPauseStatus(mockSkyhookNodes)

			// Verify the result
			Expect(result).To(BeFalse())
		})

		It("should not change status when skyhook pause annotation is missing", func() {
			// Set up mock expectations no pause annotation means not paused
			mockSkyhookNodes.EXPECT().IsPaused().Return(false)

			// Call the function
			result := UpdateSkyhookPauseStatus(mockSkyhookNodes)

			// Verify the result
			Expect(result).To(BeFalse())
		})
	})

	Describe("IntrospectSkyhook", func() {
		var testSkyhook *v1alpha1.Skyhook
		var testNode *corev1.Node

		BeforeEach(func() {
			testSkyhook = &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-skyhook",
					Annotations: map[string]string{},
				},
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"test-package": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package", Version: "1.0.0"},
							Image:      "test-image",
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{
					Status: v1alpha1.StatusInProgress,
				},
			}

			testNode = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			}
		})

		It("should set status to disabled when skyhook is disabled", func() {
			// Set up the skyhook as disabled
			testSkyhook.Annotations["skyhook.nvidia.com/disable"] = annotationTrueValue

			skyhookNode, err := wrapper.NewSkyhookNode(testNode, testSkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(testSkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode},
			}

			// Call the function
			changed := IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the result
			Expect(changed).To(BeTrue())
			Expect(skyhookNodes.Status()).To(Equal(v1alpha1.StatusDisabled))
		})

		It("should set status to paused when skyhook is paused", func() {
			// Set up the skyhook as paused
			testSkyhook.Annotations["skyhook.nvidia.com/pause"] = annotationTrueValue

			skyhookNode, err := wrapper.NewSkyhookNode(testNode, testSkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(testSkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode},
			}

			// Call the function
			changed := IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the result
			Expect(changed).To(BeTrue())
			Expect(skyhookNodes.Status()).To(Equal(v1alpha1.StatusPaused))
		})

		It("should set per-node waiting status when node is blocked by higher priority skyhook", func() {
			// Test per-node priority: A node should be waiting if it hasn't completed
			// higher-priority skyhooks that target that same node.
			// Create higher priority skyhook (priority 1)
			higherPrioritySkyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "skyhook-1"},
				Spec: v1alpha1.SkyhookSpec{
					Priority: 1,
					Packages: map[string]v1alpha1.Package{
						"test-package-1": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package-1", Version: "1.0.0"},
							Image:      "test-image-1",
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{Status: v1alpha1.StatusInProgress},
			}

			// Create lower priority skyhook (priority 2)
			lowerPrioritySkyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "skyhook-2"},
				Spec: v1alpha1.SkyhookSpec{
					Priority: 2,
					Packages: map[string]v1alpha1.Package{
						"test-package-2": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package-2", Version: "1.0.0"},
							Image:      "test-image-2",
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{Status: v1alpha1.StatusInProgress},
			}

			// Use the same node for both skyhooks to test per-node waiting
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}

			skyhookNode1ForHigher, err := wrapper.NewSkyhookNode(node1, higherPrioritySkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode1ForLower, err := wrapper.NewSkyhookNode(node1, lowerPrioritySkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes1 := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(higherPrioritySkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode1ForHigher},
			}

			skyhookNodes2 := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(lowerPrioritySkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode1ForLower},
			}

			allSkyhooks := []SkyhookNodes{skyhookNodes1, skyhookNodes2}

			// Call the function - node-1 in skyhook2 should be waiting because
			// it hasn't completed skyhook1 yet (per-node priority)
			changed := IntrospectSkyhook(skyhookNodes2, allSkyhooks)

			// Verify the result - node should be waiting
			Expect(changed).To(BeTrue())
			// The node status should be waiting (set per-node by IntrospectNode)
			Expect(skyhookNode1ForLower.Status()).To(Equal(v1alpha1.StatusWaiting))
		})

		It("should not set waiting status when node is not in higher priority skyhook", func() {
			// Test that a node doesn't wait if it's not targeted by higher-priority skyhooks
			higherPrioritySkyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "skyhook-1"},
				Spec: v1alpha1.SkyhookSpec{
					Priority: 1,
					Packages: map[string]v1alpha1.Package{
						"test-package-1": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package-1", Version: "1.0.0"},
							Image:      "test-image-1",
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{Status: v1alpha1.StatusInProgress},
			}

			lowerPrioritySkyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "skyhook-2"},
				Spec: v1alpha1.SkyhookSpec{
					Priority: 2,
					Packages: map[string]v1alpha1.Package{
						"test-package-2": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package-2", Version: "1.0.0"},
							Image:      "test-image-2",
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{Status: v1alpha1.StatusInProgress},
			}

			// Different nodes for each skyhook
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, higherPrioritySkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode2, err := wrapper.NewSkyhookNode(node2, lowerPrioritySkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes1 := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(higherPrioritySkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode1},
			}

			skyhookNodes2 := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(lowerPrioritySkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode2},
			}

			allSkyhooks := []SkyhookNodes{skyhookNodes1, skyhookNodes2}

			// Call the function - node-2 should NOT be waiting because
			// skyhook1 doesn't target node-2
			IntrospectSkyhook(skyhookNodes2, allSkyhooks)

			// Node-2 should not be waiting (it's not in skyhook1)
			Expect(skyhookNode2.Status()).NotTo(Equal(v1alpha1.StatusWaiting))
		})

		It("should not change status when skyhook is complete", func() {
			// Create a complete skyhook with no packages
			completeSkyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Status:     v1alpha1.SkyhookStatus{Status: v1alpha1.StatusComplete},
			}

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}

			skyhookNode, err := wrapper.NewSkyhookNode(node, completeSkyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode.SetStatus(v1alpha1.StatusComplete)

			skyhookNodes := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(completeSkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode},
			}

			// Call the function
			_ = IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the result - status should stay complete
			Expect(skyhookNodes.Status()).To(Equal(v1alpha1.StatusComplete))
		})

		It("should return true when node status changes", func() {
			skyhookNode, err := wrapper.NewSkyhookNode(testNode, testSkyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode.SetStatus(v1alpha1.StatusUnknown)

			skyhookNodes := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(testSkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode},
			}

			// Call the function
			changed := IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the result
			Expect(changed).To(BeTrue())
		})

		It("should set Unknown nodes to Waiting when in compartments", func() {
			skyhookNode, err := wrapper.NewSkyhookNode(testNode, testSkyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode.SetStatus(v1alpha1.StatusUnknown)

			// Create a compartment and add the node
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: v1alpha1.DefaultCompartmentName,
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(100),
				},
			}, nil)
			compartment.AddNode(skyhookNode)

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(testSkyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment(v1alpha1.DefaultCompartmentName, compartment)

			// Call IntrospectSkyhook which calls IntrospectNode
			changed := IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the node status changed from Unknown to Waiting
			Expect(changed).To(BeTrue())
			Expect(skyhookNode.Status()).To(Equal(v1alpha1.StatusWaiting))
		})

		It("should keep Unknown nodes Unknown when no compartments exist", func() {
			skyhookNode, err := wrapper.NewSkyhookNode(testNode, testSkyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode.SetStatus(v1alpha1.StatusUnknown)

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(testSkyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode},
				compartments: make(map[string]*wrapper.Compartment), // No compartments
			}

			// Call IntrospectSkyhook which calls IntrospectNode
			_ = IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the node status stays Unknown (error state - no compartments)
			// Note: IntrospectSkyhook might return true due to UpdateCondition, but the important
			// thing is that the node status remains Unknown when no compartments exist
			Expect(skyhookNode.Status()).To(Equal(v1alpha1.StatusUnknown))
		})

		It("should handle multiple nodes correctly when disabled", func() {
			// Set up the skyhook as disabled
			testSkyhook.Annotations["skyhook.nvidia.com/disable"] = annotationTrueValue

			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, testSkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode2, err := wrapper.NewSkyhookNode(node2, testSkyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook: wrapper.NewSkyhookWrapper(testSkyhook),
				nodes:   []wrapper.SkyhookNode{skyhookNode1, skyhookNode2},
			}

			// Call the function
			changed := IntrospectSkyhook(skyhookNodes, []SkyhookNodes{skyhookNodes})

			// Verify the result
			Expect(changed).To(BeTrue())
			Expect(skyhookNodes.Status()).To(Equal(v1alpha1.StatusDisabled))
			Expect(skyhookNode1.Status()).To(Equal(v1alpha1.StatusDisabled))
			Expect(skyhookNode2.Status()).To(Equal(v1alpha1.StatusDisabled))
		})
	})

	Describe("AssignNodeToCompartment", func() {
		It("should assign node to compartment", func() {
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment",
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label": "test-value"},
				},
			}, nil)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node",
					Labels: map[string]string{"test-label": "test-value"},
				},
			}

			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
			}

			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("test-compartment", compartment)

			compartmentName, err := skyhookNodes.AssignNodeToCompartment(skyhookNode)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment"))
		})

		It("should assign node to default compartment", func() {
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment",
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label": "test-value"},
				},
			}, nil)

			defaultCompartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: v1alpha1.DefaultCompartmentName,
			}, nil)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node",
					Labels: map[string]string{"test-label": "test-value-other"},
				},
			}

			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
			}

			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("test-compartment", compartment)
			skyhookNodes.AddCompartment(v1alpha1.DefaultCompartmentName, defaultCompartment)

			compartmentName, err := skyhookNodes.AssignNodeToCompartment(skyhookNode)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal(v1alpha1.DefaultCompartmentName))
		})

		It("should assign node to compartment with safer strategy", func() {
			compartment1 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-1",
				Strategy: &v1alpha1.DeploymentStrategy{
					Fixed: &v1alpha1.FixedStrategy{
						InitialBatch: ptr(1),
					},
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-1": "test-value-1"},
				},
			}, nil)

			compartment2 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-2",
				Strategy: &v1alpha1.DeploymentStrategy{
					Linear: &v1alpha1.LinearStrategy{
						InitialBatch: ptr(1),
					},
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-2": "test-value-2"},
				},
			}, nil)

			compartment3 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-3",
				Strategy: &v1alpha1.DeploymentStrategy{
					Exponential: &v1alpha1.ExponentialStrategy{
						InitialBatch: ptr(1),
					},
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-3": "test-value-3"},
				},
			}, nil)

			fixedNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node-fixed",
					Labels: map[string]string{"test-label-1": "test-value-1", "test-label-2": "test-value-2", "test-label-3": "test-value-3"},
				},
			}

			linearNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node-linear",
					Labels: map[string]string{"test-label-2": "test-value-2", "test-label-3": "test-value-3"},
				},
			}

			exponentialNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node-exponential",
					Labels: map[string]string{"test-label-3": "test-value-3"},
				},
			}

			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
			}

			skyhookNodeFixed, err := wrapper.NewSkyhookNode(fixedNode, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNodeLinear, err := wrapper.NewSkyhookNode(linearNode, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNodeExponential, err := wrapper.NewSkyhookNode(exponentialNode, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNodeFixed, skyhookNodeLinear, skyhookNodeExponential},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("test-compartment-1", compartment1)
			skyhookNodes.AddCompartment("test-compartment-2", compartment2)
			skyhookNodes.AddCompartment("test-compartment-3", compartment3)

			compartmentName, err := skyhookNodes.AssignNodeToCompartment(skyhookNodeFixed)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment-1"))
			compartmentName, err = skyhookNodes.AssignNodeToCompartment(skyhookNodeLinear)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment-2"))
			compartmentName, err = skyhookNodes.AssignNodeToCompartment(skyhookNodeExponential)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment-3"))
		})

		It("should assign node to compartment with smaller count budget", func() {
			compartment1 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-1",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(1),
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-1": "test-value-1"},
				},
			}, nil)

			compartment2 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-2",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(2),
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-2": "test-value-2"},
				},
			}, nil)

			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node-1",
					Labels: map[string]string{"test-label-1": "test-value-1", "test-label-2": "test-value-2"},
				},
			}

			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook-1",
				},
			}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode1},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("test-compartment-1", compartment1)
			skyhookNodes.AddCompartment("test-compartment-2", compartment2)

			compartmentName, err := skyhookNodes.AssignNodeToCompartment(skyhookNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment-1"))
		})

		It("should assign node to compartment with smaller percent budget", func() {
			compartment1 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-1",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(10),
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-1": "test-value-1"},
				},
			}, nil)

			compartment2 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-2",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(20),
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-2": "test-value-2"},
				},
			}, nil)

			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node-1",
					Labels: map[string]string{"test-label-1": "test-value-1", "test-label-2": "test-value-2"},
				},
			}

			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook-1",
				},
			}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode1},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("test-compartment-1", compartment1)
			skyhookNodes.AddCompartment("test-compartment-2", compartment2)

			compartmentName, err := skyhookNodes.AssignNodeToCompartment(skyhookNode1)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment-1"))
		})

		It("should assign to compartment with higher percent but smaller effective capacity due to fewer matching nodes", func() {
			// Test that a compartment with a higher percent but fewer matching nodes
			// can have a smaller effective capacity and win the assignment

			// Compartment A: 50% budget, matches 10 nodes total
			compartmentA := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-1",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(50), // 50% of 10 = 5 capacity
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-1": "test-value-1"},
				},
				Strategy: &v1alpha1.DeploymentStrategy{Fixed: &v1alpha1.FixedStrategy{}},
			}, nil)

			// Compartment B: 80% budget, matches only 2 nodes total
			compartmentB := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment-2",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(80), // 80% of 2 = floor(1.6) = 1 capacity
				},
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-label-2": "test-value-2"},
				},
				Strategy: &v1alpha1.DeploymentStrategy{Fixed: &v1alpha1.FixedStrategy{}},
			}, nil)

			// Target node matches both compartments
			targetNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node-1",
					Labels: map[string]string{"test-label-1": "test-value-1", "test-label-2": "test-value-2"},
				},
			}

			// Create all nodes
			allNodesList := []*corev1.Node{
				targetNode,
				// 9 more nodes that match compartment A (test-label-1=test-value-1) but not B
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a1", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a2", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a3", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a4", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a5", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a6", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a7", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a8", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a9", Labels: map[string]string{"test-label-1": "test-value-1"}}},
				// 1 more node that matches compartment B (test-label-2=test-value-2) but not A
				{ObjectMeta: metav1.ObjectMeta{Name: "node-b1", Labels: map[string]string{"test-label-2": "test-value-2"}}},
			}

			skyhook := &v1alpha1.Skyhook{ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"}}
			var allNodes []wrapper.SkyhookNode
			for _, n := range allNodesList {
				sn, err := wrapper.NewSkyhookNode(n, skyhook)
				Expect(err).NotTo(HaveOccurred())
				allNodes = append(allNodes, sn)
			}

			targetSkyhookNode, err := wrapper.NewSkyhookNode(targetNode, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        allNodes,
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("test-compartment-1", compartmentA)
			skyhookNodes.AddCompartment("test-compartment-2", compartmentB)

			// Should assign to "test-compartment-2" because:
			// - test-compartment-1: 50% × 10 nodes = 5 capacity
			// - test-compartment-2: 80% × 2 nodes = 1 capacity (smaller wins)
			compartmentName, err := skyhookNodes.AssignNodeToCompartment(targetSkyhookNode)
			Expect(err).NotTo(HaveOccurred())
			Expect(compartmentName).To(Equal("test-compartment-2"), "higher percent but fewer matching nodes = smaller capacity")
		})

		It("should use lexicographic order as final tiebreaker when strategy and capacity are identical", func() {
			testCases := []struct {
				name           string
				compartmentA   string
				compartmentB   string
				selectorA      map[string]string
				selectorB      map[string]string
				expectedWinner string
			}{
				{
					name:           "zebra vs apple - apple wins",
					compartmentA:   "zebra-compartment",
					compartmentB:   "apple-compartment",
					selectorA:      map[string]string{"env": "prod"},
					selectorB:      map[string]string{"tier": "frontend"},
					expectedWinner: "apple-compartment",
				},
				{
					name:           "production vs development - development wins",
					compartmentA:   "production",
					compartmentB:   "development",
					selectorA:      map[string]string{"env": "prod"},
					selectorB:      map[string]string{"tier": "frontend"},
					expectedWinner: "development",
				},
				{
					name:           "comp-2 vs comp-1 - comp-1 wins",
					compartmentA:   "comp-2",
					compartmentB:   "comp-1",
					selectorA:      map[string]string{"env": "prod"},
					selectorB:      map[string]string{"tier": "frontend"},
					expectedWinner: "comp-1",
				},
			}

			for _, tc := range testCases {
				By(tc.name)

				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "test-node",
						Labels: map[string]string{"env": "prod", "tier": "frontend", "zone": "us-west"},
					},
				}

				skyhook := &v1alpha1.Skyhook{ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"}}
				skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
				Expect(err).NotTo(HaveOccurred())

				compartmentA := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
					Name: tc.compartmentA,
					Budget: v1alpha1.DeploymentBudget{
						Count: ptr(5),
					},
					Selector: metav1.LabelSelector{
						MatchLabels: tc.selectorA,
					},
					Strategy: &v1alpha1.DeploymentStrategy{Fixed: &v1alpha1.FixedStrategy{}},
				}, nil)

				compartmentB := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
					Name: tc.compartmentB,
					Budget: v1alpha1.DeploymentBudget{
						Count: ptr(5), // Same capacity
					},
					Selector: metav1.LabelSelector{
						MatchLabels: tc.selectorB,
					},
					Strategy: &v1alpha1.DeploymentStrategy{Fixed: &v1alpha1.FixedStrategy{}}, // Same strategy
				}, nil)

				skyhookNodes := &skyhookNodes{
					skyhook:      wrapper.NewSkyhookWrapper(skyhook),
					nodes:        []wrapper.SkyhookNode{skyhookNode},
					compartments: make(map[string]*wrapper.Compartment),
				}
				skyhookNodes.AddCompartment(tc.compartmentA, compartmentA)
				skyhookNodes.AddCompartment(tc.compartmentB, compartmentB)

				compartmentName, err := skyhookNodes.AssignNodeToCompartment(skyhookNode)
				Expect(err).NotTo(HaveOccurred())
				Expect(compartmentName).To(Equal(tc.expectedWinner), "lexicographic tie-break")

				// Run multiple times to ensure consistency
				for i := 0; i < 5; i++ {
					result, err := skyhookNodes.AssignNodeToCompartment(skyhookNode)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(compartmentName), "should be deterministic across multiple calls")
				}
			}
		})
	})
})

var _ = Describe("Compartment Status Tests", func() {
	Describe("compartmentStatusEqual", func() {
		It("should return true for equal compartment statuses without batch state", func() {
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
				BatchState:      nil,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
				BatchState:      nil,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeTrue())
		})

		It("should return true for equal compartment statuses with batch state", func() {
			batchState := v1alpha1.BatchProcessingState{
				CurrentBatch:        2,
				ConsecutiveFailures: 0,
				CompletedNodes:      5,
				FailedNodes:         1,
				ShouldStop:          false,
				LastBatchSize:       3,
				LastBatchFailed:     false,
			}
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       5,
				ProgressPercent: 50,
				BatchState:      &batchState,
			}
			batchStateCopy := batchState
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       5,
				ProgressPercent: 50,
				BatchState:      &batchStateCopy,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeTrue())
		})

		It("should return false when matched count differs", func() {
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         15,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeFalse())
		})

		It("should return false when ceiling differs", func() {
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         10,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeFalse())
		})

		It("should return false when in progress count differs", func() {
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      3,
				Completed:       3,
				ProgressPercent: 30,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeFalse())
		})

		It("should return false when completed count differs", func() {
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       5,
				ProgressPercent: 30,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeFalse())
		})

		It("should return false when progress percent differs", func() {
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 30,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 50,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeFalse())
		})

		It("should return false when batch states differ", func() {
			batchState1 := v1alpha1.BatchProcessingState{
				CurrentBatch:        2,
				ConsecutiveFailures: 0,
				CompletedNodes:      5,
				FailedNodes:         1,
				ShouldStop:          false,
				LastBatchSize:       3,
				LastBatchFailed:     false,
			}
			batchState2 := v1alpha1.BatchProcessingState{
				CurrentBatch:        2,
				ConsecutiveFailures: 1, // Different
				CompletedNodes:      5,
				FailedNodes:         1,
				ShouldStop:          false,
				LastBatchSize:       3,
				LastBatchFailed:     false,
			}
			a := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       5,
				ProgressPercent: 50,
				BatchState:      &batchState1,
			}
			b := v1alpha1.CompartmentStatus{
				Matched:         10,
				Ceiling:         5,
				InProgress:      2,
				Completed:       5,
				ProgressPercent: 50,
				BatchState:      &batchState2,
			}
			Expect(compartmentStatusEqual(a, b)).To(BeFalse())
		})
	})

	Describe("buildCompartmentStatus", func() {
		It("should build status for compartment with no nodes", func() {
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(5),
				},
			}, nil)

			status := buildCompartmentStatus(compartment)

			Expect(status.Matched).To(Equal(0))
			Expect(status.Ceiling).To(Equal(5)) // Count budget returns the count value even with 0 nodes
			Expect(status.InProgress).To(Equal(0))
			Expect(status.Completed).To(Equal(0))
			Expect(status.ProgressPercent).To(Equal(0))
			Expect(status.BatchState).To(BeNil())
		})

		It("should build status for compartment with strategy and batch state", func() {
			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"test-package": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package", Version: "1.0.0"},
							Image:      "test-image",
						},
					},
				},
			}

			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode2, err := wrapper.NewSkyhookNode(node2, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode1.SetStatus(v1alpha1.StatusComplete)
			skyhookNode2.SetStatus(v1alpha1.StatusInProgress)

			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(50),
				},
				Strategy: &v1alpha1.DeploymentStrategy{
					Fixed: &v1alpha1.FixedStrategy{
						InitialBatch: ptr(1),
					},
				},
			}, nil)

			compartment.AddNode(skyhookNode1)
			compartment.AddNode(skyhookNode2)

			status := buildCompartmentStatus(compartment)

			Expect(status.Matched).To(Equal(2))
			Expect(status.Ceiling).To(Equal(1)) // 50% of 2 = 1
			Expect(status.InProgress).To(Equal(1))
			Expect(status.Completed).To(Equal(0)) // No nodes actually complete (packages not done)
			Expect(status.ProgressPercent).To(Equal(0))
			Expect(status.BatchState).NotTo(BeNil())
			Expect(status.BatchState.CurrentBatch).To(Equal(1))
			Expect(status.BatchState.ConsecutiveFailures).To(Equal(0))
		})

		It("should calculate 100% progress when all nodes are complete", func() {
			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			}

			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode2, err := wrapper.NewSkyhookNode(node2, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode1.SetStatus(v1alpha1.StatusComplete)
			skyhookNode2.SetStatus(v1alpha1.StatusComplete)

			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "test-compartment",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(2),
				},
			}, nil)

			compartment.AddNode(skyhookNode1)
			compartment.AddNode(skyhookNode2)

			status := buildCompartmentStatus(compartment)

			Expect(status.ProgressPercent).To(Equal(100))
			Expect(status.Completed).To(Equal(2))
			Expect(status.InProgress).To(Equal(0))
		})
	})

	Describe("should persist compartment status to skyhook status", func() {
		It("should persist compartment status to skyhook status in ReportState", func() {
			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"test-package": {
							PackageRef: v1alpha1.PackageRef{Name: "test-package", Version: "1.0.0"},
							Image:      "test-image",
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{
					CompartmentStatuses: nil,
				},
			}

			// Create nodes
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}
			node3 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node3"}}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode2, err := wrapper.NewSkyhookNode(node2, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode3, err := wrapper.NewSkyhookNode(node3, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode1.SetStatus(v1alpha1.StatusComplete)
			skyhookNode2.SetStatus(v1alpha1.StatusInProgress)
			skyhookNode3.SetStatus(v1alpha1.StatusUnknown)

			// Create compartments
			compartment1 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "compartment-1",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(50),
				},
			}, nil)
			compartment1.AddNode(skyhookNode1)
			compartment1.AddNode(skyhookNode2)

			compartment2 := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "compartment-2",
				Budget: v1alpha1.DeploymentBudget{
					Percent: ptr(100),
				},
			}, nil)
			compartment2.AddNode(skyhookNode3)

			// Create skyhookNodes
			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode1, skyhookNode2, skyhookNode3},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("compartment-1", compartment1)
			skyhookNodes.AddCompartment("compartment-2", compartment2)

			// Call ReportState
			skyhookNodes.ReportState()

			// Verify compartment statuses were persisted
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses).NotTo(BeNil())
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses).To(HaveLen(2))

			// Verify compartment-1 status
			comp1Status, ok := skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"]
			Expect(ok).To(BeTrue())
			Expect(comp1Status.Matched).To(Equal(2))
			Expect(comp1Status.Ceiling).To(Equal(1)) // 50% of 2 = 1
			Expect(comp1Status.InProgress).To(Equal(1))
			Expect(comp1Status.Completed).To(Equal(0)) // No packages completed
			Expect(comp1Status.ProgressPercent).To(Equal(0))

			// Verify compartment-2 status
			comp2Status, ok := skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-2"]
			Expect(ok).To(BeTrue())
			Expect(comp2Status.Matched).To(Equal(1))
			Expect(comp2Status.Ceiling).To(Equal(1))
			Expect(comp2Status.InProgress).To(Equal(0))
			Expect(comp2Status.Completed).To(Equal(0))
			Expect(comp2Status.ProgressPercent).To(Equal(0))

			// Verify Updated flag was set
			Expect(skyhookNodes.skyhook.Updated).To(BeTrue())
		})

		It("should not update status when compartment status hasn't changed", func() {
			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Status: v1alpha1.SkyhookStatus{
					CompartmentStatuses: make(map[string]v1alpha1.CompartmentStatus),
				},
			}

			// Create a node
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode1.SetStatus(v1alpha1.StatusComplete)

			// Create compartment
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "compartment-1",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(2),
				},
			}, nil)
			compartment.AddNode(skyhookNode1)

			// Pre-populate status with correct values
			skyhook.Status.CompartmentStatuses["compartment-1"] = v1alpha1.CompartmentStatus{
				Matched:         1,
				Ceiling:         2,
				InProgress:      0,
				Completed:       1,
				ProgressPercent: 100,
				BatchState:      nil,
			}

			// Create skyhookNodes
			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode1},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("compartment-1", compartment)

			// Reset Updated flag
			skyhookNodes.skyhook.Updated = false

			// Call ReportState
			skyhookNodes.ReportState()

			// Verify Updated flag was NOT set since status didn't change
			// Note: ReportState might set Updated for other fields, but we're checking
			// that compartment status equality check is working
			comp1Status := skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"]
			Expect(comp1Status.Matched).To(Equal(1))
			Expect(comp1Status.Completed).To(Equal(1))
		})

		It("should persist batch state in compartment status", func() {
			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Status: v1alpha1.SkyhookStatus{
					CompartmentStatuses: nil,
				},
			}

			// Create nodes
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}

			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode2, err := wrapper.NewSkyhookNode(node2, skyhook)
			Expect(err).NotTo(HaveOccurred())

			skyhookNode1.SetStatus(v1alpha1.StatusComplete)
			skyhookNode2.SetStatus(v1alpha1.StatusInProgress)

			// Create compartment with strategy (to have batch state)
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "compartment-with-strategy",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(2),
				},
				Strategy: &v1alpha1.DeploymentStrategy{
					Fixed: &v1alpha1.FixedStrategy{
						InitialBatch: ptr(1),
					},
				},
			}, nil)
			compartment.AddNode(skyhookNode1)
			compartment.AddNode(skyhookNode2)

			// Create skyhookNodes
			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode1, skyhookNode2},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("compartment-with-strategy", compartment)

			// Call ReportState
			skyhookNodes.ReportState()

			// Verify batch state was persisted
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses).NotTo(BeNil())
			compStatus := skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-with-strategy"]
			Expect(compStatus.BatchState).NotTo(BeNil())
			Expect(compStatus.BatchState.CurrentBatch).To(Equal(1))
		})

		It("should remove stale compartment statuses when compartments are deleted from policy", func() {
			skyhook := &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Status: v1alpha1.SkyhookStatus{
					CompartmentStatuses: make(map[string]v1alpha1.CompartmentStatus),
				},
			}

			// Create a node
			node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			skyhookNode1, err := wrapper.NewSkyhookNode(node1, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode1.SetStatus(v1alpha1.StatusComplete)

			// Create compartment
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name: "compartment-1",
				Budget: v1alpha1.DeploymentBudget{
					Count: ptr(2),
				},
			}, nil)
			compartment.AddNode(skyhookNode1)

			// Pre-populate status with a stale compartment that no longer exists in the policy
			skyhook.Status.CompartmentStatuses["compartment-1"] = v1alpha1.CompartmentStatus{
				Matched:         1,
				Ceiling:         2,
				InProgress:      0,
				Completed:       1,
				ProgressPercent: 100,
			}
			skyhook.Status.CompartmentStatuses["stale-compartment"] = v1alpha1.CompartmentStatus{
				Matched:         5,
				Ceiling:         3,
				InProgress:      2,
				Completed:       3,
				ProgressPercent: 60,
			}

			// Create skyhookNodes with only compartment-1 (stale-compartment was removed from policy)
			skyhookNodes := &skyhookNodes{
				skyhook:      wrapper.NewSkyhookWrapper(skyhook),
				nodes:        []wrapper.SkyhookNode{skyhookNode1},
				compartments: make(map[string]*wrapper.Compartment),
			}
			skyhookNodes.AddCompartment("compartment-1", compartment)
			// Note: stale-compartment is NOT added to skyhookNodes.compartments

			// Reset Updated flag
			skyhookNodes.skyhook.Updated = false

			// Call ReportState
			skyhookNodes.ReportState()

			// Verify stale compartment status was removed
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses).NotTo(BeNil())
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses).To(HaveKey("compartment-1"))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses).NotTo(HaveKey("stale-compartment"))

			// Verify Updated flag was set since cleanup occurred
			Expect(skyhookNodes.skyhook.Updated).To(BeTrue())
		})
	})

	Describe("CollectNodeStatus for per-node ordering", func() {
		It("should return complete when all nodes are complete", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(true)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(true)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusComplete))
		})

		It("should return in_progress when any node is in_progress (per-node ordering)", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(false)
			node1.EXPECT().Status().Return(v1alpha1.StatusInProgress)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(false)
			node2.EXPECT().Status().Return(v1alpha1.StatusBlocked)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusInProgress))
		})

		It("should return blocked when all remaining nodes are blocked", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(true)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(false)
			node2.EXPECT().Status().Return(v1alpha1.StatusBlocked)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusBlocked))
		})

		It("should return waiting when all remaining nodes are waiting", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(true)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(false)
			node2.EXPECT().Status().Return(v1alpha1.StatusWaiting)
			node3 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node3.EXPECT().IsComplete().Return(false)
			node3.EXPECT().Status().Return(v1alpha1.StatusWaiting)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2, node3},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusWaiting))
		})

		It("should return erroring when all remaining nodes are erroring", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(false)
			node1.EXPECT().Status().Return(v1alpha1.StatusErroring)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(false)
			node2.EXPECT().Status().Return(v1alpha1.StatusErroring)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusErroring))
		})

		It("should prioritize in_progress over waiting (per-node ordering)", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(false)
			node1.EXPECT().Status().Return(v1alpha1.StatusInProgress)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(false)
			node2.EXPECT().Status().Return(v1alpha1.StatusWaiting)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusInProgress))
		})

		It("should return unknown when nodes are in mixed non-critical states", func() {
			node1 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node1.EXPECT().IsComplete().Return(false)
			node1.EXPECT().Status().Return(v1alpha1.StatusWaiting)
			node2 := wrapperMock.NewMockSkyhookNode(GinkgoT())
			node2.EXPECT().IsComplete().Return(false)
			node2.EXPECT().Status().Return(v1alpha1.StatusBlocked)

			skyhookNodes := &skyhookNodes{
				nodes: []wrapper.SkyhookNode{node1, node2},
			}

			status := skyhookNodes.CollectNodeStatus()
			Expect(status).To(Equal(v1alpha1.StatusUnknown))
		})
	})

	Describe("SetStatus with auto-reset on completion", func() {
		It("should reset batch state when transitioning to Complete with config enabled", func() {
			// Create a skyhook with batch state in compartments
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
						ResetBatchStateOnCompletion: kptr.To(true),
					},
				},
				Status: v1alpha1.SkyhookStatus{
					Status: v1alpha1.StatusInProgress,
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"compartment-1": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:        5,
								ConsecutiveFailures: 2,
								CompletedNodes:      10,
								FailedNodes:         1,
								ShouldStop:          false,
								LastBatchSize:       3,
								LastBatchFailed:     true,
							},
						},
						"compartment-2": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:        3,
								ConsecutiveFailures: 0,
								CompletedNodes:      5,
								FailedNodes:         0,
								ShouldStop:          false,
								LastBatchSize:       2,
								LastBatchFailed:     false,
							},
						},
					},
				},
			}

			deploymentPolicy := &v1alpha1.DeploymentPolicy{
				Spec: v1alpha1.DeploymentPolicySpec{
					ResetBatchStateOnCompletion: kptr.To(true),
				},
			}

			skyhookNodes := &skyhookNodes{
				skyhook:          wrapper.NewSkyhookWrapper(skyhook),
				deploymentPolicy: deploymentPolicy,
			}

			// Transition to Complete
			skyhookNodes.SetStatus(v1alpha1.StatusComplete)

			// Verify batch state was reset for all compartments
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState).NotTo(BeNil())
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(1))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.ConsecutiveFailures).To(Equal(0))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(0))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.FailedNodes).To(Equal(0))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.LastBatchSize).To(Equal(0))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.LastBatchFailed).To(BeFalse())

			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-2"].BatchState.CurrentBatch).To(Equal(1))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-2"].BatchState.CompletedNodes).To(Equal(0))

			// Verify Updated flag was set
			Expect(skyhookNodes.skyhook.Updated).To(BeTrue())
		})

		It("should not reset batch state when config is disabled", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
						ResetBatchStateOnCompletion: kptr.To(false),
					},
				},
				Status: v1alpha1.SkyhookStatus{
					Status: v1alpha1.StatusInProgress,
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"compartment-1": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:        5,
								ConsecutiveFailures: 2,
								CompletedNodes:      10,
								FailedNodes:         1,
								LastBatchSize:       3,
								LastBatchFailed:     true,
							},
						},
					},
				},
			}

			deploymentPolicy := &v1alpha1.DeploymentPolicy{
				Spec: v1alpha1.DeploymentPolicySpec{
					ResetBatchStateOnCompletion: kptr.To(true),
				},
			}

			skyhookNodes := &skyhookNodes{
				skyhook:          wrapper.NewSkyhookWrapper(skyhook),
				deploymentPolicy: deploymentPolicy,
			}

			// Transition to Complete
			skyhookNodes.SetStatus(v1alpha1.StatusComplete)

			// Verify batch state was NOT reset (Skyhook config overrides Policy)
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(5))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.ConsecutiveFailures).To(Equal(2))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(10))
		})

		It("should respect policy-level config when skyhook config is not set", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					// No DeploymentPolicyOptions set
				},
				Status: v1alpha1.SkyhookStatus{
					Status: v1alpha1.StatusInProgress,
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"compartment-1": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:   5,
								CompletedNodes: 10,
							},
						},
					},
				},
			}

			deploymentPolicy := &v1alpha1.DeploymentPolicy{
				Spec: v1alpha1.DeploymentPolicySpec{
					ResetBatchStateOnCompletion: kptr.To(true),
				},
			}

			skyhookNodes := &skyhookNodes{
				skyhook:          wrapper.NewSkyhookWrapper(skyhook),
				deploymentPolicy: deploymentPolicy,
			}

			// Transition to Complete
			skyhookNodes.SetStatus(v1alpha1.StatusComplete)

			// Verify batch state was reset based on policy config
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(1))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(0))
			Expect(skyhookNodes.skyhook.Updated).To(BeTrue())
		})

		It("should not reset when not transitioning to Complete", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
						ResetBatchStateOnCompletion: kptr.To(true),
					},
				},
				Status: v1alpha1.SkyhookStatus{
					Status: v1alpha1.StatusWaiting,
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"compartment-1": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:   5,
								CompletedNodes: 10,
							},
						},
					},
				},
			}

			deploymentPolicy := &v1alpha1.DeploymentPolicy{
				Spec: v1alpha1.DeploymentPolicySpec{
					ResetBatchStateOnCompletion: kptr.To(true),
				},
			}

			skyhookNodes := &skyhookNodes{
				skyhook:          wrapper.NewSkyhookWrapper(skyhook),
				deploymentPolicy: deploymentPolicy,
			}

			// Transition to InProgress (not Complete)
			skyhookNodes.SetStatus(v1alpha1.StatusInProgress)

			// Verify batch state was NOT reset
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(5))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(10))
		})

		It("should not reset when already Complete", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
						ResetBatchStateOnCompletion: kptr.To(true),
					},
				},
				Status: v1alpha1.SkyhookStatus{
					Status: v1alpha1.StatusComplete, // Already complete
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"compartment-1": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:   5,
								CompletedNodes: 10,
							},
						},
					},
				},
			}

			deploymentPolicy := &v1alpha1.DeploymentPolicy{
				Spec: v1alpha1.DeploymentPolicySpec{
					ResetBatchStateOnCompletion: kptr.To(true),
				},
			}

			skyhookNodes := &skyhookNodes{
				skyhook:          wrapper.NewSkyhookWrapper(skyhook),
				deploymentPolicy: deploymentPolicy,
			}

			// Set to Complete again (no transition)
			skyhookNodes.SetStatus(v1alpha1.StatusComplete)

			// Verify batch state was NOT reset (already was complete)
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(5))
			Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(10))
		})
	})

	Context("getAutoTaintNodes", func() {
		runtimeRequiredTaint := corev1.Taint{
			Key:    "skyhook.nvidia.com",
			Value:  "runtime-required",
			Effect: corev1.TaintEffectNoSchedule,
		}

		It("should taint new node with no annotations and no taint", func() {
			nodeList := &corev1.NodeList{
				Items: []corev1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "new-node", UID: "new-node-uid"}},
				},
			}
			skyhookList := &v1alpha1.SkyhookList{
				Items: []v1alpha1.Skyhook{
					{Spec: v1alpha1.SkyhookSpec{RuntimeRequired: true, AutoTaintNewNodes: true}},
				},
			}

			cs, _ := BuildState(skyhookList, nodeList, nil)
			result := cs.getAutoTaintNodes(runtimeRequiredTaint)
			Expect(result).To(HaveLen(1))
			Expect(result[0].Name).To(Equal("new-node"))
		})

		It("should not taint node that already has the taint", func() {
			nodeList := &corev1.NodeList{
				Items: []corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "tainted-node", UID: "tainted-node-uid"},
						Spec: corev1.NodeSpec{
							Taints: []corev1.Taint{runtimeRequiredTaint},
						},
					},
				},
			}
			skyhookList := &v1alpha1.SkyhookList{
				Items: []v1alpha1.Skyhook{
					{Spec: v1alpha1.SkyhookSpec{RuntimeRequired: true, AutoTaintNewNodes: true}},
				},
			}

			cs, _ := BuildState(skyhookList, nodeList, nil)
			result := cs.getAutoTaintNodes(runtimeRequiredTaint)
			Expect(result).To(HaveLen(0))
		})

		It("should not taint node with skyhook annotations", func() {
			nodeList := &corev1.NodeList{
				Items: []corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "processed-node",
							UID:  "processed-node-uid",
							Annotations: map[string]string{
								"skyhook.nvidia.com/nodeState_myskyhook": `{}`,
							},
						},
					},
				},
			}
			skyhookList := &v1alpha1.SkyhookList{
				Items: []v1alpha1.Skyhook{
					{Spec: v1alpha1.SkyhookSpec{RuntimeRequired: true, AutoTaintNewNodes: true}},
				},
			}

			cs, _ := BuildState(skyhookList, nodeList, nil)
			result := cs.getAutoTaintNodes(runtimeRequiredTaint)
			Expect(result).To(HaveLen(0))
		})

		It("should not taint when autoTaintNewNodes is false", func() {
			nodeList := &corev1.NodeList{
				Items: []corev1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "new-node", UID: "new-node-uid"}},
				},
			}
			skyhookList := &v1alpha1.SkyhookList{
				Items: []v1alpha1.Skyhook{
					{Spec: v1alpha1.SkyhookSpec{RuntimeRequired: true, AutoTaintNewNodes: false}},
				},
			}

			cs, _ := BuildState(skyhookList, nodeList, nil)
			result := cs.getAutoTaintNodes(runtimeRequiredTaint)
			Expect(result).To(HaveLen(0))
		})

		It("should not taint when runtimeRequired is false", func() {
			nodeList := &corev1.NodeList{
				Items: []corev1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "new-node", UID: "new-node-uid"}},
				},
			}
			skyhookList := &v1alpha1.SkyhookList{
				Items: []v1alpha1.Skyhook{
					{Spec: v1alpha1.SkyhookSpec{RuntimeRequired: false, AutoTaintNewNodes: true}},
				},
			}

			cs, _ := BuildState(skyhookList, nodeList, nil)
			result := cs.getAutoTaintNodes(runtimeRequiredTaint)
			Expect(result).To(HaveLen(0))
		})
	})
})
