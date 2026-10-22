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

package registrytest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/NVIDIA/nodewright/artifact"
)

// Layer is the content of one layer: exactly one of Dir, a directory packed
// with TarGzip, or Gzip, raw bytes pushed as they are so tests can push
// crafted tars.
type Layer struct {
	Dir  string
	Gzip []byte
}

// HostFilesLayer is a host-files layer and its destination annotation.
type HostFilesLayer struct {
	Layer       Layer
	Destination string
}

// PackageSpec describes a v1 package for PushPackage. Config, when set, is the
// metadata config; otherwise it is the config.json of the first payload layer
// that is a directory.
type PackageSpec struct {
	Uninstall artifact.Uninstall
	Interrupt *artifact.Interrupt
	Config    json.RawMessage
	Payload   []Layer
	HostFiles []HostFilesLayer
}

// PushBlob pushes data unless p already has it and returns its descriptor.
func PushBlob(ctx context.Context, p Pusher, mediaType string, data []byte) (ocispec.Descriptor, error) {
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	exists, err := p.Exists(ctx, desc)
	if err == nil && !exists {
		err = p.Push(ctx, desc, bytes.NewReader(data))
	}
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

// PushManifest pushes data as a manifest of mediaType, without validating it,
// and tags it unless tag is empty.
func PushManifest(ctx context.Context, p Pusher, mediaType string, data []byte, tag string) (ocispec.Descriptor, error) {
	desc, err := PushBlob(ctx, p, mediaType, data)
	if err == nil && tag != "" {
		err = p.Tag(ctx, desc, tag)
	}
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

// PushPackage pushes a v1 package and tags its manifest. The manifest sets
// artifactType and lists the payload layers in the order given, then the
// host-files layers.
func PushPackage(ctx context.Context, p Pusher, tag string, spec PackageSpec) (ocispec.Descriptor, ocispec.Manifest, error) {
	config := spec.Config
	if config == nil {
		i := slices.IndexFunc(spec.Payload, func(l Layer) bool { return l.Dir != "" })
		if i < 0 {
			return ocispec.Descriptor{}, ocispec.Manifest{}, errors.New("registrytest: package has no Config and no directory payload layer")
		}
		var err error
		if config, err = os.ReadFile(filepath.Join(spec.Payload[i].Dir, "config.json")); err != nil {
			return ocispec.Descriptor{}, ocispec.Manifest{}, err
		}
	}

	layers := make([]ocispec.Descriptor, 0, len(spec.Payload)+len(spec.HostFiles))
	for _, l := range spec.Payload {
		desc, err := pushLayer(ctx, p, artifact.MediaTypePayload, l)
		if err != nil {
			return ocispec.Descriptor{}, ocispec.Manifest{}, err
		}
		layers = append(layers, desc)
	}
	for _, h := range spec.HostFiles {
		desc, err := pushLayer(ctx, p, artifact.MediaTypeHostFiles, h.Layer)
		if err != nil {
			return ocispec.Descriptor{}, ocispec.Manifest{}, err
		}
		desc.Annotations = map[string]string{artifact.AnnotationDestination: h.Destination}
		layers = append(layers, desc)
	}

	metadata, err := json.Marshal(struct {
		Uninstall artifact.Uninstall  `json:"uninstall"`
		Config    json.RawMessage     `json:"config"`
		Interrupt *artifact.Interrupt `json:"interrupt,omitempty"`
	}{spec.Uninstall, config, spec.Interrupt})
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, err
	}
	configDesc, err := PushBlob(ctx, p, artifact.MediaTypeMetadata, metadata)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, err
	}
	m := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: artifact.ArtifactTypePackage,
		Config:       configDesc,
		Layers:       layers,
	}
	desc, err := pushJSON(ctx, p, m.MediaType, m, tag)
	if err != nil {
		return ocispec.Descriptor{}, ocispec.Manifest{}, err
	}
	return desc, m, nil
}

// PushImage pushes a legacy single-platform linux/amd64 OCI image whose one
// tar+gzip layer is dir, and tags it.
func PushImage(ctx context.Context, p Pusher, tag, dir string) (ocispec.Descriptor, error) {
	layer, diffID, err := tarGzip(dir)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return pushImage(ctx, p, tag, "amd64", layer, diffID)
}

// PushIndex pushes an OCI index over linux/amd64 and linux/arm64 images of
// dir, which share their layer, and tags it.
func PushIndex(ctx context.Context, p Pusher, tag, dir string) (ocispec.Descriptor, error) {
	layer, diffID, err := tarGzip(dir)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	index := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex}
	for _, arch := range []string{"amd64", "arm64"} {
		desc, err := pushImage(ctx, p, "", arch, layer, diffID)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		desc.Platform = &ocispec.Platform{OS: "linux", Architecture: arch}
		index.Manifests = append(index.Manifests, desc)
	}
	return pushJSON(ctx, p, index.MediaType, index, tag)
}

func pushImage(ctx context.Context, p Pusher, tag, arch string, layer []byte, diffID digest.Digest) (ocispec.Descriptor, error) {
	layerDesc, err := PushBlob(ctx, p, ocispec.MediaTypeImageLayerGzip, layer)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	config, err := json.Marshal(ocispec.Image{
		Platform: ocispec.Platform{OS: "linux", Architecture: arch},
		RootFS:   ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{diffID}},
	})
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	configDesc, err := PushBlob(ctx, p, ocispec.MediaTypeImageConfig, config)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return pushJSON(ctx, p, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	}, tag)
}

func pushJSON(ctx context.Context, p Pusher, mediaType string, v any, tag string) (ocispec.Descriptor, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return PushManifest(ctx, p, mediaType, data, tag)
}

func pushLayer(ctx context.Context, p Pusher, mediaType string, l Layer) (ocispec.Descriptor, error) {
	if (l.Dir != "") == (l.Gzip != nil) {
		return ocispec.Descriptor{}, errors.New("registrytest: a layer sets exactly one of Dir and Gzip")
	}
	data := l.Gzip
	if l.Dir != "" {
		var err error
		if data, err = TarGzip(l.Dir); err != nil {
			return ocispec.Descriptor{}, err
		}
	}
	return PushBlob(ctx, p, mediaType, data)
}

// TarGzip returns the tar+gzip layer of dir. Entries are in lexical order with
// plain relative names, zero mtimes and no uid, gid or owner names, and keep
// the permission bits and symlinks on disk, so one tree always gives one
// digest.
func TarGzip(dir string) ([]byte, error) {
	data, _, err := tarGzip(dir)
	return data, err
}

// tarGzip also returns the digest of the uncompressed tar, an image's diff ID.
func tarGzip(dir string) ([]byte, digest.Digest, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	diffID := digest.Canonical.Digester()
	tw := tar.NewWriter(io.MultiWriter(zw, diffID.Hash()))
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(info.Mode().Perm())}
		var data []byte
		switch {
		case info.IsDir():
			hdr.Typeflag, hdr.Name = tar.TypeDir, hdr.Name+"/"
		case info.Mode()&fs.ModeSymlink != 0:
			hdr.Typeflag = tar.TypeSymlink
			if hdr.Linkname, err = os.Readlink(path); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
			hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(data))
		default:
			return fmt.Errorf("registrytest: %s has unsupported file type %s", path, info.Mode().Type())
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), diffID.Digest(), nil
}
