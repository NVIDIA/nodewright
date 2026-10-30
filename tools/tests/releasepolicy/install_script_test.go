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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const installScriptPath = "scripts/install-cli.sh"

// cliRegistry serves a CLI release the way cli-release.yaml publishes it: an
// index of oras artifact manifests, each with one layer holding the binary. The
// JSON keeps oras 1.3's field order, since the script reads it with awk rather
// than a JSON parser and the order is what that parsing depends on.
type cliRegistry struct {
	// binaries maps <os>/<arch> to the bytes that platform ships.
	binaries map[string]string
	// tags names the versions the index is tagged with.
	tags []string
	// servedBlob and servedManifest, when set, replace what is served for one
	// platform, standing in for a registry or a path that tampered with it.
	servedBlob     map[string]string
	servedManifest map[string]string
}

type ociPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

type ociIndexEntry struct {
	MediaType    string      `json:"mediaType"`
	Digest       string      `json:"digest"`
	Size         int         `json:"size"`
	Platform     ociPlatform `json:"platform"`
	ArtifactType string      `json:"artifactType"`
}

type ociLayer struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int               `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

type ociManifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	MediaType     string     `json:"mediaType"`
	ArtifactType  string     `json:"artifactType"`
	Config        ociLayer   `json:"config"`
	Layers        []ociLayer `json:"layers"`
}

func sha256Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const testRegistryToken = "test-token"

// serve starts the registry and returns the repository reference to install
// from. Every /v2 read requires the anonymous token GHCR would hand out.
func (r cliRegistry) serve(t *testing.T) string {
	t.Helper()
	objects := map[string][]byte{}
	var entries []ociIndexEntry
	for platform, binary := range r.binaries {
		goos, arch, _ := strings.Cut(platform, "/")
		title := "kubectl-nodewright"
		if goos == "windows" {
			title += ".exe"
		}
		blob := []byte(binary)
		blobDigest := sha256Digest(blob)
		config := []byte(`{"architecture":"` + arch + `","os":"` + goos + `"}`)
		manifest, err := json.Marshal(ociManifest{
			SchemaVersion: 2,
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			ArtifactType:  "application/vnd.nvidia.nodewright.cli.v1",
			Config: ociLayer{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    sha256Digest(config),
				Size:      len(config),
			},
			Layers: []ociLayer{{
				MediaType:   "application/octet-stream",
				Digest:      blobDigest,
				Size:        len(blob),
				Annotations: map[string]string{"org.opencontainers.image.title": title},
			}},
		})
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		manifestDigest := sha256Digest(manifest)
		objects["/manifests/"+manifestDigest] = manifest
		objects["/blobs/"+blobDigest] = blob
		if tampered, ok := r.servedManifest[platform]; ok {
			objects["/manifests/"+manifestDigest] = []byte(tampered)
		}
		if tampered, ok := r.servedBlob[platform]; ok {
			objects["/blobs/"+blobDigest] = []byte(tampered)
		}
		entries = append(entries, ociIndexEntry{
			MediaType:    "application/vnd.oci.image.manifest.v1+json",
			Digest:       manifestDigest,
			Size:         len(manifest),
			Platform:     ociPlatform{Architecture: arch, OS: goos},
			ArtifactType: "application/vnd.nvidia.nodewright.cli.v1",
		})
	}
	index, err := json.Marshal(struct {
		SchemaVersion int             `json:"schemaVersion"`
		MediaType     string          `json:"mediaType"`
		Manifests     []ociIndexEntry `json:"manifests"`
	}{2, "application/vnd.oci.image.index.v1+json", entries})
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	for _, tag := range r.tags {
		objects["/manifests/"+tag] = index
	}

	const name = "/v2/nvidia/nodewright/cli"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"` + testRegistryToken + `"}`))
			return
		}
		if req.Header.Get("Authorization") != "Bearer "+testRegistryToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		object, ok := objects[strings.TrimPrefix(req.URL.Path, name)]
		if !strings.HasPrefix(req.URL.Path, name+"/") || !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(object)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://") + "/nvidia/nodewright/cli"
}

