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

package interrupts

import (
	"context"
	"errors"
	"syscall"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("RestartAllServices", func() {
	It("uses the restart_all_services wire type", func() {
		Expect(RestartAllServices{}.Type()).To(Equal(RestartAllServicesType))
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := RestartAllServices{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = RestartAllServices{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = RestartAllServices{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})

	Describe("with a runner", func() {
		It("force-reloads procps and marks it complete", func() {
			runner, ran := recordingRunner(command.Result{ExitCode: command.SuccessExitCode})
			markCompleted, marked := recordMarks()

			status, err := RestartAllServices{}.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), []bool{false}, markCompleted)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(ran()).To(Equal([]string{"service procps force-reload"}))
			Expect(marked()).To(Equal([]int{0}))
		})

		It("runs nothing when the reload is already marked complete", func() {
			runner, ran := recordingRunner()
			markCompleted, marked := recordMarks()

			status, err := RestartAllServices{}.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), []bool{true}, markCompleted)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(ran()).To(BeEmpty())
			Expect(marked()).To(BeEmpty())
		})

		DescribeTable("fails without marking the reload complete",
			func(result command.Result) {
				runner, _ := recordingRunner(result)
				markCompleted, marked := recordMarks()

				status, err := RestartAllServices{}.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), []bool{false}, markCompleted)
				Expect(err).NotTo(HaveOccurred())
				Expect(status).To(Equal(execution.StatusFailed))
				Expect(marked()).To(BeEmpty())
			},
			Entry("on a non-zero exit", command.Result{ExitCode: 1}),
			Entry("on a signal", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGKILL}),
		)

		It("fails when marking the reload complete fails", func() {
			runner, _ := recordingRunner(command.Result{ExitCode: command.SuccessExitCode})
			markErr := errors.New("history unavailable")

			status, err := RestartAllServices{}.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), []bool{false}, func(int) error { return markErr })
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(markErr))
		})

		It("fails on a runner error without marking anything", func() {
			runErr := errors.New("exec failed")
			runner := commandmock.NewMockRunner(GinkgoT())
			runner.EXPECT().Run(mock.Anything, mock.Anything).Return(command.Result{}, runErr).Once()
			markCompleted, marked := recordMarks()

			status, err := RestartAllServices{}.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), []bool{false}, markCompleted)
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(runErr))
			Expect(marked()).To(BeEmpty())
		})
	})
})
