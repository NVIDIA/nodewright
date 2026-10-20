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
	"strconv"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// PodReconciler watches package pods on their own watch and workqueue. It reports one thing: a
// package step that has failed while its Job is still retrying, on the package's State; the
// node's Status waits for the Job. The Job is the completion authority, but it stays Active until
// the whole retry budget is spent — attempts paced by backoff, each bounded by its own deadline —
// so without this watch a crash-looping or hung package would show nothing for hours.
//
// It also ends an interrupt Job whose interrupt keeps failing; see endCrashLoopingInterrupt.
//
// It holds its own dependencies rather than embedding SkyhookReconciler: embedding would inherit
// the heavy pass's entire method set, including a Reconcile this one has to shadow — so deleting
// the shadow would still compile and quietly run the whole-world pass on every pod event.
//
// Leaving the heavy pass's single-threaded workqueue means this no longer serializes against it,
// so the node-state write goes through patchNodeState like the Job path.
type PodReconciler struct {
	client.Client
	// uncached reads straight from the apiserver: a Node or Job re-read after a patch conflict (see
	// patchNodeState), and the pod an interrupt Job is about to be ended for.
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
			podLabels.Has(packageAnnotationKey)
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
	// and the Job controller. This watch exists to surface in-flight erroring: a step that fails
	// mid-Job shows up here before the Job itself goes terminal. Its one write outside node state
	// ends the Job of an interrupt that keeps failing, which the Job then reports as failed. It
	// never deletes a pod or records completion, which would race the Job path for the same
	// node-state key.
	//
	// Erroring is guarded: a terminating pod (pause suspension, a sweep, a manual delete) or a
	// disruption casualty (eviction/preemption/PodGC) has no failure verdict and stays silent;
	// only a genuine terminal step failure marks erroring or ends an interrupt Job.
	if pod.DeletionTimestamp != nil || hasDisruptionTarget(pod) {
		return ctrl.Result{}, nil
	}

	_, state, restarts := containerExitedSuccessfully(pod)
	if !podDeadlineExceeded(pod) && (state != containerStateFailed || !podFailureIsGenuine(pod)) {
		return ctrl.Result{}, nil
	}

	// Independent, so a node-state write that keeps failing cannot also keep a crash-looping
	// interrupt from being ended. A malformed nodeState annotation still blocks both: ending the
	// Job checks the package's entry, which needs that annotation to parse.
	return ctrl.Result{}, utilerrors.NewAggregate([]error{
		r.recordPodErroring(ctx, pod, restarts),
		r.endCrashLoopingInterrupt(ctx, pod),
	})
}

// interruptRestarts returns the interrupt container's kubelet RestartCount, and whether the pod
// has an interrupt container at all. The package's recorded Restarts is not read: it is a copy
// other writers also set. RestartCount counts every restart, including ones that were not
// failures, such as an unplanned node reboot or a sandbox recreated after a containerd restart, so
// those spend the allowance too.
func interruptRestarts(pod *corev1.Pod) (int32, bool) {
	for _, s := range pod.Status.InitContainerStatuses {
		if s.Name == InterruptContainerName {
			return s.RestartCount, true
		}
	}
	return 0, false
}

// interruptFailingPastAllowance reports whether an interrupt pod is genuinely failing, with no
// deletion or disruption to excuse it, and its interrupt container has restarted at least allowance
// times.
func interruptFailingPastAllowance(pod *corev1.Pod, allowance int64) bool {
	if pod.DeletionTimestamp != nil || hasDisruptionTarget(pod) || !podFailureIsGenuine(pod) {
		return false
	}
	restarts, found := interruptRestarts(pod)
	return found && int64(restarts) >= allowance
}

// interruptRestartAllowance reads the allowance stamped on an interrupt Job at creation. A Job
// without one runs a reboot, or was created by an operator that did not bound interrupts, and is
// never ended here.
func interruptRestartAllowance(job *batchv1.Job) (int64, bool) {
	value, stamped := job.Annotations[annotationRestartAllowance]
	if !stamped {
		return 0, false
	}
	allowance, err := strconv.ParseInt(value, 10, 64)
	if err != nil || allowance < 0 {
		return 0, false
	}
	return allowance, true
}

