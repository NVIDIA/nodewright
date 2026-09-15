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
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/interrupts"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	agentversion "github.com/NVIDIA/nodewright/agent/internal/version"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Suite")
}

var _ = Describe("request parsing", func() {
	DescribeTable(
		"supports current and legacy operator forms",
		func(arguments []string, expected request) {
			actual, err := parseRequest(arguments)
			Expect(err).NotTo(HaveOccurred())
			Expect(actual).To(Equal(expected))
		},
		Entry(
			"legacy stage",
			[]string{"apply", "/packages/value"},
			request{stage: stage.Apply, rootMount: "/root", copyDir: "/packages/value"},
		),
		Entry(
			"legacy interrupt",
			[]string{"interrupt", "/packages/value", "payload"},
			request{
				stage: stage.Interrupt, rootMount: "/root",
				copyDir: "/packages/value", interruptData: "payload",
			},
		),
		Entry(
			"current stage",
			[]string{"apply", "/host", "/packages/value"},
			request{stage: stage.Apply, rootMount: "/host", copyDir: "/packages/value"},
		),
		Entry(
			"current interrupt",
			[]string{"interrupt", "/host", "/packages/value", "payload"},
			request{
				stage: stage.Interrupt, rootMount: "/host",
				copyDir: "/packages/value", interruptData: "payload",
			},
		),
	)

	It("identifies stages unknown to this agent version", func() {
		_, err := parseRequest([]string{"future-stage", "/host", "/package"})

		Expect(err).To(MatchError(`unsupported stage "future-stage"`))
		var unsupported *unsupportedStageError
		Expect(errors.As(err, &unsupported)).To(BeTrue())
		Expect(unsupported.stage).To(Equal("future-stage"))
	})

	DescribeTable(
		"rejects malformed requests",
		func(arguments []string, expected string) {
			_, err := parseRequest(arguments)
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("argument count", []string{"apply"}, "expected 2, 3, or 4 arguments"),
		Entry("relative root", []string{"apply", "host", "/package"}, "root mount"),
		Entry("relative copy directory", []string{"apply", "/host", "package"}, "copy directory"),
		Entry(
			"empty interrupt data",
			[]string{"interrupt", "/host", "/package", ""},
			"interrupt data must not be empty",
		),
		Entry(
			"interrupt data on a normal stage",
			[]string{"apply", "/host", "/package", "payload"},
			`stage "apply" does not accept interrupt data`,
		),
	)
})

var _ = Describe("runtime environment", func() {
	It("uses documented defaults when environment values are absent", func() {
		for _, name := range []string{
			copyResolverEnv,
			alwaysRunEnv,
			resourceIDEnv,
			dataDirEnv,
			stateRootEnv,
			logRootEnv,
			legacyBufferLimitEnv,
			writeLogsEnv,
		} {
			unsetEnvironment(name)
		}

		runtime := runtimeFromEnvironment(nil, nil, nil)

		Expect(runtime.dataDir).To(Equal(defaultDataDir))
		Expect(runtime.stateRoot).To(Equal(flags.DefaultStateRoot))
		Expect(runtime.logRoot).To(Equal(flags.DefaultLogRoot))
		Expect(runtime.resourceID).To(BeEmpty())
		Expect(runtime.alwaysRunStep).To(BeFalse())
		Expect(runtime.writeLogs).To(BeTrue())
		Expect(runtime.copyResolver).To(BeTrue())
		Expect(runtime.stdout).To(Equal(io.Writer(os.Stdout)))
		Expect(runtime.stderr).To(Equal(io.Writer(os.Stderr)))
		Expect(runtime.logger).NotTo(BeNil())
	})

	It("loads configured values and output composition", func() {
		GinkgoT().Setenv(copyResolverEnv, "false")
		GinkgoT().Setenv(alwaysRunEnv, "TRUE")
		GinkgoT().Setenv(resourceIDEnv, "resource")
		GinkgoT().Setenv(dataDirEnv, "/data")
		GinkgoT().Setenv(stateRootEnv, "/state")
		GinkgoT().Setenv(logRootEnv, "/logs")
		GinkgoT().Setenv(writeLogsEnv, "false")
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))

		runtime := runtimeFromEnvironment(stdout, stderr, logger)

		Expect(runtime.dataDir).To(Equal("/data"))
		Expect(runtime.stateRoot).To(Equal("/state"))
		Expect(runtime.logRoot).To(Equal("/logs"))
		Expect(runtime.resourceID).To(Equal("resource"))
		Expect(runtime.alwaysRunStep).To(BeTrue())
		Expect(runtime.writeLogs).To(BeFalse())
		Expect(runtime.copyResolver).To(BeFalse())
		Expect(runtime.stdout).To(BeIdenticalTo(stdout))
		Expect(runtime.stderr).To(BeIdenticalTo(stderr))
		Expect(runtime.logger).To(BeIdenticalTo(logger))
	})

	It("treats an invalid configured boolean as false and warns", func() {
		GinkgoT().Setenv(copyResolverEnv, " true ")
		logOutput := &bytes.Buffer{}
		logger := slog.New(slog.NewTextHandler(logOutput, nil))

		runtime := runtimeFromEnvironment(io.Discard, io.Discard, logger)

		Expect(runtime.copyResolver).To(BeFalse())
		Expect(logOutput.String()).To(ContainSubstring(
			"invalid boolean environment variable; treating it as false",
		))
		Expect(logOutput.String()).To(ContainSubstring(copyResolverEnv))
	})

	It("normalizes empty state and log roots", func() {
		runtime := normalizeRuntime(runtimeConfig{})

		Expect(runtime.stateRoot).To(Equal(flags.DefaultStateRoot))
		Expect(runtime.logRoot).To(Equal(flags.DefaultLogRoot))
	})
})

