// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
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
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	artifactclient "github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// packageArtifactRetry and packageArtifactRetryMax bound how long a reference that could not be
	// read for a reason that does not pass by itself, such as a refused credential or a missing
	// tag, stands before it is read again: packageArtifactRetry after the first such failure,
	// doubled for each one in a row, up to packageArtifactRetryMax. That is how a pull Secret fixed
	// later, or a push that comes later, is picked up.
	packageArtifactRetry    = 5 * time.Minute
	packageArtifactRetryMax = time.Hour
	// packageArtifactPendingRequeue is how soon a pass that queued an inspection runs again, to read
	// what the inspection found.
	packageArtifactPendingRequeue = 2 * time.Second
	// packageArtifactHostResolutions is how many references of one registry host a pass resolves at
	// once, after the probe that shows the host can be connected to (resolveConcurrently). It bounds
	// what a pass sends a host that answers; once one of them times out, the rest go at once, so a
	// host whose repositories take the connection and never answer costs a pass about three
	// timeouts, whatever number of references it serves.
	packageArtifactHostResolutions = 8
)

// packageArtifactKind is what the operator knows of the artifact a package names in one pass. The
// image, artifact and rejected kinds are also what the cache keeps for a digest.
type packageArtifactKind string

const (
	// packageArtifactDisabled: PACKAGE_ARTIFACTS is off.
	packageArtifactDisabled packageArtifactKind = "disabled"
	// packageArtifactUnresolved: the pass did not resolve the reference, because it builds no Job
	// for the package: the package's Job exists, or its stage is an interrupt.
	packageArtifactUnresolved packageArtifactKind = "unresolved"
	// packageArtifactPending: the digest the reference names is not in the cache yet, and its
	// inspection is queued.
	packageArtifactPending packageArtifactKind = "pending"
	// packageArtifactUnavailable: the reference could not be read for a transient reason, such as a
	// timeout or an unavailable registry.
	packageArtifactUnavailable packageArtifactKind = "unavailable"
	// packageArtifactUnreadable: the reference could not be read for a reason that does not pass by
	// itself, such as a refused credential or a missing tag.
	packageArtifactUnreadable packageArtifactKind = "unreadable"
	packageArtifactImage      packageArtifactKind = "image"
	packageArtifactAccepted   packageArtifactKind = "artifact"
	packageArtifactRejected   packageArtifactKind = "rejected"
)

// packageArtifactOutcome is what the operator knows of the artifact a package names.
type packageArtifactOutcome struct {
	Kind packageArtifactKind
	// Pinned is the reference at the digest it names (registry/repository@sha256:hex), for an
	// image, an accepted artifact, and a digest pending inspection.
	Pinned string
	// Uninstall is set only when accepted.
	Uninstall artifact.Uninstall
	// Reason is set only when rejected.
	Reason string
	// ErrorClass is set only when unavailable or unreadable.
	ErrorClass string
	// RetryAt is when the reference is read again: an unavailable or unreadable one, and a tag,
	// once the digest it resolved to stops answering for it (packageArtifactResolvedFor); zero for
	// one that is never read again, such as a containerSHA or a reference that does not parse.
	RetryAt time.Time
}

func rejectedPackageArtifact(reason string) packageArtifactOutcome {
	return packageArtifactOutcome{Kind: packageArtifactRejected, Reason: reason}
}

// packageArtifactBlocks reports whether outcome leaves the package with no Job at every stage until
// the package or its registry changes: its artifact is rejected, or its reference could not be read
// for a reason that does not pass by itself.
func packageArtifactBlocks(outcome packageArtifactOutcome) bool {
	return outcome.Kind == packageArtifactRejected || outcome.Kind == packageArtifactUnreadable
}

func (o packageArtifactOutcome) memo() wrapper.PackageArtifact {
	reason := o.Reason
	if o.Kind == packageArtifactUnavailable || o.Kind == packageArtifactUnreadable {
		reason = o.ErrorClass
	}
	return wrapper.PackageArtifact{Kind: string(o.Kind), Pinned: o.Pinned, Uninstall: string(o.Uninstall), Reason: reason}
}

