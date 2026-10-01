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
	"strconv"
	"time"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Mirror controller", func() {

	// The mirror reconcile + create round-trip through the shared envtest manager
	// can exceed Gomega's 1s default on the first invocation, so widen the polling
	// windows for this Describe only and restore Gomega's defaults afterwards.
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(10 * time.Second)
		SetDefaultConsistentlyDuration(2 * time.Second)
	})
	AfterEach(func() {
		SetDefaultEventuallyTimeout(time.Second)
		SetDefaultConsistentlyDuration(100 * time.Millisecond)
	})

	newLegacySkyhook := func(name string, percent int) *v1alpha1.Skyhook {
		return &v1alpha1.Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.SkyhookSpec{
				InterruptionBudget: v1alpha1.InterruptionBudget{Percent: ptr[int](percent)},
				Packages: v1alpha1.Packages{
					"test-package": v1alpha1.Package{
						PackageRef: v1alpha1.PackageRef{Name: "test-package", Version: "1.0.0"},
						Image:      "alpine:3.21.0",
					},
				},
			},
		}
	}

	// deleteLegacyAndMirror deletes a legacy Skyhook and then the NodeWright of the same name,
	// waiting until both are gone. The mirror never deletes its target, and a leftover one fails
	// the shared heavy pass for every later spec: the fixture's inline image tag fails validation.
	// The target watch rebuilds a deleted NodeWright for as long as the mirror still reads the
	// legacy object, so wait for it to leave the manager's cache (read through operator), not
	// just the apiserver. Then strip any finalizer that would hold the NodeWright Terminating.
	deleteLegacyAndMirror := func(legacy *v1alpha1.Skyhook) {
		const timeout = 10 * time.Second
		key := client.ObjectKeyFromObject(legacy)
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, legacy))).To(Succeed())
		Eventually(func() error {
			return operator.Get(ctx, key, &v1alpha1.Skyhook{})
		}).WithTimeout(timeout).Should(Satisfy(apierrors.IsNotFound))

		Eventually(func(g Gomega) {
			nw := &nwv1.NodeWright{}
			err := k8sClient.Get(ctx, key, nw)
			if apierrors.IsNotFound(err) {
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, nw))).To(Succeed())
			if len(nw.Finalizers) > 0 {
				patch := client.MergeFrom(nw.DeepCopy())
				nw.Finalizers = nil
				g.Expect(client.IgnoreNotFound(k8sClient.Patch(ctx, nw, patch))).To(Succeed())
			}
			g.Expect(k8sClient.Get(ctx, key, &nwv1.NodeWright{})).To(Satisfy(apierrors.IsNotFound))
		}).WithTimeout(timeout).Should(Succeed())
	}

	newLegacyDeploymentPolicy := func(name string, count int) *v1alpha1.DeploymentPolicy {
		return &v1alpha1.DeploymentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.DeploymentPolicySpec{
				Default: v1alpha1.PolicyDefault{
					Budget: v1alpha1.DeploymentBudget{Count: ptr[int](count)},
					Strategy: &v1alpha1.DeploymentStrategy{
						Fixed: &v1alpha1.FixedStrategy{InitialBatch: ptr[int](1)},
					},
				},
			},
		}
	}

	Context("Skyhook -> NodeWright", func() {
		It("mirrors a new legacy Skyhook into a stamped NodeWright with equal spec", func() {
			name := "mirror-basic"
			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			nw := &nwv1.NodeWright{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
				g.Expect(nw.Annotations).To(HaveKeyWithValue(MirroredFromAnnotation, name))
				g.Expect(nw.Annotations).To(HaveKey(MirroredGenerationAnnotation))
			}).Should(Succeed())

			want := &nwv1.NodeWright{}
			Expect(v1alpha1.Convert_Skyhook_To_NodeWright(legacy, want)).To(Succeed())
			Expect(nw.Spec).To(Equal(want.Spec))
		})

		It("does not overwrite a NodeWright that is not mirror-owned (back off)", func() {
			name := "owned"
			// Pre-create a user/Argo-managed NodeWright WITHOUT the mirror stamp.
			userNW := &nwv1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: nwv1.NodeWrightSpec{
					InterruptionBudget: nwv1.InterruptionBudget{Percent: ptr[int](99)},
					Packages: nwv1.Packages{
						"user-package": nwv1.Package{
							PackageRef: nwv1.PackageRef{Name: "user-package", Version: "9.9.9"},
							Image:      "alpine:3.21.0",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, userNW)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, userNW) })

			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			// Give the mirror time to (not) act, then assert the user object is untouched.
			Consistently(func(g Gomega) {
				got := &nwv1.NodeWright{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
				g.Expect(got.Annotations).NotTo(HaveKey(MirroredFromAnnotation))
				g.Expect(got.Spec.Packages).To(HaveKey("user-package"))
				g.Expect(got.Spec.Packages).NotTo(HaveKey("test-package"))
			}).Should(Succeed())
		})

		It("propagates legacy spec updates and advances the generation stamp", func() {
			name := "mirror-update"
			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			nw := &nwv1.NodeWright{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)
			}).Should(Succeed())
			origStamp := nw.Annotations[MirroredGenerationAnnotation]

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, legacy)).To(Succeed())
			legacy.Spec.InterruptionBudget.Percent = ptr[int](50)
			Expect(k8sClient.Update(ctx, legacy)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &nwv1.NodeWright{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
				g.Expect(got.Spec.InterruptionBudget.Percent).To(Equal(ptr[int](50)))
				g.Expect(got.Annotations[MirroredGenerationAnnotation]).NotTo(Equal(origStamp))
			}).Should(Succeed())
		})

		It("does not rewrite the target in steady state", func() {
			name := "mirror-steady"
			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			nw := &nwv1.NodeWright{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
				// stamp must match the legacy generation, signalling steady state.
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, legacy)).To(Succeed())
				g.Expect(nw.Annotations[MirroredGenerationAnnotation]).To(Equal(strconv.FormatInt(legacy.Generation, 10)))
			}).Should(Succeed())

			rv := nw.ResourceVersion
			Consistently(func(g Gomega) {
				got := &nwv1.NodeWright{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
				g.Expect(got.ResourceVersion).To(Equal(rv))
			}).Should(Succeed())
		})

		It("does not delete the mirrored target when the legacy object is deleted", func() {
			name := "mirror-nodelete"
			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &nwv1.NodeWright{})
			}).Should(Succeed())

			Expect(k8sClient.Delete(ctx, legacy)).To(Succeed())
			Eventually(func() bool {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &v1alpha1.Skyhook{}) != nil
			}).Should(BeTrue())

			Consistently(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &nwv1.NodeWright{})
			}).Should(Succeed())
		})

		It("recreates a mirror-owned target deleted out-of-band (target watch)", func() {
			name := "mirror-target-readd"
			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			nw := &nwv1.NodeWright{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
				g.Expect(nw.Annotations).To(HaveKeyWithValue(MirroredFromAnnotation, name))
			}).Should(Succeed())
			origUID := nw.UID

			// Delete the mirror-owned target out-of-band while the legacy source stays
			// put. Post-migration the legacy spec is frozen read-only, so its generation
			// never changes again and the source watch cannot re-fire: only the target
			// watch can re-enqueue and rebuild the bridge (a fresh object, new UID).
			Expect(k8sClient.Delete(ctx, nw)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &nwv1.NodeWright{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
				g.Expect(got.DeletionTimestamp.IsZero()).To(BeTrue())
				g.Expect(got.Annotations).To(HaveKeyWithValue(MirroredFromAnnotation, name))
				g.Expect(got.UID).NotTo(Equal(origUID))
			}).Should(Succeed())
		})

		It("strips the stranded legacy finalizer so a deleted legacy Skyhook can be reaped", func() {
			name := "mirror-finalizer"
			legacy := newLegacySkyhook(name, 25)
			// The pre-rename operator stamped this finalizer; the post-rename operator
			// only manages a finalizer on NodeWright, so without the mirror removing it
			// the delete would block forever.
			legacy.Finalizers = []string{legacySkyhookFinalizer}
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &nwv1.NodeWright{})
			}).Should(Succeed())

			// The finalizer holds the object in Terminating; the mirror must strip it
			// (which also proves the watch predicate delivers the deletion transition).
			Expect(k8sClient.Delete(ctx, legacy)).To(Succeed())
			Eventually(func() bool {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &v1alpha1.Skyhook{}) != nil
			}).Should(BeTrue())
		})

		It("preserves target-only control annotations (pause) across a re-sync", func() {
			name := "mirror-pause"
			legacy := newLegacySkyhook(name, 25)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(deleteLegacyAndMirror, legacy)

			nw := &nwv1.NodeWright{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)
			}).Should(Succeed())

			// Operator/CLI pauses the NodeWright directly: this annotation lives only on
			// the target and is never mirrored from the legacy source.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, nw)).To(Succeed())
				if nw.Annotations == nil {
					nw.Annotations = map[string]string{}
				}
				nw.Annotations[nwv1.METADATA_PREFIX+"/pause"] = "true"
				g.Expect(k8sClient.Update(ctx, nw)).To(Succeed())
			}).Should(Succeed())

			// Editing the legacy spec bumps its generation and forces a mirror re-sync.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, legacy)).To(Succeed())
				legacy.Spec.InterruptionBudget.Percent = ptr[int](50)
				g.Expect(k8sClient.Update(ctx, legacy)).To(Succeed())
			}).Should(Succeed())

			// The re-sync must propagate the spec AND keep the pause annotation.
			Eventually(func(g Gomega) {
				got := &nwv1.NodeWright{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
				g.Expect(got.Spec.InterruptionBudget.Percent).To(Equal(ptr[int](50)))
				g.Expect(got.Annotations).To(HaveKeyWithValue(nwv1.METADATA_PREFIX+"/pause", "true"))
			}).Should(Succeed())
		})
	})

	Context("DeploymentPolicy -> DeploymentPolicy", func() {
		It("mirrors a new legacy DeploymentPolicy into a stamped new-group DeploymentPolicy", func() {
			name := "mirror-dp"
			legacy := newLegacyDeploymentPolicy(name, 3)
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, legacy) })

			dp := &nwv1.DeploymentPolicy{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, dp)).To(Succeed())
				g.Expect(dp.Annotations).To(HaveKeyWithValue(MirroredFromAnnotation, name))
			}).Should(Succeed())

			want := &nwv1.DeploymentPolicy{}
			Expect(v1alpha1.Convert_DeploymentPolicy_To_NodeWright(legacy, want)).To(Succeed())
			Expect(dp.Spec).To(Equal(want.Spec))
		})

		// The read-only invariant is exercised against the DeploymentPolicy source
		// because the SkyhookReconciler (which also runs in this suite) adds a
		// finalizer and status to legacy Skyhook objects, which would mask the
		// mirror's own non-writes. The mirror's read-only core is shared across both
		// kinds, so proving it once here is sufficient. The operator never writes
		// DeploymentPolicy objects, so any change here would be the mirror's.
		It("never writes to the legacy source object", func() {
			name := "mirror-readonly"
			legacy := newLegacyDeploymentPolicy(name, 3)
			legacy.Annotations = map[string]string{"user.example.com/keep": "me"}
			legacy.Finalizers = []string{"user.example.com/finalizer"}
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			DeferCleanup(func() {
				fresh := &v1alpha1.DeploymentPolicy{}
				if k8sClient.Get(ctx, types.NamespacedName{Name: name}, fresh) == nil {
					fresh.Finalizers = nil
					_ = k8sClient.Update(ctx, fresh)
					_ = k8sClient.Delete(ctx, fresh)
				}
			})

			created := &v1alpha1.DeploymentPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, created)).To(Succeed())
			rv := created.ResourceVersion

			// Wait until the mirror has produced the target.
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &nwv1.DeploymentPolicy{})
			}).Should(Succeed())

			Consistently(func(g Gomega) {
				got := &v1alpha1.DeploymentPolicy{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
				g.Expect(got.ResourceVersion).To(Equal(rv))
				g.Expect(got.Annotations).To(Equal(map[string]string{"user.example.com/keep": "me"}))
				g.Expect(got.Finalizers).To(Equal([]string{"user.example.com/finalizer"}))
			}).Should(Succeed())
		})
	})
})
