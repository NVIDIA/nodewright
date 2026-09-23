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

package config

import (
	"bytes"
	"errors"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configmock "github.com/NVIDIA/nodewright/agent/internal/config/mock"
	"github.com/NVIDIA/nodewright/agent/internal/schema"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

func mustUpgradeStep(path string, opts ...step.Option) step.UpgradeStep {
	u, err := step.NewUpgradeStep(path, opts...)
	Expect(err).NotTo(HaveOccurred())
	return u
}

var _ = Describe("expectedCheckName", func() {
	DescribeTable("derives the check counterpart",
		func(in, want string) { Expect(expectedCheckName(in)).To(Equal(want)) },
		Entry("with extension", "foo.sh", "foo_check.sh"),
		Entry("without extension", "foo", "foo_check"),
		Entry("multi-dot keeps last as ext", "foo.tar.gz", "foo.tar_check.gz"),
		Entry("trailing dot has empty ext", "foo.", "foo_check"),
		Entry("dotted parent dir, no extension", "dir.v1/foo", "dir.v1/foo_check"),
		Entry("dotted parent dir with extension", "dir.v1/foo.sh", "dir.v1/foo_check.sh"),
	)
})

var _ = Describe("validateModes", func() {
	var (
		buf    *bytes.Buffer
		logger *slog.Logger
		tmp    string
	)

	BeforeEach(func() {
		buf = &bytes.Buffer{}
		logger = slog.New(slog.NewTextHandler(buf, nil))
		tmp = GinkgoT().TempDir()
	})

	It("errors when no steps are defined", func() {
		err := validateModes(map[stage.Stage][]step.Step{}, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("no defined steps")))
	})

	It("errors when only check stages are defined", func() {
		steps := map[stage.Stage][]step.Step{
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}
		err := validateModes(steps, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("only check stages")))
	})

	It("treats a declared-but-empty non-check stage as absent", func() {
		steps := map[stage.Stage][]step.Step{
			stage.Apply:      {},
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}
		err := validateModes(steps, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("only check stages")))
	})

	It("rejects a step path that escapes the package root", func() {
		steps := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("../escape.sh")},
			stage.ApplyCheck: {step.NewRegularStep("../escape_check.sh")},
		}
		err := validateModes(steps, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("must be relative to and contained within the package root")))
	})

	It("warns when only an unrelated check stage carries the check name", func() {
		writeStepFiles(tmp, "foo.sh", "apply_check.sh", "foo_check.sh")
		steps := map[stage.Stage][]step.Step{
			stage.Apply:       {step.NewRegularStep("foo.sh")},
			stage.ApplyCheck:  {step.NewRegularStep("apply_check.sh")},
			stage.ConfigCheck: {step.NewRegularStep("foo_check.sh")},
		}
		Expect(validateModes(steps, tmp, logger)).To(Succeed())
		// foo_check.sh lives in config-check, not apply-check, so foo.sh is
		// still unpaired in its own stage.
		Expect(buf.String()).To(ContainSubstring("no corresponding check"))
		Expect(buf.String()).To(ContainSubstring("foo_check.sh"))
	})

	It("errors when an apply stage has no checks", func() {
		steps := map[stage.Stage][]step.Step{
			stage.Apply: {step.NewRegularStep("apply.sh")},
		}
		err := validateModes(steps, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("no corresponding")))
	})

	It("errors when an UpgradeStep is placed outside the upgrade stages", func() {
		steps := map[stage.Stage][]step.Step{
			stage.Apply:      {mustUpgradeStep("up.sh")},
			stage.ApplyCheck: {step.NewRegularStep("up_check.sh")},
		}
		err := validateModes(steps, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("may only appear")))
	})

	It("errors listing every missing step file", func() {
		steps := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("apply.sh")},
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}
		err := validateModes(steps, tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("do not exist")))
		Expect(err).To(MatchError(ContainSubstring("apply.sh")))
		Expect(err).To(MatchError(ContainSubstring("apply_check.sh")))
	})

	It("passes a well-formed set with matching checks and no warnings", func() {
		writeStepFiles(tmp, "apply.sh", "apply_check.sh")
		steps := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("apply.sh")},
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}
		Expect(validateModes(steps, tmp, logger)).To(Succeed())
		Expect(buf.String()).NotTo(ContainSubstring("no corresponding check"))
	})

	It("warns (without failing) when a step has no matching check", func() {
		writeStepFiles(tmp, "foo.sh", "bar.sh")
		steps := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("foo.sh")},
			stage.ApplyCheck: {step.NewRegularStep("bar.sh")},
		}
		Expect(validateModes(steps, tmp, logger)).To(Succeed())
		Expect(buf.String()).To(ContainSubstring("no corresponding check"))
		Expect(buf.String()).To(ContainSubstring("foo_check.sh"))
	})

	It("accepts an UpgradeStep inside the upgrade stages", func() {
		writeStepFiles(tmp, "up.sh", "up_check.sh")
		steps := map[stage.Stage][]step.Step{
			stage.Upgrade:      {mustUpgradeStep("up.sh")},
			stage.UpgradeCheck: {step.NewRegularStep("up_check.sh")},
		}
		Expect(validateModes(steps, tmp, logger)).To(Succeed())
	})
})

var _ = Describe("Loader schema-validation seam", func() {
	It("surfaces the injected validator's error without parsing the document", func() {
		input := []byte(`{"schema_version":"v1"}`)
		sentinel := errors.New("boom from fake validator")
		validator := configmock.NewMockSchemaValidator(GinkgoT())
		validator.EXPECT().
			Validate(input, schema.V1).
			Return(sentinel).
			Once()
		loader := &Loader{validator: validator}

		_, err := loader.Load(input, GinkgoT().TempDir(), nil)
		Expect(err).To(MatchError(sentinel))
	})
})
