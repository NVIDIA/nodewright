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

package openvex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	openVEXContext  = "https://openvex.dev/ns/v0.2.0"
	testAMD64Digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testARM64Digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	openVEXPath     = ".openvex.json"
)

// vexDocument builds a document that is valid apart from whatever the caller
// changes. extraFields is spliced in at the document level, statements is the
// literal contents of the statements array.
func vexDocument(extraFields, statements string) string {
	return `{"@context": "` + openVEXContext + `",
		"@id": "https://github.com/NVIDIA/nodewright/.openvex.json",
		"author": "NVIDIA NodeWright maintainers",
		"timestamp": "2026-09-16T00:00:00Z",
		"version": 1` + extraFields + `,
		"statements": [` + statements + `]}`
}

// A statement carrying bare product identifiers, which is what a committed
// source document looks like: nothing has been bound to a manifest yet.
const unboundStatement = `{"vulnerability": {"name": "CVE-2026-0001"},
	"products": [{"@id": "pkg:oci/operator", "identifiers": {"purl": "pkg:oci/operator"}}],
	"status": "not_affected", "justification": "component_not_present"}`

// The same statement after binding: every identifier names the platform
// manifest the claim is published against.
const boundStatement = `{"vulnerability": {"name": "CVE-2026-0001"},
	"products": [{"@id": "pkg:oci/operator@` + testAMD64Digest + `",
	"identifiers": {"purl": "pkg:oci/operator@` + testAMD64Digest + `"}}],
	"status": "not_affected", "justification": "component_not_present"}`

// repoRoot walks up from the working directory until it finds the checkout
// root, recognized by holding both `.github` and `.openvex.json`. A hardcoded
// "../.." would silently start reading the wrong tree the moment this package
// moves a level.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		_, githubErr := os.Stat(filepath.Join(dir, ".github"))
		_, vexErr := os.Stat(filepath.Join(dir, openVEXPath))
		if githubErr == nil && vexErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no repository root above the working directory: looked for a directory holding both .github and %s", openVEXPath)
		}
		dir = parent
	}
}

// validateArgs renders the command line `openvex validate` is invoked with.
// Both flags are always passed, empty values included: an empty -mode and an
// empty -digest are cases the command has to answer for, and leaving the flag
// off would exercise the default instead.
func validateArgs(mode, in, digest string) []string {
	return []string{"-mode", mode, "-in", in, "-digest", digest}
}

