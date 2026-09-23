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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Jobs execution swap", func() {
	const (
		skyhookName = "gpu-init"
		nodeName    = "worker-1"
		namespace   = "skyhook"
		image       = "ghcr.io/nvidia/skyhook-packages/tuning:1.0.0"
	)
	pkg := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"}, Image: image}

	nameLabel := fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX)
	packageLabel := fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)
	nodeLabel := fmt.Sprintf("%s/node", v1alpha1.METADATA_PREFIX)
	stageLabel := fmt.Sprintf("%s/stage", v1alpha1.METADATA_PREFIX)

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

	newReconciler := func(objects ...client.Object) (*SkyhookReconciler, client.WithWatch) {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
			WithIndex(&corev1.Pod{}, fieldSelectorNodeName, func(obj client.Object) []string {
				pod, ok := obj.(*corev1.Pod)
				if !ok {
					return nil
				}
				return []string{pod.Spec.NodeName}
			}).Build()
		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), events.NewFakeRecorder(50), validOpts())
		Expect(err).ToNot(HaveOccurred())
		return r, c
	}
	newPodWatch := func(objects ...client.Object) (*PodReconciler, client.WithWatch) {
		_, c := newReconciler(objects...)
		return NewPodReconciler(c, c, k8sfake.NewClientset(), events.NewFakeRecorder(50), namespace), c
	}

	stageJob := func(stage v1alpha1.Stage, conditions ...batchv1.JobCondition) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tuning-1-0-0-" + string(stage), Namespace: namespace, UID: "job-uid",
				Labels: map[string]string{
					nameLabel:    skyhookName,
					packageLabel: "tuning-1.0.0",
					nodeLabel:    nodeName,
				},
			},
			Spec:   batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeName: nodeName}}},
			Status: batchv1.JobStatus{Conditions: conditions},
		}
	}

	jobOwnedPod := func(name string, statuses ...corev1.ContainerStatus) *corev1.Pod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespace,
				Labels: map[string]string{
					nameLabel:         skyhookName,
					packageLabel:      "tuning-1.0.0",
					batchJobNameLabel: "tuning-1-0-0-apply",
				},
			},
			Spec:   corev1.PodSpec{NodeName: nodeName},
			Status: corev1.PodStatus{InitContainerStatuses: statuses},
		}
		// Child pods inherit the package annotation from the Job pod template; the in-flight
		// erroring path reads it off the pod, so the fixture must carry it.
		Expect(SetPackages(pod, &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName}}, image, v1alpha1.StageApply, pkg)).To(Succeed())
		return pod
	}

	It("creates a package Job (not a raw pod) for a fresh stage", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		scr := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: namespace, Generation: 1},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"tuning": *pkg}},
		}
		r, c := newReconciler(node, scr)
		skyhookNode, err := wrapper.NewSkyhookNode(node, scr)
		Expect(err).ToNot(HaveOccurred())

		Expect(r.ApplyPackage(ctx, GinkgoLogr, nil, skyhookNode, pkg, false)).To(Succeed())

		var jobs batchv1.JobList
		Expect(c.List(ctx, &jobs, client.InNamespace(namespace))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		Expect(jobs.Items[0].Labels).To(HaveKeyWithValue(nameLabel, skyhookName))
		Expect(jobs.Items[0].Labels).To(HaveKeyWithValue(stageLabel, string(v1alpha1.StageApply)))

		// no raw package pod is created by the swap
		var pods corev1.PodList
		Expect(c.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed())
		Expect(pods.Items).To(BeEmpty())
	})

	Describe("JobExists", func() {
		It("counts an unfinished Job", func() {
			r, _ := newReconciler(stageJob(v1alpha1.StageApply))
			exists, err := r.JobExists(ctx, nodeName, skyhookName, pkg)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeTrue())
		})

		It("does not count a finished Job (so the stage can re-run once it is cleaned)", func() {
			r, _ := newReconciler(stageJob(v1alpha1.StageApply, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}))
			exists, err := r.JobExists(ctx, nodeName, skyhookName, pkg)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeFalse())
		})
	})

	Describe("pod watch", func() {
		It("does not delete a Job-owned pod on success (JobReconcile owns completion)", func() {
			pod := jobOwnedPod("tuning-pod-ok", corev1.ContainerStatus{
				Name: "apply", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			})
			r, c := newPodWatch(pod)
			_, err := r.PodReconcile(ctx, pod)
			Expect(err).ToNot(HaveOccurred())
			// still present: completion and cleanup belong to JobReconcile, never this watch
			Expect(c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pod.Name}, &corev1.Pod{})).To(Succeed())
		})

		It("stays silent for a Job-owned pod killed by a disruption", func() {
			pod := jobOwnedPod("tuning-pod-evicted", corev1.ContainerStatus{
				Name: "apply", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}},
			})
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}}
			r, c := newPodWatch(pod)
			_, err := r.PodReconcile(ctx, pod)
			Expect(err).ToNot(HaveOccurred())
			Expect(c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pod.Name}, &corev1.Pod{})).To(Succeed())
		})

		It("records erroring from a Job-owned pod's genuine step failure", func() {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			Expect(sn.Upsert(pkg.PackageRef, image, v1alpha1.StateInProgress, v1alpha1.StageApply, 0, "")).To(Succeed())

			pod := jobOwnedPod("tuning-pod-failed", corev1.ContainerStatus{
				Name: "apply", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
			})
			r, c := newPodWatch(node, pod)
			_, err = r.PodReconcile(ctx, pod)
			Expect(err).ToNot(HaveOccurred())

			// the pod carries the package annotation (from the template), so node state records erroring
			var got corev1.Node
			Expect(c.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
			gsn, err := wrapper.NewSkyhookNodeOnly(&got, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			state, err := gsn.State()
			Expect(err).ToNot(HaveOccurred())
			Expect(state[pkg.GetUniqueName()].State).To(Equal(v1alpha1.StateErroring))
		})

		// The hung-stage case the per-attempt deadline exists for. The stuck container never
		// started, so it has no exit code, and podFailureIsGenuine rejects every shape it can
		// take (Waiting, or the kubelet's ContainerStatusUnknown rewrite on termination). The
		// pod-level reason is the only evidence there is; without it a hang would read
		// in_progress until the whole retry budget burned down.
		It("records erroring from a per-attempt deadline whose container never started", func() {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			Expect(sn.Upsert(pkg.PackageRef, image, v1alpha1.StateInProgress, v1alpha1.StageApply, 0, "")).To(Succeed())

			pod := jobOwnedPod("tuning-pod-timeout", corev1.ContainerStatus{
				Name:  "apply",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
			})
			Expect(podFailureIsGenuine(pod)).To(BeFalse(), "precondition: no container verdict to read")
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = podReasonDeadlineExceeded

			r, c := newPodWatch(node, pod)
			_, err = r.PodReconcile(ctx, pod)
			Expect(err).ToNot(HaveOccurred())

			var got corev1.Node
			Expect(c.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
			gsn, err := wrapper.NewSkyhookNodeOnly(&got, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			state, err := gsn.State()
			Expect(err).ToNot(HaveOccurred())
			Expect(state[pkg.GetUniqueName()].State).To(Equal(v1alpha1.StateErroring))
		})

		// A failing Job under restartPolicy Never mints a fresh pod per attempt, so every one of
		// these lands on the watch. If the watch could write an entry the reset just cleared, the
		// package would be re-pinned to the cleared stage and the reset could never take effect.
		DescribeTable("never creates or regresses a node-state entry",
			func(seed func(wrapper.SkyhookNodeOnly)) {
				node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
				sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
				Expect(err).ToNot(HaveOccurred())
				seed(sn)

				pod := jobOwnedPod("tuning-pod-failed", corev1.ContainerStatus{
					Name: "apply", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
				})
				r, c := newPodWatch(node, pod)
				_, err = r.PodReconcile(ctx, pod)
				Expect(err).ToNot(HaveOccurred())

				var got corev1.Node
				Expect(c.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
				gsn, err := wrapper.NewSkyhookNodeOnly(&got, skyhookName)
				Expect(err).ToNot(HaveOccurred())
				after, err := gsn.State()
				Expect(err).ToNot(HaveOccurred())

				before, err := sn.State()
				Expect(err).ToNot(HaveOccurred())
				Expect(after).To(Equal(before))
			},
			Entry("entry cleared by a node reset", func(wrapper.SkyhookNodeOnly) {}),
			Entry("entry already moved past the pod's stage", func(sn wrapper.SkyhookNodeOnly) {
				Expect(sn.Upsert(pkg.PackageRef, image, v1alpha1.StateInProgress, v1alpha1.StageConfig, 0, "")).To(Succeed())
			}),
			Entry("entry already recorded complete for this stage", func(sn wrapper.SkyhookNodeOnly) {
				Expect(sn.Upsert(pkg.PackageRef, image, v1alpha1.StateComplete, v1alpha1.StageApply, 0, "")).To(Succeed())
			}),
		)
	})

	// handleExistingJob is the AlreadyExists-on-create path, and it must reach the same verdict
	// as shouldDeleteFinishedJob for the same Job — the two are mirrors, and this path is the one
	// that runs first: a finished Job does not satisfy JobExists, so the next pass tries to create
	// over its deterministic name while JobReconcile may not have processed it yet.
	Describe("handleExistingJob", func() {
		specWith := func(p v1alpha1.Package) *v1alpha1.NodeWright {
			return &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: namespace, Generation: 1},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"tuning": p}},
			}
		}

		nodeWithEntry := func(state v1alpha1.State, stage v1alpha1.Stage) (*corev1.Node, *v1alpha1.NodeWright) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			scr := specWith(*pkg)
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).ToNot(HaveOccurred())
			Expect(sn.Upsert(pkg.PackageRef, image, state, stage, 0, "")).To(Succeed())
			return node, scr
		}

		resolve := func(existing *batchv1.Job, state v1alpha1.State) bool {
			node, scr := nodeWithEntry(state, v1alpha1.StageApply)
			r, c := newReconciler(node, scr, existing)
			skyhookNode, err := wrapper.NewSkyhookNode(node, scr)
			Expect(err).ToNot(HaveOccurred())

			Expect(r.handleExistingJob(ctx, existing, pkg, skyhookNode, v1alpha1.StageApply)).To(Succeed())

			var got batchv1.Job
			err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: existing.Name}, &got)
			if apierrors.IsNotFound(err) {
				return false
			}
			Expect(err).ToNot(HaveOccurred())
			// foreground deletion leaves the object visible until its children are gone
			return got.DeletionTimestamp == nil
		}

		// Built from a package rather than by stageJob: the spec condition on the timed-out
		// carve-out runs the same comparison validation does, and an empty pod template matches
		// no package at all.
		failedFor := func(p v1alpha1.Package, processed bool) *batchv1.Job {
			job := createJobFromPackage(validOpts(), &p, wrapper.NewSkyhookWrapper(specWith(p)), nodeName, v1alpha1.StageApply)
			job.Status.Conditions = []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded,
			}}
			if processed {
				job.Annotations[annotationStateRecorded] = annotationValueTrue
			}
			return job
		}
		failed := func(processed bool) *batchv1.Job { return failedFor(*pkg, processed) }

		It("keeps a terminal Failed Job JobReconcile has not processed yet", func() {
			// Deleting here would take the retained attempts with it and restart the stage on a
			// fresh budget, before anything had a chance to write erroring.
			Expect(resolve(failed(false), v1alpha1.StateInProgress)).To(BeTrue())
		})
		It("keeps a timed-out Job while its entry sits erroring", func() {
			Expect(resolve(failed(true), v1alpha1.StateErroring)).To(BeTrue())
		})
		It("deletes a processed Failed Job whose entry never reached erroring", func() {
			// The kubelet-refused-every-attempt case: nothing timed the stage out, so the name
			// frees and the stage re-runs.
			Expect(resolve(failed(true), v1alpha1.StateInProgress)).To(BeFalse())
		})
		It("deletes a timed-out Job built from a superseded spec, so an edit takes effect", func() {
			// resolve() reconciles against a CR holding the current pkg, so a Job built from an
			// older image is what the user just edited away from.
			old := *pkg
			old.Image = "ghcr.io/nvidia/skyhook-packages/tuning-broken"
			Expect(resolve(failedFor(old, true), v1alpha1.StateErroring)).To(BeFalse())
		})
	})

	Describe("rerun predicate", func() {
		pkgSky := &PackageSkyhook{PackageRef: pkg.PackageRef, Skyhook: skyhookName, Stage: v1alpha1.StageApply}

		// The spec-drift arm of the predicate compares a Job against the packages in the CR, so
		// these need a CR to compare with. skyWith(*pkg) is the matching case.
		skyWith := func(p v1alpha1.Package) SkyhookNodes {
			scr := &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: namespace, Generation: 1},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"tuning": p}},
			}
			return &skyhookNodes{skyhook: wrapper.NewSkyhookWrapper(scr), compartments: make(map[string]*wrapper.Compartment)}
		}
		sky := skyWith(*pkg)

		// Built by the real builder, not stageJob: the spec-drift check runs the same comparison
		// validation does, and stageJob's empty pod template matches no package at all.
		builtJob := func(condition batchv1.JobCondition) *batchv1.Job {
			job := createJobFromPackage(validOpts(), pkg, sky.GetSkyhook(), nodeName, v1alpha1.StageApply)
			job.Status.Conditions = []batchv1.JobCondition{condition}
			job.Annotations[annotationStateRecorded] = annotationValueTrue
			return job
		}

		processed := func() *batchv1.Job {
			job := stageJob(v1alpha1.StageApply, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
			job.Annotations = map[string]string{annotationStateRecorded: annotationValueTrue}
			return job
		}
		erroringEntry := v1alpha1.NodeState{pkg.GetUniqueName(): {Name: "tuning", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateErroring}}
		completeEntry := v1alpha1.NodeState{pkg.GetUniqueName(): {Name: "tuning", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}}

		It("deletes a processed finished Job once its stage is no longer recorded done", func() {
			r, _ := newReconciler()
			Expect(r.shouldDeleteFinishedJob(processed(), pkgSky, v1alpha1.NodeState{}, sky)).To(BeTrue())
		})
		It("keeps a processed finished Job while its stage is recorded complete", func() {
			r, _ := newReconciler()
			Expect(r.shouldDeleteFinishedJob(processed(), pkgSky, completeEntry, sky)).To(BeFalse())
		})
		// This predicate is what makes a lost node-state update self-healing rather than a stall:
		// a completion clobbered back to (this stage, in_progress) no longer reads as recorded
		// done, so the finished Job is torn down and the stage runs again. Distinct from the
		// absent-entry case above — that is a reset or an uninstall, this is a regression.
		It("deletes a processed finished Job whose entry was reverted to this stage in_progress", func() {
			r, _ := newReconciler()
			reverted := v1alpha1.NodeState{pkg.GetUniqueName(): {Name: "tuning", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateInProgress}}
			Expect(r.shouldDeleteFinishedJob(processed(), pkgSky, reverted, sky)).To(BeTrue())
		})
		It("keeps a processed successful uninstall Job after its node-state entry is removed", func() {
			r, _ := newReconciler()
			uninstall := *pkgSky
			uninstall.Stage = v1alpha1.StageUninstall
			job := stageJob(v1alpha1.StageUninstall, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
			job.Annotations = map[string]string{annotationStateRecorded: annotationValueTrue}
			Expect(r.shouldDeleteFinishedJob(job, &uninstall, v1alpha1.NodeState{}, sky)).To(BeFalse())
		})
		It("deletes a processed failed uninstall Job after its node-state entry is removed", func() {
			r, _ := newReconciler()
			uninstall := *pkgSky
			uninstall.Stage = v1alpha1.StageUninstall
			job := stageJob(v1alpha1.StageUninstall, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded})
			job.Annotations = map[string]string{annotationStateRecorded: annotationValueTrue}
			Expect(r.shouldDeleteFinishedJob(job, &uninstall, v1alpha1.NodeState{}, sky)).To(BeTrue())
		})
		// Both outcomes wait for the marker. A finite backoffLimit can take a Job from first
		// failure to terminal in about a minute, so deleting a Failed Job before JobReconcile
		// has had its chance to write erroring would race the timeout into a fresh, doomed attempt.
		DescribeTable("never deletes a finished Job JobReconcile has not processed yet",
			func(condition batchv1.JobCondition) {
				r, _ := newReconciler()
				unprocessed := stageJob(v1alpha1.StageApply, condition)
				Expect(r.shouldDeleteFinishedJob(unprocessed, pkgSky, v1alpha1.NodeState{}, sky)).To(BeFalse())
			},
			Entry("Complete, holding an unrecorded completion",
				batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}),
			Entry("Failed, not yet given its chance to write erroring",
				batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded}),
		)
		DescribeTable("keeps a timed-out Job while its entry sits erroring, whatever ended it",
			func(reason string) {
				r, _ := newReconciler()
				timedOut := builtJob(batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason})
				Expect(r.shouldDeleteFinishedJob(timedOut, pkgSky, erroringEntry, sky)).To(BeFalse())
			},
			Entry("attempts exhausted", batchv1.JobReasonBackoffLimitExceeded),
			Entry("whole-stage deadline (interrupt Jobs)", batchv1.JobReasonDeadlineExceeded),
		)
		// Without this a stage that has spent its retries sits behind its terminal Job until the
		// failure TTL, so editing the package to fix what broke it does nothing for up to 24h.
		DescribeTable("deletes a timed-out Job when the package spec changed, so the fix takes effect",
			func(edit func(*v1alpha1.Package)) {
				r, _ := newReconciler()
				timedOut := builtJob(batchv1.JobCondition{
					Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded,
				})
				edited := *pkg
				edit(&edited)
				Expect(r.shouldDeleteFinishedJob(timedOut, pkgSky, erroringEntry, skyWith(edited))).To(BeTrue())
			},
			Entry("a new image fixes the broken one", func(p *v1alpha1.Package) {
				p.Image = "ghcr.io/nvidia/skyhook-packages/tuning-fixed"
			}),
			Entry("a version bump", func(p *v1alpha1.Package) { p.Version = "1.0.1" }),
			Entry("the package left the spec entirely", func(p *v1alpha1.Package) { p.Name = "other" }),
		)
		It("deletes a Failed Job whose entry never reached erroring, so the stage re-runs", func() {
			// The kubelet-refused-every-attempt case: JobReconcile marked it without a state
			// write, so nothing timed the stage out and the sweep clears the name.
			r, _ := newReconciler()
			failed := stageJob(v1alpha1.StageApply, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded})
			failed.Annotations = map[string]string{annotationStateRecorded: annotationValueTrue}
			inProgress := v1alpha1.NodeState{pkg.GetUniqueName(): {Name: "tuning", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateInProgress}}
			Expect(r.shouldDeleteFinishedJob(failed, pkgSky, inProgress, sky)).To(BeTrue())
		})
	})

	Describe("pause suspend cascade", func() {
		// buildSkyhookNodes wraps a NodeWright (with no packages, so any Job is stale) as the
		// SkyhookNodes the suspend/resume helpers take. The nodes matter only for the
		// ValidateRunningPackages ordering test; the plain helpers read only the Skyhook name.
		buildSkyhookNodes := func(nodes ...corev1.Node) SkyhookNodes {
			scr := v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: namespace}}
			state, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{scr}},
				&corev1.NodeList{Items: nodes},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.skyhooks).To(HaveLen(1))
			return state.skyhooks[0]
		}

		// buildDisabledSkyhookNodes is the same wrapper with the disable annotation set, which is
		// what IsDisabled reads.
		buildDisabledSkyhookNodes := func() SkyhookNodes {
			scr := v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{
				Name: skyhookName, Namespace: namespace,
				Annotations: map[string]string{v1alpha1.METADATA_PREFIX + "/disable": "true"},
			}}
			state, err := BuildState(
				&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{scr}},
				&corev1.NodeList{},
				&v1alpha1.DeploymentPolicyList{},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.skyhooks).To(HaveLen(1))
			Expect(state.skyhooks[0].IsDisabled()).To(BeTrue(), "fixture must actually read as disabled")
			return state.skyhooks[0]
		}

		suspendVal := func(c client.WithWatch, name string) *bool {
			var got batchv1.Job
			Expect(c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &got)).To(Succeed())
			return got.Spec.Suspend
		}

		invalidate := func(job *batchv1.Job) {
			Expect(SetPackages(job, &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName}}, image, v1alpha1.StageApply, pkg)).To(Succeed())
			Expect(InvalidatePackage(job)).To(Succeed())
		}

		It("suspends an unfinished Job when the Skyhook is paused", func() {
			job := stageJob(v1alpha1.StageApply)
			r, c := newReconciler(job)
			Expect(r.suspendUnfinishedJobs(ctx, buildSkyhookNodes())).To(Succeed())
			Expect(suspendVal(c, job.Name)).To(HaveValue(BeTrue()))
		})

		It("does not suspend a finished Job", func() {
			job := stageJob(v1alpha1.StageApply, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
			r, c := newReconciler(job)
			Expect(r.suspendUnfinishedJobs(ctx, buildSkyhookNodes())).To(Succeed())
			Expect(suspendVal(c, job.Name)).To(BeNil())
		})

		It("does not suspend an invalid Job (already being reaped)", func() {
			job := stageJob(v1alpha1.StageApply)
			invalidate(job)
			r, c := newReconciler(job)
			Expect(r.suspendUnfinishedJobs(ctx, buildSkyhookNodes())).To(Succeed())
			Expect(suspendVal(c, job.Name)).To(BeNil())
		})

		It("is idempotent — leaves an already-suspended Job unchanged", func() {
			job := stageJob(v1alpha1.StageApply)
			job.Spec.Suspend = ptr(true)
			r, c := newReconciler(job)
			Expect(r.suspendUnfinishedJobs(ctx, buildSkyhookNodes())).To(Succeed())
			Expect(suspendVal(c, job.Name)).To(HaveValue(BeTrue()))
		})

		It("resumes a suspended Job when the Skyhook is no longer paused", func() {
			job := stageJob(v1alpha1.StageApply)
			job.Spec.Suspend = ptr(true)
			r, c := newReconciler(job)
			Expect(r.resumeSuspendedJobs(ctx, buildSkyhookNodes())).To(Succeed())
			Expect(suspendVal(c, job.Name)).To(HaveValue(BeFalse()))
		})

		// Disable must not un-suspend what pause stopped. Clearing the pause annotation and setting
		// disable in one edit used to resume every suspended Job, so disabling a paused Skyhook
		// restarted it — disable ending up strictly weaker than pause.
		It("does not resume a disabled Skyhook's suspended Jobs", func() {
			job := stageJob(v1alpha1.StageApply)
			job.Spec.Suspend = ptr(true)
			r, c := newReconciler(job)

			Expect(r.resumeSuspendedJobs(ctx, buildDisabledSkyhookNodes())).To(Succeed())

			Expect(suspendVal(c, job.Name)).To(HaveValue(BeTrue()),
				"a disabled Skyhook must never restart work pause stopped")
		})

		It("leaves an invalid suspended Job suspended (validation reaps it before resume clears it)", func() {
			job := stageJob(v1alpha1.StageApply)
			job.Spec.Suspend = ptr(true)
			invalidate(job)
			r, c := newReconciler(job)
			Expect(r.resumeSuspendedJobs(ctx, buildSkyhookNodes())).To(Succeed())
			// still suspended — resume must not un-suspend a stale-spec Job before it is reaped
			Expect(suspendVal(c, job.Name)).To(HaveValue(BeTrue()))
		})

		It("is a no-op with only a legacy pod present (legacy pods cannot suspend)", func() {
			legacy := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "legacy-tuning", Namespace: namespace,
					Labels: map[string]string{nameLabel: skyhookName, packageLabel: "tuning-1.0.0"},
				},
				Spec: corev1.PodSpec{NodeName: nodeName},
			}
			r, _ := newReconciler(legacy)
			Expect(r.suspendUnfinishedJobs(ctx, buildSkyhookNodes())).To(Succeed())
		})

		It("invalidates a stale suspended Job without clearing suspend (resume ordering guard)", func() {
			// A suspended Job whose package is no longer in spec (edited while paused). Validation must
			// mark it invalid and return update=true — which makes the reconcile loop early-return
			// BEFORE resumeSuspendedJobs runs — and must not clear suspend, so no stale-spec attempt
			// launches before JobReconcile reaps it.
			job := stageJob(v1alpha1.StageApply)
			job.Spec.Suspend = ptr(true)
			Expect(SetPackages(job, &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName}}, image, v1alpha1.StageApply, pkg)).To(Succeed())

			node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			r, c := newReconciler(job)
			// empty Packages spec => the Job matches nothing => stale
			update, err := r.ValidateRunningPackages(ctx, buildSkyhookNodes(node))
			Expect(err).ToNot(HaveOccurred())
			Expect(update).To(BeTrue())

			var got batchv1.Job
			Expect(c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: job.Name}, &got)).To(Succeed())
			invalid, err := IsInvalidPackage(&got)
			Expect(err).ToNot(HaveOccurred())
			Expect(invalid).To(BeTrue())                     // validation marked it invalid...
			Expect(got.Spec.Suspend).To(HaveValue(BeTrue())) // ...and left suspend untouched
		})
	})
})
