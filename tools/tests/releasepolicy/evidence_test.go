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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the evidence layout golden file from the current actions and workflows")

const goldenPath = "testdata/evidence-layout.golden"

// Column widths are fixed rather than computed from the widest row so that
// adding one operation changes one line of the golden instead of reflowing the
// whole file into an unreadable diff. A detail longer than the column simply
// overflows: the rows that run long are the openvex argument lists, and nothing
// follows them on the line.
const (
	goldenLabelWidth  = 28
	goldenDetailWidth = 22
)

// evidenceOp is one operation the release path performs, reduced to what the
// policy is about: what it publishes or checks, against which subject, and
// under which flags.
type evidenceOp struct {
	File    string
	Step    string
	Label   string // "cosign attest cyclonedx", "openvex validate"
	Subject string // digest variable the operation publishes against
	Mode    string // `openvex validate`'s -mode
	Digest  string // digest variable an openvex tool is handed
	Bundle  bool   // --new-bundle-format=true is passed explicitly
	Timeout string // the deadline the call is bounded by, empty when unbounded
	Command string // the command line itself, for failure messages
}

// shellScope resolves an expression against the assignments a step's script
// makes, leaving variables the script does not assign as written. Those
// untouched names are the step's `env:`, which is where the action ties a name
// to an input or to another step's output, and they are what both the golden
// and the subject assertions are written against.
type shellScope struct {
	bindings map[string]string
}

var (
	// caseArm matches a `<pattern>) ...` case arm, the only place these scripts
	// bind a variable conditionally.
	caseArm = regexp.MustCompile(`^([A-Za-z0-9_*]+)\)\s`)
	// assignment matches a simple `name="value"` shell assignment.
	assignment = regexp.MustCompile(`(?:^|[\s;])([A-Za-z_][A-Za-z0-9_]*)="([^"]*)"`)
	// varReference matches a plain `${NAME}`. Parameter expansions such as
	// `${INDEX_DIGEST#sha256:}` deliberately do not match: nothing a subject
	// resolves through uses one, and half-expanding one would be worse than
	// leaving it alone.
	varReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

func newShellScope(script, arch string) shellScope {
	bindings := map[string]string{}
	if arch != "" {
		bindings["arch"] = arch
	}
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A case arm binds only under the architecture it selects. Without this
		// the last arm would win and every per-platform subject would resolve
		// to arm64.
		if arm := caseArm.FindStringSubmatch(trimmed); arm != nil {
			if arm[1] != "*" && arm[1] != arch {
				continue
			}
			trimmed = strings.TrimSpace(trimmed[len(arm[0]):])
		}
		for _, match := range assignment.FindAllStringSubmatch(trimmed, -1) {
			// A command substitution is not a value this resolver can carry,
			// and the regex would capture it truncated at its first inner
			// quote. Leaving the name unbound renders it as itself, which is
			// honest; a truncated value would not be.
			if strings.Contains(match[2], "$(") {
				continue
			}
			bindings[match[1]] = match[2]
		}
	}
	return shellScope{bindings: bindings}
}

func (s shellScope) expand(expression string) string {
	return s.expandDepth(expression, 0)
}

func (s shellScope) expandDepth(expression string, depth int) string {
	if depth >= 10 {
		return expression
	}
	return varReference.ReplaceAllStringFunc(expression, func(reference string) string {
		name := varReference.FindStringSubmatch(reference)[1]
		value, assigned := s.bindings[name]
		if !assigned {
			return reference
		}
		return s.expandDepth(value, depth+1)
	})
}

// digestRef reduces a resolved OCI reference to the part after its final `@`,
// which is the subject the evidence hangs on, and strips `${}` so the result is
// the bare variable name the policy is written in terms of.
func digestRef(reference string) string {
	reference = strings.Trim(reference, `"`)
	if at := strings.LastIndex(reference, "@"); at >= 0 {
		reference = reference[at+1:]
	}
	if match := varReference.FindStringSubmatch(reference); match != nil && match[0] == reference {
		return match[1]
	}
	return reference
}

// segment is a slice of a step's script together with the scope its commands
// resolve in.
type segment struct {
	arch  string
	body  string
	scope shellScope
}

var archLoopStart = regexp.MustCompile(`^for arch in ([a-z0-9 ]+); do$`)

