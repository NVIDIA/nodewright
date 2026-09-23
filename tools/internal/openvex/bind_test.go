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
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	amd64Digest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	arm64Digest   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	operatorImage = "ghcr.io/nvidia/nodewright/operator"
	agentImage    = "ghcr.io/nvidia/nodewright/agent"
)

// sourceDocument is a structurally faithful stand-in for .openvex.json: one
// statement scoped to the operator, one scoped to the agent, and the
// document-level fields the projection must carry through unchanged.
const sourceDocument = `{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://github.com/NVIDIA/nodewright/.openvex.json",
  "author": "NVIDIA NodeWright maintainers",
  "role": "document creator",
  "timestamp": "2026-09-16T00:00:00Z",
  "version": 14,
  "tooling": "manual curation",
  "statements": [
    {
      "vulnerability": {"name": "GO-2026-5942", "description": "net/http"},
      "products": [
        {"@id": "pkg:oci/operator", "identifiers": {"purl": "pkg:oci/operator"}}
      ],
      "status": "not_affected",
      "justification": "vulnerable_code_cannot_be_controlled_by_adversary",
      "impact_statement": "the listener is cluster-internal",
      "action_statement": "none required"
    },
    {
      "vulnerability": {"name": "GHSA-vjc4-5qp5-m44j"},
      "products": [
        {"@id": "pkg:oci/agent", "identifiers": {"purl": "pkg:oci/agent"}}
      ],
      "status": "not_affected",
      "justification": "component_not_present"
    }
  ]
}`

// decode renders a projection back into a tree so assertions read against
// structure rather than against formatting.
func decode(t *testing.T, document []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatalf("projection is not valid JSON: %v\n%s", err, document)
	}
	return doc
}

// statementsOf extracts the statement list from a decoded projection.
func statementsOf(t *testing.T, doc map[string]any) []any {
	t.Helper()
	statements, ok := doc["statements"].([]any)
	if !ok {
		t.Fatalf("projection statements = %T, want an array", doc["statements"])
	}
	return statements
}

// productPURLs returns the @id and identifiers.purl of every product on a
// statement, so a rewrite can be asserted on both.
func productPURLs(t *testing.T, statement any) []string {
	t.Helper()
	object, ok := statement.(map[string]any)
	if !ok {
		t.Fatalf("statement = %T, want an object", statement)
	}
	products, ok := object["products"].([]any)
	if !ok {
		t.Fatalf("statement products = %T, want an array", object["products"])
	}
	found := make([]string, 0, len(products)*2)
	for _, entry := range products {
		product, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("product = %T, want an object", entry)
		}
		id, _ := product["@id"].(string)
		found = append(found, id)
		identifiers, ok := product["identifiers"].(map[string]any)
		if !ok {
			t.Fatalf("product identifiers = %T, want an object", product["identifiers"])
		}
		purl, _ := identifiers["purl"].(string)
		found = append(found, purl)
	}
	return found
}

