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

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	skyhookNodesMock "github.com/NVIDIA/nodewright/operator/internal/controller/mock"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	dalMock "github.com/NVIDIA/nodewright/operator/internal/dal/mock"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	wrapperMock "github.com/NVIDIA/nodewright/operator/internal/wrapper/mock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("skyhook controller tests", func() {

	var logger = log.FromContext(ctx)

	It("should queue only pods we created", func() {

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foobar",
				Labels: map[string]string{
					fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): "foobar",
				},
			},
		}

		Expect(ownedPod().Create(event.CreateEvent{Object: pod})).To(BeTrue())
		Expect(ownedPod().Update(event.UpdateEvent{ObjectNew: pod})).To(BeTrue())

		foreign := pod.DeepCopy()
		foreign.Labels = map[string]string{"foo": "bar"}
		Expect(ownedPod().Create(event.CreateEvent{Object: foreign})).To(BeFalse())
		Expect(ownedPod().Update(event.UpdateEvent{ObjectNew: foreign})).To(BeFalse())
		Expect(ownedPod().Delete(event.DeleteEvent{Object: foreign})).To(BeFalse())

	})

	It("should not return if there are no skyhooks", func() {

		r, err := operator.Reconcile(context.TODO(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: ""}})
		Expect(err).ToNot(HaveOccurred())
		Expect(r.RequeueAfter).To(BeEquivalentTo(0))
	})

	Context("cluster state", func() {
		It("should pick the correct number of nodes by percent", func() {

			testfunc := func(percent, count, expected int) {
				skyhooks := &v1alpha1.NodeWrightList{
					Items: []v1alpha1.NodeWright{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "skyhook1",
							},
							Spec: v1alpha1.NodeWrightSpec{
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
								// 	"nodewright.nvidia.com/state": string(v1alpha1.ENABLED),
								// },
							},
						})
				}
				deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
				clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
				Expect(err).ToNot(HaveOccurred())

				for _, skyhook := range clusterState.skyhooks {
					picker := NewNodePicker(logger, opts.GetRuntimeRequiredTolerations())
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
				skyhooks := &v1alpha1.NodeWrightList{
					Items: []v1alpha1.NodeWright{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "skyhook1",
							},
							Spec: v1alpha1.NodeWrightSpec{
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
								// 	"nodewright.nvidia.com/state": string(v1alpha1.ENABLED),
								// },
							},
						})
				}

				deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
				clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
				Expect(err).ToNot(HaveOccurred())

				for _, skyhook := range clusterState.skyhooks {
					picker := NewNodePicker(logger, opts.GetRuntimeRequiredTolerations())
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
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour,
				JobTTLFailed:    24 * time.Hour,
			},
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

	Context("DrainNode", func() {
		fakeDrainClient := func(objects ...client.Object) client.WithWatch {
			scheme := runtime.NewScheme()
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

			return fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				WithIndex(&corev1.Pod{}, fieldSelectorNodeName, func(obj client.Object) []string {
					pod, ok := obj.(*corev1.Pod)
					if !ok {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()
		}

		It("should delete pods directly when disableEviction is true", func() {
			gracePeriodSeconds := int64(-1)
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "workload", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			baseClient := fakeDrainClient(pod)
			testClient := interceptor.NewClient(baseClient, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteOptions := &client.DeleteOptions{}
					deleteOptions.ApplyOptions(opts)
					if deleteOptions.GracePeriodSeconds != nil {
						gracePeriodSeconds = *deleteOptions.GracePeriodSeconds
					}
					return c.Delete(ctx, obj, opts...)
				},
			})

			r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts)
			Expect(err).ToNot(HaveOccurred())

			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-delete"},
				Spec: v1alpha1.NodeWrightSpec{
					DrainConfig: &v1alpha1.DrainConfig{
						DisableEviction: ptr(true),
						GracePeriod:     &metav1.Duration{Duration: 7 * time.Second},
					},
					Packages: v1alpha1.Packages{},
				},
			}
			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			drained, err := r.DrainNode(ctx, skyhookNode, &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeFalse())
			Expect(gracePeriodSeconds).To(Equal(int64(7)))

			deletedPod := &corev1.Pod{}
			err = testClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "workload"}, deletedPod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			drained, err = r.DrainNode(ctx, skyhookNode, &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeTrue())
		})

		It("should not report drained while an evicted pod is still terminating", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
					// The finalizer stands in for a pod that has accepted its eviction but has
					// not finished terminating, which is what drain now has to wait out.
					Finalizers: []string{"nodewright.nvidia.com/test-hold"},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "workload-rs",
							Controller: ptr(true),
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "workload", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			deleteCount := 0
			baseClient := fakeDrainClient(pod)
			testClient := interceptor.NewClient(baseClient, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteCount++
					return c.Delete(ctx, obj, opts...)
				},
			})

			r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts)
			Expect(err).ToNot(HaveOccurred())

			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-terminating"},
				Spec: v1alpha1.NodeWrightSpec{
					DrainConfig: &v1alpha1.DrainConfig{
						DisableEviction: ptr(true),
					},
					Packages: v1alpha1.Packages{},
				},
			}
			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			_package := &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			}

			drained, err := r.DrainNode(ctx, skyhookNode, _package)
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeFalse())
			Expect(deleteCount).To(Equal(1))

			terminating := &corev1.Pod{}
			Expect(testClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "workload"}, terminating)).To(Succeed())
			Expect(terminating.DeletionTimestamp).ToNot(BeNil())

			isDrained, err := r.IsDrained(ctx, skyhookNode)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrained).To(BeFalse())

			drained, err = r.DrainNode(ctx, skyhookNode, _package)
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeFalse())
			Expect(deleteCount).To(Equal(1))

			terminating.Finalizers = nil
			Expect(testClient.Update(ctx, terminating)).To(Succeed())
			Expect(apierrors.IsNotFound(testClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "workload"}, &corev1.Pod{}))).To(BeTrue())

			drained, err = r.DrainNode(ctx, skyhookNode, _package)
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeTrue())
		})

		It("should wait without deleting unmanaged pods when force is false", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "workload", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			deleteCalled := false
			evictCalled := false
			baseClient := fakeDrainClient(pod)
			testClient := interceptor.NewClient(baseClient, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteCalled = true
					return c.Delete(ctx, obj, opts...)
				},
				SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
					evictCalled = true
					return nil
				},
			})

			r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts)
			Expect(err).ToNot(HaveOccurred())

			force := false
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-block"},
				Spec: v1alpha1.NodeWrightSpec{
					DrainConfig: &v1alpha1.DrainConfig{
						Force: &force,
					},
					Packages: v1alpha1.Packages{},
				},
			}
			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			drained, err := r.DrainNode(ctx, skyhookNode, &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeFalse())
			Expect(deleteCalled).To(BeFalse())
			Expect(evictCalled).To(BeFalse())
			Expect(skyhookNode.Status()).To(Equal(v1alpha1.StatusInProgress))
		})

		It("should block before drain when podNonInterruptLabels match a running pod", func() {
			goldenPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "golden",
					Namespace: "default",
					Labels: map[string]string{
						"workload": "golden",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "golden", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			evictablePod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "evictable",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "evictable", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			deleteCalled := false
			evictCalled := false
			baseClient := fakeDrainClient(goldenPod, evictablePod)
			testClient := interceptor.NewClient(baseClient, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteCalled = true
					return c.Delete(ctx, obj, opts...)
				},
				SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
					evictCalled = true
					return nil
				},
			})

			r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts)
			Expect(err).ToNot(HaveOccurred())

			// Already cordoned in the API, so this spec exercises the podNonInterruptLabels
			// barrier rather than stopping at the earlier cordon-durability gate.
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-a",
					Annotations: map[string]string{
						fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, "drain-golden"): "true",
					},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-golden"},
				Spec: v1alpha1.NodeWrightSpec{
					PodNonInterruptLabels: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"workload": "golden",
						},
					},
					DrainConfig: &v1alpha1.DrainConfig{
						DisableEviction: ptr(true),
					},
					Packages: v1alpha1.Packages{},
				},
			}
			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(BeFalse())
			Expect(deleteCalled).To(BeFalse())
			Expect(evictCalled).To(BeFalse())
			Expect(skyhookNode.GetNode().Spec.Unschedulable).To(BeTrue())

			drainStartedAt, err := skyhookNode.DrainStartedAt()
			Expect(err).ToNot(HaveOccurred())
			Expect(drainStartedAt).To(BeNil())
		})

		It("should mark the node erroring when drain timeout expires", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "workload-rs",
							Controller: ptr(true),
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "workload", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			deleteCalled := false
			baseClient := fakeDrainClient(pod)
			testClient := interceptor.NewClient(baseClient, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteCalled = true
					return c.Delete(ctx, obj, opts...)
				},
				SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
					Fail("drain timeout should not evict pods")
					return nil
				},
			})

			r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts)
			Expect(err).ToNot(HaveOccurred())

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-a",
					Annotations: map[string]string{
						"nodewright.nvidia.com/drainStart_drain-timeout": time.Now().Add(-2 * time.Minute).Format(time.RFC3339Nano),
					},
				},
			}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-timeout"},
				Spec: v1alpha1.NodeWrightSpec{
					DrainConfig: &v1alpha1.DrainConfig{
						Timeout: &metav1.Duration{Duration: time.Second},
					},
					Packages: v1alpha1.Packages{},
				},
			}
			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			drained, err := r.DrainNode(ctx, skyhookNode, &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeFalse())
			Expect(deleteCalled).To(BeFalse())
			Expect(skyhookNode.Status()).To(Equal(v1alpha1.StatusErroring))
		})

		It("should emit drain timeout events when the node is already erroring", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workload",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:       "ReplicaSet",
							Name:       "workload-rs",
							Controller: ptr(true),
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-a",
					Containers: []corev1.Container{
						{Name: "workload", Image: "busybox"},
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			baseClient := fakeDrainClient(pod)
			testClient := interceptor.NewClient(baseClient, interceptor.Funcs{
				SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
					Fail("drain timeout should not evict pods")
					return nil
				},
			})

			recorder := events.NewFakeRecorder(10)
			r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), recorder, opts)
			Expect(err).ToNot(HaveOccurred())

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-a",
					Annotations: map[string]string{
						"nodewright.nvidia.com/drainStart_drain-timeout": time.Now().Add(-2 * time.Minute).Format(time.RFC3339Nano),
						"nodewright.nvidia.com/status_drain-timeout":     string(v1alpha1.StatusErroring),
					},
				},
			}
			skyhook := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-timeout"},
				Spec: v1alpha1.NodeWrightSpec{
					DrainConfig: &v1alpha1.DrainConfig{
						Timeout: &metav1.Duration{Duration: time.Second},
					},
					Packages: v1alpha1.Packages{},
				},
			}
			skyhookNode, err := wrapper.NewSkyhookNode(node, skyhook)
			Expect(err).ToNot(HaveOccurred())

			drained, err := r.DrainNode(ctx, skyhookNode, &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(drained).To(BeFalse())
			Expect(skyhookNode.Status()).To(Equal(v1alpha1.StatusErroring))
			Eventually(recorder.Events).Should(Receive(ContainSubstring("Warning Drain drain timed out after [1s] for node [node-a] package [pkg:1.0.0] from [nodewright:drain-timeout]")))
			Eventually(recorder.Events).Should(Receive(ContainSubstring("Warning Drain drain timed out after [1s] for node [node-a] package [pkg:1.0.0]")))
		})

		// The cordon is only an in-memory mutation until SaveNodesAndSkyhook patches it
		// at the end of the pass. Evicting before that patch lands lets the replacement
		// pod schedule straight back onto a node that is not yet unschedulable.
		Context("cordon barrier", func() {
			evictablePod := func(nodeName string) *corev1.Pod {
				return &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("workload-%s", nodeName),
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", Controller: ptr(true)},
						},
					},
					Spec: corev1.PodSpec{
						NodeName:   nodeName,
						Containers: []corev1.Container{{Name: "workload", Image: "busybox"}},
					},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				}
			}

			reconcilerRecordingEvictions := func(evicted *bool, objects ...client.Object) *SkyhookReconciler {
				testClient := interceptor.NewClient(fakeDrainClient(objects...), interceptor.Funcs{
					SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
						*evicted = true
						return nil
					},
				})
				r, err := NewSkyhookReconciler(testClient.Scheme(), testClient, testClient, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts)
				Expect(err).ToNot(HaveOccurred())
				return r
			}

			It("should not evict until the cordon is durable in the API", func() {
				evicted := false
				r := reconcilerRecordingEvictions(&evicted, evictablePod("node-a"))

				skyhook := &v1alpha1.NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "cordon-gate"},
					Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
				}
				skyhookNode, err := wrapper.NewSkyhookNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}, skyhook)
				Expect(err).ToNot(HaveOccurred())

				ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, &v1alpha1.Package{
					PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(ready).To(BeFalse())
				Expect(evicted).To(BeFalse())

				// Cordoned in memory and left dirty so the end-of-pass save persists it.
				Expect(skyhookNode.GetNode().Spec.Unschedulable).To(BeTrue())
				Expect(skyhookNode.Changed()).To(BeTrue())

				drainStartedAt, err := skyhookNode.DrainStartedAt()
				Expect(err).ToNot(HaveOccurred())
				Expect(drainStartedAt).To(BeNil())
			})

			It("should evict once the cordon is already durable in the API", func() {
				evicted := false
				r := reconcilerRecordingEvictions(&evicted, evictablePod("node-a"))

				skyhook := &v1alpha1.NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "cordon-gate"},
					Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
				}
				// A node as it comes back from the API on the pass after the cordon was saved.
				cordoned := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-a",
						Annotations: map[string]string{
							fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, "cordon-gate"): "true",
						},
					},
					Spec: corev1.NodeSpec{Unschedulable: true},
				}
				skyhookNode, err := wrapper.NewSkyhookNode(cordoned, skyhook)
				Expect(err).ToNot(HaveOccurred())

				ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, &v1alpha1.Package{
					PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(ready).To(BeFalse()) // still waiting on the evicted pod
				Expect(evicted).To(BeTrue())
			})

			// The barrier must not cost one reconcile per node: a single pass has to
			// cordon every node it knows about, so the next pass can drain them all.
			It("should cordon every node it is given in a single pass", func() {
				evicted := false
				nodeNames := []string{"node-a", "node-b", "node-c"}
				pods := make([]client.Object, 0, len(nodeNames))
				for _, name := range nodeNames {
					pods = append(pods, evictablePod(name))
				}
				r := reconcilerRecordingEvictions(&evicted, pods...)

				skyhook := &v1alpha1.NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "cordon-gate"},
					Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
				}

				for _, name := range nodeNames {
					skyhookNode, err := wrapper.NewSkyhookNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}, skyhook)
					Expect(err).ToNot(HaveOccurred())

					ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, &v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"},
					})
					Expect(err).ToNot(HaveOccurred())
					Expect(ready).To(BeFalse())
					Expect(skyhookNode.GetNode().Spec.Unschedulable).To(BeTrue(), "node %s should be cordoned in this pass", name)
				}

				Expect(evicted).To(BeFalse())
			})
		})
	})

	It("should set monotonic SKYHOOK_NODE_ORDER across nodes and batches", func() {
		now := time.Now()
		testSkyhook := wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			Status: v1alpha1.NodeWrightStatus{
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
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour,
				JobTTLFailed:    24 * time.Hour,
			},
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

		// reset to a fully valid set, then exercise the Job-related floors in isolation
		opts = SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			CopyDirRoot:          "/tmp",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour,
				JobTTLFailed:    24 * time.Hour,
			},
		}
		Expect(opts.Validate()).To(BeNil())

		// JobTTLSucceeded below the 1 minute floor
		opts.JobTTLSucceeded = 30 * time.Second
		Expect(opts.Validate()).ToNot(BeNil())
		opts.JobTTLSucceeded = time.Hour

		// JobTTLFailed below the 1 minute floor
		opts.JobTTLFailed = 0
		Expect(opts.Validate()).ToNot(BeNil())
		opts.JobTTLFailed = 24 * time.Hour

		// negative JobStageTimeout is rejected; 0 is allowed (removes the time bound)
		opts.JobStageTimeout = -time.Second
		Expect(opts.Validate()).ToNot(BeNil())
		opts.JobStageTimeout = 0
		Expect(opts.Validate()).To(BeNil())

		// negative JobBackoffLimit is rejected; 0 is allowed (a single attempt, no retry)
		opts.JobBackoffLimit = -1
		Expect(opts.Validate()).ToNot(BeNil())
		opts.JobBackoffLimit = 0
		Expect(opts.Validate()).To(BeNil())
	})
	It("Should group skyhooks by node correctly", func() {
		skyhooks := &v1alpha1.NodeWrightList{
			Items: []v1alpha1.NodeWright{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "skyhook1",
					},
					Spec: v1alpha1.NodeWrightSpec{
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
					Spec: v1alpha1.NodeWrightSpec{
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
		skyhooks := &v1alpha1.NodeWrightList{
			Items: []v1alpha1.NodeWright{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "skyhook1",
					},
					Spec: v1alpha1.NodeWrightSpec{
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
					Spec: v1alpha1.NodeWrightSpec{
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
	It("runtimeRequiredCordonAfterEnabled should return false for no skyhooks", func() {
		Expect(runtimeRequiredCordonAfterEnabled(nil)).To(BeFalse())
		Expect(runtimeRequiredCordonAfterEnabled([]SkyhookNodes{})).To(BeFalse())
	})
	It("runtimeRequiredCordonAfterEnabled should return false when no skyhook has it enabled", func() {
		sh := &skyhookNodes{skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{RuntimeRequired: true, RuntimeRequiredCordonAfter: false},
		})}
		Expect(runtimeRequiredCordonAfterEnabled([]SkyhookNodes{sh})).To(BeFalse())
	})
	It("runtimeRequiredCordonAfterEnabled should return false when RuntimeRequiredCordonAfter is true but RuntimeRequired is false", func() {
		sh := &skyhookNodes{skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{RuntimeRequired: false, RuntimeRequiredCordonAfter: true},
		})}
		Expect(runtimeRequiredCordonAfterEnabled([]SkyhookNodes{sh})).To(BeFalse())
	})
	It("runtimeRequiredCordonAfterEnabled should return true when any skyhook has it enabled", func() {
		sh1 := &skyhookNodes{skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{RuntimeRequired: true, RuntimeRequiredCordonAfter: false},
		})}
		sh2 := &skyhookNodes{skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{RuntimeRequired: true, RuntimeRequiredCordonAfter: true},
		})}
		Expect(runtimeRequiredCordonAfterEnabled([]SkyhookNodes{sh1, sh2})).To(BeTrue())
	})
	Context("HandleRuntimeRequired with runtimeRequiredCordonAfter", func() {
		newRuntimeRequiredReconciler := func() *SkyhookReconciler {
			return &SkyhookReconciler{
				Client:   k8sClient,
				uncached: k8sClient,
				dal:      dal.New(k8sClient, nil),
				recorder: operator.recorder,
				opts:     opts,
			}
		}

		It("should cordon the node and remove the taint when runtimeRequiredCordonAfter is true", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "rr-cordon-after-node",
					Labels: map[string]string{"nodewright.nvidia.com/rr-cordon-after-test": "true"},
				},
				Spec: corev1.NodeSpec{Taints: []corev1.Taint{opts.GetRuntimeRequiredTaint()}},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

			nodeWright := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "rr-cordon-after"},
				Spec: v1alpha1.NodeWrightSpec{
					RuntimeRequired:            true,
					RuntimeRequiredCordonAfter: true,
					NodeSelector:               metav1.LabelSelector{MatchLabels: map[string]string{"nodewright.nvidia.com/rr-cordon-after-test": "true"}},
				},
			}
			cs, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{nodeWright}},
				&corev1.NodeList{Items: []corev1.Node{*node}},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(newRuntimeRequiredReconciler().HandleRuntimeRequired(ctx, cs, &corev1.NodeList{Items: []corev1.Node{*node}})).To(Succeed())

			updated := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updated)).To(Succeed())
			for _, t := range updated.Spec.Taints {
				Expect(t.Key).ToNot(Equal(opts.GetRuntimeRequiredTaint().Key), "runtime-required taint should have been removed")
			}
			Expect(updated.Spec.Unschedulable).To(BeTrue())
			Expect(updated.Annotations).To(HaveKeyWithValue(v1alpha1.RuntimeRequiredCordonAnnotation, "true"))
		})

		It("should remove the taint without cordoning when runtimeRequiredCordonAfter is false", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "rr-no-cordon-node",
					Labels: map[string]string{"nodewright.nvidia.com/rr-no-cordon-test": "true"},
				},
				Spec: corev1.NodeSpec{Taints: []corev1.Taint{opts.GetRuntimeRequiredTaint()}},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

			nodeWright := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "rr-no-cordon"},
				Spec: v1alpha1.NodeWrightSpec{
					RuntimeRequired:            true,
					RuntimeRequiredCordonAfter: false,
					NodeSelector:               metav1.LabelSelector{MatchLabels: map[string]string{"nodewright.nvidia.com/rr-no-cordon-test": "true"}},
				},
			}
			cs, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{nodeWright}},
				&corev1.NodeList{Items: []corev1.Node{*node}},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(newRuntimeRequiredReconciler().HandleRuntimeRequired(ctx, cs, &corev1.NodeList{Items: []corev1.Node{*node}})).To(Succeed())

			updated := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updated)).To(Succeed())
			for _, t := range updated.Spec.Taints {
				Expect(t.Key).ToNot(Equal(opts.GetRuntimeRequiredTaint().Key), "runtime-required taint should have been removed")
			}
			Expect(updated.Spec.Unschedulable).To(BeFalse())
			Expect(updated.Annotations).ToNot(HaveKey(v1alpha1.RuntimeRequiredCordonAnnotation))
		})

		It("should not cordon a node that does not have the runtime-required taint", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "rr-no-taint-node",
					Labels: map[string]string{"nodewright.nvidia.com/rr-no-taint-test": "true"},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

			nodeWright := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "rr-no-taint"},
				Spec: v1alpha1.NodeWrightSpec{
					RuntimeRequired:            true,
					RuntimeRequiredCordonAfter: true,
					NodeSelector:               metav1.LabelSelector{MatchLabels: map[string]string{"nodewright.nvidia.com/rr-no-taint-test": "true"}},
				},
			}
			cs, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{nodeWright}},
				&corev1.NodeList{Items: []corev1.Node{*node}},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(newRuntimeRequiredReconciler().HandleRuntimeRequired(ctx, cs, &corev1.NodeList{Items: []corev1.Node{*node}})).To(Succeed())

			updated := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Unschedulable).To(BeFalse())
			Expect(updated.Annotations).ToNot(HaveKey(v1alpha1.RuntimeRequiredCordonAnnotation))
		})

		It("should remove the runtimeRequiredCordon annotation without re-cordoning when the node has been manually uncordoned", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "rr-manual-uncordon-node",
					Labels:      map[string]string{"nodewright.nvidia.com/rr-manual-uncordon-test": "true"},
					Annotations: map[string]string{v1alpha1.RuntimeRequiredCordonAnnotation: "true"},
				},
				Spec: corev1.NodeSpec{Unschedulable: false},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

			nodeWright := v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "rr-manual-uncordon"},
				Spec: v1alpha1.NodeWrightSpec{
					RuntimeRequired:            true,
					RuntimeRequiredCordonAfter: true,
					NodeSelector:               metav1.LabelSelector{MatchLabels: map[string]string{"nodewright.nvidia.com/rr-manual-uncordon-test": "true"}},
				},
			}
			cs, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{nodeWright}},
				&corev1.NodeList{Items: []corev1.Node{*node}},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(newRuntimeRequiredReconciler().HandleRuntimeRequired(ctx, cs, &corev1.NodeList{Items: []corev1.Node{*node}})).To(Succeed())

			updated := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updated)).To(Succeed())
			Expect(updated.Spec.Unschedulable).To(BeFalse(), "manual uncordon should not be undone")
			Expect(updated.Annotations).ToNot(HaveKey(v1alpha1.RuntimeRequiredCordonAnnotation))
		})
	})

	It("CreateTolerationForTaint should tolerate both the configured and the legacy taint", func() {
		tolerations := opts.GetRuntimeRequiredTolerations()

		for _, taint := range []corev1.Taint{
			{Key: "nodewright.nvidia.com", Value: "runtime-required", Effect: "NoSchedule"},
			{Key: "skyhook.nvidia.com", Value: "runtime-required", Effect: "NoSchedule"},
		} {
			Expect(CheckTaintToleration(logger, tolerations, []corev1.Taint{taint})).To(BeTrue(), "expected %s to be tolerated", taint.Key)
		}
	})

	It("An operator configured with the legacy taint does not list it twice", func() {
		legacyOpts := opts
		legacyOpts.RuntimeRequiredTaint = legacyRuntimeRequiredTaint

		Expect(legacyOpts.GetRuntimeRequiredTaints()).To(HaveLen(1))
		Expect(legacyOpts.GetRuntimeRequiredTaints()[0].Key).To(Equal("skyhook.nvidia.com"))
		Expect(legacyOpts.GetRuntimeRequiredTolerations()).To(HaveLen(1))
	})

	It("A custom configured taint is recognised alongside the legacy taint", func() {
		customOpts := opts
		customOpts.RuntimeRequiredTaint = "example.com/custom=runtime-required:NoSchedule"

		keys := make([]string, 0)
		for _, taint := range customOpts.GetRuntimeRequiredTaints() {
			keys = append(keys, taint.Key)
		}
		Expect(keys).To(ConsistOf("example.com/custom", "skyhook.nvidia.com"))
	})
	It("should generate deterministic pod names", func() {
		// Setup basic test data
		skyhook := &wrapper.Skyhook{
			NodeWright: &v1alpha1.NodeWright{
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
		skyhookCR := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				Name: "skyhook-meta",
				UID:  "uid-1234",
			},
			Spec: v1alpha1.NodeWrightSpec{
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

	Context("HandleConfigUpdates config sync gate", func() {

		buildSingleNodeState := func() (*clusterState, SkyhookNodes, v1alpha1.Package) {
			pkg := v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"},
				Image:      "ghcr.io/org/pkg1",
				ConfigMap:  map[string]string{"a.properties": "old"},
			}
			skyhooks := &v1alpha1.NodeWrightList{
				Items: []v1alpha1.NodeWright{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "config-sync"},
						Spec: v1alpha1.NodeWrightSpec{
							Packages: v1alpha1.Packages{"pkg1": pkg},
						},
					},
				},
			}
			nodes := &corev1.NodeList{Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}}}
			deploymentPolicies := &v1alpha1.DeploymentPolicyList{Items: []v1alpha1.DeploymentPolicy{}}
			clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
			Expect(err).ToNot(HaveOccurred())
			Expect(clusterState.skyhooks).To(HaveLen(1))
			return clusterState, clusterState.skyhooks[0], pkg
		}

		// When the ConfigMap diverges from spec but no node has completed the
		// package (the completedNodes == nodeCount gate is closed), the operator
		// must not silently drop the pending sync. It signals a near-term requeue
		// so the CM write is retried once the gate opens, instead of waiting out
		// the 10m MaxInterval fallback (see issue #245).
		It("signals a pending sync when the CM diverges but the gate is closed", func() {
			clusterState, skyhook, pkg := buildSingleNodeState()

			// node-a has no node state for pkg1, so IsPackageComplete is false and
			// the completedNodes gate cannot open.
			oldCM := &corev1.ConfigMap{Data: map[string]string{"a.properties": "old"}}
			newCM := &corev1.ConfigMap{Data: map[string]string{"a.properties": "new"}}

			updated, pendingSync, err := operator.HandleConfigUpdates(ctx, clusterState, skyhook, pkg, oldCM, newCM)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeFalse(), "CM write stays gated until a node completes")
			Expect(pendingSync).To(BeTrue(), "a deferred CM diff must request a requeue")
		})

		// No divergence means no pending work, so the operator must not request a
		// requeue and spin the reconcile loop.
		It("does not signal a pending sync when the CM already matches spec", func() {
			clusterState, skyhook, pkg := buildSingleNodeState()

			cm := &corev1.ConfigMap{Data: map[string]string{"a.properties": "same"}}

			updated, pendingSync, err := operator.HandleConfigUpdates(ctx, clusterState, skyhook, pkg, cm, cm.DeepCopy())
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeFalse())
			Expect(pendingSync).To(BeFalse())
		})

		// A deferred sync must not short-circuit the reconcile: package progression
		// toward the gate opening happens in processSkyhooksPerNode, which runs only
		// if validateAndUpsertSkyhookData does NOT signal an early return. Returning
		// early here would deadlock — the gate would never open, so the diff would
		// stay pending forever (the regression this guards against).
		It("does not short-circuit the reconcile when a config sync is only pending", func() {
			clusterState, skyhook, pkg := buildSingleNodeState()

			// An owned CM exists in the cluster but diverges from spec, and node-a has
			// not completed pkg1, so UpsertConfigmaps must defer the write.
			existingCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%s-%s", skyhook.GetSkyhook().Name, pkg.Name, pkg.Version),
					Namespace: opts.Namespace,
					Labels:    map[string]string{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name},
				},
				Data: map[string]string{"a.properties": "cluster-stale"},
			}
			Expect(k8sClient.Create(ctx, existingCM)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, existingCM)).To(Succeed())
			})

			// Wait for the reconciler's cached client (a separate client from
			// k8sClient) to observe the CM before asserting on it. Without this,
			// UpsertConfigmaps' r.List can race the watch and miss existingCM,
			// silently taking the "create" branch instead of HandleConfigUpdates.
			Eventually(func() error {
				return operator.Get(ctx, client.ObjectKeyFromObject(existingCM), &corev1.ConfigMap{})
			}).Should(Succeed())

			shouldReturn, pendingSync, _, err := operator.validateAndUpsertSkyhookData(ctx, skyhook, clusterState)
			Expect(err).ToNot(HaveOccurred())
			Expect(shouldReturn).To(BeFalse(), "a deferred sync must not skip processSkyhooksPerNode")
			Expect(pendingSync).To(BeTrue())
		})

		It("clamps the idle requeue to the config sync interval only when otherwise idle", func() {
			maxInterval := 10 * time.Minute

			// Active work supplies its own (shorter) result: leave it untouched even
			// when a sync is pending.
			active := &reconcile.Result{RequeueAfter: 2 * time.Second}
			Expect(reconcileResult(active, true, maxInterval)).To(Equal(*active))

			// Idle with a pending sync: retry soon instead of waiting MaxInterval.
			Expect(reconcileResult(nil, true, maxInterval)).To(Equal(reconcile.Result{RequeueAfter: configSyncRetryInterval}))

			// Idle with nothing pending: fall back to MaxInterval.
			Expect(reconcileResult(nil, false, maxInterval)).To(Equal(reconcile.Result{RequeueAfter: maxInterval}))
		})
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
			NodeWright: &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.NodeWrightSpec{
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	// Unlike its neighbours, this spec round-trips the pod through the envtest
	// apiserver instead of comparing two in-process structs. A DeepCopy can never
	// observe that apimachinery rewrites a quantity into its canonical form
	// ("4000m" -> "4", "8192Mi" -> "8Gi") when the pod is serialized, so the pod the
	// operator reads back is not byte-identical to the one it created.
	It("should match when the pod's quantities were canonicalized by the apiserver", func() {
		newPackage := *package_
		newPackage.Resources = &v1alpha1.ResourceRequirements{
			CPURequest:    resource.MustParse("2000m"),
			CPULimit:      resource.MustParse("4000m"),
			MemoryRequest: resource.MustParse("4096Mi"),
			MemoryLimit:   resource.MustParse("8192Mi"),
		}
		skyhook.Spec.Packages["test-package"] = newPackage

		// "test-node", not the "testNode" the sibling specs use: spec.nodeName must be
		// a lowercase RFC 1123 subdomain, which only a real apiserver enforces.
		pod := createPodFromPackage(operator.opts, &newPackage, skyhook, "test-node", v1alpha1.StageApply)
		Expect(SetPackages(pod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)).To(Succeed())

		// Capture what the operator built before Create: the client overwrites pod in
		// place with the apiserver's response, which is the rewrite under test.
		built := pod.Spec.InitContainers[0].Resources.DeepCopy()

		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod))).To(Succeed())
		})

		observedPod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), observedPod)).To(Succeed())
		observed := observedPod.Spec.InitContainers[0].Resources

		Expect(observed.Limits.Cpu().Cmp(*built.Limits.Cpu())).To(Equal(0),
			"guard: the apiserver must change only representation, never value")
		Expect(observed.Limits.Memory().Cmp(*built.Limits.Memory())).To(Equal(0),
			"guard: the apiserver must change only representation, never value")

		Expect(podMatchesPackage(operator.opts, &newPackage, *observedPod, skyhook, v1alpha1.StageApply)).To(BeTrue(),
			"a pod read back from the apiserver must still match the package that created it")
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
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
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(operator.opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})
})

