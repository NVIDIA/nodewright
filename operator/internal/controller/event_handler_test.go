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
	"errors"

	. "github.com/onsi/ginkgo/v2"

	// . "github.com/onsi/gomega"
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/dal"
	dalmock "github.com/NVIDIA/nodewright/operator/internal/dal/mock"
	MockClient "github.com/NVIDIA/nodewright/operator/internal/mocks/client"
	"github.com/NVIDIA/nodewright/operator/internal/mocks/workqueue"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Event Handler Tests", func() {

	const (
		nodename = "foonode"
	)

	It("Pod Event matches a Skyhook", func() {

		dalMock := dalmock.MockDAL{}
		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := eventHandler{
			logger: GinkgoLogr,
			dal:    &dalMock,
		}

		labels := map[string]string{
			"foo": "bar",
		}

		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				NodeName: nodename,
			},
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
				Name:   nodename,
			},
		}

		skyhooks := v1alpha1.SkyhookList{
			Items: []v1alpha1.Skyhook{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foobar_name",
				},
				Spec: v1alpha1.SkyhookSpec{
					NodeSelector: metav1.LabelSelector{MatchLabels: labels},
				},
			},
			},
		}

		dalMock.EXPECT().GetSkyhooks(ctx).Return(&skyhooks, nil).Once()

		dalMock.EXPECT().GetNode(ctx, nodename).Return(node, nil).Once()

		queue.EXPECT().Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: skyhooks.Items[0].Name}}).Once()

		/// test
		handler.Create(ctx, event.CreateEvent{Object: pod}, queue)

	})

	It("Pod Event does not match a Skyhook", func() {

		clientMock := MockClient.NewClient(GinkgoT())
		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := eventHandler{
			logger: GinkgoLogr,
			dal:    dal.New(clientMock),
		}

		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				NodeName: nodename,
			},
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"foobar": "2000",
				},
				Name: nodename,
			},
		}

		skyhook := v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foobar_name",
			},
			Spec: v1alpha1.SkyhookSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: map[string]string{
					"foo": "Bar",
				},
				}},
		}

		clientMock.EXPECT().List(ctx, &v1alpha1.SkyhookList{}).
			Return(nil).
			Run(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) {
				l := list.(*v1alpha1.SkyhookList)
				l.Items = append(l.Items, skyhook)
			})

		clientMock.EXPECT().Get(ctx, types.NamespacedName{Name: nodename}, &corev1.Node{}).
			Return(nil).
			Run(func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) {
				n := obj.(*corev1.Node)
				n.ObjectMeta = node.ObjectMeta
			})

		/// test
		handler.Create(ctx, event.CreateEvent{Object: pod}, queue)
	})

	It("All Node Event matches a Skyhook", func() {
		clientMock := MockClient.NewClient(GinkgoT())
		queue := workqueue.NewTypedRateLimitingInterface[reconcile.Request](GinkgoT())
		handler := eventHandler{
			logger: GinkgoLogr,
			dal:    dal.New(clientMock),
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"foo": "Bar",
				},
				Name: nodename,
			},
		}

		skyhook := v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foobar_name",
			},
			Spec: v1alpha1.SkyhookSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: map[string]string{
					"foo": "Bar",
				}},
			},
		}

		clientMock.EXPECT().List(ctx, &v1alpha1.SkyhookList{}).
			Return(nil).
			Run(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) {
				l := list.(*v1alpha1.SkyhookList)
				l.Items = append(l.Items, skyhook)
			}).
			Times(4)

		queue.
			EXPECT().
			Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: skyhook.Name}}).
			Times(4)

		/// test
		handler.Create(ctx, event.CreateEvent{Object: node}, queue)
		handler.Delete(ctx, event.DeleteEvent{Object: node}, queue)
		handler.Generic(ctx, event.GenericEvent{Object: node}, queue)

		oldNode := node.DeepCopy()
		oldNode.Labels = map[string]string{
			"foobar": "2000",
		}
		handler.Update(ctx, event.UpdateEvent{ObjectNew: node, ObjectOld: oldNode}, queue)
	})

	It("List Skyhook errors", func() {
		clientMock := MockClient.NewClient(GinkgoT())

		handler := eventHandler{
			logger: GinkgoLogr,
			dal:    dal.New(clientMock),
		}

		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				NodeName: nodename,
			},
		}

		err := errors.New("this is an error")

		clientMock.EXPECT().List(ctx, &v1alpha1.SkyhookList{}).
			Return(err).
			Times(3)

		handler.Create(ctx, event.CreateEvent{Object: pod}, nil)
		handler.Delete(ctx, event.DeleteEvent{Object: pod}, nil)
		handler.Generic(ctx, event.GenericEvent{Object: pod}, nil)

	})

	It("Get Node errors on pod event", func() {
		clientMock := MockClient.NewClient(GinkgoT())

		handler := eventHandler{
			logger: GinkgoLogr,
			dal:    dal.New(clientMock),
		}

		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				NodeName: nodename,
			},
		}

		err := errors.New("this is an error")
		skyhook := v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foobar_name",
			},
			Spec: v1alpha1.SkyhookSpec{
				NodeSelector: metav1.LabelSelector{MatchLabels: map[string]string{
					"foo": "Bar",
				}},
			},
		}

		clientMock.EXPECT().List(ctx, &v1alpha1.SkyhookList{}).
			Return(nil).
			Run(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) {
				l := list.(*v1alpha1.SkyhookList)
				l.Items = append(l.Items, skyhook)
			}).Once()

		clientMock.EXPECT().Get(ctx, types.NamespacedName{Name: nodename}, &corev1.Node{}).
			Return(err).
			Once()

		handler.Update(ctx, event.UpdateEvent{ObjectNew: pod, ObjectOld: pod}, nil)

	})
})
