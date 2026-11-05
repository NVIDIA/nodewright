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

package drain

import (
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func boolPtr(value bool) *bool {
	return &value
}

func durationPtr(value time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: value}
}

func int64Ptr(value int64) *int64 {
	return &value
}

var _ = Describe("DecidePod", func() {
	controller := true
	const daemonSetKind = "DaemonSet"
	terminatingTimestamp := metav1.NewTime(time.Date(2026, time.June, 2, 12, 0, 0, 0, time.UTC))

	basePod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workload",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       "ReplicaSet",
						Name:       "workload-rs",
						Controller: &controller,
					},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}

	It("should preserve the current default behavior", func() {
		pod := basePod()

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionEvict, Reason: ReasonEviction}))
		Expect(decision.BlocksDrain()).To(BeTrue())
		Expect(decision.RequiresAction()).To(BeTrue())
	})

	It("should delete directly when eviction is disabled", func() {
		pod := basePod()
		options := DefaultOptions()
		options.DisableEviction = true

		decision := DecidePod(pod, options)

		Expect(decision).To(Equal(Decision{Action: ActionDelete, Reason: ReasonDelete}))
		Expect(decision.BlocksDrain()).To(BeTrue())
		Expect(decision.RequiresAction()).To(BeTrue())
	})

	It("should ignore pods that are not running or pending", func() {
		pod := basePod()
		pod.Status.Phase = corev1.PodSucceeded

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionIgnore, Reason: ReasonPhase}))
		Expect(decision.BlocksDrain()).To(BeFalse())
	})

	It("should block on evictable pods that are already terminating", func() {
		pod := basePod()
		pod.DeletionTimestamp = &terminatingTimestamp

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionBlock, Reason: ReasonTerminating}))
		Expect(decision.BlocksDrain()).To(BeTrue())
		Expect(decision.RequiresAction()).To(BeFalse())
	})

	It("should report terminating ahead of the other blocking reasons", func() {
		pod := basePod()
		pod.DeletionTimestamp = &terminatingTimestamp
		pod.OwnerReferences = nil
		options := DefaultOptions()
		options.Force = false

		decision := DecidePod(pod, options)

		Expect(decision).To(Equal(Decision{Action: ActionBlock, Reason: ReasonTerminating}))
	})

	DescribeTable("should keep exempt pods ignored while they terminate",
		func(mutate func(*corev1.Pod), expectedDecision Decision) {
			pod := basePod()
			pod.DeletionTimestamp = &terminatingTimestamp
			mutate(pod)
			options := DefaultOptions()
			options.PackageNamespace = "skyhook"

			decision := DecidePod(pod, options)

			Expect(decision).To(Equal(expectedDecision))
			Expect(decision.BlocksDrain()).To(BeFalse())
		},
		Entry("finished pods",
			func(pod *corev1.Pod) { pod.Status.Phase = corev1.PodSucceeded },
			Decision{Action: ActionIgnore, Reason: ReasonPhase},
		),
		Entry("the operator's own package pods",
			func(pod *corev1.Pod) {
				pod.Namespace = "skyhook"
				pod.Labels = map[string]string{
					"nodewright.nvidia.com/name":    "my-skyhook",
					"nodewright.nvidia.com/package": "pkg-1.0.0",
				}
			},
			Decision{Action: ActionIgnore, Reason: ReasonSkyhookPackage},
		),
		Entry("pods tolerating the unschedulable taint",
			func(pod *corev1.Pod) {
				pod.Spec.Tolerations = []corev1.Toleration{{Key: corev1.TaintNodeUnschedulable}}
			},
			Decision{Action: ActionIgnore, Reason: ReasonUnschedulableToleration},
		),
		Entry("daemonset pods",
			func(pod *corev1.Pod) { pod.OwnerReferences[0].Kind = daemonSetKind },
			Decision{Action: ActionIgnore, Reason: ReasonDaemonSet},
		),
		Entry("mirror pods",
			func(pod *corev1.Pod) {
				pod.Annotations = map[string]string{MirrorPodAnnotationKey: "mirror-id"}
			},
			Decision{Action: ActionIgnore, Reason: ReasonMirrorPod},
		),
	)

	It("should never drain pods that tolerate the unschedulable taint", func() {
		pod := basePod()
		pod.Spec.Tolerations = []corev1.Toleration{
			{Key: corev1.TaintNodeUnschedulable},
		}

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionIgnore, Reason: ReasonUnschedulableToleration}))
		Expect(decision.BlocksDrain()).To(BeFalse())
	})

	DescribeTable("should apply Kubernetes toleration matching for the unschedulable taint",
		func(toleration corev1.Toleration, expectedDecision Decision) {
			pod := basePod()
			pod.Spec.Tolerations = []corev1.Toleration{toleration}

			decision := DecidePod(pod, DefaultOptions())

			Expect(decision).To(Equal(expectedDecision))
		},
		Entry("matches Exists on the NoSchedule unschedulable taint",
			corev1.Toleration{
				Key:      corev1.TaintNodeUnschedulable,
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
			Decision{Action: ActionIgnore, Reason: ReasonUnschedulableToleration},
		),
		Entry("does not match a different effect",
			corev1.Toleration{
				Key:      corev1.TaintNodeUnschedulable,
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			},
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
		Entry("does not match a different value with Equal",
			corev1.Toleration{
				Key:      corev1.TaintNodeUnschedulable,
				Operator: corev1.TolerationOpEqual,
				Value:    "true",
				Effect:   corev1.TaintEffectNoSchedule,
			},
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
	)

	DescribeTable("should exempt the operator's own package pods by label even when admission strips tolerations",
		func(podNamespace string, labels map[string]string, packageNamespace string, expectedDecision Decision) {
			pod := basePod()
			pod.Namespace = podNamespace
			pod.Labels = labels
			options := DefaultOptions()
			options.PackageNamespace = packageNamespace

			decision := DecidePod(pod, options)

			Expect(decision).To(Equal(expectedDecision))
		},
		Entry("ignores pods with both skyhook package labels in the operator namespace",
			"skyhook",
			map[string]string{
				"nodewright.nvidia.com/name":    "my-skyhook",
				"nodewright.nvidia.com/package": "pkg-1.0.0",
			},
			"skyhook",
			Decision{Action: ActionIgnore, Reason: ReasonSkyhookPackage},
		),
		Entry("ignores interrupt pods carrying the package labels",
			"skyhook",
			map[string]string{
				"nodewright.nvidia.com/name":      "my-skyhook",
				"nodewright.nvidia.com/package":   "pkg-1.0.0",
				"nodewright.nvidia.com/interrupt": "True",
			},
			"skyhook",
			Decision{Action: ActionIgnore, Reason: ReasonSkyhookPackage},
		),
		Entry("still drains pods with only the name label",
			"skyhook",
			map[string]string{"nodewright.nvidia.com/name": "my-skyhook"},
			"skyhook",
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
		Entry("still drains pods with only the package label",
			"skyhook",
			map[string]string{"nodewright.nvidia.com/package": "pkg-1.0.0"},
			"skyhook",
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
		Entry("still drains pods carrying both labels outside the operator namespace",
			"default",
			map[string]string{
				"nodewright.nvidia.com/name":    "my-skyhook",
				"nodewright.nvidia.com/package": "pkg-1.0.0",
			},
			"skyhook",
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
		Entry("does not exempt by label when no package namespace is configured",
			"skyhook",
			map[string]string{
				"nodewright.nvidia.com/name":    "my-skyhook",
				"nodewright.nvidia.com/package": "pkg-1.0.0",
			},
			"",
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
	)

	It("should ignore daemonset pods by default", func() {
		pod := basePod()
		pod.OwnerReferences[0].Kind = daemonSetKind

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionIgnore, Reason: ReasonDaemonSet}))
		Expect(decision.BlocksDrain()).To(BeFalse())
	})

	It("should drain daemonset pods when ignoreDaemonSets is false", func() {
		pod := basePod()
		pod.OwnerReferences[0].Kind = daemonSetKind
		options := DefaultOptions()
		options.IgnoreDaemonSets = false

		decision := DecidePod(pod, options)

		Expect(decision).To(Equal(Decision{Action: ActionEvict, Reason: ReasonEviction}))
	})

	DescribeTable("should treat kube-system pods like pods in any other namespace",
		func(mutate func(*corev1.Pod, *Options), expectedDecision Decision) {
			pod := basePod()
			pod.Namespace = "kube-system"
			options := DefaultOptions()
			mutate(pod, &options)

			decision := DecidePod(pod, options)

			Expect(decision).To(Equal(expectedDecision))
		},
		Entry("evicts controller-managed pods such as CoreDNS",
			func(*corev1.Pod, *Options) {},
			Decision{Action: ActionEvict, Reason: ReasonEviction},
		),
		Entry("deletes controller-managed pods when eviction is disabled",
			func(_ *corev1.Pod, options *Options) { options.DisableEviction = true },
			Decision{Action: ActionDelete, Reason: ReasonDelete},
		),
		Entry("waits on controller-managed pods that are terminating",
			func(pod *corev1.Pod, _ *Options) { pod.DeletionTimestamp = &terminatingTimestamp },
			Decision{Action: ActionBlock, Reason: ReasonTerminating},
		),
		Entry("ignores daemonset pods",
			func(pod *corev1.Pod, _ *Options) { pod.OwnerReferences[0].Kind = daemonSetKind },
			Decision{Action: ActionIgnore, Reason: ReasonDaemonSet},
		),
		Entry("ignores mirror pods",
			func(pod *corev1.Pod, _ *Options) {
				pod.OwnerReferences[0].Kind = "Node"
				pod.Annotations = map[string]string{MirrorPodAnnotationKey: "mirror-id"}
			},
			Decision{Action: ActionIgnore, Reason: ReasonMirrorPod},
		),
		Entry("ignores pods tolerating the unschedulable taint",
			func(pod *corev1.Pod, _ *Options) {
				pod.Spec.Tolerations = []corev1.Toleration{{Key: corev1.TaintNodeUnschedulable}}
			},
			Decision{Action: ActionIgnore, Reason: ReasonUnschedulableToleration},
		),
	)

	It("should ignore mirror pods", func() {
		pod := basePod()
		pod.Annotations = map[string]string{
			MirrorPodAnnotationKey: "mirror-id",
		}

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionIgnore, Reason: ReasonMirrorPod}))
		Expect(decision.BlocksDrain()).To(BeFalse())
	})

	It("should evict unmanaged pods by default", func() {
		pod := basePod()
		pod.OwnerReferences = nil

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionEvict, Reason: ReasonEviction}))
	})

	It("should block on unmanaged pods when force is false", func() {
		pod := basePod()
		pod.OwnerReferences = nil
		options := DefaultOptions()
		options.Force = false

		decision := DecidePod(pod, options)

		Expect(decision).To(Equal(Decision{Action: ActionBlock, Reason: ReasonUnmanaged}))
		Expect(decision.BlocksDrain()).To(BeTrue())
		Expect(decision.RequiresAction()).To(BeFalse())
	})

	It("should evict emptyDir pods by default", func() {
		pod := basePod()
		pod.Spec.Volumes = []corev1.Volume{
			{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		}

		decision := DecidePod(pod, DefaultOptions())

		Expect(decision).To(Equal(Decision{Action: ActionEvict, Reason: ReasonEviction}))
	})

	It("should block on emptyDir pods when deleteEmptyDirData is false", func() {
		pod := basePod()
		pod.Spec.Volumes = []corev1.Volume{
			{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		}
		options := DefaultOptions()
		options.DeleteEmptyDirData = false

		decision := DecidePod(pod, options)

		Expect(decision).To(Equal(Decision{Action: ActionBlock, Reason: ReasonEmptyDir}))
		Expect(decision.BlocksDrain()).To(BeTrue())
		Expect(decision.RequiresAction()).To(BeFalse())
	})
})

