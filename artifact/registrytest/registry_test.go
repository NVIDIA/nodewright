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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestRegistrytest(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Registrytest Suite")
}

// Placeholder credentials; they guard nothing but the test registry.
const (
	testUser     = "placeholder-user"
	testPassword = "placeholder-password"
)

// request sends method to path on reg, with basic credentials when given as
// user, password, and returns the response and its whole body.
func request(reg *Registry, method, path string, credentials ...string) (*http.Response, []byte) {
	GinkgoHelper()
	req, err := http.NewRequest(method, "https://"+reg.Host()+path, nil)
	Expect(err).NotTo(HaveOccurred())
	if len(credentials) == 2 {
		req.SetBasicAuth(credentials[0], credentials[1])
	}
	resp, err := reg.Client().Do(req)
	Expect(err).NotTo(HaveOccurred())
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.Body.Close()).To(Succeed())
	return resp, body
}

// expectContent asserts a 200 response carrying the given headers.
func expectContent(resp *http.Response, contentType string, dgst digest.Digest, size int) {
	GinkgoHelper()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	Expect(resp.Header.Get("Content-Type")).To(Equal(contentType))
	Expect(resp.Header.Get("Docker-Content-Digest")).To(Equal(dgst.String()))
	Expect(resp.Header.Get("Content-Length")).To(Equal(strconv.Itoa(size)))
	Expect(resp.ContentLength).To(BeEquivalentTo(size))
}

// errorCode returns the code of a distribution error body.
func errorCode(body []byte) string {
	GinkgoHelper()
	var doc struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	Expect(json.Unmarshal(body, &doc)).To(Succeed())
	Expect(doc.Errors).To(HaveLen(1))
	return doc.Errors[0].Code
}

var manifestData = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)

