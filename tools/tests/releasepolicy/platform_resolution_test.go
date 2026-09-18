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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testIndexDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testAMD64Digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testARM64Digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	testSubjectName = "ghcr.io/nvidia/nodewright/operator"
)

// TestPlatformResolutionFailsClosed drives the real resolution script with a
// fake crane. Every case here is one where crane itself is happy and the
// resolution is still wrong, which is why the script checks rather than trusts:
// a plain manifest pushed instead of an index answers `crane digest --platform`
// with its own digest and exit 0, and the action would then republish the exact
// bug it exists to fix, quietly.
func TestPlatformResolutionFailsClosed(t *testing.T) {
	t.Parallel()
	steps := loadAction(t, attestActionPath).Runs.Steps
	resolve := steps.named(t, "Resolve per-platform digests")
	if resolve.ID != "platforms" {
		t.Fatalf("platform resolution step id = %q, want platforms (the action's outputs read steps.platforms)", resolve.ID)
	}
	if strings.TrimSpace(resolve.Run) == "" {
		t.Fatal("could not extract the platform resolution script from the action")
	}

	tests := []struct {
		name string
		// platforms is what `crane manifest` reports the index ships. Empty
		// means the two platforms this action covers, which is what every case
		// about digest resolution assumes.
		platforms []string
		amd64     string
		arm64     string
		wantErr   bool
	}{
		{
			name:  "a well-formed distinct pair resolves",
			amd64: testAMD64Digest,
			arm64: testARM64Digest,
		},
		{
			name:      "an attestation manifest is not mistaken for a platform",
			platforms: []string{"linux/amd64", "linux/arm64", "unknown/unknown"},
			amd64:     testAMD64Digest,
			arm64:     testARM64Digest,
		},
		{
			name:      "an index shipping a platform this action does not attest is rejected",
			platforms: []string{"linux/amd64", "linux/arm64", "linux/ppc64le"},
			amd64:     testAMD64Digest,
			arm64:     testARM64Digest,
			wantErr:   true,
		},
		{
			name:    "amd64 resolving to the index means the reference is a plain manifest",
			amd64:   testIndexDigest,
			arm64:   testARM64Digest,
			wantErr: true,
		},
		{
			name:    "arm64 resolving to the index means the reference is a plain manifest",
			amd64:   testAMD64Digest,
			arm64:   testIndexDigest,
			wantErr: true,
		},
		{
			name:    "both platforms resolving alike means one was lost",
			amd64:   testAMD64Digest,
			arm64:   testAMD64Digest,
			wantErr: true,
		},
		{
			name:    "a malformed digest is rejected",
			amd64:   "sha256:not-a-digest",
			arm64:   testARM64Digest,
			wantErr: true,
		},
		{
			name:    "an absent platform is rejected",
			amd64:   testAMD64Digest,
			arm64:   "MISSING",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			bash := bashWithAssociativeArrays(t)
			requireJQ(t)

			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatalf("create fake bin: %v", err)
			}
			writeExecutable(t, filepath.Join(bin, "crane"), fakePlatformCrane)
			writeExecutable(t, filepath.Join(bin, "timeout"), passthroughTimeout)

			table := writeFile(t, filepath.Join(dir, "platforms.tsv"),
				fmt.Sprintf("linux/amd64\t%s\nlinux/arm64\t%s\n", test.amd64, test.arm64))

			shipped := test.platforms
			if shipped == nil {
				shipped = []string{"linux/amd64", "linux/arm64"}
			}
			index := writeFile(t, filepath.Join(dir, "index-platforms"),
				strings.Join(shipped, "\n")+"\n")
			outputs := filepath.Join(dir, "github-output")

			command := exec.Command(bash, "-c", resolve.Run)
			command.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_PLATFORM_DIGESTS="+table,
				"FAKE_INDEX_PLATFORMS="+index,
				"SUBJECT_NAME="+testSubjectName,
				"INDEX_DIGEST="+testIndexDigest,
				"GITHUB_OUTPUT="+outputs,
			)
			combined, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("resolution error = %v, wantErr %t\n%s", err, test.wantErr, combined)
			}
			if test.wantErr {
				if !strings.Contains(string(combined), "::error::") {
					t.Errorf("resolution failed without an ::error:: annotation, so the cause would not surface in the job log:\n%s", combined)
				}
				return
			}
			written, readErr := os.ReadFile(outputs)
			if readErr != nil {
				t.Fatalf("read step outputs: %v", readErr)
			}
			// Parsed as key=value records rather than searched as text: a
			// substring check is satisfied by a line like
			// `not-amd64-digest=<digest>`, which does not set the key the
			// action's outputs actually read.
			outputRecords := map[string]string{}
			for _, line := range strings.Split(string(written), "\n") {
				key, value, found := strings.Cut(strings.TrimSpace(line), "=")
				if found {
					outputRecords[key] = value
				}
			}
			for key, want := range map[string]string{"amd64-digest": test.amd64, "arm64-digest": test.arm64} {
				got, present := outputRecords[key]
				if !present {
					t.Errorf("step outputs do not set %s; the action's outputs read steps.platforms.outputs.%s\n%s", key, key, written)
					continue
				}
				if got != want {
					t.Errorf("step output %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// fakePlatformCrane answers `crane digest --platform <platform> <reference>`
// from a table, reproducing crane's own exit 1 and "no child with platform"
// message when the index has no such child. It also reproduces the case the
// script exists for: a table entry equal to the index digest stands in for a
// plain manifest, which crane resolves successfully to itself.
//
// `crane manifest` is answered from FAKE_INDEX_PLATFORMS, one os/arch per line,
// so a case can describe an index that ships a platform the action does not
// attest. Defaulting it to the two covered platforms keeps every pre-existing
// case describing the index it always described.
const fakePlatformCrane = `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "manifest" ]]; then
  printf '{"manifests":['
  separator=""
  while IFS=/ read -r os architecture; do
    [[ -z "${os}" ]] && continue
    printf '%s{"platform":{"os":"%s","architecture":"%s"}}' "${separator}" "${os}" "${architecture}"
    separator=","
  done < "${FAKE_INDEX_PLATFORMS}"
  printf ']}\n'
  exit 0
fi
platform=""
previous=""
for argument in "$@"; do
  if [[ "${previous}" == "--platform" ]]; then platform="${argument}"; fi
  previous="${argument}"
done
while IFS=$'\t' read -r candidate digest; do
  if [[ "${candidate}" == "${platform}" && "${digest}" != "MISSING" ]]; then
    printf '%s\n' "${digest}"
    exit 0
  fi
done < "${FAKE_PLATFORM_DIGESTS}"
echo "Error: no child with platform ${platform} in index" >&2
exit 1
`

// passthroughTimeout runs the wrapped command directly. That the calls are
// bounded is asserted against the script text in TestCosignInvocationPolicy; a
// real timer here would only add a way for the test to hang.
const passthroughTimeout = `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--foreground" ]]; then shift; fi
shift
exec "$@"
`