var _ = Describe("OptionsFromConfig", func() {
	It("should return defaults for nil config", func() {
		options := OptionsFromConfig(nil)

		Expect(options).To(Equal(DefaultOptions()))
	})

	It("should apply configured field overrides", func() {
		options := OptionsFromConfig(&v1alpha1.DrainConfig{
			DisableEviction:    boolPtr(true),
			DeleteEmptyDirData: boolPtr(false),
			Force:              boolPtr(false),
			IgnoreDaemonSets:   boolPtr(false),
			GracePeriod:        durationPtr(2 * time.Second),
		})

		Expect(options.DisableEviction).To(BeTrue())
		Expect(options.DeleteEmptyDirData).To(BeFalse())
		Expect(options.Force).To(BeFalse())
		Expect(options.IgnoreDaemonSets).To(BeFalse())
		Expect(options.GracePeriodSeconds).To(Equal(int64Ptr(2)))
	})

	DescribeTable("should round grace period up to whole seconds",
		func(gracePeriod time.Duration, expectedSeconds int64) {
			options := OptionsFromConfig(&v1alpha1.DrainConfig{
				GracePeriod: durationPtr(gracePeriod),
			})

			Expect(options.GracePeriodSeconds).To(Equal(int64Ptr(expectedSeconds)))
		},
		Entry("zero seconds", 0*time.Second, int64(0)),
		Entry("sub-second", 500*time.Millisecond, int64(1)),
		Entry("whole second", 1*time.Second, int64(1)),
		Entry("partial second", 1500*time.Millisecond, int64(2)),
	)
})

