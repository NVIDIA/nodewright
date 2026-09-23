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
	"testing"

	"github.com/NVIDIA/nodewright/agent/internal/command"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

func TestFlags(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Flags Suite")
}

var _ = Describe("flag store", func() {
	var (
		root  string
		store *fileStore
		cfg   config.Config
		value step.RegularStep
	)

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		cfg = config.Config{PackageName: "driver", PackageVersion: "1.2.3"}
		value = step.NewRegularStep(
			"scripts/apply.sh",
			step.WithArguments([]string{"--mode", "fast"}),
			step.WithReturncodes([]command.ExitCode{0, 2}),
		)

		var err error
		store, err = newFileStore(DefaultLayout(root), cfg)
		Expect(err).NotTo(HaveOccurred())
	})

	It("constructs the store behind its interface", func() {
		s, err := NewStore(DefaultLayout(root), cfg)
		Expect(err).NotTo(HaveOccurred())
		decision, err := s.Check(value, false, stage.Apply)
		Expect(err).NotTo(HaveOccurred())
		Expect(decision).To(Equal(Decision{Run: true, Reason: ReasonFlagMissing}))

		invalid := cfg
		invalid.PackageName = "../driver"
		s, err = NewStore(DefaultLayout(root), invalid)
		Expect(err).To(MatchError(ContainSubstring("single path component")))
		Expect(s).To(BeNil())
	})

	It("uses a stable Go-native fingerprint", func() {
		path, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(path).To(Equal(filepath.Join(
			root,
			"etc", "skyhook", "flags", "driver", "1.2.3",
			"apply.sh-f3f526b85e0a962f2b4b68089385cd5b4829c09a436112f6586ff5f288e4ecf4.flag",
		)))
	})

	It("changes the fingerprint when execution inputs change", func() {
		original, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		environmentChanged := value
		environmentChanged.Env = map[string]string{"MODE": "debug"}
		hostExecutionChanged := value
		hostExecutionChanged.OnHost = false

		cases := []step.RegularStep{
			step.NewRegularStep("scripts/other.sh", step.WithArguments([]string{"--mode", "fast"}), step.WithReturncodes([]command.ExitCode{0, 2})),
			step.NewRegularStep("scripts/apply.sh", step.WithArguments([]string{"--mode", "slow"}), step.WithReturncodes([]command.ExitCode{0, 2})),
			step.NewRegularStep("scripts/apply.sh", step.WithArguments([]string{"--mode", "fast"}), step.WithReturncodes([]command.ExitCode{command.SuccessExitCode})),
			environmentChanged,
			hostExecutionChanged,
		}
		for _, candidate := range cases {
			path, err := store.Path(candidate)
			Expect(err).NotTo(HaveOccurred())
			Expect(path).NotTo(Equal(original))
		}
	})

	It("supports upgrade steps", func() {
		upgrade, err := step.NewUpgradeStep("upgrade.sh")
		Expect(err).NotTo(HaveOccurred())

		path, err := store.Path(upgrade)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Base(path)).To(HavePrefix("upgrade.sh-"))
	})

	It("writes a private flag file", func() {
		path, err := store.Mark(value, "complete")
		Expect(err).NotTo(HaveOccurred())

		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("complete"))
		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("writes the legacy completion marker alongside the Go-native marker", func() {
		path, err := store.Mark(value, "complete")
		Expect(err).NotTo(HaveOccurred())
		legacyPath := filepath.Join(
			root,
			"etc", "skyhook", "flags", "driver", "1.2.3",
			"scripts", "apply.sh_WyctLW1vZGUnLCAnZmFzdCddX1swLCAyXQ==",
		)

		Expect(path).To(BeAnExistingFile())
		Expect(legacyPath).To(BeAnExistingFile())
		data, err := os.ReadFile(legacyPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("complete"))
	})

	It("does not publish either completion marker when preflight fails", func() {
		path, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		legacyPath, supported := store.legacyPath(value)
		Expect(supported).To(BeTrue())
		Expect(os.MkdirAll(path, 0o755)).To(Succeed())

		_, err = store.Mark(value, "complete")

		Expect(err).To(MatchError(ContainSubstring("is not a regular file")))
		Expect(legacyPath).NotTo(BeAnExistingFile())
		Expect(path).To(BeADirectory())
	})

	It("recognizes and removes a legacy completion marker", func() {
		legacyPath, supported := store.legacyPath(value)
		Expect(supported).To(BeTrue())
		Expect(store.Write(legacyPath, []byte("complete"))).To(Succeed())

		decision, err := store.Check(value, false, stage.Apply)
		Expect(err).NotTo(HaveOccurred())
		Expect(decision).To(Equal(Decision{Run: false, Reason: ReasonAlreadyCompleted}))

		Expect(store.Remove(value)).To(Succeed())
		Expect(legacyPath).NotTo(BeAnExistingFile())
	})

	It("matches legacy string representations in completion fingerprints", func() {
		value := step.NewRegularStep(
			"apply.sh",
			step.WithArguments([]string{"single'quote", "both'\"quotes", "line\nbreak"}),
		)

		legacyPath, supported := store.legacyPath(value)
		Expect(supported).To(BeTrue())
		Expect(filepath.Base(legacyPath)).To(Equal(
			"apply.sh_WyJzaW5nbGUncXVvdGUiLCAnYm90aFwnInF1b3RlcycsICdsaW5lXG5icmVhayddX1swXQ==",
		))
	})

	It("removes a completion flag idempotently", func() {
		path, err := store.Mark(value, "complete")
		Expect(err).NotTo(HaveOccurred())
		legacyPath, supported := store.legacyPath(value)
		Expect(supported).To(BeTrue())

		Expect(store.Remove(value)).To(Succeed())
		Expect(path).NotTo(BeAnExistingFile())
		Expect(legacyPath).NotTo(BeAnExistingFile())
		Expect(store.Remove(value)).To(Succeed())
	})

	It("refuses to remove a non-regular flag path", func() {
		path, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(path, 0o755)).To(Succeed())

		Expect(store.Remove(value)).To(MatchError(ContainSubstring("is not a regular file")))
		Expect(path).To(BeADirectory())
	})

	It("does not follow a symlinked flag when removing", func() {
		path, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		target := filepath.Join(GinkgoT().TempDir(), "complete")
		Expect(os.WriteFile(target, nil, 0o600)).To(Succeed())
		Expect(os.Symlink(target, path)).To(Succeed())

		Expect(store.Remove(value)).To(MatchError(ContainSubstring("is a symbolic link")))
		Expect(target).To(BeAnExistingFile())
	})

	It("refuses to remove a flag for a step outside the package root", func() {
		escaping := step.NewRegularStep("../escape.sh")

		err := store.Remove(escaping)
		Expect(err).To(MatchError(ContainSubstring("resolving flag path")))
		Expect(err).To(MatchError(ContainSubstring("must be relative to the package root")))
	})

	It("writes control flags inside the store", func() {
		path := filepath.Join(DefaultLayout(root).FlagDir(), "START")
		Expect(store.Write(path, []byte("started"))).To(Succeed())
		Expect(store.Write(path, []byte("restarted"))).To(Succeed())
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("restarted"))
	})

	It("rejects a symlinked flag parent", func() {
		flagDir := DefaultLayout(root).FlagDir()
		Expect(os.MkdirAll(flagDir, 0o755)).To(Succeed())
		outside := GinkgoT().TempDir()
		linkedParent := filepath.Join(flagDir, "linked")
		Expect(os.Symlink(outside, linkedParent)).To(Succeed())

		err := store.Write(filepath.Join(linkedParent, "START"), []byte("started"))
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		Expect(filepath.Join(outside, "START")).NotTo(BeAnExistingFile())
	})

	It("rejects a symlinked flag target without modifying it", func() {
		flagDir := DefaultLayout(root).FlagDir()
		Expect(os.MkdirAll(flagDir, 0o755)).To(Succeed())
		outside := filepath.Join(GinkgoT().TempDir(), "target")
		Expect(os.WriteFile(outside, []byte("original"), 0o600)).To(Succeed())
		flag := filepath.Join(flagDir, "START")
		Expect(os.Symlink(outside, flag)).To(Succeed())

		Expect(store.Write(flag, []byte("changed"))).To(MatchError(ContainSubstring("is a symbolic link")))
		data, err := os.ReadFile(outside)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("original"))
	})

	It("refuses to write outside the store", func() {
		outside := filepath.Join(root, "outside")
		Expect(store.Write(outside, nil)).To(MatchError(ContainSubstring("must be contained")))
		Expect(outside).NotTo(BeAnExistingFile())
	})

	It("runs when no flag exists", func() {
		decision, err := store.Check(value, false, stage.Apply)
		Expect(err).NotTo(HaveOccurred())
		Expect(decision).To(Equal(Decision{Run: true, Reason: ReasonFlagMissing}))
	})

	It("skips an idempotent completed step", func() {
		_, err := store.Mark(value, "")
		Expect(err).NotTo(HaveOccurred())

		decision, err := store.Check(value, false, stage.Apply)
		Expect(err).NotTo(HaveOccurred())
		Expect(decision).To(Equal(Decision{Run: false, Reason: ReasonAlreadyCompleted}))
	})

	It("does not accept a symlink as a completion flag", func() {
		path, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		target := filepath.Join(GinkgoT().TempDir(), "complete")
		Expect(os.WriteFile(target, nil, 0o600)).To(Succeed())
		Expect(os.Symlink(target, path)).To(Succeed())

		_, err = store.Check(value, false, stage.Apply)
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
	})

	It("rejects a non-file at the flag path", func() {
		path, err := store.Path(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(path, 0o755)).To(Succeed())

		_, err = store.Check(value, false, stage.Apply)
		Expect(err).To(MatchError(ContainSubstring("is not a regular file")))
	})

	It("rejects invalid package identities at construction", func() {
		invalid := cfg
		invalid.PackageName = "../driver"
		_, err := newFileStore(DefaultLayout(root), invalid)
		Expect(err).To(MatchError(ContainSubstring(`package name "../driver" must be a single path component`)))

		invalid = cfg
		invalid.PackageVersion = "1.2.3/patched"
		_, err = newFileStore(DefaultLayout(root), invalid)
		Expect(err).To(MatchError(ContainSubstring("single path component")))
	})

	It("rejects steps that escape the package root", func() {
		escaping := step.NewRegularStep("../escape.sh")
		_, err := store.Path(escaping)
		Expect(err).To(MatchError(ContainSubstring(`step path "../escape.sh" must be relative to the package root`)))
	})

	It("adds operation context when marking fails", func() {
		escaping := step.NewRegularStep("../escape.sh")
		_, err := store.Mark(escaping, "")
		Expect(err).To(MatchError(ContainSubstring("marking step: resolving flag path")))
		Expect(err).To(MatchError(ContainSubstring("must be relative to the package root")))
	})

	It("rejects unknown stages", func() {
		_, err := store.Check(value, false, stage.Stage("bogus"))
		Expect(err).To(MatchError(ContainSubstring(`checking flag for step "scripts/apply.sh": validating stage`)))
		Expect(err).To(MatchError(ContainSubstring(`unknown stage "bogus"`)))
	})
})

