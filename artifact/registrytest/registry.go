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

// Package registrytest is an in-memory OCI registry for tests, and builders
// that push packages, legacy images and indexes into it or into any other
// store with the same method set, such as an oras-go oci.Store. The registry
// serves only the read side of the distribution API that a client fetching
// packages uses; content goes in through Repository.
package registrytest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Options configures a Registry. When Username and Password are both set,
// every /v2/ request must carry them as basic credentials.
type Options struct {
	Username, Password string
}

// Request is one request the registry served, with its final status.
type Request struct {
	Method, Path string
	Status       int
}

// Pusher writes content into a store. It is the method set of the oras-go
// content.Storage and content.Tagger that the builders use, so an oras-go
// oci.Store satisfies it as well as Registry.Repository does.
type Pusher interface {
	Exists(ctx context.Context, desc ocispec.Descriptor) (bool, error)
	Push(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error
	Tag(ctx context.Context, desc ocispec.Descriptor, tag string) error
}

// Registry is an in-memory OCI registry served over TLS. It is safe for
// concurrent requests.
type Registry struct {
	server *httptest.Server
	opts   Options

	mu       sync.Mutex
	repos    map[string]*repo
	tampered map[digest.Digest][]byte
	requests []Request
}

type repo struct {
	content map[digest.Digest]stored
	tags    map[string]digest.Digest
}

type stored struct {
	mediaType string
	data      []byte
}

const (
	kindManifests = "manifests"
	kindBlobs     = "blobs"
)

// New starts a registry. Close stops it.
func New(opts Options) *Registry {
	r := &Registry{opts: opts, repos: map[string]*repo{}, tampered: map[digest.Digest][]byte{}}
	r.server = httptest.NewTLSServer(http.HandlerFunc(r.serveHTTP))
	return r
}

// Close stops the registry.
func (r *Registry) Close() { r.server.Close() }

// Host returns the registry's host:port.
func (r *Registry) Host() string { return r.server.Listener.Addr().String() }

// Client returns an HTTP client that trusts the registry's certificate and
// never retries.
func (r *Registry) Client() *http.Client { return r.server.Client() }

// Requests returns every request served so far, in order.
func (r *Registry) Requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.requests...)
}

// ResetRequests empties the request log.
func (r *Registry) ResetRequests() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

// Tamper makes every GET of the blob or manifest with digest dgst, in any
// repository, serve body instead, with Content-Length len(body) and the
// original Docker-Content-Digest and Content-Type. HEAD responses and the
// stored content are unchanged.
func (r *Registry) Tamper(dgst digest.Digest, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tampered[dgst] = bytes.Clone(body)
}

// Repository returns a Pusher that writes straight into the named repository
// of the in-memory store, creating it on first write.
func (r *Registry) Repository(name string) Pusher { return &repository{reg: r, name: name} }

type repository struct {
	reg  *Registry
	name string
}

func (h *repository) Exists(_ context.Context, desc ocispec.Descriptor) (bool, error) {
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()
	rp := h.reg.repos[h.name]
	if rp == nil {
		return false, nil
	}
	_, ok := rp.content[desc.Digest]
	return ok, nil
}

// Push refuses content that already exists, as an oras-go oci.Store does, so
// a builder that pushes twice fails here too.
func (h *repository) Push(_ context.Context, desc ocispec.Descriptor, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := desc.Digest.Validate(); err != nil {
		return err
	}
	if int64(len(data)) != desc.Size || desc.Digest.Algorithm().FromBytes(data) != desc.Digest {
		return fmt.Errorf("registrytest: content does not match descriptor %s (%d bytes)", desc.Digest, desc.Size)
	}
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()
	rp := h.reg.repos[h.name]
	if rp == nil {
		rp = &repo{content: map[digest.Digest]stored{}, tags: map[string]digest.Digest{}}
		h.reg.repos[h.name] = rp
	}
	if _, ok := rp.content[desc.Digest]; ok {
		return fmt.Errorf("registrytest: %s already exists in %s", desc.Digest, h.name)
	}
	rp.content[desc.Digest] = stored{mediaType: desc.MediaType, data: data}
	return nil
}

