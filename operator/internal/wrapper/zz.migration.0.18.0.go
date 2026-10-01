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

package wrapper

import (
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
)

// legacySkyhookMetadataPrefix is the metadata prefix the pre-rename operator used
// for every node annotation, node label, and node condition type. The operator now
// keys all of that under v1alpha1.METADATA_PREFIX ("nodewright.nvidia.com"). This
// literal is intentionally hardcoded rather than imported from the legacy api
// package: it is a one-shot migration constant (the value can never change), and
// wrapper otherwise depends only on the new api group.
const legacySkyhookMetadataPrefix = "skyhook.nvidia.com"

// MIGRATION-SHIM: transition-only for the skyhook.nvidia.com -> nodewright.nvidia.com
// rename. Delete this whole file (and its call in wrapper/node.go's Migrate) with the
// legacy skyhook.nvidia.com group in the removal release.
//
// migrateNodePrefixToNodeWright copies, in place, any node metadata still under the
// legacy skyhook.nvidia.com prefix to the current nodewright.nvidia.com prefix,
// KEEPING the legacy keys.
//
// WHY: after the Skyhook -> NodeWright rename the operator reads node state
// (nodeState_/status_/version_/cordon_/drainStart_/autoTaint_ annotations, status
// labels, and skyhook.nvidia.com/<name>/<type> conditions) under the NEW prefix. A
// node last written by the pre-rename operator carries only OLD-prefix keys, so
// without this copy the operator sees no state and re-runs every package on every
// node. It must run before GetVersion()/State() are trusted (both read the new
// prefix), so a renamed node is adopted instead of treated as fresh.
//
// WHY KEEP THE LEGACY KEYS: this is the reversible converge half of the rollback-safe
// migration. The legacy copies let a rolled-back pre-rename operator still find its
// state; pruneLegacyNodePrefix drops them once the rollback window (LegacyCleanupDelay)
// elapses. Idempotent: a key already present under the new prefix wins (explicit
// nodewright value is not overwritten); with no legacy keys present it is a no-op and
// does not mark the node updated. The bare runtime-required taint key
// ("skyhook.nvidia.com", no trailing slash, and a taint not an annotation/label)
// intentionally does not match and is left untouched: that key did not move in the
// rename.
// operatorOwnedAnnotationSuffixes are the node-annotation key shapes the operator
// writes under its own metadata prefix. Each is a prefix of the part AFTER
// "<group>/": nodeState_<name>, autoTaint_<taintKey>, and so on.
//
// WHY a list instead of sweeping the whole prefix: the prefix is the product's
// domain name, so users legitimately put their OWN keys under it (a
// skyhook.nvidia.com/pool node label feeding a nodeSelector, say). Those are not
// the operator's to migrate. Copying them would duplicate user data into the
// operator's namespace unasked, and pruning them later would silently delete a
// label the user still depends on, a day after an unrelated upgrade.
// nameScopedAnnotationPrefixes are the per-skyhook annotation kinds: the full suffix
// is "<kind><skyhookName>", so exactly one NodeWright owns each of these keys.
var nameScopedAnnotationPrefixes = []string{
	"nodeState_",
	statusPrefix,
	"version_",
	"cordon_",
	"drainStart_",
}

// nodeScopedAnnotationPrefixes are operator-owned but belong to NO single skyhook:
// autoTaint_<taintKey> is keyed by the runtime-required taint, not by a skyhook name.
var nodeScopedAnnotationPrefixes = []string{
	"autoTaint_",
}

// ignoreLabel is user-SET but operator-DEFINED (CheckNodeIgnoreLabel reads it),
// so it must be copied with the rename or a user's opt-out silently stops
// working. It is node-scoped: no single skyhook owns it.
const ignoreLabel = "ignore"

// isOperatorOwnedAnnotation / isOperatorOwnedLabel decide what the CONVERGE copies to
// the new prefix: everything the operator wrote, whether or not it belongs to one
// skyhook, since the post-rename operator reads all of it under the new prefix.
func isOperatorOwnedAnnotation(suffix string) bool {
	for _, p := range append(append([]string{}, nameScopedAnnotationPrefixes...), nodeScopedAnnotationPrefixes...) {
		if strings.HasPrefix(suffix, p) {
			return true
		}
	}
	return false
}

