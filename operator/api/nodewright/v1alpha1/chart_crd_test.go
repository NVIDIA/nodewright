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

package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

// The nodewright.nvidia.com group's CRDs. The legacy skyhook.nvidia.com ones are left out: they are
// a migration shim the chart drops with the legacy group.
var (
	configCRDFiles = filepath.Join("..", "..", "..", "config", "crd", "bases", "nodewright.nvidia.com_*.yaml")
	chartCRDFiles  = filepath.Join("..", "..", "..", "..", "chart", "templates", "nodewright-*crd.yaml")
)

// Envtest and every e2e lane install the CRDs from config/crd/bases, while a Helm install takes the
// chart's hand-mirrored copies, so only this catches a field the chart's schema lacks, which the
// apiserver would prune from every write of a chart-installed operator.
var _ = Describe("chart CRDs", func() {
	It("serve the versions and schemas of the generated CRDs", func() {
		generated := crdVersions(configCRDFiles)
		shipped := crdVersions(chartCRDFiles)

		Expect(generated).NotTo(BeEmpty())
		Expect(shipped).To(HaveLen(len(generated)))
		for name, versions := range generated {
			Expect(shipped).To(HaveKey(name))
			Expect(shipped[name]).To(Equal(versions), "the chart's %s CRD differs from config/crd/bases", name)
		}
	})
})

// crdVersions returns spec.versions of each CRD the files matching pattern hold, as YAML with
// sorted keys, by CRD name. The chart's templating lines are dropped before parsing: in the CRD
// templates they only add labels and the conversion webhook's service, never spec.versions.
func crdVersions(pattern string) map[string]string {
	GinkgoHelper()
	paths, err := filepath.Glob(pattern)
	Expect(err).NotTo(HaveOccurred())
	versions := map[string]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var lines []string
		for line := range strings.SplitSeq(string(data), "\n") {
			if !strings.Contains(line, "{{") {
				lines = append(lines, line)
			}
		}
		var crd struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Versions []any `json:"versions"`
			} `json:"spec"`
		}
		Expect(yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &crd)).To(Succeed(), path)
		Expect(crd.Metadata.Name).NotTo(BeEmpty(), path)
		Expect(crd.Spec.Versions).NotTo(BeEmpty(), path)
		out, err := yaml.Marshal(crd.Spec.Versions)
		Expect(err).NotTo(HaveOccurred())
		versions[crd.Metadata.Name] = string(out)
	}
	return versions
}
