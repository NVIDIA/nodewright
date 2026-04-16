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

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/util/taints"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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

	SkyhookFinalizer = "skyhook.nvidia.com/skyhook"
)

type SkyhookOperatorOptions struct {
	Namespace            string        `env:"NAMESPACE, default=skyhook"`
	MaxInterval          time.Duration `env:"DEFAULT_INTERVAL, default=10m"`
	ImagePullSecret      string        `env:"IMAGE_PULL_SECRET"`
	CopyDirRoot          string        `env:"COPY_DIR_ROOT, default=/var/lib/skyhook"`
	ReapplyOnReboot      bool          `env:"REAPPLY_ON_REBOOT, default=false"`
	RuntimeRequiredTaint string        `env:"RUNTIME_REQUIRED_TAINT, default=skyhook.nvidia.com=runtime-required:NoSchedule"`
	PauseImage           string        `env:"PAUSE_IMAGE, default=registry.k8s.io/pause:3.10"`
	AgentImage           string        `env:"AGENT_IMAGE, default=ghcr.io/nvidia/skyhook/agent:latest"` // TODO: this needs to be updated with a working default
	AgentLogRoot         string        `env:"AGENT_LOG_ROOT, default=/var/log/skyhook"`
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

func (o *SkyhookOperatorOptions) GetRuntimeRequiredTaint() corev1.Taint {
	to_add, _, _ := taints.ParseTaints([]string{o.RuntimeRequiredTaint})
	return to_add[0]
}

func (o *SkyhookOperatorOptions) GetRuntimeRequiredToleration() corev1.Toleration {
	taint := o.GetRuntimeRequiredTaint()
	return corev1.Toleration{
		Key:      taint.Key,
		Operator: corev1.TolerationOpEqual,
		Value:    taint.Value,
		Effect:   taint.Effect,
	}
}

// force type checking against this interface
var _ reconcile.Reconciler = &SkyhookReconciler{}

func NewSkyhookReconciler(schema *runtime.Scheme, c client.Client, recorder record.EventRecorder, opts SkyhookOperatorOptions) (*SkyhookReconciler, error) {

	err := opts.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid skyhook operator options: %w", err)
	}

	return &SkyhookReconciler{
		Client:   c,
		scheme:   schema,
		recorder: recorder,
		opts:     opts,
		dal:      dal.New(c),
	}, nil
}

// SkyhookReconciler reconciles a Skyhook object
type SkyhookReconciler struct {
	client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
	opts     SkyhookOperatorOptions
	dal      dal.DAL
}

// SetupWithManager sets up the controller with the Manager.
func (r *SkyhookReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// indexes allow for query on fields to use the local cache
	indexer := mgr.GetFieldIndexer()
	err := indexer.
		IndexField(context.TODO(), &corev1.Pod{}, "spec.nodeName", func(o client.Object) []string {
			pod, ok := o.(*corev1.Pod)
			if !ok {
				return nil
			}
			return []string{pod.Spec.NodeName}
		})

	if err != nil {
		return err
	}

	ehandler := &eventHandler{
		logger: mgr.GetLogger(),
		dal:    dal.New(r.Client),
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Skyhook{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(podHandlerFunc),
		).
		Watches(
			&corev1.Node{},
			ehandler,
		).
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
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *SkyhookReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	// split off requests for pods
	if strings.HasPrefix(req.Name, "pod---") {
		name := strings.Split(req.Name, "pod---")[1]
		pod, err := r.dal.GetPod(ctx, req.Namespace, name)
		if err == nil && pod != nil { // if pod, then call other wise not a pod
			return r.PodReconcile(ctx, pod)
		}
		return ctrl.Result{}, err
	}

	// get all skyhooks (SCR)
	skyhooks, err := r.dal.GetSkyhooks(ctx)
	if err != nil {
		// error, going to requeue and backoff
		logger.Error(err, "error getting skyhooks")
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
	nodePicker := NewNodePicker(logger, r.opts.GetRuntimeRequiredToleration())

	errs := make([]error, 0)
	var result *ctrl.Result

	for _, skyhook := range clusterState.skyhooks {
		if yes, result, err := shouldReturn(r.HandleFinalizer(ctx, skyhook)); yes {
			return result, err
		}

		if yes, result, err := shouldReturn(r.ReportState(ctx, clusterState, skyhook)); yes {
			return result, err
		}

		if skyhook.IsPaused() {
			if yes, result, err := shouldReturn(r.UpdatePauseStatus(ctx, clusterState, skyhook)); yes {
				return result, err
			}
			continue
		}

		if yes, result, err := r.validateAndUpsertSkyhookData(ctx, skyhook, clusterState); yes {
			return result, err
		}

		changed := IntrospectSkyhook(skyhook, clusterState.skyhooks)
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

	err = r.HandleRuntimeRequired(ctx, clusterState)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		err := utilerrors.NewAggregate(errs)
		return ctrl.Result{}, err
	}

	if result != nil {
		return *result, nil
	}

	// default happy retry after max
	return ctrl.Result{RequeueAfter: r.opts.MaxInterval}, nil
}

// processSkyhooksPerNode processes all skyhooks for nodes that are ready (per-node priority ordering).
// A node is ready for a skyhook if all higher-priority skyhooks are complete on that specific node.
func (r *SkyhookReconciler) processSkyhooksPerNode(ctx context.Context, clusterState *clusterState, nodePicker *NodePicker, logger logr.Logger) (*ctrl.Result, error) {
	var result *ctrl.Result
	var errs []error

	for _, skyhook := range clusterState.skyhooks {
		if skyhook.IsComplete() || skyhook.IsDisabled() || skyhook.IsPaused() {
			continue
		}

		// Check if any nodes are ready for this skyhook
		if !hasReadyNodesForSkyhook(skyhook, clusterState.skyhooks) {
			continue
		}

		res, err := r.RunSkyhookPackages(ctx, clusterState, nodePicker, skyhook)
		if err != nil {
			logger.Error(err, "error processing skyhook", "skyhook", skyhook.GetSkyhook().Name)
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
func hasReadyNodesForSkyhook(skyhook SkyhookNodes, allSkyhooks []SkyhookNodes) bool {
	for _, node := range skyhook.GetNodes() {
		if !node.IsComplete() && IsNodeReadyForSkyhook(node.GetNode().Name, skyhook, allSkyhooks) {
			return true
		}
	}
	return false
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
			return false, fmt.Errorf("error migrating skyhook [%s]: %w", skyhook.GetSkyhook().Name, err)
		}

		if err := skyhook.GetSkyhook().Skyhook.Validate(); err != nil {
			return false, fmt.Errorf("error validating skyhook [%s]: %w", skyhook.GetSkyhook().Name, err)
		}

		for _, node := range skyhook.GetNodes() {
			if node.Changed() {
				err := r.Status().Patch(ctx, node.GetNode(), client.MergeFrom(clusterState.tracker.GetOriginal(node.GetNode())))
				if err != nil {
					errors = append(errors, fmt.Errorf("error patching node [%s]: %w", node.GetNode().Name, err))
				}

				err = r.Patch(ctx, node.GetNode(), client.MergeFrom(clusterState.tracker.GetOriginal(node.GetNode())))
				if err != nil {
					errors = append(errors, fmt.Errorf("error patching node [%s]: %w", node.GetNode().Name, err))
				}
				updates = true
			}
		}

		if skyhook.GetSkyhook().Updated {
			// need to do this because SaveNodesAndSkyhook only saves skyhook status, not the main skyhook object where the annotations are
			// additionally it needs to be an update, a patch nils out the annotations for some reason, which the save function does a patch

			if err = r.Status().Update(ctx, skyhook.GetSkyhook().Skyhook); err != nil {
				return false, fmt.Errorf("error updating during migration skyhook status [%s]: %w", skyhook.GetSkyhook().Name, err)
			}

			// because of conflict issues (409) we need to do things a bit differently here.
			// We might be able to use server side apply in the future, but for now we need to do this
			// https://kubernetes.io/docs/reference/using-api/server-side-apply/
			// https://github.com/kubernetes-sigs/controller-runtime/issues/347

			// work around for now is to grab a new copy of the object, and then patch it

			newskyhook, err := r.dal.GetSkyhook(ctx, skyhook.GetSkyhook().Name)
			if err != nil {
				return false, fmt.Errorf("error getting skyhook to migrate [%s]: %w", skyhook.GetSkyhook().Name, err)
			}
			newPatch := client.MergeFrom(newskyhook.DeepCopy())

			// set version
			wrapper.NewSkyhookWrapper(newskyhook).SetVersion()

			if err = r.Patch(ctx, newskyhook, newPatch); err != nil {
				return false, fmt.Errorf("error updating during migration skyhook [%s]: %w", skyhook.GetSkyhook().Name, err)
			}

			updates = true
		}
	}

	if len(errors) > 0 {
		return false, utilerrors.NewAggregate(errors)
	}

	return updates, nil
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
	changed := UpdateSkyhookPauseStatus(skyhook)

	if changed {
		_, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
		if len(errs) > 0 {
			return false, utilerrors.NewAggregate(errs)
		}
		return true, nil
	}

	return false, nil
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
					r.recorder.Eventf(skyhook.GetSkyhook().Skyhook, EventTypeNormal, EventsReasonNodeReboot, "detected reboot, resetting node [%s] to be reapplied", node.GetNode().Name)
					r.recorder.Eventf(node.GetNode(), EventTypeNormal, EventsReasonNodeReboot, "detected reboot, resetting node for [%s] to be reapplied", node.GetSkyhook().Name)
					node.Reset()
				}
				skyhook.GetSkyhook().Status.NodeBootIds[node.GetNode().Name] = node.GetNode().Status.NodeInfo.BootID
				skyhook.GetSkyhook().Updated = true
			}

			if node.Changed() { // update
				updates = true
				err := r.Update(ctx, node.GetNode())
				if err != nil {
					errs = append(errs, fmt.Errorf("error updating node after reboot [%s]: %w", node.GetNode().Name, err))
				}
			}
		}
		if skyhook.GetSkyhook().Updated { // update
			updates = true
			err := r.Status().Update(ctx, skyhook.GetSkyhook().Skyhook)
			if err != nil {
				errs = append(errs, fmt.Errorf("error updating skyhook status after reboot [%s]: %w", skyhook.GetSkyhook().Name, err))
			}
		}
	}

	return updates, utilerrors.NewAggregate(errs)
}

