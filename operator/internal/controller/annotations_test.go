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
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("package annotation helpers", func() {

	var (
		nw    *v1alpha1.NodeWright
		pkg   *v1alpha1.Package
		image string
	)

	BeforeEach(func() {
		nw = &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "gpu-init"}}
		pkg = &v1alpha1.Package{
			PackageRef:   v1alpha1.PackageRef{Name: "tuning", Version: "1.2.3"},
			Image:        "ghcr.io/nvidia/skyhook-packages/tuning",
			ContainerSHA: "sha256:" + strings.Repeat("a", 64),
		}
		image = "ghcr.io/nvidia/skyhook-packages/tuning:1.2.3"
	})

	It("round-trips the package annotation identically on a Pod, a Job, and a Job pod template", func() {
		pod := &corev1.Pod{}
		job := &batchv1.Job{}
		// the Job path sets the annotation on both the Job and its pod template
		// (the in-flight erroring path reads it off the child pod), so the pod
		// template — a *corev1.PodTemplateSpec — must round-trip too.
		tmpl := &job.Spec.Template

		Expect(SetPackages(pod, nw, image, v1alpha1.StageApply, pkg)).To(Succeed())
		Expect(SetPackages(job, nw, image, v1alpha1.StageApply, pkg)).To(Succeed())
		Expect(SetPackages(tmpl, nw, image, v1alpha1.StageApply, pkg)).To(Succeed())

		fromPod, err := GetPackage(pod)
		Expect(err).ToNot(HaveOccurred())
		fromJob, err := GetPackage(job)
		Expect(err).ToNot(HaveOccurred())
		fromTmpl, err := GetPackage(tmpl)
		Expect(err).ToNot(HaveOccurred())

		Expect(fromJob).To(Equal(fromPod))
		Expect(fromTmpl).To(Equal(fromPod))
		Expect(fromPod.Skyhook).To(Equal("gpu-init"))
		Expect(fromPod.Stage).To(Equal(v1alpha1.StageApply))
		Expect(fromPod.Name).To(Equal("tuning"))
		Expect(fromPod.Version).To(Equal(pkg.Version))
		Expect(fromPod.Image).To(Equal(image))
		Expect(fromPod.ContainerSHA).To(Equal(pkg.ContainerSHA))

		Expect(job.GetAnnotations()).To(HaveKey(packageAnnotationKey))
		Expect(job.GetAnnotations()[packageAnnotationKey]).To(Equal(pod.GetAnnotations()[packageAnnotationKey]))
		Expect(tmpl.GetAnnotations()[packageAnnotationKey]).To(Equal(pod.GetAnnotations()[packageAnnotationKey]))
	})

	It("treats a typed-nil object as absent instead of panicking", func() {
		var pod *corev1.Pod // typed nil: non-nil interface wrapping a nil pointer

		pkgOut, err := GetPackage(pod)
		Expect(err).ToNot(HaveOccurred())
		Expect(pkgOut).To(BeNil())

		Expect(SetPackages(pod, nw, image, v1alpha1.StageApply, pkg)).To(Succeed())

		invalid, err := IsInvalidPackage(pod)
		Expect(err).ToNot(HaveOccurred())
		Expect(invalid).To(BeFalse())

		Expect(InvalidatePackage(pod)).To(Succeed())
	})

	It("marks a Job's package invalid via InvalidatePackage/IsInvalidPackage", func() {
		job := &batchv1.Job{}
		Expect(SetPackages(job, nw, image, v1alpha1.StageApply, pkg)).To(Succeed())

		invalid, err := IsInvalidPackage(job)
		Expect(err).ToNot(HaveOccurred())
		Expect(invalid).To(BeFalse())

		Expect(InvalidatePackage(job)).To(Succeed())

		invalid, err = IsInvalidPackage(job)
		Expect(err).ToNot(HaveOccurred())
		Expect(invalid).To(BeTrue())
	})

	It("treats an object without a package annotation as absent, not invalid", func() {
		obj := &batchv1.Job{} // non-nil, but carries no package annotation

		pkgOut, err := GetPackage(obj)
		Expect(err).ToNot(HaveOccurred())
		Expect(pkgOut).To(BeNil())

		invalid, err := IsInvalidPackage(obj)
		Expect(err).ToNot(HaveOccurred())
		Expect(invalid).To(BeFalse())

		// must be a no-op rather than a nil-package panic
		Expect(InvalidatePackage(obj)).To(Succeed())
		Expect(obj.GetAnnotations()).ToNot(HaveKey(packageAnnotationKey))
	})
})