func outcomeOfMemo(m wrapper.PackageArtifact) packageArtifactOutcome {
	outcome := packageArtifactOutcome{Kind: packageArtifactKind(m.Kind), Pinned: m.Pinned, Uninstall: artifact.Uninstall(m.Uninstall)}
	if outcome.Kind == packageArtifactUnavailable || outcome.Kind == packageArtifactUnreadable {
		outcome.ErrorClass = m.Reason
	} else {
		outcome.Reason = m.Reason
	}
	return outcome
}

// packageReference returns the reference the operator reads for _package: its image at its
// containerSHA, or else at its version, as the operator builds the image it runs.
func packageReference(_package *v1alpha1.Package) (artifactclient.Reference, error) {
	ref, err := artifactclient.NewReference(_package.Image, _package.Version, _package.ContainerSHA)
	if err != nil {
		return artifactclient.Reference{}, fmt.Errorf("building the reference of package %s: %w", _package.GetUniqueName(), err)
	}
	return ref, nil
}

// packageArtifactKey is the key of what a pass resolved of _package: its version and the image it
// names, so that two references of one version never share one.
func packageArtifactKey(_package *v1alpha1.Package) string {
	return _package.GetUniqueName() + "|" + getPackageImage(_package)
}

// packageArtifactRetryAfter is how long an unreadable reference that failed the attempts-th time in
// a row waits before it is read again.
func packageArtifactRetryAfter(attempts int32) time.Duration {
	wait := packageArtifactRetry
	for i := int32(1); i < attempts && wait < packageArtifactRetryMax; i++ {
		wait *= 2
	}
	return min(wait, packageArtifactRetryMax)
}

// buildsStageJob reports whether a pass would build a stage Job of _package on node, unless its Job
// runs: one not at an interrupt stage, which runs an interrupt Job instead, and not skipped. An
// interrupt stage the node has not completed counts as one although applyPackageStage answers
// apply for it, since ProcessInterrupt runs its interrupt Job again, and the reference is not read.
func buildsStageJob(node wrapper.SkyhookNode, _package *v1alpha1.Package) bool {
	status, found := node.PackageStatus(_package.GetUniqueName())
	if found && (status.IsSkipped() || (status.IsInterruptStage() && status.State != v1alpha1.StateComplete)) {
		return false
	}
	stage := applyPackageStage(node, _package)
	return stage != v1alpha1.StageInterrupt && stage != v1alpha1.StageUninstallInterrupt
}

// needsStageJob reports whether a pass would build a stage Job of _package on node: one
// buildsStageJob would build, and whose Job does not run.
func (r *SkyhookReconciler) needsStageJob(ctx context.Context, node wrapper.SkyhookNode, _package *v1alpha1.Package) (bool, error) {
	if !buildsStageJob(node, _package) {
		return false, nil
	}
	exists, err := r.JobExists(ctx, node.GetNode().Name, node.GetSkyhook().Name, _package)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// runningJobKey keys a Job that has not finished by the node and package labels JobExists matches.
func runningJobKey(nodeLabel, packageLabel string) string {
	return nodeLabel + "|" + packageLabel
}

// runningJobs returns the Jobs of the NodeWright named skyhookName that have not finished, by
// runningJobKey, read with one list: what JobExists answers for each node and package, without a
// list of the namespace's Jobs for each.
func (r *SkyhookReconciler) runningJobs(ctx context.Context, skyhookName string) (map[string]bool, error) {
	jobs, err := r.dal.GetJobs(ctx,
		client.InNamespace(r.opts.Namespace),
		client.MatchingLabels{fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX): skyhookName},
	)
	if err != nil {
		return nil, fmt.Errorf("error checking existing jobs: %w", err)
	}
	running := map[string]bool{}
	if jobs == nil {
		return running, nil
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !jobFinished(job) {
			running[runningJobKey(job.Labels[jobNodeLabel], job.Labels[fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)])] = true
		}
	}
	return running, nil
}

// packageArtifactWants is what one NodeWright's pass resolves the artifacts of: its packages, by
// packageArtifactKey, and the packages of the next stage Job of each of its nodes, by node name.
type packageArtifactWants struct {
	nodewright *wrapper.Skyhook
	packages   map[string]*v1alpha1.Package
	byNode     map[string][]*v1alpha1.Package
}

// packageArtifactResolveRequest is one reference a pass resolves: the package that names it, and its
// NodeWright, which the events of the resolution regard.
type packageArtifactResolveRequest struct {
	nodewright *v1alpha1.NodeWright
	_package   *v1alpha1.Package
}

