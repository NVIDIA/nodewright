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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/NVIDIA/nodewright/operator/internal/drain"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	"github.com/go-logr/logr"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubernetes/pkg/util/taints"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	EventsReasonSkyhookApply       = "Apply"
	EventsReasonSkyhookInterrupt   = "Interrupt"
	EventsReasonSkyhookDrain       = "Drain"
	EventsReasonSkyhookStateChange = "State"
	EventsReasonNodeReboot         = "Reboot"
	EventTypeNormal                = "Normal"
	// EventTypeWarning = "Warning"
	TaintUnschedulable     = corev1.TaintNodeUnschedulable
	InterruptContainerName = "interrupt"
	// pauseContainerName is the single main container in the raw-pod builders (a
	// forever-running pause). The Job builder swaps it for a container that exits 0 so
	// the pod can reach Succeeded and the Job can complete.
	pauseContainerName = "pause"
	// interruptLabelValue is the value of the .../interrupt label (capital "True",
	// distinct from the lowercase annotationTrueValue "true" used elsewhere).
	interruptLabelValue = "True"
	// shellBinary is the shell entrypoint for the init-copy step and the Job's exit-0 container.
	shellBinary = "/bin/sh"

	SkyhookFinalizer = "nodewright.nvidia.com/nodewright"

	// Annotation values used as truthy/falsy strings on Skyhook and Node objects.
	annotationTrueValue  = "true"
	annotationFalseValue = "false"

	// Field selector keys used when filtering pod lists by node.
	fieldSelectorNodeName = "spec.nodeName"

	// Volume + mountpath shared by every package container's host-root mount.
	volumeNameRootMount = "root-mount"
	mountPathRoot       = "/root"

	// Directory inside package containers where the SCR's configMap is projected.
	mountPathConfigMaps = "/skyhook-package/configmaps"

	// Environment variable names propagated into package containers.
	envSkyhookResourceID = "SKYHOOK_RESOURCE_ID"
	envSkyhookNodeOrder  = "SKYHOOK_NODE_ORDER"

	// globalReconcileName is both the controller's registered name (driving the
	// reconcile metric and log labels) and the .Name of the sentinel request the
	// heavy reconcile path collapses Skyhook and Node events onto. The sentinel
	// value is arbitrary and ignored by Reconcile (which grabs the whole world);
	// it only has to be constant and must not collide with the "pod---" dispatch
	// prefix.
	globalReconcileName = "nodewright"

	// globalReconcileDelay is how long Skyhook and Node events wait before the
	// global key becomes ready. The bulk of coalescing is already free: a burst
	// arriving while a pass is in flight dedups onto one follow-up via the
	// priority queue's locked-key handling. This short window only lets a pass's
	// own writes propagate to the cache and near-simultaneous events land before
	// the follow-up runs. It is kept small on purpose: the interrupt flow
	// advances one stage per Node event, so a larger delay (we started at 500ms)
	// adds up across stages and blows the interrupt e2e timing budget.
	globalReconcileDelay = 50 * time.Millisecond

	// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
	// rename. legacyRuntimeRequiredTaint is the pre-rename default runtime-required taint.
	// The operator still tolerates it and removes it on completion, but never applies it.
	//
	// WHY: the taint key is a coordination point with infrastructure the operator cannot
	// see — autoscaler/Karpenter node pools, machine templates, and user tolerations all
	// name it. A cluster whose provisioner still stamps the old key would otherwise bring
	// up nodes carrying a taint nothing removes, leaving them permanently unschedulable.
	// Remove with the legacy group at the removal release.
	legacyRuntimeRequiredTaint = "skyhook.nvidia.com=runtime-required:NoSchedule"
)

type SkyhookOperatorOptions struct {
	Namespace            string        `env:"NAMESPACE, default=nodewright"`
	MaxInterval          time.Duration `env:"DEFAULT_INTERVAL, default=10m"`
	ImagePullSecret      string        `env:"IMAGE_PULL_SECRET"`
	CopyDirRoot          string        `env:"COPY_DIR_ROOT, default=/var/lib/skyhook"`
	ReapplyOnReboot      bool          `env:"REAPPLY_ON_REBOOT, default=false"`
	RuntimeRequiredTaint string        `env:"RUNTIME_REQUIRED_TAINT, default=nodewright.nvidia.com=runtime-required:NoSchedule"`
	PauseImage           string        `env:"PAUSE_IMAGE, default=registry.k8s.io/pause:3.10"`
	AgentImage           string        `env:"AGENT_IMAGE, default=ghcr.io/nvidia/nodewright/agent:latest"` // TODO: pin a released agent version instead of :latest
	AgentLogRoot         string        `env:"AGENT_LOG_ROOT, default=/var/log/skyhook"`
	// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
	// rename. LegacyCleanupDelay is how long after a Skyhook finishes migrating the
	// operator keeps its legacy skyhook.nvidia.com node state / pods / ConfigMap labels
	// around (a rollback window) before pruning them. 0 or less prunes immediately (no
	// rollback window). Remove with the legacy group at the removal release.
	LegacyCleanupDelay time.Duration `env:"LEGACY_CLEANUP_DELAY, default=24h"`
	// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
	// rename. PublishLegacyMetrics keeps the deprecated skyhook_* metric series exported
	// alongside the current nodewright_* ones so existing dashboards and alerts survive
	// the rename. Setting it false halves the operator's exported series count at the
	// cost of breaking any consumer still querying the legacy names. Remove with the
	// legacy group at the removal release.
	PublishLegacyMetrics bool `env:"PUBLISH_LEGACY_METRICS, default=true"`

	// Embedded rather than a separate field so the Job knobs are one nameable unit for
	// JobReconciler while promotion keeps opts.JobStageTimeout resolving for the eight
	// builder functions that thread SkyhookOperatorOptions, and keeps the env names flat.
	JobOperatorOptions
}

// JobOperatorOptions are the knobs for package-stage Jobs, grouped so JobReconciler can take
// just these rather than the whole operator options struct. A sibling of
// WebhookControllerOptions in shape: per-controller options, parsed from the same flat env set.
type JobOperatorOptions struct {
	// JobTTLSucceeded / JobTTLFailed set ttlSecondsAfterFinished at completion time by
	// outcome so failure logs outlive success logs; JobStageTimeout is the default
	// per-attempt deadline for a package stage Job when the package sets no stageTimeout
	// (0 removes the time bound); JobBackoffLimit is how many *retries* a package stage gets
	// after its first attempt before its Job goes terminal, so the stage runs at most
	// JobBackoffLimit+1 times (0 means a single attempt, no retry). Exhausting the budget is
	// not by itself a timeout: the stage times out as erroring only if a retained attempt genuinely
	// failed, since attempts the kubelet refused to admit spend the budget without ever
	// running the package.
	JobTTLSucceeded time.Duration `env:"JOB_TTL_SUCCEEDED, default=1h"`
	JobTTLFailed    time.Duration `env:"JOB_TTL_FAILED, default=24h"`
	JobStageTimeout time.Duration `env:"JOB_STAGE_TIMEOUT, default=1h"`
	JobBackoffLimit int32         `env:"JOB_BACKOFF_LIMIT, default=3"`
}

func (o *SkyhookOperatorOptions) Validate() error {

	messages := make([]string, 0)
	if o.Namespace == "" {
		messages = append(messages, "namespace must be set")
	}
	if o.CopyDirRoot == "" {
		messages = append(messages, "copy dir root must be set")
	}
	if o.RuntimeRequiredTaint == "" {
		messages = append(messages, "runtime required taint must be set")
	}
	if o.MaxInterval < time.Minute {
		messages = append(messages, "max interval must be at least 1 minute")
	}

	// CopyDirRoot must start with /
	if !strings.HasPrefix(o.CopyDirRoot, "/") {
		messages = append(messages, "copy dir root must start with /")
	}

	// RuntimeRequiredTaint must be parsable and must not be a deletion
	_, delete, err := taints.ParseTaints([]string{o.RuntimeRequiredTaint})
	if err != nil {
		messages = append(messages, fmt.Sprintf("runtime required taint is invalid: %s", err.Error()))
	}
	if len(delete) > 0 {
		messages = append(messages, "runtime required taint must not be a deletion")
	}

	if o.AgentImage == "" {
		messages = append(messages, "agent image must be set")
	}

	if !strings.Contains(o.AgentImage, ":") {
		messages = append(messages, "agent image must contain a tag")
	}

	if o.PauseImage == "" {
		messages = append(messages, "pause image must be set")
	}

	if !strings.Contains(o.PauseImage, ":") {
		messages = append(messages, "pause image must contain a tag")
	}

	if o.JobTTLSucceeded < time.Minute {
		messages = append(messages, "job ttl succeeded must be at least 1 minute")
	}

	if o.JobTTLFailed < time.Minute {
		messages = append(messages, "job ttl failed must be at least 1 minute")
	}

	// 0 disables the stage deadline; negatives are meaningless.
	if o.JobStageTimeout < 0 {
		messages = append(messages, "job stage timeout must be greater than or equal to 0")
	}

	// 0 gives a stage a single attempt with no retry; negatives are meaningless.
	if o.JobBackoffLimit < 0 {
		messages = append(messages, "job backoff limit must be greater than or equal to 0")
	}

	if len(messages) > 0 {
		return errors.New(strings.Join(messages, ", "))
	}

	return nil
}

// AgentVersion returns the image tag portion of AgentImage
func (o *SkyhookOperatorOptions) AgentVersion() string {
	parts := strings.Split(o.AgentImage, ":")
	return parts[len(parts)-1]
}

// GetRuntimeRequiredTaint returns the taint the operator APPLIES to nodes, which is
// always the configured one. Use GetRuntimeRequiredTaints for anything that has to
// recognise a taint already on a node.
func (o *SkyhookOperatorOptions) GetRuntimeRequiredTaint() corev1.Taint {
	to_add, _, _ := taints.ParseTaints([]string{o.RuntimeRequiredTaint})
	return to_add[0]
}

// GetRuntimeRequiredTaints returns every runtime-required taint the operator RECOGNISES:
// the configured one, plus the legacy skyhook.nvidia.com taint for the deprecation
// window. The legacy entry is dropped when it is the configured taint, so an operator
// still pinned to the old key does not see it twice.
func (o *SkyhookOperatorOptions) GetRuntimeRequiredTaints() []corev1.Taint {
	configured := o.GetRuntimeRequiredTaint()
	legacy, _, _ := taints.ParseTaints([]string{legacyRuntimeRequiredTaint})
	if configured.MatchTaint(&legacy[0]) {
		return []corev1.Taint{configured}
	}
	return []corev1.Taint{configured, legacy[0]}
}

func (o *SkyhookOperatorOptions) GetRuntimeRequiredTolerations() []corev1.Toleration {
	recognised := o.GetRuntimeRequiredTaints()
	tolerations := make([]corev1.Toleration, 0, len(recognised))
	for _, taint := range recognised {
		tolerations = append(tolerations, corev1.Toleration{
			Key:      taint.Key,
			Operator: corev1.TolerationOpEqual,
			Value:    taint.Value,
			Effect:   taint.Effect,
		})
	}
	return tolerations
}

// hasAnyTaint reports whether node carries any of ts.
func hasAnyTaint(node *corev1.Node, ts []corev1.Taint) bool {
	for i := range ts {
		if taints.TaintExists(node.Spec.Taints, &ts[i]) {
			return true
		}
	}
	return false
}

// force type checking against this interface
var _ reconcile.Reconciler = &SkyhookReconciler{}

func NewSkyhookReconciler(schema *runtime.Scheme, c client.Client, uncached client.Reader, clientset kubernetes.Interface, recorder events.EventRecorder, opts SkyhookOperatorOptions) (*SkyhookReconciler, error) {

	err := opts.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid nodewright operator options: %w", err)
	}

	return &SkyhookReconciler{
		Client:    c,
		uncached:  uncached,
		scheme:    schema,
		recorder:  recorder,
		opts:      opts,
		clientset: clientset,
		dal:       dal.New(c, clientset),
	}, nil
}

// SkyhookReconciler reconciles a Skyhook object
type SkyhookReconciler struct {
	client.Client
	// uncached reads straight from the apiserver when a Node patch conflict requires the
	// mutation to be recomputed from current state. Nil falls back to the cached client,
	// which is what the fake-client tests use.
	uncached  client.Reader
	scheme    *runtime.Scheme
	recorder  events.EventRecorder
	opts      SkyhookOperatorOptions
	clientset kubernetes.Interface
	dal       dal.DAL
}

// SetupWithManager sets up the controller with the Manager.
func (r *SkyhookReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// indexes allow for query on fields to use the local cache
	indexer := mgr.GetFieldIndexer()
	err := indexer.
		IndexField(context.TODO(), &corev1.Pod{}, fieldSelectorNodeName, func(o client.Object) []string {
			pod, ok := o.(*corev1.Pod)
			if !ok {
				return nil
			}
			return []string{pod.Spec.NodeName}
		})

	if err != nil {
		return err
	}

	globalHandler := &globalDelayHandler{
		logger: mgr.GetLogger(),
		dal:    dal.New(r.Client, r.clientset),
		delay:  globalReconcileDelay,
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(globalReconcileName).
		WithOptions(controller.Options{
			// Only one heavy "grab the world" pass may run at a time: it is a
			// centralized scheduler reading across every SCR, Node and Pod, so
			// concurrent passes would race each other's writes. This makes the
			// global key the single in-flight reconcile.
			MaxConcurrentReconciles: 1,
		}).
		// Heavy path: Skyhook and Node events collapse onto the global key.
		Watches(
			&v1alpha1.NodeWright{},
			globalHandler,
		).
		Watches(
			&corev1.Node{},
			globalHandler,
		).
		// Pod and Job events are not watched here: PodReconciler and JobReconciler own them
		// on their own watches and workqueues, and the node-state writes they make are
		// themselves Node events that reach the heavy pass above.
		Complete(r)
}

// CRD Permissions
//+kubebuilder:rbac:groups=skyhook.nvidia.com,resources=skyhooks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=skyhook.nvidia.com,resources=skyhooks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=skyhook.nvidia.com,resources=skyhooks/finalizers,verbs=update
//+kubebuilder:rbac:groups=skyhook.nvidia.com,resources=deploymentpolicies,verbs=get;list;watch

// core permissions
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;update;patch;watch
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=pods/eviction,verbs=create
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// The event recorder writes via the events.k8s.io/v1 API (client-go tools/events,
// wired through mgr.GetEventRecorder), so the core rule above is not sufficient on
// its own — without this rule every recorded event is rejected as forbidden.
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// Cluster-wide to match the cluster-wide ConfigMap informer in main.go; the two must move
// together, since a cluster-wide informer under a namespaced Role is rejected at LIST/WATCH.
// Both are candidates for namespacing (see the note at the cache options), held back pending
// the e2e/core investigation.
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Package stages run as batch/v1 Jobs; pods/log is read for the deadline failure-log snapshot.
// Jobs and their pod logs are namespace-scoped rather than cluster-wide: every Job the operator
// touches lives in its own namespace (the informer is scoped there in main.go, and every list
// passes client.InNamespace), and pod logs are only read off those Jobs' child pods. controller-gen
// requires a literal namespace, so this uses the same `system` placeholder as the rest of
// config/: the kustomize namespace transformer rewrites it, and the chart templates
// .Release.Namespace.
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete,namespace=system
//+kubebuilder:rbac:groups=core,resources=pods/log,verbs=get,namespace=system

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *SkyhookReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Migration safety interlock: while any legacy skyhook.nvidia.com Skyhook is still
	// mid-rollout, hold this NodeWright reconcile and requeue rather than take over a
	// node the pre-rename operator may still be mutating. Clears once the legacy
	// Skyhooks read complete; fresh clusters and post-migration installs never hold.
	if hold := r.legacyMigrationHold(ctx); hold != nil {
		return *hold, nil
	}

	// get all skyhooks (SCR)
	skyhooks, err := r.dal.GetSkyhooks(ctx)
	if err != nil {
		// error, going to requeue and backoff
		logger.Error(err, "error getting nodewrights")
		return ctrl.Result{}, err
	}

	// if there are no skyhooks, so actually nothing to do, so don't requeue
	if skyhooks == nil || len(skyhooks.Items) == 0 {
		return ctrl.Result{}, nil
	}

	// get all nodes
	nodes, err := r.dal.GetNodes(ctx)
	if err != nil {
		// error, going to requeue and backoff
		logger.Error(err, "error getting nodes")
		return ctrl.Result{}, err
	}

	// if no nodes, well not work to do either
	if nodes == nil || len(nodes.Items) == 0 {
		// no nodes, so nothing to do
		return ctrl.Result{}, nil
	}

	// get all deployment policies
	deploymentPolicies, err := r.dal.GetDeploymentPolicies(ctx)
	if err != nil {
		logger.Error(err, "error getting deployment policies")
		return ctrl.Result{}, err
	}

	// TODO: this build state could error in a lot of ways, and I think we might want to move towards partial state
	// mean if we cant get on SCR state, great, process that one and error

	// BUILD cluster state from all skyhooks, and all nodes
	// this filters and pairs up nodes to skyhooks, also provides help methods for introspection and mutation
	clusterState, err := BuildState(skyhooks, nodes, deploymentPolicies)
	if err != nil {
		// error, going to requeue and backoff
		logger.Error(err, "error building cluster state")
		return ctrl.Result{}, err
	}

	// handle auto-tainting new nodes first so it
	if yes, result, err := shouldReturn(r.HandleAutoTaint(ctx, clusterState)); yes {
		return result, err
	}

	if yes, result, err := shouldReturn(r.HandleMigrations(ctx, clusterState)); yes {
		return result, err
	}

	if yes, result, err := shouldReturn(r.TrackReboots(ctx, clusterState)); yes {
		return result, err
	}

	// node picker is for selecting nodes to do work, tries maintain a prior of nodes between SCRs
	nodePicker := NewNodePicker(logger, r.opts.GetRuntimeRequiredTolerations())

	errs := make([]error, 0)
	var result *ctrl.Result
	configSyncPending := false

	for _, skyhook := range clusterState.skyhooks {
		if err := r.refreshSkyhookConditions(ctx, clusterState, skyhook); err != nil {
			return ctrl.Result{RequeueAfter: time.Second * 2}, err
		}

		if yes, result, err := shouldReturn(r.HandleFinalizer(ctx, skyhook, clusterState)); yes {
			return result, err
		}

		if yes, result, err := shouldReturn(r.ReportState(ctx, clusterState, skyhook)); yes {
			return result, err
		}

		if skyhook.IsPaused() {
			if err := r.suspendUnfinishedJobs(ctx, skyhook); err != nil {
				return ctrl.Result{RequeueAfter: time.Second * 2}, fmt.Errorf("suspending jobs for paused skyhook %s: %w", skyhook.GetSkyhook().Name, err)
			}
			if yes, result, err := shouldReturn(r.UpdatePauseStatus(ctx, clusterState, skyhook)); yes {
				return result, err
			}
			continue
		}

		if yes, pendingSync, result, err := r.validateAndUpsertSkyhookData(ctx, skyhook, clusterState); yes {
			return result, err
		} else if pendingSync {
			configSyncPending = true
		}

		// Resume: validation above invalidated any Job whose spec changed while paused; now clear
		// suspend on the survivors. Ordered after validation so no stale-spec attempt launches first.
		//
		// A disabled Skyhook is skipped inside resumeSuspendedJobs; see there for why.
		if err := r.resumeSuspendedJobs(ctx, skyhook); err != nil {
			return ctrl.Result{RequeueAfter: time.Second * 2}, fmt.Errorf("resuming suspended jobs for skyhook %s: %w", skyhook.GetSkyhook().Name, err)
		}

		changed := IntrospectSkyhook(skyhook, clusterState.skyhooks, logger)
		if changed {
			_, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
			if len(errs) > 0 {
				return ctrl.Result{RequeueAfter: time.Second * 2}, utilerrors.NewAggregate(errs)
			}
			return ctrl.Result{RequeueAfter: time.Second * 2}, nil
		}

		_, err := HandleVersionChange(skyhook)
		if err != nil {
			return ctrl.Result{RequeueAfter: time.Second * 2}, fmt.Errorf("error getting packages to uninstall: %w", err)
		}
	}

	// Process all non-complete, non-disabled skyhooks (in priority order)
	// Each skyhook is processed only for nodes that are ready (all higher-priority skyhooks complete on that node)
	// This enables per-node priority ordering: nodes can progress independently
	result, err = r.processSkyhooksPerNode(ctx, clusterState, nodePicker, logger)
	if err != nil {
		errs = append(errs, err)
	}

	err = r.HandleRuntimeRequired(ctx, clusterState, nodes)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		err := utilerrors.NewAggregate(errs)
		return ctrl.Result{}, err
	}

	return reconcileResult(result, configSyncPending, r.opts.MaxInterval), nil
}

