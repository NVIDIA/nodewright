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
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/history"
	historymock "github.com/NVIDIA/nodewright/agent/internal/history/mock"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
	stepmock "github.com/NVIDIA/nodewright/agent/internal/step/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

func newMockStep(path string) *stepmock.MockStep {
	value := stepmock.NewMockStep(GinkgoT())
	value.EXPECT().Path().Return(path).Maybe()
	value.EXPECT().
		Fingerprint().
		Return("fingerprint-"+filepath.Base(path), nil).
		Maybe()
	value.EXPECT().Idempotence().Return(step.Auto).Maybe()
	value.EXPECT().
		ExecutionMetadata().
		Return(step.ExecutionMetadata{
			Arguments:   []string{},
			ReturnCodes: []command.ExitCode{command.SuccessExitCode},
			OnHost:      true,
		}).
		Maybe()
	return value
}

func newRunnableMockStep(path string, status execution.Status) *stepmock.MockStep {
	value := newMockStep(path)
	value.EXPECT().
		Run(mock.Anything, mock.Anything).
		Return(status, nil).
		Once()
	return value
}

var _ = Describe("step orchestration", func() {
	var (
		root         string
		layout       flags.Layout
		cfg          config.Config
		runtime      runtimeConfig
		req          request
		flagStore    flags.Store
		historyStore *historymock.MockStore
	)

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		layout = flags.DefaultLayout(root)
		cfg = config.Config{
			PackageName:    "package",
			PackageVersion: "1.2.3",
			Modes:          map[stage.Stage][]step.Step{},
		}
		runtime = runtimeConfig{
			stdout: io.Discard,
			stderr: io.Discard,
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		req = request{
			stage:     stage.Apply,
			rootMount: root,
			copyDir:   "/packages/package",
		}
		var err error
		flagStore, err = flags.NewStore(layout, cfg)
		Expect(err).NotTo(HaveOccurred())
		historyStore = historymock.NewMockStore(GinkgoT())
	})

	It("writes START and completion flags around successful steps", func() {
		first := newRunnableMockStep("first.sh", execution.StatusSuccess)
		second := newRunnableMockStep("second.sh", execution.StatusSuccess)
		cfg.Modes[stage.Apply] = []step.Step{first, second}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(layout.FlagDir(), startFlagName)).To(BeAnExistingFile())
		firstFlag, err := flagStore.Path(first)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstFlag).To(BeAnExistingFile())
	})

	It("writes the step header after START and before execution", func() {
		output := &bytes.Buffer{}
		runtime.stdout = output
		value := newMockStep("apply.sh")
		value.EXPECT().
			Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				Expect(filepath.Join(layout.FlagDir(), startFlagName)).To(BeAnExistingFile())
				Expect(output.String()).To(Equal(
					"apply apply.sh [] [0] Idempotence.Auto True\n",
				))
			}).
			Return(execution.StatusSuccess, nil).
			Once()
		cfg.Modes[stage.Apply] = []step.Step{value}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("prints resolved package versions in an upgrade step header", func() {
		req.stage = stage.Upgrade
		versions := history.Versions{Previous: "1.0.0", Current: "1.2.3"}
		historyStore.EXPECT().Read().Return(versions, nil).Once()
		value, err := step.NewUpgradeStep("upgrade.sh", step.WithOnHost(false))
		Expect(err).NotTo(HaveOccurred())
		cfg.Modes[stage.Upgrade] = []step.Step{value}
		stepDirectory := filepath.Join(root, "packages", "package", "skyhook_dir")
		Expect(os.MkdirAll(stepDirectory, 0o755)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(stepDirectory, value.Path()),
			[]byte("#!/bin/sh\nexit 0\n"),
			0o700,
		)).To(Succeed())
		output := &bytes.Buffer{}
		runtime.stdout = output

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(output.String()).To(Equal(
			"upgrade upgrade.sh ['1.0.0', '1.2.3'] [0] Idempotence.Auto False\n",
		))
	})

	It("formats step headers like the legacy agent", func() {
		value := step.NewRegularStep(
			"apply.sh",
			step.WithArguments([]string{"plain", "it's"}),
			step.WithReturncodes([]command.ExitCode{command.SuccessExitCode, 2}),
			step.WithIdempotence(step.Disabled),
			step.WithOnHost(false),
		)
		output := &bytes.Buffer{}

		Expect(writeStepHeader(output, stage.Apply, value)).To(Succeed())

		Expect(output.String()).To(Equal(
			"apply apply.sh ['plain', \"it's\"] [0, 2] Idempotence.Disabled False\n",
		))
	})

	It("skips an idempotent step that is already complete", func() {
		value := newMockStep("apply.sh")
		cfg.Modes[stage.Apply] = []step.Step{value}
		_, err := flagStore.Mark(value, "complete")
		Expect(err).NotTo(HaveOccurred())

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("runs a completed step when the always-run policy is enabled", func() {
		value := newRunnableMockStep("apply.sh", execution.StatusSuccess)
		cfg.Modes[stage.Apply] = []step.Step{value}
		runtime.alwaysRunStep = true
		logOutput := &bytes.Buffer{}
		runtime.logger = slog.New(slog.NewTextHandler(logOutput, nil))
		_, err := flagStore.Mark(value, "complete")
		Expect(err).NotTo(HaveOccurred())

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(logOutput.String()).To(ContainSubstring(
			"running completed step because always-run policy is enabled",
		))
	})

	It("stops a normal stage after the first failed step", func() {
		first := newRunnableMockStep("first.sh", execution.StatusFailed)
		second := newMockStep("second.sh")
		cfg.Modes[stage.Apply] = []step.Step{first, second}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
	})

	It("runs every check and persists mixed results", func() {
		req.stage = stage.ApplyCheck
		first := newRunnableMockStep("first.sh", execution.StatusSuccess)
		second := newRunnableMockStep("second.sh", execution.StatusFailed)
		cfg.Modes[stage.ApplyCheck] = []step.Step{first, second}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		data, err := os.ReadFile(filepath.Join(layout.FlagDir(), checkResultsFlagName))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("first.sh False\nsecond.sh True"))
		Expect(filepath.Join(layout.FlagDir(), "apply-check_ALL_CHECKED")).NotTo(BeAnExistingFile())
	})

	It("marks successful checks and records package history", func() {
		req.stage = stage.UpgradeCheck
		value := newRunnableMockStep("check.sh", execution.StatusSuccess)
		cfg.Modes[stage.UpgradeCheck] = []step.Step{value}
		versions := history.Versions{Previous: "1.0.0", Current: "1.2.3"}
		historyStore.EXPECT().Read().Return(versions, nil).Once()
		historyStore.EXPECT().
			Record(stage.UpgradeCheck, mock.Anything).
			Return(nil).
			Once()
		value.EXPECT().
			WithVersions(versions.Previous, versions.Current).
			Return(value).
			Once()

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(layout.FlagDir(), "upgrade-check_ALL_CHECKED")).To(BeAnExistingFile())
	})

	It("removes every step completion flag after uninstall-check", func() {
		req.stage = stage.UninstallCheck
		apply := newMockStep("apply.sh")
		uninstallCheck := newRunnableMockStep("uninstall-check.sh", execution.StatusSuccess)
		historyStore.EXPECT().
			Record(stage.UninstallCheck, mock.Anything).
			Return(nil).
			Once()
		cfg.Modes[stage.Apply] = []step.Step{apply}
		cfg.Modes[stage.UninstallCheck] = []step.Step{uninstallCheck}
		for _, value := range []step.Step{apply, uninstallCheck} {
			_, err := flagStore.Mark(value, "complete")
			Expect(err).NotTo(HaveOccurred())
		}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		for _, value := range []step.Step{apply, uninstallCheck} {
			path, err := flagStore.Path(value)
			Expect(err).NotTo(HaveOccurred())
			Expect(path).NotTo(BeAnExistingFile())
		}
	})

	It("propagates cancellation and does not start another step", func() {
		ctx, cancel := context.WithCancel(context.Background())
		first := newMockStep("first.sh")
		first.EXPECT().
			Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				cancel()
			}).
			Return(execution.StatusSuccess, nil).
			Once()
		second := newMockStep("second.sh")
		cfg.Modes[stage.Apply] = []step.Step{first, second}

		status, err := runSteps(ctx, req, runtime, layout, cfg, flagStore, historyStore)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("composes command output with a retained log file", func() {
		runtime.writeLogs = true
		streamed := &bytes.Buffer{}
		runtime.stdout = streamed
		output := "step output\n"
		value := newMockStep("apply.sh")
		value.EXPECT().
			Run(mock.Anything, mock.Anything).
			Run(func(_ context.Context, cfg execution.Config) {
				_, err := io.WriteString(cfg.Stdout(), output)
				Expect(err).NotTo(HaveOccurred())
			}).
			Return(execution.StatusSuccess, nil).
			Once()
		cfg.Modes[stage.Apply] = []step.Step{value}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		entries, err := os.ReadDir(filepath.Join(layout.LogDir(), cfg.PackageName, cfg.PackageVersion))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		data, err := os.ReadFile(filepath.Join(layout.LogDir(), cfg.PackageName, cfg.PackageVersion, entries[0].Name()))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(output))
		Expect(streamed.String()).To(Equal(
			"apply apply.sh [] [0] Idempotence.Auto True\n" + output,
		))
	})

	It("marks a successful step complete before log cleanup", func() {
		runtime.writeLogs = true
		logDirectory := filepath.Join(layout.LogDir(), cfg.PackageName, cfg.PackageVersion)
		savedLogDirectory := logDirectory + "-saved"
		outside := GinkgoT().TempDir()
		value := newMockStep("apply.sh")
		value.EXPECT().
			Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				Expect(os.Rename(logDirectory, savedLogDirectory)).To(Succeed())
				Expect(os.Symlink(outside, logDirectory)).To(Succeed())
			}).
			Return(execution.StatusSuccess, nil).
			Once()
		cfg.Modes[stage.Apply] = []step.Step{value}

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("cleaning old logs for step")))
		completionFlag, err := flagStore.Path(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(completionFlag).To(BeAnExistingFile())

		status, err = runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("cleans old retained logs after repeated runtime errors", func() {
		runtime.writeLogs = true
		value := newMockStep("apply.sh")
		value.EXPECT().
			Run(mock.Anything, mock.Anything).
			Return(execution.StatusFailed, errors.New("run failed")).
			Times(flags.DefaultLogRetention + 2)

		for range flags.DefaultLogRetention + 2 {
			status, err := runStep(context.Background(), req, runtime, layout, cfg, value, nil)
			Expect(status).To(Equal(execution.StatusFailed))
			Expect(err).To(MatchError(ContainSubstring("run failed")))
		}

		entries, err := os.ReadDir(filepath.Join(layout.LogDir(), cfg.PackageName, cfg.PackageVersion))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(flags.DefaultLogRetention))
	})

	It("clears stale all-checked state before a failing check run", func() {
		req.stage = stage.ApplyCheck
		success := newRunnableMockStep("check.sh", execution.StatusSuccess)
		cfg.Modes[stage.ApplyCheck] = []step.Step{success}
		historyStore.EXPECT().
			Record(stage.ApplyCheck, mock.Anything).
			Return(nil).
			Once()

		status, err := runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		marker := filepath.Join(layout.FlagDir(), "apply-check_ALL_CHECKED")
		Expect(marker).To(BeAnExistingFile())

		failed := newRunnableMockStep("check.sh", execution.StatusFailed)
		cfg.Modes[stage.ApplyCheck] = []step.Step{failed}
		status, err = runSteps(
			context.Background(), req, runtime, layout, cfg, flagStore, historyStore,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(marker).NotTo(BeAnExistingFile())
	})
})
