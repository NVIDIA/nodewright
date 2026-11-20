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

package agentrules

import (
	"reflect"
	"strings"
	"testing"
)

const validRule = `---
summary: "The CLI stays compatible with older operators"
paths:
  - "operator/cmd/cli/**"
  - "operator/api/**"
---
# CLI contract

See ` + "`docs/user-guide/cli.md`" + ` and [the spec](https://example.com/spec#x).
`

func TestParseRuleValid(t *testing.T) {
	got, err := ParseRule("cli-contract", []byte(validRule))
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	want := Rule{
		Name:    "cli-contract",
		Summary: "The CLI stays compatible with older operators",
		Paths:   []string{"operator/cmd/cli/**", "operator/api/**"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRule = %+v, want %+v", got, want)
	}
}

func TestParseRuleErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "no frontmatter",
			content: "# Title\n",
			wantErr: "must start with a --- frontmatter line",
		},
		{
			name:    "unterminated frontmatter",
			content: "---\nsummary: \"x\"\npaths:\n  - \"a/**\"\n# Title\n",
			wantErr: "frontmatter is not closed",
		},
		{
			name:    "missing summary",
			content: "---\npaths:\n  - \"a/**\"\n---\n",
			wantErr: "missing summary",
		},
		{
			name:    "missing paths",
			content: "---\nsummary: \"x\"\n---\n",
			wantErr: "missing paths",
		},
		{
			name:    "empty paths",
			content: "---\nsummary: \"x\"\npaths:\n---\n",
			wantErr: "missing paths",
		},
		{
			name:    "unquoted path",
			content: "---\nsummary: \"x\"\npaths:\n  - **/*.go\n---\n",
			wantErr: "must be double-quoted",
		},
		{
			name:    "unquoted summary",
			content: "---\nsummary: x: y\npaths:\n  - \"a/**\"\n---\n",
			wantErr: "must be double-quoted",
		},
		{
			name:    "quote inside value",
			content: "---\nsummary: \"say \"hi\"\"\npaths:\n  - \"a/**\"\n---\n",
			wantErr: "must not contain",
		},
		{
			name:    "pipe inside path",
			content: "---\nsummary: \"x\"\npaths:\n  - \"a|b\"\n---\n",
			wantErr: "must not contain",
		},
		{
			name:    "unknown key",
			content: "---\nsummary: \"x\"\nglobs: \"a/**\"\npaths:\n  - \"a/**\"\n---\n",
			wantErr: "unexpected frontmatter line",
		},
		{
			name:    "no space after summary colon",
			content: "---\nsummary:\"x\"\npaths:\n  - \"a/**\"\n---\n",
			wantErr: "unexpected frontmatter line",
		},
		{
			name:    "summary twice",
			content: "---\nsummary: \"x\"\nsummary: \"y\"\npaths:\n  - \"a/**\"\n---\n",
			wantErr: "summary: appears more than once",
		},
		{
			name:    "paths twice",
			content: "---\nsummary: \"x\"\npaths:\n  - \"a/**\"\npaths:\n  - \"b/**\"\n---\n",
			wantErr: "paths: appears more than once",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRule("r", []byte(tc.content))
			if err == nil {
				t.Fatalf("ParseRule succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseRule error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
