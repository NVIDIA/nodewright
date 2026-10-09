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

	// rejectedChildPod is an attempt the kubelet refused to admit, with the reason a node at its
	// pod limit gives.
	rejectedChildPod := func(job *batchv1.Job, name string) *corev1.Pod {
		pod := failedChildPod(job, name, time.Minute, false)
		pod.Status.Reason = "OutOfpods"
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

	It("records a completion without the attempts the kubelet rejected at admission", func() {
		job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
		job.Status.Failed = 2
		r := newReconciler(nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply), job,
			rejectedChildPod(job, "attempt-rejected-0"), rejectedChildPod(job, "attempt-rejected-1"))

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		status := getNodeState(r)[pkgRef.GetUniqueName()]
		Expect(status.State).To(Equal(v1alpha1.StateComplete))
		Expect(status.Restarts).To(Equal(int32(0)))
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

	// A failed attempt while the Job still has retries is evidence on the package, not a verdict on
	// the node. Marked on the node, it ends the batch early and counts the node failed, and a retry
	// that succeeds cannot undo that.
	Describe("a package whose attempt fails while its Job retries", func() {
		// The in_progress node status is the heavy pass's, written when the stage started.
		startedNode := func(state v1alpha1.State, stage v1alpha1.Stage) *corev1.Node {
			node := nodeWithState(state, stage)
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			sn.SetStatus(v1alpha1.StatusInProgress)
			return node
		}

		// A package stage fails as a Failed pod under restartPolicy Never. An interrupt runs under
		// OnFailure, so its container restarts in place and the pod stays Pending in CrashLoopBackOff.
		failedAttempt := func(stage v1alpha1.Stage, interrupt bool) *corev1.Pod {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "tuning-attempt", Namespace: namespace, Labels: map[string]string{}},
				Spec:       corev1.PodSpec{NodeName: nodeName, RestartPolicy: corev1.RestartPolicyNever},
				Status: corev1.PodStatus{Phase: corev1.PodFailed, InitContainerStatuses: []corev1.ContainerStatus{{
					Name: "tuning-" + string(stage), State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
				}}},
			}
			if interrupt {
				pod.Labels[interruptLabel] = interruptLabelValue
				pod.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
				pod.Status = corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{{
					Name: InterruptContainerName, RestartCount: 1,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}}}
			}
			Expect(SetPackages(pod, &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName}}, image, stage,
				&v1alpha1.Package{PackageRef: pkgRef, Image: image})).To(Succeed())
			return pod
		}

		nodeStatus := func(r client.Client) v1alpha1.Status {
			var node corev1.Node
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
			sn, err := wrapper.NewSkyhookNodeOnly(&node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			return sn.Status()
		}

		DescribeTable("is recorded complete when a retry succeeds, and its node never reads erroring",
			func(stage v1alpha1.Stage, interrupt bool) {
				job := packageJob(stage, interrupt)
				pod := failedAttempt(stage, interrupt)
				r := newReconciler(startedNode(v1alpha1.StateInProgress, stage), job, pod)
				podWatch := NewPodReconciler(r.Client, r.Client, k8sfake.NewClientset(), events.NewFakeRecorder(50), namespace)

				_, err := podWatch.PodReconcile(ctx, pod)
				Expect(err).ToNot(HaveOccurred())
				Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateErroring), "the failed attempt shows on the package")
				Expect(nodeStatus(r)).To(Equal(v1alpha1.StatusInProgress))

				job.Status.Conditions = []batchv1.JobCondition{trueCondition(batchv1.JobComplete, "")}
				_, err = r.JobReconcile(ctx, job)
				Expect(err).ToNot(HaveOccurred())
				Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
				Expect(nodeStatus(r)).To(Equal(v1alpha1.StatusInProgress))
			},
			Entry("a package stage", v1alpha1.StageApply, false),
			Entry("an interrupt, killed by the reboot it requested", v1alpha1.StageInterrupt, true),
		)

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

	It("keeps an interrupt's in-place restarts when it times out before the pod watch recorded erroring", func() {
		// An interrupt that hung without crash-looping has restarted 0 times; status.failed counts
		// only the pod its deadline killed.
		job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobFailed, batchv1.JobReasonDeadlineExceeded))
		job.Status.Failed = 1
		r := newReconciler(nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageInterrupt), job)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		status := getNodeState(r)[pkgRef.GetUniqueName()]
		Expect(status.State).To(Equal(v1alpha1.StateErroring))
		Expect(status.Restarts).To(Equal(int32(0)))
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

	It("times a stage out at the attempts that ran, without the ones rejected at admission", func() {
		job := packageJob(v1alpha1.StageApply, false,
			trueCondition(batchv1.JobFailed, batchv1.JobReasonBackoffLimitExceeded))
		job.Status.Failed = 4
		objects := []client.Object{nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply), job,
			genuineFailedChildPod(job, "attempt-exit-1", 4*time.Minute)}
		for i := range 3 {
			objects = append(objects, rejectedChildPod(job, fmt.Sprintf("attempt-rejected-%d", i)))
		}
		r := newReconciler(objects...)

		_, err := r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		status := getNodeState(r)[pkgRef.GetUniqueName()]
		Expect(status.State).To(Equal(v1alpha1.StateErroring))
		Expect(status.Restarts).To(Equal(int32(1)))
		var node corev1.Node
		Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
		sn, err := wrapper.NewSkyhookNodeOnly(&node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		Expect(sn.Status()).To(Equal(v1alpha1.StatusErroring))
	})

	// Restarts rides on the state transition, so a pod list that fails must not hold the transition
	// up: the count falls back to status.failed, rejections included.
	DescribeTable("records the transition at status.failed when the Job's pods cannot be listed",
		func(condition batchv1.JobCondition, expected v1alpha1.State) {
			job := packageJob(v1alpha1.StageApply, false, condition)
			job.Status.Failed = 2

			scheme := runtime.NewScheme()
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			// Both failed pods are admission rejections, which a pod list that worked would subtract.
			c := interceptor.NewClient(
				fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply), job,
					rejectedChildPod(job, "attempt-rejected-0"), rejectedChildPod(job, "attempt-rejected-1")).Build(),
				interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, pods := list.(*corev1.PodList); pods {
							return fmt.Errorf("simulated pod list failure")
						}
						return c.List(ctx, list, opts...)
					},
				})
			r := NewJobReconciler(c, c, k8sfake.NewClientset(), events.NewFakeRecorder(50), validOpts().JobOperatorOptions)

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			status := getNodeState(r)[pkgRef.GetUniqueName()]
			Expect(status.State).To(Equal(expected))
			Expect(status.Restarts).To(Equal(int32(2)))
			Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		},
		Entry("a completion", trueCondition(batchv1.JobComplete, ""), v1alpha1.StateComplete),
		// DeadlineExceeded is genuine without reading the pods, so the classifier never lists them.
		Entry("a timeout", trueCondition(batchv1.JobFailed, batchv1.JobReasonDeadlineExceeded), v1alpha1.StateErroring),
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

	Describe("a failed Job whose entry the pod watch already recorded erroring", func() {
		// The ContainerSHA is one the Job would not write, so overwriting what the pod watch recorded
		// shows.
		erroringNode := func(stage v1alpha1.Stage, restarts int32, status v1alpha1.Status) *corev1.Node {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			Expect(sn.Upsert(pkgRef, image, v1alpha1.StateErroring, stage, restarts, "sha256:recorded")).To(Succeed())
			sn.SetStatus(status)
			return node
		}

		// The Job is not yet marked processed, so a re-served event reaches the state write again.
		failedJob := func(stage v1alpha1.Stage, failed int32) (*batchv1.Job, *corev1.Pod) {
			job := packageJob(stage, false, trueCondition(batchv1.JobFailed, batchv1.JobReasonBackoffLimitExceeded))
			job.Status.Failed = failed
			return job, genuineFailedChildPod(job, "attempt-exit-1", time.Minute)
		}

		getNode := func(r client.Client) *corev1.Node {
			var node corev1.Node
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
			return &node
		}

		nodeStatus := func(node *corev1.Node) v1alpha1.Status {
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			return sn.Status()
		}

		recordedEvents := func(r *JobReconciler) chan string {
			return r.recorder.(*events.FakeRecorder).Events
		}

		It("marks the node erroring and corrects Restarts, keeping what the pod watch recorded", func() {
			job, attempt := failedJob(v1alpha1.StageApply, 3)
			r := newReconciler(erroringNode(v1alpha1.StageApply, 2, v1alpha1.StatusInProgress), job, attempt)
			want := getNodeState(r)[pkgRef.GetUniqueName()]
			want.Restarts = 3

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNodeState(r)[pkgRef.GetUniqueName()]).To(Equal(want))
			Expect(nodeStatus(getNode(r))).To(Equal(v1alpha1.StatusErroring), "the pod watch leaves the node's status to the Job")
			Expect(recordedEvents(r)).To(HaveLen(1))
		})

		It("marks the node erroring when the attempts still on the Job read as not genuine", func() {
			// The genuine attempt the pod watch recorded has been garbage-collected, leaving only an
			// admission rejection to judge. The entry stays erroring as the timeout marker, so the
			// node has to be marked, and the count is every failed pod but that rejection.
			job, _ := failedJob(v1alpha1.StageApply, 3)
			r := newReconciler(erroringNode(v1alpha1.StageApply, 1, v1alpha1.StatusInProgress), job,
				rejectedChildPod(job, "attempt-outofpods"))

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(nodeStatus(getNode(r))).To(Equal(v1alpha1.StatusErroring))
			Expect(getNodeState(r)[pkgRef.GetUniqueName()].Restarts).To(Equal(int32(2)))
			Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		})

		It("writes nothing on a re-served event", func() {
			// The first event already corrected Restarts and marked the node.
			job, attempt := failedJob(v1alpha1.StageApply, 3)
			r := newReconciler(erroringNode(v1alpha1.StageApply, 3, v1alpha1.StatusErroring), job, attempt)
			before := getNode(r)

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNode(r).ResourceVersion).To(Equal(before.ResourceVersion))
			Expect(recordedEvents(r)).To(BeEmpty())
			Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		})

		It("leaves an entry erroring at another stage untouched", func() {
			// The node has moved on to config and is failing there; an apply Job's terminal event
			// must not overwrite the newer stage's count.
			job, attempt := failedJob(v1alpha1.StageApply, 3)
			r := newReconciler(erroringNode(v1alpha1.StageConfig, 1, v1alpha1.StatusInProgress), job, attempt)
			before := getNode(r)

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNode(r).ResourceVersion).To(Equal(before.ResourceVersion))
			Expect(getNodeState(r)[pkgRef.GetUniqueName()].Restarts).To(Equal(int32(1)))
		})

		It("keeps an interrupt's in-place restarts rather than its Job's failed count", func() {
			// An interrupt restarts in place, so its entry holds the container's RestartCount;
			// status.failed counts only the pod its deadline killed.
			job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobFailed, batchv1.JobReasonDeadlineExceeded))
			job.Status.Failed = 1
			r := newReconciler(erroringNode(v1alpha1.StageInterrupt, 5, v1alpha1.StatusInProgress), job)

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNodeState(r)[pkgRef.GetUniqueName()].Restarts).To(Equal(int32(5)))
			Expect(nodeStatus(getNode(r))).To(Equal(v1alpha1.StatusErroring))
		})
	})

	// A package stage retries as fresh pods, so its attempts live on the Job, and each Job status
	// change reaches this reconciler. Restarts is status.failed less the admission rejections among
	// the Job's pods, which are read from another cache: every Job status write reruns the count, and
	// a terminal Job has no uncounted pods.
	Describe("a package Job still retrying", func() {
		activeJob := func(stage v1alpha1.Stage, interrupt bool, failed int32) *batchv1.Job {
			job := packageJob(stage, interrupt)
			job.Status.Active = 1
			job.Status.Failed = failed
			return job
		}

		// The node status and ContainerSHA are ones the Job would not write, so a write of anything
		// but Restarts shows.
		nodeAt := func(state v1alpha1.State, stage v1alpha1.Stage, restarts int32) *corev1.Node {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			Expect(sn.Upsert(pkgRef, image, state, stage, restarts, "sha256:recorded")).To(Succeed())
			sn.SetStatus(v1alpha1.StatusInProgress)
			return node
		}

		getNode := func(r client.Client) *corev1.Node {
			var node corev1.Node
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())
			return &node
		}

		DescribeTable("sets an open entry's Restarts to status.failed and changes nothing else",
			func(state v1alpha1.State) {
				job := activeJob(v1alpha1.StageApply, false, 2)
				r := newReconciler(nodeAt(state, v1alpha1.StageApply, 0), job)
				want := getNodeState(r)[pkgRef.GetUniqueName()]
				want.Restarts = 2
				before := getNode(r)

				_, err := r.JobReconcile(ctx, job)
				Expect(err).ToNot(HaveOccurred())

				after := getNode(r)
				Expect(getNodeState(r)[pkgRef.GetUniqueName()]).To(Equal(want))
				Expect(after.Labels).To(Equal(before.Labels), "the node status is untouched")
				Expect(r.recorder.(*events.FakeRecorder).Events).To(BeEmpty())
			},
			Entry("erroring", v1alpha1.StateErroring),
			Entry("in progress", v1alpha1.StateInProgress),
		)

		DescribeTable("writes nothing",
			func(state v1alpha1.State, stage v1alpha1.Stage, restarts int32, jobStage v1alpha1.Stage, interrupt bool) {
				job := activeJob(jobStage, interrupt, 2)
				r := newReconciler(nodeAt(state, stage, restarts), job)
				before := getNode(r)

				_, err := r.JobReconcile(ctx, job)
				Expect(err).ToNot(HaveOccurred())

				Expect(getNode(r).ResourceVersion).To(Equal(before.ResourceVersion))
			},
			Entry("when Restarts already equals status.failed",
				v1alpha1.StateErroring, v1alpha1.StageApply, int32(2), v1alpha1.StageApply, false),
			Entry("to an entry that has moved on to another stage",
				v1alpha1.StateErroring, v1alpha1.StageConfig, int32(1), v1alpha1.StageApply, false),
			Entry("to an entry already complete at this stage",
				v1alpha1.StateComplete, v1alpha1.StageApply, int32(0), v1alpha1.StageApply, false),
			// An interrupt restarts in place, so its entry holds the container's RestartCount.
			Entry("to an interrupt, whose entry holds in-place restarts",
				v1alpha1.StateErroring, v1alpha1.StageInterrupt, int32(5), v1alpha1.StageInterrupt, true),
		)

		// A node on its way back from a reboot can refuse replacement after replacement at
		// admission, and status.failed counts every one. None of them ran the package.
		It("writes nothing for a Job whose failed pods were all rejected at admission", func() {
			job := activeJob(v1alpha1.StageApply, false, 4)
			objects := []client.Object{nodeAt(v1alpha1.StateInProgress, v1alpha1.StageApply, 0), job}
			for i := range 4 {
				objects = append(objects, rejectedChildPod(job, fmt.Sprintf("attempt-rejected-%d", i)))
			}
			r := newReconciler(objects...)
			before := getNode(r)

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNode(r).ResourceVersion).To(Equal(before.ResourceVersion))
			Expect(getNodeState(r)[pkgRef.GetUniqueName()].Restarts).To(Equal(int32(0)))
		})

		DescribeTable("sets Restarts to status.failed less the admission rejections it counts",
			func(failed, recorded int32, attempts func(*batchv1.Job) []client.Object, want int32) {
				job := activeJob(v1alpha1.StageApply, false, failed)
				r := newReconciler(append([]client.Object{nodeAt(v1alpha1.StateInProgress, v1alpha1.StageApply, recorded), job}, attempts(job)...)...)

				_, err := r.JobReconcile(ctx, job)
				Expect(err).ToNot(HaveOccurred())

				Expect(getNodeState(r)[pkgRef.GetUniqueName()].Restarts).To(Equal(want))
			},
			Entry("one genuine failure among two rejections", int32(3), int32(0),
				func(job *batchv1.Job) []client.Object {
					return []client.Object{genuineFailedChildPod(job, "attempt-exit-1", 3*time.Minute),
						rejectedChildPod(job, "attempt-rejected-0"), rejectedChildPod(job, "attempt-rejected-1")}
				}, int32(1)),
			// The pods come from another cache than the Job, so it can show more rejections than
			// status.failed counts yet.
			Entry("never below 0", int32(1), int32(1),
				func(job *batchv1.Job) []client.Object {
					return []client.Object{rejectedChildPod(job, "attempt-rejected-0"), rejectedChildPod(job, "attempt-rejected-1")}
				}, int32(0)),
			Entry("a disruption casualty is not subtracted", int32(1), int32(0),
				func(job *batchv1.Job) []client.Object {
					return []client.Object{failedChildPod(job, "attempt-evicted", time.Minute, true)}
				}, int32(1)),
			Entry("a node crash the kubelet could not account for is not subtracted", int32(1), int32(0),
				func(job *batchv1.Job) []client.Object {
					pod := failedChildPod(job, "attempt-unknown", time.Minute, false)
					pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "tuning-apply", State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "ContainerStatusUnknown"},
					}}}
					return []client.Object{pod}
				}, int32(1)),
			// No container statuses, the rejection shape, but killed by its own deadline.
			Entry("an attempt killed by its own deadline is not subtracted", int32(1), int32(0),
				func(job *batchv1.Job) []client.Object {
					pod := failedChildPod(job, "attempt-timeout", time.Minute, false)
					pod.Status.Reason = podReasonDeadlineExceeded
					return []client.Object{pod}
				}, int32(1)),
			// status.failed does not include a pod until the Job controller has counted it.
			Entry("a rejection the Job has not counted yet is not subtracted", int32(1), int32(0),
				func(job *batchv1.Job) []client.Object {
					pod := rejectedChildPod(job, "attempt-rejected-0")
					pod.UID = "rejected-uid"
					job.Status.UncountedTerminatedPods = &batchv1.UncountedTerminatedPods{Failed: []types.UID{pod.UID}}
					return []client.Object{pod}
				}, int32(1)),
		)
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

	// An interrupt ended for its restart limit loses its pod when the Job fails, and a container
	// waiting to restart is the usual shape at that moment. Its last run's logs are what say why.
	Describe("snapshotting a container that ran before", func() {
		waitingAfterARun := func(job *batchv1.Job, reason string) *corev1.Pod {
			return &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "tuning-interrupt-pod", Namespace: namespace,
					Labels: map[string]string{batchControllerUIDLabel: string(job.UID)},
				},
				Spec: corev1.PodSpec{NodeName: nodeName},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{{
						Name:                 InterruptContainerName,
						State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "waiting message"}},
						LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
						RestartCount:         4,
					}},
				},
			}
		}
		failureTargetJob := func() *batchv1.Job {
			return packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobFailureTarget, batchv1.JobReasonBackoffLimitExceeded))
		}

		It("takes the last run's logs for an interrupt in CrashLoopBackOff", func() {
			job := failureTargetJob()
			r := newReconciler(job, waitingAfterARun(job, "CrashLoopBackOff"))

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			snap := getJob(r, job.Name).Annotations[annotationLastLogs]
			Expect(snap).To(ContainSubstring(InterruptContainerName))
			Expect(snap).To(ContainSubstring("fake logs"))
			Expect(snap).ToNot(ContainSubstring("CrashLoopBackOff"))
		})

		// After a reboot the container can be stuck before it starts again, and the logs the
		// kubelet still has are from before the reboot: the waiting reason is the current problem.
		It("records the waiting reason, not stale logs, for a container that cannot start again", func() {
			job := failureTargetJob()
			r := newReconciler(job, waitingAfterARun(job, "CreateContainerConfigError"))

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			snap := getJob(r, job.Name).Annotations[annotationLastLogs]
			Expect(snap).To(ContainSubstring("CreateContainerConfigError"))
			Expect(snap).ToNot(ContainSubstring("fake logs"))
		})

		It("falls back to the waiting reason when the logs cannot be read", func() {
			job := failureTargetJob()
			pod := waitingAfterARun(job, "CrashLoopBackOff")
			scheme := runtime.NewScheme()
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod).Build()
			// No clientset: every log read fails.
			r := NewJobReconciler(c, c, nil, events.NewFakeRecorder(50), validOpts().JobOperatorOptions)

			_, err := r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getJob(r, job.Name).Annotations[annotationLastLogs]).To(ContainSubstring("CrashLoopBackOff: waiting message"))
		})
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

	// The pod watch ends a crash-looping interrupt by dropping its Job's backoffLimit to 0, and
	// OnFailure leaves no Failed attempt pod behind: the failed runs were restarted in place and
	// the Job controller deletes the live pod when it gives up. The annotation is the evidence.
	DescribeTable("an interrupt Job failed with BackoffLimitExceeded and no pods left",
		func(interrupt, verdict bool, expected v1alpha1.State) {
			stage := v1alpha1.StageApply
			if interrupt {
				stage = v1alpha1.StageInterrupt
			}
			node := nodeWithState(v1alpha1.StateInProgress, stage)
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			sn.SetStatus(v1alpha1.StatusInProgress)

			job := packageJob(stage, interrupt, trueCondition(batchv1.JobFailed, batchv1.JobReasonBackoffLimitExceeded))
			if verdict {
				job.Annotations[annotationRestartLimitExceeded] = "4"
			}
			r := newReconciler(node, job)

			_, err = r.JobReconcile(ctx, job)
			Expect(err).ToNot(HaveOccurred())

			Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(expected))
			var got corev1.Node
			Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
			gotNode, err := wrapper.NewSkyhookNodeOnly(&got, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			if expected == v1alpha1.StateErroring {
				Expect(gotNode.Status()).To(Equal(v1alpha1.StatusErroring))
			} else {
				Expect(gotNode.Status()).To(Equal(v1alpha1.StatusInProgress))
			}

			// Kept and marked either way; with the entry at (stage, erroring) it is the timeout
			// marker the sweep leaves in place.
			kept := getJob(r, job.Name)
			Expect(kept.Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
		},
		Entry("is a timeout when the pod watch ended it", true, true, v1alpha1.StateErroring),
		Entry("without that verdict, still needs pod evidence", true, false, v1alpha1.StateInProgress),
		Entry("the verdict counts only on an interrupt Job", false, true, v1alpha1.StateInProgress),
	)

	// The realistic shape: the pod watch recorded each failed run on the package before ending the
	// Job, so the entry already reads erroring when the Job fails. The node must still be marked.
	It("marks the node erroring for an interrupt Job the pod watch ended, its package already erroring", func() {
		node := nodeWithState(v1alpha1.StateErroring, v1alpha1.StageInterrupt)
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		sn.SetStatus(v1alpha1.StatusInProgress)
		job := packageJob(v1alpha1.StageInterrupt, true, trueCondition(batchv1.JobFailed, batchv1.JobReasonBackoffLimitExceeded))
		job.Annotations[annotationRestartLimitExceeded] = "4"
		r := newReconciler(node, job)

		_, err = r.JobReconcile(ctx, job)
		Expect(err).ToNot(HaveOccurred())

		var got corev1.Node
		Expect(r.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
		gotNode, err := wrapper.NewSkyhookNodeOnly(&got, skyhookName)
		Expect(err).ToNot(HaveOccurred())
		Expect(gotNode.Status()).To(Equal(v1alpha1.StatusErroring))
		Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateErroring))
		Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
	})

	Describe("JobReconciler", func() {

		It("reconciles the Job named by the request", func() {
			node := nodeWithState(v1alpha1.StateInProgress, v1alpha1.StageApply)
			job := packageJob(v1alpha1.StageApply, false, trueCondition(batchv1.JobComplete, ""))
			r := newReconciler(node, job)

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
				Namespace: namespace, Name: job.Name,
			}})
			Expect(err).ToNot(HaveOccurred())

			// Went through JobReconcile: completion recorded and the Job marked.
			Expect(getNodeState(r)[pkgRef.GetUniqueName()].State).To(Equal(v1alpha1.StateComplete))
			Expect(getJob(r, job.Name).Annotations).To(HaveKeyWithValue(annotationStateRecorded, annotationValueTrue))
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
