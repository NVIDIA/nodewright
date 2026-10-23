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

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// uninstallUnsupportedRemedy ends the UninstallUnsupported Warning and condition.
const uninstallUnsupportedRemedy = "Set uninstall.apply or uninstall.enabled to false, or point the package at an artifact that declares uninstall supported."

// uninstallDecisionFor maps what the operator knows of the artifact of a package to what its
// uninstall may do. An accepted artifact declaring uninstall unsupported starts none; one that is
// rejected, pending inspection, or whose reference could not be read starts none until it is
// accepted; an image, an artifact declaring uninstall supported, and anything with the option off
// run as the spec asks.
func uninstallDecisionFor(outcome packageArtifactOutcome) wrapper.UninstallDecision {
	switch outcome.Kind {
	case packageArtifactAccepted:
		if outcome.Uninstall == artifact.UninstallUnsupported {
			return wrapper.UninstallDecisionUnsupported
		}
	case packageArtifactRejected, packageArtifactUnreadable, packageArtifactPending, packageArtifactUnavailable:
		return wrapper.UninstallDecisionDeferred
	}
	return wrapper.UninstallDecisionSpec
}

// uninstallRequested is the needsUninstall of HandleUninstallRequests: uninstall.apply, or the
// deletion of the NodeWright of an uninstall.enabled package.
func uninstallRequested(skyhook *wrapper.Skyhook, _package *v1alpha1.Package) bool {
	return _package.IsUninstalling() || (!skyhook.DeletionTimestamp.IsZero() && _package.UninstallEnabled())
}

// parsedNodeStates returns the state of every node whose state parses. A node whose state does
// not is left out, as NodeStateMalformed reports it.
func parsedNodeStates(skyhook SkyhookNodes) []v1alpha1.NodeState {
	states := make([]v1alpha1.NodeState, 0, len(skyhook.GetNodes()))
	for _, node := range skyhook.GetNodes() {
		if state, err := node.State(); err == nil {
			states = append(states, state)
		}
	}
	return states
}

// uninstallHeldBack reports whether the decision for _package holds back its requested uninstall
// on a node that holds the package outside its uninstall cycle.
func uninstallHeldBack(skyhook *wrapper.Skyhook, _package *v1alpha1.Package, states []v1alpha1.NodeState) bool {
	if !uninstallRequested(skyhook, _package) || skyhook.UninstallDecision(_package.Name) == wrapper.UninstallDecisionSpec {
		return false
	}
	return slices.ContainsFunc(states, func(state v1alpha1.NodeState) bool {
		_, held := state[_package.GetUniqueName()]
		return held && !state.IsUninstallCycleInProgress(_package.GetUniqueName())
	})
}

// uninstallUnderDecision reports whether the uninstall of _package is decided in a pass: whether
// some node has it in its uninstall cycle, or holds it while its uninstall is requested.
func uninstallUnderDecision(skyhook *wrapper.Skyhook, _package *v1alpha1.Package, states []v1alpha1.NodeState) bool {
	held, inCycle := false, false
	for _, state := range states {
		_, holds := state[_package.GetUniqueName()]
		held = held || holds
		inCycle = inCycle || state.IsUninstallCycleInProgress(_package.GetUniqueName())
	}
	return inCycle || (held && uninstallRequested(skyhook, _package))
}

// resolveUninstallDecisions decides, first in a pass and after resolvePackageArtifacts, what the
// artifact the pass resolved for each package lets its uninstall do, and sets the decisions on
// skyhook for the rest of the pass. Only a package uninstallUnderDecision is decided. An uninstall a
// decision holds back is reported with a Warning: an unsupported one, not started or stalled at
// StageUninstall, until the UninstallUnsupported condition lists it
// (uninstallUnsupportedAnnounced), and a requested one whose artifact blocks the package on every
// pass. One pending inspection, or a reference that is unavailable, is not reported: the inspector
// reports a reference it could not read, and the next pass decides it again.
func (r *SkyhookReconciler) resolveUninstallDecisions(ctx context.Context, skyhook SkyhookNodes) {
	nodewright := skyhook.GetSkyhook()
	states := parsedNodeStates(skyhook)
	nodewright.UninstallDecisions = map[string]wrapper.UninstallDecision{}
	for _, name := range slices.Sorted(maps.Keys(nodewright.Spec.Packages)) {
		_package := nodewright.Spec.Packages[name]
		if !uninstallUnderDecision(nodewright, &_package, states) {
			continue
		}
		outcome := r.resolvedPackageArtifact(ctx, nodewright, &_package)
		decision := uninstallDecisionFor(outcome)
		nodewright.UninstallDecisions[name] = decision
		stalled := uninstallStalled(&_package, states)
		heldBack := uninstallHeldBack(nodewright, &_package, states)

		switch {
		case decision == wrapper.UninstallDecisionUnsupported && (stalled || heldBack):
			if uninstallUnsupportedAnnounced(nodewright, &_package) {
				continue
			}
			_, digest, _ := strings.Cut(outcome.Pinned, "@")
			if stalled {
				r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonPackageArtifactUninstallUnsupported,
					fmt.Sprintf("package [%s:%s] artifact [%s] declares uninstall unsupported, so its uninstall stage is not run",
						_package.Name, _package.Version, digest))
			}
			if heldBack {
				r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonUninstallUnsupported,
					fmt.Sprintf("package [%s:%s] artifact [%s] declares uninstall unsupported, so its uninstall is not started. %s",
						_package.Name, _package.Version, digest, uninstallUnsupportedRemedy))
			}
		case outcome.Kind == packageArtifactRejected && heldBack:
			r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonUninstallDeferred,
				fmt.Sprintf("package [%s:%s] artifact [%s] is rejected (%s), so its uninstall is deferred. Push a package "+
					"artifact this operator runs under the reference, or set uninstall.enabled to false.",
					_package.Name, _package.Version, getPackageImage(&_package), outcome.Reason))
		case outcome.Kind == packageArtifactUnreadable && heldBack:
			r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonUninstallDeferred,
				fmt.Sprintf("package [%s:%s] reference [%s] could not be read (%s), so its uninstall is deferred. Give the "+
					"operator a pull Secret that reads it, or set uninstall.enabled to false.",
					_package.Name, _package.Version, getPackageImage(&_package), outcome.ErrorClass))
		}
	}
}

