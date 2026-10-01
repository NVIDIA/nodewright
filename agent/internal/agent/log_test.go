// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
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

package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nodewright/agent/internal/config"
	"github.com/NVIDIA/nodewright/agent/internal/flags"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("operation logging", func() {
	var (
		root   string
		layout flags.Layout
		cfg    config.Config
	)

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		layout = flags.DefaultLayout(root)
		cfg = config.Config{PackageName: "package", PackageVersion: "1.2.3"}
	})

	It("uses the configured streams without creating a retained log when disabled", func() {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		runtime := runtimeConfig{stdout: stdout, stderr: stderr}

		operationLog, err := prepareOperationLog(
			runtime,
			layout,
			cfg,
			"apply.sh",
			`step "apply.sh"`,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(operationLog.stdout).To(BeIdenticalTo(stdout))
		Expect(operationLog.stderr).To(BeIdenticalTo(stderr))
		_, err = io.WriteString(operationLog.stdout, "stdout")
		Expect(err).NotTo(HaveOccurred())
		_, err = io.WriteString(operationLog.stderr, "stderr")
		Expect(err).NotTo(HaveOccurred())

		Expect(operationLog.finalize()).To(Succeed())
		Expect(stdout.String()).To(Equal("stdout"))
		Expect(stderr.String()).To(Equal("stderr"))
		Expect(layout.LogDir()).NotTo(BeAnExistingFile())
	})

	It("streams output while retaining it in one operation log", func() {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		runtime := runtimeConfig{
			writeLogs: true,
			stdout:    stdout,
			stderr:    stderr,
		}

		operationLog, err := prepareOperationLog(
			runtime,
			layout,
			cfg,
			filepath.Join("steps", "apply.sh"),
			`step "steps/apply.sh"`,
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = io.WriteString(operationLog.stdout, "stdout\n")
		Expect(err).NotTo(HaveOccurred())
		_, err = io.WriteString(operationLog.stderr, "stderr\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(operationLog.finalize()).To(Succeed())

		Expect(stdout.String()).To(Equal("stdout\n"))
		Expect(stderr.String()).To(Equal("stderr\n"))
		logDirectory := filepath.Join(
			layout.LogDir(),
			cfg.PackageName,
			cfg.PackageVersion,
			"steps",
		)
		entries, err := os.ReadDir(logDirectory)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		contents, err := os.ReadFile(filepath.Join(logDirectory, entries[0].Name()))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(contents)).To(Equal("stdout\nstderr\n"))
	})

	It("retains only the configured number of finalized logs", func() {
		runtime := runtimeConfig{
			writeLogs: true,
			stdout:    io.Discard,
			stderr:    io.Discard,
		}

		for range flags.DefaultLogRetention + 2 {
			operationLog, err := prepareOperationLog(
				runtime,
				layout,
				cfg,
				"apply.sh",
				`step "apply.sh"`,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(operationLog.finalize()).To(Succeed())
		}

		entries, err := os.ReadDir(filepath.Join(
			layout.LogDir(),
			cfg.PackageName,
			cfg.PackageVersion,
		))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(flags.DefaultLogRetention))
	})
})
