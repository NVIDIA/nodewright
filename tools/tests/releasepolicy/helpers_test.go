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

// Package releasepolicy pins the release evidence policy: which subject each
// piece of container image evidence is published against, how cosign and crane
// are invoked, and that both OpenVEX documents are validated before anything is
// signed. The policy is spread across two composite actions and three
// workflows, so a refactor that moves a subject or drops a flag looks local and
// is not. The OpenVEX rules themselves are unit-tested in
// tools/internal/openvex; what is pinned here is that the action still calls
// the validator, in both modes.
//
// This package is a Go module of its own. The parent tools module has no
// third-party dependencies, which is what lets the release job run
// `go run ./cmd/openvex bind` with a toolchain and no module download, and a
// directory holding its own go.mod is invisible to the parent's package
// pattern. The tests therefore read these files with a real YAML parser without
// putting a dependency anywhere near the release path.
//
// The published layout as a whole is pinned by a golden file, rendered in
// evidence_test.go. The rules that must NOT be repairable by regenerating a
// golden -- which subject each attestation targets, which flags every cosign
// call carries, which cosign version each file installs -- are asserted by hand
// in release_workflow_test.go instead.
package releasepolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Repository-relative paths of everything the policy lives in.
const (
	attestActionPath = ".github/actions/cosign-attest-multiplatform/action.yml"
	verifyActionPath = ".github/actions/cosign-verify-release/action.yml"
	// The Helm chart's single-subject signing path. It is not part of the
	// per-platform split, but it shares the cosign pin and the bundle format
	// with the actions that are, and it is verified by the same verify action.
	chartSignActionPath = ".github/actions/cosign-sign-sbom/action.yml"
	operatorCIPath      = ".github/workflows/operator-ci.yaml"
	agentCIPath         = ".github/workflows/agent-ci.yaml"
	releasePath         = ".github/workflows/release.yml"
	openVEXPath         = ".openvex.json"
)

// Shell variables the actions use to carry each kind of digest. The subject
// assertions are written against these names because the digests themselves are
// only known at release time.
const (
	indexDigestVar   = "INDEX_DIGEST"
	amd64DigestVar   = "AMD64_DIGEST"
	arm64DigestVar   = "ARM64_DIGEST"
	subjectDigestVar = "SUBJECT_DIGEST"
)

// Step is the subset of a GitHub Actions step this policy reads. Both a
// composite action's `runs.steps` and a workflow job's `steps` decode into it.
type Step struct {
	Name string    `yaml:"name"`
	ID   string    `yaml:"id"`
	Uses string    `yaml:"uses"`
	Run  string    `yaml:"run"`
	With stringMap `yaml:"with"`
	Env  stringMap `yaml:"env"`
}

// stringMap decodes a `with:` or `env:` mapping, taking every value as the
// scalar text the author wrote. A plain map[string]string cannot: GitHub
// Actions inputs are typed by YAML, so `push-to-registry: true` decodes as a
// bool and fails the whole document.
type stringMap map[string]string

func (m *stringMap) UnmarshalYAML(node *yaml.Node) error {
	*m = stringMap{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		(*m)[node.Content[i].Value] = node.Content[i+1].Value
	}
	return nil
}

// ActionInput is a composite action's declared input. Only the default matters
// here: it is where the cosign version pin lives.
type ActionInput struct {
	Description string `yaml:"description"`
	Default     string `yaml:"default"`
}

// CompositeAction is an action.yml.
type CompositeAction struct {
	Name   string                 `yaml:"name"`
	Inputs map[string]ActionInput `yaml:"inputs"`
	Runs   struct {
		Using string `yaml:"using"`
		Steps Steps  `yaml:"steps"`
	} `yaml:"runs"`

	// path is the repository-relative path it was read from, so failure
	// messages can name the file without the caller repeating it.
	path string
}

// Job is one workflow job.
type Job struct {
	Name  string `yaml:"name"`
	Steps Steps  `yaml:"steps"`
}

// Workflow is a workflow YAML file.
type Workflow struct {
	Name string  `yaml:"name"`
	Jobs jobList `yaml:"jobs"`

	path string
}

// NamedJob keeps a job together with its id.
type NamedJob struct {
	ID  string
	Job Job
}

// jobList decodes `jobs:` in document order. A map would decode fine and then
// iterate randomly, which would reshuffle the golden file on every run.
type jobList []NamedJob

