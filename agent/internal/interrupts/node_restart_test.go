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
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
})