// Tag points tag at desc, which must already be in the repository. An
// existing tag is re-pointed.
func (h *repository) Tag(_ context.Context, desc ocispec.Descriptor, tag string) error {
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()
	rp := h.reg.repos[h.name]
	if rp == nil {
		return fmt.Errorf("registrytest: tagging %s: repository %s not found", desc.Digest, h.name)
	}
	if _, ok := rp.content[desc.Digest]; !ok {
		return fmt.Errorf("registrytest: tagging %s: not found in %s", desc.Digest, h.name)
	}
	rp.tags[tag] = desc.Digest
	return nil
}

func (r *Registry) serveHTTP(w http.ResponseWriter, req *http.Request) {
	rest, ok := strings.CutPrefix(req.URL.Path, "/v2/")
	if !ok {
		r.fail(w, req, http.StatusNotFound, "UNSUPPORTED", "not a distribution API path")
		return
	}
	if r.opts.Username != "" && r.opts.Password != "" {
		user, pass, ok := req.BasicAuth()
		if !ok || user != r.opts.Username || pass != r.opts.Password {
			w.Header().Set("WWW-Authenticate", "Basic realm=registrytest")
			r.fail(w, req, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		r.fail(w, req, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		return
	}
	if rest == "" {
		w.Header().Set("Content-Type", "application/json")
		r.reply(w, req, http.StatusOK, []byte("{}"))
		return
	}
	// A repository name may contain slashes; the kind and reference never do.
	segs := strings.Split(rest, "/")
	n := len(segs)
	if n < 3 || segs[n-1] == "" || (segs[n-2] != kindManifests && segs[n-2] != kindBlobs) {
		r.fail(w, req, http.StatusNotFound, "UNSUPPORTED", "endpoint not served")
		return
	}
	r.serveContent(w, req, strings.Join(segs[:n-2], "/"), segs[n-2], segs[n-1])
}

func (r *Registry) serveContent(w http.ResponseWriter, req *http.Request, name, kind, ref string) {
	r.mu.Lock()
	rp := r.repos[name]
	var content stored
	found := false
	dgst := digest.Digest(ref)
	if rp != nil {
		if tagged, ok := rp.tags[ref]; ok && kind == kindManifests {
			dgst = tagged
		}
		content, found = rp.content[dgst]
	}
	tampered, isTampered := r.tampered[dgst]
	r.mu.Unlock()

	switch {
	case rp == nil:
		r.fail(w, req, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	case !found && kind == kindManifests:
		r.fail(w, req, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	case !found:
		r.fail(w, req, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}

	contentType := content.mediaType
	if kind == kindBlobs {
		contentType = "application/octet-stream"
	}
	body := content.data
	if isTampered && req.Method == http.MethodGet {
		body = tampered
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Docker-Content-Digest", dgst.String())
	r.reply(w, req, http.StatusOK, body)
}

// fail replies with a distribution error body.
func (r *Registry) fail(w http.ResponseWriter, req *http.Request, status int, code, message string) {
	type errorEntry struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	body, _ := json.Marshal(map[string][]errorEntry{"errors": {{Code: code, Message: message}}})
	w.Header().Set("Content-Type", "application/json")
	r.reply(w, req, status, body)
}

// reply logs the request, then writes the response with Content-Length
// len(body), and the body itself unless the request is a HEAD. Logging first
// puts a request in the log before its client can see the response.
func (r *Registry) reply(w http.ResponseWriter, req *http.Request, status int, body []byte) {
	r.mu.Lock()
	r.requests = append(r.requests, Request{Method: req.Method, Path: req.URL.Path, Status: status})
	r.mu.Unlock()
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