func isOperatorOwnedLabel(suffix string) bool {
	return suffix == ignoreLabel || strings.HasPrefix(suffix, statusPrefix)
}

// The ownedBy* predicates decide what the PRUNE deletes, and are deliberately
// narrower than the isOperatorOwned* pair above.
//
// WHY: the prune runs per NodeWright, once that object's own rollback window has
// elapsed, but the keys live on a shared Node. Deleting every operator-owned legacy
// key would destroy the rollback state of every OTHER NodeWright selecting that node,
// including ones still inside their window. Only keys this skyhook owns are its to
// remove.
//
// Node-scoped keys (autoTaint_, ignore) are therefore never pruned here: no single
// NodeWright owns them, so none may decide they are finished with. That also means the
// migration never destroys the user-set ignore label, only copies it forward.
func ownedByAnnotation(skyhookName string) func(string) bool {
	return func(suffix string) bool {
		for _, p := range nameScopedAnnotationPrefixes {
			if suffix == p+skyhookName {
				return true
			}
		}
		return false
	}
}

func ownedByLabel(skyhookName string) func(string) bool {
	return func(suffix string) bool { return suffix == statusPrefix+skyhookName }
}

// ownedByCondition matches the condition types UpdateCondition writes, whose suffix is
// "<skyhookName>/<Type>". It reuses the constants from node.go rather than repeating
// the literals so a new condition type cannot be added there while silently dropping
// out of the migration here.
func ownedByCondition(skyhookName string) func(string) bool {
	return func(suffix string) bool {
		return suffix == skyhookName+"/"+conditionTypeNotReady ||
			suffix == skyhookName+"/"+conditionTypeErroring
	}
}

// isOperatorOwnedCondition is the converge-side counterpart: any skyhook's condition.
func isOperatorOwnedCondition(suffix string) bool {
	return strings.HasSuffix(suffix, "/"+conditionTypeNotReady) ||
		strings.HasSuffix(suffix, "/"+conditionTypeErroring)
}

// HasOperatorOwnedLegacyMetadata reports whether the node still carries legacy
// skyhook.nvidia.com metadata that the operator owns FOR THE NAMED SKYHOOK.
//
// The controller uses this to decide the rollback-window stamp, which is per
// NodeWright. A whole-prefix scan would answer a different question: it returns true
// for a user's own skyhook.nvidia.com/* key, which the prune deliberately preserves,
// so the stamp could never clear and would be set on clusters that never ran the
// pre-rename operator at all.
func HasOperatorOwnedLegacyMetadata(n *corev1.Node, skyhookName string) bool {
	oldPrefix := legacySkyhookMetadataPrefix + "/"
	ownsAnnotation, ownsLabel := ownedByAnnotation(skyhookName), ownedByLabel(skyhookName)
	ownsCondition := ownedByCondition(skyhookName)

	for k := range n.Annotations {
		if suffix, ok := strings.CutPrefix(k, oldPrefix); ok && ownsAnnotation(suffix) {
			return true
		}
	}
	for k := range n.Labels {
		if suffix, ok := strings.CutPrefix(k, oldPrefix); ok && ownsLabel(suffix) {
			return true
		}
	}
	for _, c := range n.Status.Conditions {
		if suffix, ok := strings.CutPrefix(string(c.Type), oldPrefix); ok && ownsCondition(suffix) {
			return true
		}
	}
	return false
}

