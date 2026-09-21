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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
)

var _ = Describe("reconcileLegacyLabeledWorkloads", func() {
	const (
		shName = "legacy-sh"
		ns     = "skyhook"
	)
	ctx := context.Background()

	opts := SkyhookOperatorOptions{
		Namespace:            ns,
		MaxInterval:          time.Second * 61,
		CopyDirRoot:          "/tmp",
		RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
		AgentImage:           "foo:bar",
		PauseImage:           "foo:bar",
		AgentLogRoot:         "/log",
		JobOperatorOptions: JobOperatorOptions{
			JobTTLSucceeded: time.Hour,
			JobTTLFailed:    24 * time.Hour,
			JobStageTimeout: time.Hour,
			JobBackoffLimit: 3,
		},
	}

	buildClient := func(objs ...client.Object) client.Client {
		s := runtime.NewScheme()
		Expect(corev1.AddToScheme(s)).To(Succeed())
		Expect(v1alpha1.AddToScheme(s)).To(Succeed())
		return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	}

	// The NodeWright the ConfigMaps must end up owned by. A UID is required for
	// IsControlledBy and SetControllerReference to behave like they do in a cluster.
	nodeWright := func() *v1alpha1.NodeWright {
		return &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: shName, UID: types.UID("nw-uid-1")},
		}
	}

	// An ownerReference of the shape the PRE-RENAME operator left behind.
	legacyOwnerRef := metav1.OwnerReference{
		APIVersion: "skyhook.nvidia.com/v1alpha1",
		Kind:       "Skyhook",
		Name:       shName,
		UID:        types.UID("legacy-uid-1"),
		Controller: ptr(true),
	}

	legacyPod := func(name, node string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					"skyhook.nvidia.com/name":      shName,
					"skyhook.nvidia.com/Node.name": node,
				},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
		}
	}

	legacyCM := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-cm",
				Namespace: ns,
				Labels: map[string]string{
					"skyhook.nvidia.com/skyhook-node-meta": shName,
					"skyhook.nvidia.com/name":              shName,
				},
			},
			Data: map[string]string{"k": "v"},
		}
	}

	It("converge: adds the nodewright ConfigMap label but keeps the legacy label and the legacy pods", func() {
		c := buildClient(legacyPod("legacy-pod", "node-a"), legacyCM())
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		hadLegacy, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(hadLegacy).To(BeTrue())
		Expect(changed).To(BeTrue())

		By("leaving the legacy pod in place for a possible rollback")
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-pod"}, &corev1.Pod{})).To(Succeed())

		By("adding the nodewright labels while keeping the legacy ones")
		gotCM := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-cm"}, gotCM)).To(Succeed())
		Expect(gotCM.Labels).To(HaveKeyWithValue("nodewright.nvidia.com/skyhook-node-meta", shName))
		Expect(gotCM.Labels).To(HaveKeyWithValue("skyhook.nvidia.com/skyhook-node-meta", shName))
		Expect(gotCM.Data).To(HaveKeyWithValue("k", "v"))

		By("being idempotent (the nodewright label is already present)")
		hadLegacy2, changed2, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(hadLegacy2).To(BeTrue())
		Expect(changed2).To(BeFalse())
	})

	It("prune: graceful-deletes legacy pods, drops the legacy ConfigMap label, and leaves other objects untouched", func() {
		newPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "new-pod",
				Namespace: ns,
				Labels:    map[string]string{"nodewright.nvidia.com/name": shName},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
		}
		otherPod := legacyPod("other-pod", "node-a")
		otherPod.Labels["skyhook.nvidia.com/name"] = "some-other-skyhook"

		// A converged ConfigMap carries both prefixes.
		cm := legacyCM()
		cm.Labels["nodewright.nvidia.com/skyhook-node-meta"] = shName
		cm.Labels["nodewright.nvidia.com/name"] = shName

		c := buildClient(legacyPod("legacy-pod", "node-a"), newPod, otherPod, cm)
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		hadLegacy, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), true)
		Expect(err).ToNot(HaveOccurred())
		Expect(hadLegacy).To(BeTrue())
		Expect(changed).To(BeTrue())

		By("deleting the legacy-labeled pod for this skyhook")
		Expect(apierrors.IsNotFound(
			c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-pod"}, &corev1.Pod{}),
		)).To(BeTrue())

		By("leaving the new-labeled pod untouched")
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "new-pod"}, &corev1.Pod{})).To(Succeed())

		By("leaving another skyhook's legacy pod untouched (scoped by name)")
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "other-pod"}, &corev1.Pod{})).To(Succeed())

		By("dropping the legacy ConfigMap label while preserving the nodewright label and data")
		gotCM := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-cm"}, gotCM)).To(Succeed())
		Expect(gotCM.Labels).To(HaveKeyWithValue("nodewright.nvidia.com/skyhook-node-meta", shName))
		for k := range gotCM.Labels {
			Expect(k).ToNot(HavePrefix("skyhook.nvidia.com/"))
		}
		Expect(gotCM.Data).To(HaveKeyWithValue("k", "v"))

		By("reporting nothing legacy remains on a second prune pass")
		hadLegacy2, changed2, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), true)
		Expect(err).ToNot(HaveOccurred())
		Expect(hadLegacy2).To(BeFalse())
		Expect(changed2).To(BeFalse())
	})

	// A real pre-rename cluster splits the two label keys across DIFFERENT ConfigMaps:
	// the per-node metadata ConfigMap carries skyhook-node-meta, while the package
	// ConfigMap carries only /name. The legacyCM fixture above puts both keys on one
	// object, which is why a converge that listed by skyhook-node-meta alone still
	// looked correct here while wedging every real upgrade.
	packageCM := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-sh-shellscript-1.1.1",
				Namespace: ns,
				Labels:    map[string]string{"skyhook.nvidia.com/name": shName},
			},
			Data: map[string]string{"apply.sh": "#!/bin/bash\necho hi\n"},
		}
	}

	nodeMetaCM := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-sh-node-a-metadata-abc123",
				Namespace: ns,
				Labels:    map[string]string{"skyhook.nvidia.com/skyhook-node-meta": shName},
			},
			Data: map[string]string{"packages.json": "{}"},
		}
	}

	It("converge: relabels the package ConfigMap, which carries only the legacy /name label", func() {
		c := buildClient(packageCM(), nodeMetaCM())
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		hadLegacy, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(hadLegacy).To(BeTrue())
		Expect(changed).To(BeTrue())

		By("converging the package ConfigMap so UpsertConfigmaps can find it by the new label")
		gotPkg := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-shellscript-1.1.1"}, gotPkg)).To(Succeed())
		Expect(gotPkg.Labels).To(HaveKeyWithValue("nodewright.nvidia.com/name", shName))
		Expect(gotPkg.Labels).To(HaveKeyWithValue("skyhook.nvidia.com/name", shName))
		Expect(gotPkg.Data).To(HaveKeyWithValue("apply.sh", "#!/bin/bash\necho hi\n"))

		By("converging the per-node metadata ConfigMap as well")
		gotMeta := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-node-a-metadata-abc123"}, gotMeta)).To(Succeed())
		Expect(gotMeta.Labels).To(HaveKeyWithValue("nodewright.nvidia.com/skyhook-node-meta", shName))

		By("being idempotent across both label keys")
		_, changed2, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(changed2).To(BeFalse())
	})

	It("prune: drops the legacy label from the package ConfigMap", func() {
		pkg := packageCM()
		pkg.Labels["nodewright.nvidia.com/name"] = shName

		c := buildClient(pkg)
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		_, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), true)
		Expect(err).ToNot(HaveOccurred())
		Expect(changed).To(BeTrue())

		got := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-shellscript-1.1.1"}, got)).To(Succeed())
		Expect(got.Labels).To(HaveKeyWithValue("nodewright.nvidia.com/name", shName))
		for k := range got.Labels {
			Expect(k).ToNot(HavePrefix("skyhook.nvidia.com/"))
		}
		Expect(got.Data).To(HaveKeyWithValue("apply.sh", "#!/bin/bash\necho hi\n"))
	})

	It("converge: re-parents ConfigMaps off the legacy Skyhook onto the NodeWright", func() {
		// Without this, the migration guide's own "delete the old CRs" step
		// cascade-deletes these ConfigMaps out from under the live NodeWright.
		pkg := packageCM()
		pkg.OwnerReferences = []metav1.OwnerReference{legacyOwnerRef}
		meta := nodeMetaCM()
		meta.OwnerReferences = []metav1.OwnerReference{legacyOwnerRef}

		c := buildClient(pkg, meta)
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		_, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(changed).To(BeTrue())

		for _, name := range []string{"legacy-sh-shellscript-1.1.1", "legacy-sh-node-a-metadata-abc123"} {
			got := &corev1.ConfigMap{}
			Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, got)).To(Succeed())

			By("dropping the legacy Skyhook owner on " + name)
			for _, ref := range got.OwnerReferences {
				Expect(ref.APIVersion).ToNot(HavePrefix("skyhook.nvidia.com/"))
			}

			By("making the NodeWright the controller of " + name)
			Expect(metav1.IsControlledBy(got, nodeWright())).To(BeTrue())
		}

		By("being idempotent once ownership is already correct")
		_, changed2, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(changed2).To(BeFalse())
	})

	// LegacyCleanupDelay=0 makes legacyCleanupShouldPrune true on the very first
	// reconcile, so prune can run without any converge ever having happened. If
	// re-parenting only ran on the converge path, that configuration would leave the
	// ConfigMaps owned by the legacy Skyhook and still cascade-delete bait.
	It("prune: re-parents ConfigMaps even when no converge ran first", func() {
		pkg := packageCM()
		pkg.OwnerReferences = []metav1.OwnerReference{legacyOwnerRef}

		c := buildClient(pkg)
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		_, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), true)
		Expect(err).ToNot(HaveOccurred())
		Expect(changed).To(BeTrue())

		got := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-shellscript-1.1.1"}, got)).To(Succeed())
		for _, ref := range got.OwnerReferences {
			Expect(ref.APIVersion).ToNot(HavePrefix("skyhook.nvidia.com/"))
		}
		Expect(metav1.IsControlledBy(got, nodeWright())).To(BeTrue())
	})

	It("converge: preserves an unrelated non-legacy ownerReference", func() {
		other := metav1.OwnerReference{
			APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-else", UID: types.UID("other-uid"),
		}
		pkg := packageCM()
		pkg.OwnerReferences = []metav1.OwnerReference{other, legacyOwnerRef}

		c := buildClient(pkg)
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		_, _, err = r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())

		got := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-shellscript-1.1.1"}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(ContainElement(HaveField("Name", "someone-else")))
		Expect(metav1.IsControlledBy(got, nodeWright())).To(BeTrue())
	})

	// SetControllerReference returns AlreadyOwnedError when something else already
	// controls the object. Propagating that would abort HandleMigrations for EVERY
	// NodeWright, so one oddly-owned ConfigMap must not wedge the whole migration.
	It("converge: skips a ConfigMap controlled by another controller instead of wedging", func() {
		foreignController := metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       "someone-elses-controller",
			UID:        types.UID("foreign-uid"),
			Controller: ptr(true),
		}
		pkg := packageCM()
		pkg.OwnerReferences = []metav1.OwnerReference{foreignController}

		c := buildClient(pkg, nodeMetaCM())
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		By("not returning an error for the whole skyhook")
		_, _, err = r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())

		By("leaving the foreign controller's ownership intact")
		got := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-shellscript-1.1.1"}, got)).To(Succeed())
		Expect(got.OwnerReferences).To(ContainElement(HaveField("Name", "someone-elses-controller")))
		Expect(metav1.IsControlledBy(got, nodeWright())).To(BeFalse())

		By("still converging the other ConfigMap it can own")
		gotMeta := &corev1.ConfigMap{}
		Expect(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "legacy-sh-node-a-metadata-abc123"}, gotMeta)).To(Succeed())
		Expect(metav1.IsControlledBy(gotMeta, nodeWright())).To(BeTrue())
	})

	It("is a cheap no-op when there is nothing legacy-labeled", func() {
		c := buildClient()
		r, err := NewSkyhookReconciler(c.Scheme(), c, c, events.NewFakeRecorder(10), opts)
		Expect(err).ToNot(HaveOccurred())

		hadLegacy, changed, err := r.reconcileLegacyLabeledWorkloads(ctx, nodeWright(), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(hadLegacy).To(BeFalse())
		Expect(changed).To(BeFalse())
	})
})