// RunSkyhookPackages runs all skyhook packages then saves and requeues if changes were made
func (r *SkyhookReconciler) RunSkyhookPackages(ctx context.Context, clusterState *clusterState, nodePicker *NodePicker, skyhook SkyhookNodes) (*ctrl.Result, error) {

	logger := log.FromContext(ctx)
	requeue := false

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

	// Check for blocked dependencies: if a package is being uninstalled and another
	// package depends on it, set a Blocked condition. The DAG naturally prevents the
	// dependent from running (uninstalling packages are not in GetComplete).
	updateBlockedCondition(skyhook)

	changed := IntrospectSkyhook(skyhook, clusterState.skyhooks)
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

		// Filter out packages that have been uninstalled (node state says absent)
		// or are currently being uninstalled (node state says StageUninstall).
		// Uses node annotations as source of truth, not the spec's apply flag.
		nodeState, _ := node.State()
		filtered := make([]*v1alpha1.Package, 0, len(toRun))
		for _, pkg := range toRun {
			if nodeState.IsUninstalled(pkg.GetUniqueName()) && pkg.UninstallEnabled() {
				continue // uninstalled and was enabled — skip apply
			}
			if nodeState.IsUninstallInProgress(pkg.GetUniqueName()) {
				continue // uninstall running — skip apply
			}
			filtered = append(filtered, pkg)
		}
		toRun = filtered

		// prepend the uninstall packages so they are ran first
		toRun = append(toUninstall, toRun...)

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
				return &ctrl.Result{Requeue: true}, nil
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

// SaveNodesAndSkyhook saves nodes and skyhook and will update the events if the skyhook status changes
func (r *SkyhookReconciler) SaveNodesAndSkyhook(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes) (bool, []error) {
	saved := false
	errs := make([]error, 0)

	for _, node := range skyhook.GetNodes() {
		patch := client.StrategicMergeFrom(clusterState.tracker.GetOriginal(node.GetNode()))
		if node.Changed() {
			err := r.Patch(ctx, node.GetNode(), patch)
			if err != nil {
				errs = append(errs, fmt.Errorf("error patching node [%s]: %w", node.GetNode().Name, err))
			}
			saved = true

			err = r.UpsertNodeLabelsAnnotationsPackages(ctx, skyhook.GetSkyhook(), node.GetNode())
			if err != nil {
				errs = append(errs, fmt.Errorf("error upserting labels, annotations, and packages config map for node [%s]: %w", node.GetNode().Name, err))
			}

			if node.IsComplete() {
				r.recorder.Eventf(node.GetNode(), EventTypeNormal, EventsReasonSkyhookStateChange, "Skyhook [%s] complete.", skyhook.GetSkyhook().Name)

				// since node is complete remove from priority
				skyhook.GetSkyhook().RemoveNodePriority(node.GetNode().Name)
			}
		}

		// updates node's condition
		node.UpdateCondition()
		if node.Changed() {
			// conditions are in status
			err := r.Status().Patch(ctx, node.GetNode(), patch)
			if err != nil {
				errs = append(errs, fmt.Errorf("error patching node status [%s]: %w", node.GetNode().Name, err))
			}
			saved = true
		}

		if node.GetSkyhook() != nil && node.GetSkyhook().Updated {
			skyhook.GetSkyhook().Updated = true
		}
	}

	if skyhook.GetSkyhook().Updated {
		patch := client.MergeFrom(clusterState.tracker.GetOriginal(skyhook.GetSkyhook().Skyhook))
		err := r.Status().Patch(ctx, skyhook.GetSkyhook().Skyhook, patch)
		if err != nil {
			errs = append(errs, err)
		}
		saved = true

		if skyhook.GetPriorStatus() != "" && skyhook.GetPriorStatus() != skyhook.Status() {
			// we transitioned, fire event
			r.recorder.Eventf(skyhook.GetSkyhook(), EventTypeNormal, EventsReasonSkyhookStateChange, "Skyhook transitioned [%s] -> [%s]", skyhook.GetPriorStatus(), skyhook.Status())
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
			return nil, err
		}
		for name, pkg := range skyhook.GetSkyhook().Spec.Packages {
			needsUninstall := pkg.IsUninstalling() || (beingDeleted && pkg.UninstallEnabled())
			if !needsUninstall {
				continue
			}
			status, exists := nodeState[pkg.GetUniqueName()]
			if !exists {
				continue // already uninstalled on this node (absent = done)
			}
			if status.Stage == v1alpha1.StageUninstall {
				if status.State == v1alpha1.StateInProgress || status.State == v1alpha1.StateErroring {
					// already in uninstall pipeline, add to pod list
					p := skyhook.GetSkyhook().Spec.Packages[name]
					toUninstall = appendIfNotPresent(toUninstall, &p)
				}
				continue
			}
			// Only trigger uninstall from a completed state — don't uninstall
			// a package that hasn't finished installing yet
			if status.State != v1alpha1.StateComplete {
				continue
			}
			// Trigger uninstall: transition from install-complete to uninstall
			skyhookExplicitUninstallsTotal.WithLabelValues(
				skyhook.GetSkyhook().Name, pkg.Name, pkg.Version).Inc()
			err = node.Upsert(pkg.PackageRef, pkg.Image,
				v1alpha1.StateInProgress, v1alpha1.StageUninstall, 0, pkg.ContainerSHA)
			if err != nil {
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
func HandleCancelledUninstalls(skyhook SkyhookNodes) error {
	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return err
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
			// Package is at StageUninstall but apply is false → cancelled
			if status.State == v1alpha1.StateInProgress || status.State == v1alpha1.StateErroring {
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

// updateBlockedCondition sets or clears the Blocked condition based on whether
// any package's dependency is being uninstalled. The DAG naturally prevents the
// dependent from running; this condition informs the user.
// Uses node annotations (StageUninstall) as source of truth, not the spec's apply flag.
func updateBlockedCondition(skyhook SkyhookNodes) {
	var blockedMsgs []string

	// Build a set of packages that are being uninstalled on any node (source of truth)
	uninstallingOnAnyNode := make(map[string]bool)
	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			continue
		}
		for _, pkg := range skyhook.GetSkyhook().Spec.Packages {
			if nodeState.IsUninstallInProgress(pkg.GetUniqueName()) {
				uninstallingOnAnyNode[pkg.Name] = true
			}
		}
	}

	for bName, bPkg := range skyhook.GetSkyhook().Spec.Packages {
		for depName := range bPkg.DependsOn {
			if uninstallingOnAnyNode[depName] {
				blockedMsgs = append(blockedMsgs, fmt.Sprintf(
					"package %s is blocked: dependency %s is being uninstalled", bName, depName))
			}
		}
	}

	sort.Strings(blockedMsgs) // deterministic order to avoid unnecessary status writes

	condType := fmt.Sprintf("%s/Blocked", v1alpha1.METADATA_PREFIX)
	if len(blockedMsgs) > 0 {
		skyhook.GetSkyhook().AddCondition(metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: skyhook.GetSkyhook().Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "DependencyUninstalled",
			Message:            strings.Join(blockedMsgs, "; "),
		})
	} else {
		// Clear the condition if no packages are blocked
		skyhook.GetSkyhook().RemoveCondition(condType)
	}
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

// HandleVersionChange updates the state for the node or skyhook if a version is changed on a package
func HandleVersionChange(skyhook SkyhookNodes) ([]*v1alpha1.Package, error) {
	toUninstall := make([]*v1alpha1.Package, 0)
	versionChangeDetected := false

	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return nil, err
		}

		for _, packageStatus := range nodeState {
			upgrade := false

			_package, exists := skyhook.GetSkyhook().Spec.Packages[packageStatus.Name]

			// Skip packages where uninstall has started on this node — handled by HandleUninstallRequests.
			// Uses node annotation (StageUninstall) as source of truth, not the spec's apply flag.
			if nodeState.IsUninstallInProgress(_package.GetUniqueName()) {
				continue
			}

			if exists && _package.Version == packageStatus.Version {
				continue // no uninstall needed for package
			}

			packageStatusRef := v1alpha1.PackageRef{
				Name:    packageStatus.Name,
				Version: packageStatus.Version,
			}

			if !exists {
				if packageStatus.Stage == v1alpha1.StageUninstall {
					// Already in an uninstall pipeline from a prior version-change downgrade;
					// let it finish naturally via the synthetic-package path below
				} else {
					// Package removed from spec. The webhook blocks removal of enabled=true
					// packages unless uninstall is complete, so if we reach here either:
					// (a) enabled=false — just remove state, no uninstall pod needed
					// (b) enabled=true but uninstall already completed — clean up residual
					err := node.RemoveState(packageStatusRef)
					if err != nil {
						return nil, fmt.Errorf("error removing state for deleted package %s: %w", packageStatus.Name, err)
					}
					node.SetStatus(v1alpha1.StatusInProgress)
					skyhook.GetSkyhook().RemoveConfigUpdates(packageStatus.Name)
					versionChangeDetected = true
					continue
				}
			} else if exists && _package.Version != packageStatus.Version {
				versionChangeDetected = true
				comparison := version.Compare(_package.Version, packageStatus.Version)
				if comparison == -2 {
					return nil, errors.New("error comparing package versions: invalid version string provided enabling webhooks validates versions before being applied")
				}

				if comparison == 1 {
					_packageStatus, found := node.PackageStatus(_package.GetUniqueName())
					if found && _packageStatus.Stage == v1alpha1.StageUpgrade {
						continue
					}

					// start upgrade of package
					err := node.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, v1alpha1.StageUpgrade, 0, _package.ContainerSHA)
					if err != nil {
						return nil, fmt.Errorf("error updating node status: %w", err)
					}

					upgrade = true
				} else if comparison == -1 && packageStatus.Stage != v1alpha1.StageUninstall {
					// Start uninstall of old package
					err := node.Upsert(packageStatusRef, packageStatus.Image, v1alpha1.StateInProgress, v1alpha1.StageUninstall, 0, "")
					if err != nil {
						return nil, fmt.Errorf("error updating node status: %w", err)
					}

					// If version changed then update new version to wait
					err = node.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateSkipped, v1alpha1.StageUninstall, 0, _package.ContainerSHA)
					if err != nil {
						return nil, fmt.Errorf("error updating node status: %w", err)
					}
				}
			}

			// only need to create a feaux package for uninstall since it won't be in the DAG (Upgrade will)
			newPackageStatus, found := node.PackageStatus(packageStatusRef.GetUniqueName())
			if !upgrade && found && newPackageStatus.Stage == v1alpha1.StageUninstall && newPackageStatus.State == v1alpha1.StateInProgress {
				// create fake package with the info we can salvage from the node state
				newPackage := &v1alpha1.Package{
					PackageRef: packageStatusRef,
					Image:      packageStatus.Image,
				}

				// Add package to uninstall list if it's not already present
				found := false
				for _, uninstallPackage := range toUninstall {
					if reflect.DeepEqual(uninstallPackage, newPackage) {
						found = true
					}
				}

				if !found {
					toUninstall = append(toUninstall, newPackage)
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

	if err := ctrl.SetControllerReference(skyhook.Skyhook, newCM, r.scheme); err != nil {
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

// HandleConfigUpdates checks whether the configMap on a package was updated and if it was the configmap will
// be updated and the package will be put into config mode if the package is complete or erroring
func (r *SkyhookReconciler) HandleConfigUpdates(ctx context.Context, clusterState *clusterState, skyhook SkyhookNodes, _package v1alpha1.Package, oldConfigMap, newConfigMap *corev1.ConfigMap) (bool, error) {
	completedNodes, nodeCount := 0, len(skyhook.GetNodes())
	erroringNode := false

	// if configmap changed
	if !reflect.DeepEqual(oldConfigMap.Data, newConfigMap.Data) {
		for _, node := range skyhook.GetNodes() {
			exists, err := r.PodExists(ctx, node.GetNode().Name, skyhook.GetSkyhook().Name, &_package)
			if err != nil {
				return false, err
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

						// delete the erroring pod from the node so that it can be recreated
						// with the updated configmap
						pods, err := r.dal.GetPods(ctx,
							client.MatchingFields{
								"spec.nodeName": node.GetNode().Name,
							},
							client.MatchingLabels{
								fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):    skyhook.GetSkyhook().Name,
								fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX): fmt.Sprintf("%s-%s", _package.Name, _package.Version),
							},
						)
						if err != nil {
							return false, err
						}

						if pods != nil {
							for _, pod := range pods.Items {
								err := r.Delete(ctx, &pod)
								if err != nil {
									return false, err
								}
							}
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
					return false, fmt.Errorf("error upserting node status [%s]: %w", node.GetNode().Name, err)
				}

				node.SetStatus(v1alpha1.StatusInProgress)
			}

			_, errs := r.SaveNodesAndSkyhook(ctx, clusterState, skyhook)
			if len(errs) > 0 {
				return false, utilerrors.NewAggregate(errs)
			}

			// update config map
			err := r.Update(ctx, newConfigMap)
			if err != nil {
				return false, fmt.Errorf("error updating config map [%s]: %w", newConfigMap.Name, err)
			}

			return true, nil
		}
	}

	return false, nil
}

func (r *SkyhookReconciler) UpsertConfigmaps(ctx context.Context, skyhook SkyhookNodes, clusterState *clusterState) (bool, error) {
	updated := false

	var list corev1.ConfigMapList
	err := r.List(ctx, &list, client.InNamespace(r.opts.Namespace), client.MatchingLabels{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name})
	if err != nil {
		return false, fmt.Errorf("error listing config maps while upserting: %w", err)
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
				return false, fmt.Errorf("error deleting existing config map [%s] while upserting: %w", v.Name, err)
			}
		}
	}

	// Build set of packages that are being uninstalled on any node (source of truth)
	uninstallingPkgs := make(map[string]bool)
	for _, node := range skyhook.GetNodes() {
		ns, err := node.State()
		if err != nil {
			continue
		}
		for _, _package := range skyhook.GetSkyhook().Spec.Packages {
			if ns.IsUninstallInProgress(_package.GetUniqueName()) {
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
			if err := ctrl.SetControllerReference(skyhook.GetSkyhook().Skyhook, newCM, r.scheme); err != nil {
				return false, fmt.Errorf("error setting ownership of cm: %w", err)
			}

			if existingCM, ok := existingCMs[strings.ToLower(fmt.Sprintf("%s-%s-%s", skyhook.GetSkyhook().Name, _package.Name, _package.Version))]; ok {
				updatedConfigMap, err := r.HandleConfigUpdates(ctx, clusterState, skyhook, _package, &existingCM, newCM)
				if err != nil {
					return false, fmt.Errorf("error updating config map [%s]: %s", newCM.Name, err)
				}
				if updatedConfigMap {
					updated = true
				}
			} else {
				// create
				err := r.Create(ctx, newCM)
				if err != nil {
					return false, fmt.Errorf("error creating config map [%s]: %w", newCM.Name, err)
				}
			}
		}
	}

	return updated, nil
}

func (r *SkyhookReconciler) IsDrained(ctx context.Context, skyhookNode wrapper.SkyhookNode) (bool, error) {

	pods, err := r.dal.GetPods(ctx, client.MatchingFields{
		"spec.nodeName": skyhookNode.GetNode().Name,
	})
	if err != nil {
		return false, err
	}

	if pods == nil || len(pods.Items) == 0 {
		return true, nil
	}

	// checking for any running or pending pods with no toleration to unschedulable
	// if its has an unschedulable toleration we can ignore
	for _, pod := range pods.Items {

		if ShouldEvict(&pod) {
			return false, nil
		}

	}

	return true, nil
}

func ShouldEvict(pod *corev1.Pod) bool {
	switch pod.Status.Phase {
	case corev1.PodRunning, corev1.PodPending:

		for _, taint := range pod.Spec.Tolerations {
			switch taint.Key {
			case "node.kubernetes.io/unschedulable": // ignoring
				return false
			}
		}

		if len(pod.ObjectMeta.OwnerReferences) > 1 {
			for _, owner := range pod.ObjectMeta.OwnerReferences {
				if owner.Kind == "DaemonSet" { // ignoring
					return false
				}
			}
		}

		if pod.GetNamespace() == "kube-system" {
			return false
		}

		return true
	}
	return false
}

// HandleFinalizer returns true only if we container is deleted and we handled it completely, else false.
// For Skyhooks with UninstallEnabled packages, this uses a multi-reconcile flow:
// Phase 1: trigger uninstall for enabled packages, return false to requeue
// Phase 2: check completion, requeue if still in progress
// Phase 3: cleanup (zero metrics, uncordon, remove SCR metadata, remove finalizer)
func (r *SkyhookReconciler) HandleFinalizer(ctx context.Context, skyhook SkyhookNodes) (bool, error) {
	if skyhook.GetSkyhook().DeletionTimestamp.IsZero() { // if not deleted, and does not have our finalizer, add it
		if !controllerutil.ContainsFinalizer(skyhook.GetSkyhook().Skyhook, SkyhookFinalizer) {
			controllerutil.AddFinalizer(skyhook.GetSkyhook().Skyhook, SkyhookFinalizer)

			if err := r.Update(ctx, skyhook.GetSkyhook().Skyhook); err != nil {
				return false, fmt.Errorf("error updating skyhook to add finalizer: %w", err)
			}
		}
	} else { // being deleted
		if controllerutil.ContainsFinalizer(skyhook.GetSkyhook().Skyhook, SkyhookFinalizer) {

			// Check if any UninstallEnabled packages still have node state (not yet fully uninstalled).
			// HandleUninstallRequests handles the actual triggering and pod scheduling when
			// DeletionTimestamp is set; the finalizer just checks completion.
			stillInProgress := false
			hasErrors := false
			for _, pkg := range skyhook.GetSkyhook().Spec.Packages {
				if !pkg.UninstallEnabled() {
					continue
				}
				for _, node := range skyhook.GetNodes() {
					nodeState, err := node.State()
					if err != nil {
						continue
					}
					status, inState := nodeState[pkg.GetUniqueName()]
					if inState {
						stillInProgress = true
						if status.State == v1alpha1.StateErroring {
							hasErrors = true
						}
					}
				}
			}
			if stillInProgress {
				condType := fmt.Sprintf("%s/UninstallInProgress", v1alpha1.METADATA_PREFIX)
				skyhook.GetSkyhook().AddCondition(metav1.Condition{
					Type:               condType,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: skyhook.GetSkyhook().Generation,
					LastTransitionTime: metav1.Now(),
					Reason:             "FinalizerUninstall",
					Message:            "Uninstalling enabled packages before CR deletion",
				})
				if hasErrors {
					failedCondType := fmt.Sprintf("%s/UninstallFailed", v1alpha1.METADATA_PREFIX)
					skyhook.GetSkyhook().AddCondition(metav1.Condition{
						Type:               failedCondType,
						Status:             metav1.ConditionTrue,
						ObservedGeneration: skyhook.GetSkyhook().Generation,
						LastTransitionTime: metav1.Now(),
						Reason:             "UninstallPodFailing",
						Message:            "One or more uninstall pods are failing during CR deletion cleanup",
					})
				}
				return false, nil
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

			controllerutil.RemoveFinalizer(skyhook.GetSkyhook().Skyhook, SkyhookFinalizer)
			if err := r.Update(ctx, skyhook.GetSkyhook().Skyhook); err != nil {
				return false, fmt.Errorf("error updating skyhook removing finalizer: %w", err)
			}
			// should be 1, and now 2. we want to set ObservedGeneration up to not trigger an logic from this update adding the finalizer
			skyhook.GetSkyhook().Status.ObservedGeneration = skyhook.GetSkyhook().Status.ObservedGeneration + 1

			if err := r.Status().Update(ctx, skyhook.GetSkyhook().Skyhook); err != nil {
				return false, fmt.Errorf("error updating skyhook status: %w", err)
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
			"spec.nodeName": skyhookNode.GetNode().Name,
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
	pods, err := r.dal.GetPods(ctx,
		client.HasLabels{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX)},
		client.MatchingFields{
			"spec.nodeName": skyhookNode.GetNode().Name,
		},
	)
	if err != nil {
		return false, fmt.Errorf("error getting pods: %w", err)
	}

	return pods != nil && len(pods.Items) > 0, nil
}

func (r *SkyhookReconciler) DrainNode(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) (bool, error) {
	drained, err := r.IsDrained(ctx, skyhookNode)
	if err != nil {
		return false, err
	}
	if drained {
		return true, nil
	}

	pods, err := r.dal.GetPods(ctx, client.MatchingFields{
		"spec.nodeName": skyhookNode.GetNode().Name,
	})
	if err != nil {
		return false, err
	}

	if pods == nil || len(pods.Items) == 0 {
		return true, nil
	}

	r.recorder.Eventf(skyhookNode.GetNode(), EventTypeNormal, EventsReasonSkyhookInterrupt,
		"draining node [%s] package [%s:%s] from [skyhook:%s]",
		skyhookNode.GetNode().Name,
		_package.Name,
		_package.Version,
		skyhookNode.GetSkyhook().Name,
	)

	errs := make([]error, 0)
	for _, pod := range pods.Items {

		if ShouldEvict(&pod) {
			eviction := policyv1.Eviction{}
			err := r.Client.SubResource("eviction").Create(ctx, &pod, &eviction)
			if err != nil {
				errs = append(errs, fmt.Errorf("error evicting pod [%s:%s]: %w", pod.Namespace, pod.Name, err))
			}
		}
	}

	return len(errs) == 0, utilerrors.NewAggregate(errs)
}

// Interrupt should not be called unless safe to do so, IE already cordoned and drained
func (r *SkyhookReconciler) Interrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package, _interrupt *v1alpha1.Interrupt) error {

	hasPackagesRunning, err := r.HasRunningPackages(ctx, skyhookNode)
	if err != nil {
		return err
	}

	if hasPackagesRunning { // keep waiting...
		return nil
	}

	exists, err := r.PodExists(ctx, skyhookNode.GetNode().Name, skyhookNode.GetSkyhook().Name, _package)
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

	pod := createInterruptPodForPackage(r.opts, _interrupt, argEncode, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name)

	if err := SetPackages(pod, skyhookNode.GetSkyhook().Skyhook, _package.Image, v1alpha1.StageInterrupt, _package); err != nil {
		return fmt.Errorf("error setting package on interrupt: %w", err)
	}

	if err := ctrl.SetControllerReference(skyhookNode.GetSkyhook().Skyhook, pod, r.scheme); err != nil {
		return fmt.Errorf("error setting ownership: %w", err)
	}

	if err := r.Create(ctx, pod); err != nil {
		return fmt.Errorf("error creating interruption pod: %w", err)
	}

	_ = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, v1alpha1.StageInterrupt, 0, _package.ContainerSHA)

	r.recorder.Eventf(skyhookNode.GetSkyhook().Skyhook, EventTypeNormal, EventsReasonSkyhookInterrupt,
		"Interrupting node [%s] package [%s:%s] from [skyhook:%s]",
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
		return update, fmt.Errorf("error getting skyhook for metadata validation: %w", err)
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

// PodExists tests if this package is exists on a node.
func (r *SkyhookReconciler) PodExists(ctx context.Context, nodeName, skyhookName string, _package *v1alpha1.Package) (bool, error) {

	pods, err := r.dal.GetPods(ctx,
		client.MatchingFields{
			"spec.nodeName": nodeName,
		},
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):    skyhookName,
			fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX): fmt.Sprintf("%s-%s", _package.Name, _package.Version),
		},
	)
	if err != nil {
		return false, fmt.Errorf("error check from existing pods: %w", err)
	}

	if pods == nil || len(pods.Items) == 0 {
		return false, nil
	}
	return true, nil
}

// createInterruptPodForPackage returns the pod spec for an interrupt pod given an package
func createInterruptPodForPackage(opts SkyhookOperatorOptions, _interrupt *v1alpha1.Interrupt, argEncode string, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string) *corev1.Pod {
	copyDir := fmt.Sprintf("%s/%s/%s-%s-%s-%d",
		opts.CopyDirRoot,
		skyhook.Name,
		_package.Name,
		_package.Version,
		skyhook.UID,
		skyhook.Generation,
	)

	volumes := []corev1.Volume{
		{
			Name: "root-mount",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
		{
			// node names in different CSPs might include dots which isn't allowed in volume names
			// so we have to replace all dots with dashes
			Name: generateSafeName(63, skyhook.Name, nodeName, "metadata"),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: strings.ReplaceAll(fmt.Sprintf("%s-%s-metadata", skyhook.Name, nodeName), ".", "-"),
					},
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:             "root-mount",
			MountPath:        "/root",
			MountPropagation: ptr(corev1.MountPropagationHostToContainer),
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateSafeName(63, skyhook.Name, "interrupt", string(_interrupt.Type), nodeName),
			Namespace: opts.Namespace,
			Labels: map[string]string{
				fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):      skyhook.Name,
				fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX):   fmt.Sprintf("%s-%s", _package.Name, _package.Version),
				fmt.Sprintf("%s/interrupt", v1alpha1.METADATA_PREFIX): "True",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyOnFailure,
			InitContainers: []corev1.Container{
				{
					Name:  InterruptContainerName,
					Image: getAgentImage(opts, _package),
					Args:  []string{"interrupt", "/root", copyDir, argEncode},
					Env:   getAgentConfigEnvVars(opts, _package.Name, _package.Version, skyhook.ResourceID(), skyhook.Name, skyhook.NodeOrder(nodeName)),
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "pause",
					Image: opts.PauseImage,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
					},
				},
			},
			HostPID:     true,
			HostNetwork: true,
			// If you change these go change the SelectNode toleration in cluster_state.go
			Tolerations: append([]corev1.Toleration{ // tolerate all cordon
				{
					Key:      TaintUnschedulable,
					Operator: corev1.TolerationOpExists,
				},
				opts.GetRuntimeRequiredToleration(),
			}, skyhook.Spec.AdditionalTolerations...),
			Volumes: volumes,
		},
	}
	if opts.ImagePullSecret != "" {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: opts.ImagePullSecret,
			},
		}
	}
	return pod
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
			Value: "false",
		},
		{
			Name:  "SKYHOOK_RESOURCE_ID",
			Value: fmt.Sprintf("%s_%s_%s", resourceID, packageName, packageVersion),
		},
		{
			Name:  "SKYHOOK_NODE_ORDER",
			Value: strconv.Itoa(nodeOrder),
		},
	}
}

