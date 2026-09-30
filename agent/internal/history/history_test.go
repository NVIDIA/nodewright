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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
)

func TestHistory(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "History Suite")
}

var _ = Describe("history store", func() {
	var (
		root   string
		dir    string
		store  *fileStore
		logger *slog.Logger
		cfg    config.Config
		now    time.Time
	)

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		dir = filepath.Join(root, "history")
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		cfg = config.Config{PackageName: "driver", PackageVersion: "1.2.3"}
		now = time.Date(2026, time.June, 30, 12, 34, 56, 123456789, time.FixedZone("test", -7*60*60))
		layout := flags.DefaultLayout(root)
		dir = layout.HistoryDir()

		var err error
		store, err = newFileStore(layout, cfg, logger)
		Expect(err).NotTo(HaveOccurred())
	})

	It("constructs the store behind its interface", func() {
		layout := flags.DefaultLayout(root)
		s, err := NewStore(layout, cfg, logger)
		Expect(err).NotTo(HaveOccurred())
		versions, err := s.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(Equal(Versions{Current: "1.2.3", Previous: UnknownVersion}))

		cfg.PackageVersion = ""
		s, err = NewStore(layout, cfg, logger)
		Expect(err).To(MatchError("package version must not be empty"))
		Expect(s).To(BeNil())
	})

	It("returns unknown without creating a missing history file", func() {
		versions, err := store.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(Equal(Versions{Current: "1.2.3", Previous: UnknownVersion}))
		Expect(dir).NotTo(BeADirectory())
	})

	It("reads an existing ledger", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		data := `{
  "current-version": "1.1.0",
  "history": [{"version":"1.1.0","time":"2024-08-28T14:33:20.123456+00:00"}]
}`
		Expect(os.WriteFile(store.Path(), []byte(data), 0o600)).To(Succeed())

		versions, err := store.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(Equal(Versions{Current: "1.2.3", Previous: "1.1.0"}))
	})

	It("treats empty or missing current versions as unknown", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())

		for _, data := range []string{`{}`, `{"current-version":"","history":[]}`} {
			Expect(os.WriteFile(store.Path(), []byte(data), 0o600)).To(Succeed())
			loaded, err := store.load()
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.CurrentVersion).To(Equal(UnknownVersion))
			Expect(loaded.Entries).To(Equal([]LedgerEntry{}))

			versions, err := store.Read()
			Expect(err).NotTo(HaveOccurred())
			Expect(versions.Previous).To(Equal(UnknownVersion))
		}
	})

	It("moves corrupt history aside and returns unknown", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.WriteFile(store.Path(), []byte("{"), 0o600)).To(Succeed())

		versions, err := store.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(versions.Previous).To(Equal(UnknownVersion))
		Expect(store.Path()).NotTo(BeAnExistingFile())
		backup, err := os.ReadFile(store.Path() + ".backup")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(backup)).To(Equal("{"))
	})

	It("records a new installed version", func() {
		Expect(store.Record(stage.ApplyCheck, now)).To(Succeed())

		data, err := os.ReadFile(store.Path())
		Expect(err).NotTo(HaveOccurred())
		var saved Ledger
		Expect(json.Unmarshal(data, &saved)).To(Succeed())
		Expect(saved.CurrentVersion).To(Equal("1.2.3"))
		Expect(saved.Entries).To(Equal([]LedgerEntry{{Version: "1.2.3", Time: now.UTC()}}))

		info, err := os.Stat(store.Path())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		Expect(names).To(ConsistOf("driver.json"), "no temporary or stray files may remain after Record")
	})

	It("prepends entries without losing prior history", func() {
		oldConfig := cfg
		oldConfig.PackageVersion = "1.1.0"
		oldStore, err := newFileStore(flags.DefaultLayout(root), oldConfig, logger)
		Expect(err).NotTo(HaveOccurred())
		Expect(oldStore.Record(stage.ApplyCheck, now.Add(-time.Hour))).To(Succeed())
		Expect(store.Record(stage.UpgradeCheck, now)).To(Succeed())

		data, err := os.ReadFile(store.Path())
		Expect(err).NotTo(HaveOccurred())
		var saved Ledger
		Expect(json.Unmarshal(data, &saved)).To(Succeed())
		Expect(saved.CurrentVersion).To(Equal("1.2.3"))
		Expect(saved.Entries).To(Equal([]LedgerEntry{
			{Version: "1.2.3", Time: now.UTC()},
			{Version: "1.1.0", Time: now.Add(-time.Hour).UTC()},
		}))
	})

	It("retains only the most recent history entries", func() {
		entries := make([]LedgerEntry, historyEntryLimit)
		for i := range entries {
			entries[i] = LedgerEntry{Version: "old", Time: now.Add(-time.Duration(i+1) * time.Hour).UTC()}
		}
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		data, err := json.Marshal(Ledger{CurrentVersion: "old", Entries: entries})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(store.Path(), data, 0o600)).To(Succeed())

		Expect(store.Record(stage.UpgradeCheck, now)).To(Succeed())
		data, err = os.ReadFile(store.Path())
		Expect(err).NotTo(HaveOccurred())
		var saved Ledger
		Expect(json.Unmarshal(data, &saved)).To(Succeed())
		Expect(saved.Entries).To(HaveLen(historyEntryLimit))
		Expect(saved.Entries[0]).To(Equal(LedgerEntry{Version: cfg.PackageVersion, Time: now.UTC()}))
		Expect(saved.Entries[1:]).To(Equal(entries[:historyEntryLimit-1]))
	})

	It("records uninstall completion distinctly", func() {
		Expect(store.Record(stage.UninstallCheck, now)).To(Succeed())

		versions, err := store.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(versions.Previous).To(Equal(UninstalledVersion))
	})

	It("recovers corrupt history before recording", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.WriteFile(store.Path(), []byte("{"), 0o600)).To(Succeed())

		Expect(store.Record(stage.ApplyCheck, now)).To(Succeed())
		Expect(store.Path() + ".backup").To(BeAnExistingFile())
		versions, err := store.Read()
		Expect(err).NotTo(HaveOccurred())
		Expect(versions.Previous).To(Equal("1.2.3"))
	})

	It("rejects invalid package identities at construction", func() {
		invalid := cfg
		invalid.PackageVersion = ""
		layout := flags.DefaultLayout(root)
		_, err := newFileStore(layout, invalid, logger)
		Expect(err).To(MatchError("package version must not be empty"))

		invalid = cfg
		invalid.PackageName = "../driver"
		_, err = newFileStore(layout, invalid, logger)
		Expect(err).To(MatchError(ContainSubstring(`package name "../driver" must be a single path component`)))

		invalid.PackageName = ""
		_, err = newFileStore(layout, invalid, logger)
		Expect(err).To(MatchError(ContainSubstring("single path component")))
		Expect(dir).NotTo(BeADirectory())
	})

	It("rejects invalid record input before writing", func() {
		err := store.Record(stage.Stage("bogus"), now)
		Expect(err).To(MatchError(ContainSubstring(`recording history for package "driver": validating completed stage`)))
		Expect(err).To(MatchError(ContainSubstring(`unknown stage "bogus"`)))
		Expect(store.Record(stage.ApplyCheck, time.Time{})).To(MatchError("history timestamp must not be zero"))
		Expect(dir).NotTo(BeADirectory())
	})

	It("returns filesystem errors with context", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.Mkdir(store.Path(), 0o755)).To(Succeed())

		_, err := store.Read()
		Expect(err).To(MatchError(ContainSubstring("reading history")))
	})

	It("does not follow a symlinked history directory", func() {
		outside := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Dir(dir), 0o755)).To(Succeed())
		Expect(os.Symlink(outside, dir)).To(Succeed())

		err := store.Record(stage.ApplyCheck, now)
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		Expect(filepath.Join(outside, "driver.json")).NotTo(BeAnExistingFile())
	})
})
