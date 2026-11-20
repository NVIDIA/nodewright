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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ruleA = "---\nsummary: \"Rule A\"\npaths:\n  - \"a/**\"\n---\n# A\n"
	ruleB = "---\nsummary: \"Rule B\"\npaths:\n  - \"b/**\"\n  - \"**/*_b.go\"\n---\n# B\n"
)

const wantIndex = IndexStart + "\n" +
	"| If you touch | Read first | Because |\n" +
	"| --- | --- | --- |\n" +
	"| `a/**` | `.claude/rules/a.md` | Rule A |\n" +
	"| `b/**`, `**/*_b.go` | `.claude/rules/b.md` | Rule B |\n" +
	IndexEnd

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validTree builds a repository layout that passes Check.
func validTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, RulesDir, "b.md"), ruleB)
	writeFile(t, filepath.Join(root, RulesDir, "a.md"), ruleA)
	writeFile(t, filepath.Join(root, GuidePath), "# Guide\n\n"+wantIndex+"\n\n## After\n")
	return root
}

func TestLoadRulesSortsByName(t *testing.T) {
	rules, err := LoadRules(validTree(t))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rules) != 2 || rules[0].Name != "a" || rules[1].Name != "b" {
		t.Fatalf("LoadRules = %+v, want rules a and b in order", rules)
	}
}

func TestRenderIndex(t *testing.T) {
	rules, err := LoadRules(validTree(t))
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if got := RenderIndex(rules); got != wantIndex {
		t.Fatalf("RenderIndex =\n%s\nwant\n%s", got, wantIndex)
	}
}

func TestCheckValidTree(t *testing.T) {
	if errs := Check(validTree(t)); len(errs) != 0 {
		t.Fatalf("Check on a valid tree: %v", errors.Join(errs...))
	}
}

func TestCheckFailures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, root string)
		wantErr string
	}{
		{
			name: "no rules",
			mutate: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, RulesDir)); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "no rule files",
		},
		{
			name: "invalid rule file",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, RulesDir, "a.md"), "# no frontmatter\n")
			},
			wantErr: "a.md",
		},
		{
			name: "file without paths would load in every session",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, RulesDir, "README.md"), "# Notes\n")
			},
			wantErr: "README.md",
		},
		{
			name: "symlinked rule",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "docs", "c.md"), ruleA)
				if err := os.Symlink("../../docs/c.md", filepath.Join(root, RulesDir, "c.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "must be a regular file",
		},
		{
			name: "nested rule file",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, RulesDir, "nested", "README.md"), "# Notes\n")
			},
			wantErr: "only top-level .md rule files",
		},
		{
			name: "index markers missing",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, GuidePath), "# Guide\n")
			},
			wantErr: "index markers",
		},
		{
			name: "stale index",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, RulesDir, "a.md"), strings.Replace(ruleA, "Rule A", "Rule A changed", 1))
			},
			wantErr: "make agent-rules-index",
		},
		{
			name: "guide too large",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, GuidePath), "# Guide\n\n"+wantIndex+"\n"+strings.Repeat("x", MaxGuideBytes))
			},
			wantErr: "bytes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := validTree(t)
			tc.mutate(t, root)
			err := errors.Join(Check(root)...)
			if err == nil {
				t.Fatalf("Check succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Check error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestWriteIndexReplacesBlockAndIsIdempotent(t *testing.T) {
	root := validTree(t)
	guide := filepath.Join(root, GuidePath)
	writeFile(t, guide, "# Guide\n\n"+IndexStart+"\nold\n"+IndexEnd+"\n\n## After\n")

	for i := range 2 {
		if err := WriteIndex(root); err != nil {
			t.Fatalf("WriteIndex: %v", err)
		}
		got, err := os.ReadFile(guide)
		if err != nil {
			t.Fatal(err)
		}
		if want := "# Guide\n\n" + wantIndex + "\n\n## After\n"; string(got) != want {
			t.Fatalf("pass %d: guide =\n%s\nwant\n%s", i, got, want)
		}
	}
}