// reconcileResult picks the requeue for a completed reconcile pass. Active work
// supplies its own (shorter) result, which is returned untouched. When the pass is
// otherwise idle but an owned ConfigMap write was deferred because the completedNodes
// gate was closed (configSyncPending), retry after configSyncRetryInterval instead of
// the much longer maxInterval so the CM converges promptly rather than appearing stuck
// while status reads complete (issue #245). Otherwise fall back to maxInterval.
func reconcileResult(result *ctrl.Result, configSyncPending bool, maxInterval time.Duration) ctrl.Result {
	if result != nil {
		return *result
	}
	if configSyncPending {
		return ctrl.Result{RequeueAfter: configSyncRetryInterval}
	}
	return ctrl.Result{RequeueAfter: maxInterval}
}

// refreshSkyhookConditions updates and persists the per-Skyhook conditions
// that have to stay accurate regardless of pause / disable / delete state:
//
//   - NodeStateMalformed surfaces unreadable nodeState annotations BEFORE
//     any handler that reads node.State() runs and aborts on parse errors.
//   - Blocked + UninstallInProgress + UninstallFailed mirror node state so
//     paused or disabled Skyhooks (which short-circuit
//     processSkyhooksPerNode) still get current conditions, and so
//     HandleFinalizer's deletion-gating logic stays focused on its own
//     concern instead of duplicating condition-mirroring work.
//
// UpdateBlockedCondition / UpdateUninstallConditions are tolerant to
// per-node state read errors — they skip unreadable nodes and let
// UpdateNodeStateMalformedCondition (set above) be the user-visible signal.
// This keeps HandleFinalizer's malformed-state branch reachable so its
// DeletionBlocked condition + Warning event fire on CR deletion.
func (r *SkyhookReconciler) refreshSkyhookConditions(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes) error {
	skyhook.UpdateNodeStateMalformedCondition()
	if _, saveErrs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook); len(saveErrs) > 0 {
		return utilerrors.NewAggregate(saveErrs)
	}
	if err := skyhook.UpdateBlockedCondition(); err != nil {
		return fmt.Errorf("error updating blocked condition: %w", err)
	}
	if err := skyhook.UpdateUninstallConditions(); err != nil {
		return fmt.Errorf("error updating uninstall conditions: %w", err)
	}
	return nil
}

// processSkyhooksPerNode processes all skyhooks for nodes that are ready (per-node priority ordering).
// A node is ready for a skyhook if all higher-priority skyhooks are complete on that specific node.
func (r *SkyhookReconciler) processSkyhooksPerNode(ctx context.Context, clusterState *clusterState, nodePicker *NodePicker, logger logr.Logger) (*ctrl.Result, error) {
	var result *ctrl.Result
	var errs []error

	for _, skyhook := range clusterState.skyhooks {
		if skyhook.IsDisabled() || skyhook.IsPaused() {
			continue
		}
		hasWork, err := skyhook.HasUninstallWork()
		if err != nil {
			errs = append(errs, fmt.Errorf("error checking uninstall work for nodewright %s: %w", skyhook.GetSkyhook().Name, err))
			continue
		}
		if skyhook.IsComplete() && !hasWork {
			continue
		}

		// Check if any nodes are ready for this skyhook
		ready, err := hasReadyNodesForSkyhook(skyhook, clusterState.skyhooks)
		if err != nil {
			errs = append(errs, fmt.Errorf("error checking ready nodes for nodewright %s: %w", skyhook.GetSkyhook().Name, err))
			continue
		}
		if !ready {
			continue
		}

		res, err := r.RunSkyhookPackages(ctx, clusterState, nodePicker, skyhook)
		if err != nil {
			logger.Error(err, "error processing nodewright", "nodewright", skyhook.GetSkyhook().Name)
			errs = append(errs, err)
		}
		if res != nil {
			result = res
		}
	}

	if len(errs) > 0 {
		return result, utilerrors.NewAggregate(errs)
	}
	return result, nil
}

// hasReadyNodesForSkyhook checks if any nodes are ready to process this skyhook.
// A node is ready if it's not complete and all higher-priority skyhooks are complete on that node.
func hasReadyNodesForSkyhook(skyhook SkyhookNodes, allSkyhooks []SkyhookNodes) (bool, error) {
	pendingUninstall, err := skyhook.HasUninstallWork()
	if err != nil {
		return false, err
	}
	for _, node := range skyhook.GetNodes() {
		if node.IsComplete() && !pendingUninstall {
			continue
		}
		if IsNodeReadyForSkyhook(node.GetNode().Name, skyhook, allSkyhooks) {
			return true, nil
		}
	}
	return false, nil
}

func shouldReturn(updates bool, err error) (bool, ctrl.Result, error) {
	if err != nil {
		return true, ctrl.Result{}, err
	}
	if updates {
		return true, ctrl.Result{RequeueAfter: time.Second * 2}, nil
	}
	return false, ctrl.Result{}, nil
}

func (r *SkyhookReconciler) HandleMigrations(ctx context.Context, clusterState *clusterState) (bool, error) {

	updates := false

	if version.VERSION == "" {
		// this means the binary was complied without version information
		return false, nil
	}

	logger := log.FromContext(ctx)
	errors := make([]error, 0)
	for _, skyhook := range clusterState.skyhooks {

		err := skyhook.Migrate(logger)
		if err != nil {
			return false, fmt.Errorf("error migrating nodewright [%s]: %w", skyhook.GetSkyhook().Name, err)
		}

		if err := skyhook.GetSkyhook().NodeWright.Validate(); err != nil {
			return false, fmt.Errorf("error validating nodewright [%s]: %w", skyhook.GetSkyhook().Name, err)
		}

		// MIGRATION-SHIM: rollback-safe legacy cleanup. skyhook.Migrate above adopts
		// legacy node state ADDITIVELY (keeps the skyhook.nvidia.com keys). We only
		// prune those legacy keys/pods/labels once the rollback window has elapsed,
		// tracked by the legacy-migrated-at stamp on the NodeWright. See
		// docs/plans/2026-07-20-legacy-cleanup-ttl-design.md.
		nw := skyhook.GetSkyhook().NodeWright
		prune := legacyCleanupShouldPrune(nw.GetAnnotations()[legacyMigratedAtAnnotation], r.opts.LegacyCleanupDelay, time.Now())

		if prune {
			for _, node := range skyhook.GetNodes() {
				node.PruneLegacyMetadata()
			}
		}

		for _, node := range skyhook.GetNodes() {
			if node.Changed() {
				err := r.Status().Patch(ctx, node.GetNode(), client.MergeFrom(clusterState.tracker.GetOriginal(node.GetNode())))
				if err != nil {
					errors = append(errors, fmt.Errorf("error patching node [%s]: %w", node.GetNode().Name, err))
				}

				// Deliberately NOT optimistic-locked. The pass patches every node from one
				// whole-world snapshot, so a conflict cannot be retried in place: the state the
				// patch was computed from is already stale. Gating it produced a conflict storm
				// in e2e (0 -> 156 conflicts on one lifecycle run) where the pass never
				// converged. JobReconciler's own writes are locked and retried instead, since a
				// single object can be re-read cheaply.
				err = r.Patch(ctx, node.GetNode(), client.MergeFrom(clusterState.tracker.GetOriginal(node.GetNode())))
				if err != nil {
					errors = append(errors, fmt.Errorf("error patching node [%s]: %w", node.GetNode().Name, err))
				}
				updates = true
			}
		}

		// Converge (add nodewright labels, keep legacy pods) or prune (delete legacy
		// pods, drop legacy labels) the workloads the pre-rename operator created under
		// the legacy skyhook.nvidia.com labels.
		hadLegacyWorkloads, workloadsChanged, err := r.reconcileLegacyLabeledWorkloads(ctx, nw, prune)
		if err != nil {
			return false, fmt.Errorf("error reconciling legacy-labeled workloads for nodewright [%s]: %w", skyhook.GetSkyhook().Name, err)
		}
		if workloadsChanged {
			updates = true
		}

		if skyhook.GetSkyhook().Updated {
			// need to do this because SaveNodesAndSkyhook only saves skyhook status, not the main skyhook object where the annotations are
			// additionally it needs to be an update, a patch nils out the annotations for some reason, which the save function does a patch

			if err = r.Status().Update(ctx, skyhook.GetSkyhook().NodeWright); err != nil {
				return false, fmt.Errorf("error updating during migration nodewright status [%s]: %w", skyhook.GetSkyhook().Name, err)
			}

			// because of conflict issues (409) we need to do things a bit differently here.
			// We might be able to use server side apply in the future, but for now we need to do this
			// https://kubernetes.io/docs/reference/using-api/server-side-apply/
			// https://github.com/kubernetes-sigs/controller-runtime/issues/347

			// work around for now is to grab a new copy of the object, and then patch it

			newskyhook, err := r.dal.GetSkyhook(ctx, skyhook.GetSkyhook().Name)
			if err != nil {
				return false, fmt.Errorf("error getting nodewright to migrate [%s]: %w", skyhook.GetSkyhook().Name, err)
			}
			newPatch := client.MergeFrom(newskyhook.DeepCopy())

			// set version
			wrapper.NewSkyhookWrapper(newskyhook).SetVersion()

			if err = r.Patch(ctx, newskyhook, newPatch); err != nil {
				return false, fmt.Errorf("error updating during migration nodewright [%s]: %w", skyhook.GetSkyhook().Name, err)
			}

			updates = true
		}

		// Manage the rollback-window stamp LAST: it re-gets and patches the NodeWright,
		// bumping its resourceVersion. Running it after the status/version writes above
		// (which submit the in-memory copy) avoids a stale-RV 409 on the first migration
		// reconcile. Set the stamp on the first converge that finds legacy artifacts
		// (unless pruning immediately); clear it once a prune has removed everything.
		if stampChanged, err := r.reconcileLegacyMigratedStamp(ctx, skyhook, prune, hadLegacyWorkloads); err != nil {
			return false, err
		} else if stampChanged {
			updates = true
		}
	}

	if len(errors) > 0 {
		return false, utilerrors.NewAggregate(errors)
	}

	return updates, nil
}

// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
// rename. Delete everything tagged MIGRATION-SHIM together with the legacy
// skyhook.nvidia.com group in the removal release (see docs/plans Phase 10).
//
// legacyMetadataPrefix is the metadata prefix the pre-rename operator stamped on
// package pods and per-node metadata ConfigMaps. It is intentionally hardcoded (a
// one-shot migration constant whose value can never change) so the controller keeps
// depending only on the new nodewright api group.
const legacyMetadataPrefix = "skyhook.nvidia.com"

// The full label keys the pre-rename operator stamped, one per kind of object it
// owned: per-node metadata ConfigMaps carry the node-meta key, while package
// ConfigMaps and package pods carry the name key. Frozen historical values, since
// nothing can change what an already-released operator wrote to a cluster.
// MIGRATION-SHIM: remove with the legacy group.
const (
	legacyNodeMetaLabel = legacyMetadataPrefix + "/skyhook-node-meta"
	legacyNameLabel     = legacyMetadataPrefix + "/name"
)

// legacyMigratedAtAnnotation is stamped on a NodeWright (RFC3339) the first time its
// legacy skyhook.nvidia.com artifacts are adopted. The prune of those artifacts is
// deferred until LegacyCleanupDelay has elapsed since this time, giving a rollback
// window. MIGRATION-SHIM: remove with the legacy group.
const legacyMigratedAtAnnotation = v1alpha1.METADATA_PREFIX + "/legacy-migrated-at"

// legacyCleanupShouldPrune reports whether the legacy skyhook.nvidia.com artifacts for
// a NodeWright may be pruned yet. delay <= 0 prunes immediately (no rollback window).
// Otherwise a NodeWright is prunable only once delay has elapsed since its stamp; an
// absent stamp means "not yet adopted, converge first", and an unparseable stamp fails
// toward prune so a corrupt value cannot pin legacy state forever.
func legacyCleanupShouldPrune(stamp string, delay time.Duration, now time.Time) bool {
	if delay <= 0 {
		return true
	}
	if stamp == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return true
	}
	return !now.Before(t.Add(delay))
}

// MIGRATION-SHIM (see legacyMetadataPrefix): remove with the legacy group.
// reconcileLegacyLabeledWorkloads converges or prunes the workloads the pre-rename
// operator created under the legacy skyhook.nvidia.com labels for the named skyhook.
// Converge (prune=false) adds the nodewright label to the per-node metadata ConfigMaps
// alongside the legacy one and leaves the legacy package pods in place, so a rolled-back
// pre-rename operator still owns its workloads. Prune (prune=true) graceful-deletes the
// legacy package pods and drops the legacy ConfigMap label. Returns whether any legacy
// artifact was found and whether anything was written. Level-triggered and idempotent.
func (r *SkyhookReconciler) reconcileLegacyLabeledWorkloads(ctx context.Context, nodewright *v1alpha1.NodeWright, prune bool) (bool, bool, error) {
	skyhookName := nodewright.Name
	hadLegacy := false
	changed := false

	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{legacyNameLabel: skyhookName}); err != nil {
		return false, false, fmt.Errorf("listing legacy-labeled pods for nodewright [%s]: %w", skyhookName, err)
	}
	if len(pods.Items) > 0 {
		hadLegacy = true
	}
	if prune {
		for i := range pods.Items {
			// Graceful delete: honor the pod's terminationGracePeriodSeconds. Single-writer
			// safety comes from the migration hold (the legacy Skyhook is complete, so its
			// pods are no longer mutating the host), not from the grace period.
			if err := r.Delete(ctx, &pods.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return false, false, fmt.Errorf("deleting legacy-labeled pod [%s]: %w", pods.Items[i].Name, err)
			}
			changed = true
		}
	}

	// Missing either of these label keys wedges the upgrade permanently, so both are
	// swept: UpsertConfigmaps lists package ConfigMaps by the NEW <nodewright>/name
	// label, so an unconverged legacy package ConfigMap is invisible to it, it falls
	// through to Create, and the reconcile fails on AlreadyExists forever. The
	// pre-rename operator used a different key per kind of ConfigMap: per-node metadata
	// carries <legacy>/skyhook-node-meta, package ConfigMaps carry <legacy>/name.
	//
	// Two Lists rather than one: MatchingLabels ANDs its terms, so a single selector
	// cannot express OR across two different keys.
	legacyConfigMapLabelKeys := []string{
		legacyNodeMetaLabel,
		legacyNameLabel,
	}

	// A ConfigMap could carry both keys; dedupe so it is not updated twice.
	seenConfigMaps := make(map[client.ObjectKey]struct{})
	for _, labelKey := range legacyConfigMapLabelKeys {
		cms := &corev1.ConfigMapList{}
		if err := r.List(ctx, cms, client.InNamespace(r.opts.Namespace),
			client.MatchingLabels{labelKey: skyhookName}); err != nil {
			return false, false, fmt.Errorf("listing legacy-labeled configmaps by [%s] for nodewright [%s]: %w", labelKey, skyhookName, err)
		}
		for i := range cms.Items {
			cm := &cms.Items[i]
			key := client.ObjectKeyFromObject(cm)
			if _, dup := seenConfigMaps[key]; dup {
				continue
			}
			seenConfigMaps[key] = struct{}{}
			hadLegacy = true

			// Re-parent on BOTH paths, not just converge. With LegacyCleanupDelay
			// set to 0 (a documented "no rollback window" setting)
			// legacyCleanupShouldPrune is true on the very first reconcile, so the
			// prune branch runs without any converge ever having happened. Leaving
			// the legacy ownerReference in place there would keep these ConfigMaps
			// cascade-delete bait for the guide's own "delete the old CRs" step.
			reparented, err := reparentToNodeWright(ctx, cm, nodewright, r.scheme)
			if err != nil {
				return false, false, err
			}

			var mutated bool
			if prune {
				mutated = relabelLegacyMetadataPrefix(cm.Labels)
			} else {
				mutated = addNodeWrightMetaLabel(cm.Labels)
			}
			mutated = mutated || reparented
			if mutated {
				if err := r.Update(ctx, cm); err != nil {
					return false, false, fmt.Errorf("updating legacy configmap labels [%s]: %w", cm.Name, err)
				}
				changed = true
			}
		}
	}

	return hadLegacy, changed, nil
}

