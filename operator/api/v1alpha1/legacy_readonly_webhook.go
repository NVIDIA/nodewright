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

package v1alpha1

import (
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
// rename. Delete this file with the legacy group at the removal release.
//
// Once the post-rename operator is running, a legacy Skyhook/DeploymentPolicy is a
// frozen, deprecated snapshot; its mirrored nodewright.nvidia.com object is the single
// writable source of truth. These helpers make the legacy admission webhooks reject any
// user-meaningful mutation so operators and GitOps are pushed onto the new object,
// while still allowing deletions, finalizer edits, and no-op re-applies (so a
// steady-state GitOps sync does not break; only a real edit is rejected).
//
// These helpers must never be copied into the nodewright group. They exist to make one
// group read-only, and the group they are compiled into is the one they freeze: a copy in
// the nodewright package would make NodeWright reject its own writes, turning the single
// writable source of truth into an object nobody can edit. Nothing at build time would
// catch that, because the copy compiles perfectly well; it only surfaces as a rejected
// admission request in a live cluster.

// legacyReadOnlyError rejects a spec- or pause/disable-changing update to a migrated
// legacy Skyhook. It returns nil (allow) for creates (oldSkyhook nil), for objects being
// deleted, and for updates that change nothing user-meaningful (finalizer edits, status,
// incidental metadata, identical re-applies).
func legacyReadOnlyError(oldSkyhook, newSkyhook *Skyhook) error {
	if oldSkyhook == nil || !newSkyhook.GetDeletionTimestamp().IsZero() {
		return nil
	}
	pauseKey := fmt.Sprintf("%s/pause", METADATA_PREFIX)
	disableKey := fmt.Sprintf("%s/disable", METADATA_PREFIX)
	// Semantic (not reflect) equality: the spec carries resource.Quantity fields, which
	// reflect.DeepEqual compares by internal representation and so can report a
	// re-serialized-but-equal quantity as changed, falsely rejecting a GitOps no-op.
	unchanged := apiequality.Semantic.DeepEqual(oldSkyhook.Spec, newSkyhook.Spec) &&
		oldSkyhook.Annotations[pauseKey] == newSkyhook.Annotations[pauseKey] &&
		oldSkyhook.Annotations[disableKey] == newSkyhook.Annotations[disableKey]
	if unchanged {
		return nil
	}
	return fmt.Errorf(
		"skyhook.nvidia.com Skyhook %q is migrated to NodeWright and is read-only; "+
			"edit NodeWright %q instead (e.g. kubectl nodewright pause/disable/... or its spec)",
		newSkyhook.Name, newSkyhook.Name)
}

// legacyDeploymentPolicyReadOnlyError rejects a spec-changing update to a migrated legacy
// DeploymentPolicy, with the same allow-list as legacyReadOnlyError.
func legacyDeploymentPolicyReadOnlyError(oldDeploymentPolicy, newDeploymentPolicy *DeploymentPolicy) error {
	if oldDeploymentPolicy == nil || !newDeploymentPolicy.GetDeletionTimestamp().IsZero() {
		return nil
	}
	if apiequality.Semantic.DeepEqual(oldDeploymentPolicy.Spec, newDeploymentPolicy.Spec) {
		return nil
	}
	return fmt.Errorf(
		"skyhook.nvidia.com DeploymentPolicy %q is migrated to the nodewright.nvidia.com group and is "+
			"read-only; edit the nodewright.nvidia.com DeploymentPolicy %q instead",
		newDeploymentPolicy.Name, newDeploymentPolicy.Name)
}
