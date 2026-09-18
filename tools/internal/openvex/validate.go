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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
)

// Mode names which of the two documents is being checked. Exactly one rule is
// mode-specific, and the modes exist only to switch it.
type Mode string

const (
	// ModeSource checks the committed .openvex.json, which carries bare
	// pkg:oci/<image> products by design, so the binding rule is switched off
	// rather than left unsatisfied.
	ModeSource Mode = "source"
	// ModeProjection checks what RunBind derived for one platform
	// manifest, where every product identifier must end in that manifest's
	// digest.
	ModeProjection Mode = "projection"
)

// maxDocumentFieldBytes bounds every document-level string. The longest
// legitimate value is a projection @id: the source URL plus
// `#<image>@sha256:<64 hex>`, 130 bytes for the longest NodeWright image name.
// 256 leaves room for roughly double that and for a tool identifier carrying a
// repository URL and a version, while staying far below the 8,010 byte
// `tooling` value that prompted the bound: in a sibling project that field grew
// into a revision changelog and shipped signed on all seven release images
// before anyone noticed (NVIDIA/aicr#2706). Every string the spec defines at
// this level (@context, @id, author, role, tooling, timestamp) is an
// identifier, not prose, so a field over the bound is prose in an identifier
// slot, which is the shape of the bug.
//
// Statement-level fields are deliberately NOT bounded: impact_statement is
// evidence and is expected to run to paragraphs. Bound the identifier fields,
// not the evidence fields.
const maxDocumentFieldBytes = 256

// Validate returns every way doc fails the OpenVEX v0.2.0 contract, in a
// deterministic order, or nil when it satisfies it. Every problem is reported
// rather than only the first: a release that fails this check is going to be
// fixed by hand, and one round trip per problem is a release held open.
//
// digest is the platform manifest every product identifier must be bound to.
// It is empty in source mode, which switches the binding rule off.
func Validate(doc map[string]any, digest string) []string {
	problems := documentProblems(doc)
	statements, _ := doc["statements"].([]any)
	for index, entry := range statements {
		problems = append(problems, statementProblems(index, entry, digest)...)
	}
	return problems
}

func documentProblems(doc map[string]any) []string {
	var problems []string
	if doc["@context"] != Context {
		problems = append(problems, fmt.Sprintf("@context must be %s, got %s", Context, asJSON(doc["@context"])))
	}
	for _, key := range []string{"@id", "author", "timestamp"} {
		if _, ok := NonEmptyString(doc[key]); !ok {
			problems = append(problems, key+" must be a non-empty string")
		}
	}
	if _, ok := doc["version"].(json.Number); !ok {
		problems = append(problems, "version must be a number")
	}
	if _, ok := doc["statements"].([]any); !ok {
		problems = append(problems, "statements must be an array")
	}
	// Sorted because Go map iteration order is randomized and a validator that
	// reorders its own output between runs on one unchanged document reads as
	// a flake rather than as a finding.
	for _, key := range slices.Sorted(maps.Keys(doc)) {
		text, ok := doc[key].(string)
		if !ok || len(text) <= maxDocumentFieldBytes {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"document field %s is %d bytes, over the %d byte bound for a document-level identifier",
			key, len(text), maxDocumentFieldBytes))
	}
	return problems
}

func statementProblems(index int, entry any, digest string) []string {
	statement, ok := entry.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("statement %d must be a JSON object", index)}
	}

	var problems []string
	report := func(format string, arguments ...any) {
		problems = append(problems, fmt.Sprintf("statement %d %s", index, fmt.Sprintf(format, arguments...)))
	}

	if _, ok := NonEmptyString(Field(statement["vulnerability"], "name")); !ok {
		report("must set vulnerability.name to a non-empty string")
	}

	// The spec marks `products` optional only because it can cascade from an
	// encapsulating format; this document defines no such product tree, and
	// grype and trivy match statements by products[].purl, so a statement
	// without products is unusable here.
	products, _ := statement["products"].([]any)
	if len(products) == 0 {
		report("must list at least one product")
	}
	if anyProduct(products, func(product any) bool { return !identifiable(product) }) {
		report("has a product with neither @id nor identifiers.purl")
	}
	if digest != "" && anyProduct(products, func(product any) bool { return !digestBound(product, digest) }) {
		report("has a product identifier not bound to @%s", digest)
	}
	if anyProduct(products, func(product any) bool { return !componentList(product) }) {
		report("has a product whose subcomponents is not an array of objects")
	}

	status := statement["status"]
	if !inEnum(Statuses, status) {
		report("status %s is not one of %s", asJSON(status), strings.Join(Statuses, ", "))
	}
	if status == "not_affected" && !anySet(statement, "justification", "impact_statement") {
		report("is not_affected and must carry a justification or an impact_statement")
	}
	if justification := statement["justification"]; justification != nil && !inEnum(Justifications, justification) {
		report("justification %s is not one of %s", asJSON(justification), strings.Join(Justifications, ", "))
	}
	if status == "affected" && !anySet(statement, "action_statement") {
		report("is affected and must carry an action_statement")
	}
	return problems
}