// resolvePackageArtifacts is resolvePackageArtifactsOf for the one NodeWright of skyhook.
func (r *SkyhookReconciler) resolvePackageArtifacts(ctx context.Context, skyhook SkyhookNodes) (*ctrl.Result, error) {
	return r.resolvePackageArtifactsOf(ctx, []SkyhookNodes{skyhook})
}

// resolvePackageArtifactsOf runs first in a pass, for every NodeWright of it at once. While
// PACKAGE_ARTIFACTS is on, it resolves the artifact of every package the pass decides, concurrently
// and once per reference, whatever number of NodeWrights name it (resolveConcurrently): the next
// packages of each node that would get a stage Job, and each package whose uninstall is decided. It
// gives what it resolved, and the nodes it blocks, to each NodeWright and every node of it for the
// rest of the pass, and asks for the pass to run again when an inspection is pending, a reference
// that could not be read is due to be read again, or a tag's digest stops answering for it. Nothing
// is resolved for a NodeWright held (packageArtifactsHeld).
func (r *SkyhookReconciler) resolvePackageArtifactsOf(ctx context.Context, skyhooks []SkyhookNodes) (*ctrl.Result, error) {
	wanted := make([]packageArtifactWants, 0, len(skyhooks))
	for _, skyhook := range skyhooks {
		wants, err := r.packageArtifactsWanted(ctx, skyhook)
		if err != nil {
			return nil, err
		}
		wanted = append(wanted, wants)
	}

	references := map[string]packageArtifactResolveRequest{}
	for _, wants := range wanted {
		for _, key := range slices.Sorted(maps.Keys(wants.packages)) {
			reference := packageArtifactReferenceKey(wants.packages[key])
			if _, ok := references[reference]; !ok {
				references[reference] = packageArtifactResolveRequest{nodewright: wants.nodewright.NodeWright, _package: wants.packages[key]}
			}
		}
	}
	if len(references) == 0 {
		return nil, nil
	}
	outcomes := r.resolveConcurrently(ctx, references)

	now := r.opts.artifactInspector.now()
	var result *ctrl.Result
	for _, wants := range wanted {
		for _, key := range slices.Sorted(maps.Keys(wants.packages)) {
			outcome := outcomes[packageArtifactReferenceKey(wants.packages[key])]
			wants.nodewright.PackageArtifacts[key] = outcome.memo()
			switch {
			case outcome.Kind == packageArtifactPending:
				result = soonerRequeue(result, &ctrl.Result{RequeueAfter: packageArtifactPendingRequeue})
			case !outcome.RetryAt.IsZero():
				result = soonerRequeue(result, &ctrl.Result{RequeueAfter: max(outcome.RetryAt.Sub(now), time.Second)})
			}
		}
		for name, packages := range wants.byNode {
			if slices.ContainsFunc(packages, func(_package *v1alpha1.Package) bool {
				return packageArtifactBlocks(outcomes[packageArtifactReferenceKey(_package)])
			}) {
				wants.nodewright.PackageArtifactBlockedNodes[name] = true
			}
		}
	}
	return result, nil
}

