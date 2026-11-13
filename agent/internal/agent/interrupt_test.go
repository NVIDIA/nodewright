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
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	commandmock "github.com/NVIDIA/nodewright/agent/internal/command/mock"
	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/interrupts"
	interruptsmock "github.com/NVIDIA/nodewright/agent/internal/interrupts/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

type resumableTestInterrupt struct {
	operationCount int
	runPending     func([]bool, func(int) error) (execution.Status, error)
}

func (r *resumableTestInterrupt) Type() interrupts.InterruptType {
	return interrupts.ServiceRestartType
}

func (r *resumableTestInterrupt) Run(
	context.Context,
	execution.Config,
) (execution.Status, error) {
	return execution.StatusFailed, errors.New("unexpected non-resumable interrupt execution")
}

func (r *resumableTestInterrupt) Serialize() ([]byte, error) { return nil, nil }

func (r *resumableTestInterrupt) OperationCount() int { return r.operationCount }

func (r *resumableTestInterrupt) RunPending(
	_ context.Context,
	_ execution.Config,
	completed []bool,
	markCompleted func(int) error,
) (execution.Status, error) {
	return r.runPending(completed, markCompleted)
}

func newMockInterrupt(
	interruptType interrupts.InterruptType,
	status execution.Status,
) *interruptsmock.MockInterrupt {
	value := interruptsmock.NewMockInterrupt(GinkgoT())
	value.EXPECT().Type().Return(interruptType).Maybe()
	value.EXPECT().
		Run(mock.Anything, mock.Anything).
		Return(status, nil).
		Once()
	return value
}

