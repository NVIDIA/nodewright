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
	"context"
	"errors"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/NVIDIA/nodewright/artifact"
)

// Inspection is what Inspect found at a target. Everything in it rests on
// manifest and metadata bytes checked against the resolved descriptor.
type Inspection struct {
	Kind artifact.Kind
	// Descriptor is the resolved manifest descriptor: its media type, digest
	// and size.
	Descriptor ocispec.Descriptor
	// Pinned is the target's repository name, @ and the resolved digest, in
	// the form ParsePinned accepts.
	Pinned string
	// Package is nil for a legacy image or index.
	Package *artifact.Package

	// target is kept so the package's layers are later read from where its
	// manifest was.
	target *Target
}

// Inspect resolves the reference of t, reads and verifies its manifest and
// classifies it. For a package it also reads and parses the metadata blob,
// and it never fetches a layer. It exposes an interrupt declaration and
// host-files layers rather than refusing them; whether to refuse is the
// caller's decision.
//
// An error is one of the Target sentinels, artifact.ErrUnsupportedForm,
// artifact.ErrUnsupportedVersion, artifact.ErrMalformedManifest,
// artifact.ErrInvalidArtifact, or the context error. A metadata blob that
// does not match its descriptor matches both ErrDigestMismatch and an
// *artifact.InvalidArtifactError with artifact.ReasonMetadataMismatch.
func Inspect(ctx context.Context, t *Target) (*Inspection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	desc, err := t.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if desc.Size > artifact.MaxManifestSize {
		return nil, fmt.Errorf("%w: manifest %s of %d bytes exceeds the %d byte limit", artifact.ErrMalformedManifest, desc.Digest, desc.Size, artifact.MaxManifestSize)
	}
	// The pinned reference must parse, and ParsePinned takes sha256 only.
	if !digestRegexp.MatchString(desc.Digest.String()) {
		return nil, fmt.Errorf("%w: manifest digest %q is not sha256", artifact.ErrUnsupportedForm, desc.Digest)
	}
	manifest, err := inspectRead(ctx, t, desc)
	if err != nil {
		return nil, err
	}
	kind, m, err := artifact.Classify(desc.MediaType, manifest)
	if err != nil {
		return nil, fmt.Errorf("classifying %s: %w", desc.Digest, err)
	}
	in := &Inspection{Kind: kind, Descriptor: desc, Pinned: t.ref.Name() + "@" + desc.Digest.String(), target: t}
	if kind != artifact.KindPackage {
		return in, nil
	}

	config := m.Config()
	blob, err := inspectRead(ctx, t, config)
	if errors.Is(err, ErrDigestMismatch) {
		mismatch := &artifact.InvalidArtifactError{
			Reason: artifact.ReasonMetadataMismatch,
			Detail: fmt.Sprintf("metadata blob does not match descriptor %s (%d bytes)", config.Digest, config.Size),
		}
		return nil, fmt.Errorf("%w: %w", mismatch, err)
	}
	if err != nil {
		return nil, err
	}
	if in.Package, err = m.ParseMetadata(blob); err != nil {
		return nil, fmt.Errorf("parsing metadata %s: %w", config.Digest, err)
	}
	return in, nil
}

// Resolve returns the descriptor of the manifest the reference of t names, its
// media type, digest and size, from the one manifest request that resolves it,
// and reads no content. It is how a caller that knows what each digest holds
// learns the digest a tag names now. A digest that is not sha256 fails with
// artifact.ErrUnsupportedForm, as for Inspect; any other error is one of the
// Target sentinels or the context error.
func Resolve(ctx context.Context, t *Target) (ocispec.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return ocispec.Descriptor{}, err
	}
	desc, err := t.resolve(ctx)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	if !digestRegexp.MatchString(desc.Digest.String()) {
		return ocispec.Descriptor{}, fmt.Errorf("%w: manifest digest %q is not sha256", artifact.ErrUnsupportedForm, desc.Digest)
	}
	return desc, nil
}

// inspectRead reads the whole of the content desc describes, which fetch
// bounds by desc.Size and checks against desc.
func inspectRead(ctx context.Context, t *Target, desc ocispec.Descriptor) ([]byte, error) {
	reader, err := t.fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}
