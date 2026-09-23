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

package agent

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
)

const logFileMode = 0o600

type operationLog struct {
	stdout    io.Writer
	stderr    io.Writer
	close     func() error
	files     flags.LogFiles
	operation string
	retained  bool
}

func prepareOperationLog(
	runtime runtimeConfig,
	layout flags.Layout,
	cfg config.Config,
	logPath string,
	operation string,
) (operationLog, error) {
	prepared := operationLog{
		stdout:    runtime.stdout,
		stderr:    runtime.stderr,
		close:     func() error { return nil },
		operation: operation,
		retained:  runtime.writeLogs,
	}
	if !runtime.writeLogs {
		return prepared, nil
	}

	_, file, err := layout.CreateLogFile(cfg, logPath, time.Now(), logFileMode)
	if err != nil {
		return operationLog{}, fmt.Errorf("preparing log for %s: %w", operation, err)
	}
	prepared.stdout = io.MultiWriter(runtime.stdout, file)
	prepared.stderr = io.MultiWriter(runtime.stderr, file)
	prepared.close = file.Close
	prepared.files, err = layout.LogFilePattern(cfg, logPath)
	if err != nil {
		return operationLog{}, errors.Join(
			fmt.Errorf("resolving log retention for %s: %w", operation, err),
			prepared.closeLog(),
		)
	}
	return prepared, nil
}

func (log operationLog) finalize() error {
	closeErr := log.closeLog()
	var cleanupErr error
	if log.retained {
		cleanupErr = flags.CleanupOldLogs(log.files, flags.DefaultLogRetention)
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("cleaning old logs for %s: %w", log.operation, cleanupErr)
		}
	}
	return errors.Join(closeErr, cleanupErr)
}

func (log operationLog) closeLog() error {
	if err := log.close(); err != nil {
		return fmt.Errorf("closing log for %s: %w", log.operation, err)
	}
	return nil
}