func TestBindSelectsAndRewritesProducts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		image       string
		digest      string
		wantKept    int
		wantDropped int
		wantPURLs   []string
	}{
		{
			name:        "operator statement binds to the operator digest",
			image:       operatorImage,
			digest:      amd64Digest,
			wantKept:    1,
			wantDropped: 1,
			wantPURLs:   []string{"pkg:oci/operator@" + amd64Digest, "pkg:oci/operator@" + amd64Digest},
		},
		{
			name:        "arm64 binds the other platform manifest",
			image:       operatorImage,
			digest:      arm64Digest,
			wantKept:    1,
			wantDropped: 1,
			wantPURLs:   []string{"pkg:oci/operator@" + arm64Digest, "pkg:oci/operator@" + arm64Digest},
		},
		{
			name:        "agent statement is the one kept for the agent image",
			image:       agentImage,
			digest:      amd64Digest,
			wantKept:    1,
			wantDropped: 1,
			wantPURLs:   []string{"pkg:oci/agent@" + amd64Digest, "pkg:oci/agent@" + amd64Digest},
		},
		{
			name:        "tagged reference resolves to the same basename",
			image:       operatorImage + ":v0.15.0",
			digest:      amd64Digest,
			wantKept:    1,
			wantDropped: 1,
			wantPURLs:   []string{"pkg:oci/operator@" + amd64Digest, "pkg:oci/operator@" + amd64Digest},
		},
		{
			name:        "image with no statements yields an empty projection",
			image:       "ghcr.io/nvidia/nodewright/charts/nodewright",
			digest:      amd64Digest,
			wantKept:    0,
			wantDropped: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := Bind([]byte(sourceDocument), Options{Image: tt.image, Digest: tt.digest})
			if err != nil {
				t.Fatalf("Bind() error = %v, want nil", err)
			}
			if result.Kept != tt.wantKept || result.Dropped != tt.wantDropped {
				t.Fatalf("Bind() kept/dropped = %d/%d, want %d/%d",
					result.Kept, result.Dropped, tt.wantKept, tt.wantDropped)
			}
			statements := statementsOf(t, decode(t, result.Document))
			if len(statements) != tt.wantKept {
				t.Fatalf("projection has %d statement(s), want %d", len(statements), tt.wantKept)
			}
			if tt.wantKept == 0 {
				return
			}
			if got := productPURLs(t, statements[0]); !reflect.DeepEqual(got, tt.wantPURLs) {
				t.Errorf("product identifiers = %q, want %q", got, tt.wantPURLs)
			}
		})
	}
}

// TestBindDropsForeignStatementsRatherThanRepointing is the rule that keeps the
// projection honest: a statement triaged against a different image must not be
// re-aimed at this digest, because that would publish a signed claim nobody
// made.
func TestBindDropsForeignStatementsRatherThanRepointing(t *testing.T) {
	t.Parallel()
	result, err := Bind([]byte(sourceDocument), Options{Image: operatorImage, Digest: amd64Digest})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	rendered := string(result.Document)
	if strings.Contains(rendered, "GHSA-vjc4-5qp5-m44j") {
		t.Error("projection carries the agent-only statement; it must be dropped, not re-pointed")
	}
	if !strings.Contains(rendered, "GO-2026-5942") {
		t.Error("projection is missing the operator statement")
	}
}

// TestBindPassesCuratedFieldsThrough covers the judgment fields: status,
// justification, impact and action statements, subcomponents and the
// vulnerability object are the curated value, so the projection must copy them
// verbatim.
func TestBindPassesCuratedFieldsThrough(t *testing.T) {
	t.Parallel()
	const source = `{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://github.com/NVIDIA/nodewright/.openvex.json",
  "author": "NVIDIA NodeWright maintainers",
  "timestamp": "2026-09-16T00:00:00Z",
  "version": 7,
  "statements": [
    {
      "vulnerability": {"name": "CVE-2026-0001", "description": "example", "aliases": ["GHSA-aaaa-bbbb-cccc"]},
      "products": [
        {
          "@id": "pkg:oci/operator",
          "identifiers": {"purl": "pkg:oci/operator"},
          "subcomponents": [{"@id": "pkg:golang/golang.org/x/net@v0.44.0"}]
        }
      ],
      "status": "affected",
      "action_statement": "upgrade to v0.16.0",
      "action_statement_timestamp": "2026-09-16T00:00:00Z"
    }
  ]
}`
	result, err := Bind([]byte(source), Options{Image: operatorImage, Digest: amd64Digest})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	doc := decode(t, result.Document)
	statement, ok := statementsOf(t, doc)[0].(map[string]any)
	if !ok {
		t.Fatal("projected statement is not an object")
	}

	sourceDoc := decode(t, []byte(source))
	sourceStatement, ok := statementsOf(t, sourceDoc)[0].(map[string]any)
	if !ok {
		t.Fatal("source statement is not an object")
	}
	for _, field := range []string{"vulnerability", "status", "action_statement", "action_statement_timestamp"} {
		if !reflect.DeepEqual(statement[field], sourceStatement[field]) {
			t.Errorf("%s = %#v, want %#v unchanged", field, statement[field], sourceStatement[field])
		}
	}

	products, _ := statement["products"].([]any)
	if len(products) != 1 {
		t.Fatalf("projected products = %d, want exactly the bound one", len(products))
	}
	product, _ := products[0].(map[string]any)
	sourceProducts, _ := sourceStatement["products"].([]any)
	sourceProduct, _ := sourceProducts[0].(map[string]any)
	if !reflect.DeepEqual(product["subcomponents"], sourceProduct["subcomponents"]) {
		t.Errorf("subcomponents = %#v, want %#v unchanged", product["subcomponents"], sourceProduct["subcomponents"])
	}

	for _, field := range []string{"@context", "author", "timestamp", "version"} {
		if !reflect.DeepEqual(doc[field], sourceDoc[field]) {
			t.Errorf("document %s = %#v, want %#v unchanged", field, doc[field], sourceDoc[field])
		}
	}
	if !bytes.Contains(result.Document, []byte(`"version": 7`)) {
		t.Errorf("version did not round-trip as an integer:\n%s", result.Document)
	}
}

