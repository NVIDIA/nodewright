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

// Command openvex is the release evidence tool for the repository's OpenVEX
// document. It binds .openvex.json to a platform manifest digest and checks
// both the committed source and the resulting projection against the OpenVEX
// v0.2.0 contract, so a release cannot publish a VEX attestation no scanner
// can apply.
//
// The two halves are one binary because the release step runs them over the
// same documents in the same loop, and a shared package is what keeps their
// idea of "an OpenVEX document" from diverging.
//
// This file does dispatch and nothing else. Each subcommand's flags, rules and
// exit status live with its implementation in internal/openvex.
package main

import (
	"fmt"
	"os"

	"github.com/NVIDIA/nodewright/tools/internal/openvex"
)

const usage = `openvex binds and validates the repository's OpenVEX document.

Usage:
  openvex <command> [flags]

Commands:
  bind        Project .openvex.json onto one platform manifest digest, rewriting
              every kept statement's products to pkg:oci/<name>@<digest>.
              openvex bind -in .openvex.json -out amd64.openvex.json \
                -image ghcr.io/nvidia/nodewright/operator -digest sha256:<64 hex>

  validate    Check one OpenVEX document against the v0.2.0 contract the release
              depends on. Source mode checks the committed document; projection
              mode additionally requires every product bound to the given digest.
              openvex validate -mode source -in .openvex.json
              openvex validate -mode projection -in amd64.openvex.json -digest sha256:<64 hex>

Run "openvex <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	command, args := os.Args[1], os.Args[2:]
	// Neither subcommand is handed os.Stdin. Both read their document from
	// -in, so a stdin parameter would be a wire nothing ever pulls on, and a
	// reader that is never read is the kind of thing a later change quietly
	// starts depending on.
	switch command {
	case "bind":
		os.Exit(openvex.RunBind(args, os.Stdout, os.Stderr))
	case "validate":
		os.Exit(openvex.RunValidate(args, os.Stdout, os.Stderr))
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "openvex: unknown command %q\n\n", command)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
