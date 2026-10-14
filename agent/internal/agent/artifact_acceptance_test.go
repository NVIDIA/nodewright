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
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/NVIDIA/nodewright/agent/internal/flags"
	"github.com/NVIDIA/nodewright/agent/internal/interrupts"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/artifact/registrytest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	acceptanceConfigKey      = "settings.conf"
	acceptancePayloadConfig  = "payload default\n"
	acceptanceMountedConfig  = "mounted value\n"
	acceptanceRootFile       = "package.conf"
	acceptanceRootContent    = "placed from root_dir\n"
	acceptanceNodeMetadata   = "node-metadata"
	acceptanceStateRoot      = "/state"
	acceptanceLogRoot        = "/logs"
	acceptanceCopyDir        = "/package"
	acceptanceEffectSuffix   = ".effect"
	acceptanceAllCheckedFlag = "_ALL_CHECKED"
	// imagePodUmask is the umask of a package container, which an image
	// pod's init container copied the package under.
	imagePodUmask = 0o022
)

var _ = Describe("package artifact acceptance", func() {
	BeforeEach(func() {
		GinkgoT().Setenv(copyResolverEnv, "false")
		GinkgoT().Setenv(alwaysRunEnv, "false")
		GinkgoT().Setenv(resourceIDEnv, "resource_package_1.0.0")
		GinkgoT().Setenv(stateRootEnv, acceptanceStateRoot)
		GinkgoT().Setenv(logRootEnv, acceptanceLogRoot)
		GinkgoT().Setenv(writeLogsEnv, "false")
		unsetEnvironment(packageRefEnv)
		unsetEnvironment(registryAuthFileEnv)
	})

	It("runs a package from install to uninstall from its artifact and the mounted package data", func() {
		payload := writeAcceptancePackage()
		reg := newArtifactRegistry(registrytest.Options{})
		GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, supportedPackage(payload)))
		GinkgoT().Setenv(dataDirEnv, writeAcceptanceMounts())
		root := GinkgoT().TempDir()
		cfg, err := loadConfig(payload, nil)
		Expect(err).NotTo(HaveOccurred())
		layout, err := flags.NewLayout(root, acceptanceStateRoot, acceptanceLogRoot)
		Expect(err).NotTo(HaveOccurred())
		flagStore, err := flags.NewStore(layout, *cfg)
		Expect(err).NotTo(HaveOccurred())

		for _, current := range []stage.Stage{
			stage.Apply, stage.ApplyCheck,
			stage.Config, stage.ConfigCheck,
			stage.Upgrade, stage.UpgradeCheck,
			stage.Uninstall, stage.UninstallCheck,
		} {
			exitCode, stderr := runArtifactAgent(reg, string(current), root, acceptanceCopyDir)

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(acceptanceEffect(root, current)).To(BeARegularFile())
			if _, isCheck := stage.CheckToApply[current]; isCheck {
				Expect(filepath.Join(layout.FlagDir(), string(current)+acceptanceAllCheckedFlag)).To(BeARegularFile())
				continue
			}
			// Checked before uninstall-check, which removes every step flag.
			for _, configured := range cfg.Modes[current] {
				flag, err := flagStore.Path(configured)
				Expect(err).NotTo(HaveOccurred())
				Expect(flag).To(BeARegularFile())
			}
		}
		Expect(os.ReadFile(acceptanceEffect(root, stage.Config))).To(Equal([]byte(acceptanceMountedConfig)))
		Expect(os.ReadFile(filepath.Join(root, acceptanceRootFile))).To(Equal([]byte(acceptanceRootContent)))
		Expect(filepath.Join(layout.FlagDir(), startFlagName)).To(BeARegularFile())
	})

	DescribeTable(
		"leaves the host as the legacy data-dir path does",
		func(mode stage.Stage) {
			payload := writeAcceptancePackage()
			reg := newArtifactRegistry(registrytest.Options{})
			ref := pushArtifact(reg, supportedPackage(payload))

			writeMountFiles(payload)
			GinkgoT().Setenv(dataDirEnv, payload)
			legacyRoot := runAcceptanceStage(nil, mode)

			GinkgoT().Setenv(packageRefEnv, ref)
			GinkgoT().Setenv(dataDirEnv, writeAcceptanceMounts())
			artifactRoot := runAcceptanceStage(reg, mode)

			Expect(hostFiles(artifactRoot)).To(Equal(hostFiles(legacyRoot)))
		},
		Entry("apply", stage.Apply),
		Entry("apply-check", stage.ApplyCheck),
		Entry("config", stage.Config),
		Entry("config-check", stage.ConfigCheck),
		Entry("uninstall", stage.Uninstall),
		Entry("uninstall-check", stage.UninstallCheck),
		Entry("upgrade", stage.Upgrade),
		Entry("upgrade-check", stage.UpgradeCheck),
		Entry("post-interrupt", stage.PostInterrupt),
		Entry("post-interrupt-check", stage.PostInterruptCheck),
		Entry("interrupt", stage.Interrupt),
	)

	It("lays out the package copy as the image pod's init container copied it", func() {
		// The agent creates a directory only the mounts have under the
		// process umask, so run it under the container umask that
		// imagePodCopy models.
		previousUmask := syscall.Umask(imagePodUmask)
		DeferCleanup(syscall.Umask, previousUmask)
		payload := writeAcceptancePackage()
		reg := newArtifactRegistry(registrytest.Options{})
		GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, supportedPackage(payload)))
		mounts := writeAcceptanceMounts()
		GinkgoT().Setenv(dataDirEnv, mounts)
		root := GinkgoT().TempDir()

		exitCode, stderr := runArtifactAgent(reg, string(stage.Apply), root, acceptanceCopyDir)

		Expect(exitCode).To(Equal(ExitSuccess), stderr)
		Expect(describeTree(readTree(filepath.Join(root, acceptanceCopyDir)))).To(
			Equal(describeTree(imagePodCopy(payload, mounts))),
		)
	})
})

