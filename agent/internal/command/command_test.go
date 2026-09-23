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
	"bytes"
	"io"
	"io/fs"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCommand(t *testing.T) {
	// Command execution tests re-enter this binary as a controlled subprocess.
	// Helper invocations exit before registering and running the Ginkgo suite.
	if runCommandTestHelper() {
		return
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Command Suite")
}

var _ = Describe("Command constructor", func() {
	It("sets every command default", func() {
		command := NewCommand("test")

		Expect(command).To(Equal(Command{
			Executable:          "test",
			Arguments:           []string{},
			WorkingDirectory:    "",
			Environment:         map[string]string{},
			Stdout:              io.Discard,
			Stderr:              io.Discard,
			Permissions:         0,
			RequiredPermissions: 0,
		}))
	})

	It("applies every option without retaining caller-owned data", func() {
		arguments := []string{"first", "second"}
		environment := map[string]string{"NAME": "value"}
		argumentOption := WithArguments(arguments...)
		environmentOption := WithEnvironment(environment)
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}

		command := NewCommand(
			"test",
			argumentOption,
			WithWorkingDirectory("/work"),
			environmentOption,
			WithStdout(stdout),
			WithStderr(stderr),
			WithPermissions(0o700),
		)

		arguments[0] = "changed"
		environment["NAME"] = "changed"
		Expect(command.Arguments).To(Equal([]string{"first", "second"}))
		Expect(command.WorkingDirectory).To(Equal("/work"))
		Expect(command.Environment).To(Equal(map[string]string{"NAME": "value"}))
		Expect(command.Stdout).To(BeIdenticalTo(stdout))
		Expect(command.Stderr).To(BeIdenticalTo(stderr))
		Expect(command.Permissions).To(Equal(fs.FileMode(0o700)))
		Expect(NewCommand("/test", WithRequiredPermissions(0o111)).RequiredPermissions).To(
			Equal(fs.FileMode(0o111)),
		)

		second := NewCommand("test", argumentOption, environmentOption)
		command.Arguments[0] = "mutated"
		command.Environment["NAME"] = "mutated"
		Expect(second.Arguments).To(Equal([]string{"first", "second"}))
		Expect(second.Environment).To(Equal(map[string]string{"NAME": "value"}))
	})
})

var _ = Describe("Command validation", func() {
	DescribeTable("rejects invalid fields",
		func(command Command, expected string) {
			Expect(command.validate()).To(MatchError(ContainSubstring(expected)))
		},
		Entry("empty executable", Command{}, "command executable is empty"),
		Entry(
			"non-permission mode bits",
			Command{Executable: "/test", Permissions: fs.ModeDir | 0o755},
			"permissions contain non-permission mode bits",
		),
		Entry(
			"non-permission required mode bits",
			Command{Executable: "/test", RequiredPermissions: fs.ModeDir | 0o111},
			"required permissions contain non-permission mode bits",
		),
		Entry(
			"conflicting permission policies",
			Command{Executable: "/test", Permissions: 0o700, RequiredPermissions: 0o111},
			"mutually exclusive",
		),
		Entry(
			"relative executable with permissions",
			Command{Executable: "script", Permissions: 0o755},
			"permissions require an absolute executable path",
		),
		Entry("NUL executable", Command{Executable: "bad\x00name"}, "executable contains a NUL byte"),
		Entry(
			"NUL argument",
			Command{Executable: "/test", Arguments: []string{"bad\x00value"}},
			"argument 0 contains a NUL byte",
		),
		Entry(
			"NUL working directory",
			Command{Executable: "/test", WorkingDirectory: "/bad\x00directory"},
			"working directory contains a NUL byte",
		),
		Entry(
			"relative working directory",
			Command{Executable: "/test", WorkingDirectory: "tmp"},
			"working directory \"tmp\" is not absolute",
		),
		Entry(
			"empty environment name",
			Command{Executable: "/test", Environment: map[string]string{"": "value"}},
			"environment contains an empty name",
		),
		Entry(
			"environment name containing equals",
			Command{Executable: "/test", Environment: map[string]string{"BAD=NAME": "value"}},
			"environment name \"BAD=NAME\" contains '='",
		),
		Entry(
			"NUL environment name",
			Command{Executable: "/test", Environment: map[string]string{"BAD\x00NAME": "value"}},
			"environment variable \"BAD\\x00NAME\" contains a NUL byte",
		),
		Entry(
			"NUL environment value",
			Command{Executable: "/test", Environment: map[string]string{"NAME": "bad\x00value"}},
			"environment variable \"NAME\" contains a NUL byte",
		),
	)

	It("reports invalid environment variables in name order", func() {
		command := Command{
			Executable: "test",
			Environment: map[string]string{
				"BAD=NAME": "value",
				"":         "value",
			},
		}

		err := command.validate()

		Expect(err).To(MatchError("command environment contains an empty name"))
	})
})
