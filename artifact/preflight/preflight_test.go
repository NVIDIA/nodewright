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

package preflight

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/artifact"
)

func TestPreflight(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Preflight Suite")
}

var (
	sha       = "sha256:" + strings.Repeat("ab", 32)
	otherSHA  = "sha256:" + strings.Repeat("cd", 32)
	tagRef    = "ghcr.io/nvidia/app:1.0"
	digestRef = "ghcr.io/nvidia/app@" + sha
	pinned    = "ghcr.io/nvidia/app@" + sha
)

// document returns r as JSON with apiVersion set, without Encode.
func document(r Result) []byte {
	GinkgoHelper()
	r.APIVersion = APIVersion
	data, err := json.Marshal(r)
	Expect(err).NotTo(HaveOccurred())
	return data
}

// padded returns a valid result document for tagRef of exactly size bytes,
// padded by a field Decode does not know.
func padded(size int) []byte {
	GinkgoHelper()
	doc := map[string]any{"apiVersion": APIVersion, "reference": tagRef, "kind": KindImage, "pinned": pinned, "pad": ""}
	data, err := json.Marshal(doc)
	Expect(err).NotTo(HaveOccurred())
	doc["pad"] = strings.Repeat("x", size-len(data))
	data, err = json.Marshal(doc)
	Expect(err).NotTo(HaveOccurred())
	Expect(data).To(HaveLen(size))
	return data
}

