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
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/interrupts"
)

const (
	interruptsDirName     = "interrupts"
	interruptFlagsDirName = "flags"
	markerFileMode        = 0o600
	hostBootIDPath        = "/proc/sys/kernel/random/boot_id"
	restartMarkerPrefix   = "boot-id:"
)

func runDecodedInterrupt(
	ctx context.Context,
	req request,
	runtime runtimeConfig,
	layout flags.Layout,
) (execution.Status, error) {
	value, err := interrupts.Decode(req.interruptData)
	if err != nil {
		return execution.StatusFailed, fmt.Errorf("decoding interrupt: %w", err)
	}
	return runInterrupt(ctx, req, runtime, layout, value)
}

func runInterrupt(
	ctx context.Context,
	req request,
	runtime runtimeConfig,
	layout flags.Layout,
	value interrupts.Interrupt,
) (status execution.Status, retErr error) {
	interruptType := value.Type()
	completeMarkers := interruptCompletionMarkers(layout, runtime.resourceID, value)
	completedOperations, err := completedInterruptOperations(req.rootMount, completeMarkers)
	if err != nil {
		return execution.StatusFailed, err
	}
	if len(completedOperations) > 0 && allOperationsComplete(completedOperations) {
		runtime.logger.Info(
			"skipping completed interrupt",
			"interrupt", interruptType,
			"resourceID", runtime.resourceID,
		)
		return execution.StatusSuccess, nil
	}

	pendingMarker := ""
	retainPendingMarker := false
	if interruptType == interrupts.NodeRestartType {
		var completed bool
		pendingMarker, completed, err = prepareNodeRestartMarker(req, runtime, completeMarkers[0])
		if err != nil {
			return execution.StatusFailed, err
		}
		if completed {
			runtime.logger.Info(
				"skipping completed interrupt after host reboot",
				"interrupt", interruptType,
				"resourceID", runtime.resourceID,
			)
			return execution.StatusSuccess, nil
		}
	}

	defer func() {
		if pendingMarker == "" || retainPendingMarker {
			return
		}
		if err := hostfs.RemoveFile(req.rootMount, pendingMarker); err != nil {
			status = execution.StatusFailed
			retErr = errors.Join(
				retErr,
				fmt.Errorf("removing failed node restart marker: %w", err),
			)
		}
	}()

	logConfig := config.Config{}
	if runtime.writeLogs {
		logConfig, err = configFromResourceID(runtime.resourceID)
		if err != nil {
			return execution.StatusFailed, fmt.Errorf("resolving interrupt log identity: %w", err)
		}
	}
	logName := filepath.Join(interruptsDirName, string(interruptType))
	operationLog, err := prepareOperationLog(
		runtime,
		layout,
		logConfig,
		logName,
		fmt.Sprintf("interrupt %q", interruptType),
	)
	if err != nil {
		return execution.StatusFailed, err
	}

	runConfig, err := execution.NewConfig(
		execution.WithRootMount(req.rootMount),
		execution.WithStepRoot(flags.StepsDir(req.copyDir)),
		execution.WithSkyhookDir(req.copyDir),
		execution.WithRunOutput(operationLog.stdout, operationLog.stderr),
	)
	if err != nil {
		return execution.StatusFailed, errors.Join(
			fmt.Errorf("configuring interrupt %q execution: %w", interruptType, err),
			operationLog.finalize(),
		)
	}
	if interruptType == interrupts.NodeRestartType {
		// A successful reboot can terminate the agent by several signal paths.
		// Keep the pending marker once execution begins and decide completion by
		// comparing host boot IDs on the next invocation.
		retainPendingMarker = true
	}
	status, runErr := executeInterrupt(
		ctx,
		req.rootMount,
		value,
		runConfig,
		completedOperations,
		completeMarkers,
	)
	finalizeErr := operationLog.finalize()
	if runErr != nil || finalizeErr != nil {
		if runErr != nil {
			runErr = fmt.Errorf("running interrupt %q: %w", interruptType, runErr)
		}
		return execution.StatusFailed, errors.Join(
			runErr,
			finalizeErr,
		)
	}
	if status != execution.StatusSuccess {
		return execution.StatusFailed, nil
	}

	return execution.StatusSuccess, nil
}

func executeInterrupt(
	ctx context.Context,
	rootMount string,
	value interrupts.Interrupt,
	runConfig execution.Config,
	completedOperations []bool,
	completeMarkers []string,
) (execution.Status, error) {
	if resumable, ok := value.(interrupts.ResumableInterrupt); ok {
		return resumable.RunPending(
			ctx,
			runConfig,
			completedOperations,
			func(index int) error {
				if index < 0 || index >= len(completeMarkers) {
					return fmt.Errorf("interrupt operation index %d is out of range", index)
				}
				return markInterruptComplete(rootMount, completeMarkers[index])
			},
		)
	}

	status, err := value.Run(ctx, runConfig)
	if err != nil || status != execution.StatusSuccess ||
		value.Type() == interrupts.NodeRestartType || len(completeMarkers) == 0 {
		return status, err
	}
	for index, completed := range completedOperations {
		if completed {
			continue
		}
		if err := markInterruptComplete(rootMount, completeMarkers[index]); err != nil {
			return status, fmt.Errorf("marking interrupt %q complete: %w", value.Type(), err)
		}
	}
	return status, nil
}

