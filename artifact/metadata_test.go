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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type metadataFixture struct {
	description string
	blob        string
	// want is nil when the metadata is valid, else the Reason it fails with.
	want any
}

// interrupt renders metadata whose interrupt declares sense as given and a
// valid run step.
func interrupt(sense string) string {
	return `{"uninstall":"supported","config":{},"interrupt":{"sense":` + sense +
		`,"run":{"path":"run.sh","arguments":[],"on_host":true}}}`
}

func sensePath(path string) string {
	return interrupt(fmt.Sprintf(`{"path":%q,"arguments":[],"on_host":false}`, path))
}

// metadataFixtures are documents whose descriptor matches, so only their
// content decides the outcome.
var metadataFixtures = []metadataFixture{
	{"minimal metadata", `{"uninstall":"supported","config":{}}`, nil},
	{"an unsupported uninstall", `{"uninstall":"unsupported","config":{}}`, nil},
	{"config contents are unconstrained", `{"uninstall":"supported","config":{"modes":[1,{"x":null}],"extra":true}}`, nil},
	{"an interrupt with empty arguments", interrupt(`{"path":"sense.sh","arguments":[],"on_host":false}`), nil},
	{"an interrupt with arguments", interrupt(`{"path":"bin/sense","arguments":["-v","--all"],"on_host":true}`), nil},
	{"metadata of exactly MaxMetadataSize", string(padTo(validMetadata, MaxMetadataSize)), nil},
	{"a step path in a hidden directory", sensePath(".hidden/sense.sh"), nil},
	{"a step path segment of three dots", sensePath("a/.../sense"), nil},
	{"a step path segment that starts with ..", sensePath("..sense"), nil},

	{"bytes that are not JSON", `not json`, ReasonMalformedMetadata},
	{"truncated JSON", `{"uninstall":"supported"`, ReasonMalformedMetadata},
	{"trailing data after the object", `{"uninstall":"supported","config":{}} {}`, ReasonMalformedMetadata},
	{"a JSON array", `[]`, ReasonMalformedMetadata},
	{"a JSON string", `"metadata"`, ReasonMalformedMetadata},
	{"a JSON null", `null`, ReasonMalformedMetadata},

	{"no uninstall", `{"config":{}}`, ReasonMetadataSchema},
	{"an unknown uninstall value", `{"uninstall":"maybe","config":{}}`, ReasonMetadataSchema},
	{"a boolean uninstall", `{"uninstall":true,"config":{}}`, ReasonMetadataSchema},
	{"no config", `{"uninstall":"supported"}`, ReasonMetadataSchema},
	{"a config array", `{"uninstall":"supported","config":[]}`, ReasonMetadataSchema},
	{"a null config", `{"uninstall":"supported","config":null}`, ReasonMetadataSchema},
	{"a string config", `{"uninstall":"supported","config":"{}"}`, ReasonMetadataSchema},
	{"an unknown top-level field", `{"uninstall":"supported","config":{},"extra":1}`, ReasonMetadataSchema},
	{"a top-level field in another case", `{"Uninstall":"supported","config":{}}`, ReasonMetadataSchema},
	{"a null interrupt", `{"uninstall":"supported","config":{},"interrupt":null}`, ReasonMetadataSchema},
	{"an interrupt without sense", `{"uninstall":"supported","config":{},"interrupt":{"run":{"path":"r","arguments":[],"on_host":true}}}`,
		ReasonMetadataSchema},
	{"an interrupt without run", `{"uninstall":"supported","config":{},"interrupt":{"sense":{"path":"s","arguments":[],"on_host":true}}}`,
		ReasonMetadataSchema},
	{"an unknown interrupt field", strings.Replace(interrupt(`{"path":"s","arguments":[],"on_host":true}`), `"sense"`, `"extra":{},"sense"`, 1),
		ReasonMetadataSchema},
	{"a step without path", interrupt(`{"arguments":[],"on_host":true}`), ReasonMetadataSchema},
	{"a step without arguments", interrupt(`{"path":"s","on_host":true}`), ReasonMetadataSchema},
	{"a step without on_host", interrupt(`{"path":"s","arguments":[]}`), ReasonMetadataSchema},
	{"an unknown step field", interrupt(`{"path":"s","arguments":[],"on_host":true,"env":{}}`), ReasonMetadataSchema},
	{"arguments that are not an array", interrupt(`{"path":"s","arguments":"-v","on_host":true}`), ReasonMetadataSchema},
	{"a non-string argument", interrupt(`{"path":"s","arguments":[1],"on_host":true}`), ReasonMetadataSchema},
	{"a string on_host", interrupt(`{"path":"s","arguments":[],"on_host":"true"}`), ReasonMetadataSchema},
	{"an empty step path", sensePath(""), ReasonMetadataSchema},
	{"an absolute step path", sensePath("/bin/sense"), ReasonMetadataSchema},
	{"a step path with a trailing slash", sensePath("bin/"), ReasonMetadataSchema},
	{"a step path with an empty segment", sensePath("bin//sense"), ReasonMetadataSchema},
	{"a step path of .", sensePath("."), ReasonMetadataSchema},
	{"a step path of ..", sensePath(".."), ReasonMetadataSchema},
	{"a step path with a leading ./", sensePath("./sense"), ReasonMetadataSchema},
	{"a step path with a . segment", sensePath("bin/./sense"), ReasonMetadataSchema},
	{"a step path with a leading ..", sensePath("../sense"), ReasonMetadataSchema},
	{"a step path with a trailing ..", sensePath("bin/.."), ReasonMetadataSchema},
	{"a step path with an inner ..", sensePath("bin/../sense"), ReasonMetadataSchema},
}

