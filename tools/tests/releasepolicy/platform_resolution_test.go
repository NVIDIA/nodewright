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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	testIndexDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testSubjectName = "ghcr.io/nvidia/nodewright/operator"

	// imagePlatforms is the action's default, the operator and agent images.
	imagePlatforms = "linux/amd64 linux/arm64"
	// cliPlatforms is every platform cli-release-build produces.
	cliPlatforms = "linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
)

// testDigest returns a well-formed digest made of one repeated hex character,
// so each platform in a case gets a distinct, recognisable digest.
func testDigest(c byte) string {
	return "sha256:" + strings.Repeat(string(c), 64)
}

// distinctDigests assigns each platform its own digest, in order.
func distinctDigests(platforms string) map[string]string {
	digests := map[string]string{}
	for i, platform := range strings.Fields(platforms) {
		digests[platform] = testDigest("123456789abcdef"[i])
	}
	return digests
}

// with returns a copy of digests with the given overrides applied.
func with(digests map[string]string, overrides map[string]string) map[string]string {
	merged := map[string]string{}
	for platform, digest := range digests {
		merged[platform] = digest
	}
	for platform, digest := range overrides {
		merged[platform] = digest
	}
	return merged
}

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

	images := distinctDigests(imagePlatforms)
	cli := distinctDigests(cliPlatforms)

	tests := []struct {
		name string
		// platforms is the action's platforms input.
		platforms string
		// shipped is what `crane manifest` reports the index ships. Nil means
		// exactly the requested platforms.
		shipped []string
		// digests is what `crane digest --platform` answers per platform;
		// MISSING stands for a platform the index does not have.
		digests map[string]string
		wantErr bool
	}{
		{
			name:      "the image pair resolves",
			platforms: imagePlatforms,
			digests:   images,
		},
		{
			name:      "every CLI platform resolves",
			platforms: cliPlatforms,
			digests:   cli,
		},
		{
			name:      "an attestation manifest is not mistaken for a platform",
			platforms: imagePlatforms,
			shipped:   []string{"linux/amd64", "linux/arm64", "unknown/unknown"},
			digests:   images,
		},
		{
			name:      "an index shipping a platform the action was not given is rejected",
			platforms: imagePlatforms,
			shipped:   []string{"linux/amd64", "linux/arm64", "linux/ppc64le"},
			digests:   images,
			wantErr:   true,
		},
		{
			name:      "a CLI index shipping a platform the action was not given is rejected",
			platforms: "linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64",
			shipped:   strings.Fields(cliPlatforms),
			digests:   cli,
			wantErr:   true,
		},
		{
			name:      "the first platform resolving to the index means the reference is a plain manifest",
			platforms: imagePlatforms,
			digests:   with(images, map[string]string{"linux/amd64": testIndexDigest}),
			wantErr:   true,
		},
		{
			name:      "a later platform resolving to the index means the reference is a plain manifest",
			platforms: cliPlatforms,
			digests:   with(cli, map[string]string{"windows/arm64": testIndexDigest}),
			wantErr:   true,
		},
		{
			name:      "two platforms resolving alike means one was lost",
			platforms: imagePlatforms,
			digests:   with(images, map[string]string{"linux/arm64": images["linux/amd64"]}),
			wantErr:   true,
		},
		{
			name:      "two platforms far apart in the list resolving alike means one was lost",
			platforms: cliPlatforms,
			digests:   with(cli, map[string]string{"windows/amd64": cli["linux/arm64"]}),
			wantErr:   true,
		},
		{
			name:      "a platform listed twice is rejected",
			platforms: "linux/amd64 linux/arm64 linux/amd64",
			shipped:   []string{"linux/amd64", "linux/arm64"},
			digests:   images,
			wantErr:   true,
		},
		{
			name:      "a malformed digest is rejected",
			platforms: imagePlatforms,
			digests:   with(images, map[string]string{"linux/amd64": "sha256:not-a-digest"}),
			wantErr:   true,
		},
		{
			name:      "an absent platform is rejected",
			platforms: cliPlatforms,
			digests:   with(cli, map[string]string{"darwin/amd64": "MISSING"}),
			wantErr:   true,
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

			var table strings.Builder
			for platform, digest := range test.digests {
				fmt.Fprintf(&table, "%s\t%s\n", platform, digest)
			}
			digestTable := writeFile(t, filepath.Join(dir, "platforms.tsv"), table.String())
			shipped := test.shipped
			if shipped == nil {
				shipped = strings.Fields(test.platforms)
			}
			index := writeFile(t, filepath.Join(dir, "index-platforms"),
				strings.Join(shipped, "\n")+"\n")
			outputs := filepath.Join(dir, "github-output")

			command := exec.Command(bash, "-c", resolve.Run)
			command.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_PLATFORM_DIGESTS="+digestTable,
				"FAKE_INDEX_PLATFORMS="+index,
				"SUBJECT_NAME="+testSubjectName,
				"INDEX_DIGEST="+testIndexDigest,
				"PLATFORMS="+test.platforms,
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
			// `not-platform-digests=<pairs>`, which does not set the key the
			// action's outputs actually read.
			outputRecords := map[string]string{}
			for _, line := range strings.Split(string(written), "\n") {
				key, value, found := strings.Cut(strings.TrimSpace(line), "=")
				if found {
					outputRecords[key] = value
				}
			}
			var want []string
			for _, platform := range strings.Fields(test.platforms) {
				want = append(want, platform+"="+test.digests[platform])
			}
			got, present := outputRecords["platform-digests"]
			if !present {
				t.Fatalf("step outputs do not set platform-digests; the action's outputs read steps.platforms.outputs.platform-digests\n%s", written)
			}
			if got != strings.Join(want, " ") {
				t.Errorf("step output platform-digests = %q, want %q (one pair per platform, in input order)", got, strings.Join(want, " "))
			}
		})
	}
}