// createPodFromPackage creates a pod spec for a skyhook pod for a given package
func createPodFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage) *corev1.Pod {
	// Generate consistent names that won't exceed k8s limits
	volumeName := generateSafeName(63, "metadata", nodeName)
	configMapName := generateSafeName(253, skyhook.Name, nodeName, "metadata")

	volumes := []corev1.Volume{
		{
			Name: "root-mount",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
		{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:             "root-mount",
			MountPath:        "/root",
			MountPropagation: ptr(corev1.MountPropagationHostToContainer),
		},
		{
			Name:      volumeName,
			MountPath: "/skyhook-package/node-metadata",
		},
	}

	if len(_package.ConfigMap) > 0 {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      _package.Name,
			MountPath: "/skyhook-package/configmaps",
		})

		volumes = append(volumes, corev1.Volume{
			Name: _package.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: strings.ToLower(fmt.Sprintf("%s-%s-%s", skyhook.Name, _package.Name, _package.Version)),
					},
				},
			},
		})
	}

	copyDir := fmt.Sprintf("%s/%s/%s-%s-%s-%d",
		opts.CopyDirRoot,
		skyhook.Name,
		_package.Name,
		_package.Version,
		skyhook.UID,
		skyhook.Generation,
	)
	applyargs := []string{strings.ToLower(string(stage)), "/root", copyDir}
	checkargs := []string{strings.ToLower(string(stage) + "-check"), "/root", copyDir}

	agentEnvs := append(
		_package.Env,
		getAgentConfigEnvVars(opts, _package.Name, _package.Version, skyhook.ResourceID(), skyhook.Name, skyhook.NodeOrder(nodeName))...,
	)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateSafeName(63, skyhook.Name, _package.Name, _package.Version, string(stage), nodeName),
			Namespace: opts.Namespace,
			Labels: map[string]string{
				fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):    skyhook.Name,
				fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX): fmt.Sprintf("%s-%s", _package.Name, _package.Version),
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyOnFailure,
			InitContainers: []corev1.Container{
				{
					Name:            fmt.Sprintf("%s-init", trunstr(_package.Name, 43)),
					Image:           getPackageImage(_package),
					ImagePullPolicy: "Always",
					Command:         []string{"/bin/sh"},
					Args: []string{
						"-c",
						"mkdir -p /root/${SKYHOOK_DIR} && cp -r /skyhook-package/* /root/${SKYHOOK_DIR}",
					},
					Env: []corev1.EnvVar{
						{
							Name:  "SKYHOOK_DIR",
							Value: copyDir,
						},
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
				},
				{
					Name:            fmt.Sprintf("%s-%s", trunstr(_package.Name, 43), stage),
					Image:           getAgentImage(opts, _package),
					ImagePullPolicy: "Always",
					Args:            applyargs,
					Env:             agentEnvs,
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
				},
				{
					Name:            fmt.Sprintf("%s-%scheck", trunstr(_package.Name, 43), stage),
					Image:           getAgentImage(opts, _package),
					ImagePullPolicy: "Always",
					Args:            checkargs,
					Env:             agentEnvs,
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "pause",
					Image: opts.PauseImage,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
					},
				},
			},
			Volumes:     volumes,
			HostPID:     true,
			HostNetwork: true,
			// If you change these go change the SelectNode toleration in cluster_state.go
			Tolerations: append([]corev1.Toleration{ // tolerate all cordon
				{
					Key:      TaintUnschedulable,
					Operator: corev1.TolerationOpExists,
				},
				opts.GetRuntimeRequiredToleration(),
			}, skyhook.Spec.AdditionalTolerations...),
		},
	}
	if opts.ImagePullSecret != "" {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: opts.ImagePullSecret,
			},
		}
	}
	if _package.GracefulShutdown != nil {
		pod.Spec.TerminationGracePeriodSeconds = ptr(int64(_package.GracefulShutdown.Duration.Seconds()))
	}
	setPodResources(pod, _package.Resources)
	return pod
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