var _ = Describe("Registry", func() {
	var (
		ctx context.Context
		reg *Registry
	)

	BeforeEach(func() {
		ctx = context.Background()
		reg = New(Options{})
		DeferCleanup(reg.Close)
	})

	It("answers the ping", func() {
		resp, _ := request(reg, http.MethodGet, "/v2/")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("serves a manifest by tag and by digest", func() {
		desc, err := PushManifest(ctx, reg.Repository("org/pkg"), ocispec.MediaTypeImageManifest, manifestData, "v1")
		Expect(err).NotTo(HaveOccurred())
		for _, ref := range []string{"v1", desc.Digest.String()} {
			resp, body := request(reg, http.MethodGet, "/v2/org/pkg/manifests/"+ref)
			expectContent(resp, ocispec.MediaTypeImageManifest, desc.Digest, len(manifestData))
			Expect(body).To(Equal(manifestData))

			resp, body = request(reg, http.MethodHead, "/v2/org/pkg/manifests/"+ref)
			expectContent(resp, ocispec.MediaTypeImageManifest, desc.Digest, len(manifestData))
			Expect(body).To(BeEmpty())
		}
	})

	It("serves a blob by digest", func() {
		data := []byte("layer bytes")
		desc, err := PushBlob(ctx, reg.Repository("pkg"), ocispec.MediaTypeImageLayerGzip, data)
		Expect(err).NotTo(HaveOccurred())

		resp, body := request(reg, http.MethodGet, "/v2/pkg/blobs/"+desc.Digest.String())
		expectContent(resp, "application/octet-stream", desc.Digest, len(data))
		Expect(body).To(Equal(data))

		resp, body = request(reg, http.MethodHead, "/v2/pkg/blobs/"+desc.Digest.String())
		expectContent(resp, "application/octet-stream", desc.Digest, len(data))
		Expect(body).To(BeEmpty())
	})

	DescribeTable("answers 404 with a distribution error",
		func(path, code string) {
			_, err := PushManifest(ctx, reg.Repository("pkg"), ocispec.MediaTypeImageManifest, manifestData, "v1")
			Expect(err).NotTo(HaveOccurred())

			resp, body := request(reg, http.MethodGet, path)
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
			Expect(errorCode(body)).To(Equal(code))
			resp, _ = request(reg, http.MethodHead, path)
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		},
		Entry("an unknown repository", "/v2/other/manifests/v1", "NAME_UNKNOWN"),
		Entry("an unknown tag", "/v2/pkg/manifests/v2", "MANIFEST_UNKNOWN"),
		Entry("an unknown manifest", "/v2/pkg/manifests/"+digest.FromString("other").String(), "MANIFEST_UNKNOWN"),
		Entry("an unknown blob", "/v2/pkg/blobs/"+digest.FromString("other").String(), "BLOB_UNKNOWN"),
	)

	It("re-points a tag pushed again", func() {
		repo := reg.Repository("pkg")
		first, err := PushManifest(ctx, repo, ocispec.MediaTypeImageManifest, manifestData, "v1")
		Expect(err).NotTo(HaveOccurred())
		second, err := PushManifest(ctx, repo, ocispec.MediaTypeImageIndex, []byte(`{"schemaVersion":2}`), "v1")
		Expect(err).NotTo(HaveOccurred())

		resp, _ := request(reg, http.MethodGet, "/v2/pkg/manifests/v1")
		expectContent(resp, ocispec.MediaTypeImageIndex, second.Digest, int(second.Size))
		resp, _ = request(reg, http.MethodGet, "/v2/pkg/manifests/"+first.Digest.String())
		expectContent(resp, ocispec.MediaTypeImageManifest, first.Digest, int(first.Size))
	})

	It("logs every request with its final status", func() {
		desc, err := PushBlob(ctx, reg.Repository("pkg"), ocispec.MediaTypeImageLayerGzip, []byte("layer"))
		Expect(err).NotTo(HaveOccurred())
		request(reg, http.MethodGet, "/v2/")
		request(reg, http.MethodHead, "/v2/pkg/blobs/"+desc.Digest.String())
		request(reg, http.MethodGet, "/v2/pkg/manifests/missing")
		Expect(reg.Requests()).To(Equal([]Request{
			{Method: http.MethodGet, Path: "/v2/", Status: http.StatusOK},
			{Method: http.MethodHead, Path: "/v2/pkg/blobs/" + desc.Digest.String(), Status: http.StatusOK},
			{Method: http.MethodGet, Path: "/v2/pkg/manifests/missing", Status: http.StatusNotFound},
		}))

		reg.ResetRequests()
		Expect(reg.Requests()).To(BeEmpty())
		request(reg, http.MethodGet, "/v2/")
		Expect(reg.Requests()).To(HaveLen(1))
	})

	Context("with credentials", func() {
		var path string

		BeforeEach(func() {
			reg = New(Options{Username: testUser, Password: testPassword})
			DeferCleanup(reg.Close)
			desc, err := PushBlob(ctx, reg.Repository("pkg"), ocispec.MediaTypeImageLayerGzip, []byte("layer"))
			Expect(err).NotTo(HaveOccurred())
			path = "/v2/pkg/blobs/" + desc.Digest.String()
		})

		It("accepts the right credentials", func() {
			for _, p := range []string{"/v2/", path} {
				resp, _ := request(reg, http.MethodGet, p, testUser, testPassword)
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
			}
		})

		DescribeTable("rejects a request with a Basic challenge",
			func(credentials ...string) {
				for _, p := range []string{"/v2/", path} {
					resp, body := request(reg, http.MethodGet, p, credentials...)
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(resp.Header.Get("WWW-Authenticate")).To(Equal("Basic realm=registrytest"))
					Expect(errorCode(body)).To(Equal("UNAUTHORIZED"))
					Expect(reg.Requests()).To(HaveLen(1))
					Expect(reg.Requests()[0].Status).To(Equal(http.StatusUnauthorized))
					reg.ResetRequests()
				}
			},
			Entry("without credentials"),
			Entry("with a wrong password", testUser, "wrong-password"),
			Entry("with a wrong user", "wrong-user", testPassword),
		)
	})

	DescribeTable("Tamper serves GET the given body under the original headers",
		func(kind string, tampered []byte) {
			var desc ocispec.Descriptor
			for _, name := range []string{"one", "two"} {
				var err error
				desc, err = PushManifest(ctx, reg.Repository(name), ocispec.MediaTypeImageManifest, manifestData, "v1")
				Expect(err).NotTo(HaveOccurred())
			}
			reg.Tamper(desc.Digest, tampered)

			contentType, refs := ocispec.MediaTypeImageManifest, []string{"v1", desc.Digest.String()}
			if kind == kindBlobs {
				contentType, refs = "application/octet-stream", []string{desc.Digest.String()}
			}
			for _, name := range []string{"one", "two"} {
				for _, ref := range refs {
					path := "/v2/" + name + "/" + kind + "/" + ref
					resp, body := request(reg, http.MethodGet, path)
					expectContent(resp, contentType, desc.Digest, len(tampered))
					Expect(body).To(Equal(tampered))

					resp, _ = request(reg, http.MethodHead, path)
					expectContent(resp, contentType, desc.Digest, len(manifestData))
				}
			}
		},
		Entry("of a blob at the same length", kindBlobs, bytes.ToUpper(manifestData)),
		Entry("of a blob at a different length", kindBlobs, []byte("tampered")),
		Entry("of a manifest at the same length", kindManifests, bytes.ToUpper(manifestData)),
		Entry("of a manifest at a different length", kindManifests, []byte("tampered")),
	)
})

var _ = Describe("Repository", func() {
	It("refuses a second push of the same content, as an oci.Store does", func() {
		reg := New(Options{})
		DeferCleanup(reg.Close)
		repo := reg.Repository("pkg")
		desc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(manifestData), Size: int64(len(manifestData))}
		Expect(repo.Push(context.Background(), desc, bytes.NewReader(manifestData))).To(Succeed())
		Expect(repo.Push(context.Background(), desc, bytes.NewReader(manifestData))).NotTo(Succeed())
	})
})
