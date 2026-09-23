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
	"os"
)

// ExitCode is a process exit status.
type ExitCode int

const (
	// SuccessExitCode indicates that the process exited successfully.
	SuccessExitCode ExitCode = 0
	// SignalExitCode indicates that a signal terminated the process. Result.Signal
	// identifies the signal.
	SignalExitCode ExitCode = -1
)

// Result reports how a started process completed.
//
// A nonzero ExitCode does not by itself cause Runner.Run to return an error.
type Result struct {
	// ExitCode uses os.ProcessState.ExitCode semantics. It contains the process
	// exit status, or SignalExitCode when a signal terminated the process.
	ExitCode ExitCode

	// Signal identifies the signal that terminated the process. It is nil when
	// the process exited normally.
	Signal os.Signal
}

// Runner executes commands and copies their output to caller-provided writers.
//
// Errors report failures in validation, path resolution, permission changes,
// process setup, cancellation, output copying, or waiting. Once a process
// starts and exits normally, its status is returned through Result even when
// that status is nonzero.
type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type runnerConfig struct {
	chroot *string
}

// RunnerOption configures a Runner created by NewRunner.
type RunnerOption func(*runnerConfig)

// NewRunner returns a Runner for the requested execution environment. With no
// options, commands execute against the caller's normal filesystem.
func NewRunner(options ...RunnerOption) Runner {
	config := runnerConfig{
		chroot: nil,
	}

	for _, option := range options {
		option(&config)
	}

	if config.chroot != nil {
		return &chrootCommandRunner{root: *config.chroot}
	}
	return &commandRunner{}
}

// WithChroot configures a Runner to execute every command inside root.
func WithChroot(root string) RunnerOption {
	return func(config *runnerConfig) {
		config.chroot = &root
	}
}
