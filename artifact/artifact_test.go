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
	"encoding/json"
	"errors"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestArtifact(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Artifact Suite")
}

// expectOutcome asserts err against want: nil for success, a sentinel for
// errors.Is, or a Reason for an InvalidArtifactError.
func expectOutcome(err error, want any) {
	GinkgoHelper()
	switch want := want.(type) {
	case nil:
		Expect(err).NotTo(HaveOccurred())
	case Reason:
		var invalidErr *InvalidArtifactError
		Expect(errors.As(err, &invalidErr)).To(BeTrue(), "want reason %s, got %v", want, err)
		Expect(errors.Is(err, ErrInvalidArtifact)).To(BeTrue())
		Expect(invalidErr.Reason).To(Equal(want))
	case error:
		Expect(errors.Is(err, want)).To(BeTrue(), "want %v, got %v", want, err)
	default:
		Fail("unexpected outcome type")
	}
}

var validMetadata = []byte(`{"uninstall":"supported","config":{}}`)

func blobDescriptor(mediaType string, blob []byte) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(blob), Size: int64(len(blob))}
}

// layer builds a layer descriptor over content, with annotations given as
// key, value pairs.
func layer(mediaType, content string, annotations ...string) ocispec.Descriptor {
	d := blobDescriptor(mediaType, []byte(content))
	if len(annotations) > 0 {
		d.Annotations = map[string]string{}
		for i := 0; i < len(annotations); i += 2 {
			d.Annotations[annotations[i]] = annotations[i+1]
		}
	}
	return d
}

func payloadLayer(content string) ocispec.Descriptor { return layer(MediaTypePayload, content) }

func hostFilesLayer(content, destination string) ocispec.Descriptor {
	return layer(MediaTypeHostFiles, content, AnnotationDestination, destination)
}

// pkg builds a valid v1 package manifest and applies edits to it.
func pkg(edits ...func(*ocispec.Manifest)) ocispec.Manifest {
	m := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePackage,
		Config:       blobDescriptor(MediaTypeMetadata, validMetadata),
		Layers:       []ocispec.Descriptor{payloadLayer("payload")},
	}
	for _, edit := range edits {
		edit(&m)
	}
	return m
}

func withMediaType(mediaType string) func(*ocispec.Manifest) {
	return func(m *ocispec.Manifest) { m.MediaType = mediaType }
}

func withArtifactType(artifactType string) func(*ocispec.Manifest) {
	return func(m *ocispec.Manifest) { m.ArtifactType = artifactType }
}

func withConfig(config ocispec.Descriptor) func(*ocispec.Manifest) {
	return func(m *ocispec.Manifest) { m.Config = config }
}

func withLayers(layers ...ocispec.Descriptor) func(*ocispec.Manifest) {
	return func(m *ocispec.Manifest) { m.Layers = layers }
}

// mustJSON panics rather than calling Expect because table entries are built
// while the spec tree is constructed, outside any running spec.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
