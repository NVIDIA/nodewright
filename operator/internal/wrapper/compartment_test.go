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

package wrapper_test

import (
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	mockwrapper "github.com/NVIDIA/nodewright/operator/internal/wrapper/mock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func newMockNode(t FullGinkgoTInterface, name string, status v1alpha1.Status, complete bool, skyhook *wrapper.Skyhook) *mockwrapper.MockSkyhookNode {
	mock := mockwrapper.NewMockSkyhookNode(t)
	mock.EXPECT().GetNode().Return(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}).Maybe()
	mock.EXPECT().GetSkyhook().Return(skyhook).Maybe()
	mock.EXPECT().Status().Return(status).Maybe()
	mock.EXPECT().IsComplete().Return(complete).Maybe()
	return mock
}

var _ = Describe("Compartment", func() {
	Context("calculateCeiling", func() {
		It("should calculate ceiling for count budget", func() {
			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(3)},
				},
			}

			// Add 10 mock nodes (just need count for ceiling calculation)
			for i := 0; i < 10; i++ {
				compartment.Nodes = append(compartment.Nodes, nil)
			}

			ceiling := wrapper.CalculateCeiling(compartment.Budget, len(compartment.Nodes))
			Expect(ceiling).To(Equal(3))
		})

		It("should calculate ceiling for percent budget", func() {
			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Percent: ptr.To(30)},
				},
			}

			// Add 10 mock nodes - 30% should be 3
			for i := 0; i < 10; i++ {
				compartment.Nodes = append(compartment.Nodes, nil)
			}

			ceiling := wrapper.CalculateCeiling(compartment.Budget, len(compartment.Nodes))
			Expect(ceiling).To(Equal(3)) // max(1, int(10 * 0.3)) = 3
		})

		It("should handle small percent budgets with minimum 1", func() {
			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Percent: ptr.To(30)},
				},
			}

			// Add 2 mock nodes - 30% of 2 = 0.6, should round to 1
			for i := 0; i < 2; i++ {
				compartment.Nodes = append(compartment.Nodes, nil)
			}

			ceiling := wrapper.CalculateCeiling(compartment.Budget, len(compartment.Nodes))
			Expect(ceiling).To(Equal(1)) // max(1, int(2 * 0.3)) = max(1, 0) = 1
		})

		It("should return 0 for no nodes", func() {
			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Percent: ptr.To(50)},
				},
			}

			ceiling := wrapper.CalculateCeiling(compartment.Budget, len(compartment.Nodes))
			Expect(ceiling).To(Equal(0))
		})
	})

	Context("NewCompartmentWrapperWithState", func() {
		It("should create compartment with provided batch state", func() {
			batchState := &v1alpha1.BatchProcessingState{
				CurrentBatch:        3,
				ConsecutiveFailures: 1,
				CompletedNodes:      4,
				FailedNodes:         1,
			}

			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:   "test",
				Budget: v1alpha1.DeploymentBudget{Count: ptr.To(5)},
			}, batchState)

			state := compartment.GetBatchState()
			Expect(state.CurrentBatch).To(Equal(3))
			Expect(state.ConsecutiveFailures).To(Equal(1))
			Expect(state.CompletedNodes).To(Equal(4))
			Expect(state.FailedNodes).To(Equal(1))
		})

		It("should create compartment with default batch state when nil", func() {
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:   "test",
				Budget: v1alpha1.DeploymentBudget{Count: ptr.To(5)},
			}, nil)

			state := compartment.GetBatchState()
			Expect(state.CurrentBatch).To(Equal(1))
			Expect(state.ConsecutiveFailures).To(Equal(0))
			Expect(state.CompletedNodes).To(Equal(0))
			Expect(state.FailedNodes).To(Equal(0))
		})
	})

	Context("EvaluateAndUpdateBatchState", func() {
		It("should update basic state without strategy", func() {
			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:   "test-compartment",
				Budget: v1alpha1.DeploymentBudget{Count: ptr.To(10)},
			}, &v1alpha1.BatchProcessingState{
				CurrentBatch:   1,
				CompletedNodes: 0,
				FailedNodes:    0,
			})

			compartment.EvaluateAndUpdateBatchState(3, 2, 1)

			state := compartment.GetBatchState()
			Expect(state.CurrentBatch).To(Equal(2))
			Expect(state.LastBatchSize).To(Equal(3))
		})

		It("should reset consecutive failures on successful batch", func() {
			strategy := &v1alpha1.DeploymentStrategy{
				Fixed: &v1alpha1.FixedStrategy{
					InitialBatch:     ptr.To(3),
					BatchThreshold:   ptr.To(80),
					FailureThreshold: ptr.To(2),
					SafetyLimit:      ptr.To(50),
				},
			}

			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:     "test-compartment",
				Budget:   v1alpha1.DeploymentBudget{Count: ptr.To(10)},
				Strategy: strategy,
			}, &v1alpha1.BatchProcessingState{
				CurrentBatch:        1,
				CompletedNodes:      4, // Simulating cumulative state after batch evaluation
				FailedNodes:         1,
				ConsecutiveFailures: 1, // Should reset on success
			})

			// Add 10 mock nodes for totalNodes calculation
			for i := 0; i < 10; i++ {
				compartment.Nodes = append(compartment.Nodes, nil)
			}

			// 80% success (4 out of 5) - using delta values
			compartment.EvaluateAndUpdateBatchState(5, 4, 1)

			state := compartment.GetBatchState()
			Expect(state.ConsecutiveFailures).To(Equal(0)) // Should reset
			Expect(state.ShouldStop).To(BeFalse())
		})

		It("should increment consecutive failures and trigger stop when below safety limit", func() {
			strategy := &v1alpha1.DeploymentStrategy{
				Fixed: &v1alpha1.FixedStrategy{
					InitialBatch:     ptr.To(3),
					BatchThreshold:   ptr.To(80),
					FailureThreshold: ptr.To(2),
					SafetyLimit:      ptr.To(50),
				},
			}

			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:     "test-compartment",
				Budget:   v1alpha1.DeploymentBudget{Count: ptr.To(10)},
				Strategy: strategy,
			}, &v1alpha1.BatchProcessingState{
				CurrentBatch:        2,
				CompletedNodes:      1, // After this batch: (1+3)/10 = 40% (below 50% safety limit)
				FailedNodes:         0, // Will add 2 more
				ConsecutiveFailures: 1, // Will increment to 2 (threshold)
			})

			// Add 10 mock nodes for totalNodes calculation
			for i := 0; i < 10; i++ {
				compartment.Nodes = append(compartment.Nodes, nil)
			}

			// 33% success (1 out of 3) - below 80% threshold, progress will be (1+3)/10 = 40% (below safety limit)
			compartment.EvaluateAndUpdateBatchState(3, 1, 2)

			state := compartment.GetBatchState()
			Expect(state.ConsecutiveFailures).To(Equal(2)) // Should increment
			Expect(state.ShouldStop).To(BeTrue())          // Should trigger stop (below safety limit)
		})

		It("should not trigger stop when above safety limit", func() {
			strategy := &v1alpha1.DeploymentStrategy{
				Fixed: &v1alpha1.FixedStrategy{
					InitialBatch:     ptr.To(3),
					BatchThreshold:   ptr.To(80),
					FailureThreshold: ptr.To(2),
					SafetyLimit:      ptr.To(50),
				},
			}

			compartment := wrapper.NewCompartmentWrapper(&v1alpha1.Compartment{
				Name:     "test-compartment",
				Budget:   v1alpha1.DeploymentBudget{Count: ptr.To(10)},
				Strategy: strategy,
			}, &v1alpha1.BatchProcessingState{
				CurrentBatch:        3,
				CompletedNodes:      4, // After this batch: (4+2+3)/10 = 90% but we use cumulative
				FailedNodes:         2, // Total 6 processed, 60% (above 50% safety limit)
				ConsecutiveFailures: 1,
			})

			// Add 10 mock nodes for totalNodes calculation
			for i := 0; i < 10; i++ {
				compartment.Nodes = append(compartment.Nodes, nil)
			}

			// 40% success (2 out of 5) - below 80% threshold, but above safety limit
			// After evaluation: CompletedNodes would be 6, FailedNodes would be 5, total 11 processed
			// For this test, we assume deltas add to existing: 4+2=6 complete, 2+3=5 failed = 11/10
			compartment.EvaluateAndUpdateBatchState(5, 2, 3)

			state := compartment.GetBatchState()
			Expect(state.ConsecutiveFailures).To(Equal(2)) // Should increment
			Expect(state.ShouldStop).To(BeFalse())         // Should NOT stop (above safety limit)
		})
	})

	Context("GetNodesForNextBatch with stickiness", func() {
		var skyhook *wrapper.Skyhook

		BeforeEach(func() {
			skyhook = &wrapper.Skyhook{
				Skyhook: &v1alpha1.Skyhook{},
			}
		})

		It("should return sticky nodes instead of creating a new batch", func() {
			// node-1 is in NodePriority (sticky) and Waiting (between packages)
			// node-2 and node-3 are Waiting but NOT in NodePriority
			skyhook.Status.NodePriority = map[string]metav1.Time{
				"node-1": metav1.Now(),
			}

			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(1)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-3", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			Expect(result).To(HaveLen(1))
			Expect(result[0].GetNode().Name).To(Equal("node-1"))
		})

		It("should skip complete sticky nodes and fall through to new batch", func() {
			// node-1 was in NodePriority but is now Complete
			skyhook.Status.NodePriority = map[string]metav1.Time{
				"node-1": metav1.Now(),
			}

			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(1)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusComplete, true, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-3", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			// Should fall through to createNewBatch and pick node-2
			Expect(result).To(HaveLen(1))
			Expect(result[0].GetNode().Name).To(Equal("node-2"))
		})

		It("should return InProgress nodes even when sticky nodes exist", func() {
			skyhook.Status.NodePriority = map[string]metav1.Time{
				"node-1": metav1.Now(),
				"node-2": metav1.Now(),
			}

			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(1)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusInProgress, false, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			// InProgress takes precedence over sticky
			Expect(result).To(HaveLen(1))
			Expect(result[0].GetNode().Name).To(Equal("node-1"))
		})

		It("should fall through to new batch when NodePriority is nil", func() {
			// No NodePriority set at all
			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(1)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			Expect(result).To(HaveLen(1))
			Expect(result[0].GetNode().Name).To(Equal("node-1"))
		})

		It("should return multiple sticky nodes when batch size allows", func() {
			skyhook.Status.NodePriority = map[string]metav1.Time{
				"node-1": metav1.Now(),
				"node-2": metav1.Now(),
			}

			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(3)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-3", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			// Both sticky nodes returned
			Expect(result).To(HaveLen(2))
			names := []string{result[0].GetNode().Name, result[1].GetNode().Name}
			Expect(names).To(ConsistOf("node-1", "node-2"))
		})

		It("should not include non-sticky nodes in the sticky batch", func() {
			// Only node-1 is sticky — node-2 and node-3 must NOT be returned
			// even though they are Waiting and not Complete
			skyhook.Status.NodePriority = map[string]metav1.Time{
				"node-1": metav1.Now(),
			}

			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(3)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-3", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			Expect(result).To(HaveLen(1))
			Expect(result[0].GetNode().Name).To(Equal("node-1"))
			// Explicitly verify the other nodes are NOT in the result
			for _, node := range result {
				Expect(node.GetNode().Name).NotTo(Equal("node-2"))
				Expect(node.GetNode().Name).NotTo(Equal("node-3"))
			}
		})

		It("should not leak non-sticky nodes even when budget exceeds sticky count", func() {
			// Budget allows 5 nodes, but only 2 are sticky — should return exactly 2
			skyhook.Status.NodePriority = map[string]metav1.Time{
				"node-1": metav1.Now(),
				"node-3": metav1.Now(),
			}

			compartment := &wrapper.Compartment{
				Compartment: v1alpha1.Compartment{
					Budget: v1alpha1.DeploymentBudget{Count: ptr.To(5)},
				},
				BatchState: v1alpha1.BatchProcessingState{CurrentBatch: 1},
				Nodes: []wrapper.SkyhookNode{
					newMockNode(GinkgoT(), "node-1", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-2", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-3", v1alpha1.StatusWaiting, false, skyhook),
					newMockNode(GinkgoT(), "node-4", v1alpha1.StatusWaiting, false, skyhook),
				},
			}

			result := compartment.GetNodesForNextBatch()
			Expect(result).To(HaveLen(2))
			names := []string{result[0].GetNode().Name, result[1].GetNode().Name}
			Expect(names).To(ConsistOf("node-1", "node-3"))
			Expect(names).NotTo(ContainElement("node-2"))
			Expect(names).NotTo(ContainElement("node-4"))
		})
	})
})
