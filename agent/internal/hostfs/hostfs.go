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

// Package hostfs provides symlink-safe filesystem operations rooted at the
// host filesystem mounted into the agent container. Rooted path arguments
// must already include rootMount; HostPathToMounted explicitly converts a
// host-absolute path to that mounted form.
package hostfs

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const directoryMode = 0o755

// ValidatePathComponent rejects values that could escape or add hierarchy to
// an agent-owned path.
func ValidatePathComponent(name, value string) error {
	if value == "" || value == "." || !filepath.IsLocal(value) || filepath.Base(value) != value {
		return fmt.Errorf("%s %q must be a single path component", name, value)
	}
	return nil
}

// HostPathToMounted maps an absolute host path beneath the mounted host root.
func HostPathToMounted(rootMount, hostPath string) (string, error) {
	if !filepath.IsAbs(rootMount) {
		return "", fmt.Errorf("root mount %q is not absolute", rootMount)
	}
	if !filepath.IsAbs(hostPath) {
		return "", fmt.Errorf("host path %q is not absolute", hostPath)
	}

	rootMount = filepath.Clean(rootMount)
	relative := strings.TrimPrefix(filepath.Clean(hostPath), string(filepath.Separator))
	if relative == "" || relative == "." {
		return rootMount, nil
	}
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("host path %q escapes the mounted host root", hostPath)
	}
	return filepath.Join(rootMount, relative), nil
}

// RegularFileExists reports whether path is a regular file beneath rootMount.
func RegularFileExists(rootMount, path string) (exists bool, retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return false, err
	}
	defer closeRoot(root, &retErr)

	info, exists, err := inspect(root, relative)
	if err != nil {
		return false, fmt.Errorf("inspecting file %q: %w", path, err)
	}
	if exists && !info.Mode().IsRegular() {
		return false, fmt.Errorf("path %q is not a regular file", path)
	}
	return exists, nil
}

// ReadFile reads a regular file beneath rootMount without following symbolic
// links in its path.
func ReadFile(rootMount, path string) (data []byte, retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return nil, err
	}
	defer closeRoot(root, &retErr)

	info, exists, err := inspect(root, relative)
	if err != nil {
		return nil, fmt.Errorf("validating file %q: %w", path, err)
	}
	if !exists {
		return nil, fmt.Errorf("reading file %q: %w", path, fs.ErrNotExist)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path %q is not a regular file", path)
	}
	data, err = root.ReadFile(relative)
	if err != nil {
		return nil, fmt.Errorf("reading file %q: %w", path, err)
	}
	return data, nil
}

// ReadDir reads a directory beneath rootMount without following symbolic
// links in its path.
func ReadDir(rootMount, path string) (entries []fs.DirEntry, retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return nil, err
	}
	defer closeRoot(root, &retErr)

	info, exists, err := inspect(root, relative)
	if err != nil {
		return nil, fmt.Errorf("validating directory %q: %w", path, err)
	}
	if !exists {
		return nil, fmt.Errorf("reading directory %q: %w", path, fs.ErrNotExist)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path %q is not a directory", path)
	}
	directory, err := root.Open(relative)
	if err != nil {
		return nil, fmt.Errorf("opening directory %q: %w", path, err)
	}
	defer func() {
		if err := directory.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("closing directory %q: %w", path, err)
		}
	}()
	entries, err = directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", path, err)
	}
	return entries, nil
}

// CreateFile creates a new regular file beneath rootMount without replacing an
// existing path, then syncs the file and its parent directory.
func CreateFile(rootMount, path string, data []byte, mode fs.FileMode) (retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	if err := ensureDirectories(root, filepath.Dir(relative), directoryMode); err != nil {
		return fmt.Errorf("preparing parent directory for %q: %w", path, err)
	}
	info, exists, err := inspect(root, relative)
	if err != nil {
		return fmt.Errorf("validating file target %q: %w", path, err)
	}
	if exists {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path %q is not a regular file", path)
		}
		return fmt.Errorf("file %q already exists: %w", path, fs.ErrExist)
	}
	if err := writeFile(root, relative, data, mode, false); err != nil {
		return fmt.Errorf("creating file %q: %w", path, err)
	}
	return nil
}

