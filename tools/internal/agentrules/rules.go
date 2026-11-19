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

// Package agentrules keeps the per-area coding-agent rule files in
// .claude/rules and the index in the root agent guide in step.
package agentrules

import (
	"errors"
	"fmt"
	"strings"
)

// Rule is one area rule file under RulesDir.
type Rule struct {
	Name    string
	Summary string
	Paths   []string
}

const frontmatterDelim = "---"

// ParseRule parses a rule file's frontmatter.
//
// The frontmatter is a strict subset of YAML parsed by hand so the module
// stays stdlib-only. Values must be double-quoted: Claude Code parses this
// block as YAML, and an unquoted glob such as **/*.go is a YAML alias, which
// makes Claude Code drop the paths silently and load the rule everywhere.
func ParseRule(name string, content []byte) (Rule, error) {
	r := Rule{Name: name}
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 || lines[0] != frontmatterDelim {
		return r, errors.New("must start with a --- frontmatter line")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == frontmatterDelim {
			end = i
			break
		}
	}
	if end < 0 {
		return r, errors.New("frontmatter is not closed with a --- line")
	}

	inPaths, seenSummary, seenPaths := false, false, false
	for _, line := range lines[1:end] {
		switch {
		case strings.HasPrefix(line, "summary: "):
			if seenSummary {
				return r, errors.New("summary: appears more than once")
			}
			seenSummary, inPaths = true, false
			v, err := quoted("summary", strings.TrimSpace(strings.TrimPrefix(line, "summary: ")))
			if err != nil {
				return r, err
			}
			r.Summary = v
		case line == "paths:":
			if seenPaths {
				return r, errors.New("paths: appears more than once")
			}
			seenPaths, inPaths = true, true
		case inPaths && strings.HasPrefix(line, "  - "):
			v, err := quoted("path", strings.TrimPrefix(line, "  - "))
			if err != nil {
				return r, err
			}
			r.Paths = append(r.Paths, v)
		default:
			return r, fmt.Errorf("unexpected frontmatter line %q: only summary: and a paths: list are allowed", line)
		}
	}
	if r.Summary == "" {
		return r, errors.New("missing summary")
	}
	if len(r.Paths) == 0 {
		return r, errors.New("missing paths")
	}
	return r, nil
}

func quoted(field, v string) (string, error) {
	if len(v) < 2 || !strings.HasPrefix(v, `"`) || !strings.HasSuffix(v, `"`) {
		return "", fmt.Errorf("%s %s must be double-quoted", field, v)
	}
	inner := v[1 : len(v)-1]
	if strings.ContainsAny(inner, "\"\\|") {
		return "", fmt.Errorf("%s %s must not contain \", \\ or |", field, v)
	}
	return inner, nil
}