var _ = Describe("cluster state compartments", func() {
	It("should partition nodes into compartments", func() {
		skyhooks := &v1alpha1.NodeWrightList{
			Items: []v1alpha1.NodeWright{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "skyhook-a"},
					Spec: v1alpha1.NodeWrightSpec{
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
		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
			Status: v1alpha1.NodeWrightStatus{
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
		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
			Status: v1alpha1.NodeWrightStatus{
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
		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
			Status: v1alpha1.NodeWrightStatus{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
				Packages: v1alpha1.Packages{
					// "removed-pkg" is NOT in spec
				},
			},
			Status: v1alpha1.NodeWrightStatus{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhookCR := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.NodeWrightSpec{
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

		r := &JobReconciler{dal: mockDAL}
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

		skyhookCR := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.NodeWrightSpec{
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

		r := &JobReconciler{dal: mockDAL}
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
		skyhookCR := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.NodeWrightSpec{
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

		r := &JobReconciler{dal: mockDAL}
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

		skyhookCR := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.NodeWrightSpec{
				Packages: v1alpha1.Packages{
					// my-pkg NOT in spec
				},
			},
		}

		mockDAL := dalMock.NewMockDAL(t)
		mockDAL.EXPECT().GetSkyhook(context.Background(), "test-skyhook").Return(skyhookCR, nil)

		mockNode := wrapperMock.NewMockSkyhookNodeOnly(t)
		mockNode.EXPECT().RemoveState(v1alpha1.PackageRef{Name: "my-pkg", Version: "1.0.0"}).Return(nil)

		r := &JobReconciler{dal: mockDAL}
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
		skyhook := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now, // CR is being deleted
			},
			Spec: v1alpha1.NodeWrightSpec{
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
		skyhook := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now,
			},
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
	blockedCondType := wrapper.SkyhookConditionBlocked

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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
			Status: v1alpha1.NodeWrightStatus{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
			Status: v1alpha1.NodeWrightStatus{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
	condType := wrapper.SkyhookConditionNodeStateMalformed

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
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
				Status: v1alpha1.NodeWrightStatus{
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
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{}),
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
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{}),
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
			skyhook: wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{}),
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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
		skyhook := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now,
			},
			Spec: v1alpha1.NodeWrightSpec{
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
		skyhookCR := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			Spec: v1alpha1.NodeWrightSpec{
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

		r := &JobReconciler{dal: mockDAL}
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

		skyhook := &v1alpha1.NodeWright{
			Spec: v1alpha1.NodeWrightSpec{
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

// Reproduction for the "reapply-on-reboot loses the reboot on busy nodes" bug.
//
// On a node with high churn (many controllers writing labels/annotations/status), the
// node-state reset that TrackReboots performs on reboot detection is persisted with a full,
// optimistic-concurrency r.Update(node). That Update loses the resourceVersion race and is
// rejected, but TrackReboots advances Status.NodeBootIds BEFORE (and independent of) that
// write, so the reboot is "consumed" and never retried. The node keeps its stale "complete"
// node-state annotation, and on the next reconcile derives complete -> no reapply pod.
//
// This spec drives the real envtest apiserver so the 409 is genuine (not injected). It
// asserts on the in-memory clusterState object so the running manager (which reconciles real
// CRs asynchronously) cannot race the assertion — the manager cannot touch our Go object.
var _ = Describe("TrackReboots reapply-on-reboot on a busy node", func() {

	It("must not consume the reboot when the node-state reset write loses an optimistic-concurrency race", func() {
		const (
			skyhookName = "reboot-conflict-sh"
			nodeName    = "reboot-conflict-node"
			oldBootID   = "boot-A"
			newBootID   = "boot-B"
		)
		nodeLabel := map[string]string{"reboot-conflict-test": "yes"}

		pkgRef := v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"}
		completeState := v1alpha1.NodeState{}
		completeState.Upsert(pkgRef, "ghcr.io/org/pkg1", v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")
		stateJSON, err := json.Marshal(completeState)
		Expect(err).ToNot(HaveOccurred())
		nodeStateKey := fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, skyhookName)
		cordonKey := fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, skyhookName)

		// Real Node in the apiserver carrying the old "complete" node-state annotation plus a
		// held cordon annotation. BootID lives on the status subresource (not persisted by
		// Create); we only need it on the in-memory snapshot below for reboot detection.
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: nodeLabel,
				Annotations: map[string]string{
					nodeStateKey: string(stateJSON),
					cordonKey:    "true",
				},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, node)
		})

		// Persist the post-reboot BootID to the Node status (as the kubelet would). This makes
		// TrackReboots see a reboot, and lets the reset Patch round-trip preserve the BootID so
		// the new boot id is recorded correctly.
		node.Status.NodeInfo.BootID = newBootID
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

		// The operator's in-memory snapshot, taken at this resourceVersion; the busy-node churn
		// below advances the live Node past it.
		snapshotNode := node.DeepCopy()

		// Skyhook is kept in memory only: creating it would let the running manager reconcile
		// it asynchronously. We assert on the in-memory clusterState, which the manager cannot
		// reach, so the test stays deterministic.
		skyhook := v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: nodeLabel},
				Packages: v1alpha1.Packages{
					pkgRef.Name: {PackageRef: pkgRef, Image: "ghcr.io/org/pkg1"},
				},
			},
			Status: v1alpha1.NodeWrightStatus{
				NodeBootIds: map[string]string{nodeName: oldBootID},
			},
		}

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{skyhook}},
			&corev1.NodeList{Items: []corev1.Node{*snapshotNode}},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))
		Expect(clusterState.skyhooks[0].GetNodes()).To(HaveLen(1), "node must be paired to the skyhook")

		// NOISE: simulate a busy node — a concurrent controller mutates the live Node, bumping
		// its resourceVersion. A single out-of-band write is enough: TrackReboots persists the
		// node with a full Update, which conflicts if the live object moved at all. The 409 is
		// the real apiserver's response, not an injected error, and is deterministic with no
		// sleeps or polling.
		churn := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, churn)).To(Succeed())
		churn.Labels["busy-controller"] = "wrote-here"
		Expect(k8sClient.Update(ctx, churn)).To(Succeed())

		r := &SkyhookReconciler{
			Client:   k8sClient,
			dal:      dal.New(k8sClient, nil),
			recorder: operator.recorder,
			opts:     SkyhookOperatorOptions{ReapplyOnReboot: true},
		}

		_, err = r.TrackReboots(ctx, clusterState)

		// The node-state reset must NOT fail because the live Node moved under concurrent churn.
		// A merge Patch is not resourceVersion-gated, so it lands where the old full Update lost
		// a 409. (The skyhook-status write is expected to fail here only because this test keeps
		// the Skyhook in memory; that is unrelated to the node write under test.)
		if err != nil {
			Expect(err.Error()).ToNot(ContainSubstring("node after reboot"),
				"the node-state reset write must not fail on a busy node")
		}

		// PRIMARY: the stale "complete" node-state annotation must be cleared on the apiserver,
		// so the package will be reapplied. On the buggy full-Update path the write is rejected
		// and this annotation survives, so this assertion fails.
		live := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, live)).To(Succeed())
		Expect(live.Annotations).ToNot(HaveKey(nodeStateKey),
			"node still carries stale complete state after a reboot on a busy node; reapply will never happen")

		// The held cordon annotation must also be cleared. The old Reset() built this key without
		// the Skyhook name, so it never matched what Cordon() writes and a cordon survived reset.
		Expect(live.Annotations).ToNot(HaveKey(cordonKey),
			"cordon annotation survived reset; the node stays cordoned and is never reapplied")

		// Documents the post-fix invariant, not the red-on-old discriminator: the buggy path
		// advances NodeBootIds *before* the failed write, so it also reaches newBootID here. The
		// stale-annotation assertions above are what fail on old code and pass on the fix.
		sh := clusterState.skyhooks[0].GetSkyhook()
		Expect(sh.Status.NodeBootIds[nodeName]).To(Equal(newBootID))
	})
})

