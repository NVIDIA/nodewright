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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("interrupt chroot execution", func() {
	It("executes every concrete command interrupt against the target root", func() {
		if os.Geteuid() != 0 {
			Fail("chroot execution requires root privileges")
		}

		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, "package"), 0o755)).To(Succeed())
		commandDirectory := firstAbsolutePathDirectory()
		Expect(commandDirectory).NotTo(BeEmpty())
		hostCommandDirectory := filepath.Join(root, strings.TrimPrefix(commandDirectory, string(filepath.Separator)))
		Expect(os.MkdirAll(hostCommandDirectory, 0o755)).To(Succeed())
		testExecutable, err := filepath.Abs(os.Args[0])
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(testExecutable)
		Expect(err).NotTo(HaveOccurred())
		for _, name := range []string{"reboot", "systemctl", "service"} {
			Expect(os.WriteFile(filepath.Join(hostCommandDirectory, name), data, 0o700)).To(Succeed())
		}
		config, err := execution.NewConfig(
			execution.WithRootMount(root),
			execution.WithStepRoot("/steps"),
			execution.WithSkyhookDir("/package"),
			execution.WithRunOutput(io.Discard, io.Discard),
		)
		Expect(err).NotTo(HaveOccurred())

		restartContext, cancelRestart := context.WithTimeout(context.Background(), time.Second)
		status, err := NodeRestart{}.Run(restartContext, config)
		cancelRestart()
		Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
		Expect(status).To(Equal(execution.StatusFailed))

		for _, testCase := range []struct {
			interrupt Interrupt
			status    execution.Status
		}{
			{interrupt: ServiceRestart{Services: []string{"containerd", "kubelet"}}, status: execution.StatusSuccess},
			{interrupt: RestartAllServices{}, status: execution.StatusSuccess},
		} {
			status, err := testCase.interrupt.Run(context.Background(), config)
			Expect(err).NotTo(HaveOccurred(), string(testCase.interrupt.Type()))
			Expect(status).To(Equal(testCase.status), string(testCase.interrupt.Type()))
		}

		calls, err := os.ReadFile(filepath.Join(root, "package", "interrupt-calls"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Split(strings.TrimSpace(string(calls)), "\n")).To(Equal([]string{
			"reboot",
			"systemctl daemon-reload",
			"systemctl restart containerd",
			"systemctl restart kubelet",
			"service procps force-reload",
		}))
	})
})

func firstAbsolutePathDirectory() string {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(directory) {
			return filepath.Clean(directory)
		}
	}
	return ""
}
