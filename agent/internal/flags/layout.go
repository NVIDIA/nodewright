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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
)

const (
	DefaultStateRoot    = "/etc/skyhook"
	DefaultLogRoot      = "/var/log/skyhook"
	logTimeFormat       = "2006-01-02-150405.000000000"
	legacyLogTimeFormat = "2006-01-02-150405"
)

// Layout resolves agent-owned paths beneath the mounted host root. StateRoot
// and LogRoot are supplied by the process configuration rather than read from
// the environment here, which keeps path construction deterministic.
type Layout struct {
	rootMount string
	stateRoot string
	logRoot   string
}

// NewLayout constructs a host layout. Empty state and log roots select the
// agent defaults. Absolute state and log roots are interpreted relative to the
// mounted host root; roots that would traverse above it are rejected.
func NewLayout(rootMount, stateRoot, logRoot string) (Layout, error) {
	if stateRoot == "" {
		stateRoot = DefaultStateRoot
	}
	if logRoot == "" {
		logRoot = DefaultLogRoot
	}
	stateRoot, err := normalizeLayoutRoot("state root", stateRoot)
	if err != nil {
		return Layout{}, fmt.Errorf("constructing layout: normalizing state root: %w", err)
	}
	logRoot, err = normalizeLayoutRoot("log root", logRoot)
	if err != nil {
		return Layout{}, fmt.Errorf("constructing layout: normalizing log root: %w", err)
	}
	return Layout{
		rootMount: normalizeRootMount(rootMount),
		stateRoot: stateRoot,
		logRoot:   logRoot,
	}, nil
}

// DefaultLayout constructs a layout using the standard agent directories.
func DefaultLayout(rootMount string) Layout {
	return Layout{
		rootMount: normalizeRootMount(rootMount),
		stateRoot: strings.TrimPrefix(DefaultStateRoot, string(filepath.Separator)),
		logRoot:   strings.TrimPrefix(DefaultLogRoot, string(filepath.Separator)),
	}
}

func (l Layout) RootMount() string {
	return l.rootMount
}

func (l Layout) StateDir() string {
	return filepath.Join(l.rootMount, l.stateRoot)
}

func (l Layout) FlagDir() string {
	return filepath.Join(l.StateDir(), "flags")
}

func (l Layout) HistoryDir() string {
	return filepath.Join(l.StateDir(), "history")
}

func (l Layout) LogDir() string {
	return filepath.Join(l.rootMount, l.logRoot)
}

// StepsDir returns the directory where package step files are copied.
func StepsDir(copyDir string) string {
	return filepath.Join(copyDir, "skyhook_dir")
}

// LogFilePath derives a log path without touching the filesystem. stepPath
// must be relative to the package's step root.
func (l Layout) LogFilePath(cfg config.Config, stepPath string, at time.Time) (string, error) {
	logDir, err := l.packageLogDir(cfg, stepPath)
	if err != nil {
		return "", fmt.Errorf("building log file path for step %q: resolving package log path: %w", stepPath, err)
	}
	if at.IsZero() {
		return "", fmt.Errorf("log timestamp must not be zero")
	}

	return logDir + "-" + at.UTC().Format(logTimeFormat) + ".log", nil
}

// LogFiles identifies prior logs for one step without interpreting the step
// path as a glob expression.
type LogFiles struct {
	rootMount string
	directory string
	prefix    string
	suffix    string
}

// LogFilePattern returns the literal directory and filename shape used to find
// prior logs for one step.
func (l Layout) LogFilePattern(cfg config.Config, stepPath string) (LogFiles, error) {
	logBase, err := l.packageLogDir(cfg, stepPath)
	if err != nil {
		return LogFiles{}, fmt.Errorf("building log file pattern for step %q: resolving package log path: %w", stepPath, err)
	}
	return LogFiles{
		rootMount: l.rootMount,
		directory: filepath.Dir(logBase),
		prefix:    filepath.Base(logBase) + "-",
		suffix:    ".log",
	}, nil
}

// CreateLogFile creates and opens a unique log file beneath the mounted host
// root. The caller owns the returned file and must close it.
func (l Layout) CreateLogFile(
	cfg config.Config,
	stepPath string,
	at time.Time,
	mode fs.FileMode,
) (string, *os.File, error) {
	basePath, err := l.LogFilePath(cfg, stepPath, at)
	if err != nil {
		return "", nil, fmt.Errorf("creating log file for step %q: resolving log file path: %w", stepPath, err)
	}
	collision, err := nextLogCollision(l.rootMount, basePath)
	if err != nil {
		return "", nil, fmt.Errorf("creating log file for step %q: finding next collision index: %w", stepPath, err)
	}
	for ; ; collision++ {
		path := basePath
		if collision > 0 {
			path = strings.TrimSuffix(basePath, ".log") + fmt.Sprintf("-%d.log", collision)
		}
		file, err := hostfs.CreateFileWriter(l.rootMount, path, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("creating log file %q: %w", path, err)
		}
		return path, file, nil
	}
}

func nextLogCollision(rootMount, basePath string) (int, error) {
	entries, err := hostfs.ReadDir(rootMount, filepath.Dir(basePath))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading log directory %q: %w", filepath.Dir(basePath), err)
	}

	baseName := filepath.Base(basePath)
	prefix := strings.TrimSuffix(baseName, ".log") + "-"
	next := 0
	for _, entry := range entries {
		name := entry.Name()
		if name == baseName {
			next = max(next, 1)
			continue
		}
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		index, ok := parsePositiveDecimal(value)
		if ok {
			next = max(next, index+1)
		}
	}
	return next, nil
}

func (l Layout) packageLogDir(cfg config.Config, stepPath string) (string, error) {
	if err := validatePackagePath(cfg); err != nil {
		return "", fmt.Errorf("building package log path for step %q: validating package path: %w", stepPath, err)
	}
	if !filepath.IsLocal(stepPath) {
		return "", fmt.Errorf("step path %q must be relative to the package root", stepPath)
	}
	return filepath.Join(l.LogDir(), cfg.PackageName, cfg.PackageVersion, stepPath), nil
}

func validatePackagePath(cfg config.Config) error {
	if err := hostfs.ValidatePathComponent("package name", cfg.PackageName); err != nil {
		return err
	}
	if err := hostfs.ValidatePathComponent("package version", cfg.PackageVersion); err != nil {
		return err
	}
	return nil
}

func normalizeRootMount(rootMount string) string {
	if rootMount == "" {
		return string(filepath.Separator)
	}
	return filepath.Clean(rootMount)
}

func normalizeLayoutRoot(name, root string) (string, error) {
	root = strings.TrimLeft(root, string(filepath.Separator))
	root = filepath.Clean(root)
	if root == "." || !filepath.IsLocal(root) {
		return "", fmt.Errorf("%s %q must remain within the mounted host root", name, root)
	}
	return root, nil
}
