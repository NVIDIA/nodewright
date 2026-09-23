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
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
)

// ExecutionMetadata contains the configured values orchestration needs without
// depending on a concrete step type.
type ExecutionMetadata struct {
	Arguments   []string
	ReturnCodes []command.ExitCode
	OnHost      bool
}

// Step is the interface satisfied by every concrete step type the
// agent recognises (RegularStep, UpgradeStep). Callers that must
// distinguish the kinds type-assert on UpgradeStep.
type Step interface {
	// Run executes the step using the supplied filesystem and output
	// composition.
	Run(context.Context, execution.Config) (execution.Status, error)

	// WithVersions returns a copy prepared with the package versions supplied
	// by upgrade-stage orchestration.
	WithVersions(previous, current string) Step

	// Encode serializes the step to its JSON wire form. Each concrete
	// type owns its own discriminator bit and any invariants it
	// enforces before marshaling.
	Encode() ([]byte, error)

	// Path is the step's script path, relative to the package root.
	Path() string

	// Fingerprint is a stable SHA-256 hex digest of the step's
	// execution-relevant inputs (path, arguments, return codes, env,
	// host execution); changing any of them changes the fingerprint.
	Fingerprint() (string, error)

	// Idempotence reports how the agent treats re-runs of the step.
	Idempotence() Idempotence

	// ExecutionMetadata reports the command settings shared by every step type.
	ExecutionMetadata() ExecutionMetadata
}

// Decode parses a JSON step payload, returning either a RegularStep or an
// UpgradeStep based on the upgrade_step discriminator. Stdlib drives field
// parsing (the exported tags + the Idempotence codec); Decode routes on the
// discriminator and applies defaults.
//
// Missing JSON fields receive the constructor defaults. Full package configs
// are schema-validated before they reach Decode, but Decode also preserves the
// default OnHost behavior for direct callers while retaining an explicit false.
func Decode(data []byte) (Step, error) {
	var probe struct {
		UpgradeStep bool  `json:"upgrade_step"`
		OnHost      *bool `json:"on_host"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("decode step: %w", err)
	}

	if probe.UpgradeStep {
		var u UpgradeStep
		if err := json.Unmarshal(data, &u); err != nil {
			return nil, fmt.Errorf("decode upgrade step: %w", err)
		}
		if probe.OnHost == nil {
			u.OnHost = true
		}
		u.applyDefaults()
		if err := u.Validate(); err != nil {
			return nil, fmt.Errorf("upgrade step validation failed: %w", err)
		}
		return u, nil
	}

	var regular RegularStep
	if err := json.Unmarshal(data, &regular); err != nil {
		return nil, fmt.Errorf("decode step: %w", err)
	}
	if probe.OnHost == nil {
		regular.OnHost = true
	}
	regular.applyDefaults()
	return regular, nil
}