// endCrashLoopingInterrupt fails the Job of an interrupt that has spent its restart allowance, by
// lowering the Job's backoffLimit to 0 and recording the verdict in annotationRestartLimitExceeded.
// The Job controller then fails the Job with BackoffLimitExceeded, and JobReconcile records the
// timeout and marks the node erroring exactly as for any other failed stage.
//
// Interrupt Jobs are created with an unbounded backoffLimit because under OnFailure it counts
// container restarts, and a reboot's shutdown restarts its container. This watch is the only place
// that sees each restart (they do not change the Job's status), so it applies the bound instead,
// to the Jobs stamped with an allowance: every interrupt but a reboot. It never sets the node's
// status: a node marked erroring while its Job still runs ends its batch as a failure that a later
// success cannot undo.
//
// The verdict lands on the first failed run this watch observes with the restarts at the
// allowance. With an allowance of 0 that may be the second failed run rather than the first: the
// kubelet restarts a container immediately after its first failure, and the re-read below often
// already finds it running. backoffLimit 0 also fails a Job only once a restart count is above 0,
// so a verdict on the first failure takes effect as that immediate restart begins.
func (r *PodReconciler) endCrashLoopingInterrupt(ctx context.Context, pod *corev1.Pod) error {
	if pod.Labels[interruptLabel] != interruptLabelValue {
		return nil
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.APIVersion != batchv1.SchemeGroupVersion.String() || owner.Kind != jobKind {
		return nil
	}

	cachedJob, err := r.dal.GetJob(ctx, pod.Namespace, owner.Name)
	if err != nil {
		return fmt.Errorf("getting job %s for pod %s: %w", owner.Name, pod.Name, err)
	}
	endable, err := interruptJobEndable(cachedJob, owner.UID)
	if err != nil {
		return fmt.Errorf("ending crash-looping interrupt job %s: %w", owner.Name, err)
	}
	if !endable {
		return nil
	}
	allowance, _ := interruptRestartAllowance(cachedJob)
	if restarts, _ := interruptRestarts(pod); int64(restarts) < allowance {
		return nil
	}

	node, open, err := r.packageEntryOpen(ctx, pod)
	if err != nil {
		return fmt.Errorf("ending crash-looping interrupt job %s: %w", owner.Name, err)
	}
	if !open {
		return nil
	}

	// The event may be stale: the interrupt can have restarted and be running again since. With
	// backoffLimit 0 the Job controller fails the Job on its restart count alone, so the verdict is
	// taken from the pod as the apiserver has it now.
	live, err := r.readLivePod(ctx, pod)
	if err != nil {
		return fmt.Errorf("ending crash-looping interrupt job %s: re-reading pod %s: %w", owner.Name, pod.Name, err)
	}
	if live == nil || !interruptFailingPastAllowance(live, allowance) {
		return nil
	}
	restarts, _ := interruptRestarts(live)

	var ended *batchv1.Job
	attempt := 0
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		job, err := readForPatch(ctx, func() (*batchv1.Job, error) { return cachedJob.DeepCopy(), nil },
			r.uncached, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, attempt)
		attempt++
		if err != nil {
			return fmt.Errorf("getting job %s: %w", owner.Name, err)
		}
		if endable, err := interruptJobEndable(job, owner.UID); err != nil || !endable {
			return err
		}

		patch := client.MergeFromWithOptions(job.DeepCopy(), client.MergeFromWithOptimisticLock{})
		job.Annotations[annotationRestartLimitExceeded] = strconv.Itoa(int(restarts))
		job.Spec.BackoffLimit = ptr(int32(0))
		if err := r.Patch(ctx, job, patch); err != nil {
			return err
		}
		ended = job
		return nil
	})
	if err != nil {
		return fmt.Errorf("ending crash-looping interrupt job %s for pod %s: %w", owner.Name, pod.Name, err)
	}

	if ended != nil {
		r.announceRestartLimitExceeded(ctx, pod, node, ended, restarts, allowance)
	}
	return nil
}

// readLivePod reads the pod from the apiserver, falling back to the cache when there is no
// uncached reader, the same fallback readForPatch makes. nil means the pod is gone.
func (r *PodReconciler) readLivePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	if r.uncached == nil {
		return r.dal.GetPod(ctx, pod.Namespace, pod.Name)
	}
	var live corev1.Pod
	if err := r.uncached.Get(ctx, client.ObjectKeyFromObject(pod), &live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &live, nil
}

// interruptJobEndable reports whether the pod watch may end this Job: the interrupt Job the pod
// belongs to (Job names are reused across reruns, hence the UID), stamped with an allowance, still
// running and not already on its way to failing (a FailureTarget, from its deadline say), not
// paused, not already marked invalid for the sweep to reap, and not already ended.
func interruptJobEndable(job *batchv1.Job, uid types.UID) (bool, error) {
	if job == nil || job.UID != uid || !isInterruptJob(job) || job.DeletionTimestamp != nil ||
		jobFinished(job) || hasJobCondition(job, batchv1.JobFailureTarget) || jobSuspended(job) {
		return false, nil
	}
	if _, stamped := interruptRestartAllowance(job); !stamped {
		return false, nil
	}
	if _, ended := job.Annotations[annotationRestartLimitExceeded]; ended {
		return false, nil
	}
	invalid, err := IsInvalidPackage(job)
	if err != nil {
		return false, fmt.Errorf("checking invalid package on job %s: %w", job.Name, err)
	}
	return !invalid, nil
}