// unroll splits a step's script into the part that runs once and the part that
// runs once per architecture. The per-platform work is a literal
// `for arch in amd64 arm64` loop, and what this package is about is the layout
// as published rather than as written, so the loop is unrolled here: each
// platform's evidence gets its own operation.
func unroll(script string) []segment {
	lines := strings.Split(script, "\n")
	start, arches := -1, []string(nil)
	for i, line := range lines {
		if match := archLoopStart.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			start, arches = i, strings.Fields(match[1])
			break
		}
	}
	if start < 0 {
		return []segment{{body: script, scope: newShellScope(script, "")}}
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "done" && leadingSpaces(lines[i]) == leadingSpaces(lines[start]) {
			end = i
			break
		}
	}
	preamble := strings.Join(lines[:start], "\n")
	body := strings.Join(lines[start+1:end], "\n")
	tail := ""
	if end+1 < len(lines) {
		tail = strings.Join(lines[end+1:], "\n")
	}

	segments := []segment{{body: preamble, scope: newShellScope(preamble, "")}}
	for _, arch := range arches {
		segments = append(segments, segment{
			arch:  arch,
			body:  body,
			scope: newShellScope(preamble+"\n"+body, arch),
		})
	}
	return append(segments, segment{body: tail, scope: newShellScope(preamble+"\n"+tail, "")})
}