var _ = Describe("Encode and Decode", func() {
	DescribeTable("round-trip a result of each kind",
		func(reference string, outcome Outcome) {
			r := Result{Reference: reference, Outcome: outcome, Credential: "/etc/nodewright/registry-auth/0/.dockerconfigjson", Detail: "a detail"}
			data, err := Encode(r)
			Expect(err).NotTo(HaveOccurred())
			r.APIVersion = APIVersion
			Expect(Decode(data, reference)).To(Equal(r))
		},
		Entry("an image", tagRef, Outcome{Kind: KindImage, Pinned: pinned}),
		Entry("an artifact", tagRef, Outcome{Kind: KindArtifact, Pinned: pinned, Uninstall: artifact.UninstallSupported}),
		Entry("an artifact that declares uninstall unsupported", tagRef, Outcome{Kind: KindArtifact, Pinned: pinned, Uninstall: artifact.UninstallUnsupported}),
		Entry("an artifact by digest", digestRef, Outcome{Kind: KindArtifact, Pinned: pinned, Uninstall: artifact.UninstallSupported}),
		Entry("a rejection", tagRef, Outcome{Kind: KindRejected, Reason: ReasonInvalidArtifact + "metadata-too-large"}),
		Entry("an unknown outcome", tagRef, Outcome{Kind: KindUnknown, ErrorClass: ErrorUnauthorized}),
	)

	It("cuts Detail to its bound without splitting a character", func() {
		data, err := Encode(Result{Reference: tagRef, Outcome: Outcome{Kind: KindUnknown, ErrorClass: ErrorOther}, Detail: strings.Repeat("é", maxDetailSize)})
		Expect(err).NotTo(HaveOccurred())
		r, err := Decode(data, tagRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(r.Detail)).To(BeNumerically("<=", maxDetailSize))
		Expect(len(r.Detail)).To(BeNumerically(">=", maxDetailSize-1))
		Expect(utf8.ValidString(r.Detail)).To(BeTrue())
	})

	It("leaves Detail out when the result would not fit with it", func() {
		long := "ghcr.io/" + strings.Repeat("a", MaxResultSize-maxDetailSize) + ":1.0"
		data, err := Encode(Result{Reference: long, Outcome: Outcome{Kind: KindUnknown, ErrorClass: ErrorOther}, Detail: strings.Repeat("d", maxDetailSize)})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(data)).To(BeNumerically("<=", MaxResultSize))
		r, err := Decode(data, long)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Detail).To(BeEmpty())
	})

	It("refuses to encode a result that does not fit even without Detail", func() {
		long := "ghcr.io/" + strings.Repeat("a", MaxResultSize) + ":1.0"
		_, err := Encode(Result{Reference: long, Outcome: Outcome{Kind: KindUnknown, ErrorClass: ErrorOther}})
		Expect(err).To(MatchError(ContainSubstring("exceeds the 4096 byte limit")))
	})

	It("decodes a document of exactly MaxResultSize bytes and refuses one byte more", func() {
		Expect(Decode(padded(MaxResultSize), tagRef)).To(Equal(Result{APIVersion: APIVersion, Reference: tagRef, Outcome: Outcome{Kind: KindImage, Pinned: pinned}}))
		_, err := Decode(padded(MaxResultSize+1), tagRef)
		Expect(err).To(MatchError(ErrInvalidResult))
		Expect(err).To(MatchError(ContainSubstring("exceeds the 4096 byte limit")))
	})

	DescribeTable("Decode refuses",
		func(data func() []byte, reference, why string) {
			_, err := Decode(data(), reference)
			Expect(err).To(MatchError(ErrInvalidResult))
			Expect(err).To(MatchError(ContainSubstring(why)))
		},
		Entry("an empty message", func() []byte { return nil }, tagRef, "unexpected end of JSON input"),
		Entry("text that is not JSON", func() []byte { return []byte("done") }, tagRef, "invalid character"),
		Entry("another apiVersion", func() []byte {
			return []byte(`{"apiVersion":"nodewright.nvidia.com/preflight/v2","reference":"` + tagRef + `","kind":"image","pinned":"` + pinned + `"}`)
		}, tagRef, `apiVersion "nodewright.nvidia.com/preflight/v2"`),
		Entry("another reference", func() []byte {
			return document(Result{Reference: "ghcr.io/nvidia/app:2.0", Outcome: Outcome{Kind: KindImage, Pinned: pinned}})
		}, tagRef, "is not the requested"),
		Entry("an unknown kind", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: "package", Pinned: pinned}})
		}, tagRef, `kind "package"`),
		Entry("an image with an uninstall declaration", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindImage, Pinned: pinned, Uninstall: artifact.UninstallSupported}})
		}, tagRef, "has an uninstall declaration"),
		Entry("an image with an error class", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindImage, Pinned: pinned, ErrorClass: ErrorTimeout}})
		}, tagRef, "has a reason or an error class"),
		Entry("an artifact with no uninstall declaration", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindArtifact, Pinned: pinned}})
		}, tagRef, `uninstall ""`),
		Entry("an artifact with another uninstall declaration", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindArtifact, Pinned: pinned, Uninstall: "sometimes"}})
		}, tagRef, `uninstall "sometimes"`),
		Entry("an artifact with no pinned digest", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindArtifact, Uninstall: artifact.UninstallSupported}})
		}, tagRef, "pinned: invalid image reference"),
		Entry("a pinned tag", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindImage, Pinned: tagRef}})
		}, tagRef, "pinned: invalid image reference"),
		Entry("a pinned digest of another repository", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindImage, Pinned: "ghcr.io/nvidia/other@" + sha}})
		}, tagRef, `is not of repository "ghcr.io/nvidia/app"`),
		Entry("a pinned digest other than the one requested", func() []byte {
			return document(Result{Reference: digestRef, Outcome: Outcome{Kind: KindImage, Pinned: "ghcr.io/nvidia/app@" + otherSHA}})
		}, digestRef, "is not the requested digest"),
		Entry("a rejection with no reason", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindRejected}})
		}, tagRef, "must have a reason and nothing else"),
		Entry("a rejection with a pinned digest", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindRejected, Reason: ReasonInterrupt, Pinned: pinned}})
		}, tagRef, "must have a reason and nothing else"),
		Entry("an unknown outcome with no error class", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindUnknown}})
		}, tagRef, "must have an error class and nothing else"),
		Entry("an unknown outcome with a reason", func() []byte {
			return document(Result{Reference: tagRef, Outcome: Outcome{Kind: KindUnknown, ErrorClass: ErrorTimeout, Reason: ReasonInterrupt}})
		}, tagRef, "must have an error class and nothing else"),
	)
})
