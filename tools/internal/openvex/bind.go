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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// ociPURLPrefix is the purl scheme and type every product identifier in
// .openvex.json uses. Product matching and rewriting both key off it.
const ociPURLPrefix = "pkg:oci/"

// projectionTooling is what every projection reports for tooling, replacing
// whatever the source carried. OpenVEX v0.2.0 defines the field as an
// identifier for what generated the document, and the projection is generated
// here. Replacing rather than passing through is deliberate: a fixed constant
// makes the value structurally independent of the committed source, so prose
// that grows in .openvex.json cannot reach a signed attestation.
const projectionTooling = "openvex bind (github.com/NVIDIA/nodewright/tools/cmd/openvex); statements curated manually"

// Options names the platform manifest a projection binds to.
type Options struct {
	// Image is the image name without tag or digest, e.g.
	// ghcr.io/nvidia/nodewright/operator. Only its basename participates in
	// product identity; the registry and namespace do not, because purl
	// pkg:oci names are repository-independent.
	Image string
	// Digest is the linux/<arch> child manifest digest, never the index
	// digest: an SBOM and its VEX describe one root filesystem.
	Digest string
}

// Result is a rendered projection plus the counts a caller logs. Kept is the
// number of source statements that named this image; Dropped is the number that
// named some other one.
type Result struct {
	Document []byte
	Kept     int
	Dropped  int
}

// Bind projects the committed OpenVEX document onto one platform manifest.
//
// Three things happen and nothing else does. Statements that do not name this
// image are dropped rather than re-pointed, because binding them to this digest
// would publish a signed claim about a product they were never triaged against.
// Statements that do name it keep their status, justification, impact
// statement, action statement, subcomponents and vulnerability object
// byte-for-byte, with products replaced by the single digest-qualified
// identifier pkg:oci/<basename>@<digest> that makes the claim verifiable
// against the manifest it ships with. The document-level tooling field is set
// to projectionTooling; every other document field passes through.
//
// The output is a pure function of (source, Image, Digest): no wall clock, no
// UUID, and encoding/json orders object keys, so re-running on the same inputs
// reproduces the same bytes. The source document's own timestamp and version
// pass through, which keeps provenance pointing back at the reviewed file
// rather than at the moment the release happened to run.
func Bind(source []byte, o Options) (*Result, error) {
	if !digestPattern.MatchString(o.Digest) {
		return nil, fmt.Errorf("digest %q must be in the form sha256:<64 lowercase hex>", o.Digest)
	}
	name, err := imageBasename(o.Image)
	if err != nil {
		return nil, fmt.Errorf("resolving the product name: %w", err)
	}

	doc, err := Decode(source)
	if err != nil {
		return nil, fmt.Errorf("reading the source document: %w", err)
	}
	sourceID, ok := NonEmptyString(doc["@id"])
	if !ok {
		return nil, errors.New("source document must set @id to a non-empty string")
	}
	statements, ok := doc["statements"].([]any)
	if !ok {
		return nil, errors.New("source document must set statements to an array")
	}

	purl := ociPURLPrefix + name + "@" + o.Digest
	kept := make([]any, 0, len(statements))
	for i, entry := range statements {
		statement, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("statement %d must be a JSON object", i)
		}
		products, ok := statement["products"].([]any)
		if !ok || len(products) == 0 {
			return nil, fmt.Errorf("statement %d must list at least one product", i)
		}
		bound, matched, bindErr := bindProducts(products, name, purl)
		if bindErr != nil {
			return nil, fmt.Errorf("statement %d: %w", i, bindErr)
		}
		if !matched {
			continue
		}
		// Copy before mutating so the decoded source tree is left intact and a
		// later failure cannot leave a half-rewritten document behind.
		projected := make(map[string]any, len(statement))
		for key, value := range statement {
			projected[key] = value
		}
		projected["products"] = []any{bound}
		kept = append(kept, projected)
	}

	// A projection is a distinct document from the file it derives from, so it
	// needs a distinct @id. Deriving it from the source @id plus the bound purl
	// keeps that identity stable across runs and self-describing about which
	// image and platform it covers.
	doc["@id"] = sourceID + "#" + name + "@" + o.Digest
	doc["statements"] = kept
	doc["tooling"] = projectionTooling

	rendered, err := encodeDocument(doc)
	if err != nil {
		return nil, fmt.Errorf("rendering the projection: %w", err)
	}
	return &Result{Document: rendered, Kept: len(kept), Dropped: len(statements) - len(kept)}, nil
}

