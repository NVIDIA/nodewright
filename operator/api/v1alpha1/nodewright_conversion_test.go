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

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// fullSkyhook builds a Skyhook with every Spec and Status field populated to a
// distinct non-zero value, including nested structs, non-empty slices and maps,
// a populated resource.Quantity, a non-nil *metav1.Duration, a non-empty
// []metav1.Condition, and a non-empty map[string]metav1.Time. It is the fixture
// behind the exhaustive fidelity test and the zero-value guard: a field that the
// converter forgets shows up as zero in the output and fails MatchJSON.
func fullSkyhook() *Skyhook {
	return &Skyhook{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "demo",
			ResourceVersion: "12345",
			UID:             "abc-123",
			Labels:          map[string]string{"team": "infra"},
			Annotations:     map[string]string{"skyhook.nvidia.com/pause": "true"},
		},
		Spec: SkyhookSpec{
			Serial:            true,
			RuntimeRequired:   true,
			AutoTaintNewNodes: true,
			Priority:          50,
			DeploymentPolicy:  "policy-a",
			Sequencing:        SequencingAll,
			PodNonInterruptLabels: metav1.LabelSelector{
				MatchLabels: map[string]string{"pod": "keep"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "pod", Operator: metav1.LabelSelectorOpIn, Values: []string{"keep"}},
				},
			},
			NodeSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"node": "target"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "node", Operator: metav1.LabelSelectorOpIn, Values: []string{"target"}},
				},
			},
			DeploymentPolicyOptions: &DeploymentPolicyOptions{
				ResetBatchStateOnCompletion: ptr.To(true),
			},
			InterruptionBudget: InterruptionBudget{
				Percent: ptr.To(25),
				Count:   ptr.To(3),
			},
			DrainConfig: &DrainConfig{
				DisableEviction:    ptr.To(true),
				DeleteEmptyDirData: ptr.To(true),
				Force:              ptr.To(true),
				IgnoreDaemonSets:   ptr.To(true),
				Timeout:            &metav1.Duration{Duration: 5 * time.Minute},
				GracePeriod:        &metav1.Duration{Duration: 30 * time.Second},
			},
			Packages: Packages{
				"tuning": Package{
					PackageRef:         PackageRef{Name: "tuning", Version: "1.2.3"},
					Image:              "alpine",
					ContainerSHA:       "sha256:deadbeef",
					AgentImageOverride: "alpine:3.21.0",
					Interrupt: &Interrupt{
						Type:     SERVICE,
						Services: []string{"kubelet"},
					},
					DependsOn: map[string]string{"base": "0.1.0"},
					ConfigInterrupts: map[string]Interrupt{
						"foo.conf": {Type: REBOOT, Services: []string{"containerd"}},
					},
					ConfigMap: map[string]string{"foo.conf": "bar"},
					Env: []corev1.EnvVar{
						{Name: "FOO", Value: "BAR"},
					},
					Resources: &ResourceRequirements{
						CPURequest:    resource.MustParse("500m"),
						CPULimit:      resource.MustParse("1"),
						MemoryRequest: resource.MustParse("64Mi"),
						MemoryLimit:   resource.MustParse("128Mi"),
					},
					GracefulShutdown: &metav1.Duration{Duration: 30 * time.Second},
					Uninstall: &Uninstall{
						Enabled: true,
						Apply:   true,
					},
				},
			},
			AdditionalTolerations: []corev1.Toleration{
				{
					Key:               "runtime",
					Operator:          corev1.TolerationOpEqual,
					Value:             "required",
					Effect:            corev1.TaintEffectNoSchedule,
					TolerationSeconds: ptr.To(int64(60)),
				},
			},
		},
		Status: SkyhookStatus{
			ObservedGeneration: 7,
			NodeState: map[string]NodeState{
				"node-1": {
					"tuning|1.2.3": PackageStatus{
						Name:             "tuning",
						Version:          "1.2.3",
						Image:            "alpine",
						ContainerSHA:     "sha256:deadbeef",
						Stage:            StageConfig,
						State:            StateComplete,
						Restarts:         2,
						InterruptPending: true,
					},
				},
			},
			NodeStatus: map[string]Status{
				"node-1": StatusInProgress,
			},
			Status: StatusInProgress,
			Conditions: []metav1.Condition{
				{
					Type:               "Available",
					Status:             metav1.ConditionTrue,
					Reason:             "AllGood",
					Message:            "everything is fine",
					ObservedGeneration: 7,
					LastTransitionTime: metav1.NewTime(time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)),
				},
			},
			NodeBootIds: map[string]string{"node-1": "boot-xyz"},
			NodePriority: map[string]metav1.Time{
				"node-1": metav1.NewTime(time.Date(2026, 6, 23, 11, 0, 0, 0, time.UTC)),
			},
			NodeOrderOffset: 4,
			ConfigUpdates: map[string][]string{
				"tuning": {"foo.conf"},
			},
			CompartmentStatuses: map[string]CompartmentStatus{
				"gpu": {
					Matched:         10,
					Ceiling:         5,
					InProgress:      2,
					Completed:       3,
					ProgressPercent: 30,
					BatchState: &BatchProcessingState{
						CurrentBatch:        2,
						ConsecutiveFailures: 1,
						CompletedNodes:      3,
						FailedNodes:         1,
						ShouldStop:          true,
						LastBatchSize:       2,
						LastBatchFailed:     true,
					},
				},
			},
			NodesInProgress: 2,
			CompleteNodes:   "3/5",
			PackageList:     "tuning",
		},
	}
}

