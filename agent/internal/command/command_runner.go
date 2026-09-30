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
	"fmt"
	"os"
)

type commandRunner struct{}

var _ Runner = (*commandRunner)(nil)

func (*commandRunner) Run(ctx context.Context, command Command) (Result, error) {
	if err := validateRun(ctx, command); err != nil {
		return Result{}, fmt.Errorf("validating command execution: %w", err)
	}
	if command.Permissions != 0 || command.RequiredPermissions != 0 {
		executableFile, err := os.Open(command.Executable)
		if err != nil {
			return Result{}, fmt.Errorf(
				"applying command executable permissions: opening command executable %q: %w",
				command.Executable,
				err,
			)
		}
		permissionsErr := applyExecutablePermissions(ctx, command, executableFile)
		closeErr := closeExecutable(command, executableFile)
		if err := errors.Join(permissionsErr, closeErr); err != nil {
			return Result{}, fmt.Errorf("applying command executable permissions: %w", err)
		}
	}
	result, err := executeCommand(ctx, command, command.Executable, command.WorkingDirectory, "")
	if err != nil {
		return result, fmt.Errorf("executing command: %w", err)
	}
	return result, nil
}