// MIGRATION-SHIM (see legacyMetadataPrefix): remove with the legacy group.
// reparentToNodeWright moves a ConfigMap the pre-rename operator owned onto the
// NodeWright, dropping any ownerReference back to the legacy skyhook.nvidia.com
// group. Returns whether it changed anything.
//
// WHY: the pre-rename operator set the legacy Skyhook as controller owner. The
// migration guide tells users to delete that Skyhook once the NodeWright is live,
// and without this the delete cascades and garbage-collects the package and per-node
// metadata ConfigMaps out from under the running NodeWright. The operator recreates
// them, but a package pod scheduled in that window sees a missing ConfigMap.
func reparentToNodeWright(ctx context.Context, cm *corev1.ConfigMap, nodewright *v1alpha1.NodeWright, scheme *runtime.Scheme) (bool, error) {
	kept := make([]metav1.OwnerReference, 0, len(cm.OwnerReferences))
	dropped := false
	for _, ref := range cm.OwnerReferences {
		if strings.HasPrefix(ref.APIVersion, legacyMetadataPrefix+"/") {
			dropped = true
			continue
		}
		kept = append(kept, ref)
	}
	cm.OwnerReferences = kept

	if !dropped && metav1.IsControlledBy(cm, nodewright) {
		return false, nil
	}
	if err := ctrl.SetControllerReference(nodewright, cm, scheme); err != nil {
		// A ConfigMap some OTHER controller owns is not ours to take. Skip it loudly
		// rather than returning: this error would propagate out of HandleMigrations and
		// wedge the migration for EVERY NodeWright, which is the same blast radius as
		// the AlreadyExists wedge this shim exists to prevent. Dropping the legacy owner
		// ref is still worth persisting, so report whether that happened.
		var alreadyOwned *controllerutil.AlreadyOwnedError
		if errors.As(err, &alreadyOwned) {
			log.FromContext(ctx).Info("not re-parenting a configmap owned by another controller; it keeps its current owner and will not be adopted",
				"configmap", cm.Name, "ownerKind", alreadyOwned.Owner.Kind, "ownerName", alreadyOwned.Owner.Name, "nodewright", nodewright.Name)
			return dropped, nil
		}
		return false, fmt.Errorf("re-parenting configmap [%s] onto NodeWright [%s]: %w", cm.Name, nodewright.Name, err)
	}
	return true, nil
}

// MIGRATION-SHIM (see legacyMetadataPrefix): remove with the legacy group.
// reconcileLegacyMigratedStamp stamps the NodeWright's legacy-migrated-at annotation
// the first time a converge finds legacy artifacts (starting the rollback window), and
// clears it once a prune has removed everything legacy. Returns whether it wrote.
func (r *SkyhookReconciler) reconcileLegacyMigratedStamp(ctx context.Context, skyhook SkyhookNodes, prune, hadLegacyWorkloads bool) (bool, error) {
	nw := skyhook.GetSkyhook().NodeWright
	stamp := nw.GetAnnotations()[legacyMigratedAtAnnotation]
	name := skyhook.GetSkyhook().Name

	if prune {
		if stamp != "" && !hadLegacyWorkloads && !anyNodeHasLegacyMetadata(skyhook.GetNodes(), name) {
			return true, r.patchLegacyMigratedStamp(ctx, name, "", true)
		}
		return false, nil
	}

	if stamp == "" && (hadLegacyWorkloads || anyNodeHasLegacyMetadata(skyhook.GetNodes(), name)) {
		return true, r.patchLegacyMigratedStamp(ctx, name, time.Now().Format(time.RFC3339), false)
	}
	return false, nil
}

// patchLegacyMigratedStamp sets or removes the legacy-migrated-at annotation on the
// NodeWright with a focused merge patch (re-read then patch, to avoid clobbering other
// concurrent writers and the annotation-nilling gotcha of a full update).
func (r *SkyhookReconciler) patchLegacyMigratedStamp(ctx context.Context, name, value string, remove bool) error {
	nw, err := r.dal.GetSkyhook(ctx, name)
	if err != nil {
		return fmt.Errorf("getting nodewright to update legacy-migrated-at stamp [%s]: %w", name, err)
	}
	before := nw.DeepCopy()
	annotations := nw.GetAnnotations()
	if remove {
		if annotations == nil {
			return nil
		}
		delete(annotations, legacyMigratedAtAnnotation)
	} else {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[legacyMigratedAtAnnotation] = value
	}
	nw.SetAnnotations(annotations)
	if err := r.Patch(ctx, nw, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("patching legacy-migrated-at stamp on [%s]: %w", name, err)
	}
	return nil
}

// anyNodeHasLegacyMetadata reports whether any node still carries legacy
// skyhook.nvidia.com metadata that the operator owns for the named skyhook.
//
// Ownership is decided by wrapper.HasOperatorOwnedLegacyMetadata rather than a
// prefix match here, so the rule lives next to the migration that applies it. A
// whole-prefix scan answers the wrong question on two counts: the prune deliberately
// preserves a user's own skyhook.nvidia.com/* keys, so the stamp could never clear,
// and it would also count another skyhook's keys on a shared node against this one.
func anyNodeHasLegacyMetadata(nodes []wrapper.SkyhookNode, skyhookName string) bool {
	for _, node := range nodes {
		if wrapper.HasOperatorOwnedLegacyMetadata(node.GetNode(), skyhookName) {
			return true
		}
	}
	return false
}

// addNodeWrightMetaLabel adds a nodewright.nvidia.com-prefixed copy of each legacy
// skyhook.nvidia.com label, keeping the legacy label. Returns true if it added any.
func addNodeWrightMetaLabel(labels map[string]string) bool {
	changed := false
	var legacy []string
	for k := range labels {
		if strings.HasPrefix(k, legacyMetadataPrefix+"/") {
			legacy = append(legacy, k)
		}
	}
	for _, k := range legacy {
		suffix := strings.TrimPrefix(k, legacyMetadataPrefix+"/")
		newKey := fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, suffix)
		if _, exists := labels[newKey]; !exists {
			labels[newKey] = labels[k]
			changed = true
		}
	}
	return changed
}

// MIGRATION-SHIM (see legacyMetadataPrefix): remove with the legacy group.
// relabelLegacyMetadataPrefix rewrites, in place, any label key under the legacy
// skyhook.nvidia.com prefix to the current nodewright.nvidia.com prefix, preserving
// the value. It returns true if it changed anything. Safe to mutate the map during
// the range: rewritten keys carry the new prefix, so CutPrefix skips them if the
// iteration happens to visit them.
func relabelLegacyMetadataPrefix(labels map[string]string) bool {
	changed := false
	for k, v := range labels {
		suffix, ok := strings.CutPrefix(k, legacyMetadataPrefix+"/")
		if !ok {
			continue
		}
		delete(labels, k)
		labels[fmt.Sprintf("%s/%s", v1alpha1.METADATA_PREFIX, suffix)] = v
		changed = true
	}
	return changed
}

// ReportState computes and puts important information into the skyhook status so that monitoring tools such as k9s
// can see the information at a glance. For example, the number of completed nodes and the list of packages in the skyhook.
func (r *SkyhookReconciler) ReportState(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes) (bool, error) {

	// save updated state to skyhook status
	skyhook.ReportState()

	if skyhook.GetSkyhook().Updated {
		_, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
		if len(errs) > 0 {
			return false, utilerrors.NewAggregate(errs)
		}
		return true, nil
	}

	return false, nil
}

func (r *SkyhookReconciler) UpdatePauseStatus(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes) (bool, error) {
	changed := UpdateSkyhookPauseStatus(skyhook, log.FromContext(ctx))

	if changed {
		_, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
		if len(errs) > 0 {
			return false, utilerrors.NewAggregate(errs)
		}
		return true, nil
	}

	return false, nil
}

// suspendUnfinishedJobs gives the Emergency Stop teeth: while a Skyhook is paused every one of its
// unfinished Jobs is set spec.suspend=true, so the Job controller SIGTERMs the running pod (honoring
// terminationGracePeriodSeconds) and starts nothing until resume. Idempotent; already-suspended Jobs
// and invalid ones (mid-reap) are skipped. The killed pod carries a DeletionTimestamp, so
// erroring-evidence guard (c) keeps node state at in_progress. A pre-rename raw pod has no Job and
// so cannot be suspended, but pause never has one of its own to stop: this change ships with the
// nodewright rename, so legacyMigrationHold keeps the two execution models from running side by
// side. See the design doc's Upgrade section.
func (r *SkyhookReconciler) suspendUnfinishedJobs(ctx context.Context, skyhook SkyhookNodes) error {
	return r.setSuspendOnUnfinishedJobs(ctx, skyhook, true)
}

// resumeSuspendedJobs clears spec.suspend on a non-paused Skyhook's surviving unfinished Jobs. It
// must run AFTER validation (ValidateRunningPackages), which invalidates any Job whose spec changed
// while paused — clearing suspend first could launch one stale-spec attempt before validation caught
// it. On resume the Job controller starts a fresh pod that re-runs the interrupted stage (the same
// recovery shape as an eviction mid-stage), and because suspension cleared the Job's start time the
// stage deadline restarts from full.
//
// A disabled Skyhook is never resumed. Disable does not claim to stop work already in flight, but it
// must never RESTART work that pause stopped: clearing the pause annotation and setting disable in
// one edit would otherwise un-suspend everything pause had suspended, so disabling a paused Skyhook
// would resume it — making disable strictly weaker than pause. Re-enabling resumes them.
//
// The guard lives here rather than at the call site so it holds for every caller.
func (r *SkyhookReconciler) resumeSuspendedJobs(ctx context.Context, skyhook SkyhookNodes) error {
	if skyhook.IsDisabled() {
		return nil
	}
	return r.setSuspendOnUnfinishedJobs(ctx, skyhook, false)
}

// setSuspendOnUnfinishedJobs sets spec.suspend to the given value on every unfinished, valid Job of
// the Skyhook, skipping Jobs already in the desired state. suspend is a mutable Job field, so a
// write touching only it is accepted where a full spec write would be rejected as immutable.
//
// Merge-patched rather than Updated, and deliberately without an optimistic lock: these Jobs come
// from a cached list, and the Job controller rewrites Job status constantly — flipping suspend
// itself deletes the pod and produces more status writes — so a full Update would carry a
// resourceVersion that is already stale and 409 for no reason. The operator is the only writer of
// spec.suspend and sets it to an absolute value rather than a read-modify-write, so last-write-wins
// is the correct semantic here. Same tradeoff the Node patches document in TrackReboots below.
func (r *SkyhookReconciler) setSuspendOnUnfinishedJobs(ctx context.Context, skyhook SkyhookNodes, suspend bool) error {
	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name,
		},
	)
	if err != nil {
		return fmt.Errorf("listing jobs to set suspend=%t for skyhook %s: %w", suspend, skyhook.GetSkyhook().Name, err)
	}
	if jobs == nil {
		return nil
	}

	for i := range jobs.Items {
		job := &jobs.Items[i]
		if jobFinished(job) {
			continue
		}
		// An invalid Job is already being foreground-deleted; neither suspend nor resume it.
		invalid, err := IsInvalidPackage(job)
		if err != nil {
			return fmt.Errorf("checking invalid marker on job %s: %w", job.Name, err)
		}
		if invalid {
			continue
		}
		current := job.Spec.Suspend != nil && *job.Spec.Suspend
		if current == suspend {
			continue
		}
		patch := client.MergeFrom(job.DeepCopy())
		job.Spec.Suspend = ptr(suspend)
		if err := r.Patch(ctx, job, patch); err != nil {
			return fmt.Errorf("setting suspend=%t on job %s: %w", suspend, job.Name, err)
		}
	}
	return nil
}

func (r *SkyhookReconciler) TrackReboots(ctx context.Context, clusterState *clusterState) (bool, error) {

	updates := false
	errs := make([]error, 0)

	for _, skyhook := range clusterState.skyhooks {
		if skyhook.GetSkyhook().Status.NodeBootIds == nil {
			skyhook.GetSkyhook().Status.NodeBootIds = make(map[string]string)
		}

		for _, node := range skyhook.GetNodes() {
			id, ok := skyhook.GetSkyhook().Status.NodeBootIds[node.GetNode().Name]

			if !ok { // new node
				skyhook.GetSkyhook().Status.NodeBootIds[node.GetNode().Name] = node.GetNode().Status.NodeInfo.BootID
				skyhook.GetSkyhook().Updated = true
			}

			if id != "" && id != node.GetNode().Status.NodeInfo.BootID { // node rebooted
				if r.opts.ReapplyOnReboot {
					r.recorder.Eventf(skyhook.GetSkyhook().NodeWright, nil, EventTypeNormal, EventsReasonNodeReboot, "ResetNodeState", "detected reboot, resetting node [%s] to be reapplied", node.GetNode().Name)
					r.recorder.Eventf(node.GetNode(), nil, EventTypeNormal, EventsReasonNodeReboot, "ResetNodeState", "detected reboot, resetting node for [%s] to be reapplied", node.GetSkyhook().Name)
					node.Reset()

					// A completion from a previous boot must not land on freshly-reset state (the
					// postcondition guard cannot distinguish it), so clear this node's Jobs for the
					// Skyhook regardless of status, including unprocessed Complete ones.
					if err := r.deleteNodeJobs(ctx, skyhook.GetSkyhook().Name, node.GetNode().Name); err != nil {
						errs = append(errs, fmt.Errorf("error clearing jobs after reboot on node %s: %w", node.GetNode().Name, err))
					}

					// Re-apply the runtime-required taint so workloads cannot schedule on the
					// rebooted node until Skyhook finishes re-applying. The original auto-taint
					// annotation survives Reset() and remains the record that this taint is
					// operator-managed; no annotation update is needed.
					if skyhook.GetSkyhook().Spec.RuntimeRequired && skyhook.GetSkyhook().Spec.AutoTaintNewNodes &&
						!hasAnyTaint(node.GetNode(), r.opts.GetRuntimeRequiredTaints()) {
						taintToAdd := r.opts.GetRuntimeRequiredTaint()
						newNode, updated, _ := taints.AddOrUpdateTaint(node.GetNode(), &taintToAdd)
						if updated {
							node.GetNode().Spec.Taints = newNode.Spec.Taints
							log.FromContext(ctx).Info("re-applying runtime-required taint after reboot", "node", node.GetNode().Name, "taint", taintToAdd.Key)
						}
					}

					// Persist the reset before recording the new boot id. We Patch rather than
					// Update because a busy node's resourceVersion churns constantly under other
					// controllers, and a full Update would lose that optimistic-concurrency race; a
					// strategic merge of only our annotation/label changes does not. And we advance
					// NodeBootIds only after the write is durable: if the reset never lands, leaving
					// the boot id unchanged keeps the reboot pending so it is re-detected and retried
					// next reconcile, instead of being silently consumed while the node's stale
					// "complete" state remains and the package is never reapplied.
					if node.Changed() {
						updates = true
						// Deliberately NOT optimistic-locked: the reboot reset must land on a busy
						// node whose resourceVersion moved under other controllers. Losing the reset
						// strands a stale "complete" and the package is never reapplied, which is
						// worse than the narrow chance of overwriting a concurrent state write.
						patch := client.StrategicMergeFrom(clusterState.tracker.GetOriginal(node.GetNode()))
						if err := r.Patch(ctx, node.GetNode(), patch); err != nil {
							errs = append(errs, fmt.Errorf("error patching node after reboot [%s]: %w", node.GetNode().Name, err))
							continue
						}
					}
				}
				skyhook.GetSkyhook().Status.NodeBootIds[node.GetNode().Name] = node.GetNode().Status.NodeInfo.BootID
				skyhook.GetSkyhook().Updated = true
			}
		}
		if skyhook.GetSkyhook().Updated { // update
			updates = true
			err := r.Status().Update(ctx, skyhook.GetSkyhook().NodeWright)
			if err != nil {
				errs = append(errs, fmt.Errorf("error updating nodewright status after reboot [%s]: %w", skyhook.GetSkyhook().Name, err))
			}
		}
	}

	return updates, utilerrors.NewAggregate(errs)
}

