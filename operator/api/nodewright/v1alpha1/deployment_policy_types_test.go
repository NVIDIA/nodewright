// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("DeploymentStrategy", func() {

	Describe("getBatchThreshold", func() {
		It("should return default 100 when receiver is nil", func() {
			var s *DeploymentStrategy
			Expect(s.getBatchThreshold()).To(Equal(100))
		})

		It("should return default 100 when strategy is empty", func() {
			s := &DeploymentStrategy{}
			Expect(s.getBatchThreshold()).To(Equal(100))
		})

		It("should return default 100 when Fixed strategy exists but BatchThreshold is nil", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{},
			}
			Expect(s.getBatchThreshold()).To(Equal(100))
		})

		It("should return value when Fixed strategy has BatchThreshold set", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{
					BatchThreshold: ptr.To(80),
				},
			}
			Expect(s.getBatchThreshold()).To(Equal(80))
		})

		It("should return default 100 when Linear strategy exists but BatchThreshold is nil", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{},
			}
			Expect(s.getBatchThreshold()).To(Equal(100))
		})

		It("should return value when Linear strategy has BatchThreshold set", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{
					BatchThreshold: ptr.To(90),
				},
			}
			Expect(s.getBatchThreshold()).To(Equal(90))
		})

		It("should return default 100 when Exponential strategy exists but BatchThreshold is nil", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{},
			}
			Expect(s.getBatchThreshold()).To(Equal(100))
		})

		It("should return value when Exponential strategy has BatchThreshold set", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{
					BatchThreshold: ptr.To(75),
				},
			}
			Expect(s.getBatchThreshold()).To(Equal(75))
		})
	})

	Describe("getSafetyLimit", func() {
		It("should return default 50 when receiver is nil", func() {
			var s *DeploymentStrategy
			Expect(s.getSafetyLimit()).To(Equal(50))
		})

		It("should return default 50 when strategy is empty", func() {
			s := &DeploymentStrategy{}
			Expect(s.getSafetyLimit()).To(Equal(50))
		})

		It("should return default 50 when Fixed strategy exists but SafetyLimit is nil", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{},
			}
			Expect(s.getSafetyLimit()).To(Equal(50))
		})

		It("should return value when Fixed strategy has SafetyLimit set", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{
					SafetyLimit: ptr.To(30),
				},
			}
			Expect(s.getSafetyLimit()).To(Equal(30))
		})

		It("should return default 50 when Linear strategy exists but SafetyLimit is nil", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{},
			}
			Expect(s.getSafetyLimit()).To(Equal(50))
		})

		It("should return value when Linear strategy has SafetyLimit set", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{
					SafetyLimit: ptr.To(40),
				},
			}
			Expect(s.getSafetyLimit()).To(Equal(40))
		})

		It("should return default 50 when Exponential strategy exists but SafetyLimit is nil", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{},
			}
			Expect(s.getSafetyLimit()).To(Equal(50))
		})

		It("should return value when Exponential strategy has SafetyLimit set", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{
					SafetyLimit: ptr.To(60),
				},
			}
			Expect(s.getSafetyLimit()).To(Equal(60))
		})
	})

	Describe("getFailureThreshold", func() {
		It("should return nil when receiver is nil", func() {
			var s *DeploymentStrategy
			Expect(s.getFailureThreshold()).To(BeNil())
		})

		It("should return nil when strategy is empty", func() {
			s := &DeploymentStrategy{}
			Expect(s.getFailureThreshold()).To(BeNil())
		})

		It("should return nil when Fixed strategy exists but FailureThreshold is nil", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{},
			}
			Expect(s.getFailureThreshold()).To(BeNil())
		})

		It("should return value when Fixed strategy has FailureThreshold set", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{
					FailureThreshold: ptr.To(3),
				},
			}
			Expect(s.getFailureThreshold()).To(HaveValue(Equal(3)))
		})

		It("should return nil when Linear strategy exists but FailureThreshold is nil", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{},
			}
			Expect(s.getFailureThreshold()).To(BeNil())
		})

		It("should return value when Linear strategy has FailureThreshold set", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{
					FailureThreshold: ptr.To(5),
				},
			}
			Expect(s.getFailureThreshold()).To(HaveValue(Equal(5)))
		})

		It("should return nil when Exponential strategy exists but FailureThreshold is nil", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{},
			}
			Expect(s.getFailureThreshold()).To(BeNil())
		})

		It("should return value when Exponential strategy has FailureThreshold set", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{
					FailureThreshold: ptr.To(2),
				},
			}
			Expect(s.getFailureThreshold()).To(HaveValue(Equal(2)))
		})
	})

	Describe("CalculateBatchSize dispatch", func() {
		It("should delegate to the Fixed strategy", func() {
			s := &DeploymentStrategy{
				Fixed: &FixedStrategy{InitialBatch: ptr.To(4)},
			}
			Expect(s.CalculateBatchSize(10, &BatchProcessingState{})).To(Equal(4))
		})

		It("should delegate to the Linear strategy", func() {
			s := &DeploymentStrategy{
				Linear: &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(1), SafetyLimit: ptr.To(50)},
			}
			Expect(s.CalculateBatchSize(10, &BatchProcessingState{})).To(Equal(3))
		})

		It("should delegate to the Exponential strategy", func() {
			s := &DeploymentStrategy{
				Exponential: &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(50)},
			}
			Expect(s.CalculateBatchSize(10, &BatchProcessingState{})).To(Equal(2))
		})

		It("should fall back to a batch of 1 when no strategy is set", func() {
			s := &DeploymentStrategy{}
			Expect(s.CalculateBatchSize(10, &BatchProcessingState{})).To(Equal(1))
		})
	})

	DescribeTable("CalculateBatchCapacity preserves strategy limits without the remaining-node cap",
		func(strategy *DeploymentStrategy, state BatchProcessingState, capacity, nextSize int) {
			before := state
			Expect(strategy.CalculateBatchCapacity(10, &state)).To(Equal(capacity))
			Expect(strategy.CalculateBatchSize(10, &state)).To(Equal(nextSize))
			Expect(state).To(Equal(before))
			Expect(strategy.CalculateBatchCapacity(0, &state)).To(BeZero())
		},
		Entry("fixed after every node failed", &DeploymentStrategy{Fixed: &FixedStrategy{InitialBatch: ptr.To(5)}},
			BatchProcessingState{FailedNodes: 10}, 5, 0),
		Entry("fixed bounded by the population", &DeploymentStrategy{Fixed: &FixedStrategy{InitialBatch: ptr.To(20)}},
			BatchProcessingState{CompletedNodes: 8}, 10, 2),
		Entry("linear initial batch", &DeploymentStrategy{Linear: &LinearStrategy{InitialBatch: ptr.To(5)}},
			BatchProcessingState{FailedNodes: 8}, 5, 2),
		Entry("linear slowdown", &DeploymentStrategy{Linear: &LinearStrategy{Delta: ptr.To(2), SafetyLimit: ptr.To(100)}},
			BatchProcessingState{LastBatchSize: 5, LastBatchFailed: true, FailedNodes: 9}, 3, 1),
		Entry("linear beyond the safety limit", &DeploymentStrategy{Linear: &LinearStrategy{Delta: ptr.To(2), SafetyLimit: ptr.To(50)}},
			BatchProcessingState{LastBatchSize: 5, LastBatchFailed: true, CompletedNodes: 5, FailedNodes: 4}, 7, 1),
		Entry("exponential initial batch", &DeploymentStrategy{Exponential: &ExponentialStrategy{InitialBatch: ptr.To(5)}},
			BatchProcessingState{FailedNodes: 8}, 5, 2),
		Entry("exponential slowdown", &DeploymentStrategy{Exponential: &ExponentialStrategy{GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(100)}},
			BatchProcessingState{LastBatchSize: 8, LastBatchFailed: true, FailedNodes: 9}, 4, 1),
		Entry("exponential beyond the safety limit", &DeploymentStrategy{Exponential: &ExponentialStrategy{GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(50)}},
			BatchProcessingState{LastBatchSize: 3, LastBatchFailed: true, CompletedNodes: 5, FailedNodes: 4}, 6, 1),
		Entry("empty strategy fallback", &DeploymentStrategy{}, BatchProcessingState{}, 1, 1),
	)

	Describe("EvaluateBatchResult", func() {
		It("should leave state untouched when the batch size is zero", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{BatchThreshold: ptr.To(100)}}
			state := &BatchProcessingState{CurrentBatch: 2, ConsecutiveFailures: 1}

			s.EvaluateBatchResult(state, 0, 0, 0, 10)

			Expect(state.CurrentBatch).To(Equal(2))
			Expect(state.ConsecutiveFailures).To(Equal(1))
			Expect(state.LastBatchSize).To(BeZero())
		})

		It("should reset consecutive failures when the batch meets the threshold", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{BatchThreshold: ptr.To(100)}}
			state := &BatchProcessingState{ConsecutiveFailures: 2, CompletedNodes: 4}

			s.EvaluateBatchResult(state, 4, 4, 0, 10)

			Expect(state.ConsecutiveFailures).To(BeZero())
			Expect(state.LastBatchFailed).To(BeFalse())
			Expect(state.LastBatchSize).To(Equal(4))
			Expect(state.CurrentBatch).To(Equal(1))
			Expect(state.ShouldStop).To(BeFalse())
		})

		It("should count a consecutive failure when the batch misses the threshold", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{BatchThreshold: ptr.To(100)}}
			state := &BatchProcessingState{CompletedNodes: 3, FailedNodes: 1}

			s.EvaluateBatchResult(state, 4, 3, 1, 10)

			Expect(state.ConsecutiveFailures).To(Equal(1))
			Expect(state.LastBatchFailed).To(BeTrue())
			Expect(state.ShouldStop).To(BeFalse())
		})

		It("should stop once consecutive failures reach the threshold below the safety limit", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{
				BatchThreshold:   ptr.To(100),
				FailureThreshold: ptr.To(2),
				SafetyLimit:      ptr.To(50),
			}}
			state := &BatchProcessingState{ConsecutiveFailures: 1, CompletedNodes: 1, FailedNodes: 1}

			s.EvaluateBatchResult(state, 2, 1, 1, 10)

			Expect(state.ConsecutiveFailures).To(Equal(2))
			Expect(state.ShouldStop).To(BeTrue())
		})

		It("should not stop past the safety limit even when failures reach the threshold", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{
				BatchThreshold:   ptr.To(100),
				FailureThreshold: ptr.To(2),
				SafetyLimit:      ptr.To(50),
			}}
			state := &BatchProcessingState{ConsecutiveFailures: 1, CompletedNodes: 7, FailedNodes: 1}

			s.EvaluateBatchResult(state, 2, 1, 1, 10)

			Expect(state.ConsecutiveFailures).To(Equal(2))
			Expect(state.ShouldStop).To(BeFalse())
		})

		It("should never stop when no failure threshold is configured", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{BatchThreshold: ptr.To(100), SafetyLimit: ptr.To(50)}}
			state := &BatchProcessingState{ConsecutiveFailures: 9}

			s.EvaluateBatchResult(state, 2, 0, 2, 10)

			Expect(state.ConsecutiveFailures).To(Equal(10))
			Expect(state.ShouldStop).To(BeFalse())
		})

		It("should treat progress as zero when there are no nodes to roll out", func() {
			s := &DeploymentStrategy{Fixed: &FixedStrategy{
				BatchThreshold:   ptr.To(100),
				FailureThreshold: ptr.To(1),
				SafetyLimit:      ptr.To(50),
			}}
			state := &BatchProcessingState{}

			s.EvaluateBatchResult(state, 2, 0, 2, 0)

			Expect(state.ShouldStop).To(BeTrue())
		})
	})
})

