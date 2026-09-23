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

package flags

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

const flagFileMode = 0o600

// Reason explains why a step should run or be skipped.
type Reason string

const (
	ReasonFlagMissing         Reason = "flag-missing"
	ReasonAlwaysRun           Reason = "always-run"
	ReasonStageAlwaysRuns     Reason = "stage-always-runs"
	ReasonIdempotenceDisabled Reason = "idempotence-disabled"
	ReasonAlreadyCompleted    Reason = "already-completed"
)

// Decision is the result of checking a step's completion flag.
type Decision struct {
	Run    bool
	Reason Reason
}

// Store manages completion and control flags for one package.
//
// A flag is a small marker file the agent writes on the host after a step
// finishes successfully. On later runs the agent checks for the flag to decide
// whether the step already completed and can be skipped, which is how package
// execution stays idempotent across agent restarts and node reboots. Flag
// filenames embed a fingerprint of the step's execution-relevant inputs (path,
// arguments, return codes, environment, host execution), so changing any of
// those produces a new flag and the step runs again. Flags live beneath the
// package state directory on the mounted host root, grouped by package name
// and version.
type Store interface {
	Path(value step.Step) (string, error)
	Mark(value step.Step, message string) (string, error)
	Remove(value step.Step) error
	Write(path string, data []byte) error
	Check(value step.Step, alwaysRun bool, currentStage stage.Stage) (Decision, error)
}