// writeAcceptancePackage writes a package tree with one on_host false step
// for every stage that has steps, and returns it. Each step records a side
// effect beside the package copy, named for its script; the config step's
// holds the configmaps file it copies. Every mode is set explicitly, so the
// tree is the same under any umask.
func writeAcceptancePackage() string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	Expect(os.Chmod(dir, 0o755)).To(Succeed())
	for _, name := range []string{"skyhook_dir", configMapsDirName, rootOverlayDirName} {
		Expect(os.Mkdir(filepath.Join(dir, name), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(dir, name), 0o755)).To(Succeed())
	}
	modes := map[stage.Stage][]map[string]any{}
	for _, current := range stage.All {
		if !stage.IsStepStage(current) {
			continue
		}
		script := acceptanceScript(current)
		effect := `"$SKYHOOK_DIR/../` + script + acceptanceEffectSuffix + `"`
		body := fmt.Sprintf("#!/bin/sh\necho %s > %s\n", script, effect)
		if current == stage.Config {
			body = fmt.Sprintf("#!/bin/sh\ncp \"$SKYHOOK_DIR/%s/%s\" %s\n", configMapsDirName, acceptanceConfigKey, effect)
		}
		writeFileMode(filepath.Join(dir, "skyhook_dir", script), body, 0o755)
		modes[current] = []map[string]any{{
			"name":         script,
			"path":         script,
			"arguments":    []string{},
			"returncodes":  []int{0},
			"on_host":      false,
			"idempotence":  false,
			"upgrade_step": false,
		}}
	}
	config, err := json.Marshal(map[string]any{
		"schema_version":        "v1",
		"package_name":          "package",
		"package_version":       "1.0.0",
		"expected_config_files": []string{acceptanceConfigKey},
		"modes":                 modes,
	})
	Expect(err).NotTo(HaveOccurred())
	writeFileMode(filepath.Join(dir, configFileName), string(config), 0o644)
	writeFileMode(filepath.Join(dir, configMapsDirName, acceptanceConfigKey), acceptancePayloadConfig, 0o644)
	writeFileMode(filepath.Join(dir, rootOverlayDirName, acceptanceRootFile), acceptanceRootContent, 0o644)
	Expect(os.Symlink(configFileName, filepath.Join(dir, "config-link.json"))).To(Succeed())
	return dir
}

// acceptanceScript names the step script of a stage so that each check
// script is the expected check of its stage's script.
func acceptanceScript(current stage.Stage) string {
	return strings.ReplaceAll(string(current), "-", "_")
}

// acceptanceEffect is the side effect the step of current records beneath
// root.
func acceptanceEffect(root string, current stage.Stage) string {
	return filepath.Join(root, acceptanceScript(current)+acceptanceEffectSuffix)
}

// writeMountFiles writes the files a package pod mounts beneath dir: the
// configmaps file that overrides the payload default, and plain
// node-metadata files.
func writeMountFiles(dir string) {
	GinkgoHelper()
	for _, name := range []string{configMapsDirName, acceptanceNodeMetadata} {
		Expect(os.MkdirAll(filepath.Join(dir, name), 0o755)).To(Succeed())
	}
	writeFileMode(filepath.Join(dir, configMapsDirName, acceptanceConfigKey), acceptanceMountedConfig, 0o644)
	writeFileMode(filepath.Join(dir, acceptanceNodeMetadata, "labels.json"), `{"zone":"a"}`, 0o644)
	writeFileMode(filepath.Join(dir, acceptanceNodeMetadata, "annotations.json"), `{}`, 0o644)
}