// TestBindNormalizesTooling covers the one document field that is rewritten
// rather than passed through, whatever the source carried.
func TestBindNormalizesTooling(t *testing.T) {
	t.Parallel()
	const narrative = "this sentence exists only to make the source tooling field long. "
	tests := []struct {
		name    string
		tooling string
	}{
		{name: "short source value", tooling: `"tooling": "manual curation",`},
		{name: "narrative source value", tooling: `"tooling": "` + strings.Repeat(narrative, 96) + `",`},
		{name: "absent source value", tooling: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := `{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://github.com/NVIDIA/nodewright/.openvex.json",
  ` + tt.tooling + `
  "statements": []
}`
			result, err := Bind([]byte(source), Options{Image: operatorImage, Digest: amd64Digest})
			if err != nil {
				t.Fatalf("Bind() error = %v, want nil", err)
			}
			tooling, _ := decode(t, result.Document)["tooling"].(string)
			if tooling != projectionTooling {
				t.Errorf("tooling = %q, want %q", tooling, projectionTooling)
			}
			if len(tooling) >= 256 {
				t.Errorf("tooling is %d bytes, want it well under 256", len(tooling))
			}
		})
	}
}

// TestBindIsDeterministic is the reproducibility contract: the same inputs must
// produce the same signed bytes on a re-run of a release.
func TestBindIsDeterministic(t *testing.T) {
	t.Parallel()
	for _, image := range []string{operatorImage, agentImage} {
		first, err := Bind([]byte(sourceDocument), Options{Image: image, Digest: amd64Digest})
		if err != nil {
			t.Fatalf("Bind() error = %v, want nil", err)
		}
		for i := range 8 {
			again, err := Bind([]byte(sourceDocument), Options{Image: image, Digest: amd64Digest})
			if err != nil {
				t.Fatalf("Bind() error = %v on run %d, want nil", err, i)
			}
			if !bytes.Equal(first.Document, again.Document) {
				t.Fatalf("run %d for %s differs from the first:\n%s\n---\n%s",
					i, image, first.Document, again.Document)
			}
		}
	}
}

// TestBindDoesNotMutateSource guards the caller's buffer: the reviewed document
// is the source of truth and every platform binds from the same bytes.
func TestBindDoesNotMutateSource(t *testing.T) {
	t.Parallel()
	source := []byte(sourceDocument)
	original := append([]byte(nil), source...)
	if _, err := Bind(source, Options{Image: operatorImage, Digest: amd64Digest}); err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	if !bytes.Equal(source, original) {
		t.Error("Bind() mutated the source buffer")
	}
}

// TestBindEmptyStatements covers the repo's current real state: nothing
// measured is legitimately not affected, so the committed document has no
// statements and the projection must be a valid empty one rather than an error.
func TestBindEmptyStatements(t *testing.T) {
	t.Parallel()
	const source = `{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://github.com/NVIDIA/nodewright/.openvex.json",
  "author": "NVIDIA NodeWright maintainers",
  "timestamp": "2026-09-16T00:00:00Z",
  "version": 1,
  "statements": []
}`
	result, err := Bind([]byte(source), Options{Image: operatorImage, Digest: amd64Digest})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil on an empty statement list", err)
	}
	if result.Kept != 0 || result.Dropped != 0 {
		t.Errorf("kept/dropped = %d/%d, want 0/0", result.Kept, result.Dropped)
	}
	if statements := statementsOf(t, decode(t, result.Document)); len(statements) != 0 {
		t.Errorf("statements = %#v, want an empty array", statements)
	}
	if !bytes.Contains(result.Document, []byte(`"statements": []`)) {
		t.Errorf("statements did not render as an empty array:\n%s", result.Document)
	}
}