// RunSkyhookPackages runs all skyhook packages then saves and requeues if changes were made
func (r *SkyhookReconciler) RunSkyhookPackages(ctx context.Context, clusterState *clusterState, nodePicker *NodePicker, skyhook SkyhookNodes) (*ctrl.Result, error) {

	logger := log.FromContext(ctx)
	requeue := false
	beingDeleted := !skyhook.GetSkyhook().DeletionTimestamp.IsZero()

	toExplicitUninstall, err := HandleUninstallRequests(skyhook)
	if err != nil {
		return nil, fmt.Errorf("error handling uninstall requests: %w", err)
	}

	err = HandleCancelledUninstalls(skyhook)
	if err != nil {
		return nil, fmt.Errorf("error handling cancelled uninstalls: %w", err)
	}

	toUninstall, err := HandleVersionChange(skyhook)
	if err != nil {
		return nil, fmt.Errorf("error getting packages to uninstall: %w", err)
	}

	toUninstall = append(toExplicitUninstall, toUninstall...)

	// UpdateBlockedCondition / UpdateUninstallConditions run at the top of
	// Reconcile (before the pause/disable short-circuit) so paused and
	// disabled Skyhooks get the same conditions as running ones.

	changed := IntrospectSkyhook(skyhook, clusterState.skyhooks, logger)
	if !changed && skyhook.IsComplete() {
		return nil, nil
	}

	selectedNode := nodePicker.SelectNodes(skyhook)

	for _, node := range selectedNode {
		// Skip nodes that are waiting on higher-priority skyhooks
		// This enables per-node priority ordering
		if !IsNodeReadyForSkyhook(node.GetNode().Name, skyhook, clusterState.skyhooks) {
			continue
		}

		if node.IsComplete() && !node.Changed() {
			continue
		}

		toRun, err := node.RunNext()
		if err != nil {
			return nil, fmt.Errorf("error getting next packages to run: %w", err)
		}

		// Filter out packages where uninstall is in progress or already
		// completed on this node. A package absent from nodeState with
		// uninstall requested (IsUninstalling, or finalizer-driven via
		// beingDeleted && UninstallEnabled) means uninstall finished — skip
		// apply. Absent + never-requested means never installed yet — allow
		// apply.
		//
		// A State() error here would silently produce a nil nodeState, and the
		// IsUninstallCycleInProgress check below is nil-safe (returns false) — so
		// we'd queue an apply pod while the node might actually be mid-uninstall.
		// Propagate the error instead; the user-visible NodeStateMalformed
		// condition is already set at the top of Reconcile.
		nodeState, err := node.State()
		if err != nil {
			return nil, fmt.Errorf("node %s: reading state while filtering runnable packages: %w",
				node.GetNode().Name, err)
		}
		filtered := make([]*v1alpha1.Package, 0, len(toRun))
		for _, pkg := range toRun {
			if shouldSkipApplyForUninstall(pkg, nodeState, beingDeleted) {
				continue
			}
			filtered = append(filtered, pkg)
		}
		toRun = filtered

		// prepend the uninstall packages so they are ran first.
		// filterUninstallForNode drops entries that aren't in this node's
		// state — toUninstall is global across all nodes, so a package can
		// be pending uninstall on node B while already absent on node A.
		toRun = append(filterUninstallForNode(toUninstall, nodeState), toRun...)

		interrupt, pack := fudgeInterruptWithPriority(toRun, skyhook.GetSkyhook().GetConfigUpdates(), skyhook.GetSkyhook().GetConfigInterrupts())

		for _, f := range toRun {

			ok, err := r.ProcessInterrupt(ctx, node, f, interrupt, interrupt != nil && f.Name == pack)
			if err != nil {
				// TODO: error handle
				return nil, fmt.Errorf("error processing if we should interrupt [%s:%s]: %w", f.Name, f.Version, err)
			}
			if !ok {
				requeue = true
				continue
			}

			err = r.ApplyPackage(ctx, logger, clusterState, node, f, interrupt != nil && f.Name == pack)
			if err != nil {
				return nil, fmt.Errorf("error applying package [%s:%s]: %w", f.Name, f.Version, err)
			}

			// process one package at a time
			if skyhook.GetSkyhook().Spec.Serial {
				return &ctrl.Result{RequeueAfter: time.Second * 2}, nil
			}
		}
	}

	saved, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
	if len(errs) > 0 {
		return &ctrl.Result{}, utilerrors.NewAggregate(errs)
	}
	if saved {
		requeue = true
	}

	if !skyhook.IsComplete() || requeue {
		return &ctrl.Result{RequeueAfter: time.Second * 2}, nil // not sure this is better then just requeue bool
	}

	return nil, utilerrors.NewAggregate(errs)
}

// nodeStateAnnotationKey is the single annotation holding every package's status for one
// NodeWright on one node.
func nodeStateAnnotationKey(skyhookName string) string {
	return fmt.Sprintf("%s/nodeState_%s", v1alpha1.METADATA_PREFIX, skyhookName)
}

// nodeStateDelta is the set of package entries one heavy pass changed, derived by diffing the
// pass's starting snapshot against the state it ended up with. A nil value marks a removal.
//
// This exists because the pass cannot write the value it computed. Its result was built from a
// snapshot taken at cluster-state build time, and JobReconciler/PodReconciler write the same
// annotation concurrently; writing the whole value back would revert a completion recorded in
// between. Only the entries the pass actually touched are its to apply.
type nodeStateDelta map[string]*v1alpha1.PackageStatus

// computeNodeStateDelta diffs before against after. Entries equal in both are omitted: the pass
// did not change them, so it has no opinion to impose on whatever the annotation holds now.
func computeNodeStateDelta(before, after v1alpha1.NodeState) nodeStateDelta {
	delta := make(nodeStateDelta)
	for key, afterStatus := range after {
		beforeStatus, existed := before[key]
		if !existed || beforeStatus != afterStatus {
			status := afterStatus
			delta[key] = &status
		}
	}
	for key := range before {
		if _, stillThere := after[key]; !stillThere {
			delta[key] = nil
		}
	}
	return delta
}

// apply lays the delta over whatever state is current, leaving every untouched entry alone.
func (d nodeStateDelta) apply(current v1alpha1.NodeState) v1alpha1.NodeState {
	merged := make(v1alpha1.NodeState, len(current)+len(d))
	for key, status := range current {
		merged[key] = status
	}
	for key, status := range d {
		if status == nil {
			delete(merged, key)
			continue
		}
		merged[key] = *status
	}
	return merged
}

// parseNodeState reads a node-state annotation. An absent key is an empty state, not an error.
func parseNodeState(node *corev1.Node, key string) (v1alpha1.NodeState, error) {
	raw, ok := node.Annotations[key]
	if !ok || raw == "" {
		return v1alpha1.NodeState{}, nil
	}
	state := v1alpha1.NodeState{}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("unmarshalling node state from %s: %w", key, err)
	}
	return state, nil
}

// saveNodeChanges persists one node's changes from this pass under an optimistic lock, re-merging
// the contended node-state annotation against whatever the annotation holds at write time.
//
// The heavy pass, JobReconciler and PodReconciler are three separate controllers with three
// workqueues, all writing nodewright.nvidia.com/nodeState_<name> — one annotation whose value is a
// single JSON document covering every package. Before the Job and Pod watches became their own
// controllers, pod events rode this controller's queue at MaxConcurrentReconciles 1 and could not
// interleave; now they can, and an unconditional patch of a snapshot-derived value silently
// reverts a completion recorded mid-pass. The Job is already marked state-recorded by then, so
// nothing re-records it; the divergence is only cleared by shouldDeleteFinishedJob tearing the
// finished Job down so the stage runs a second time.
//
// Locking the write alone would not fix that. The value is computed long before the write, so a
// lock would just serialize a stale value into place. What closes it is replacing that one value
// with this pass's delta applied on top of what the annotation holds now.
//
// Only that one value is re-derived. Everything else the pass changed stays an ordinary diff
// against the pass's own snapshot, which is what keeps the patch from mentioning any label,
// annotation, taint or cordon this pass did not touch.
func (r *SkyhookReconciler) saveNodeChanges(ctx context.Context, original *corev1.Node, node wrapper.SkyhookNode, skyhookName string) error {
	if original == nil {
		// Unreachable: BuildState tracks and adds a node in the same step, so every node in
		// GetNodes() has a snapshot. Say so instead of guessing at a delta — the old fallback
		// here diffed the object against a copy of itself, which is an empty patch, so a broken
		// invariant would have silently dropped the write rather than surfacing.
		return fmt.Errorf("no tracked snapshot for node %s; cannot derive its node-state delta", node.GetNode().Name)
	}

	key := nodeStateAnnotationKey(skyhookName)
	before, err := parseNodeState(original, key)
	if err != nil {
		return fmt.Errorf("reading the pass's starting node state for %s: %w", node.GetNode().Name, err)
	}
	after, err := parseNodeState(node.GetNode(), key)
	if err != nil {
		return fmt.Errorf("reading the pass's resulting node state for %s: %w", node.GetNode().Name, err)
	}
	delta := computeNodeStateDelta(before, after)

	// A pass that deleted the annotation outright (Reset) means it, and the plain diff below
	// carries that deletion. Re-merging would resurrect the key it just wiped.
	_, passKeptState := node.GetNode().Annotations[key]

	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, err := readNodeForPatch(ctx, r.dal, r.uncached, node.GetNode().Name, attempt)
		attempt++
		if err != nil {
			return fmt.Errorf("re-reading node %s before patching: %w", node.GetNode().Name, err)
		}
		if fresh == nil {
			return nil // node went away mid-pass; its state is not ours to resurrect
		}

		if passKeptState && len(delta) > 0 {
			current, err := parseNodeState(fresh, key)
			if err != nil {
				return fmt.Errorf("reading current node state for %s: %w", node.GetNode().Name, err)
			}
			raw, err := json.Marshal(delta.apply(current))
			if err != nil {
				return fmt.Errorf("marshalling merged node state for %s: %w", node.GetNode().Name, err)
			}
			node.GetNode().Annotations[key] = string(raw)
		}

		// The base does two jobs, and they want different objects. It is the left-hand side of the
		// diff, where the pass's own snapshot is what keeps the patch to the keys this pass
		// actually changed — a key another writer added since is in neither side, so the diff
		// cannot mention it. It is also where MergeFromWithOptimisticLock reads the
		// resourceVersion, and that has to be the read we just merged against, or the precondition
		// is stale before it is sent and every attempt loses.
		base := original.DeepCopy()
		base.SetResourceVersion(fresh.GetResourceVersion())

		patch := client.StrategicMergeFrom(base, client.MergeFromWithOptimisticLock{})
		// Wrapped with %w deliberately: RetryOnConflict decides via apierrors.IsConflict, which
		// unwraps, so the retry keeps working. The conflict-retry spec fails if that ever stops
		// being true.
		if err := r.Patch(ctx, node.GetNode(), patch); err != nil {
			return fmt.Errorf("patching node %s: %w", node.GetNode().Name, err)
		}
		// The wrapper caches a parsed copy of node state and the accessors read that cache
		// directly, so the merged annotation has to be re-read into it — otherwise
		// IsComplete/NextStage/UpdateCondition keep answering from the pass's pre-merge map.
		// Re-seeded, never nilled — see ReloadState for why a nil cache stalls the rollout.
		if err := node.ReloadState(); err != nil {
			return fmt.Errorf("reloading node state for %s after merge: %w", node.GetNode().Name, err)
		}
		return nil
	})
}

// SaveNodesAndSkyhook saves nodes and skyhook and will update the events if the skyhook status changes
func (r *SkyhookReconciler) SaveNodesAndSkyhook(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes) (bool, []error) {
	saved := false
	errs := make([]error, 0)
	logger := log.FromContext(ctx)

	for _, node := range skyhook.GetNodes() {
		if node.Changed() {
			originalNode, _ := clusterState.tracker.GetOriginal(node.GetNode()).(*corev1.Node)
			err := r.saveNodeChanges(ctx, originalNode, node, skyhook.GetSkyhook().Name)
			if err != nil {
				errs = append(errs, fmt.Errorf("error patching node [%s]: %w", node.GetNode().Name, err))
			}
			saved = true

			err = r.UpsertNodeLabelsAnnotationsPackages(ctx, skyhook.GetSkyhook(), node.GetNode())
			if err != nil {
				errs = append(errs, fmt.Errorf("error upserting labels, annotations, and packages config map for node [%s]: %w", node.GetNode().Name, err))
			}

			if node.IsComplete() {
				r.recorder.Eventf(node.GetNode(), nil, EventTypeNormal, EventsReasonSkyhookStateChange, "MarkComplete", "NodeWright [%s] complete.", skyhook.GetSkyhook().Name)

				// since node is complete remove from priority
				skyhook.GetSkyhook().RemoveNodePriority(node.GetNode().Name)
			}
		}

		// updates node's condition
		//
		// The base is snapshotted here, after saveNodeChanges has replaced the object with the one
		// the apiserver returned, so the diff is exactly the conditions UpdateCondition touches.
		// Basing it on the pass's original snapshot instead would re-send whatever the kubelet
		// changed to .status in the meantime, and would carry that snapshot's resourceVersion into
		// the patch body, where the apiserver reads it as a precondition — turning every
		// concurrent write into a 409 that suppresses the whole Skyhook's status update.
		statusBase := node.GetNode().DeepCopy()
		node.UpdateCondition()
		if node.Changed() {
			// conditions are in status
			err := r.Status().Patch(ctx, node.GetNode(), client.StrategicMergeFrom(statusBase))
			if err != nil {
				errs = append(errs, fmt.Errorf("error patching node status [%s]: %w", node.GetNode().Name, err))
			}
			saved = true
		}

		if node.GetSkyhook() != nil && node.GetSkyhook().Updated {
			skyhook.GetSkyhook().Updated = true
		}
	}

	if len(errs) == 0 {
		skyhook.UpdateCondition(logger)
	}

	if len(errs) == 0 && skyhook.GetSkyhook().Updated {
		patch := client.MergeFrom(clusterState.tracker.GetOriginal(skyhook.GetSkyhook().NodeWright))
		err := r.Status().Patch(ctx, skyhook.GetSkyhook().NodeWright, patch)
		if err != nil {
			errs = append(errs, err)
		}
		saved = true

		if skyhook.GetPriorStatus() != "" && skyhook.GetPriorStatus() != skyhook.Status() {
			// we transitioned, fire event
			r.recorder.Eventf(skyhook.GetSkyhook(), nil, EventTypeNormal, EventsReasonSkyhookStateChange, "Transition", "NodeWright transitioned [%s] -> [%s]", skyhook.GetPriorStatus(), skyhook.Status())
		}
	}

	if len(errs) > 0 {
		saved = false
	}
	return saved, errs
}

// HandleUninstallRequests checks for packages that need uninstall and triggers
// StageUninstall on nodes where the package is at a complete install stage. Returns the
// list of packages that need uninstall pods created.
// A package needs uninstall when:
//   - IsUninstalling()==true (explicit: user set apply=true), OR
//   - UninstallEnabled()==true and the CR is being deleted (finalizer-driven)
func HandleUninstallRequests(skyhook SkyhookNodes) ([]*v1alpha1.Package, error) {
	toUninstall := make([]*v1alpha1.Package, 0)
	beingDeleted := !skyhook.GetSkyhook().DeletionTimestamp.IsZero()
	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return nil, fmt.Errorf("node %s: reading state in HandleUninstallRequests: %w",
				node.GetNode().Name, err)
		}
		for name, pkg := range skyhook.GetSkyhook().Spec.Packages {
			status, exists := nodeState[pkg.GetUniqueName()]
			if !exists {
				continue // already uninstalled on this node (absent = done)
			}

			needsUninstall := pkg.IsUninstalling() || (beingDeleted && pkg.UninstallEnabled())
			if !needsUninstall && !nodeState.IsUninstallCycleInProgress(pkg.GetUniqueName()) {
				continue
			}

			// Handle packages progressing through the uninstall cycle.
			switch status.Stage {
			case v1alpha1.StageUninstall:
				// All states re-add so ApplyPackage / ProcessInterrupt sees the
				// package. StageUninstall/Complete is not used under the new rules
				// (HandleCompletePod transitions directly to either
				// StageUninstallInterrupt/InProgress or RemoveState), but we re-add
				// it defensively.
				p := skyhook.GetSkyhook().Spec.Packages[name]
				toUninstall = appendIfNotPresent(toUninstall, &p)
				continue

			case v1alpha1.StageUninstallInterrupt:
				if status.State == v1alpha1.StateComplete {
					if err := node.RemoveState(pkg.PackageRef); err != nil {
						return nil, fmt.Errorf("error removing state after uninstall interrupt for %s: %w", name, err)
					}
					zeroOutSkyhookPackageMetrics(skyhook.GetSkyhook().Name, pkg.Name, pkg.Version)
					node.SetStatus(v1alpha1.StatusInProgress)
				} else {
					p := skyhook.GetSkyhook().Spec.Packages[name]
					toUninstall = appendIfNotPresent(toUninstall, &p)
				}
				continue
			}

			// Install-cycle stages (Apply, Config, Interrupt, PostInterrupt, Upgrade):
			// only kick off uninstall from a terminal Complete state. Don't interrupt
			// an install mid-flight — wait for it.
			if status.State != v1alpha1.StateComplete {
				continue
			}
			if err := node.Upsert(pkg.PackageRef, pkg.Image,
				v1alpha1.StateInProgress, v1alpha1.StageUninstall, 0, pkg.ContainerSHA); err != nil {
				return nil, fmt.Errorf("error triggering uninstall for %s: %w", name, err)
			}
			node.SetStatus(v1alpha1.StatusInProgress)
			p := skyhook.GetSkyhook().Spec.Packages[name]
			toUninstall = appendIfNotPresent(toUninstall, &p)
		}
	}
	return toUninstall, nil
}

// HandleCancelledUninstalls resets packages at StageUninstall back to the install pipeline
// when uninstall.apply has been set to false (cancel). For packages where uninstall already
// completed (absent from node state), RunNext will naturally re-apply them.
// Skips cancellation during CR deletion — the finalizer drives uninstall via
// HandleUninstallRequests and must not be interfered with.
func HandleCancelledUninstalls(skyhook SkyhookNodes) error {
	// During CR deletion, the finalizer drives uninstall for enabled packages.
	// Do not cancel those — they must complete for the finalizer to proceed.
	beingDeleted := !skyhook.GetSkyhook().DeletionTimestamp.IsZero()

	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return fmt.Errorf("node %s: reading state in HandleCancelledUninstalls: %w",
				node.GetNode().Name, err)
		}
		for _, status := range nodeState {
			if status.Stage != v1alpha1.StageUninstall {
				continue
			}
			pkg, exists := skyhook.GetSkyhook().Spec.Packages[status.Name]
			if !exists {
				continue // removed from spec — HandleVersionChange handles
			}
			if pkg.IsUninstalling() {
				continue // still actively uninstalling — not cancelled
			}
			if beingDeleted && pkg.UninstallEnabled() {
				continue // finalizer-driven uninstall — do not cancel
			}
			// Package is at StageUninstall but apply is false → cancelled
			if status.IsActive() {
				// Reset to re-enter install pipeline
				err := node.Upsert(pkg.PackageRef, pkg.Image,
					v1alpha1.StateInProgress, v1alpha1.StageApply, 0, pkg.ContainerSHA)
				if err != nil {
					return fmt.Errorf("error resetting cancelled uninstall for %s: %w", status.Name, err)
				}
				node.SetStatus(v1alpha1.StatusInProgress)
			}
		}
	}
	return nil
}

// appendIfNotPresent appends a package to the list if not already present (by name+version).
func appendIfNotPresent(list []*v1alpha1.Package, pkg *v1alpha1.Package) []*v1alpha1.Package {
	for _, existing := range list {
		if existing.Name == pkg.Name && existing.Version == pkg.Version {
			return list
		}
	}
	return append(list, pkg)
}