// ProcessInterrupt skipped-package promotion guards the level-triggered backstop for packages
// parked at (interrupt, skipped). Such a package lost the node's single interrupt slot to a
// higher-priority interrupt (e.g. a reboot). The pod controller promotes it when that interrupt
// pod completes, but a skipped package has no pod of its own, so if that edge is missed the main
// reconcile must still converge. Once the preempting interrupt has finished and left the runnable
// set, the skipped package becomes the chosen interrupt winner (runInterrupt=true), which is the
// signal that it is safe to promote it to complete from the reconcile loop itself.
var _ = Describe("ProcessInterrupt skipped-package promotion", func() {

	newSkippedNode := func() wrapper.SkyhookNode {
		pkg := v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{Name: "baxter", Version: "3.2.1"},
			Image:      "baxter-image",
			Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.SERVICE, Services: []string{"cron"}},
		}
		skyhook := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "config-skyhook"},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"baxter": pkg}},
		}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "kind-worker"}}
		sn, err := wrapper.NewSkyhookNode(node, skyhook)
		Expect(err).NotTo(HaveOccurred())
		// Park baxter at interrupt/skipped (Upsert persists it to the annotation).
		Expect(sn.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateSkipped, v1alpha1.StageInterrupt, 0, "")).To(Succeed())
		return sn
	}

	It("promotes a skipped package once it is the interrupt winner (runInterrupt=true)", func() {
		sn := newSkippedNode()
		pkg := sn.GetSkyhook().Spec.Packages["baxter"]

		r := &SkyhookReconciler{}
		proceed, err := r.ProcessInterrupt(context.Background(), sn, &pkg, pkg.Interrupt, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(proceed).To(BeFalse())

		status, found := sn.PackageStatus("baxter|3.2.1")
		Expect(found).To(BeTrue())
		Expect(status.State).To(Equal(v1alpha1.StateComplete),
			"a skipped package that is now the interrupt winner must be promoted by the reconcile, not left for a pod event that never comes")
	})

	It("does NOT promote a skipped package while a higher-priority interrupt is still pending (runInterrupt=false)", func() {
		sn := newSkippedNode()
		pkg := sn.GetSkyhook().Spec.Packages["baxter"]

		r := &SkyhookReconciler{}
		proceed, err := r.ProcessInterrupt(context.Background(), sn, &pkg, pkg.Interrupt, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(proceed).To(BeFalse())

		status, found := sn.PackageStatus("baxter|3.2.1")
		Expect(found).To(BeTrue())
		Expect(status.State).To(Equal(v1alpha1.StateSkipped),
			"a skipped package must keep waiting while the preempting interrupt is still pending")
	})
})