// fullDeploymentPolicy builds a DeploymentPolicy with every Spec field populated
// to a distinct non-zero value, including both default and per-compartment
// strategy variants.
func fullDeploymentPolicy() *DeploymentPolicy {
	return &DeploymentPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "policy",
			ResourceVersion: "9999",
			UID:             "def-456",
			Labels:          map[string]string{"team": "infra"},
			Annotations:     map[string]string{"skyhook.nvidia.com/pause": "true"},
		},
		Spec: DeploymentPolicySpec{
			Default: PolicyDefault{
				Budget: DeploymentBudget{
					Percent: ptr.To(20),
					Count:   ptr.To(2),
				},
				Strategy: &DeploymentStrategy{
					Fixed: &FixedStrategy{
						InitialBatch:     ptr.To(1),
						BatchThreshold:   ptr.To(80),
						FailureThreshold: ptr.To(3),
						SafetyLimit:      ptr.To(40),
					},
					Linear: &LinearStrategy{
						InitialBatch:     ptr.To(2),
						Delta:            ptr.To(2),
						BatchThreshold:   ptr.To(70),
						FailureThreshold: ptr.To(4),
						SafetyLimit:      ptr.To(45),
					},
					Exponential: &ExponentialStrategy{
						InitialBatch:     ptr.To(1),
						GrowthFactor:     ptr.To(2),
						BatchThreshold:   ptr.To(60),
						FailureThreshold: ptr.To(5),
						SafetyLimit:      ptr.To(50),
					},
				},
			},
			Compartments: []Compartment{
				{
					Name: "gpu",
					Selector: metav1.LabelSelector{
						MatchLabels: map[string]string{"gpu": "true"},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "gpu", Operator: metav1.LabelSelectorOpIn, Values: []string{"true"}},
						},
					},
					Budget: DeploymentBudget{
						Percent: ptr.To(10),
						Count:   ptr.To(1),
					},
					Strategy: &DeploymentStrategy{
						Fixed: &FixedStrategy{
							InitialBatch:     ptr.To(3),
							BatchThreshold:   ptr.To(90),
							FailureThreshold: ptr.To(2),
							SafetyLimit:      ptr.To(35),
						},
						Linear: &LinearStrategy{
							InitialBatch:     ptr.To(1),
							Delta:            ptr.To(1),
							BatchThreshold:   ptr.To(85),
							FailureThreshold: ptr.To(3),
							SafetyLimit:      ptr.To(30),
						},
						Exponential: &ExponentialStrategy{
							InitialBatch:     ptr.To(2),
							GrowthFactor:     ptr.To(3),
							BatchThreshold:   ptr.To(75),
							FailureThreshold: ptr.To(4),
							SafetyLimit:      ptr.To(55),
						},
					},
				},
			},
			ResetBatchStateOnCompletion: ptr.To(true),
		},
	}
}

