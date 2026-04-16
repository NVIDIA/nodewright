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
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("SkyhookNode", func() {
	Context("RunNext", func() {
		It("should return packages in deterministic order and respect dependencies", func() {
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			}
			skyhook := v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.SkyhookSpec{
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
						"skyhook.nvidia.com/nodeState_myskyhook": `{"some":"state"}`,
					},
				},
			},
			}
			Expect(node.HasSkyhookAnnotations()).To(BeTrue())
		})
	})

	Context("CleanupSCRMetadata", func() {
		It("should remove matching keys and preserve others", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						"skyhook.nvidia.com/status_my-skyhook":    "complete",
						"skyhook.nvidia.com/nodeState_my-skyhook": "{}",
						"skyhook.nvidia.com/version_my-skyhook":   "1.0.0",
						"skyhook.nvidia.com/status_other-skyhook": "complete", // different skyhook
						"unrelated-annotation":                    "keep-me",
					},
					Labels: map[string]string{
						"skyhook.nvidia.com/status_my-skyhook":    "complete",
						"skyhook.nvidia.com/status_other-skyhook": "in_progress", // different skyhook
						"unrelated-label":                         "keep-me",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: "skyhook.nvidia.com/my-skyhook/NotReady", Status: "False"},
						{Type: "skyhook.nvidia.com/my-skyhook/Erroring", Status: "False"},
						{Type: "skyhook.nvidia.com/other-skyhook/NotReady", Status: "True"}, // different skyhook
						{Type: "Ready", Status: "True"},                                     // system condition
					},
				},
			}

			sn, err := NewSkyhookNode(node, &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{Name: "my-skyhook"},
				Spec:       v1alpha1.SkyhookSpec{Packages: v1alpha1.Packages{}},
			})
			Expect(err).ToNot(HaveOccurred())

			sn.CleanupSCRMetadata()

			// my-skyhook keys removed
			Expect(node.Annotations).ToNot(HaveKey("skyhook.nvidia.com/status_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("skyhook.nvidia.com/nodeState_my-skyhook"))
			Expect(node.Annotations).ToNot(HaveKey("skyhook.nvidia.com/version_my-skyhook"))
			Expect(node.Labels).ToNot(HaveKey("skyhook.nvidia.com/status_my-skyhook"))

			// other-skyhook keys preserved
			Expect(node.Annotations).To(HaveKey("skyhook.nvidia.com/status_other-skyhook"))
			Expect(node.Labels).To(HaveKey("skyhook.nvidia.com/status_other-skyhook"))

			// unrelated keys preserved
			Expect(node.Annotations).To(HaveKey("unrelated-annotation"))
			Expect(node.Labels).To(HaveKey("unrelated-label"))

			// my-skyhook conditions removed, others preserved
			Expect(node.Status.Conditions).To(HaveLen(2))
			types := []string{}
			for _, c := range node.Status.Conditions {
				types = append(types, string(c.Type))
			}
			Expect(types).To(ContainElement("skyhook.nvidia.com/other-skyhook/NotReady"))
			Expect(types).To(ContainElement("Ready"))
		})
	})
})
