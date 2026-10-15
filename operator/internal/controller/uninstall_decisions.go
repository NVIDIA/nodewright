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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// uninstallUnsupportedRemedy ends the UninstallUnsupported Warning and condition.
const uninstallUnsupportedRemedy = "Set uninstall.apply or uninstall.enabled to false, or point the package at an artifact that declares uninstall supported."

// uninstallDecisionFor maps what the operator knows of the artifact of a package to what its
// uninstall may do. An accepted artifact declaring uninstall unsupported starts none, and one that
// is rejected or could not be inspected starts none until it is accepted; anything else runs as the
// spec asks.
func uninstallDecisionFor(outcome packageArtifactOutcome) wrapper.UninstallDecision {
	switch outcome.Kind {
	case packageArtifactAccepted:
		if outcome.Package.Uninstall == artifact.UninstallUnsupported {
			return wrapper.UninstallDecisionUnsupported
		}
	case packageArtifactRejected, packageArtifactUnknown:
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

// resolveUninstallDecisions decides, first in a pass, what the artifact of each package lets its
// uninstall do, and sets the decisions on skyhook for the rest of the pass. Only a package that
// some node still holds, and whose uninstall is requested or under way there, is decided, so no
// other one, such as one whose uninstall finished on every node, causes a registry request. A
// requested uninstall a decision holds back is reported with a Warning.
//
// The result requeues the pass so that a re-pushed tag, or a registry that answers again, is
// picked up: within packageArtifactTagTTL while UninstallUnsupported lists a package, and within
// packageArtifactFailureBackoff while a deferred package is held back. It never stops the pass.
func (r *SkyhookReconciler) resolveUninstallDecisions(ctx context.Context, skyhook SkyhookNodes) *ctrl.Result {
	nodewright := skyhook.GetSkyhook()
	states := parsedNodeStates(skyhook)
	nodewright.UninstallDecisions = map[string]wrapper.UninstallDecision{}
	deferredHeldBack := false
	for _, name := range slices.Sorted(maps.Keys(nodewright.Spec.Packages)) {
		_package := nodewright.Spec.Packages[name]
		held, inCycle := false, false
		for _, state := range states {
			_, holds := state[_package.GetUniqueName()]
			held = held || holds
			inCycle = inCycle || state.IsUninstallCycleInProgress(_package.GetUniqueName())
		}
		if !inCycle && (!held || !uninstallRequested(nodewright, &_package)) {
			continue
		}
		outcome := r.packageArtifactOutcome(ctx, nodewright.NodeWright, &_package)
		decision := uninstallDecisionFor(outcome)
		nodewright.UninstallDecisions[name] = decision
		if !uninstallHeldBack(nodewright, &_package, states) {
			continue
		}

		if decision == wrapper.UninstallDecisionUnsupported {
			_, digest, _ := strings.Cut(outcome.Pinned, "@")
			r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonUninstallUnsupported,
				fmt.Sprintf("package [%s:%s] artifact [%s] declares uninstall unsupported, so its uninstall is not started. %s",
					_package.Name, _package.Version, digest, uninstallUnsupportedRemedy))
			continue
		}
		deferredHeldBack = true
		problem := fmt.Sprintf("could not be inspected (%s)", outcome.ErrorClass)
		if outcome.Kind == packageArtifactRejected {
			problem = fmt.Sprintf("is rejected (%s)", outcome.Reason)
		}
		r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonUninstallDeferred,
			fmt.Sprintf("package [%s:%s] artifact [%s] %s, so its uninstall is deferred. Give the operator access to the registry "+
				"through its image pull Secret (IMAGE_PULL_SECRET), set uninstall.enabled to false, or turn PACKAGE_ARTIFACTS off.",
				_package.Name, _package.Version, getPackageImage(&_package), problem))
	}

	var result *ctrl.Result
	if len(uninstallUnsupportedListed(nodewright, states)) > 0 {
		result = &ctrl.Result{RequeueAfter: packageArtifactTagTTL}
	}
	if deferredHeldBack {
		result = soonerRequeue(result, &ctrl.Result{RequeueAfter: packageArtifactFailureBackoff})
	}
	return result
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
		stalled := slices.ContainsFunc(states, func(state v1alpha1.NodeState) bool {
			return state[_package.GetUniqueName()].Stage == v1alpha1.StageUninstall
		})
		switch {
		case stalled:
			listed = append(listed, fmt.Sprintf("%s:%s (stalled)", _package.Name, _package.Version))
		case uninstallHeldBack(skyhook, &_package, states):
			listed = append(listed, fmt.Sprintf("%s:%s (not started)", _package.Name, _package.Version))
		}
	}
	return listed
}

// updateUninstallUnsupportedCondition sets UninstallUnsupported on the NodeWright from the
// decisions of the pass, its spec and the state of its nodes, never asking a registry, and removes
// it when it lists nothing: once uninstall.apply or uninstall.enabled is false, the artifact
// declares uninstall supported, or the option is off.
func updateUninstallUnsupportedCondition(skyhook SkyhookNodes) {
	nodewright := skyhook.GetSkyhook()
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
