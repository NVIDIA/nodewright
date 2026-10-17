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
	"fmt"
	"time"

	v1alpha1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	skyhookv1 "github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	kptr "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Lifecycle taint admission", func() {
	makeFixture := func() (*v1alpha1.NodeWright, *corev1.Node, *corev1.Node) {
		name := "review-unreachable"
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1, Finalizers: []string{SkyhookFinalizer}, Annotations: map[string]string{v1alpha1.METADATA_PREFIX + "/version": version.VERSION}},
			Spec: v1alpha1.NodeWrightSpec{
				InterruptionBudget: v1alpha1.InterruptionBudget{Count: kptr.To(1)},
				Packages:           v1alpha1.Packages{"demo": {PackageRef: v1alpha1.PackageRef{Name: "demo", Version: "1.0.0"}, Image: "example/demo"}},
			},
			Status: v1alpha1.NodeWrightStatus{NodeBootIds: map[string]string{"a-unreachable": "", "b-healthy": ""}},
		}
		dead := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "a-unreachable", Annotations: map[string]string{v1alpha1.METADATA_PREFIX + "/version_" + name: version.VERSION}},
			Spec: corev1.NodeSpec{Taints: []corev1.Taint{
				{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule},
				{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoExecute},
			}},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}},
		}
		healthy := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "b-healthy", Annotations: map[string]string{v1alpha1.METADATA_PREFIX + "/version_" + name: version.VERSION}},
			Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
		}
		return nw, dead, healthy
	}

	It("does not admit a fresh unreachable waiting node ahead of a healthy node", func() {
		nw, dead, healthy := makeFixture()
		cluster, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, &corev1.NodeList{Items: []corev1.Node{*dead, *healthy}}, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		state := cluster.skyhooks[0]
		for _, node := range state.GetNodes() {
			node.SetStatus(v1alpha1.StatusWaiting)
		}
		Expect(state.GetSkyhook().Status.NodePriority).To(BeEmpty())
		picked := NewNodePicker(testLogger, nil).SelectNodes(state)
		names := []string{}
		for _, node := range picked {
			names = append(names, node.GetNode().Name)
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "INITIAL PICK: selected=%v priority=%v\n", names, state.GetSkyhook().Status.NodePriority)
		Expect(names).To(Equal([]string{"b-healthy"}))
	})

	It("allows the healthy node to start across twelve reconciles while its peer remains unreachable", func() {
		nw, dead, healthy := makeFixture()
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(skyhookv1.AddToScheme(scheme)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nw, dead, healthy).
			WithStatusSubresource(&v1alpha1.NodeWright{}).
			WithIndex(&corev1.Pod{}, fieldSelectorNodeName, func(obj client.Object) []string {
				return []string{obj.(*corev1.Pod).Spec.NodeName}
			}).Build()
		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), events.NewFakeRecorder(100), SkyhookOperatorOptions{
			Namespace: "nodewright", CopyDirRoot: "/var/lib/skyhook", AgentLogRoot: "/var/log/skyhook",
			RuntimeRequiredTaint: "nodewright.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/nodewright/agent:1.2.3", PauseImage: "registry.k8s.io/pause:3.10",
			MaxInterval:        10 * time.Minute,
			JobOperatorOptions: JobOperatorOptions{JobTTLSucceeded: time.Hour, JobTTLFailed: 24 * time.Hour, JobStageTimeout: time.Hour, JobBackoffLimit: 3},
		})
		Expect(err).NotTo(HaveOccurred())
		for pass := range 12 {
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nw.Name}})
			Expect(err).NotTo(HaveOccurred())
			live := &v1alpha1.NodeWright{}
			Expect(c.Get(ctx, types.NamespacedName{Name: nw.Name}, live)).To(Succeed())
			jobs := &batchv1.JobList{}
			Expect(c.List(ctx, jobs)).To(Succeed())
			nodesWithJobs := []string{}
			for _, job := range jobs.Items {
				nodesWithJobs = append(nodesWithJobs, job.Spec.Template.Spec.NodeName)
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "PASS %d: jobs=%v statuses=%v priority=%v requeue=%v\n", pass, nodesWithJobs, live.Status.NodeStatus, live.Status.NodePriority, result.RequeueAfter)
		}
		jobs := &batchv1.JobList{}
		Expect(c.List(ctx, jobs)).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		job := jobs.Items[0]
		_, _ = fmt.Fprintf(GinkgoWriter, "FINAL JOB: node=%s jobDeadline=%v podDeadline=%v\n", job.Spec.Template.Spec.NodeName, job.Spec.ActiveDeadlineSeconds, *job.Spec.Template.Spec.ActiveDeadlineSeconds)
		Expect(job.Spec.ActiveDeadlineSeconds).To(BeNil())
		Expect(job.Spec.Template.Spec.ActiveDeadlineSeconds).To(Equal(kptr.To(int64(3600))))
		Expect(job.Spec.Template.Spec.NodeName).To(Equal("b-healthy"))
	})
})