// TestEveryPlatformGetsBothAttestations runs the real attest step with a fake
// cosign. The static checks in release_workflow_test.go see the loop body once;
// this is what shows the loop really publishes an SBOM and a VEX document on
// every platform's own manifest, each from that platform's evidence file.
func TestEveryPlatformGetsBothAttestations(t *testing.T) {
	t.Parallel()
	bash := bashWithAssociativeArrays(t)
	attest := loadAction(t, attestActionPath).Runs.Steps.named(t, "Attest per-platform SBOM and VEX")

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatalf("create fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(bin, "cosign"), fakeRecordingCosign)
	writeExecutable(t, filepath.Join(bin, "timeout"), passthroughTimeout)
	log := filepath.Join(dir, "cosign.log")

	digests := distinctDigests(cliPlatforms)
	var pairs []string
	for _, platform := range strings.Fields(cliPlatforms) {
		pairs = append(pairs, platform+"="+digests[platform])
	}
	evidence := filepath.Join(dir, "evidence")

	command := exec.Command(bash, "-c", attest.Run)
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_COSIGN_LOG="+log,
		"SUBJECT_NAME="+testSubjectName,
		"PLATFORM_DIGESTS="+strings.Join(pairs, " "),
		"EVIDENCE_DIR="+evidence,
	)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("attest step failed: %v\n%s", err, combined)
	}
	recorded, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read fake cosign log: %v", err)
	}

	calls := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		calls[line]++
	}
	for _, platform := range strings.Fields(cliPlatforms) {
		slug := strings.ReplaceAll(platform, "/", "-")
		subject := testSubjectName + "@" + digests[platform]
		for _, predicate := range []string{"cyclonedx", "openvex"} {
			want := fmt.Sprintf("attest --yes --new-bundle-format=true --type %s --predicate %s/%s.%s.json %s",
				predicate, evidence, slug, predicate, subject)
			if calls[want] != 1 {
				t.Errorf("%s: want exactly one call\n  %s\ngot %d; every call:\n%s", platform, want, calls[want], recorded)
			}
		}
	}
	if got, want := len(calls), 2*len(strings.Fields(cliPlatforms)); got != want {
		t.Errorf("cosign was called %d distinct ways, want %d (two per platform):\n%s", got, want, recorded)
	}
}