var _ = Describe("Agent.Run", func() {
	BeforeEach(func() {
		GinkgoT().Setenv(copyResolverEnv, "false")
		GinkgoT().Setenv(alwaysRunEnv, "false")
		GinkgoT().Setenv(resourceIDEnv, "resource_package_1.0.0")
		GinkgoT().Setenv(stateRootEnv, "/state")
		GinkgoT().Setenv(logRootEnv, "/logs")
		GinkgoT().Setenv(writeLogsEnv, "false")
		GinkgoT().Setenv(legacyBufferLimitEnv, "4096")
	})

	It("defines process exit codes", func() {
		Expect(ExitSuccess).To(Equal(ExitCode(0)))
		Expect(ExitFailure).To(Equal(ExitCode(1)))
		Expect(ExitUsage).To(Equal(ExitCode(2)))
	})

	It("prints the embedded build version", func() {
		version, gitSHA := agentversion.Version, agentversion.GitSHA
		agentversion.Version, agentversion.GitSHA = "6.5.0", "abc1234"
		DeferCleanup(func() {
			agentversion.Version, agentversion.GitSHA = version, gitSHA
		})
		stdout := &bytes.Buffer{}

		exitCode := New().Run(context.Background(), []string{versionArgument}, stdout, io.Discard)

		Expect(exitCode).To(Equal(ExitSuccess))
		Expect(stdout.String()).To(Equal("6.5.0\n"))
	})

	It("rejects additional version arguments", func() {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}

		exitCode := New().Run(
			context.Background(),
			[]string{versionArgument, "extra"},
			stdout,
			stderr,
		)

		Expect(exitCode).To(Equal(ExitUsage))
		Expect(stdout.String()).To(BeEmpty())
		Expect(stderr.String()).To(ContainSubstring("--version does not accept additional arguments"))
		Expect(stderr.String()).To(ContainSubstring(usage))
	})

	It("runs a valid operator invocation", func() {
		root := GinkgoT().TempDir()
		dataDir := GinkgoT().TempDir()
		writePackageFixture(dataDir, "[]", true)
		GinkgoT().Setenv(dataDirEnv, dataDir)
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}

		exitCode := New().Run(
			context.Background(),
			[]string{"config", root, "/package"},
			stdout,
			stderr,
		)

		Expect(exitCode).To(Equal(ExitSuccess))
		Expect(filepath.Join(root, "package", configFileName)).To(BeAnExistingFile())
		Expect(stderr.String()).NotTo(ContainSubstring("starting agent"))
		Expect(stdout.String()).To(Equal(fmt.Sprintf(
			"--------------------\n"+
				"--CLI CONFIGURATION-\n"+
				"mode: config\n"+
				"root_mount: %s\n"+
				"copy_dir: /package\n"+
				"interrupt_data: None\n"+
				"always_run_step: False\n"+
				"--ENV CONFIGURATION-\n"+
				"COPY_RESOLV: False\n"+
				"OVERLAY_ALWAYS_RUN_STEP: False\n"+
				"SKYHOOK_RESOURCE_ID: resource_package_1.0.0\n"+
				"SKYHOOK_DATA_DIR: %s\n"+
				"SKYHOOK_ROOT_DIR: /state\n"+
				"SKYHOOK_LOG_DIR: /logs\n"+
				"SKYHOOK_AGENT_BUFFER_LIMIT: 4096\n"+
				"SKYHOOK_AGENT_WRITE_LOGS: False\n"+
				"Directory CONFIGURATION\n"+
				"flag_dir: /state/flags/package/1.0.0\n"+
				"log_dir: /logs/package/1.0.0\n"+
				"history_file: /state/history/package.json\n"+
				"--------------------\n",
			root,
			dataDir,
		)))
	})

	It("treats an unsupported stage as a successful no-op", func() {
		stderr := &bytes.Buffer{}

		exitCode := New().Run(
			context.Background(),
			[]string{"future-stage", "/host", "/package"},
			io.Discard,
			stderr,
		)

		Expect(exitCode).To(Equal(ExitSuccess))
		Expect(stderr.String()).To(ContainSubstring("does not support the requested stage"))
	})

	It("returns a usage exit code for an invalid invocation", func() {
		stderr := &bytes.Buffer{}

		exitCode := New().Run(context.Background(), []string{"apply"}, io.Discard, stderr)

		Expect(exitCode).To(Equal(ExitUsage))
		Expect(stderr.String()).To(ContainSubstring("expected 2, 3, or 4 arguments"))
		Expect(stderr.String()).To(ContainSubstring(usage))
	})

	It("returns a failure exit code when execution fails", func() {
		root := GinkgoT().TempDir()
		GinkgoT().Setenv(dataDirEnv, filepath.Join(root, "missing"))
		stderr := &bytes.Buffer{}

		exitCode := New().Run(
			context.Background(),
			[]string{"apply", root, "/package"},
			io.Discard,
			stderr,
		)

		Expect(exitCode).To(Equal(ExitFailure))
		Expect(stderr.String()).To(ContainSubstring("agent execution failed"))
	})

	It("returns a failure exit code when execution is canceled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stderr := &bytes.Buffer{}

		exitCode := New().Run(
			ctx,
			[]string{"apply", GinkgoT().TempDir(), "/package"},
			io.Discard,
			stderr,
		)

		Expect(exitCode).To(Equal(ExitFailure))
		Expect(stderr.String()).To(ContainSubstring("agent stopped after receiving a termination signal"))
	})

	It("cancels an active step and does not start the next step", func() {
		root := GinkgoT().TempDir()
		dataDir := GinkgoT().TempDir()
		writeCancellationPackageFixture(dataDir)
		GinkgoT().Setenv(dataDirEnv, dataDir)
		GinkgoT().Setenv("NODEWRIGHT_AGENT_TEST_MARKER_DIR", filepath.Join(root, "package"))
		stderr := &bytes.Buffer{}
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		exitCodes := make(chan ExitCode, 1)
		var earlyExit error

		go func() {
			exitCodes <- New().Run(
				ctx,
				[]string{"apply", root, "/package"},
				io.Discard,
				stderr,
			)
		}()

		firstStarted := filepath.Join(root, "package", "first-started")
		Eventually(func() error {
			if _, err := os.Stat(firstStarted); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if earlyExit == nil {
				select {
				case exitCode := <-exitCodes:
					earlyExit = fmt.Errorf(
						"agent exited with code %d before the first step started: %s",
						exitCode,
						stderr,
					)
				default:
				}
			}
			if earlyExit != nil {
				return earlyExit
			}
			return errors.New("first step has not started")
		}).WithTimeout(5 * time.Second).Should(Succeed())
		cancel()

		var exitCode ExitCode
		Eventually(exitCodes).WithTimeout(5 * time.Second).Should(Receive(&exitCode))
		Expect(exitCode).To(Equal(ExitFailure))
		Expect(filepath.Join(root, "package", "first-finished")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(root, "package", "second-started")).NotTo(BeAnExistingFile())
		Expect(stderr.String()).To(ContainSubstring("agent stopped after receiving a termination signal"))
	})
})

