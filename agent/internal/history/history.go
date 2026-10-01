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

package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
)

const (
	UnknownVersion     = "unknown"
	UninstalledVersion = "uninstalled"
	historyFileMode    = 0o600
	historyEntryLimit  = 100
)

// LedgerEntry is one recorded version transition.
type LedgerEntry struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

// Ledger is the persisted history document for one package.
type Ledger struct {
	CurrentVersion string        `json:"current-version"`
	Entries        []LedgerEntry `json:"history"`
}

// Versions is the current package version and the previously recorded host
// version. It is returned to callers instead of mutating a step in place.
type Versions struct {
	Previous string
	Current  string
}

// Store records completed version transitions for one package and reports the
// versions a step should receive.
type Store interface {
	Read() (Versions, error)
	Record(completedStage stage.Stage, at time.Time) error
}

// NewStore returns the file-backed history store. The implementation is
// unexported so downstream consumers depend on the Store interface.
func NewStore(layout flags.Layout, cfg config.Config, logger *slog.Logger) (Store, error) {
	s, err := newFileStore(layout, cfg, logger)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// fileStore keeps one package's install history in a JSON ledger file.
type fileStore struct {
	rootMount   string
	path        string
	packageName string
	version     string
	logger      *slog.Logger
}

var _ Store = (*fileStore)(nil)

// newFileStore returns a store for the package described by cfg, keeping its
// ledger under the layout's history directory. Package identity is validated
// once here so later reads and writes cannot escape that directory or record
// an unusable version.
func newFileStore(layout flags.Layout, cfg config.Config, logger *slog.Logger) (*fileStore, error) {
	name := cfg.PackageName
	if err := hostfs.ValidatePathComponent("package name", name); err != nil {
		return nil, err
	}
	if cfg.PackageVersion == "" {
		return nil, errors.New("package version must not be empty")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &fileStore{
		rootMount:   layout.RootMount(),
		path:        filepath.Join(layout.HistoryDir(), name+".json"),
		packageName: name,
		version:     cfg.PackageVersion,
		logger:      logger,
	}, nil
}

// Path returns the ledger path for this package.
func (s *fileStore) Path() string {
	return s.path
}

// Read returns the versions a step should receive. A package with no usable
// history is treated as an unknown prior installation.
func (s *fileStore) Read() (Versions, error) {
	ledger, err := s.load()
	if err != nil {
		return Versions{}, fmt.Errorf("reading versions for package %q: %w", s.packageName, err)
	}
	return Versions{Current: s.version, Previous: ledger.CurrentVersion}, nil
}

// Record prepends a completed version transition to the package ledger.
// UninstallCheck records the package as uninstalled; every other valid stage
// records the configured package version.
func (s *fileStore) Record(completedStage stage.Stage, at time.Time) error {
	if _, err := stage.ParseStage(string(completedStage)); err != nil {
		return fmt.Errorf("recording history for package %q: validating completed stage: %w", s.packageName, err)
	}
	if at.IsZero() {
		return errors.New("history timestamp must not be zero")
	}

	ledger, err := s.load()
	if err != nil {
		return fmt.Errorf("recording history for package %q: %w", s.packageName, err)
	}
	version := s.version
	if completedStage == stage.UninstallCheck {
		version = UninstalledVersion
	}
	ledger.CurrentVersion = version
	ledger.Entries = append([]LedgerEntry{{Version: version, Time: at.UTC()}}, ledger.Entries...)
	if len(ledger.Entries) > historyEntryLimit {
		ledger.Entries = ledger.Entries[:historyEntryLimit]
	}

	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding history for package %q: %w", s.packageName, err)
	}
	data = append(data, '\n')
	if err := hostfs.WriteFile(s.rootMount, s.path, data, historyFileMode); err != nil {
		return fmt.Errorf("writing history for package %q: %w", s.packageName, err)
	}
	return nil
}

func (s *fileStore) load() (Ledger, error) {
	data, err := hostfs.ReadFile(s.rootMount, s.path)
	if errors.Is(err, fs.ErrNotExist) {
		s.logger.Info("package history does not exist", "package", s.packageName, "path", s.path)
		return Ledger{CurrentVersion: UnknownVersion, Entries: []LedgerEntry{}}, nil
	}
	if err != nil {
		return Ledger{}, fmt.Errorf("reading history %q: %w", s.path, err)
	}

	var result Ledger
	if err := json.Unmarshal(data, &result); err != nil {
		backup := s.path + ".backup"
		// Preserve the damaged bytes and continue from an unknown version so a
		// corrupt file cannot permanently block package execution on this node.
		if renameErr := hostfs.RenameFile(s.rootMount, s.path, backup); renameErr != nil {
			return Ledger{}, fmt.Errorf("moving corrupt history %q to %q: %w", s.path, backup, renameErr)
		}
		s.logger.Error(
			"moved corrupt package history aside",
			"package", s.packageName,
			"path", s.path,
			"backup", backup,
			"error", err,
		)
		return Ledger{CurrentVersion: UnknownVersion, Entries: []LedgerEntry{}}, nil
	}
	if result.CurrentVersion == "" {
		result.CurrentVersion = UnknownVersion
	}
	if result.Entries == nil {
		result.Entries = []LedgerEntry{}
	}
	return result, nil
}