// requireAllFieldsPopulated walks v recursively and fails if any field is its
// zero value. The intent is self-maintaining coverage: when someone adds a field
// to SkyhookSpec/SkyhookStatus/DeploymentPolicySpec, this guard fails until the
// fixture populates it, which in turn forces the converter to handle it.
//
// Recursion rules (pragmatic, not fully general):
//   - structs: recurse into every exported field.
//   - pointers / maps / slices: must be non-nil and (for maps/slices) have at
//     least one element; recurse into the element(s).
//   - strings: must be non-empty; numbers/bools: must be non-zero/true.
//
// Known limitation: shared k8s types (resource.Quantity, metav1.Time,
// metav1.Duration, metav1.Condition, corev1.EnvVar, corev1.Toleration) are
// treated as opaque leaves, checked only for non-zero via reflect.Value.IsZero
// rather than recursed into. They are upstream machinery whose nested optional
// fields (e.g. EnvVar.ValueFrom) the fixture cannot meaningfully populate, and
// the converter copies/deep-copies them wholesale. The guard's job is to catch
// drift in our own SkyhookSpec/Status schema, not in apimachinery/core. The
// fixture still populates each opaque value to a non-zero state so the IsZero
// check has teeth.
func requireAllFieldsPopulated(path string, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		ExpectWithOffset(1, v.IsNil()).To(BeFalse(), "%s: pointer must be non-nil", path)
		requireAllFieldsPopulated(path, v.Elem())
	case reflect.Struct:
		switch v.Type().String() {
		// Shared k8s types are opaque leaves: they are upstream machinery the
		// fixture cannot (and need not) populate field-by-field, and the
		// converter copies/deep-copies them wholesale. The guard exists to catch
		// drift in our own SkyhookSpec/Status schema, not in apimachinery/core.
		case "resource.Quantity", "v1.Time", "v1.Duration", "v1.MicroTime",
			"v1.Condition", "v1.EnvVar", "v1.Toleration":
			ExpectWithOffset(1, v.IsZero()).To(BeFalse(), "%s: opaque k8s value must be non-zero", path)
			return
		}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported
			}
			requireAllFieldsPopulated(fmt.Sprintf("%s.%s", path, f.Name), v.Field(i))
		}
	case reflect.Map:
		ExpectWithOffset(1, v.IsNil()).To(BeFalse(), "%s: map must be non-nil", path)
		ExpectWithOffset(1, v.Len()).To(BeNumerically(">", 0), "%s: map must have an element", path)
		iter := v.MapRange()
		for iter.Next() {
			requireAllFieldsPopulated(fmt.Sprintf("%s[%v]", path, iter.Key().Interface()), iter.Value())
		}
	case reflect.Slice:
		ExpectWithOffset(1, v.IsNil()).To(BeFalse(), "%s: slice must be non-nil", path)
		ExpectWithOffset(1, v.Len()).To(BeNumerically(">", 0), "%s: slice must have an element", path)
		for i := 0; i < v.Len(); i++ {
			requireAllFieldsPopulated(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
		}
	case reflect.String:
		ExpectWithOffset(1, v.Len()).To(BeNumerically(">", 0), "%s: string must be non-empty", path)
	case reflect.Bool:
		ExpectWithOffset(1, v.Bool()).To(BeTrue(), "%s: bool must be true", path)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		ExpectWithOffset(1, v.Int()).NotTo(BeZero(), "%s: int must be non-zero", path)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		ExpectWithOffset(1, v.Uint()).NotTo(BeZero(), "%s: uint must be non-zero", path)
	case reflect.Float32, reflect.Float64:
		ExpectWithOffset(1, v.Float()).NotTo(BeZero(), "%s: float must be non-zero", path)
	default:
		ExpectWithOffset(1, v.IsZero()).To(BeFalse(), "%s: %s must be non-zero", path, v.Kind())
	}
}

