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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
)

// gatherFamily returns the exported metric family by name, or nil if no series
// under that name has been written yet.
func gatherFamily(name string) *dto.MetricFamily {
	families, err := metrics.Registry.Gather()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// seriesFor returns the series in f whose labelKey equals want, or nil.
func seriesFor(f *dto.MetricFamily, labelKey, want string) *dto.Metric {
	if f == nil {
		return nil
	}
	for _, m := range f.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == labelKey && l.GetValue() == want {
				return m
			}
		}
	}
	return nil
}

func labelKeys(m *dto.Metric) []string {
	keys := make([]string, 0, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		keys = append(keys, l.GetName())
	}
	return keys
}

var _ = Describe("metrics dual publishing", func() {

	// Every metric exists twice for the deprecation window: the legacy skyhook_*
	// series and the current nodewright_* one. These specs are the guard that the
	// halves cannot drift, since a metric updated under one name and forgotten
	// under the other would surface only on a user's dashboard.

	It("publishes every registered metric under both prefixes, keyed by its own CR-name label", func() {
		const name = "dual-coverage-spec"

		// Drive every public setter so all registered metrics have at least one series.
		SetSkyhookStatusMetrics(name, v1alpha1.StatusComplete, true)
		SetNodeStatusMetrics(name, v1alpha1.StatusComplete, 1)
		SetNodeTargetCountMetrics(name, 1)
		SetPackageStateMetrics(name, "pkg", "1.0.0", v1alpha1.StateComplete, 1)
		SetPackageStageMetrics(name, "pkg", "1.0.0", v1alpha1.StageApply, 1)
		SetRolloutMetrics(name, "policy", "compartment", "fixed", v1alpha1.CompartmentStatus{})

		for _, m := range allMetrics {
			legacy := seriesFor(gatherFamily(legacyMetricPrefix+m.name), labelSkyhookName, name)
			Expect(legacy).NotTo(BeNil(),
				"legacy series %s%s missing; every metric must publish both names for the deprecation window",
				legacyMetricPrefix, m.name)

			current := seriesFor(gatherFamily(metricPrefix+m.name), labelNodeWrightName, name)
			Expect(current).NotTo(BeNil(),
				"current series %s%s missing", metricPrefix, m.name)

			Expect(labelKeys(legacy)).NotTo(ContainElement(labelNodeWrightName),
				"legacy series %s%s must keep the legacy label key only", legacyMetricPrefix, m.name)
			Expect(labelKeys(current)).NotTo(ContainElement(labelSkyhookName),
				"current series %s%s must not carry the legacy label key", metricPrefix, m.name)

			Expect(current.GetGauge().GetValue()).To(Equal(legacy.GetGauge().GetValue()),
				"both halves of %s must carry the same value", m.name)
		}
	})

	It("registers both halves of every metric", func() {
		for _, m := range allMetrics {
			Expect(m.collectors()).To(HaveLen(2))
			Expect(m.name).NotTo(BeEmpty())
		}
	})

	It("deletes both series together", func() {
		const name = "dual-delete-spec"

		SetNodeStatusMetrics(name, v1alpha1.StatusComplete, 3)
		Expect(seriesFor(gatherFamily(legacyMetricPrefix+"node_status_count"), labelSkyhookName, name)).NotTo(BeNil())
		Expect(seriesFor(gatherFamily(metricPrefix+"node_status_count"), labelNodeWrightName, name)).NotTo(BeNil())

		nodewright_node_status_count.Delete(name, string(v1alpha1.StatusComplete))

		Expect(seriesFor(gatherFamily(legacyMetricPrefix+"node_status_count"), labelSkyhookName, name)).To(BeNil())
		Expect(seriesFor(gatherFamily(metricPrefix+"node_status_count"), labelNodeWrightName, name)).To(BeNil())
	})

	// DisableLegacyMetrics is deliberately one-way in production, so this spec restores
	// the package default itself rather than leaving the rest of the suite with the
	// legacy half switched off.
	It("stops exporting the legacy series when disabled, and keeps the current ones", func() {
		const name = "dual-disable-spec"

		DeferCleanup(func() {
			legacyMetricsEnabled.Store(true)
			for _, m := range allMetrics {
				metrics.Registry.MustRegister(m.legacy)
			}
		})

		DisableLegacyMetrics()

		SetNodeTargetCountMetrics(name, 5)

		Expect(gatherFamily(legacyMetricPrefix+"node_target_count")).To(BeNil(),
			"unregistered legacy collectors must not appear in the exposition at all")

		current := seriesFor(gatherFamily(metricPrefix+"node_target_count"), labelNodeWrightName, name)
		Expect(current).NotTo(BeNil())
		Expect(current.GetGauge().GetValue()).To(Equal(5.0))
	})

	It("points the legacy help text at its replacement and names the removal release", func() {
		const name = "dual-help-spec"
		SetSkyhookStatusMetrics(name, v1alpha1.StatusComplete, true)

		legacy := gatherFamily(legacyMetricPrefix + "status")
		Expect(legacy).NotTo(BeNil())
		Expect(legacy.GetHelp()).To(ContainSubstring(metricPrefix + "status"))
		Expect(legacy.GetHelp()).To(ContainSubstring(labelNodeWrightName))
		Expect(legacy.GetHelp()).To(ContainSubstring(legacyMetricRemovalRelease))

		current := gatherFamily(metricPrefix + "status")
		Expect(current).NotTo(BeNil())
		Expect(current.GetHelp()).NotTo(ContainSubstring("DEPRECATED"))
	})
})