var _ = Describe("TrackReboots auto-taint on reboot", func() {
	const (
		defaultRuntimeRequiredTaint = "nodewright.nvidia.com=runtime-required:NoSchedule"
		oldBootID                   = "boot-A"
		newBootID                   = "boot-B"
	)

	It("should re-apply the runtime-required taint when all three conditions are met", func() {
		const (
			skyhookName = "auto-taint-reboot-sh"
			nodeName    = "auto-taint-reboot-node"
		)
		nodeLabel := map[string]string{"auto-taint-reboot-test": "yes"}
		autoTaintAnnotationKey := fmt.Sprintf("%s/autoTaint_nodewright.nvidia.com", v1alpha1.METADATA_PREFIX)

		// Node arrives without the taint (it was removed after previous completion)
		// but retains the autoTaint annotation from the original auto-taint.
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: nodeLabel,
				Annotations: map[string]string{
					autoTaintAnnotationKey: "true",
				},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		node.Status.NodeInfo.BootID = newBootID
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		snapshotNode := node.DeepCopy()

		pkgRef := v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"}
		skyhook := v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector:      metav1.LabelSelector{MatchLabels: nodeLabel},
				RuntimeRequired:   true,
				AutoTaintNewNodes: true,
				Packages: v1alpha1.Packages{
					pkgRef.Name: {PackageRef: pkgRef, Image: "ghcr.io/org/pkg1"},
				},
			},
			Status: v1alpha1.NodeWrightStatus{
				NodeBootIds: map[string]string{nodeName: oldBootID},
			},
		}

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{skyhook}},
			&corev1.NodeList{Items: []corev1.Node{*snapshotNode}},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks[0].GetNodes()).To(HaveLen(1))

		r := &SkyhookReconciler{
			Client:   k8sClient,
			dal:      dal.New(k8sClient, nil),
			recorder: operator.recorder,
			opts: SkyhookOperatorOptions{
				ReapplyOnReboot:      true,
				RuntimeRequiredTaint: defaultRuntimeRequiredTaint,
			},
		}

		_, err = r.TrackReboots(ctx, clusterState)
		// Status().Update fails because the Skyhook is in-memory only; the node patch is what matters.
		if err != nil {
			Expect(err.Error()).ToNot(ContainSubstring("node after reboot"),
				"the node-state reset/taint write must not fail")
		}

		live := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, live)).To(Succeed())
		Expect(live.Spec.Taints).To(ContainElement(
			Equal(corev1.Taint{Key: "nodewright.nvidia.com", Value: "runtime-required", Effect: corev1.TaintEffectNoSchedule}),
		), "runtime-required taint must be re-applied after reboot when all three conditions are met")
	})

	It("should NOT re-apply the taint when AutoTaintNewNodes is false", func() {
		const (
			skyhookName = "no-auto-taint-reboot-sh"
			nodeName    = "no-auto-taint-reboot-node"
		)
		nodeLabel := map[string]string{"no-auto-taint-reboot-test": "yes"}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: nodeLabel,
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		node.Status.NodeInfo.BootID = newBootID
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		snapshotNode := node.DeepCopy()

		pkgRef := v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"}
		skyhook := v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector:      metav1.LabelSelector{MatchLabels: nodeLabel},
				RuntimeRequired:   true,
				AutoTaintNewNodes: false, // guard: disabled
				Packages: v1alpha1.Packages{
					pkgRef.Name: {PackageRef: pkgRef, Image: "ghcr.io/org/pkg1"},
				},
			},
			Status: v1alpha1.NodeWrightStatus{
				NodeBootIds: map[string]string{nodeName: oldBootID},
			},
		}

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{skyhook}},
			&corev1.NodeList{Items: []corev1.Node{*snapshotNode}},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())

		r := &SkyhookReconciler{
			Client:   k8sClient,
			dal:      dal.New(k8sClient, nil),
			recorder: operator.recorder,
			opts: SkyhookOperatorOptions{
				ReapplyOnReboot:      true,
				RuntimeRequiredTaint: defaultRuntimeRequiredTaint,
			},
		}

		_, err = r.TrackReboots(ctx, clusterState)
		if err != nil {
			Expect(err.Error()).ToNot(ContainSubstring("node after reboot"),
				"the node-state reset write must not fail")
		}

		live := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, live)).To(Succeed())
		Expect(live.Spec.Taints).ToNot(ContainElement(
			HaveField("Key", "nodewright.nvidia.com"),
		), "taint must NOT be re-applied when AutoTaintNewNodes is false")
	})

	It("should NOT stack a second taint on a node that already carries the legacy one", func() {
		const (
			skyhookName = "legacy-taint-reboot-sh"
			nodeName    = "legacy-taint-reboot-node"
		)
		nodeLabel := map[string]string{"legacy-taint-reboot-test": "yes"}
		legacyTaint := corev1.Taint{Key: "skyhook.nvidia.com", Value: "runtime-required", Effect: corev1.TaintEffectNoSchedule}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: nodeLabel,
			},
			Spec: corev1.NodeSpec{Taints: []corev1.Taint{legacyTaint}},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		node.Status.NodeInfo.BootID = newBootID
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		snapshotNode := node.DeepCopy()

		pkgRef := v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"}
		skyhook := v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector:      metav1.LabelSelector{MatchLabels: nodeLabel},
				RuntimeRequired:   true,
				AutoTaintNewNodes: true,
				Packages: v1alpha1.Packages{
					pkgRef.Name: {PackageRef: pkgRef, Image: "ghcr.io/org/pkg1"},
				},
			},
			Status: v1alpha1.NodeWrightStatus{
				NodeBootIds: map[string]string{nodeName: oldBootID},
			},
		}

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{skyhook}},
			&corev1.NodeList{Items: []corev1.Node{*snapshotNode}},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())

		r := &SkyhookReconciler{
			Client:   k8sClient,
			dal:      dal.New(k8sClient, nil),
			recorder: operator.recorder,
			opts: SkyhookOperatorOptions{
				ReapplyOnReboot:      true,
				RuntimeRequiredTaint: defaultRuntimeRequiredTaint,
			},
		}

		_, err = r.TrackReboots(ctx, clusterState)
		if err != nil {
			Expect(err.Error()).ToNot(ContainSubstring("node after reboot"))
		}

		live := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, live)).To(Succeed())
		Expect(live.Spec.Taints).To(ContainElement(Equal(legacyTaint)))
		Expect(live.Spec.Taints).ToNot(ContainElement(HaveField("Key", "nodewright.nvidia.com")),
			"a node already gated by the legacy taint must not also get the new one")
	})
})