// TestBinariesDirNeedsOneFilePerPlatform drives the input check that runs before
// anything is resolved or signed. Each platform's SBOM is generated from exactly
// one file, so a missing or ambiguous one must stop the release there rather
// than publish a platform with no SBOM, or one describing the wrong file.
func TestBinariesDirNeedsOneFilePerPlatform(t *testing.T) {
	t.Parallel()
	validate := loadAction(t, attestActionPath).Runs.Steps.named(t, "Validate inputs")

	tests := []struct {
		name    string
		files   map[string][]string // per platform slug, the files its directory holds
		wantErr bool
	}{
		{
			name:  "one binary per platform",
			files: map[string][]string{"linux-amd64": {"kubectl-nodewright"}, "windows-amd64": {"kubectl-nodewright.exe"}},
		},
		{
			name:    "a platform with no directory",
			files:   map[string][]string{"linux-amd64": {"kubectl-nodewright"}},
			wantErr: true,
		},
		{
			name:    "a platform directory with two files",
			files:   map[string][]string{"linux-amd64": {"kubectl-nodewright", "README"}, "windows-amd64": {"kubectl-nodewright.exe"}},
			wantErr: true,
		},
		{
			name:    "an empty platform directory",
			files:   map[string][]string{"linux-amd64": {"kubectl-nodewright"}, "windows-amd64": nil},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			bash := bashWithAssociativeArrays(t)
			binaries := t.TempDir()
			for slug, files := range test.files {
				platformDir := filepath.Join(binaries, slug)
				if err := os.Mkdir(platformDir, 0o700); err != nil {
					t.Fatalf("create %s: %v", platformDir, err)
				}
				for _, file := range files {
					writeFile(t, filepath.Join(platformDir, file), "binary")
				}
			}

			command := exec.Command(bash, "-c", validate.Run)
			command.Env = append(os.Environ(),
				"SUBJECT_NAME="+testSubjectName,
				"INDEX_DIGEST="+testIndexDigest,
				"PLATFORMS=linux/amd64 windows/amd64",
				"BINARIES_DIR="+binaries,
			)
			combined, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, wantErr %t\n%s", err, test.wantErr, combined)
			}
			if test.wantErr && !strings.Contains(string(combined), "::error::") {
				t.Errorf("validation failed without an ::error:: annotation:\n%s", combined)
			}
		})
	}
}

// TestEachSBOMDescribesItsPlatformsBinary runs the real evidence step with fake
// syft, go and crane. For an image, syft must read the platform manifest from
// the registry; for the CLI, it must read the one staged file that platform
// ships, and only after the manifest is shown to ship exactly that file. A
// mixed-up slug or a stale staging directory would otherwise publish a signed
// SBOM describing some other platform's binary.
func TestEachSBOMDescribesItsPlatformsBinary(t *testing.T) {
	t.Parallel()
	evidence := loadAction(t, attestActionPath).Runs.Steps.named(t, "Generate and validate per-platform evidence")

	tests := []struct {
		name      string
		platforms string
		// staged says whether the case stages one binary per platform and runs
		// with BINARIES_DIR set, as the CLI does.
		staged bool
		// layers overrides what `crane manifest` reports a platform's manifest
		// ships; by default it is exactly that platform's staged binary.
		layers  func(platform, staged string) string
		wantErr bool
	}{
		{
			name:      "an image SBOM is read from its platform manifest",
			platforms: imagePlatforms,
		},
		{
			name:      "every CLI SBOM is read from that platform's staged binary",
			platforms: cliPlatforms,
			staged:    true,
		},
		{
			name:      "a staged binary the manifest does not ship is rejected",
			platforms: cliPlatforms,
			staged:    true,
			layers: func(platform, staged string) string {
				if platform == "darwin/arm64" {
					return testDigest('e')
				}
				return staged
			},
			wantErr: true,
		},
		{
			name:      "a manifest shipping more than the binary is rejected",
			platforms: cliPlatforms,
			staged:    true,
			layers: func(platform, staged string) string {
				if platform == "windows/amd64" {
					return staged + " " + testDigest('e')
				}
				return staged
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			bash := bashWithAssociativeArrays(t)
			requireJQ(t)
			if _, err := exec.LookPath("sha256sum"); err != nil && test.staged {
				t.Skip("sha256sum not found; the action's staged-binary check hashes the file with it")
			}

			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatalf("create fake bin: %v", err)
			}
			syft := filepath.Join(bin, "syft")
			writeExecutable(t, syft, fakeRecordingSyft)
			writeExecutable(t, filepath.Join(bin, "go"), fakeOpenVEXGo)
			writeExecutable(t, filepath.Join(bin, "crane"), fakeLayersCrane)
			writeExecutable(t, filepath.Join(bin, "timeout"), passthroughTimeout)

			workspace := filepath.Join(dir, "workspace")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			writeFile(t, filepath.Join(workspace, ".openvex.json"), `{"statements":[]}`)

			digests := distinctDigests(test.platforms)
			var pairs []string
			var layerTable strings.Builder
			binaries := ""
			want := map[string]string{}
			runnerTemp := filepath.Join(dir, "runner")
			evidenceDir := filepath.Join(runnerTemp, "nodewright-evidence", strings.TrimPrefix(testIndexDigest, "sha256:"))
			if test.staged {
				binaries = filepath.Join(dir, "binaries")
			}
			for _, platform := range strings.Fields(test.platforms) {
				slug := strings.ReplaceAll(platform, "/", "-")
				pairs = append(pairs, platform+"="+digests[platform])
				sbom := filepath.Join(evidenceDir, slug+".cyclonedx.json")
				if !test.staged {
					want[platform] = "registry:" + testSubjectName + "@" + digests[platform] + " -o cyclonedx-json=" + sbom
					continue
				}
				name := "kubectl-nodewright"
				if strings.HasPrefix(platform, "windows/") {
					name += ".exe"
				}
				platformDir := filepath.Join(binaries, slug)
				if err := os.MkdirAll(platformDir, 0o700); err != nil {
					t.Fatalf("create %s: %v", platformDir, err)
				}
				binary := writeFile(t, filepath.Join(platformDir, name), "binary for "+platform)
				sum := sha256.Sum256([]byte("binary for " + platform))
				stagedDigest := "sha256:" + hex.EncodeToString(sum[:])
				layers := stagedDigest
				if test.layers != nil {
					layers = test.layers(platform, stagedDigest)
				}
				fmt.Fprintf(&layerTable, "%s\t%s\n", digests[platform], layers)
				want[platform] = "file:" + binary + " -o cyclonedx-json=" + sbom
			}
			syftLog := filepath.Join(dir, "syft.log")

			command := exec.Command(bash, "-c", evidence.Run)
			command.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_SYFT_LOG="+syftLog,
				"FAKE_MANIFEST_LAYERS="+writeFile(t, filepath.Join(dir, "layers.tsv"), layerTable.String()),
				"SYFT="+syft,
				"SUBJECT_NAME="+testSubjectName,
				"INDEX_DIGEST="+testIndexDigest,
				"PLATFORM_DIGESTS="+strings.Join(pairs, " "),
				"BINARIES_DIR="+binaries,
				"GITHUB_WORKSPACE="+workspace,
				"GITHUB_OUTPUT="+filepath.Join(dir, "github-output"),
				"RUNNER_TEMP="+runnerTemp,
			)
			combined, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("evidence step error = %v, wantErr %t\n%s", err, test.wantErr, combined)
			}
			if test.wantErr {
				if !strings.Contains(string(combined), "::error::") {
					t.Errorf("evidence step failed without an ::error:: annotation:\n%s", combined)
				}
				return
			}

			recorded, readErr := os.ReadFile(syftLog)
			if readErr != nil {
				t.Fatalf("read fake syft log: %v", readErr)
			}
			calls := map[string]int{}
			for _, line := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
				calls[line]++
			}
			for platform, call := range want {
				if calls[call] != 1 {
					t.Errorf("%s: want exactly one syft call\n  %s\ngot %d; every call:\n%s", platform, call, calls[call], recorded)
				}
			}
			if len(calls) != len(want) {
				t.Errorf("syft was called %d distinct ways, want %d (one per platform):\n%s", len(calls), len(want), recorded)
			}
		})
	}
}

