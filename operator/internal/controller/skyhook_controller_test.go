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
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	skyhookNodesMock "github.com/NVIDIA/nodewright/operator/internal/controller/mock"
	dalMock "github.com/NVIDIA/nodewright/operator/internal/dal/mock"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	wrapperMock "github.com/NVIDIA/nodewright/operator/internal/wrapper/mock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("skyhook controller tests", func() {

	var logger = log.FromContext(ctx)

	It("should map only pods we created", func() {

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foobar",
				Labels: map[string]string{
					fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): "foobar",
				},
			},
		}

		ret := podHandlerFunc(ctx, pod)
		Expect(ret).To(HaveLen(1))
		Expect(ret[0].Name).To(BeEquivalentTo("pod---foobar"))

		pod.Labels = map[string]string{"foo": "bar"}
		ret = podHandlerFunc(ctx, pod)
		Expect(ret).To(BeNil())

	})

	It("should not return if there are no skyhooks", func() {

		r, err := operator.Reconcile(context.TODO(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: ""}})
		Expect(err).ToNot(HaveOccurred())
		Expect(r.RequeueAfter).To(BeEquivalentTo(0))
	})

	Context("cluster state", func() {
		It("should pick the correct number of nodes by percent", func() {

			testfunc := func(percent, count, expected int) {
				skyhooks := &v1alpha1.SkyhookList{
					Items: []v1alpha1.Skyhook{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "skyhook1",
							},
							Spec: v1alpha1.SkyhookSpec{
								InterruptionBudget: v1alpha1.InterruptionBudget{
									Percent: ptr[int](percent),
								},
								Packages: v1alpha1.Packages{
									"test-package": v1alpha1.Package{
										PackageRef: v1alpha1.PackageRef{
											Name:    "test-package",
											Version: "1.0.0",
										},
									},
								},
							},
						},
					},
				}

				nodes := &corev1.NodeList{
					Items: make([]corev1.Node, 0),
				}
				for i := 0; i < count; i++ {
					nodes.Items = append(nodes.Items,
						corev1.Node{
							ObjectMeta: metav1.ObjectMeta{
								Name: fmt.Sprintf("node_%d", i),
								// Annotations: map[string]string{
								// 	"skyhook.nvidia.com/state": string(v1alpha1.ENABLED),
								// },
							},
						})
				}
				deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
				clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
				Expect(err).ToNot(HaveOccurred())

				for _, skyhook := range clusterState.skyhooks {
					picker := NewNodePicker(logger, opts.GetRuntimeRequiredToleration())
					pick := picker.SelectNodes(skyhook)
					Expect(pick).To(HaveLen(expected))
				}
			}

			testfunc(20, 2, 1)
			testfunc(20, 6, 1)
			testfunc(20, 10, 2)
			testfunc(20, 15, 3)
			testfunc(0, 15, 1)

		})

		It("should pick the correct number of nodes by count", func() {

			testfunc := func(count, nodeCode, expected int) {
				skyhooks := &v1alpha1.SkyhookList{
					Items: []v1alpha1.Skyhook{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "skyhook1",
							},
							Spec: v1alpha1.SkyhookSpec{
								InterruptionBudget: v1alpha1.InterruptionBudget{
									Count: ptr[int](count),
								},
								Packages: v1alpha1.Packages{
									"test-package": v1alpha1.Package{
										PackageRef: v1alpha1.PackageRef{
											Name:    "test-package",
											Version: "1.0.0",
										},
									},
								},
							},
						},
					},
				}

				nodes := &corev1.NodeList{
					Items: make([]corev1.Node, 0),
				}
				for i := 0; i < nodeCode; i++ {
					nodes.Items = append(nodes.Items,
						corev1.Node{
							ObjectMeta: metav1.ObjectMeta{
								Name: fmt.Sprintf("node_%d", i),
								// Annotations: map[string]string{
								// 	"skyhook.nvidia.com/state": string(v1alpha1.ENABLED),
								// },
							},
						})
				}

				deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
				clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
				Expect(err).ToNot(HaveOccurred())

				for _, skyhook := range clusterState.skyhooks {
					picker := NewNodePicker(logger, opts.GetRuntimeRequiredToleration())
					pick := picker.SelectNodes(skyhook)
					Expect(pick).To(HaveLen(expected))
				}
			}

			testfunc(1, 2, 1)
			testfunc(2, 6, 2)
			testfunc(0, 10, 1)
		})
	})

	It("should merge interrupts", func() {
		packages := []*v1alpha1.Package{
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "bar"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "bar",
					Version: "3.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"ducks", "kittens"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "buz",
					Version: "2.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "log"},
				},
			},
		}

		// this faulty interrupt should not even be considered
		// as it's not in the packages
		interrupts := map[string][]*v1alpha1.Interrupt{
			"bogus": {
				{
					Type: v1alpha1.REBOOT,
				},
			},
		}
		configUpdates := make(map[string][]string)
		interrupt, _package := fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(interrupt.Services).To(BeEquivalentTo([]string{"bar", "ducks", "foo", "kittens", "log"}))
		Expect(_package).To(BeEquivalentTo("bar"))

		packages = []*v1alpha1.Package{
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "bar"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "bar",
					Version: "3.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"ducks", "kittens"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "buz",
					Version: "2.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "log"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name: "omg", Version: "1.2.3"},
				Interrupt: &v1alpha1.Interrupt{
					Type: v1alpha1.REBOOT,
				},
			},
		}

		interrupt, _package = fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(_package).To(BeEquivalentTo("omg"))
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.REBOOT))
		Expect(interrupt.Services).To(BeEmpty())

		packages = []*v1alpha1.Package{
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "bar"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "bar",
					Version: "3.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"ducks", "kittens"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "buz",
					Version: "2.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "log"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name: "omg", Version: "1.2.3"},
				Interrupt: &v1alpha1.Interrupt{
					Type: v1alpha1.REBOOT,
				},
			},
		}

		interrupts = map[string][]*v1alpha1.Interrupt{
			"foo": {
				{
					Type:     v1alpha1.SERVICE,
					Services: []string{"dogs"},
				},
			},
			"buz": {
				{
					Type:     v1alpha1.SERVICE,
					Services: []string{"cows"},
				},
			},
		}

		configUpdates = map[string][]string{
			"buz": {
				"foo",
			},
			"omg": {
				"bar",
			},
		}

		interrupt, _package = fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(_package).To(BeEquivalentTo("bar"))
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.SERVICE))
		Expect(interrupt.Services).To(BeEquivalentTo([]string{"bar", "cows", "ducks", "foo", "kittens"}))

		packages = []*v1alpha1.Package{
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "bar"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "bar",
					Version: "3.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"ducks", "kittens"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "buz",
					Version: "2.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type:     v1alpha1.SERVICE,
					Services: []string{"foo", "log"},
				},
			},
			{
				PackageRef: v1alpha1.PackageRef{
					Name: "omg", Version: "1.2.3"},
				Interrupt: &v1alpha1.Interrupt{
					Type: v1alpha1.REBOOT,
				},
			},
		}

		interrupts = map[string][]*v1alpha1.Interrupt{
			"foo": {
				{
					Type:     v1alpha1.SERVICE,
					Services: []string{"dogs"},
				},
			},
			"buz": {
				{
					Type:     v1alpha1.SERVICE,
					Services: []string{"cows"},
				},
			},
		}

		configUpdates = map[string][]string{
			"foo": {
				"foo",
			},
			"omg": {
				"bar",
			},
		}

		// configUpdate matches package so config interrupts are now added but not package interrupts
		interrupt, _package = fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(_package).To(BeEquivalentTo("bar"))
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.SERVICE))
		Expect(interrupt.Services).To(BeEquivalentTo([]string{"dogs", "ducks", "foo", "kittens", "log"}))
	})

	It("Should filter envs correctly", func() {
		envs := []corev1.EnvVar{
			{
				Name:  "DOGS",
				Value: "foobar",
			},
			{
				Name:  "CATS",
				Value: "foobar",
			},
			{
				Name:  "DUCKS",
				Value: "foobar",
			},
		}

		Expect(FilterEnv(envs, "NOTEXIST")).To(BeEquivalentTo([]corev1.EnvVar{
			{
				Name:  "DOGS",
				Value: "foobar",
			},
			{
				Name:  "CATS",
				Value: "foobar",
			},
			{
				Name:  "DUCKS",
				Value: "foobar",
			},
		}))

		Expect(FilterEnv(envs, "CATS")).To(BeEquivalentTo([]corev1.EnvVar{
			{
				Name:  "DOGS",
				Value: "foobar",
			},
			{
				Name:  "DUCKS",
				Value: "foobar",
			},
		}))

		Expect(FilterEnv(envs, "CATS", "DUCKS")).To(BeEquivalentTo([]corev1.EnvVar{
			{
				Name:  "DOGS",
				Value: "foobar",
			},
		}))

		Expect(FilterEnv(envs, "CATS", "DUCKS", "DOGS")).To(BeNil())
	})

	It("Ensure all the config env vars are set", func() {
		opts := SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			ImagePullSecret:      "foo",
			CopyDirRoot:          "/tmp",
			ReapplyOnReboot:      true,
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
			AgentLogRoot:         "/log",
		}
		Expect(opts.Validate()).To(BeNil())

		envs := getAgentConfigEnvVars(opts, "package", "version", "id", "skyhook_name", 0)
		expected := []corev1.EnvVar{
			{
				Name:  "SKYHOOK_LOG_DIR",
				Value: "/log/skyhook_name",
			},
			{
				Name:  "SKYHOOK_ROOT_DIR",
				Value: "/tmp/skyhook_name",
			},
			{
				Name:  "COPY_RESOLV",
				Value: "false",
			},
			{
				Name:  "SKYHOOK_RESOURCE_ID",
				Value: "id_package_version",
			},
			{
				Name:  "SKYHOOK_NODE_ORDER",
				Value: "0",
			},
		}
		Expect(envs).To(BeEquivalentTo(expected))
	})

	It("should set monotonic SKYHOOK_NODE_ORDER across nodes and batches", func() {
		now := time.Now()
		testSkyhook := wrapper.NewSkyhookWrapper(&v1alpha1.Skyhook{
			Status: v1alpha1.SkyhookStatus{
				NodePriority: map[string]metav1.Time{
					"node-a": metav1.NewTime(now),
					"node-b": metav1.NewTime(now.Add(1 * time.Second)),
				},
			},
		})
		testPackage := &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0"},
			Image:      "test:latest",
		}

		// Batch 1: node-a=0, node-b=1
		podA := createPodFromPackage(operator.opts, testPackage, testSkyhook, "node-a", v1alpha1.StageApply)
		podB := createPodFromPackage(operator.opts, testPackage, testSkyhook, "node-b", v1alpha1.StageApply)

		getNodeOrder := func(pod *corev1.Pod) string {
			for _, c := range pod.Spec.InitContainers {
				for _, env := range c.Env {
					if env.Name == "SKYHOOK_NODE_ORDER" {
						return env.Value
					}
				}
			}
			return ""
		}

		Expect(getNodeOrder(podA)).To(Equal("0"))
		Expect(getNodeOrder(podB)).To(Equal("1"))

		// Simulate batch completion: remove both nodes, offset becomes 2
		testSkyhook.RemoveNodePriority("node-a")
		testSkyhook.RemoveNodePriority("node-b")
		Expect(testSkyhook.Status.NodeOrderOffset).To(Equal(2))

		// Batch 2: add node-c, should get order 2
		testSkyhook.Status.NodePriority = map[string]metav1.Time{
			"node-c": metav1.NewTime(now.Add(2 * time.Second)),
		}
		podC := createPodFromPackage(operator.opts, testPackage, testSkyhook, "node-c", v1alpha1.StageApply)
		Expect(getNodeOrder(podC)).To(Equal("2"))
	})

	It("should pick highest priority interrupt", func() {
		packages := []*v1alpha1.Package{
			{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.2.1",
				},
				Interrupt: &v1alpha1.Interrupt{
					Type: v1alpha1.NOOP,
				},
			},
		}

		interrupts := make(map[string][]*v1alpha1.Interrupt)
		configUpdates := make(map[string][]string)
		interrupt, _package := fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.NOOP))
		Expect(_package).To(BeEquivalentTo("foo"))

		packages = append(packages, &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name: "bar", Version: "1.2.3"},
			Interrupt: &v1alpha1.Interrupt{
				Type:     v1alpha1.SERVICE,
				Services: []string{"foo", "bar"},
			},
		})

		interrupt, _package = fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(_package).To(BeEquivalentTo("bar"))
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.SERVICE))
		Expect(interrupt.Services).To(BeEquivalentTo([]string{"bar", "foo"}))

		packages = append(packages, &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name: "baz", Version: "1.2.3"},
			Interrupt: &v1alpha1.Interrupt{
				Type: v1alpha1.RESTART_ALL_SERVICES,
			},
		})

		interrupt, _package = fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(_package).To(BeEquivalentTo("baz"))
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.RESTART_ALL_SERVICES))
		Expect(interrupt.Services).To(BeEmpty())

		packages = append(packages, &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name: "omg", Version: "1.2.3"},
			Interrupt: &v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
		})

		interrupt, _package = fudgeInterruptWithPriority(packages, configUpdates, interrupts)
		Expect(interrupt).ToNot(BeNil())
		Expect(_package).To(BeEquivalentTo("omg"))
		Expect(interrupt.Type).To(BeEquivalentTo(v1alpha1.REBOOT))
		Expect(interrupt.Services).To(BeEmpty())
	})

	It("Check validations of skyhook options", func() {
		// good options
		opts := SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			ImagePullSecret:      "foo",
			CopyDirRoot:          "/tmp",
			ReapplyOnReboot:      true,
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
		}
		Expect(opts.Validate()).To(BeNil())

		// bad MaxInterval
		opts.MaxInterval = time.Second * 0
		Expect(opts.Validate()).ToNot(BeNil())

		// bad CopyDirRoot
		opts.MaxInterval = time.Second * 10
		opts.CopyDirRoot = "foo/bar"
		Expect(opts.Validate()).ToNot(BeNil())

		// bad RuntimeRequiredTaint
		opts.CopyDirRoot = "/tmp"
		opts.RuntimeRequiredTaint = "foo"
		Expect(opts.Validate()).ToNot(BeNil())

		// bad RuntimeRequiredTaint
		opts.RuntimeRequiredTaint = "foo=bar"
		Expect(opts.Validate()).ToNot(BeNil())

		// RuntimeRequiredTaint is a delete
		opts.RuntimeRequiredTaint = "skyhook.nvidia.com=runtime-required:NoExecute-"
		Expect(opts.Validate()).ToNot(BeNil())

		opts.AgentImage = ""
		Expect(opts.Validate()).ToNot(BeNil())

		opts.AgentImage = "foo"
		Expect(opts.Validate()).ToNot(BeNil())

		opts.PauseImage = ""
		Expect(opts.Validate()).ToNot(BeNil())

		opts.PauseImage = "bar"
		Expect(opts.Validate()).ToNot(BeNil())
	})
	It("Should group skyhooks by node correctly", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "skyhook1",
					},
					Spec: v1alpha1.SkyhookSpec{
						NodeSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"foo": "bar",
							},
						},
						RuntimeRequired: true,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "skyhook2",
					},
					Spec: v1alpha1.SkyhookSpec{
						NodeSelector: metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "foo",
									Operator: metav1.LabelSelectorOpExists,
								},
							},
						},
						RuntimeRequired: true,
					},
				},
			},
		}

		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"foo": "bar",
						},
						UID: "node1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
						Labels: map[string]string{
							"foo": "baz",
						},
						UID: "node2",
					},
				},
			},
		}

		deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())

		node_to_skyhooks, _ := groupSkyhooksByNode(clusterState)
		Expect(node_to_skyhooks).To(HaveLen(2))
		Expect(node_to_skyhooks[nodes.Items[0].UID]).To(HaveLen(2))
		Expect(node_to_skyhooks[nodes.Items[1].UID]).To(HaveLen(1))
	})
	It("Should group skyhooks by node but ignore ones without runtime required", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "skyhook1",
					},
					Spec: v1alpha1.SkyhookSpec{
						NodeSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"foo": "bar",
							},
						},
						RuntimeRequired: true,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "skyhook2",
					},
					Spec: v1alpha1.SkyhookSpec{
						NodeSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"foo": "bar",
							},
						},
						RuntimeRequired: false,
					},
				},
			},
		}

		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
						Labels: map[string]string{
							"foo": "baz",
						},
					},
				},
			},
		}

		deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())

		node_to_skyhooks, _ := groupSkyhooksByNode(clusterState)
		Expect(node_to_skyhooks).To(HaveLen(1))
		Expect(node_to_skyhooks[nodes.Items[0].UID]).To(HaveLen(1))
	})
	It("Should only select nodes to remove when all runtime required skyhooks have completed on that specific node", func() {
		// Test per-node completion: Node taint should be removed when all skyhooks
		// are complete ON THAT NODE, regardless of other nodes' completion status.

		node1 := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				UID:  "node1",
			},
		}

		node2 := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
				UID:  "node2",
			},
		}

		// Mock node wrappers with different completion states per node
		node1WrapperA := wrapperMock.NewMockSkyhookNode(GinkgoT())
		node1WrapperA.EXPECT().GetNode().Return(&node1).Maybe()
		node1WrapperA.EXPECT().IsComplete().Return(true).Maybe()

		node1WrapperB := wrapperMock.NewMockSkyhookNode(GinkgoT())
		node1WrapperB.EXPECT().GetNode().Return(&node1).Maybe()
		// First call returns false, then subsequent calls return true
		node1WrapperB.EXPECT().IsComplete().Return(false).Once()
		node1WrapperB.EXPECT().IsComplete().Return(true).Maybe()

		node2WrapperA := wrapperMock.NewMockSkyhookNode(GinkgoT())
		node2WrapperA.EXPECT().GetNode().Return(&node2).Maybe()
		node2WrapperA.EXPECT().IsComplete().Return(true).Maybe()

		// skyhook_a: complete on both nodes
		skyhook_a_mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
		skyhook_a_mock.EXPECT().GetNode("node1").Return(v1alpha1.StatusComplete, node1WrapperA).Maybe()
		skyhook_a_mock.EXPECT().GetNode("node2").Return(v1alpha1.StatusComplete, node2WrapperA).Maybe()

		// skyhook_b: incomplete on node1, doesn't target node2
		skyhook_b_mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
		skyhook_b_mock.EXPECT().GetNode("node1").Return(v1alpha1.StatusInProgress, node1WrapperB).Maybe()

		node_to_skyhooks := map[types.UID][]SkyhookNodes{
			node1.UID: {
				skyhook_a_mock,
				skyhook_b_mock,
			},
			node2.UID: {
				skyhook_a_mock,
			},
		}

		node_map := map[types.UID]*corev1.Node{
			node1.UID: &node1,
			node2.UID: &node2,
		}

		// First check: node2 should have taint removed (all skyhooks complete on node2)
		// node1 should NOT have taint removed (skyhook_b incomplete on node1)
		to_remove := getRuntimeRequiredTaintCompleteNodes(node_to_skyhooks, node_map)
		Expect(to_remove).To(HaveLen(1))
		Expect(to_remove[0].UID).To(BeEquivalentTo(node2.UID))

		// Second check: now node1WrapperB returns true, so both nodes should be removed
		to_remove = getRuntimeRequiredTaintCompleteNodes(node_to_skyhooks, node_map)
		Expect(to_remove).To(HaveLen(2))
	})

	It("Should remove taint per-node even if other nodes in same skyhook are incomplete", func() {
		// This tests the key behavioral change: Node A's taint is removed when Node A
		// completes all its skyhooks, even if Node B is still incomplete on those skyhooks.

		nodeA := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nodeA",
				UID:  "nodeA",
			},
		}

		nodeB := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nodeB",
				UID:  "nodeB",
			},
		}

		// Both nodes are targeted by the same skyhook
		// Node A is complete, Node B is incomplete
		nodeAWrapper := wrapperMock.NewMockSkyhookNode(GinkgoT())
		nodeAWrapper.EXPECT().GetNode().Return(&nodeA).Maybe()
		nodeAWrapper.EXPECT().IsComplete().Return(true).Maybe()

		nodeBWrapper := wrapperMock.NewMockSkyhookNode(GinkgoT())
		nodeBWrapper.EXPECT().GetNode().Return(&nodeB).Maybe()
		nodeBWrapper.EXPECT().IsComplete().Return(false).Maybe()

		skyhook_mock := skyhookNodesMock.NewMockSkyhookNodes(GinkgoT())
		skyhook_mock.EXPECT().GetNode("nodeA").Return(v1alpha1.StatusComplete, nodeAWrapper).Maybe()
		skyhook_mock.EXPECT().GetNode("nodeB").Return(v1alpha1.StatusInProgress, nodeBWrapper).Maybe()

		node_to_skyhooks := map[types.UID][]SkyhookNodes{
			nodeA.UID: {skyhook_mock},
			nodeB.UID: {skyhook_mock},
		}

		node_map := map[types.UID]*corev1.Node{
			nodeA.UID: &nodeA,
			nodeB.UID: &nodeB,
		}

		// Node A should have taint removed (complete on nodeA)
		// Node B should NOT have taint removed (incomplete on nodeB)
		to_remove := getRuntimeRequiredTaintCompleteNodes(node_to_skyhooks, node_map)
		Expect(to_remove).To(HaveLen(1))
		Expect(to_remove[0].UID).To(BeEquivalentTo(nodeA.UID))
	})
	It("CreateTolerationForTaint should tolerate the passed taint", func() {
		taint := corev1.Taint{
			Key:    "skyhook.nvidia.com",
			Value:  "runtime-required",
			Effect: "NoSchedule",
		}
		toleration := opts.GetRuntimeRequiredToleration()
		Expect(toleration.ToleratesTaint(logger, &taint, false)).To(BeTrue())

	})
	It("Pods should always tolerate runtime required taint", func() {
		pod := createPodFromPackage(
			operator.opts,
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Spec: v1alpha1.SkyhookSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageApply,
		)
		found_toleration := false
		expected_toleration := opts.GetRuntimeRequiredToleration()
		for _, toleration := range pod.Spec.Tolerations {
			if toleration.Key == expected_toleration.Key && toleration.Value == expected_toleration.Value && toleration.Effect == expected_toleration.Effect {
				found_toleration = true
				break
			}
		}
		Expect(found_toleration).To(BeTrue())
	})
	It("Interrupt pods should tolerate runtime required taint when it is runtime required", func() {
		pod := createInterruptPodForPackage(
			operator.opts,
			&v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
			"argEncode",

			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Spec: v1alpha1.SkyhookSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageInterrupt,
		)
		found_toleration := false
		expected_toleration := opts.GetRuntimeRequiredToleration()
		for _, toleration := range pod.Spec.Tolerations {
			if toleration.Key == expected_toleration.Key && toleration.Value == expected_toleration.Value && toleration.Effect == expected_toleration.Effect {
				found_toleration = true
				break
			}
		}
		Expect(found_toleration).To(BeTrue())
	})

	It("Pods should not have imagePullSecrets when ImagePullSecret is empty", func() {
		emptyOpts := SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			ImagePullSecret:      "", // Empty - no pull secret
			CopyDirRoot:          "/tmp",
			ReapplyOnReboot:      true,
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
		}

		pod := createPodFromPackage(
			emptyOpts,
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Spec: v1alpha1.SkyhookSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageApply,
		)
		Expect(pod.Spec.ImagePullSecrets).To(BeEmpty())
	})

	It("Interrupt pods should not have imagePullSecrets when ImagePullSecret is empty", func() {
		emptyOpts := SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			ImagePullSecret:      "", // Empty - no pull secret
			CopyDirRoot:          "/tmp",
			ReapplyOnReboot:      true,
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
		}

		pod := createInterruptPodForPackage(
			emptyOpts,
			&v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
			"argEncode",
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				Skyhook: &v1alpha1.Skyhook{
					Spec: v1alpha1.SkyhookSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageInterrupt,
		)
		Expect(pod.Spec.ImagePullSecrets).To(BeEmpty())
	})

	It("should generate deterministic pod names", func() {
		// Setup basic test data
		skyhook := &wrapper.Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
			},
		}

		package1 := &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "test-package",
				Version: "1.2.3",
			},
		}

		package2 := &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "test-package",
				Version: "1.2.4",
			},
		}

		nodeName := "test-node"
		nodeName2 := "test-node-2"

		// Create a function to generate the namePrefix in the same way the controller does
		createNamePrefix := func(skyhookName, pkgName, pkgVersion, stage string) string {
			return fmt.Sprintf("%s-%s-%s-%s", skyhookName, pkgName, pkgVersion, stage)
		}

		// Test 1: Deterministic behavior (same inputs = same output)
		prefix1 := createNamePrefix(skyhook.Name, package1.Name, package1.Version, string(v1alpha1.StageApply))
		name1 := generateSafeName(63, prefix1, nodeName)
		name2 := generateSafeName(63, prefix1, nodeName)
		Expect(name1).To(Equal(name2), "Generated pod names should be deterministic")

		// Test 2: Uniqueness with different inputs
		// Different stage
		prefixApply := createNamePrefix(skyhook.Name, package1.Name, package1.Version, string(v1alpha1.StageApply))
		prefixConfig := createNamePrefix(skyhook.Name, package1.Name, package1.Version, string(v1alpha1.StageConfig))
		nameApply := generateSafeName(63, prefixApply, nodeName)
		nameConfig := generateSafeName(63, prefixConfig, nodeName)
		Expect(nameApply).NotTo(Equal(nameConfig), "Different stages should produce different pod names")

		// Different package version
		prefix2 := createNamePrefix(skyhook.Name, package2.Name, package2.Version, string(v1alpha1.StageApply))
		nameVersion1 := generateSafeName(63, prefix1, nodeName)
		nameVersion2 := generateSafeName(63, prefix2, nodeName)
		Expect(nameVersion1).NotTo(Equal(nameVersion2), "Different package versions should produce different pod names")

		// Different node
		nameNode1 := generateSafeName(63, prefix1, nodeName)
		nameNode2 := generateSafeName(63, prefix1, nodeName2)
		Expect(nameNode1).NotTo(Equal(nameNode2), "Different nodes should produce different pod names")

		// Test for uninstall pods with timestamp
		uninstallPrefix1 := fmt.Sprintf("%s-uninstall-123456789", prefixApply)
		uninstallPrefix2 := fmt.Sprintf("%s-uninstall-987654321", prefixApply)
		uninstallName1 := generateSafeName(63, uninstallPrefix1, nodeName)
		uninstallName2 := generateSafeName(63, uninstallPrefix2, nodeName)
		Expect(uninstallName1).NotTo(Equal(uninstallName2), "Uninstall pods with different timestamps should have different names")
		Expect(uninstallName1).NotTo(Equal(nameApply), "Uninstall pod name should be different from regular pod name")

		// Test 3: Length constraints
		longSkyhookName := "this-is-a-very-long-skyhook-name-that-exceeds-kubernetes-naming-limits-by-a-significant-margin"
		longPackageName := "this-is-a-very-long-package-name-that-also-exceeds-kubernetes-naming-limits"
		longPackageVersion := "1.2.3.4.5.6.7.8.9.10"
		longPrefix := createNamePrefix(longSkyhookName, longPackageName, longPackageVersion, string(v1alpha1.StageApply))
		longName := generateSafeName(63, longPrefix, "node1")
		Expect(len(longName)).To(BeNumerically("<=", 63), "Pod name should not exceed Kubernetes 63 character limit")
		Expect(longName).To(MatchRegexp(`-[0-9a-f]+$`), "Pod name should end with a hash component")
	})

	It("should correctly identify if a pod matches a package", func() {

		// Create a test package
		testPackage := &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "test-package",
				Version: "1.2.3",
			},
			Image: "test-image:1.2.3",
			Resources: &v1alpha1.ResourceRequirements{
				CPURequest:    resource.MustParse("100m"),
				CPULimit:      resource.MustParse("200m"),
				MemoryRequest: resource.MustParse("64Mi"),
				MemoryLimit:   resource.MustParse("128Mi"),
			},
		}

		// Create a test skyhook
		testSkyhook := &wrapper.Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.SkyhookSpec{
					Packages: v1alpha1.Packages{
						"test-package": *testPackage,
					},
				},
			},
		}

		// Stage to test
		testStage := v1alpha1.StageApply

		// Create actual pods that would be created by the operator functions
		// First using CreatePodFromPackage
		actualPod := createPodFromPackage(operator.opts, testPackage, testSkyhook, "test-node", testStage)

		// Verify that the pod matches the package according to PodMatchesPackage
		matches := podMatchesPackage(operator.opts, testPackage, *actualPod, testSkyhook, testStage)
		Expect(matches).To(BeTrue(), "PodMatchesPackage should recognize the pod it created")

		// Now let's modify the package version and see if it correctly identifies non-matches
		modifiedPackage := testPackage.DeepCopy()
		modifiedPackage.Version = "1.2.4"

		matches = podMatchesPackage(operator.opts, modifiedPackage, *actualPod, testSkyhook, testStage)
		Expect(matches).To(BeFalse(), "PodMatchesPackage should not match when package version changed")

		// Test with different stage
		matches = podMatchesPackage(operator.opts, testPackage, *actualPod, testSkyhook, v1alpha1.StageConfig)
		Expect(matches).To(BeFalse(), "PodMatchesPackage should not match when stage changed")

		// Test with interrupt pods
		interruptPod := createInterruptPodForPackage(
			operator.opts,
			&v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
			"argEncode",
			testPackage,
			testSkyhook,
			"test-node",
			testStage,
		)

		// Verify that the interrupt pod matches the package
		matches = podMatchesPackage(operator.opts, testPackage, *interruptPod, testSkyhook, testStage)
		Expect(matches).To(BeTrue(), "PodMatchesPackage should recognize the interrupt pod it created")
	})

	It("should generate valid volume names", func() {
		tests := []struct {
			name        string
			prefix      string
			nodeName    string
			expectedLen int
			shouldMatch string
			description string
		}{
			{
				name:        "short name",
				prefix:      "metadata",
				nodeName:    "node1",
				expectedLen: 23, // "metadata-node1-" + 8 char hash
				description: "should handle short names",
			},
			{
				name:        "very long node name",
				prefix:      "metadata",
				nodeName:    "very-long-node-name-that-exceeds-kubernetes-limits-and-needs-to-be-truncated-to-something-shorter",
				expectedLen: 63,
				description: "should handle long names by hashing",
			},
			{
				name:        "consistent hashing",
				prefix:      "metadata",
				nodeName:    "node1",
				shouldMatch: generateSafeName(63, "metadata", "node1"),
				description: "should generate consistent names for the same input",
			},
		}

		for _, tt := range tests {
			result := generateSafeName(63, tt.prefix, tt.nodeName)

			if tt.expectedLen > 0 {
				Expect(len(result)).To(Equal(tt.expectedLen), tt.description)
			}
			if tt.shouldMatch != "" {
				Expect(result).To(Equal(tt.shouldMatch), tt.description)
			}
			Expect(len(result)).To(BeNumerically("<=", 63), "volume name should never exceed 63 characters")
			Expect(result).To(MatchRegexp(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), "volume name should match kubernetes naming requirements")
		}
	})

	It("should generate valid configmap names", func() {
		tests := []struct {
			name        string
			skyhookName string
			nodeName    string
			expectedLen int
			shouldMatch string
			description string
		}{
			{
				name:        "short names",
				skyhookName: "skyhook1",
				nodeName:    "node1",
				expectedLen: 32, // "skyhook1-node1-metadata-" + 8 char hash
				description: "should handle short names",
			},
			{
				name:        "very long names",
				skyhookName: "very-long-skyhook-name",
				nodeName:    "very-long-node-name-that-exceeds-kubernetes-limits-and-needs-to-be-truncated",
				expectedLen: 63,
				description: "should handle long names by truncating and hashing",
			},
			{
				name:        "consistent hashing",
				skyhookName: "skyhook1",
				nodeName:    "node1",
				shouldMatch: generateSafeName(63, "skyhook1", "node1", "metadata"),
				description: "should generate consistent names for the same input",
			},
			{
				name:        "handles dots in names",
				skyhookName: "skyhook.1",
				nodeName:    "node.1",
				expectedLen: 34,
				description: "should handle dots in names consistently",
			},
		}

		for _, tt := range tests {
			result := generateSafeName(63, tt.skyhookName, tt.nodeName, "metadata")

			if tt.expectedLen > 0 {
				Expect(len(result)).To(Equal(tt.expectedLen), tt.description)
			}
			if tt.shouldMatch != "" {
				Expect(result).To(Equal(tt.shouldMatch), tt.description)
			}
			Expect(len(result)).To(BeNumerically("<=", 63), "configmap name should never exceed 63 characters")
			Expect(result).To(MatchRegexp(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), "configmap name should match kubernetes naming requirements")
		}
	})

	It("should create metadata configmap with packages.json including agentVersion and packages", func() {
		// build minimal skyhook and node
		skyhookCR := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				Name: "skyhook-meta",
				UID:  "uid-1234",
			},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"pkg1": {
						PackageRef: v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"},
						Image:      "ghcr.io/org/pkg1",
					},
				},
			},
		}
		sw := wrapper.NewSkyhookWrapper(skyhookCR)

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"a": "b"}}}

		// use initialized reconciler
		r := operator

		// upsert configmap
		Expect(r.UpsertNodeLabelsAnnotationsPackages(ctx, sw, node)).To(Succeed())

		// fetch configmap
		cmName := generateSafeName(253, sw.Name, node.Name, "metadata")
		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: opts.Namespace}, &cm)).To(Succeed())

		// validate packages.json exists and has expected agentVersion and packages
		Expect(cm.Data).To(HaveKey("packages.json"))
		var meta struct {
			AgentVersion string         `json:"agentVersion"`
			Packages     map[string]any `json:"packages"`
		}
		Expect(json.Unmarshal([]byte(cm.Data["packages.json"]), &meta)).To(Succeed())
		Expect(meta.AgentVersion).To(Equal(opts.AgentVersion()))
		Expect(meta.Packages).To(HaveKey("pkg1"))
	})
})

