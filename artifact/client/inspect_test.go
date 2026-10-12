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
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
)

const (
	inspectRepo   = "nodewright/hello"
	inspectConfig = `{"schema_version":"v1","package_name":"hello","package_version":"1.0.0","modes":{}}`
	// Placeholder credentials; they guard nothing but the test registry.
	inspectUser     = "placeholder-user"
	inspectPassword = "placeholder-password"
)

// inspectPackageDir writes a payload tree holding only config.json.
func inspectPackageDir() string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(dir, "config.json"), []byte(inspectConfig), 0o600)).To(Succeed())
	return dir
}

// inspectPush pushes spec to inspectRepo on reg, tagged 1.0.
func inspectPush(ctx context.Context, reg *registrytest.Registry, spec registrytest.PackageSpec) (ocispec.Descriptor, ocispec.Manifest) {
	GinkgoHelper()
	desc, m, err := registrytest.PushPackage(ctx, reg.Repository(inspectRepo), "1.0", spec)
	Expect(err).NotTo(HaveOccurred())
	return desc, m
}

// inspectRemote reads inspectRepo on reg by version, or by containerSHA when
// it is set.
func inspectRemote(reg *registrytest.Registry, creds []Credential, version, containerSHA string) *Target {
	GinkgoHelper()
	ref, err := NewReference(reg.Host()+"/"+inspectRepo, version, containerSHA)
	Expect(err).NotTo(HaveOccurred())
	target, err := NewRemote(ref, RemoteOptions{Credentials: creds, HTTPClient: reg.Client()})
	Expect(err).NotTo(HaveOccurred())
	return target
}

// inspectRequests returns the logged requests for a manifest or blob, as kind
// says, of method, or of any method when method is empty.
func inspectRequests(reg *registrytest.Registry, method, kind string) []registrytest.Request {
	var matched []registrytest.Request
	for _, r := range reg.Requests() {
		if strings.Contains(r.Path, "/"+kind+"/") && (method == "" || r.Method == method) {
			matched = append(matched, r)
		}
	}
	return matched
}

// inspectPaddedManifest returns a legacy image manifest of exactly size bytes.
func inspectPaddedManifest(size int) []byte {
	GinkgoHelper()
	m := ocispec.Manifest{
		Versioned:   specs.Versioned{SchemaVersion: 2},
		MediaType:   ocispec.MediaTypeImageManifest,
		Config:      descriptorOf(ocispec.MediaTypeImageConfig, []byte(`{}`)),
		Annotations: map[string]string{"pad": ""},
	}
	m.Annotations["pad"] = strings.Repeat("x", size-len(mustJSON(m)))
	data := mustJSON(m)
	Expect(data).To(HaveLen(size))
	return data
}