// filterUninstallForNode keeps only the packages from toUninstall that are
// still tracked in nodeState. HandleUninstallRequests and HandleVersionChange
// build toUninstall globally across all of a Skyhook's nodes, so a package
// may be pending uninstall on one node while already absent on another;
// feeding those absent entries into ApplyPackage would fall through to
// StageApply and re-install a package the user explicitly uninstalled.
func filterUninstallForNode(toUninstall []*v1alpha1.Package, nodeState v1alpha1.NodeState) []*v1alpha1.Package {
	filtered := make([]*v1alpha1.Package, 0, len(toUninstall))
	for _, pkg := range toUninstall {
		if _, inState := nodeState[pkg.GetUniqueName()]; !inState {
			continue
		}
		filtered = append(filtered, pkg)
	}
	return filtered
}

// shouldSkipApplyForUninstall reports whether a package should be filtered
// out of a node's runnable set because uninstall is either in progress on
// this node or has already completed. Mirrors HandleUninstallRequests'
// needsUninstall predicate: a finalizer-driven delete (beingDeleted &&
// UninstallEnabled) counts as "uninstall requested" even when the spec's
// Uninstall.Apply is false, which IsUninstalling alone would miss.
func shouldSkipApplyForUninstall(pkg *v1alpha1.Package, nodeState v1alpha1.NodeState, beingDeleted bool) bool {
	if nodeState.IsUninstallCycleInProgress(pkg.GetUniqueName()) {
		return true
	}
	uninstallRequested := pkg.IsUninstalling() || (beingDeleted && pkg.UninstallEnabled())
	return uninstallRequested && nodeState.IsUninstalled(pkg.GetUniqueName())
}

// HandleVersionChange updates the state for the node or skyhook if a version is changed on a package
func HandleVersionChange(skyhook SkyhookNodes) ([]*v1alpha1.Package, error) {
	toUninstall := make([]*v1alpha1.Package, 0)
	versionChangeDetected := false

	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return nil, fmt.Errorf("node %s: reading state in HandleVersionChange: %w",
				node.GetNode().Name, err)
		}

		for _, packageStatus := range nodeState {
			_package, exists := skyhook.GetSkyhook().Spec.Packages[packageStatus.Name]

			// Skip packages where uninstall has started on this node — handled by HandleUninstallRequests.
			// Uses node annotation (StageUninstall) as source of truth, not the spec's apply flag.
			if exists && nodeState.IsUninstallCycleInProgress(_package.GetUniqueName()) {
				continue
			}

			if exists && _package.Version == packageStatus.Version {
				continue // no uninstall needed for package
			}

			if !exists {
				// Package removed from spec. The webhook blocks removal of
				// enabled=true packages unless the package is already fully
				// uninstalled on all nodes, so if we reach here the package
				// was enabled=false (or unset). Per D2 semantics, leave the
				// node state entry in place — non-absent signals to the user
				// that the package's files are still on the node (no
				// uninstall.sh ran). The operator no longer tracks it; any
				// future spec changes are what would clean it up.
				skyhook.GetSkyhook().RemoveConfigUpdates(packageStatus.Name)
				continue
			} else if exists && _package.Version != packageStatus.Version {
				versionChangeDetected = true
				comparison := version.Compare(_package.Version, packageStatus.Version)
				if comparison == -2 {
					return nil, errors.New("error comparing package versions: invalid version string provided enabling webhooks validates versions before being applied")
				}

				if comparison == 1 {
					// Upgrade path.
					_packageStatus, found := node.PackageStatus(_package.GetUniqueName())
					if found && _packageStatus.Stage == v1alpha1.StageUpgrade {
						continue
					}

					// start upgrade of package
					err := node.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, v1alpha1.StageUpgrade, 0, _package.ContainerSHA)
					if err != nil {
						return nil, fmt.Errorf("error updating node status: %w", err)
					}
				} else {
					// Downgrade: no-op. Webhook rejects downgrades of enabled=true packages
					// unless the package is already fully uninstalled. For enabled=false
					// packages, the old-version state stays in node state per D2 semantics
					// (non-absent = "not cleanly uninstalled, just superseded").
					continue
				}
			}

			// remove all config updates for the package since it's being uninstalled or
			// upgraded. NOTE: The config updates must be removed whenever the version changes
			// or else the package interrupt may be skipped if there is one.
			// Use packageStatus.Name (from node state) since _package may be zero-value when
			// the package has been removed from spec.
			skyhook.GetSkyhook().RemoveConfigUpdates(packageStatus.Name)

			// set the node and skyhook status to in progress
			node.SetStatus(v1alpha1.StatusInProgress)
		}
	}

	// Auto-reset batch state when version changes are detected (if configured)
	if versionChangeDetected {
		resetSkyhookBatchState(skyhook)
	}

	return toUninstall, nil
}

// helper for get a point to a ref
func ptr[E any](e E) *E {
	return &e
}

// generateSafeName generates a consistent name for Kubernetes resources that is unique
// while staying within the specified character limit
func generateSafeName(maxLen int, nameParts ...string) string {
	name := strings.Join(nameParts, "-")
	// Replace dots with dashes as they're not allowed in resource names
	name = strings.ReplaceAll(name, ".", "-")

	unique := sha256.Sum256([]byte(name))
	uniqueStr := hex.EncodeToString(unique[:])[:8]

	maxlen := maxLen - len(uniqueStr) - 1
	if len(name) > maxlen {
		name = name[:maxlen]
	}

	return strings.ToLower(fmt.Sprintf("%s-%s", name, uniqueStr))
}

func (r *SkyhookReconciler) UpsertNodeLabelsAnnotationsPackages(ctx context.Context, skyhook *wrapper.Skyhook, node *corev1.Node) error {
	// No work to do if there is no labels or annotations for node
	if len(node.Labels) == 0 && len(node.Annotations) == 0 {
		return nil
	}

	annotations, err := json.Marshal(node.Annotations)
	if err != nil {
		return fmt.Errorf("error converting annotations into byte array: %w", err)
	}

	labels, err := json.Marshal(node.Labels)
	if err != nil {
		return fmt.Errorf("error converting labels into byte array: %w", err)
	}

	// marshal intermediary package metadata for the agent
	metadata := NewSkyhookMetadata(r.opts, skyhook)
	packages, err := metadata.Marshal()
	if err != nil {
		return fmt.Errorf("error converting packages into byte array: %w", err)
	}

	configMapName := generateSafeName(253, skyhook.Name, node.Name, "metadata")
	newCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: r.opts.Namespace,
			Labels: map[string]string{
				fmt.Sprintf("%s/skyhook-node-meta", v1alpha1.METADATA_PREFIX): skyhook.Name,
			},
			Annotations: map[string]string{
				fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):      skyhook.Name,
				fmt.Sprintf("%s/Node.name", v1alpha1.METADATA_PREFIX): node.Name,
			},
		},
		Data: map[string]string{
			"annotations.json": string(annotations),
			"labels.json":      string(labels),
			"packages.json":    string(packages),
		},
	}

	if err := ctrl.SetControllerReference(skyhook.NodeWright, newCM, r.scheme); err != nil {
		return fmt.Errorf("error setting ownership: %w", err)
	}

	existingConfigMap := &corev1.ConfigMap{}
	err = r.Get(ctx, client.ObjectKey{Namespace: r.opts.Namespace, Name: configMapName}, existingConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// create
			err := r.Create(ctx, newCM)
			if err != nil {
				return fmt.Errorf("error creating config map [%s]: %w", newCM.Name, err)
			}
		} else {
			return fmt.Errorf("error getting config map: %w", err)
		}
	} else {
		if !reflect.DeepEqual(existingConfigMap.Data, newCM.Data) {
			// update
			err := r.Update(ctx, newCM)
			if err != nil {
				return fmt.Errorf("error updating config map [%s]: %w", newCM.Name, err)
			}
		}
	}

	return nil
}

// configSyncRetryInterval is the requeue delay used when HandleConfigUpdates
// observes a ConfigMap diff it cannot yet apply because the completedNodes gate
// is closed. Without it the only fallback is the 10m MaxInterval requeue, which
// can leave an owned ConfigMap diverged from spec for minutes while status reads
// complete (issue #245). Short enough to heal quickly, long enough not to spin
// the grab-the-world reconcile while a node works through an interrupt cycle.
const configSyncRetryInterval = 30 * time.Second

// HandleConfigUpdates checks whether the configMap on a package was updated and if it was the configmap will
// be updated and the package will be put into config mode if the package is complete or erroring.
//
// The second return value (pendingSync) reports that a ConfigMap diff was observed
// but could not be applied this reconcile because the completedNodes gate was
// closed; the caller should requeue so the write is retried once the gate opens.
func (r *SkyhookReconciler) HandleConfigUpdates(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes, _package v1alpha1.Package, oldConfigMap, newConfigMap *corev1.ConfigMap) (bool, bool, error) {
	completedNodes, nodeCount := 0, len(skyhook.GetNodes())
	erroringNode := false

	// if configmap changed
	if !reflect.DeepEqual(oldConfigMap.Data, newConfigMap.Data) {
		for _, node := range skyhook.GetNodes() {
			exists, err := r.JobExists(ctx, node.GetNode().Name, skyhook.GetSkyhook().Name, &_package)
			if err != nil {
				return false, false, fmt.Errorf("checking package pod existence on node %s for package %s: %w",
					node.GetNode().Name, _package.GetUniqueName(), err)
			}

			if !exists && node.IsPackageComplete(_package) {
				completedNodes++
			}

			// if we have an erroring node in the config, interrupt, or post-interrupt mode
			// then we will restart the config changes
			if packageStatus, found := node.PackageStatus(_package.GetUniqueName()); found {
				switch packageStatus.Stage {
				case v1alpha1.StageConfig, v1alpha1.StageInterrupt, v1alpha1.StagePostInterrupt:
					if packageStatus.State == v1alpha1.StateErroring {
						erroringNode = true

						// clear the package's in-flight or timed-out executors on the node so the config
						// change re-runs the stage with the updated configmap
						if err := r.deleteConfigUpdateExecutors(ctx, node, skyhook.GetSkyhook().Name, &_package); err != nil {
							return false, false, err
						}
					}
				}
			}
		}

		// if the update is complete or there is an erroring node put the package back into
		// the config mode and update the config map
		if completedNodes == nodeCount || erroringNode {
			// get the keys in the configmap that changed
			newConfigUpdates := make([]string, 0)
			for key, new_val := range newConfigMap.Data {
				if old_val, exists := oldConfigMap.Data[key]; !exists || old_val != new_val {
					newConfigUpdates = append(newConfigUpdates, key)
				}
			}

			// if updates completed then clear out old config updates as they are finished
			if completedNodes == nodeCount {
				skyhook.GetSkyhook().RemoveConfigUpdates(_package.Name)
			}

			// Add the new changed keys to the config updates
			skyhook.GetSkyhook().AddConfigUpdates(_package.Name, newConfigUpdates...)

			for _, node := range skyhook.GetNodes() {
				err := node.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, v1alpha1.StageConfig, 0, _package.ContainerSHA)
				if err != nil {
					return false, false, fmt.Errorf("error upserting node status [%s]: %w", node.GetNode().Name, err)
				}

				node.SetStatus(v1alpha1.StatusInProgress)
			}

			_, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
			if len(errs) > 0 {
				return false, false, utilerrors.NewAggregate(errs)
			}

			// update config map
			err := r.Update(ctx, newConfigMap)
			if err != nil {
				return false, false, fmt.Errorf("error updating config map [%s]: %w", newConfigMap.Name, err)
			}

			return true, false, nil
		}

		// Diff observed but the gate is closed (no node has completed the package
		// and none is erroring). Defer the CM write and ask the caller to requeue
		// so we retry once the gate opens, rather than relying on the 10m fallback.
		return false, true, nil
	}

	return false, false, nil
}

func (r *SkyhookReconciler) UpsertConfigmaps(ctx context.Context, skyhook SkyhookNodes, clusterState *clusterState) (bool, bool, error) {
	updated := false
	pendingSync := false

	var list corev1.ConfigMapList
	err := r.List(ctx, &list, client.InNamespace(r.opts.Namespace), client.MatchingLabels{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name})
	if err != nil {
		return false, false, fmt.Errorf("error listing config maps while upserting: %w", err)
	}

	existingCMs := make(map[string]corev1.ConfigMap)
	for _, cm := range list.Items {
		existingCMs[cm.Name] = cm
	}

	// clean up from an update
	shouldExist := make(map[string]struct{})
	for _, _package := range skyhook.GetSkyhook().Spec.Packages {
		shouldExist[strings.ToLower(fmt.Sprintf("%s-%s-%s", skyhook.GetSkyhook().Name, _package.Name, _package.Version))] = struct{}{}
	}

	for k, v := range existingCMs {
		if _, ok := shouldExist[k]; !ok {
			// delete
			err := r.Delete(ctx, &v)
			if err != nil {
				return false, false, fmt.Errorf("error deleting existing config map [%s] while upserting: %w", v.Name, err)
			}
		}
	}

	// Build set of packages that are being uninstalled on any node (source of truth).
	// A failed state read on any node must propagate: if we silently skip an
	// unreadable node, we might miss a package mid-uninstall on that node and
	// then blow away its ConfigMap in the loop below, interfering with the
	// in-progress uninstall. Surface the error so the reconcile requeues and
	// the user-visible NodeStateMalformed condition (set at the top of
	// Reconcile) stays accurate.
	uninstallingPkgs := make(map[string]bool)
	for _, node := range skyhook.GetNodes() {
		ns, err := node.State()
		if err != nil {
			return false, false, fmt.Errorf("node %s: reading state while upserting configmaps: %w",
				node.GetNode().Name, err)
		}
		for _, _package := range skyhook.GetSkyhook().Spec.Packages {
			if ns.IsUninstallCycleInProgress(_package.GetUniqueName()) {
				uninstallingPkgs[_package.Name] = true
			}
		}
	}

	for _, _package := range skyhook.GetSkyhook().Spec.Packages {
		if uninstallingPkgs[_package.Name] {
			continue // config changes should not interfere with an in-progress uninstall
		}
		if len(_package.ConfigMap) > 0 {

			newCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      strings.ToLower(fmt.Sprintf("%s-%s-%s", skyhook.GetSkyhook().Name, _package.Name, _package.Version)),
					Namespace: r.opts.Namespace,
					Labels: map[string]string{
						fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name,
					},
					Annotations: map[string]string{
						fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):            skyhook.GetSkyhook().Name,
						fmt.Sprintf("%s/Package.Name", v1alpha1.METADATA_PREFIX):    _package.Name,
						fmt.Sprintf("%s/Package.Version", v1alpha1.METADATA_PREFIX): _package.Version,
					},
				},
				Data: _package.ConfigMap,
			}
			// set owner of CM to the SCR, which will clean up the CM in delete of the SCR
			if err := ctrl.SetControllerReference(skyhook.GetSkyhook().NodeWright, newCM, r.scheme); err != nil {
				return false, false, fmt.Errorf("error setting ownership of cm: %w", err)
			}

			if existingCM, ok := existingCMs[strings.ToLower(fmt.Sprintf("%s-%s-%s", skyhook.GetSkyhook().Name, _package.Name, _package.Version))]; ok {
				updatedConfigMap, pendingConfigSync, err := r.HandleConfigUpdates(ctx, clusterState, skyhook, _package, &existingCM, newCM)
				if err != nil {
					return false, false, fmt.Errorf("error updating config map [%s]: %w", newCM.Name, err)
				}
				if updatedConfigMap {
					updated = true
				}
				if pendingConfigSync {
					pendingSync = true
				}
			} else {
				// create
				err := r.Create(ctx, newCM)
				if err != nil {
					return false, false, fmt.Errorf("error creating config map [%s]: %w", newCM.Name, err)
				}
			}
		}
	}

	return updated, pendingSync, nil
}

func (r *SkyhookReconciler) IsDrained(ctx context.Context, skyhookNode wrapper.SkyhookNode) (bool, error) {

	pods, err := r.dal.GetPods(ctx, client.MatchingFields{
		fieldSelectorNodeName: skyhookNode.GetNode().Name,
	})
	if err != nil {
		return false, err
	}

	if pods == nil || len(pods.Items) == 0 {
		return true, nil
	}

	options := drain.OptionsFromConfig(skyhookNode.GetSkyhook().Spec.DrainConfig)
	options.PackageNamespace = r.opts.Namespace
	for _, pod := range pods.Items {
		if drain.DecidePod(&pod, options).BlocksDrain() {
			return false, nil
		}
	}

	return true, nil
}