var _ = Describe("Explicit lifecycle eviction timeout", func() {
	for _, interrupt := range []bool{false, true} {
		for _, key := range []string{corev1.TaintNodeNotReady, corev1.TaintNodeUnreachable} {
			It(fmt.Sprintf("preserves the explicit 60-second %s timeout with interrupt=%t", key, interrupt), func() {
				skyhook := wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "review-timeout", UID: "review-timeout", Generation: 1},
					Spec: v1alpha1.NodeWrightSpec{AdditionalTolerations: []corev1.Toleration{{
						Key: key, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: kptr.To(int64(60)),
					}}},
				})
				opts := SkyhookOperatorOptions{Namespace: "nodewright", CopyDirRoot: "/var/lib/skyhook", AgentLogRoot: "/var/log/skyhook", RuntimeRequiredTaint: "nodewright.nvidia.com=runtime-required:NoSchedule", AgentImage: "ghcr.io/nvidia/nodewright/agent:1.2.3", PauseImage: "registry.k8s.io/pause:3.10", JobOperatorOptions: JobOperatorOptions{JobStageTimeout: time.Hour, JobBackoffLimit: 3}}
				pkg := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "demo", Version: "1.0.0"}, Image: "example/demo"}
				var job *batchv1.Job
				if interrupt {
					job = createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, "worker", v1alpha1.StageInterrupt)
				} else {
					job = createJobFromPackage(opts, pkg, skyhook, "worker", v1alpha1.StageApply)
				}
				taint := corev1.Taint{Key: key, Effect: corev1.TaintEffectNoExecute}
				var firstMatch *corev1.Toleration
				for i := range job.Spec.Template.Spec.Tolerations {
					toleration := &job.Spec.Template.Spec.Tolerations[i]
					if toleration.ToleratesTaint(testLogger, &taint, false) {
						firstMatch = toleration
						break
					}
				}
				Expect(firstMatch).NotTo(BeNil())
				_, _ = fmt.Fprintf(GinkgoWriter, "FIRST MATCH: interrupt=%t key=%s effect=%s seconds=%v tolerations=%v\n", interrupt, key, firstMatch.Effect, firstMatch.TolerationSeconds, job.Spec.Template.Spec.Tolerations)
				Expect(firstMatch.TolerationSeconds).To(Equal(kptr.To(int64(60))))
			})
		}
	}
})

var _ = Describe("Lifecycle taints and current batch membership", func() {
	DescribeTable("keeps admission and introspection consistent", func(status v1alpha1.Status, picked, custom, ignored, explicit, want bool) {
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "membership"},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"demo": {PackageRef: v1alpha1.PackageRef{Name: "demo", Version: "1.0.0"}, Image: "example/demo"}}},
		}
		if picked {
			nw.Status.NodePriority = map[string]metav1.Time{"worker": metav1.Now()}
		}
		if explicit {
			nw.Spec.AdditionalTolerations = []corev1.Toleration{{Key: corev1.TaintNodeUnreachable, Operator: corev1.TolerationOpExists}}
		}
		n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{
			{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule},
			{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoExecute},
		}}}
		if custom {
			n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "example.com/maintenance", Effect: corev1.TaintEffectNoSchedule})
		}
		if ignored {
			n.Labels = map[string]string{v1alpha1.METADATA_PREFIX + "/ignore": "true"}
		}
		cluster, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, &corev1.NodeList{Items: []corev1.Node{n}}, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		state := cluster.skyhooks[0]
		node := state.GetNodes()[0]
		node.SetStatus(status)
		IntrospectNode(node, state, cluster.skyhooks, nil, testLogger)
		Expect(node.Status() == v1alpha1.StatusBlocked).To(Equal(!want))
		selected := NewNodePicker(testLogger, nil).SelectNodes(state)
		Expect(len(selected) == 1).To(Equal(want))
	},
		Entry("fresh unreachable node", v1alpha1.StatusWaiting, false, false, false, false, false),
		Entry("in-progress recovery without priority", v1alpha1.StatusInProgress, false, false, false, false, true),
		Entry("persisted waiting batch after restart", v1alpha1.StatusWaiting, true, false, false, false, true),
		Entry("persisted paused batch on resume", v1alpha1.StatusPaused, true, false, false, false, true),
		Entry("custom taint still blocks batch", v1alpha1.StatusInProgress, true, true, false, false, false),
		Entry("ignore label still blocks batch", v1alpha1.StatusInProgress, true, false, true, false, false),
		Entry("explicit admission opt-in", v1alpha1.StatusWaiting, false, false, false, true, true),
	)
})
