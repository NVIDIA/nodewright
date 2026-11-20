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

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/NVIDIA/nodewright/tools/internal/agentrules"
)

const usage = `agentrules keeps the coding-agent rule files in step.

Usage:
  agentrules <command> -root <repository root>

Commands:
  index   Regenerate the area index in .claude/CLAUDE.md from the rule files
          in .claude/rules/.
  check   Fail if a rule file is malformed or not a regular file, the index
          is stale, or .claude/CLAUDE.md is over its size limit.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	command := os.Args[1]
	if command == "-h" || command == "--help" || command == "help" {
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	}

	fs := flag.NewFlagSet(command, flag.ExitOnError)
	root := fs.String("root", ".", "repository root")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	switch command {
	case "index":
		if err := agentrules.WriteIndex(*root); err != nil {
			fmt.Fprintf(os.Stderr, "agentrules index: %v\n", err)
			os.Exit(1)
		}
	case "check":
		errs := agentrules.Check(*root)
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "agentrules check: %v\n", err)
		}
		if len(errs) > 0 {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "agentrules: unknown command %q\n\n", command)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