func interruptCompletionMarkers(
	layout flags.Layout,
	resourceID string,
	value interrupts.Interrupt,
) []string {
	interruptType := value.Type()
	baseDirectory := filepath.Join(
		layout.StateDir(),
		interruptsDirName,
		interruptFlagsDirName,
		resourceID,
	)
	if interruptType == interrupts.ScriptInterruptType {
		return nil
	}
	if interruptType == interrupts.NoOpType {
		return []string{filepath.Join(baseDirectory, string(interruptType)+".complete")}
	}
	operationCount := 1
	if resumable, ok := value.(interrupts.ResumableInterrupt); ok {
		operationCount = resumable.OperationCount()
	}
	markers := make([]string, operationCount)
	for index := range operationCount {
		markers[index] = filepath.Join(
			baseDirectory,
			fmt.Sprintf("%s_%d.complete", interruptType, index),
		)
	}
	return markers
}

func completedInterruptOperations(rootMount string, markers []string) ([]bool, error) {
	completed := make([]bool, len(markers))
	for index, marker := range markers {
		exists, err := hostfs.RegularFileExists(rootMount, marker)
		if err != nil {
			return nil, fmt.Errorf("checking interrupt completion marker %q: %w", marker, err)
		}
		completed[index] = exists
	}
	return completed, nil
}

func allOperationsComplete(completed []bool) bool {
	for _, value := range completed {
		if !value {
			return false
		}
	}
	return true
}

func markInterruptComplete(rootMount, marker string) error {
	return hostfs.CreateFile(
		rootMount,
		marker,
		[]byte(time.Now().UTC().Format(time.RFC3339Nano)),
		markerFileMode,
	)
}

func prepareNodeRestartMarker(
	req request,
	runtime runtimeConfig,
	completeMarker string,
) (string, bool, error) {
	bootID, err := hostBootID(req.rootMount)
	if err != nil {
		return "", false, fmt.Errorf("reading host boot ID: %w", err)
	}
	pendingMarker := strings.TrimSuffix(completeMarker, ".complete") + ".pending"
	pending, err := hostfs.RegularFileExists(req.rootMount, pendingMarker)
	if err != nil {
		return "", false, fmt.Errorf("checking pending node restart: %w", err)
	}
	if pending {
		data, err := hostfs.ReadFile(req.rootMount, pendingMarker)
		if err != nil {
			return "", false, fmt.Errorf("reading pending node restart: %w", err)
		}
		previousBootID, valid := strings.CutPrefix(strings.TrimSpace(string(data)), restartMarkerPrefix)
		if valid && previousBootID != "" && previousBootID != bootID {
			if err := hostfs.RenameFile(req.rootMount, pendingMarker, completeMarker); err != nil {
				return "", false, fmt.Errorf("completing node restart marker: %w", err)
			}
			return pendingMarker, true, nil
		}
		if !valid || previousBootID == "" {
			runtime.logger.Warn(
				"discarding malformed pending node restart marker",
				"resourceID", runtime.resourceID,
			)
		}
		if err := hostfs.RemoveFile(req.rootMount, pendingMarker); err != nil {
			return "", false, fmt.Errorf("removing stale node restart marker: %w", err)
		}
	}
	if err := hostfs.CreateFile(
		req.rootMount,
		pendingMarker,
		[]byte(restartMarkerPrefix+bootID+"\n"),
		markerFileMode,
	); err != nil {
		return "", false, fmt.Errorf("marking node restart pending: %w", err)
	}
	return pendingMarker, false, nil
}

func hostBootID(rootMount string) (string, error) {
	path, err := hostfs.HostPathToMounted(rootMount, hostBootIDPath)
	if err != nil {
		return "", err
	}
	data, err := hostfs.ReadFile(rootMount, path)
	if err != nil {
		return "", err
	}
	bootID := strings.TrimSpace(string(data))
	if bootID == "" {
		return "", errors.New("host boot ID is empty")
	}
	return bootID, nil
}

func configFromResourceID(resourceID string) (config.Config, error) {
	versionSeparator := strings.LastIndex(resourceID, "_")
	if versionSeparator <= 0 || versionSeparator == len(resourceID)-1 {
		return config.Config{}, fmt.Errorf("resource ID %q must end in _PACKAGE_VERSION", resourceID)
	}
	packageSeparator := strings.Index(resourceID[:versionSeparator], "_")
	if packageSeparator <= 0 || packageSeparator == versionSeparator-1 {
		return config.Config{}, fmt.Errorf("resource ID %q must end in _PACKAGE_VERSION", resourceID)
	}
	packageName := resourceID[packageSeparator+1 : versionSeparator]
	packageVersion := resourceID[versionSeparator+1:]
	if err := hostfs.ValidatePathComponent("package name", packageName); err != nil {
		return config.Config{}, err
	}
	if err := hostfs.ValidatePathComponent("package version", packageVersion); err != nil {
		return config.Config{}, err
	}
	return config.Config{PackageName: packageName, PackageVersion: packageVersion}, nil
}
