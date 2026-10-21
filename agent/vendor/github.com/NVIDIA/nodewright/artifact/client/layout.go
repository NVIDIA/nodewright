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

package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NVIDIA/nodewright/artifact"
)

const (
	// payloadConfig is the file at the root of a payload that the agent reads.
	payloadConfig = "config.json"
	// installAttempts bounds how often Layout checks the destination again
	// after a concurrent layout changed it before the rename or swap.
	installAttempts = 3
)

// Layout lays out the payload of the package insp describes at dest, a local
// path beneath root other than root itself, and installs it atomically by
// name within the parent of dest. insp is what Inspect returned.
//
// Every filesystem operation is relative to root, so a symlink in the parent
// path of dest is followed only while it stays beneath root, and an absolute
// one fails. Missing parents are created as mkdir -p does, with mode 0o755
// less the umask. The payload layers are laid out in manifest order, each as
// ExtractLayer lays it out, into a staging directory of mode 0o755 beside
// dest, named .<base of dest>.staging-<random hex>, which Layout holds an
// advisory lock on while it runs; host-files layers are not placed.
// config.json at its root must then be a regular file JSON-equal to
// Package.Config, else Layout fails with an artifact.InvalidArtifactError
// with artifact.ReasonConfigMismatch.
//
// When dest is a directory, every entry of it that the payload lacks is then
// carried over into the staging directory, so the result is what cp -r of the
// payload over dest leaves: a payload entry wins at its path, and a directory
// both hold keeps the entries of dest that the payload lacks. A directory is
// carried over as a new one with the permission bits it has in dest, and any
// other entry as a hard link, which keeps its type, mode, owner and content.
// Directory modes are set only once every payload layer is written and every
// entry carried over, so a later layer or a carried-over entry still goes
// beneath a directory made read-only, whether or not the caller is root.
// Last, the staging directory is renamed onto dest when dest does not exist,
// or swapped with it when dest is a directory, and the tree it replaced is
// removed, best effort. A dest of any other type fails. Replacing a directory
// needs Linux or darwin, and fails elsewhere with an error that matches
// errors.ErrUnsupported.
//
// When Layout fails, dest is as it was and the staging directory is removed.
// Before it starts, Layout removes each staging directory of dest that no
// running layout holds the lock on, so one that a layout killed by a signal,
// the OOM killer or a reboot left behind is gone after the next layout of
// dest, while layouts of dest running at the same time keep theirs. Where no
// advisory lock is wired up for the GOOS, none is removed.
// An error is one of ExtractLayer's, ReasonConfigMismatch, a filesystem error
// or the context error.
func Layout(ctx context.Context, insp *Inspection, root *os.Root, dest string) error {
	if insp.Kind != artifact.KindPackage {
		return fmt.Errorf("laying out %s: a %s manifest is not a package", insp.Pinned, insp.Kind)
	}
	if !filepath.IsLocal(dest) || filepath.Clean(dest) == "." {
		return fmt.Errorf("laying out %s: destination %q is not a local path beneath the root", insp.Pinned, dest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := layout(ctx, insp, root, filepath.Clean(dest)); err != nil {
		return fmt.Errorf("laying out %s at %s: %w", insp.Pinned, dest, err)
	}
	return nil
}

func layout(ctx context.Context, insp *Inspection, root *os.Root, dest string) (err error) {
	parentName, name := filepath.Dir(dest), filepath.Base(dest)
	if err := root.MkdirAll(parentName, 0o755); err != nil {
		return err
	}
	parent, err := root.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()

	removeStaleStaging(parent, name)
	staging, lock, err := mkdirStaging(parent, name)
	if err != nil {
		return err
	}
	// Closed last, so the staging directory is still held while a failed
	// layout removes it.
	defer func() { _ = lock.Close() }()
	defer func() {
		if err != nil {
			err = errors.Join(err, removeTree(parent, staging))
		}
	}()
	if err := extractPayload(ctx, insp, parent, staging, name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return install(parent, staging, name)
}

// stagingPrefix is the name of a staging directory of name, less its random
// suffix of stagingSuffixLen lowercase hex digits.
func stagingPrefix(name string) string {
	return "." + name + ".staging-"
}

const stagingSuffixLen = 16

// isStaging reports whether entry is named as a staging directory of name.
func isStaging(entry, name string) bool {
	suffix, ok := strings.CutPrefix(entry, stagingPrefix(name))
	return ok && len(suffix) == stagingSuffixLen &&
		strings.Trim(suffix, "0123456789abcdef") == ""
}

// mkdirStaging creates a directory of mode 0o755 in parent with a fresh
// random name derived from name, and returns that name and the directory open
// with its lock held, which lasts until the file is closed.
func mkdirStaging(parent *os.Root, name string) (string, *os.File, error) {
	for {
		var random [stagingSuffixLen / 2]byte
		_, _ = rand.Read(random[:])
		staging := stagingPrefix(name) + hex.EncodeToString(random[:])
		err := parent.Mkdir(staging, 0o755)
		switch {
		case errors.Is(err, fs.ErrExist):
			continue
		case err != nil:
			return "", nil, err
		}
		lock, held, err := lockStaging(parent, staging)
		switch {
		case err != nil:
			return "", nil, errors.Join(err, parent.Remove(staging))
		case !held:
			// Another layout's removeStaleStaging took the directory before
			// its lock was taken here, and removes it.
			continue
		}
		// Mkdir applies the umask.
		if err := parent.Chmod(staging, 0o755); err != nil {
			return "", nil, errors.Join(err, removeTree(parent, staging), lock.Close())
		}
		return staging, lock, nil
	}
}

// lockStaging opens the entry staging of parent and takes its lock without
// waiting. It returns the open file when it holds the lock and the file is
// still the entry at staging, and reports that it does not when the entry is
// gone, is replaced, or is locked by a running layout.
func lockStaging(parent *os.Root, staging string) (*os.File, bool, error) {
	f, err := parent.Open(staging)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	held, err := tryLock(f)
	if held {
		held, err = stillAt(parent, staging, f)
	}
	if err != nil || !held {
		return nil, false, errors.Join(err, f.Close())
	}
	return f, true, nil
}

// stillAt reports whether f is the entry name of parent.
func stillAt(parent *os.Root, name string, f *os.File) (bool, error) {
	opened, err := f.Stat()
	if err != nil {
		return false, err
	}
	current, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && os.SameFile(opened, current), err
}

// removeStaleStaging removes, best effort, each staging directory of name in
// parent that no running layout holds: one a layout left when it was killed,
// or one a version of Layout that took no lock left.
func removeStaleStaging(parent *os.Root, name string) {
	if !stagingLocks {
		return
	}
	entries, err := fs.ReadDir(parent.FS(), ".")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !isStaging(entry.Name(), name) {
			continue
		}
		lock, held, err := lockStaging(parent, entry.Name())
		if err != nil || !held {
			continue
		}
		_ = removeTree(parent, entry.Name())
		_ = lock.Close()
	}
}

// extractPayload lays out the payload layers of insp in manifest order in
// the directory staging of parent, checks its config.json, carries over what
// the entry previous of parent holds beyond the payload, then sets the
// directory modes.
func extractPayload(ctx context.Context, insp *Inspection, parent *os.Root, staging, previous string) error {
	dir, err := parent.OpenRoot(staging)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	dirModes := map[string]fs.FileMode{}
	for _, desc := range insp.Package.Payload {
		if err := extractLayer(ctx, insp.target, desc, dir, dirModes); err != nil {
			return err
		}
	}
	// why: checked before the carry-over, so a config.json the payload lacks
	// is never taken from previous.
	if err := checkConfig(dir, insp.Package.Config); err != nil {
		return err
	}
	if err := carryOver(ctx, parent, previous, staging, dirModes); err != nil {
		return err
	}
	return chmodDirs(dir, dirModes)
}

// carryOver puts into the directory staging of parent each entry of the
// directory previous of parent that staging lacks, descending into each
// directory both hold, and leaves previous as it was. A carried-over
// directory is created with mode 0o700 and its permission bits in previous
// are added to dirModes, for chmodDirs to set; any other entry is hard-linked.
// A previous that does not exist or is not a directory has nothing to carry
// over.
func carryOver(ctx context.Context, parent *os.Root, previous, staging string, dirModes map[string]fs.FileMode) error {
	info, err := parent.Lstat(previous)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return nil
	}
	return fs.WalkDir(parent.FS(), previous, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == previous {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimPrefix(path, previous+"/")
		target := staging + "/" + name
		existing, err := parent.Lstat(target)
		switch {
		case err == nil:
			// The payload's entry wins; descend only into a directory both hold.
			if d.IsDir() && !existing.IsDir() {
				return fs.SkipDir
			}
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return err
		case !d.IsDir():
			return parent.Link(path, target)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		dirModes[name] = info.Mode().Perm()
		return parent.Mkdir(target, 0o700)
	})
}

// checkConfig requires config.json at the root of dir to be a regular file
// JSON-equal to config.
func checkConfig(dir *os.Root, config json.RawMessage) error {
	data, err := readRegular(dir, payloadConfig)
	if err == nil && jsonEqual(data, config) {
		return nil
	}
	detail := payloadConfig + " is not JSON-equal to the metadata config"
	if err != nil {
		detail = err.Error()
	}
	return &artifact.InvalidArtifactError{Reason: artifact.ReasonConfigMismatch, Detail: detail}
}

// readRegular reads name in dir, which must be a regular file.
func readRegular(dir *os.Root, name string) ([]byte, error) {
	info, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return dir.ReadFile(name)
}

// jsonEqual reports whether a and b each hold exactly one JSON value and the
// two are equal, whatever their key order, whitespace and number spelling.
// Numbers are compared exactly, not as float64.
func jsonEqual(a, b []byte) bool {
	x, errX := decodeJSON(a)
	y, errY := decodeJSON(b)
	return errX == nil && errY == nil && sameJSON(x, y)
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the JSON value")
	}
	return v, nil
}

func sameJSON(x, y any) bool {
	switch x := x.(type) {
	case map[string]any:
		y, ok := y.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for key, v := range x {
			if w, ok := y[key]; !ok || !sameJSON(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := y.([]any)
		return ok && slices.EqualFunc(x, y, sameJSON)
	case json.Number:
		y, ok := y.(json.Number)
		if !ok {
			return false
		}
		// SetString refuses a decimal exponent beyond 1e6 rather than expand
		// it, so a hostile number cannot exhaust memory and compares unequal.
		p, okP := new(big.Rat).SetString(x.String())
		q, okQ := new(big.Rat).SetString(y.String())
		return okP && okQ && p.Cmp(q) == 0
	}
	return x == y
}

// install puts the directory staging of parent in place of name: by rename
// when name does not exist, or by swap when it is a directory, after which
// the replaced tree is removed, best effort. A concurrent change to name
// between the check and the rename or swap makes it check again.
func install(parent *os.Root, staging, name string) error {
	for range installAttempts {
		info, err := parent.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			err = parent.Rename(staging, name)
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		case err != nil:
			return err
		case !info.IsDir():
			return fmt.Errorf("%s exists and is not a directory", name)
		}
		err = swap(parent, staging, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		_ = removeTree(parent, staging)
		return nil
	}
	return fmt.Errorf("%s changed during each of %d attempts to install it", name, installAttempts)
}

// swap exchanges the entries from and to of dir atomically.
func swap(dir *os.Root, from, to string) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return exchange(f, from, to)
}

// removeTree removes name from dir and everything beneath it, never
// following a symlink. It first gives the owner read, write and search
// permission on each directory before descending into it, so a payload
// directory of mode 0o555 does not stop a caller that is not root.
func removeTree(dir *os.Root, name string) error {
	var walkErr error
	if info, err := dir.Lstat(name); err == nil && info.IsDir() {
		walkErr = fs.WalkDir(dir.FS(), name, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			info, err := d.Info()
			if err != nil || info.Mode().Perm()&0o700 == 0o700 {
				return err
			}
			return dir.Chmod(path, info.Mode().Perm()|0o700)
		})
	}
	return errors.Join(walkErr, dir.RemoveAll(name))
}
