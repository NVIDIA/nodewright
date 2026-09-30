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

// Package execution defines runtime contracts shared by agent operations.
//
// Status records an operation outcome as StatusSuccess or StatusFailed. Config
// composes the host root mount, step and package paths within that host, and
// the stdout and stderr writers used to stream command output.
package execution

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Status reports whether an agent operation satisfied its execution policy.
type Status string

const (
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
)

// Config contains the filesystem and output composition for an agent operation.
type Config struct {
	rootMount  string
	stepRoot   string
	skyhookDir string
	stdout     io.Writer
	stderr     io.Writer
}

// RootMount returns the absolute host root used by host operations.
func (config Config) RootMount() string { return config.rootMount }

// StepRoot returns the directory containing step scripts inside the target host.
func (config Config) StepRoot() string { return config.stepRoot }

// SkyhookDir returns the package working directory inside the target host.
func (config Config) SkyhookDir() string { return config.skyhookDir }

// Stdout returns the destination for command standard output.
func (config Config) Stdout() io.Writer { return config.stdout }

// Stderr returns the destination for command standard error.
func (config Config) Stderr() io.Writer { return config.stderr }

// Validate reports whether the run configuration is complete.
func (config Config) Validate() error {
	if !filepath.IsAbs(config.rootMount) {
		return fmt.Errorf("root mount %q is not absolute", config.rootMount)
	}
	if !filepath.IsAbs(config.stepRoot) {
		return fmt.Errorf("step root %q is not absolute", config.stepRoot)
	}
	if !filepath.IsAbs(config.skyhookDir) {
		return fmt.Errorf("skyhook directory %q is not absolute", config.skyhookDir)
	}
	if config.stdout == nil {
		return errors.New("stdout writer is nil")
	}
	if config.stderr == nil {
		return errors.New("stderr writer is nil")
	}
	return nil
}

// Option configures a Config.
type Option func(*Config)

// NewConfig constructs agent execution policy.
func NewConfig(options ...Option) (Config, error) {
	config := Config{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
	for _, option := range options {
		option(&config)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("constructing agent run config: %w", err)
	}
	return config, nil
}

// WithRootMount sets the absolute host root used by host operations.
func WithRootMount(rootMount string) Option {
	return func(config *Config) {
		config.rootMount = rootMount
	}
}

// WithStepRoot sets the absolute path containing step scripts inside the target host.
func WithStepRoot(stepRoot string) Option {
	return func(config *Config) {
		config.stepRoot = stepRoot
	}
}

// WithSkyhookDir sets the absolute package working directory inside the target host.
func WithSkyhookDir(skyhookDir string) Option {
	return func(config *Config) {
		config.skyhookDir = skyhookDir
	}
}

// WithRunOutput sets the parent streams that receive raw command output.
func WithRunOutput(stdout, stderr io.Writer) Option {
	return func(config *Config) {
		if stdout == nil {
			stdout = io.Discard
		}
		if stderr == nil {
			stderr = io.Discard
		}
		config.stdout = stdout
		config.stderr = stderr
	}
}
