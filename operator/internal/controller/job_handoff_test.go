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
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// The suite's manager runs only the heavy pass, and the JobReconcile specs drive a fake client, so
// nothing else here runs the two controllers side by side the way production does.
var _ = Describe("JobReconciler hand-off with the heavy pass", Ordered, ContinueOnFailure, func() {
	const (
		// Created for real, so the suite manager's heavy pass drives it.
		passName = "job-handoff-pass"
		// Never created. The heavy pass sweeps only Jobs labelled with its own CRs' names, so it
		// leaves this one's Job alone and the spec plays the pass by hand.
		snapshotName = "job-handoff-snapshot"

		timeout = 30 * time.Second
	)
	stageLabel := fmt.Sprintf("%s/stage", v1alpha1.METADATA_PREFIX)
	nodeMetaLabel := fmt.Sprintf("%s/skyhook-node-meta", v1alpha1.METADATA_PREFIX)

	var saver *SkyhookReconciler

	BeforeAll(func() {
		// One manager for both specs: controller-runtime keeps controller names in a
		// process-global set and JobReconciler always registers "job", so a second manager in
		// this process would fail SetupWithManager.
		owned, err := labels.NewRequirement(nameLabel, selection.In, []string{passName, snapshotName})
		Expect(err).ToNot(HaveOccurred())
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
			Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
				&batchv1.Job{}: {
					Namespaces: map[string]cache.Config{opts.Namespace: {}},
					Label:      labels.NewSelector().Add(*owned),
				},
			}},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(NewJobReconciler(mgr.GetClient(), mgr.GetAPIReader(), k8sfake.NewClientset(),
			mgr.GetEventRecorder("job-controller"), opts.JobOperatorOptions).SetupWithManager(mgr)).To(Succeed())

		saver, err = NewSkyhookReconciler(mgr.GetScheme(), mgr.GetClient(), mgr.GetAPIReader(), k8sfake.NewClientset(),
			mgr.GetEventRecorder("nodewright-controller"), opts)
		Expect(err).ToNot(HaveOccurred())

		mgrCtx, stop := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(stopped)
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		DeferCleanup(func() {
			stop()
			Eventually(stopped).WithTimeout(timeout).Should(BeClosed())
		})
	})

	packageStatus := func(g Gomega, nodeName, skyhookName string, pkg v1alpha1.PackageRef) v1alpha1.PackageStatus {
		var node corev1.Node
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		state, err := parseNodeState(&node, nodeStateAnnotationKey(skyhookName))
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(state).To(HaveKey(pkg.GetUniqueName()))
		return state[pkg.GetUniqueName()]
	}

	// completeJob writes the terminal status the Job controller would. The apiserver validates it:
	// Complete needs SuccessCriteriaMet beside it, and a finished Job needs its start and
	// completion times.
	completeJob := func(job *batchv1.Job) {
		now := metav1.Now()
		patch := client.MergeFrom(job.DeepCopy())
		job.Status.StartTime = &now
		job.Status.CompletionTime = &now
		job.Status.Succeeded = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonCompletionsReached, LastTransitionTime: now},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonCompletionsReached, LastTransitionTime: now},
		}
		Expect(k8sClient.Status().Patch(ctx, job, patch)).To(Succeed())
	}

	expectStateRecorded := func(job *batchv1.Job) {
		Eventually(func(g Gomega) {
			var got batchv1.Job
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &got)).To(Succeed())
			g.Expect(got.Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		}).WithTimeout(timeout).Should(Succeed())
	}

	// removeJobs deletes every Job labelled with the NodeWright's name. envtest runs no garbage
	// collector, and batch/v1's default Orphan policy and Foreground propagation both leave a
	// finalizer only the collector removes, so delete with Background and strip whatever finalizer a
	// Job already carries (deleteJobForeground puts one there).
	removeJobs := func(skyhookName string) {
		var jobs batchv1.JobList
		Expect(k8sClient.List(ctx, &jobs, client.InNamespace(opts.Namespace),
			client.MatchingLabels{nameLabel: skyhookName})).To(Succeed())
		for i := range jobs.Items {
			key := client.ObjectKeyFromObject(&jobs.Items[i])
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &jobs.Items[i],
				client.PropagationPolicy(metav1.DeletePropagationBackground)))).To(Succeed())
			Eventually(func(g Gomega) {
				var job batchv1.Job
				err := k8sClient.Get(ctx, key, &job)
				if apierrors.IsNotFound(err) {
					return
				}
				g.Expect(err).ToNot(HaveOccurred())
				if len(job.Finalizers) > 0 {
					patch := client.MergeFrom(job.DeepCopy())
					job.Finalizers = nil
					g.Expect(client.IgnoreNotFound(k8sClient.Patch(ctx, &job, patch))).To(Succeed())
				}
				g.Expect(k8sClient.Get(ctx, key, &batchv1.Job{})).To(Satisfy(apierrors.IsNotFound))
			}).WithTimeout(timeout).Should(Succeed())
		}
	}

	It("records a finished Job's completion and the heavy pass carries the rollout to complete", func() {
		const nodeName = "job-handoff-pass-node"
		pkg := v1alpha1.PackageRef{Name: "pkg", Version: "1.0.0"}
		selector := map[string]string{"job-handoff": passName}

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: selector}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		// DeferCleanup runs last-registered first, so teardown is the reverse of this order: the
		// NodeWright, then its Jobs and ConfigMaps, then the node. Deleting the Jobs while the
		// NodeWright still exists would let the heavy pass recreate any stage it has in flight,
		// and envtest has no garbage collector to remove what it recreates.
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed()) })
		DeferCleanup(func() {
			removeJobs(passName)
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.InNamespace(opts.Namespace),
				client.MatchingLabels{nodeMetaLabel: passName})).To(Succeed())
		})

		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: passName},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: selector},
				Packages:     v1alpha1.Packages{pkg.Name: {PackageRef: pkg, Image: "ghcr.io/org/pkg"}},
				// envtest stamps node.kubernetes.io/not-ready on every Node it creates and nothing
				// clears it; untolerated, the pass reports the node blocked and never starts a stage.
				AdditionalTolerations: []corev1.Toleration{{
					Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
				}},
			},
		}
		Expect(k8sClient.Create(ctx, nw)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, nw))).To(Succeed())
			// The heavy pass holds the finalizer and releases it once it has cleaned the node.
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(nw), &v1alpha1.NodeWright{})
			}).WithTimeout(timeout).Should(Satisfy(apierrors.IsNotFound))
		})

		for _, stage := range []v1alpha1.Stage{v1alpha1.StageApply, v1alpha1.StageConfig} {
			// Wait for the heavy pass to record the stage in_progress before finishing its Job. A
			// completion that lands first finds no entry open at this stage, so JobReconcile only
			// marks the Job, and the pass then deletes the marked Job and runs the stage again.
			var job batchv1.Job
			Eventually(func(g Gomega) {
				status := packageStatus(g, nodeName, passName, pkg)
				g.Expect(status.Stage).To(Equal(stage))
				g.Expect(status.State).To(Equal(v1alpha1.StateInProgress))

				var jobs batchv1.JobList
				g.Expect(k8sClient.List(ctx, &jobs, client.InNamespace(opts.Namespace),
					client.MatchingLabels{nameLabel: passName, stageLabel: string(stage)})).To(Succeed())
				g.Expect(jobs.Items).To(HaveLen(1))
				job = jobs.Items[0]
			}).WithTimeout(timeout).Should(Succeed())

			completeJob(&job)
			expectStateRecorded(&job)
		}

		// Config is the last stage of a package with no interrupt, so nothing moves this again.
		Eventually(func(g Gomega) {
			status := packageStatus(g, nodeName, passName, pkg)
			g.Expect(status.Stage).To(Equal(v1alpha1.StageConfig))
			g.Expect(status.State).To(Equal(v1alpha1.StateComplete))

			var got v1alpha1.NodeWright
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nw), &got)).To(Succeed())
			g.Expect(got.Status.NodeStatus).To(HaveKeyWithValue(nodeName, v1alpha1.StatusComplete))
			g.Expect(got.Status.Status).To(Equal(v1alpha1.StatusComplete))
		}).WithTimeout(timeout).Should(Succeed())
	})

	It("keeps a completion recorded after the heavy pass's snapshot when the pass saves its own change", func() {
		const nodeName = "job-handoff-snapshot-node"
		pkgA := v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "pkg-a", Version: "1.0.0"}, Image: "ghcr.io/org/pkg-a"}
		pkgB := v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "pkg-b", Version: "1.0.0"}, Image: "ghcr.io/org/pkg-b"}
		selector := map[string]string{"job-handoff": snapshotName}
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: snapshotName},
			Spec: v1alpha1.NodeWrightSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: selector},
				Packages:     v1alpha1.Packages{pkgA.Name: pkgA, pkgB.Name: pkgB},
			},
		}

		// A is running its apply Job; B has finished apply and is due for config.
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: selector}}
		seed, err := wrapper.NewSkyhookNodeOnly(node, snapshotName)
		Expect(err).ToNot(HaveOccurred())
		Expect(seed.Upsert(pkgA.PackageRef, pkgA.Image, v1alpha1.StateInProgress, v1alpha1.StageApply, 0, "")).To(Succeed())
		Expect(seed.Upsert(pkgB.PackageRef, pkgB.Image, v1alpha1.StateComplete, v1alpha1.StageApply, 0, "")).To(Succeed())
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed()) })

		var snapshot corev1.Node
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), &snapshot)).To(Succeed())
		clusterState, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}},
			&corev1.NodeList{Items: []corev1.Node{snapshot}}, &v1alpha1.DeploymentPolicyList{})
		Expect(err).ToNot(HaveOccurred())
		Expect(clusterState.skyhooks[0].GetNodes()).To(HaveLen(1))
		passNode := clusterState.skyhooks[0].GetNodes()[0]

		job := createJobFromPackage(opts, &pkgA, wrapper.NewSkyhookWrapper(nw), nodeName, v1alpha1.StageApply)
		Expect(setJobPackage(job, nw, pkgA.Image, v1alpha1.StageApply, &pkgA)).To(Succeed())
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(removeJobs, snapshotName)

		completeJob(job)
		expectStateRecorded(job)
		// The node write precedes the mark, so a marked Job means the completion is stored.
		Expect(packageStatus(Default, nodeName, snapshotName, pkgA.PackageRef).State).To(Equal(v1alpha1.StateComplete))

		snapshotA, found := passNode.PackageStatus(pkgA.GetUniqueName())
		Expect(found).To(BeTrue())
		Expect(snapshotA.State).To(Equal(v1alpha1.StateInProgress), "the pass's snapshot must predate the completion")

		// The pass's own change, the one ApplyPackage makes when it launches B's config stage. A
		// pass that changes nothing never reaches saveNodeChanges, so pin that this one carries B.
		Expect(passNode.Upsert(pkgB.PackageRef, pkgB.Image, v1alpha1.StateInProgress, v1alpha1.StageConfig, 0, "")).To(Succeed())
		Expect(passNode.Changed()).To(BeTrue())
		original, ok := clusterState.tracker.GetOriginal(passNode.GetNode()).(*corev1.Node)
		Expect(ok).To(BeTrue())
		key := nodeStateAnnotationKey(snapshotName)
		before, err := parseNodeState(original, key)
		Expect(err).ToNot(HaveOccurred())
		after, err := parseNodeState(passNode.GetNode(), key)
		Expect(err).ToNot(HaveOccurred())
		delta := computeNodeStateDelta(before, after)
		Expect(delta).To(HaveLen(1))
		Expect(delta).To(HaveKey(pkgB.GetUniqueName()))

		Expect(saver.saveNodeChanges(ctx, original, passNode, snapshotName)).To(Succeed())

		Expect(packageStatus(Default, nodeName, snapshotName, pkgA.PackageRef).State).To(Equal(v1alpha1.StateComplete),
			"the JobReconciler's completion must survive the pass's write")
		b := packageStatus(Default, nodeName, snapshotName, pkgB.PackageRef)
		Expect(b.Stage).To(Equal(v1alpha1.StageConfig), "the pass's own change must land")
		Expect(b.State).To(Equal(v1alpha1.StateInProgress), "the pass's own change must land")
	})
})