// TestBindMergesSubcomponentsAcrossMatchingProducts covers the collapse to a
// single bound product: a statement that lists the image more than once keeps
// every subcomponent, in source order, without duplicates.
func TestBindMergesSubcomponentsAcrossMatchingProducts(t *testing.T) {
	t.Parallel()
	const source = `{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://github.com/NVIDIA/nodewright/.openvex.json",
  "statements": [
    {
      "vulnerability": {"name": "CVE-2026-0002"},
      "products": [
        {"@id": "pkg:oci/operator", "subcomponents": [{"@id": "pkg:golang/a@v1"}]},
        {"@id": "pkg:oci/operator?repository_url=ghcr.io", "subcomponents": [{"@id": "pkg:golang/a@v1"}, {"@id": "pkg:golang/b@v2"}]},
        {"@id": "pkg:oci/agent", "subcomponents": [{"@id": "pkg:golang/c@v3"}]}
      ],
      "status": "not_affected",
      "justification": "component_not_present"
    }
  ]
}`
	result, err := Bind([]byte(source), Options{Image: operatorImage, Digest: amd64Digest})
	if err != nil {
		t.Fatalf("Bind() error = %v, want nil", err)
	}
	statement, ok := statementsOf(t, decode(t, result.Document))[0].(map[string]any)
	if !ok {
		t.Fatal("projected statement is not an object")
	}
	products, _ := statement["products"].([]any)
	if len(products) != 1 {
		t.Fatalf("projected products = %d, want exactly the bound one", len(products))
	}
	product, _ := products[0].(map[string]any)
	want := []any{
		map[string]any{"@id": "pkg:golang/a@v1"},
		map[string]any{"@id": "pkg:golang/b@v2"},
	}
	if !reflect.DeepEqual(product["subcomponents"], want) {
		t.Errorf("subcomponents = %#v, want %#v", product["subcomponents"], want)
	}
}

func TestBindRejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		image  string
		digest string
	}{
		{name: "empty digest", source: sourceDocument, image: operatorImage},
		{name: "short digest", source: sourceDocument, image: operatorImage, digest: "sha256:abc"},
		{
			name:   "uppercase digest",
			source: sourceDocument,
			image:  operatorImage,
			digest: "sha256:AAAA111111111111111111111111111111111111111111111111111111111111",
		},
		{name: "unprefixed digest", source: sourceDocument, image: operatorImage, digest: strings.TrimPrefix(amd64Digest, "sha256:")},
		{name: "empty image", source: sourceDocument, digest: amd64Digest},
		{name: "image with whitespace", source: sourceDocument, image: " " + operatorImage, digest: amd64Digest},
		{name: "image with no repository name", source: sourceDocument, image: "ghcr.io/nvidia/", digest: amd64Digest},
		{name: "source is not JSON", source: "not json", image: operatorImage, digest: amd64Digest},
		{name: "source is a JSON array", source: "[]", image: operatorImage, digest: amd64Digest},
		{name: "source is JSON null", source: "null", image: operatorImage, digest: amd64Digest},
		{name: "source is a JSON scalar", source: "42", image: operatorImage, digest: amd64Digest},
		{name: "source is empty", source: "", image: operatorImage, digest: amd64Digest},
		{
			name:   "document has no @id",
			source: `{"@context": "https://openvex.dev/ns/v0.2.0", "statements": []}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "statements is not an array",
			source: `{"@id": "x", "statements": {}}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "statements is missing",
			source: `{"@id": "x"}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "statement is not an object",
			source: `{"@id": "x", "statements": ["nope"]}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "statement lists no products",
			source: `{"@id": "x", "statements": [{"products": []}]}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "statement omits products",
			source: `{"@id": "x", "statements": [{"status": "not_affected"}]}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "product is not an object",
			source: `{"@id": "x", "statements": [{"products": ["pkg:oci/operator"]}]}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "trailing JSON value after the document",
			source: sourceDocument + `{"@id": "second"}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "trailing scalar after the document",
			source: sourceDocument + ` 1`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "subcomponents is not an array",
			source: `{"@id": "x", "statements": [{"products": [{"@id": "pkg:oci/operator", "subcomponents": "nope"}]}]}`,
			image:  operatorImage, digest: amd64Digest,
		},
		{
			name:   "subcomponents array has a scalar member",
			source: `{"@id": "x", "statements": [{"products": [{"@id": "pkg:oci/operator", "subcomponents": ["nope"]}]}]}`,
			image:  operatorImage, digest: amd64Digest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := Bind([]byte(tt.source), Options{Image: tt.image, Digest: tt.digest})
			if err == nil {
				t.Fatalf("Bind() error = nil, want a rejection; got %s", result.Document)
			}
		})
	}
}