var _ = Describe("FixedStrategy CalculateBatchSize", func() {
	It("should return the initial batch size", func() {
		s := &FixedStrategy{InitialBatch: ptr.To(5)}
		Expect(s.CalculateBatchSize(20, &BatchProcessingState{})).To(Equal(5))
	})

	It("should clamp to the nodes still remaining", func() {
		s := &FixedStrategy{InitialBatch: ptr.To(5)}
		state := &BatchProcessingState{CompletedNodes: 16, FailedNodes: 2}
		Expect(s.CalculateBatchSize(20, state)).To(Equal(2))
	})

	It("should return zero once every node has been processed", func() {
		s := &FixedStrategy{InitialBatch: ptr.To(5)}
		state := &BatchProcessingState{CompletedNodes: 18, FailedNodes: 2}
		Expect(s.CalculateBatchSize(20, state)).To(BeZero())
	})
})

var _ = Describe("LinearStrategy CalculateBatchSize", func() {
	It("should return zero when there are no nodes", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(1), SafetyLimit: ptr.To(50)}
		Expect(s.CalculateBatchSize(0, &BatchProcessingState{})).To(BeZero())
	})

	It("should use the initial batch size for the first batch", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(1), SafetyLimit: ptr.To(50)}
		Expect(s.CalculateBatchSize(100, &BatchProcessingState{})).To(Equal(3))
	})

	It("should grow by delta after a successful batch", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(2), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 4, CompletedNodes: 4}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(6))
	})

	It("should shrink by delta after a failed batch below the safety limit", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(3), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 8, LastBatchFailed: true, CompletedNodes: 10}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(5))
	})

	It("should never shrink below one", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(5), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 2, LastBatchFailed: true, CompletedNodes: 10}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(1))
	})

	It("should keep growing past the safety limit even after a failed batch", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(2), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 4, LastBatchFailed: true, CompletedNodes: 80}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(6))
	})

	It("should clamp to the nodes still remaining", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(9), Delta: ptr.To(1), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{CompletedNodes: 8}
		Expect(s.CalculateBatchSize(10, state)).To(Equal(2))
	})

	It("should return zero once every node has been processed", func() {
		s := &LinearStrategy{InitialBatch: ptr.To(3), Delta: ptr.To(1), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{CompletedNodes: 10}
		Expect(s.CalculateBatchSize(10, state)).To(BeZero())
	})
})