// runInstallScript runs scripts/install-cli.sh under a PATH holding only the
// tools it is documented to need, plus fakes for uname and, when given, cosign,
// so the host's own cosign and platform do not leak into the result.
func runInstallScript(t *testing.T, repository, unameS, unameM, cosign string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	for _, tool := range []string{"sh", "curl", "awk", "tr", "sed", "mktemp", "mkdir", "cp", "chmod", "mv", "rm", "cat"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	linked := false
	for _, tool := range []string{"sha256sum", "shasum"} {
		if path, err := exec.LookPath(tool); err == nil {
			if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
				t.Fatalf("link %s: %v", tool, err)
			}
			linked = true
			break
		}
	}
	if !linked {
		t.Skip("neither sha256sum nor shasum found")
	}
	writeExecutable(t, filepath.Join(bin, "uname"), `#!/bin/sh
case "$1" in -s) echo "`+unameS+`" ;; -m) echo "`+unameM+`" ;; esac
`)
	cosignLog := filepath.Join(dir, "cosign.log")
	if cosign != "" {
		writeExecutable(t, filepath.Join(bin, "cosign"), cosign)
	}

	installDir := filepath.Join(dir, "install")
	command := exec.Command(filepath.Join(bin, "sh"),
		append([]string{filepath.Join(repoRoot(t), installScriptPath), "--dir", installDir}, args...)...)
	command.Env = []string{
		"PATH=" + bin,
		"HOME=" + dir,
		"TMPDIR=" + t.TempDir(),
		"NODEWRIGHT_CLI_REPOSITORY=" + repository,
		"FAKE_COSIGN_LOG=" + cosignLog,
	}
	combined, err := command.CombinedOutput()
	if recorded, readErr := os.ReadFile(cosignLog); readErr == nil {
		t.Logf("cosign calls:\n%s", recorded)
	}
	return installDir, string(combined), err
}

// TestInstallScriptInstallsThePlatformsBinary drives the install script against
// a registry serving a CLI index, with the host's platform and cosign faked.
func TestInstallScriptInstallsThePlatformsBinary(t *testing.T) {
	t.Parallel()
	binaries := map[string]string{}
	for _, platform := range strings.Fields(cliPlatforms) {
		binaries[platform] = "kubectl-nodewright for " + platform
	}

	tests := []struct {
		name         string
		registry     cliRegistry
		unameS       string
		unameM       string
		cosign       string
		args         []string
		wantPlatform string
		wantOutput   string
	}{
		{
			name:         "latest on linux/amd64",
			registry:     cliRegistry{binaries: binaries, tags: []string{"latest"}},
			unameS:       "Linux",
			unameM:       "x86_64",
			wantPlatform: "linux/amd64",
			wantOutput:   "cosign not found",
		},
		{
			name:         "a pinned version on darwin/arm64",
			registry:     cliRegistry{binaries: binaries, tags: []string{"v0.6.0"}},
			unameS:       "Darwin",
			unameM:       "arm64",
			args:         []string{"--version", "v0.6.0"},
			wantPlatform: "darwin/arm64",
		},
		{
			name:         "linux/arm64 reported as aarch64",
			registry:     cliRegistry{binaries: binaries, tags: []string{"latest"}},
			unameS:       "Linux",
			unameM:       "aarch64",
			wantPlatform: "linux/arm64",
		},
		{
			name:         "the signature is verified when cosign is present",
			registry:     cliRegistry{binaries: binaries, tags: []string{"latest"}},
			unameS:       "Linux",
			unameM:       "x86_64",
			cosign:       recordingCosignRequiringNewBundleFormat,
			wantPlatform: "linux/amd64",
			wantOutput:   "Verified the release signature",
		},
		{
			name:         "--no-verify skips cosign",
			registry:     cliRegistry{binaries: binaries, tags: []string{"latest"}},
			unameS:       "Linux",
			unameM:       "x86_64",
			cosign:       failingCosign,
			args:         []string{"--no-verify"},
			wantPlatform: "linux/amd64",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := test.registry.serve(t)
			installDir, output, err := runInstallScript(t, repository, test.unameS, test.unameM, test.cosign, test.args...)
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			installed := filepath.Join(installDir, "kubectl-nodewright")
			content, err := os.ReadFile(installed)
			if err != nil {
				t.Fatalf("read installed binary: %v\n%s", err, output)
			}
			if string(content) != binaries[test.wantPlatform] {
				t.Errorf("installed %q, want the %s binary %q", content, test.wantPlatform, binaries[test.wantPlatform])
			}
			info, err := os.Stat(installed)
			if err != nil {
				t.Fatalf("stat installed binary: %v", err)
			}
			if mode := info.Mode().Perm(); mode != 0o755 {
				t.Errorf("installed mode = %v, want 0755", mode)
			}
			entries, err := os.ReadDir(installDir)
			if err != nil {
				t.Fatalf("read install dir: %v", err)
			}
			if len(entries) != 1 {
				t.Errorf("install dir holds %d entries, want only kubectl-nodewright (a staged copy was left behind?)", len(entries))
			}
			if !strings.Contains(output, test.wantOutput) {
				t.Errorf("output does not mention %q:\n%s", test.wantOutput, output)
			}
		})
	}
}

