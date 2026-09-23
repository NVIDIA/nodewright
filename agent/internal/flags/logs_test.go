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
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/agent/internal/config"
)

var _ = Describe("CleanupOldLogs", func() {
	It("keeps the newest filename timestamps for a literal step path", func() {
		root := GinkgoT().TempDir()
		layout := DefaultLayout(root)
		cfg := config.Config{PackageName: "driver", PackageVersion: "1.2.3"}
		stepPath := "scripts/apply[*?].sh"
		logFiles, err := layout.LogFilePattern(cfg, stepPath)
		Expect(err).NotTo(HaveOccurred())
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		creationOrder := []int{5, 6, 7, 8, 9, 0, 1, 2, 3, 4}
		paths := make([]string, len(creationOrder))
		for _, second := range creationOrder {
			path, err := layout.LogFilePath(cfg, stepPath, base.Add(time.Duration(second)*time.Second))
			Expect(err).NotTo(HaveOccurred())
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte("log"), 0o600)).To(Succeed())
			paths[second] = path
		}
		unrelated := filepath.Join(logFiles.directory, "applyX.sh-2026-01-01-000000.log")
		malformed := filepath.Join(logFiles.directory, logFiles.prefix+"not-a-timestamp"+logFiles.suffix)
		legacy := filepath.Join(logFiles.directory, logFiles.prefix+"2025-01-01-000000"+logFiles.suffix)
		Expect(os.WriteFile(unrelated, nil, 0o600)).To(Succeed())
		Expect(os.WriteFile(malformed, nil, 0o600)).To(Succeed())
		Expect(os.WriteFile(legacy, nil, 0o600)).To(Succeed())

		Expect(CleanupOldLogs(logFiles, DefaultLogRetention)).To(Succeed())
		for _, path := range paths[:5] {
			Expect(path).NotTo(BeAnExistingFile())
		}
		for _, path := range paths[5:] {
			Expect(path).To(BeAnExistingFile())
		}
		Expect(unrelated).To(BeAnExistingFile())
		Expect(malformed).To(BeAnExistingFile())
		Expect(legacy).NotTo(BeAnExistingFile())
	})

	It("does nothing when no files match", func() {
		layout := DefaultLayout(GinkgoT().TempDir())
		logFiles, err := layout.LogFilePattern(config.Config{PackageName: "driver", PackageVersion: "1.2.3"}, "apply.sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(CleanupOldLogs(logFiles, DefaultLogRetention)).To(Succeed())
	})

	It("ignores collision suffixes containing non-digits", func() {
		layout := DefaultLayout(GinkgoT().TempDir())
		cfg := config.Config{PackageName: "driver", PackageVersion: "1.2.3"}
		path, err := layout.LogFilePath(cfg, "apply.sh", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		Expect(err).NotTo(HaveOccurred())
		path = strings.TrimSuffix(path, ".log") + "-+1.log"
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, nil, 0o600)).To(Succeed())
		logFiles, err := layout.LogFilePattern(cfg, "apply.sh")
		Expect(err).NotTo(HaveOccurred())

		Expect(CleanupOldLogs(logFiles, 0)).To(Succeed())
		Expect(path).To(BeAnExistingFile())
	})

	It("keeps the newest files when timestamps collide", func() {
		layout := DefaultLayout(GinkgoT().TempDir())
		cfg := config.Config{PackageName: "driver", PackageVersion: "1.2.3"}
		at := time.Date(2026, 1, 1, 0, 0, 0, 123, time.UTC)
		paths := make([]string, 7)
		for index := range paths {
			path, file, err := layout.CreateLogFile(cfg, "apply.sh", at, 0o600)
			Expect(err).NotTo(HaveOccurred())
			Expect(file.Close()).To(Succeed())
			paths[index] = path
		}
		logFiles, err := layout.LogFilePattern(cfg, "apply.sh")
		Expect(err).NotTo(HaveOccurred())

		Expect(CleanupOldLogs(logFiles, DefaultLogRetention)).To(Succeed())
		for _, path := range paths[:2] {
			Expect(path).NotTo(BeAnExistingFile())
		}
		for _, path := range paths[2:] {
			Expect(path).To(BeAnExistingFile())
		}

		newPath, file, err := layout.CreateLogFile(cfg, "apply.sh", at, 0o600)
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Close()).To(Succeed())
		Expect(newPath).NotTo(BeElementOf(paths[:2]))

		Expect(CleanupOldLogs(logFiles, DefaultLogRetention)).To(Succeed())
		Expect(newPath).To(BeAnExistingFile())
		Expect(paths[2]).NotTo(BeAnExistingFile())
		for _, path := range paths[3:] {
			Expect(path).To(BeAnExistingFile())
		}
	})

	It("does not follow a symlinked log directory", func() {
		root := GinkgoT().TempDir()
		layout := DefaultLayout(root)
		logFiles, err := layout.LogFilePattern(
			config.Config{PackageName: "driver", PackageVersion: "1.2.3"},
			"scripts/apply.sh",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(logFiles.directory), 0o755)).To(Succeed())
		Expect(os.Symlink(GinkgoT().TempDir(), logFiles.directory)).To(Succeed())

		err = CleanupOldLogs(logFiles, DefaultLogRetention)
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
	})

	It("rejects negative retention", func() {
		Expect(CleanupOldLogs(LogFiles{}, -1)).To(MatchError("log retention must not be negative: -1"))
	})
})
