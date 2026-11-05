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

	"github.com/NVIDIA/nodewright/operator/internal/dal"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newFakeClientBuilder() *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithIndex(&corev1.Pod{}, indexPodJobUID, indexPodByJobUID).
		WithIndex(&batchv1.Job{}, indexJobNode, indexJobByNode)
}

var _ = Describe("field indexes", func() {
	It("indexes a pod by its Job's controller-uid and skips pods without one", func() {
		owned := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{batchControllerUIDLabel: "uid-1"}}}
		Expect(indexPodByJobUID(owned)).To(Equal([]string{"uid-1"}))
		Expect(indexPodByJobUID(&corev1.Pod{})).To(BeNil())
	})

	It("indexes a Job by its node label and skips Jobs without one", func() {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{jobNodeLabel: nodeLabelValue("worker-7")}}}
		Expect(indexJobByNode(job)).To(Equal([]string{nodeLabelValue("worker-7")}))
		Expect(indexJobByNode(&batchv1.Job{})).To(BeNil())
	})

	It("lets childPods find a Job's pods through the index", func() {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns", UID: "uid-1"}}
		mine := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "ns", Labels: map[string]string{batchControllerUIDLabel: "uid-1"}}}
		other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", Labels: map[string]string{batchControllerUIDLabel: "uid-2"}}}
		var podListFields string
		c := newFakeClientBuilder().WithObjects(mine, other).WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.PodList); ok {
					o := &client.ListOptions{}
					o.ApplyOptions(opts)
					if o.FieldSelector != nil {
						podListFields = o.FieldSelector.String()
					}
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
		r := &JobReconciler{dal: dal.New(c)}
		pods, err := r.childPods(context.Background(), job)
		Expect(err).NotTo(HaveOccurred())
		Expect(podListFields).To(Equal(indexPodJobUID + "=uid-1"))
		Expect(pods).To(HaveLen(1))
		Expect(pods[0].Name).To(Equal("mine"))
	})
})