// PodMatchesPackage asserts that a given pod matches the given pod spec
func podMatchesPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, pod corev1.Pod, skyhook *wrapper.Skyhook, stage v1alpha1.Stage) bool {
	var expectedPod *corev1.Pod

	// need to differentiate whether the pod is for an interrupt or not so we know
	// what to expect and how to compare them
	isInterrupt := false
	_, limitRange := pod.Annotations["kubernetes.io/limit-ranger"]

	if pod.Labels[fmt.Sprintf("%s/interrupt", v1alpha1.METADATA_PREFIX)] == "True" {
		expectedPod = createInterruptPodForPackage(opts, &v1alpha1.Interrupt{}, "", _package, skyhook, "")
		isInterrupt = true
	} else {
		expectedPod = createPodFromPackage(opts, _package, skyhook, "", stage)
	}

	actualPod := pod.DeepCopy()

	// check to see whether the name or the version of the package changed
	packageLabel := fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)
	if actualPod.Labels[packageLabel] != expectedPod.Labels[packageLabel] {
		return false
	}

	// compare initContainers since this is where a lot of the important info lives
	for i := range actualPod.Spec.InitContainers {
		expectedContainer := expectedPod.Spec.InitContainers[i]
		actualContainer := actualPod.Spec.InitContainers[i]

		if expectedContainer.Name != actualContainer.Name {
			return false
		}

		if expectedContainer.Image != actualContainer.Image {
			return false
		}

		// compare the containers env vars except for the ones that are inserted
		// by the operator by default as the SKYHOOK_RESOURCE_ID will change every
		// time the skyhook is updated and would cause every pod to be removed
		// TODO: This is ignoring all the static env vars that are set by operator config.
		// It probably should be just SKYHOOK_RESOURCE_ID that is ignored. Otherwise,
		// a user will have to manually delete the pod to update the package when operator is updated.
		dummyAgentEnv := getAgentConfigEnvVars(opts, "", "", "", "", 0)
		excludedEnvs := make([]string, len(dummyAgentEnv))
		for i, env := range dummyAgentEnv {
			excludedEnvs[i] = env.Name
		}
		expectedFilteredEnv := FilterEnv(expectedContainer.Env, excludedEnvs...)
		actualFilteredEnv := FilterEnv(actualContainer.Env, excludedEnvs...)
		if !reflect.DeepEqual(expectedFilteredEnv, actualFilteredEnv) {
			return false
		}

		if !isInterrupt { // dont compare these since they are not configured on interrupt
			// compare resource requests and limits (CPU, memory, etc.)
			expectedResources := expectedContainer.Resources
			actualResources := actualContainer.Resources
			if skyhook.Spec.Packages[_package.Name].Resources != nil {
				// If CR has resources specified, they should match exactly
				if !reflect.DeepEqual(expectedResources, actualResources) {
					return false
				}
			} else {
				// If CR has no resources specified, ensure pod has no resource overrides
				if !limitRange {
					if actualResources.Requests != nil || actualResources.Limits != nil {
						return false
					}
				}
			}
		}
	}

	return true
}

