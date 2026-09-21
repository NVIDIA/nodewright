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
	"sort"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// annotationStateRecorded marks a finished Job whose completion the operator has
	// already written to node state. Pods now linger past completion, so delete-on-success
	// can no longer be the processed-once marker; this persisted annotation replaces it and
	// makes a duplicate terminal event for a marked Job a no-op.
	annotationStateRecorded = v1alpha1.METADATA_PREFIX + "/state-recorded"
	annotationValueTrue     = "true"

	// annotationLastLogs holds a best-effort tail of a deadline-killed stage's stuck
	// container, or its waiting reason when the container never started, so a timed-out
	// tombstone still names the problem after the kubelet garbage-collects the pod's logs.
	annotationLastLogs = v1alpha1.METADATA_PREFIX + "/last-logs"

	// batchControllerUIDLabel selects a Job's own child pods. Job names are deterministic
	// and reused across reruns, so a same-named prior Job's pod can still be terminating;
	// the controller UID is the only unambiguous parent link (job-name alone is not).
	batchControllerUIDLabel = "batch.kubernetes.io/controller-uid"

	// batchJobNameLabel is stamped by the Job controller on every child pod.
	batchJobNameLabel = "batch.kubernetes.io/job-name"

	// nameLabel is the ownership label every package Job and child pod carries.
	nameLabel = v1alpha1.METADATA_PREFIX + "/name"

	// interruptLabel marks a Job as running an interrupt stage.
	interruptLabel = v1alpha1.METADATA_PREFIX + "/interrupt"

	// lastLogsMaxBytes caps the deadline log snapshot. Annotations share a per-object
	// metadata budget, so the tail stays small.
	lastLogsMaxBytes = 16 * 1024

	// failureTargetGrace bounds how long a Job may sit at FailureTarget without going
	// terminal before its stuck stage is treated as erroring evidence: the unreachable-node
	// case, where the Job controller cannot delete the pod to finalize the failure.
	failureTargetGrace = 5 * time.Minute
)

// JobReconciler drives package-stage Jobs on their own watch and workqueue. It is a real
// controller rather than a prefixed request routed through SkyhookReconciler (the shape the
// pod path used): JobReconcile is already per-object and returns a per-object Result, so a
// real controller gives it a real requeue and per-object backoff instead of folding both into
// the whole-world pass.
//
// It holds its own dependencies rather than embedding SkyhookReconciler: embedding would inherit
// the heavy pass's entire method set, including a Reconcile this one has to shadow — so deleting
// the shadow would still compile and quietly run the whole-world pass on every Job event.
//
// Running concurrently with the heavy pass means both write nodewright.nvidia.com/nodeState_<name>,
// so every node-state write on both sides carries an optimistic-lock precondition.
type JobReconciler struct {
	client.Client
	// uncached reads straight from the apiserver, used only to re-read a Node after a patch
	// conflict; see patchNodeState.
	uncached client.Reader
	recorder events.EventRecorder
	opts     JobOperatorOptions
	dal      dal.DAL
}

func NewJobReconciler(c client.Client, uncached client.Reader, clientset kubernetes.Interface, recorder events.EventRecorder, opts JobOperatorOptions) *JobReconciler {
	return &JobReconciler{
		Client:   c,
		uncached: uncached,
		recorder: recorder,
		opts:     opts,
		dal:      dal.New(c, clientset),
	}
}

// ownedJob gates on nodewright.nvidia.com/name so foreign Jobs in the namespace (a CronJob's,
// say) stay out of the workqueue entirely, rather than being filtered after the fact in Reconcile.
func ownedJob() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(o client.Object) bool {
		return labels.Set(o.GetLabels()).Has(nameLabel)
	})
}

func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("job").
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(jobToNodeRequest), builder.WithPredicates(ownedJob())).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}

func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	jobs, err := r.jobsForNode(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting jobs for node %s: %w", req.Name, err)
	}
	if len(jobs) == 0 {
		return ctrl.Result{}, nil
	}

	job := jobs[0]
	result, err := r.JobReconcile(ctx, job)
	if err != nil {
		return result, err
	}
	if len(jobs) > 1 {
		result.RequeueAfter = 100 * time.Millisecond
	}
	return result, nil
}

// jobsForNode returns the node's unprocessed Jobs in work order. Terminal Jobs are handled
// first so their node state is recorded before active work continues; FailureTarget Jobs follow
// because they may need their deadline state recorded before ordinary reconciliation. The node
// name comes from each pod template rather than the bounded node label.
func (r *JobReconciler) jobsForNode(ctx context.Context, namespace, nodeName string) ([]*batchv1.Job, error) {
	list, err := r.dal.GetJobs(ctx, client.InNamespace(namespace))
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, nil
	}

	jobs := make([]*batchv1.Job, 0)
	for i := range list.Items {
		job := &list.Items[i]
		if !labels.Set(job.Labels).Has(nameLabel) || jobNodeName(job) != nodeName || jobProcessed(job) {
			continue
		}
		jobs = append(jobs, job)
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobPriority(jobs[i]) < jobPriority(jobs[j])
	})
	return jobs, nil
}