func TestImageBasename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		image   string
		want    string
		wantErr bool
	}{
		{name: "nested namespace", image: operatorImage, want: "operator"},
		{name: "single namespace", image: "ghcr.io/nvidia/nodewright", want: "nodewright"},
		{name: "tagged", image: agentImage + ":v6.4.2", want: "agent"},
		{name: "digest pinned", image: operatorImage + "@" + amd64Digest, want: "operator"},
		{name: "bare name", image: "operator", want: "operator"},
		{name: "empty", image: "", wantErr: true},
		{name: "trailing slash", image: "ghcr.io/nvidia/", wantErr: true},
		{name: "leading space", image: " operator", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := imageBasename(tt.image)
			if (err != nil) != tt.wantErr {
				t.Fatalf("imageBasename(%q) error = %v, wantErr %t", tt.image, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("imageBasename(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

func TestParseOCIPURLName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		identifier string
		want       string
		wantOK     bool
	}{
		{name: "bare", identifier: "pkg:oci/operator", want: "operator", wantOK: true},
		{name: "digest qualified", identifier: "pkg:oci/operator@" + amd64Digest, want: "operator", wantOK: true},
		{
			name:       "with repository_url qualifier",
			identifier: "pkg:oci/operator?repository_url=ghcr.io/nvidia/nodewright",
			want:       "operator", wantOK: true,
		},
		{name: "non-oci purl", identifier: "pkg:golang/golang.org/x/net", wantOK: false},
		{name: "not a purl", identifier: operatorImage, wantOK: false},
		{name: "empty name", identifier: "pkg:oci/", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseOCIPURLName(tt.identifier)
			if ok != tt.wantOK {
				t.Fatalf("parseOCIPURLName(%q) ok = %t, want %t", tt.identifier, ok, tt.wantOK)
			}
			if tt.wantOK && got != tt.want {
				t.Errorf("parseOCIPURLName(%q) = %q, want %q", tt.identifier, got, tt.want)
			}
		})
	}
}

// TestBindCommittedDocument binds the file the release actually ships, so a
// change to .openvex.json that this tool cannot project fails here rather than
// mid-release.
func TestBindCommittedDocument(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile(filepath.Join(repoRoot(t), openVEXPath))
	if err != nil {
		t.Fatalf("read the committed document: %v", err)
	}
	for _, image := range []string{operatorImage, agentImage} {
		result, err := Bind(source, Options{Image: image, Digest: amd64Digest})
		if err != nil {
			t.Fatalf("Bind(%s) error = %v, want nil", image, err)
		}
		doc := decode(t, result.Document)
		if _, ok := doc["statements"].([]any); !ok {
			t.Errorf("projection for %s has statements = %T, want an array", image, doc["statements"])
		}
		if tooling, _ := doc["tooling"].(string); tooling != projectionTooling {
			t.Errorf("projection for %s has tooling = %q, want %q", image, tooling, projectionTooling)
		}
	}
}
