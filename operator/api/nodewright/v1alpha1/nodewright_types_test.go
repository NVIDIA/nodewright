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
	"encoding/base64"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("StateToStatus", func() {
	DescribeTable("maps a package State onto a node Status",
		func(state State, expected Status) {
			Expect(StateToStatus(state)).To(Equal(expected))
		},
		Entry("complete", StateComplete, StatusComplete),
		Entry("erroring", StateErroring, StatusErroring),
		Entry("in_progress", StateInProgress, StatusInProgress),
		Entry("skipped has no Status of its own", StateSkipped, StatusUnknown),
		Entry("unknown", StateUnknown, StatusUnknown),
		Entry("a State the operator never writes", State("nonsense"), StatusUnknown),
	)
})

var _ = Describe("NodeWright WasUpdated", func() {
	It("should be false for a freshly created NodeWright", func() {
		nw := &NodeWright{ObjectMeta: metav1.ObjectMeta{Generation: 1}}
		Expect(nw.WasUpdated()).To(BeFalse())
	})

	It("should be false once the observed generation has caught up", func() {
		nw := &NodeWright{ObjectMeta: metav1.ObjectMeta{Generation: 3}}
		nw.Status.ObservedGeneration = 3
		Expect(nw.WasUpdated()).To(BeFalse())
	})

	It("should be true when the spec has moved ahead of the observed generation", func() {
		nw := &NodeWright{ObjectMeta: metav1.ObjectMeta{Generation: 3}}
		nw.Status.ObservedGeneration = 2
		Expect(nw.WasUpdated()).To(BeTrue())
	})
})

var _ = Describe("NodeWright pause and disable annotations", func() {
	pauseKey := fmt.Sprintf("%s/pause", METADATA_PREFIX)
	disableKey := fmt.Sprintf("%s/disable", METADATA_PREFIX)

	DescribeTable("IsPaused",
		func(annotations map[string]string, expected bool) {
			nw := &NodeWright{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}
			Expect(nw.IsPaused()).To(Equal(expected))
		},
		Entry("no annotations at all", nil, false),
		Entry("unrelated annotations only", map[string]string{"other": "true"}, false),
		Entry("pause=true", map[string]string{pauseKey: "true"}, true),
		Entry("pause=false", map[string]string{pauseKey: "false"}, false),
		Entry("pause set to something else", map[string]string{pauseKey: "yes"}, false),
	)

	DescribeTable("IsDisabled",
		func(annotations map[string]string, expected bool) {
			nw := &NodeWright{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}}
			Expect(nw.IsDisabled()).To(Equal(expected))
		},
		Entry("no annotations at all", nil, false),
		Entry("unrelated annotations only", map[string]string{"other": "true"}, false),
		Entry("disable=true", map[string]string{disableKey: "true"}, true),
		Entry("disable=false", map[string]string{disableKey: "false"}, false),
	)
})

var _ = Describe("NodeWright ResetCompartmentBatchStates", func() {
	It("should report nothing to do when there are no compartment statuses", func() {
		nw := &NodeWright{}
		Expect(nw.ResetCompartmentBatchStates()).To(BeFalse())
	})

	It("should reset every compartment's batch state to a fresh first batch", func() {
		nw := &NodeWright{}
		nw.Status.CompartmentStatuses = map[string]CompartmentStatus{
			"gpu": {BatchState: &BatchProcessingState{
				CurrentBatch:        7,
				ConsecutiveFailures: 3,
				CompletedNodes:      12,
				FailedNodes:         4,
				ShouldStop:          true,
				LastBatchSize:       6,
				LastBatchFailed:     true,
			}},
			"cpu": {BatchState: nil},
		}

		Expect(nw.ResetCompartmentBatchStates()).To(BeTrue())

		for name, cs := range nw.Status.CompartmentStatuses {
			Expect(cs.BatchState).ToNot(BeNil(), "compartment %s", name)
			Expect(*cs.BatchState).To(Equal(BatchProcessingState{CurrentBatch: 1}))
		}
	})
})

var _ = Describe("InterruptionBudget Validate", func() {
	It("should accept a budget with only a count", func() {
		Expect((&InterruptionBudget{Count: ptr.To(3)}).Validate()).To(Succeed())
	})

	It("should accept a budget with only a percent", func() {
		Expect((&InterruptionBudget{Percent: ptr.To(10)}).Validate()).To(Succeed())
	})

	It("should accept an empty budget", func() {
		Expect((&InterruptionBudget{}).Validate()).To(Succeed())
	})

	It("should reject a budget setting both count and percent", func() {
		err := (&InterruptionBudget{Count: ptr.To(3), Percent: ptr.To(10)}).Validate()
		Expect(err).To(MatchError(ContainSubstring("both percent and count can not be set at the same time")))
	})
})

