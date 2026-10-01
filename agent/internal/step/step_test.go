/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package step

import (
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nodewright/agent/internal/command"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStep(t *testing.T) {
	if runStepTestHelper() {
		return
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Step Suite")
}

var (
	_ Step = RegularStep{}
	_ Step = UpgradeStep{}
)

var _ = Describe("Step interface", func() {
	It("is satisfied by RegularStep and exposes its path", func() {
		var s Step = RegularStep{ScriptPath: "foo.sh"}
		Expect(s.Path()).To(Equal("foo.sh"))
	})

	It("is satisfied by UpgradeStep and exposes its path", func() {
		var s Step = UpgradeStep{ScriptPath: "foo.sh"}
		Expect(s.Path()).To(Equal("foo.sh"))
	})

	It("exposes the shared command fields on UpgradeStep", func() {
		u := UpgradeStep{
			Name:            "explicit-name",
			ScriptPath:      "foo.sh",
			Arguments:       []string{},
			Returncodes:     []command.ExitCode{command.SuccessExitCode},
			Env:             map[string]string{"K": "V"},
			OnHost:          true,
			IdempotenceMode: Auto,
		}

		Expect(u.Name).To(Equal("explicit-name"))
		Expect(u.ScriptPath).To(Equal("foo.sh"))
		Expect(u.Arguments).To(Equal([]string{}))
		Expect(u.Returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(u.Env).To(Equal(map[string]string{"K": "V"}))
		Expect(u.OnHost).To(BeTrue())
		Expect(u.Idempotence()).To(Equal(Auto))
		Expect(u.RequiresInterrupt).To(BeFalse())
	})

	It("supports type-switching between RegularStep and UpgradeStep", func() {
		steps := []Step{
			RegularStep{ScriptPath: "foo.sh"},
			UpgradeStep{ScriptPath: "upgrade.sh"},
		}

		var sawRegular, sawUpgrade bool
		for _, s := range steps {
			switch s.(type) {
			case RegularStep:
				sawRegular = true
			case UpgradeStep:
				sawUpgrade = true
			}
		}
		Expect(sawRegular).To(BeTrue())
		Expect(sawUpgrade).To(BeTrue())
	})

	It("exposes cloned execution metadata", func() {
		value := NewRegularStep(
			"foo.sh",
			WithArguments([]string{"first"}),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithOnHost(false),
		)

		metadata := value.ExecutionMetadata()

		Expect(metadata).To(Equal(ExecutionMetadata{
			Arguments:   []string{"first"},
			ReturnCodes: []command.ExitCode{0, 2},
			OnHost:      false,
		}))
		metadata.Arguments[0] = "changed"
		metadata.ReturnCodes[0] = 9
		Expect(value.Arguments).To(Equal([]string{"first"}))
		Expect(value.Returncodes).To(Equal([]command.ExitCode{0, 2}))
	})
})

var _ = Describe("legacy compatibility formatting", func() {
	It("formats configured arguments with legacy quoting", func() {
		Expect(FormatLegacyArguments([]string{"plain", "it's", "line\n"})).To(Equal(
			`['plain', "it's", 'line\n']`,
		))
	})

	It("formats configured return codes as a legacy list", func() {
		Expect(FormatLegacyReturnCodes([]command.ExitCode{0, 2})).To(Equal("[0, 2]"))
	})
})

var _ = Describe("Decode", func() {
	It("applies defaults to a direct decode payload", func() {
		// idempotence is omitted so this exercises the missing-field fallback
		// (absent -> Auto), not the legacy boolean form.
		data := []byte(`{"path":"foo.sh","on_host":true,"upgrade_step":false}`)
		s, err := Decode(data)
		Expect(err).NotTo(HaveOccurred())

		rs := s.(RegularStep)
		Expect(rs.Name).To(Equal("foo.sh"))
		Expect(rs.ScriptPath).To(Equal("foo.sh"))
		Expect(rs.Arguments).To(Equal([]string{}))
		Expect(rs.Returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(rs.OnHost).To(BeTrue())
		Expect(rs.Env).To(Equal(map[string]string{}))
		Expect(rs.Idempotence()).To(Equal(Auto))
	})

	It("defaults an omitted on_host to true for both step types", func() {
		for _, data := range [][]byte{
			[]byte(`{"path":"foo.sh","upgrade_step":false}`),
			[]byte(`{"path":"upgrade.sh","upgrade_step":true}`),
		} {
			s, err := Decode(data)
			Expect(err).NotTo(HaveOccurred())
			switch value := s.(type) {
			case RegularStep:
				Expect(value.OnHost).To(BeTrue())
			case UpgradeStep:
				Expect(value.OnHost).To(BeTrue())
			default:
				Fail("unexpected step type")
			}
		}
	})

	It("preserves an explicit on_host false for both step types", func() {
		for _, data := range [][]byte{
			[]byte(`{"path":"foo.sh","on_host":false,"upgrade_step":false}`),
			[]byte(`{"path":"upgrade.sh","on_host":false,"upgrade_step":true}`),
		} {
			s, err := Decode(data)
			Expect(err).NotTo(HaveOccurred())
			switch value := s.(type) {
			case RegularStep:
				Expect(value.OnHost).To(BeFalse())
			case UpgradeStep:
				Expect(value.OnHost).To(BeFalse())
			default:
				Fail("unexpected step type")
			}
		}
	})

	It("decodes the legacy idempotence boolean", func() {
		data := []byte(`{"path":"foo.sh","idempotence":true,"upgrade_step":false}`)
		s, err := Decode(data)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.(RegularStep).Idempotence()).To(Equal(Disabled))
	})

	It("returns a RegularStep when upgrade_step is false", func() {
		data := []byte(`{"path":"foo.sh","idempotence":false,"upgrade_step":false}`)
		s, err := Decode(data)
		Expect(err).NotTo(HaveOccurred())
		_, ok := s.(RegularStep)
		Expect(ok).To(BeTrue(), "expected RegularStep, got %T", s)
	})

	It("returns an UpgradeStep when upgrade_step is true", func() {
		data := []byte(`{"path":"upgrade.sh","idempotence":false,"upgrade_step":true}`)
		s, err := Decode(data)
		Expect(err).NotTo(HaveOccurred())
		_, ok := s.(UpgradeStep)
		Expect(ok).To(BeTrue(), "expected UpgradeStep, got %T", s)
	})

	It("rejects an upgrade_step payload with non-empty arguments", func() {
		data := []byte(`{"name":"u","path":"u.sh","arguments":["x"],"idempotence":false,"upgrade_step":true}`)
		_, err := Decode(data)
		Expect(err).To(MatchError(
			ContainSubstring("UpgradeStep u can not have any arguments, but found: [x]"),
		))
	})
})

var _ = Describe("Encode/Decode round-trip", func() {
	It("round-trips idempotence and preserves RegularStep type", func() {
		start := RegularStep{
			Name:            "foo",
			ScriptPath:      "foo",
			Arguments:       []string{},
			Returncodes:     []command.ExitCode{command.SuccessExitCode},
			OnHost:          true,
			IdempotenceMode: Disabled,
		}

		dumped, err := start.Encode()
		Expect(err).NotTo(HaveOccurred())

		end, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())

		rs, ok := end.(RegularStep)
		Expect(ok).To(BeTrue(), "expected RegularStep, got %T", end)
		Expect(rs.Idempotence()).To(Equal(Disabled))
	})

	It("preserves UpgradeStep type through the round-trip", func() {
		start, err := NewUpgradeStep(
			"upgrade",
			WithName("upgrade"),
			WithReturncodes([]command.ExitCode{command.SuccessExitCode}),
			WithOnHost(true),
			WithIdempotence(Auto),
		)
		Expect(err).NotTo(HaveOccurred())

		dumped, err := start.Encode()
		Expect(err).NotTo(HaveOccurred())

		end, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())

		_, ok := end.(UpgradeStep)
		Expect(ok).To(BeTrue(), "expected UpgradeStep, got %T", end)
	})

	It("round-trips every field through Encode/Decode", func() {
		start := NewRegularStep(
			"foo.sh",
			WithName("custom"),
			WithArguments([]string{"-x"}),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithEnv(map[string]string{"K": "V"}),
			WithOnHost(false),
			WithIdempotence(Disabled),
			WithRequiresInterrupt(true),
		)

		dumped, err := start.Encode()
		Expect(err).NotTo(HaveOccurred())

		round, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())
		Expect(round.(RegularStep)).To(Equal(start))
	})

	It("encodes a zero-value RegularStep by applying defaults", func() {
		bare := RegularStep{ScriptPath: "foo.sh"}
		dumped, err := bare.Encode()
		Expect(err).NotTo(HaveOccurred())

		round, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())
		rs := round.(RegularStep)
		Expect(rs.Name).To(Equal("foo.sh"))
		Expect(rs.Arguments).To(Equal([]string{}))
		Expect(rs.Returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(rs.Idempotence()).To(Equal(Auto))
	})
})

