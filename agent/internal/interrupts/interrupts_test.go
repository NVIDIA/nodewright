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

package interrupts

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestInterrupts(t *testing.T) {
	if runInterruptTestHelper() {
		return
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Interrupts Suite")
}

var _ = Describe("Encode/Decode", func() {
	It("formats the supported interrupt types", func() {
		Expect(supportedTypes()).To(Equal(
			"node_restart, service_restart, restart_all_services, no_op, script_interrupt",
		))
	})

	It("round-trips Encode and Decode for all interrupt types", func() {
		starts := []Interrupt{
			ServiceRestart{Services: []string{"containerd"}},
			NodeRestart{},
			ScriptInterrupt{},
			RestartAllServices{},
			NoOp{},
		}
		for _, start := range starts {
			startSerialized, err := start.Serialize()
			Expect(err).NotTo(HaveOccurred())

			encodedFromSerialized := base64.StdEncoding.EncodeToString(startSerialized)
			encodedFromHelper, err := Encode(start)
			Expect(err).NotTo(HaveOccurred())

			decodedA, err := Decode(encodedFromSerialized)
			Expect(err).NotTo(HaveOccurred())

			decodedB, err := Decode(encodedFromHelper)
			Expect(err).NotTo(HaveOccurred())

			for _, candidate := range []Interrupt{decodedA, decodedB} {
				candidateSerialized, err := candidate.Serialize()
				Expect(err).NotTo(HaveOccurred())
				Expect(string(candidateSerialized)).To(MatchJSON(string(startSerialized)))
				Expect(candidate.Type()).To(Equal(start.Type()))
			}
		}
	})

	It("returns the wire-shape error when input is not base64", func() {
		_, err := Decode("not-base64")
		Expect(err).To(MatchError(errInvalidSerializedInterrupt))
	})

	It("wraps the underlying decode error while staying branchable on the sentinel", func() {
		_, err := Decode("not-base64")
		Expect(errors.Is(err, errInvalidSerializedInterrupt)).To(BeTrue())
		// The original base64 failure is preserved for diagnostics, not dropped.
		Expect(err.Error()).To(ContainSubstring("illegal base64"))
	})

	It("rejects payload without a type field", func() {
		encoded := base64.StdEncoding.EncodeToString([]byte(`{"services":["kubelet"]}`))

		_, err := Decode(encoded)
		Expect(err).To(MatchError(errInvalidSerializedInterrupt))
	})

	It("treats an empty type as unknown", func() {
		encoded := base64.StdEncoding.EncodeToString([]byte(`{"type":""}`))

		_, err := Decode(encoded)
		Expect(err).To(MatchError(
			`unknown interrupt "" must be one of: node_restart, service_restart, restart_all_services, no_op, script_interrupt`,
		))
	})

	It("errors on an unknown type, listing the supported types", func() {
		encoded := base64.StdEncoding.EncodeToString([]byte(`{"type":"made_up"}`))

		_, err := Decode(encoded)
		Expect(err).To(MatchError(
			`unknown interrupt "made_up" must be one of: node_restart, service_restart, restart_all_services, no_op, script_interrupt`,
		))
	})

	It("returns an error rather than panicking on a nil Interrupt", func() {
		_, err := Encode(nil)
		Expect(err).To(MatchError("nil interrupt"))
	})
})

func newInterruptRunConfig() execution.Config {
	config, err := execution.NewConfig(
		execution.WithRootMount("/host"),
		execution.WithStepRoot("/steps"),
		execution.WithSkyhookDir("/package"),
		execution.WithRunOutput(io.Discard, io.Discard),
	)
	Expect(err).NotTo(HaveOccurred())
	return config
}

func runInterruptTestHelper() bool {
	executable := filepath.Base(os.Args[0])
	switch executable {
	case "reboot", "systemctl", "service":
		file, err := os.OpenFile("interrupt-calls", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_, writeErr := fmt.Fprintln(file, strings.Join(append([]string{executable}, os.Args[1:]...), " "))
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
		return true
	default:
		return false
	}
}
