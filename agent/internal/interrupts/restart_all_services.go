// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package interrupts

import (
	"context"
	"fmt"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
)

// RestartAllServices reloads all services.
type RestartAllServices struct{}

var _ Interrupt = RestartAllServices{}
var _ ResumableInterrupt = RestartAllServices{}

func (RestartAllServices) Type() InterruptType { return RestartAllServicesType }

func (r RestartAllServices) Run(ctx context.Context, config execution.Config) (execution.Status, error) {
	return r.run(ctx, config, nil)
}

// OperationCount reports the single service reload command.
func (RestartAllServices) OperationCount() int { return 1 }

// RunPending skips the command when its indexed completion marker exists.
func (r RestartAllServices) RunPending(
	ctx context.Context,
	config execution.Config,
	completed []bool,
	markCompleted func(int) error,
) (execution.Status, error) {
	if len(completed) != r.OperationCount() {
		return execution.StatusFailed, fmt.Errorf(
			"running interrupt %q: received %d completion states for %d operations",
			r.Type(),
			len(completed),
			r.OperationCount(),
		)
	}
	if markCompleted == nil {
		return execution.StatusFailed, fmt.Errorf("running interrupt %q: completion callback is nil", r.Type())
	}
	if completed[0] {
		return execution.StatusSuccess, nil
	}
	return r.run(ctx, config, markCompleted)
}

func (r RestartAllServices) run(
	ctx context.Context,
	config execution.Config,
	markCompleted func(int) error,
) (execution.Status, error) {
	if err := validateRun(ctx, config, r.Type()); err != nil {
		return execution.StatusFailed, err
	}

	runner := command.NewRunner(command.WithChroot(config.RootMount()))
	cmd := command.NewCommand(
		"service",
		command.WithArguments("procps", "force-reload"),
		command.WithWorkingDirectory(config.SkyhookDir()),
		command.WithStdout(config.Stdout()),
		command.WithStderr(config.Stderr()),
	)
	result, runErr := runner.Run(ctx, cmd)
	if runErr != nil {
		return execution.StatusFailed, fmt.Errorf("running interrupt %q command %q: %w", r.Type(), cmd.Executable, runErr)
	}
	if result.Signal != nil || result.ExitCode != command.SuccessExitCode {
		return execution.StatusFailed, nil
	}
	if markCompleted != nil {
		if err := markCompleted(0); err != nil {
			return execution.StatusFailed, fmt.Errorf("marking interrupt %q complete: %w", r.Type(), err)
		}
	}
	return execution.StatusSuccess, nil
}

func (r RestartAllServices) Serialize() ([]byte, error) {
	return marshalTypeOnly(r.Type())
}
