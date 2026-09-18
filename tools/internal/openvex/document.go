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

// Package openvex implements the two halves of the release evidence path for
// the repository's OpenVEX document: binding it to one platform manifest, and
// checking it against the v0.2.0 contract. Both run over the same two
// documents in the same release step, so they live in one package and share
// one idea of what an OpenVEX document is; a divergence would mean the bytes
// that were checked are not the bytes that were bound.
//
// This file holds what both halves agree on. RunBind and RunValidate in
// bind.go and validate.go are the entry points tools/cmd/openvex dispatches
// to; neither calls os.Exit, so both are directly testable.
package openvex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Context is the pinned OpenVEX namespace. Equality against it is what pins
// the spec version: it is a local, network-free way to assert the enums below
// still describe the document. Fetching a schema or installing a validator
// would add a new failure mode at the point in a release where a failure is
// most expensive.
const Context = "https://openvex.dev/ns/v0.2.0"

// digestPattern is the only accepted form for a platform manifest digest,
// shared by both halves because they have to agree on it: the binder refuses
// to produce a document bound to a malformed digest, and the validator refuses
// to check one against it. Either way a bad digest stops the release rather
// than yielding a VEX bound to nothing.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// maxDocumentBytes caps every document read. The committed source is a few KB
// of hand-curated statements and a projection is smaller still; a megabyte is
// far past any plausible growth and well inside what a release runner can hold.
const maxDocumentBytes int64 = 1 << 20

// errMissingIn is shared because both subcommands name the document they read
// with the same flag.
var errMissingIn = errors.New("-in is required")

// Statuses is the v0.2.0 statement status enum.
var Statuses = []string{"not_affected", "affected", "fixed", "under_investigation"}

// Justifications is the v0.2.0 justification enum, valid only on a
// not_affected statement.
var Justifications = []string{
	"component_not_present",
	"vulnerable_code_not_present",
	"vulnerable_code_not_in_execute_path",
	"vulnerable_code_cannot_be_controlled_by_adversary",
	"inline_mitigations_already_exist",
}

// Decode's failure modes, distinguished so a caller can report which one it
// hit rather than printing one message for every kind of unusable input.
var (
	ErrNotJSON       = errors.New("not valid JSON")
	ErrNotObject     = errors.New("not a JSON object")
	ErrTrailingValue = errors.New("must hold exactly one JSON value")
)

// Decode parses an OpenVEX document into a generic tree.
//
// UseNumber is set so integer fields such as version re-encode as the literal
// they arrived as, not as a float.
func Decode(source []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotJSON, err)
	}
	doc, ok := value.(map[string]any)
	if !ok || doc == nil {
		return nil, ErrNotObject
	}
	// json.Decoder stops at the end of the first value, so a document followed
	// by a second one would be silently truncated to the first. Input that
	// ambiguous must not become signed evidence.
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrTrailingValue
	}
	return doc, nil
}

// NonEmptyString reports whether value is a string with non-whitespace
// content, which is what both tools mean by a field being set.
func NonEmptyString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// Field returns object[key], or nil when object is not a JSON object. It keeps
// a caller from having to type-assert at every level of a path that may not
// exist in a malformed document.
func Field(object any, key string) any {
	mapping, ok := object.(map[string]any)
	if !ok {
		return nil
	}
	return mapping[key]
}