// HandleFinalizer returns true only if we container is deleted and we handled it completely, else false.
// For Skyhooks with UninstallEnabled packages, this uses a multi-reconcile flow:
// Phase 1: trigger uninstall for enabled packages, return false to requeue
// Phase 2: check completion, requeue if still in progress
// Phase 3: cleanup (zero metrics, uncordon, remove SCR metadata, remove finalizer)
func (r *SkyhookReconciler) HandleFinalizer(ctx context.Context, skyhook SkyhookNodes, clusterState *clusterState) (bool, error) {
	if skyhook.GetSkyhook().DeletionTimestamp.IsZero() { // if not deleted, and does not have our finalizer, add it
		if !controllerutil.ContainsFinalizer(skyhook.GetSkyhook().NodeWright, SkyhookFinalizer) {
			patch := client.MergeFromWithOptions(
				skyhook.GetSkyhook().NodeWright.DeepCopy(),
				client.MergeFromWithOptimisticLock{},
			)
			controllerutil.AddFinalizer(skyhook.GetSkyhook().NodeWright, SkyhookFinalizer)

			if err := r.Patch(ctx, skyhook.GetSkyhook().NodeWright, patch); err != nil {
				return false, fmt.Errorf("error patching nodewright to add finalizer: %w", err)
			}
		}
	} else { // being deleted
		if controllerutil.ContainsFinalizer(skyhook.GetSkyhook().NodeWright, SkyhookFinalizer) {

			// Phase 2: scan uninstall-enabled packages across all nodes to
			// decide whether to block, wait, or proceed to Phase 3. Capture
			// both a state-read error (if any) and whether any uninstall-
			// enabled package is still tracked in nodeState.
			var stateErr error
			hasPendingUninstall := false
			for _, pkg := range skyhook.GetSkyhook().Spec.Packages {
				if !pkg.UninstallEnabled() {
					continue
				}
				for _, node := range skyhook.GetNodes() {
					nodeState, err := node.State()
					if err != nil {
						stateErr = fmt.Errorf(
							"error reading node state for finalizer on node %s: %w",
							node.GetNode().Name, err,
						)
						break
					}
					if _, inState := nodeState[pkg.GetUniqueName()]; inState {
						hasPendingUninstall = true
					}
				}
				if stateErr != nil {
					break
				}
			}

			switch {
			case stateErr != nil:
				// Malformed nodeState: we can't safely decide what to
				// preserve or what still needs uninstalling. NodeStateMalformed
				// is set separately at the top of reconcile; surface
				// DeletionBlocked too so the impact on deletion is explicit.
				// Return the error so controller-runtime retries with backoff —
				// the user must repair the annotation to proceed.
				wrapper.AddSkyhookCondition(skyhook.GetSkyhook(), metav1.Condition{
					Type:               wrapper.SkyhookConditionDeletionBlocked,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: skyhook.GetSkyhook().Generation,
					LastTransitionTime: metav1.Now(),
					Reason:             "MalformedNodeState",
					Message: "Cannot safely delete NodeWright: malformed nodeState on one or more nodes. " +
						"Repair the nodeState annotation before deletion.",
				})
				r.recorder.Eventf(
					skyhook.GetSkyhook().NodeWright,
					nil,
					corev1.EventTypeWarning,
					"DeletionBlocked",
					"BlockDelete",
					"Cannot delete NodeWright %s: malformed nodeState. Repair and retry.",
					skyhook.GetSkyhook().Name,
				)
				if _, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook); len(errs) > 0 {
					return false, utilerrors.NewAggregate(errs)
				}
				return false, stateErr

			case skyhook.IsPaused() && hasPendingUninstall:
				// Paused Skyhooks cannot drive uninstall (processSkyhooksPerNode
				// short-circuits). Block deletion so the user unpauses and
				// lets uninstall run rather than silently leaving host-side
				// remnants — pause is a temporary "resume later" signal.
				wrapper.AddSkyhookCondition(skyhook.GetSkyhook(), metav1.Condition{
					Type:               wrapper.SkyhookConditionDeletionBlocked,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: skyhook.GetSkyhook().Generation,
					LastTransitionTime: metav1.Now(),
					Reason:             "PausedWithPendingUninstall",
					Message: "NodeWright is paused with uninstall-enabled packages still tracked in nodeState. " +
						"Unpause to let uninstall complete before deletion.",
				})
				r.recorder.Eventf(
					skyhook.GetSkyhook().NodeWright,
					nil,
					corev1.EventTypeWarning,
					"DeletionBlocked",
					"BlockDelete",
					"Cannot delete NodeWright %s: paused with uninstall work pending. Unpause to proceed.",
					skyhook.GetSkyhook().Name,
				)
				if _, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook); len(errs) > 0 {
					return false, utilerrors.NewAggregate(errs)
				}
				return false, nil

			case skyhook.IsDisabled() && hasPendingUninstall:
				// Disabled Skyhooks also can't drive uninstall
				// (processSkyhooksPerNode short-circuits on disable).
				// uninstall.enabled=true packages are an explicit request to
				// run uninstall scripts before the CR goes away; allowing
				// deletion here would silently leave host-side state that
				// should have been cleaned up. Block and require the user
				// to re-enable the Skyhook so the uninstall flow can run.
				wrapper.AddSkyhookCondition(skyhook.GetSkyhook(), metav1.Condition{
					Type:               wrapper.SkyhookConditionDeletionBlocked,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: skyhook.GetSkyhook().Generation,
					LastTransitionTime: metav1.Now(),
					Reason:             "DisabledWithPendingUninstall",
					Message: "NodeWright is disabled with uninstall-enabled packages still tracked in nodeState. " +
						"Re-enable the NodeWright to let uninstall complete before deletion.",
				})
				r.recorder.Eventf(
					skyhook.GetSkyhook().NodeWright,
					nil,
					corev1.EventTypeWarning,
					"DeletionBlocked",
					"BlockDelete",
					"Cannot delete NodeWright %s: disabled with uninstall work pending. Re-enable to proceed.",
					skyhook.GetSkyhook().Name,
				)
				if _, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook); len(errs) > 0 {
					return false, utilerrors.NewAggregate(errs)
				}
				return false, nil

			case hasPendingUninstall:
				// Normal path: wait for the main reconcile to drive packages
				// out of nodeState via the uninstall flow
				// (processSkyhooksPerNode → RunSkyhookPackages →
				// HandleUninstallRequests → ApplyPackage). The finalizer only
				// gates cleanup.
				wrapper.RemoveSkyhookConditionTypes(skyhook.GetSkyhook(), wrapper.SkyhookConditionDeletionBlocked)
				return false, nil

			default:
				// No pending uninstall — proceed to Phase 3.
				wrapper.RemoveSkyhookConditionTypes(skyhook.GetSkyhook(), wrapper.SkyhookConditionDeletionBlocked)
			}

			// Phase 3: All enabled packages uninstalled (or none exist). Cleanup.
			errs := make([]error, 0)

			// zero out all the metrics related to this skyhook both skyhook and packages
			zeroOutSkyhookMetrics(skyhook)

			for _, node := range skyhook.GetNodes() {
				patch := client.StrategicMergeFrom(node.GetNode().DeepCopy())

				node.Uncordon()
				node.CleanupSCRMetadata()

				// if this doesn't change the node then don't patch
				if !node.Changed() {
					continue
				}

				err := r.Patch(ctx, node.GetNode(), patch)
				if err != nil {
					errs = append(errs, fmt.Errorf("error patching node [%s] in finalizer: %w", node.GetNode().Name, err))
				}
			}

			if len(errs) > 0 { // we errored, so we need to return error, otherwise we would release the skyhook when we didnt finish
				return false, utilerrors.NewAggregate(errs)
			}

			// Write status BEFORE removing the finalizer:
			// removing the last finalizer lets the apiserver delete the object
			// immediately, so a status update afterward would race that deletion into a
			// spurious NotFound.
			if err := r.Status().Update(ctx, skyhook.GetSkyhook().NodeWright); err != nil {
				return false, fmt.Errorf("error updating nodewright status: %w", err)
			}

			patch := client.MergeFromWithOptions(
				skyhook.GetSkyhook().NodeWright.DeepCopy(),
				client.MergeFromWithOptimisticLock{},
			)
			controllerutil.RemoveFinalizer(skyhook.GetSkyhook().NodeWright, SkyhookFinalizer)
			if err := r.Patch(ctx, skyhook.GetSkyhook().NodeWright, patch); err != nil {
				return false, fmt.Errorf("error patching nodewright removing finalizer: %w", err)
			}

			return true, nil
		}
	}
	return false, nil
}

// HasNonInterruptWork returns true if pods are running on the node that are either packages, or matches the SCR selector
func (r *SkyhookReconciler) HasNonInterruptWork(ctx context.Context, skyhookNode wrapper.SkyhookNode) (bool, error) {

	selector, err := metav1.LabelSelectorAsSelector(&skyhookNode.GetSkyhook().Spec.PodNonInterruptLabels)
	if err != nil {
		return false, fmt.Errorf("error creating selector: %w", err)
	}

	if selector.Empty() { // when selector is empty it does not do any selecting, ie will return all pods on node.
		return false, nil
	}

	pods, err := r.dal.GetPods(ctx,
		client.MatchingLabelsSelector{Selector: selector},
		client.MatchingFields{
			fieldSelectorNodeName: skyhookNode.GetNode().Name,
		},
	)
	if err != nil {
		return false, fmt.Errorf("error getting pods: %w", err)
	}

	if pods == nil || len(pods.Items) == 0 {
		return false, nil
	}

	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case corev1.PodRunning, corev1.PodPending:
			return true, nil
		}
	}

	return false, nil
}

func (r *SkyhookReconciler) HasRunningPackages(ctx context.Context, skyhookNode wrapper.SkyhookNode) (bool, error) {
	nodeName := skyhookNode.GetNode().Name

	// Any unfinished Job for any package/skyhook on this node counts as running work that an
	// interrupt must wait out (a retained Succeeded Job must not hold the interrupt hostage).
	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.HasLabels{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX)},
		client.MatchingLabels{fmt.Sprintf("%s/node", v1alpha1.METADATA_PREFIX): nodeLabelValue(nodeName)},
	)
	if err != nil {
		return false, fmt.Errorf("error getting jobs: %w", err)
	}
	if jobs != nil {
		for i := range jobs.Items {
			if !jobFinished(&jobs.Items[i]) {
				return true, nil
			}
		}
	}

	return false, nil
}

func (r *SkyhookReconciler) DrainNode(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) (bool, error) {
	drained, err := r.IsDrained(ctx, skyhookNode)
	if err != nil {
		return false, err
	}
	if drained {
		skyhookNode.ClearDrainStart()
		return true, nil
	}

	drainStartedAt, err := skyhookNode.DrainStartedAt()
	if err != nil {
		return false, fmt.Errorf("error reading drain start for node [%s]: %w", skyhookNode.GetNode().Name, err)
	}

	drainConfig := skyhookNode.GetSkyhook().Spec.DrainConfig
	now := metav1.Now()
	if drainStartedAt == nil {
		skyhookNode.StartDrain(now)
		skyhookNode.SetStatus(v1alpha1.StatusInProgress)
	} else if drainConfig != nil && drain.TimedOut(drainStartedAt, drainConfig.Timeout, now.Time) {
		r.recorder.Eventf(skyhookNode.GetNode(), nil, corev1.EventTypeWarning, EventsReasonSkyhookDrain, "DrainTimeout",
			"drain timed out after [%s] for node [%s] package [%s:%s] from [nodewright:%s]",
			drainConfig.Timeout.Duration,
			skyhookNode.GetNode().Name,
			_package.Name,
			_package.Version,
			skyhookNode.GetSkyhook().Name,
		)
		r.recorder.Eventf(skyhookNode.GetSkyhook().NodeWright, nil, corev1.EventTypeWarning, EventsReasonSkyhookDrain, "DrainTimeout",
			"drain timed out after [%s] for node [%s] package [%s:%s]",
			drainConfig.Timeout.Duration,
			skyhookNode.GetNode().Name,
			_package.Name,
			_package.Version,
		)
		skyhookNode.SetStatus(v1alpha1.StatusErroring)
		return false, nil
	}

	pods, err := r.dal.GetPods(ctx, client.MatchingFields{
		fieldSelectorNodeName: skyhookNode.GetNode().Name,
	})
	if err != nil {
		return false, err
	}

	if pods == nil || len(pods.Items) == 0 {
		return true, nil
	}

	r.recorder.Eventf(skyhookNode.GetNode(), nil, EventTypeNormal, EventsReasonSkyhookDrain, "DrainNode",
		"draining node [%s] package [%s:%s] from [nodewright:%s]",
		skyhookNode.GetNode().Name,
		_package.Name,
		_package.Version,
		skyhookNode.GetSkyhook().Name,
	)

	options := drain.OptionsFromConfig(skyhookNode.GetSkyhook().Spec.DrainConfig)
	options.PackageNamespace = r.opts.Namespace
	errs := make([]error, 0)
	waitingForPods := false
	for _, pod := range pods.Items {
		decision := drain.DecidePod(&pod, options)
		switch decision.Action {
		case drain.ActionBlock:
			waitingForPods = true
		case drain.ActionEvict:
			waitingForPods = true
			eviction := policyv1.Eviction{DeleteOptions: options.EvictionDeleteOptions()}
			err := r.Client.SubResource("eviction").Create(ctx, &pod, &eviction)
			if err != nil {
				errs = append(errs, fmt.Errorf("error evicting pod [%s:%s]: %w", pod.Namespace, pod.Name, err))
			}
		case drain.ActionDelete:
			waitingForPods = true
			err := r.Delete(ctx, &pod, options.DeleteOptions()...)
			if err != nil {
				errs = append(errs, fmt.Errorf("error deleting pod [%s:%s]: %w", pod.Namespace, pod.Name, err))
			}
		}
	}

	if len(errs) > 0 {
		return false, utilerrors.NewAggregate(errs)
	}

	return !waitingForPods, nil
}

// Interrupt should not be called unless safe to do so, IE already cordoned and drained
func (r *SkyhookReconciler) Interrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package, _interrupt *v1alpha1.Interrupt, stage v1alpha1.Stage) error {

	hasPackagesRunning, err := r.HasRunningPackages(ctx, skyhookNode)
	if err != nil {
		return err
	}

	if hasPackagesRunning { // keep waiting...
		return nil
	}

	exists, err := r.JobExists(ctx, skyhookNode.GetNode().Name, skyhookNode.GetSkyhook().Name, _package)
	if err != nil {
		return err
	}
	if exists {
		// nothing to do here, already running
		return nil
	}

	// Ensure the node metadata configmap exists before creating the pod
	// This prevents a race where the pod starts before its required configmap is created
	if err := r.UpsertNodeLabelsAnnotationsPackages(ctx, skyhookNode.GetSkyhook(), skyhookNode.GetNode()); err != nil {
		return fmt.Errorf("error upserting node metadata configmap: %w", err)
	}

	argEncode, err := _interrupt.ToArgs()
	if err != nil {
		return fmt.Errorf("error creating interrupt args: %w", err)
	}

	job := createInterruptJobFromPackage(r.opts, _interrupt, argEncode, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name, stage)

	if err := setJobPackage(job, skyhookNode.GetSkyhook().NodeWright, _package.Image, stage, _package); err != nil {
		return fmt.Errorf("error setting package on interrupt: %w", err)
	}

	if err := ctrl.SetControllerReference(skyhookNode.GetSkyhook().NodeWright, job, r.scheme); err != nil {
		return fmt.Errorf("error setting ownership: %w", err)
	}

	if err := r.Create(ctx, job); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return r.handleExistingJob(ctx, job, _package, skyhookNode, stage)
		}
		return fmt.Errorf("error creating interruption job: %w", err)
	}

	_ = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, stage, 0, _package.ContainerSHA)

	r.recorder.Eventf(skyhookNode.GetSkyhook().NodeWright, nil, EventTypeNormal, EventsReasonSkyhookInterrupt, "InterruptNode",
		"Interrupting node [%s] package [%s:%s] from [nodewright:%s]",
		skyhookNode.GetNode().Name,
		_package.Name,
		_package.Version,
		skyhookNode.GetSkyhook().Name)

	return nil
}

// fudgeInterruptWithPriority takes a list of packages, interrupts, and configUpdates and returns the correct merged interrupt to run to handle all the packages
func fudgeInterruptWithPriority(next []*v1alpha1.Package, configUpdates map[string][]string, interrupts map[string][]*v1alpha1.Interrupt) (*v1alpha1.Interrupt, string) {
	var ret *v1alpha1.Interrupt
	var pack string

	// map interrupt to priority
	// A lower priority value means a higher priority and will be used in favor of anything with a higher value
	var priorities = map[v1alpha1.InterruptType]int{
		v1alpha1.REBOOT:               0,
		v1alpha1.RESTART_ALL_SERVICES: 1,
		v1alpha1.SERVICE:              2,
		v1alpha1.NOOP:                 3,
	}

	for _, _package := range next {

		if len(configUpdates[_package.Name]) == 0 {
			interrupts[_package.Name] = []*v1alpha1.Interrupt{}
			if _package.HasInterrupt() {
				interrupts[_package.Name] = append(interrupts[_package.Name], _package.Interrupt)
			}
		}
	}

	packageNames := make([]string, 0, len(next))
	for _, pkg := range next {
		packageNames = append(packageNames, pkg.Name)
	}
	sort.Strings(packageNames)

	for _, _package := range packageNames {
		_interrupts, ok := interrupts[_package]
		if !ok {
			continue
		}

		for _, interrupt := range _interrupts {
			if ret == nil { // prime ret, base case
				ret = interrupt
				pack = _package
			}

			// short circuit, reboot has highest priority
			switch interrupt.Type {
			case v1alpha1.REBOOT:
				return interrupt, _package
			}

			// check if interrupt is higher priority using the priority_order
			// A lower priority value means a higher priority
			if priorities[interrupt.Type] < priorities[ret.Type] {
				ret = interrupt
				pack = _package
			} else if priorities[interrupt.Type] == priorities[ret.Type] {
				mergeInterrupt(ret, interrupt)
			}
		}
	}

	return ret, pack // return merged interrupt and package
}

func mergeInterrupt(left, right *v1alpha1.Interrupt) {

	// make sure both are of type service
	if left.Type != v1alpha1.SERVICE || right.Type != v1alpha1.SERVICE {
		return
	}

	left.Services = merge(left.Services, right.Services)
}

func merge[T cmp.Ordered](left, right []T) []T {
	for _, r := range right {
		if !slices.Contains(left, r) {
			left = append(left, r)
		}
	}
	slices.Sort(left)
	return left
}

