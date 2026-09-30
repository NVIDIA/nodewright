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

package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/nodewright/agent/internal/schema"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

func TestConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Config Suite")
}

const validConfigJSON = `{
  "schema_version": "v1",
  "root_dir": "/",
  "expected_config_files": [],
  "package_name": "test-package",
  "package_version": "1.0.0",
  "modes": {
    "apply": [
      {"name": "apply", "path": "apply.sh", "arguments": [], "returncodes": [0], "on_host": true, "idempotence": false, "upgrade_step": false}
    ],
    "apply-check": [
      {"name": "apply-check", "path": "apply_check.sh", "arguments": [], "returncodes": [0], "on_host": true, "idempotence": false, "upgrade_step": false}
    ]
  }
}`

func writeStepFiles(dir string, names ...string) {
	for _, n := range names {
		Expect(os.WriteFile(filepath.Join(dir, n), []byte("fixture"), 0o755)).To(Succeed())
	}
}

var _ = Describe("schema compilation", func() {
	It("compiles the embedded v1 schema", func() {
		_, err := compileSchema(schema.V1)
		Expect(err).NotTo(HaveOccurred())
	})

	It("reports an unknown version", func() {
		_, err := compileSchema(schema.SchemaVersion("v99"))
		Expect(err).To(MatchError(ContainSubstring("unknown schema version")))
	})
})

var _ = Describe("Load", func() {
	var (
		tmp    string
		logger *slog.Logger
		loader *Loader
	)

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		logger = slog.New(slog.NewTextHandler(GinkgoWriter, nil))
		loader = NewLoader()
	})

	It("loads a valid config", func() {
		writeStepFiles(tmp, "apply.sh", "apply_check.sh")
		cfg, err := loader.Load([]byte(validConfigJSON), tmp, logger)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.SchemaVersion).To(Equal(schema.V1))
		Expect(cfg.PackageName).To(Equal("test-package"))
		Expect(cfg.PackageVersion).To(Equal("1.0.0"))
		Expect(cfg.Modes).To(HaveKey(stage.Apply))
		Expect(cfg.Modes).To(HaveKey(stage.ApplyCheck))
		Expect(cfg.Modes[stage.Apply]).To(HaveLen(1))
	})

	It("rejects a config missing a required field", func() {
		data := `{"schema_version": "v1", "root_dir": "/", "expected_config_files": [], "package_version": "1.0.0", "modes": {}}`
		_, err := loader.Load([]byte(data), tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("schema validation")))
	})

	It("rejects a step missing required on_host", func() {
		data := `{
  "schema_version": "v1", "root_dir": "/", "expected_config_files": [],
  "package_name": "p", "package_version": "1.0.0",
  "modes": {"apply": [{"name": "apply", "path": "apply.sh", "arguments": [], "returncodes": [0], "idempotence": false, "upgrade_step": false}]}
}`
		_, err := loader.Load([]byte(data), tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("schema validation")))
	})

	It("rejects an unknown schema version", func() {
		data := `{"schema_version": "v99", "root_dir": "/", "expected_config_files": [], "package_name": "p", "package_version": "1.0.0", "modes": {}}`
		_, err := loader.Load([]byte(data), tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("unknown schema version")))
	})

	It("rejects a non-semver package_version", func() {
		writeStepFiles(tmp, "apply.sh", "apply_check.sh")
		data := `{
  "schema_version": "v1", "root_dir": "/", "expected_config_files": [],
  "package_name": "p", "package_version": "1.0",
  "modes": {"apply": [{"name": "apply", "path": "apply.sh", "arguments": [], "returncodes": [0], "on_host": true, "idempotence": false, "upgrade_step": false}]}
}`
		_, err := loader.Load([]byte(data), tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("schema validation")))
	})

	It("surfaces a missing schema_version distinctly", func() {
		_, err := loader.Load([]byte(`{"root_dir": "/"}`), tmp, logger)
		Expect(err).To(MatchError(ContainSubstring("missing required field schema_version")))
	})
})

var _ = Describe("Dump", func() {
	var (
		tmp    string
		logger *slog.Logger
		loader *Loader
	)

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		logger = slog.New(slog.NewTextHandler(GinkgoWriter, nil))
		loader = NewLoader()
	})

	It("produces output Load accepts (round-trip)", func() {
		writeStepFiles(tmp, "apply.sh", "apply_check.sh")
		modes := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("apply.sh")},
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}

		data, err := loader.Dump("pkg", "1.2.3", "/", modes, nil)
		Expect(err).NotTo(HaveOccurred())

		cfg, err := loader.Load(data, tmp, logger)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.PackageName).To(Equal("pkg"))
		Expect(cfg.PackageVersion).To(Equal("1.2.3"))
		Expect(cfg.SchemaVersion).To(Equal(schema.V1))
		Expect(cfg.Modes[stage.Apply]).To(HaveLen(1))
	})

	It("always emits the latest schema version", func() {
		modes := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("apply.sh")},
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}
		data, err := loader.Dump("pkg", "1.0.0", "/", modes, nil)
		Expect(err).NotTo(HaveOccurred())
		latest, err := schema.Highest()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"schema_version":"` + string(latest) + `"`))
	})

	It("rejects a non-semver package version up front", func() {
		modes := map[stage.Stage][]step.Step{
			stage.Apply:      {step.NewRegularStep("apply.sh")},
			stage.ApplyCheck: {step.NewRegularStep("apply_check.sh")},
		}
		_, err := loader.Dump("pkg", "1.0", "/", modes, nil)
		Expect(err).To(MatchError(ContainSubstring("invalid config")))
	})

	It("rejects modes that would fail Load's cross-step rules", func() {
		// An apply stage with no apply-check would serialize fine but be
		// rejected on reload; Dump must catch it up front.
		modes := map[stage.Stage][]step.Step{
			stage.Apply: {step.NewRegularStep("apply.sh")},
		}
		_, err := loader.Dump("pkg", "1.0.0", "/", modes, nil)
		Expect(err).To(MatchError(ContainSubstring("no corresponding")))
	})

	It("rejects a bogus stage key up front", func() {
		modes := map[stage.Stage][]step.Step{stage.Stage("bogus"): {step.NewRegularStep("x.sh")}}
		_, err := loader.Dump("pkg", "1.0.0", "/", modes, nil)
		Expect(err).To(MatchError(ContainSubstring("unknown stage")))
	})
})
