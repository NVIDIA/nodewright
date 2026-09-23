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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type chrootCommandRunner struct {
	root string
}

var _ Runner = (*chrootCommandRunner)(nil)

func (runner *chrootCommandRunner) Run(
	ctx context.Context,
	command Command,
) (result Result, err error) {
	if err := validateRun(ctx, command); err != nil {
		return Result{}, fmt.Errorf("validating chroot command execution: %w", err)
	}
	setup, err := prepareChroot(runner.root, command)
	if err != nil {
		return Result{}, fmt.Errorf("preparing chroot command execution: %w", err)
	}
	defer func() {
		if closeErr := setup.close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if command.Permissions != 0 || command.RequiredPermissions != 0 {
		relative := strings.TrimPrefix(filepath.Clean(command.Executable), string(filepath.Separator))
		executableFile, openErr := setup.root.Open(relative)
		if openErr != nil {
			return Result{}, fmt.Errorf(
				"applying chroot command executable permissions: opening command executable %q: %w",
				command.Executable,
				openErr,
			)
		}
		permissionsErr := applyExecutablePermissions(ctx, command, executableFile)
		closeErr := closeExecutable(command, executableFile)
		if err := errors.Join(permissionsErr, closeErr); err != nil {
			return Result{}, fmt.Errorf("applying chroot command executable permissions: %w", err)
		}
	}
	result, err = executeCommand(
		ctx,
		command,
		setup.executable,
		setup.workingDirectory,
		setup.rootPath,
	)
	if err != nil {
		return result, fmt.Errorf("executing chroot command: %w", err)
	}
	return result, nil
}

type chrootSetup struct {
	root             *os.Root
	rootPath         string
	executable       string
	workingDirectory string
}

func (setup chrootSetup) close() error {
	if setup.root == nil {
		return nil
	}
	if err := setup.root.Close(); err != nil {
		return fmt.Errorf("closing target root %q: %w", setup.rootPath, err)
	}
	return nil
}

func prepareChroot(rootPath string, command Command) (setup chrootSetup, err error) {
	if rootPath == "" {
		return chrootSetup{}, errors.New("command chroot is empty")
	}
	if strings.ContainsRune(rootPath, 0) {
		return chrootSetup{}, errors.New("command chroot contains a NUL byte")
	}
	if !filepath.IsAbs(rootPath) {
		return chrootSetup{}, fmt.Errorf("chroot %q is not absolute", rootPath)
	}

	configuredRoot := rootPath
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		return chrootSetup{}, fmt.Errorf("resolving chroot %q: %w", configuredRoot, err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return chrootSetup{}, fmt.Errorf("opening target root %q: %w", rootPath, err)
	}
	setup = chrootSetup{
		root:             root,
		rootPath:         rootPath,
		executable:       command.Executable,
		workingDirectory: command.WorkingDirectory,
	}
	openedRoot := setup
	defer func() {
		if err != nil {
			err = errors.Join(err, openedRoot.close())
		}
	}()

	if setup.workingDirectory == "" {
		setup.workingDirectory = "/"
	}
	if strings.ContainsRune(setup.executable, filepath.Separator) {
		return setup, nil
	}

	pathEnvironment, ok := command.Environment["PATH"]
	if !ok {
		pathEnvironment = os.Getenv("PATH")
	}
	setup.executable, err = lookPathInRoot(root, setup.workingDirectory, pathEnvironment, setup.executable)
	if err != nil {
		return chrootSetup{}, fmt.Errorf("resolving command executable: %w", err)
	}
	return setup, nil
}

// lookPathInRoot returns a child-visible executable path. It intentionally
// leaves symlink resolution to the kernel after the child enters its chroot.
func lookPathInRoot(
	root *os.Root,
	workingDirectory string,
	pathEnvironment string,
	executable string,
) (string, error) {
	directories := filepath.SplitList(pathEnvironment)

	var permissionErr error
	for _, directory := range directories {
		if directory == "" {
			directory = workingDirectory
		} else if !filepath.IsAbs(directory) {
			directory = filepath.Join(workingDirectory, directory)
		}

		candidate := filepath.Clean(filepath.Join(directory, executable))
		relative := strings.TrimPrefix(candidate, string(filepath.Separator))
		info, err := root.Lstat(relative)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return "", fmt.Errorf("checking executable candidate %q: %w", candidate, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
		if permissionErr == nil {
			permissionErr = fmt.Errorf("executable candidate %q is not executable: %w", candidate, fs.ErrPermission)
		}
	}

	if permissionErr != nil {
		return "", permissionErr
	}
	return "", fmt.Errorf("executable %q not found in PATH", executable)
}
