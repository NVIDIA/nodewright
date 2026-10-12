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
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// blobReader is the one integrity check of the package. Read yields at most
// desc.Size bytes and returns io.EOF only once exactly desc.Size bytes were
// read, no byte follows them and their digest matches; any other outcome is
// ErrDigestMismatch, unless reading the body itself failed.
type blobReader struct {
	ctx      context.Context
	body     io.ReadCloser
	source   *errorRecorder
	verifier *content.VerifyReader
	desc     ocispec.Descriptor
	remote   bool
	err      error
}

func newBlobReader(ctx context.Context, body io.ReadCloser, desc ocispec.Descriptor, remote bool) *blobReader {
	source := &errorRecorder{reader: body}
	return &blobReader{
		ctx:      ctx,
		body:     body,
		source:   source,
		verifier: content.NewVerifyReader(source, desc),
		desc:     desc,
		remote:   remote,
	}
}

func (r *blobReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.verifier.Read(p)
	switch err {
	case nil:
		return n, nil
	case io.EOF:
		if err = r.verifier.Verify(); err == nil {
			r.err = io.EOF
			return n, io.EOF
		}
	}
	r.err = r.fail(err)
	return n, r.err
}

func (r *blobReader) fail(err error) error {
	switch {
	case r.ctx.Err() != nil:
		return fmt.Errorf("%w: reading %s: %v", r.ctx.Err(), r.desc.Digest, err)
	case r.source.err != nil && r.remote:
		return fmt.Errorf("%w: reading %s: %w", ErrTransport, r.desc.Digest, r.source.err)
	case r.source.err != nil:
		return fmt.Errorf("reading %s: %w", r.desc.Digest, r.source.err)
	}
	return fmt.Errorf("%w: %s: %w", ErrDigestMismatch, r.desc.Digest, err)
}

// drain reads and discards the rest of the content, up to desc.Size bytes,
// and returns nil when all of it matched desc.
func (r *blobReader) drain() error {
	_, err := io.Copy(io.Discard, r)
	return err
}

func (r *blobReader) Close() error {
	return r.body.Close()
}

// errorRecorder keeps the first error other than io.EOF that reading its
// reader returned. VerifyReader reports a failed read and content that does
// not verify alike, and turns a read failure after the last byte into
// content.ErrTrailingData, so only this tells them apart.
type errorRecorder struct {
	reader io.Reader
	err    error
}

func (e *errorRecorder) Read(p []byte) (int, error) {
	n, err := e.reader.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}
