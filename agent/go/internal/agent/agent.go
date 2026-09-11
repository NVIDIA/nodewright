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

// Package agent coordinates package preparation, lifecycle steps, interrupts,
// completion flags, history, and logs for one agent invocation.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/history"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	agentversion "github.com/NVIDIA/nodewright/agent/internal/version"
)

const (
	usage            = "usage: agent MODE [ROOT_MOUNT] COPY_DIR [INTERRUPT_DATA]\n       agent --version"
	defaultRootMount = "/root"
	defaultDataDir   = "/skyhook-package"
	versionArgument  = "--version"

	copyResolverEnv          = "COPY_RESOLV"
	alwaysRunEnv             = "OVERLAY_ALWAYS_RUN_STEP"
	resourceIDEnv            = "SKYHOOK_RESOURCE_ID"
	dataDirEnv               = "SKYHOOK_DATA_DIR"
	stateRootEnv             = "SKYHOOK_ROOT_DIR"
	logRootEnv               = "SKYHOOK_LOG_DIR"
	legacyBufferLimitEnv     = "SKYHOOK_AGENT_BUFFER_LIMIT"
	writeLogsEnv             = "SKYHOOK_AGENT_WRITE_LOGS"
	defaultLegacyBufferLimit = "8192"
)

// ExitCode is the process result returned by Agent.Run.
type ExitCode int

const (
	ExitSuccess ExitCode = iota
	ExitFailure
	ExitUsage
)

type request struct {
	stage         stage.Stage
	rootMount     string
	copyDir       string
	interruptData string
}

type runtimeConfig struct {
	dataDir       string
	stateRoot     string
	logRoot       string
	resourceID    string
	alwaysRunStep bool
	writeLogs     bool
	copyResolver  bool
	stdout        io.Writer
	stderr        io.Writer
	logger        *slog.Logger
}

type unsupportedStageError struct {
	stage string
}

func (err *unsupportedStageError) Error() string {
	return fmt.Sprintf("unsupported stage %q", err.stage)
}

// Agent executes one operator invocation.
type Agent interface {
	Run(context.Context, []string, io.Writer, io.Writer) ExitCode
}

type orchestrator struct{}

var _ Agent = orchestrator{}

// New constructs the agent orchestrator.
func New() Agent {
	return orchestrator{}
}

// Run executes one operator invocation and returns its process exit code.
func (agent orchestrator) Run(
	ctx context.Context,
	arguments []string,
	stdout, stderr io.Writer,
) ExitCode {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	if len(arguments) > 0 && arguments[0] == versionArgument {
		if len(arguments) != 1 {
			_, _ = fmt.Fprintln(stderr, "--version does not accept additional arguments")
			_, _ = fmt.Fprintln(stderr, usage)
			return ExitUsage
		}
		if _, err := fmt.Fprintln(stdout, agentversion.GetVersion()); err != nil {
			logger.Error("writing agent version", "error", err)
			return ExitFailure
		}
		return ExitSuccess
	}

	req, err := parseRequest(arguments)
	if err != nil {
		var unsupportedStage *unsupportedStageError
		if errors.As(err, &unsupportedStage) {
			logger.Warn(
				"this agent version does not support the requested stage; treating it as a no-op",
				"stage", unsupportedStage.stage,
			)
			return ExitSuccess
		}
		_, _ = fmt.Fprintln(stderr, err)
		_, _ = fmt.Fprintln(stderr, usage)
		return ExitUsage
	}

	runtime := runtimeFromEnvironment(stdout, stderr, logger)
	if err := printStartupBanner(stdout, req, runtime); err != nil {
		logger.Error("preparing agent startup banner", "error", err)
		return ExitFailure
	}

	status, err := agent.runRequest(ctx, req, runtime)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Info("agent stopped after receiving a termination signal", "error", err)
		} else {
			logger.Error("agent execution failed", "error", err)
		}
		return ExitFailure
	}

	if status == execution.StatusFailed {
		return ExitFailure
	}

	return ExitSuccess
}