// identifiable reports whether a product names something a scanner can match.
// Either @id or identifiers.purl is enough: the spec makes either sufficient.
func identifiable(product any) bool {
	if _, ok := NonEmptyString(Field(product, "@id")); ok {
		return true
	}
	_, ok := NonEmptyString(Field(Field(product, "identifiers"), "purl"))
	return ok
}

// digestBound reports whether every identifier a product actually carries ends
// in the platform digest. An absent identifier passes; combined with
// identifiable above, that means every product names the manifest the
// statement is published against.
func digestBound(product any, digest string) bool {
	for _, identifier := range []any{
		Field(product, "@id"),
		Field(Field(product, "identifiers"), "purl"),
	} {
		text, ok := NonEmptyString(identifier)
		if ok && !strings.HasSuffix(text, "@"+digest) {
			return false
		}
	}
	return true
}

// componentList reports whether a product's subcomponents, if it has any, are
// an array of Component objects. OpenVEX v0.2.0 types every entry as an object,
// and a scalar member marshals without complaint, so it would otherwise be
// copied verbatim into the signed projection.
func componentList(product any) bool {
	value := Field(product, "subcomponents")
	if value == nil {
		return true
	}
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, entry := range list {
		if _, ok := entry.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func anyProduct(products []any, predicate func(any) bool) bool {
	return slices.ContainsFunc(products, predicate)
}

// anySet reports whether the statement carries at least one of the named
// fields as a non-empty string.
func anySet(statement map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := NonEmptyString(statement[key]); ok {
			return true
		}
	}
	return false
}

func inEnum(labels []string, value any) bool {
	text, ok := value.(string)
	return ok && slices.Contains(labels, text)
}

// asJSON renders a rejected value the way it appeared in the document, so the
// message distinguishes a missing field from an empty string from a number.
func asJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

// errMissingMode is fatal rather than defaulted: defaulting would silently
// pick one of the two mode-specific behaviors, and the dangerous default is
// the one that skips the binding rule.
var errMissingMode = errors.New("-mode is required; expected source or projection")

// validateOptions is the parsed `openvex validate` command line.
type validateOptions struct {
	mode   string
	in     string
	digest string
}

// RunValidate checks an OpenVEX v0.2.0 document against the contract the
// NodeWright release evidence path depends on, and fails closed rather than
// letting a release publish a VEX attestation no scanner can apply. It returns
// the process exit status rather than taking it: 0 when the document satisfies
// the contract, 1 when it does not, 2 on an unusable command line.
//
// Two documents pass through it and BOTH must be checked: the committed
// .openvex.json source, and each per-platform projection RunBind derives from
// it. Validating only the source would leave the documents that are actually
// signed unvalidated, since binding rewrites `products` on every statement it
// keeps.
//
// The rules live in one place shared by both modes so they cannot drift.
// Exactly one is mode-specific: in projection mode every product identifier
// must end in `@<platform-digest>`. That is the one failure this command exists
// to prevent, a bare pkg:oci/<image> product no consumer can tie to a manifest,
// so projection mode requires the digest rather than treating it as optional.
//
// Usage:
//
//	openvex validate -mode source     -in .openvex.json
//	openvex validate -mode projection -in amd64.openvex.json -digest sha256:<64 hex>
func RunValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("openvex validate", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var o validateOptions
	flags.StringVar(&o.mode, "mode", "", "which document is being checked: source or projection (required)")
	flags.StringVar(&o.in, "in", "", "OpenVEX document to check (required)")
	flags.StringVar(&o.digest, "digest", "", "platform manifest digest every product must be bound to, sha256:<64 hex> (required in projection mode)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if err := runValidate(o, stdout); err != nil {
		fmt.Fprintf(stderr, "openvex validate: %v\n", err)
		return 1
	}
	return 0
}

// runValidate checks one document and reports every problem it found. Problems
// are written to stdout as `::error::` lines so GitHub Actions surfaces each
// one against the step, and the returned error carries the exit status.
func runValidate(o validateOptions, stdout io.Writer) error {
	mode, digest, err := resolveMode(o)
	if err != nil {
		return annotate(stdout, err)
	}

	source, err := readDocument(mode, o.in)
	if err != nil {
		return annotate(stdout, err)
	}
	doc, err := Decode(source)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotObject):
			return annotate(stdout, fmt.Errorf("OpenVEX %s document is not a JSON object: %s", mode, o.in))
		case errors.Is(err, ErrTrailingValue):
			return annotate(stdout, fmt.Errorf("OpenVEX %s document holds more than one JSON value: %s", mode, o.in))
		default:
			return annotate(stdout, fmt.Errorf("OpenVEX %s document is not valid JSON: %s", mode, o.in))
		}
	}

	problems := Validate(doc, digest)
	if len(problems) > 0 {
		for _, problem := range problems {
			if err := emit(stdout, fmt.Sprintf("OpenVEX %s document violates the v0.2.0 contract: %s", mode, problem)); err != nil {
				return err
			}
		}
		return fmt.Errorf("%s document %s violates the OpenVEX v0.2.0 contract in %d place(s)", mode, o.in, len(problems))
	}

	if _, err := fmt.Fprintf(stdout, "OpenVEX %s document %s satisfies the v0.2.0 contract\n", mode, o.in); err != nil {
		return fmt.Errorf("writing the summary line: %w", err)
	}
	return nil
}