// ValidateRunningPackages deletes pods that don't match the current spec and checks if there are pods running
// that don't match the node state and removes them if they exist
func (r *SkyhookReconciler) ValidateRunningPackages(ctx context.Context, skyhook SkyhookNodes) (bool, error) {

	update := false
	errs := make([]error, 0)
	// get all pods for this skyhook packages
	pods, err := r.dal.GetPods(ctx,
		client.MatchingLabels{
			fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhook.GetSkyhook().Name,
		},
	)
	if err != nil {
		return false, fmt.Errorf("error getting pods while validating packages: %w", err)
	}
	if pods == nil || len(pods.Items) == 0 {
		return false, nil // nothing running for this skyhook on this node
	}

	// Initialize metrics for each stage
	stages := make(map[string]map[string]map[v1alpha1.Stage]int)

	// group pods by node
	podsbyNode := make(map[string][]corev1.Pod)
	for _, pod := range pods.Items {
		podsbyNode[pod.Spec.NodeName] = append(podsbyNode[pod.Spec.NodeName], pod)
	}

	for _, node := range skyhook.GetNodes() {
		nodeState, err := node.State()
		if err != nil {
			return false, fmt.Errorf("error getting node state: %w", err)
		}

		for _, pod := range podsbyNode[node.GetNode().Name] {
			found := false

			runningPackage, err := GetPackage(&pod)
			if err != nil {
				errs = append(errs, fmt.Errorf("error getting package from pod [%s:%s] while validating packages: %w", pod.Namespace, pod.Name, err))
			}

			// check if the package is part of the skyhook spec, if not we need to delete it
			for _, v := range skyhook.GetSkyhook().Spec.Packages {
				if podMatchesPackage(r.opts, &v, pod, skyhook.GetSkyhook(), runningPackage.Stage) {
					found = true
				}
			}

			// Increment the stage count for metrics
			if _, ok := stages[runningPackage.Name]; !ok {
				stages[runningPackage.Name] = make(map[string]map[v1alpha1.Stage]int)
				if _, ok := stages[runningPackage.Name][runningPackage.Version]; !ok {
					stages[runningPackage.Name][runningPackage.Version] = make(map[v1alpha1.Stage]int)
					for _, stage := range v1alpha1.Stages {
						stages[runningPackage.Name][runningPackage.Version][stage] = 0
					}
				}
			}
			stages[runningPackage.Name][runningPackage.Version][runningPackage.Stage]++

			// uninstall is by definition not part of the skyhook spec, so we cant delete it (because it used to be but was removed, hence uninstalling it)
			if runningPackage.Stage == v1alpha1.StageUninstall {
				found = true
			}

			if !found {
				update = true

				err := r.InvalidPackage(ctx, &pod)
				if err != nil {
					errs = append(errs, fmt.Errorf("error invalidating package: %w", err))
				}
				continue
			}

			// Check if package exists in node state, ie a package running that the node state doesn't know about
			// something that is often done to try to fix bad node state is to clear the node state completely
			// which if a package is running, we want to terminate it gracefully. Ofthen what leads to this is
			// the package is in a crashloop and the operator want to restart it the whole package.
			// when we apply a package it just check if there is a running package on the node for the state of the package
			// this can cause to leave a pod running in say config mode, and it there is a depends on you might not correctly
			// run thins in the correct order.
			deleteMe := false
			packageStatus, exists := nodeState[runningPackage.GetUniqueName()]
			if !exists { // package not in node state, so we need to delete it
				deleteMe = true
			} else { // package in node state, so we need to check if it's running
				// need check if the stats match, if not we need to delete it
				if packageStatus.Stage != runningPackage.Stage {
					deleteMe = true
				}
			}

			if deleteMe {
				update = true
				err := r.InvalidPackage(ctx, &pod)
				if err != nil {
					errs = append(errs, fmt.Errorf("error invalidating package: %w", err))
				}
			}
		}
	}

	return update, utilerrors.NewAggregate(errs)
}

