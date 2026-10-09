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
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// metadataSchemaJSON carries every metadata rule; ParseMetadata's typed decode
// adds none of its own.
//
//go:embed schemas/v1/metadata-schema.json
var metadataSchemaJSON []byte

// metadataSchemaURL is a synthetic absolute URL the embedded schema compiles
// under, so the compiler never resolves it against the filesystem.
const metadataSchemaURL = "nodewright://schemas/v1/metadata-schema.json"

// Uninstall is whether a package supports being uninstalled.
type Uninstall string

const (
	UninstallSupported   Uninstall = "supported"
	UninstallUnsupported Uninstall = "unsupported"
)

// InterruptStep is one executable of an interrupt declaration. Path is
// relative to skyhook_dir, like the step paths in config.json.
type InterruptStep struct {
	Path      string   `json:"path"`
	Arguments []string `json:"arguments"`
	OnHost    bool     `json:"on_host"`
}

// Interrupt declares how a package senses that it needs an interrupt and how
// it runs one.
type Interrupt struct {
	Sense InterruptStep `json:"sense"`
	Run   InterruptStep `json:"run"`
}

// HostFileLayer is a host-files layer and the absolute host path it unpacks to.
type HostFileLayer struct {
	Descriptor  ocispec.Descriptor
	Destination string
}

// Package is a v1 package artifact: its validated manifest and parsed
// metadata. Payload and HostFiles each keep manifest order.
type Package struct {
	Uninstall Uninstall
	// Config is the package's raw config JSON, which the agent validates as
	// config.json.
	Config    json.RawMessage
	Payload   []ocispec.Descriptor
	HostFiles []HostFileLayer
	// Interrupt is nil when the package declares none.
	Interrupt        *Interrupt
	ConfigDescriptor ocispec.Descriptor
	// Annotations are the manifest's annotations. They are informational and
	// never consulted.
	Annotations map[string]string
}

type metadata struct {
	Uninstall Uninstall       `json:"uninstall"`
	Config    json.RawMessage `json:"config"`
	Interrupt *Interrupt      `json:"interrupt"`
}

// ParseMetadata checks blob against the manifest's config descriptor and the
// metadata schema, and returns the package it describes.
func (m *Manifest) ParseMetadata(blob []byte) (*Package, error) {
	desc := m.manifest.Config
	if len(blob) > MaxMetadataSize {
		return nil, invalid(ReasonMetadataTooLarge, "metadata blob of %d bytes exceeds the %d byte limit", len(blob), MaxMetadataSize)
	}
	if int64(len(blob)) != desc.Size || desc.Digest.Algorithm().FromBytes(blob) != desc.Digest {
		return nil, invalid(ReasonMetadataMismatch, "metadata blob does not match descriptor %s (%d bytes)", desc.Digest, desc.Size)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(blob))
	if err != nil {
		return nil, invalid(ReasonMalformedMetadata, "%v", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, invalid(ReasonMalformedMetadata, "metadata is not a JSON object")
	}
	schema, err := compileMetadataSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, invalid(ReasonMetadataSchema, "%v", err)
	}

	var md metadata
	dec := json.NewDecoder(bytes.NewReader(blob))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&md); err != nil {
		return nil, invalid(ReasonMetadataSchema, "%v", err)
	}

	pkg := &Package{
		Uninstall:        md.Uninstall,
		Config:           md.Config,
		Interrupt:        md.Interrupt,
		ConfigDescriptor: desc,
		Annotations:      m.manifest.Annotations,
	}
	for _, l := range m.manifest.Layers {
		switch l.MediaType {
		case MediaTypePayload:
			pkg.Payload = append(pkg.Payload, l)
		case MediaTypeHostFiles:
			pkg.HostFiles = append(pkg.HostFiles, HostFileLayer{Descriptor: l, Destination: l.Annotations[AnnotationDestination]})
		}
	}
	return pkg, nil
}

// compileMetadataSchema compiles the embedded schema on demand. It is small
// and parsed once per package, so there is no package-global cache.
func compileMetadataSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(metadataSchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("parsing embedded metadata schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(metadataSchemaURL, doc); err != nil {
		return nil, fmt.Errorf("adding embedded metadata schema: %w", err)
	}
	schema, err := c.Compile(metadataSchemaURL)
	if err != nil {
		return nil, fmt.Errorf("compiling embedded metadata schema: %w", err)
	}
	return schema, nil
}