func (l *jobList) UnmarshalYAML(node *yaml.Node) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		var job Job
		if err := node.Content[i+1].Decode(&job); err != nil {
			return err
		}
		*l = append(*l, NamedJob{ID: node.Content[i].Value, Job: job})
	}
	return nil
}

// steps returns every step the workflow declares, in file order.
func (w Workflow) steps() Steps {
	var all Steps
	for _, job := range w.Jobs {
		all = append(all, job.Job.Steps...)
	}
	return all
}

// Steps is a step list with the lookups these assertions need.
type Steps []Step

// named returns the single step carrying this name, failing the test when there
// is no such step: a renamed step silently dropping an assertion is the failure
// mode worth making loud.
func (s Steps) named(t *testing.T, name string) Step {
	t.Helper()
	for _, step := range s {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("no step named %q", name)
	return Step{}
}

// using returns every step whose `uses:` starts with the given reference, which
// is how a pinned action (`owner/action@sha`) is matched by name alone.
func (s Steps) using(reference string) Steps {
	var matched Steps
	for _, step := range s {
		if strings.HasPrefix(step.Uses, reference) {
			matched = append(matched, step)
		}
	}
	return matched
}

// ids returns every step id the list declares.
func (s Steps) ids() map[string]bool {
	declared := map[string]bool{}
	for _, step := range s {
		if step.ID != "" {
			declared[step.ID] = true
		}
	}
	return declared
}

func loadAction(t *testing.T, relative string) CompositeAction {
	t.Helper()
	action := CompositeAction{path: relative}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, relative)), &action); err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	if len(action.Runs.Steps) == 0 {
		t.Fatalf("%s declares no steps", relative)
	}
	return action
}

func loadWorkflow(t *testing.T, relative string) Workflow {
	t.Helper()
	workflow := Workflow{path: relative}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, relative)), &workflow); err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatalf("%s declares no jobs", relative)
	}
	return workflow
}

// repoRoot walks up from the working directory until it finds the checkout
// root, recognized by holding both `.github` and `.openvex.json`. A hardcoded
// "../../.." would silently start reading the wrong tree the moment this
// package moves a level.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		_, githubErr := os.Stat(filepath.Join(dir, ".github"))
		_, vexErr := os.Stat(filepath.Join(dir, openVEXPath))
		if githubErr == nil && vexErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no repository root above the working directory: looked for a directory holding both .github and %s", openVEXPath)
		}
		dir = parent
	}
}

