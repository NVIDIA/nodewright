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

// The rules `openvex validate` enforces are unit-tested in tools/internal/openvex.
// What is asserted here is that the action still calls it, in both modes: the
// source check and the projection check cover different bytes, so dropping
// either one during a refactor looks local and is not. Like the rest of the
// hand-written assertions, none of this is golden-driven.

package releasepolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evidenceStepName is the step that generates and checks everything the release
// attests. Both OpenVEX documents pass through it.
const evidenceStepName = "Generate and validate per-platform evidence"

func evidenceCommands(t *testing.T) []string {
	t.Helper()
	steps := loadAction(t, attestActionPath).Runs.Steps
	return shellCommands(steps.named(t, evidenceStepName).Run)
}

// indexOfCommand returns the position of the first command containing every
// given fragment, or -1.
func indexOfCommand(commands []string, fragments ...string) int {
	for i, command := range commands {
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(command, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

// indexOfOpenVEX returns the position of the first command running the given
// ./cmd/openvex subcommand and carrying every extra fragment, or -1.
func indexOfOpenVEX(commands []string, subcommand string, fragments ...string) int {
	for i, command := range commands {
		if !openvexIs(command, subcommand) {
			continue
		}
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(command, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

// TestOpenVEXIsValidatedInBothModes pins that both checks survive. Source mode
// checks the committed document; projection mode checks the bytes cosign is
// about to sign, which binding rewrote.
func TestOpenVEXIsValidatedInBothModes(t *testing.T) {
	t.Parallel()
	ops := validations(actionOps(loadAction(t, attestActionPath)))
	if len(ops) == 0 {
		t.Fatalf("%s never runs `openvex validate`", attestActionPath)
	}

	modes := map[string][]evidenceOp{}
	for _, op := range ops {
		modes[op.Mode] = append(modes[op.Mode], op)
	}

	if len(modes["source"]) == 0 {
		t.Errorf(`the committed .openvex.json is never validated in source mode.
A document that violates the OpenVEX v0.2.0 contract cannot produce a projection
worth signing, and one failure on the source is cheaper to read than the same
failure repeated once per platform.`)
	}
	if len(modes["projection"]) == 0 {
		t.Fatalf(`the per-platform projection is never validated in projection mode.
That is the one check proving every product identifier is bound to a manifest a
scanner can match; without it a signed VEX can name an image nobody ships.`)
	}
	for _, op := range modes["projection"] {
		if op.Digest == "" {
			t.Errorf(`projection mode is invoked without -digest, so nothing checks what the products are bound to:
  %s`, op.Command)
		}
	}
	for mode := range modes {
		if mode != "source" && mode != "projection" {
			t.Errorf("`openvex validate` is invoked with -mode %q, which this policy does not know", mode)
		}
	}
}

// TestOpenVEXProjectionIsCheckedAgainstThePlatformDigest pins what the
// projection check compares against. A projection validated against the index
// digest would pass while asserting a claim about a manifest no platform ships.
func TestOpenVEXProjectionIsCheckedAgainstThePlatformDigest(t *testing.T) {
	t.Parallel()
	ops := actionOps(loadAction(t, attestActionPath))

	bound := map[string]bool{}
	for _, op := range validations(ops) {
		if op.Mode != "projection" {
			continue
		}
		if op.Digest == indexDigestVar {
			t.Errorf(`the digest passed to projection mode resolves to %s.
A projection checked against the index digest asserts a claim about a manifest no
platform ships, so the check would pass on a document no scanner can match.
  %s`, indexDigestVar, op.Command)
		}
		bound[op.Digest] = true
	}
	for _, platform := range []string{amd64DigestVar, arm64DigestVar} {
		if !bound[platform] {
			t.Errorf(`no projection is validated against %s, so that platform's VEX document is signed without anything checking what its products bind to`, platform)
		}
	}
}

// TestOpenVEXSourceIsValidatedBeforeItIsBound keeps the order meaningful. A
// source that violates the contract cannot produce a projection worth signing,
// and a projection is only worth checking after binding has rewritten it.
func TestOpenVEXSourceIsValidatedBeforeItIsBound(t *testing.T) {
	t.Parallel()
	commands := evidenceCommands(t)

	source := indexOfOpenVEX(commands, "validate", "-mode source")
	bind := indexOfOpenVEX(commands, "bind")
	projection := indexOfOpenVEX(commands, "validate", "-mode projection")
	if source < 0 || bind < 0 || projection < 0 {
		t.Fatalf("expected a source validation, a bind and a projection validation, got %d/%d/%d:\n%s",
			source, bind, projection, strings.Join(commands, "\n"))
	}
	if !(source < bind && bind < projection) {
		t.Errorf("expected source validation -> bind -> projection validation, got positions %d, %d, %d:\n%s",
			source, bind, projection, strings.Join(commands, "\n"))
	}
}

// TestOpenVEXToolsRunFromTheToolsModule pins where the commands run from. The
// tools module is separate from operator/, so `go run ./cmd/openvex` issued
// from the workspace root resolves to nothing.
//
// The directory travels with each command as `go -C <dir>` rather than through
// a preceding `cd` in a subshell. That is deliberate and it is what makes this
// assertion sound: a `cd` inside one subshell says nothing about a command in
// the next one, so a per-platform `cd` could be deleted while a test that only
// compared positions against the first `cd` kept passing.
func TestOpenVEXToolsRunFromTheToolsModule(t *testing.T) {
	t.Parallel()
	const wantDirectory = "${GITHUB_WORKSPACE}/tools"

	var found int
	for _, command := range evidenceCommands(t) {
		directory, subcommand, ok := openvexSubcommand(command)
		if !ok {
			continue
		}
		found++
		if directory != wantDirectory {
			t.Errorf(`openvex %s runs with -C %q, want %q.
Without it the command resolves against whatever module the step happens to be
in, which is not the one holding ./cmd/openvex.
  %s`, subcommand, directory, wantDirectory, command)
		}
	}
	if found == 0 {
		t.Error("the evidence step runs no ./cmd/openvex command at all")
	}
}

// TestOpenVEXRulesHaveOneImplementation guards the reason the bash guard was
// replaced rather than kept alongside the Go command. Two copies of the OpenVEX
// contract drift, and the copy that drifts is the one nothing unit-tests.
func TestOpenVEXRulesHaveOneImplementation(t *testing.T) {
	t.Parallel()

	removed := filepath.Join(repoRoot(t), ".github/actions/cosign-attest-multiplatform/openvex-guard.sh")
	if _, err := os.Stat(removed); err == nil {
		t.Errorf("%s is back; the OpenVEX contract lives in tools/internal/openvex, and a second copy in bash cannot be unit-tested", removed)
	}

	action := readRepoFile(t, attestActionPath)
	for _, ghost := range []string{"openvex-guard.sh", "validate_openvex"} {
		if strings.Contains(action, ghost) {
			t.Errorf("%s still references %q, which the Go validator replaced", attestActionPath, ghost)
		}
	}
}
