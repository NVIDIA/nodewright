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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/NVIDIA/nodewright/artifact"
)

var (
	layer         = []byte("placeholder layer content")
	alteredLayer  = []byte("placeholder layer CONTENT")
	layerDesc     = descriptorOf("application/vnd.oci.image.layer.v1.tar", layer)
	config        = []byte(`{}`)
	configDesc    = descriptorOf(ocispec.MediaTypeImageConfig, config)
	manifest      = mustJSON(ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest, Config: configDesc, Layers: []ocispec.Descriptor{layerDesc}})
	manifestDesc  = descriptorOf(ocispec.MediaTypeImageManifest, manifest)
	allSentinels  = []error{ErrNotFound, ErrUnauthorized, ErrTransport, ErrDigestMismatch}
	errNoSentinel error
)

func descriptorOf(mediaType string, blob []byte) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(blob), Size: int64(len(blob))}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// expectSentinel asserts that err matches want, or no sentinel when want is
// errNoSentinel, and no other sentinel.
func expectSentinel(err, want error) {
	GinkgoHelper()
	Expect(err).To(HaveOccurred())
	for _, sentinel := range allSentinels {
		Expect(errors.Is(err, sentinel)).To(Equal(sentinel == want), "%v: matching %v", err, sentinel)
	}
}

// newRegistry starts a TLS server for handler and returns it with a reference
// to test/app:1.0 on it.
func newRegistry(handler http.Handler) (*httptest.Server, Reference) {
	GinkgoHelper()
	server := httptest.NewTLSServer(handler)
	DeferCleanup(server.Close)
	ref, err := NewReference(server.Listener.Addr().String()+"/test/app", "1.0", "")
	Expect(err).NotTo(HaveOccurred())
	return server, ref
}

// remoteTarget reads ref through the client of server, which trusts its
// certificate and does not retry.
func remoteTarget(server *httptest.Server, ref Reference, cred Credential) *Target {
	GinkgoHelper()
	target, err := NewRemote(ref, RemoteOptions{Credential: cred, HTTPClient: server.Client()})
	Expect(err).NotTo(HaveOccurred())
	return target
}

