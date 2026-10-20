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
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	artifactclient "github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/artifact/preflight"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// preflightRejectedRetry is how long a preflight that rejected its package's artifact holds the
// package at (preflight, erroring) before preflight runs again, so a corrected artifact pushed
// under the same tag is picked up. It is the TTL of the rejecting Job, which holds the stage while
// it lasts, as a terminally failed Job holds its stage.
const preflightRejectedRetry = 5 * time.Minute

// packageReference returns the reference the preflight of _package inspects: its image at its
// containerSHA, or else at its version.
func packageReference(_package *v1alpha1.Package) (string, error) {
	ref, err := artifactclient.NewReference(_package.Image, _package.Version, _package.ContainerSHA)
	if err != nil {
		return "", fmt.Errorf("building the reference of package %s: %w", _package.GetUniqueName(), err)
	}
	return ref.String(), nil
}

// preflightResultFrom is the result recorded on a node for outcome.
func preflightResultFrom(outcome preflight.Outcome) *v1alpha1.PreflightResult {
	return &v1alpha1.PreflightResult{
		Kind:       string(outcome.Kind),
		Pinned:     outcome.Pinned,
		Uninstall:  string(outcome.Uninstall),
		Reason:     outcome.Reason,
		ErrorClass: outcome.ErrorClass,
	}
}

// preflightAccepted reports whether result accepted an artifact, which then runs every stage Job of
// its version on its node.
func preflightAccepted(result *v1alpha1.PreflightResult) bool {
	return result != nil && result.Kind == string(preflight.KindArtifact)
}

// preflightUninstallUnsupported reports whether result accepted an artifact that declares uninstall
// unsupported, whose uninstall is never started on its node.
func preflightUninstallUnsupported(result *v1alpha1.PreflightResult) bool {
	return preflightAccepted(result) && result.Uninstall == string(artifact.UninstallUnsupported)
}

// packagePreflight returns the preflight result the node recorded for _package, or nil when it has
// none, as for a package installed while PACKAGE_ARTIFACTS was off.
func packagePreflight(node wrapper.SkyhookNode, _package *v1alpha1.Package) *v1alpha1.PreflightResult {
	if status, found := node.PackageStatus(_package.GetUniqueName()); found {
		return status.Preflight
	}
	return nil
}

// packagePullSecrets returns the imagePullSecrets of the package ServiceAccount, in order. They are
// read each time a Job that needs them is built, so a change to the ServiceAccount applies to the
// next Job, as it does to the kubelet's pulls.
func (r *SkyhookReconciler) packagePullSecrets(ctx context.Context) ([]string, error) {
	var account corev1.ServiceAccount
	key := types.NamespacedName{Namespace: r.opts.Namespace, Name: r.opts.PackageServiceAccount}
	if err := r.Get(ctx, key, &account); err != nil {
		return nil, fmt.Errorf("reading the package ServiceAccount %s: %w", key, err)
	}
	names := make([]string, 0, len(account.ImagePullSecrets))
	for _, secret := range account.ImagePullSecrets {
		names = append(names, secret.Name)
	}
	return names, nil
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
func (r *SkyhookReconciler) warnPackageArtifact(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package, reason, note string) {
	action := packageEventAction(_package)
	log.FromContext(ctx).Info("package artifact warning", "nodewright", nodewright.Name, "reason", reason, "action", action, "note", note)
	r.recorder.Eventf(nodewright, nil, corev1.EventTypeWarning, reason, action, "%s", note)
}

// updatePackageArtifactRejectedCondition sets PackageArtifactRejected on the NodeWright from the
// preflight results its nodes recorded, listing each spec package some node rejected with the
// reason, and removes it when none is.
func updatePackageArtifactRejectedCondition(skyhook SkyhookNodes) {
	nodewright := skyhook.GetSkyhook()
	states := parsedNodeStates(skyhook)
	var rejected []string
	for _, name := range slices.Sorted(maps.Keys(nodewright.Spec.Packages)) {
		_package := nodewright.Spec.Packages[name]
		for _, state := range states {
			result := state[_package.GetUniqueName()].Preflight
			if result != nil && result.Kind == string(preflight.KindRejected) {
				rejected = append(rejected, fmt.Sprintf("%s:%s (%s)", _package.Name, _package.Version, result.Reason))
				break
			}
		}
	}
	if len(rejected) == 0 {
		wrapper.RemoveSkyhookConditionTypes(nodewright, wrapper.SkyhookConditionPackageArtifactRejected)
		return
	}

	noun := "package artifact"
	if len(rejected) > 1 {
		noun += "s"
	}
	listed := rejected[:min(len(rejected), wrapper.ReadyConditionNodeListLimit)]
	message := fmt.Sprintf("%d %s rejected: %s", len(rejected), noun, strings.Join(listed, ", "))
	if rest := len(rejected) - len(listed); rest > 0 {
		message += fmt.Sprintf(", and %d more", rest)
	}
	wrapper.AddSkyhookCondition(nodewright, metav1.Condition{
		Type:               wrapper.SkyhookConditionPackageArtifactRejected,
		Status:             metav1.ConditionTrue,
		Reason:             wrapper.SkyhookReasonArtifactRejected,
		Message:            message,
		ObservedGeneration: nodewright.Generation,
		LastTransitionTime: metav1.Now(),
	})
}
