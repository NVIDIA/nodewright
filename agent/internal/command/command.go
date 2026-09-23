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

// Package command executes operating-system commands with caller-controlled
// output handling and optional runner-level chroot isolation.
//
// Runner copies stdout and stderr concurrently into the writers supplied on
// each Command. The package writes raw bytes without adding timestamps,
// prefixes, or lifecycle policy, and it never closes caller-owned writers.
// Passing a bytes.Buffer captures output in memory; passing os.Stdout or a
// fan-out writer makes output visible as the process runs. Nil writers discard
// their corresponding output.
//
// Paths in WorkingDirectory, Executable, and PATH are interpreted inside the
// runner's chroot when it is constructed with WithChroot. A runner without
// WithChroot executes against the caller's normal filesystem.
//
// Production support is Linux only. The package relies on chroot, Unix process
// groups, and Unix signal semantics and does not provide a Windows implementation.
package command

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Command describes one process invocation.
//
// Command is treated as caller-owned input. Run does not mutate Arguments or
// Environment and does not close Stdout or Stderr.
type Command struct {
	// Executable is either a path or a name to resolve through PATH. The normal
	// runner uses the agent's PATH through os/exec; a chroot runner resolves the
	// name inside its configured root.
	Executable string

	// Arguments are passed to Executable without shell interpretation.
	Arguments []string

	// WorkingDirectory is the absolute directory in which the process starts.
	// It is interpreted inside the runner's execution root. Empty uses the
	// current directory for normal execution and "/" for chroot execution.
	WorkingDirectory string

	// Environment overrides variables inherited by the child process. The map is
	// copied before use. As with os/exec, PATH does not affect normal-runner
	// executable lookup; a chroot runner uses it for target-root lookup.
	Environment map[string]string

	// Stdout and Stderr receive raw process output as it is read. The two writers
	// may be called concurrently, so a shared writer must be concurrency-safe.
	// Nil writers discard their corresponding output.
	Stdout io.Writer
	Stderr io.Writer

	// Permissions replaces the resolved executable's permission bits before
	// starting it. Zero leaves its permissions unchanged. Executable must be
	// absolute when Permissions is nonzero.
	Permissions fs.FileMode

	// RequiredPermissions are added to the resolved executable's existing
	// permission bits before starting it. Executable must be absolute when this
	// field is nonzero.
	RequiredPermissions fs.FileMode
}

// CommandOption configures a Command created by NewCommand.
type CommandOption func(*Command)

// NewCommand returns a Command for executable with no arguments, inherited
// environment, the current working directory, discarded output, and unchanged
// executable permissions. A chrooted runner interprets the empty working
// directory as "/" instead of the caller's current working directory.
func NewCommand(executable string, options ...CommandOption) Command {
	command := Command{
		Executable:          executable,
		Arguments:           []string{},
		WorkingDirectory:    "",
		Environment:         map[string]string{},
		Stdout:              io.Discard,
		Stderr:              io.Discard,
		Permissions:         0,
		RequiredPermissions: 0,
	}
	for _, option := range options {
		option(&command)
	}
	return command
}

// WithArguments sets the arguments passed to the executable.
func WithArguments(arguments ...string) CommandOption {
	arguments = slices.Clone(arguments)
	return func(command *Command) {
		command.Arguments = slices.Clone(arguments)
	}
}

// WithWorkingDirectory sets the command's initial working directory.
func WithWorkingDirectory(directory string) CommandOption {
	return func(command *Command) {
		command.WorkingDirectory = directory
	}
}

// WithEnvironment sets command-specific environment overrides.
func WithEnvironment(environment map[string]string) CommandOption {
	environment = maps.Clone(environment)
	return func(command *Command) {
		command.Environment = maps.Clone(environment)
	}
}

// WithStdout sets the destination for the command's standard output.
func WithStdout(writer io.Writer) CommandOption {
	return func(command *Command) {
		command.Stdout = writer
	}
}

// WithStderr sets the destination for the command's standard error.
func WithStderr(writer io.Writer) CommandOption {
	return func(command *Command) {
		command.Stderr = writer
	}
}

// WithPermissions sets the executable's permission bits before it is run.
func WithPermissions(permissions fs.FileMode) CommandOption {
	return func(command *Command) {
		command.Permissions = permissions
	}
}

// WithRequiredPermissions adds permission bits without clearing the
// executable's existing permissions.
func WithRequiredPermissions(permissions fs.FileMode) CommandOption {
	return func(command *Command) {
		command.RequiredPermissions = permissions
	}
}

func (command Command) validate() error {
	if command.Executable == "" {
		return errors.New("command executable is empty")
	}
	if command.Permissions != command.Permissions.Perm() {
		return errors.New("command permissions contain non-permission mode bits")
	}
	if command.RequiredPermissions != command.RequiredPermissions.Perm() {
		return errors.New("command required permissions contain non-permission mode bits")
	}
	if command.Permissions != 0 && command.RequiredPermissions != 0 {
		return errors.New("command permissions and required permissions are mutually exclusive")
	}
	if command.Permissions|command.RequiredPermissions != 0 && !filepath.IsAbs(command.Executable) {
		return errors.New("command permissions require an absolute executable path")
	}
	if strings.ContainsRune(command.Executable, 0) {
		return errors.New("command executable contains a NUL byte")
	}
	for index, argument := range command.Arguments {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("command argument %d contains a NUL byte", index)
		}
	}
	if command.WorkingDirectory != "" {
		if strings.ContainsRune(command.WorkingDirectory, 0) {
			return errors.New("command working directory contains a NUL byte")
		}
		if !filepath.IsAbs(command.WorkingDirectory) {
			return fmt.Errorf("working directory %q is not absolute", command.WorkingDirectory)
		}
	}
	for _, name := range sortedEnvironmentNames(command.Environment) {
		value := command.Environment[name]
		if name == "" {
			return errors.New("command environment contains an empty name")
		}
		if strings.ContainsRune(name, '=') {
			return fmt.Errorf("command environment name %q contains '='", name)
		}
		if strings.ContainsRune(name, 0) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("command environment variable %q contains a NUL byte", name)
		}
	}
	return nil
}

func sortedEnvironmentNames(environment map[string]string) []string {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