func parseRequest(arguments []string) (request, error) {
	var (
		mode          string
		rootMount     string
		copyDir       string
		interruptData string
	)
	switch len(arguments) {
	case 2:
		mode = arguments[0]
		rootMount = defaultRootMount
		copyDir = arguments[1]
	case 3:
		mode = arguments[0]
		if mode == string(stage.Interrupt) {
			rootMount = defaultRootMount
			copyDir = arguments[1]
			interruptData = arguments[2]
		} else {
			rootMount = arguments[1]
			copyDir = arguments[2]
		}
	case 4:
		mode = arguments[0]
		rootMount = arguments[1]
		copyDir = arguments[2]
		interruptData = arguments[3]
	default:
		return request{}, fmt.Errorf(
			"expected 2, 3, or 4 arguments; received %d",
			len(arguments),
		)
	}

	currentStage, err := stage.ParseStage(mode)
	if err != nil {
		return request{}, &unsupportedStageError{stage: mode}
	}
	parsed := request{
		stage:         currentStage,
		rootMount:     rootMount,
		copyDir:       copyDir,
		interruptData: interruptData,
	}
	if err := validateRequest(parsed); err != nil {
		return request{}, err
	}
	return parsed, nil
}

func runtimeFromEnvironment(stdout, stderr io.Writer, logger *slog.Logger) runtimeConfig {
	runtime := normalizeRuntime(runtimeConfig{
		dataDir:    envOrDefault(dataDirEnv, defaultDataDir),
		stateRoot:  envOrDefault(stateRootEnv, flags.DefaultStateRoot),
		logRoot:    envOrDefault(logRootEnv, flags.DefaultLogRoot),
		resourceID: os.Getenv(resourceIDEnv),
		stdout:     stdout,
		stderr:     stderr,
		logger:     logger,
	})
	runtime.alwaysRunStep = envBool(alwaysRunEnv, false, runtime.logger)
	runtime.writeLogs = envBool(writeLogsEnv, true, runtime.logger)
	runtime.copyResolver = envBool(copyResolverEnv, true, runtime.logger)
	return runtime
}

// The dashed startup banner is consumed by operator diagnostics and existing
// end-to-end tests, so its field order and legacy value formatting are a
// compatibility contract.
func printStartupBanner(output io.Writer, req request, runtime runtimeConfig) error {
	cfg, err := configFromResourceID(runtime.resourceID)
	if err != nil {
		return fmt.Errorf("resolving package identity: %w", err)
	}
	interruptData := req.interruptData
	if interruptData == "" {
		interruptData = "None"
	}
	lines := []string{
		"--------------------",
		"--CLI CONFIGURATION-",
		"mode: " + string(req.stage),
		"root_mount: " + req.rootMount,
		"copy_dir: " + req.copyDir,
		"interrupt_data: " + interruptData,
		"always_run_step: " + formatLegacyBool(runtime.alwaysRunStep),
		"--ENV CONFIGURATION-",
		"COPY_RESOLV: " + formatLegacyBool(runtime.copyResolver),
		"OVERLAY_ALWAYS_RUN_STEP: " + formatLegacyBool(runtime.alwaysRunStep),
		"SKYHOOK_RESOURCE_ID: " + runtime.resourceID,
		"SKYHOOK_DATA_DIR: " + runtime.dataDir,
		"SKYHOOK_ROOT_DIR: " + runtime.stateRoot,
		"SKYHOOK_LOG_DIR: " + runtime.logRoot,
		"SKYHOOK_AGENT_BUFFER_LIMIT: " + envOrDefault(legacyBufferLimitEnv, defaultLegacyBufferLimit),
		"SKYHOOK_AGENT_WRITE_LOGS: " + formatLegacyBool(runtime.writeLogs),
		"Directory CONFIGURATION",
		"flag_dir: " + filepath.Join(runtime.stateRoot, "flags", cfg.PackageName, cfg.PackageVersion),
		"log_dir: " + filepath.Join(runtime.logRoot, cfg.PackageName, cfg.PackageVersion),
		"history_file: " + filepath.Join(runtime.stateRoot, "history", cfg.PackageName+".json"),
		"--------------------",
	}
	if _, err := fmt.Fprintln(output, strings.Join(lines, "\n")); err != nil {
		return fmt.Errorf("writing startup banner: %w", err)
	}
	return nil
}

func formatLegacyBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

func envOrDefault(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool, logger *slog.Logger) bool {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	if strings.EqualFold(value, "true") {
		return true
	}
	if !strings.EqualFold(value, "false") {
		logger.Warn(
			"invalid boolean environment variable; treating it as false",
			"name", name,
			"value", value,
		)
	}
	return false
}

