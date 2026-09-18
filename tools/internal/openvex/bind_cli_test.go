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

package openvex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSource drops a source document into a temp dir and returns its path.
func writeSource(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openvex.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return path
}

// bindArgs renders the command line `openvex bind` is invoked with. An omitted
// -in is passed as the empty string rather than left off, so the case exercises
// the missing-input rejection instead of falling back on the flag default.
func bindArgs(in, out, image, digest string, omitIn, omitOut bool) []string {
	args := []string{"-image", image, "-digest", digest}
	if omitIn {
		in = ""
	}
	args = append(args, "-in", in)
	if !omitOut {
		args = append(args, "-out", out)
	}
	return args
}

func TestRunBind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		source   string
		omitOut  bool
		omitIn   bool
		image    string
		digest   string
		wantErr  bool
		wantKept string
	}{
		{
			name:     "writes the projection",
			source:   sourceDocument,
			image:    operatorImage,
			digest:   amd64Digest,
			wantKept: "bound 1 statement(s)",
		},
		{
			name:     "image with no statements still writes a document",
			source:   sourceDocument,
			image:    "ghcr.io/nvidia/nodewright/charts/nodewright",
			digest:   amd64Digest,
			wantKept: "bound 0 statement(s)",
		},
		{name: "missing -out", source: sourceDocument, omitOut: true, image: operatorImage, digest: amd64Digest, wantErr: true},
		{name: "missing -in", source: sourceDocument, omitIn: true, image: operatorImage, digest: amd64Digest, wantErr: true},
		{name: "empty source file", source: "", image: operatorImage, digest: amd64Digest, wantErr: true},
		{name: "bad digest", source: sourceDocument, image: operatorImage, digest: "sha256:nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := filepath.Join(t.TempDir(), "bound.openvex.json")
			args := bindArgs(writeSource(t, tt.source), out, tt.image, tt.digest, tt.omitIn, tt.omitOut)

			var stdout, stderr bytes.Buffer
			code := RunBind(args, &stdout, &stderr)
			if (code != 0) != tt.wantErr {
				t.Fatalf("RunBind() = %d, wantErr %t\nstderr: %s", code, tt.wantErr, stderr.String())
			}
			if tt.wantErr {
				if code != 1 {
					t.Errorf("RunBind() = %d on a rejected invocation, want 1", code)
				}
				if !strings.Contains(stderr.String(), "openvex bind:") {
					t.Errorf("failure did not explain itself on stderr: %q", stderr.String())
				}
				return
			}
			if stderr.Len() != 0 {
				t.Errorf("RunBind() wrote to stderr on success: %q", stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantKept) {
				t.Errorf("RunBind() stdout = %q, want it to contain %q", stdout.String(), tt.wantKept)
			}
			written, readErr := os.ReadFile(out)
			if readErr != nil {
				t.Fatalf("read projection: %v", readErr)
			}
			if len(written) == 0 {
				t.Error("projection file is empty")
			}
			info, statErr := os.Stat(out)
			if statErr != nil {
				t.Fatalf("stat projection: %v", statErr)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("projection mode = %o, want 600", perm)
			}
		})
	}
}

// TestRunBindIsDeterministic re-runs the whole command path, not just Bind, so
// a release that repeats produces byte-identical evidence on disk.
func TestRunBindIsDeterministic(t *testing.T) {
	t.Parallel()
	source := writeSource(t, sourceDocument)
	outputs := make([][]byte, 2)
	for i := range outputs {
		out := filepath.Join(t.TempDir(), "bound.openvex.json")
		var stderr bytes.Buffer
		if code := RunBind(bindArgs(source, out, operatorImage, amd64Digest, false, false), &bytes.Buffer{}, &stderr); code != 0 {
			t.Fatalf("RunBind() = %d on pass %d, want 0\nstderr: %s", code, i, stderr.String())
		}
		written, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("read projection: %v", err)
		}
		outputs[i] = written
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Errorf("two runs differ:\n%s\n---\n%s", outputs[0], outputs[1])
	}
}

// TestRunBindOnMissingFile keeps the failure a clean error rather than a panic;
// a release that cannot find its VEX source must stop before attesting.
func TestRunBindOnMissingFile(t *testing.T) {
	t.Parallel()
	args := bindArgs(
		filepath.Join(t.TempDir(), "absent.json"),
		filepath.Join(t.TempDir(), "bound.json"),
		operatorImage, amd64Digest, false, false)
	var stderr bytes.Buffer
	if code := RunBind(args, &bytes.Buffer{}, &stderr); code == 0 {
		t.Fatal("RunBind() = 0, want a failure on a missing source")
	}
}

// TestRunBindRejectsAnUnknownFlag pins that a bad command line returns a status
// rather than exiting the process, which is what lets every case above run
// in-process. Parse errors are reported on the writer the caller supplied, not
// on os.Stderr.
func TestRunBindRejectsAnUnknownFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := RunBind([]string{"-nope"}, &stdout, &stderr); code != 2 {
		t.Errorf("RunBind(-nope) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "-nope") {
		t.Errorf("parse failure was not reported on the supplied stderr: %q", stderr.String())
	}
}

// TestReadSourceEnforcesSizeCap covers the bounded-read rule: the source is
// streamed under a limit rather than allocated whole, so a path that resolves
// to something unbounded fails instead of exhausting memory.
func TestReadSourceEnforcesSizeCap(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("a", int(maxDocumentBytes)+1)
	if _, err := readSource(writeSource(t, oversized)); err == nil {
		t.Fatal("readSource() error = nil, want a size-limit failure")
	}
	atLimit := strings.Repeat("a", int(maxDocumentBytes))
	if _, err := readSource(writeSource(t, atLimit)); err != nil {
		t.Fatalf("readSource() error = %v at exactly the limit, want success", err)
	}
	if _, err := readSource(writeSource(t, "")); err == nil {
		t.Fatal("readSource() error = nil on an empty file, want a rejection")
	}
}