func jobPriority(job *batchv1.Job) int {
	if jobTerminal(job) {
		return 0
	}
	if hasJobCondition(job, batchv1.JobFailureTarget) {
		return 1
	}
	return 2
}

func jobTerminal(job *batchv1.Job) bool {
	if hasJobCondition(job, batchv1.JobComplete) {
		return true
	}
	failed, _ := jobFailure(job)
	return failed
}

func jobToNodeRequest(_ context.Context, obj client.Object) []reconcile.Request {
	job, ok := obj.(*batchv1.Job)
	if !ok || !labels.Set(job.Labels).Has(nameLabel) || jobNodeName(job) == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: job.Namespace,
		Name:      jobNodeName(job),
	}}}
}

// JobReconcile records the outcome of a package/interrupt stage Job into node state,
// exactly once, and manages the finished Job's failure archive and deadline snapshot.
// It is the Job analogue of PodReconcile and the completion authority for the Jobs
// execution path (the Pod watch keeps reporting only in-flight erroring for Job-owned
// pods). Driven by JobReconciler above, one call per Job event.
func (r *JobReconciler) JobReconcile(ctx context.Context, job *batchv1.Job) (ctrl.Result, error) {
	// A terminating Job is already being reaped (foreground delete keeps it visible until its
	// children are gone); never record its completion onto node state that a sweep just reset.
	if job.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	// A package invalidated mid-flight (spec drift) is torn down, mirroring
	// HandleInvalidPackage for raw pods. Foreground so the deterministic name frees only
	// once the child pods are gone.
	if invalid, err := IsInvalidPackage(job); err != nil {
		return ctrl.Result{}, fmt.Errorf("checking invalid package on job %s: %w", job.Name, err)
	} else if invalid {
		return ctrl.Result{}, deleteJobForeground(ctx, r.Client, job, "package marked invalid")
	}

	// Already processed: a persisted marker, not pod deletion, is the processed-once
	// guard now that completed pods linger, so a re-served terminal event is harmless.
	if jobProcessed(job) {
		return ctrl.Result{}, nil
	}

	if hasJobCondition(job, batchv1.JobComplete) {
		return r.handleCompleteJob(ctx, job)
	}
	if failed, reason := jobFailure(job); failed {
		return r.handleFailedJob(ctx, job, reason)
	}
	return r.handleActiveJob(ctx, job)
}

// handleCompleteJob records a successful stage completion once, then marks the Job with
// the success TTL. The record is skipped (only marked) when the node is gone or the
// completion is already reflected, so a re-served event after a crash between the two
// writes cannot double-apply a non-idempotent transition.
func (r *JobReconciler) handleCompleteJob(ctx context.Context, job *batchv1.Job) (ctrl.Result, error) {
	pkg, err := GetPackage(job)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting package from job %s: %w", job.Name, err)
	}
	if pkg == nil {
		// A Job we own with no package annotation cannot be recorded; mark it so it TTLs out
		// instead of re-serving forever. Once #303 wires creation the annotation is always set,
		// so log it; this surfaces a wiring regression rather than silently losing completions.
		log.FromContext(ctx).WithName("job-reconcile").Info("completed job has no package annotation; marking without recording", "job", job.Name)
		return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLSucceeded)
	}

	node, err := r.dal.GetNode(ctx, jobNodeName(job))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting node for job %s: %w", job.Name, err)
	}
	// Node gone → there is no state to record; mark processed rather than error-loop on
	// a node that will never return.
	if node == nil {
		return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLSucceeded)
	}

	if err := r.recordJobCompletion(ctx, job, pkg); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLSucceeded)
}

// shouldRecordCompletion is the re-serve guard. It records a completion only when the
// package's node-state entry is still at this Job's stage and not yet complete: a valid
// forward transition. An absent entry (uninstalled or node reset), an already-complete
// entry, or an entry that has advanced past this stage all mean recording would resurrect,
// duplicate, or regress state; the marker alone suffices. Interrupt completions also promote
// packages skipped during interrupt sequencing, so they re-run while any remain skipped.
func (r *JobReconciler) shouldRecordCompletion(job *batchv1.Job, pkg *PackageSkyhook, node *corev1.Node) (bool, error) {
	skyhookNode, err := wrapper.NewSkyhookNodeOnly(node, pkg.Skyhook)
	if err != nil {
		return false, fmt.Errorf("creating node wrapper for job %s: %w", job.Name, err)
	}
	state, err := skyhookNode.State()
	if err != nil {
		return false, fmt.Errorf("reading node state for job %s: %w", job.Name, err)
	}

	if isInterruptJob(job) {
		// ProgressSkipped promotes only StageInterrupt-skipped packages, so re-run the record
		// only while such a package remains, matching what the promotion can actually advance.
		//
		// This authorizes the write on a SIBLING's state, so it says nothing about this Job's own
		// package — recordJobCompletion gates its self-write on entryOpenAtStage separately.
		for _, s := range state {
			if s.Stage == v1alpha1.StageInterrupt && s.State == v1alpha1.StateSkipped {
				return true, nil
			}
		}
	}

	return entryOpenAtStage(state, pkg), nil
}