var _ = Describe("request orchestration", func() {
	It("copies legacy package data before running a stage", func() {
		root := GinkgoT().TempDir()
		dataDir := GinkgoT().TempDir()
		writePackageFixture(dataDir, "[]", true)
		_, resolverErr := os.Stat("/etc/resolv.conf")
		copyResolver := resolverErr == nil
		if resolverErr != nil && !errors.Is(resolverErr, os.ErrNotExist) {
			Expect(resolverErr).NotTo(HaveOccurred())
		}
		runtime := runtimeConfig{
			dataDir:      dataDir,
			stateRoot:    "/state",
			logRoot:      "/logs",
			copyResolver: copyResolver,
			writeLogs:    false,
			stdout:       io.Discard,
			stderr:       io.Discard,
			logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		status, err := orchestrator{}.runRequest(context.Background(), request{
			stage:     stage.Config,
			rootMount: root,
			copyDir:   "/package",
		}, runtime)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(root, "package", configFileName)).To(BeAnExistingFile())
		if copyResolver {
			Expect(filepath.Join(root, "etc", "resolv.conf")).To(BeAnExistingFile())
		}
		Expect(filepath.Join(root, "state", "flags", startFlagName)).To(BeAnExistingFile())
	})

	It("does not apply host preparation requirements during uninstall", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		writePackageFixture(copyRoot, `["missing"]`, true)
		Expect(os.Mkdir(filepath.Join(copyRoot, rootOverlayDirName), 0o755)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(copyRoot, rootOverlayDirName, "should-not-copy"),
			[]byte("value"),
			0o600,
		)).To(Succeed())
		runtime := runtimeConfig{
			stateRoot: "/state",
			logRoot:   "/logs",
			writeLogs: false,
			stdout:    io.Discard,
			stderr:    io.Discard,
			logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		status, err := orchestrator{}.runRequest(context.Background(), request{
			stage:     stage.Uninstall,
			rootMount: root,
			copyDir:   "/package",
		}, runtime)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(root, "should-not-copy")).NotTo(BeAnExistingFile())
	})

	It("runs an encoded interrupt through the Agent interface", func() {
		root := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(root, "package"), 0o755)).To(Succeed())
		encoded, err := interrupts.Encode(interrupts.NoOp{})
		Expect(err).NotTo(HaveOccurred())

		status, err := orchestrator{}.runRequest(
			context.Background(),
			request{
				stage:         stage.Interrupt,
				rootMount:     root,
				copyDir:       "/package",
				interruptData: encoded,
			},
			runtimeConfig{
				stateRoot:  "/state",
				logRoot:    "/logs",
				resourceID: "resource_package_1.0.0",
				stdout:     io.Discard,
				stderr:     io.Discard,
				logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
			},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(filepath.Join(
			root,
			"state",
			interruptsDirName,
			interruptFlagsDirName,
			"resource_package_1.0.0",
			"no_op.complete",
		)).To(BeAnExistingFile())
	})
})

