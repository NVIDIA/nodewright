/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package schema

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSchema(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Schema Suite")
}

var _ = Describe("SchemaVersion", func() {
	It("compares concrete versions numerically", func() {
		cmp, err := Compare("v1", "v2")
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(-1))

		cmp, err = Compare("v3", "v2")
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(1))

		cmp, err = Compare("v3", "v3")
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(0))
	})

	It("treats latest as greatest", func() {
		cmp, err := Compare(Latest, V1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(1))

		cmp, err = Compare(V1, Latest)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(-1))

		cmp, err = Compare(Latest, Latest)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(0))
	})

	It("returns 0 for parse-equal but string-different versions", func() {
		cmp, err := Compare("v1", "v01")
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp).To(Equal(0))
	})

	It("returns an error when one input fails to parse", func() {
		_, err := Compare("v1", "vfoo")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`"v1"`))
		Expect(err.Error()).To(ContainSubstring(`"vfoo"`))
	})

	It("returns an error when both inputs fail to parse", func() {
		_, err := Compare("vbad", "vworse")
		Expect(err).To(HaveOccurred())
	})

	It("fails parsing invalid versions", func() {
		_, err := ParseSchemaVersion("invalid")
		Expect(err).To(HaveOccurred())
	})

	It("canonicalizes parsed versions to vN form", func() {
		v, err := ParseSchemaVersion("v01")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(SchemaVersion("v1")))

		v, err = ParseSchemaVersion("V002")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal(SchemaVersion("v2")))
	})

	It("rejects non-positive versions", func() {
		_, err := ParseSchemaVersion("v0")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be >= v1"))

		_, err = ParseSchemaVersion("v-1")
		Expect(err).To(HaveOccurred())
	})

	It("finds latest from a schema list", func() {
		latest, err := LatestFrom([]SchemaVersion{"v1", "v2", "v3", Latest})
		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal(SchemaVersion("v3")))
	})

	It("returns v1 as current highest concrete schema", func() {
		highest, err := Highest()
		Expect(err).NotTo(HaveOccurred())
		Expect(highest).To(Equal(V1))
	})

	It("stringifies to its underlying value", func() {
		Expect(V1.String()).To(Equal("v1"))
		Expect(Latest.String()).To(Equal("latest"))
	})
})
