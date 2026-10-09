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

// Package artifact defines the v1 NodeWright package artifact: the media types
// that identify it, the rules a manifest must satisfy to be one, its metadata
// document, and the entries its layers may carry. It is a pure library: it
// reads bytes and descriptors handed to it and never touches a registry, the
// network or the filesystem.
package artifact

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ArtifactTypePackage   = "application/vnd.nvidia.nodewright.package.v1"
	MediaTypeMetadata     = "application/vnd.nvidia.nodewright.package.config.v1+json"
	MediaTypePayload      = "application/vnd.nvidia.nodewright.package.payload.v1.tar+gzip"
	MediaTypeHostFiles    = "application/vnd.nvidia.nodewright.package.hostfiles.v1.tar+gzip"
	AnnotationDestination = "com.nvidia.nodewright.package.destination"
)

// Both bounds are inclusive. MaxManifestSize is the default manifest read
// limit of oras-go and the size registries must accept.
const (
	MaxManifestSize = 4 << 20
	MaxMetadataSize = 1 << 20
)

const (
	familyPrefix     = "application/vnd.nvidia.nodewright.package."
	supportedVersion = "v1"
)

// inFamily reports whether mediaType belongs to the package media type family.
func inFamily(mediaType string) bool {
	return strings.HasPrefix(mediaType, familyPrefix)
}

// familyVersion returns the first dot-separated segment after the family
// prefix that is "v" followed by digits, ignoring any +suffix, or "" when the
// type carries no version.
func familyVersion(mediaType string) string {
	rest, _, _ := strings.Cut(strings.TrimPrefix(mediaType, familyPrefix), "+")
	for seg := range strings.SplitSeq(rest, ".") {
		if len(seg) > 1 && seg[0] == 'v' && strings.Trim(seg[1:], "0123456789") == "" {
			return seg
		}
	}
	return ""
}

// unsupportedVersion reports whether mediaType is a family type of a version
// other than v1.
func unsupportedVersion(mediaType string) bool {
	if !inFamily(mediaType) {
		return false
	}
	v := familyVersion(mediaType)
	return v != "" && v != supportedVersion
}

var (
	ErrUnsupportedForm    = errors.New("unsupported artifact form")
	ErrUnsupportedVersion = errors.New("unsupported package artifact version")
	ErrMalformedManifest  = errors.New("malformed manifest")
	ErrInvalidArtifact    = errors.New("invalid package artifact")
)

// Reason names the rule an invalid package artifact broke.
type Reason string

const (
	ReasonMissingMetadata   Reason = "missing-metadata"
	ReasonArtifactType      Reason = "artifact-type"
	ReasonMediaType         Reason = "media-type"
	ReasonDescriptor        Reason = "descriptor"
	ReasonMetadataTooLarge  Reason = "metadata-too-large"
	ReasonNoPayload         Reason = "no-payload"
	ReasonDestination       Reason = "destination"
	ReasonMetadataMismatch  Reason = "metadata-mismatch"
	ReasonMalformedMetadata Reason = "malformed-metadata"
	ReasonMetadataSchema    Reason = "metadata-schema"
	ReasonLayerEntry        Reason = "layer-entry"
)

// InvalidArtifactError is a package artifact that breaks a v1 rule. It
// matches ErrInvalidArtifact under errors.Is.
type InvalidArtifactError struct {
	Reason Reason
	Detail string
}

func (e *InvalidArtifactError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrInvalidArtifact, e.Reason, e.Detail)
}

func (e *InvalidArtifactError) Unwrap() error { return ErrInvalidArtifact }

func invalid(reason Reason, format string, args ...any) error {
	return &InvalidArtifactError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}
