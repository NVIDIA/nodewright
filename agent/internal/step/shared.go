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

package step

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
)

type stepVersions struct {
	previous string
	current  string
}

type stepOptions struct {
	name              string
	arguments         []string
	returncodes       []command.ExitCode
	onHost            bool
	idempotence       Idempotence
	env               map[string]string
	requiresInterrupt bool
}

// Option configures a command-backed step during construction.
type Option func(*stepOptions)

const (
	currentVersionEnv  = "CURRENT_VERSION"
	previousVersionEnv = "PREVIOUS_VERSION"
	stepRootEnv        = "STEP_ROOT"
	skyhookDirEnv      = "SKYHOOK_DIR"
)

func newStepOptions(path string, opts ...Option) stepOptions {
	// Seed OnHost here because the later defaulting pass cannot distinguish an
	// omitted bool from an explicit WithOnHost(false).
	value := stepOptions{onHost: true}
	for _, opt := range opts {
		opt(&value)
	}
	applyStepDefaults(
		&value.name,
		path,
		&value.arguments,
		&value.returncodes,
		&value.env,
		&value.idempotence,
	)
	return value
}

func newExecutionMetadata(
	arguments []string,
	returnCodes []command.ExitCode,
	onHost bool,
) ExecutionMetadata {
	return ExecutionMetadata{
		Arguments:   slices.Clone(arguments),
		ReturnCodes: slices.Clone(returnCodes),
		OnHost:      onHost,
	}
}

// FormatLegacyArguments formats arguments for legacy-agent-compatible state and output.
func FormatLegacyArguments(values []string) string {
	encoded := make([]string, len(values))
	for index, value := range values {
		encoded[index] = formatLegacyString(value)
	}
	return "[" + strings.Join(encoded, ", ") + "]"
}

// FormatLegacyReturnCodes formats return codes for legacy-agent-compatible state and output.
func FormatLegacyReturnCodes(values []command.ExitCode) string {
	encoded := make([]string, len(values))
	for index, value := range values {
		encoded[index] = strconv.Itoa(int(value))
	}
	return "[" + strings.Join(encoded, ", ") + "]"
}

func formatLegacyString(value string) string {
	quote := '\''
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var encoded strings.Builder
	encoded.WriteRune(quote)
	for _, value := range value {
		switch value {
		case '\\':
			encoded.WriteString(`\\`)
		case quote:
			encoded.WriteRune('\\')
			encoded.WriteRune(value)
		case '\t':
			encoded.WriteString(`\t`)
		case '\n':
			encoded.WriteString(`\n`)
		case '\r':
			encoded.WriteString(`\r`)
		default:
			switch {
			case unicode.IsPrint(value):
				encoded.WriteRune(value)
			case value <= 0xff:
				_, _ = fmt.Fprintf(&encoded, `\x%02x`, value)
			case value <= 0xffff:
				_, _ = fmt.Fprintf(&encoded, `\u%04x`, value)
			default:
				_, _ = fmt.Fprintf(&encoded, `\U%08x`, value)
			}
		}
	}
	encoded.WriteRune(quote)
	return encoded.String()
}

// WithName overrides the default name, which otherwise falls back to the script path.
func WithName(name string) Option {
	return func(value *stepOptions) { value.name = name }
}

// WithArguments sets the step's arguments.
func WithArguments(arguments []string) Option {
	return func(value *stepOptions) { value.arguments = arguments }
}

// WithReturncodes sets the accepted return codes.
func WithReturncodes(returncodes []command.ExitCode) Option {
	return func(value *stepOptions) { value.returncodes = returncodes }
}

// WithEnv sets the environment map applied to the step process.
func WithEnv(environment map[string]string) Option {
	return func(value *stepOptions) { value.env = environment }
}

// WithOnHost controls whether the command runs inside the host root.
func WithOnHost(onHost bool) Option {
	return func(value *stepOptions) { value.onHost = onHost }
}

// WithIdempotence sets the step's idempotence mode.
func WithIdempotence(idempotence Idempotence) Option {
	return func(value *stepOptions) { value.idempotence = idempotence }
}

// WithRequiresInterrupt sets whether the step requires an interrupt.
func WithRequiresInterrupt(requiresInterrupt bool) Option {
	return func(value *stepOptions) { value.requiresInterrupt = requiresInterrupt }
}