var _ = Describe("TimedOut", func() {
	start := metav1.NewTime(time.Date(2026, time.June, 5, 12, 0, 0, 0, time.UTC))
	timeout := metav1.Duration{Duration: 5 * time.Second}
	zeroTimeout := metav1.Duration{}

	DescribeTable("should evaluate timeout boundaries",
		func(startedAt *metav1.Time, timeout *metav1.Duration, now time.Time, expected bool) {
			Expect(TimedOut(startedAt, timeout, now)).To(Equal(expected))
		},
		Entry("nil start", nil, &timeout, start.Add(6*time.Second), false),
		Entry("nil timeout", &start, nil, start.Add(6*time.Second), false),
		Entry("zero timeout", &start, &zeroTimeout, start.Add(6*time.Second), false),
		Entry("just before deadline", &start, &timeout, start.Add(5*time.Second-time.Nanosecond), false),
		Entry("exactly at deadline", &start, &timeout, start.Add(5*time.Second), true),
		Entry("after deadline", &start, &timeout, start.Add(6*time.Second), true),
	)
})

var _ = Describe("delete options", func() {
	It("should omit delete options when grace period is unset", func() {
		options := DefaultOptions()

		Expect(options.DeleteOptions()).To(BeNil())
		Expect(options.EvictionDeleteOptions()).To(BeNil())
	})

	It("should carry configured grace period into delete options", func() {
		options := Options{GracePeriodSeconds: int64Ptr(7)}

		deleteOptions := options.DeleteOptions()
		Expect(deleteOptions).To(HaveLen(1))

		applied := &client.DeleteOptions{}
		for _, option := range deleteOptions {
			option.ApplyToDelete(applied)
		}
		Expect(applied.GracePeriodSeconds).To(Equal(int64Ptr(7)))

		evictionDeleteOptions := options.EvictionDeleteOptions()
		Expect(evictionDeleteOptions).NotTo(BeNil())
		Expect(evictionDeleteOptions.GracePeriodSeconds).To(Equal(int64Ptr(7)))
	})
})
