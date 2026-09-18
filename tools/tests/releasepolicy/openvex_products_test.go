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

package releasepolicy

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A statement whose product names no released image is the one VEX mistake
// nothing downstream can catch. The release job cannot catch it: it only knows
// its own image, so "this statement is for the other image" and "this statement
// has a typo" look identical from there, and both produce an empty projection
// that is valid OpenVEX. The scanner cannot catch it either, because grype
// matches by product purl and simply applies nothing.
//
// The set of released images is only knowable from the workflows, so the check
// lives here, where it fails in the pull request that writes the bad statement
// rather than at a release months later.

// subjectNameAssignment matches the shell line each image workflow uses to
// publish its repository to later steps, for example:
//
//	echo "subject-name=${REGISTRY}/${IMAGE_NAME}/operator" >> $GITHUB_OUTPUT
//
// The final path segment is the basename grype derives its pkg:oci product purl
// from, and the basename tools/internal/openvex binds statements against.
var subjectNameAssignment = regexp.MustCompile(`subject-name=[^"'\s]*/([A-Za-z0-9._-]+)"`)

// releasedImageBasenames derives the images the release actually publishes,
// rather than hardcoding them, so adding a third image extends the check
// instead of silently escaping it.
func releasedImageBasenames(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, workflow := range []string{operatorCIPath, agentCIPath} {
		for _, match := range subjectNameAssignment.FindAllStringSubmatch(readRepoFile(t, workflow), -1) {
			seen[match[1]] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ociPURLName mirrors parseOCIPURLName in tools/internal/openvex: strip the
// pkg:oci/ prefix, then truncate at the first separator, so a product carrying
// a digest or a repository_url qualifier still yields its bare name.
func ociPURLName(identifier string) (string, bool) {
	const prefix = "pkg:oci/"
	if !strings.HasPrefix(identifier, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(identifier, prefix)
	for _, separator := range []string{"@", "?", "#"} {
		if at := strings.Index(name, separator); at >= 0 {
			name = name[:at]
		}
	}
	return name, name != ""
}

type vexDocument struct {
	Statements []struct {
		Vulnerability struct {
			Name string `json:"name"`
		} `json:"vulnerability"`
		Products []struct {
			ID          string `json:"@id"`
			Identifiers struct {
				PURL string `json:"purl"`
			} `json:"identifiers"`
		} `json:"products"`
	} `json:"statements"`
}

func TestReleasedImageBasenamesAreDerivable(t *testing.T) {
	t.Parallel()
	names := releasedImageBasenames(t)
	if len(names) == 0 {
		t.Fatalf(`no image basenames could be derived from %s or %s.
The product check below silently passes on an empty set, so the derivation is
asserted separately. If the workflows stopped publishing subject-name through a
shell assignment, update subjectNameAssignment rather than deleting this test.`,
			operatorCIPath, agentCIPath)
	}
	t.Logf("released image basenames: %s", strings.Join(names, ", "))
}

func TestOpenVEXProductsNameAReleasedImage(t *testing.T) {
	t.Parallel()
	released := releasedImageBasenames(t)
	known := map[string]bool{}
	for _, name := range released {
		known[name] = true
	}

	var document vexDocument
	if err := json.Unmarshal([]byte(readRepoFile(t, openVEXPath)), &document); err != nil {
		t.Fatalf("parse %s: %v", openVEXPath, err)
	}

	for index, statement := range document.Statements {
		label := statement.Vulnerability.Name
		if label == "" {
			label = "<unnamed>"
		}
		// A product usually carries the same value in both @id and
		// identifiers.purl, so report each distinct problem once rather than
		// twice per product.
		reported := map[string]bool{}
		for _, product := range statement.Products {
			for _, identifier := range []string{product.ID, product.Identifiers.PURL} {
				if identifier == "" || reported[identifier] {
					continue
				}
				reported[identifier] = true
				name, ok := ociPURLName(identifier)
				if !ok {
					t.Errorf("statement %d (%s) has product identifier %q, which is not a pkg:oci purl; grype derives its product purl from the registry repository basename, so an identifier in any other shape matches nothing",
						index, label, identifier)
					continue
				}
				if !known[name] {
					t.Errorf(`statement %d (%s) names image %q, which this repository does not release.
Released images: %s
A product naming no released image suppresses nothing and reports nothing: the
projection for every image comes out empty, which is valid OpenVEX, and grype
applies the statement to no scan. Check the name against the registry
repository basename, not the org.opencontainers.image.title label.`,
						index, label, name, strings.Join(released, ", "))
				}
			}
		}
	}
}