// resolveMode returns the mode and the digest the rules run with. An unknown
// mode is itself a failure: defaulting it would silently pick one of the two
// mode-specific behaviors, and the dangerous default is the one that skips the
// binding rule.
func resolveMode(o validateOptions) (Mode, string, error) {
	if o.in == "" {
		return "", "", errMissingIn
	}
	switch Mode(o.mode) {
	case ModeSource:
		// A committed source carries bare pkg:oci/<image> products by design,
		// so the binding rule is switched off rather than merely unsatisfied.
		// A digest handed to source mode is rejected rather than ignored: the
		// caller asked for a check this mode does not perform.
		if o.digest != "" {
			return "", "", errors.New("source mode does not take -digest; the committed document is not bound to a manifest")
		}
		return ModeSource, "", nil
	case ModeProjection:
		if !digestPattern.MatchString(o.digest) {
			return "", "", fmt.Errorf("projection mode needs the platform digest as sha256:<64 hex>, got %q", o.digest)
		}
		return ModeProjection, o.digest, nil
	case "":
		return "", "", errMissingMode
	default:
		return "", "", fmt.Errorf("unknown mode %s; expected source or projection", o.mode)
	}
}

func readDocument(mode Mode, path string) ([]byte, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("OpenVEX %s document not found: %s", mode, path)
	case err != nil:
		return nil, fmt.Errorf("OpenVEX %s document cannot be read: %s: %w", mode, path, err)
	case info.Size() > maxDocumentBytes:
		return nil, fmt.Errorf("OpenVEX %s document %s is %d bytes, over the %d byte limit", mode, path, info.Size(), maxDocumentBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("OpenVEX %s document cannot be read: %s: %w", mode, path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("OpenVEX %s document is empty: %s", mode, path)
	}
	return data, nil
}

// annotate reports err as a workflow error before returning it, so a failure
// reaches the Actions log the same way a contract violation does.
func annotate(stdout io.Writer, err error) error {
	if emitErr := emit(stdout, err.Error()); emitErr != nil {
		return emitErr
	}
	return err
}

func emit(stdout io.Writer, message string) error {
	if _, err := fmt.Fprintf(stdout, "::error::%s\n", message); err != nil {
		return fmt.Errorf("writing a workflow error annotation: %w", err)
	}
	return nil
}