// entryOpenAtStage reports whether the package's entry is present, still at the stage the caller
// is reporting on, and not already complete — the only shape an executor may write onto at all.
// Absent means removed (rerun, reset, uninstall) and writing would resurrect it; a later stage or
// an existing complete would regress or duplicate.
//
// Named for the shape it tests, not for one caller's use: both the completion write and the Pod
// watch's erroring write require exactly this, and the two must not drift. It deliberately does
// not exclude an already-erroring entry — the Pod watch re-reports erroring so a rising restart
// count still lands, and an otherwise identical write is a no-op the Changed() check drops.
// recordJobErroring adds that exclusion itself; see the note there.
func entryOpenAtStage(state v1alpha1.NodeState, pkg *PackageSkyhook) bool {
	status, present := state[pkg.GetUniqueName()]
	return present && status.Stage == pkg.Stage && status.State != v1alpha1.StateComplete
}

// patchNodeState re-reads the node, applies mutate, and patches under an optimistic-lock
// precondition, retrying the whole read-modify-write on conflict. JobReconciler and PodReconciler
// both run concurrently with the heavy pass and all three write
// nodewright.nvidia.com/nodeState_<name> (one annotation key holding every package), so an
// unconditional patch would silently drop whichever write landed second. mutate re-evaluates its
// own guards on every attempt: a retry starts from state some other writer just changed, so a
// decision made against the previous read is not reusable.
//
// A free function rather than a method so both per-object controllers can call it while owning
// their dependencies outright, instead of embedding SkyhookReconciler to reach it.
//
// The first read comes from the cache, but every retry after a conflict reads uncached. dal reads
// through the manager's cached client, and the informer has usually not seen the write that just
// beat us, so a cached re-read rebuilds the identical resourceVersion precondition and loses
// again — burning all of RetryOnConflict's attempts on a copy that cannot succeed. Going to the
// apiserver only on retry keeps the hot path cached while letting the read-modify-write actually
// converge; without it the conflict escapes to the workqueue and only a requeue clears it.
func patchNodeState(ctx context.Context, d dal.DAL, uncached client.Reader, c client.Client, nodeName string, mutate func(*corev1.Node) (bool, error)) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := readNodeForPatch(ctx, d, uncached, nodeName, attempt)
		attempt++
		if err != nil {
			return fmt.Errorf("getting node %s: %w", nodeName, err)
		}
		if node == nil {
			return nil
		}

		patch := client.StrategicMergeFrom(node.DeepCopy(), client.MergeFromWithOptimisticLock{})
		changed, err := mutate(node)
		if err != nil || !changed {
			return err
		}
		return c.Patch(ctx, node, patch)
	})
}

// readNodeForPatch serves attempt 0 from the cache and every retry from the apiserver. A nil
// uncached reader falls back to the cache throughout, which is what the fake-client tests use.
func readNodeForPatch(ctx context.Context, d dal.DAL, uncached client.Reader, nodeName string, attempt int) (*corev1.Node, error) {
	if attempt == 0 || uncached == nil {
		return d.GetNode(ctx, nodeName)
	}

	var node corev1.Node
	if err := uncached.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &node, nil
}

