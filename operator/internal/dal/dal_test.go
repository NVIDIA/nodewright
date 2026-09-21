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
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	// two packages named "fake" collide here: the controller-runtime one builds the
	// client.Client the DAL wraps, the client-go one the clientset GetPodLogTail needs.
	k8sfake "k8s.io/client-go/kubernetes/fake"
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
		return New(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(), nil)
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
			}).Build(), nil)

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
			}).Build(), nil)

			jobs, err := d.GetJobs(ctx)
			Expect(err).To(MatchError(ContainSubstring("error getting jobs")))
			Expect(err).To(MatchError(ContainSubstring("boom")))
			Expect(jobs).To(BeNil())
		})
	})
})

var _ = Describe("tailAndSanitize", func() {
	// A rune whose UTF-8 encoding is longer than one byte, used to build inputs
	// that a byte-boundary cut would split.
	multibyte := strings.Repeat("é", 100) // 2 bytes each → 200 bytes

	cases := []struct {
		name     string
		input    string
		maxBytes int64
		want     string
		wantErr  bool
	}{
		{name: "shorter than cap returns whole", input: "hello", maxBytes: 1024, want: "hello"},
		{name: "longer than cap keeps the tail", input: "abcdef", maxBytes: 3, want: "def"},
		{name: "zero cap returns empty", input: "abc", maxBytes: 0, want: ""},
		{name: "negative cap returns empty", input: "abc", maxBytes: -1, want: ""},
		{name: "exact cap returns whole", input: "abc", maxBytes: 3, want: "abc"},
		{name: "spans multiple read chunks", input: strings.Repeat("x", 100*1024) + "TAIL", maxBytes: 4, want: "TAIL"},
	}

	It("handles the configured byte caps", func() {
		for _, tc := range cases {
			got, err := tailAndSanitize(strings.NewReader(tc.input), tc.maxBytes)
			if tc.wantErr {
				Expect(err).To(HaveOccurred(), tc.name)
			} else {
				Expect(err).ToNot(HaveOccurred(), tc.name)
			}
			Expect(got).To(Equal(tc.want), tc.name)
		}
	})

	// Invalid bytes, including a multibyte rune the tail cut in half, must come
	// back as valid UTF-8.
	It("always returns valid UTF-8", func() {
		got, err := tailAndSanitize(strings.NewReader(string([]byte{0xff, 0xfe})+"ok"), 1024)
		Expect(err).ToNot(HaveOccurred())
		if !utf8.ValidString(got) {
			Fail("result is not valid UTF-8")
		}
		Expect(got).To(HaveSuffix("ok"))

		// Cut a 2-byte rune in half by capping to an odd tail length.
		cut, err := tailAndSanitize(strings.NewReader(multibyte), 3)
		Expect(err).ToNot(HaveOccurred())
		Expect(utf8.ValidString(cut)).To(BeTrue())
	})

	// Sanitizing runs after the cap, and each invalid byte becomes a 3-byte replacement
	// rune, so a tail already at the cap can grow past it unless the cap is re-applied.
	It("stays within the cap after sanitizing", func() {
		const maxBytes = 64
		invalid := strings.Repeat(string([]byte{0xff, 'a'}), 200)
		got, err := tailAndSanitize(strings.NewReader(invalid), maxBytes)
		Expect(err).ToNot(HaveOccurred())
		Expect(int64(len(got))).To(BeNumerically("<=", maxBytes))
		Expect(utf8.ValidString(got)).To(BeTrue())
	})
})

var _ = Describe("GetPodLogTail", func() {
	It("returns the container logs from the clientset", func() {
		d := New(nil, k8sfake.NewClientset())
		got, err := d.GetPodLogTail(context.Background(), "skyhook", "pod-1", "step", 1024)
		Expect(err).ToNot(HaveOccurred())
		// The fake clientset serves a canned "fake logs" body for GetLogs.
		Expect(got).To(ContainSubstring("fake logs"))
	})

	It("errors when no clientset is configured", func() {
		d := New(nil, nil)
		_, err := d.GetPodLogTail(context.Background(), "skyhook", "pod-1", "step", 1024)
		Expect(err).To(HaveOccurred())
	})
})
