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

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// uninstallUnsupportedRemedy ends the UninstallUnsupported Warning and condition.
const uninstallUnsupportedRemedy = "Set uninstall.apply or uninstall.enabled to false, or upgrade the package to a version whose artifact declares uninstall supported."

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

// uninstallHeldBackOn reports whether the node whose state is state holds back an uninstall of
// _package: it holds the package outside its uninstall cycle, installed from an artifact that its
// preflight recorded as declaring uninstall unsupported. The node never starts that uninstall,
// whether or not one is requested. An entry still at the preflight stage is not held back:
// HandleUninstallRequests removes it, as one with nothing installed, or, when preflight ran again
// after the version's install started, waits for that install to complete.
func uninstallHeldBackOn(state v1alpha1.NodeState, _package *v1alpha1.Package) bool {
	status, held := state[_package.GetUniqueName()]
	return held && status.Stage != v1alpha1.StagePreflight && !state.IsUninstallCycleInProgress(_package.GetUniqueName()) &&
		preflightUninstallUnsupported(status.Preflight)
}

// resolveUninstallDecisions sets, first in a pass, the uninstall decisions of the pass on skyhook
// from the preflight results its nodes recorded: unsupported for a package that some node holds
// outside its uninstall cycle when every such node holds it back (uninstallHeldBackOn), so that no
// node can start its uninstall, and spec otherwise. HandleUninstallRequests and deletionWaitsOn
// decide each node from its own entry; the decisions are what the readers that see a package
// across nodes, HasUninstallWork and UpdateBlockedCondition, go by. A requested uninstall some node
// holds back is reported with a Warning once, in the pass before the UninstallUnsupported
// condition lists it (see uninstallUnsupportedAnnounced).
func (r *SkyhookReconciler) resolveUninstallDecisions(ctx context.Context, skyhook SkyhookNodes) {
	nodewright := skyhook.GetSkyhook()
	states := parsedNodeStates(skyhook)
	nodewright.UninstallDecisions = map[string]wrapper.UninstallDecision{}
	for _, name := range slices.Sorted(maps.Keys(nodewright.Spec.Packages)) {
		_package := nodewright.Spec.Packages[name]
		held, heldBack := 0, 0
		for _, state := range states {
			if _, holds := state[_package.GetUniqueName()]; !holds || state.IsUninstallCycleInProgress(_package.GetUniqueName()) {
				continue
			}
			held++
			if uninstallHeldBackOn(state, &_package) {
				heldBack++
			}
		}
		if held > 0 && heldBack == held {
			nodewright.UninstallDecisions[name] = wrapper.UninstallDecisionUnsupported
		}
		if heldBack > 0 && uninstallRequested(nodewright, &_package) && !uninstallUnsupportedAnnounced(nodewright, &_package) {
			r.warnPackageArtifact(ctx, nodewright.NodeWright, &_package, EventsReasonUninstallUnsupported,
				fmt.Sprintf("package [%s:%s] was installed from an artifact that declares uninstall unsupported, so its uninstall "+
					"is not started on the nodes that hold that artifact. %s", _package.Name, _package.Version, uninstallUnsupportedRemedy))
		}
	}
}

// uninstallUnsupportedAnnounced reports whether the UninstallUnsupported condition an earlier pass
// left on the NodeWright names _package, so its Warning was recorded then. The condition is what
// persists across passes and operator restarts; the event series does not fold a repeat once the
// NodeWright's resourceVersion has changed. Past ReadyConditionNodeListLimit the condition only
// counts the packages, so the Warning of those repeats each pass.
func uninstallUnsupportedAnnounced(nodewright *wrapper.Skyhook, _package *v1alpha1.Package) bool {
	condition := meta.FindStatusCondition(nodewright.Status.Conditions, wrapper.SkyhookConditionUninstallUnsupported)
	if condition == nil {
		return false
	}
	_, named, _ := strings.Cut(condition.Message, ": ")
	named, _, _ = strings.Cut(named, ". ")
	return slices.Contains(strings.Split(named, ", "), fmt.Sprintf("%s:%s", _package.Name, _package.Version))
}

// uninstallUnsupportedListed returns, sorted by name, the packages UninstallUnsupported lists:
// each whose requested uninstall some node holds back.
func uninstallUnsupportedListed(skyhook *wrapper.Skyhook, states []v1alpha1.NodeState) []string {
	var listed []string
	for _, name := range slices.Sorted(maps.Keys(skyhook.Spec.Packages)) {
		_package := skyhook.Spec.Packages[name]
		if !uninstallRequested(skyhook, &_package) {
			continue
		}
		if slices.ContainsFunc(states, func(state v1alpha1.NodeState) bool { return uninstallHeldBackOn(state, &_package) }) {
			listed = append(listed, fmt.Sprintf("%s:%s", _package.Name, _package.Version))
		}
	}
	return listed
}

// updateUninstallUnsupportedCondition sets UninstallUnsupported on the NodeWright from its spec and
// the state of its nodes, and removes it when it lists nothing: once uninstall.apply or
// uninstall.enabled is false, or no node holds a package installed from an artifact that declares
// uninstall unsupported.
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
