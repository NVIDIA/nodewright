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

package config

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/NVIDIA/nodewright/agent/internal/schema"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

// Config is a parsed, validated skyhook package configuration.
type Config struct {
	SchemaVersion       schema.SchemaVersion
	RootDir             string
	ExpectedConfigFiles []string
	PackageName         string
	PackageVersion      string
	Modes               map[stage.Stage][]step.Step
}

// configJSON is the wire form of a Config and the only thing that touches the
// JSON tags. It exists because Config.Modes holds the step.Step interface,
// which stdlib json can neither unmarshal into nor re-encode with each step's
// own discriminator logic; (un)marshaling routes the modes through here and
// step.Decode / step.Encode. Field order is the canonical serialization order.
type configJSON struct {
	SchemaVersion       string                       `json:"schema_version"`
	RootDir             string                       `json:"root_dir"`
	ExpectedConfigFiles []string                     `json:"expected_config_files"`
	PackageName         string                       `json:"package_name"`
	PackageVersion      string                       `json:"package_version"`
	Modes               map[string][]json.RawMessage `json:"modes"`
}

// MarshalJSON encodes the config, deferring per-step encoding to step.Encode so
// the step wire form (and its upgrade_step discriminator) stays owned by the
// step type. expected_config_files is coerced to [] so it never serializes as
// null, which the schema's array type would reject.
func (c Config) MarshalJSON() ([]byte, error) {
	modes := make(map[string][]json.RawMessage, len(c.Modes))
	for st, steps := range c.Modes {
		encoded := make([]json.RawMessage, 0, len(steps))
		for i, s := range steps {
			b, err := s.Encode()
			if err != nil {
				return nil, fmt.Errorf("encoding step %d in stage %q: %w", i, st, err)
			}
			encoded = append(encoded, b)
		}
		modes[string(st)] = encoded
	}

	expected := c.ExpectedConfigFiles
	if expected == nil {
		expected = []string{}
	}

	return json.Marshal(configJSON{
		SchemaVersion:       string(c.SchemaVersion),
		RootDir:             c.RootDir,
		ExpectedConfigFiles: expected,
		PackageName:         c.PackageName,
		PackageVersion:      c.PackageVersion,
		Modes:               modes,
	})
}

// UnmarshalJSON decodes the wire form, mapping each mode key to a stage.Stage
// (rejecting unknown stages) and decoding each step via step.Decode.
func (c *Config) UnmarshalJSON(data []byte) error {
	var w configJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	modes := make(map[stage.Stage][]step.Step, len(w.Modes))
	for rawStage, rawSteps := range w.Modes {
		st, err := stage.ParseStage(rawStage)
		if err != nil {
			return fmt.Errorf("config modes: %w", err)
		}
		steps := make([]step.Step, 0, len(rawSteps))
		for i, raw := range rawSteps {
			s, err := step.Decode(raw)
			if err != nil {
				return fmt.Errorf("decoding step %d in stage %q: %w", i, st, err)
			}
			steps = append(steps, s)
		}
		modes[st] = steps
	}

	*c = Config{
		SchemaVersion:       schema.SchemaVersion(w.SchemaVersion),
		RootDir:             w.RootDir,
		ExpectedConfigFiles: w.ExpectedConfigFiles,
		PackageName:         w.PackageName,
		PackageVersion:      w.PackageVersion,
		Modes:               modes,
	}
	return nil
}

// Loader loads and dumps package configs. It holds the SchemaValidator the
// document is checked against, so callers (and tests) can substitute the
// validation backend without reaching through package state.
type Loader struct {
	validator SchemaValidator
}

// NewLoader returns a Loader that validates against the embedded JSON Schemas.
func NewLoader() *Loader {
	return &Loader{validator: newSchemaValidator()}
}

// Load parses and validates a package config: it reads the declared schema
// version, validates the document against that schema (which also rejects
// unrecognised versions), materializes the typed Config, then runs the
// cross-step document rules. Warnings are emitted via logger (nil uses
// slog.Default()).
//
// stepRootDir is the on-disk directory the step files must exist under; it is
// intentionally distinct from Config.RootDir (the package-relative root
// recorded in the config) and the two need not match.
func (l *Loader) Load(data []byte, stepRootDir string, logger *slog.Logger) (*Config, error) {
	version, err := schemaVersionOf(data)
	if err != nil {
		return nil, err
	}

	if err := l.validator.Validate(data, version); err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config json: %w", err)
	}
	cfg.SchemaVersion = version

	if err := validateModes(cfg.Modes, stepRootDir, logger); err != nil {
		return nil, fmt.Errorf("validating steps: %w", err)
	}

	return &cfg, nil
}

// Dump serializes a package config at the latest schema version. Output is
// deterministic (struct field order + stdlib-sorted map keys) and is validated
// before returning so Dump never emits a document Load would reject for shape
// or schema reasons. It does not check step files on disk — that is Load's job
// against a real stepRootDir.
func (l *Loader) Dump(packageName, packageVersion, rootDir string, modes map[stage.Stage][]step.Step, expectedConfigFiles []string) ([]byte, error) {
	latest, err := schema.Highest()
	if err != nil {
		return nil, fmt.Errorf("resolving latest schema version: %w", err)
	}

	for st := range modes {
		if _, err := stage.ParseStage(string(st)); err != nil {
			return nil, fmt.Errorf("dumping config: %w", err)
		}
	}

	// Run the same structural cross-step rules Load enforces so a dumped
	// config can't serialize cleanly yet fail on reload. The on-disk step-file
	// check is intentionally excluded — Dump records a package-relative root,
	// not a path it can stat.
	if err := checkModeRules(modes); err != nil {
		return nil, fmt.Errorf("dumping config: %w", err)
	}

	data, err := json.Marshal(Config{
		SchemaVersion:       latest,
		RootDir:             rootDir,
		ExpectedConfigFiles: expectedConfigFiles,
		PackageName:         packageName,
		PackageVersion:      packageVersion,
		Modes:               modes,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling config: %w", err)
	}

	if err := l.validator.Validate(data, latest); err != nil {
		return nil, fmt.Errorf("dump produced invalid config: %w", err)
	}
	return data, nil
}

// schemaVersionOf extracts the declared schema_version without validating the
// rest of the document, so the loader can pick the right schema before
// validating against it.
func schemaVersionOf(data []byte) (schema.SchemaVersion, error) {
	var head struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", fmt.Errorf("parsing config json: %w", err)
	}
	if head.SchemaVersion == "" {
		return "", fmt.Errorf("config is missing required field schema_version")
	}
	return schema.SchemaVersion(head.SchemaVersion), nil
}
