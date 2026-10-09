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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	familyV2 = "application/vnd.nvidia.nodewright.package.v2"
	imageCfg = "application/vnd.oci.image.config.v1+json"
	imageTar = "application/vnd.oci.image.layer.v1.tar+gzip"
)

// padTo pads a JSON document with trailing whitespace to exactly size bytes.
func padTo(doc []byte, size int) []byte {
	return append(bytes.Clone(doc), bytes.Repeat([]byte(" "), size-len(doc))...)
}

func index(mediaType, artifactType string) []byte {
	return mustJSON(ocispec.Index{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    mediaType,
		ArtifactType: artifactType,
		Manifests:    []ocispec.Descriptor{blobDescriptor(ocispec.MediaTypeImageManifest, []byte("child"))},
	})
}

// image is an ordinary container image manifest.
func image(mediaType string) ocispec.Manifest {
	return pkg(withMediaType(mediaType), withArtifactType(""),
		withConfig(layer(imageCfg, "image config")), withLayers(layer(imageTar, "rootfs")))
}

var _ = Describe("Classify", func() {
	DescribeTable("classifies a manifest",
		func(mediaType string, manifest []byte, want any) {
			kind, m, err := Classify(mediaType, manifest)
			wantKind, isKind := want.(Kind)
			if !isKind {
				expectOutcome(err, want)
				Expect(kind).To(BeEmpty())
				Expect(m).To(BeNil())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(kind).To(Equal(wantKind))
			Expect(m != nil).To(Equal(wantKind == KindPackage))
		},

		// Rule 1: unreadable bytes.
		Entry("a manifest of exactly MaxManifestSize", "", padTo(mustJSON(pkg()), MaxManifestSize), KindPackage),
		Entry("a manifest one byte over MaxManifestSize", "", padTo(mustJSON(pkg()), MaxManifestSize+1), ErrMalformedManifest),
		Entry("bytes that are not JSON", "", []byte("not json"), ErrMalformedManifest),
		Entry("a JSON null", ocispec.MediaTypeImageManifest, []byte("null"), ErrMalformedManifest),
		Entry("a JSON array", ocispec.MediaTypeImageManifest, []byte("[]"), ErrMalformedManifest),
		Entry("a field of the wrong JSON type", "", []byte(`{"mediaType":"`+ocispec.MediaTypeImageManifest+`","layers":"x"}`), ErrMalformedManifest),
		Entry("an argument that disagrees with the document", ocispec.MediaTypeImageIndex, mustJSON(pkg()), ErrMalformedManifest),
		Entry("an argument that agrees with the document", ocispec.MediaTypeImageManifest, mustJSON(pkg()), KindPackage),
		Entry("an empty argument falls back to the document", "", mustJSON(pkg()), KindPackage),
		Entry("a document without mediaType takes the argument", ocispec.MediaTypeImageManifest, mustJSON(pkg(withMediaType(""))), KindPackage),

		// Rule 2: forms other than the four manifest and index types.
		Entry("no media type anywhere", "", mustJSON(pkg(withMediaType(""))), ErrUnsupportedForm),
		Entry("a Docker schema 1 manifest", "application/vnd.docker.distribution.manifest.v1+prettyjws", []byte(`{"schemaVersion":1}`), ErrUnsupportedForm),

		// Rule 3: indexes and manifest lists.
		Entry("an OCI index with the package artifact type", "", index(ocispec.MediaTypeImageIndex, ArtifactTypePackage), ErrUnsupportedForm),
		Entry("an OCI index with a v2 family artifact type", "", index(ocispec.MediaTypeImageIndex, familyV2), ErrUnsupportedForm),
		Entry("a Docker manifest list with a family artifact type", "", index(mediaTypeDockerManifestList, ArtifactTypePackage), ErrUnsupportedForm),
		Entry("an OCI index of images", "", index(ocispec.MediaTypeImageIndex, ""), KindLegacy),
		Entry("an OCI index with a foreign artifact type", "", index(ocispec.MediaTypeImageIndex, "application/vnd.example+type"), KindLegacy),
		Entry("a Docker manifest list", "", index(mediaTypeDockerManifestList, ""), KindLegacy),

		// Rule 4: what makes a manifest a family manifest.
		Entry("an OCI image", "", mustJSON(image(ocispec.MediaTypeImageManifest)), KindLegacy),
		Entry("a Docker schema 2 image", "", mustJSON(image(mediaTypeDockerManifest)), KindLegacy),
		Entry("a legacy image is not otherwise checked", "", mustJSON(pkg(withArtifactType(""), withConfig(layer(imageCfg, "")),
			withLayers(ocispec.Descriptor{MediaType: imageTar, Digest: "bogus"}))), KindLegacy),
		Entry("the family prefix needs its trailing dot", "", mustJSON(pkg(withArtifactType("application/vnd.nvidia.nodewright.packages.v1"),
			withConfig(layer(imageCfg, "c")), withLayers(layer(imageTar, "l")))), KindLegacy),
		Entry("a family artifact type alone", "", mustJSON(pkg(withConfig(layer(imageCfg, "c")), withLayers(layer(imageTar, "l")))), ReasonMissingMetadata),
		Entry("a family config alone", "", mustJSON(pkg(withArtifactType(""))), KindPackage),
		Entry("family layers alone, as oras push without --config writes", "", mustJSON(pkg(withArtifactType(""),
			withConfig(ocispec.DescriptorEmptyJSON))), ReasonMissingMetadata),

		// Rule 5: versions and the Docker form.
		Entry("a v2 artifact type", "", mustJSON(pkg(withArtifactType(familyV2))), ErrUnsupportedVersion),
		Entry("a v2 config", "", mustJSON(pkg(withConfig(layer("application/vnd.nvidia.nodewright.package.config.v2+json", "c")))), ErrUnsupportedVersion),
		Entry("a v2 layer in an otherwise valid package", "", mustJSON(pkg(withLayers(payloadLayer("p"),
			layer("application/vnd.nvidia.nodewright.package.payload.v2.tar+gzip", "p2")))), ErrUnsupportedVersion),
		Entry("a v2 layer in an image", "", mustJSON(pkg(withArtifactType(""), withConfig(layer(imageCfg, "c")),
			withLayers(layer("application/vnd.nvidia.nodewright.package.hostfiles.v2.tar+gzip", "h")))), ErrUnsupportedVersion),
		Entry("the first version segment wins", "", mustJSON(pkg(withLayers(payloadLayer("p"),
			layer("application/vnd.nvidia.nodewright.package.payload.v10.v1.tar+gzip", "p2")))), ErrUnsupportedVersion),
		Entry("a +suffix is not a version", "", mustJSON(pkg(withLayers(payloadLayer("p"),
			layer("application/vnd.nvidia.nodewright.package.payload.v1.tar+v2", "p2")))), ReasonMediaType),
		Entry("a Docker schema 2 package", "", mustJSON(pkg(withMediaType(mediaTypeDockerManifest))), ErrUnsupportedForm),
		Entry("a Docker schema 2 manifest with a v2 family type", "", mustJSON(pkg(withMediaType(mediaTypeDockerManifest),
			withArtifactType(familyV2))), ErrUnsupportedVersion),

		// Rule 6: invalid packages, in reason order.
		Entry("a config that is not the metadata", "", mustJSON(pkg(withConfig(layer(imageCfg, "c")))), ReasonMissingMetadata),
		Entry("a config of an unknown family type", "", mustJSON(pkg(withConfig(layer("application/vnd.nvidia.nodewright.package.other.v1+json", "c")))),
			ReasonMissingMetadata),
		Entry("the metadata type as artifact type", "", mustJSON(pkg(withArtifactType(MediaTypeMetadata))), ReasonArtifactType),
		Entry("a foreign artifact type", "", mustJSON(pkg(withArtifactType("application/vnd.example+type"))), ReasonArtifactType),
		Entry("a layer of an unknown v1 family type", "", mustJSON(pkg(withLayers(payloadLayer("p"),
			layer("application/vnd.nvidia.nodewright.package.extras.v1.tar+gzip", "x")))), ReasonMediaType),
		Entry("a layer of a versionless family type", "", mustJSON(pkg(withLayers(payloadLayer("p"),
			layer("application/vnd.nvidia.nodewright.package.payload.tar+gzip", "x")))), ReasonMediaType),
		Entry("a layer of a family type with a non-numeric version", "", mustJSON(pkg(withLayers(payloadLayer("p"),
			layer("application/vnd.nvidia.nodewright.package.payload.vx.tar+gzip", "x")))), ReasonMediaType),
		Entry("a layer of a foreign type", "", mustJSON(pkg(withLayers(payloadLayer("p"), layer(imageTar, "x")))), ReasonMediaType),
		Entry("the metadata type as a layer", "", mustJSON(pkg(withLayers(payloadLayer("p"), layer(MediaTypeMetadata, "x")))), ReasonMediaType),
		Entry("a media-type fault outranks an earlier descriptor fault", "", mustJSON(pkg(withLayers(
			ocispec.Descriptor{MediaType: MediaTypePayload, Digest: "bogus", Size: 1}, layer(imageTar, "x")))), ReasonMediaType),
		Entry("a config digest that is not a digest", "", mustJSON(pkg(withConfig(ocispec.Descriptor{
			MediaType: MediaTypeMetadata, Digest: "sha256:abc", Size: 1}))), ReasonDescriptor),
		Entry("a layer digest of an unknown algorithm", "", mustJSON(pkg(withLayers(ocispec.Descriptor{
			MediaType: MediaTypePayload, Digest: "md5:d41d8cd98f00b204e9800998ecf8427e", Size: 1}))), ReasonDescriptor),
		Entry("a layer without a digest", "", mustJSON(pkg(withLayers(ocispec.Descriptor{MediaType: MediaTypePayload, Size: 1}))), ReasonDescriptor),
		Entry("a sha512 layer digest", "", mustJSON(pkg(withLayers(ocispec.Descriptor{
			MediaType: MediaTypePayload, Digest: digest.SHA512.FromString("p"), Size: 1}))), KindPackage),
		Entry("a layer of size zero", "", mustJSON(pkg(withLayers(ocispec.Descriptor{
			MediaType: MediaTypePayload, Digest: digest.FromString(""), Size: 0}))), ReasonDescriptor),
		Entry("a config of negative size", "", mustJSON(pkg(withConfig(ocispec.Descriptor{
			MediaType: MediaTypeMetadata, Digest: digest.FromString("c"), Size: -1}))), ReasonDescriptor),
		Entry("metadata of exactly MaxMetadataSize", "", mustJSON(pkg(withConfig(ocispec.Descriptor{
			MediaType: MediaTypeMetadata, Digest: digest.FromString("c"), Size: MaxMetadataSize}))), KindPackage),
		Entry("metadata one byte over MaxMetadataSize", "", mustJSON(pkg(withConfig(ocispec.Descriptor{
			MediaType: MediaTypeMetadata, Digest: digest.FromString("c"), Size: MaxMetadataSize + 1}))), ReasonMetadataTooLarge),
		Entry("no layers", "", mustJSON(pkg(withLayers())), ReasonNoPayload),
		Entry("host files but no payload", "", mustJSON(pkg(withLayers(hostFilesLayer("h", "/opt/x")))), ReasonNoPayload),
		Entry("a host-files layer without a destination", "", mustJSON(pkg(withLayers(payloadLayer("p"), layer(MediaTypeHostFiles, "h")))),
			ReasonDestination),
		Entry("an empty destination", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "")))), ReasonDestination),
		Entry("a relative destination", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "opt/x")))), ReasonDestination),
		Entry("the root as destination", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/")))), ReasonDestination),
		Entry("a destination with a trailing slash", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/opt/x/")))), ReasonDestination),
		Entry("a destination with an empty segment", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/opt//x")))), ReasonDestination),
		Entry("a destination with a . segment", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/opt/./x")))), ReasonDestination),
		Entry("a destination with a .. segment", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/opt/../etc")))), ReasonDestination),
		Entry("a destination that climbs above the root", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/../etc")))), ReasonDestination),
		Entry("a payload layer with a destination", "", mustJSON(pkg(withLayers(layer(MediaTypePayload, "p", AnnotationDestination, "/opt/x")))),
			ReasonDestination),
		Entry("a clean absolute destination", "", mustJSON(pkg(withLayers(payloadLayer("p"), hostFilesLayer("h", "/opt/x")))), KindPackage),

		// What Classify ignores.
		Entry("the oras title annotation on every layer", "", mustJSON(pkg(withLayers(
			layer(MediaTypePayload, "p", ocispec.AnnotationTitle, "payload.tar.gz"),
			layer(MediaTypeHostFiles, "h", ocispec.AnnotationTitle, "hostfiles.tar.gz", AnnotationDestination, "/opt/x")))), KindPackage),
		Entry("manifest annotations", "", mustJSON(pkg(func(m *ocispec.Manifest) {
			m.Annotations = map[string]string{AnnotationDestination: "relative", ocispec.AnnotationCreated: "yesterday"}
		})), KindPackage),
		Entry("unknown manifest fields", "", append([]byte(`{"futureField":{"nested":[1]},`), mustJSON(pkg())[1:]...), KindPackage),
	)

	It("returns a package whose fields follow the manifest and metadata", func() {
		blob := []byte(`{"uninstall":"unsupported","config":{"schema_version":"v1","modes":{}},` +
			`"interrupt":{"sense":{"path":"interrupt/sense.sh","arguments":[],"on_host":true},` +
			`"run":{"path":"interrupt/run.sh","arguments":["--now"],"on_host":false}}}`)
		config := blobDescriptor(MediaTypeMetadata, blob)
		p1 := layer(MediaTypePayload, "p1", ocispec.AnnotationTitle, "p1.tar.gz")
		h1 := hostFilesLayer("h1", "/etc/nodewright")
		p2 := payloadLayer("p2")
		h2 := hostFilesLayer("h2", "/opt/tools")
		annotations := map[string]string{ocispec.AnnotationVersion: "1.2.3"}

		kind, m, err := Classify("", mustJSON(pkg(withConfig(config), withLayers(p1, h1, p2, h2), func(m *ocispec.Manifest) {
			m.Annotations = annotations
		})))
		Expect(err).NotTo(HaveOccurred())
		Expect(kind).To(Equal(KindPackage))
		Expect(m.Config()).To(Equal(config))

		got, err := m.ParseMetadata(blob)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Uninstall).To(Equal(UninstallUnsupported))
		Expect(got.Config).To(MatchJSON(`{"schema_version":"v1","modes":{}}`))
		Expect(got.Payload).To(Equal([]ocispec.Descriptor{p1, p2}))
		Expect(got.HostFiles).To(Equal([]HostFileLayer{
			{Descriptor: h1, Destination: "/etc/nodewright"},
			{Descriptor: h2, Destination: "/opt/tools"},
		}))
		Expect(got.Interrupt).To(Equal(&Interrupt{
			Sense: InterruptStep{Path: "interrupt/sense.sh", Arguments: []string{}, OnHost: true},
			Run:   InterruptStep{Path: "interrupt/run.sh", Arguments: []string{"--now"}, OnHost: false},
		}))
		Expect(got.ConfigDescriptor).To(Equal(config))
		Expect(got.Annotations).To(Equal(annotations))
	})

	It("leaves Interrupt nil when the metadata declares none", func() {
		_, m, err := Classify("", mustJSON(pkg()))
		Expect(err).NotTo(HaveOccurred())
		got, err := m.ParseMetadata(validMetadata)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Uninstall).To(Equal(UninstallSupported))
		Expect(got.Interrupt).To(BeNil())
	})
})
