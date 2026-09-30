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
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyTreeIfExists recursively copies source into a destination beneath
// rootMount. A missing source is treated as an empty tree.
func CopyTreeIfExists(rootMount, source, destination string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stating copy source %q: %w", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("copy source %q is a symbolic link", source)
	}
	if !info.IsDir() {
		return fmt.Errorf("copy source %q is not a directory", source)
	}
	return CopyTree(rootMount, source, destination)
}

// CopyTree recursively copies regular files from source into a destination
// beneath rootMount and rejects symbolic links in the source tree. A failed
// copy can leave entries already copied to the destination; callers may retry.
func CopyTree(rootMount, source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking copy source %q: %w", path, walkErr)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolving copy source %q: %w", path, err)
		}
		if relative != "." && !filepath.IsLocal(relative) {
			return fmt.Errorf("copy source path %q escapes %q", path, source)
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stating copy source %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("copy source %q is a symbolic link", path)
		}
		if info.IsDir() {
			if err := makeDirectory(rootMount, target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("creating copy destination directory %q: %w", target, err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("copy source %q is not a regular file", path)
		}
		if err := copyRegularFile(rootMount, path, target, info.Mode().Perm()); err != nil {
			return err
		}
		return nil
	})
}

// CopyTreeIfExistsFollowingSymlinks recursively copies source into a
// destination beneath rootMount. Source links are dereferenced and destination
// links are followed only when they remain inside rootMount. A missing source
// is treated as an empty tree.
func CopyTreeIfExistsFollowingSymlinks(rootMount, source, destination string) error {
	info, err := os.Stat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stating copy source %q: %w", source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("copy source %q is not a directory", source)
	}
	return CopyTreeFollowingSymlinks(rootMount, source, destination)
}

// CopyTreeFollowingSymlinks recursively copies source into a destination
// beneath rootMount while dereferencing source links. Relative destination
// links are followed by os.Root and cannot escape rootMount.
func CopyTreeFollowingSymlinks(rootMount, source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stating copy source %q: %w", source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("copy source %q is not a directory", source)
	}
	return copyTreeEntryFollowingSymlinks(
		rootMount,
		source,
		destination,
		map[string]struct{}{},
	)
}

func copyTreeEntryFollowingSymlinks(
	rootMount, source, destination string,
	ancestors map[string]struct{},
) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolving copy source %q: %w", source, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stating copy source %q: %w", source, err)
	}
	if info.IsDir() {
		if _, exists := ancestors[resolved]; exists {
			return fmt.Errorf("copy source %q contains a symbolic-link cycle", source)
		}
		ancestors[resolved] = struct{}{}
		defer delete(ancestors, resolved)

		if err := makeDirectoryFollowingSymlinks(rootMount, destination, info.Mode().Perm()); err != nil {
			return fmt.Errorf("creating copy destination directory %q: %w", destination, err)
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return fmt.Errorf("reading copy source directory %q: %w", source, err)
		}
		for _, entry := range entries {
			if err := copyTreeEntryFollowingSymlinks(
				rootMount,
				filepath.Join(resolved, entry.Name()),
				filepath.Join(destination, entry.Name()),
				ancestors,
			); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy source %q is not a regular file", source)
	}
	if err := copyRegularFileFollowingSymlinks(
		rootMount,
		resolved,
		destination,
		info.Mode().Perm(),
	); err != nil {
		return err
	}
	return nil
}

// CopyFile copies a regular file and its permissions to a destination beneath
// rootMount. Source symbolic links are followed, while symbolic links in the
// destination path are rejected.
func CopyFile(rootMount, source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stating copy source %q: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy source %q is not a regular file", source)
	}
	return copyRegularFile(rootMount, source, destination, info.Mode().Perm())
}

// CopyFileFollowingSymlinks copies a regular source file to a destination
// beneath rootMount. Source links are dereferenced and relative destination
// links are followed only when they remain inside rootMount.
func CopyFileFollowingSymlinks(rootMount, source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stating copy source %q: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("copy source %q is not a regular file", source)
	}
	return copyRegularFileFollowingSymlinks(rootMount, source, destination, info.Mode().Perm())
}