var _ = Describe("Resource Comparison", func() {
	var (
		expectedPod *corev1.Pod
		actualPod   *corev1.Pod
		skyhook     *wrapper.Skyhook
		package_    *v1alpha1.Package
	)

	BeforeEach(func() {
		// Setup common test objects
		nodeName := "testNode"
		stage := v1alpha1.StageApply
		package_ = &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "test-package",
				Version: "1.0.0",
			},
			Image: "test-image",
		}

		skyhook = &wrapper.Skyhook{
			Skyhook: &v1alpha1.Skyhook{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"test-package": *package_,
					},
				},
			},
		}

		// Create base pod structure, to much work to do it again
		expectedPod = createPodFromPackage(operator.opts, package_, skyhook, nodeName, stage)
		actualPod = expectedPod.DeepCopy()
	})

	It("should match when resources are identical", func() {
		// Setup: Add resources to package and expected pod
		newPackage := *package_
		newPackage.Resources = &v1alpha1.ResourceRequirements{
			CPURequest:    resource.MustParse("100m"),
			CPULimit:      resource.MustParse("200m"),
			MemoryRequest: resource.MustParse("128Mi"),
			MemoryLimit:   resource.MustParse("256Mi"),
		}
		skyhook.Spec.Packages["test-package"] = newPackage

		expectedResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}

		// Set resources for all init containers in expected pod
		for i := range expectedPod.Spec.InitContainers {
			expectedPod.Spec.InitContainers[i].Resources = expectedResources
		}

		// Test: Set actual pod resources to match expected
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = expectedResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	It("should not match when resources differ", func() {
		// Setup: Add resources to package and expected pod
		newPackage := *package_
		newPackage.Resources = &v1alpha1.ResourceRequirements{
			CPURequest:    resource.MustParse("100m"),
			CPULimit:      resource.MustParse("200m"),
			MemoryRequest: resource.MustParse("128Mi"),
			MemoryLimit:   resource.MustParse("256Mi"),
		}
		skyhook.Spec.Packages["test-package"] = newPackage

		expectedResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}

		// Set resources for all init containers in expected pod
		for i := range expectedPod.Spec.InitContainers {
			expectedPod.Spec.InitContainers[i].Resources = expectedResources
		}

		// Test: Set different CPU request in actual pod for all init containers
		differentResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"), // Different CPU request
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = differentResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should match when no resources are specified and pod has no overrides", func() {
		// Setup: Ensure no resources in package
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Test: Ensure pod has no resource overrides for any init container
		emptyResources := corev1.ResourceRequirements{}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = emptyResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	It("should not match when no resources are specified but pod has requests", func() {
		// Setup: Ensure no resources in package
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Test: Add resource requests to all init containers
		requestResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = requestResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should not match when no resources are specified but pod has limits", func() {
		// Setup: Ensure no resources in package
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Test: Add resource limits to all init containers
		limitResources := corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = limitResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should ignore SKYHOOK_RESOURCE_ID env var", func() {
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Setup: Add SKYHOOK_RESOURCE_ID env var to all init containers
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Env = append(actualPod.Spec.InitContainers[i].Env, corev1.EnvVar{
				Name:  "SKYHOOK_RESOURCE_ID",
				Value: "SOME_VALUE",
			})
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	It("should not ignore non static env vars", func() {
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Setup: Add SKYHOOK_RESOURCE_ID env var to all init containers
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Env = append(actualPod.Spec.InitContainers[i].Env, corev1.EnvVar{
				Name:  "SOME_ENV_VAR",
				Value: "SOME_VALUE",
			})
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.Skyhook, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should partition nodes into compartments", func() {
		skyhooks := &v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "skyhook-a"},
					Spec: v1alpha1.SkyhookSpec{
						DeploymentPolicy: "deployment-policy-a",
					},
				},
			},
		}
		nodes := &corev1.NodeList{
			Items: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"a": "a"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-b", Labels: map[string]string{"a": "a"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-c", Labels: map[string]string{"b": "b"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node-d", Labels: map[string]string{"c": "c"}}},
			},
		}
		deploymentPolicies := &v1alpha1.DeploymentPolicyList{
			Items: []v1alpha1.DeploymentPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "deployment-policy-a"},
					Spec: v1alpha1.DeploymentPolicySpec{
						Compartments: []v1alpha1.Compartment{
							{Name: "compartment-a", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"a": "a"}}},
							{Name: "compartment-b", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"c": "c"}}},
						},
					},
				},
			},
		}

		clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks[0].GetCompartments()).To(HaveLen(3))
		Expect(clusterState.skyhooks[0].GetCompartments()["compartment-a"].GetNodes()).To(HaveLen(2))
		Expect(clusterState.skyhooks[0].GetCompartments()["compartment-b"].GetNodes()).To(HaveLen(1))
		Expect(clusterState.skyhooks[0].GetCompartments()["__default__"].GetNodes()).To(HaveLen(1))
	})
})