// readRepoFile returns the contents of a repository-relative path.
func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), relative)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(contents)
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// shellCommands flattens a run body into logical command lines: backslash
// continuations are joined, whitespace is collapsed, and comment lines are
// dropped. Comments matter here because the actions discuss `cosign attest` in
// prose right next to the invocations, and a prose mention is not a command.
func shellCommands(script string) []string {
	var commands []string
	var current strings.Builder
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if current.Len() == 0 && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if strings.HasSuffix(trimmed, `\`) {
			current.WriteString(strings.TrimSuffix(trimmed, `\`))
			current.WriteString(" ")
			continue
		}
		current.WriteString(trimmed)
		commands = append(commands, strings.Join(strings.Fields(current.String()), " "))
		current.Reset()
	}
	if current.Len() > 0 {
		commands = append(commands, strings.Join(strings.Fields(current.String()), " "))
	}
	return commands
}

// commandsInvoking returns every logical command line running the given
// program, across every step of a composite action or workflow.
// timeoutBefore reports the duration of a `timeout` wrapper ending immediately
// before position i, if there is one. The token holding `timeout` is matched by
// suffix because a wrapped call is often inside a substitution, which leaves it
// glued to what precedes it, for example `digest="$(timeout`.
func timeoutBefore(tokens []string, i int) (duration string, at int, ok bool) {
	j := i - 1
	if j < 0 {
		return "", 0, false
	}
	duration = tokens[j]
	for j--; j >= 0 && strings.HasPrefix(tokens[j], "-"); j-- {
	}
	if j < 0 || !strings.HasSuffix(tokens[j], "timeout") {
		return "", 0, false
	}
	return duration, j, true
}

// commandIntroducers are the tokens after which the next token is an executable
// rather than an argument.
var commandIntroducers = map[string]bool{
	"if": true, "!": true, "then": true, "else": true, "elif": true,
	"do": true, "while": true, "until": true,
	"|": true, "||": true, "&&": true, ";": true, "{": true, "(": true, "$(": true,
}

// commandPosition reports whether tokens[i] sits where a shell would take an
// executable. Matching a program anywhere in the line would count one that is
// only named, for example inside an `echo "::error::... cosign attest ..."`,
// and a bare first-token check would miss the real calls, which are wrapped in
// `timeout` and nested inside `if !  x="$( ... )"`.
func commandPosition(tokens []string, i int) bool {
	if i == 0 {
		return true
	}
	// The wrapper only confers a command position if it is in one itself.
	// Without that, `echo timeout 120s cosign sign` reads as a real call: the
	// tokens are identical to one, and only the enclosing executable says
	// otherwise. Recursion terminates because the wrapper is always earlier.
	if _, at, wrapped := timeoutBefore(tokens, i); wrapped && commandPosition(tokens, at) {
		return true
	}
	previous := tokens[i-1]
	if commandIntroducers[previous] {
		return true
	}
	return strings.HasSuffix(previous, "$(") || strings.HasSuffix(previous, "(")
}

// invocationIndex returns the token index at which command actually runs
// program, matching on whole tokens from a command position.
func invocationIndex(command, program string) (int, bool) {
	want := strings.Fields(program)
	tokens := strings.Fields(command)
	for i := 0; i+len(want) <= len(tokens); i++ {
		matched := true
		for offset, token := range want {
			if tokens[i+offset] != token {
				matched = false
				break
			}
		}
		if matched && commandPosition(tokens, i) {
			return i, true
		}
	}
	return 0, false
}

// invokes reports whether a command runs program. Token matching also keeps
// `cosign verify` from matching `cosign verify-attestation`: different
// commands, reading different things.
func invokes(command, program string) bool {
	_, ok := invocationIndex(command, program)
	return ok
}

// openvexSubcommand returns the `./cmd/openvex` subcommand a command runs, if
// it runs one. The module directory travels with the command as `go -C <dir>`
// rather than through a preceding `cd`, so a single command carries everything
// this package needs to check: which subcommand, and where it runs.
func openvexSubcommand(command string) (directory, subcommand string, ok bool) {
	if !invokes(command, "go") {
		return "", "", false
	}
	tokens := strings.Fields(command)
	for i, token := range tokens {
		if token == "-C" && i+1 < len(tokens) {
			directory = strings.Trim(tokens[i+1], `"`)
		}
		if token == "./cmd/openvex" && i+1 < len(tokens) {
			return directory, tokens[i+1], true
		}
	}
	return "", "", false
}

// hasFlag reports whether a command carries flag as a whole argument. A
// substring test would accept `--new-bundle-format=true-invalid`.
func hasFlag(command, flag string) bool {
	for _, token := range strings.Fields(command) {
		if token == flag {
			return true
		}
	}
	return false
}

func commandsInvoking(steps Steps, program string) []string {
	var matched []string
	for _, step := range steps {
		for _, command := range shellCommands(step.Run) {
			if invokes(command, program) {
				matched = append(matched, command)
			}
		}
	}
	return matched
}

// timeoutFor returns the deadline program is invoked under on this command
// line, and whether it is bounded at all.
func timeoutFor(command, program string) (string, bool) {
	at, ok := invocationIndex(command, program)
	if !ok {
		return "", false
	}
	duration, _, bounded := timeoutBefore(strings.Fields(command), at)
	return duration, bounded
}

func timeoutWrapped(command, program string) bool {
	_, bounded := timeoutFor(command, program)
	return bounded
}

// bashWithAssociativeArrays finds a bash that can run the action's scripts.
// The platform resolution step uses `declare -A`, which bash 3.2 (still
// /bin/bash on macOS) does not have; GitHub's runners ship bash 5.
func bashWithAssociativeArrays(t *testing.T) string {
	t.Helper()
	candidates := []string{"bash", "/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if err := exec.Command(path, "-c", "declare -A probe=([k]=v); [ \"${probe[k]}\" = v ]").Run(); err == nil {
			return path
		}
	}
	t.Skip("no bash with associative array support found; the action's scripts need bash 4 or newer")
	return ""
}

// requireJQ skips when jq is absent. The resolution script pipes
// `crane manifest` into jq under `set -euo pipefail`, so on a host without it
// the script fails for a reason that has nothing to do with the policy under
// test. Skipping loudly is the honest outcome; a silent pass would not be.
func requireJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not found; the action's platform resolution pipes crane manifest into jq")
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
