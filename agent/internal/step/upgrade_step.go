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

package step

import (
	"context"
	"errors"
	"fmt"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
)

// UpgradeStep runs during the Upgrade and UpgradeCheck modes.
type UpgradeStep struct {
	Name              string             `json:"name"`
	ScriptPath        string             `json:"path"`
	Arguments         []string           `json:"arguments"`
	Returncodes       []command.ExitCode `json:"returncodes"`
	OnHost            bool               `json:"on_host"`
	IdempotenceMode   Idempotence        `json:"idempotence"`
	UpgradeStep       bool               `json:"upgrade_step"`
	Env               map[string]string  `json:"env,omitempty"`
	RequiresInterrupt bool               `json:"requires_interrupt,omitempty"`

	versions *stepVersions
}

var _ Step = UpgradeStep{}

// NewUpgradeStep constructs an UpgradeStep with canonical defaults.
func NewUpgradeStep(path string, opts ...Option) (UpgradeStep, error) {
	configured := newStepOptions(path, opts...)
	value := UpgradeStep{
		Name:              configured.name,
		ScriptPath:        path,
		Arguments:         configured.arguments,
		Returncodes:       configured.returncodes,
		OnHost:            configured.onHost,
		IdempotenceMode:   configured.idempotence,
		UpgradeStep:       true,
		Env:               configured.env,
		RequiresInterrupt: configured.requiresInterrupt,
	}
	if err := value.Validate(); err != nil {
		return UpgradeStep{}, fmt.Errorf("NewUpgradeStep: %w", err)
	}
	return value, nil
}

// Path returns the step's script path, relative to the package root.
func (s UpgradeStep) Path() string {
	return s.ScriptPath
}

// Idempotence reports how the agent treats re-runs of the step.
func (s UpgradeStep) Idempotence() Idempotence {
	return s.IdempotenceMode
}

// ExecutionMetadata reports the upgrade step's configured command settings.
func (s UpgradeStep) ExecutionMetadata() ExecutionMetadata {
	s.applyDefaults()
	arguments := s.Arguments
	if s.versions != nil {
		arguments = []string{s.versions.previous, s.versions.current}
	}
	return newExecutionMetadata(arguments, s.Returncodes, s.OnHost)
}

// WithVersions returns a copy prepared with version environment and arguments.
func (s UpgradeStep) WithVersions(previous, current string) Step {
	s.versions = &stepVersions{previous: previous, current: current}
	return s
}

// Run executes the upgrade command with the previous and current versions as arguments.
func (s UpgradeStep) Run(ctx context.Context, config execution.Config) (execution.Status, error) {
	if err := s.Validate(); err != nil {
		return execution.StatusFailed, fmt.Errorf("upgrade step validation failed: %w", err)
	}
	if s.versions == nil {
		return execution.StatusFailed, errors.New("running upgrade step: versions were not provided")
	}
	if s.versions.previous == "" {
		return execution.StatusFailed, errors.New("running upgrade step: previous version must not be empty")
	}
	if s.versions.current == "" {
		return execution.StatusFailed, errors.New("running upgrade step: current version must not be empty")
	}
	s.applyDefaults()
	return runStep(
		ctx,
		config,
		s.ScriptPath,
		[]string{s.versions.previous, s.versions.current},
		s.Returncodes,
		s.OnHost,
		s.Env,
		s.versions,
	)
}

// Fingerprint returns a stable digest of the step's execution-relevant inputs.
func (s UpgradeStep) Fingerprint() (string, error) {
	return stepFingerprint(s.ScriptPath, s.Arguments, s.Returncodes, s.Env, s.OnHost)
}

// Validate reports whether the upgrade step has configured arguments.
func (s UpgradeStep) Validate() error {
	if len(s.Arguments) != 0 {
		return fmt.Errorf(
			"UpgradeStep %s can not have any arguments, but found: %v",
			s.Name,
			s.Arguments,
		)
	}
	return nil
}

// Encode validates and serializes the upgrade-step wire form.
func (s UpgradeStep) Encode() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("upgrade step validation failed: %w", err)
	}
	s.applyDefaults()
	s.UpgradeStep = true
	return encodeStep(s, s.IdempotenceMode)
}

func (s *UpgradeStep) applyDefaults() {
	applyStepDefaults(
		&s.Name,
		s.ScriptPath,
		&s.Arguments,
		&s.Returncodes,
		&s.Env,
		&s.IdempotenceMode,
	)
}