var _ = Describe("ExponentialStrategy CalculateBatchSize", func() {
	It("should return zero when there are no nodes", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(50)}
		Expect(s.CalculateBatchSize(0, &BatchProcessingState{})).To(BeZero())
	})

	It("should use the initial batch size for the first batch", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(50)}
		Expect(s.CalculateBatchSize(100, &BatchProcessingState{})).To(Equal(2))
	})

	It("should multiply by the growth factor after a successful batch", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(3), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 2, CompletedNodes: 2}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(6))
	})

	It("should divide by the growth factor after a failed batch below the safety limit", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 8, LastBatchFailed: true, CompletedNodes: 10}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(4))
	})

	It("should fall back to the initial batch size when the growth factor is zero", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(0), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 8, CompletedNodes: 10}
		Expect(s.CalculateBatchSize(100, state)).To(Equal(2))
	})

	It("should cap growth at the total node count", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(4), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{LastBatchSize: 30}
		Expect(s.CalculateBatchSize(50, state)).To(Equal(50))
	})

	It("should return zero once every node has been processed", func() {
		s := &ExponentialStrategy{InitialBatch: ptr.To(2), GrowthFactor: ptr.To(2), SafetyLimit: ptr.To(50)}
		state := &BatchProcessingState{CompletedNodes: 10}
		Expect(s.CalculateBatchSize(10, state)).To(BeZero())
	})
})

