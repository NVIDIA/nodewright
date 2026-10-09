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
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/NVIDIA/nodewright/artifact"
)

// ExtractLayer lays out the payload or host-files layer desc of t in dir,
// streaming it without holding it in memory. Every filesystem operation is
// relative to dir, and no symlink beneath dir is ever followed: an entry
// whose path passes through a symlink or any other non-directory, whoever
// laid it down, fails, and an existing symlink at the path of an entry is
// replaced. A later entry replaces whatever is at its path, except that a
// directory entry keeps an existing directory and its contents. Symlinks get
// their target verbatim. Regular files and directories get exactly the
// permission bits of their entry, whatever the umask and without the setuid,
// setgid and sticky bits; directories once every entry of the layer is
// written, so a read-only directory still receives its children. Parents
// the layer does not name are created 0o755. The mode of the root entry,
// ownership and modification times are ignored.
//
// A layer whose bytes do not match desc fails with ErrDigestMismatch, even
// when it is also malformed. A layer that matches desc but is not a
// well-formed gzip-compressed tar, or has an entry that breaks a tar rule or
// cannot be laid out, fails as an artifact.InvalidArtifactError with
// artifact.ReasonLayerEntry, and a desc of any other media type with
// artifact.ReasonMediaType, before any request. A done context stops
// extraction and its error is returned.
//
// ExtractLayer is not atomic and does no cleanup, so a failure can leave
// part of the layer in dir. Callers that need atomicity extract into a
// staging directory, as Layout does. A follow-up will reuse it to place
// host-files layers beneath their destination.
func ExtractLayer(ctx context.Context, t *Target, desc ocispec.Descriptor, dir *os.Root) error {
	if desc.MediaType != artifact.MediaTypePayload && desc.MediaType != artifact.MediaTypeHostFiles {
		return &artifact.InvalidArtifactError{
			Reason: artifact.ReasonMediaType,
			Detail: fmt.Sprintf("layer %s has media type %q", desc.Digest, desc.MediaType),
		}
	}
	blob, err := t.fetch(ctx, desc)
	if err != nil {
		return err
	}
	defer func() { _ = blob.Close() }()

	err = extractTar(ctx, blob, dir)
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		return fmt.Errorf("extracting layer %s: %w", desc.Digest, ctxErr)
	}
	// why: integrity takes precedence. Tampered bytes can break the gzip or
	// tar format, or an entry, before the digest is checked at the end of
	// the blob, so the rest of it is read and verified before any other
	// error is reported. A blob read that already failed fails drain too.
	if drainErr := blob.drain(); drainErr != nil {
		return drainErr
	}
	if err != nil {
		return fmt.Errorf("extracting layer %s: %w", desc.Digest, err)
	}
	return nil
}

// extractTar lays out the gzip-compressed tar r in dir, then reads the gzip
// stream to its end, which checks its checksum.
func extractTar(ctx context.Context, r io.Reader, dir *os.Root) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return extractNotTar(err)
	}
	x := &extractor{ctx: ctx, dir: dir, tr: tar.NewReader(zr), buf: make([]byte, 32<<10), dirModes: map[string]fs.FileMode{}}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := x.tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return extractNotTar(err)
		}
		if err := x.entry(hdr); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return extractNotTar(err)
	}
	return x.chmodDirs()
}

// extractNotTar reports an error reading the gzip or tar stream as a layer
// that is not a well-formed gzip-compressed tar. ExtractLayer returns it only
// once the blob has verified, when the error can only be in the format.
func extractNotTar(err error) error {
	return &artifact.InvalidArtifactError{
		Reason: artifact.ReasonLayerEntry,
		Detail: fmt.Sprintf("not a gzip-compressed tar: %v", err),
	}
}

// extractor lays out the entries of one layer. dirModes holds the mode of
// each directory entry, set once every entry is written.
type extractor struct {
	ctx      context.Context
	dir      *os.Root
	tr       *tar.Reader
	buf      []byte
	dirModes map[string]fs.FileMode
}

func (x *extractor) entry(hdr *tar.Header) error {
	name, err := artifact.TarEntryName(hdr)
	if err != nil {
		return err
	}
	if name == "." {
		// The directory the layer is laid out in keeps its own mode.
		return nil
	}
	if err := x.parents(name); err != nil {
		return err
	}
	mode := fs.FileMode(hdr.Mode & 0o777)
	existing, err := x.dir.Lstat(name)
	switch {
	case err == nil && existing.IsDir() && hdr.Typeflag == tar.TypeDir:
		x.dirModes[name] = mode
		return nil
	case err == nil:
		// RemoveAll unlinks a symlink rather than following it.
		if err := x.dir.RemoveAll(name); err != nil {
			return err
		}
		if existing.IsDir() {
			maps.DeleteFunc(x.dirModes, func(dir string, _ fs.FileMode) bool {
				return dir == name || strings.HasPrefix(dir, name+"/")
			})
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		x.dirModes[name] = mode
		return x.mkdir(name)
	case tar.TypeSymlink:
		return x.dir.Symlink(hdr.Linkname, name)
	}
	return x.file(name, mode)
}

// parents creates the missing parent directories of name. A parent that is
// a symlink or any other non-directory fails, so none is ever followed.
func (x *extractor) parents(name string) error {
	for i, c := range name {
		if c != '/' {
			continue
		}
		parent := name[:i]
		info, err := x.dir.Lstat(parent)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			err = x.mkdir(parent)
		case err == nil && !info.IsDir():
			err = &artifact.InvalidArtifactError{
				Reason: artifact.ReasonLayerEntry,
				Detail: fmt.Sprintf("entry %q passes through %q, which is not a directory", name, parent),
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// mkdir creates the directory name with mode 0o755, whatever the umask.
func (x *extractor) mkdir(name string) error {
	if err := x.dir.Mkdir(name, 0o755); err != nil {
		return err
	}
	return x.dir.Chmod(name, 0o755)
}

// file writes the regular file name from the current entry.
func (x *extractor) file(name string, mode fs.FileMode) (err error) {
	// O_EXCL fails rather than follow a symlink at name.
	f, err := x.dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		n, readErr := x.tr.Read(x.buf)
		if _, err := f.Write(x.buf[:n]); err != nil {
			return err
		}
		switch {
		case errors.Is(readErr, io.EOF):
			return nil
		case readErr != nil:
			return extractNotTar(readErr)
		}
	}
}

// chmodDirs sets the mode of each directory entry. A directory sorts after
// its parent, so in reverse order a parent that loses search permission does
// so only after its children have their modes.
func (x *extractor) chmodDirs() error {
	for _, name := range slices.Backward(slices.Sorted(maps.Keys(x.dirModes))) {
		if err := x.dir.Chmod(name, x.dirModes[name]); err != nil {
			return err
		}
	}
	return nil
}