func TestGenerateValidPodNames(t *testing.T) {
	g := NewWithT(t)

	// Test short name
	name := generateSafeName(63, "test", "node1")
	g.Expect(len(name)).To(Equal(19)) // "test-node1-" + 8 char hash
	g.Expect(name).To(MatchRegexp(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`))

	// Test very long name
	name = generateSafeName(63, "test-very-long-name-that-should-be-truncated", "node1")
	g.Expect(len(name)).To(Equal(59))
	g.Expect(name).To(MatchRegexp(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`))

	// Test consistent hashing
	name1 := generateSafeName(63, "test", "node1")
	name2 := generateSafeName(63, "test", "node1")
	g.Expect(name1).To(Equal(name2))

	// Test dots in name
	name = generateSafeName(63, "test.name", "node.1")
	g.Expect(name).To(MatchRegexp(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`))
	g.Expect(len(name)).To(Equal(25)) // "test-name-node-1-" + 8 char hash
}

func TestHandleVersionChangeAutoReset(t *testing.T) {
	g := NewWithT(t)

	t.Run("should reset batch state when version change detected with config enabled", func(t *testing.T) {
		// Create a skyhook with batch state and an old package version
		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
					ResetBatchStateOnCompletion: ptr(true),
				},
				Packages: v1alpha1.Packages{
					"test-package": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{
							Name:    "test-package",
							Version: "v2.0.0", // New version
						},
						Image: "test-image",
					},
				},
			},
			Status: v1alpha1.SkyhookStatus{
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
				ResetBatchStateOnCompletion: ptr(true),
			},
		}

		// Create a mock node with old package version
		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"test-package|v1.0.0": v1alpha1.PackageStatus{
				Name:    "test-package",
				Version: "v1.0.0", // Old version
				Image:   "test-image",
				Stage:   v1alpha1.StageConfig,
				State:   v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().Upsert(v1alpha1.PackageRef{Name: "test-package", Version: "v2.0.0"}, "test-image", v1alpha1.StateInProgress, v1alpha1.StageUpgrade, int32(0), "").Return(nil).Maybe()
		node.EXPECT().PackageStatus("test-package|v2.0.0").Return(&v1alpha1.PackageStatus{Stage: v1alpha1.StageUpgrade}, true).Once()
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress).Maybe()

		skyhookNodes := &skyhookNodes{
			skyhook:          wrapper.NewSkyhookWrapper(skyhook),
			nodes:            []wrapper.SkyhookNode{node},
			deploymentPolicy: deploymentPolicy,
		}

		// Call HandleVersionChange
		_, err := HandleVersionChange(skyhookNodes)
		g.Expect(err).To(BeNil())

		// Verify batch state was reset
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState).NotTo(BeNil())
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(1))
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.ConsecutiveFailures).To(Equal(0))
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(0))
		g.Expect(skyhookNodes.skyhook.Updated).To(BeTrue())
	})

	t.Run("should not reset batch state when config is disabled", func(t *testing.T) {
		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
					ResetBatchStateOnCompletion: ptr(false),
				},
				Packages: v1alpha1.Packages{
					"test-package": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{
							Name:    "test-package",
							Version: "v2.0.0",
						},
						Image: "test-image",
					},
				},
			},
			Status: v1alpha1.SkyhookStatus{
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
				ResetBatchStateOnCompletion: ptr(true),
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"test-package|v1.0.0": v1alpha1.PackageStatus{
				Name:    "test-package",
				Version: "v1.0.0",
				Image:   "test-image",
				Stage:   v1alpha1.StageConfig,
				State:   v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().Upsert(v1alpha1.PackageRef{Name: "test-package", Version: "v2.0.0"}, "test-image", v1alpha1.StateInProgress, v1alpha1.StageUpgrade, int32(0), "").Return(nil).Maybe()
		node.EXPECT().PackageStatus("test-package|v2.0.0").Return(&v1alpha1.PackageStatus{Stage: v1alpha1.StageUpgrade}, true).Once()
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress).Maybe()

		skyhookNodes := &skyhookNodes{
			skyhook:          wrapper.NewSkyhookWrapper(skyhook),
			nodes:            []wrapper.SkyhookNode{node},
			deploymentPolicy: deploymentPolicy,
		}

		_, err := HandleVersionChange(skyhookNodes)
		g.Expect(err).To(BeNil())

		// Verify batch state was NOT reset (config disabled)
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(5))
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(10))
	})

	t.Run("should not reset when no version changes detected", func(t *testing.T) {
		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				DeploymentPolicyOptions: &v1alpha1.DeploymentPolicyOptions{
					ResetBatchStateOnCompletion: ptr(true),
				},
				Packages: v1alpha1.Packages{
					"test-package": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{
							Name:    "test-package",
							Version: "v1.0.0", // Same version
						},
						Image: "test-image",
					},
				},
			},
			Status: v1alpha1.SkyhookStatus{
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
				ResetBatchStateOnCompletion: ptr(true),
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"test-package|v1.0.0": v1alpha1.PackageStatus{
				Name:    "test-package",
				Version: "v1.0.0", // Same version
				Image:   "test-image",
				Stage:   v1alpha1.StageConfig,
				State:   v1alpha1.StateComplete,
			},
		}, nil)

		skyhookNodes := &skyhookNodes{
			skyhook:          wrapper.NewSkyhookWrapper(skyhook),
			nodes:            []wrapper.SkyhookNode{node},
			deploymentPolicy: deploymentPolicy,
		}

		_, err := HandleVersionChange(skyhookNodes)
		g.Expect(err).To(BeNil())

		// Verify batch state was NOT reset (no version change)
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CurrentBatch).To(Equal(5))
		g.Expect(skyhookNodes.skyhook.Status.CompartmentStatuses["compartment-1"].BatchState.CompletedNodes).To(Equal(10))
	})
}

func TestHandleUninstallRequests(t *testing.T) {
	t.Run("should trigger uninstall for package at complete install stage", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						ConfigMap:  map[string]string{"install.sh": "echo hi"},
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageUninstall, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(HaveLen(1))
		g.Expect(result[0].Name).To(Equal("my-pkg"))
		// Verify the returned package has full config (not a synthetic package)
		g.Expect(result[0].ConfigMap).To(HaveKey("install.sh"))
	})

	t.Run("should skip package absent from node state (already uninstalled)", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{}, nil) // package not in state

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})

	t.Run("should skip package with IsUninstalling false", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})

	t.Run("should return full package with ConfigMap/Env/Resources", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						ConfigMap:  map[string]string{"uninstall.sh": "echo bye"},
						Env:        []corev1.EnvVar{{Name: "MY_VAR", Value: "hello"}},
						Resources: &v1alpha1.ResourceRequirements{
							CPURequest: resource.MustParse("100m"),
						},
						Uninstall: &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageUninstall, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(HaveLen(1))
		g.Expect(result[0].ConfigMap).To(HaveKeyWithValue("uninstall.sh", "echo bye"))
		g.Expect(result[0].Env).To(HaveLen(1))
		g.Expect(result[0].Resources).ToNot(BeNil())
	})

	t.Run("should trigger uninstall for PostInterrupt/Complete package (bug #1 regression)", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StagePostInterrupt, State: v1alpha1.StateComplete,
			},
		}, nil)
		// Expect uninstall trigger, NOT RemoveState
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageUninstall, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(HaveLen(1))
		g.Expect(result[0].Name).To(Equal("my-pkg"))
	})

	t.Run("should not trigger uninstall for StageInterrupt/InProgress (install mid-interrupt)", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageInterrupt, State: v1alpha1.StateInProgress,
			},
		}, nil)
		// No Upsert, no RemoveState — must wait for install interrupt to finish

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})

	t.Run("should cleanup StageUninstallInterrupt/Complete", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstallInterrupt, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().RemoveState(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})

	t.Run("should cleanup StageUninstallInterrupt/Complete even when apply=false (cancel-strand)", func(t *testing.T) {
		g := NewWithT(t)

		// User flipped apply back to false AFTER interrupt completed.
		// Must still RemoveState — otherwise the node state is stranded.
		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false}, // cancelled
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstallInterrupt, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().RemoveState(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})
}

func TestHandleVersionChange_WI3(t *testing.T) {
	t.Run("should preserve node state for package removed from spec with enabled=false", func(t *testing.T) {
		g := NewWithT(t)

		// D2 semantics: when an enabled=false package is removed from spec,
		// its node-state entry stays so the user can see the package's files
		// are still on the node (no uninstall.sh was ever run). The operator
		// stops tracking it; only config-update status is cleaned.
		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					// "removed-pkg" is NOT in spec
				},
			},
			Status: v1alpha1.SkyhookStatus{
				ConfigUpdates: map[string][]string{
					"removed-pkg": {"key1"},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"removed-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "removed-pkg", Version: "1.0.0", Image: "old-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		// No RemoveState, no SetStatus expected — operator leaves the entry alone.

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleVersionChange(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
		// Config updates for the removed package are still cleaned up.
		g.Expect(sn.skyhook.Status.ConfigUpdates).ToNot(HaveKey("removed-pkg"))
	})

	t.Run("should skip IsUninstalling packages", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		// No Upsert/RemoveState/SetStatus expected — package should be skipped

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleVersionChange(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})

}

func TestHandleCompletePod_WI4(t *testing.T) {
	t.Run("should RemoveState and zero metrics for explicit uninstall", func(t *testing.T) {
		g := NewWithT(t)

		skyhookCR := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		mockDAL := dalMock.NewMockDAL(t)
		mockDAL.EXPECT().GetSkyhook(context.Background(), "test-skyhook").Return(skyhookCR, nil)

		mockNode := wrapperMock.NewMockSkyhookNodeOnly(t)
		mockNode.EXPECT().RemoveState(v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}).Return(nil)

		r := &SkyhookReconciler{dal: mockDAL}
		packagePtr := &PackageSkyhook{
			PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
			Skyhook:    "test-skyhook",
			Stage:      v1alpha1.StageUninstall,
			Image:      "my-image",
		}

		updated, err := r.HandleCompletePod(context.Background(), mockNode, packagePtr, "apply")
		g.Expect(err).To(BeNil())
		g.Expect(updated).To(BeTrue())
	})

	t.Run("should transition to StageUninstallInterrupt for explicit uninstall with interrupt", func(t *testing.T) {
		g := NewWithT(t)

		skyhookCR := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		mockDAL := dalMock.NewMockDAL(t)
		mockDAL.EXPECT().GetSkyhook(context.Background(), "test-skyhook").Return(skyhookCR, nil)

		mockNode := wrapperMock.NewMockSkyhookNodeOnly(t)
		// With interrupt configured, should advance to StageUninstallInterrupt/InProgress
		// (NOT call RemoveState, NOT set StageUninstall/Complete).
		mockNode.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageUninstallInterrupt, int32(0), "",
		).Return(nil)

		r := &SkyhookReconciler{dal: mockDAL}
		packagePtr := &PackageSkyhook{
			PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
			Skyhook:    "test-skyhook",
			Stage:      v1alpha1.StageUninstall,
			Image:      "my-image",
		}

		updated, err := r.HandleCompletePod(context.Background(), mockNode, packagePtr, "apply")
		g.Expect(err).To(BeNil())
		g.Expect(updated).To(BeTrue())
	})

	t.Run("should RemoveState defensively when completing pod's version differs from spec", func(t *testing.T) {
		g := NewWithT(t)

		// Spec has v2.0.0, but a pod completes at v1.0.0 (version mismatch — shouldn't
		// happen under new webhook rules, but HandleCompletePod guards defensively).
		skyhookCR := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "2.0.0"},
						Image:      "my-image-v2",
					},
				},
			},
		}

		mockDAL := dalMock.NewMockDAL(t)
		mockDAL.EXPECT().GetSkyhook(context.Background(), "test-skyhook").Return(skyhookCR, nil)

		mockNode := wrapperMock.NewMockSkyhookNodeOnly(t)
		// Defensive cleanup: RemoveState the old-version ref. No Upsert.
		mockNode.EXPECT().RemoveState(v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}).Return(nil)

		r := &SkyhookReconciler{dal: mockDAL}
		packagePtr := &PackageSkyhook{
			PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
			Skyhook:    "test-skyhook",
			Stage:      v1alpha1.StageUninstall,
			Image:      "my-image",
		}

		updated, err := r.HandleCompletePod(context.Background(), mockNode, packagePtr, "apply")
		g.Expect(err).To(BeNil())
		g.Expect(updated).To(BeTrue())
	})

	t.Run("should RemoveState when package removed from spec", func(t *testing.T) {
		g := NewWithT(t)

		skyhookCR := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					// my-pkg NOT in spec
				},
			},
		}

		mockDAL := dalMock.NewMockDAL(t)
		mockDAL.EXPECT().GetSkyhook(context.Background(), "test-skyhook").Return(skyhookCR, nil)

		mockNode := wrapperMock.NewMockSkyhookNodeOnly(t)
		mockNode.EXPECT().RemoveState(v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}).Return(nil)

		r := &SkyhookReconciler{dal: mockDAL}
		packagePtr := &PackageSkyhook{
			PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
			Skyhook:    "test-skyhook",
			Stage:      v1alpha1.StageUninstall,
			Image:      "my-image",
		}

		updated, err := r.HandleCompletePod(context.Background(), mockNode, packagePtr, "apply")
		g.Expect(err).To(BeNil())
		g.Expect(updated).To(BeTrue())
	})
}

func TestHandleCancelledUninstalls(t *testing.T) {
	t.Run("should reset InProgress uninstall to StageApply", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false}, // cancelled
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
		}, nil)
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageApply, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		err := HandleCancelledUninstalls(sn)
		g.Expect(err).To(BeNil())
	})

	t.Run("should reset Erroring uninstall to StageApply", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false}, // cancelled
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateErroring,
			},
		}, nil)
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageApply, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		err := HandleCancelledUninstalls(sn)
		g.Expect(err).To(BeNil())
	})

	t.Run("should skip active uninstall", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true}, // still active
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
		}, nil)
		// No Upsert/SetStatus expected — package should be skipped

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		err := HandleCancelledUninstalls(sn)
		g.Expect(err).To(BeNil())
	})

	t.Run("should not cancel finalizer-driven uninstall during CR deletion", func(t *testing.T) {
		g := NewWithT(t)

		now := metav1.Now()
		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now, // CR is being deleted
			},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false}, // apply=false but CR deleting
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
		}, nil)
		// No Upsert expected — should NOT cancel during deletion

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		err := HandleCancelledUninstalls(sn)
		g.Expect(err).To(BeNil())
	})
}

func TestHandleUninstallRequests_FinalizerPath(t *testing.T) {
	t.Run("should trigger uninstall for enabled package during CR deletion even with apply=false", func(t *testing.T) {
		g := NewWithT(t)

		now := metav1.Now()
		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now,
			},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageUninstall, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(HaveLen(1))
		g.Expect(result[0].Name).To(Equal("my-pkg"))
	})
}

func TestHandleUninstallRequests_InstallCompleteGuard(t *testing.T) {
	t.Run("should not trigger uninstall for package still installing", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageApply, State: v1alpha1.StateInProgress, // still installing
			},
		}, nil)
		// No Upsert expected — package should be skipped (not yet complete)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleUninstallRequests(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})
}

func TestFilterUninstallForNode(t *testing.T) {
	pkgP := &v1alpha1.Package{
		PackageRef: v1alpha1.PackageRef{Name: "pkg-p", Version: "1.0.0"},
		Image:      "img",
		Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
	}
	pkgQ := &v1alpha1.Package{
		PackageRef: v1alpha1.PackageRef{Name: "pkg-q", Version: "2.0.0"},
		Image:      "img",
		Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
	}
	presentP := v1alpha1.PackageStatus{
		Name: "pkg-p", Version: "1.0.0", Image: "img",
		Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
	}
	presentQ := v1alpha1.PackageStatus{
		Name: "pkg-q", Version: "2.0.0", Image: "img",
		Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
	}

	t.Run("empty input returns empty", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(filterUninstallForNode(nil, v1alpha1.NodeState{})).To(BeEmpty())
		g.Expect(filterUninstallForNode([]*v1alpha1.Package{}, v1alpha1.NodeState{"pkg-p|1.0.0": presentP})).To(BeEmpty())
	})

	t.Run("package present in nodeState is kept", func(t *testing.T) {
		g := NewWithT(t)
		state := v1alpha1.NodeState{"pkg-p|1.0.0": presentP}
		result := filterUninstallForNode([]*v1alpha1.Package{pkgP}, state)
		g.Expect(result).To(HaveLen(1))
		g.Expect(result[0].Name).To(Equal("pkg-p"))
	})

	t.Run("package absent from nodeState is dropped (bug scenario)", func(t *testing.T) {
		// Guards against the multi-node-staggered-uninstall bug:
		// HandleUninstallRequests builds toUninstall globally across all of
		// a Skyhook's nodes, so a package pending uninstall on node B can
		// land in the list even though node A already has it absent.
		// Without this filter, prepending toUninstall to node A's toRun
		// would feed ApplyPackage a package-not-in-state, which falls
		// through to StageApply and re-installs a package the user
		// explicitly uninstalled.
		g := NewWithT(t)
		state := v1alpha1.NodeState{} // node A — already uninstalled
		result := filterUninstallForNode([]*v1alpha1.Package{pkgP}, state)
		g.Expect(result).To(BeEmpty())
	})

	t.Run("mixed: present kept, absent dropped, order preserved", func(t *testing.T) {
		g := NewWithT(t)
		state := v1alpha1.NodeState{"pkg-q|2.0.0": presentQ} // only Q present
		result := filterUninstallForNode([]*v1alpha1.Package{pkgP, pkgQ}, state)
		g.Expect(result).To(HaveLen(1))
		g.Expect(result[0].Name).To(Equal("pkg-q"))
	})

	t.Run("version mismatch treated as absent", func(t *testing.T) {
		// GetUniqueName keys on name+version. A stale state entry for a
		// different version of the same package is a cache miss here, so
		// the uninstall entry is dropped (no install pod will be spawned
		// for the stale version either).
		g := NewWithT(t)
		state := v1alpha1.NodeState{
			"pkg-p|0.9.0": v1alpha1.PackageStatus{Name: "pkg-p", Version: "0.9.0", Image: "img"},
		}
		result := filterUninstallForNode([]*v1alpha1.Package{pkgP}, state)
		g.Expect(result).To(BeEmpty())
	})
}

func TestShouldSkipApplyForUninstall(t *testing.T) {
	pkgExplicitApply := &v1alpha1.Package{
		PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
		Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
	}
	pkgEnabledOnly := &v1alpha1.Package{
		PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
		Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
	}
	pkgDisabled := &v1alpha1.Package{
		PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
		Uninstall:  &v1alpha1.Uninstall{Enabled: false},
	}
	inCycle := v1alpha1.NodeState{
		"pkg|1.0.0": v1alpha1.PackageStatus{
			Name: "pkg", Version: "1.0.0",
			Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
		},
	}
	complete := v1alpha1.NodeState{
		"pkg|1.0.0": v1alpha1.PackageStatus{
			Name: "pkg", Version: "1.0.0",
			Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
		},
	}
	empty := v1alpha1.NodeState{}

	t.Run("skip: uninstall cycle in progress", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(shouldSkipApplyForUninstall(pkgExplicitApply, inCycle, false)).To(BeTrue())
	})

	t.Run("skip: explicit uninstall completed (apply=true, absent)", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(shouldSkipApplyForUninstall(pkgExplicitApply, empty, false)).To(BeTrue())
	})

	t.Run("skip: finalizer uninstall completed (apply=false, CR deleting, absent)", func(t *testing.T) {
		// Guards against the reinstall loop: CR is being deleted, the
		// finalizer drove uninstall to completion on this node, and the
		// package's spec Apply is still false. IsUninstalling() is false
		// here, so the old pre-gate predicate missed this and
		// ApplyPackage re-installed the package on the next reconcile.
		g := NewWithT(t)
		g.Expect(shouldSkipApplyForUninstall(pkgEnabledOnly, empty, true)).To(BeTrue())
	})

	t.Run("allow: never-installed enabled package, CR not being deleted", func(t *testing.T) {
		// This is the "too broad" hazard of the original finding: an
		// uninstall-enabled package that has never been installed looks
		// absent. Without the beingDeleted gate we'd never install it.
		g := NewWithT(t)
		g.Expect(shouldSkipApplyForUninstall(pkgEnabledOnly, empty, false)).To(BeFalse())
	})

	t.Run("allow: installed and complete, no uninstall requested", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(shouldSkipApplyForUninstall(pkgEnabledOnly, complete, false)).To(BeFalse())
	})

	t.Run("allow: disabled package absent — ordinary first-install path", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(shouldSkipApplyForUninstall(pkgDisabled, empty, true)).To(BeFalse())
	})
}

func TestUpdateBlockedCondition(t *testing.T) {
	const dependentPkgName = "pkg-b"
	blockedCondType := fmt.Sprintf("%s/Blocked", v1alpha1.METADATA_PREFIX)

	// assertBlocked fails if Blocked isn't set and returns its Message otherwise.
	assertBlocked := func(g *WithT, sn *skyhookNodes) string {
		for _, c := range sn.skyhook.Status.Conditions {
			if c.Type == blockedCondType {
				g.Expect(c.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(c.Reason).To(Equal("DependencyUninstalled"))
				return c.Message
			}
		}
		g.Fail("expected Blocked condition to be set")
		return ""
	}
	assertNotBlocked := func(g *WithT, sn *skyhookNodes) {
		for _, c := range sn.skyhook.Status.Conditions {
			g.Expect(c.Type).ToNot(Equal(blockedCondType))
		}
	}
	matchesPkgB := func(p v1alpha1.Package) bool { return p.Name == dependentPkgName }

	t.Run("set: dep in uninstall cycle, dependent has pending work", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"dep-a|1.0.0": v1alpha1.PackageStatus{
				Name: "dep-a", Version: "1.0.0", Image: "img-a",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
			// pkg-b still at StageApply/InProgress — not complete, has work to do.
			"pkg-b|2.0.0": v1alpha1.PackageStatus{
				Name: dependentPkgName, Version: "2.0.0", Image: "img-b",
				Stage: v1alpha1.StageApply, State: v1alpha1.StateInProgress,
			},
		}, nil)
		node.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(false)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())
		msg := assertBlocked(g, sn)
		g.Expect(msg).To(ContainSubstring("pkg-b is blocked"))
		g.Expect(msg).To(ContainSubstring("is being uninstalled"))
	})

	t.Run("set: dep uninstall completed (absent + IsUninstalling), dependent not complete", func(t *testing.T) {
		// After dep-a's uninstall pod finishes, dep-a is absent from nodeState
		// but IsUninstalling (spec still has apply=true). pkg-b is now permanently
		// blocked until the user cancels/re-installs dep-a — and since pkg-b isn't
		// complete, the Skyhook is still in_progress and Blocked must persist.
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		// dep-a absent from nodeState. pkg-b never installed.
		node.EXPECT().State().Return(v1alpha1.NodeState{}, nil)
		node.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(false)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())
		msg := assertBlocked(g, sn)
		g.Expect(msg).To(ContainSubstring("pkg-b is blocked"))
		g.Expect(msg).To(ContainSubstring("has been uninstalled"))
	})

	t.Run("clear: dep uninstalling but dependent is already complete on all nodes", func(t *testing.T) {
		// Per the rule "Blocked only when the Skyhook would otherwise be
		// in_progress": if pkg-b was installed before dep-a's uninstall started
		// and is now sitting at complete, there is no in-flight work that the
		// broken dep blocks. The Skyhook's in_progress status comes from dep-a
		// itself, not from pkg-b — don't double-signal via Blocked.
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
			Status: v1alpha1.SkyhookStatus{
				Conditions: []metav1.Condition{
					{Type: blockedCondType, Status: metav1.ConditionTrue},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"dep-a|1.0.0": v1alpha1.PackageStatus{
				Name: "dep-a", Version: "1.0.0", Image: "img-a",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
			"pkg-b|2.0.0": v1alpha1.PackageStatus{
				Name: dependentPkgName, Version: "2.0.0", Image: "img-b",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(true)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())
		assertNotBlocked(g, sn)
	})

	t.Run("clear: dep uninstall completed and dependent is complete on all nodes", func(t *testing.T) {
		// Post-uninstall, pkg-b is still complete from before. Per D2 the
		// Skyhook is complete (dep-a excluded as "uninstalled"). Don't raise
		// Blocked — there's no in-progress work to be blocked.
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"pkg-b|2.0.0": v1alpha1.PackageStatus{
				Name: dependentPkgName, Version: "2.0.0", Image: "img-b",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(true)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())
		assertNotBlocked(g, sn)
	})

	t.Run("clear: no dependencies are gone", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
			Status: v1alpha1.SkyhookStatus{
				Conditions: []metav1.Condition{
					{Type: blockedCondType, Status: metav1.ConditionTrue},
				},
			},
		}

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{},
		}

		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())
		assertNotBlocked(g, sn)
	})

	t.Run("tolerant: skip nodes whose State() errors, do not short-circuit", func(t *testing.T) {
		// A malformed nodeState annotation on one node must not abort the
		// per-Skyhook reconcile loop — that would make HandleFinalizer's
		// own malformed-state branch unreachable and drop its deletion-
		// specific DeletionBlocked condition. UpdateNodeStateMalformedCondition
		// surfaces the parse failure separately.
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
		}

		badNode := wrapperMock.NewMockSkyhookNode(t)
		badNode.EXPECT().State().Return(nil, fmt.Errorf("unmarshal: unexpected end of JSON input"))

		goodNode := wrapperMock.NewMockSkyhookNode(t)
		goodNode.EXPECT().State().Return(v1alpha1.NodeState{
			"dep-a|1.0.0": v1alpha1.PackageStatus{
				Name: "dep-a", Version: "1.0.0", Image: "img-a",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
		}, nil)
		// isPackageCompleteOnAllNodes short-circuits on the first false;
		// we don't care which node is probed first. Use .Maybe() on both.
		goodNode.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(false).Maybe()
		badNode.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(false).Maybe()

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{badNode, goodNode},
		}

		// No error returned.
		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())

		// Good node's in-cycle observation is surfaced even though the other
		// node is malformed.
		msg := assertBlocked(g, sn)
		g.Expect(msg).To(ContainSubstring("is being uninstalled"))
	})

	t.Run("tolerant: unreadable node blocks 'done' determination (no premature cleared condition)", func(t *testing.T) {
		// One node is malformed, the other shows dep-a absent with IsUninstalling.
		// Without the unreadable guard we'd (wrongly) flag dep-a as "done" and
		// emit a "has been uninstalled" message. With the guard we cannot rule
		// out the unreadable node still having dep-a, so neither inCycle nor
		// done fires — the Blocked condition stays clear and the malformed
		// signal is left to NodeStateMalformed alone.
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"dep-a": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "dep-a", Version: "1.0.0"},
						Image:      "img-a",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
					dependentPkgName: v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: dependentPkgName, Version: "2.0.0"},
						Image:      "img-b",
						DependsOn:  map[string]string{"dep-a": "1.0.0"},
					},
				},
			},
		}

		badNode := wrapperMock.NewMockSkyhookNode(t)
		badNode.EXPECT().State().Return(nil, fmt.Errorf("unmarshal: unexpected end of JSON input"))

		goodNode := wrapperMock.NewMockSkyhookNode(t)
		goodNode.EXPECT().State().Return(v1alpha1.NodeState{}, nil) // dep-a absent
		// pkg-b is not complete on either node — isPackageCompleteOnAllNodes
		// will short-circuit the first time it sees false, so we only need a
		// single expectation for whichever runs first. Use .Maybe() for both.
		goodNode.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(false).Maybe()
		badNode.EXPECT().IsPackageComplete(mock.MatchedBy(matchesPkgB)).Return(false).Maybe()

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{badNode, goodNode},
		}

		g.Expect(sn.UpdateBlockedCondition()).To(Succeed())
		assertNotBlocked(g, sn)
	})
}

func TestUpdateNodeStateMalformedCondition(t *testing.T) {
	condType := fmt.Sprintf("%s/NodeStateMalformed", v1alpha1.METADATA_PREFIX)

	// findCondition returns the NodeStateMalformed condition or nil.
	findCondition := func(sn *skyhookNodes) *metav1.Condition {
		for i, c := range sn.skyhook.Status.Conditions {
			if c.Type == condType {
				return &sn.skyhook.Status.Conditions[i]
			}
		}
		return nil
	}

	// makeBadNode produces a mock node whose State() returns a parse error.
	makeBadNode := func(t *testing.T, name string) wrapper.SkyhookNode {
		n := wrapperMock.NewMockSkyhookNode(t)
		n.EXPECT().GetNode().Return(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}).Maybe()
		n.EXPECT().State().Return(nil, fmt.Errorf("unmarshal: unexpected end of JSON input")).Maybe()
		return n
	}

	t.Run("clears condition when no nodes are malformed", func(t *testing.T) {
		g := NewWithT(t)

		good := wrapperMock.NewMockSkyhookNode(t)
		good.EXPECT().State().Return(v1alpha1.NodeState{}, nil)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.Skyhook{
				Status: v1alpha1.SkyhookStatus{
					Conditions: []metav1.Condition{
						{Type: condType, Status: metav1.ConditionTrue, Reason: "ParseError", Message: "stale"},
					},
				},
			}),
			nodes: []wrapper.SkyhookNode{good},
		}

		sn.UpdateNodeStateMalformedCondition()
		g.Expect(findCondition(sn)).To(BeNil())
	})

	t.Run("lists every name when count is at or below the cap", func(t *testing.T) {
		g := NewWithT(t)

		nodes := []wrapper.SkyhookNode{
			makeBadNode(t, "node-c"),
			makeBadNode(t, "node-a"),
			makeBadNode(t, "node-b"),
		}
		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.Skyhook{}),
			nodes:   nodes,
		}

		sn.UpdateNodeStateMalformedCondition()
		c := findCondition(sn)
		g.Expect(c).NotTo(BeNil())
		g.Expect(c.Status).To(Equal(metav1.ConditionTrue))
		g.Expect(c.Reason).To(Equal("ParseError"))
		// All names listed, sorted, no "and N more" suffix.
		g.Expect(c.Message).To(Equal("nodeState annotation cannot be parsed on 3 node(s): node-a, node-b, node-c"))
	})

	t.Run("caps the listed names and reports remainder when over cap", func(t *testing.T) {
		g := NewWithT(t)

		// 8 malformed nodes — over the cap of maxMalformedNodesListed (5).
		names := []string{"node-1", "node-2", "node-3", "node-4", "node-5", "node-6", "node-7", "node-8"}
		nodes := make([]wrapper.SkyhookNode, 0, len(names))
		for _, n := range names {
			nodes = append(nodes, makeBadNode(t, n))
		}
		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.Skyhook{}),
			nodes:   nodes,
		}

		sn.UpdateNodeStateMalformedCondition()
		c := findCondition(sn)
		g.Expect(c).NotTo(BeNil())
		// Total count reflects all 8 affected nodes; only the first 5 (sorted)
		// are inlined and the remainder is summarised.
		g.Expect(c.Message).To(Equal(
			"nodeState annotation cannot be parsed on 8 node(s): node-1, node-2, node-3, node-4, node-5 and 3 more"))
	})

	t.Run("listed names are individually shortened by truncateNodeName", func(t *testing.T) {
		g := NewWithT(t)

		long := "ip-10-0-1-234.us-west-2.compute.internal"
		nodes := []wrapper.SkyhookNode{makeBadNode(t, long)}
		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.Skyhook{}),
			nodes:   nodes,
		}

		sn.UpdateNodeStateMalformedCondition()
		c := findCondition(sn)
		g.Expect(c).NotTo(BeNil())
		// Per-name truncation still applies inside the cap.
		g.Expect(c.Message).To(ContainSubstring(truncateNodeName(long)))
		g.Expect(c.Message).NotTo(ContainSubstring(long))
	})
}

func TestHasUninstallWork(t *testing.T) {
	t.Run("should return true when a package has IsUninstalling", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{},
		}

		hasWork, err := sn.HasUninstallWork()
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(hasWork).To(BeTrue())
	})

	t.Run("should return true when node has StageUninstall even with apply=false", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageUninstall, State: v1alpha1.StateInProgress,
			},
		}, nil)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		hasWork, err := sn.HasUninstallWork()
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(hasWork).To(BeTrue())
	})

	t.Run("should return false when no uninstall work exists", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		hasWork, err := sn.HasUninstallWork()
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(hasWork).To(BeFalse())
	})

	t.Run("should return true when CR deleting and enabled package still in node state", func(t *testing.T) {
		g := NewWithT(t)

		now := metav1.Now()
		skyhook := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now,
			},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		hasWork, err := sn.HasUninstallWork()
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(hasWork).To(BeTrue())
	})
}

func TestHandleCompletePod_VersionComparison(t *testing.T) {
	t.Run("should RemoveState for same-version uninstall (finalizer path)", func(t *testing.T) {
		g := NewWithT(t)

		// Simulates finalizer-driven uninstall where apply=false but enabled=true
		skyhookCR := &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: true, Apply: false}, // NOT IsUninstalling
					},
				},
			},
		}

		mockDAL := dalMock.NewMockDAL(t)
		mockDAL.EXPECT().GetSkyhook(context.Background(), "test-skyhook").Return(skyhookCR, nil)

		mockNode := wrapperMock.NewMockSkyhookNodeOnly(t)
		mockNode.EXPECT().RemoveState(v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}).Return(nil)

		r := &SkyhookReconciler{dal: mockDAL}
		packagePtr := &PackageSkyhook{
			PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
			Skyhook:    "test-skyhook",
			Stage:      v1alpha1.StageUninstall,
			Image:      "my-image",
		}

		updated, err := r.HandleCompletePod(context.Background(), mockNode, packagePtr, "apply")
		g.Expect(err).To(BeNil())
		g.Expect(updated).To(BeTrue())
	})
}

func TestHandleVersionChange_DowngradeIsNoOp(t *testing.T) {
	t.Run("downgrade with enabled=false leaves old state in node state", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &v1alpha1.Uninstall{Enabled: false, Apply: false},
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|2.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "2.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		// No Upsert, no RemoveState for old version — old state is preserved.

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		result, err := HandleVersionChange(sn)
		g.Expect(err).To(BeNil())
		g.Expect(result).To(BeEmpty())
	})

	t.Run("upgrade still triggers StageUpgrade", func(t *testing.T) {
		g := NewWithT(t)

		skyhook := &v1alpha1.Skyhook{
			Spec: v1alpha1.SkyhookSpec{
				Packages: v1alpha1.Packages{
					"my-pkg": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "my-pkg", Version: "2.0.0"},
						Image:      "my-image",
					},
				},
			},
		}

		node := wrapperMock.NewMockSkyhookNode(t)
		node.EXPECT().State().Return(v1alpha1.NodeState{
			"my-pkg|1.0.0": v1alpha1.PackageStatus{
				Name: "my-pkg", Version: "1.0.0", Image: "my-image",
				Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete,
			},
		}, nil)
		node.EXPECT().PackageStatus("my-pkg|2.0.0").Return(nil, false)
		node.EXPECT().Upsert(
			v1alpha1.PackageRef{Name: "my-pkg", Version: "2.0.0"}, "my-image",
			v1alpha1.StateInProgress, v1alpha1.StageUpgrade, int32(0), "",
		).Return(nil)
		node.EXPECT().SetStatus(v1alpha1.StatusInProgress)

		sn := &skyhookNodes{
			skyhook: wrapper.NewSkyhookWrapper(skyhook),
			nodes:   []wrapper.SkyhookNode{node},
		}

		_, err := HandleVersionChange(sn)
		g.Expect(err).To(BeNil())
	})
}
