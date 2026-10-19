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
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/client"

	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
)

// nodeDockerConfigPaths are the host Docker configs a registry credential is
// looked up in after the auth file, in order.
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

	insp, err := agent.inspectPackageArtifact(ctx, ref, req.rootMount, runtime)
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

// inspectPackageArtifact looks up the registry credentials for ref, logs which
// sources it skipped and which credentials it will try, and inspects ref. It
// logs source labels and library reasons only, never a credential.
func (agent orchestrator) inspectPackageArtifact(
	ctx context.Context,
	ref client.Reference,
	rootMount string,
	runtime runtimeConfig,
) (*client.Inspection, error) {
	paths, err := credentialSourcePaths(rootMount, runtime.registryAuthFile)
	if err != nil {
		return nil, fmt.Errorf("resolving registry credential sources: %w", err)
	}
	sources := make([]client.CredentialSource, 0, len(paths))
	for _, path := range paths {
		sources = append(sources, client.DockerConfigFile(path))
	}

	creds, skipped := client.LookupCredential(ref, sources)
	for _, source := range skipped {
		runtime.logger.Info(
			"registry credential source skipped",
			"source", source.Source,
			"reason", source.Reason,
		)
	}
	if len(creds) == 0 {
		runtime.logger.Info(
			"no registry credential found; reading the package artifact anonymously",
			"reference", ref.String(),
		)
	} else {
		labels := make([]string, 0, len(creds))
		for _, cred := range creds {
			labels = append(labels, cred.Source())
		}
		runtime.logger.Info(
			"registry credentials to try in order; the next is tried only when the registry refuses one",
			"reference", ref.String(),
			"sources", labels,
		)
	}

	target, err := client.NewRemote(ref, client.RemoteOptions{
		Credentials: creds,
		HTTPClient:  agent.registryHTTPClient,
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

// credentialSourcePaths returns the Docker configs to look a registry
// credential up in, in order: authFile, a container path, when it is set,
// then the node's configs beneath rootMount.
func credentialSourcePaths(rootMount, authFile string) ([]string, error) {
	paths := make([]string, 0, len(nodeDockerConfigPaths)+1)
	if authFile != "" {
		paths = append(paths, authFile)
	}
	for _, hostPath := range nodeDockerConfigPaths {
		path, err := hostfs.HostPathToMounted(rootMount, hostPath)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// refuseUnsupportedPackage fails for an artifact this agent version cannot
// install, and for an uninstall stage of a package that declares uninstall
// unsupported.
func refuseUnsupportedPackage(ref client.Reference, current stage.Stage, insp *client.Inspection) error {
	if insp.Kind != artifact.KindPackage {
		return fmt.Errorf("%s is a container image, not a package artifact", ref)
	}
	var unsupported []error
	if len(insp.Package.HostFiles) > 0 {
		unsupported = append(unsupported, fmt.Errorf(
			"package %s declares host-files layers, which this agent version does not support", ref,
		))
	}
	if insp.Package.Interrupt != nil {
		unsupported = append(unsupported, fmt.Errorf(
			"package %s declares an interrupt, which this agent version does not support", ref,
		))
	}
	if err := errors.Join(unsupported...); err != nil {
		return err
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