// CreateFileWriter creates a new regular file beneath rootMount and returns it
// open for writing. The caller owns the returned file and must close it.
func CreateFileWriter(rootMount, path string, mode fs.FileMode) (file *os.File, retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil && file != nil {
			retErr = errors.Join(retErr, file.Close())
			file = nil
		}
	}()
	defer closeRoot(root, &retErr)

	if err := ensureDirectories(root, filepath.Dir(relative), directoryMode); err != nil {
		return nil, fmt.Errorf("preparing parent directory for %q: %w", path, err)
	}
	info, exists, err := inspect(root, relative)
	if err != nil {
		return nil, fmt.Errorf("validating file target %q: %w", path, err)
	}
	if exists {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("path %q is not a regular file", path)
		}
		return nil, fmt.Errorf("file %q already exists: %w", path, fs.ErrExist)
	}
	file, err = root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return nil, fmt.Errorf("creating file %q without following symlinks: %w", path, err)
	}
	if err := root.Chmod(relative, mode); err != nil {
		closeErr := file.Close()
		file = nil
		removeErr := root.Remove(relative)
		return nil, errors.Join(
			fmt.Errorf("setting permissions on %q: %w", path, err),
			closeErr,
			removeErr,
		)
	}
	return file, nil
}

// WriteFile creates or atomically replaces a regular file beneath rootMount,
// then syncs the file and its parent directory.
func WriteFile(rootMount, path string, data []byte, mode fs.FileMode) (retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	if err := ensureDirectories(root, filepath.Dir(relative), directoryMode); err != nil {
		return fmt.Errorf("preparing parent directory for %q: %w", path, err)
	}
	info, exists, err := inspect(root, relative)
	if err != nil {
		return fmt.Errorf("validating file target %q: %w", path, err)
	}
	if exists && !info.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a regular file", path)
	}
	if err := writeFile(root, relative, data, mode, true); err != nil {
		return fmt.Errorf("writing file %q: %w", path, err)
	}
	return nil
}

// RenameFile renames a regular file beneath rootMount and syncs the affected
// directories. An existing regular destination is replaced.
func RenameFile(rootMount, oldPath, newPath string) (retErr error) {
	root, oldRelative, err := openRoot(rootMount, oldPath)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	newRelative, err := filepath.Rel(filepath.Clean(rootMount), filepath.Clean(newPath))
	if err != nil {
		return fmt.Errorf("resolving host path %q: %w", newPath, err)
	}
	if !filepath.IsLocal(newRelative) {
		return fmt.Errorf("host path %q must be contained within %q", newPath, rootMount)
	}
	oldInfo, exists, err := inspect(root, oldRelative)
	if err != nil {
		return fmt.Errorf("validating rename source %q: %w", oldPath, err)
	}
	if !exists {
		return fmt.Errorf("renaming file %q: %w", oldPath, fs.ErrNotExist)
	}
	if !oldInfo.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a regular file", oldPath)
	}
	newInfo, exists, err := inspect(root, newRelative)
	if err != nil {
		return fmt.Errorf("validating rename destination %q: %w", newPath, err)
	}
	if exists && !newInfo.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a regular file", newPath)
	}
	if err := ensureDirectories(root, filepath.Dir(newRelative), directoryMode); err != nil {
		return fmt.Errorf("preparing parent directory for %q: %w", newPath, err)
	}
	if err := root.Rename(oldRelative, newRelative); err != nil {
		return fmt.Errorf("renaming file %q to %q: %w", oldPath, newPath, err)
	}
	newDirectory := filepath.Dir(newRelative)
	if err := syncRootedDirectory(root, newDirectory); err != nil {
		return fmt.Errorf("syncing rename destination directory %q: %w", filepath.Dir(newPath), err)
	}
	oldDirectory := filepath.Dir(oldRelative)
	if oldDirectory != newDirectory {
		if err := syncRootedDirectory(root, oldDirectory); err != nil {
			return fmt.Errorf("syncing rename source directory %q: %w", filepath.Dir(oldPath), err)
		}
	}
	return nil
}

