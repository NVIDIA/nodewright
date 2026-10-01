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

package hostfs

import (
	"os"
	"path/filepath"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("filesystem copying", func() {
	It("copies directory contents and replaces existing regular files", func() {
		source := GinkgoT().TempDir()
		destination := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(source, "nested"), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(source, "nested"), 0o701)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(source, "nested", "step"), []byte("new"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(source, "existing"), []byte("new"), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(destination, "existing"), []byte("old"), 0o600)).To(Succeed())
		previousUmask := syscall.Umask(0o077)
		DeferCleanup(syscall.Umask, previousUmask)

		Expect(CopyTree(destination, source, destination)).To(Succeed())

		directoryInfo, err := os.Stat(filepath.Join(destination, "nested"))
		Expect(err).NotTo(HaveOccurred())
		Expect(directoryInfo.Mode().Perm()).To(Equal(os.FileMode(0o701)))
		data, err := os.ReadFile(filepath.Join(destination, "nested", "step"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("new"))
		data, err = os.ReadFile(filepath.Join(destination, "existing"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("new"))
		info, err := os.Stat(filepath.Join(destination, "nested", "step"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))

		Expect(os.Chmod(filepath.Join(source, "nested"), 0o750)).To(Succeed())
		Expect(os.Chmod(filepath.Join(destination, "nested"), 0o777)).To(Succeed())
		Expect(CopyTree(destination, source, destination)).To(Succeed())
		directoryInfo, err = os.Stat(filepath.Join(destination, "nested"))
		Expect(err).NotTo(HaveOccurred())
		Expect(directoryInfo.Mode().Perm()).To(Equal(os.FileMode(0o750)))
	})

	It("uses the default mode for destination ancestors", func() {
		root := GinkgoT().TempDir()
		source := GinkgoT().TempDir()
		destination := filepath.Join(root, "var", "lib", "nodewright", "root")
		Expect(os.Chmod(source, 0o700)).To(Succeed())

		Expect(CopyTree(root, source, destination)).To(Succeed())

		for _, path := range []string{
			filepath.Join(root, "var"),
			filepath.Join(root, "var", "lib"),
			filepath.Join(root, "var", "lib", "nodewright"),
		} {
			info, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		}
		info, err := os.Stat(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	It("rejects symbolic links in copy sources", func() {
		source := GinkgoT().TempDir()
		destination := GinkgoT().TempDir()
		Expect(os.Symlink(GinkgoT().TempDir(), filepath.Join(source, "linked"))).To(Succeed())

		Expect(CopyTree(destination, source, destination)).To(MatchError(ContainSubstring("is a symbolic link")))
	})

	It("rejects symbolic links at copy destinations", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(GinkgoT().TempDir(), "source")
		outside := filepath.Join(GinkgoT().TempDir(), "outside")
		destination := filepath.Join(root, "destination")
		Expect(os.WriteFile(source, []byte("contents"), 0o600)).To(Succeed())
		Expect(os.WriteFile(outside, []byte("original"), 0o600)).To(Succeed())
		Expect(os.Symlink(outside, destination)).To(Succeed())

		err := CopyFile(root, source, destination)

		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		data, err := os.ReadFile(outside)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("original"))
	})

	It("rejects a copy destination symlink that points at the source", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(root, "source")
		destination := filepath.Join(root, "destination")
		Expect(os.WriteFile(source, []byte("contents"), 0o600)).To(Succeed())
		Expect(os.Symlink(source, destination)).To(Succeed())

		Expect(CopyFile(root, source, destination)).To(MatchError(ContainSubstring("is a symbolic link")))
	})

	It("rejects a destination whose parent symlink resolves to the source", func() {
		root := GinkgoT().TempDir()
		outside := GinkgoT().TempDir()
		source := filepath.Join(outside, "source")
		destination := filepath.Join(root, "linked", "source")
		Expect(os.WriteFile(source, []byte("contents"), 0o600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(root, "linked"))).To(Succeed())

		Expect(CopyFile(root, source, destination)).To(MatchError(ContainSubstring("is a symbolic link")))
	})

	It("follows a symbolic link at a copy source", func() {
		root := GinkgoT().TempDir()
		target := filepath.Join(GinkgoT().TempDir(), "target")
		source := filepath.Join(GinkgoT().TempDir(), "source")
		destination := filepath.Join(root, "destination")
		Expect(os.WriteFile(target, []byte("contents"), 0o640)).To(Succeed())
		Expect(os.Symlink(target, source)).To(Succeed())

		Expect(CopyFile(root, source, destination)).To(Succeed())

		data, err := os.ReadFile(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("contents"))
		info, err := os.Stat(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o640)))
	})

	It("dereferences symbolic links in compatibility copy trees", func() {
		root := GinkgoT().TempDir()
		source := GinkgoT().TempDir()
		linkedDirectory := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(linkedDirectory, "step"), []byte("contents"), 0o640)).To(Succeed())
		Expect(os.Symlink(linkedDirectory, filepath.Join(source, "linked"))).To(Succeed())

		Expect(CopyTreeFollowingSymlinks(root, source, filepath.Join(root, "destination"))).To(Succeed())

		copied := filepath.Join(root, "destination", "linked", "step")
		data, err := os.ReadFile(copied)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("contents"))
		info, err := os.Lstat(filepath.Join(root, "destination", "linked"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
	})

	It("follows contained relative links at compatibility copy destinations", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(GinkgoT().TempDir(), "source")
		Expect(os.WriteFile(source, []byte("contents"), 0o640)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "usr", "lib"), 0o755)).To(Succeed())
		Expect(os.Symlink("usr/lib", filepath.Join(root, "lib"))).To(Succeed())

		Expect(CopyFileFollowingSymlinks(root, source, filepath.Join(root, "lib", "package"))).To(Succeed())

		data, err := os.ReadFile(filepath.Join(root, "usr", "lib", "package"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("contents"))
	})

	It("rejects destination links that escape compatibility copy roots", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(GinkgoT().TempDir(), "source")
		outside := GinkgoT().TempDir()
		Expect(os.WriteFile(source, []byte("contents"), 0o640)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(root, "linked"))).To(Succeed())

		err := CopyFileFollowingSymlinks(root, source, filepath.Join(root, "linked", "package"))

		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(outside, "package")).NotTo(BeAnExistingFile())
	})

	It("rejects a symbolic link as an optional copy source", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(root, "source")
		Expect(os.Symlink(GinkgoT().TempDir(), source)).To(Succeed())

		Expect(CopyTreeIfExists(root, source, filepath.Join(root, "destination"))).To(
			MatchError(ContainSubstring("is a symbolic link")),
		)
	})

	It("treats a missing optional tree as empty", func() {
		root := GinkgoT().TempDir()

		Expect(CopyTreeIfExists(
			root,
			filepath.Join(root, "missing"),
			filepath.Join(root, "destination"),
		)).To(Succeed())
	})

	It("copies a regular file and its permissions", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(root, "source")
		destination := filepath.Join(root, "destination", "file")
		Expect(os.WriteFile(source, []byte("contents"), 0o640)).To(Succeed())

		Expect(CopyFile(root, source, destination)).To(Succeed())

		data, err := os.ReadFile(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("contents"))
		info, err := os.Stat(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o640)))
	})

	It("treats copying a regular file onto itself as complete", func() {
		root := GinkgoT().TempDir()
		path := filepath.Join(root, "file")
		Expect(os.WriteFile(path, []byte("contents"), 0o600)).To(Succeed())

		Expect(CopyFile(root, path, path)).To(Succeed())

		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("contents"))
	})

	It("applies copied permissions despite the process umask", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(root, "source")
		destination := filepath.Join(root, "destination")
		Expect(os.WriteFile(source, []byte("contents"), 0o600)).To(Succeed())
		Expect(os.Chmod(source, 0o666)).To(Succeed())
		previousUmask := syscall.Umask(0o077)
		DeferCleanup(syscall.Umask, previousUmask)

		Expect(CopyFile(root, source, destination)).To(Succeed())

		info, err := os.Stat(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o666)))
	})

	It("rejects copy destinations outside the mounted root", func() {
		root := GinkgoT().TempDir()
		source := filepath.Join(root, "source")
		Expect(os.WriteFile(source, []byte("contents"), 0o600)).To(Succeed())

		err := CopyFile(root, source, filepath.Join(root, "..", "outside"))

		Expect(err).To(MatchError(ContainSubstring("must be contained")))
	})
})
