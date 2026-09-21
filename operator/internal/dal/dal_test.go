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

package dal

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var _ = Describe("Job accessors", func() {

	const namespace = "skyhook"

	var (
		ctx    context.Context
		scheme *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
	})

	mkJob := func(name string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	}

	build := func(objs ...client.Object) DAL {
		return New(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build())
	}

	Describe("GetJob", func() {

		It("returns the job", func() {
			job, err := build(mkJob("apply-worker-1")).GetJob(ctx, namespace, "apply-worker-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(job).ToNot(BeNil())
			Expect(job.Name).To(Equal("apply-worker-1"))
		})

		It("returns nil without an error when the job is gone", func() {
			// not-found is nil/nil, not an error: a Job reaped by its TTL is a normal
			// observation for a level-triggered caller, matching GetPod.
			job, err := build().GetJob(ctx, namespace, "apply-worker-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(job).To(BeNil())
		})

		It("wraps a client error", func() {
			d := New(fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return errors.New("boom")
				},
			}).Build())

			job, err := d.GetJob(ctx, namespace, "apply-worker-1")
			Expect(err).To(MatchError(ContainSubstring("error getting job [skyhook|apply-worker-1]")))
			Expect(err).To(MatchError(ContainSubstring("boom")))
			Expect(job).To(BeNil())
		})
	})

	Describe("GetJobs", func() {

		It("returns the jobs", func() {
			jobs, err := build(mkJob("apply-worker-1"), mkJob("apply-worker-2")).GetJobs(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).ToNot(BeNil())
			Expect(jobs.Items).To(HaveLen(2))
		})

		It("filters by list options", func() {
			jobs, err := build(mkJob("apply-worker-1")).GetJobs(ctx, client.InNamespace("other"))
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).To(BeNil())
		})

		It("returns nil without an error for an empty list", func() {
			// an empty list collapses to nil so callers test the list itself rather than
			// its length, matching GetPods.
			jobs, err := build().GetJobs(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).To(BeNil())
		})

		It("wraps a client error", func() {
			d := New(fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
					return errors.New("boom")
				},
			}).Build())

			jobs, err := d.GetJobs(ctx)
			Expect(err).To(MatchError(ContainSubstring("error getting jobs")))
			Expect(err).To(MatchError(ContainSubstring("boom")))
			Expect(jobs).To(BeNil())
		})
	})
})
