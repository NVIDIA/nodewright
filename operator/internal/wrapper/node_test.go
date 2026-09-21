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
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("SkyhookNode", func() {
	Context("SetStatus", func() {
		It("repairs a stale status label when the annotation already matches", func() {
			const statusKey = "nodewright.nvidia.com/status_test-skyhook"
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:        "test-node",
				Annotations: map[string]string{statusKey: string(v1alpha1.StatusWaiting)},
				Labels:      map[string]string{statusKey: string(v1alpha1.StatusErroring)},
			}}
			skyhook := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"}}

			skyhookNode, err := NewSkyhookNode(node, skyhook)
			Expect(err).NotTo(HaveOccurred())
			skyhookNode.SetStatus(v1alpha1.StatusWaiting)

			Expect(node.Annotations).To(HaveKeyWithValue(statusKey, string(v1alpha1.StatusWaiting)))
			Expect(node.Labels).To(HaveKeyWithValue(statusKey, string(v1alpha1.StatusWaiting)))
			Expect(skyhookNode.Changed()).To(BeTrue())
		})
	})

	Context("RunNext", func() {
		It("should return packages in deterministic order and respect dependencies", func() {
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			}
			skyhook := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.NodeWrightSpec{
					Packages: map[string]v1alpha1.Package{
						"a-package": {
							PackageRef: v1alpha1.PackageRef{Name: "a-package", Version: "1.0"},
							Image:      "a-image",
						},
						"b-package": {
							PackageRef: v1alpha1.PackageRef{Name: "b-package", Version: "1.0"},
							Image:      "b-image",
						},
						"c-package": {
							PackageRef: v1alpha1.PackageRef{Name: "c-package", Version: "1.0"},
							Image:      "c-image",
							DependsOn:  map[string]string{"a-package": "1.0", "b-package": "1.0"},
						},
						"d-package": {
							PackageRef: v1alpha1.PackageRef{Name: "d-package", Version: "1.0"},
							Image:      "d-image",
							DependsOn:  map[string]string{"c-package": "1.0"},
						},
						"e-package": {
							PackageRef: v1alpha1.PackageRef{Name: "e-package", Version: "1.0"},
							Image:      "e-image",
							DependsOn:  map[string]string{"c-package": "1.0"},
						},
						"f-package": {
							PackageRef: v1alpha1.PackageRef{Name: "f-package", Version: "1.0"},
							Image:      "f-image",
							DependsOn:  map[string]string{"d-package": "1.0", "e-package": "1.0"},
						},
					},
				},
			}

			// Create node
			skyhookNode, err := NewSkyhookNode(&node, &skyhook)
			Expect(err).NotTo(HaveOccurred())

			// First run should return a and b in alphabetical order
			pkgs, err := skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(HaveLen(2))
			Expect(pkgs[0].Name).To(Equal("a-package"))
			Expect(pkgs[1].Name).To(Equal("b-package"))

			// Complete b-package
			err = skyhookNode.Upsert(v1alpha1.PackageRef{Name: "b-package", Version: "1.0"}, "image", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
			Expect(err).NotTo(HaveOccurred())
			// Should still get a-package since c-package depends on both a and b
			pkgs, err = skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(HaveLen(1))
			Expect(pkgs[0].Name).To(Equal("a-package"))

			// Complete a-package
			err = skyhookNode.Upsert(v1alpha1.PackageRef{Name: "a-package", Version: "1.0"}, "image", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
			Expect(err).NotTo(HaveOccurred())
			// Now should get c-package since both dependencies are complete
			pkgs, err = skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(HaveLen(1))
			Expect(pkgs[0].Name).To(Equal("c-package"))

			// Complete c-package
			err = skyhookNode.Upsert(v1alpha1.PackageRef{Name: "c-package", Version: "1.0"}, "image", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
			Expect(err).NotTo(HaveOccurred())
			// Now should get d-package since c-package is complete
			pkgs, err = skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(HaveLen(2))
			Expect(pkgs[0].Name).To(Equal("d-package"))
			Expect(pkgs[1].Name).To(Equal("e-package"))

			// Complete e-package
			err = skyhookNode.Upsert(v1alpha1.PackageRef{Name: "e-package", Version: "1.0"}, "image", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
			Expect(err).NotTo(HaveOccurred())
			// Now should get d-package since c-package and e-package are complete
			pkgs, err = skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(HaveLen(1))
			Expect(pkgs[0].Name).To(Equal("d-package"))

			// Complete d-package
			err = skyhookNode.Upsert(v1alpha1.PackageRef{Name: "d-package", Version: "1.0"}, "image", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
			Expect(err).NotTo(HaveOccurred())
			// Now should get f-package since both d-package and e-package are complete
			pkgs, err = skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(HaveLen(1))
			Expect(pkgs[0].Name).To(Equal("f-package"))

			// Complete f-package
			err = skyhookNode.Upsert(v1alpha1.PackageRef{Name: "f-package", Version: "1.0"}, "image", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
			Expect(err).NotTo(HaveOccurred())
			// Now should get nothing since all packages are complete
			pkgs, err = skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())
			Expect(pkgs).To(BeEmpty())
		})

		// Mirrors simple-update-skyhook after its update step: three packages with no
		// dependsOn (baxter, dexter, jackie-chan) plus two that depend on dexter. A
		// dependency-free package is nobody's child, so it has to stay runnable until it
		// is itself complete, including once more packages are complete than there are
		// dependency-free ones. Regression for the stall that parked a package at
		// apply/complete and left the NodeWright in_progress forever.
		It("still returns a dependency-free package once more packages are complete than there are roots", func() {
			node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}
			skyhook := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "simple-update-skyhook"},
				Spec: v1alpha1.NodeWrightSpec{
					Packages: map[string]v1alpha1.Package{
						"baxter":      {PackageRef: v1alpha1.PackageRef{Name: "baxter", Version: "2.3.1-test"}, Image: "img"},
						"dexter":      {PackageRef: v1alpha1.PackageRef{Name: "dexter", Version: "1.2.3"}, Image: "img"},
						"jackie-chan": {PackageRef: v1alpha1.PackageRef{Name: "jackie-chan", Version: "2024.7.7-test"}, Image: "img"},
						"foobar":      {PackageRef: v1alpha1.PackageRef{Name: "foobar", Version: "1.2"}, Image: "img", DependsOn: map[string]string{"dexter": "1.2.3"}},
						"spencer":     {PackageRef: v1alpha1.PackageRef{Name: "spencer", Version: "3.2.3"}, Image: "img", DependsOn: map[string]string{"dexter": "1.2.3"}},
					},
				},
			}

			skyhookNode, err := NewSkyhookNode(&node, &skyhook)
			Expect(err).NotTo(HaveOccurred())

			// everything except jackie-chan reaches config/complete
			for _, ref := range []v1alpha1.PackageRef{
				{Name: "baxter", Version: "2.3.1-test"},
				{Name: "dexter", Version: "1.2.3"},
				{Name: "foobar", Version: "1.2"},
				{Name: "spencer", Version: "3.2.3"},
			} {
				Expect(skyhookNode.Upsert(ref, "img", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")).To(Succeed())
			}

			// jackie-chan finished apply and is waiting to be advanced to config
			Expect(skyhookNode.Upsert(v1alpha1.PackageRef{Name: "jackie-chan", Version: "2024.7.7-test"},
				"img", v1alpha1.StateComplete, v1alpha1.StageApply, 0, "")).To(Succeed())

			Expect(skyhookNode.IsComplete()).To(BeFalse())

			pkgs, err := skyhookNode.RunNext()
			Expect(err).NotTo(HaveOccurred())

			names := make([]string, 0, len(pkgs))
			for _, p := range pkgs {
				names = append(names, p.Name)
			}
			Expect(names).To(ContainElement("jackie-chan"),
				"jackie-chan is incomplete and has no unmet dependencies, so it must still be runnable")
		})
	})

	Context("HasSkyhookAnnotations", func() {
		It("should return false for node with no annotations", func() {
			node := &skyhookNode{Node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "new-node",
				},
			},
			}
			Expect(node.HasSkyhookAnnotations()).To(BeFalse())
		})

		It("should return false for node with non-skyhook annotations", func() {
			node := &skyhookNode{Node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-with-other-annotations",
					Annotations: map[string]string{
						"kubernetes.io/arch": "amd64",
						"some-other/key":     "value",
					},
				},
			},
			}
			Expect(node.HasSkyhookAnnotations()).To(BeFalse())
		})

		It("should return true for node with skyhook annotations", func() {
			node := &skyhookNode{Node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "processed-node",
					Annotations: map[string]string{
						"nodewright.nvidia.com/nodeState_myskyhook": `{"some":"state"}`,
					},
				},
			},
			}
			Expect(node.HasSkyhookAnnotations()).To(BeTrue())
		})
	})

	Context("Cordon", func() {
		It("should initialize annotations if the node has none", func() {
			myCordonKey := cordonAnnotationKey("my-skyhook")
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			Expect(sn.Cordon()).To(BeTrue())

			Expect(node.Spec.Unschedulable).To(BeTrue())
			Expect(node.Annotations).To(HaveKeyWithValue(myCordonKey, cordonAnnotationValue))
			Expect(sn.Changed()).To(BeTrue())
		})

		// The caller uses the return value to decide whether the cordon still needs to
		// reach the API server before it is safe to start evicting.
		It("should report no change when the cordon is already recorded", func() {
			myCordonKey := cordonAnnotationKey("my-skyhook")
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						myCordonKey: cordonAnnotationValue,
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			Expect(sn.Cordon()).To(BeFalse())
			Expect(sn.Changed()).To(BeFalse())
		})

		It("should report a change when another Skyhook already cordoned the node", func() {
			myCordonKey := cordonAnnotationKey("my-skyhook")
			otherCordonKey := cordonAnnotationKey("other-skyhook")
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						otherCordonKey: cordonAnnotationValue,
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			// Unschedulable is already set, but this Skyhook's ownership annotation is
			// not persisted yet, and Uncordon keys off that annotation.
			Expect(sn.Cordon()).To(BeTrue())
			Expect(node.Annotations).To(HaveKeyWithValue(myCordonKey, cordonAnnotationValue))
			Expect(sn.Changed()).To(BeTrue())
		})
	})

	Context("Uncordon", func() {
		myCordonKey := cordonAnnotationKey("my-skyhook")
		otherCordonKey := cordonAnnotationKey("other-skyhook")

		It("should make the node schedulable if this Skyhook owns the only cordon", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						myCordonKey: cordonAnnotationValue,
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			sn.Uncordon()

			Expect(node.Spec.Unschedulable).To(BeFalse())
			Expect(node.Annotations).ToNot(HaveKey(myCordonKey))
			Expect(sn.Changed()).To(BeTrue())
		})

		It("should keep the node unschedulable if another Skyhook still owns a cordon", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						myCordonKey:    cordonAnnotationValue,
						otherCordonKey: cordonAnnotationValue,
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			sn.Uncordon()

			Expect(node.Spec.Unschedulable).To(BeTrue())
			Expect(node.Annotations).ToNot(HaveKey(myCordonKey))
			Expect(node.Annotations).To(HaveKeyWithValue(otherCordonKey, cordonAnnotationValue))
			Expect(sn.Changed()).To(BeTrue())
		})

		It("should make the node schedulable if only unrelated Skyhook annotations remain", func() {
			otherStatusKey := "nodewright.nvidia.com/status_other-skyhook"
			otherNodeStateKey := "nodewright.nvidia.com/nodeState_other-skyhook"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						myCordonKey:       cordonAnnotationValue,
						otherStatusKey:    string(v1alpha1.StatusInProgress),
						otherNodeStateKey: "{}",
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			sn.Uncordon()

			Expect(node.Spec.Unschedulable).To(BeFalse())
			Expect(node.Annotations).ToNot(HaveKey(myCordonKey))
			Expect(node.Annotations).To(HaveKeyWithValue(otherStatusKey, string(v1alpha1.StatusInProgress)))
			Expect(node.Annotations).To(HaveKeyWithValue(otherNodeStateKey, "{}"))
			Expect(sn.Changed()).To(BeTrue())
		})

		It("should keep the node cordoned if the runtimeRequiredCordon annotation is present", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						myCordonKey:                              cordonAnnotationValue,
						v1alpha1.RuntimeRequiredCordonAnnotation: cordonAnnotationValue,
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			sn.Uncordon()

			Expect(node.Spec.Unschedulable).To(BeTrue())
			Expect(node.Annotations).ToNot(HaveKey(myCordonKey))
			Expect(node.Annotations).To(HaveKeyWithValue(v1alpha1.RuntimeRequiredCordonAnnotation, cordonAnnotationValue))
			Expect(sn.Changed()).To(BeTrue())
		})

		It("should not change the node if this Skyhook does not own a cordon", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						otherCordonKey: cordonAnnotationValue,
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}

			sn, err := NewSkyhookNodeOnly(node, "my-skyhook")
			Expect(err).ToNot(HaveOccurred())

			sn.Uncordon()

			Expect(node.Spec.Unschedulable).To(BeTrue())
			Expect(node.Annotations).To(HaveKeyWithValue(otherCordonKey, cordonAnnotationValue))
			Expect(sn.Changed()).To(BeFalse())
		})
	})

	Context("DrainStart", func() {
		It("should record, read, and clear the drain start annotation", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			}

			sn, err := NewSkyhookNode(node, &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "my-skyhook"},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
			})
			Expect(err).ToNot(HaveOccurred())

			started := metav1.NewTime(time.Date(2026, time.June, 2, 13, 14, 15, 0, time.UTC))
			sn.StartDrain(started)

			Expect(node.Annotations).To(HaveKeyWithValue("nodewright.nvidia.com/drainStart_my-skyhook", "2026-06-02T13:14:15Z"))
			Expect(sn.Changed()).To(BeTrue())

			drainStartedAt, err := sn.DrainStartedAt()
			Expect(err).ToNot(HaveOccurred())
			Expect(drainStartedAt).To(Equal(&started))

			sn.ClearDrainStart()
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/drainStart_my-skyhook"))
		})

		It("should return an error for a malformed drain start annotation", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						"nodewright.nvidia.com/drainStart_my-skyhook": "not-a-time",
					},
				},
			}

			sn, err := NewSkyhookNode(node, &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "my-skyhook"},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
			})
			Expect(err).ToNot(HaveOccurred())

			drainStartedAt, err := sn.DrainStartedAt()
			Expect(err).To(HaveOccurred())
			Expect(drainStartedAt).To(BeNil())
		})
	})

	Context("Reset", func() {
		It("should remove drain start and other node metadata for the skyhook", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						"nodewright.nvidia.com/cordon_my-skyhook":     "true",
						"nodewright.nvidia.com/drainStart_my-skyhook": "2026-06-02T13:14:15Z",
						"nodewright.nvidia.com/nodeState_my-skyhook":  "{}",
						"nodewright.nvidia.com/status_my-skyhook":     "erroring",
						"nodewright.nvidia.com/version_my-skyhook":    "1.0.0",
					},
					Labels: map[string]string{
						"nodewright.nvidia.com/status_my-skyhook": "erroring",
					},
				},
			}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "my-skyhook"},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
				Status: v1alpha1.NodeWrightStatus{
					NodeState:  map[string]v1alpha1.NodeState{"test-node": {}},
					NodeStatus: map[string]v1alpha1.Status{"test-node": v1alpha1.StatusErroring},
				},
			}

			sn, err := NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			sn.Reset()

			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/cordon_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/drainStart_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/nodeState_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/status_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/version_my-skyhook"))
			Expect(node.Labels).ToNot(HaveKey("nodewright.nvidia.com/status_my-skyhook"))
			Expect(skyhook.Status.NodeState).ToNot(HaveKey("test-node"))
			Expect(skyhook.Status.NodeStatus).ToNot(HaveKey("test-node"))
			Expect(skyhook.Status.Status).To(Equal(v1alpha1.StatusUnknown))
		})
	})

	Context("CleanupSCRMetadata", func() {
		It("should remove matching keys and preserve others", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						"nodewright.nvidia.com/status_my-skyhook":    "complete",
						"nodewright.nvidia.com/nodeState_my-skyhook": "{}",
						"nodewright.nvidia.com/version_my-skyhook":   "1.0.0",
						"nodewright.nvidia.com/status_other-skyhook": "complete", // different skyhook
						"unrelated-annotation":                       "keep-me",
					},
					Labels: map[string]string{
						"nodewright.nvidia.com/status_my-skyhook":    "complete",
						"nodewright.nvidia.com/status_other-skyhook": "in_progress", // different skyhook
						"unrelated-label": "keep-me",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: "nodewright.nvidia.com/my-skyhook/NotReady", Status: "False"},
						{Type: "nodewright.nvidia.com/my-skyhook/Erroring", Status: "False"},
						{Type: "nodewright.nvidia.com/other-skyhook/NotReady", Status: "True"}, // different skyhook
						{Type: "Ready", Status: "True"},                                        // system condition
					},
				},
			}

			sn, err := NewSkyhookNode(node, &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "my-skyhook"},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
			})
			Expect(err).ToNot(HaveOccurred())

			sn.CleanupSCRMetadata()

			// my-skyhook keys removed
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/status_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/nodeState_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/version_my-skyhook"))
			Expect(node.Labels).ToNot(HaveKey("nodewright.nvidia.com/status_my-skyhook"))

			// other-skyhook keys preserved
			Expect(node.Annotations).To(HaveKey("nodewright.nvidia.com/status_other-skyhook"))
			Expect(node.Labels).To(HaveKey("nodewright.nvidia.com/status_other-skyhook"))

			// unrelated keys preserved
			Expect(node.Annotations).To(HaveKey("unrelated-annotation"))
			Expect(node.Labels).To(HaveKey("unrelated-label"))

			// my-skyhook conditions removed, others preserved
			Expect(node.Status.Conditions).To(HaveLen(2))
			types := []string{}
			for _, c := range node.Status.Conditions {
				types = append(types, string(c.Type))
			}
			Expect(types).To(ContainElement("nodewright.nvidia.com/other-skyhook/NotReady"))
			Expect(types).To(ContainElement("Ready"))
		})

		It("should preserve nodeState and version annotations when state still has packages", func() {
			// D2 semantics: non-absent entry means files remain on host. The
			// finalizer's Phase 3 cleanup must not erase that record when a
			// package (e.g. uninstall.enabled=false) was never uninstalled.
			nodeState := `{"leftover|1.0":{"name":"leftover","version":"1.0","state":"complete","stage":"config","image":"img","restarts":0}}`
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						"nodewright.nvidia.com/status_my-skyhook":    "complete",
						"nodewright.nvidia.com/nodeState_my-skyhook": nodeState,
						"nodewright.nvidia.com/version_my-skyhook":   "1.0.0",
					},
					Labels: map[string]string{
						"nodewright.nvidia.com/status_my-skyhook": "complete",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: "nodewright.nvidia.com/my-skyhook/NotReady", Status: "False"},
					},
				},
			}

			sn, err := NewSkyhookNode(node, &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "my-skyhook"},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
			})
			Expect(err).ToNot(HaveOccurred())

			sn.CleanupSCRMetadata()

			// nodeState and version preserved because state is non-empty
			Expect(node.Annotations).To(HaveKeyWithValue("nodewright.nvidia.com/nodeState_my-skyhook", nodeState))
			Expect(node.Annotations).To(HaveKeyWithValue("nodewright.nvidia.com/version_my-skyhook", "1.0.0"))

			// status annotation, label, and condition still cleaned up
			Expect(node.Annotations).ToNot(HaveKey("nodewright.nvidia.com/status_my-skyhook"))
			Expect(node.Labels).ToNot(HaveKey("nodewright.nvidia.com/status_my-skyhook"))
			Expect(node.Status.Conditions).To(BeEmpty())
		})
	})

	Context("ProgressSkipped", func() {
		It("persists the skipped->complete promotion to the nodeState annotation", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			}
			skyhook := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
				Spec: v1alpha1.NodeWrightSpec{
					Packages: map[string]v1alpha1.Package{
						"baxter": {
							PackageRef: v1alpha1.PackageRef{Name: "baxter", Version: "3.2.1"},
							Image:      "baxter-image",
						},
					},
				},
			}

			sn, err := NewSkyhookNode(node, &skyhook)
			Expect(err).NotTo(HaveOccurred())

			// Park baxter at interrupt/skipped. Upsert persists this to the annotation.
			Expect(sn.Upsert(v1alpha1.PackageRef{Name: "baxter", Version: "3.2.1"}, "baxter-image",
				v1alpha1.StateSkipped, v1alpha1.StageInterrupt, 0, "")).To(Succeed())

			Expect(sn.ProgressSkipped()).To(Succeed())

			// The promotion must be persisted to the nodeState annotation, not just the
			// in-memory map: the reconcile is level-triggered and reloads state from the
			// annotation each pass. A fresh wrapper over the same Node models that reload.
			reloaded, err := NewSkyhookNode(node, &skyhook)
			Expect(err).NotTo(HaveOccurred())
			status, found := reloaded.PackageStatus("baxter|3.2.1")
			Expect(found).To(BeTrue())
			Expect(status.State).To(Equal(v1alpha1.StateComplete))
		})
	})

	Context("Taint and RemoveTaint", func() {
		const taintKey = "nodewright.nvidia.com/runtime-required"

		newNode := func(taints ...corev1.Taint) SkyhookNode {
			GinkgoHelper()
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
				Spec:       corev1.NodeSpec{Taints: taints},
			}
			skyhook := v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"}}

			sn, err := NewSkyhookNode(node, &skyhook)
			Expect(err).NotTo(HaveOccurred())
			return sn
		}

		It("should add a NoSchedule taint valued with the Skyhook name", func() {
			sn := newNode()

			sn.Taint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(ConsistOf(corev1.Taint{
				Key:    taintKey,
				Value:  "test-skyhook",
				Effect: corev1.TaintEffectNoSchedule,
			}))
			Expect(sn.Changed()).To(BeTrue())
		})

		It("should keep taints set by others when adding", func() {
			existing := corev1.Taint{Key: "other/taint", Value: "keep", Effect: corev1.TaintEffectNoExecute}
			sn := newNode(existing)

			sn.Taint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(HaveLen(2))
			Expect(sn.GetNode().Spec.Taints).To(ContainElement(existing))
		})

		// Re-tainting is not an error the operator can afford: reconcile is level-triggered
		// and will call Taint on every pass, and a duplicate key is rejected by the apiserver.
		It("should be a no-op when the taint key is already present", func() {
			sn := newNode(corev1.Taint{Key: taintKey, Value: "someone-else", Effect: corev1.TaintEffectNoSchedule})

			sn.Taint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(HaveLen(1))
			Expect(sn.GetNode().Spec.Taints[0].Value).To(Equal("someone-else"))
			Expect(sn.Changed()).To(BeFalse())
		})

		It("should remove only the taint with the given key", func() {
			other := corev1.Taint{Key: "other/taint", Value: "keep", Effect: corev1.TaintEffectNoExecute}
			sn := newNode(
				corev1.Taint{Key: taintKey, Value: "test-skyhook", Effect: corev1.TaintEffectNoSchedule},
				other,
			)

			sn.RemoveTaint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(ConsistOf(other))
			Expect(sn.Changed()).To(BeTrue())
		})

		It("should be a no-op when the node carries no taints", func() {
			sn := newNode()

			sn.RemoveTaint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(BeEmpty())
			Expect(sn.Changed()).To(BeFalse())
		})

		It("should be a no-op when the taint key is absent", func() {
			other := corev1.Taint{Key: "other/taint", Value: "keep", Effect: corev1.TaintEffectNoExecute}
			sn := newNode(other)

			sn.RemoveTaint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(ConsistOf(other))
			Expect(sn.Changed()).To(BeFalse())
		})

		It("should round-trip an add followed by a remove", func() {
			sn := newNode()

			sn.Taint(taintKey)
			sn.RemoveTaint(taintKey)

			Expect(sn.GetNode().Spec.Taints).To(BeEmpty())
		})
	})
})