// bindProducts returns the single digest-qualified product replacing a
// statement's product list. The bool reports whether any listed product named
// this image; false means the statement belongs to a different one and is
// dropped. Subcomponents survive the collapse: they are gathered from every
// matching product, in source order, deduplicated by encoded form.
func bindProducts(products []any, name, purl string) (map[string]any, bool, error) {
	matched := false
	subcomponents := make([]any, 0)
	seen := map[string]struct{}{}
	for _, entry := range products {
		product, ok := entry.(map[string]any)
		if !ok {
			return nil, false, errors.New("every product must be a JSON object")
		}
		if !productNames(product, name) {
			continue
		}
		matched = true
		listed, listErr := subcomponentList(product)
		if listErr != nil {
			return nil, false, fmt.Errorf("malformed product: %w", listErr)
		}
		for _, sub := range listed {
			key, err := json.Marshal(sub)
			if err != nil {
				return nil, false, fmt.Errorf("encoding a subcomponent: %w", err)
			}
			if _, duplicate := seen[string(key)]; duplicate {
				continue
			}
			seen[string(key)] = struct{}{}
			subcomponents = append(subcomponents, sub)
		}
	}
	if !matched {
		return nil, false, nil
	}
	bound := map[string]any{
		"@id":         purl,
		"identifiers": map[string]any{"purl": purl},
	}
	if len(subcomponents) > 0 {
		bound["subcomponents"] = subcomponents
	}
	return bound, true, nil
}

// subcomponentList returns a product's subcomponents, or nil when it has none.
// A present-but-malformed value is fatal rather than ignored: subcomponents
// narrow a statement to specific packages, so silently dropping one would
// publish a signed claim broader than the curated source made, and passing one
// through unchecked would sign a component that is not a component.
//
// OpenVEX v0.2.0 types every entry as a Component object, so a scalar member is
// rejected too: it marshals without complaint and would otherwise be copied
// verbatim into the signed projection.
func subcomponentList(product map[string]any) ([]any, error) {
	value, present := product["subcomponents"]
	if !present || value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("subcomponents must be an array, got %T", value)
	}
	for i, entry := range list {
		if _, ok := entry.(map[string]any); !ok {
			return nil, fmt.Errorf("subcomponents[%d] must be a JSON object, got %T", i, entry)
		}
	}
	return list, nil
}

// productNames reports whether a product identifies the image named by
// basename. Both @id and identifiers.purl are consulted because the OpenVEX
// spec makes either sufficient to identify a component.
func productNames(product map[string]any, basename string) bool {
	candidates := []any{product["@id"]}
	if identifiers, ok := product["identifiers"].(map[string]any); ok {
		candidates = append(candidates, identifiers["purl"])
	}
	for _, candidate := range candidates {
		value, ok := NonEmptyString(candidate)
		if !ok {
			continue
		}
		if parsed, ok := parseOCIPURLName(value); ok && parsed == basename {
			return true
		}
	}
	return false
}

// parseOCIPURLName pulls the package name out of a pkg:oci/... identifier,
// discarding any version (@sha256:...) or qualifier (?repository_url=...) the
// source happens to carry. A non-OCI identifier yields false.
func parseOCIPURLName(identifier string) (string, bool) {
	if !strings.HasPrefix(identifier, ociPURLPrefix) {
		return "", false
	}
	name := strings.TrimPrefix(identifier, ociPURLPrefix)
	for _, separator := range []string{"@", "?", "#"} {
		if at := strings.Index(name, separator); at >= 0 {
			name = name[:at]
		}
	}
	if name == "" {
		return "", false
	}
	return name, true
}

// imageBasename strips the registry, namespace, and any tag or digest from an
// image reference, leaving the repository name purl pkg:oci uses.
func imageBasename(image string) (string, error) {
	if image == "" || strings.TrimSpace(image) != image {
		return "", errors.New("image must be a non-empty reference with no surrounding whitespace")
	}
	trimmed := image
	if at := strings.Index(trimmed, "@"); at >= 0 {
		trimmed = trimmed[:at]
	}
	// Deliberately not path.Base: it strips trailing slashes, so
	// "ghcr.io/nvidia/" would silently yield the namespace "nvidia" as the
	// repository name. The last slash-separated segment must be present.
	base := trimmed
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[slash+1:]
	}
	if colon := strings.LastIndex(base, ":"); colon >= 0 {
		base = base[:colon]
	}
	if base == "" || base == "." {
		return "", fmt.Errorf("image %q has no repository name", image)
	}
	return base, nil
}