var _ = Describe("Inspect", func() {
	var (
		ctx  context.Context
		reg  *registrytest.Registry
		spec registrytest.PackageSpec
	)

	BeforeEach(func() {
		ctx = context.Background()
		reg = registrytest.New(registrytest.Options{})
		DeferCleanup(reg.Close)
		spec = registrytest.PackageSpec{
			Uninstall: artifact.UninstallSupported,
			Payload:   []registrytest.Layer{{Dir: inspectPackageDir()}},
		}
	})

	It("exposes an interrupt and host-files layers, fetching only the metadata blob", func() {
		hostDir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(hostDir, "motd"), []byte("host\n"), 0o600)).To(Succeed())
		interrupt := &artifact.Interrupt{
			Sense: artifact.InterruptStep{Path: "interrupt/sense.sh", Arguments: []string{}, OnHost: true},
			Run:   artifact.InterruptStep{Path: "interrupt/run.sh", Arguments: []string{"--now"}, OnHost: true},
		}
		spec.Interrupt = interrupt
		spec.HostFiles = []registrytest.HostFilesLayer{{Layer: registrytest.Layer{Dir: hostDir}, Destination: "/etc/nodewright"}}
		desc, m := inspectPush(ctx, reg, spec)
		Expect(m.Layers).To(HaveLen(2))
		reg.ResetRequests()

		in, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Kind).To(Equal(artifact.KindPackage))
		Expect(in.Descriptor).To(Equal(desc))
		Expect(in.Package.ConfigDescriptor).To(Equal(m.Config))
		Expect(in.Package.Interrupt).To(Equal(interrupt))
		Expect(in.Package.Payload).To(Equal(m.Layers[:1]))
		Expect(in.Package.HostFiles).To(Equal([]artifact.HostFileLayer{{Descriptor: m.Layers[1], Destination: "/etc/nodewright"}}))

		Expect(inspectRequests(reg, "", "blobs")).To(ConsistOf(
			HaveField("Path", "/v2/"+inspectRepo+"/blobs/"+m.Config.Digest.String())))
		for _, l := range m.Layers {
			Expect(reg.Requests()).NotTo(ContainElement(HaveField("Path", ContainSubstring(l.Digest.Encoded()))))
		}
	})

	It("gives the same Descriptor by tag and by digest, and a Pinned that ParsePinned round-trips", func() {
		desc, _ := inspectPush(ctx, reg, spec)

		byTag, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		Expect(err).NotTo(HaveOccurred())
		pinned, err := ParsePinned(byTag.Pinned)
		Expect(err).NotTo(HaveOccurred())
		Expect(pinned).To(Equal(Reference{Registry: reg.Host(), Repository: inspectRepo, Digest: desc.Digest}))

		target, err := NewRemote(pinned, RemoteOptions{HTTPClient: reg.Client()})
		Expect(err).NotTo(HaveOccurred())
		byDigest, err := Inspect(ctx, target)
		Expect(err).NotTo(HaveOccurred())
		Expect(byDigest.Descriptor).To(Equal(byTag.Descriptor))
		Expect(byDigest.Descriptor).To(Equal(desc))
		Expect(byDigest.Pinned).To(Equal(byTag.Pinned))
		Expect(byDigest.Package).To(Equal(byTag.Package))
	})

	DescribeTable("classifies as KindLegacy without fetching a blob",
		func(push func(context.Context, registrytest.Pusher, string, string) (ocispec.Descriptor, error)) {
			desc, err := push(ctx, reg.Repository(inspectRepo), "1.0", inspectPackageDir())
			Expect(err).NotTo(HaveOccurred())
			reg.ResetRequests()

			in, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
			Expect(err).NotTo(HaveOccurred())
			Expect(in.Kind).To(Equal(artifact.KindLegacy))
			Expect(in.Descriptor).To(Equal(desc))
			Expect(in.Package).To(BeNil())
			Expect(inspectRequests(reg, "", "blobs")).To(BeEmpty())
		},
		Entry("a legacy image", registrytest.PushImage),
		Entry("an index", registrytest.PushIndex),
	)

	It("pins the containerSHA after the version tag is re-pushed to another manifest", func() {
		first, _ := inspectPush(ctx, reg, spec)
		spec.Uninstall = artifact.UninstallUnsupported
		second, _ := inspectPush(ctx, reg, spec)
		Expect(second.Digest).NotTo(Equal(first.Digest))

		byTag, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(byTag.Descriptor).To(Equal(second))

		in, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", first.Digest.String()))
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Descriptor).To(Equal(first))
		Expect(in.Pinned).To(Equal(reg.Host() + "/" + inspectRepo + "@" + first.Digest.String()))
		Expect(in.Package.Uninstall).To(Equal(artifact.UninstallSupported))
	})

	DescribeTable("bounds the manifest by its resolved size",
		func(size int, want error) {
			_, err := registrytest.PushManifest(ctx, reg.Repository(inspectRepo), ocispec.MediaTypeImageManifest, inspectPaddedManifest(size), "1.0")
			Expect(err).NotTo(HaveOccurred())
			reg.ResetRequests()

			in, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
				Expect(in.Kind).To(Equal(artifact.KindLegacy))
				return
			}
			expectSentinel(err, errNoSentinel)
			Expect(err).To(MatchError(want))
			Expect(inspectRequests(reg, http.MethodGet, "manifests")).To(BeEmpty())
		},
		Entry("accepting one at the bound", artifact.MaxManifestSize, nil),
		Entry("failing one a byte over it with ErrMalformedManifest before the manifest GET", artifact.MaxManifestSize+1, artifact.ErrMalformedManifest),
	)

	It("fails a manifest whose resolved digest is not sha256 with ErrUnsupportedForm before the manifest GET", func() {
		data := inspectPaddedManifest(512)
		desc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.SHA512.FromBytes(data), Size: int64(len(data))}
		repo := reg.Repository(inspectRepo)
		Expect(repo.Push(ctx, desc, bytes.NewReader(data))).To(Succeed())
		Expect(repo.Tag(ctx, desc, "1.0")).To(Succeed())
		reg.ResetRequests()

		_, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		expectSentinel(err, errNoSentinel)
		Expect(err).To(MatchError(artifact.ErrUnsupportedForm))
		Expect(inspectRequests(reg, http.MethodGet, "manifests")).To(BeEmpty())
	})

	DescribeTable("fails a tampered manifest with ErrDigestMismatch before any blob request",
		func(tamper func(manifest []byte, m ocispec.Manifest) []byte) {
			desc, m := inspectPush(ctx, reg, spec)
			manifest := mustJSON(m)
			Expect(digest.FromBytes(manifest)).To(Equal(desc.Digest))
			reg.Tamper(desc.Digest, tamper(manifest, m))
			reg.ResetRequests()

			_, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
			expectSentinel(err, ErrDigestMismatch)
			Expect(inspectRequests(reg, "", "blobs")).To(BeEmpty())
		},
		// Each tampered manifest is a package manifest that would classify, so
		// only the digest check keeps Inspect from fetching its config.
		Entry("at the same length", func(manifest []byte, m ocispec.Manifest) []byte {
			return bytes.Replace(manifest, []byte(m.Config.Digest.Encoded()), []byte(digest.FromString("other").Encoded()), 1)
		}),
		Entry("at a different length", func(_ []byte, m ocispec.Manifest) []byte {
			m.Annotations = map[string]string{"tampered": "true"}
			return mustJSON(m)
		}),
	)

	DescribeTable("fails a tampered metadata blob with ReasonMetadataMismatch and ErrDigestMismatch",
		func(extra int) {
			_, m := inspectPush(ctx, reg, spec)
			reg.Tamper(m.Config.Digest, bytes.Repeat([]byte("x"), int(m.Config.Size)+extra))

			_, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
			expectSentinel(err, ErrDigestMismatch)
			Expect(err).To(MatchError(artifact.ErrInvalidArtifact))
			var invalid *artifact.InvalidArtifactError
			Expect(errors.As(err, &invalid)).To(BeTrue())
			Expect(invalid.Reason).To(Equal(artifact.ReasonMetadataMismatch))
		},
		Entry("at the same length", 0),
		Entry("at a different length", 1),
	)

	It("fails a package of another version with ErrUnsupportedVersion", func() {
		m := ocispec.Manifest{
			Versioned:    specs.Versioned{SchemaVersion: 2},
			MediaType:    ocispec.MediaTypeImageManifest,
			ArtifactType: "application/vnd.nvidia.nodewright.package.v2",
			Config:       descriptorOf(ocispec.MediaTypeImageConfig, []byte(`{}`)),
		}
		_, err := registrytest.PushManifest(ctx, reg.Repository(inspectRepo), m.MediaType, mustJSON(m), "1.0")
		Expect(err).NotTo(HaveOccurred())

		_, err = Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		expectSentinel(err, errNoSentinel)
		Expect(err).To(MatchError(artifact.ErrUnsupportedVersion))
	})

	It("fails metadata that breaks the schema with ReasonMetadataSchema", func() {
		spec.Uninstall = "placeholder-invalid"
		inspectPush(ctx, reg, spec)

		_, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		expectSentinel(err, errNoSentinel)
		var invalid *artifact.InvalidArtifactError
		Expect(errors.As(err, &invalid)).To(BeTrue())
		Expect(invalid.Reason).To(Equal(artifact.ReasonMetadataSchema))
	})

	It("reports an unknown tag as ErrNotFound", func() {
		inspectPush(ctx, reg, spec)
		_, err := Inspect(ctx, inspectRemote(reg, nil, "2.0", ""))
		expectSentinel(err, ErrNotFound)
	})

	DescribeTable("returns the error of a done context before any request",
		func(done func() (context.Context, context.CancelFunc), want error) {
			inspectPush(ctx, reg, spec)
			target := inspectRemote(reg, nil, "1.0", "")
			reg.ResetRequests()

			doneCtx, cancel := done()
			cancel()
			_, err := Inspect(doneCtx, target)
			Expect(err).To(BeIdenticalTo(want))
			Expect(reg.Requests()).To(BeEmpty())
		},
		Entry("cancelled", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, context.Canceled),
		Entry("past its deadline", func() (context.Context, context.CancelFunc) { return context.WithTimeout(context.Background(), 0) }, context.DeadlineExceeded),
	)

	It("gives the same Kind, Descriptor and Package from an OCI layout, pinned to the reference's name", func() {
		desc, _ := inspectPush(ctx, reg, spec)
		fromRegistry, err := Inspect(ctx, inspectRemote(reg, nil, "1.0", ""))
		Expect(err).NotTo(HaveOccurred())

		dir := GinkgoT().TempDir()
		store, err := oci.New(dir)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = registrytest.PushPackage(ctx, store, "1.0", spec)
		Expect(err).NotTo(HaveOccurred())
		ref, err := NewReference(inspectRepo, "1.0", "")
		Expect(err).NotTo(HaveOccurred())
		target, err := NewLayout(ctx, dir, ref)
		Expect(err).NotTo(HaveOccurred())

		fromLayout, err := Inspect(ctx, target)
		Expect(err).NotTo(HaveOccurred())
		Expect(fromLayout.Kind).To(Equal(fromRegistry.Kind))
		Expect(fromLayout.Descriptor).To(Equal(fromRegistry.Descriptor))
		Expect(fromLayout.Package).To(Equal(fromRegistry.Package))
		Expect(fromLayout.target).To(BeIdenticalTo(target))
		// docker.io, the reference's registry, and never its API host.
		Expect(fromLayout.Pinned).To(Equal("docker.io/" + inspectRepo + "@" + desc.Digest.String()))
		Expect(ParsePinned(fromLayout.Pinned)).To(Equal(Reference{Registry: "docker.io", Repository: inspectRepo, Digest: desc.Digest}))
	})

	Describe("against a registry that requires basic credentials", func() {
		const (
			right = `{"username":"` + inspectUser + `","password":"` + inspectPassword + `"}`
			wrong = `{"username":"` + inspectUser + `","password":"placeholder-wrong"}`
		)

		BeforeEach(func() {
			reg = registrytest.New(registrytest.Options{Username: inspectUser, Password: inspectPassword})
			DeferCleanup(reg.Close)
			inspectPush(ctx, reg, spec)
		})

		inspectWith := func(sources ...CredentialSource) ([]Credential, error) {
			ref, err := NewReference(reg.Host()+"/"+inspectRepo, "1.0", "")
			Expect(err).NotTo(HaveOccurred())
			creds, _ := LookupCredential(ref, sources)
			_, err = Inspect(ctx, inspectRemote(reg, creds, "1.0", ""))
			return creds, err
		}

		It("succeeds with the right credentials", func() {
			_, err := inspectWith(configSource("right", onlyEntry(reg.Host(), right)))
			Expect(err).NotTo(HaveOccurred())
		})

		It("gives ErrUnauthorized for wrong credentials", func() {
			_, err := inspectWith(configSource("wrong", onlyEntry(reg.Host(), wrong)))
			expectSentinel(err, ErrUnauthorized)
		})

		It("gives ErrUnauthorized for anonymous access", func() {
			_, err := inspectWith()
			expectSentinel(err, ErrUnauthorized)
			Expect(err).To(MatchError(auth.ErrBasicCredentialNotFound))
		})

		It("falls through a first source lacking the host to a second that has it", func() {
			creds, err := inspectWith(
				configSource("first", onlyEntry("other.example", right)),
				configSource("second", onlyEntry(reg.Host(), right)),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(creds).To(HaveLen(1))
			Expect(creds[0].Source()).To(Equal("second"))
		})

		It("passes a first source with wrong credentials to a later correct one, as the kubelet does", func() {
			creds, err := inspectWith(
				configSource("first", onlyEntry(reg.Host(), wrong)),
				configSource("second", onlyEntry(reg.Host(), right)),
			)
			Expect(creds).To(HaveLen(2))
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