// ValidateNodeConfigmaps validates that there are no orphaned or stale config maps for a node
func (r *SkyhookReconciler) ValidateNodeConfigmaps(ctx context.Context, skyhookName string, nodes []wrapper.SkyhookNode) (bool, error) {
	var list corev1.ConfigMapList
	err := r.List(ctx, &list, client.InNamespace(r.opts.Namespace), client.MatchingLabels{fmt.Sprintf("%s/skyhook-node-meta", v1alpha1.METADATA_PREFIX): skyhookName})
	if err != nil {
		return false, fmt.Errorf("error listing config maps: %w", err)
	}

	// No configmaps created by this skyhook, no work needs to be done
	if len(list.Items) == 0 {
		return false, nil
	}

	existingCMs := make(map[string]corev1.ConfigMap)
	for _, cm := range list.Items {
		existingCMs[cm.Name] = cm
	}

	shouldExist := make(map[string]struct{})
	for _, node := range nodes {
		shouldExist[generateSafeName(253, skyhookName, node.GetNode().Name, "metadata")] = struct{}{}
	}

	update := false
	errs := make([]error, 0)
	for k, v := range existingCMs {
		if _, ok := shouldExist[k]; !ok {
			update = true
			err := r.Delete(ctx, &v)
			if err != nil {
				errs = append(errs, fmt.Errorf("error deleting existing config map [%s]: %w", v.Name, err))
			}
		}
	}

	// Ensure packages.json is present and up-to-date for expected configmaps
	skyhookCR, err := r.dal.GetSkyhook(ctx, skyhookName)
	if err != nil {
		return update, fmt.Errorf("error getting nodewright for metadata validation: %w", err)
	}
	skyhookWrapper := wrapper.NewSkyhookWrapper(skyhookCR)
	metadata := NewSkyhookMetadata(r.opts, skyhookWrapper)
	expectedBytes, err := metadata.Marshal()
	if err != nil {
		return update, fmt.Errorf("error marshalling metadata for validation: %w", err)
	}
	expected := string(expectedBytes)

	for i := range list.Items {
		cm := &list.Items[i]
		if _, ok := shouldExist[cm.Name]; !ok {
			continue
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		if cm.Data["packages.json"] != expected {
			cm.Data["packages.json"] = expected
			if err := r.Update(ctx, cm); err != nil {
				errs = append(errs, fmt.Errorf("error updating packages.json on config map [%s]: %w", cm.Name, err))
			} else {
				update = true
			}
		}
	}

	return update, utilerrors.NewAggregate(errs)
}

// JobExists reports whether an unfinished stage Job for this package still runs on the node. A
// finished Job does not count, so a stage re-runs after its retained Job is cleaned up.
//
// Deliberately not stage-scoped: two executors for one package on a node share the same hostPath
// copy dir, so an apply Job must not start beside a live config Job.
func (r *SkyhookReconciler) JobExists(ctx context.Context, nodeName, skyhookName string, _package *v1alpha1.Package) (bool, error) {
	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):    skyhookName,
			fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX): fmt.Sprintf("%s-%s", _package.Name, _package.Version),
			fmt.Sprintf("%s/node", v1alpha1.METADATA_PREFIX):    nodeLabelValue(nodeName),
		},
	)
	if err != nil {
		return false, fmt.Errorf("error checking existing jobs: %w", err)
	}
	if jobs != nil {
		for i := range jobs.Items {
			if !jobFinished(&jobs.Items[i]) {
				return true, nil
			}
		}
	}

	return false, nil
}

// handleExistingJob resolves an AlreadyExists on create against the deterministic Job name. It
// never records in_progress (the create did not happen this pass): an unfinished Job that matches
// is a benign race won by another pass; a timed-out Job whose entry sits erroring is
// left in place to absorb the recreate; an unprocessed completion is left for JobReconcile; and a
// stale-spec unfinished Job or a processed finished Job is foreground-deleted so the stage can
// re-run next pass.
func (r *SkyhookReconciler) handleExistingJob(ctx context.Context, want *batchv1.Job, _package *v1alpha1.Package, skyhookNode wrapper.SkyhookNode, stage v1alpha1.Stage) error {
	existing, err := r.dal.GetJob(ctx, want.Namespace, want.Name)
	if err != nil {
		return fmt.Errorf("error getting existing job %s: %w", want.Name, err)
	}
	if existing == nil {
		return nil // vanished between create and get; the next pass recreates
	}

	if !jobFinished(existing) {
		if jobMatchesPackage(r.opts, _package, *existing, skyhookNode.GetSkyhook(), stage) {
			return nil // another pass won the race with a matching Job
		}
		return deleteJobForeground(ctx, r.Client, existing, "unfinished job no longer matches the package spec")
	}

	// Any finished Job JobReconcile has not processed yet is left alone, the same rule
	// shouldDeleteFinishedJob applies: a Complete one holds an unrecorded completion, and a
	// Failed one has not yet had its chance to write erroring. Deleting the Failed case here
	// would take its retained attempts with it and restart the stage on a fresh budget, and
	// this path is reachable in exactly that window — a finished Job does not satisfy
	// JobExists, so the next pass tries to create over its deterministic name.
	if !jobProcessed(existing) {
		return nil
	}

	// Timed out and still the current spec: the finished Job is doing its job, absorb this
	// recreate attempt. The spec condition is what lets an edit clear a timed-out stage — mirrors
	// shouldDeleteFinishedJob, which the two must agree with for the same Job.
	if jobFailedTerminally(existing) && r.entryErroringAtStage(skyhookNode, _package, stage) &&
		jobMatchesPackage(r.opts, _package, *existing, skyhookNode.GetSkyhook(), stage) {
		return nil
	}
	return deleteJobForeground(ctx, r.Client, existing, "finished job superseded by a recreate attempt")
}

// entryErroringAtStage reports whether this package's node-state entry sits at (stage, erroring),
// the timed-out state a terminally Failed Job represents.
func (r *SkyhookReconciler) entryErroringAtStage(skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package, stage v1alpha1.Stage) bool {
	status, found := skyhookNode.PackageStatus(_package.GetUniqueName())
	return found && status.Stage == stage && status.State == v1alpha1.StateErroring
}

// deleteConfigUpdateExecutors clears a package's in-flight work on a node so a config change
// re-runs it: its unfinished or timed-out (terminally Failed) Jobs. Retained successful Jobs for
// other stages are left in place — a literal port of the old delete-all-matching loop would gut
// retention on every config update.
func (r *SkyhookReconciler) deleteConfigUpdateExecutors(ctx context.Context, node wrapper.SkyhookNode, skyhookName string, _package *v1alpha1.Package) error {
	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):    skyhookName,
			fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX): fmt.Sprintf("%s-%s", _package.Name, _package.Version),
			fmt.Sprintf("%s/node", v1alpha1.METADATA_PREFIX):    nodeLabelValue(node.GetNode().Name),
		},
	)
	if err != nil {
		return fmt.Errorf("listing package jobs on node %s for package %s: %w", node.GetNode().Name, _package.GetUniqueName(), err)
	}
	if jobs != nil {
		for i := range jobs.Items {
			job := &jobs.Items[i]
			if !jobFinished(job) || jobFailedTerminally(job) {
				if err := deleteJobForeground(ctx, r.Client, job, "clearing an unfinished or terminally failed stage"); err != nil {
					return fmt.Errorf("deleting erroring job %s on node %s: %w", job.Name, node.GetNode().Name, err)
				}
			}
		}
	}

	return nil
}

// deleteNodeJobs foreground-deletes every one of a Skyhook's Jobs on a node, regardless of status.
// Used by the reboot-reset path, where a retained or unprocessed-Complete Job from a previous boot
// must not survive to land its completion on freshly-reset node state.
func (r *SkyhookReconciler) deleteNodeJobs(ctx context.Context, skyhookName, nodeName string) error {
	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhookName,
			fmt.Sprintf("%s/node", v1alpha1.METADATA_PREFIX): nodeLabelValue(nodeName),
		},
	)
	if err != nil {
		return fmt.Errorf("listing jobs on node %s: %w", nodeName, err)
	}
	if jobs == nil {
		return nil
	}
	for i := range jobs.Items {
		if err := deleteJobForeground(ctx, r.Client, &jobs.Items[i], "removing all jobs on the node"); err != nil {
			return fmt.Errorf("deleting job %s on node %s: %w", jobs.Items[i].Name, nodeName, err)
		}
	}
	return nil
}

func trunstr(str string, length int) string {
	if len(str) > length {
		return str[:length]
	}
	return str
}

func getAgentImage(opts SkyhookOperatorOptions, _package *v1alpha1.Package) string {
	if _package.AgentImageOverride != "" {
		return _package.AgentImageOverride
	}
	return opts.AgentImage
}

// getPackageImage returns the full image reference for a package, using the digest if specified
func getPackageImage(_package *v1alpha1.Package) string {
	if _package.ContainerSHA != "" {
		// When containerSHA is specified, use it instead of the version tag for immutable image reference
		return fmt.Sprintf("%s@%s", _package.Image, _package.ContainerSHA)
	}
	// Fall back to version tag
	return fmt.Sprintf("%s:%s", _package.Image, _package.Version)
}

func getAgentConfigEnvVars(opts SkyhookOperatorOptions, packageName string, packageVersion string, resourceID string, skyhookName string, nodeOrder int) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "SKYHOOK_LOG_DIR",
			Value: fmt.Sprintf("%s/%s", opts.AgentLogRoot, skyhookName),
		},
		{
			Name:  "SKYHOOK_ROOT_DIR",
			Value: fmt.Sprintf("%s/%s", opts.CopyDirRoot, skyhookName),
		},
		{
			Name:  "COPY_RESOLV",
			Value: annotationFalseValue,
		},
		{
			Name:  envSkyhookResourceID,
			Value: fmt.Sprintf("%s_%s_%s", resourceID, packageName, packageVersion),
		},
		{
			Name:  envSkyhookNodeOrder,
			Value: strconv.Itoa(nodeOrder),
		},
	}
}

// FilterEnv removes the environment variables passed into exlude
func FilterEnv(envs []corev1.EnvVar, exclude ...string) []corev1.EnvVar {
	var filteredEnv []corev1.EnvVar

	// build map of exclude strings for faster lookup
	excludeMap := make(map[string]struct{})
	for _, name := range exclude {
		excludeMap[name] = struct{}{}
	}

	// If the environment variable name is in the exclude list, skip it
	// otherwise append it to the final list
	for _, env := range envs {
		if _, found := excludeMap[env.Name]; !found {
			filteredEnv = append(filteredEnv, env)
		}
	}

	return filteredEnv
}

// ValidateRunningPackages reconciles a Skyhook's package executor Jobs against spec and node
// state: it sweeps Jobs whose node is gone, deletes processed finished Jobs once their stage
// should re-run (leaving a timed-out Job and any unprocessed finished Job in place), and
// invalidates unfinished Jobs whose spec or stage no longer matches. Jobs only, deliberately:
// this change ships with the nodewright rename, so legacyMigrationHold stops a NodeWright
// reconcile while any pre-rename Skyhook is still rolling out, and the two execution models never
// run side by side. See the design doc's Upgrade section.
func (r *SkyhookReconciler) ValidateRunningPackages(ctx context.Context, skyhook SkyhookNodes) (bool, error) {

	update := false
	errs := make([]error, 0)

	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name,
		},
	)
	if err != nil {
		return false, fmt.Errorf("error getting jobs while validating packages: %w", err)
	}

	nodesByName := make(map[string]wrapper.SkyhookNode)
	for _, node := range skyhook.GetNodes() {
		nodesByName[node.GetNode().Name] = node
	}

	if jobs != nil {
		for i := range jobs.Items {
			job := &jobs.Items[i]

			pkg, err := GetPackage(job)
			if err != nil {
				errs = append(errs, fmt.Errorf("error getting package from job %s: %w", job.Name, err))
				continue
			}
			if pkg == nil {
				continue
			}

			// Orphaned-node sweep: the node this Job pins to no longer exists. Delete regardless of
			// status — an unfinished one churns PodGC<->replacement forever against a missing node,
			// and a finished one has no node state left to claim it.
			node, nodeExists := nodesByName[jobNodeName(job)]
			if !nodeExists {
				if err := deleteJobForeground(ctx, r.Client, job, "node no longer exists"); err != nil {
					errs = append(errs, fmt.Errorf("error deleting orphaned-node job %s: %w", job.Name, err))
				} else {
					update = true
				}
				continue
			}

			nodeState, err := node.State()
			if err != nil {
				errs = append(errs, fmt.Errorf("node %s: reading state while validating jobs: %w", node.GetNode().Name, err))
				continue
			}

			if jobFinished(job) {
				if r.shouldDeleteFinishedJob(job, pkg, nodeState, skyhook) {
					if err := deleteJobForeground(ctx, r.Client, job, "finished job no longer recorded as done in node state"); err != nil {
						errs = append(errs, fmt.Errorf("error deleting finished job %s: %w", job.Name, err))
					} else {
						update = true
					}
				}
				continue
			}

			// Unfinished Job whose spec or stage no longer matches → invalidate; JobReconcile reaps it.
			if r.jobIsStale(job, pkg, nodeState, skyhook) {
				update = true
				if err := r.InvalidPackage(ctx, job); err != nil {
					errs = append(errs, fmt.Errorf("error invalidating job %s: %w", job.Name, err))
				}
			}
		}
	}

	return update, utilerrors.NewAggregate(errs)
}

// shouldDeleteFinishedJob is the rerun predicate: a state-recorded finished Job is deleted once
// node state no longer records its stage as done, so a reset or config change re-runs the stage.
// Two carve-outs: a finished Job JobReconcile has not processed yet is left alone, and a
// timed-out Job stays while its entry sits (stage, erroring) — that pair is what keeps the stage
// from churning. A spec change overrides the second carve-out.
func (r *SkyhookReconciler) shouldDeleteFinishedJob(job *batchv1.Job, pkg *PackageSkyhook, nodeState v1alpha1.NodeState, skyhook SkyhookNodes) bool {
	// Both outcomes wait for the marker. A Complete Job holds an unrecorded completion; a Failed
	// one has not yet had its chance to write erroring, and a finite backoffLimit can take a Job
	// from first failure to terminal in about a minute — well inside one pass of this sweep — so
	// deleting first would race the timeout into a fresh, equally doomed attempt.
	if !jobProcessed(job) {
		return false
	}

	status, found := nodeState[pkg.GetUniqueName()]

	// The timed-out carve-out holds only while the Job still reflects the current spec. Spec-drift
	// invalidation covers unfinished Jobs only, so without that last condition a stage that has
	// spent its retries sits behind its terminal Job until the failure TTL — editing the package to
	// fix exactly what broke it would do nothing, which is the opposite of what an edit means.
	// Deleting puts the stage where any other erroring package is: entry at (stage, erroring), no
	// executor, so the next pass builds a fresh Job from the new spec.
	if jobFailedTerminally(job) && found && status.Stage == pkg.Stage && status.State == v1alpha1.StateErroring &&
		r.jobSpecMatchesPackage(job, pkg, skyhook) {
		return false
	}

	// "recorded done" = the entry still reflects this stage having run: present, and not sitting
	// at this stage awaiting a fresh (in_progress/erroring) attempt. Absent, or reset to this stage
	// not-complete, means the stage should re-run — so the finished Job is cleared.
	recordedDone := found && (status.Stage != pkg.Stage || status.State == v1alpha1.StateComplete)
	return !recordedDone
}

// jobSpecMatchesPackage reports whether a Job still corresponds to a package in the current spec,
// built from the same config. Shared by the unfinished-Job staleness check and the finished-Job
// rerun predicate so a spec change means the same thing to both.
func (r *SkyhookReconciler) jobSpecMatchesPackage(job *batchv1.Job, pkg *PackageSkyhook, skyhook SkyhookNodes) bool {
	for _, v := range skyhook.GetSkyhook().Spec.Packages {
		if jobMatchesPackage(r.opts, &v, *job, skyhook.GetSkyhook(), pkg.Stage) {
			return true
		}
	}

	// Uninstall legacy special-case: a downgrade/removed-from-spec uninstall can't be validated
	// against a spec that no longer has it, so treat it as matched; an explicit uninstall (still in
	// spec, same version) is left to jobMatchesPackage.
	if pkg.Stage == v1alpha1.StageUninstall {
		specPkg, inSpec := skyhook.GetSkyhook().Spec.Packages[pkg.Name]
		if !inSpec || specPkg.Version != pkg.Version {
			return true
		}
	}

	return false
}

// jobIsStale reports whether an unfinished Job no longer matches the current spec or node state:
// its package left the spec (or its spec changed), or node state doesn't record it at this stage.
func (r *SkyhookReconciler) jobIsStale(job *batchv1.Job, pkg *PackageSkyhook, nodeState v1alpha1.NodeState, skyhook SkyhookNodes) bool {
	if !r.jobSpecMatchesPackage(job, pkg, skyhook) {
		return true
	}

	status, exists := nodeState[pkg.GetUniqueName()]
	if !exists {
		return true
	}
	return status.Stage != pkg.Stage
}

// InvalidPackage marks a package executor invalid and persists it, which triggers JobReconcile to
// delete the Job foreground. Takes client.Object rather than *batchv1.Job because the callers hold
// the executor as an Object and the mark itself is kind-agnostic.
func (r *SkyhookReconciler) InvalidPackage(ctx context.Context, obj client.Object) error {
	// Patch, not Update: the mark is a metadata-only change, but the executor is a cached
	// object whose spec.suspend the pause path writes concurrently. Sending the whole object
	// back would revert a suspend that landed after this copy was read.
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	if err := InvalidatePackage(obj); err != nil {
		return fmt.Errorf("error invalidating package: %w", err)
	}

	if err := r.Patch(ctx, obj, patch); err != nil {
		return fmt.Errorf("error patching executor %s: %w", obj.GetName(), err)
	}

	return nil
}

// ProcessInterrupt will check and do the interrupt if need, and returns
// false means we are waiting
// true means we are good to proceed
func (r *SkyhookReconciler) ProcessInterrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package, interrupt *v1alpha1.Interrupt, runInterrupt bool) (bool, error) {

	if !skyhookNode.HasInterrupt(*_package) {
		return true, nil
	}

	// default starting stage
	stage := v1alpha1.StageApply
	nextStage := skyhookNode.NextStage(_package)
	if nextStage != nil {
		stage = *nextStage
	}

	// wait tell this is done if its happening
	status, found := skyhookNode.PackageStatus(_package.GetUniqueName())
	if found && status.IsSkipped() {
		// Level-triggered backstop. This package was skipped because a higher-priority
		// interrupt won the node's single interrupt slot (e.g. a reboot that also satisfies
		// it). The pod controller promotes skipped packages when that interrupt pod completes,
		// but a skipped package has no pod of its own, so if that edge is missed (the pod is
		// already GC'd, or the skip was written after it completed) nothing else ever promotes
		// it and the node never finishes. runInterrupt is true only once this is the highest-
		// priority interrupt left to run, meaning the preempting interrupt has completed and
		// left the runnable set: promote then, so the reconcile converges on its own rather
		// than depending on a pod event that may never arrive. While a higher-priority
		// interrupt is still pending (runInterrupt false) the package keeps waiting.
		if runInterrupt {
			if err := skyhookNode.ProgressSkipped(); err != nil {
				return false, fmt.Errorf("error progressing skipped package [%s]: %w", _package.GetUniqueName(), err)
			}
		}
		return false, nil
	}

	// Theres is a race condition when a node reboots and api cleans up the interrupt pod
	// so we need to check if the pod exists and if it does, we need to recreate it
	if status != nil && status.IsActive() && status.IsInterruptStage() {
		// call interrupt to recreate the pod if missing
		err := r.Interrupt(ctx, skyhookNode, _package, interrupt, status.Stage)
		if err != nil {
			return false, err
		}
	}

	// drain and cordon node before applying package that has an interrupt
	if stage == v1alpha1.StageApply || stage == v1alpha1.StageUninstall {
		ready, err := r.EnsureNodeIsReadyForInterrupt(ctx, skyhookNode, _package)
		if err != nil {
			return false, err
		}

		if !ready {
			return false, nil
		}
	}

	// time to interrupt (once other packages have finished)
	if stage == v1alpha1.StageInterrupt && runInterrupt {
		err := r.Interrupt(ctx, skyhookNode, _package, interrupt, v1alpha1.StageInterrupt)
		if err != nil {
			return false, err
		}

		return false, nil
	}

	//skipping
	if stage == v1alpha1.StageInterrupt && !runInterrupt {
		err := skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateSkipped, stage, 0, _package.ContainerSHA)
		if err != nil {
			return false, fmt.Errorf("error upserting to skip interrupt: %w", err)
		}
		return false, nil
	}

	// Uninstall-cycle interrupt: HandleCompletePod set StageUninstallInterrupt/InProgress;
	// fire the interrupt pod (idempotent — r.Interrupt bails if pod exists).
	// Always runs — once uninstall has started, the interrupt must run to completion.
	if status != nil && status.Stage == v1alpha1.StageUninstallInterrupt && status.State != v1alpha1.StateComplete {
		err := r.Interrupt(ctx, skyhookNode, _package, interrupt, v1alpha1.StageUninstallInterrupt)
		if err != nil {
			return false, err
		}
		return false, nil
	}

	// wait tell this is done if its happening
	if status != nil && status.IsInterruptStage() && status.State != v1alpha1.StateComplete {
		return false, nil
	}

	return true, nil
}

