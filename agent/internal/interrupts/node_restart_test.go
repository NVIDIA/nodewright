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
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("NodeRestart", func() {
	It("uses the node_restart wire type", func() {
		Expect(NodeRestart{}.Type()).To(Equal(NodeRestartType))
	})

	It("recognizes only SIGTERM as reboot completion", func() {
		Expect(nodeRestartCompleted(command.Result{
			ExitCode: command.SignalExitCode,
			Signal:   syscall.SIGTERM,
		})).To(BeTrue())
		Expect(nodeRestartCompleted(command.Result{
			ExitCode: command.SignalExitCode,
			Signal:   syscall.SIGKILL,
		})).To(BeFalse())
		Expect(nodeRestartCompleted(command.Result{ExitCode: 15})).To(BeFalse())
		Expect(nodeRestartCompleted(command.Result{ExitCode: command.SuccessExitCode})).To(BeFalse())
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := NodeRestart{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = NodeRestart{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = NodeRestart{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})

	It("reports when an enqueued reboot does not restart the host", func() {
		err := waitForNodeRestart(context.Background(), time.Millisecond)

		Expect(err).To(MatchError(
			"reboot was enqueued but the host did not restart within 1ms",
		))
	})

	It("stops waiting for a host restart when execution is canceled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := waitForNodeRestart(ctx, time.Hour)

		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	Describe("with a runner", func() {
		It("counts reboot ending in SIGTERM as success", func() {
			runner, ran := recordingRunner(command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM})

			status, err := NodeRestart{}.Run(context.Background(), newInterruptRunConfigWithRunner(runner))
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(ran()).To(Equal([]string{"reboot"}))
		})

		DescribeTable("fails without waiting for a restart",
			func(result command.Result) {
				runner, _ := recordingRunner(result)

				status, err := NodeRestart{}.Run(context.Background(), newInterruptRunConfigWithRunner(runner))
				Expect(err).NotTo(HaveOccurred())
				Expect(status).To(Equal(execution.StatusFailed))
			},
			Entry("on a non-zero exit", command.Result{ExitCode: 1}),
			Entry("on a signal other than SIGTERM", command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGKILL}),
		)

		It("fails on a runner error", func() {
			runErr := errors.New("exec failed")
			runner := commandmock.NewMockRunner(GinkgoT())
			runner.EXPECT().Run(mock.Anything, mock.Anything).Return(command.Result{}, runErr).Once()

			status, err := NodeRestart{}.Run(context.Background(), newInterruptRunConfigWithRunner(runner))
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(runErr))
		})
	})
})
