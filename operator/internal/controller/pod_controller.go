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

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// PodReconciler watches package pods on their own watch and workqueue. It reports one thing: a
// package step that has failed while its Job is still retrying. The Job is the completion
// authority, but it stays Active until the whole retry budget is spent — attempts paced by
// backoff, each bounded by its own deadline — so without this watch a crash-looping or hung
// package would read in_progress for hours before anything surfaced.
//
// It holds its own dependencies rather than embedding SkyhookReconciler: embedding would inherit
// the heavy pass's entire method set, including a Reconcile this one has to shadow — so deleting
// the shadow would still compile and quietly run the whole-world pass on every pod event.
//
// Leaving the heavy pass's single-threaded workqueue means this no longer serializes against it,
// so the node-state write goes through patchNodeState like the Job path.
type PodReconciler struct {
	client.Client
	// uncached reads straight from the apiserver, used only to re-read a Node after a patch
	// conflict; see patchNodeState.
	uncached  client.Reader
	recorder  events.EventRecorder
	dal       dal.DAL
	namespace string
}

func NewPodReconciler(c client.Client, uncached client.Reader, clientset kubernetes.Interface, recorder events.EventRecorder, namespace string) *PodReconciler {
	return &PodReconciler{
		Client:    c,
		uncached:  uncached,
		recorder:  recorder,
		dal:       dal.New(c, clientset),
		namespace: namespace,
	}
}

// The shared Pod cache is cluster-wide for drain, so this watch must enforce its own scope.
// Job child pods inherit both package ownership labels in the operator namespace.
func ownedPod(namespace string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		podLabels := labels.Set(o.GetLabels())
		return o.GetNamespace() == namespace && podLabels.Has(nameLabel) &&
			podLabels.Has(v1alpha1.METADATA_PREFIX+"/package")
	})
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("pod").
		For(&corev1.Pod{}, builder.WithPredicates(ownedPod(r.namespace))).
		Complete(r)
}

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod, err := r.dal.GetPod(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting pod %s: %w", req.Name, err)
	}
	// Deleted between the event and this read: there is no in-flight failure left to report.
	if pod == nil {
		return ctrl.Result{}, nil
	}
	return r.PodReconcile(ctx, pod)
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.0/pkg/reconcile