var _ = DescribeTable(
	"Decide",
	func(flagExists, alwaysRun bool, currentStage stage.Stage, idempotence step.Idempotence, expected Decision) {
		Expect(Decide(flagExists, alwaysRun, currentStage, idempotence)).To(Equal(expected))
	},
	Entry("missing flag", false, false, stage.Apply, step.Auto, Decision{Run: true, Reason: ReasonFlagMissing}),
	Entry("always-run override", true, true, stage.Apply, step.Auto, Decision{Run: true, Reason: ReasonAlwaysRun}),
	Entry("config stage", true, false, stage.Config, step.Auto, Decision{Run: true, Reason: ReasonStageAlwaysRuns}),
	Entry("uninstall stage", true, false, stage.Uninstall, step.Auto, Decision{Run: true, Reason: ReasonStageAlwaysRuns}),
	Entry("upgrade stage", true, false, stage.Upgrade, step.Auto, Decision{Run: true, Reason: ReasonStageAlwaysRuns}),
	Entry("disabled idempotence", true, false, stage.Apply, step.Disabled, Decision{Run: true, Reason: ReasonIdempotenceDisabled}),
	Entry("completed idempotent step", true, false, stage.Apply, step.Auto, Decision{Run: false, Reason: ReasonAlreadyCompleted}),
)