var _ = Describe("interrupt orchestration", func() {
	It("preserves underscores in package names parsed from resource IDs", func() {
		cfg, err := configFromResourceID("nodewright-uid-1_package_with_underscores_1.2.3")

		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(config.Config{
			PackageName:    "package_with_underscores",
			PackageVersion: "1.2.3",
		}))
	})

	It("marks a successful interrupt and skips it on the next invocation", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			writeLogs:  true,
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		value := newMockInterrupt(interrupts.NoOpType, execution.StatusSuccess)

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		status, err = runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "no_op.complete",
		)).To(BeAnExistingFile())
		logEntries, err := os.ReadDir(filepath.Join(
			layout.LogDir(), "package", "1.0.0", interruptsDirName,
		))
		Expect(err).NotTo(HaveOccurred())
		Expect(logEntries).To(HaveLen(1))
	})

	It("does not expose an ordinary interrupt as complete while it is running", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		marker := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.ServiceRestartType).Maybe()
		value.EXPECT().Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				Expect(marker).NotTo(BeAnExistingFile())
			}).
			Return(execution.StatusSuccess, nil).
			Once()

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(marker).To(BeAnExistingFile())
	})

	It("does not mark a failed ordinary interrupt complete", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		value := newMockInterrupt(interrupts.ServiceRestartType, execution.StatusFailed)

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)).NotTo(BeAnExistingFile())
	})

	It("recognizes legacy interrupt completion markers", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		marker := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)
		Expect(os.MkdirAll(filepath.Dir(marker), 0o755)).To(Succeed())
		Expect(os.WriteFile(marker, nil, 0o600)).To(Succeed())
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.ServiceRestartType).Maybe()

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("resumes multi-command interrupts from indexed legacy markers", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		markerDirectory := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName, runtime.resourceID,
		)
		Expect(os.MkdirAll(markerDirectory, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(markerDirectory, "service_restart_0.complete"), nil, 0o600)).To(Succeed())
		value := &resumableTestInterrupt{operationCount: 3}
		value.runPending = func(completed []bool, markCompleted func(int) error) (execution.Status, error) {
			Expect(completed).To(Equal([]bool{true, false, false}))
			Expect(markCompleted(1)).To(Succeed())
			return execution.StatusFailed, nil
		}

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(filepath.Join(markerDirectory, "service_restart_1.complete")).To(BeAnExistingFile())
		Expect(filepath.Join(markerDirectory, "service_restart_2.complete")).NotTo(BeAnExistingFile())

		value.runPending = func(completed []bool, markCompleted func(int) error) (execution.Status, error) {
			Expect(completed).To(Equal([]bool{true, true, false}))
			Expect(markCompleted(2)).To(Succeed())
			return execution.StatusSuccess, nil
		}
		status, err = runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(markerDirectory, "service_restart_2.complete")).To(BeAnExistingFile())
	})

	It("retains interrupt completion when log cleanup fails", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			writeLogs:  true,
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		logDirectory := filepath.Join(layout.LogDir(), "package", "1.0.0", interruptsDirName)
		savedLogDirectory := logDirectory + "-saved"
		outside := GinkgoT().TempDir()
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.ServiceRestartType).Maybe()
		value.EXPECT().Run(mock.Anything, mock.Anything).
			Run(func(context.Context, execution.Config) {
				Expect(os.Rename(logDirectory, savedLogDirectory)).To(Succeed())
				Expect(os.Symlink(outside, logDirectory)).To(Succeed())
			}).
			Return(execution.StatusSuccess, nil).
			Once()

		status, err := runInterrupt(context.Background(), req, runtime, layout, value)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("cleaning old logs for interrupt")))
		marker := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "service_restart_0.complete",
		)
		Expect(marker).To(BeAnExistingFile())
		status, err = runInterrupt(context.Background(), req, runtime, layout, value)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})

	It("retries a canceled node restart until the host boot ID changes", func() {
		root := GinkgoT().TempDir()
		bootIDPath := filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id")
		Expect(os.MkdirAll(filepath.Dir(bootIDPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(bootIDPath, []byte("boot-a\n"), 0o600)).To(Succeed())
		layout := flags.DefaultLayout(root)
		req := request{rootMount: root, copyDir: "/package"}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		markerBase := filepath.Join(
			layout.StateDir(), interruptsDirName, interruptFlagsDirName,
			runtime.resourceID, "node_restart_0",
		)
		pendingMarker := markerBase + ".pending"
		completeMarker := markerBase + ".complete"
		canceled := interruptsmock.NewMockInterrupt(GinkgoT())
		canceled.EXPECT().Type().Return(interrupts.NodeRestartType).Maybe()
		canceled.EXPECT().Run(mock.Anything, mock.Anything).
			Return(execution.StatusFailed, context.Canceled).
			Once()

		status, err := runInterrupt(context.Background(), req, runtime, layout, canceled)
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(pendingMarker).To(BeAnExistingFile())
		Expect(completeMarker).NotTo(BeAnExistingFile())

		retried := newMockInterrupt(interrupts.NodeRestartType, execution.StatusSuccess)
		status, err = runInterrupt(context.Background(), req, runtime, layout, retried)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(pendingMarker).To(BeAnExistingFile())
		Expect(completeMarker).NotTo(BeAnExistingFile())

		Expect(os.WriteFile(bootIDPath, []byte("boot-b\n"), 0o600)).To(Succeed())
		completed := interruptsmock.NewMockInterrupt(GinkgoT())
		completed.EXPECT().Type().Return(interrupts.NodeRestartType).Maybe()
		status, err = runInterrupt(context.Background(), req, runtime, layout, completed)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(pendingMarker).NotTo(BeAnExistingFile())
		Expect(completeMarker).To(BeAnExistingFile())
	})

	It("decodes the operator wire payload before running an interrupt", func() {
		root := GinkgoT().TempDir()
		layout := flags.DefaultLayout(root)
		encoded, err := interrupts.Encode(interrupts.NoOp{})
		Expect(err).NotTo(HaveOccurred())
		req := request{
			rootMount:     root,
			copyDir:       "/package",
			interruptData: encoded,
		}
		runtime := runtimeConfig{
			resourceID: "resource_package_1.0.0",
			stdout:     io.Discard,
			stderr:     io.Discard,
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		status, err := runDecodedInterrupt(context.Background(), req, runtime, layout)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
	})
})

type nodeRestartFixture struct {
	root       string
	layout     flags.Layout
	req        request
	runtime    runtimeConfig
	bootIDPath string
	pending    string
	complete   string
}

// newNodeRestartFixture builds a temp host root for a node_restart interrupt. An empty
// bootID leaves the host boot_id file absent.
func newNodeRestartFixture(bootID string) nodeRestartFixture {
	root := GinkgoT().TempDir()
	layout := flags.DefaultLayout(root)
	runtime := runtimeConfig{
		resourceID: "resource_package_1.0.0",
		stdout:     io.Discard,
		stderr:     io.Discard,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	bootIDPath := filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id")
	if bootID != "" {
		Expect(os.MkdirAll(filepath.Dir(bootIDPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(bootIDPath, []byte(bootID), 0o600)).To(Succeed())
	}
	markerBase := filepath.Join(
		layout.StateDir(), interruptsDirName, interruptFlagsDirName,
		runtime.resourceID, "node_restart_0",
	)
	return nodeRestartFixture{
		root:       root,
		layout:     layout,
		req:        request{rootMount: root, copyDir: "/package"},
		runtime:    runtime,
		bootIDPath: bootIDPath,
		pending:    markerBase + ".pending",
		complete:   markerBase + ".complete",
	}
}

func runnerFactoryFor(runner command.Runner) func(...command.RunnerOption) command.Runner {
	return func(...command.RunnerOption) command.Runner { return runner }
}

// rebootRunner answers every reboot with the given result and counts how often it ran.
func rebootRunner(result command.Result) (*commandmock.MockRunner, *[]string) {
	runner := commandmock.NewMockRunner(GinkgoT())
	ran := &[]string{}
	runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, cmd command.Command) (command.Result, error) {
			*ran = append(*ran, cmd.Executable)
			return result, nil
		},
	).Maybe()
	return runner, ran
}

var sigtermResult = command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGTERM}

var _ = Describe("node restart interrupt through runInterrupt", func() {
	It("honours a Python-style node_restart_0.complete without running or reading the boot ID", func() {
		f := newNodeRestartFixture("") // no boot_id: reading it would fail the run
		Expect(os.MkdirAll(filepath.Dir(f.complete), 0o755)).To(Succeed())
		// The Python agent wrote str(time.time()) to <type>_<index>.complete before
		// running the command and kept it for a reboot ended by SIGTERM; only the
		// file's existence matters to the Go agent.
		Expect(os.WriteFile(f.complete, []byte("1762900000.123456"), 0o600)).To(Succeed())
		// A runner with no expectations fails the spec if the reboot is attempted.
		f.runtime.runnerFactory = runnerFactoryFor(commandmock.NewMockRunner(GinkgoT()))

		status, err := runInterrupt(
			context.Background(), f.req, f.runtime, f.layout, interrupts.NodeRestart{},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(f.pending).NotTo(BeAnExistingFile())
	})

	It("treats SIGTERM as success, keeps the pending marker, and completes on the next boot", func() {
		f := newNodeRestartFixture("boot-a\n")
		runner, ran := rebootRunner(sigtermResult)
		f.runtime.runnerFactory = runnerFactoryFor(runner)

		status, err := runInterrupt(
			context.Background(), f.req, f.runtime, f.layout, interrupts.NodeRestart{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(*ran).To(Equal([]string{"reboot"}))
		Expect(os.ReadFile(f.pending)).To(Equal([]byte("boot-id:boot-a\n")))
		Expect(f.complete).NotTo(BeAnExistingFile())

		Expect(os.WriteFile(f.bootIDPath, []byte("boot-b\n"), 0o600)).To(Succeed())
		status, err = runInterrupt(
			context.Background(), f.req, f.runtime, f.layout, interrupts.NodeRestart{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(*ran).To(HaveLen(1), "the completed restart must not run reboot again")
		Expect(f.pending).NotTo(BeAnExistingFile())
		Expect(f.complete).To(BeAnExistingFile())
	})

	It("reruns the reboot when a pending marker's boot ID has not changed", func() {
		f := newNodeRestartFixture("boot-a\n")
		Expect(os.MkdirAll(filepath.Dir(f.pending), 0o755)).To(Succeed())
		Expect(os.WriteFile(f.pending, []byte("boot-id:boot-a\n"), 0o600)).To(Succeed())
		runner, ran := rebootRunner(sigtermResult)
		f.runtime.runnerFactory = runnerFactoryFor(runner)

		status, err := runInterrupt(
			context.Background(), f.req, f.runtime, f.layout, interrupts.NodeRestart{},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(*ran).To(Equal([]string{"reboot"}))
		Expect(f.complete).NotTo(BeAnExistingFile())
		Expect(os.ReadFile(f.pending)).To(Equal([]byte("boot-id:boot-a\n")))
	})

	It("does not treat SIGKILL as a started restart, and keeps the pending marker", func() {
		f := newNodeRestartFixture("boot-a\n")
		runner, _ := rebootRunner(command.Result{ExitCode: command.SignalExitCode, Signal: syscall.SIGKILL})
		f.runtime.runnerFactory = runnerFactoryFor(runner)

		status, err := runInterrupt(
			context.Background(), f.req, f.runtime, f.layout, interrupts.NodeRestart{},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(f.pending).To(BeAnExistingFile())
		Expect(f.complete).NotTo(BeAnExistingFile())
	})

	It("fails when reboot exits zero but the host does not restart", func() {
		f := newNodeRestartFixture("boot-a\n")
		// The real wait is 10s. Cancelling from inside the runner, once reboot has
		// "exited zero", ends the wait through the same select without a wall-clock race.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runner := commandmock.NewMockRunner(GinkgoT())
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(context.Context, command.Command) (command.Result, error) {
				cancel()
				return command.Result{ExitCode: command.SuccessExitCode}, nil
			},
		).Once()
		f.runtime.runnerFactory = runnerFactoryFor(runner)

		status, err := runInterrupt(ctx, f.req, f.runtime, f.layout, interrupts.NodeRestart{})

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("waiting for host restart")))
		Expect(f.pending).To(BeAnExistingFile())
	})
})

var _ = Describe("prepareNodeRestartMarker", func() {
	DescribeTable("reconciles the pending marker with the host boot ID",
		func(bootID, pendingContent string, wantCompleted bool, wantPending string) {
			f := newNodeRestartFixture(bootID)
			if pendingContent != "" {
				Expect(os.MkdirAll(filepath.Dir(f.pending), 0o755)).To(Succeed())
				Expect(os.WriteFile(f.pending, []byte(pendingContent), 0o600)).To(Succeed())
			}

			marker, completed, err := prepareNodeRestartMarker(f.req, f.runtime, f.complete)

			Expect(err).NotTo(HaveOccurred())
			Expect(marker).To(Equal(f.pending))
			Expect(completed).To(Equal(wantCompleted))
			if wantCompleted {
				Expect(f.pending).NotTo(BeAnExistingFile())
				Expect(f.complete).To(BeAnExistingFile())
				return
			}
			Expect(f.complete).NotTo(BeAnExistingFile())
			Expect(os.ReadFile(f.pending)).To(Equal([]byte(wantPending)))
		},
		Entry("no pending marker records the current boot ID",
			"boot-a\n", "", false, "boot-id:boot-a\n"),
		Entry("a pending marker from an earlier boot completes the restart",
			"boot-b\n", "boot-id:boot-a\n", true, ""),
		Entry("a pending marker from this boot is stale and is rewritten",
			"boot-a\n", "boot-id:boot-a\n", false, "boot-id:boot-a\n"),
		Entry("a marker without the boot-id prefix is discarded and rewritten",
			"boot-a\n", "garbage", false, "boot-id:boot-a\n"),
		Entry("a marker with an empty boot ID is discarded and rewritten",
			"boot-a\n", "boot-id:\n", false, "boot-id:boot-a\n"),
	)

	DescribeTable("fails without touching the marker when the host boot ID is unusable",
		func(bootID string, wantErr string) {
			f := newNodeRestartFixture(bootID)

			marker, completed, err := prepareNodeRestartMarker(f.req, f.runtime, f.complete)

			Expect(err).To(MatchError(ContainSubstring(wantErr)))
			Expect(marker).To(BeEmpty())
			Expect(completed).To(BeFalse())
			Expect(f.pending).NotTo(BeAnExistingFile())
		},
		Entry("a missing boot_id file", "", "reading host boot ID"),
		Entry("an empty boot_id file", " \n", "host boot ID is empty"),
	)
})

var _ = Describe("interrupt orchestration failures", func() {
	markerDir := func(f nodeRestartFixture) string { return filepath.Dir(f.pending) }

	It("reports an undecodable payload without writing any marker", func() {
		f := newNodeRestartFixture("boot-a\n")
		f.req.interruptData = "not base64!"

		status, err := runDecodedInterrupt(context.Background(), f.req, f.runtime, f.layout)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("decoding interrupt")))
		Expect(markerDir(f)).NotTo(BeADirectory())
	})

	DescribeTable("rejects a resource ID that is not <prefix>_<package>_<version>",
		func(resourceID, wantErr string) {
			_, err := configFromResourceID(resourceID)

			Expect(err).To(MatchError(ContainSubstring(wantErr)))
		},
		Entry("no underscore", "nounderscore", "must end in _PACKAGE_VERSION"),
		Entry("empty version", "resource_package_", "must end in _PACKAGE_VERSION"),
		Entry("empty package", "resource__1.0.0", "must end in _PACKAGE_VERSION"),
		Entry("empty prefix", "_package_1.0.0", "must end in _PACKAGE_VERSION"),
		Entry("package that is not one path component", "resource_a/b_1.0.0", "package name"),
		Entry("version that escapes its directory", "resource_package_..", "package version"),
	)

	It("fails before running anything when the log identity cannot be resolved", func() {
		f := newNodeRestartFixture("boot-a\n")
		f.runtime.writeLogs = true
		f.runtime.resourceID = "bad"
		// No Run expectation: the mock fails the spec if the interrupt is executed.
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.NoOpType).Maybe()

		status, err := runInterrupt(context.Background(), f.req, f.runtime, f.layout, value)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("resolving interrupt log identity")))
	})

	It("reports a completion marker that is a directory, not done and not pending", func() {
		f := newNodeRestartFixture("boot-a\n")
		Expect(os.MkdirAll(f.complete, 0o755)).To(Succeed())
		// No Run expectation: reading the marker as absent would run, and so reboot, again.
		value := interruptsmock.NewMockInterrupt(GinkgoT())
		value.EXPECT().Type().Return(interrupts.NodeRestartType).Maybe()

		status, err := runInterrupt(context.Background(), f.req, f.runtime, f.layout, value)

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("checking interrupt completion marker")))
		Expect(err).To(MatchError(ContainSubstring("not a regular file")))
		Expect(f.pending).NotTo(BeAnExistingFile())
	})

	It("rejects a pending marker that is a directory", func() {
		f := newNodeRestartFixture("boot-a\n")
		Expect(os.MkdirAll(f.pending, 0o755)).To(Succeed())

		marker, completed, err := prepareNodeRestartMarker(f.req, f.runtime, f.complete)

		Expect(err).To(MatchError(ContainSubstring("checking pending node restart")))
		Expect(marker).To(BeEmpty())
		Expect(completed).To(BeFalse())
	})

	It("keeps the pending marker when promoting it to complete fails", func() {
		f := newNodeRestartFixture("boot-b\n")
		Expect(os.MkdirAll(filepath.Dir(f.pending), 0o755)).To(Succeed())
		Expect(os.WriteFile(f.pending, []byte("boot-id:boot-a\n"), 0o600)).To(Succeed())
		// A file cannot be renamed onto a directory, for any user on any platform.
		Expect(os.MkdirAll(f.complete, 0o755)).To(Succeed())

		marker, completed, err := prepareNodeRestartMarker(f.req, f.runtime, f.complete)

		Expect(err).To(MatchError(ContainSubstring("completing node restart marker")))
		Expect(marker).To(BeEmpty())
		Expect(completed).To(BeFalse())
		Expect(f.pending).To(BeAnExistingFile())
	})

	It("reports a host root that is not absolute when reading the boot ID", func() {
		f := newNodeRestartFixture("boot-a\n")
		f.req.rootMount = "relative-root"

		marker, completed, err := prepareNodeRestartMarker(f.req, f.runtime, f.complete)

		Expect(err).To(MatchError(ContainSubstring("reading host boot ID")))
		Expect(marker).To(BeEmpty())
		Expect(completed).To(BeFalse())
	})

	It("refuses to mark an operation index the interrupt does not have", func() {
		f := newNodeRestartFixture("boot-a\n")
		value := &resumableTestInterrupt{operationCount: 2}
		value.runPending = func(_ []bool, markCompleted func(int) error) (execution.Status, error) {
			Expect(markCompleted(-1)).To(MatchError(ContainSubstring("out of range")))
			Expect(markCompleted(2)).To(MatchError(ContainSubstring("out of range")))
			return execution.StatusFailed, nil
		}

		status, err := runInterrupt(context.Background(), f.req, f.runtime, f.layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(markerDir(f)).NotTo(BeADirectory())
	})

	It("runs a script interrupt without writing any completion marker", func() {
		f := newNodeRestartFixture("boot-a\n")
		value := newMockInterrupt(interrupts.ScriptInterruptType, execution.StatusSuccess)

		status, err := runInterrupt(context.Background(), f.req, f.runtime, f.layout, value)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(markerDir(f)).NotTo(BeADirectory())
	})
})