// TestPlatformsInputIsAnOSArchList drives the input check on platforms. The
// list is split on whitespace, so a comma-separated value would otherwise reach
// crane as one platform named "linux/amd64,linux/arm64".
func TestPlatformsInputIsAnOSArchList(t *testing.T) {
	t.Parallel()
	validate := loadAction(t, attestActionPath).Runs.Steps.named(t, "Validate inputs")

	tests := []struct {
		platforms string
		wantErr   bool
	}{
		{platforms: imagePlatforms},
		{platforms: cliPlatforms},
		{platforms: " linux/amd64   linux/arm64 "},
		{platforms: "", wantErr: true},
		{platforms: "   ", wantErr: true},
		{platforms: "linux/amd64,linux/arm64", wantErr: true},
		{platforms: "linux", wantErr: true},
		{platforms: "linux/amd64/v8", wantErr: true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%q", test.platforms), func(t *testing.T) {
			t.Parallel()
			bash := bashWithAssociativeArrays(t)
			command := exec.Command(bash, "-c", validate.Run)
			command.Env = append(os.Environ(),
				"SUBJECT_NAME="+testSubjectName,
				"INDEX_DIGEST="+testIndexDigest,
				"PLATFORMS="+test.platforms,
				"BINARIES_DIR=",
			)
			combined, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, wantErr %t\n%s", err, test.wantErr, combined)
			}
			if test.wantErr && !strings.Contains(string(combined), "::error::") {
				t.Errorf("validation failed without an ::error:: annotation:\n%s", combined)
			}
		})
	}
}