// packageArtifactsWanted gives the NodeWright of skyhook, and every node of it, the empty results of
// this pass, and returns what the pass resolves for it: nothing while PACKAGE_ARTIFACTS is off or
// the NodeWright is held.
func (r *SkyhookReconciler) packageArtifactsWanted(ctx context.Context, skyhook SkyhookNodes) (packageArtifactWants, error) {
	nodewright := skyhook.GetSkyhook()
	resolved := map[string]wrapper.PackageArtifact{}
	nodewright.PackageArtifacts = resolved
	nodewright.PackageArtifactBlockedNodes = map[string]bool{}
	// The node wrappers each hold their own Skyhook of the same NodeWright, so each gets the one map.
	for _, node := range skyhook.GetNodes() {
		node.GetSkyhook().PackageArtifacts = resolved
	}
	wants := packageArtifactWants{nodewright: nodewright, packages: map[string]*v1alpha1.Package{}, byNode: map[string][]*v1alpha1.Package{}}
	if !r.opts.PackageArtifacts || r.opts.artifactInspector == nil || packageArtifactsHeld(nodewright) {
		return wants, nil
	}

	beingDeleted := !nodewright.DeletionTimestamp.IsZero()
	candidates := uninstallCandidates(skyhook)
	// Listed once, at the first package that needs it, as the pass decides for every unfinished
	// node and a list per node and package would scan the namespace's Jobs for each.
	var running map[string]bool
	for _, node := range skyhook.GetNodes() {
		if _, err := node.State(); err != nil || node.IsComplete() {
			continue
		}
		toRun, err := packagesToRun(node, candidates, beingDeleted)
		if err != nil {
			continue
		}
		for _, _package := range toRun {
			if !buildsStageJob(node, _package) {
				continue
			}
			if running == nil {
				if running, err = r.runningJobs(ctx, nodewright.Name); err != nil {
					return wants, fmt.Errorf("deciding the package artifacts of NodeWright %s: %w", nodewright.Name, err)
				}
			}
			if !running[runningJobKey(nodeLabelValue(node.GetNode().Name), fmt.Sprintf("%s-%s", _package.Name, _package.Version))] {
				wants.packages[packageArtifactKey(_package)] = _package
				wants.byNode[node.GetNode().Name] = append(wants.byNode[node.GetNode().Name], _package)
			}
		}
	}
	states := parsedNodeStates(skyhook)
	for name := range nodewright.Spec.Packages {
		_package := nodewright.Spec.Packages[name]
		if uninstallUnderDecision(nodewright, &_package, states) {
			wants.packages[packageArtifactKey(&_package)] = &_package
		}
	}
	return wants, nil
}

// packageArtifactReferenceKey is the reference _package names, which one resolution answers for
// every package and NodeWright that names it; a reference that does not parse is its own.
func packageArtifactReferenceKey(_package *v1alpha1.Package) string {
	ref, err := packageReference(_package)
	if err != nil {
		return "invalid|" + packageArtifactKey(_package)
	}
	return ref.String()
}

// prunePackageArtifacts removes from the package artifact cache, and from what the inspector
// remembers, every digest and reference no package of nodewrights, the NodeWrights of the cluster,
// names (packageArtifactInspector.prune), while PACKAGE_ARTIFACTS is on.
func (r *SkyhookReconciler) prunePackageArtifacts(ctx context.Context, nodewrights *v1alpha1.NodeWrightList) {
	if !r.opts.PackageArtifacts || r.opts.artifactInspector == nil {
		return
	}
	var items []v1alpha1.NodeWright
	if nodewrights != nil {
		items = nodewrights.Items
	}
	r.opts.artifactInspector.prune(ctx, items)
}

// resolveConcurrently resolves each of references, by its key: the registry hosts at once, and on
// each host one probe first, then the rest, packageArtifactHostResolutions at a time. The probe is
// the first tag a resolution would send a request for (packageArtifactInspector.asks): when the
// host cannot be connected to, its failure opens the host's packageArtifactUnavailableWait, which
// turns the rest away with no request, so a black-holed host costs one connection per window. A
// failure once connected is the reference's own, so the rest are asked; once one of them times out
// they go at once, so a host whose repositories take the connection and never answer costs about
// three timeouts, whatever number of references it serves.
func (r *SkyhookReconciler) resolveConcurrently(ctx context.Context, references map[string]packageArtifactResolveRequest) map[string]packageArtifactOutcome {
	inspector := r.opts.artifactInspector
	byHost := map[string][]string{}
	for _, key := range slices.Sorted(maps.Keys(references)) {
		host := ""
		if ref, err := packageReference(references[key]._package); err == nil {
			host = ref.APIHost()
		}
		byHost[host] = append(byHost[host], key)
	}
	outcomes := make(map[string]packageArtifactOutcome, len(references))
	var mu sync.Mutex
	resolve := func(key string) packageArtifactOutcome {
		outcome := inspector.resolve(ctx, references[key].nodewright, references[key]._package)
		mu.Lock()
		outcomes[key] = outcome
		mu.Unlock()
		return outcome
	}
	var hosts sync.WaitGroup
	for _, keys := range byHost {
		hosts.Go(func() {
			// hanging is set once a resolution on the host times out. The bound only spares a host
			// that answers; one that takes the connection and never answers would cost a timeout for
			// each packageArtifactHostResolutions references, so the rest of its references go at
			// once. One that cannot be connected to has opened its window, which turns them away.
			var hanging atomic.Bool
			resolveOn := func(key string) {
				if outcome := resolve(key); outcome.Kind == packageArtifactUnavailable && outcome.ErrorClass == packageArtifactErrorTimeout {
					hanging.Store(true)
				}
			}
			if probe := slices.IndexFunc(keys, func(key string) bool { return inspector.asks(references[key]._package) }); probe >= 0 {
				resolveOn(keys[probe])
				keys = slices.Delete(slices.Clone(keys), probe, probe+1)
			}
			var rest sync.WaitGroup
			slots := make(chan struct{}, packageArtifactHostResolutions)
			for _, key := range keys {
				bounded := !hanging.Load()
				if bounded {
					slots <- struct{}{}
				}
				rest.Go(func() {
					if bounded {
						defer func() { <-slots }()
					}
					resolveOn(key)
				})
			}
			rest.Wait()
		})
	}
	hosts.Wait()
	return outcomes
}