// recordJobCompletion writes the StateComplete transition through the existing node-state
// path (HandleCompletePod carries the upgrade/uninstall/interrupt special cases unchanged),
// sourced from the Job rather than a pod so it still works after the child pod is GC'd.
func (r *JobReconciler) recordJobCompletion(ctx context.Context, job *batchv1.Job, pkg *PackageSkyhook) error {
	return patchNodeState(ctx, r.dal, r.uncached, r.Client, jobNodeName(job), func(node *corev1.Node) (bool, error) {
		record, err := r.shouldRecordCompletion(job, pkg, node)
		if err != nil || !record {
			return false, err
		}

		skyhookNode, err := wrapper.NewSkyhookNodeOnly(node, pkg.Skyhook)
		if err != nil {
			return false, fmt.Errorf("creating node wrapper for job %s: %w", job.Name, err)
		}

		// HandleCompletePod only compares containerName against InterruptContainerName, so the
		// Job's interrupt label determines it; no child pod needed. For non-interrupt stages the
		// exact name is cosmetic; read it from the succeeded pod when present, else leave it empty.
		containerName := ""
		if isInterruptJob(job) {
			containerName = InterruptContainerName
		} else {
			containerName = r.succeededContainerName(ctx, job)
		}

		updated, err := r.HandleCompletePod(ctx, skyhookNode, pkg, containerName)
		if err != nil {
			return false, fmt.Errorf("recording completion for job %s: %w", job.Name, err)
		}

		// Read after HandleCompletePod: its interrupt branch promotes skipped packages, which can
		// move this package's own entry.
		state, err := skyhookNode.State()
		if err != nil {
			return false, fmt.Errorf("reading node state for job %s: %w", job.Name, err)
		}

		// Upsert creates, so it must never run on an entry that is not there. An interrupt Job
		// reaches this on a sibling's behalf (see shouldRecordCompletion), so its own package may
		// have been removed by a rerun, reset or uninstall since the Job started; re-creating it
		// at (interrupt, complete) would then make the rerun predicate keep the Job, and the stage
		// would never run again. The promotion above still lands either way.
		//
		// Both guards are kept deliberately: updated says HandleCompletePod already wrote this
		// entry, entryOpenAtStage says there is an entry to write onto. They agree today —
		// every branch that sets updated leaves the entry absent or at another stage — and neither
		// is safe to drop on the strength of the other.
		recorded := false
		if !updated && entryOpenAtStage(state, pkg) {
			if err := skyhookNode.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateComplete, pkg.Stage, job.Status.Failed, pkg.ContainerSHA); err != nil {
				return false, fmt.Errorf("upserting complete state for job %s: %w", job.Name, err)
			}
			recorded = true
		}

		if !skyhookNode.Changed() {
			return false, nil
		}

		// The event follows what was actually written. An interrupt Job can reach here purely on a
		// sibling's promotion with its own entry gone, and announcing that package complete would
		// name the one a reset just cleared.
		if recorded || updated {
			r.recorder.Eventf(node, nil, EventTypeNormal, EventsReasonSkyhookStateChange, "JobComplete",
				"Package [%s:%s] state %s on [nodewright:%s]", pkg.Name, pkg.Version, v1alpha1.StateComplete, pkg.Skyhook)
		} else {
			r.recorder.Eventf(node, nil, EventTypeNormal, EventsReasonSkyhookStateChange, "JobComplete",
				"Interrupt complete on [nodewright:%s]: promoted packages skipped during interrupt sequencing", pkg.Skyhook)
		}
		return true, nil
	})
}

// HandleCompletePod applies the StateComplete transition, including the interrupt, upgrade and
// uninstall special cases. The name is historical: JobReconcile is the completion authority now
// and the pod watch never records completion.
func (r *JobReconciler) HandleCompletePod(ctx context.Context, skyhookNode wrapper.SkyhookNodeOnly, packagePtr *PackageSkyhook, containerName string) (bool, error) {
	updated := false

	if containerName == InterruptContainerName {
		// in this one case do we need a skyhook instance to get packages
		// kind of sucks, but does not update, just reads so that is better
		// seems safer to leave it this way unfortunately.
		// by passing in packages we can not update load packages
		skyhook, err := r.dal.GetSkyhook(ctx, packagePtr.Skyhook)
		if err != nil {
			return false, err
		}

		// A deleted CR reads as (nil, nil), and Convert dereferences it. That is reachable in the
		// same window this stage's guards are about: the CR removed while a completed interrupt
		// Job is still unprocessed. Nothing is left to promote then, so skip the branch rather
		// than panic — matching the nil check the uninstall branch below already does.
		if skyhook != nil {
			upgraded, err := wrapper.Convert(skyhookNode, skyhook)
			if err != nil {
				return false, fmt.Errorf("error converting node wrapper: %w", err)
			}

			// progress forward any skipped packages that this interrupt completed
			if err := upgraded.ProgressSkipped(); err != nil {
				return false, fmt.Errorf("error progressing skipped packages: %w", err)
			}
		}
	} else if packagePtr.Stage == v1alpha1.StageUpgrade {
		nodeState, err := skyhookNode.State()
		if err != nil {
			return false, fmt.Errorf("error getting node state: %w", err)
		}

		// go through and remove all the old node states for the package
		// after upgrade has finished
		for _, packageStatus := range nodeState {
			if packageStatus.Name == packagePtr.Name && packageStatus.Version != packagePtr.Version {
				packageStatusRef := v1alpha1.PackageRef{
					Name:    packageStatus.Name,
					Version: packageStatus.Version,
				}

				err = skyhookNode.RemoveState(packageStatusRef)
				if err != nil {
					return false, fmt.Errorf("error removing node state: %w", err)
				}
			}
		}
	} else if packagePtr.Stage == v1alpha1.StageUninstall {
		skyhook, err := r.dal.GetSkyhook(ctx, packagePtr.Skyhook)
		if err != nil {
			return false, err
		}

		if skyhook != nil {
			_package, exists := skyhook.Spec.Packages[packagePtr.Name]

			if !exists {
				// Package removed from spec — clean up node state.
				if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
					return false, fmt.Errorf("error removing state for removed package: %w", err)
				}
				updated = true
				return updated, nil
			}

			if _package.Version != packagePtr.Version {
				// Defensive: webhook rejects version changes unless the package is
				// already fully uninstalled, so the uninstall pod shouldn't complete
				// for a version that's not in spec. Clean up defensively.
				if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
					return false, fmt.Errorf("error cleaning up stale uninstall: %w", err)
				}
				updated = true
				return updated, nil
			}

			// Same version in spec: explicit or finalizer-driven uninstall.
			if _package.HasInterrupt() {
				// Package has an interrupt — advance to the uninstall-interrupt stage
				// so ProcessInterrupt fires the interrupt pod on the next reconcile.
				if err = skyhookNode.Upsert(packagePtr.PackageRef, packagePtr.Image,
					v1alpha1.StateInProgress, v1alpha1.StageUninstallInterrupt, 0, packagePtr.ContainerSHA); err != nil {
					return false, fmt.Errorf("error transitioning to uninstall-interrupt: %w", err)
				}
			} else {
				// No interrupt — remove state immediately (absent = uninstalled per D2).
				if err = skyhookNode.RemoveState(packagePtr.PackageRef); err != nil {
					return false, fmt.Errorf("error removing uninstalled package state: %w", err)
				}
				zeroOutSkyhookPackageMetrics(packagePtr.Skyhook, packagePtr.Name, packagePtr.Version)
			}
			updated = true
		}
	}

	return updated, nil
}