var _ = Describe("Interrupt ToArgs", func() {
	decode := func(encoded string) map[string]any {
		GinkgoHelper()
		raw, err := base64.StdEncoding.DecodeString(encoded)
		Expect(err).ToNot(HaveOccurred())
		out := map[string]any{}
		Expect(json.Unmarshal(raw, &out)).To(Succeed())
		return out
	}

	DescribeTable("translates the CRD interrupt type to the name the agent expects",
		func(crdType InterruptType, agentType string) {
			interrupt := &Interrupt{Type: crdType}

			encoded, err := interrupt.ToArgs()
			Expect(err).ToNot(HaveOccurred())
			Expect(decode(encoded)).To(HaveKeyWithValue("type", agentType))
			Expect(interrupt.Type).To(Equal(crdType), "ToArgs must not mutate the receiver")
		},
		Entry("reboot", REBOOT, "node_restart"),
		Entry("service", SERVICE, "service_restart"),
		Entry("noop", NOOP, "no_op"),
		Entry("restartAllServices", RESTART_ALL_SERVICES, "restart_all_services"),
	)

	It("should carry the service list through", func() {
		encoded, err := (&Interrupt{Type: SERVICE, Services: []string{"containerd", "kubelet"}}).ToArgs()
		Expect(err).ToNot(HaveOccurred())
		Expect(decode(encoded)).To(HaveKeyWithValue("services", ConsistOf("containerd", "kubelet")))
	})
})

var _ = Describe("NodeWrightSpec BuildGraph", func() {
	It("should build a graph from packages with dependencies", func() {
		spec := &NodeWrightSpec{Packages: Packages{
			"base": Package{PackageRef: PackageRef{Name: "base", Version: "1.0.0"}},
			"tuning": Package{
				PackageRef: PackageRef{Name: "tuning", Version: "2.0.0"},
				DependsOn:  map[string]string{"base": "1.0.0"},
			},
		}}

		dependencyGraph, err := spec.BuildGraph()
		Expect(err).ToNot(HaveOccurred())
		Expect(dependencyGraph).ToNot(BeNil())

		ready, err := dependencyGraph.Next()
		Expect(err).ToNot(HaveOccurred())
		Expect(ready).To(Equal([]string{"base|1.0.0"}), "only the dependency-free package is ready first")

		ready, err = dependencyGraph.Next("base|1.0.0")
		Expect(err).ToNot(HaveOccurred())
		Expect(ready).To(Equal([]string{"tuning|2.0.0"}), "tuning unblocks once base is done")
	})

	It("should reject a dependency pinned to an empty version", func() {
		spec := &NodeWrightSpec{Packages: Packages{
			"tuning": Package{
				PackageRef: PackageRef{Name: "tuning", Version: "2.0.0"},
				DependsOn:  map[string]string{"base": ""},
			},
		}}

		_, err := spec.BuildGraph()
		Expect(err).To(MatchError(ContainSubstring("DependsOn version is empty for [base]")))
	})

	// Packages is keyed by map key, but the graph is keyed by name|version, so two
	// entries can collide on the graph key while looking distinct in the spec.
	It("should reject two packages that collide on name and version", func() {
		spec := &NodeWrightSpec{Packages: Packages{
			"tuning":      Package{PackageRef: PackageRef{Name: "tuning", Version: "2.0.0"}},
			"tuning-copy": Package{PackageRef: PackageRef{Name: "tuning", Version: "2.0.0"}},
		}}

		_, err := spec.BuildGraph()
		Expect(err).To(MatchError(ContainSubstring("error building graph from packages")))
	})
})

