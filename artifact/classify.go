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

package artifact

import (
	// go-digest validates a digest only when its hash is linked into the
	// binary, and it does not import any hash itself. Linking them here keeps
	// the descriptor rule independent of what else the caller imports.
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/json"
	"fmt"
	"path"
	"slices"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// Kind is what Classify found a manifest to be.
type Kind string

const (
	KindPackage Kind = "package"
	KindLegacy  Kind = "legacy"
)

// Manifest is a manifest that passed Classify as a v1 package artifact. Only
// Classify constructs one, so metadata is only ever parsed against a manifest
// whose descriptors were already validated.
type Manifest struct {
	manifest ocispec.Manifest
}

// Config returns the descriptor of the metadata blob.
func (m *Manifest) Config() ocispec.Descriptor { return m.manifest.Config }

// Classify decides whether manifest is a v1 package artifact or a legacy
// image. mediaType is the manifest's media type as the registry reported it;
// when empty, the document's own mediaType field is used. A package comes back
// with its validated Manifest; a legacy image comes back with nil.
func Classify(mediaType string, manifest []byte) (Kind, *Manifest, error) {
	if len(manifest) > MaxManifestSize {
		return "", nil, fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrMalformedManifest, len(manifest), MaxManifestSize)
	}
	// An index or manifest list shares the mediaType and artifactType fields
	// with a manifest, and those are all Classify reads from one, so a single
	// decode serves every form.
	var doc *ocispec.Manifest
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrMalformedManifest, err)
	}
	if doc == nil {
		return "", nil, fmt.Errorf("%w: document is null", ErrMalformedManifest)
	}
	switch {
	case mediaType == "":
		mediaType = doc.MediaType
	case doc.MediaType != "" && doc.MediaType != mediaType:
		return "", nil, fmt.Errorf("%w: media type %q disagrees with document media type %q", ErrMalformedManifest, mediaType, doc.MediaType)
	}

	switch mediaType {
	case ocispec.MediaTypeImageIndex, mediaTypeDockerManifestList:
		if inFamily(doc.ArtifactType) {
			return "", nil, fmt.Errorf("%w: package artifact type %q on an index", ErrUnsupportedForm, doc.ArtifactType)
		}
		return KindLegacy, nil, nil
	case ocispec.MediaTypeImageManifest, mediaTypeDockerManifest:
	default:
		return "", nil, fmt.Errorf("%w: manifest media type %q", ErrUnsupportedForm, mediaType)
	}

	types := familyCandidates(doc)
	if !slices.ContainsFunc(types, inFamily) {
		return KindLegacy, nil, nil
	}
	if i := slices.IndexFunc(types, unsupportedVersion); i >= 0 {
		return "", nil, fmt.Errorf("%w: %q", ErrUnsupportedVersion, types[i])
	}
	if mediaType == mediaTypeDockerManifest {
		return "", nil, fmt.Errorf("%w: package artifact in a Docker schema 2 manifest", ErrUnsupportedForm)
	}
	if err := validateManifest(doc); err != nil {
		return "", nil, err
	}
	return KindPackage, &Manifest{manifest: *doc}, nil
}

// familyCandidates lists the media types that can make a manifest a package:
// its artifactType, its config's and every layer's. Layers count because
// `oras push` without --config writes the empty config over our layer types,
// and that must not pass as a legacy image.
func familyCandidates(m *ocispec.Manifest) []string {
	types := make([]string, 0, 2+len(m.Layers))
	types = append(types, m.ArtifactType, m.Config.MediaType)
	for _, l := range m.Layers {
		types = append(types, l.MediaType)
	}
	return types
}

// validateManifest applies the v1 rules in reason order, each across the whole
// manifest, so the reported reason does not depend on layer order.
func validateManifest(m *ocispec.Manifest) error {
	if m.Config.MediaType != MediaTypeMetadata {
		return invalid(ReasonMissingMetadata, "config media type %q is not %q", m.Config.MediaType, MediaTypeMetadata)
	}
	if m.ArtifactType != "" && m.ArtifactType != ArtifactTypePackage {
		return invalid(ReasonArtifactType, "artifact type %q is not %q", m.ArtifactType, ArtifactTypePackage)
	}
	for i, l := range m.Layers {
		if l.MediaType != MediaTypePayload && l.MediaType != MediaTypeHostFiles {
			return invalid(ReasonMediaType, "layer %d media type %q", i, l.MediaType)
		}
	}
	for _, d := range append([]ocispec.Descriptor{m.Config}, m.Layers...) {
		if err := d.Digest.Validate(); err != nil {
			return invalid(ReasonDescriptor, "%s digest %q: %v", d.MediaType, d.Digest, err)
		}
		if d.Size <= 0 {
			return invalid(ReasonDescriptor, "%s size %d is not positive", d.MediaType, d.Size)
		}
	}
	if m.Config.Size > MaxMetadataSize {
		return invalid(ReasonMetadataTooLarge, "metadata size %d exceeds the %d byte limit", m.Config.Size, MaxMetadataSize)
	}
	if !slices.ContainsFunc(m.Layers, func(l ocispec.Descriptor) bool { return l.MediaType == MediaTypePayload }) {
		return invalid(ReasonNoPayload, "no %s layer", MediaTypePayload)
	}
	for i, l := range m.Layers {
		dest, ok := l.Annotations[AnnotationDestination]
		switch {
		case l.MediaType == MediaTypePayload && ok:
			return invalid(ReasonDestination, "payload layer %d carries %s", i, AnnotationDestination)
		case l.MediaType == MediaTypeHostFiles && !ok:
			return invalid(ReasonDestination, "host-files layer %d has no %s", i, AnnotationDestination)
		case l.MediaType == MediaTypeHostFiles && !validDestination(dest):
			return invalid(ReasonDestination, "host-files layer %d destination %q", i, dest)
		}
	}
	return nil
}

// validDestination accepts a clean absolute path other than "/". path.Clean
// removes every ".." from a rooted path, so clean-equality also rules out a
// ".." segment.
func validDestination(dest string) bool {
	return path.IsAbs(dest) && dest != "/" && path.Clean(dest) == dest
}
