// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/artifact/preflight"
)

const (
	// preflightMode is not a stage: it runs no package, so it takes neither a
	// copy directory nor config.json.
	preflightMode   = "preflight"
	preflightRefEnv = "NODEWRIGHT_PREFLIGHT_REF"
	// defaultPreflightResultFile is corev1.TerminationMessagePathDefault: the
	// operator reads the result from the container's termination message.
	defaultPreflightResultFile = "/dev/termination-log"
	preflightResultFileMode    = 0o644
	// preflightTimeout bounds a whole preflight. It lets one credential
	// provider plugin take the whole minute the kubelet gives it and leaves
	// 30 seconds to inspect the reference.
	preflightTimeout = 90 * time.Second
	// registryRequestTimeout bounds one registry request of a preflight, its
	// retries included.
	registryRequestTimeout = 10 * time.Second
)

// runPreflight inspects the reference preflightRefEnv names, with the
// registry credentials the node's kubelet would use, and writes what it found
// to the result file as a preflight.Result. A reference that is not an
// installable artifact, or that cannot be inspected, is a result too, so it
// fails only when it cannot write one.
func (agent orchestrator) runPreflight(
	ctx context.Context,
	arguments []string,
	stderr io.Writer,
	logger *slog.Logger,
) ExitCode {
	if len(arguments) != 1 || !filepath.IsAbs(arguments[0]) {
		_, _ = fmt.Fprintf(stderr, "%s expects one absolute ROOT_MOUNT argument; received %q\n", preflightMode, arguments)
		_, _ = fmt.Fprintln(stderr, usage)
		return ExitUsage
	}

	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	result := agent.preflightResult(ctx, os.Getenv(preflightRefEnv), arguments[0], logger)
	logger.Info(
		"preflight result",
		"reference", result.Reference,
		"kind", result.Kind,
		"pinned", result.Pinned,
		"uninstall", result.Uninstall,
		"reason", result.Reason,
		"errorClass", result.ErrorClass,
		"credential", result.Credential,
		"detail", result.Detail,
	)

	data, err := preflight.Encode(result)
	if err != nil {
		logger.Error("encoding the preflight result", "error", err)
		return ExitFailure
	}
	path := cmp.Or(agent.preflightResultFile, defaultPreflightResultFile)
	if err := os.WriteFile(path, data, preflightResultFileMode); err != nil {
		logger.Error("writing the preflight result", "path", path, "error", err)
		return ExitFailure
	}
	return ExitSuccess
}

// preflightResult returns the result of inspecting reference, which it echoes as given.
func (agent orchestrator) preflightResult(
	ctx context.Context,
	reference string,
	rootMount string,
	logger *slog.Logger,
) preflight.Result {
	ref, err := client.ParseReference(reference)
	if err != nil {
		return preflight.NewResult(reference, nil, fmt.Errorf("parsing %s: %w", preflightRefEnv, err))
	}
	creds, err := agent.registryCredentials(ctx, ref, rootMount, registryAuthFiles(), logger)
	if err != nil {
		return preflight.NewResult(reference, nil, err)
	}
	httpClient := agent.registryHTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: registryRequestTimeout, Transport: retry.NewTransport(nil)}
	}
	insp, err := inspectPackageArtifact(ctx, ref, creds, httpClient)
	return preflight.NewResult(reference, insp, err)
}
