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
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const specDocPath = "../docs/user-guide/package-artifact-spec.md"

// specExample is a fenced block of the specification page marked as an
// example by its info string: "json valid", "json invalid <reason>" or
// "json manifest".
type specExample struct {
	line   int
	marker string
	// want is, for a metadata example, nil when it is valid, else the Reason
	// it fails with.
	want any
	body []byte
}

func (e specExample) String() string {
	return fmt.Sprintf("the %s example at line %d", e.marker, e.line)
}

// specExamples returns the marked fenced blocks of doc. Any info string
// beyond a bare language must be a marker, so a mistyped marker fails the
// spec rather than silently dropping its example.
func specExamples(doc []byte) []specExample {
	GinkgoHelper()
	var examples []specExample
	var current *specExample
	inFence := false
	for i, line := range strings.Split(string(doc), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inFence && trimmed == "```":
			if current != nil {
				examples = append(examples, *current)
				current = nil
			}
			inFence = false
		case inFence:
			if current != nil {
				current.body = append(current.body, line+"\n"...)
			}
		case strings.HasPrefix(trimmed, "```"):
			inFence = true
			info := strings.Fields(strings.TrimPrefix(trimmed, "```"))
			if len(info) < 2 {
				continue
			}
			current = &specExample{line: i + 1, marker: info[1]}
			switch {
			case info[0] == "json" && len(info) == 2 && (info[1] == "valid" || info[1] == "manifest"):
			case info[0] == "json" && len(info) == 3 && info[1] == "invalid":
				current.want = Reason(info[2])
			default:
				Fail(fmt.Sprintf("line %d: unknown example marker %q", i+1, strings.Join(info, " ")))
			}
		}
	}
	Expect(inFence).To(BeFalse(), "the page ends inside a fenced block")
	return examples
}

var _ = Describe("the specification page", func() {
	var examples []specExample

	BeforeEach(func() {
		doc, err := os.ReadFile(specDocPath)
		Expect(err).NotTo(HaveOccurred())
		examples = specExamples(doc)
	})

	It("marks at least one valid, one invalid and one manifest example", func() {
		markers := map[string]bool{}
		for _, ex := range examples {
			markers[ex.marker] = true
		}
		Expect(markers).To(Equal(map[string]bool{"valid": true, "invalid": true, "manifest": true}))
	})

	It("gives each metadata example the verdict ParseMetadata returns", func() {
		for _, ex := range examples {
			if ex.marker != "manifest" {
				By(ex.String())
				_, err := parse(ex.body)
				expectOutcome(err, ex.want)
			}
		}
	})

	It("gives each metadata example the verdict the embedded schema returns", func() {
		schema, err := compileMetadataSchema()
		Expect(err).NotTo(HaveOccurred())
		for _, ex := range examples {
			if ex.marker != "manifest" {
				doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ex.body))
				schemaAccepts := err == nil && schema.Validate(doc) == nil
				Expect(schemaAccepts).To(Equal(ex.marker == "valid"), ex.String())
			}
		}
	})

	It("gives manifest examples that classify as packages", func() {
		for _, ex := range examples {
			if ex.marker == "manifest" {
				kind, m, err := Classify("", ex.body)
				Expect(err).NotTo(HaveOccurred(), ex.String())
				Expect(kind).To(Equal(KindPackage), ex.String())
				Expect(m).NotTo(BeNil(), ex.String())
			}
		}
	})
})
