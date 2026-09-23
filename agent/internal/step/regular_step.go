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

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
)

// RegularStep is the default Step implementation.
type RegularStep struct {
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

var _ Step = RegularStep{}

// NewRegularStep constructs a RegularStep with canonical defaults.
func NewRegularStep(path string, opts ...Option) RegularStep {
	configured := newStepOptions(path, opts...)
	return RegularStep{
		Name:              configured.name,
		ScriptPath:        path,
		Arguments:         configured.arguments,
		Returncodes:       configured.returncodes,
		OnHost:            configured.onHost,
		IdempotenceMode:   configured.idempotence,
		Env:               configured.env,
		RequiresInterrupt: configured.requiresInterrupt,
	}
}

// Path returns the step's script path, relative to the package root.
func (s RegularStep) Path() string {
	return s.ScriptPath
}

// Idempotence reports how the agent treats re-runs of the step.
func (s RegularStep) Idempotence() Idempotence {
	return s.IdempotenceMode
}

// ExecutionMetadata reports the regular step's configured command settings.
func (s RegularStep) ExecutionMetadata() ExecutionMetadata {
	s.applyDefaults()
	return newExecutionMetadata(s.Arguments, s.Returncodes, s.OnHost)
}

// WithVersions returns a copy with the package versions in its command environment.
func (s RegularStep) WithVersions(previous, current string) Step {
	s.versions = &stepVersions{previous: previous, current: current}
	return s
}

// Run executes the configured command.
func (s RegularStep) Run(ctx context.Context, config execution.Config) (execution.Status, error) {
	s.applyDefaults()
	return runStep(
		ctx,
		config,
		s.ScriptPath,
		s.Arguments,
		s.Returncodes,
		s.OnHost,
		s.Env,
		s.versions,
	)
}

// Fingerprint returns a stable digest of the step's execution-relevant inputs.
func (s RegularStep) Fingerprint() (string, error) {
	return stepFingerprint(s.ScriptPath, s.Arguments, s.Returncodes, s.Env, s.OnHost)
}

// Encode serializes the regular-step wire form.
func (s RegularStep) Encode() ([]byte, error) {
	s.applyDefaults()
	s.UpgradeStep = false
	return encodeStep(s, s.IdempotenceMode)
}

func (s *RegularStep) applyDefaults() {
	applyStepDefaults(
		&s.Name,
		s.ScriptPath,
		&s.Arguments,
		&s.Returncodes,
		&s.Env,
		&s.IdempotenceMode,
	)
}