func makeDirectory(rootMount, path string, mode fs.FileMode) (retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	if relative == "." {
		// CopyTree merges source contents into rootMount without changing the
		// mounted host root's own permissions.
		return nil
	}
	if err := ensureDirectories(root, filepath.Dir(relative), directoryMode); err != nil {
		return fmt.Errorf("preparing parent directory for %q: %w", path, err)
	}
	if err := ensureDirectories(root, relative, mode); err != nil {
		return fmt.Errorf("creating directory %q: %w", path, err)
	}
	if err := root.Chmod(relative, mode); err != nil {
		return fmt.Errorf("setting permissions on directory %q: %w", path, err)
	}
	return nil
}

func copyRegularFile(
	rootMount, source, destination string,
	mode fs.FileMode,
) (retErr error) {
	root, relative, err := openRoot(rootMount, destination)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening copy source %q: %w", source, err)
	}
	defer func() {
		if err := sourceFile.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("closing copy source %q: %w", source, err)
		}
	}()

	if err := ensureDirectories(root, filepath.Dir(relative), directoryMode); err != nil {
		return fmt.Errorf("creating copy destination directory %q: %w", filepath.Dir(destination), err)
	}
	info, exists, err := inspect(root, relative)
	if err != nil {
		return fmt.Errorf("validating copy destination %q: %w", destination, err)
	}
	if exists {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("copy destination %q is not a regular file", destination)
		}
		sourceInfo, err := sourceFile.Stat()
		if err != nil {
			return fmt.Errorf("stating open copy source %q: %w", source, err)
		}
		if os.SameFile(sourceInfo, info) {
			return nil
		}
	}

	if err := writeRootedFile(root, relative, mode, true, func(destinationFile *os.File) error {
		if _, err := io.Copy(destinationFile, sourceFile); err != nil {
			return fmt.Errorf("copying %q to %q: %w", source, destination, err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("writing copy destination %q: %w", destination, err)
	}
	return nil
}

func makeDirectoryFollowingSymlinks(rootMount, path string, mode fs.FileMode) (retErr error) {
	root, relative, err := openRoot(rootMount, path)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	if relative == "." {
		return nil
	}
	if err := root.MkdirAll(filepath.Dir(relative), directoryMode); err != nil {
		return fmt.Errorf("preparing parent directory for %q: %w", path, err)
	}
	info, err := root.Stat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir(relative, mode); err != nil {
			return fmt.Errorf("creating directory %q: %w", path, err)
		}
		info, err = root.Stat(relative)
	}
	if err != nil {
		return fmt.Errorf("inspecting directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("copy destination %q is not a directory", path)
	}
	if err := root.Chmod(relative, mode); err != nil {
		return fmt.Errorf("setting permissions on directory %q: %w", path, err)
	}
	return nil
}

func copyRegularFileFollowingSymlinks(
	rootMount, source, destination string,
	mode fs.FileMode,
) (retErr error) {
	root, relative, err := openRoot(rootMount, destination)
	if err != nil {
		return err
	}
	defer closeRoot(root, &retErr)

	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening copy source %q: %w", source, err)
	}
	defer func() {
		if err := sourceFile.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("closing copy source %q: %w", source, err)
		}
	}()

	if err := root.MkdirAll(filepath.Dir(relative), directoryMode); err != nil {
		return fmt.Errorf("creating copy destination directory %q: %w", filepath.Dir(destination), err)
	}
	info, err := root.Stat(relative)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("validating copy destination %q: %w", destination, err)
	}
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("copy destination %q is not a regular file", destination)
		}
		sourceInfo, err := sourceFile.Stat()
		if err != nil {
			return fmt.Errorf("stating open copy source %q: %w", source, err)
		}
		if os.SameFile(sourceInfo, info) {
			return nil
		}
	}

	destinationFile, err := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("opening copy destination %q: %w", destination, err)
	}
	defer func() {
		if err := destinationFile.Close(); err != nil && !errors.Is(err, os.ErrClosed) && retErr == nil {
			retErr = fmt.Errorf("closing copy destination %q: %w", destination, err)
		}
	}()
	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		return fmt.Errorf("copying %q to %q: %w", source, destination, err)
	}
	if err := root.Chmod(relative, mode); err != nil {
		return fmt.Errorf("setting permissions on %q: %w", destination, err)
	}
	if err := destinationFile.Sync(); err != nil {
		return fmt.Errorf("syncing %q: %w", destination, err)
	}
	if err := destinationFile.Close(); err != nil {
		return fmt.Errorf("closing copy destination %q: %w", destination, err)
	}
	if err := syncRootedDirectory(root, filepath.Dir(relative)); err != nil {
		return fmt.Errorf("syncing parent directory for %q: %w", destination, err)
	}
	return nil
}