// encodeDocument renders the projection. encoding/json sorts map keys, so the
// byte output is stable for a given input even though the source key order is
// not preserved.
func encodeDocument(doc map[string]any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("encoding the bound document: %w", err)
	}
	return buffer.Bytes(), nil
}

// errMissingOut is fatal rather than defaulted: the projection is signed
// evidence, so where it lands is the caller's decision and not this tool's.
var errMissingOut = errors.New("-out is required")

// bindOptions is the parsed `openvex bind` command line.
type bindOptions struct {
	in     string
	out    string
	image  string
	digest string
}

// RunBind projects the committed OpenVEX document onto one platform manifest
// digest, producing the document the release attests to that manifest
// alongside its SBOM. It returns the process exit status rather than taking
// it: 0 on success, 1 on a failure to bind, 2 on an unusable command line.
//
// Why a projection and not a committed file: verification requires the VEX
// product identifier to bind to the specific manifest the claim covers, and
// that digest does not exist until the image is built. .openvex.json stays the
// reviewed source of truth with bare pkg:oci/<name> products; this rewrites
// those to pkg:oci/<name>@sha256:<platform-manifest-digest> and writes the
// result to a file the attest step feeds to cosign. The source document is
// never modified.
//
// What it does not do: no format translation, no status or justification
// mapping, no merging of scan results. Statuses, justifications, impact
// statements and subcomponents pass through untouched, because the curated
// judgment is exactly what has value and any rewrite of it is a guess. The one
// field it rewrites besides products and @id is document-level tooling, which
// names this tool rather than whatever the source carried.
//
// Output is deterministic: a pure function of the source bytes, the image name
// and the digest, with no wall clock and no UUID, so a re-run of a release
// produces byte-identical evidence.
//
// Usage:
//
//	openvex bind -in .openvex.json -out vex-linux-amd64.openvex.json \
//	  -image ghcr.io/nvidia/nodewright/operator \
//	  -digest sha256:<64 hex>
func RunBind(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("openvex bind", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var o bindOptions
	flags.StringVar(&o.in, "in", ".openvex.json", "source OpenVEX document to project")
	flags.StringVar(&o.out, "out", "", "path to write the digest-bound projection (required)")
	flags.StringVar(&o.image, "image", "", "image name without tag or digest, e.g. ghcr.io/nvidia/nodewright/operator (required)")
	flags.StringVar(&o.digest, "digest", "", "platform manifest digest to bind products to, sha256:<64 hex> (required)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if err := runBind(o, stdout); err != nil {
		fmt.Fprintf(stderr, "openvex bind: %v\n", err)
		return 1
	}
	return 0
}

// runBind reads the source document, binds it, and writes the projection. It
// reports the kept and dropped statement counts on stdout so a release log
// records which statements this platform actually received.
func runBind(o bindOptions, stdout io.Writer) error {
	if o.out == "" {
		return errMissingOut
	}
	source, err := readSource(o.in)
	if err != nil {
		return err
	}
	result, err := Bind(source, Options{Image: o.image, Digest: o.digest})
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.out, result.Document, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", o.out, err)
	}
	if _, err := fmt.Fprintf(stdout, "bound %d statement(s) to %s@%s (%d not for this image)\n",
		result.Kept, o.image, o.digest, result.Dropped); err != nil {
		return fmt.Errorf("writing the summary line: %w", err)
	}
	return nil
}

// readSource reads the OpenVEX document under a size cap. os.ReadFile would
// allocate the whole file before any check, so a path that resolves to a pipe,
// /proc or a network mount could exhaust memory.
func readSource(path string) (data []byte, err error) {
	if path == "" {
		return nil, errMissingIn
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			data, err = nil, fmt.Errorf("closing %s: %w", path, closeErr)
		}
	}()

	data, err = io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if int64(len(data)) > maxDocumentBytes {
		return nil, fmt.Errorf("%s exceeds the %d byte OpenVEX size limit", path, maxDocumentBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return data, nil
}
