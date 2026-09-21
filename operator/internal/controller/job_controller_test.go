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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

var _ = Describe("JobReconcile", func() {
	const (
		skyhookName = "gpu-init"
		nodeName    = "worker-7"
		namespace   = "skyhook"
		image       = "ghcr.io/nvidia/skyhook-packages/tuning:1.0.0"
	)

	var pkgRef = v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"}

	validOpts := func() SkyhookOperatorOptions {
		return SkyhookOperatorOptions{
			Namespace:            namespace,
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
			PauseImage:           "registry.k8s.io/pause:3.10",
			MaxInterval:          10 * time.Minute,
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour,
				JobTTLFailed:    24 * time.Hour,
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
	}

	// newReconciler builds an isolated reconciler over a fake client seeded with objects,
	// avoiding the background manager. The fake clientset serves the deadline log snapshot.
	newReconciler := func(objects ...client.Object) *JobReconciler {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		return NewJobReconciler(c, c, k8sfake.NewClientset(), events.NewFakeRecorder(50), validOpts().JobOperatorOptions)
	}

	// nodeWithState returns a Node carrying node state for one package at (stage, state).
	nodeWithState := func(state v1alpha1.State, stage v1alpha1.Stage) *corev1.Node {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		Expect(sn.Upsert(pkgRef, image, state, stage, 0, "")).To(Succeed())
		return node
	}

	// packageJob returns a package/interrupt Job pinned to the node, carrying the package
	// annotation and the conditions supplied.
	packageJob := func(stage v1alpha1.Stage, interrupt bool, conditions ...batchv1.JobCondition) *batchv1.Job {
		lbls := map[string]string{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhookName}
		if interrupt {
			lbls[fmt.Sprintf("%s/interrupt", v1alpha1.METADATA_PREFIX)] = interruptLabelValue
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "tuning-1-0-0-" + string(stage), Namespace: namespace, UID: "job-uid-1", Labels: lbls},
			Spec:       batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeName: nodeName}}},
			Status:     batchv1.JobStatus{Conditions: conditions},
		}
		Expect(SetPackages(job, &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName}}, image, stage,
			&v1alpha1.Package{PackageRef: pkgRef, Image: image})).To(Succeed())
		return job
	}

	trueCondition := func(t batchv1.JobConditionType, reason string) batchv1.JobCondition {
		return batchv1.JobCondition{Type: t, Status: corev1.ConditionTrue, Reason: reason}
	}

	getNodeState := func(r client.Client) v1alpha1.NodeState {
		var node corev1.Node
		Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		sn, err := wrapper.NewSkyhookNodeOnly(&node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		state, err := sn.State()
		Expect(err).ToNot(HaveOccurred())
		return state
	}

	getJob := func(r client.Client, name string) *batchv1.Job {
		var job batchv1.Job
		Expect(r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &job)).To(Succeed())
		return &job
	}

	// failedChildPod returns a Failed child pod of the Job, aged ageAgo, optionally a disruption
	// casualty (carrying DisruptionTarget).
	failedChildPod := func(job *batchv1.Job, name string, ageAgo time.Duration, disrupted bool) *corev1.Pod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespace,
				Labels:            map[string]string{batchControllerUIDLabel: string(job.UID)},
				CreationTimestamp: metav1.NewTime(time.Now().Add(-ageAgo)),
			},
			Status: corev1.PodStatus{Phase: corev1.PodFailed},
		}
		if disrupted {
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}}
		}
		return pod
	}

	// failedChildPod alone is the kubelet-rejection shape: Failed with no container statuses, so
	// no verdict. The pruner, the classifier and the log snapshot all ignore those, so a spec that
	// needs a real archive has to say a step actually failed.
	genuineFailedChildPod := func(job *batchv1.Job, name string, ageAgo time.Duration) *corev1.Pod {
		pod := failedChildPod(job, name, ageAgo, false)
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
			{Name: "tuning-apply", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}},
		}
		return pod
	}

	exists := func(r client.Client, name string) bool {
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &corev1.Pod{})
		if apierrors.IsNotFound(err) {
			return false
		}
		Expect(err).ToNot(HaveOccurred())
		return true
	}

	It("records a completed apply once and marks the Job with the success TTL", func() {
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply)
		job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		status, ok := getNodeState(r)[pkgRef.GetUniqueName()]
		Expect(ok).To(BeTrue())
		Expect(status.State).To(Equal(v1alpha1.StateComplete))
		Expect(status.Stage).To(Equal(v1alpha1.StageApply))

		marked := getJob(r, job.Name)
		Expect(marked.Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		Expect(marked.Spec.TTLSecondsAfterFinished).ToNot(BeNil())
		Expect(*marked.Spec.TTLSecondsAfterFinished).To(BeEquivalentTo(int32(time.Hour.Seconds())))
	})

	It("is a no-op for an already-recorded Job (duplicate event)", func() {
		node := nodeWithState(v1alpha1.StateComplete, v1alpha1.StageConfig)
		job := packageJob(v1alpha1.StageConfig, false, trueCondition(batchv1.JobComplete, ""))
		job.Annotations[annotationStateRecorded] = annotationValueTrue
		r := newReconciler(node, job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())
		// unchanged
		Expect(getNodeState(r)[pkgRef.GetUniqueName()].Stage).To(Equal(v1alpha1.StageConfig))
	})

	It("does not regress a stage when a completion is re-served after the node advanced", func() {
		// The apply Job re-fires, but node state has already moved on to config.
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageConfig)
		job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		// still at config, not regressed back to apply
		Expect(getNodeState(r)[pkgRef.GetUniqueName()].Stage).To(Equal(v1alpha1.StageConfig))
		Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
	})

	It("marks a completion but writes no state when the node is gone", func() {
		job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(job) // no node seeded

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())
		Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
	})

	It("records a whole-stage DeadlineExceeded as erroring, leaving the Job in place", func() {
		// Only interrupt Jobs carry a Job-level deadline, and it can fire with no failed attempt
		// behind it, so DeadlineExceeded is genuine on its own evidence.
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageConfig)
		job := packageJob(v1alpha1.StageConfig, false,
			trueCondition(batchv1.JobFailed, batchv1.JobReasonDeadlineExceeded))
		r := newReconciler(node, job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateErroring))

		timedOut := getJob(r, job.Name) // still present (timeout marker), marked with failure TTL
		Expect(timedOut.Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		Expect(*timedOut.Spec.TTLSecondsAfterFinished).To(BeEquivalentTo(int32((24 * time.Hour).Seconds())))
	})

	DescribeTable("BackoffLimitExceeded times a stage out only on genuine attempt evidence",
		func(archive func(*batchv1.Job) *corev1.Pod, expected v1alpha1.State) {
			node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply)
			job := packageJob(v1alpha1.StageApply, false,
				trueCondition(batchv1.JobFailed, batchv1.JobReasonBackoffLimitExceeded))
			r := newReconciler(node, job, archive(job))

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(expected))
			Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		},
		Entry("a step that exited nonzero is the package's failure",
			func(job *batchv1.Job) *corev1.Pod {
				pod := failedChildPod(job, "attempt-exit-1", time.Minute, false)
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
					{Name: "tuning-apply", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}},
				}
				return pod
			}, v1alpha1.StateErroring),
		Entry("an attempt killed by its own deadline is the package's failure",
			func(job *batchv1.Job) *corev1.Pod {
				pod := failedChildPod(job, "attempt-timeout", time.Minute, false)
				pod.Status.Reason = podReasonDeadlineExceeded
				return pod
			}, v1alpha1.StateErroring),
		// These pods carry no container statuses: the kubelet refused them before the package
		// ran. Finite backoff makes that reachable in about a minute on a rebooting node, so
		// timing out here would strand a package that never executed a line of script.
		Entry("attempts the kubelet refused to admit are not the package's failure",
			func(job *batchv1.Job) *corev1.Pod {
				pod := failedChildPod(job, "attempt-outofpods", time.Minute, false)
				pod.Status.Reason = "OutOfpods"
				return pod
			}, v1alpha1.StateInProgress),
		Entry("a disruption casualty is not the package's failure",
			func(job *batchv1.Job) *corev1.Pod {
				return failedChildPod(job, "attempt-evicted", time.Minute, true)
			}, v1alpha1.StateInProgress),
	)

	It("writes no state for a BackoffLimitExceeded Job whose attempts are already gone", func() {
		// Nothing left to judge: the safe direction is to re-run the stage, not time it out.
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply)
		job := packageJob(v1alpha1.StageApply, false,
			trueCondition(batchv1.JobFailed, batchv1.JobReasonBackoffLimitExceeded))
		r := newReconciler(node, job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateInProgress))
		Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
	})

	It("deletes a Job whose package was invalidated", func() {
		job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
		Expect(InvalidatePackage(job)).To(Succeed())
		r := newReconciler(job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		var got batchv1.Job
		err = r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: job.Name}, &got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("snapshots the stuck container's logs on FailureTarget", func() {
		job := packageJob(v1alpha1.StageConfig, false, trueCondition(batchv1.JobFailureTarget, ""))
		stuckPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tuning-pod-1", Namespace: namespace,
				Labels: map[string]string{batchControllerUIDLabel: string(job.UID)},
			},
			Spec: corev1.PodSpec{NodeName: nodeName},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				InitContainerStatuses: []corev1.ContainerStatus{
					{Name: "init-copy", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
					{Name: "config", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			},
		}
		r := newReconciler(job, stuckPod)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		snap := getJob(r, job.Name).Annotations[annotationLastLogs]
		Expect(snap).To(ContainSubstring("config"))
		Expect(snap).To(ContainSubstring("fake logs"))
	})

	It("records the waiting reason when the stuck container never started", func() {
		job := packageJob(v1alpha1.StageConfig, false, trueCondition(batchv1.JobFailureTarget, ""))
		stuckPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tuning-pod-2", Namespace: namespace,
				Labels: map[string]string{batchControllerUIDLabel: string(job.UID)},
			},
			Spec: corev1.PodSpec{NodeName: nodeName},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{
					{Name: "config", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: "ImagePullBackOff", Message: "back-off pulling image",
					}}},
				},
			},
		}
		r := newReconciler(job, stuckPod)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(getJob(r, job.Name).Annotations[annotationLastLogs]).To(ContainSubstring("ImagePullBackOff"))
	})

	It("keeps the first and most-recent genuine failures, pruning those in between", func() {
		job := packageJob(v1alpha1.StageApply, false) // active (no terminal condition)
		first := genuineFailedChildPod(job, "attempt-first", 3*time.Hour)
		middle := genuineFailedChildPod(job, "attempt-middle", 2*time.Hour)
		newest := genuineFailedChildPod(job, "attempt-newest", time.Hour)
		r := newReconciler(job, first, middle, newest)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(exists(r, "attempt-first")).To(BeTrue())   // root-cause archive
		Expect(exists(r, "attempt-middle")).To(BeFalse()) // pruned
		Expect(exists(r, "attempt-newest")).To(BeTrue())  // most-recent archive
	})

	It("keeps both archives when there are only two genuine failures", func() {
		job := packageJob(v1alpha1.StageApply, false)
		first := genuineFailedChildPod(job, "attempt-first", 2*time.Hour)
		newest := genuineFailedChildPod(job, "attempt-newest", time.Hour)
		r := newReconciler(job, first, newest)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(exists(r, "attempt-first")).To(BeTrue())
		Expect(exists(r, "attempt-newest")).To(BeTrue())
	})

	It("promotes interrupt-skipped packages on interrupt completion", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		// the winning package ran the interrupt; a sibling was skipped during merge
		Expect(sn.Upsert(pkgRef, image, v1alpha1.StateInProgress, v1alpha1.StageInterrupt, 0, "")).To(Succeed())
		sibling := v1alpha1.PackageRef{Name: "other", Version: "2.0.0"}
		Expect(sn.Upsert(sibling, image, v1alpha1.StateSkipped, v1alpha1.StageInterrupt, 0, "")).To(Succeed())

		scr := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{
				"tuning": {PackageRef: pkgRef, Image: image},
				"other":  {PackageRef: sibling, Image: image},
			}},
		}
		job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, scr, job)

		_, err = r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		state := getNodeState(r)
		Expect(state[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
		Expect(state[sibling.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
	})

	It("promotes the sibling without resurrecting a package whose entry was removed", func() {
		// A rerun/reset/uninstall clears an entry while that package's interrupt Job is
		// completing. The Job is package-agnostic and still owes the sibling its promotion, but
		// re-creating the cleared entry would put it back at (interrupt, complete) — the rerun
		// predicate would then keep the Job and the stage would never run again, so the rerun
		// the user asked for would silently do nothing.
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		sibling := v1alpha1.PackageRef{Name: "other", Version: "2.0.0"}
		Expect(sn.Upsert(sibling, image, v1alpha1.StateSkipped, v1alpha1.StageInterrupt, 0, "")).To(Succeed())
		// deliberately no entry for pkgRef: that is the removal

		scr := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{
				"tuning": {PackageRef: pkgRef, Image: image},
				"other":  {PackageRef: sibling, Image: image},
			}},
		}
		job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, scr, job)

		_, err = r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		state := getNodeState(r)
		Expect(state).ToNot(HaveKey(pkgRef.GetUniqueName()), "the removal must stand")
		Expect(state[sibling.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete), "the sibling is still promoted")
	})

	It("does not regress an entry that already advanced past the interrupt", func() {
		// The non-interrupt path never reaches the write here — shouldRecordCompletion returns
		// false on a mismatched stage. On the interrupt path a skipped sibling makes it return
		// true, so this direction is guarded only by the completion check.
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		Expect(sn.Upsert(pkgRef, image, v1alpha1.StateComplete, v1alpha1.StagePostInterrupt, 0, "")).To(Succeed())
		sibling := v1alpha1.PackageRef{Name: "other", Version: "2.0.0"}
		Expect(sn.Upsert(sibling, image, v1alpha1.StateSkipped, v1alpha1.StageInterrupt, 0, "")).To(Succeed())

		scr := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName},
			Spec: v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{
				"tuning": {PackageRef: pkgRef, Image: image},
				"other":  {PackageRef: sibling, Image: image},
			}},
		}
		job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, scr, job)

		_, err = r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		state := getNodeState(r)
		// The sibling's promotion is what proves the interrupt completion path actually ran:
		// without it this spec would pass on a reconcile that did nothing at all.
		Expect(state[sibling.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
		Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))

		entry := state[pkgRef.GetUniqueName()]
		Expect(entry.Stage).To(Equal(v1alpha1.StagePostInterrupt))
		Expect(entry.State).To(Equal(v1alpha1.StateComplete))
	})

	It("marks an interrupt completion without panicking when the CR is already gone", func() {
		// Same window as the resurrection case: the NodeWright deleted while a completed interrupt
		// Job is still unprocessed. GetSkyhook reads a missing CR as (nil, nil).
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageInterrupt)
		job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, job) // no NodeWright seeded

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
		Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
	})

	It("removes the superseded version's entry on upgrade completion", func() {
		// The upgrade branch is the other place that calls RemoveState and then leans on the
		// guarded fallback; NodeState is keyed name|version, so only the old key goes.
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		old := v1alpha1.PackageRef{Name: "tuning", Version: "0.9.0"}
		Expect(sn.Upsert(old, image, v1alpha1.StateComplete, v1alpha1.StageConfig, 0, "")).To(Succeed())
		Expect(sn.Upsert(pkgRef, image, v1alpha1.StateInProgress, v1alpha1.StageUpgrade, 0, "")).To(Succeed())

		job := packageJob(v1alpha1.StageUpgrade, false, trueCondition(batchv1.JobComplete, ""))
		r := newReconciler(node, job)

		_, err = r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		state := getNodeState(r)
		Expect(state).ToNot(HaveKey(old.GetUniqueName()))
		Expect(state[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
		Expect(state[pkgRef.GetUniqueName()].Stage).To(Equal(v1alpha1.StageUpgrade))
	})

	It("records erroring (state only, no marker) for a stale FailureTarget on an unreachable node", func() {
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageConfig)
		job := packageJob(v1alpha1.StageConfig, false)
		job.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-10 * time.Minute)), // past the grace window
		}}
		r := newReconciler(node, job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateErroring))
		// state-only: no marker or TTL until the Job goes terminal
		Expect(getJob(r, job.Name).Annotations).ToNot(HaveKey(annotationStateRecorded))
	})

	It("requeues a fresh (not-yet-stale) FailureTarget so the stale check re-fires", func() {
		job := packageJob(v1alpha1.StageConfig, false)
		job.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(time.Now()),
		}}
		r := newReconciler(job)

		res, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())
		// Just-transitioned, so effectively the whole window plus the boundary slack.
		Expect(res.RequeueAfter).To(BeNumerically("~", failureTargetGrace+time.Second, time.Second))
	})

	It("requeues only the grace left on a part-way FailureTarget, not a fresh window", func() {
		job := packageJob(v1alpha1.StageConfig, false)
		job.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-4 * time.Minute)),
		}}
		r := newReconciler(job)

		res, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())
		// 4 of the 5 minutes are already spent; re-check in the remaining ~1, not another 5.
		Expect(res.RequeueAfter).To(BeNumerically("~", time.Minute+time.Second, 2*time.Second))
	})

	It("does not count or delete disruption casualties when pruning", func() {
		job := packageJob(v1alpha1.StageApply, false)
		disruption := failedChildPod(job, "attempt-disrupted", 4*time.Hour, true)
		first := genuineFailedChildPod(job, "attempt-first", 3*time.Hour)
		middle := genuineFailedChildPod(job, "attempt-middle", 2*time.Hour)
		newest := genuineFailedChildPod(job, "attempt-newest", time.Hour)
		r := newReconciler(job, disruption, first, middle, newest)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		// disruption casualty untouched (no failure verdict, not counted); only the middle
		// genuine failure is pruned; first and newest remain.
		Expect(exists(r, "attempt-disrupted")).To(BeTrue())
		Expect(exists(r, "attempt-first")).To(BeTrue())
		Expect(exists(r, "attempt-middle")).To(BeFalse())
		Expect(exists(r, "attempt-newest")).To(BeTrue())
	})

	stuckChildPod := func(job *batchv1.Job, name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespace,
				Labels: map[string]string{batchControllerUIDLabel: string(job.UID)},
			},
			Spec: corev1.PodSpec{NodeName: nodeName},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{
					{Name: "config", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: "ImagePullBackOff", Message: "back-off pulling image",
					}}},
				},
			},
		}
	}

	It("does not snapshot logs when a genuine failed archive already carries them", func() {
		job := packageJob(v1alpha1.StageConfig, false, trueCondition(batchv1.JobFailureTarget, ""))
		r := newReconciler(job, genuineFailedChildPod(job, "archive-pod", time.Hour), stuckChildPod(job, "stuck-pod"))

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())
		Expect(getJob(r, job.Name).Annotations).ToNot(HaveKey(annotationLastLogs))
	})

	It("still snapshots when the only failed attempt is one the kubelet refused", func() {
		// A rejected attempt is Failed but carries no container statuses and no logs. Treating it
		// as the archive would leave the timed-out stage with no evidence at all.
		job := packageJob(v1alpha1.StageConfig, false, trueCondition(batchv1.JobFailureTarget, ""))
		r := newReconciler(job, failedChildPod(job, "rejected-pod", time.Hour, false), stuckChildPod(job, "stuck-pod"))

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())
		Expect(getJob(r, job.Name).Annotations[annotationLastLogs]).To(ContainSubstring("ImagePullBackOff"))
	})

	It("returns an error (for a backoff retry) when stale-FailureTarget recording fails", func() {
		node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageConfig)
		job := packageJob(v1alpha1.StageConfig, false)
		job.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
		}}

		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		// Fail the node patch so recordStaleFailureTarget errors; on an unreachable node
		// nothing else would retry the erroring write, so JobReconcile must surface the error.
		c := interceptor.NewClient(
			fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, job).Build(),
			interceptor.Funcs{
				Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
					return fmt.Errorf("simulated node patch conflict")
				},
			})
		r := NewJobReconciler(c, c, k8sfake.NewClientset(), events.NewFakeRecorder(50), validOpts().JobOperatorOptions)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).To(HaveOccurred())
	})

	Describe("JobReconciler", func() {

		It("reconciles the Job named by the request", func() {
			node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply)
			job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
			r := newReconciler(node, job)

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
				Namespace: namespace, Name: nodeName,
			}})
			Expect(err).ToNot(HaveOccurred())

			// Went through JobReconcile: completion recorded and the Job marked.
			Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
			Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		})

		It("maps owned Job events to the full node name", func() {
			job := packageJob(v1alpha1.StageApply, false)

			requests := jobToNodeRequest(ctx, job)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Namespace).To(Equal(namespace))
			Expect(requests[0].Name).To(Equal(nodeName))
		})

		It("is a no-op for a Job deleted between the event and the read", func() {
			node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply)
			r := newReconciler(node)

			// A terminal event can outlive its Job (TTL reap, foreground delete); the read
			// returns nothing and the node state must be left for the heavy pass to derive.
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
				Namespace: namespace, Name: "tuning-1-0-0-apply",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateInProgress))
		})
	})

	Describe("ownedJob predicate", func() {

		It("admits a Job carrying the nodewright name label", func() {
			Expect(ownedJob().Create(event.CreateEvent{Object: packageJob(v1alpha1.StageApply, false)})).To(BeTrue())
		})

		It("rejects a foreign Job in the same namespace", func() {
			// Filtered at the predicate rather than inside Reconcile, so a CronJob's Jobs never
			// reach the workqueue at all.
			foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name: "some-cronjob-28234", Namespace: namespace, Labels: map[string]string{"foo": "bar"},
			}}
			Expect(ownedJob().Create(event.CreateEvent{Object: foreign})).To(BeFalse())
			Expect(ownedJob().Update(event.UpdateEvent{ObjectNew: foreign})).To(BeFalse())
			Expect(ownedJob().Delete(event.DeleteEvent{Object: foreign})).To(BeFalse())
		})
	})

})
