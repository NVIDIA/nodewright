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

import "testing"

// Every subject, flag and timeout assertion in this package is only as good as
// the matcher underneath it, so the matcher is tested rather than trusted. A
// substring match would count a command that merely names the program, which
// would let `echo "... cosign attest ..."` satisfy a check that no real
// attestation satisfies.
func TestInvokesMatchesCommandPositionsOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		program string
		want    bool
	}{
		{
			name:    "a bare invocation",
			command: `cosign sign --yes "${SUBJECT_NAME}@${INDEX_DIGEST}"`,
			program: "cosign sign",
			want:    true,
		},
		{
			name:    "wrapped in timeout",
			command: `timeout --foreground 120s cosign attest --yes --type openvex "${subject}"`,
			program: "cosign attest",
			want:    true,
		},
		{
			// The shape the real resolution step uses.
			name:    "nested in a conditional command substitution",
			command: `if ! digest="$(timeout --foreground 120s crane digest --platform "${platform}" "${subject}")"; then`,
			program: "crane digest",
			want:    true,
		},
		{
			name:    "piped",
			command: `timeout --foreground 120s crane manifest "${subject}" | jq -r '.manifests[]?'`,
			program: "crane manifest",
			want:    true,
		},
		{
			name:    "merely named in an error message",
			command: `echo "::error::cosign attest failed for ${subject}"`,
			program: "cosign attest",
			want:    false,
		},
		{
			name:    "merely named in a comment-like echo of a command",
			command: `echo timeout --foreground 120s cosign sign --new-bundle-format=true`,
			program: "cosign sign",
			want:    false,
		},
		{
			// The reason the bundle-format sweep names both forms explicitly.
			name:    "verify does not match verify-attestation",
			command: `timeout --foreground 120s cosign verify-attestation --type openvex "${target}"`,
			program: "cosign verify",
			want:    false,
		},
		{
			name:    "verify-attestation matches itself",
			command: `timeout --foreground 120s cosign verify-attestation --type openvex "${target}"`,
			program: "cosign verify-attestation",
			want:    true,
		},
		{
			name:    "a different subcommand is not a match",
			command: `timeout --foreground 120s cosign attest --type cyclonedx "${subject}"`,
			program: "cosign sign",
			want:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := invokes(test.command, test.program); got != test.want {
				t.Errorf("invokes(%q, %q) = %t, want %t", test.command, test.program, got, test.want)
			}
		})
	}
}

func TestTimeoutForResolvesTheWrapperOfTheMatchedCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		command  string
		program  string
		duration string
		bounded  bool
	}{
		{
			name:     "a wrapped call reports its duration",
			command:  `timeout --foreground 120s cosign attest --yes "${subject}"`,
			program:  "cosign attest",
			duration: "120s",
			bounded:  true,
		},
		{
			name:     "a wrapper inside a substitution still counts",
			command:  `if ! digest="$(timeout --foreground 120s crane digest --platform "${platform}" "${subject}")"; then`,
			program:  "crane digest",
			duration: "120s",
			bounded:  true,
		},
		{
			name:    "an unwrapped call is unbounded",
			command: `cosign attest --yes "${subject}"`,
			program: "cosign attest",
		},
		{
			// A timeout somewhere else on the line must not be credited to this
			// call, which is what a "first substring wins" rule would do.
			name:    "a timeout wrapping a different command does not count",
			command: `timeout --foreground 120s crane digest "${subject}" && cosign attest --yes "${subject}"`,
			program: "cosign attest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			duration, bounded := timeoutFor(test.command, test.program)
			if bounded != test.bounded || duration != test.duration {
				t.Errorf("timeoutFor(%q, %q) = (%q, %t), want (%q, %t)",
					test.command, test.program, duration, bounded, test.duration, test.bounded)
			}
		})
	}
}
