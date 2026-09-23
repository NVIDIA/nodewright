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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHostFS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Host Filesystem Suite")
}

var _ = Describe("mounted host filesystem", func() {
	It("validates path components used below agent-owned directories", func() {
		Expect(ValidatePathComponent("package name", "driver")).To(Succeed())
		Expect(ValidatePathComponent("package name", ".")).To(MatchError(
			`package name "." must be a single path component`,
		))
		Expect(ValidatePathComponent("package name", "../driver")).To(MatchError(
			`package name "../driver" must be a single path component`,
		))
	})

	It("resolves absolute host paths beneath the mounted root", func() {
		root := GinkgoT().TempDir()

		path, err := HostPathToMounted(root, "/etc/nodewright/config")

		Expect(err).NotTo(HaveOccurred())
		Expect(path).To(Equal(filepath.Join(root, "etc", "nodewright", "config")))
	})

	It("rejects relative roots and host paths", func() {
		_, err := HostPathToMounted("root", "/etc/config")
		Expect(err).To(MatchError(ContainSubstring("root mount")))

		_, err = HostPathToMounted(GinkgoT().TempDir(), "etc/config")
		Expect(err).To(MatchError(ContainSubstring("host path")))
	})

	It("creates, replaces, finds, and removes regular files", func() {
		root := GinkgoT().TempDir()
		path := filepath.Join(root, "state", "marker")

		Expect(CreateFile(root, path, []byte("created"), 0o600)).To(Succeed())
		exists, err := RegularFileExists(root, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
		err = CreateFile(root, path, nil, 0o600)
		Expect(errors.Is(err, fs.ErrExist)).To(BeTrue())

		Expect(WriteFile(root, path, []byte("replaced"), 0o600)).To(Succeed())
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("replaced"))

		Expect(RemoveFile(root, path)).To(Succeed())
		Expect(RemoveFile(root, path)).To(Succeed())
		exists, err = RegularFileExists(root, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse())
	})

	It("reads regular files and directories", func() {
		root := GinkgoT().TempDir()
		path := filepath.Join(root, "state", "marker")
		Expect(CreateFile(root, path, []byte("value"), 0o600)).To(Succeed())

		data, err := ReadFile(root, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("value"))
		entries, err := ReadDir(root, filepath.Dir(path))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Name()).To(Equal("marker"))
	})

	It("creates an exclusive file for streaming writes", func() {
		root := GinkgoT().TempDir()
		path := filepath.Join(root, "logs", "step.log")

		file, err := CreateFileWriter(root, path, 0o600)
		Expect(err).NotTo(HaveOccurred())
		_, err = file.Write([]byte("streamed"))
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Close()).To(Succeed())
		_, err = CreateFileWriter(root, path, 0o600)
		Expect(errors.Is(err, fs.ErrExist)).To(BeTrue())

		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("streamed"))
	})

	It("renames regular files without following symbolic links", func() {
		root := GinkgoT().TempDir()
		oldPath := filepath.Join(root, "state", "old")
		newPath := filepath.Join(root, "state", "new")
		Expect(CreateFile(root, oldPath, []byte("value"), 0o600)).To(Succeed())
		Expect(CreateFile(root, newPath, []byte("replaced"), 0o600)).To(Succeed())

		Expect(RenameFile(root, oldPath, newPath)).To(Succeed())
		Expect(oldPath).NotTo(BeAnExistingFile())
		data, err := os.ReadFile(newPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("value"))
	})

	It("rejects a non-regular rename destination", func() {
		root := GinkgoT().TempDir()
		oldPath := filepath.Join(root, "state", "old")
		newPath := filepath.Join(root, "state", "new")
		Expect(CreateFile(root, oldPath, []byte("value"), 0o600)).To(Succeed())
		Expect(os.Mkdir(newPath, 0o755)).To(Succeed())

		Expect(RenameFile(root, oldPath, newPath)).To(MatchError(ContainSubstring("is not a regular file")))
		Expect(oldPath).To(BeAnExistingFile())
		Expect(newPath).To(BeADirectory())
	})

	It("applies requested write permissions despite the process umask", func() {
		root := GinkgoT().TempDir()
		path := filepath.Join(root, "state", "marker")
		previousUmask := syscall.Umask(0o077)
		DeferCleanup(syscall.Umask, previousUmask)

		Expect(WriteFile(root, path, []byte("value"), 0o666)).To(Succeed())

		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o666)))
	})

	It("rejects paths outside the mounted root", func() {
		root := GinkgoT().TempDir()

		err := CreateFile(root, filepath.Join(root, "..", "outside"), nil, 0o600)

		Expect(err).To(MatchError(ContainSubstring("must be contained")))
	})

	It("does not follow symbolic links in rooted paths", func() {
		root := GinkgoT().TempDir()
		outside := GinkgoT().TempDir()
		Expect(os.Symlink(outside, filepath.Join(root, "linked"))).To(Succeed())

		err := WriteFile(root, filepath.Join(root, "linked", "marker"), nil, 0o600)

		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		Expect(filepath.Join(outside, "marker")).NotTo(BeAnExistingFile())
	})

	It("rejects symbolic links for reads, directory listings, writers, and renames", func() {
		root := GinkgoT().TempDir()
		outside := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(outside, "marker"), []byte("outside"), 0o600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(root, "linked"))).To(Succeed())

		_, err := ReadFile(root, filepath.Join(root, "linked", "marker"))
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		_, err = ReadDir(root, filepath.Join(root, "linked"))
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		_, err = CreateFileWriter(root, filepath.Join(root, "linked", "log"), 0o600)
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
		err = RenameFile(
			root,
			filepath.Join(root, "linked", "marker"),
			filepath.Join(root, "renamed"),
		)
		Expect(err).To(MatchError(ContainSubstring("is a symbolic link")))
	})

	It("keeps an existing path when an atomic replacement fails", func() {
		rootPath := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(rootPath, "target"), 0o755)).To(Succeed())
		root, err := os.OpenRoot(rootPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(root.Close()).To(Succeed())
		})

		err = writeFile(root, "target", []byte("replacement"), 0o600, true)

		Expect(err).To(MatchError(ContainSubstring("atomically replacing")))
		info, err := os.Stat(filepath.Join(rootPath, "target"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
		entries, err := os.ReadDir(rootPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
	})
})