// TestVerifyKeepsTheChartOnOneSubject drives the verify step the way the Helm
// chart calls it, with no platform digests. An empty list is how a
// single-artifact subject asks for every check against subject-digest, so it
// has to verify the signature, SBOM and provenance there and look for no VEX,
// which the chart does not carry. Rejecting the empty list would fail every
// chart release at its last step.
func TestVerifyKeepsTheChartOnOneSubject(t *testing.T) {
	t.Parallel()
	verify := loadAction(t, verifyActionPath).Runs.Steps.named(t, "Verify release subject")

	for _, platformDigests := range []string{"", "  "} {
		t.Run(fmt.Sprintf("%q", platformDigests), func(t *testing.T) {
			t.Parallel()
			bash := bashWithAssociativeArrays(t)
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatalf("create fake bin: %v", err)
			}
			log := filepath.Join(dir, "cosign.log")
			writeExecutable(t, filepath.Join(bin, "cosign"), fakeRecordingCosign)
			writeExecutable(t, filepath.Join(bin, "timeout"), passthroughTimeout)

			command := exec.Command(bash, "-c", verify.Run)
			command.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_COSIGN_LOG="+log,
				"SUBJECT_NAME="+testSubjectName,
				"SUBJECT_DIGEST="+testIndexDigest,
				"PLATFORM_DIGESTS="+platformDigests,
				"CERTIFICATE_IDENTITY_REGEXP=.*",
				"CERTIFICATE_OIDC_ISSUER=https://token.actions.githubusercontent.com",
			)
			if combined, err := command.CombinedOutput(); err != nil {
				t.Fatalf("chart verification failed: %v\n%s", err, combined)
			}
			recorded, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("read cosign log: %v", err)
			}

			subject := testSubjectName + "@" + testIndexDigest
			var got []string
			for line := range strings.Lines(string(recorded)) {
				fields := strings.Fields(line)
				var predicate string
				for i, field := range fields {
					if field == "--type" && i+1 < len(fields) {
						predicate = fields[i+1]
					}
				}
				got = append(got, fields[0]+" "+predicate+" "+fields[len(fields)-1])
			}
			want := []string{
				"verify  " + subject,
				"verify-attestation cyclonedx " + subject,
				"verify-attestation https://slsa.dev/provenance/v1 " + subject,
			}
			if !slices.Equal(got, want) {
				t.Errorf("cosign calls = %q, want %q", got, want)
			}
		})
	}
}

// fakeRecordingSyft records each invocation's arguments, one call per line, and
// writes a minimal CycloneDX document to the requested output so the step's
// format check passes.
const fakeRecordingSyft = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_SYFT_LOG}"
for argument in "$@"; do
  case "${argument}" in
    cyclonedx-json=*) printf '{"bomFormat":"CycloneDX"}\n' > "${argument#cyclonedx-json=}" ;;
  esac
done
`

// fakeOpenVEXGo stands in for `go run ./cmd/openvex`: validate succeeds, and
// bind writes an empty projection, which matches the empty source document the
// test supplies. The binder itself is tested in tools/internal/openvex.
const fakeOpenVEXGo = `#!/usr/bin/env bash
set -euo pipefail
out=""
previous=""
for argument in "$@"; do
  if [[ "${previous}" == "-out" ]]; then out="${argument}"; fi
  previous="${argument}"
done
if [[ " $* " == *" bind "* ]]; then
  printf '{"statements":[]}\n' > "${out}"
fi
`

// fakeLayersCrane answers `crane manifest <repository>@<digest>` with the layer
// digests FAKE_MANIFEST_LAYERS lists for that digest, space-separated.
const fakeLayersCrane = `#!/usr/bin/env bash
set -euo pipefail
reference="${*: -1}"
digest="${reference##*@}"
while IFS=$'\t' read -r candidate layers; do
  if [[ "${candidate}" == "${digest}" ]]; then
    printf '{"layers":['
    separator=""
    for layer in ${layers}; do
      printf '%s{"digest":"%s"}' "${separator}" "${layer}"
      separator=","
    done
    printf ']}\n'
    exit 0
  fi
done < "${FAKE_MANIFEST_LAYERS}"
echo "Error: manifest unknown ${reference}" >&2
exit 1
`

// fakeRecordingCosign records each invocation's arguments, one call per line.
const fakeRecordingCosign = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_COSIGN_LOG}"
`

// fakePlatformCrane answers `crane digest --platform <platform> <reference>`
// from a table, reproducing crane's own exit 1 and "no child with platform"
// message when the index has no such child. It also reproduces the case the
// script exists for: a table entry equal to the index digest stands in for a
// plain manifest, which crane resolves successfully to itself.
//
// `crane manifest` is answered from FAKE_INDEX_PLATFORMS, one os/arch per line,
// so a case can describe an index that ships a platform the action was not
// given.
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