var _ = Describe("HandleRuntimeRequired legacy taint removal", func() {
	newReconciler := func(configured string) *SkyhookReconciler {
		return &SkyhookReconciler{
			Client:   k8sClient,
			uncached: k8sClient,
			dal:      dal.New(k8sClient, nil),
			recorder: operator.recorder,
			opts: SkyhookOperatorOptions{
				RuntimeRequiredTaint: configured,
			},
		}
	}

	removeTaintsFor := func(r *SkyhookReconciler, nodeName string, nodeTaints []corev1.Taint) []corev1.Taint {
		nodeLabel := map[string]string{"runtime-required-removal-test": nodeName}
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: nodeLabel},
			Spec:       corev1.NodeSpec{Taints: nodeTaints},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		pkgRef := v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"}
		skyhook := v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName + "-sh"},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector:    metav1.LabelSelector{MatchLabels: nodeLabel},
				RuntimeRequired: true,
				Packages: v1alpha1.Packages{
					pkgRef.Name: {PackageRef: pkgRef, Image: "ghcr.io/org/pkg1"},
				},
			},
		}

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{skyhook}},
			&corev1.NodeList{Items: []corev1.Node{*node.DeepCopy()}},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())

		// Mark the package complete on the node so the taint becomes removable.
		_, nodeWrapper := clusterState.skyhooks[0].GetNode(nodeName)
		Expect(nodeWrapper).ToNot(BeNil())
		pkg := skyhook.Spec.Packages[pkgRef.Name]
		Expect(nodeWrapper.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateComplete, v1alpha1.StageConfig, int32(0), "")).To(Succeed())

		Expect(r.HandleRuntimeRequired(ctx, clusterState, &corev1.NodeList{Items: []corev1.Node{*node.DeepCopy()}})).To(Succeed())

		live := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, live)).To(Succeed())
		return live.Spec.Taints
	}

	legacyTaint := corev1.Taint{Key: "skyhook.nvidia.com", Value: "runtime-required", Effect: corev1.TaintEffectNoSchedule}
	newTaint := corev1.Taint{Key: "nodewright.nvidia.com", Value: "runtime-required", Effect: corev1.TaintEffectNoSchedule}
	unrelatedTaint := corev1.Taint{Key: "nvidia.com/gpu", Value: "present", Effect: corev1.TaintEffectNoSchedule}

	// envtest stamps its own node.kubernetes.io/not-ready taint on created Nodes, so the
	// assertions name the keys under test rather than the whole taint list.
	expectRuntimeRequiredGone := func(remaining []corev1.Taint) {
		GinkgoHelper()
		Expect(remaining).To(ContainElement(Equal(unrelatedTaint)), "unrelated taints must be preserved")
		Expect(remaining).ToNot(ContainElement(HaveField("Key", "skyhook.nvidia.com")))
		Expect(remaining).ToNot(ContainElement(HaveField("Key", "nodewright.nvidia.com")))
	}

	It("removes the legacy taint from a node the provisioner still stamps with it", func() {
		r := newReconciler("nodewright.nvidia.com=runtime-required:NoSchedule")
		expectRuntimeRequiredGone(removeTaintsFor(r, "legacy-only-node", []corev1.Taint{legacyTaint, unrelatedTaint}))
	})

	It("removes both keys when a node somehow carries both", func() {
		r := newReconciler("nodewright.nvidia.com=runtime-required:NoSchedule")
		expectRuntimeRequiredGone(removeTaintsFor(r, "both-keys-node", []corev1.Taint{legacyTaint, newTaint, unrelatedTaint}))
	})

	It("still removes the legacy taint when the operator is pinned to the legacy key", func() {
		r := newReconciler(legacyRuntimeRequiredTaint)
		expectRuntimeRequiredGone(removeTaintsFor(r, "pinned-legacy-node", []corev1.Taint{legacyTaint, unrelatedTaint}))
	})

	It("retries a conflict in place and preserves a concurrently added taint", func() {
		const nodeName = "concurrent-taint-node"
		nodeLabel := map[string]string{"runtime-required-removal-test": nodeName}
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: nodeLabel},
			Spec:       corev1.NodeSpec{Taints: []corev1.Taint{newTaint}},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		stored := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, stored)).To(Succeed())
		snapshot := stored.DeepCopy()

		pkgRef := v1alpha1.PackageRef{Name: "pkg1", Version: "1.0.0"}
		skyhook := v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName + "-sh"},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector:    metav1.LabelSelector{MatchLabels: nodeLabel},
				RuntimeRequired: true,
				Packages: v1alpha1.Packages{
					pkgRef.Name: {PackageRef: pkgRef, Image: "ghcr.io/org/pkg1"},
				},
			},
		}

		buildCompleteState := func(node *corev1.Node) *clusterState {
			state, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{skyhook}},
				&corev1.NodeList{Items: []corev1.Node{*node.DeepCopy()}},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			_, nodeWrapper := state.skyhooks[0].GetNode(nodeName)
			Expect(nodeWrapper).ToNot(BeNil())
			pkg := skyhook.Spec.Packages[pkgRef.Name]
			Expect(nodeWrapper.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateComplete, v1alpha1.StageConfig, int32(0), "")).To(Succeed())
			return state
		}

		concurrentTaint := corev1.Taint{Key: "example.com/concurrent", Effect: corev1.TaintEffectNoSchedule}
		live := stored.DeepCopy()
		live.Spec.Taints = append(live.Spec.Taints, concurrentTaint)
		Expect(k8sClient.Patch(ctx, live, client.MergeFrom(stored.DeepCopy()))).To(Succeed())

		patches := 0
		withWatchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		conflictClient := interceptor.NewClient(withWatchClient, interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				if patches == 1 {
					return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, nodeName,
						fmt.Errorf("simulated concurrent write"))
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		})
		r := &SkyhookReconciler{
			Client:   conflictClient,
			uncached: k8sClient,
			dal:      dal.New(conflictClient, nil),
			recorder: operator.recorder,
			opts: SkyhookOperatorOptions{
				RuntimeRequiredTaint: "nodewright.nvidia.com=runtime-required:NoSchedule",
			},
		}
		Expect(r.HandleRuntimeRequired(ctx, buildCompleteState(snapshot), &corev1.NodeList{Items: []corev1.Node{*snapshot}})).To(Succeed())
		Expect(patches).To(Equal(2), "the conflict must be retried within the same reconcile pass")

		fresh := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, fresh)).To(Succeed())
		Expect(fresh.Spec.Taints).ToNot(ContainElement(newTaint))
		Expect(fresh.Spec.Taints).To(ContainElement(concurrentTaint))
	})
})