var _ = Describe("Encode env handling", func() {
	It("omits env when empty and includes it when populated", func() {
		noEnv := RegularStep{
			Name:            "a",
			ScriptPath:      "a",
			Arguments:       []string{},
			Returncodes:     []command.ExitCode{command.SuccessExitCode},
			OnHost:          true,
			IdempotenceMode: Auto,
		}
		noEnvJSON, err := noEnv.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(noEnvJSON)).NotTo(ContainSubstring(`"env"`))

		withEnv := noEnv
		withEnv.Env = map[string]string{"hello": "world"}
		withEnvJSON, err := withEnv.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(withEnvJSON)).To(ContainSubstring(`"env":{"hello":"world"}`))
	})
})

var _ = Describe("Direct stdlib JSON", func() {
	It("marshals and unmarshals a RegularStep without the Encode/Decode helpers", func() {
		s := NewRegularStep("foo.sh")
		b, err := json.Marshal(s)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"idempotence":false`))
		Expect(string(b)).To(ContainSubstring(`"on_host":true`))
		Expect(string(b)).NotTo(ContainSubstring("requires_interrupt"))

		var back RegularStep
		Expect(json.Unmarshal(b, &back)).To(Succeed())
		back.applyDefaults()
		Expect(back).To(Equal(s))
	})
})

var _ = Describe("RequiresInterrupt round-trip", func() {
	It("emits requires_interrupt when set and decodes it back", func() {
		s := NewRegularStep("foo.sh", WithRequiresInterrupt(true))
		dumped, err := s.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).To(ContainSubstring(`"requires_interrupt":true`))

		round, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())
		Expect(round.(RegularStep).RequiresInterrupt).To(BeTrue())
	})

	It("omits requires_interrupt when unset and decodes to false", func() {
		s := NewRegularStep("foo.sh")
		dumped, err := s.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).NotTo(ContainSubstring("requires_interrupt"))

		round, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())
		Expect(round.(RegularStep).RequiresInterrupt).To(BeFalse())
	})
})