// NewStore returns the file-backed flag store. The implementation is
// unexported so downstream consumers depend on the Store interface.
func NewStore(layout Layout, cfg config.Config) (Store, error) {
	s, err := newFileStore(layout, cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// fileStore keeps one package's flags under a host layout.
type fileStore struct {
	rootMount      string
	dir            string
	packageName    string
	packageVersion string
}

var _ Store = (*fileStore)(nil)

// newFileStore returns a store for the package described by cfg. Package
// identity is validated once here so later flag paths cannot escape the
// package's flag directory.
func newFileStore(layout Layout, cfg config.Config) (*fileStore, error) {
	if err := validatePackagePath(cfg); err != nil {
		return nil, fmt.Errorf("validating package path: %w", err)
	}
	return &fileStore{
		rootMount:      layout.rootMount,
		dir:            filepath.Join(layout.stateRoot, "flags"),
		packageName:    cfg.PackageName,
		packageVersion: cfg.PackageVersion,
	}, nil
}

// Path returns the completion-flag path for a step. The filename contains a
// stable SHA-256 fingerprint of the step path, arguments, and accepted return
// codes so any execution-relevant change gets a distinct flag.
func (s *fileStore) Path(value step.Step) (string, error) {
	if value == nil {
		return "", errors.New("building flag path: step must not be nil")
	}
	if !filepath.IsLocal(value.Path()) {
		return "", fmt.Errorf("building flag path: step path %q must be relative to the package root", value.Path())
	}

	marker, err := value.Fingerprint()
	if err != nil {
		return "", fmt.Errorf("building flag path: fingerprinting step %q: %w", value.Path(), err)
	}
	filename := filepath.Base(value.Path()) + "-" + marker + ".flag"
	return filepath.Join(s.rootMount, s.dir, s.packageName, s.packageVersion, filename), nil
}

// Mark records successful completion and returns the flag path written.
func (s *fileStore) Mark(value step.Step, message string) (string, error) {
	path, err := s.Path(value)
	if err != nil {
		return "", fmt.Errorf("marking step: resolving flag path: %w", err)
	}
	legacyPath, hasLegacyPath := s.legacyPath(value)
	paths := []string{path}
	if hasLegacyPath {
		paths = append([]string{legacyPath}, paths...)
	}
	existed := make([]bool, len(paths))
	for index, markerPath := range paths {
		if err := s.validatePath(markerPath); err != nil {
			return "", fmt.Errorf("marking step %q: validating flag path: %w", value.Path(), err)
		}
		existed[index], err = hostfs.RegularFileExists(s.rootMount, markerPath)
		if err != nil {
			return "", fmt.Errorf("marking step %q: inspecting flag path %q: %w", value.Path(), markerPath, err)
		}
	}
	for index, markerPath := range paths {
		if err := s.Write(markerPath, []byte(message)); err != nil {
			var rollbackErr error
			for rollbackIndex, writtenPath := range paths[:index+1] {
				if existed[rollbackIndex] {
					continue
				}
				if removeErr := hostfs.RemoveFile(s.rootMount, writtenPath); removeErr != nil {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf("removing incomplete flag %q: %w", writtenPath, removeErr),
					)
				}
			}
			return "", errors.Join(
				fmt.Errorf("marking step %q: %w", value.Path(), err),
				rollbackErr,
			)
		}
	}
	return path, nil
}

// Remove deletes a step completion flag. A missing flag is already removed and
// therefore succeeds.
func (s *fileStore) Remove(value step.Step) error {
	path, err := s.Path(value)
	if err != nil {
		return fmt.Errorf("removing step flag: resolving flag path: %w", err)
	}
	legacyPath, hasLegacyPath := s.legacyPath(value)
	paths := []string{path}
	if hasLegacyPath {
		paths = append(paths, legacyPath)
	}
	var removeErr error
	for _, flagPath := range paths {
		if err := s.validatePath(flagPath); err != nil {
			removeErr = errors.Join(
				removeErr,
				fmt.Errorf("removing step flag: validating store path: %w", err),
			)
			continue
		}
		if err := hostfs.RemoveFile(s.rootMount, flagPath); err != nil {
			removeErr = errors.Join(
				removeErr,
				fmt.Errorf("removing step flag %q: %w", flagPath, err),
			)
		}
	}
	return removeErr
}

// Write creates or replaces a flag file owned by this store. In addition to
// per-step flags, callers can use it for control files such as START and
// check_results without duplicating directory and permission handling.
func (s *fileStore) Write(path string, data []byte) error {
	if err := s.validatePath(path); err != nil {
		return fmt.Errorf("validating flag store path %q: %w", path, err)
	}
	if err := hostfs.WriteFile(
		s.rootMount,
		path,
		data,
		flagFileMode,
	); err != nil {
		return fmt.Errorf("writing flag %q: %w", path, err)
	}
	return nil
}

// Check reports whether a step should run based on its completion flag and
// current execution policy.
func (s *fileStore) Check(value step.Step, alwaysRun bool, currentStage stage.Stage) (Decision, error) {
	if value == nil {
		return Decision{}, errors.New("checking flag: step must not be nil")
	}
	if _, err := stage.ParseStage(string(currentStage)); err != nil {
		return Decision{}, fmt.Errorf("checking flag for step %q: validating stage: %w", value.Path(), err)
	}
	if err := value.Idempotence().Validate(); err != nil {
		return Decision{}, fmt.Errorf("checking flag for step %q: validating idempotence: %w", value.Path(), err)
	}
	path, err := s.Path(value)
	if err != nil {
		return Decision{}, fmt.Errorf("checking flag for step %q: resolving store path: %w", value.Path(), err)
	}
	if err := s.validatePath(path); err != nil {
		return Decision{}, fmt.Errorf("checking flag for step %q: validating store path: %w", value.Path(), err)
	}
	exists, err := hostfs.RegularFileExists(s.rootMount, path)
	if err != nil {
		return Decision{}, fmt.Errorf("checking flag for step %q: inspecting store path: %w", value.Path(), err)
	}
	if !exists {
		legacyPath, hasLegacyPath := s.legacyPath(value)
		if hasLegacyPath {
			exists, err = hostfs.RegularFileExists(s.rootMount, legacyPath)
			if err != nil {
				return Decision{}, fmt.Errorf("checking flag for step %q: inspecting legacy store path: %w", value.Path(), err)
			}
		}
		if !exists {
			return Decide(false, alwaysRun, currentStage, value.Idempotence()), nil
		}
	}
	return Decide(true, alwaysRun, currentStage, value.Idempotence()), nil
}

func (s *fileStore) legacyPath(value step.Step) (string, bool) {
	if value == nil || !filepath.IsLocal(value.Path()) {
		return "", false
	}
	metadata := value.ExecutionMetadata()
	payload := step.FormatLegacyArguments(metadata.Arguments) + "_" +
		step.FormatLegacyReturnCodes(metadata.ReturnCodes)
	marker := base64.StdEncoding.EncodeToString([]byte(payload))
	return filepath.Join(
		s.rootMount,
		s.dir,
		s.packageName,
		s.packageVersion,
		value.Path()+"_"+marker,
	), true
}

// Decide applies flag policy without filesystem access.
func Decide(flagExists, alwaysRun bool, currentStage stage.Stage, idempotence step.Idempotence) Decision {
	if !flagExists {
		return Decision{Run: true, Reason: ReasonFlagMissing}
	}
	if alwaysRun {
		return Decision{Run: true, Reason: ReasonAlwaysRun}
	}
	switch currentStage {
	case stage.Config, stage.Uninstall, stage.Upgrade:
		return Decision{Run: true, Reason: ReasonStageAlwaysRuns}
	}
	if idempotence == step.Disabled {
		return Decision{Run: true, Reason: ReasonIdempotenceDisabled}
	}
	return Decision{Run: false, Reason: ReasonAlreadyCompleted}
}

func (s *fileStore) validatePath(path string) error {
	storePath := filepath.Join(s.rootMount, s.dir)
	relative, err := filepath.Rel(storePath, path)
	if err != nil {
		return fmt.Errorf("resolving path relative to flag store: %w", err)
	}
	if !filepath.IsLocal(relative) {
		return fmt.Errorf("flag path %q must be contained within %q", path, storePath)
	}
	return nil
}
