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

package stage

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStage(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Stage Suite")
}

var _ = Describe("Stage", func() {
	It("treats Interrupt as a non-step stage", func() {
		Expect(IsStepStage(Interrupt)).To(BeFalse())
	})

	It("treats every apply/check pair as a step stage", func() {
		for apply, check := range ApplyToCheck {
			Expect(IsStepStage(apply)).To(BeTrue(), "expected %s to be a step stage", apply)
			Expect(IsStepStage(check)).To(BeTrue(), "expected %s to be a step stage", check)
		}
	})

	It("locks Interrupt as the only known non-step stage", func() {
		Expect(nonStepStages).To(HaveLen(1))
		Expect(nonStepStages).To(HaveKey(Interrupt))
	})
})

var _ = Describe("ParseStage", func() {
	It("accepts every known stage", func() {
		// An explicit list (not the All registry) so a stage const that is
		// added but never registered with ParseStage is caught as drift.
		for _, s := range []Stage{
			Uninstall, UninstallCheck,
			Upgrade, UpgradeCheck,
			Apply, ApplyCheck,
			Config, ConfigCheck,
			Interrupt,
			PostInterrupt, PostInterruptCheck,
		} {
			got, err := ParseStage(string(s))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(s))
		}
	})

	It("rejects an unknown stage", func() {
		_, err := ParseStage("not-a-stage")
		Expect(err).To(MatchError(ContainSubstring("unknown stage")))
	})
})