func normalizeRuntime(runtime runtimeConfig) runtimeConfig {
	if runtime.dataDir == "" {
		runtime.dataDir = defaultDataDir
	}
	if runtime.stateRoot == "" {
		runtime.stateRoot = flags.DefaultStateRoot
	}
	if runtime.logRoot == "" {
		runtime.logRoot = flags.DefaultLogRoot
	}
	if runtime.stdout == nil {
		runtime.stdout = os.Stdout
	}
	if runtime.stderr == nil {
		runtime.stderr = os.Stderr
	}
	if runtime.logger == nil {
		runtime.logger = slog.New(slog.NewTextHandler(runtime.stderr, nil))
	}
	return runtime
}

func validateRun(ctx context.Context, req request, runtime runtimeConfig) error {
	if ctx == nil {
		return errors.New("running agent: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("running agent: %w", err)
	}
	if err := validateRequest(req); err != nil {
		return fmt.Errorf("running agent: %w", err)
	}
	if !filepath.IsAbs(runtime.dataDir) {
		return fmt.Errorf("running agent: data directory %q is not absolute", runtime.dataDir)
	}
	if req.stage == stage.Interrupt {
		if err := hostfs.ValidatePathComponent("resource ID", runtime.resourceID); err != nil {
			return fmt.Errorf("running agent: %w", err)
		}
	}
	return nil
}

func validateRequest(req request) error {
	if _, err := stage.ParseStage(string(req.stage)); err != nil {
		return err
	}
	if !filepath.IsAbs(req.rootMount) {
		return fmt.Errorf("root mount %q is not absolute", req.rootMount)
	}
	if !filepath.IsAbs(req.copyDir) {
		return fmt.Errorf("copy directory %q is not absolute", req.copyDir)
	}
	if req.stage == stage.Interrupt {
		if req.interruptData == "" {
			return errors.New("interrupt data must not be empty")
		}
	} else if req.interruptData != "" {
		return fmt.Errorf("stage %q does not accept interrupt data", req.stage)
	}
	return nil
}

func (orchestrator) runRequest(
	ctx context.Context,
	req request,
	runtime runtimeConfig,
) (execution.Status, error) {
	runtime = normalizeRuntime(runtime)
	if err := validateRun(ctx, req, runtime); err != nil {
		return execution.StatusFailed, err
	}

	if runtime.copyResolver {
		if err := copyResolverConfig(req.rootMount); err != nil {
			return execution.StatusFailed, fmt.Errorf("copying resolver configuration: %w", err)
		}
	}

	copyRoot, err := hostfs.HostPathToMounted(req.rootMount, req.copyDir)
	if err != nil {
		return execution.StatusFailed, fmt.Errorf("resolving package copy directory: %w", err)
	}
	if err := ensurePackageData(
		req.rootMount,
		copyRoot,
		packageDataSources{
			dataDir:            runtime.dataDir,
			legacyNodeFilesDir: legacyNodeFilesDir,
		},
	); err != nil {
		return execution.StatusFailed, fmt.Errorf("preparing package data: %w", err)
	}

	layout, err := flags.NewLayout(req.rootMount, runtime.stateRoot, runtime.logRoot)
	if err != nil {
		return execution.StatusFailed, fmt.Errorf("preparing agent filesystem layout: %w", err)
	}
	if req.stage == stage.Interrupt {
		return runDecodedInterrupt(ctx, req, runtime, layout)
	}

	cfg, err := loadConfig(copyRoot, runtime.logger)
	if err != nil {
		return execution.StatusFailed, err
	}

	if req.stage != stage.Uninstall && req.stage != stage.UninstallCheck {
		if err := prepareHost(req.rootMount, copyRoot, *cfg); err != nil {
			return execution.StatusFailed, err
		}
	}

	flagStore, err := flags.NewStore(layout, *cfg)
	if err != nil {
		return execution.StatusFailed, fmt.Errorf("constructing flag store: %w", err)
	}

	historyStore, err := history.NewStore(layout, *cfg, runtime.logger)
	if err != nil {
		return execution.StatusFailed, fmt.Errorf("constructing history store: %w", err)
	}

	return runSteps(ctx, req, runtime, layout, *cfg, flagStore, historyStore)
}