// serve answers every request with status, the given header pairs and body.
func serve(status int, body []byte, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func serveBlob(body []byte, contentDigest digest.Digest) http.HandlerFunc {
	return serve(http.StatusOK, body, "Content-Type", "application/octet-stream", "Docker-Content-Digest", string(contentDigest))
}

var _ = Describe("Target", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("builds an HTTPS repository at the API host over retry.DefaultClient by default", func() {
		ref, err := NewReference("alpine", "3.20", "")
		Expect(err).NotTo(HaveOccurred())
		target, err := NewRemote(ref, RemoteOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(target.Reference()).To(Equal(ref))

		repo := target.store.(*remote.Repository)
		Expect(repo.Reference.Registry).To(Equal("registry-1.docker.io"))
		Expect(repo.Reference.Repository).To(Equal("library/alpine"))
		Expect(repo.PlainHTTP).To(BeFalse())
		Expect(repo.MaxMetadataBytes).To(BeEquivalentTo(artifact.MaxManifestSize))
		client := repo.Client.(authClient).client
		Expect(client.Client).To(BeIdenticalTo(retry.DefaultClient))
		Expect(client.Cache).NotTo(BeNil())
		Expect(client.Cache).NotTo(BeIdenticalTo(auth.DefaultCache))
	})

	It("rejects an invalid reference", func() {
		_, err := NewRemote(Reference{Registry: "ghcr.io", Repository: "Org/App", Tag: "1.0"}, RemoteOptions{})
		Expect(err).To(MatchError(ErrInvalidReference))
	})

	It("resolves a manifest and fetches content that matches its descriptor", func() {
		server, ref := newRegistry(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/test/app/manifests/1.0":
				serve(http.StatusOK, manifest, "Content-Type", manifestDesc.MediaType, "Docker-Content-Digest", string(manifestDesc.Digest))(w, r)
			case "/v2/test/app/blobs/" + string(layerDesc.Digest):
				serveBlob(layer, layerDesc.Digest)(w, r)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		target := remoteTarget(server, ref, Credential{})

		Expect(target.resolve(ctx)).To(Equal(manifestDesc))
		reader, err := target.fetch(ctx, layerDesc)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
		Expect(io.ReadAll(reader)).To(Equal(layer))
	})

	It("sends the credential to the registry that challenges for it", func() {
		server, ref := newRegistry(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, password, _ := r.BasicAuth(); user != "placeholder-user" || password != "placeholder-password" {
				serve(http.StatusUnauthorized, nil, "WWW-Authenticate", `Basic realm="test"`)(w, r)
				return
			}
			serve(http.StatusOK, manifest, "Content-Type", manifestDesc.MediaType, "Docker-Content-Digest", string(manifestDesc.Digest))(w, r)
		}))
		cred, _ := LookupCredential(ref.Registry, []CredentialSource{
			configSource("s", onlyEntry(ref.Registry, `{"username":"placeholder-user","password":"placeholder-password"}`)),
		})
		Expect(remoteTarget(server, ref, cred).resolve(ctx)).To(Equal(manifestDesc))
	})

	Describe("classifies a failed resolve", func() {
		basicChallenge := serve(http.StatusUnauthorized, nil, "WWW-Authenticate", `Basic realm="test"`)

		It("as ErrUnauthorized for a Basic challenge without a credential", func() {
			server, ref := newRegistry(basicChallenge)
			_, err := remoteTarget(server, ref, Credential{}).resolve(ctx)
			expectSentinel(err, ErrUnauthorized)
			Expect(err).To(MatchError(auth.ErrBasicCredentialNotFound))
		})

		It("as ErrUnauthorized for a Basic challenge with an identity-token-only credential", func() {
			server, ref := newRegistry(basicChallenge)
			cred, _ := LookupCredential(ref.Registry, []CredentialSource{
				configSource("s", onlyEntry(ref.Registry, `{"identitytoken":"placeholder-identity"}`)),
			})
			Expect(cred.Source()).To(Equal("s"))
			_, err := remoteTarget(server, ref, cred).resolve(ctx)
			expectSentinel(err, ErrUnauthorized)
			Expect(err).To(MatchError(ContainSubstring("missing username or password for basic auth")))
		})

		DescribeTable("by response status",
			func(status int, want error) {
				server, ref := newRegistry(serve(status, nil))
				_, err := remoteTarget(server, ref, Credential{}).resolve(ctx)
				expectSentinel(err, want)
				if status != http.StatusNotFound {
					var errResp *errcode.ErrorResponse
					Expect(errors.As(err, &errResp)).To(BeTrue())
					Expect(errResp.StatusCode).To(Equal(status))
				}
			},
			Entry("a bare 401", http.StatusUnauthorized, ErrUnauthorized),
			Entry("403", http.StatusForbidden, ErrUnauthorized),
			Entry("404", http.StatusNotFound, ErrNotFound),
			Entry("500", http.StatusInternalServerError, errNoSentinel),
		)

		It("as ErrTransport for a closed server", func() {
			server, ref := newRegistry(serve(http.StatusOK, nil))
			target := remoteTarget(server, ref, Credential{})
			server.Close()
			_, err := target.resolve(ctx)
			expectSentinel(err, ErrTransport)
			var urlErr *url.Error
			Expect(errors.As(err, &urlErr)).To(BeTrue())
		})

		It("as ErrTransport for a certificate the client does not trust", func() {
			_, ref := newRegistry(serve(http.StatusOK, nil))
			target, err := NewRemote(ref, RemoteOptions{HTTPClient: &http.Client{}})
			Expect(err).NotTo(HaveOccurred())
			_, err = target.resolve(ctx)
			expectSentinel(err, ErrTransport)
		})

		It("as ErrTransport for a plain-HTTP server, without falling back to plain HTTP", func() {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			DeferCleanup(server.Close)
			ref, err := NewReference(server.Listener.Addr().String()+"/test/app", "1.0", "")
			Expect(err).NotTo(HaveOccurred())
			_, err = remoteTarget(server, ref, Credential{}).resolve(ctx)
			expectSentinel(err, ErrTransport)
			Expect(requests.Load()).To(BeZero())
		})

		It("as the context error alone for a cancelled context", func() {
			server, ref := newRegistry(serve(http.StatusOK, manifest, "Content-Type", manifestDesc.MediaType, "Docker-Content-Digest", string(manifestDesc.Digest)))
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, err := remoteTarget(server, ref, Credential{}).resolve(cancelled)
			expectSentinel(err, errNoSentinel)
			Expect(err).To(MatchError(context.Canceled))
		})
	})

	Describe("checks content against its descriptor", func() {
		DescribeTable("rejecting a fetch response as ErrDigestMismatch",
			func(handler http.HandlerFunc, desc ocispec.Descriptor) {
				server, ref := newRegistry(handler)
				_, err := remoteTarget(server, ref, Credential{}).fetch(ctx, desc)
				expectSentinel(err, ErrDigestMismatch)
			},
			Entry("a blob whose Content-Length differs from the descriptor",
				serveBlob(append(bytes.Clone(layer), 'x'), ""), layerDesc),
			Entry("a blob whose Docker-Content-Digest names another digest",
				serveBlob(layer, digest.FromString("another")), layerDesc),
			Entry("a manifest of another Content-Type",
				serve(http.StatusOK, manifest, "Content-Type", "application/json", "Docker-Content-Digest", string(manifestDesc.Digest)), manifestDesc),
		)

		It("failing the read of a same-length altered body with ErrDigestMismatch", func() {
			server, ref := newRegistry(serveBlob(alteredLayer, layerDesc.Digest))
			reader, err := remoteTarget(server, ref, Credential{}).fetch(ctx, layerDesc)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(reader.Close)
			_, err = io.ReadAll(reader)
			expectSentinel(err, ErrDigestMismatch)
			Expect(err).To(MatchError(content.ErrMismatchedDigest))
		})

		It("failing the read of a body cut off below HTTP with ErrTransport", func() {
			server, ref := newRegistry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(layer)))
				_, _ = w.Write(layer[:4])
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			reader, err := remoteTarget(server, ref, Credential{}).fetch(ctx, layerDesc)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(reader.Close)
			_, err = io.ReadAll(reader)
			expectSentinel(err, ErrTransport)
			Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		})

		DescribeTable("draining after a partial read",
			func(body []byte, want error) {
				server, ref := newRegistry(serveBlob(body, layerDesc.Digest))
				reader, err := remoteTarget(server, ref, Credential{}).fetch(ctx, layerDesc)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(reader.Close)
				_, err = reader.Read(make([]byte, 4))
				Expect(err).NotTo(HaveOccurred())
				if want == nil {
					Expect(reader.drain()).To(Succeed())
				} else {
					expectSentinel(reader.drain(), want)
				}
			},
			Entry("of altered bytes fails with ErrDigestMismatch", alteredLayer, ErrDigestMismatch),
			Entry("of correct bytes succeeds", layer, nil),
		)
	})

	Describe("in an OCI image layout", func() {
		var dir string

		writeBlob := func(blob []byte) {
			path := filepath.Join(dir, "blobs", "sha256", digest.FromBytes(blob).Encoded())
			Expect(os.WriteFile(path, blob, 0o600)).To(Succeed())
		}

		layoutTarget := func(tag string) *Target {
			GinkgoHelper()
			ref, err := NewReference("ghcr.io/org/app", tag, "")
			Expect(err).NotTo(HaveOccurred())
			target, err := NewLayout(ctx, dir, ref)
			Expect(err).NotTo(HaveOccurred())
			return target
		}

		BeforeEach(func() {
			dir = GinkgoT().TempDir()
			Expect(os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o700)).To(Succeed())
			tagged := manifestDesc
			tagged.Annotations = map[string]string{ocispec.AnnotationRefName: "1.0"}
			index := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{tagged}}
			Expect(os.WriteFile(filepath.Join(dir, ocispec.ImageLayoutFile), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, ocispec.ImageIndexFile), mustJSON(index), 0o600)).To(Succeed())
			for _, blob := range [][]byte{manifest, config, layer} {
				writeBlob(blob)
			}
		})

		It("resolves a tag to the descriptor a registry gives, without the tag annotation", func() {
			target := layoutTarget("1.0")
			Expect(target.resolve(ctx)).To(Equal(manifestDesc))
			reader, err := target.fetch(ctx, layerDesc)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(reader.Close)
			Expect(io.ReadAll(reader)).To(Equal(layer))
		})

		It("reports a missing tag as ErrNotFound", func() {
			_, err := layoutTarget("2.0").resolve(ctx)
			expectSentinel(err, ErrNotFound)
		})

		It("fails the read of a blob altered on disk with ErrDigestMismatch", func() {
			target := layoutTarget("1.0")
			Expect(os.WriteFile(filepath.Join(dir, "blobs", "sha256", layerDesc.Digest.Encoded()), alteredLayer, 0o600)).To(Succeed())
			reader, err := target.fetch(ctx, layerDesc)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(reader.Close)
			_, err = io.ReadAll(reader)
			expectSentinel(err, ErrDigestMismatch)
		})

		It("refuses a directory without an oci-layout file", func() {
			Expect(os.Remove(filepath.Join(dir, ocispec.ImageLayoutFile))).To(Succeed())
			ref, err := NewReference("ghcr.io/org/app", "1.0", "")
			Expect(err).NotTo(HaveOccurred())
			_, err = NewLayout(ctx, dir, ref)
			Expect(err).To(HaveOccurred())
			Expect(err).NotTo(MatchError(ErrInvalidReference))
		})

		// An invalid registry or repository would give Inspect a Pinned that
		// ParsePinned rejects.
		DescribeTable("rejects an invalid reference",
			func(ref Reference) {
				_, err := NewLayout(ctx, dir, ref)
				Expect(err).To(MatchError(ErrInvalidReference))
			},
			Entry("with no registry or repository", Reference{Tag: "1.0"}),
			Entry("with a repository outside the grammar", Reference{Registry: "ghcr.io", Repository: "Org/App", Tag: "1.0"}),
		)
	})
})