func runStep(
	ctx context.Context,
	config execution.Config,
	path string,
	arguments []string,
	returncodes []command.ExitCode,
	onHost bool,
	environment map[string]string,
	versions *stepVersions,
) (execution.Status, error) {
	if ctx == nil {
		return execution.StatusFailed, errors.New("running step: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return execution.StatusFailed, fmt.Errorf("running step %q: %w", path, err)
	}
	if err := config.Validate(); err != nil {
		return execution.StatusFailed, fmt.Errorf("running step %q: invalid run config: %w", path, err)
	}
	if !filepath.IsLocal(path) {
		return execution.StatusFailed, fmt.Errorf(
			"running step: path %q must be relative to the step root",
			path,
		)
	}

	resolvedArguments, missing := resolveArguments(arguments)
	if len(missing) > 0 {
		return execution.StatusFailed, fmt.Errorf(
			"running step %q: expected environment variables do not exist: %s",
			path,
			strings.Join(missing, ", "),
		)
	}

	runner := command.NewRunner(command.WithChroot(config.RootMount()))
	stepRoot := config.StepRoot()
	skyhookDir := config.SkyhookDir()
	if !onHost {
		runner = command.NewRunner()
		mountedStepRoot, err := hostfs.HostPathToMounted(config.RootMount(), stepRoot)
		if err != nil {
			return execution.StatusFailed, fmt.Errorf(
				"running step %q: resolving mounted step root: %w",
				path,
				err,
			)
		}
		stepRoot = mountedStepRoot
		mountedSkyhookDir, err := hostfs.HostPathToMounted(config.RootMount(), skyhookDir)
		if err != nil {
			return execution.StatusFailed, fmt.Errorf(
				"running step %q: resolving mounted package directory: %w",
				path,
				err,
			)
		}
		skyhookDir = mountedSkyhookDir
	}

	if environment == nil {
		environment = map[string]string{}
	} else {
		environment = maps.Clone(environment)
	}
	if versions != nil {
		environment[previousVersionEnv] = versions.previous
		environment[currentVersionEnv] = versions.current
	}
	environment[stepRootEnv] = stepRoot
	environment[skyhookDirEnv] = skyhookDir
	cmd := command.NewCommand(
		filepath.Join(stepRoot, path),
		command.WithArguments(resolvedArguments...),
		command.WithWorkingDirectory(skyhookDir),
		command.WithEnvironment(environment),
		command.WithStdout(config.Stdout()),
		command.WithStderr(config.Stderr()),
		command.WithRequiredPermissions(0o111),
	)

	result, err := runner.Run(ctx, cmd)
	if err != nil {
		return execution.StatusFailed, fmt.Errorf("running step %q command: %w", path, err)
	}
	if result.Signal != nil || !slices.Contains(returncodes, result.ExitCode) {
		return execution.StatusFailed, nil
	}

	return execution.StatusSuccess, nil
}

func stepFingerprint(
	path string,
	arguments []string,
	returncodes []command.ExitCode,
	environment map[string]string,
	onHost bool,
) (string, error) {
	if arguments == nil {
		arguments = []string{}
	}
	if returncodes == nil {
		returncodes = []command.ExitCode{}
	}
	if environment == nil {
		environment = map[string]string{}
	}
	payload, err := json.Marshal(struct {
		Path        string             `json:"path"`
		Arguments   []string           `json:"arguments"`
		ReturnCodes []command.ExitCode `json:"returnCodes"`
		Env         map[string]string  `json:"env"`
		OnHost      bool               `json:"onHost"`
	}{
		Path:        path,
		Arguments:   arguments,
		ReturnCodes: returncodes,
		Env:         environment,
		OnHost:      onHost,
	})
	if err != nil {
		return "", fmt.Errorf("encoding step fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func encodeStep(value any, idempotence Idempotence) ([]byte, error) {
	if err := idempotence.Validate(); err != nil {
		return nil, fmt.Errorf("validate idempotence: %w", err)
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal step: %w", err)
	}
	return out, nil
}

// OnHost is intentionally excluded: constructors and Decode preserve whether
// false was explicitly selected before applying the remaining defaults.
func applyStepDefaults(
	name *string,
	path string,
	arguments *[]string,
	returncodes *[]command.ExitCode,
	environment *map[string]string,
	idempotence *Idempotence,
) {
	if *name == "" {
		*name = path
	}
	if len(*arguments) == 0 {
		*arguments = []string{}
	}
	if len(*returncodes) == 0 {
		*returncodes = []command.ExitCode{command.SuccessExitCode}
	}
	if *environment == nil {
		*environment = map[string]string{}
	}
	if *idempotence == "" {
		*idempotence = Auto
	}
}

func resolveArguments(configured []string) ([]string, []string) {
	arguments := slices.Clone(configured)
	missing := make([]string, 0)
	seenMissing := make(map[string]struct{})
	for index, argument := range arguments {
		name, found := strings.CutPrefix(argument, "env:")
		if !found {
			continue
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			if _, seen := seenMissing[name]; !seen {
				missing = append(missing, name)
				seenMissing[name] = struct{}{}
			}
			continue
		}
		arguments[index] = value
	}
	return arguments, missing
}
