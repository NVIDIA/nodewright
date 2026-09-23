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

package execution

import (
	"bytes"
	"io"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestExecution(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Execution Suite")
}

var _ = Describe("Config", func() {
	It("constructs a readable immutable execution configuration", func() {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		config, err := NewConfig(
			WithRootMount("/host"),
			WithStepRoot("/package/steps"),
			WithSkyhookDir("/package"),
			WithRunOutput(stdout, stderr),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.RootMount()).To(Equal("/host"))
		Expect(config.StepRoot()).To(Equal("/package/steps"))
		Expect(config.SkyhookDir()).To(Equal("/package"))
		Expect(config.Stdout()).To(BeIdenticalTo(stdout))
		Expect(config.Stderr()).To(BeIdenticalTo(stderr))
	})

	DescribeTable("rejects non-absolute execution paths",
		func(options []Option, expected string) {
			_, err := NewConfig(options...)
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("root mount", []Option{
			WithRootMount("host"), WithStepRoot("/steps"), WithSkyhookDir("/package"),
		}, `root mount "host" is not absolute`),
		Entry("step root", []Option{
			WithRootMount("/host"), WithStepRoot("steps"), WithSkyhookDir("/package"),
		}, `step root "steps" is not absolute`),
		Entry("skyhook directory", []Option{
			WithRootMount("/host"), WithStepRoot("/steps"), WithSkyhookDir("package"),
		}, `skyhook directory "package" is not absolute`),
	)

	It("treats nil output writers as discarded streams", func() {
		config, err := NewConfig(
			WithRootMount("/host"),
			WithStepRoot("/steps"),
			WithSkyhookDir("/package"),
			WithRunOutput(nil, nil),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Stdout()).To(Equal(io.Discard))
		Expect(config.Stderr()).To(Equal(io.Discard))
	})

	It("rejects its zero value", func() {
		Expect(Config{}.Validate()).To(MatchError(ContainSubstring("root mount")))
	})
})

var _ = Describe("Status", func() {
	It("uses stable success and failure values", func() {
		Expect(StatusSuccess).To(Equal(Status("success")))
		Expect(StatusFailed).To(Equal(Status("failed")))
	})
})
