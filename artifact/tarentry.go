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
	"path"
	"slices"
	"strings"
)

// TarEntryName checks one entry of a payload or host-files layer and returns
// its cleaned name relative to the layer root, "." for the root directory.
// Regular files, directories and symlinks, whatever their target, are
// accepted; every other entry type is not.
func TarEntryName(hdr *tar.Header) (string, error) {
	name := hdr.Name
	switch {
	case name == "":
		return "", invalid(ReasonLayerEntry, "entry has an empty name")
	case path.IsAbs(name):
		return "", invalid(ReasonLayerEntry, "entry %q is absolute", name)
	case slices.Contains(strings.Split(name, "/"), ".."):
		return "", invalid(ReasonLayerEntry, "entry %q has a .. segment", name)
	}

	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeSymlink: //nolint:staticcheck // TypeRegA is deprecated, but old archivers still write it.
	default:
		return "", invalid(ReasonLayerEntry, "entry %q has unsupported type %q", name, hdr.Typeflag)
	}

	// path.Clean also strips a leading "./", so the root entry "./" becomes ".".
	rel := path.Clean(name)
	if rel == "." && hdr.Typeflag != tar.TypeDir {
		return "", invalid(ReasonLayerEntry, "root entry %q is not a directory", name)
	}
	return rel, nil
}
