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
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/artifact/preflight"

	"github.com/NVIDIA/nodewright/agent/internal/credentialprovider"
	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
)

// nodeDockerConfigPaths are the host Docker configs a registry credential is
// looked up in after the auth files, in order.
var nodeDockerConfigPaths = []string{
	"/var/lib/kubelet/config.json",
	"/root/.docker/config.json",
	"/.docker/config.json",
}

// installPackageArtifact lays out the package artifact that runtime.packageRef
// pins at copyRoot, keeping what copyRoot already holds beyond the payload as
// the merging cp of an image pod does, then overlays the files of the package
// data directory on it. A check stage or an interrupt reuses an existing
// copyRoot instead.
func (agent orchestrator) installPackageArtifact(
	ctx context.Context,
	req request,
	runtime runtimeConfig,
	copyRoot string,
) error {
	ref, err := client.ParsePinned(runtime.packageRef)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", packageRefEnv, err)
	}

	if _, isCheck := stage.CheckToApply[req.stage]; isCheck || req.stage == stage.Interrupt {
		info, err := os.Stat(copyRoot)
		switch {
		case err == nil && !info.IsDir():
			return fmt.Errorf("package copy path %q is not a directory", copyRoot)
		case err == nil:
			runtime.logger.Info(
				"reusing the laid-out package without reading the registry",
				"stage", req.stage,
				"path", copyRoot,
			)
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("stating package copy path %q: %w", copyRoot, err)
		}
	}

	creds, err := agent.registryCredentials(ctx, ref, req.rootMount, runtime.registryAuthFiles, runtime.logger)
	if err != nil {
		return err
	}
	insp, err := inspectPackageArtifact(ctx, ref, creds, agent.registryHTTPClient)
	if err != nil {
		return err
	}
	if err := refuseUnsupportedPackage(ref, req.stage, insp); err != nil {
		return err
	}
	runtime.logger.Info(
		"laying out package artifact",
		"reference", ref.String(),
		"digest", insp.Descriptor.Digest.String(),
	)

	root, err := os.OpenRoot(req.rootMount)
	if err != nil {
		return fmt.Errorf("opening root mount %q: %w", req.rootMount, err)
	}
	defer func() { _ = root.Close() }()
	dest := strings.TrimPrefix(filepath.Clean(req.copyDir), string(filepath.Separator))
	if err := client.Layout(ctx, insp, root, dest); err != nil {
		return err
	}

	if err := overlayPackageData(req.rootMount, runtime.dataDir, copyRoot); err != nil {
		return fmt.Errorf("overlaying package data from %q: %w", runtime.dataDir, err)
	}
	return nil
}

// registryCredentials returns the credentials for ref that the sources
// credentialSources lists hold, in the order the registry is to be tried with
// them, and logs every source it skipped and the credentials it found. It
// logs source labels and library reasons only, never a credential.
func (agent orchestrator) registryCredentials(
	ctx context.Context,
	ref client.Reference,
	rootMount string,
	authFiles []string,
	logger *slog.Logger,
) ([]client.Credential, error) {
	sources, skippedPlugins, err := agent.credentialSources(ctx, ref, rootMount, authFiles)
	if err != nil {
		return nil, fmt.Errorf("resolving registry credential sources: %w", err)
	}

	creds, skipped := client.LookupCredential(ref, sources)
	for _, source := range append(skipped, skippedPlugins...) {
		logger.Info(
			"registry credential source skipped",
			"source", source.Source,
			"reason", source.Reason,
		)
	}
	if len(creds) == 0 {
		logger.Info(
			"no registry credential found; reading the package artifact anonymously",
			"reference", ref.String(),
		)
	} else {
		labels := make([]string, 0, len(creds))
		for _, cred := range creds {
			labels = append(labels, cred.Source())
		}
		logger.Info(
			"registry credentials to try in order; the next is tried only when the registry refuses one",
			"reference", ref.String(),
			"sources", labels,
		)
	}
	return creds, nil
}