// TestInstallScriptRefusesWhatItCannotVerify covers every way the chain from
// the tag to the installed bytes can break. In each, nothing may be installed:
// a half-checked binary on PATH is worse than a failed install.
func TestInstallScriptRefusesWhatItCannotVerify(t *testing.T) {
	t.Parallel()
	binaries := map[string]string{}
	for _, platform := range strings.Fields(cliPlatforms) {
		binaries[platform] = "kubectl-nodewright for " + platform
	}
	withoutLinuxArm64 := map[string]string{}
	for platform, binary := range binaries {
		if platform != "linux/arm64" {
			withoutLinuxArm64[platform] = binary
		}
	}

	tests := []struct {
		name       string
		registry   cliRegistry
		unameM     string
		cosign     string
		args       []string
		wantOutput string
	}{
		{
			name:       "a tampered binary",
			registry:   cliRegistry{binaries: binaries, tags: []string{"latest"}, servedBlob: map[string]string{"linux/amd64": "something else"}},
			wantOutput: "has digest",
		},
		{
			name: "a tampered manifest",
			registry: cliRegistry{binaries: binaries, tags: []string{"latest"}, servedManifest: map[string]string{
				"linux/amd64": `{"layers":[{"digest":"sha256:` + strings.Repeat("e", 64) + `"}]}`,
			}},
			wantOutput: "has digest",
		},
		{
			name:       "a release with no build for this platform",
			registry:   cliRegistry{binaries: withoutLinuxArm64, tags: []string{"latest"}},
			unameM:     "aarch64",
			wantOutput: "has no linux/arm64 build",
		},
		{
			name:       "a version that was never published",
			registry:   cliRegistry{binaries: binaries, tags: []string{"latest"}},
			args:       []string{"--version", "v9.9.9"},
			wantOutput: "could not read",
		},
		{
			name:       "a version that is not semver",
			registry:   cliRegistry{binaries: binaries, tags: []string{"latest"}},
			args:       []string{"--version", "main"},
			wantOutput: "is not 'latest' or v<major>.<minor>.<patch>",
		},
		{
			name:       "a signature cosign rejects",
			registry:   cliRegistry{binaries: binaries, tags: []string{"latest"}},
			cosign:     failingCosign,
			wantOutput: "signature verification failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			unameM := test.unameM
			if unameM == "" {
				unameM = "x86_64"
			}
			repository := test.registry.serve(t)
			installDir, output, err := runInstallScript(t, repository, "Linux", unameM, test.cosign, test.args...)
			if err == nil {
				t.Fatalf("install succeeded, want a refusal:\n%s", output)
			}
			if !strings.Contains(output, test.wantOutput) {
				t.Errorf("output does not mention %q:\n%s", test.wantOutput, output)
			}
			if _, statErr := os.Stat(filepath.Join(installDir, "kubectl-nodewright")); statErr == nil {
				t.Error("kubectl-nodewright was installed despite the failure")
			}
		})
	}
}

// recordingCosignRequiringNewBundleFormat succeeds only when asked to read the
// new bundle format, which is how the release is signed; cosign v2 without the
// flag looks for a legacy signature tag and reports the release unsigned.
const recordingCosignRequiringNewBundleFormat = `#!/bin/sh
printf '%s\n' "$*" >> "${FAKE_COSIGN_LOG}"
case " $* " in *" --new-bundle-format=true "*) exit 0 ;; esac
echo "no signatures found" >&2
exit 1
`

const failingCosign = `#!/bin/sh
printf '%s\n' "$*" >> "${FAKE_COSIGN_LOG}"
echo "no matching signatures" >&2
exit 1
`
