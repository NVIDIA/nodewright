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
		Expect(names).To(Equal([]string{"b-healthy"}))
	})

	DescribeTable("retains an erroring interrupt slot across repeated selection and reconstruction", func(status v1alpha1.Status) {
		nw, rebooting, healthy := makeFixture()
		lifecycleTaints := rebooting.Spec.Taints
		rebooting.Spec.Taints = nil
		pkg := nw.Spec.Packages["demo"]
		pkg.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
		nw.Spec.Packages["demo"] = pkg
		cluster, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, &corev1.NodeList{Items: []corev1.Node{*rebooting, *healthy}}, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		state := cluster.skyhooks[0]
		for _, node := range state.GetNodes() {
			node.SetStatus(v1alpha1.StatusWaiting)
		}
		picked := NewNodePicker(testLogger, nil).SelectNodes(state)
		Expect(picked).To(HaveLen(1))
		Expect(picked[0].GetNode().Name).To(Equal(rebooting.Name))
		Expect(picked[0].Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateInProgress, v1alpha1.StageInterrupt, 0, "")).To(Succeed())
		picked[0].SetStatus(status)
		Expect(picked[0].Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateErroring, v1alpha1.StageInterrupt, 1, "")).To(Succeed())
		picked[0].GetNode().Spec.Taints = lifecycleTaints

		for range 12 {
			nodes := &corev1.NodeList{}
			for _, node := range state.GetNodes() {
				nodes.Items = append(nodes.Items, *node.GetNode().DeepCopy())
			}
			cluster, err = BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*state.GetSkyhook().NodeWright.DeepCopy()}}, nodes, &v1alpha1.DeploymentPolicyList{})
			Expect(err).NotTo(HaveOccurred())
			state = cluster.skyhooks[0]
			IntrospectSkyhook(state, cluster.skyhooks, nil, testLogger)
			picked = NewNodePicker(testLogger, nil).SelectNodes(state)
			Expect(picked).To(HaveLen(1))
			Expect(picked[0].GetNode().Name).To(Equal(rebooting.Name))
			Expect(picked[0].Status()).NotTo(Equal(v1alpha1.StatusBlocked))
			Expect(state.GetSkyhook().Status.NodePriority).To(HaveLen(1))
			Expect(state.GetSkyhook().Status.NodePriority).NotTo(HaveKey(healthy.Name))
			condition := findSkyhookStatusCondition(state.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionTaintNotTolerable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			_, peer := state.GetNode(healthy.Name)
			Expect(peer.Status()).To(Equal(v1alpha1.StatusWaiting))
			peerState, err := peer.State()
			Expect(err).NotTo(HaveOccurred())
			Expect(peerState).To(BeEmpty())
		}
	}, Entry("with node status in progress", v1alpha1.StatusInProgress), Entry("with persisted waiting membership", v1alpha1.StatusWaiting))

	// #869: a reboot interrupt that never comes back. The Job hits its deadline, the entry sits
	// at (interrupt, erroring), and until the slot is released the node holds the budget of 1
	// until someone deletes it. Parking it lets the healthy peer start and the lifecycle taints
	// block the dead node like any other down node.
	It("releases the slot of an unreachable node whose interrupt timed out", func() {
		nw, dead, healthy := makeFixture()
		pkg := nw.Spec.Packages["demo"]
		pkg.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
		nw.Spec.Packages["demo"] = pkg
		nw.Status.NodePriority = map[string]metav1.Time{dead.Name: metav1.NewTime(time.Unix(123, 0))}
		nw.Status.CompartmentStatuses = map[string]v1alpha1.CompartmentStatus{v1alpha1.DefaultCompartmentName: {Matched: 2, BatchState: &v1alpha1.BatchProcessingState{CurrentBatch: 1}}}
		cluster, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, &corev1.NodeList{Items: []corev1.Node{*dead, *healthy}}, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		state := cluster.skyhooks[0]
		_, down := state.GetNode(dead.Name)
		_, peer := state.GetNode(healthy.Name)
		peer.SetStatus(v1alpha1.StatusWaiting)
		Expect(down.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateErroring, v1alpha1.StageInterrupt, 1, "")).To(Succeed())
		down.SetStatus(v1alpha1.StatusErroring)

		// While the Job retries, #870 holds the slot: nothing else starts.
		IntrospectSkyhook(state, cluster.skyhooks, nil, testLogger)
		Expect(NewNodePicker(testLogger, nil).SelectNodes(state)).To(ConsistOf(down))
		Expect(down.Status()).To(Equal(v1alpha1.StatusErroring))

		// The Job gave up: the sweep parks the node.
		picker := NewNodePicker(testLogger, nil)
		picker.Park(nw.Name, []string{dead.Name})
		Expect(picker.SelectNodes(state)).To(ConsistOf(peer))
		Expect(state.GetSkyhook().Status.NodePriority).To(HaveKey(healthy.Name))
		Expect(state.GetSkyhook().Status.NodePriority).NotTo(HaveKey(dead.Name))
		Expect(state.GetSkyhook().Status.NodeOrderOffset).To(Equal(1))
		Expect(down.Status()).To(Equal(v1alpha1.StatusBlocked), "without a slot the lifecycle taints apply")

		// Still parked on later passes, with the peer in progress: no churn on the released entry.
		peer.SetStatus(v1alpha1.StatusInProgress)
		for range 3 {
			picker := NewNodePicker(testLogger, nil)
			picker.Park(nw.Name, []string{dead.Name})
			Expect(picker.SelectNodes(state)).To(ConsistOf(peer))
			Expect(state.GetSkyhook().Status.NodeOrderOffset).To(Equal(1))
		}

		// The peer completes. The released node reads Blocked, but its failure still scores, so a
		// failureThreshold of 1 would stop here as it did while the node held its slot.
		Expect(peer.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateComplete, v1alpha1.StagePostInterrupt, 0, "")).To(Succeed())
		peer.SetStatus(v1alpha1.StatusComplete)
		IntrospectSkyhook(state, cluster.skyhooks, nil, testLogger)
		Expect(down.Status()).To(Equal(v1alpha1.StatusBlocked))
		batch := state.GetCompartments()[v1alpha1.DefaultCompartmentName].GetBatchState()
		Expect(batch.FailedNodes).To(Equal(1))
		Expect(batch.CompletedNodes).To(Equal(1))
	})

	It("forms the next batch across reconciles once an unreachable node's Job has given up", func() {
		nw, dead, healthy := makeFixture()
		pkg := nw.Spec.Packages["demo"]
		pkg.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
		nw.Spec.Packages["demo"] = pkg
		nw.Status.NodePriority = map[string]metav1.Time{dead.Name: metav1.NewTime(time.Unix(123, 0))}
		sn, err := wrapper.NewSkyhookNodeOnly(dead, nw.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(sn.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")).To(Succeed())
		Expect(sn.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateErroring, v1alpha1.StageInterrupt, 1, "")).To(Succeed())
		sn.SetStatus(v1alpha1.StatusErroring)
		opts := SkyhookOperatorOptions{
			Namespace: "nodewright", CopyDirRoot: "/var/lib/skyhook", AgentLogRoot: "/var/log/skyhook",
			RuntimeRequiredTaint: "nodewright.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/nodewright/agent:1.2.3", PauseImage: "registry.k8s.io/pause:3.10",
			MaxInterval:        10 * time.Minute,
			JobOperatorOptions: JobOperatorOptions{JobTTLSucceeded: time.Hour, JobTTLFailed: 24 * time.Hour, JobStageTimeout: time.Hour, JobBackoffLimit: 3},
		}
		args, err := pkg.Interrupt.ToArgs()
		Expect(err).NotTo(HaveOccurred())
		timedOut := createInterruptJobFromPackage(opts, pkg.Interrupt, args, &pkg, wrapper.NewSkyhookWrapper(nw), dead.Name, v1alpha1.StageInterrupt)
		Expect(setJobPackage(timedOut, nw, pkg.Image, v1alpha1.StageInterrupt, &pkg)).To(Succeed())
		timedOut.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded}}
		timedOut.Annotations[annotationStateRecorded] = annotationValueTrue

		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(skyhookv1.AddToScheme(scheme)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nw, dead, healthy, timedOut).
			WithStatusSubresource(&v1alpha1.NodeWright{}).
			WithIndex(&corev1.Pod{}, fieldSelectorNodeName, func(obj client.Object) []string {
				return []string{obj.(*corev1.Pod).Spec.NodeName}
			}).Build()
		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), events.NewFakeRecorder(100), opts)
		Expect(err).NotTo(HaveOccurred())
		for range 6 {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nw.Name}})
			Expect(err).NotTo(HaveOccurred())
		}

		jobs := &batchv1.JobList{}
		Expect(c.List(ctx, jobs)).To(Succeed())
		byNode := map[string]int{}
		for _, job := range jobs.Items {
			byNode[job.Spec.Template.Spec.NodeName]++
		}
		Expect(byNode).To(Equal(map[string]int{dead.Name: 1, healthy.Name: 1}), "the timed-out Job stays as the marker and the peer starts")

		got := &v1alpha1.NodeWright{}
		Expect(c.Get(ctx, types.NamespacedName{Name: nw.Name}, got)).To(Succeed())
		Expect(got.Status.NodePriority).To(HaveKey(healthy.Name))
		Expect(got.Status.NodePriority).NotTo(HaveKey(dead.Name))
		Expect(got.Status.NodeOrderOffset).To(Equal(1))
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
		for range 12 {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: nw.Name}})
			Expect(err).NotTo(HaveOccurred())
		}
		jobs := &batchv1.JobList{}
		Expect(c.List(ctx, jobs)).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		Expect(jobs.Items[0].Spec.Template.Spec.NodeName).To(Equal("b-healthy"))
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
				Expect(firstMatch.TolerationSeconds).To(Equal(kptr.To(int64(60))))
			})
		}
	}
})