// handleFailedJob handles a terminal Failed Job. A genuine failure — attempts exhausted on real
// step failures or timeouts, or the Job-level ceiling — records erroring, marks with the failure
// TTL, and leaves the Job in place as the timeout marker so the main pass does not recreate the stage
// until a rerun/reset/config-change/TTL clears it. A Job that only ever lost pods the package
// never ran in is not the package's failure: mark it and let the sweep clear it so the stage
// re-runs, matching the invisible self-heal a vanished pod gets today.
func (r *JobReconciler) handleFailedJob(ctx context.Context, job *batchv1.Job, reason string) (ctrl.Result, error) {
	genuine, err := r.jobFailureIsGenuine(ctx, job, reason)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("classifying failed job %s: %w", job.Name, err)
	}
	if !genuine {
		return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLFailed)
	}

	pkg, err := GetPackage(job)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting package from job %s: %w", job.Name, err)
	}
	if pkg == nil {
		return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLFailed)
	}

	node, err := r.dal.GetNode(ctx, jobNodeName(job))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting node for job %s: %w", job.Name, err)
	}
	if node == nil {
		return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLFailed)
	}

	if err := r.recordJobErroring(ctx, job, pkg, reason); err != nil {
		return ctrl.Result{}, fmt.Errorf("recording failure for job %s: %w", job.Name, err)
	}

	return ctrl.Result{}, r.markJobProcessed(ctx, job, r.opts.JobTTLFailed)
}

// jobFailureIsGenuine reports whether a terminal Failed Job represents a real package failure.
//
// backoffLimit is finite, and these pods carry spec.nodeName rather than going through the
// scheduler, so kubelet admission is the only gate they face: a node at capacity or on its way
// back from a reboot can reject several node-pinned replacements in a row, each Failed with no
// container statuses and no DisruptionTarget for the podFailurePolicy to ignore. Under the old
// unbounded limit those only cost an archive slot; under a finite one they can exhaust the budget
// in about a minute and time out a package that never ran a line of script. So BackoffLimitExceeded is
// believed only when a retained attempt actually failed. Any other terminal reason is the
// Job-level ceiling, genuine on its own: nothing finished inside the entire retry budget.
//
// The archive pruner selects on the same podFailedGenuinely predicate, so a real failure is never
// pruned out from under this classifier by rejections either side of it.
//
// Losing the archives entirely (terminated-pod GC on a large cluster) does not by itself lose the
// timeout. This is the second of two writers that put an entry at (stage, erroring), and the
// timeout predicate reads only that entry plus terminal Failed — never this verdict. The Pod watch
// is the first writer and runs live, while the archives still exist; this one runs at terminal,
// from archives, and so covers the window where the operator was down for the Pod watch. Both use
// the same classification, so they agree. Both must miss to lose a timeout, and the stage re-runs
// and times out on the next cycle rather than churning.
func (r *JobReconciler) jobFailureIsGenuine(ctx context.Context, job *batchv1.Job, reason string) (bool, error) {
	if reason != batchv1.JobReasonBackoffLimitExceeded {
		return true, nil
	}

	pods, err := r.childPods(ctx, job)
	if err != nil {
		return false, err
	}
	for i := range pods {
		if podFailedGenuinely(&pods[i]) {
			return true, nil
		}
	}
	return false, nil
}