// uninstallUnsupportedAnnounced reports whether the UninstallUnsupported condition an earlier pass
// left on the NodeWright names _package, so its Warnings were recorded then. It reads the
// condition as persisted only before refreshSkyhookConditions, which lists the package in memory
// for this pass. The condition is what persists across passes and operator restarts; the event
// series does not fold a repeat once the NodeWright's resourceVersion has changed. Past
// ReadyConditionNodeListLimit the condition only counts the packages, so the Warnings of those
// repeat each pass.
func uninstallUnsupportedAnnounced(nodewright *wrapper.Skyhook, _package *v1alpha1.Package) bool {
	condition := meta.FindStatusCondition(nodewright.Status.Conditions, wrapper.SkyhookConditionUninstallUnsupported)
	if condition == nil {
		return false
	}
	_, named, _ := strings.Cut(condition.Message, ": ")
	named, _, _ = strings.Cut(named, ". ")
	return slices.ContainsFunc(strings.Split(named, ", "), func(entry string) bool {
		entry, _, _ = strings.Cut(entry, " (")
		return entry == fmt.Sprintf("%s:%s", _package.Name, _package.Version)
	})
}

// uninstallUnsupportedListed returns, sorted by name, the packages UninstallUnsupported lists:
// each whose decision is unsupported, as stalled when a node has it at StageUninstall, where its
// uninstall gets no Job, or else as not started when its uninstall is held back.
func uninstallUnsupportedListed(skyhook *wrapper.Skyhook, states []v1alpha1.NodeState) []string {
	var listed []string
	for _, name := range slices.Sorted(maps.Keys(skyhook.Spec.Packages)) {
		if skyhook.UninstallDecision(name) != wrapper.UninstallDecisionUnsupported {
			continue
		}
		_package := skyhook.Spec.Packages[name]
		switch {
		case uninstallStalled(&_package, states):
			listed = append(listed, fmt.Sprintf("%s:%s (stalled)", _package.Name, _package.Version))
		case uninstallHeldBack(skyhook, &_package, states):
			listed = append(listed, fmt.Sprintf("%s:%s (not started)", _package.Name, _package.Version))
		}
	}
	return listed
}

// uninstallStalled reports whether a node has _package at StageUninstall, where an uninstall its
// decision refuses gets no Job.
func uninstallStalled(_package *v1alpha1.Package, states []v1alpha1.NodeState) bool {
	return slices.ContainsFunc(states, func(state v1alpha1.NodeState) bool {
		return state[_package.GetUniqueName()].Stage == v1alpha1.StageUninstall
	})
}

// updateUninstallUnsupportedCondition sets UninstallUnsupported on the NodeWright from the
// decisions of the pass, its spec and the state of its nodes, and removes it when it lists nothing:
// once uninstall.apply or uninstall.enabled is false, or the artifact the pass resolved for the
// package's reference declares uninstall supported. A pass of a NodeWright held
// (packageArtifactsHeld) leaves it as it is.
func updateUninstallUnsupportedCondition(skyhook SkyhookNodes) {
	nodewright := skyhook.GetSkyhook()
	if packageArtifactsHeld(nodewright) {
		return
	}
	listed := uninstallUnsupportedListed(nodewright, parsedNodeStates(skyhook))
	if len(listed) == 0 {
		wrapper.RemoveSkyhookConditionTypes(nodewright, wrapper.SkyhookConditionUninstallUnsupported)
		return
	}

	declares := "package artifact declares"
	if len(listed) > 1 {
		declares = "package artifacts declare"
	}
	named := listed[:min(len(listed), wrapper.ReadyConditionNodeListLimit)]
	message := fmt.Sprintf("%d %s uninstall unsupported: %s", len(listed), declares, strings.Join(named, ", "))
	if rest := len(listed) - len(named); rest > 0 {
		message += fmt.Sprintf(", and %d more", rest)
	}
	wrapper.AddSkyhookCondition(nodewright, metav1.Condition{
		Type:               wrapper.SkyhookConditionUninstallUnsupported,
		Status:             metav1.ConditionTrue,
		Reason:             wrapper.SkyhookReasonArtifactUninstallUnsupported,
		Message:            message + ". " + uninstallUnsupportedRemedy,
		ObservedGeneration: nodewright.Generation,
		LastTransitionTime: metav1.Now(),
	})
}
