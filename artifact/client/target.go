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
	"net/http"
	"net/url"
	"os"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/NVIDIA/nodewright/artifact"
)

// Target is where a package artifact is read from: a registry repository or
// an OCI image layout.
type Target struct {
	ref    Reference
	store  store
	remote bool
}

type store interface {
	content.Resolver
	content.Fetcher
}

// RemoteOptions configures NewRemote.
type RemoteOptions struct {
	// Credential authenticates to the registry. The zero value is anonymous.
	Credential Credential
	// HTTPClient sends the requests. Nil means retry.DefaultClient, which
	// retries over http.DefaultTransport and so honours HTTP(S)_PROXY.
	HTTPClient *http.Client
}

// NewRemote returns a Target that reads ref from its registry, over HTTPS
// only.
func NewRemote(ref Reference, opts RemoteOptions) (*Target, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = retry.DefaultClient
	}
	repo := &remote.Repository{
		Client: authClient{client: &auth.Client{
			Client:     httpClient,
			Cache:      auth.NewCache(),
			Credential: auth.StaticCredential(ref.APIHost(), opts.Credential.auth),
		}},
		Reference:        registry.Reference{Registry: ref.APIHost(), Repository: ref.Repository},
		MaxMetadataBytes: artifact.MaxManifestSize,
	}
	return &Target{ref: ref, store: repo, remote: true}, nil
}

// NewLayout returns a Target that reads ref from the read-only OCI image
// layout in dir. ref must be valid, as for NewRemote. Only its tag or digest
// is resolved; its registry and repository name the package in the Pinned
// reference Inspect returns.
func NewLayout(ctx context.Context, dir string, ref Reference) (*Target, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	store, err := oci.NewFromFS(ctx, os.DirFS(dir))
	if err != nil {
		return nil, fmt.Errorf("opening OCI image layout %s: %w", dir, err)
	}
	return &Target{ref: ref, store: store}, nil
}

// Reference returns the reference the target reads.
func (t *Target) Reference() Reference { return t.ref }

// resolve returns the descriptor of the manifest t.ref names with only its
// media type, digest and size, dropping the annotations a layout tag entry
// carries so that a registry and a layout agree.
func (t *Target) resolve(ctx context.Context) (ocispec.Descriptor, error) {
	reference := t.ref.Tag
	if t.ref.Digest != "" {
		reference = t.ref.Digest.String()
	}
	desc, err := t.store.Resolve(ctx, reference)
	if err != nil {
		return ocispec.Descriptor{}, classify(ctx, fmt.Errorf("resolving %s: %w", t.ref, err), nil)
	}
	return ocispec.Descriptor{MediaType: desc.MediaType, Digest: desc.Digest, Size: desc.Size}, nil
}

// fetch returns a reader over the manifest or blob desc describes.
func (t *Target) fetch(ctx context.Context, desc ocispec.Descriptor) (*blobReader, error) {
	// The digest becomes part of a request URL or a file path.
	if err := desc.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("fetching %s: %w", desc.Digest, err)
	}
	body, err := t.store.Fetch(ctx, desc)
	if err != nil {
		var otherwise error
		if t.remote {
			// Once its status checks pass, every error oras-go returns from
			// a remote fetch is a 200 response that contradicts desc: its
			// Content-Length, Docker-Content-Digest or manifest Content-Type.
			otherwise = ErrDigestMismatch
		}
		return nil, classify(ctx, fmt.Errorf("fetching %s: %w", desc.Digest, err), otherwise)
	}
	return newBlobReader(ctx, body, desc, t.remote), nil
}

// classify maps a resolve or fetch error onto the sentinels. otherwise, when
// not nil, is the sentinel of an error no other rule matches.
func classify(ctx context.Context, err, otherwise error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// %v, not %w: the error may already carry a sentinel, such as the
		// ErrTransport of a request the cancellation aborted.
		return fmt.Errorf("%w: %v", ctxErr, err)
	}
	var errResp *errcode.ErrorResponse
	switch {
	case errors.Is(err, ErrTransport), errors.Is(err, ErrUnauthorized), errors.Is(err, ErrNotFound):
		return err
	case errors.Is(err, errdef.ErrNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.As(err, &errResp):
		return classifyStatus(errResp.StatusCode, err)
	case otherwise != nil:
		return fmt.Errorf("%w: %w", otherwise, err)
	}
	return err
}

func classifyStatus(status int, err error) error {
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}
	return err
}

// authClient classifies the errors auth.Client returns itself, where their
// origin is still known. Past it, classify would take a refused connection or
// a failed token fetch during a remote fetch for a response that contradicts
// its descriptor.
type authClient struct {
	client *auth.Client
}

func (c authClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.client.Do(req)
	if err == nil {
		return resp, nil
	}
	var urlErr *url.Error
	var errResp *errcode.ErrorResponse
	switch {
	// url.Parse also returns a *url.Error, for a malformed bearer realm,
	// which is an authentication failure rather than a transport one.
	case errors.As(err, &urlErr) && urlErr.Op != "parse":
		return nil, fmt.Errorf("%w: %w", ErrTransport, err)
	case errors.As(err, &errResp):
		return nil, classifyStatus(errResp.StatusCode, err)
	}
	return nil, fmt.Errorf("%w: %w", ErrUnauthorized, err)
}
