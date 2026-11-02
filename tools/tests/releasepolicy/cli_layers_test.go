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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliCommitTime is the tagged commit's time in the fake repository; every
// timestamp the push step writes must be this one.
var cliCommitTime = time.Date(2026, 10, 30, 23, 53, 21, 0, time.UTC)

// TestCLIPushBuildsReproducibleImageLayers runs the real "Push the CLI artifact
// index" step with a fake oras and the real tar, gzip and jq. Each platform has
// to be an image a scanner can read: one gzip tar layer holding only the
// binary, executable, and a config whose diff_id is that tar's digest and whose
// platform is the one oras manifest index create will list. And the push has to
// be reproducible, or a re-run moves the version tag off the signed index.
func TestCLIPushBuildsReproducibleImageLayers(t *testing.T) {
	t.Parallel()
	job := loadWorkflow(t, cliReleasePath).publishingJob(t).Job
	push := job.Steps.named(t, "Push the CLI artifact index")
	bash := bashWithAssociativeArrays(t)
	requireJQ(t)

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatalf("create fake bin: %v", err)
	}
	linkGNUTar(t, bin)
	for _, tool := range []string{"gzip", "sha256sum", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	writeExecutable(t, filepath.Join(bin, "oras"), fakeRecordingOras)
	writeExecutable(t, filepath.Join(bin, "timeout"), passthroughTimeout)

	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	stamp := cliCommitTime.Format(time.RFC3339)
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
			"commit", "-q", "--allow-empty", "-m", "release"},
	} {
		command := exec.Command("git", args...)
		command.Dir = repo
		command.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		if combined, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[0], err, combined)
		}
	}

	// Staged without the executable bit, as upload-artifact hands them over.
	binaries := filepath.Join(dir, "binaries")
	names := map[string]string{}
	for _, platform := range strings.Fields(cliPlatforms) {
		name := "kubectl-nodewright"
		if strings.HasPrefix(platform, "windows/") {
			name += ".exe"
		}
		names[platform] = name
		platformDir := filepath.Join(binaries, strings.ReplaceAll(platform, "/", "-"))
		if err := os.MkdirAll(platformDir, 0o700); err != nil {
			t.Fatalf("create %s: %v", platformDir, err)
		}
		writeFile(t, filepath.Join(platformDir, name), "binary for "+platform)
	}

	run := func(runnerTemp string) string {
		t.Helper()
		orasLog := filepath.Join(dir, filepath.Base(runnerTemp)+"-oras.log")
		command := exec.Command(bash, "-c", push.Run)
		command.Dir = repo
		command.Env = append(os.Environ(),
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_ORAS_LOG="+orasLog,
			"REGISTRY=ghcr.io",
			"IMAGE_NAME=NVIDIA/nodewright",
			"VERSION=v0.6.0",
			"ARTIFACT_TYPE="+job.Env["ARTIFACT_TYPE"],
			"BINARIES="+binaries,
			"GITHUB_SHA=HEAD",
			"GITHUB_OUTPUT="+filepath.Join(dir, "github-output"),
			"RUNNER_TEMP="+runnerTemp,
		)
		if combined, err := command.CombinedOutput(); err != nil {
			t.Fatalf("push step failed: %v\n%s", err, combined)
		}
		recorded, err := os.ReadFile(orasLog)
		if err != nil {
			t.Fatalf("read fake oras log: %v", err)
		}
		return string(recorded)
	}
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	orasCalls := run(first)
	run(second)

	if job.Env["ARTIFACT_TYPE"] != "application/vnd.nvidia.nodewright.cli.v1" {
		t.Errorf("publish-oci ARTIFACT_TYPE = %q; catalogs identify the CLI by application/vnd.nvidia.nodewright.cli.v1", job.Env["ARTIFACT_TYPE"])
	}
	for _, want := range []string{
		"--artifact-type application/vnd.nvidia.nodewright.cli.v1",
		"--config config.json:application/vnd.oci.image.config.v1+json",
		"kubectl-nodewright.tar.gz:application/vnd.oci.image.layer.v1.tar+gzip",
	} {
		if got, wantCount := strings.Count(orasCalls, want), len(names); got != wantCount {
			t.Errorf("oras push received %q %d times, want once per platform (%d); every call:\n%s", want, got, wantCount, orasCalls)
		}
	}

	for platform, name := range names {
		slug := strings.ReplaceAll(platform, "/", "-")
		layer := readFile(t, filepath.Join(first, "cli-layers", slug, "kubectl-nodewright.tar.gz"))
		config := readFile(t, filepath.Join(first, "cli-layers", slug, "config.json"))
		if again := readFile(t, filepath.Join(second, "cli-layers", slug, "kubectl-nodewright.tar.gz")); !bytes.Equal(layer, again) {
			t.Errorf("%s: two runs packed different layers; a re-run would push new digests", platform)
		}
		if again := readFile(t, filepath.Join(second, "cli-layers", slug, "config.json")); !bytes.Equal(config, again) {
			t.Errorf("%s: two runs wrote different configs; a re-run would push new digests", platform)
		}

		zipped, err := gzip.NewReader(bytes.NewReader(layer))
		if err != nil {
			t.Fatalf("%s: layer is not gzip: %v", platform, err)
		}
		uncompressed, err := io.ReadAll(zipped)
		if err != nil {
			t.Fatalf("%s: decompress layer: %v", platform, err)
		}
		archive := tar.NewReader(bytes.NewReader(uncompressed))
		var entries []*tar.Header
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: read layer tar: %v", platform, err)
			}
			entries = append(entries, header)
		}
		if len(entries) != 1 {
			t.Fatalf("%s: layer holds %d entries, want only the binary", platform, len(entries))
		}
		entry := entries[0]
		if entry.Name != name || entry.Typeflag != tar.TypeReg {
			t.Errorf("%s: layer entry is %q (type %c), want the regular file %q", platform, entry.Name, entry.Typeflag, name)
		}
		if entry.Mode&0o7777 != 0o755 {
			t.Errorf("%s: layer entry mode = %o, want 755; extracting it must give a runnable plugin", platform, entry.Mode&0o7777)
		}
		if entry.Uid != 0 || entry.Gid != 0 || entry.Uname != "" || entry.Gname != "" {
			t.Errorf("%s: layer entry owned by %d:%d (%q:%q), want 0:0 with no names", platform, entry.Uid, entry.Gid, entry.Uname, entry.Gname)
		}
		if !entry.ModTime.Equal(cliCommitTime) {
			t.Errorf("%s: layer entry mtime = %v, want the commit time %v", platform, entry.ModTime, cliCommitTime)
		}

		var image struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Created      string `json:"created"`
			RootFS       struct {
				Type    string   `json:"type"`
				DiffIDs []string `json:"diff_ids"`
			} `json:"rootfs"`
		}
		if err := json.Unmarshal(config, &image); err != nil {
			t.Fatalf("%s: config is not JSON: %v", platform, err)
		}
		if got := image.OS + "/" + image.Architecture; got != platform {
			t.Errorf("%s: config platform = %s; oras manifest index create lists the entry under it", platform, got)
		}
		if image.Created != stamp {
			t.Errorf("%s: config created = %q, want the commit time %q", platform, image.Created, stamp)
		}
		if image.RootFS.Type != "layers" || len(image.RootFS.DiffIDs) != 1 || image.RootFS.DiffIDs[0] != sha256Digest(uncompressed) {
			t.Errorf("%s: config rootfs = %+v, want type layers with the one diff_id %s; scanners verify layers against it",
				platform, image.RootFS, sha256Digest(uncompressed))
		}
	}
}

// linkGNUTar puts GNU tar on the fake PATH as tar. The step uses GNU-only flags
// (--sort, --pax-option) that the Ubuntu runner's tar has and bsdtar, macOS's
// tar, does not; Homebrew installs GNU tar as gtar.
func linkGNUTar(t *testing.T, bin string) {
	t.Helper()
	for _, candidate := range []string{"tar", "gtar"} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		version, err := exec.Command(path, "--version").Output()
		if err != nil || !strings.Contains(string(version), "GNU tar") {
			continue
		}
		if err := os.Symlink(path, filepath.Join(bin, "tar")); err != nil {
			t.Fatalf("link GNU tar: %v", err)
		}
		return
	}
	t.Skip("GNU tar not found; the push step uses flags only GNU tar has")
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

// fakeRecordingOras records each invocation's arguments, one call per line,
// and answers a push with a digest unique to the config it was handed, which
// the step collects for oras manifest index create.
const fakeRecordingOras = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_ORAS_LOG}"
if [[ "${1:-}" == "push" ]]; then
  printf 'sha256:%s\n' "$(sha256sum config.json | cut -d' ' -f1)"
fi
`