var _ = Describe("HandleFinalizer merge patch", func() {
	It("preserves user-authored resource quantity formatting when adding finalizer", func() {
		const name = "finalizer-format-test"
		gvk := schema.GroupVersionKind{
			Group:   v1alpha1.GroupVersion.Group,
			Version: v1alpha1.GroupVersion.Version,
			Kind:    "NodeWright",
		}

		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		u.Object["spec"] = map[string]interface{}{
			"nodeSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					"test-finalizer": name,
				},
			},
			"packages": map[string]interface{}{
				"pkg-a": map[string]interface{}{
					"version": "1.0.0",
					"image":   "ghcr.io/org/pkg-a",
					"resources": map[string]interface{}{
						"cpuLimit":      "4000m",
						"cpuRequest":    "2000m",
						"memoryLimit":   "8192Mi",
						"memoryRequest": "4096Mi",
					},
				},
			},
		}

		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() {
			del := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: name}}
			_ = k8sClient.Delete(ctx, del)
		})

		nw := &v1alpha1.NodeWright{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
		Expect(nw.Finalizers).To(BeEmpty())

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}},
			&corev1.NodeList{},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))

		handled, err := operator.HandleFinalizer(ctx, clusterState.skyhooks[0], clusterState)
		Expect(err).ToNot(HaveOccurred())
		Expect(handled).To(BeFalse(), "HandleFinalizer returns false when adding finalizer")

		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(gvk)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, live)).To(Succeed())

		Expect(live.GetFinalizers()).To(ContainElement(SkyhookFinalizer))

		spec, ok := live.Object["spec"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		pkgs, ok := spec["packages"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		pkgA, ok := pkgs["pkg-a"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(pkgA).ToNot(HaveKey("name"), "an omitted package name must remain absent")
		res, ok := pkgA["resources"].(map[string]interface{})
		Expect(ok).To(BeTrue())

		Expect(res["cpuLimit"]).To(Equal("4000m"), "cpuLimit must not be canonicalized to '4'")
		Expect(res["cpuRequest"]).To(Equal("2000m"), "cpuRequest must not be canonicalized to '2'")
		Expect(res["memoryLimit"]).To(Equal("8192Mi"), "memoryLimit must not be canonicalized to '8Gi'")
		Expect(res["memoryRequest"]).To(Equal("4096Mi"), "memoryRequest must not be canonicalized to '4Gi'")
	})

	It("removes the finalizer via merge patch without re-writing spec and reaps the deleted object", func() {
		const name = "finalizer-delete-test"
		gvk := schema.GroupVersionKind{
			Group:   v1alpha1.GroupVersion.Group,
			Version: v1alpha1.GroupVersion.Version,
			Kind:    "NodeWright",
		}

		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		u.SetFinalizers([]string{SkyhookFinalizer})
		u.Object["spec"] = map[string]interface{}{
			"nodeSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					"test-finalizer": name,
				},
			},
			"packages": map[string]interface{}{
				"pkg-a": map[string]interface{}{
					"name":    "pkg-a",
					"version": "1.0.0",
					"image":   "ghcr.io/org/pkg-a",
				},
			},
		}

		Expect(k8sClient.Create(ctx, u)).To(Succeed())

		del := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(k8sClient.Delete(ctx, del)).To(Succeed())

		nw := &v1alpha1.NodeWright{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
		Expect(nw.DeletionTimestamp.IsZero()).To(BeFalse())

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}},
			&corev1.NodeList{},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks).To(HaveLen(1))

		handled, err := operator.HandleFinalizer(ctx, clusterState.skyhooks[0], clusterState)
		Expect(err).ToNot(HaveOccurred())
		Expect(handled).To(BeTrue(), "HandleFinalizer returns true when deletion cleanup completes")

		live := &v1alpha1.NodeWright{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: name}, live)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "object should be reaped once finalizer is removed")
	})

	It("keeps observedGeneration stable when finalizer removal is retried", func() {
		const name = "finalizer-observed-generation-test"
		const concurrentFinalizer = "example.com/concurrent-cleanup"
		const generation int64 = 7
		deletionTimestamp := metav1.Now()

		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Generation:        generation,
				DeletionTimestamp: &deletionTimestamp,
				Finalizers:        []string{SkyhookFinalizer, concurrentFinalizer},
			},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: map[string]string{"test-finalizer": name}},
				Packages: v1alpha1.Packages{
					"pkg-a": {
						PackageRef: v1alpha1.PackageRef{Name: "pkg-a", Version: "1.0.0"},
						Image:      "ghcr.io/org/pkg-a",
					},
				},
			},
			Status: v1alpha1.NodeWrightStatus{
				ObservedGeneration: generation,
				Conditions: []metav1.Condition{{
					Type:               wrapper.SkyhookConditionDeletionBlocked,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: generation,
					LastTransitionTime: metav1.Now(),
					Reason:             "TestBlocked",
					Message:            "removed when deletion can proceed",
				}},
			},
		}

		buildDeletingState := func(current *v1alpha1.NodeWright) *clusterState {
			state, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*current}},
				&corev1.NodeList{},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.skyhooks).To(HaveLen(1))
			return state
		}

		testScheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(testScheme)).To(Succeed())
		baseClient := fake.NewClientBuilder().
			WithScheme(testScheme).
			WithStatusSubresource(nw).
			WithObjects(nw).
			Build()
		patches := 0
		conflictClient := interceptor.NewClient(baseClient, interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				if patches == 1 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "nodewrights"},
						name,
						fmt.Errorf("simulated concurrent finalizer write"),
					)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		})
		r := &SkyhookReconciler{
			Client:   conflictClient,
			uncached: baseClient,
			dal:      dal.New(conflictClient, nil),
			recorder: operator.recorder,
			opts:     opts,
		}

		state := buildDeletingState(nw)
		handled, err := r.HandleFinalizer(ctx, state.skyhooks[0], state)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(handled).To(BeFalse())
		Expect(patches).To(Equal(1))

		live := &v1alpha1.NodeWright{}
		Expect(baseClient.Get(ctx, types.NamespacedName{Name: name}, live)).To(Succeed())
		Expect(live.Status.ObservedGeneration).To(Equal(live.Generation))
		Expect(live.Finalizers).To(ConsistOf(SkyhookFinalizer, concurrentFinalizer))
		Expect(live.Status.Conditions).ToNot(ContainElement(HaveField("Type", wrapper.SkyhookConditionDeletionBlocked)))

		state = buildDeletingState(live)
		handled, err = r.HandleFinalizer(ctx, state.skyhooks[0], state)
		Expect(err).ToNot(HaveOccurred())
		Expect(handled).To(BeTrue())
		Expect(patches).To(Equal(2))

		Expect(baseClient.Get(ctx, types.NamespacedName{Name: name}, live)).To(Succeed())
		Expect(live.Status.ObservedGeneration).To(Equal(live.Generation))
		Expect(live.Status.ObservedGeneration).To(Equal(generation))
		Expect(live.Finalizers).To(ConsistOf(concurrentFinalizer))
	})

	It("retries after concurrent metadata edits and preserves every finalizer", func() {
		const name = "finalizer-concurrent-test"
		const concurrentFinalizer = "example.com/concurrent-cleanup"
		gvk := schema.GroupVersionKind{
			Group:   v1alpha1.GroupVersion.Group,
			Version: v1alpha1.GroupVersion.Version,
			Kind:    "NodeWright",
		}

		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		u.Object["spec"] = map[string]interface{}{
			"nodeSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					"test-finalizer": name,
				},
			},
			"packages": map[string]interface{}{
				"pkg-a": map[string]interface{}{
					"name":    "pkg-a",
					"version": "1.0.0",
					"image":   "ghcr.io/org/pkg-a",
				},
			},
		}

		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() {
			del := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: name}}
			_ = k8sClient.Delete(ctx, del)
		})

		nw := &v1alpha1.NodeWright{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
		snapshotNW := nw.DeepCopy()

		liveEdit := nw.DeepCopy()
		liveEdit.Labels = map[string]string{"concurrent-label": "applied"}
		controllerutil.AddFinalizer(liveEdit, concurrentFinalizer)
		Expect(k8sClient.Patch(ctx, liveEdit, client.MergeFrom(nw.DeepCopy()))).To(Succeed())

		clusterState, err := BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*snapshotNW}},
			&corev1.NodeList{},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())

		handled, err := operator.HandleFinalizer(ctx, clusterState.skyhooks[0], clusterState)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(handled).To(BeFalse())

		live := &v1alpha1.NodeWright{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, live)).To(Succeed())
		Expect(live.Finalizers).To(ConsistOf(concurrentFinalizer))

		clusterState, err = BuildState(
			&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*live}},
			&corev1.NodeList{},
			&v1alpha1.DeploymentPolicyList{},
		)
		Expect(err).ToNot(HaveOccurred())

		handled, err = operator.HandleFinalizer(ctx, clusterState.skyhooks[0], clusterState)
		Expect(err).ToNot(HaveOccurred())
		Expect(handled).To(BeFalse())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, live)).To(Succeed())
		Expect(live.Finalizers).To(ContainElement(SkyhookFinalizer))
		Expect(live.Finalizers).To(ContainElement(concurrentFinalizer))
		Expect(live.Labels).To(HaveKeyWithValue("concurrent-label", "applied"))
	})
})