func writeFile(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestValidateDocuments drives the command the release step runs, so what these
// cases accept is what ships signed. Each case is a whole invocation rather
// than a call into Validate, because mode resolution and document reading are
// as much of the contract as the rules are.
func TestValidateDocuments(t *testing.T) {
	t.Parallel()

	longImpact := strings.Repeat("evidence. ", 60)
	longTooling := strings.Repeat("t", 300)

	tests := []struct {
		name string
		mode string
		// document is written to a temp file, empty content included, which is
		// how the empty-file case is expressed. Set useCommitted to validate
		// the repository's own .openvex.json instead, or missingFile to point
		// at a path nothing wrote.
		document     string
		useCommitted bool
		missingFile  bool
		digest       string
		wantErr      bool
		wantMessage  string
		note         string
	}{
		{
			name:     "source accepts an empty statements array",
			mode:     "source",
			document: vexDocument("", ""),
			note: `NodeWright deliberately diverges from the sibling implementation in NVIDIA/aicr, which requires a non-empty statements array in source mode.
An empty array asserts that there are no exceptions to declare; it does not assert the images are clean. If this case now fails, someone restored the sibling's rule, which gates every NodeWright release on having a statement to write.`,
		},
		{
			name:         "source accepts the committed document",
			mode:         "source",
			useCommitted: true,
			note:         "the committed .openvex.json must pass the validator that runs against it at release time",
		},
		{
			name:     "source accepts unbound products",
			mode:     "source",
			document: vexDocument("", unboundStatement),
			note:     "a committed source carries bare pkg:oci/<image> products by design, so the binding rule is off in source mode",
		},
		{
			name:        "source rejects a digest it would not use",
			mode:        "source",
			document:    vexDocument("", unboundStatement),
			digest:      testAMD64Digest,
			wantErr:     true,
			wantMessage: "source mode does not take -digest",
			note:        "ignoring the digest would let a caller believe the binding rule ran when this mode never applies it",
		},
		{
			name:     "projection accepts an empty projection",
			mode:     "projection",
			document: vexDocument("", ""),
			digest:   testAMD64Digest,
			note:     "a source with no statements projects to a projection with no statements, which is still a document worth publishing",
		},
		{
			name:     "projection accepts digest-bound products",
			mode:     "projection",
			document: vexDocument("", boundStatement),
			digest:   testAMD64Digest,
		},
		{
			name:        "projection rejects a product not bound to the platform digest",
			mode:        "projection",
			document:    vexDocument("", unboundStatement),
			digest:      testAMD64Digest,
			wantErr:     true,
			wantMessage: "not bound to @" + testAMD64Digest,
			note:        "a bare pkg:oci/<image> product is one no consumer can tie to a manifest; this is the failure the whole command exists to prevent",
		},
		{
			name:        "projection rejects a product bound to the other platform",
			mode:        "projection",
			document:    vexDocument("", boundStatement),
			digest:      testARM64Digest,
			wantErr:     true,
			wantMessage: "not bound to @" + testARM64Digest,
		},
		{
			name: "projection rejects an identifiers.purl left unbound",
			mode: "projection",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator@`+testAMD64Digest+`",
				"identifiers": {"purl": "pkg:oci/operator"}}],
				"status": "fixed"}`),
			digest:      testAMD64Digest,
			wantErr:     true,
			wantMessage: "not bound to @" + testAMD64Digest,
			note:        "both identifiers are consulted; binding one and leaving the other bare still publishes an identifier nothing can match",
		},
		{
			name:        "projection rejects a missing digest",
			mode:        "projection",
			document:    vexDocument("", boundStatement),
			wantErr:     true,
			wantMessage: "projection mode needs the platform digest",
			note:        "a projection that cannot be checked against a digest is exactly the case this command exists to reject",
		},
		{
			name:        "projection rejects a malformed digest",
			mode:        "projection",
			document:    vexDocument("", boundStatement),
			digest:      "sha256:not-a-digest",
			wantErr:     true,
			wantMessage: "projection mode needs the platform digest",
		},
		{
			name:        "an unknown mode is rejected rather than defaulted",
			mode:        "strict",
			document:    vexDocument("", unboundStatement),
			wantErr:     true,
			wantMessage: "unknown mode strict",
			note:        "defaulting an unrecognized mode would silently pick one of the two mode-specific behaviors, and the dangerous default is the one that skips the binding rule",
		},
		{
			name:        "an absent mode is rejected rather than defaulted",
			document:    vexDocument("", unboundStatement),
			wantErr:     true,
			wantMessage: "-mode is required",
		},
		{
			name:        "a missing file is rejected",
			mode:        "source",
			missingFile: true,
			wantErr:     true,
			wantMessage: "document not found",
		},
		{
			name:        "an empty file is rejected",
			mode:        "source",
			document:    "",
			wantErr:     true,
			wantMessage: "document is empty",
		},
		{
			name:        "a file that is not JSON is rejected",
			mode:        "source",
			document:    "not json at all",
			wantErr:     true,
			wantMessage: "is not valid JSON",
		},
		{
			name:        "a JSON array is rejected",
			mode:        "source",
			document:    "[]",
			wantErr:     true,
			wantMessage: "is not a JSON object",
			note:        "an array parses cleanly, so it has to be rejected by shape rather than by the JSON decoder",
		},
		{
			name:        "a second JSON document is rejected",
			mode:        "source",
			document:    vexDocument("", "") + `{"@id": "second"}`,
			wantErr:     true,
			wantMessage: "more than one JSON value",
			note:        "a decoder stops at the first value, so input this ambiguous would be silently truncated into signed evidence",
		},
		{
			name:        "a downgraded @context is rejected",
			mode:        "source",
			document:    strings.Replace(vexDocument("", unboundStatement), openVEXContext, "https://openvex.dev/ns/v0.0.1", 1),
			wantErr:     true,
			wantMessage: "@context must be " + openVEXContext,
			note:        "the @context equality check is what pins the spec version the enums describe",
		},
		{
			name:        "a missing @id is rejected",
			mode:        "source",
			document:    `{"@context": "` + openVEXContext + `", "author": "a", "timestamp": "t", "version": 1, "statements": []}`,
			wantErr:     true,
			wantMessage: "@id must be a non-empty string",
		},
		{
			name:        "a blank author is rejected",
			mode:        "source",
			document:    `{"@context": "` + openVEXContext + `", "@id": "x", "author": "   ", "timestamp": "t", "version": 1, "statements": []}`,
			wantErr:     true,
			wantMessage: "author must be a non-empty string",
		},
		{
			name:        "a missing timestamp is rejected",
			mode:        "source",
			document:    `{"@context": "` + openVEXContext + `", "@id": "x", "author": "a", "version": 1, "statements": []}`,
			wantErr:     true,
			wantMessage: "timestamp must be a non-empty string",
		},
		{
			name:        "a version that is a string is rejected",
			mode:        "source",
			document:    `{"@context": "` + openVEXContext + `", "@id": "x", "author": "a", "timestamp": "t", "version": "1", "statements": []}`,
			wantErr:     true,
			wantMessage: "version must be a number",
		},
		{
			name:        "statements that is not an array is rejected",
			mode:        "source",
			document:    `{"@context": "` + openVEXContext + `", "@id": "x", "author": "a", "timestamp": "t", "version": 1, "statements": {}}`,
			wantErr:     true,
			wantMessage: "statements must be an array",
		},
		{
			name:        "a document-level string over the byte bound is rejected",
			mode:        "source",
			document:    vexDocument(`, "tooling": "`+longTooling+`"`, unboundStatement),
			wantErr:     true,
			wantMessage: "over the 256 byte bound",
			note:        "document-level strings are identifiers, not prose; a sibling project shipped an 8,010 byte tooling field signed on seven images",
		},
		{
			name: "a statement-level field of several hundred bytes is accepted",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "not_affected",
				"impact_statement": "`+longImpact+`"}`),
			note: "only document-level fields are bounded: impact_statement is evidence and is expected to run to paragraphs",
		},
		{
			name:        "a statement that is not an object is rejected",
			mode:        "source",
			document:    vexDocument("", `"nope"`),
			wantErr:     true,
			wantMessage: "statement 0 must be a JSON object",
		},
		{
			name: "a statement with no vulnerability name is rejected",
			mode: "source",
			document: vexDocument("", `{"products": [{"@id": "pkg:oci/operator"}],
				"status": "fixed"}`),
			wantErr:     true,
			wantMessage: "must set vulnerability.name to a non-empty string",
		},
		{
			name: "a statement with no products is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"status": "not_affected", "justification": "component_not_present"}`),
			wantErr:     true,
			wantMessage: "must list at least one product",
			note:        "scanners match statements by products[].purl, so a statement with no products is unusable here",
		},
		{
			name: "a product with neither @id nor identifiers.purl is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"identifiers": {"cpe23": "cpe:2.3:a:nvidia:operator"}}],
				"status": "fixed"}`),
			wantErr:     true,
			wantMessage: "has a product with neither @id nor identifiers.purl",
		},
		{
			name: "subcomponents that are not an array are rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator", "subcomponents": "nope"}],
				"status": "fixed"}`),
			wantErr:     true,
			wantMessage: "subcomponents is not an array of objects",
		},
		{
			name: "a scalar subcomponent is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator", "subcomponents": ["nope"]}],
				"status": "fixed"}`),
			wantErr:     true,
			wantMessage: "subcomponents is not an array of objects",
			note:        "a scalar marshals without complaint and would be copied verbatim into the signed projection",
		},
		{
			name: "an array of subcomponent objects is accepted",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator",
				"subcomponents": [{"@id": "pkg:golang/stdlib"}]}],
				"status": "fixed"}`),
		},
		{
			name: "a status outside the four-label enum is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "probably_fine"}`),
			wantErr:     true,
			wantMessage: "is not one of not_affected, affected, fixed, under_investigation",
		},
		{
			name: "a missing status is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}]}`),
			wantErr:     true,
			wantMessage: "status null is not one of",
		},
		{
			name: "not_affected with neither justification nor impact statement is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "not_affected"}`),
			wantErr:     true,
			wantMessage: "must carry a justification or an impact_statement",
		},
		{
			name: "not_affected carrying only an impact statement is accepted",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "not_affected",
				"impact_statement": "the vulnerable path is unreachable from any entrypoint"}`),
		},
		{
			name: "a justification outside the five-label enum is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "not_affected",
				"justification": "we looked and it seemed fine"}`),
			wantErr:     true,
			wantMessage: "is not one of component_not_present",
		},
		{
			name: "a justification on a fixed statement is still enum-checked",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "fixed",
				"justification": "we looked and it seemed fine"}`),
			wantErr:     true,
			wantMessage: "is not one of component_not_present",
			note:        "a justification present at all must be one of the five labels, whatever status carries it",
		},
		{
			name: "affected without an action statement is rejected",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "affected"}`),
			wantErr:     true,
			wantMessage: "must carry an action_statement",
		},
		{
			name: "affected with an action statement is accepted",
			mode: "source",
			document: vexDocument("", `{"vulnerability": {"name": "CVE-2026-0001"},
				"products": [{"@id": "pkg:oci/operator"}], "status": "affected",
				"action_statement": "upgrade to operator v0.16.0"}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var path string
			switch {
			case test.useCommitted:
				path = filepath.Join(repoRoot(t), openVEXPath)
			case test.missingFile:
				path = filepath.Join(t.TempDir(), "absent.openvex.json")
			default:
				path = writeFile(t, filepath.Join(t.TempDir(), "openvex.json"), test.document)
			}

			var out, stderr bytes.Buffer
			code := RunValidate(validateArgs(test.mode, path, test.digest), &out, &stderr)
			output := out.String()

			if (code != 0) != test.wantErr {
				t.Fatalf("RunValidate(-mode %s -digest %q) = %d, wantErr %t\n%s\n%s\n%s",
					test.mode, test.digest, code, test.wantErr, output, stderr.String(), test.note)
			}
			if test.wantErr && code != 1 {
				t.Errorf("RunValidate(-mode %s) = %d on a rejected document, want 1", test.mode, code)
			}
			if test.wantErr && !strings.Contains(stderr.String(), "openvex validate:") {
				t.Errorf("rejection was not summarized on stderr: %q", stderr.String())
			}
			if !test.wantErr && stderr.Len() != 0 {
				t.Errorf("document was accepted but the command still wrote to stderr: %q", stderr.String())
			}
			if test.wantErr && !strings.Contains(output, test.wantMessage) {
				t.Errorf("rejection did not explain itself: want a message containing %q, got:\n%s\n%s",
					test.wantMessage, output, test.note)
			}
			if test.wantErr && !strings.Contains(output, "::error::") {
				t.Errorf("rejection was not reported as a workflow error annotation:\n%s", output)
			}
			if !test.wantErr && strings.Contains(output, "::error::") {
				t.Errorf("document was accepted but the command still logged an error:\n%s", output)
			}
		})
	}
}

// TestValidateReportsEveryProblem pins the "print every problem" behavior. A
// release that fails this check is fixed by hand, and one round trip per
// problem is a release held open.
func TestValidateReportsEveryProblem(t *testing.T) {
	t.Parallel()

	document := vexDocument("", `{"vulnerability": {"name": ""}, "products": [], "status": "probably_fine"},
		{"vulnerability": {"name": "CVE-2026-0002"},
		 "products": [{"@id": "pkg:oci/operator"}], "status": "affected"}`)
	path := writeFile(t, filepath.Join(t.TempDir(), "openvex.json"), document)

	var out bytes.Buffer
	if code := RunValidate(validateArgs("source", path, ""), &out, &bytes.Buffer{}); code == 0 {
		t.Fatalf("RunValidate() = 0, want a rejection:\n%s", out.String())
	}

	want := []string{
		"statement 0 must set vulnerability.name to a non-empty string",
		"statement 0 must list at least one product",
		`statement 0 status "probably_fine" is not one of`,
		"statement 1 is affected and must carry an action_statement",
	}
	for _, message := range want {
		if !strings.Contains(out.String(), message) {
			t.Errorf("missing problem %q in:\n%s", message, out.String())
		}
	}
	if got := strings.Count(out.String(), "::error::"); got != len(want) {
		t.Errorf("reported %d problems, want %d:\n%s", got, len(want), out.String())
	}
}

// TestValidateIsDeterministic guards the sorted document-field scan. Go
// randomizes map iteration, so an unsorted scan would reorder its own output
// between runs on one unchanged document and read as a flake, not a finding.
func TestValidateIsDeterministic(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("t", 300)
	document := vexDocument(`, "tooling": "`+long+`", "role": "`+long+`"`, "")
	path := writeFile(t, filepath.Join(t.TempDir(), "openvex.json"), document)

	var first bytes.Buffer
	if code := RunValidate(validateArgs("source", path, ""), &first, &bytes.Buffer{}); code == 0 {
		t.Fatalf("RunValidate() = 0, want a rejection:\n%s", first.String())
	}
	for i := 0; i < 20; i++ {
		var next bytes.Buffer
		if code := RunValidate(validateArgs("source", path, ""), &next, &bytes.Buffer{}); code == 0 {
			t.Fatalf("RunValidate() = 0, want a rejection:\n%s", next.String())
		}
		if next.String() != first.String() {
			t.Fatalf("output is not stable across runs:\n%s\nvs\n%s", first.String(), next.String())
		}
	}
}

// TestRunValidateRejectsAnUnknownFlag pins that a bad command line returns a
// status rather than exiting the process, which is what lets every case above
// run in-process. Parse errors are reported on the writer the caller supplied,
// not on os.Stderr.
func TestRunValidateRejectsAnUnknownFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := RunValidate([]string{"-nope"}, &stdout, &stderr); code != 2 {
		t.Errorf("RunValidate(-nope) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-nope") {
		t.Errorf("parse failure was not reported on the supplied stderr: %q", stderr.String())
	}
}