var _ = Describe("Convert_Skyhook_To_NodeWright", func() {

	var in *Skyhook

	BeforeEach(func() {
		in = fullSkyhook()
	})

	It("populates every Spec and Status field in the fixture (zero-value guard)", func() {
		// Self-maintaining: a new schema field that the fixture forgets to set
		// fails here, forcing both fixture and converter to cover it.
		requireAllFieldsPopulated("Spec", reflect.ValueOf(in.Spec))
		requireAllFieldsPopulated("Status", reflect.ValueOf(in.Status))
	})

	It("copies ObjectMeta and translates the skyhook.nvidia.com/ prefix to nodewright.nvidia.com/", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Name).To(Equal("demo"))
		Expect(out.Labels).To(Equal(map[string]string{"team": "infra"}))
		// skyhook.nvidia.com/pause must become nodewright.nvidia.com/pause so the new
		// reconciler (which reads nodewright.nvidia.com/*) sees the object as paused.
		Expect(out.Annotations).To(Equal(map[string]string{"nodewright.nvidia.com/pause": "true"}))
	})

	It("lets explicit nodewright.nvidia.com metadata win on a prefix collision (deterministic)", func() {
		// If both prefixes carry the same suffix, the rekey must not depend on Go map
		// iteration order: the explicit nodewright.nvidia.com value always wins.
		in.Annotations = map[string]string{
			"skyhook.nvidia.com/pause":    "true",
			"nodewright.nvidia.com/pause": "false",
		}
		in.Labels = map[string]string{
			"skyhook.nvidia.com/team":    "legacy",
			"nodewright.nvidia.com/team": "infra",
		}
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Annotations).To(HaveKeyWithValue("nodewright.nvidia.com/pause", "false"))
		Expect(out.Labels).To(HaveKeyWithValue("nodewright.nvidia.com/team", "infra"))
	})

	It("clears the legacy finalizer (the new reconciler manages its own)", func() {
		in.Finalizers = []string{"skyhook.nvidia.com/skyhook"}
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Finalizers).To(BeEmpty())
	})

	It("converts the whole Spec with full fidelity", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		inJSON, err := json.Marshal(in.Spec)
		Expect(err).NotTo(HaveOccurred())
		outJSON, err := json.Marshal(out.Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(outJSON).To(MatchJSON(inJSON))
	})

	It("converts the whole Status with full fidelity", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Status.ObservedGeneration).To(Equal(int64(7)))
		Expect(out.Status.Status).To(Equal(nwv1.StatusInProgress))

		inJSON, err := json.Marshal(in.Status)
		Expect(err).NotTo(HaveOccurred())
		outJSON, err := json.Marshal(out.Status)
		Expect(err).NotTo(HaveOccurred())
		Expect(outJSON).To(MatchJSON(inJSON))
	})

	It("sets TypeMeta to the NodeWright GVK", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		Expect(out.APIVersion).To(Equal("nodewright.nvidia.com/v1alpha1"))
		Expect(out.Kind).To(Equal("NodeWright"))
	})

	It("clears ResourceVersion and UID on the converted object", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		Expect(out.ResourceVersion).To(BeEmpty())
		Expect(out.UID).To(BeEmpty())
	})

	It("deep-copies reference fields so mutating the source does not affect the output", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		// Snapshot the converted values that the source mutations below target.
		convertedConditionCount := len(out.Status.Conditions)
		convertedNodePriority := out.Status.NodePriority["node-1"]
		convertedPkg := out.Spec.Packages["tuning"]
		convertedImage := convertedPkg.Image
		convertedServices := append([]string(nil), convertedPkg.Interrupt.Services...)

		// Mutate the SOURCE's reference fields after conversion.
		in.Status.Conditions = append(in.Status.Conditions, metav1.Condition{Type: "Extra"})
		in.Status.NodePriority["node-1"] = metav1.NewTime(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
		srcPkg := in.Spec.Packages["tuning"]
		srcPkg.Image = "mutated"
		srcPkg.Interrupt.Services[0] = "mutated-service"
		in.Spec.Packages["tuning"] = srcPkg

		// The converted output must be unchanged.
		Expect(out.Status.Conditions).To(HaveLen(convertedConditionCount))
		Expect(out.Status.NodePriority["node-1"]).To(Equal(convertedNodePriority))
		Expect(out.Spec.Packages["tuning"].Image).To(Equal(convertedImage))
		Expect(out.Spec.Packages["tuning"].Interrupt.Services).To(Equal(convertedServices))
	})

	It("deep-copies the resource.Quantity in package resources", func() {
		out := &nwv1.NodeWright{}
		Expect(Convert_Skyhook_To_NodeWright(in, out)).To(Succeed())

		original := out.Spec.Packages["tuning"].Resources.CPURequest.String()

		srcPkg := in.Spec.Packages["tuning"]
		mutated := resource.MustParse("999m")
		srcPkg.Resources.CPURequest = mutated
		in.Spec.Packages["tuning"] = srcPkg

		Expect(out.Spec.Packages["tuning"].Resources.CPURequest.String()).To(Equal(original))
	})
})