func lastField(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func flagValue(command, flag string) string {
	fields := strings.Fields(command)
	for i, field := range fields {
		if field == flag && i+1 < len(fields) {
			return strings.Trim(fields[i+1], `"`)
		}
	}
	return ""
}

// classify turns a command line into the operation it performs, or reports that
// it performs none. Anything that neither publishes evidence nor validates it
// is not part of the layout.
// openvexIs reports whether a command runs the given ./cmd/openvex subcommand.
func openvexIs(command, subcommand string) bool {
	_, got, ok := openvexSubcommand(command)
	return ok && got == subcommand
}

func classify(command string, scope shellScope) (evidenceOp, bool) {
	op := evidenceOp{Command: command}
	switch {
	case invokes(command, "cosign sign"):
		op.Label = "cosign sign"
		op.Subject = digestRef(scope.expand(lastField(command)))
		op.Timeout, _ = timeoutFor(command, "cosign sign")
	case invokes(command, "cosign attest"):
		op.Label = "cosign attest " + flagValue(command, "--type")
		op.Subject = digestRef(scope.expand(lastField(command)))
		op.Timeout, _ = timeoutFor(command, "cosign attest")
	case openvexIs(command, "validate"):
		op.Label = "openvex validate"
		op.Mode = flagValue(command, "-mode")
		op.Digest = digestRef(scope.expand(flagValue(command, "-digest")))
	case openvexIs(command, "bind"):
		op.Label = "openvex bind"
		op.Digest = digestRef(scope.expand(flagValue(command, "-digest")))
	default:
		return evidenceOp{}, false
	}
	op.Bundle = hasFlag(command, "--new-bundle-format=true")
	return op, true
}

// actionOps returns every evidence operation a composite action performs, in
// published order.
func actionOps(action CompositeAction) []evidenceOp {
	var ops []evidenceOp
	for _, step := range action.Runs.Steps {
		for _, part := range unroll(step.Run) {
			for _, command := range shellCommands(part.body) {
				op, ok := classify(command, part.scope)
				if !ok {
					continue
				}
				op.File, op.Step = action.path, step.Name
				ops = append(ops, op)
			}
		}
	}
	return ops
}

// attestations returns the operations publishing SBOM and VEX predicates.
func attestations(ops []evidenceOp) []evidenceOp {
	var matched []evidenceOp
	for _, op := range ops {
		if strings.HasPrefix(op.Label, "cosign attest") {
			matched = append(matched, op)
		}
	}
	return matched
}

// signatures returns the operations publishing a cosign signature.
func signatures(ops []evidenceOp) []evidenceOp {
	var matched []evidenceOp
	for _, op := range ops {
		if op.Label == "cosign sign" {
			matched = append(matched, op)
		}
	}
	return matched
}

// validations returns the `openvex validate` operations, in run order.
func validations(ops []evidenceOp) []evidenceOp {
	var matched []evidenceOp
	for _, op := range ops {
		if op.Label == "openvex validate" {
			matched = append(matched, op)
		}
	}
	return matched
}

// evidenceWiring is one digest a workflow hands to an evidence action. The
// actions decide what hangs where; only the workflow decides what they are
// given, so a subject can move without any action changing.
type evidenceWiring struct {
	Action string
	Input  string
	Value  string
}

var wiringActions = []struct {
	reference string
	label     string
	inputs    []string
}{
	{"./.github/actions/cosign-attest-multiplatform", "cosign-attest-multiplatform", []string{"index-digest"}},
	{"./.github/actions/cosign-sign-sbom", "cosign-sign-sbom", []string{"subject-digest"}},
	{"actions/attest-build-provenance", "attest-build-provenance", []string{"subject-digest"}},
	{"./.github/actions/cosign-verify-release", "cosign-verify-release", []string{"subject-digest", "amd64-digest", "arm64-digest"}},
}

func workflowWiring(workflow Workflow) []evidenceWiring {
	var rows []evidenceWiring
	for _, step := range workflow.steps() {
		for _, candidate := range wiringActions {
			if !strings.HasPrefix(step.Uses, candidate.reference) {
				continue
			}
			for _, input := range candidate.inputs {
				if value, present := step.With[input]; present {
					rows = append(rows, evidenceWiring{Action: candidate.label, Input: input, Value: value})
				}
			}
			break
		}
	}
	return rows
}

type digestBinding struct {
	name  string
	value string
}

// digestBindings reports where an action's digest variables come from. Without
// them the subjects below would pin nothing but names: a step could set
// AMD64_DIGEST from the index input and still read as per-platform.
func digestBindings(action CompositeAction) []digestBinding {
	var bindings []digestBinding
	seen := map[string]bool{}
	for _, step := range action.Runs.Steps {
		for _, name := range []string{indexDigestVar, subjectDigestVar, amd64DigestVar, arm64DigestVar} {
			value, present := step.Env[name]
			if !present || seen[name] {
				continue
			}
			seen[name] = true
			bindings = append(bindings, digestBinding{name: name, value: value})
		}
	}
	return bindings
}

const goldenHeader = `# The evidence layout this release path publishes, rendered from the composite
# actions and workflows that publish it. Regenerate with:
#
#     cd tools/tests && go test ./releasepolicy -update
#
# Read a diff here as a claim about what a release publishes. A line whose arrow
# target moved between the index digest and a platform digest is the bug this
# package exists to catch, and regenerating the golden would hide it. That is
# why the subject rules, the cosign flags and the version pins are ALSO asserted
# by hand in release_workflow_test.go, where -update cannot reach them.
#
# Subjects are shown as the shell variable carrying the digest; the env lines
# under each action say where that variable comes from. The per-platform
# ` + "`for arch`" + ` loops are unrolled, so each platform's evidence gets its own line.
#
# cosign-verify-release is not rendered: its cosign calls take their subject
# through a shell function parameter, which this renderer does not follow. The
# workflow sections show which digests each workflow hands it.
`

func renderOp(op evidenceOp) string {
	var attributes []string
	if op.Bundle {
		attributes = append(attributes, "--new-bundle-format=true")
	}
	if op.Timeout != "" {
		attributes = append(attributes, "timeout "+op.Timeout)
	}

	detail := ""
	switch {
	case op.Subject != "":
		detail = "-> @" + op.Subject
	default:
		if op.Mode != "" {
			detail = "-mode " + op.Mode
		}
		if op.Digest != "" {
			detail = strings.TrimSpace(detail + " -digest @" + op.Digest)
		}
	}

	line := fmt.Sprintf("%-*s %-*s", goldenLabelWidth, op.Label, goldenDetailWidth, detail)
	if len(attributes) > 0 {
		line += "[" + strings.Join(attributes, ", ") + "]"
	}
	return strings.TrimRight(line, " ") + "\n"
}

func renderEvidenceLayout(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	out.WriteString(goldenHeader)

	for _, path := range []string{attestActionPath, chartSignActionPath} {
		action := loadAction(t, path)
		fmt.Fprintf(&out, "\n== %s ==\n", path)
		for _, binding := range digestBindings(action) {
			fmt.Fprintf(&out, "env %-*s = %s\n", goldenLabelWidth-4, binding.name, binding.value)
		}
		out.WriteString("\n")
		for _, op := range actionOps(action) {
			out.WriteString(renderOp(op))
		}
	}

	for _, path := range []string{operatorCIPath, agentCIPath, releasePath} {
		fmt.Fprintf(&out, "\n== %s ==\n", path)
		for _, row := range workflowWiring(loadWorkflow(t, path)) {
			fmt.Fprintf(&out, "%-*s %-14s -> %s\n", goldenLabelWidth, row.Action, row.Input, row.Value)
		}
	}
	return out.String()
}

// lineDiff names the lines that differ rather than printing both copies of the
// layout. It compares by position, so an inserted line reports everything after
// it as changed; the first reported line is still the one to read.
func lineDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	var out strings.Builder
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		wantLine, gotLine := "", ""
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine == gotLine {
			continue
		}
		fmt.Fprintf(&out, "  line %d:\n    golden:   %s\n    actual:   %s\n", i+1, wantLine, gotLine)
	}
	return out.String()
}

// TestEvidenceLayoutMatchesGolden renders the whole published layout into one
// artifact so a subject moving is visible as a diff rather than as an absent
// assertion. It deliberately does not carry the subject rules themselves: its
// repair mechanism is -update, and a rule a refactor can regenerate away is not
// a rule. Those live in release_workflow_test.go.
func TestEvidenceLayoutMatchesGolden(t *testing.T) {
	rendered := renderEvidenceLayout(t)
	path := filepath.FromSlash(goldenPath)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nregenerate it with `go test ./releasepolicy -update`", path, err)
	}
	if string(want) != rendered {
		t.Errorf(`the published evidence layout no longer matches %s:
%s
If this change is intended, regenerate with `+"`go test ./releasepolicy -update`"+` and read the
diff as a change to what a release publishes. An arrow target that moved between
the index digest and a platform digest is not a stale golden.`, path, lineDiff(string(want), rendered))
	}
}
