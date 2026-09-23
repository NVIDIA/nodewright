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
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/history"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

const (
	startFlagName        = "START"
	checkResultsFlagName = "check_results"
)

func runSteps(
	ctx context.Context,
	req request,
	runtime runtimeConfig,
	layout flags.Layout,
	cfg config.Config,
	flagStore flags.Store,
	historyStore history.Store,
) (execution.Status, error) {
	if err := flagStore.Write(filepath.Join(layout.FlagDir(), startFlagName), nil); err != nil {
		return execution.StatusFailed, fmt.Errorf("writing agent start flag: %w", err)
	}

	configuredSteps := cfg.Modes[req.stage]
	if len(configuredSteps) == 0 {
		runtime.logger.Warn("stage has no configured steps; treating it as a no-op", "stage", req.stage)
	}

	var versions history.Versions
	if isUpgradeStage(req.stage) {
		var err error
		versions, err = historyStore.Read()
		if err != nil {
			return execution.StatusFailed, fmt.Errorf(
				"reading package history for stage %q: %w",
				req.stage,
				err,
			)
		}
	}

	_, isCheck := stage.CheckToApply[req.stage]
	if isCheck && len(configuredSteps) > 0 {
		completedCheckFlag := filepath.Join(
			layout.FlagDir(),
			string(req.stage)+"_ALL_CHECKED",
		)
		if err := hostfs.RemoveFile(req.rootMount, completedCheckFlag); err != nil {
			return execution.StatusFailed, fmt.Errorf(
				"clearing completed-check flag for stage %q: %w",
				req.stage,
				err,
			)
		}
	}
	results := make([]checkResult, 0, len(configuredSteps))
	for _, configuredStep := range configuredSteps {
		if err := ctx.Err(); err != nil {
			return execution.StatusFailed, fmt.Errorf("running stage %q: %w", req.stage, err)
		}

		runnableStep := configuredStep
		if isUpgradeStage(req.stage) {
			runnableStep = configuredStep.WithVersions(versions.Previous, versions.Current)
		}

		if !isCheck {
			decision, err := flagStore.Check(configuredStep, runtime.alwaysRunStep, req.stage)
			if err != nil {
				return execution.StatusFailed, fmt.Errorf(
					"checking completion for step %q: %w",
					configuredStep.Path(),
					err,
				)
			}
			if decision.Reason == flags.ReasonAlwaysRun {
				runtime.logger.Warn(
					"running completed step because always-run policy is enabled",
					"stage", req.stage,
					"step", configuredStep.Path(),
				)
			}
			if !decision.Run {
				runtime.logger.Info(
					"skipping completed step",
					"stage", req.stage,
					"step", configuredStep.Path(),
					"reason", decision.Reason,
				)
				continue
			}
		}
		if err := writeStepHeader(runtime.stdout, req.stage, runnableStep); err != nil {
			return execution.StatusFailed, fmt.Errorf(
				"writing execution header for step %q: %w",
				configuredStep.Path(),
				err,
			)
		}

		var markComplete func() error
		if !isCheck {
			markComplete = func() error {
				message := fmt.Sprintf(
					"last_run: %s\nstep_always_runs: %t",
					time.Now().UTC().Format(time.RFC3339Nano),
					configuredStep.Idempotence() == step.Disabled,
				)
				if _, err := flagStore.Mark(configuredStep, message); err != nil {
					return fmt.Errorf("marking step %q complete: %w", configuredStep.Path(), err)
				}
				return nil
			}
		}
		status, err := runStep(ctx, req, runtime, layout, cfg, runnableStep, markComplete)
		if err != nil {
			return execution.StatusFailed, err
		}
		if isCheck {
			results = append(results, checkResult{
				path:   configuredStep.Path(),
				failed: status != execution.StatusSuccess,
			})
			continue
		}
		if status != execution.StatusSuccess {
			return execution.StatusFailed, nil
		}
	}

	if isCheck && len(configuredSteps) > 0 {
		status, err := summarizeChecks(req.stage, results, flagStore, layout)
		if err != nil || status == execution.StatusFailed {
			return status, err
		}
	}
	if recordsHistory(req.stage) {
		if err := historyStore.Record(req.stage, time.Now()); err != nil {
			return execution.StatusFailed, fmt.Errorf(
				"recording package history for stage %q: %w",
				req.stage,
				err,
			)
		}
	}
	if req.stage == stage.UninstallCheck {
		if err := removeStepFlags(cfg, flagStore); err != nil {
			return execution.StatusFailed, err
		}
	}
	return execution.StatusSuccess, nil
}

