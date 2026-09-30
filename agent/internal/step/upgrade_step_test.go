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
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("NewUpgradeStep", func() {
	It("succeeds when no arguments are supplied", func() {
		u, err := NewUpgradeStep("upgrade.sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(u.ScriptPath).To(Equal("upgrade.sh"))
	})

	It("rejects WithArguments at construction", func() {
		_, err := NewUpgradeStep(
			"upgrade.sh",
			WithName("upgrade"),
			WithArguments([]string{"-x", "foo"}),
		)
		Expect(err).To(MatchError(
			ContainSubstring("UpgradeStep upgrade can not have any arguments, but found: [-x foo]"),
		))
	})

	It("accepts the other options unchanged", func() {
		u, err := NewUpgradeStep(
			"upgrade.sh",
			WithName("custom"),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithEnv(map[string]string{"K": "V"}),
			WithOnHost(false),
			WithIdempotence(Disabled),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(u.Name).To(Equal("custom"))
		Expect(u.Returncodes).To(Equal([]command.ExitCode{0, 2}))
		Expect(u.Env).To(Equal(map[string]string{"K": "V"}))
		Expect(u.OnHost).To(BeFalse())
		Expect(u.Idempotence()).To(Equal(Disabled))
	})
})

var _ = Describe("UpgradeStep.Validate", func() {
	It("passes when arguments are empty", func() {
		u := UpgradeStep{ScriptPath: "upgrade.sh"}
		Expect(u.Validate()).To(Succeed())
	})

	It("rejects directly-constructed values with non-empty arguments", func() {
		u := UpgradeStep{
			Name:       "upgrade",
			ScriptPath: "upgrade.sh",
			Arguments:  []string{"x"},
		}
		Expect(u.Validate()).To(MatchError(
			"UpgradeStep upgrade can not have any arguments, but found: [x]",
		))
	})
})

var _ = Describe("UpgradeStep.Run", func() {
	It("rejects invalid arguments before running the regular step", func() {
		u := UpgradeStep{
			Name:       "upgrade",
			ScriptPath: "upgrade.sh",
			Arguments:  []string{"x"},
		}

		status, err := u.Run(context.Background(), execution.Config{})

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(
			ContainSubstring("UpgradeStep upgrade can not have any arguments, but found: [x]"),
		))
	})

	It("requires orchestration to provide package versions", func() {
		u, err := NewUpgradeStep("upgrade.sh")
		Expect(err).NotTo(HaveOccurred())

		status, err := u.Run(context.Background(), execution.Config{})

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running upgrade step: versions were not provided"))
	})

	DescribeTable(
		"rejects empty package versions",
		func(previous, current, message string) {
			u, err := NewUpgradeStep("upgrade.sh")
			Expect(err).NotTo(HaveOccurred())

			status, err := u.WithVersions(previous, current).Run(
				context.Background(),
				execution.Config{},
			)

			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(message))
		},
		Entry("missing previous version", "", "current", "running upgrade step: previous version must not be empty"),
		Entry("missing current version", "previous", "", "running upgrade step: current version must not be empty"),
	)

	It("runs package versions without treating them as configured arguments", func() {
		stepRoot := GinkgoT().TempDir()
		skyhookDir := GinkgoT().TempDir()
		executable := filepath.Join(stepRoot, "upgrade.sh")
		Expect(os.WriteFile(executable, []byte(`#!/bin/sh
printf 'arguments=%s,%s\n' "$1" "$2"
printf 'environment=%s,%s\n' "$PREVIOUS_VERSION" "$CURRENT_VERSION"
`), 0o700)).To(Succeed())
		stdout := &bytes.Buffer{}
		u, err := NewUpgradeStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithEnv(map[string]string{
				previousVersionEnv: "configured-previous",
				currentVersionEnv:  "configured-current",
			}),
		)
		Expect(err).NotTo(HaveOccurred())
		runnable := u.WithVersions("previous", "current")
		Expect(runnable.ExecutionMetadata().Arguments).To(Equal([]string{"previous", "current"}))
		Expect(u.ExecutionMetadata().Arguments).To(BeEmpty())

		status, err := runnable.Run(
			context.Background(),
			newStepRunConfig(stepRoot, skyhookDir, execution.WithRunOutput(stdout, io.Discard)),
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(stdout.String()).To(ContainSubstring("arguments=previous,current"))
		Expect(stdout.String()).To(ContainSubstring("environment=previous,current"))
		Expect(u.Validate()).To(Succeed())
		dumped, err := runnable.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).To(ContainSubstring(`"arguments":[]`))
		Expect(string(dumped)).To(ContainSubstring(`"CURRENT_VERSION":"configured-current"`))
		Expect(string(dumped)).To(ContainSubstring(`"PREVIOUS_VERSION":"configured-previous"`))
		Expect(string(dumped)).NotTo(ContainSubstring(`"CURRENT_VERSION":"current"`))
		Expect(string(dumped)).NotTo(ContainSubstring(`"PREVIOUS_VERSION":"previous"`))
	})
})

var _ = Describe("UpgradeStep fields", func() {
	It("exposes the shared command fields", func() {
		u := UpgradeStep{
			Name:            "explicit",
			ScriptPath:      "upgrade.sh",
			Returncodes:     []command.ExitCode{command.SuccessExitCode},
			Env:             map[string]string{"K": "V"},
			OnHost:          true,
			IdempotenceMode: Disabled,
		}

		Expect(u.Name).To(Equal("explicit"))
		Expect(u.ScriptPath).To(Equal("upgrade.sh"))
		Expect(u.Returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(u.Env).To(Equal(map[string]string{"K": "V"}))
		Expect(u.OnHost).To(BeTrue())
		Expect(u.Idempotence()).To(Equal(Disabled))
	})
})

var _ = Describe("UpgradeStep.Encode", func() {
	It("emits upgrade_step:true", func() {
		u, err := NewUpgradeStep("upgrade.sh")
		Expect(err).NotTo(HaveOccurred())

		dumped, err := u.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).To(ContainSubstring(`"upgrade_step":true`))
	})

	It("forces upgrade_step:true for a directly-constructed value", func() {
		u := UpgradeStep{ScriptPath: "upgrade.sh"}
		dumped, err := u.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).To(ContainSubstring(`"upgrade_step":true`))
	})

	It("returns the invariant error before marshaling when arguments are set", func() {
		u := UpgradeStep{
			Name:       "upgrade",
			ScriptPath: "upgrade.sh",
			Arguments:  []string{"x"},
		}
		_, err := u.Encode()
		Expect(err).To(MatchError(
			ContainSubstring("UpgradeStep upgrade can not have any arguments, but found: [x]"),
		))
	})

	It("produces wire bytes that differ from RegularStep only in upgrade_step", func() {
		options := []Option{
			WithName("shared"),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithOnHost(false),
			WithIdempotence(Disabled),
			WithEnv(map[string]string{"KEY": "value"}),
			WithRequiresInterrupt(true),
		}
		rs := NewRegularStep("shared.sh", options...)
		u, err := NewUpgradeStep("shared.sh", options...)
		Expect(err).NotTo(HaveOccurred())

		rsBytes, err := rs.Encode()
		Expect(err).NotTo(HaveOccurred())

		uBytes, err := u.Encode()
		Expect(err).NotTo(HaveOccurred())

		expected := strings.Replace(string(rsBytes), `"upgrade_step":false`, `"upgrade_step":true`, 1)
		Expect(string(uBytes)).To(Equal(expected))
	})
})