var _ = Describe("Lifecycle taints and current batch membership", func() {
	for _, key := range []string{corev1.TaintNodeNotReady, corev1.TaintNodeUnreachable} {
		for _, effect := range []corev1.TaintEffect{corev1.TaintEffectNoSchedule, corev1.TaintEffectNoExecute} {
			DescribeTable(fmt.Sprintf("keeps admission and introspection consistent for %s:%s", key, effect), func(status v1alpha1.Status, picked bool, stage v1alpha1.Stage, packageState v1alpha1.State, custom, ignored, explicit, want bool) {
				nw := &v1alpha1.NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "membership"},
					Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"demo": {PackageRef: v1alpha1.PackageRef{Name: "demo", Version: "1.0.0"}, Image: "example/demo"}}},
				}
				if picked {
					nw.Status.NodePriority = map[string]metav1.Time{"worker": metav1.Now()}
				}
				if explicit {
					nw.Spec.AdditionalTolerations = []corev1.Toleration{{Key: key, Operator: corev1.TolerationOpExists}}
				}
				n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{
					{Key: key, Effect: effect},
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
				if stage != "" {
					pkg := nw.Spec.Packages["demo"]
					Expect(node.Upsert(pkg.PackageRef, pkg.Image, packageState, stage, 0, "")).To(Succeed())
				}
				IntrospectNode(node, state, cluster.skyhooks, nil, testLogger)
				Expect(node.Status() == v1alpha1.StatusBlocked).To(Equal(!want))
				selected := NewNodePicker(testLogger, nil).SelectNodes(state)
				Expect(len(selected) == 1).To(Equal(want))
			},
				Entry("fresh unreachable node", v1alpha1.StatusWaiting, false, v1alpha1.Stage(""), v1alpha1.State(""), false, false, false, false),
				Entry("in-progress recovery without priority", v1alpha1.StatusInProgress, false, v1alpha1.StageApply, v1alpha1.StateInProgress, false, false, false, true),
				Entry("persisted waiting batch after restart", v1alpha1.StatusWaiting, true, v1alpha1.StageApply, v1alpha1.StateInProgress, false, false, false, true),
				Entry("persisted paused batch on resume", v1alpha1.StatusPaused, true, v1alpha1.StageApply, v1alpha1.StateInProgress, false, false, false, true),
				Entry("displaced member that never started a stage", v1alpha1.StatusWaiting, true, v1alpha1.Stage(""), v1alpha1.State(""), false, false, false, false),
				Entry("custom taint still blocks batch", v1alpha1.StatusInProgress, true, v1alpha1.StageApply, v1alpha1.StateInProgress, true, false, false, false),
				Entry("ignore label still blocks batch", v1alpha1.StatusInProgress, true, v1alpha1.StageApply, v1alpha1.StateInProgress, false, true, false, false),
				Entry("explicit admission opt-in", v1alpha1.StatusWaiting, false, v1alpha1.Stage(""), v1alpha1.State(""), false, false, true, true),
				Entry("erroring interrupt in progress", v1alpha1.StatusInProgress, false, v1alpha1.StageInterrupt, v1alpha1.StateErroring, false, false, false, true),
				Entry("erroring interrupt waiting after restart", v1alpha1.StatusWaiting, true, v1alpha1.StageInterrupt, v1alpha1.StateErroring, false, false, false, true),
				Entry("erroring uninstall interrupt in progress", v1alpha1.StatusInProgress, true, v1alpha1.StageUninstallInterrupt, v1alpha1.StateErroring, false, false, false, true),
				Entry("erroring uninstall interrupt waiting after restart", v1alpha1.StatusWaiting, true, v1alpha1.StageUninstallInterrupt, v1alpha1.StateErroring, false, false, false, true),
				Entry("custom taint still blocks erroring package", v1alpha1.StatusInProgress, true, v1alpha1.StageInterrupt, v1alpha1.StateErroring, true, false, false, false),
				Entry("ignore label still blocks erroring package", v1alpha1.StatusInProgress, true, v1alpha1.StageInterrupt, v1alpha1.StateErroring, false, true, false, false),
				Entry("erroring package without membership", v1alpha1.StatusWaiting, false, v1alpha1.StageInterrupt, v1alpha1.StateErroring, false, false, false, false),
				Entry("terminal erroring member retains its slot", v1alpha1.StatusErroring, true, v1alpha1.StageInterrupt, v1alpha1.StateErroring, false, false, false, true),
			)
		}
	}
})