// RemoveFile removes a regular file beneath rootMount. A missing file is
// already removed and succeeds.
func RemoveFile(rootMount, path string) (retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	info, exists, err := inspect(root, relative)
	if err != nil {
		return fmt.Errorf("inspecting file %q: %w", path, err)
	}
	if !exists {
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a regular file", path)
	}
	if err := root.Remove(relative); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing file %q: %w", path, err)
	}
	return nil
}

func openRoot(rootMount, path string) (*os.Root, string, error) {
	if !filepath.IsAbs(rootMount) {
		return nil, "", fmt.Errorf("root mount %q is not absolute", rootMount)
	}
	relative, err := filepath.Rel(filepath.Clean(rootMount), filepath.Clean(path))
	if err != nil {
		return nil, "", fmt.Errorf("resolving host path %q: %w", path, err)
	}
	if !filepath.IsLocal(relative) {
		return nil, "", fmt.Errorf("host path %q must be contained within %q", path, rootMount)
	}
	root, err := os.OpenRoot(rootMount)
	if err != nil {
		return nil, "", fmt.Errorf("opening mounted host root %q: %w", rootMount, err)
	}
	return root, relative, nil
}

func ensureDirectories(root *os.Root, directory string, mode fs.FileMode) error {
	current := ""
	for _, component := range pathComponents(directory) {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if mkdirErr := root.Mkdir(current, mode); mkdirErr != nil {
				if !errors.Is(mkdirErr, fs.ErrExist) {
					return fmt.Errorf("creating directory %q: %w", current, mkdirErr)
				}
			} else {
				// Preserve existing directory modes while correcting umask on directories created here.
				if err := root.Chmod(current, mode); err != nil {
					return fmt.Errorf("setting permissions on directory %q: %w", current, err)
				}
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspecting directory %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symbolic link", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("path component %q is not a directory", current)
		}
	}
	return nil
}

func inspect(root *os.Root, path string) (os.FileInfo, bool, error) {
	components := pathComponents(path)
	current := ""
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("inspecting path component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("path component %q is a symbolic link", current)
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, false, fmt.Errorf("path component %q is not a directory", current)
		}
		if index == len(components)-1 {
			return info, true, nil
		}
	}
	return nil, false, nil
}

func pathComponents(path string) []string {
	clean := filepath.Clean(path)
	if clean == "." {
		return nil
	}
	return strings.Split(clean, string(filepath.Separator))
}

func writeFile(
	root *os.Root,
	path string,
	data []byte,
	mode fs.FileMode,
	replace bool,
) error {
	return writeRootedFile(root, path, mode, replace, func(file *os.File) error {
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("writing %q: %w", path, err)
		}
		return nil
	})
}

func writeRootedFile(
	root *os.Root,
	path string,
	mode fs.FileMode,
	replace bool,
	write func(*os.File) error,
) (retErr error) {
	writePath := path
	if replace {
		writePath = filepath.Join(filepath.Dir(path), ".hostfs-"+rand.Text()+".tmp")
	}
	file, err := root.OpenFile(writePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("creating %q without following symlinks: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) && retErr == nil {
			retErr = fmt.Errorf("closing %q: %w", path, err)
		}
		if retErr != nil {
			if err := root.Remove(writePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("removing incomplete file %q: %w", writePath, err))
			}
		}
	}()

	if err := write(file); err != nil {
		return err
	}
	if err := root.Chmod(writePath, mode); err != nil {
		return fmt.Errorf("setting permissions on %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", path, err)
	}
	if replace {
		if err := root.Rename(writePath, path); err != nil {
			return fmt.Errorf("atomically replacing %q: %w", path, err)
		}
	}
	if err := syncRootedDirectory(root, filepath.Dir(path)); err != nil {
		return fmt.Errorf("syncing parent directory for %q: %w", path, err)
	}
	return nil
}

func syncRootedDirectory(root *os.Root, directory string) (retErr error) {
	file, err := root.Open(directory)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	if err := file.Sync(); err != nil {
		return err
	}
	return nil
}

func closeRoot(root *os.Root, retErr *error) {
	if err := root.Close(); err != nil && *retErr == nil {
		*retErr = fmt.Errorf("closing mounted host root %q: %w", root.Name(), err)
	}
}