func writePackageFixture(directory, expectedConfigFiles string, onHost bool) {
	Expect(os.MkdirAll(filepath.Join(directory, "skyhook_dir"), 0o755)).To(Succeed())
	for _, name := range []string{"apply", "second", "apply-check"} {
		Expect(os.WriteFile(filepath.Join(directory, "skyhook_dir", name), nil, 0o700)).To(Succeed())
	}
	data := fmt.Sprintf(`{
		"schema_version": "v1",
		"root_dir": "/",
		"expected_config_files": %s,
		"package_name": "package",
		"package_version": "1.0.0",
		"modes": {
			"apply": [
				{
					"name": "apply",
					"path": "apply",
					"arguments": [],
					"returncodes": [0],
					"on_host": %t,
					"idempotence": false,
					"upgrade_step": false
				},
				{
					"name": "second",
					"path": "second",
					"arguments": [],
					"returncodes": [0],
					"on_host": %t,
					"idempotence": false,
					"upgrade_step": false
				}
			],
			"apply-check": [
				{
					"name": "apply-check",
					"path": "apply-check",
					"arguments": [],
					"returncodes": [0],
					"on_host": %t,
					"idempotence": false,
					"upgrade_step": false
				}
			]
		}
	}`, expectedConfigFiles, onHost, onHost, onHost)
	Expect(os.WriteFile(filepath.Join(directory, configFileName), []byte(data), 0o600)).To(Succeed())
}

func writeCancellationPackageFixture(directory string) {
	writePackageFixture(directory, "[]", false)
	stepsDir := filepath.Join(directory, "skyhook_dir")
	Expect(os.WriteFile(
		filepath.Join(stepsDir, "apply"),
		[]byte("#!/bin/sh\n: > \"$NODEWRIGHT_AGENT_TEST_MARKER_DIR/first-started\"\nsleep 3600\n: > \"$NODEWRIGHT_AGENT_TEST_MARKER_DIR/first-finished\"\n"),
		0o700,
	)).To(Succeed())
	Expect(os.WriteFile(
		filepath.Join(stepsDir, "second"),
		[]byte("#!/bin/sh\n: > \"$NODEWRIGHT_AGENT_TEST_MARKER_DIR/second-started\"\n"),
		0o700,
	)).To(Succeed())
}

func unsetEnvironment(name string) {
	value, exists := os.LookupEnv(name)
	Expect(os.Unsetenv(name)).To(Succeed())
	DeferCleanup(func() {
		if exists {
			Expect(os.Setenv(name, value)).To(Succeed())
			return
		}
		Expect(os.Unsetenv(name)).To(Succeed())
	})
}
