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
	"syscall"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
)

const nodeRestartConfirmationTimeout = 10 * time.Second

// NodeRestart runs reboot inside the configured root mount. A SIGTERM that
// terminates the reboot command proves shutdown began; an exit-zero command
// waits briefly so an enqueued reboot is not reported as an immediate failure.
type NodeRestart struct{}

var _ Interrupt = NodeRestart{}

func (NodeRestart) Type() InterruptType { return NodeRestartType }

func (n NodeRestart) Run(ctx context.Context, config execution.Config) (execution.Status, error) {
	if err := validateRun(ctx, config, n.Type()); err != nil {
		return execution.StatusFailed, err
	}

	runner := command.NewRunner(command.WithChroot(config.RootMount()))
	cmd := command.NewCommand(
		"reboot",
		command.WithWorkingDirectory(config.SkyhookDir()),
		command.WithStdout(config.Stdout()),
		command.WithStderr(config.Stderr()),
	)
	result, runErr := runner.Run(ctx, cmd)
	if runErr == nil && nodeRestartCompleted(result) {
		return execution.StatusSuccess, nil
	}
	if runErr != nil {
		return execution.StatusFailed, fmt.Errorf("running interrupt %q command %q: %w", n.Type(), cmd.Executable, runErr)
	}
	if result.Signal != nil || result.ExitCode != command.SuccessExitCode {
		return execution.StatusFailed, nil
	}
	return execution.StatusFailed, waitForNodeRestart(ctx, nodeRestartConfirmationTimeout)
}

func waitForNodeRestart(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting for host restart: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf("reboot was enqueued but the host did not restart within %s", timeout)
	}
}

func nodeRestartCompleted(result command.Result) bool {
	// A reboot terminates userspace with SIGTERM before the command can return.
	// Preserving success prevents the next agent pod from rebooting the node again.
	return result.ExitCode == command.SignalExitCode && result.Signal == syscall.SIGTERM
}

func (n NodeRestart) Serialize() ([]byte, error) {
	return marshalTypeOnly(n.Type())
}