// recordJobErroring records the stage as timed out at (stage, erroring). It is guarded like the completion
// path: only the stage the Job actually ran is touched, and never regressed to a later stage
// or resurrected onto a removed package. This is also the state-only write for a stale
// FailureTarget on an unreachable node (no marker/TTL there; those wait for terminal Failed).
// reason is the Job's failure reason, carried into the event so the timeout names what ended it.
func (r *JobReconciler) recordJobErroring(ctx context.Context, job *batchv1.Job, pkg *PackageSkyhook, reason string) error {
	return patchNodeState(ctx, r.dal, r.uncached, r.Client, jobNodeName(job), func(node *corev1.Node) (bool, error) {
		skyhookNode, err := wrapper.NewSkyhookNodeOnly(node, pkg.Skyhook)
		if err != nil {
			return false, fmt.Errorf("creating node wrapper for job %s: %w", job.Name, err)
		}
		state, err := skyhookNode.State()
		if err != nil {
			return false, fmt.Errorf("reading node state for job %s: %w", job.Name, err)
		}

		// Deliberately NOT entryOpenAtStage: the completion guard excludes an entry that is
		// already complete, this one excludes an entry that is already erroring, for idempotence
		// on a re-served terminal event. Same shape, different exclusion — do not unify them.
		status, present := state[pkg.GetUniqueName()]
		if !present || status.Stage != pkg.Stage || status.State == v1alpha1.StateErroring {
			return false, nil
		}

		if err := skyhookNode.Upsert(pkg.PackageRef, pkg.Image, v1alpha1.StateErroring, pkg.Stage, job.Status.Failed, pkg.ContainerSHA); err != nil {
			return false, fmt.Errorf("upserting erroring state for job %s: %w", job.Name, err)
		}
		skyhookNode.SetStatus(v1alpha1.StatusErroring)

		if !skyhookNode.Changed() {
			return false, nil
		}
		r.recorder.Eventf(node, nil, EventTypeNormal, EventsReasonSkyhookStateChange, "JobFailed",
			"Package [%s:%s] stage %s failed on [nodewright:%s]: %s", pkg.Name, pkg.Version, pkg.Stage, pkg.Skyhook, reason)
		return true, nil
	})
}

// handleActiveJob runs on a non-terminal Job: it takes the best-effort deadline log snapshot
// when the Job is at FailureTarget (and records erroring if that state has gone stale on an
// unreachable node), then prunes failed attempts to a single archive. Completion itself waits
// for the terminal Complete/Failed condition.
func (r *JobReconciler) handleActiveJob(ctx context.Context, job *batchv1.Job) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("job-reconcile")

	result := ctrl.Result{}
	if hasJobCondition(job, batchv1.JobFailureTarget) {
		if err := r.snapshotFailureLogs(ctx, job); err != nil {
			logger.Error(err, "error snapshotting failure logs", "job", job.Name)
		}
		if failureTargetStale(job) {
			// A real state write. On an unreachable node no further Job event retries it, so
			// let the error escape to the work queue for a backoff retry rather than deferring
			// the erroring evidence to the next informer resync.
			if err := r.recordStaleFailureTarget(ctx, job); err != nil {
				return ctrl.Result{}, fmt.Errorf("recording erroring for stale failure target on job %s: %w", job.Name, err)
			}
		} else {
			// An unreachable node emits no further Job event to drive the stale→erroring
			// transition, so schedule the re-check ourselves rather than wait for a resync.
			// Requeue the time actually left, not a fresh full grace: a Job already part-way
			// through the window would otherwise wait up to two full windows.
			result.RequeueAfter = failureTargetRemaining(job)
		}
	}

	if err := r.pruneFailedAttempts(ctx, job); err != nil {
		logger.Error(err, "error pruning failed attempts", "job", job.Name)
	}

	return result, nil
}

// recordStaleFailureTarget records erroring (state only) for a Job stuck at FailureTarget on
// an unreachable node, where the Job controller can never delete the pod to reach terminal
// Failed. Terminal handling (marker, TTL, timeout) still waits for Failed.
func (r *JobReconciler) recordStaleFailureTarget(ctx context.Context, job *batchv1.Job) error {
	pkg, err := GetPackage(job)
	if err != nil {
		return fmt.Errorf("getting package from job %s: %w", job.Name, err)
	}
	if pkg == nil {
		return nil
	}
	return r.recordJobErroring(ctx, job, pkg, string(batchv1.JobFailureTarget))
}