var _ = Describe("NodeState", func() {

	Describe("RemoveState", func() {
		It("should report no change when the state map is nil", func() {
			var ns NodeState
			Expect(ns.RemoveState(PackageRef{Name: "foo", Version: "1.0.0"})).To(BeFalse())
		})

		It("should report no change when the package is not present", func() {
			ns := NodeState{"foo|1.0.0": PackageStatus{Name: "foo", Version: "1.0.0"}}
			Expect(ns.RemoveState(PackageRef{Name: "bar", Version: "2.0.0"})).To(BeFalse())
			Expect(ns).To(HaveLen(1))
		})

		It("should treat a version bump as a different package", func() {
			ns := NodeState{"foo|1.0.0": PackageStatus{Name: "foo", Version: "1.0.0"}}
			Expect(ns.RemoveState(PackageRef{Name: "foo", Version: "1.0.1"})).To(BeFalse())
			Expect(ns).To(HaveKey("foo|1.0.0"))
		})

		It("should remove the package and report the change", func() {
			ns := NodeState{
				"foo|1.0.0": PackageStatus{Name: "foo", Version: "1.0.0"},
				"bar|2.0.0": PackageStatus{Name: "bar", Version: "2.0.0"},
			}
			Expect(ns.RemoveState(PackageRef{Name: "foo", Version: "1.0.0"})).To(BeTrue())
			Expect(ns).ToNot(HaveKey("foo|1.0.0"))
			Expect(ns).To(HaveKey("bar|2.0.0"))
		})
	})

	Describe("IsUninstalled", func() {
		It("should treat a nil state map as uninstalled", func() {
			var ns NodeState
			Expect(ns.IsUninstalled("foo|1.0.0")).To(BeTrue())
		})

		It("should treat an absent package as uninstalled", func() {
			ns := NodeState{"bar|2.0.0": PackageStatus{Name: "bar", Version: "2.0.0"}}
			Expect(ns.IsUninstalled("foo|1.0.0")).To(BeTrue())
		})

		It("should treat a package still in state as not uninstalled", func() {
			ns := NodeState{"foo|1.0.0": PackageStatus{Name: "foo", Version: "1.0.0"}}
			Expect(ns.IsUninstalled("foo|1.0.0")).To(BeFalse())
		})
	})

	Describe("Contains", func() {
		packages := Packages{
			"foo": Package{PackageRef: PackageRef{Name: "foo", Version: "1.0.0"}},
			"bar": Package{PackageRef: PackageRef{Name: "bar", Version: "2.0.0"}},
		}

		It("should be true when state holds every package at the spec's version", func() {
			ns := NodeState{
				"foo|1.0.0": PackageStatus{Name: "foo", Version: "1.0.0"},
				"bar|2.0.0": PackageStatus{Name: "bar", Version: "2.0.0"},
			}
			Expect(ns.Contains(packages)).To(BeTrue())
		})

		It("should tolerate state holding extra packages the spec dropped", func() {
			ns := NodeState{
				"foo|1.0.0":   PackageStatus{Name: "foo", Version: "1.0.0"},
				"bar|2.0.0":   PackageStatus{Name: "bar", Version: "2.0.0"},
				"stale|9.0.0": PackageStatus{Name: "stale", Version: "9.0.0"},
			}
			Expect(ns.Contains(packages)).To(BeTrue())
		})

		It("should be false when state holds fewer packages than the spec", func() {
			ns := NodeState{"foo|1.0.0": PackageStatus{Name: "foo", Version: "1.0.0"}}
			Expect(ns.Contains(packages)).To(BeFalse())
		})

		It("should be false when a spec package is missing from state", func() {
			ns := NodeState{
				"foo|1.0.0":   PackageStatus{Name: "foo", Version: "1.0.0"},
				"other|1.0.0": PackageStatus{Name: "other", Version: "1.0.0"},
			}
			Expect(ns.Contains(packages)).To(BeFalse())
		})

		// The key already encodes the version, so a status whose Version disagrees with
		// its own key can only come from a hand-edited or corrupted node annotation.
		// Contains still has to reject it rather than trust the key.
		It("should be false when a status contradicts the version in its own key", func() {
			ns := NodeState{
				"foo|1.0.0": PackageStatus{Name: "foo", Version: "0.9.0"},
				"bar|2.0.0": PackageStatus{Name: "bar", Version: "2.0.0"},
			}
			Expect(ns.Contains(packages)).To(BeFalse())
		})
	})
})

var _ = Describe("PackageStatus predicates", func() {

	DescribeTable("IsInterruptStage",
		func(status *PackageStatus, expected bool) {
			Expect(status.IsInterruptStage()).To(Equal(expected))
		},
		Entry("nil status is in no Stage", nil, false),
		Entry("interrupt", &PackageStatus{Stage: StageInterrupt}, true),
		Entry("uninstall-interrupt", &PackageStatus{Stage: StageUninstallInterrupt}, true),
		Entry("apply", &PackageStatus{Stage: StageApply}, false),
		Entry("post-interrupt", &PackageStatus{Stage: StagePostInterrupt}, false),
	)

	DescribeTable("IsActive",
		func(status *PackageStatus, expected bool) {
			Expect(status.IsActive()).To(Equal(expected))
		},
		Entry("nil status is not active", nil, false),
		Entry("in_progress", &PackageStatus{State: StateInProgress}, true),
		Entry("erroring", &PackageStatus{State: StateErroring}, true),
		Entry("complete", &PackageStatus{State: StateComplete}, false),
		Entry("skipped", &PackageStatus{State: StateSkipped}, false),
	)

	DescribeTable("IsSkipped",
		func(status *PackageStatus, expected bool) {
			Expect(status.IsSkipped()).To(Equal(expected))
		},
		Entry("nil status is not skipped", nil, false),
		Entry("skipped", &PackageStatus{State: StateSkipped}, true),
		Entry("complete", &PackageStatus{State: StateComplete}, false),
	)
})
