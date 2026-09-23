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

package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
)

const (
	legacyNodeFilesDir = "/etc/nvidia-bootstrap/node-files"
	configFileName     = "config.json"
	configMapsDirName  = "configmaps"
	rootOverlayDirName = "root_dir"
)

type packageDataSources struct {
	dataDir            string
	legacyNodeFilesDir string
}

func ensurePackageData(rootMount, copyRoot string, sources packageDataSources) error {
	info, err := os.Stat(copyRoot)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("package copy path %q is not a directory", copyRoot)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stating package copy path %q: %w", copyRoot, err)
	}
	if err := hostfs.CopyTreeIfExistsFollowingSymlinks(rootMount, sources.dataDir, copyRoot); err != nil {
		return fmt.Errorf("copying package data from %q: %w", sources.dataDir, err)
	}
	if _, err := os.Stat(copyRoot); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("package data directory %q does not exist", sources.dataDir)
	} else if err != nil {
		return fmt.Errorf("stating copied package data %q: %w", copyRoot, err)
	}
	if err := hostfs.CopyTreeIfExistsFollowingSymlinks(
		rootMount,
		sources.legacyNodeFilesDir,
		copyRoot,
	); err != nil {
		return fmt.Errorf("copying legacy node files from %q: %w", sources.legacyNodeFilesDir, err)
	}
	return nil
}

func loadConfig(copyRoot string, logger *slog.Logger) (*config.Config, error) {
	configPath := filepath.Join(copyRoot, configFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading package config %q: %w", configPath, err)
	}
	cfg, err := config.NewLoader().Load(data, flags.StepsDir(copyRoot), logger)
	if err != nil {
		return nil, fmt.Errorf("loading package config %q: %w", configPath, err)
	}
	return cfg, nil
}

func prepareHost(rootMount, copyRoot string, cfg config.Config) error {
	if err := hostfs.CopyTreeIfExistsFollowingSymlinks(
		rootMount,
		filepath.Join(copyRoot, rootOverlayDirName),
		rootMount,
	); err != nil {
		return fmt.Errorf("copying package root overlay: %w", err)
	}
	for _, expected := range cfg.ExpectedConfigFiles {
		if !filepath.IsLocal(expected) {
			return fmt.Errorf("expected config file %q must be relative to the configmaps directory", expected)
		}
		path := filepath.Join(copyRoot, configMapsDirName, expected)
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("expected config file %q was not found in the configmaps directory", expected)
		}
		if err != nil {
			return fmt.Errorf("stating expected config file %q: %w", expected, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("expected config file %q is not a regular file", expected)
		}
	}
	return nil
}

func copyResolverConfig(rootMount string) error {
	source := filepath.Join(string(filepath.Separator), "etc", "resolv.conf")
	destination, err := hostfs.HostPathToMounted(rootMount, source)
	if err != nil {
		return fmt.Errorf("resolving host resolver path: %w", err)
	}
	if err := hostfs.CopyFileFollowingSymlinks(rootMount, source, destination); err != nil {
		return fmt.Errorf("copying resolver configuration: %w", err)
	}
	return nil
}
