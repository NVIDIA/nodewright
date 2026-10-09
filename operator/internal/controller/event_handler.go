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
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// globalReconcileKey is the single sentinel request that every relevant Skyhook
// and Node event collapses onto. The heavy reconcile path ignores req identity
// (it grabs the whole world), so routing all such events to one key lets the
// workqueue dedup a burst into one pass; paired with MaxConcurrentReconciles: 1
// it also guarantees only one heavy reconcile runs at a time. The Name is an
// arbitrary sentinel (see globalReconcileName) — not a namespace or object
// reference.
var globalReconcileKey = reconcile.Request{
	NamespacedName: types.NamespacedName{Name: globalReconcileName},
}

// globalDelayHandler enqueues globalReconcileKey after a fixed delay on any
// watched event that is relevant to at least one Skyhook. A Skyhook event is
// always relevant; a Node event is relevant only if some Skyhook's node
// selector matches it — without that filter every kubelet heartbeat across the
// cluster would wake the heavy reconcile. Job events are not routed here at all:
// JobReconciler owns them, and the node-state write it makes is itself a Node event.
//
// We enqueue via AddAfter rather than a plain EnqueueRequestsFromMapFunc because
// the controller's priority queue only applies a delay on AddAfter/AddRateLimited:
// a plain Add lands the item in the ready tree immediately, leaving no window for
// the write storm a single pass produces across many Nodes to coalesce into one
// follow-up pass. The delay is that coalescing window.
type globalDelayHandler struct {
	logger logr.Logger
	dal    dal.DAL
	delay  time.Duration
}

// force compiler to check that we implement the interface
var _ handler.EventHandler = &globalDelayHandler{}

func (h *globalDelayHandler) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, evt.Object, queue)
}

func (h *globalDelayHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// ignoring the old object: relevance is decided from current state.
	h.enqueue(ctx, evt.ObjectNew, queue)
}

func (h *globalDelayHandler) Delete(ctx context.Context, evt event.TypedDeleteEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, evt.Object, queue)
}

func (h *globalDelayHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, evt.Object, queue)
}

func (h *globalDelayHandler) enqueue(ctx context.Context, object client.Object, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	relevant, err := h.relevant(ctx, object)
	if err != nil {
		// On error we skip rather than enqueue: a spurious global reconcile is
		// cheap, but more importantly the next event (or the periodic resync)
		// will retry, and we don't want a transient list error to amplify into
		// a reconcile per event.
		h.logger.Error(err, "error deciding event relevance for global reconcile",
			"namespace", object.GetNamespace(),
			"name", object.GetName(),
			"kind", object.GetObjectKind())
		return
	}

	if relevant {
		queue.AddAfter(globalReconcileKey, h.delay)
	}
}

// relevant reports whether an event on object should wake the heavy reconcile.
func (h *globalDelayHandler) relevant(ctx context.Context, object client.Object) (bool, error) {
	switch obj := object.(type) {
	case *v1alpha1.NodeWright:
		return true, nil
	case *corev1.Node:
		// Read-only: matchSelectors only inspects spec.nodeSelector and names, so skip the
		// per-event deep copy of every NodeWright (status carries per-node state, so on a
		// large cluster that copy is the dominant allocation on the Node event path).
		// Items on this path also come back without TypeMeta set, so never hand them to
		// anything that keys on Kind, such as ObjectTracker.
		list, err := h.dal.GetSkyhooks(ctx, client.UnsafeDisableDeepCopy)
		if err != nil {
			return false, fmt.Errorf("listing nodewrights for node relevance check: %w", err)
		}
		if list == nil {
			return false, nil
		}
		return len(matchSelectors(list, obj.Labels)) > 0, nil
	default:
		return false, nil
	}
}

// matchSelectors returns the Skyhooks whose node selector matches the given
// labels.
func matchSelectors(crs *v1alpha1.NodeWrightList, lbs map[string]string) []types.NamespacedName {

	ret := make([]types.NamespacedName, 0)

	for _, cr := range crs.Items {
		match := false

		selector, err := metav1.LabelSelectorAsSelector(&cr.Spec.NodeSelector)
		if err != nil {
			match = true
		}

		if selector.Matches(labels.Set(lbs)) {
			match = true
		}

		if match {
			ret = append(ret, types.NamespacedName{
				Name:      cr.Name,
				Namespace: cr.Namespace,
			})
		}
	}

	return ret
}
