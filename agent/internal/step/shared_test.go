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

package step

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("shared step behavior", func() {
	It("applies every construction option and the remaining defaults", func() {
		configured := newStepOptions(
			"step.sh",
			WithName("custom"),
			WithArguments([]string{"argument"}),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithEnv(map[string]string{"KEY": "value"}),
			WithOnHost(false),
			WithIdempotence(Disabled),
			WithRequiresInterrupt(true),
		)

		Expect(configured.name).To(Equal("custom"))
		Expect(configured.arguments).To(Equal([]string{"argument"}))
		Expect(configured.returncodes).To(Equal([]command.ExitCode{0, 2}))
		Expect(configured.env).To(Equal(map[string]string{"KEY": "value"}))
		Expect(configured.onHost).To(BeFalse())
		Expect(configured.idempotence).To(Equal(Disabled))
		Expect(configured.requiresInterrupt).To(BeTrue())

		defaults := newStepOptions("step.sh")
		Expect(defaults.name).To(Equal("step.sh"))
		Expect(defaults.arguments).To(Equal([]string{}))
		Expect(defaults.returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(defaults.env).To(Equal(map[string]string{}))
		Expect(defaults.onHost).To(BeTrue())
		Expect(defaults.idempotence).To(Equal(Auto))
	})

	It("normalizes nil and empty values when fingerprinting", func() {
		nilFingerprint, err := stepFingerprint("step.sh", nil, nil, nil, false)
		Expect(err).NotTo(HaveOccurred())

		emptyFingerprint, err := stepFingerprint(
			"step.sh",
			[]string{},
			[]command.ExitCode{},
			map[string]string{},
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(nilFingerprint).To(Equal(emptyFingerprint))

		changedFingerprint, err := stepFingerprint(
			"other.sh",
			[]string{},
			[]command.ExitCode{},
			map[string]string{},
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(changedFingerprint).NotTo(Equal(emptyFingerprint))
	})

	It("resolves environment arguments and reports each missing variable once", func() {
		const (
			present = "NODEWRIGHT_SHARED_STEP_PRESENT"
			missing = "NODEWRIGHT_SHARED_STEP_MISSING"
		)
		GinkgoT().Setenv(present, "resolved")
		previous, existed := os.LookupEnv(missing)
		Expect(os.Unsetenv(missing)).To(Succeed())
		DeferCleanup(func() {
			if existed {
				Expect(os.Setenv(missing, previous)).To(Succeed())
			}
		})

		arguments, missingVariables := resolveArguments([]string{
			"literal",
			"env:" + present,
			"env:" + missing,
			"env:" + missing,
		})

		Expect(arguments).To(Equal([]string{
			"literal",
			"resolved",
			"env:" + missing,
			"env:" + missing,
		}))
		Expect(missingVariables).To(Equal([]string{missing}))
	})

	It("wraps JSON encoding failures", func() {
		_, err := encodeStep(make(chan struct{}), Auto)
		Expect(err).To(MatchError(ContainSubstring("marshal step:")))
	})

	It("allocates a nil command environment before adding runtime values", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()

		status, err := runStep(
			context.Background(),
			newStepRunConfig(stepRoot, skyhookDir),
			filepath.Base(executable),
			[]string{"-test.run=^TestStep$", "--", "inspect"},
			[]command.ExitCode{command.SuccessExitCode},
			false,
			nil,
			&stepVersions{previous: "1.0.0", current: "2.0.0"},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("pins the step fingerprint, which is the on-disk flag filename", func() {
		// The digest names the completion flag, so a change to the payload re-runs
		// every step already done on every node after an upgrade. These values were
		// computed independently of stepFingerprint (SHA-256 over the compact JSON
		// payload) and must only change together with a deliberate flag migration.
		const (
			populated = "66667bef91535784cccda0d2279bcf13ff9402d8e10b25ceb0b896ef2c114296"
			empty     = "3dbdc6fc2970058ede907125aabb993ba94369faa3d4d654a5276d79584db055"
		)

		got, err := stepFingerprint(
			"step.sh",
			[]string{"--flag", "value"},
			[]command.ExitCode{0, 2},
			map[string]string{"B": "2", "A": "1"},
			true,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(populated))

		got, err = stepFingerprint("step.sh", nil, nil, nil, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(empty))

		fromStep, err := RegularStep{
			ScriptPath:  "step.sh",
			Arguments:   []string{"--flag", "value"},
			Returncodes: []command.ExitCode{0, 2},
			Env:         map[string]string{"A": "1", "B": "2"},
			OnHost:      true,
		}.Fingerprint()
		Expect(err).NotTo(HaveOccurred())
		Expect(fromStep).To(Equal(populated))
	})

	DescribeTable("runs the command on the runner that on_host selects",
		func(onHost bool) {
			// WithChroot is the only option a step runner is built with, so one
			// option means the chroot runner and none means the plain one. A mock
			// built with GinkgoT fails the spec if the wrong runner is ever run.
			chroot := commandmock.NewMockRunner(GinkgoT())
			plain := commandmock.NewMockRunner(GinkgoT())
			selected := plain
			if onHost {
				selected = chroot
			}
			selected.EXPECT().Run(mock.Anything, mock.Anything).
				Return(command.Result{ExitCode: command.SuccessExitCode}, nil).
				Once()
			config, err := execution.NewConfig(
				execution.WithRootMount("/host"),
				execution.WithStepRoot("/steps"),
				execution.WithSkyhookDir("/package"),
				execution.WithRunOutput(io.Discard, io.Discard),
				execution.WithRunnerFactory(func(options ...command.RunnerOption) command.Runner {
					if len(options) == 1 {
						return chroot
					}
					return plain
				}),
			)
			Expect(err).NotTo(HaveOccurred())

			status, err := runStep(
				context.Background(),
				config,
				"step.sh",
				nil,
				[]command.ExitCode{command.SuccessExitCode},
				onHost,
				nil,
				nil,
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
		},
		Entry("on_host: true runs inside the host root", true),
		Entry("on_host: false runs in the agent container", false),
	)
})