// snapshotFailureLogs captures the last evidence of a deadline-bound stage before its pod is
// deleted. It is skipped when a failed-attempt archive already exists (that pod carries full
// logs and survives the deadline) or when the snapshot is already taken. A never-started
// container has no logs, so its waiting reason/message is recorded instead. Any error is
// swallowed by the caller; this must never delay the erroring/timeout path.
func (r *JobReconciler) snapshotFailureLogs(ctx context.Context, job *batchv1.Job) error {
	if _, ok := job.Annotations[annotationLastLogs]; ok {
		return nil
	}

	pods, err := r.childPods(ctx, job)
	if err != nil {
		return err
	}

	var target *corev1.Pod
	for i := range pods {
		// A genuine failed archive already holds full logs; nothing to snapshot. Judged on the
		// same predicate as the pruner that keeps it: a pod the kubelet refused to admit is also
		// Failed, but has no container statuses and no logs, so treating it as the archive would
		// leave the timed-out stage with no evidence at all.
		if podFailedGenuinely(&pods[i]) {
			return nil
		}
		if target == nil && (pods[i].Status.Phase == corev1.PodRunning || pods[i].Status.Phase == corev1.PodPending) {
			target = &pods[i]
		}
	}
	if target == nil {
		return nil
	}

	container, waitingReason, waitingMessage := stuckInitContainer(target)
	if container == "" {
		return nil
	}

	var snapshot string
	if waitingReason != "" {
		snapshot = fmt.Sprintf("%s: %s: %s", container, waitingReason, waitingMessage)
	} else {
		logs, err := r.dal.GetPodLogTail(ctx, target.Namespace, target.Name, container, lastLogsMaxBytes)
		if err != nil {
			return nil
		}
		snapshot = fmt.Sprintf("%s:\n%s", container, logs)
	}

	// Patch, not Update: this writes one annotation off a cached Job, and the pause path
	// concurrently writes spec.suspend on the same object. A full Update would send our stale
	// spec back and silently resume a Job that was just suspended.
	patch := client.MergeFrom(job.DeepCopy())
	if job.Annotations == nil {
		job.Annotations = map[string]string{}
	}
	job.Annotations[annotationLastLogs] = snapshot
	if err := r.Patch(ctx, job, patch); err != nil {
		return fmt.Errorf("annotating job %s with failure logs: %w", job.Name, err)
	}
	return nil
}

// pruneFailedAttempts keeps two archives: the first genuine failure (most likely the root
// cause, before cascading errors obscure it) and the most recent, and deletes the genuine
// failures in between. Only pods that failed with a verdict count, on the same
// podFailedGenuinely predicate the terminal classifier uses — anything else (a disruption
// casualty, a kubelet admission rejection) has no failure verdict, so it must neither be kept
// nor deleted, and above all must not shadow a real failure into the delete range. Those
// leftovers are bounded by backoffLimit+1 and go with the Job at its TTL.
// Normal deletion only: the Job-tracking finalizer guarantees a failure is counted and
// policy-classified before its pod is removed.
func (r *JobReconciler) pruneFailedAttempts(ctx context.Context, job *batchv1.Job) error {
	pods, err := r.childPods(ctx, job)
	if err != nil {
		return err
	}

	archives := make([]corev1.Pod, 0, len(pods))
	for i := range pods {
		if podFailedGenuinely(&pods[i]) && pods[i].DeletionTimestamp == nil {
			archives = append(archives, pods[i])
		}
	}
	if len(archives) <= 2 {
		return nil
	}

	// Name breaks CreationTimestamp ties (second granularity) so the kept pair is deterministic.
	sort.Slice(archives, func(i, j int) bool {
		if archives[i].CreationTimestamp.Equal(&archives[j].CreationTimestamp) {
			return archives[i].Name < archives[j].Name
		}
		return archives[i].CreationTimestamp.Before(&archives[j].CreationTimestamp)
	})

	// Keep the first and the last; delete the middle.
	for i := 1; i < len(archives)-1; i++ {
		if err := r.Delete(ctx, &archives[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("pruning failed attempt %s for job %s: %w", archives[i].Name, job.Name, err)
		}
	}
	return nil
}

// deleteJobForeground deletes a Job with foreground propagation so its deterministic name is
// freed only after the child pods are gone.
// A free function because both JobReconciler and the heavy pass delete Jobs, and neither
// should have to reach through the other to do it.
func deleteJobForeground(ctx context.Context, c client.Client, job *batchv1.Job, reason string) error {
	// A Job vanishing is otherwise unexplained: this is called from orphan cleanup, stale-spec
	// replacement, and package invalidation, and which one fired is the first thing anyone
	// debugging a restarted stage needs.
	log.FromContext(ctx).Info("deleting job", "job", job.Name, "reason", reason)
	if err := c.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting job %s: %w", job.Name, err)
	}
	return nil
}

// markJobProcessed sets the state-recorded marker and the outcome TTL in one patch, run after
// the node-state write so a crash can never mark a Job whose state is still in_progress. TTL is
// unset at creation, so the TTL controller cannot race this. Patched rather than Updated for the
// same reason as the other Job writes here: only these two fields should travel.
func (r *JobReconciler) markJobProcessed(ctx context.Context, job *batchv1.Job, ttl time.Duration) error {
	patch := client.MergeFrom(job.DeepCopy())
	if job.Annotations == nil {
		job.Annotations = map[string]string{}
	}
	job.Annotations[annotationStateRecorded] = annotationValueTrue
	seconds := int32(ttl.Seconds())
	job.Spec.TTLSecondsAfterFinished = &seconds

	if err := r.Patch(ctx, job, patch); err != nil {
		return fmt.Errorf("marking job %s processed: %w", job.Name, err)
	}
	return nil
}

