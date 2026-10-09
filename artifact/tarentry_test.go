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
	"archive/tar"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TarEntryName", func() {
	DescribeTable("checks a layer entry",
		func(hdr tar.Header, want any) {
			name, err := TarEntryName(&hdr)
			if wantName, ok := want.(string); ok {
				Expect(err).NotTo(HaveOccurred())
				Expect(name).To(Equal(wantName))
				return
			}
			expectOutcome(err, want)
			Expect(name).To(BeEmpty())
		},
		Entry("a regular file", tar.Header{Name: "usr/bin/tool", Typeflag: tar.TypeReg}, "usr/bin/tool"),
		Entry("a legacy regular file", tar.Header{Name: "tool", Typeflag: tar.TypeRegA}, "tool"), //nolint:staticcheck // the legacy alias under test
		Entry("a directory", tar.Header{Name: "etc/conf.d/", Typeflag: tar.TypeDir}, "etc/conf.d"),
		Entry("a symlink to an absolute target", tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "/usr/lib"}, "lib"),
		Entry("a symlink that climbs", tar.Header{Name: "up", Typeflag: tar.TypeSymlink, Linkname: "../../.."}, "up"),
		Entry("a leading ./", tar.Header{Name: "./etc/motd", Typeflag: tar.TypeReg}, "etc/motd"),
		Entry("a name that needs cleaning", tar.Header{Name: "a//b/./c", Typeflag: tar.TypeReg}, "a/b/c"),
		Entry("a segment that starts with ..", tar.Header{Name: "..data", Typeflag: tar.TypeReg}, "..data"),
		Entry("the root directory", tar.Header{Name: "./", Typeflag: tar.TypeDir}, "."),

		Entry("the root as a file", tar.Header{Name: "./", Typeflag: tar.TypeReg}, ReasonLayerEntry),
		Entry("the root as a symlink", tar.Header{Name: ".", Typeflag: tar.TypeSymlink, Linkname: "/"}, ReasonLayerEntry),
		Entry("an empty name", tar.Header{Name: "", Typeflag: tar.TypeReg}, ReasonLayerEntry),
		Entry("an absolute name", tar.Header{Name: "/etc/passwd", Typeflag: tar.TypeReg}, ReasonLayerEntry),
		Entry("a leading .. segment", tar.Header{Name: "../etc/passwd", Typeflag: tar.TypeReg}, ReasonLayerEntry),
		Entry("an inner .. segment that cleans away", tar.Header{Name: "a/../b", Typeflag: tar.TypeReg}, ReasonLayerEntry),
		Entry("a trailing .. segment", tar.Header{Name: "a/..", Typeflag: tar.TypeDir}, ReasonLayerEntry),
		Entry("a hardlink", tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}, ReasonLayerEntry),
		Entry("a character device", tar.Header{Name: "dev/null", Typeflag: tar.TypeChar}, ReasonLayerEntry),
		Entry("a block device", tar.Header{Name: "dev/sda", Typeflag: tar.TypeBlock}, ReasonLayerEntry),
		Entry("a fifo", tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}, ReasonLayerEntry),
		Entry("a contiguous file", tar.Header{Name: "file", Typeflag: tar.TypeCont}, ReasonLayerEntry),
		Entry("a PAX global header", tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader}, ReasonLayerEntry),
		Entry("an unknown type", tar.Header{Name: "file", Typeflag: 'Z'}, ReasonLayerEntry),
	)
})
