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
	"errors"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/config"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("package preparation", func() {
	It("copies the container resolver into the mounted root", func() {
		_, err := os.Stat("/etc/resolv.conf")
		if errors.Is(err, os.ErrNotExist) {
			Skip("/etc/resolv.conf is not available")
		}
		Expect(err).NotTo(HaveOccurred())

		root := GinkgoT().TempDir()

		Expect(copyResolverConfig(root)).To(Succeed())

		expected, err := os.ReadFile("/etc/resolv.conf")
		Expect(err).NotTo(HaveOccurred())
		actual, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(expected))
	})

	It("copies the container resolver through a relative host symlink", func() {
		_, err := os.Stat("/etc/resolv.conf")
		if errors.Is(err, os.ErrNotExist) {
			Skip("/etc/resolv.conf is not available")
		}
		Expect(err).NotTo(HaveOccurred())

		root := GinkgoT().TempDir()
		target := filepath.Join(root, "run", "resolver", "resolv.conf")
		Expect(os.MkdirAll(filepath.Dir(target), 0o755)).To(Succeed())
		Expect(os.WriteFile(target, []byte("old"), 0o600)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "etc"), 0o755)).To(Succeed())
		Expect(os.Symlink("../run/resolver/resolv.conf", filepath.Join(root, "etc", "resolv.conf"))).To(Succeed())

		Expect(copyResolverConfig(root)).To(Succeed())

		expected, err := os.ReadFile("/etc/resolv.conf")
		Expect(err).NotTo(HaveOccurred())
		actual, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(expected))
	})

	It("copies root overlays and validates expected configuration files", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		Expect(os.MkdirAll(filepath.Join(copyRoot, rootOverlayDirName, "etc"), 0o755)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(copyRoot, rootOverlayDirName, "etc", "package.conf"),
			[]byte("configured"),
			0o600,
		)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(copyRoot, configMapsDirName), 0o755)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(copyRoot, configMapsDirName, "input.conf"),
			[]byte("input"),
			0o600,
		)).To(Succeed())

		Expect(prepareHost(root, copyRoot, config.Config{
			ExpectedConfigFiles: []string{"input.conf"},
		})).To(Succeed())
		Expect(filepath.Join(root, "etc", "package.conf")).To(BeAnExistingFile())

		err := prepareHost(root, copyRoot, config.Config{
			ExpectedConfigFiles: []string{"missing.conf"},
		})
		Expect(err).To(MatchError(ContainSubstring(
			`expected config file "missing.conf" was not found`,
		)))
	})

	It("dereferences source links and follows relative host directory links", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		overlay := filepath.Join(copyRoot, rootOverlayDirName)
		shared := filepath.Join(copyRoot, "shared")
		Expect(os.MkdirAll(filepath.Join(overlay, "lib", "systemd", "system"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(shared, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(shared, "package.service"), []byte("unit"), 0o600)).To(Succeed())
		Expect(os.Symlink(
			filepath.Join(shared, "package.service"),
			filepath.Join(overlay, "lib", "systemd", "system", "package.service"),
		)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "usr", "lib"), 0o755)).To(Succeed())
		Expect(os.Symlink("usr/lib", filepath.Join(root, "lib"))).To(Succeed())

		Expect(prepareHost(root, copyRoot, config.Config{})).To(Succeed())

		data, err := os.ReadFile(filepath.Join(root, "usr", "lib", "systemd", "system", "package.service"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("unit"))
	})

	It("rejects expected configuration paths that escape the configmaps directory", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		Expect(os.MkdirAll(filepath.Join(copyRoot, configMapsDirName), 0o755)).To(Succeed())

		err := prepareHost(root, copyRoot, config.Config{
			ExpectedConfigFiles: []string{"../outside.conf"},
		})

		Expect(err).To(MatchError(ContainSubstring("must be relative to the configmaps directory")))
	})

	It("rejects an expected configuration path that is not a regular file", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		path := filepath.Join(copyRoot, configMapsDirName, "nested")
		Expect(os.MkdirAll(path, 0o755)).To(Succeed())

		err := prepareHost(root, copyRoot, config.Config{
			ExpectedConfigFiles: []string{"nested"},
		})

		Expect(err).To(MatchError(ContainSubstring("is not a regular file")))
	})

	It("rejects an existing package copy path that is not a directory", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		Expect(os.WriteFile(copyRoot, nil, 0o600)).To(Succeed())

		err := ensurePackageData(root, copyRoot, packageDataSources{
			dataDir:            GinkgoT().TempDir(),
			legacyNodeFilesDir: GinkgoT().TempDir(),
		})

		Expect(err).To(MatchError(ContainSubstring("is not a directory")))
	})

	It("reports a missing package data directory", func() {
		root := GinkgoT().TempDir()

		err := ensurePackageData(
			root,
			filepath.Join(root, "package"),
			packageDataSources{
				dataDir:            filepath.Join(root, "missing"),
				legacyNodeFilesDir: GinkgoT().TempDir(),
			},
		)

		Expect(err).To(MatchError(ContainSubstring("does not exist")))
	})

	It("merges current package data and legacy node files", func() {
		root := GinkgoT().TempDir()
		copyRoot := filepath.Join(root, "package")
		dataDir := GinkgoT().TempDir()
		legacyDataDir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("config"), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(legacyDataDir, "node.conf"), []byte("node"), 0o600)).To(Succeed())

		Expect(ensurePackageData(root, copyRoot, packageDataSources{
			dataDir:            dataDir,
			legacyNodeFilesDir: legacyDataDir,
		})).To(Succeed())

		Expect(filepath.Join(copyRoot, "config.json")).To(BeARegularFile())
		Expect(filepath.Join(copyRoot, "node.conf")).To(BeARegularFile())
	})
})
