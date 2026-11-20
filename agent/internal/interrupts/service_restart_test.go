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
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("ServiceRestart", func() {
	It("uses the service_restart wire type", func() {
		Expect(ServiceRestart{}.Type()).To(Equal(ServiceRestartType))
	})

	It("decodes a service payload into a ServiceRestart value", func() {
		payload := map[string]any{
			"type":     ServiceRestart{}.Type(),
			"services": []string{"containerd", "kubelet"},
		}
		data, err := json.Marshal(payload)
		Expect(err).NotTo(HaveOccurred())

		interrupt, err := Decode(base64.StdEncoding.EncodeToString(data))
		Expect(err).NotTo(HaveOccurred())
		Expect(interrupt).To(Equal(ServiceRestart{Services: []string{"containerd", "kubelet"}}))
	})

	It("rejects a payload whose services field is the wrong type", func() {
		encoded := base64.StdEncoding.EncodeToString([]byte(`{"type":"service_restart","services":"kubelet"}`))

		_, err := Decode(encoded)
		Expect(err).To(MatchError(errInvalidSerializedInterrupt))
	})

	It("emits an empty services array when Services is nil, not null", func() {
		data, err := ServiceRestart{}.Serialize()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"services":[]`))
		Expect(string(data)).NotTo(ContainSubstring("null"))
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := ServiceRestart{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = ServiceRestart{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = ServiceRestart{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})

	Describe("with a runner", func() {
		succeeded := command.Result{ExitCode: command.SuccessExitCode}
		failed := command.Result{ExitCode: 1}
		services := ServiceRestart{Services: []string{"a", "b"}}

		It("reloads systemd, then restarts each service in order, marking each command complete", func() {
			runner, ran := recordingRunner(succeeded, succeeded, succeeded)
			markCompleted, marked := recordMarks()

			status, err := services.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), make([]bool, 3), markCompleted)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(ran()).To(Equal([]string{"systemctl daemon-reload", "systemctl restart a", "systemctl restart b"}))
			Expect(marked()).To(Equal([]int{0, 1, 2}))
		})

		It("stops at a failing command without marking it or running the next", func() {
			runner, ran := recordingRunner(succeeded, failed)
			markCompleted, marked := recordMarks()

			status, err := services.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), make([]bool, 3), markCompleted)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(ran()).To(Equal([]string{"systemctl daemon-reload", "systemctl restart a"}))
			Expect(marked()).To(Equal([]int{0}))
		})

		It("skips commands already marked complete, so a retry resumes after them", func() {
			runner, ran := recordingRunner(succeeded)
			markCompleted, marked := recordMarks()

			status, err := services.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), []bool{true, true, false}, markCompleted)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(execution.StatusSuccess))
			Expect(ran()).To(Equal([]string{"systemctl restart b"}))
			Expect(marked()).To(Equal([]int{2}))
		})

		It("fails when marking a command complete fails, before running the next", func() {
			runner, ran := recordingRunner(succeeded)
			markErr := errors.New("history unavailable")

			status, err := services.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), make([]bool, 3), func(int) error { return markErr })
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(markErr))
			Expect(ran()).To(Equal([]string{"systemctl daemon-reload"}))
		})

		It("fails on a runner error without marking anything", func() {
			runErr := errors.New("exec failed")
			runner := commandmock.NewMockRunner(GinkgoT())
			runner.EXPECT().Run(mock.Anything, mock.Anything).Return(command.Result{}, runErr).Once()
			markCompleted, marked := recordMarks()

			status, err := services.RunPending(context.Background(), newInterruptRunConfigWithRunner(runner), make([]bool, 3), markCompleted)
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(runErr))
			Expect(marked()).To(BeEmpty())
		})
	})
})