// credentialSources returns the sources to look a registry credential for ref
// up in, in the order the kubelet takes them for an image pull: authFiles,
// the pod's pull secrets, in order; then the node's Docker configs beneath
// rootMount; then the response of each kubelet credential provider plugin
// that matches ref. It also returns the plugins it skipped, with why.
//
// The kubelet merges a pod's pull secrets into one keyring ordered by the
// specificity of their keys, and its plugins' responses into another, while
// LookupCredential takes these sources one by one. Of two pull secrets, or
// two plugins, that both hold a credential for ref, the first listed is tried
// first here, where the kubelet first tries the one whose key matches ref
// more specifically.
func (agent orchestrator) credentialSources(
	ctx context.Context,
	ref client.Reference,
	rootMount string,
	authFiles []string,
) ([]client.CredentialSource, []client.SkippedSource, error) {
	sources := make([]client.CredentialSource, 0, len(authFiles)+len(nodeDockerConfigPaths))
	for _, path := range authFiles {
		sources = append(sources, client.DockerConfigFile(path))
	}
	for _, hostPath := range nodeDockerConfigPaths {
		path, err := hostfs.HostPathToMounted(rootMount, hostPath)
		if err != nil {
			return nil, nil, err
		}
		sources = append(sources, client.DockerConfigFile(path))
	}
	plugins, skipped := credentialprovider.Sources(ctx, ref, credentialprovider.Host{
		RootMount: rootMount,
		ProcRoot:  agent.procRoot,
		Runner:    agent.pluginRunner,
	})
	return append(sources, plugins...), skipped, nil
}

// inspectPackageArtifact inspects ref at its registry with creds, through
// httpClient, or the artifact client's default when it is nil.
func inspectPackageArtifact(
	ctx context.Context,
	ref client.Reference,
	creds []client.Credential,
	httpClient *http.Client,
) (*client.Inspection, error) {
	target, err := client.NewRemote(ref, client.RemoteOptions{
		Credentials: creds,
		HTTPClient:  httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("opening registry for %s: %w", ref, err)
	}
	insp, err := client.Inspect(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("inspecting package artifact %s: %w", ref, err)
	}
	return insp, nil
}

// unsupportedDeclarations describe the rejection reasons preflight.Classify
// gives an artifact that declares what this agent version cannot install.
var unsupportedDeclarations = map[string]string{
	preflight.ReasonHostFiles: "host-files layers",
	preflight.ReasonInterrupt: "an interrupt",
}

// refuseUnsupportedPackage fails for what preflight.Classify does not take
// for an installable artifact, as the operator's preflight does, and for an
// uninstall stage of a package that declares uninstall unsupported.
func refuseUnsupportedPackage(ref client.Reference, current stage.Stage, insp *client.Inspection) error {
	switch outcome := preflight.Classify(insp, nil); outcome.Kind {
	case preflight.KindArtifact:
	case preflight.KindImage:
		return fmt.Errorf("%s is a container image, not a package artifact", ref)
	default:
		if declared, ok := unsupportedDeclarations[outcome.Reason]; ok {
			return fmt.Errorf("package %s declares %s, which this agent version does not support (%s)", ref, declared, outcome.Reason)
		}
		return fmt.Errorf("package %s is %s by this agent version (%s)", ref, outcome.Kind, outcome.Reason)
	}
	if (current == stage.Uninstall || current == stage.UninstallCheck) &&
		insp.Package.Uninstall == artifact.UninstallUnsupported {
		return fmt.Errorf("package %s declares uninstall unsupported, so stage %q cannot run", ref, current)
	}
	return nil
}

// overlayPackageData copies every file of dataDir, the mounted package data,
// to the same relative path beneath copyRoot. A missing dataDir has nothing to
// overlay.
//
// It copies files only and never sets a directory mode, unlike
// hostfs.CopyTreeIfExistsFollowingSymlinks: the kubelet leaves a configMap
// volume directory at mode 0777, and copying that mode would make a directory
// of the laid-out package world-writable. A directory the payload lacks is
// created with mode 0755 less the umask, as cp -r made it.
//
// The keys of a configMap volume are symlinks into its ..data link, which
// itself names a timestamped directory, so a link to a file is copied as the
// file and a link to a directory is skipped: the walk visits that directory
// on its own.
func overlayPackageData(rootMount, dataDir, copyRoot string) error {
	info, err := os.Stat(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stating package data directory %q: %w", dataDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("package data path %q is not a directory", dataDir)
	}
	// filepath.WalkDir does not follow a root that is a symlink.
	source, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return fmt.Errorf("resolving package data directory %q: %w", dataDir, err)
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking package data %q: %w", path, walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stating package data %q: %w", path, err)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package data %q is not a regular file", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolving package data %q: %w", path, err)
		}
		return hostfs.CopyFileFollowingSymlinks(rootMount, path, filepath.Join(copyRoot, relative))
	})
}
