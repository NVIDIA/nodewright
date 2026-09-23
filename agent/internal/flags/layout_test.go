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

var _ = Describe("Layout", func() {
	It("resolves the default host directories", func() {
		root := GinkgoT().TempDir()
		layout := DefaultLayout(root)

		Expect(layout.RootMount()).To(Equal(root))
		Expect(layout.StateDir()).To(Equal(filepath.Join(root, "etc", "skyhook")))
		Expect(layout.FlagDir()).To(Equal(filepath.Join(root, "etc", "skyhook", "flags")))
		Expect(layout.HistoryDir()).To(Equal(filepath.Join(root, "etc", "skyhook", "history")))
		Expect(layout.LogDir()).To(Equal(filepath.Join(root, "var", "log", "skyhook")))
	})

	It("uses caller-supplied state and log roots", func() {
		layout, err := NewLayout("/host", "/state/nodewright", "/logs/nodewright")
		Expect(err).NotTo(HaveOccurred())
		Expect(layout.StateDir()).To(Equal("/host/state/nodewright"))
		Expect(layout.LogDir()).To(Equal("/host/logs/nodewright"))
	})

	It("rejects roots that traverse above the mounted host root", func() {
		_, err := NewLayout("/host", "../../state", "/logs/nodewright")
		Expect(err).To(MatchError(ContainSubstring("constructing layout: normalizing state root")))
		Expect(err).To(MatchError(ContainSubstring("must remain within the mounted host root")))

		_, err = NewLayout("/host", "/state/nodewright", "/../../logs")
		Expect(err).To(MatchError(ContainSubstring("constructing layout: normalizing log root")))
		Expect(err).To(MatchError(ContainSubstring("must remain within the mounted host root")))
	})

	It("resolves the copied step directory", func() {
		Expect(StepsDir("/copy/package")).To(Equal("/copy/package/skyhook_dir"))
	})
})

var _ = Describe("log paths", func() {
	var (
		root   string
		layout Layout
		cfg    config.Config
	)

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		layout = DefaultLayout(root)
		cfg = config.Config{PackageName: "driver", PackageVersion: "1.2.3"}
	})

	It("uses UTC and preserves a relative step hierarchy", func() {
		zone := time.FixedZone("test", 2*60*60)
		at := time.Date(2026, time.June, 30, 12, 34, 56, 0, zone)

		path, err := layout.LogFilePath(cfg, "scripts/apply.sh", at)
		Expect(err).NotTo(HaveOccurred())
		Expect(path).To(Equal(filepath.Join(
			root,
			"var", "log", "skyhook", "driver", "1.2.3", "scripts", "apply.sh-2026-06-30-103456.000000000.log",
		)))
	})

	It("creates a rooted log file and its parent directory", func() {
		path, file, err := layout.CreateLogFile(
			cfg,
			"scripts/apply.sh",
			time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			0o600,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Close()).To(Succeed())

		info, err := os.Stat(filepath.Dir(path))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
	})

	It("does not reuse a log path when timestamps collide", func() {
		at := time.Date(2026, 1, 2, 3, 4, 5, 123, time.UTC)
		firstPath, first, err := layout.CreateLogFile(cfg, "apply.sh", at, 0o600)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Close()).To(Succeed())
		secondPath, second, err := layout.CreateLogFile(cfg, "apply.sh", at, 0o600)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Close()).To(Succeed())
		Expect(os.Remove(firstPath)).To(Succeed())
		thirdPath, third, err := layout.CreateLogFile(cfg, "apply.sh", at, 0o600)
		Expect(err).NotTo(HaveOccurred())
		Expect(third.Close()).To(Succeed())

		Expect(secondPath).NotTo(Equal(firstPath))
		Expect(secondPath).To(Equal(strings.TrimSuffix(firstPath, ".log") + "-1.log"))
		Expect(thirdPath).To(Equal(strings.TrimSuffix(firstPath, ".log") + "-2.log"))
	})

	It("derives a literal log selector", func() {
		pattern, err := layout.LogFilePattern(cfg, "scripts/apply[*?].sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(pattern.directory).To(Equal(filepath.Join(root, "var", "log", "skyhook", "driver", "1.2.3", "scripts")))
		Expect(pattern.prefix).To(Equal("apply[*?].sh-"))
		Expect(pattern.suffix).To(Equal(".log"))
	})

	It("rejects paths that escape the package root", func() {
		_, err := layout.LogFilePath(cfg, "../apply.sh", time.Now())
		Expect(err).To(MatchError(ContainSubstring(`building log file path for step "../apply.sh": resolving package log path`)))
		Expect(err).To(MatchError(ContainSubstring("must be relative")))
	})

	It("adds operation context to package path failures", func() {
		invalidConfig := cfg
		invalidConfig.PackageName = "../driver"

		_, err := layout.LogFilePattern(invalidConfig, "apply.sh")
		Expect(err).To(MatchError(ContainSubstring(`building log file pattern for step "apply.sh": resolving package log path`)))

		_, _, err = layout.CreateLogFile(invalidConfig, "apply.sh", time.Now(), 0o600)
		Expect(err).To(MatchError(ContainSubstring(`creating log file for step "apply.sh": resolving log file path`)))

		_, err = layout.packageLogDir(invalidConfig, "apply.sh")
		Expect(err).To(MatchError(ContainSubstring(`building package log path for step "apply.sh": validating package path`)))
	})

	It("requires an explicit timestamp", func() {
		_, err := layout.LogFilePath(cfg, "apply.sh", time.Time{})
		Expect(err).To(MatchError("log timestamp must not be zero"))
	})
})