func (r *SkyhookReconciler) EnsureNodeIsReadyForInterrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) (bool, error) {
	// Cordon is an in-memory mutation; SaveNodesAndSkyhook patches it at the end of this
	// pass, after every selected node has been visited. Draining in the same pass that
	// first cordons the node would evict while spec.unschedulable is still only local, so
	// the scheduler could put the replacement pod straight back on a node we are about to
	// interrupt. Defer the drain one reconcile, until the cordon is durable.
	//
	// This costs one pass per drain cycle, not one per node: the caller's loop keeps going
	// after a false return, so a single pass still cordons every node it selected.
	if skyhookNode.Cordon() {
		return false, nil
	}

	hasWork, err := r.HasNonInterruptWork(ctx, skyhookNode)
	if err != nil {
		return false, err
	}
	if hasWork { // keep waiting...
		return false, nil
	}

	ready, err := r.DrainNode(ctx, skyhookNode, _package)
	if err != nil {
		return false, fmt.Errorf("error draining node [%s]: %w", skyhookNode.GetNode().Name, err)
	}

	return ready, nil
}

// ApplyPackage starts a pod on node for the package
func (r *SkyhookReconciler) ApplyPackage(ctx context.Context, logger logr.Logger, clusterState *clusterState, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package, runInterrupt bool) error {

	if _package == nil {
		return errors.New("can not apply nil package")
	}

	// default starting stage
	stage := v1alpha1.StageApply

	// These modes don't have anything that comes before them so we must specify them as the
	// starting point. The next stage function will return nil until these modes complete.
	// Config is a special case as sometimes apply will come before it and other times it wont
	// which is why it needs to be here as well
	if packageStatus, found := skyhookNode.PackageStatus(_package.GetUniqueName()); found {
		switch packageStatus.Stage {
		case v1alpha1.StageConfig, v1alpha1.StageUpgrade, v1alpha1.StageUninstall,
			v1alpha1.StageUninstallInterrupt:
			stage = packageStatus.Stage
		}
	}

	// if stage != v1alpha1.StageApply {
	// 	// If a node gets rest by a user, the about method will return the wrong node state. Above sources it from the skyhook status.
	// 	// check if the node has nothing, reset it then apply the package.
	// 	nodeState, err := skyhookNode.State()
	// 	if err != nil {
	// 		return fmt.Errorf("error getting node state: %w", err)
	// 	}

	// 	_, found := nodeState[_package.GetUniqueName()]
	// 	if !found {
	// 		stage = v1alpha1.StageApply
	// 	}
	// }

	nextStage := skyhookNode.NextStage(_package)
	if nextStage != nil {
		stage = *nextStage
	}

	// Uninstall-cycle interrupt pods are controller-created by ProcessInterrupt via
	// r.Interrupt (type-based pod, not a stage-script pod). ApplyPackage has no
	// work to do here.
	if stage == v1alpha1.StageUninstallInterrupt {
		return nil
	}

	// test if an executor already exists for this stage, if so, bailout
	exists, err := r.JobExists(ctx, skyhookNode.GetNode().Name, skyhookNode.GetSkyhook().Name, _package)
	if err != nil {
		return err
	}

	// wait tell this is done if its happening
	status, found := skyhookNode.PackageStatus(_package.GetUniqueName())

	if found && status.IsSkipped() { // skipped, so nothing to do
		return nil
	}

	if found && status.State == v1alpha1.StateInProgress { // running, so do nothing atm
		if exists {
			return nil
		}
	}

	if exists {
		// nothing to do here, already running
		return nil
	}

	// Ensure the node metadata configmap exists before creating the pod
	// This prevents a race where the pod starts before its required configmap is created
	if err := r.UpsertNodeLabelsAnnotationsPackages(ctx, skyhookNode.GetSkyhook(), skyhookNode.GetNode()); err != nil {
		return fmt.Errorf("error upserting node metadata configmap: %w", err)
	}

	job := createJobFromPackage(r.opts, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name, stage)

	if err := setJobPackage(job, skyhookNode.GetSkyhook().NodeWright, _package.Image, stage, _package); err != nil {
		return fmt.Errorf("error setting package on job: %w", err)
	}

	// setup ownership of the job we created so it and its child pods GC with the Skyhook CR
	if err := ctrl.SetControllerReference(skyhookNode.GetSkyhook().NodeWright, job, r.scheme); err != nil {
		return fmt.Errorf("error setting ownership: %w", err)
	}

	if err := r.Create(ctx, job); err != nil {
		// Deterministic names make Create idempotent, but AlreadyExists is not blindly benign:
		// a retained, timed-out, or mismatched Job with our name needs GET-and-decide, and must not
		// record an in_progress that isn't happening.
		if apierrors.IsAlreadyExists(err) {
			return r.handleExistingJob(ctx, job, _package, skyhookNode, stage)
		}
		return fmt.Errorf("error creating job: %w", err)
	}

	if err = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, stage, 0, _package.ContainerSHA); err != nil {
		err = fmt.Errorf("error upserting package: %w", err) // want to keep going in this case, but don't want to lose the err
	}

	skyhookNode.SetStatus(v1alpha1.StatusInProgress)

	wrapper.AddSkyhookConditionWithLegacy(skyhookNode.GetSkyhook(), metav1.Condition{
		Type:               wrapper.SkyhookConditionApplyPackage,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: skyhookNode.GetSkyhook().Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "ApplyPackage",
		Message:            fmt.Sprintf("Applying package [%s:%s] to node [%s]", _package.Name, _package.Version, skyhookNode.GetNode().Name),
	})

	r.recorder.Eventf(skyhookNode.GetNode(), nil, EventTypeNormal, EventsReasonSkyhookApply, "ApplyPackage", "Applying package [%s:%s] from [nodewright:%s] stage [%s]", _package.Name, _package.Version, skyhookNode.GetSkyhook().Name, stage)
	r.recorder.Eventf(skyhookNode.GetSkyhook(), nil, EventTypeNormal, EventsReasonSkyhookApply, "ApplyPackage", "Applying package [%s:%s] to node [%s] stage [%s]", _package.Name, _package.Version, skyhookNode.GetNode().Name, stage)

	skyhookNode.GetSkyhook().Updated = true

	return err
}

// HandleRuntimeRequired finds any nodes for which all runtime required Skyhooks are complete and remove their runtime required taint
// Will return an error if the patching of the nodes is not possible
func (r *SkyhookReconciler) HandleRuntimeRequired(ctx context.Context, clusterState *clusterState, nodes *corev1.NodeList) error {
	node_to_skyhooks, skyhook_node_map := groupSkyhooksByNode(clusterState)
	to_remove := getRuntimeRequiredTaintCompleteNodes(node_to_skyhooks, skyhook_node_map)
	// Remove every recognised runtime-required taint, not just the configured one: a node
	// provisioned by infrastructure still stamping the legacy skyhook.nvidia.com key would
	// otherwise stay unschedulable forever, since nothing else removes it.
	taints_to_remove := r.opts.GetRuntimeRequiredTaints()
	errs := make([]error, 0)
	for _, node := range to_remove {
		cordonAfter := runtimeRequiredCordonAfterEnabled(node_to_skyhooks[node.UID])
		if err := r.removeRuntimeRequiredTaints(ctx, node.Name, taints_to_remove, cordonAfter); err != nil {
			errs = append(errs, fmt.Errorf("removing runtime-required taints from node %s: %w", node.Name, err))
		}
	}

	// Remove any stale runtimeRequiredCordon annotation from nodes which were originally cordoned via a
	// runtimeRequiredCordonAfter NodeWright but were externally uncordoned without removing the corresponding
	// annotation.
	for i := range nodes.Items {
		node := &nodes.Items[i]
		_, annotated := node.Annotations[v1alpha1.RuntimeRequiredCordonAnnotation]
		if !node.Spec.Unschedulable && annotated {
			new_node := node.DeepCopy()
			delete(new_node.Annotations, v1alpha1.RuntimeRequiredCordonAnnotation)
			if err := r.Patch(ctx, new_node, client.MergeFromWithOptions(node, client.MergeFromWithOptimisticLock{})); err != nil {
				errs = append(errs, fmt.Errorf("removing runtime-required cordon annotation from node %s: %w", node.Name, err))
			}
		}
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
}

// removeRuntimeRequiredTaints re-reads and recomputes the mutation inside each conflict
// retry. The eligibility decision comes from a whole-cluster snapshot whose Node copy may
// already be stale, so patching that copy under an optimistic lock would repeatedly conflict
// instead of preserving concurrent taints and converging during this reconcile pass.
func (r *SkyhookReconciler) removeRuntimeRequiredTaints(ctx context.Context, nodeName string, taintsToRemove []corev1.Taint, cordonAfter bool) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := readNodeForPatch(ctx, r.dal, r.uncached, nodeName, attempt)
		attempt++
		if err != nil {
			return fmt.Errorf("re-reading node before patching: %w", err)
		}
		if node == nil {
			return nil
		}

		patch := client.MergeFromWithOptions(node.DeepCopy(), client.MergeFromWithOptimisticLock{})
		current, changed := node, false
		for i := range taintsToRemove {
			// RemoveTaint always returns a nil error.
			next, updated, _ := taints.RemoveTaint(current, &taintsToRemove[i])
			if updated {
				current, changed = next, true
			}
		}
		if !changed {
			return nil
		}

		// Apply the persistent cordon in the same write that removes the taint so the
		// node never loses its scheduling gate between the two operations.
		if cordonAfter {
			if current.Annotations == nil {
				current.Annotations = make(map[string]string)
			}
			current.Annotations[v1alpha1.RuntimeRequiredCordonAnnotation] = annotationTrueValue
			current.Spec.Unschedulable = true
		}

		if err := r.Patch(ctx, current, patch); err != nil {
			return fmt.Errorf("patching node: %w", err)
		}
		return nil
	})
}

func runtimeRequiredCordonAfterEnabled(skyhooks []SkyhookNodes) bool {
	for _, skyhook := range skyhooks {
		spec := skyhook.GetSkyhook().Spec
		if spec.RuntimeRequired && spec.RuntimeRequiredCordonAfter {
			return true
		}
	}
	return false
}

// Group Skyhooks by what node they target
func groupSkyhooksByNode(clusterState *clusterState) (map[types.UID][]SkyhookNodes, map[types.UID]*corev1.Node) {
	node_to_skyhooks := make(map[types.UID][]SkyhookNodes)
	nodes := make(map[types.UID]*corev1.Node)
	for _, skyhook := range clusterState.skyhooks {
		// Ignore skyhooks that don't have runtime required
		if !skyhook.GetSkyhook().Spec.RuntimeRequired {
			continue
		}
		for _, node := range skyhook.GetNodes() {
			if _, ok := node_to_skyhooks[node.GetNode().UID]; !ok {
				node_to_skyhooks[node.GetNode().UID] = make([]SkyhookNodes, 0)
				nodes[node.GetNode().UID] = node.GetNode()
			}
			node_to_skyhooks[node.GetNode().UID] = append(node_to_skyhooks[node.GetNode().UID], skyhook)
		}

	}
	return node_to_skyhooks, nodes
}

// Get the nodes to remove runtime required taint from node that all skyhooks targeting that node have completed
// Note: This checks per-node completion, not skyhook-level completion. A node's taint is removed when all
// runtime-required skyhooks are complete ON THAT SPECIFIC NODE, regardless of other nodes' completion status.
func getRuntimeRequiredTaintCompleteNodes(node_to_skyhooks map[types.UID][]SkyhookNodes, nodes map[types.UID]*corev1.Node) []*corev1.Node {
	to_remove := make([]*corev1.Node, 0)
	for node_uid, skyhooks := range node_to_skyhooks {
		node := nodes[node_uid]
		all_complete := true
		for _, skyhook := range skyhooks {
			// Check if THIS specific node is complete for this skyhook (not all nodes)
			_, nodeWrapper := skyhook.GetNode(node.Name)
			if nodeWrapper == nil || !nodeWrapper.IsComplete() {
				all_complete = false
				break
			}
		}
		if all_complete {
			to_remove = append(to_remove, node)
		}
	}
	return to_remove
}

// HandleAutoTaint applies the runtime-required taint to new nodes matching runtime-required
// Skyhooks that have AutoTaintNewNodes enabled. Only the configured taint is ever applied;
// the legacy key is recognised on the way in and removed on completion, never stamped.
func (r *SkyhookReconciler) HandleAutoTaint(ctx context.Context, clusterState *clusterState) (bool, error) {
	taint_to_add := r.opts.GetRuntimeRequiredTaint()
	to_taint := clusterState.getAutoTaintNodes(r.opts.GetRuntimeRequiredTaints())
	errs := make([]error, 0)
	changed := false
	for _, node := range to_taint {
		newNode, updated, _ := taints.AddOrUpdateTaint(node, &taint_to_add)
		if !updated {
			continue
		}
		// add annotation to indicate that the node was auto-tainted
		if newNode.Annotations == nil {
			newNode.Annotations = make(map[string]string)
		}
		newNode.Annotations[fmt.Sprintf("%s/autoTaint_%s", v1alpha1.METADATA_PREFIX, taint_to_add.Key)] = annotationTrueValue

		if err := r.Patch(ctx, newNode, client.MergeFrom(node)); err != nil {
			errs = append(errs, err)
		}
		changed = true
	}
	if len(errs) > 0 {
		return changed, utilerrors.NewAggregate(errs)
	}
	return changed, nil
}

// setPodResources sets resources for all containers and init containers in the pod if override is set, else leaves empty for LimitRange
func setPodResources(pod *corev1.Pod, res *v1alpha1.ResourceRequirements) {
	if res == nil {
		return
	}
	if !res.CPURequest.IsZero() || !res.CPULimit.IsZero() || !res.MemoryRequest.IsZero() || !res.MemoryLimit.IsZero() {
		for i := range pod.Spec.InitContainers {
			pod.Spec.InitContainers[i].Resources = corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    res.CPULimit,
					corev1.ResourceMemory: res.MemoryLimit,
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    res.CPURequest,
					corev1.ResourceMemory: res.MemoryRequest,
				},
			}
		}
	}
}

// PartitionNodesIntoCompartments partitions nodes for each skyhook that uses deployment policies.
func partitionNodesIntoCompartments(clusterState *clusterState) error {
	for _, skyhook := range clusterState.skyhooks {
		// Skip skyhooks without a deployment policy (they use the default compartment created in BuildState)
		if skyhook.GetSkyhook().Spec.DeploymentPolicy == "" {
			continue
		}

		// Skip if no compartments exist (e.g., deployment policy not found)
		// The webhook should prevent this at admission time, and the controller sets a condition at runtime,
		// but we guard here to prevent panics if the policy goes missing
		if len(skyhook.GetCompartments()) == 0 {
			continue
		}

		// Clear all compartments before reassigning nodes to prevent stale nodes
		// This ensures nodes are only in their current compartment based on current labels
		for _, compartment := range skyhook.GetCompartments() {
			compartment.ClearNodes()
		}

		for _, node := range skyhook.GetNodes() {
			compartmentName, err := skyhook.AssignNodeToCompartment(node)
			if err != nil {
				return fmt.Errorf("error assigning node %s: %w", node.GetNode().Name, err)
			}
			if err := skyhook.AddCompartmentNode(compartmentName, node); err != nil {
				return fmt.Errorf("error adding node %s to compartment %s: %w", node.GetNode().Name, compartmentName, err)
			}
		}
	}

	return nil
}

// validateAndUpsertSkyhookData performs validation and configmap operations for a skyhook.
//
// The second return value (pendingSync) reports that an owned ConfigMap diverged from
// spec but the write was deferred because the completedNodes gate was closed. It is
// deliberately decoupled from the first (shouldReturn): a deferred sync must NOT
// short-circuit the reconcile, because progression toward the gate opening happens in
// processSkyhooksPerNode. The caller uses pendingSync only to shorten the otherwise
// MaxInterval idle requeue so the deferred write is retried promptly (issue #245).
func (r *SkyhookReconciler) validateAndUpsertSkyhookData(ctx context.Context, skyhook SkyhookNodes, clusterState *clusterState) (bool, bool, ctrl.Result, error) {
	if yes, result, err := shouldReturn(r.ValidateRunningPackages(ctx, skyhook)); yes {
		return yes, false, result, err
	}

	if yes, result, err := shouldReturn(r.ValidateNodeConfigmaps(ctx, skyhook.GetSkyhook().Name, skyhook.GetNodes())); yes {
		return yes, false, result, err
	}

	updated, pendingSync, err := r.UpsertConfigmaps(ctx, skyhook, clusterState)
	if err != nil {
		return true, false, ctrl.Result{}, err
	}
	if updated {
		return true, false, ctrl.Result{RequeueAfter: time.Second * 2}, nil
	}

	return false, pendingSync, ctrl.Result{}, nil
}