// knownPackageArtifact is what the pass resolved of the artifact _package names, without resolving
// anything: disabled with the option off, and unresolved for a package the pass decides nothing for.
func (r *SkyhookReconciler) knownPackageArtifact(skyhook *wrapper.Skyhook, _package *v1alpha1.Package) packageArtifactOutcome {
	if !r.opts.PackageArtifacts || r.opts.artifactInspector == nil {
		return packageArtifactOutcome{Kind: packageArtifactDisabled}
	}
	if m, ok := skyhook.PackageArtifacts[packageArtifactKey(_package)]; ok {
		return outcomeOfMemo(m)
	}
	return packageArtifactOutcome{Kind: packageArtifactUnresolved}
}

// packageArtifactsHeld reports whether skyhook is paused or disabled, and not being deleted. It runs
// no stage then, so a pass of it resolves no package artifact, and so neither sets nor removes a
// condition it would decide from them: they stand as the last pass that ran left them.
func packageArtifactsHeld(skyhook *wrapper.Skyhook) bool {
	return (skyhook.IsPaused() || skyhook.IsDisabled()) && skyhook.DeletionTimestamp.IsZero()
}

// resolvedPackageArtifact is what the pass resolved of the artifact _package names, resolving it now
// when the pass has not, as when a pass starts the uninstall of a package it found nothing to run
// for. What it resolves is kept for the rest of the pass. Like resolvePackageArtifacts it resolves
// nothing for a NodeWright held (packageArtifactsHeld).
func (r *SkyhookReconciler) resolvedPackageArtifact(ctx context.Context, skyhook *wrapper.Skyhook, _package *v1alpha1.Package) packageArtifactOutcome {
	outcome := r.knownPackageArtifact(skyhook, _package)
	if outcome.Kind != packageArtifactUnresolved || packageArtifactsHeld(skyhook) {
		return outcome
	}
	outcome = r.opts.artifactInspector.resolve(ctx, skyhook.NodeWright, _package)
	if skyhook.PackageArtifacts != nil {
		skyhook.PackageArtifacts[packageArtifactKey(_package)] = outcome.memo()
	}
	return outcome
}

// Error classes of an unavailable or unreadable outcome.
const (
	packageArtifactErrorInvalidReference = "invalid-reference"
	packageArtifactErrorCanceled         = "canceled"
	packageArtifactErrorTimeout          = "timeout"
	packageArtifactErrorDigestMismatch   = "digest-mismatch"
	packageArtifactErrorUnauthorized     = "unauthorized"
	packageArtifactErrorNotFound         = "not-found"
	packageArtifactErrorTransport        = "transport"
	packageArtifactErrorTLS              = "tls"
	packageArtifactErrorPlainHTTP        = "plain-http"
	packageArtifactErrorNoSuchHost       = "no-such-host"
	packageArtifactErrorRefused          = "refused"
	packageArtifactErrorUnavailable      = "unavailable"
	packageArtifactErrorOther            = "error"
)

// packageArtifactTransient reports whether a failure of class passes by itself, so the package only
// waits for it. A refused credential, a missing tag, content that does not match its digest, a
// reference the operator rejects, a certificate it does not trust, a plain-HTTP registry, a host
// name that does not resolve and a request the registry refuses do not.
func packageArtifactTransient(class string) bool {
	switch class {
	case packageArtifactErrorTimeout, packageArtifactErrorTransport, packageArtifactErrorUnavailable,
		packageArtifactErrorOther, packageArtifactErrorCanceled:
		return true
	}
	return false
}