var _ = Describe("Displaced node that later goes unreachable", func() {
	It("does not hold the batch slot it lost to another node", func() {
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "displaced"},
			Spec: v1alpha1.NodeWrightSpec{
				InterruptionBudget: v1alpha1.InterruptionBudget{Count: kptr.To(1)},
				Packages:           v1alpha1.Packages{"demo": {PackageRef: v1alpha1.PackageRef{Name: "demo", Version: "1.0.0"}, Image: "example/demo"}},
			},
		}
		nodes := &corev1.NodeList{}
		for _, name := range []string{"a", "b", "c"} {
			nodes.Items = append(nodes.Items, corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
			})
		}
		cluster, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, nodes, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		state := cluster.skyhooks[0]
		byName := map[string]wrapper.SkyhookNode{}
		for _, node := range state.GetNodes() {
			byName[node.GetNode().Name] = node
			node.SetStatus(v1alpha1.StatusWaiting)
		}
		pass := func() []string {
			IntrospectSkyhook(state, cluster.skyhooks, nil, testLogger)
			names := []string{}
			for _, node := range NewNodePicker(testLogger, nil).SelectNodes(state) {
				names = append(names, node.GetNode().Name)
			}
			return names
		}

		// a takes the single slot.
		Expect(pass()).To(Equal([]string{"a"}))
		byName["a"].SetStatus(v1alpha1.StatusInProgress)

		// a picks up a taint nobody tolerates; b takes the slot while a keeps its NodePriority entry.
		byName["a"].GetNode().Spec.Taints = []corev1.Taint{{Key: "example.com/maintenance", Effect: corev1.TaintEffectNoSchedule}}
		Expect(pass()).To(Equal([]string{"b"}))
		Expect(byName["a"].Status()).To(Equal(v1alpha1.StatusBlocked))
		Expect(state.GetSkyhook().Status.NodePriority).To(HaveKey("a"))

		// b completes.
		pkg := nw.Spec.Packages["demo"]
		Expect(byName["b"].Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")).To(Succeed())
		byName["b"].SetStatus(v1alpha1.StatusComplete)
		Expect(byName["b"].IsComplete()).To(BeTrue())

		// The custom taint comes off and a goes unreachable, for example powered off for repair.
		byName["a"].GetNode().Spec.Taints = []corev1.Taint{
			{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule},
			{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoExecute},
		}
		byName["a"].GetNode().Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}

		// a lost its slot to b and never started work, so it must not block c.
		Expect(pass()).To(Equal([]string{"c"}))
		Expect(byName["a"].Status()).To(Equal(v1alpha1.StatusBlocked))
		Expect(pass()).To(Equal([]string{"c"}))
	})
})
