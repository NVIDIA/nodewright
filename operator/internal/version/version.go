/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package version

import (
	"fmt"

	"golang.org/x/mod/semver"
)

var GIT_SHA string = "unknown"
var VERSION string = "dev"

// IsValid checks if the version is a valid semver version adds a 'v' prefix if missing
func IsValid(version string) bool {
	if version == "" {
		return false
	}
	if version[0] != 'v' {
		version = "v" + version
	}

	return semver.IsValid(version)
}

// Compare compares two versions and returns 0 if they are equal, 1 if version1 is greater than version2, -1 if version1 is less than version2
func Compare(version1, version2 string) int {
	// Empty strings panic on version[0] and are not valid for reconcile.
	if version1 == "" || version2 == "" {
		return 0
	}
	if version1[0] != 'v' {
		version1 = "v" + version1
	}
	if version2[0] != 'v' {
		version2 = "v" + version2
	}
	return semver.Compare(version1, version2)
}

func MajorMinor(version string) string {
	if version == "" {
		return ""
	}
	if version[0] != 'v' {
		version = "v" + version
	}
	return semver.MajorMinor(version)
}

// GetVersion returns the current version.
func GetVersion() string {
	return VERSION
}

// Summary returns a human-friendly representation of the build metadata.
// It shows both version and git SHA when available, or just the version if SHA is missing.
func Summary() string {
	if GIT_SHA == "" || GIT_SHA == "unknown" {
		return VERSION
	}
	return fmt.Sprintf("%s (%s)", VERSION, GIT_SHA)
}