// packageEntryOpen applies the pod watch's write guard, shouldRecordPodErroring, to ending the
// Job: a package whose entry a rerun or reset removed, or that has moved past this stage, is not
// this pod's to fail. It returns the node it read, for the events.
func (r *PodReconciler) packageEntryOpen(ctx context.Context, pod *corev1.Pod) (*corev1.Node, bool, error) {
	packagePtr, err := GetPackage(pod)
	if err != nil {
		return nil, false, fmt.Errorf("getting package from pod %s: %w", pod.Name, err)
	}
	if packagePtr == nil {
		return nil, false, nil
	}
	node, err := r.dal.GetNode(ctx, pod.Spec.NodeName)
	if err != nil {
		return nil, false, fmt.Errorf("getting node %s for pod %s: %w", pod.Spec.NodeName, pod.Name, err)
	}
	if node == nil {
		return nil, false, nil
	}
	skyhookNode, err := wrapper.NewSkyhookNodeOnly(node, packagePtr.Skyhook)
	if err != nil {
		return nil, false, fmt.Errorf("creating node wrapper for pod %s: %w", pod.Name, err)
	}
	open, err := shouldRecordPodErroring(skyhookNode, packagePtr)
	return node, open, err
}

// announceRestartLimitExceeded emits the Warning events for an interrupt Job the pod watch has just
// ended. Best-effort: the Job patch already carries the verdict, so a failed lookup is only logged.
func (r *PodReconciler) announceRestartLimitExceeded(ctx context.Context, pod *corev1.Pod, node *corev1.Node, job *batchv1.Job, restarts int32, allowance int64) {
	logger := log.FromContext(ctx).WithName("pod-reconcile")
	nodeWrightName := pod.Labels[nameLabel]
	packageName := pod.Labels[packageAnnotationKey]

	r.recorder.Eventf(node, nil, corev1.EventTypeWarning, EventsReasonSkyhookInterrupt, interruptRestartLimitAction,
		"interrupt for package [%s] from [nodewright:%s] still failing after %d restarts (allowance %d); failing job [%s]",
		packageName, nodeWrightName, restarts, allowance, job.Name)

	nodeWright, err := r.dal.GetSkyhook(ctx, nodeWrightName)
	if err != nil {
		logger.Error(err, "error getting nodewright for restart limit event", "nodewright", nodeWrightName, "job", job.Name)
	} else if nodeWright != nil {
		r.recorder.Eventf(nodeWright, nil, corev1.EventTypeWarning, EventsReasonSkyhookInterrupt, interruptRestartLimitAction,
			"interrupt for package [%s] on node [%s] still failing after %d restarts (allowance %d); failing job [%s]",
			packageName, pod.Spec.NodeName, restarts, allowance, job.Name)
	}
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

		// A package stage retries as fresh pods whose RestartCount is always 0; its attempts are its
		// Job's failed pods, which JobReconcile records from the Job, so keep that value. An interrupt
		// restarts in place, so its container RestartCount is its attempts.
		attempts := restarts
		if pod.Spec.RestartPolicy == corev1.RestartPolicyNever {
			state, err := skyhookNode.State()
			if err != nil {
				return false, fmt.Errorf("reading node state for pod %s: %w", pod.Name, err)
			}
			attempts = state[packagePtr.GetUniqueName()].Restarts
		}

		if err := skyhookNode.Upsert(packagePtr.PackageRef, packagePtr.Image,
			v1alpha1.StateErroring, packagePtr.Stage, attempts, packagePtr.ContainerSHA); err != nil {
			return false, fmt.Errorf("upserting erroring state for pod %s: %w", pod.Name, err)
		}
		// The node's status is deliberately left alone: the Job may still retry, and a node read as
		// erroring ends its batch as a failure that a later successful attempt cannot undo. The node
		// is marked erroring only when the Job fails (recordJobErroring).

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
		case s.State.Waiting != nil && s.State.Waiting.Reason == waitingReasonCrashLoopBackOff:
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

// podRejectedAtAdmission reports whether a Failed pod is one the kubelet refused to admit: it has
// no container statuses, since no container was ever created, and no verdict, neither a disruption
// nor a genuine failure (its own deadline included). It never ran the package.
func podRejectedAtAdmission(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodFailed &&
		len(pod.Status.InitContainerStatuses) == 0 && len(pod.Status.ContainerStatuses) == 0 &&
		!hasDisruptionTarget(pod) && !podFailedGenuinely(pod)
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
			if status.State.Waiting.Reason == waitingReasonCrashLoopBackOff {
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
