// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
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

package credentialprovider

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	// execWaitDelay bounds the wait for a plugin's output after it exits or
	// is killed, when a process it started keeps the output open.
	execWaitDelay = 2 * time.Second
	maxStderrSize = 512
)

// ExecRunner returns a Runner that runs a plugin chrooted into root, the
// host's root filesystem as the agent mounts it, so that the plugin finds the
// host's libraries and configuration as the kubelet's run of it does. With an
// empty root it runs the plugin at its path in the agent's own filesystem.
//
// It does not use the command package: a plugin gets exactly Command.Env
// rather than the agent's environment, reads Command.Stdin, and is killed
// when ctx ends, as the kubelet kills it, where a package step is left to
// finish. A plugin only answers, and changes nothing on the host.
func ExecRunner(root string) Runner {
	return func(ctx context.Context, cmd Command) ([]byte, error) {
		process := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
		// A nil Env would inherit the agent's environment.
		process.Env = append([]string{}, cmd.Env...)
		process.Dir = "/"
		process.Stdin = bytes.NewReader(cmd.Stdin)
		var stdout, stderr bytes.Buffer
		process.Stdout = &stdout
		process.Stderr = &stderr
		process.WaitDelay = execWaitDelay
		if root != "" {
			process.SysProcAttr = &syscall.SysProcAttr{Chroot: root}
		}

		err := process.Run()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("running %s: %w", cmd.Path, ctxErr)
		}
		if err != nil {
			return nil, fmt.Errorf("running %s: %w%s", cmd.Path, err, stderrSuffix(stderr.Bytes()))
		}
		return stdout.Bytes(), nil
	}
}

// stderrSuffix returns the end of a failed plugin's standard error to append
// to its error, as the kubelet appends all of it.
func stderrSuffix(stderr []byte) string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(stderr[max(0, len(stderr)-maxStderrSize):]), ""))
	if text == "" {
		return ""
	}
	return ": " + text
}