// writeAcceptanceMounts returns SKYHOOK_DATA_DIR as the kubelet presents it
// in an artifact pod: a directory of mode 0o700 holding the mount files,
// whose configmaps/ and node-metadata/ have the mode 0o777 the kubelet's
// emptyDir wrapper gives a configMap volume. The node-metadata files are
// plain because cp -r would keep the atomic writer's symlinks, which the
// overlay resolves.
func writeAcceptanceMounts() string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	writeMountFiles(dir)
	for _, name := range []string{configMapsDirName, acceptanceNodeMetadata} {
		Expect(os.Chmod(filepath.Join(dir, name), 0o777)).To(Succeed())
	}
	Expect(os.Chmod(dir, 0o700)).To(Succeed())
	return dir
}

// writeFileMode writes path with exactly mode, whatever the umask.
func writeFileMode(path, content string, mode fs.FileMode) {
	GinkgoHelper()
	Expect(os.WriteFile(path, []byte(content), mode)).To(Succeed())
	Expect(os.Chmod(path, mode)).To(Succeed())
}

// runAcceptanceStage runs mode on a fresh root, through reg when it is not
// nil, expects it to succeed and returns the root.
func runAcceptanceStage(reg *registrytest.Registry, mode stage.Stage) string {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	arguments := []string{string(mode), root, acceptanceCopyDir}
	if mode == stage.Interrupt {
		encoded, err := interrupts.Encode(interrupts.NoOp{})
		Expect(err).NotTo(HaveOccurred())
		arguments = append(arguments, encoded)
	}
	exitCode, stderr := runArtifactAgent(reg, arguments...)
	Expect(exitCode).To(Equal(ExitSuccess), stderr)
	return root
}

// hostFiles returns each path beneath root outside the package copy, with
// the content of each regular file outside the state root. Files under the
// state root embed timestamps, so they are returned by path only.
func hostFiles(root string) map[string]string {
	GinkgoHelper()
	copyRoot := filepath.Join(root, acceptanceCopyDir)
	stateRoot := filepath.Join(root, acceptanceStateRoot)
	files := map[string]string{}
	Expect(filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case path == copyRoot:
			return filepath.SkipDir
		case path == root:
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = ""
		if entry.IsDir() || strings.HasPrefix(path, stateRoot+string(filepath.Separator)) {
			return nil
		}
		data, err := os.ReadFile(path)
		files[relative] = string(data)
		return err
	})).To(Succeed())
	return files
}

type treeEntry struct {
	mode    fs.FileMode
	content string
}

// readTree returns each entry of the tree at dir, dir itself as ".", with its
// type and permission bits and a regular file's content or a symlink's
// target.
func readTree(dir string) map[string]treeEntry {
	GinkgoHelper()
	tree := map[string]treeEntry{}
	Expect(filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := treeEntry{mode: info.Mode().Type() | info.Mode().Perm()}
		var data []byte
		switch {
		case entry.mode&fs.ModeSymlink != 0:
			entry.content, err = os.Readlink(path)
		case entry.mode.IsRegular():
			data, err = os.ReadFile(path)
			entry.content = string(data)
		}
		tree[relative] = entry
		return err
	})).To(Succeed())
	return tree
}

// imagePodCopy models the package copy an image pod's init container made:
// mkdir -p of COPY_DIR, then cp -r without -p of /skyhook-package under
// imagePodUmask. /skyhook-package showed the payload with each mount file
// placed over it, and each directory only the mounts have at its mount mode;
// a directory the payload has keeps the payload mode, because subPath file
// mounts do not replace it.
func imagePodCopy(payload, mounts string) map[string]treeEntry {
	GinkgoHelper()
	tree := readTree(payload)
	for relative, entry := range readTree(mounts) {
		if _, exists := tree[relative]; !exists || !entry.mode.IsDir() {
			tree[relative] = entry
		}
	}
	for relative, entry := range tree {
		if entry.mode&fs.ModeSymlink == 0 {
			entry.mode &^= imagePodUmask
			tree[relative] = entry
		}
	}
	tree["."] = treeEntry{mode: fs.ModeDir | 0o755}
	return tree
}

// describeTree describes each entry by what the package copy must match: a
// directory's permission bits, a regular file's executable bits and content,
// and a symlink's target.
func describeTree(tree map[string]treeEntry) map[string]string {
	described := make(map[string]string, len(tree))
	for relative, entry := range tree {
		mode := entry.mode
		switch {
		case mode&fs.ModeSymlink != 0:
			mode = fs.ModeSymlink
		case mode.IsRegular():
			mode &= 0o111
		}
		described[relative] = fmt.Sprintf("%v %q", mode, entry.content)
	}
	return described
}
