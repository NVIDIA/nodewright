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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	dalmock "github.com/NVIDIA/nodewright/operator/internal/dal/mock"
	"github.com/NVIDIA/nodewright/operator/internal/mocks/workqueue"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Global delay handler", func() {

	const delay = 50 * time.Millisecond

	matchingLabels := map[string]string{"foo": "bar"}

	skyhookList := &v1alpha1.NodeWrightList{
		Items: []v1alpha1.NodeWright{{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec:       v1alpha1.NodeWrightSpec{NodeSelector: metav1.LabelSelector{MatchLabels: matchingLabels}},
		}},
	}

	It("enqueues the global key for a node matched by a skyhook, on every event type", func() {

		dalMock := &dalmock.MockDAL{}
		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := &globalDelayHandler{logger: GinkgoLogr, dal: dalMock, delay: delay}

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "foonode", Labels: matchingLabels}}

		dalMock.EXPECT().GetSkyhooks(ctx, client.UnsafeDisableDeepCopy).Return(skyhookList, nil).Times(4)
		queue.EXPECT().AddAfter(globalReconcileKey, delay).Times(4)

		handler.Create(ctx, event.CreateEvent{Object: node}, queue)
		handler.Update(ctx, event.UpdateEvent{ObjectNew: node, ObjectOld: node}, queue)
		handler.Delete(ctx, event.DeleteEvent{Object: node}, queue)
		handler.Generic(ctx, event.GenericEvent{Object: node}, queue)
	})

	It("does not enqueue for a node no skyhook selects", func() {

		dalMock := &dalmock.MockDAL{}
		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := &globalDelayHandler{logger: GinkgoLogr, dal: dalMock, delay: delay}

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "foonode", Labels: map[string]string{"no": "match"}}}

		// no AddAfter expectation: the mock fails the test if AddAfter is called.
		dalMock.EXPECT().GetSkyhooks(ctx, client.UnsafeDisableDeepCopy).Return(skyhookList, nil).Once()

		handler.Create(ctx, event.CreateEvent{Object: node}, queue)
	})

	It("always enqueues for a skyhook event without consulting selectors", func() {

		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		// dal is intentionally nil: a skyhook event must not need to list skyhooks.
		handler := &globalDelayHandler{logger: GinkgoLogr, delay: delay}

		skyhook := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

		queue.EXPECT().AddAfter(globalReconcileKey, delay).Once()

		handler.Create(ctx, event.CreateEvent{Object: skyhook}, queue)
	})

	It("does not enqueue for a type relevance does not opt in", func() {

		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := &globalDelayHandler{logger: GinkgoLogr, delay: delay}

		// relevant() defaults to false, so a watch added without a matching case here
		// silently drops every event. This pins that default down. Jobs land here now:
		// JobReconciler owns them, and its node write is itself a Node event.
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foopod", Namespace: "skyhook"}}

		handler.Create(ctx, event.CreateEvent{Object: pod}, queue)
	})

	It("does not enqueue when listing skyhooks errors", func() {

		dalMock := &dalmock.MockDAL{}
		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := &globalDelayHandler{logger: GinkgoLogr, dal: dalMock, delay: delay}

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "foonode", Labels: matchingLabels}}

		// no AddAfter expectation: a list error must not enqueue.
		dalMock.EXPECT().GetSkyhooks(ctx, client.UnsafeDisableDeepCopy).Return(nil, errors.New("boom")).Once()

		handler.Create(ctx, event.CreateEvent{Object: node}, queue)
	})
})