func writeStepHeader(output io.Writer, currentStage stage.Stage, value step.Step) error {
	metadata := value.ExecutionMetadata()
	_, err := fmt.Fprintf(
		output,
		"%s %s %s %s %s %s\n",
		currentStage,
		value.Path(),
		step.FormatLegacyArguments(metadata.Arguments),
		step.FormatLegacyReturnCodes(metadata.ReturnCodes),
		formatLegacyIdempotence(value.Idempotence()),
		formatLegacyBool(metadata.OnHost),
	)
	return err
}

func formatLegacyIdempotence(value step.Idempotence) string {
	switch value {
	case step.Auto:
		return "Idempotence.Auto"
	case step.Disabled:
		return "Idempotence.Disabled"
	default:
		return "Idempotence." + string(value)
	}
}

func runStep(
	ctx context.Context,
	req request,
	runtime runtimeConfig,
	layout flags.Layout,
	cfg config.Config,
	value step.Step,
	markComplete func() error,
) (execution.Status, error) {
	operation := fmt.Sprintf("step %q", value.Path())
	operationLog, err := prepareOperationLog(runtime, layout, cfg, value.Path(), operation)
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
			fmt.Errorf("configuring step %q execution: %w", value.Path(), err),
			operationLog.finalize(),
		)
	}
	status, runErr := value.Run(ctx, runConfig)
	var markErr error
	if runErr == nil && status == execution.StatusSuccess && markComplete != nil {
		markErr = markComplete()
	}
	finalizeErr := operationLog.finalize()
	if runErr != nil || markErr != nil || finalizeErr != nil {
		if runErr != nil {
			runErr = fmt.Errorf("running step %q: %w", value.Path(), runErr)
		}
		return execution.StatusFailed, errors.Join(
			runErr,
			markErr,
			finalizeErr,
		)
	}
	return status, nil
}

type checkResult struct {
	path   string
	failed bool
}

func summarizeChecks(
	currentStage stage.Stage,
	results []checkResult,
	flagStore flags.Store,
	layout flags.Layout,
) (execution.Status, error) {
	lines := make([]string, 0, len(results))
	failed := false
	for _, result := range results {
		value := "False"
		if result.failed {
			value = "True"
			failed = true
		}
		lines = append(lines, result.path+" "+value)
	}
	if err := flagStore.Write(
		filepath.Join(layout.FlagDir(), checkResultsFlagName),
		[]byte(strings.Join(lines, "\n")),
	); err != nil {
		return execution.StatusFailed, fmt.Errorf("writing check results for stage %q: %w", currentStage, err)
	}
	if failed {
		return execution.StatusFailed, nil
	}
	if err := flagStore.Write(
		filepath.Join(layout.FlagDir(), string(currentStage)+"_ALL_CHECKED"),
		nil,
	); err != nil {
		return execution.StatusFailed, fmt.Errorf(
			"writing completed-check flag for stage %q: %w",
			currentStage,
			err,
		)
	}
	return execution.StatusSuccess, nil
}

func isUpgradeStage(currentStage stage.Stage) bool {
	return currentStage == stage.Upgrade || currentStage == stage.UpgradeCheck
}

func recordsHistory(currentStage stage.Stage) bool {
	switch currentStage {
	case stage.ApplyCheck, stage.UpgradeCheck, stage.UninstallCheck:
		return true
	default:
		return false
	}
}

func removeStepFlags(cfg config.Config, flagStore flags.Store) error {
	for _, currentStage := range stage.All {
		for _, value := range cfg.Modes[currentStage] {
			if err := flagStore.Remove(value); err != nil {
				return fmt.Errorf("removing completion flag for step %q: %w", value.Path(), err)
			}
		}
	}
	return nil
}