// parseWith classifies a package whose config descriptor is config and parses
// blob against it.
func parseWith(config ocispec.Descriptor, blob []byte) (*Package, error) {
	GinkgoHelper()
	_, m, err := Classify("", mustJSON(pkg(withConfig(config))))
	Expect(err).NotTo(HaveOccurred())
	return m.ParseMetadata(blob)
}

func parse(blob []byte) (*Package, error) {
	GinkgoHelper()
	return parseWith(blobDescriptor(MediaTypeMetadata, blob), blob)
}

var _ = Describe("ParseMetadata", func() {
	fixtureEntries := make([]TableEntry, 0, len(metadataFixtures))
	for _, f := range metadataFixtures {
		fixtureEntries = append(fixtureEntries, Entry(f.description, f.blob, f.want))
	}
	DescribeTable("parses metadata whose descriptor matches",
		func(blob string, want any) {
			got, err := parse([]byte(blob))
			expectOutcome(err, want)
			Expect(got == nil).To(Equal(want != nil))
		},
		fixtureEntries,
	)

	DescribeTable("checks the blob against its descriptor",
		func(config ocispec.Descriptor, blob []byte, want any) {
			_, err := parseWith(config, blob)
			expectOutcome(err, want)
		},
		Entry("a blob one byte over MaxMetadataSize",
			ocispec.Descriptor{MediaType: MediaTypeMetadata, Digest: digest.FromString("c"), Size: MaxMetadataSize},
			padTo(validMetadata, MaxMetadataSize+1), ReasonMetadataTooLarge),
		Entry("a blob longer than its descriptor",
			blobDescriptor(MediaTypeMetadata, validMetadata), append(bytes.Clone(validMetadata), ' '), ReasonMetadataMismatch),
		Entry("a blob of the right length and the wrong digest",
			blobDescriptor(MediaTypeMetadata, validMetadata), bytes.Replace(validMetadata, []byte("supported"), []byte("SUPPORTED"), 1),
			ReasonMetadataMismatch),
		Entry("a blob under a sha512 descriptor",
			ocispec.Descriptor{MediaType: MediaTypeMetadata, Digest: digest.SHA512.FromBytes(validMetadata), Size: int64(len(validMetadata))},
			validMetadata, nil),
	)

	It("agrees with the embedded schema on every metadata fixture", func() {
		schema, err := compileMetadataSchema()
		Expect(err).NotTo(HaveOccurred())
		for _, f := range metadataFixtures {
			doc, err := jsonschema.UnmarshalJSON(strings.NewReader(f.blob))
			schemaAccepts := err == nil && schema.Validate(doc) == nil
			_, err = parse([]byte(f.blob))
			Expect(err == nil).To(Equal(schemaAccepts), f.description)
		}
	})
})
