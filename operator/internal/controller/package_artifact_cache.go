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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nodewright/artifact"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// packageArtifactCacheDigest is a digest the cache keys a file by: sha256 and 64 lowercase
// hexadecimal characters, so that it names a file in the cache's directory and nothing else.
var packageArtifactCacheDigest = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)

// packageArtifactCacheFile is the name of the file of an entry, which the file a write creates
// before it renames it into place (packageArtifactCacheTemp) never matches.
var packageArtifactCacheFile = regexp.MustCompile(`^([0-9a-f]{64})\.json$`)

// packageArtifactCacheTemp prefixes the file a write creates before it renames it into place.
const packageArtifactCacheTemp = ".entry-"

// packageArtifactCacheEntry is what the operator found at one manifest digest. A digest's content
// never changes, so an entry never goes stale.
type packageArtifactCacheEntry struct {
	Kind      packageArtifactKind `json:"kind"`
	Uninstall artifact.Uninstall  `json:"uninstall,omitempty"`
	Reason    string              `json:"reason,omitempty"`
	// Repository is the registry/repository the digest was inspected at, which keeps the entry
	// while a tag of that repository has not been resolved since the operator started (prune).
	Repository  string    `json:"repository,omitempty"`
	InspectedAt time.Time `json:"inspectedAt"`
}

// packageArtifactCache keeps what the operator found at each manifest digest, one file per digest,
// in a directory the deployment mounts from a memory-backed emptyDir. It outlives a container
// restart but not the pod; an empty one is filled again by inspecting. It holds only what a digest
// is, an artifact, an image or a rejected artifact, never a reference that could not be read.
type packageArtifactCache struct {
	dir string
	// mu serializes writes, so emptying a full cache never removes the file of a write in flight.
	// Reads take no lock: an entry is renamed into place whole.
	mu sync.Mutex
	// createTemp creates the file an entry is written in before it is renamed into place; a test
	// seam, os.CreateTemp.
	createTemp func(dir, pattern string) (*os.File, error)
}

// newPackageArtifactCache returns the cache in dir, which it creates, and fails unless it can write
// there.
func newPackageArtifactCache(dir string) (*packageArtifactCache, error) {
	if dir == "" {
		return nil, errors.New("the package artifact cache directory is not set")
	}
	files := filepath.Join(dir, "sha256")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return nil, fmt.Errorf("creating the package artifact cache directory %s: %w", files, err)
	}
	probe, err := os.CreateTemp(files, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("writing in the package artifact cache directory %s: %w", files, err)
	}
	closeErr := probe.Close()
	removeErr := os.Remove(probe.Name())
	if err := errors.Join(closeErr, removeErr); err != nil {
		return nil, fmt.Errorf("writing in the package artifact cache directory %s: %w", files, err)
	}
	return &packageArtifactCache{dir: dir, createTemp: os.CreateTemp}, nil
}

func (c *packageArtifactCache) path(digest string) (string, error) {
	match := packageArtifactCacheDigest.FindStringSubmatch(digest)
	if match == nil {
		return "", fmt.Errorf("digest %q is not sha256 followed by 64 lowercase hexadecimal characters", digest)
	}
	return filepath.Join(c.dir, "sha256", match[1]+".json"), nil
}

func cacheableKind(kind packageArtifactKind) bool {
	return kind == packageArtifactAccepted || kind == packageArtifactImage || kind == packageArtifactRejected
}

// get returns the entry of digest, and false when the cache has none.
func (c *packageArtifactCache) get(digest string) (packageArtifactCacheEntry, bool, error) {
	path, err := c.path(digest)
	if err != nil {
		return packageArtifactCacheEntry{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return packageArtifactCacheEntry{}, false, nil
	}
	if err != nil {
		return packageArtifactCacheEntry{}, false, fmt.Errorf("reading the package artifact cache entry of %s: %w", digest, err)
	}
	var entry packageArtifactCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return packageArtifactCacheEntry{}, false, fmt.Errorf("decoding the package artifact cache entry of %s: %w", digest, err)
	}
	if !cacheableKind(entry.Kind) {
		return packageArtifactCacheEntry{}, false, fmt.Errorf("the package artifact cache entry of %s has kind %q", digest, entry.Kind)
	}
	return entry, true, nil
}

// put records entry for digest. The file is written beside its final name and renamed into place,
// so a reader never sees half of one. A write that fails, as every one does once the memory-backed
// volume is full, empties the cache, whose every entry an inspection finds again, and is tried once
// more.
func (c *packageArtifactCache) put(ctx context.Context, digest string, entry packageArtifactCacheEntry) error {
	if !cacheableKind(entry.Kind) {
		return fmt.Errorf("a package artifact cache entry cannot have kind %q", entry.Kind)
	}
	path, err := c.path(digest)
	if err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encoding the package artifact cache entry of %s: %w", digest, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	err = c.write(path, data)
	if err == nil {
		return nil
	}
	log.FromContext(ctx).Info("emptying the package artifact cache, which could not be written", "digest", digest, "error", err.Error())
	if clearErr := c.clear(); clearErr != nil {
		return fmt.Errorf("writing the package artifact cache entry of %s: %w", digest, errors.Join(err, clearErr))
	}
	if err := c.write(path, data); err != nil {
		return fmt.Errorf("writing the package artifact cache entry of %s: %w", digest, err)
	}
	return nil
}

// write writes data to a file beside path and renames it into place.
func (c *packageArtifactCache) write(path string, data []byte) (err error) {
	file, err := c.createTemp(filepath.Dir(path), packageArtifactCacheTemp+"*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// clear removes every entry of the cache, and any file a failed write left.
func (c *packageArtifactCache) clear() error {
	files := filepath.Join(c.dir, "sha256")
	entries, err := os.ReadDir(files)
	if err != nil {
		return fmt.Errorf("emptying the package artifact cache directory %s: %w", files, err)
	}
	var errs []error
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(files, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("emptying the package artifact cache directory %s: %w", files, err)
	}
	return nil
}

// prune removes every entry whose digest is not in digests, unless its repository is in
// repositories, and returns how many it removed. It reads an entry only to find its repository,
// when repositories is not empty. It holds the lock of a write, so no write is in flight, and any
// file a write creates before its rename is left by one the process never finished, as when it was
// killed between the two: prune removes it too, since the volume outlives a container restart.
func (c *packageArtifactCache) prune(digests, repositories map[string]bool) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	files := filepath.Join(c.dir, "sha256")
	entries, err := os.ReadDir(files)
	if err != nil {
		return 0, fmt.Errorf("pruning the package artifact cache directory %s: %w", files, err)
	}
	removed := 0
	var errs []error
	for _, file := range entries {
		match := packageArtifactCacheFile.FindStringSubmatch(file.Name())
		if match == nil {
			if strings.HasPrefix(file.Name(), packageArtifactCacheTemp) {
				if err := os.Remove(filepath.Join(files, file.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
			}
			continue
		}
		digest := "sha256:" + match[1]
		if digests[digest] {
			continue
		}
		if len(repositories) > 0 {
			if entry, ok, err := c.get(digest); err == nil && ok && repositories[entry.Repository] {
				continue
			}
		}
		if err := os.Remove(filepath.Join(files, file.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	if err := errors.Join(errs...); err != nil {
		return removed, fmt.Errorf("pruning the package artifact cache directory %s: %w", files, err)
	}
	return removed, nil
}
