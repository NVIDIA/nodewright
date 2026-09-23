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

package command

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeRunner struct {
	command Command
	result  Result
	err     error
}

var _ Runner = (*fakeRunner)(nil)

func (f *fakeRunner) Run(_ context.Context, command Command) (Result, error) {
	f.command = command
	return f.result, f.err
}

var _ = Describe("Runner", func() {
	It("uses the os/exec signal sentinel", func() {
		Expect(SignalExitCode).To(Equal(ExitCode(-1)))
	})

	It("constructs a non-nil interface implementation", func() {
		runner := NewRunner()
		Expect(runner).NotTo(BeNil())
	})

	It("applies options to shared configuration in order", func() {
		var observedChroot string
		observeChroot := func(config *runnerConfig) {
			if config.chroot != nil {
				observedChroot = *config.chroot
			}
		}

		runner := NewRunner(WithChroot("/target"), observeChroot)

		Expect(runner).NotTo(BeNil())
		Expect(observedChroot).To(Equal("/target"))
	})

	It("permits downstream substitution through the interface", func() {
		command := NewCommand("test")
		fake := &fakeRunner{result: Result{ExitCode: 7}, err: errors.New("failed")}

		result, err := fake.Run(context.Background(), command)

		Expect(result).To(Equal(Result{ExitCode: 7}))
		Expect(err).To(MatchError("failed"))
		Expect(fake.command).To(Equal(command))
	})
})