// succeededContainerName returns the last init-container name of the Job's succeeded child pod
// (selected by controller UID, never job-name), or "" if the pod was already GC'd.
func (r *JobReconciler) succeededContainerName(ctx context.Context, job *batchv1.Job) string {
	pods, err := r.childPods(ctx, job)
	if err != nil {
		return ""
	}
	for i := range pods {
		if pods[i].Status.Phase == corev1.PodSucceeded {
			statuses := pods[i].Status.InitContainerStatuses
			if len(statuses) > 0 {
				return statuses[len(statuses)-1].Name
			}
		}
	}
	return ""
}

// childPods lists a Job's own pods by controller UID (job-name is ambiguous across reruns of a
// deterministic name).
func (r *JobReconciler) childPods(ctx context.Context, job *batchv1.Job) ([]corev1.Pod, error) {
	list, err := r.dal.GetPods(ctx, client.InNamespace(job.Namespace), client.MatchingLabels{batchControllerUIDLabel: string(job.UID)})
	if err != nil {
		return nil, fmt.Errorf("listing child pods for job %s: %w", job.Name, err)
	}
	if list == nil {
		return nil, nil
	}
	return list.Items, nil
}

// jobProcessed reports whether the Job's completion has already been recorded.
func jobProcessed(job *batchv1.Job) bool {
	return job.Annotations[annotationStateRecorded] == annotationValueTrue
}

// jobNodeName is the authoritative full node name the Job's pod is pinned to; the node label
// may be a hash for >63-char node names, so the pod template's nodeName is the source of truth.
func jobNodeName(job *batchv1.Job) string {
	return job.Spec.Template.Spec.NodeName
}

// isInterruptJob reports whether the Job runs an interrupt stage (carries the interrupt label).
func isInterruptJob(job *batchv1.Job) bool {
	return job.Labels[interruptLabel] == interruptLabelValue
}

// hasJobCondition reports whether the Job has the given condition set True.
func hasJobCondition(job *batchv1.Job, condition batchv1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == condition && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// jobFailure reports whether the Job is terminally Failed and, if so, its reason.
func jobFailure(job *batchv1.Job) (bool, string) {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true, c.Reason
		}
	}
	return false, ""
}

// jobFailedTerminally reports whether the Job has given up: a terminal Failed condition, whatever
// the reason. That is all it reports — it is exactly jobFailure's first return, named for what it
// tests rather than for what its callers conclude.
//
// A *timed-out stage* is this plus the node-state entry sitting at (stage, erroring), and it takes
// both: with a finite backoffLimit the Job's reason no longer separates a real failure from a
// backstop, so jobFailureIsGenuine draws that line, and only a genuine failure leaves the entry
// erroring. Callers that mean "timed out" pair the two; deleteConfigUpdateExecutors means only
// "terminally failed" and pairs it with nothing.
func jobFailedTerminally(job *batchv1.Job) bool {
	failed, _ := jobFailure(job)
	return failed
}

// jobFinished reports whether the Job has reached a terminal state (Complete or Failed).
// An unfinished Job is what gates re-creating a stage; a finished one no longer does.
func jobFinished(job *batchv1.Job) bool {
	if hasJobCondition(job, batchv1.JobComplete) {
		return true
	}
	failed, _ := jobFailure(job)
	return failed
}

// failureTargetStale reports whether the Job has sat at FailureTarget past the grace window
// without going terminal; the unreachable-node case.
func failureTargetStale(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailureTarget && c.Status == corev1.ConditionTrue {
			return time.Since(c.LastTransitionTime.Time) > failureTargetGrace
		}
	}
	return false
}

// failureTargetRemaining is how much of the grace window a Job at FailureTarget has left, with
// a second of slack so the requeue lands after the boundary rather than a hair before it and
// burns another whole window. Zero when the Job is not at FailureTarget.
func failureTargetRemaining(job *batchv1.Job) time.Duration {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailureTarget && c.Status == corev1.ConditionTrue {
			remaining := failureTargetGrace - time.Since(c.LastTransitionTime.Time) + time.Second
			if remaining < time.Second {
				return time.Second
			}
			return remaining
		}
	}
	return 0
}

// hasDisruptionTarget reports whether the pod carries the DisruptionTarget condition: a pod
// lost to eviction/preemption/PodGC rather than a genuine step failure.
func hasDisruptionTarget(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// stuckInitContainer returns the first init container that has not exited 0: the one that hung
// or crashed under the deadline. When that container never started (unpullable image, missing
// configmap) its waiting reason and message are returned instead, since there are no logs.
func stuckInitContainer(pod *corev1.Pod) (name, waitingReason, waitingMessage string) {
	for _, s := range pod.Status.InitContainerStatuses {
		if s.State.Terminated != nil && s.State.Terminated.ExitCode == 0 {
			continue
		}
		if s.State.Waiting != nil {
			return s.Name, s.State.Waiting.Reason, s.State.Waiting.Message
		}
		return s.Name, "", ""
	}
	return "", "", ""
}
