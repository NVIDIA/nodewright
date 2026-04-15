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

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Skyhook wrapper tests", func() {
	It("Should get config updates", func() {
		skyhook := &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					ConfigUpdates: map[string][]string{
						"foo": {
							"changed",
						},
						"bar": {
							"changed",
						},
					},
				},
			},
			Updated: false,
		}

		Expect(skyhook.GetConfigUpdates()).To(BeEquivalentTo(map[string][]string{
			"foo": {
				"changed",
			},
			"bar": {
				"changed",
			},
		}))

		skyhook = &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{},
			},
			Updated: false,
		}

		Expect(skyhook.GetConfigUpdates()).To(BeNil())
	})

	It("Should add config updates", func() {
		skyhook := &Skyhook{
			Skyhook: &v1alpha1.Skyhook{},
			Updated: false,
		}

		skyhook.AddConfigUpdates("foo", "changed")
		Expect(skyhook.Status.ConfigUpdates).To(BeEquivalentTo(map[string][]string{
			"foo": {
				"changed",
			},
		}))

		skyhook.AddConfigUpdates("foo", "added")
		skyhook.AddConfigUpdates("bar", "changed")

		Expect(skyhook.Status.ConfigUpdates).To(BeEquivalentTo(map[string][]string{
			"foo": {
				"changed",
				"added",
			},
			"bar": {
				"changed",
			},
		}))

		skyhook.AddConfigUpdates("foo", "added", "changed")

		Expect(skyhook.Status.ConfigUpdates).To(BeEquivalentTo(map[string][]string{
			"foo": {
				"changed",
				"added",
			},
			"bar": {
				"changed",
			},
		}))
	})

	It("Should remove config updates on a per-package basis", func() {
		skyhook := &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{},
			},
			Updated: false,
		}

		skyhook.RemoveConfigUpdates("foo")
		skyhook.RemoveConfigUpdates("")
		Expect(skyhook.Status.ConfigUpdates).To(BeNil())

		skyhook = &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					ConfigUpdates: map[string][]string{
						"foo": {
							"changed",
							"again",
						},
						"bar": {
							"changed",
							"again",
						},
					},
				},
			},
			Updated: false,
		}

		skyhook.RemoveConfigUpdates("foo")
		Expect(skyhook.Status.ConfigUpdates).To(BeEquivalentTo(map[string][]string{
			"bar": {
				"changed",
				"again",
			},
		}))
	})

	It("Should get config interrupts", func() {
		skyhook := &Skyhook{
			Skyhook: &v1alpha1.Skyhook{},
			Updated: false,
		}

		Expect(skyhook.GetConfigInterrupts()).To(BeEquivalentTo(map[string][]*v1alpha1.Interrupt{}))

		skyhook = &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: v1alpha1.Packages{
						"foo": v1alpha1.Package{
							PackageRef: v1alpha1.PackageRef{
								Name:    "foo",
								Version: "1.1.2",
							},
						},
						"bar": v1alpha1.Package{
							PackageRef: v1alpha1.PackageRef{
								Name:    "bar",
								Version: "3",
							},
							ConfigInterrupts: map[string]v1alpha1.Interrupt{
								"run.sh": {
									Type: v1alpha1.REBOOT,
								},
								"check.sh": {
									Type:     v1alpha1.SERVICE,
									Services: []string{"cron", "foo"},
								},
							},
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{
					ConfigUpdates: map[string][]string{
						"bar": {
							"run.sh",
						},
						"foo": {
							"check.sh",
						},
					},
				},
			},
			Updated: false,
		}

		Expect(skyhook.GetConfigInterrupts()).To(BeEquivalentTo(map[string][]*v1alpha1.Interrupt{
			"bar": {
				{
					Type: v1alpha1.REBOOT,
				},
			},
		}))

		skyhook = &Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: v1alpha1.Packages{
						"foo": v1alpha1.Package{
							PackageRef: v1alpha1.PackageRef{
								Name:    "foo",
								Version: "1.1.2",
							},
						},
						"bar": v1alpha1.Package{
							PackageRef: v1alpha1.PackageRef{
								Name:    "bar",
								Version: "3",
							},
							ConfigInterrupts: map[string]v1alpha1.Interrupt{
								"run.sh": {
									Type: v1alpha1.REBOOT,
								},
								"check.sh": {
									Type:     v1alpha1.SERVICE,
									Services: []string{"cron", "foo"},
								},
								"foobar.sh": {
									Type:     v1alpha1.SERVICE,
									Services: []string{"testing"},
								},
							},
						},
					},
				},
				Status: v1alpha1.SkyhookStatus{
					ConfigUpdates: map[string][]string{
						"bar": {
							"run.sh",
							"check.sh",
							"foobar.sh",
						},
						"foo": {
							"check.sh",
						},
					},
				},
			},
			Updated: false,
		}

		Expect(skyhook.GetConfigInterrupts()).To(BeEquivalentTo(map[string][]*v1alpha1.Interrupt{
			"bar": {
				{
					Type: v1alpha1.REBOOT,
				},
				{
					Type:     v1alpha1.SERVICE,
					Services: []string{"cron", "foo"},
				},
				{
					Type:     v1alpha1.SERVICE,
					Services: []string{"testing"},
				},
			},
		}))
	})

	Context("RemoveNodePriority", func() {
		It("should delete node and increment offset", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						NodePriority: map[string]metav1.Time{
							"node-1": metav1.Now(),
							"node-2": metav1.Now(),
						},
					},
				},
			}

			skyhook.RemoveNodePriority("node-1")
			Expect(skyhook.Status.NodePriority).NotTo(HaveKey("node-1"))
			Expect(skyhook.Status.NodePriority).To(HaveKey("node-2"))
			Expect(skyhook.Status.NodeOrderOffset).To(Equal(1))
			Expect(skyhook.Updated).To(BeTrue())
		})

		It("should be a no-op for missing node", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						NodePriority: map[string]metav1.Time{
							"node-1": metav1.Now(),
						},
					},
				},
			}

			skyhook.RemoveNodePriority("node-missing")
			Expect(skyhook.Status.NodePriority).To(HaveKey("node-1"))
			Expect(skyhook.Status.NodeOrderOffset).To(Equal(0))
			Expect(skyhook.Updated).To(BeFalse())
		})

		It("should be a no-op for nil map", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{},
			}

			skyhook.RemoveNodePriority("node-1")
			Expect(skyhook.Status.NodeOrderOffset).To(Equal(0))
			Expect(skyhook.Updated).To(BeFalse())
		})
	})

	Context("NodeOrder", func() {
		It("should return offset + position for node in priority", func() {
			now := time.Now()
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						NodeOrderOffset: 3,
						NodePriority: map[string]metav1.Time{
							"node-a": metav1.NewTime(now),
							"node-b": metav1.NewTime(now.Add(1 * time.Second)),
						},
					},
				},
			}

			Expect(skyhook.NodeOrder("node-a")).To(Equal(3)) // offset 3 + index 0
			Expect(skyhook.NodeOrder("node-b")).To(Equal(4)) // offset 3 + index 1
		})

		It("should use name as tiebreaker for same timestamps", func() {
			now := time.Now()
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						NodePriority: map[string]metav1.Time{
							"node-b": metav1.NewTime(now),
							"node-a": metav1.NewTime(now),
						},
					},
				},
			}

			Expect(skyhook.NodeOrder("node-a")).To(Equal(0)) // a before b
			Expect(skyhook.NodeOrder("node-b")).To(Equal(1))
		})

		It("should return 0 for node not in priority", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						NodeOrderOffset: 5,
						NodePriority: map[string]metav1.Time{
							"node-1": metav1.Now(),
						},
					},
				},
			}

			Expect(skyhook.NodeOrder("node-missing")).To(Equal(0))
		})

		It("should return 0 for nil map", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{},
			}

			Expect(skyhook.NodeOrder("node-1")).To(Equal(0))
		})
	})

	Context("RemoveCondition", func() {
		It("should remove a condition by type", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						Conditions: []metav1.Condition{
							{Type: "TypeA", Status: metav1.ConditionTrue, Reason: "A"},
							{Type: "TypeB", Status: metav1.ConditionTrue, Reason: "B"},
						},
					},
				},
			}

			skyhook.RemoveCondition("TypeA")
			Expect(skyhook.Status.Conditions).To(HaveLen(1))
			Expect(skyhook.Status.Conditions[0].Type).To(Equal("TypeB"))
			Expect(skyhook.Updated).To(BeTrue())
		})

		It("should be a no-op when condition does not exist", func() {
			skyhook := &Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Status: v1alpha1.SkyhookStatus{
						Conditions: []metav1.Condition{
							{Type: "TypeA", Status: metav1.ConditionTrue, Reason: "A"},
						},
					},
				},
			}

			skyhook.RemoveCondition("TypeNonExistent")
			Expect(skyhook.Status.Conditions).To(HaveLen(1))
			Expect(skyhook.Updated).To(BeFalse())
		})
	})
})
