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

package packagepolicy

import (
	"reflect"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
)

// coveredPackageFields are the package fields the chart's package policy reads
// to decide what a package runs (chart/templates/_package-policy.tpl).
var coveredPackageFields = map[string]bool{
	"Image":              true,
	"ContainerSHA":       true,
	"AgentImageOverride": true,
}

var sourceLikeField = regexp.MustCompile(`(?i)image|sha|digest|artifact|registry|oci`)

// sourceLikeFields walks t's string fields, and those of nested types from this
// repository's API packages, returning the dotted paths whose names suggest
// they choose what a package runs.
func sourceLikeFields(t reflect.Type, prefix string) []string {
	var found []string
	for f := range t.Fields() {
		path := strings.TrimPrefix(prefix+"."+f.Name, ".")
		if f.Anonymous {
			path = prefix
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.String:
			if sourceLikeField.MatchString(f.Name) {
				found = append(found, path)
			}
		case ft.Kind() == reflect.Struct && strings.Contains(ft.PkgPath(), "nodewright/operator/api"):
			found = append(found, sourceLikeFields(ft, path)...)
		}
	}
	return found
}

var _ = Describe("package fields the policy must cover", func() {
	DescribeTable("every field that chooses what a package runs is read by the policy",
		func(pkg any) {
			for _, field := range sourceLikeFields(reflect.TypeOf(pkg), "") {
				Expect(coveredPackageFields).To(HaveKey(field),
					"Package.%s looks like it chooses what a package runs, but the package policy does not read it. "+
						"Cover it in chart/templates/_package-policy.tpl and docs/security/package-policy.md, then add it here.", field)
			}
		},
		Entry("nodewright.nvidia.com Package", nwv1.Package{}),
		Entry("skyhook.nvidia.com Package", v1alpha1.Package{}),
	)

	It("finds the fields it is meant to find", func() {
		Expect(sourceLikeFields(reflect.TypeOf(nwv1.Package{}), "")).To(ConsistOf("Image", "ContainerSHA", "AgentImageOverride"))
	})
})
