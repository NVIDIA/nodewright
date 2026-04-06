/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

type eventHandler struct {
	logger logr.Logger
	dal    dal.DAL
}

// force compiler to check that we implement the interface
var _ handler.EventHandler = &eventHandler{}

// the EventHandler interface
func (e *eventHandler) Create(ctx context.Context, event event.TypedCreateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {

	matches, err := e.genericHandler(ctx, event.Object)
	if err != nil {
		e.logger.Error(err, "error handling create event",
			"namespace", event.Object.GetNamespace(),
			"name", event.Object.GetName(),
			"kind", event.Object.GetObjectKind())
	}

	// not sure if actually we want to many or one
	for _, match := range matches {
		queue.Add(reconcile.Request{NamespacedName: match})
	}
}

func (e *eventHandler) Update(ctx context.Context, event event.TypedUpdateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {

	// ignoring the old for now, might need to do some comparing to decided
	// if we want to do something, but for now starting simple

	matches, err := e.genericHandler(ctx, event.ObjectNew)
	if err != nil {
		e.logger.Error(err, "error handling update event",
			"namespace", event.ObjectNew.GetNamespace(),
			"name", event.ObjectNew.GetName(),
			"kind", event.ObjectNew.GetObjectKind())
	}

	// not sure if actually we want to many or one
	for _, match := range matches {
		queue.Add(reconcile.Request{NamespacedName: match})
	}
}

func (e *eventHandler) Delete(ctx context.Context, event event.TypedDeleteEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	matches, err := e.genericHandler(ctx, event.Object)
	if err != nil {
		e.logger.Error(err, "error handling delete event",
			"namespace", event.Object.GetNamespace(),
			"name", event.Object.GetName(),
			"kind", event.Object.GetObjectKind())
	}

	// not sure if actually we want to many or one
	for _, match := range matches {
		queue.Add(reconcile.Request{NamespacedName: match})
	}
}

// Generic not sure what Generic is, so just loging for now
func (e *eventHandler) Generic(ctx context.Context, event event.TypedGenericEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {

	matches, err := e.genericHandler(ctx, event.Object)
	if err != nil {
		e.logger.Error(err, "error handling generic event",
			"namespace", event.Object.GetNamespace(),
			"name", event.Object.GetName(),
			"kind", event.Object.GetObjectKind())
	}

	// not sure if actually we want to many or one
	for _, match := range matches {
		queue.Add(reconcile.Request{NamespacedName: match})
	}
}

// genericHandler should be able to handle most of the logic for all the different event types
func (e *eventHandler) genericHandler(ctx context.Context, object client.Object) ([]types.NamespacedName, error) {

	list, err := e.dal.GetSkyhooks(ctx)
	if err != nil {

		return nil, err
	}
	if list == nil {
		return nil, nil
	}

	var matches []types.NamespacedName
	// kind := "unknown"
	switch obj := object.(type) {
	case *corev1.Pod:
		// kind = "pod"
		node, err := e.dal.GetNode(ctx, obj.Spec.NodeName)
		if err != nil {
			return nil, err
		}
		matches = matchSelectors(list, node.Labels)
	case *corev1.Node:
		// kind = "node"
		matches = matchSelectors(list, obj.Labels)
	}

	// e.logger.Info("Event Handler",
	// 	"event_type", event_type,
	// 	"match_labels", matches,
	// 	"namespace", object.GetNamespace(),
	// 	"name", object.GetName(),
	// 	"kind", kind)

	return matches, nil
}

func matchSelectors(crs *v1alpha1.SkyhookList, lbs map[string]string) []types.NamespacedName {

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
				Namespace: cr.Namespace, // guessing always empty string
			})
		}
	}

	return ret
}