func migrateNodePrefixToNodeWright(node *skyhookNode, logger logr.Logger) error {
	oldPrefix := legacySkyhookMetadataPrefix + "/"
	newPrefix := v1alpha1.METADATA_PREFIX + "/"

	changed := false
	copyPrefixedMap(node.Annotations, oldPrefix, newPrefix, isOperatorOwnedAnnotation, &changed)
	copyPrefixedMap(node.Labels, oldPrefix, newPrefix, isOperatorOwnedLabel, &changed)

	// Add a nodewright-prefixed copy of each legacy condition, keeping the legacy one.
	conditions := node.GetNode().Status.Conditions
	var added []corev1.NodeCondition
	for i := range conditions {
		suffix, ok := strings.CutPrefix(string(conditions[i].Type), oldPrefix)
		if !ok || !isOperatorOwnedCondition(suffix) {
			continue
		}
		newType := corev1.NodeConditionType(newPrefix + suffix)
		if hasCondition(conditions, newType) {
			continue
		}
		c := conditions[i]
		c.Type = newType
		added = append(added, c)
		changed = true
	}
	if len(added) > 0 {
		node.GetNode().Status.Conditions = append(conditions, added...)
	}

	if changed {
		node.updated = true
		logger.Info("adopted legacy node metadata under the nodewright prefix (legacy kept for rollback)", "node", node.Name, "skyhook", node.skyhookName)
	}
	return nil
}

// pruneLegacyNodePrefix removes, in place, all node metadata still under the legacy
// skyhook.nvidia.com prefix (annotations, labels, and conditions). This is the
// destructive prune half of the rollback-safe migration: it runs only after the
// rollback window elapses. The nodewright-prefixed copies written by the converge are
// the live state and are left untouched. Returns true if anything was removed.
func pruneLegacyNodePrefix(node *skyhookNode) bool {
	oldPrefix := legacySkyhookMetadataPrefix + "/"

	// Scoped to THIS skyhook's keys: see the ownedBy* comment. A node is shared, and
	// the prune decision is per NodeWright.
	changed := false
	dropPrefixedKeys(node.Annotations, oldPrefix, ownedByAnnotation(node.skyhookName), &changed)
	dropPrefixedKeys(node.Labels, oldPrefix, ownedByLabel(node.skyhookName), &changed)

	ownsCondition := ownedByCondition(node.skyhookName)
	conditions := node.GetNode().Status.Conditions
	kept := conditions[:0]
	for _, c := range conditions {
		if suffix, ok := strings.CutPrefix(string(c.Type), oldPrefix); ok && ownsCondition(suffix) {
			changed = true
			continue
		}
		kept = append(kept, c)
	}
	node.GetNode().Status.Conditions = kept

	if changed {
		node.updated = true
	}
	return changed
}

// copyPrefixedMap copies every OPERATOR-OWNED key in m from oldPrefix to newPrefix,
// KEEPING the legacy key. isOwned decides ownership from the part after oldPrefix;
// keys it rejects are the user's and are left untouched. A key already present under
// newPrefix is left as-is (explicit new value wins). Sets *changed when it added a
// new-prefix key. A nil map is a no-op.
func copyPrefixedMap(m map[string]string, oldPrefix, newPrefix string, isOwned func(suffix string) bool, changed *bool) {
	// Collect legacy keys first so we do not mutate the map while ranging it.
	var legacy []string
	for k := range m {
		if suffix, ok := strings.CutPrefix(k, oldPrefix); ok && isOwned(suffix) {
			legacy = append(legacy, k)
		}
	}
	for _, k := range legacy {
		newKey := newPrefix + strings.TrimPrefix(k, oldPrefix)
		if _, exists := m[newKey]; !exists {
			m[newKey] = m[k]
			*changed = true
		}
	}
}

// dropPrefixedKeys deletes every OPERATOR-OWNED key in m under prefix, in place.
// Keys isOwned rejects belong to the user and are preserved: the prune must not
// delete metadata the operator never created. Sets *changed when anything was
// deleted. A nil map is a no-op.
func dropPrefixedKeys(m map[string]string, prefix string, isOwned func(suffix string) bool, changed *bool) {
	for k := range m {
		if suffix, ok := strings.CutPrefix(k, prefix); ok && isOwned(suffix) {
			delete(m, k)
			*changed = true
		}
	}
}

// hasCondition reports whether conditions already carries a condition of type t.
func hasCondition(conditions []corev1.NodeCondition, t corev1.NodeConditionType) bool {
	for i := range conditions {
		if conditions[i].Type == t {
			return true
		}
	}
	return false
}