// InvalidPackage invalidates a package and updates the pod, which will trigger the pod to be deleted
func (r *SkyhookReconciler) InvalidPackage(ctx context.Context, pod *corev1.Pod) error {
	err := InvalidatePackage(pod)
	if err != nil {
		return fmt.Errorf("error invalidating package: %w", err)
	}

	err = r.Update(ctx, pod)
	if err != nil {
		return fmt.Errorf("error updating pod: %w", err)
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
	if found && status.State == v1alpha1.StateSkipped {
		return false, nil
	}

	// Theres is a race condition when a node reboots and api cleans up the interrupt pod
	// so we need to check if the pod exists and if it does, we need to recreate it
	if status != nil && (status.State == v1alpha1.StateInProgress || status.State == v1alpha1.StateErroring) && status.Stage == v1alpha1.StageInterrupt {
		// call interrupt to recreate the pod if missing
		err := r.Interrupt(ctx, skyhookNode, _package, interrupt)
		if err != nil {
			return false, err
		}
	}

	// drain and cordon node before applying package that has an interrupt
	if stage == v1alpha1.StageApply {
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
		err := r.Interrupt(ctx, skyhookNode, _package, interrupt)
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

	// wait tell this is done if its happening
	if status != nil && status.Stage == v1alpha1.StageInterrupt && status.State != v1alpha1.StateComplete {
		return false, nil
	}

	return true, nil
}

func (r *SkyhookReconciler) EnsureNodeIsReadyForInterrupt(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) (bool, error) {
	// cordon node
	skyhookNode.Cordon()

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
		case v1alpha1.StageConfig, v1alpha1.StageUpgrade, v1alpha1.StageUninstall:
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

	// test if pod exists, if so, bailout
	exists, err := r.PodExists(ctx, skyhookNode.GetNode().Name, skyhookNode.GetSkyhook().Name, _package)
	if err != nil {
		return err
	}

	// wait tell this is done if its happening
	status, found := skyhookNode.PackageStatus(_package.GetUniqueName())

	if found && status.State == v1alpha1.StateSkipped { // skipped, so nothing to do
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

	pod := createPodFromPackage(r.opts, _package, skyhookNode.GetSkyhook(), skyhookNode.GetNode().Name, stage)

	if err := SetPackages(pod, skyhookNode.GetSkyhook().Skyhook, _package.Image, stage, _package); err != nil {
		return fmt.Errorf("error setting package on pod: %w", err)
	}

	// setup ownership of the pod we created
	// helps run time know what to do when something happens to this pod we are about to create
	if err := ctrl.SetControllerReference(skyhookNode.GetSkyhook().Skyhook, pod, r.scheme); err != nil {
		return fmt.Errorf("error setting ownership: %w", err)
	}

	if err := r.Create(ctx, pod); err != nil {
		return fmt.Errorf("error creating pod: %w", err)
	}

	if err = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateInProgress, stage, 0, _package.ContainerSHA); err != nil {
		err = fmt.Errorf("error upserting package: %w", err) // want to keep going in this case, but don't want to lose the err
	}

	skyhookNode.SetStatus(v1alpha1.StatusInProgress)

	skyhookNode.GetSkyhook().AddCondition(metav1.Condition{
		Type:               fmt.Sprintf("%s/ApplyPackage", v1alpha1.METADATA_PREFIX),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: skyhookNode.GetSkyhook().Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "ApplyPackage",
		Message:            fmt.Sprintf("Applying package [%s:%s] to node [%s]", _package.Name, _package.Version, skyhookNode.GetNode().Name),
	})

	r.recorder.Eventf(skyhookNode.GetNode(), EventTypeNormal, EventsReasonSkyhookApply, "Applying package [%s:%s] from [skyhook:%s] stage [%s]", _package.Name, _package.Version, skyhookNode.GetSkyhook().Name, stage)
	r.recorder.Eventf(skyhookNode.GetSkyhook(), EventTypeNormal, EventsReasonSkyhookApply, "Applying package [%s:%s] to node [%s] stage [%s]", _package.Name, _package.Version, skyhookNode.GetNode().Name, stage)

	skyhookNode.GetSkyhook().Updated = true

	return err
}

// HandleRuntimeRequired finds any nodes for which all runtime required Skyhooks are complete and remove their runtime required taint
// Will return an error if the patching of the nodes is not possible
func (r *SkyhookReconciler) HandleRuntimeRequired(ctx context.Context, clusterState *clusterState) error {
	node_to_skyhooks, skyhook_node_map := groupSkyhooksByNode(clusterState)
	to_remove := getRuntimeRequiredTaintCompleteNodes(node_to_skyhooks, skyhook_node_map)
	// Remove the runtime required taint from nodes in to_remove
	taint_to_remove := r.opts.GetRuntimeRequiredTaint()
	errs := make([]error, 0)
	for _, node := range to_remove {
		// check before removing taint that it even exists to begin with
		if !taints.TaintExists(node.Spec.Taints, &taint_to_remove) {
			continue
		}
		// RemoveTaint will ALWAYS return nil for its error so no need to check it
		new_node, updated, _ := taints.RemoveTaint(node, &taint_to_remove)
		if updated {
			err := r.Patch(ctx, new_node, client.MergeFrom(node))
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
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
// Skyhooks that have AutoTaintNewNodes enabled.
func (r *SkyhookReconciler) HandleAutoTaint(ctx context.Context, clusterState *clusterState) (bool, error) {
	taint_to_add := r.opts.GetRuntimeRequiredTaint()
	to_taint := clusterState.getAutoTaintNodes(taint_to_add)
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
		newNode.Annotations[fmt.Sprintf("%s/autoTaint_%s", v1alpha1.METADATA_PREFIX, taint_to_add.Key)] = "true"

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

// validateAndUpsertSkyhookData performs validation and configmap operations for a skyhook
func (r *SkyhookReconciler) validateAndUpsertSkyhookData(ctx context.Context, skyhook SkyhookNodes, clusterState *clusterState) (bool, ctrl.Result, error) {
	if yes, result, err := shouldReturn(r.ValidateRunningPackages(ctx, skyhook)); yes {
		return yes, result, err
	}

	if yes, result, err := shouldReturn(r.ValidateNodeConfigmaps(ctx, skyhook.GetSkyhook().Name, skyhook.GetNodes())); yes {
		return yes, result, err
	}

	if yes, result, err := shouldReturn(r.UpsertConfigmaps(ctx, skyhook, clusterState)); yes {
		return yes, result, err
	}

	return false, ctrl.Result{}, nil
}