// eventActionLengthLimit is ActionLengthLimit of the events.k8s.io/v1 validation in kubernetes
// pkg/apis/core/validation/events.go: the apiserver rejects an Event with a longer action, and the
// Event is lost.
const eventActionLengthLimit = 128

// packageEventAction is the action of a Warning about the artifact of _package: its name:version,
// cut to eventActionLengthLimit bytes, while the note keeps it whole. The package is the action,
// not only the note, because the events.k8s.io broadcaster folds Events with the same type,
// reason, action, reporting controller and instance, regarding (which carries resourceVersion) and
// related into one series and never sends a later note (getKey and recordToSink in client-go
// tools/events/event_broadcaster.go): with a fixed action only the Warning of the first package
// would reach the API while the NodeWright is unchanged. The webhook bounds a name to 43
// characters, and names are unique within a NodeWright, so the cut never gives two of its packages
// one action. Repeats for one package, from any node, fold by design, so no note names a node.
func packageEventAction(_package *v1alpha1.Package) string {
	return trunstr(_package.Name+":"+_package.Version, eventActionLengthLimit)
}

// warnPackageArtifact records a Warning about the artifact of _package on nodewright, and logs it
// with the same fields, since the log keeps the note of a repeat the broadcaster folds.
func warnPackageArtifact(ctx context.Context, recorder events.EventRecorder, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package, reason, note string) {
	action := packageEventAction(_package)
	log.FromContext(ctx).Info("package artifact warning", "nodewright", nodewright.Name, "reason", reason, "action", action, "note", note)
	recorder.Eventf(nodewright, nil, corev1.EventTypeWarning, reason, action, "%s", note)
}

func (r *SkyhookReconciler) warnPackageArtifact(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package, reason, note string) {
	warnPackageArtifact(ctx, r.recorder, nodewright, _package, reason, note)
}

// stageJobRefused reports whether outcome leaves the package with no Job at stage until the package
// or its registry changes: when the artifact blocks it (packageArtifactBlocks) at every stage, and
// when an accepted artifact declares uninstall unsupported at uninstall.
func stageJobRefused(outcome packageArtifactOutcome, stage v1alpha1.Stage) bool {
	return packageArtifactBlocks(outcome) || uninstallUnsupported(outcome, stage)
}

func uninstallUnsupported(outcome packageArtifactOutcome, stage v1alpha1.Stage) bool {
	return stage == v1alpha1.StageUninstall && outcome.Kind == packageArtifactAccepted &&
		outcome.Uninstall == artifact.UninstallUnsupported
}

// stageJobWithheld reports whether outcome gives the package no Job at stage for now: when it
// refuses one, while the digest is pending inspection, and while the reference is unavailable.
func stageJobWithheld(outcome packageArtifactOutcome, stage v1alpha1.Stage) bool {
	return outcome.Kind == packageArtifactPending || outcome.Kind == packageArtifactUnavailable || stageJobRefused(outcome, stage)
}

// packageArtifactWithheld decides, at the stage ApplyPackage would run, whether _package gets no Job
// on skyhookNode for now, from the outcome the pass resolved for it. A package whose Job runs, or
// whose stage is an interrupt, is decided nothing for. IntrospectNode reports the node blocked by a
// package its artifact blocks, and the inspector why when it reads the reference.
func (r *SkyhookReconciler) packageArtifactWithheld(ctx context.Context, skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) (bool, error) {
	if !r.opts.PackageArtifacts || r.opts.artifactInspector == nil {
		return false, nil
	}
	needed, err := r.needsStageJob(ctx, skyhookNode, _package)
	if err != nil || !needed {
		return false, err
	}
	nodewright := skyhookNode.GetSkyhook()
	stage := applyPackageStage(skyhookNode, _package)
	outcome := r.resolvedPackageArtifact(ctx, nodewright, _package)
	if stageJobWithheld(outcome, stage) {
		log.FromContext(ctx).V(1).Info("package artifact withholds its Job", "nodewright", nodewright.Name,
			"package", _package.GetUniqueName(), "stage", stage, "kind", outcome.Kind)
		return true, nil
	}
	return false, nil
}