func (r *PodReconciler) PodReconcile(ctx context.Context, pod *corev1.Pod) (ctrl.Result, error) {
	// Every package pod is a Job's child now, so completion and cleanup belong to JobReconcile
	// and the Job controller. This watch exists only to surface in-flight erroring: a step that
	// fails mid-Job shows up here before the Job itself goes terminal. It never deletes a pod or
	// records completion, which would race the Job path for the same node-state key.
	//
	// Erroring is guarded: a terminating pod (pause suspension, a sweep, a manual delete) or a
	// disruption casualty (eviction/preemption/PodGC) has no failure verdict and stays silent;
	// only a genuine terminal step failure marks erroring.
	if pod.DeletionTimestamp != nil || hasDisruptionTarget(pod) {
		return ctrl.Result{}, nil
	}

	_, state, restarts := containerExitedSuccessfully(pod)
	if !podDeadlineExceeded(pod) && (state != containerStateFailed || !podFailureIsGenuine(pod)) {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, r.recordPodErroring(ctx, pod, restarts)
}

// podReasonDeadlineExceeded is the pod-level status reason the kubelet's active-deadline handler
// sets when a pod outlives its own spec.activeDeadlineSeconds. Nothing else sets it.
//
// Declared here rather than taken from the SDK, which has no constant for it: core/v1 exports
// PodReason* only for the PodScheduled and DisruptionTarget conditions, and this is a
// status.reason the kubelet writes. batchv1.JobReasonDeadlineExceeded happens to carry the same
// string but is the Job controller's condition reason on a different object — binding to it would
// couple this check to an unrelated surface that is free to diverge.
const podReasonDeadlineExceeded = "DeadlineExceeded"

// podDeadlineExceeded reports whether the pod was killed by its own per-attempt deadline.
//
// This is evidence the container statuses cannot carry, which is why it is checked separately
// rather than folded into podFailureIsGenuine. When the stuck container never started — an
// unpullable image, a missing configmap, exactly the hung-stage case the deadline exists for —
// the pod is Failed with that container still Waiting, or with the kubelet's
// ContainerStatusUnknown rewrite on termination, and podFailureIsGenuine rejects both shapes.
// Without this the first timeout would write nothing and a hang would read in_progress until the
// entire retry budget burned down.
func podDeadlineExceeded(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == podReasonDeadlineExceeded
}

// recordPodErroring puts the package's entry at (stage, erroring) from a failed attempt pod. The write
// is optimistic-locked and retried because this controller runs concurrently with the heavy pass
// and both write nodewright.nvidia.com/nodeState_<name>, the single annotation key holding every
// package; an unconditional patch would silently drop whichever write landed second.
func (r *PodReconciler) recordPodErroring(ctx context.Context, pod *corev1.Pod, restarts int32) error {
	packagePtr, err := GetPackage(pod)
	if err != nil {
		return fmt.Errorf("getting package from pod %s: %w", pod.Name, err)
	}
	// Not an error: the ownership labels do not guarantee the package annotation is present.
	// A pod without that annotation is simply not ours to record. Returning an error here requeues it
	// forever with backoff, logging on every attempt, and no retry can ever add the
	// annotation.
	if packagePtr == nil {
		return nil
	}

	return patchNodeState(ctx, r.dal, r.uncached, r.Client, pod.Spec.NodeName, func(node *corev1.Node) (bool, error) {
		skyhookNode, err := wrapper.NewSkyhookNodeOnly(node, packagePtr.Skyhook)
		if err != nil {
			return false, fmt.Errorf("creating node wrapper for pod %s: %w", pod.Name, err)
		}

		record, err := shouldRecordPodErroring(skyhookNode, packagePtr)
		if err != nil || !record {
			return false, err
		}

		if err := skyhookNode.Upsert(packagePtr.PackageRef, packagePtr.Image,
			v1alpha1.StateErroring, packagePtr.Stage, restarts, packagePtr.ContainerSHA); err != nil {
			return false, fmt.Errorf("upserting erroring state for pod %s: %w", pod.Name, err)
		}
		skyhookNode.SetStatus(v1alpha1.StatusErroring)

		if !skyhookNode.Changed() {
			return false, nil
		}

		r.recorder.Eventf(node, nil, EventTypeNormal, EventsReasonSkyhookApply, "UpdateNodeState",
			"Package [%s:%s] state %s on [nodewright:%s]", packagePtr.Name, packagePtr.Version, v1alpha1.StateErroring, packagePtr.Skyhook)
		return true, nil
	})
}

// podFailureIsGenuine reports whether the pod's first failing init container is a real terminal
// step failure — a nonzero exit (including OOMKilled), or an interrupt Job's CrashLoopBackOff —
// rather than a kubelet-couldn't-tell node-crash artifact (ContainerStatusUnknown) or an
// admission rejection (no container statuses).
func podFailureIsGenuine(pod *corev1.Pod) bool {
	for _, s := range pod.Status.InitContainerStatuses {
		switch {
		case s.State.Terminated != nil && s.State.Terminated.ExitCode == 0:
			continue // succeeded step, keep looking down the chain
		case s.State.Terminated != nil:
			return s.State.Terminated.Reason != "ContainerStatusUnknown"
		case s.State.Waiting != nil && s.State.Waiting.Reason == "CrashLoopBackOff":
			return true
		default:
			return false // an init container still running/pending: no terminal failure yet
		}
	}
	return false
}

// podFailedGenuinely reports whether a Failed pod carries the package's own failure verdict — a
// per-attempt deadline kill, or a genuine terminal step failure — rather than a disruption
// casualty or a kubelet admission rejection.
//
// Every site that asks "which attempts are the package's failures?" must use this one predicate,
// or they disagree about the same pod. When the archive pruner used the looser
// Failed-and-not-disrupted test, a real failure sandwiched between two admission rejections was
// prunable while both rejections survived; the terminal classifier then saw only rejections, took
// the Job for a non-failure, and cleared a genuinely failing stage to run again.
func podFailedGenuinely(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodFailed || hasDisruptionTarget(pod) {
		return false
	}
	return podDeadlineExceeded(pod) || podFailureIsGenuine(pod)
}

// shouldRecordPodErroring is the pod watch's write guard, the analogue of JobReconcile's
// shouldRecordCompletion / recordJobErroring: this watch reports in-flight evidence, it is not an
// authority that may create or resurrect a node-state entry. Erroring is recorded only when the
// package's entry is already present, still at this pod's stage, and not already complete.
//
// The absent case is what makes the guard mandatory rather than defensive. A reset (the CLI, or a
// user clearing nodewright.nvidia.com/nodeState_<name>) removes the entry while the failing Job is
// still unfinished, and under restartPolicy Never that Job keeps minting a fresh pod per attempt,
// each landing here. An ungated write re-pins the package to the stage the reset cleared, which in
// turn makes jobIsStale read the Job as matching node state, so the sweep never invalidates it and
// JobExists blocks the new stage forever: the reset can never take effect.
//
// The already-complete case guards the other direction: pruneFailedAttempts deliberately keeps
// failed attempt pods after the Job succeeds, so a re-served archive pod would otherwise regress a
// recorded completion back to erroring.
func shouldRecordPodErroring(skyhookNode wrapper.SkyhookNodeOnly, packagePtr *PackageSkyhook) (bool, error) {
	state, err := skyhookNode.State()
	if err != nil {
		return false, fmt.Errorf("error reading node state for package %s: %w", packagePtr.GetUniqueName(), err)
	}

	return entryOpenAtStage(state, packagePtr), nil
}

const (
	containerStateSuccess string = "Success"
	containerStateWaiting string = "Waiting"
	containerStateRunning string = "Running"
	containerStateFailed  string = "Failed"
)

func containerExitedSuccessfully(pod *corev1.Pod) (string, string, int32) {

	// can be either
	// apply and check
	// or just interrupt
	// need to check all passed or all failed

	checkStatus := func(status corev1.ContainerStatus) (string, int32) {
		if status.State.Terminated != nil {
			if status.State.Terminated.ExitCode == 0 {
				return containerStateSuccess, status.RestartCount
			}
			return containerStateFailed, status.RestartCount // TODO: is this always true? or should it be configuration?
		}
		if status.State.Running != nil {
			return containerStateRunning, status.RestartCount
		}
		if status.State.Waiting != nil {
			if status.State.Waiting.Reason == "CrashLoopBackOff" {
				return containerStateFailed, status.RestartCount
			}
			return containerStateWaiting, status.RestartCount
		}
		return "", int32(0)
	}

	state := ""
	restarts := int32(0)
	name := ""
	for _, status := range pod.Status.InitContainerStatuses {

		state, restarts = checkStatus(status)
		name = status.Name

		if state == containerStateFailed {
			return name, state, restarts
		}
	}

	return name, state, restarts
}
