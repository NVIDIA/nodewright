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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	skyhookv1 "github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// noMatchReader stands in for the apiserver when the legacy skyhook.nvidia.com CRD is
// not installed: List returns a NoKindMatchError, exactly as a real client does for an
// unserved group. The fake client cannot reproduce this (it returns a scheme
// not-registered error instead), so this narrow stub exercises the real branch.
type noMatchReader struct{ client.Reader }

func (noMatchReader) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "skyhook.nvidia.com", Kind: "Skyhook"}}
}

var _ = Describe("Migration hold", func() {

	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(skyhookv1.AddToScheme(scheme)).To(Succeed())
	})

	mkSkyhook := func(name string, status skyhookv1.Status) *skyhookv1.Skyhook {
		sh := &skyhookv1.Skyhook{ObjectMeta: metav1.ObjectMeta{Name: name}}
		sh.Status.Status = status
		return sh
	}

	build := func(objs ...client.Object) client.Client {
		return newFakeClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	}

	It("holds while a legacy Skyhook is mid-rollout", func() {
		c := build(mkSkyhook("done", skyhookv1.StatusComplete), mkSkyhook("rolling", skyhookv1.StatusInProgress))

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(ConsistOf("rolling"))

		r := &SkyhookReconciler{Client: c}
		hold := r.legacyMigrationHold(ctx)
		Expect(hold).ToNot(BeNil())
		Expect(hold.RequeueAfter).To(Equal(legacyMigrationHoldRequeue))
	})

	It("does not hold when every legacy Skyhook is complete", func() {
		c := build(mkSkyhook("a", skyhookv1.StatusComplete), mkSkyhook("b", skyhookv1.StatusComplete))

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(BeEmpty())

		r := &SkyhookReconciler{Client: c}
		Expect(r.legacyMigrationHold(ctx)).To(BeNil())
	})

	It("ignores legacy Skyhooks with an empty status (never reconciled by the old operator)", func() {
		// A Skyhook created after the upgrade (mirrored into a NodeWright) has no status;
		// it must not wedge the hold open forever.
		c := build(mkSkyhook("fresh", ""))

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(BeEmpty())
	})

	It("does not hold on a fresh cluster with no legacy Skyhooks", func() {
		r := &SkyhookReconciler{Client: build()}
		Expect(r.legacyMigrationHold(ctx)).To(BeNil())
	})

	It("does not hold when the legacy Skyhook CRD is not installed (no-match)", func() {
		// The migration's own end state (legacy CRD removed) and nodewright-only
		// topologies both surface a NoKindMatchError; that means no legacy objects
		// exist, not unknown state, so the operator must not wedge itself.
		inFlight, err := inFlightLegacySkyhooks(ctx, noMatchReader{})
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(BeEmpty())
	})

	It("reports every mid-rollout legacy Skyhook by name", func() {
		c := build(
			mkSkyhook("erroring", skyhookv1.StatusErroring),
			mkSkyhook("progress", skyhookv1.StatusInProgress),
			mkSkyhook("done", skyhookv1.StatusComplete),
		)

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(ConsistOf("erroring", "progress"))
	})

	It("does not hold on paused or disabled Skyhooks (they migrate in that state)", func() {
		// A user must not be forced to unpause/enable a Skyhook to migrate it: the
		// mirror carries the paused/disabled state onto the NodeWright, which then does
		// not roll out, so there is no in-flight work to double-drive.
		c := build(
			mkSkyhook("paused", skyhookv1.StatusPaused),
			mkSkyhook("disabled", skyhookv1.StatusDisabled),
			mkSkyhook("done", skyhookv1.StatusComplete),
		)

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(BeEmpty())

		r := &SkyhookReconciler{Client: c}
		Expect(r.legacyMigrationHold(ctx)).To(BeNil())
	})

	It("holds on blocked and waiting Skyhooks (rollout still in flight)", func() {
		c := build(
			mkSkyhook("blocked", skyhookv1.StatusBlocked),
			mkSkyhook("waiting", skyhookv1.StatusWaiting),
			mkSkyhook("paused", skyhookv1.StatusPaused),
			mkSkyhook("done", skyhookv1.StatusComplete),
		)

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(ConsistOf("blocked", "waiting"))
	})

	It("does not hold on a paused Skyhook whose status is stale (annotation is authoritative)", func() {
		// Pausing just before the upgrade can leave a stale in_progress status because
		// the old operator never wrote "paused". The pause annotation carries onto the
		// NodeWright (which then does not roll out), so it is safe and must not wedge.
		stale := mkSkyhook("stale-paused", skyhookv1.StatusInProgress)
		stale.Annotations = map[string]string{skyhookv1.METADATA_PREFIX + "/pause": "true"}
		c := build(stale, mkSkyhook("done", skyhookv1.StatusComplete))

		inFlight, err := inFlightLegacySkyhooks(ctx, c)
		Expect(err).ToNot(HaveOccurred())
		Expect(inFlight).To(BeEmpty())

		r := &SkyhookReconciler{Client: c}
		Expect(r.legacyMigrationHold(ctx)).To(BeNil())
	})
})