// knownStageJobRefused reports, from what the pass resolved, whether _package gets no Job on
// skyhookNode at the stage ApplyPackage would run until its spec or registry changes: never at a
// stage that runs no stage Job (buildsStageJob), such as an interrupt the node is in.
// HandleFinalizer asks it, so that a deletion that cannot finish is told from one still waiting.
func (r *SkyhookReconciler) knownStageJobRefused(skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) bool {
	if !buildsStageJob(skyhookNode, _package) {
		return false
	}
	stage := applyPackageStage(skyhookNode, _package)
	return stageJobRefused(r.knownPackageArtifact(skyhookNode.GetSkyhook(), _package), stage)
}

// knownStageJobWithheld reports, from what the pass resolved, whether _package gets no Job on
// skyhookNode for now at the stage ApplyPackage would run, as packageArtifactWithheld decides. The
// drain-blocked conditions ask it, so that they never wait on a drain RunSkyhookPackages will not
// start.
func (r *SkyhookReconciler) knownStageJobWithheld(skyhookNode wrapper.SkyhookNode, _package *v1alpha1.Package) bool {
	if !buildsStageJob(skyhookNode, _package) {
		return false
	}
	stage := applyPackageStage(skyhookNode, _package)
	return stageJobWithheld(r.knownPackageArtifact(skyhookNode.GetSkyhook(), _package), stage)
}

// listedCondition sets condition type on skyhook with reason and a message counting entries, as
// noun names them, and naming at most ReadyConditionNodeListLimit of them, followed by suffix;
// with no entries it removes the condition.
func listedCondition(skyhook *wrapper.Skyhook, conditionType, reason, noun string, entries []string, suffix string) {
	if len(entries) == 0 {
		wrapper.RemoveSkyhookConditionTypes(skyhook, conditionType)
		return
	}
	listed := entries[:min(len(entries), wrapper.ReadyConditionNodeListLimit)]
	message := fmt.Sprintf("%d %s: %s", len(entries), noun, strings.Join(listed, ", "))
	if rest := len(entries) - len(listed); rest > 0 {
		message += fmt.Sprintf(", and %d more", rest)
	}
	wrapper.AddSkyhookCondition(skyhook, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message + suffix,
		ObservedGeneration: skyhook.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

// updatePackageArtifactUnusableCondition sets the Blocked condition, with reason
// PackageArtifactUnusable, on skyhook while the pass found the artifact of a spec package unusable,
// rejected or not readable for a reason that does not pass by itself, and removes it once none is.
// Blocked is one condition with one reason: a DependencyUninstalled one, which UpdateBlockedCondition
// sets, keeps it, and this one replaces a NonInterruptPodsRunning one, which
// updateDrainBlockedCondition then leaves alone until this one is removed. A pass of a NodeWright held
// (packageArtifactsHeld) leaves it as it is.
func (r *SkyhookReconciler) updatePackageArtifactUnusableCondition(skyhook *wrapper.Skyhook) {
	if packageArtifactsHeld(skyhook) {
		return
	}
	var blocked []string
	for _, name := range slices.Sorted(maps.Keys(skyhook.Spec.Packages)) {
		_package := skyhook.Spec.Packages[name]
		outcome := r.knownPackageArtifact(skyhook, &_package)
		switch outcome.Kind {
		case packageArtifactRejected:
			blocked = append(blocked, fmt.Sprintf("%s:%s (%s)", _package.Name, _package.Version, outcome.Reason))
		case packageArtifactUnreadable:
			blocked = append(blocked, fmt.Sprintf("%s:%s (%s)", _package.Name, _package.Version, outcome.ErrorClass))
		}
	}
	existing := meta.FindStatusCondition(skyhook.Status.Conditions, wrapper.SkyhookConditionBlocked)
	switch {
	case existing != nil && existing.Reason != wrapper.SkyhookReasonPackageArtifactUnusable &&
		existing.Reason != wrapper.SkyhookReasonNonInterruptPodsRunning:
	case len(blocked) > 0:
		noun := "package blocked"
		if len(blocked) > 1 {
			noun = "packages blocked"
		}
		listedCondition(skyhook, wrapper.SkyhookConditionBlocked, wrapper.SkyhookReasonPackageArtifactUnusable, noun, blocked,
			". A package artifact in a private registry needs its pull Secret in imagePullSecrets.")
	case existing != nil && existing.Reason == wrapper.SkyhookReasonPackageArtifactUnusable:
		wrapper.RemoveSkyhookConditionTypeAndReason(skyhook, wrapper.SkyhookConditionBlocked, wrapper.SkyhookReasonPackageArtifactUnusable)
	}
}