var _ = Describe("Compartment Validate", func() {
	validBudget := DeploymentBudget{Count: ptr.To(1)}

	It("should accept a compartment with a valid budget, strategy and selector", func() {
		c := &Compartment{
			Name:     "gpu",
			Budget:   validBudget,
			Strategy: &DeploymentStrategy{Fixed: &FixedStrategy{InitialBatch: ptr.To(1)}},
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"accelerator": "gpu"}},
		}
		Expect(c.Validate()).To(Succeed())
	})

	It("should accept a compartment with no strategy override", func() {
		c := &Compartment{Name: "gpu", Budget: validBudget}
		Expect(c.Validate()).To(Succeed())
	})

	It("should reject a budget setting neither percent nor count", func() {
		c := &Compartment{Name: "gpu"}
		Expect(c.Validate()).To(MatchError(ContainSubstring(`compartment "gpu" budget`)))
	})

	It("should reject a strategy that sets no variant", func() {
		c := &Compartment{Name: "gpu", Budget: validBudget, Strategy: &DeploymentStrategy{}}
		Expect(c.Validate()).To(MatchError(ContainSubstring(`compartment "gpu" strategy`)))
	})

	It("should reject an unparsable selector", func() {
		c := &Compartment{
			Name:   "gpu",
			Budget: validBudget,
			Selector: metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "accelerator", Operator: "NotAnOperator"},
				},
			},
		}
		Expect(c.Validate()).To(MatchError(ContainSubstring(`compartment "gpu" has invalid selector`)))
	})
})