var _ = Describe("Convert_DeploymentPolicy_To_NodeWright", func() {

	var in *DeploymentPolicy

	BeforeEach(func() {
		in = fullDeploymentPolicy()
	})

	It("populates every Spec field in the fixture (zero-value guard)", func() {
		requireAllFieldsPopulated("Spec", reflect.ValueOf(in.Spec))
	})

	It("copies ObjectMeta and clears ResourceVersion/UID", func() {
		out := &nwv1.DeploymentPolicy{}
		Expect(Convert_DeploymentPolicy_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Name).To(Equal("policy"))
		Expect(out.Labels).To(Equal(map[string]string{"team": "infra"}))
		Expect(out.ResourceVersion).To(BeEmpty())
		Expect(out.UID).To(BeEmpty())
	})

	It("translates the skyhook.nvidia.com/ prefix to nodewright.nvidia.com/", func() {
		out := &nwv1.DeploymentPolicy{}
		Expect(Convert_DeploymentPolicy_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Annotations).To(Equal(map[string]string{"nodewright.nvidia.com/pause": "true"}))
	})

	It("clears the legacy finalizer", func() {
		in.Finalizers = []string{"skyhook.nvidia.com/skyhook"}
		out := &nwv1.DeploymentPolicy{}
		Expect(Convert_DeploymentPolicy_To_NodeWright(in, out)).To(Succeed())

		Expect(out.Finalizers).To(BeEmpty())
	})

	It("converts the whole Spec with full fidelity", func() {
		out := &nwv1.DeploymentPolicy{}
		Expect(Convert_DeploymentPolicy_To_NodeWright(in, out)).To(Succeed())

		inJSON, err := json.Marshal(in.Spec)
		Expect(err).NotTo(HaveOccurred())
		outJSON, err := json.Marshal(out.Spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(outJSON).To(MatchJSON(inJSON))
	})

	It("sets TypeMeta to the new-group DeploymentPolicy GVK", func() {
		out := &nwv1.DeploymentPolicy{}
		Expect(Convert_DeploymentPolicy_To_NodeWright(in, out)).To(Succeed())

		Expect(out.APIVersion).To(Equal("nodewright.nvidia.com/v1alpha1"))
		Expect(out.Kind).To(Equal("DeploymentPolicy"))
	})

	It("deep-copies reference fields so mutating the source does not affect the output", func() {
		out := &nwv1.DeploymentPolicy{}
		Expect(Convert_DeploymentPolicy_To_NodeWright(in, out)).To(Succeed())

		convertedCount := len(out.Spec.Compartments)
		convertedName := out.Spec.Compartments[0].Name
		convertedBatch := *out.Spec.Default.Strategy.Fixed.InitialBatch

		in.Spec.Compartments = append(in.Spec.Compartments, Compartment{Name: "extra"})
		in.Spec.Compartments[0].Name = "mutated"
		*in.Spec.Default.Strategy.Fixed.InitialBatch = 999

		Expect(out.Spec.Compartments).To(HaveLen(convertedCount))
		Expect(out.Spec.Compartments[0].Name).To(Equal(convertedName))
		Expect(*out.Spec.Default.Strategy.Fixed.InitialBatch).To(Equal(convertedBatch))
	})
})
