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

package controller

import (
	"fmt"
	"sync/atomic"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	// LegacyPolicyName is used when no deployment policy is specified (backward compatibility)
	LegacyPolicyName = "legacy"

	// metricPrefix is the current metric-name prefix; legacyMetricPrefix is the
	// pre-rename one, published alongside it for the deprecation window.
	metricPrefix       = "nodewright_"
	legacyMetricPrefix = "skyhook_"

	// legacyMetricRemovalRelease is the release that drops the skyhook_* set. It is
	// deliberately the same release that removes the legacy skyhook.nvidia.com API
	// group (see docs/getting-started/migration.md) so users have one deadline, not two.
	legacyMetricRemovalRelease = "v0.20.0"

	// Prometheus label keys shared across multiple metric vectors.
	labelNodeWrightName  = "nodewright_name"
	labelSkyhookName     = "skyhook_name"
	labelPackageName     = "package_name"
	labelPackageVersion  = "package_version"
	labelPolicyName      = "policy_name"
	labelCompartmentName = "compartment_name"
	labelStrategy        = "strategy"
)

// dualGaugeVec publishes one logical gauge under two series: the legacy
// skyhook_<name> keyed by a skyhook_name label, and the current
// nodewright_<name> keyed by nodewright_name. Every other label is identical.
//
// WHY: metric names and label keys are the identifiers users bake into Grafana
// dashboards, Prometheus alerting rules, and recording rules, so the
// Skyhook -> NodeWright rename cannot swap them in place without breaking every
// consumer the moment they upgrade. Both series are written on every update for
// the deprecation window, then the legacy half is deleted along with the legacy
// API group.
//
// WHY fan out here rather than at the call sites: there are ~40 Set/Delete sites
// across this file, and duplicating each one makes it possible to update a metric
// under one name and silently forget the other, which is exactly the class of bug
// a dashboard would surface only after the window closed.
type dualGaugeVec struct {
	// name is the unprefixed base name, e.g. "node_status_count". The two exported
	// series are legacyMetricPrefix+name and metricPrefix+name.
	name    string
	legacy  *prometheus.GaugeVec
	current *prometheus.GaugeVec
}

// newDualGaugeVec builds both halves from an unprefixed base name (e.g.
// "node_status_count"). extraLabels follow the CR-name label, which is always first
// and is the only label whose key differs between the two halves.
func newDualGaugeVec(name, help string, extraLabels ...string) *dualGaugeVec {
	return &dualGaugeVec{
		name: name,
		legacy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: legacyMetricPrefix + name,
				Help: fmt.Sprintf("%s (DEPRECATED: use %s%s with the %s label; removed in %s)",
					help, metricPrefix, name, labelNodeWrightName, legacyMetricRemovalRelease),
			},
			append([]string{labelSkyhookName}, extraLabels...),
		),
		current: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: metricPrefix + name,
				Help: help,
			},
			append([]string{labelNodeWrightName}, extraLabels...),
		),
	}
}

// legacyMetricsEnabled gates the deprecated half. Atomic rather than a plain bool
// because DisableLegacyMetrics is called from main during startup while the specs
// that exercise it run alongside other suites; the race detector flags the plain
// read/write pair even though the production ordering is safe.
var legacyMetricsEnabled atomic.Bool

// DisableLegacyMetrics stops the deprecated skyhook_* series being exported, for
// operators that would rather halve their series count than keep the compatibility
// window. Call once at startup, before the manager starts, from the
// PublishLegacyMetrics option. It is one-way: the window is a rollout concern, not
// something to toggle at runtime.
func DisableLegacyMetrics() {
	legacyMetricsEnabled.Store(false)
	for _, m := range allMetrics {
		metrics.Registry.Unregister(m.legacy)
	}
}

func (d *dualGaugeVec) Set(value float64, labelValues ...string) {
	d.current.WithLabelValues(labelValues...).Set(value)
	if legacyMetricsEnabled.Load() {
		d.legacy.WithLabelValues(labelValues...).Set(value)
	}
}

// Delete drops both halves unconditionally: an unregistered legacy vector is not
// exported, but leaving series in it would resurrect them if it were ever
// re-registered, and deleting from an empty vector is a no-op.
func (d *dualGaugeVec) Delete(labelValues ...string) {
	d.legacy.DeleteLabelValues(labelValues...)
	d.current.DeleteLabelValues(labelValues...)
}

func (d *dualGaugeVec) collectors() []prometheus.Collector {
	return []prometheus.Collector{d.legacy, d.current}
}

var (
	// nodewright metrics
	nodewright_status = newDualGaugeVec(
		"status",
		"Binary metric indicating the status of the NodeWright Custom Resource (1 if in that status, 0 otherwise)",
		"status",
	)

	// node metrics
	nodewright_node_status_count = newDualGaugeVec(
		"node_status_count",
		"Number of nodes in the cluster by status for the NodeWright Custom Resource",
		"status",
	)

	nodewright_node_target_count = newDualGaugeVec(
		"node_target_count",
		"Total number of nodes targeted by this NodeWright Custom Resource",
	)

	// package metrics
	nodewright_package_state_count = newDualGaugeVec(
		"package_state_count",
		"Number of nodes in the cluster by state for this package",
		labelPackageName, labelPackageVersion, "state",
	)

	nodewright_package_stage_count = newDualGaugeVec(
		"package_stage_count",
		"Number of nodes in the cluster by stage for this package",
		labelPackageName, labelPackageVersion, "stage",
	)

	// rollout metrics (per-compartment)
	nodewright_rollout_matched_nodes = newDualGaugeVec(
		"rollout_matched_nodes",
		"Number of nodes matched by this compartment's selector",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_ceiling = newDualGaugeVec(
		"rollout_ceiling",
		"Maximum number of nodes that can be in progress at once in this compartment",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_in_progress = newDualGaugeVec(
		"rollout_in_progress",
		"Number of nodes currently in progress in this compartment",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_completed = newDualGaugeVec(
		"rollout_completed",
		"Number of nodes completed in this compartment",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_progress_percent = newDualGaugeVec(
		"rollout_progress_percent",
		"Percentage of nodes completed in this compartment (0-100)",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_current_batch = newDualGaugeVec(
		"rollout_current_batch",
		"Current batch number in the rollout strategy (0 if no batch processing)",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_consecutive_failures = newDualGaugeVec(
		"rollout_consecutive_failures",
		"Number of consecutive batch failures in this compartment",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	nodewright_rollout_should_stop = newDualGaugeVec(
		"rollout_should_stop",
		"Binary metric indicating if rollout should be stopped due to failures (1 = stopped, 0 = continuing)",
		labelPolicyName, labelCompartmentName, labelStrategy,
	)

	// allMetrics is the registration list; every dualGaugeVec above must appear here.
	allMetrics = []*dualGaugeVec{
		nodewright_status,
		nodewright_node_status_count,
		nodewright_node_target_count,
		nodewright_package_state_count,
		nodewright_package_stage_count,
		nodewright_rollout_matched_nodes,
		nodewright_rollout_ceiling,
		nodewright_rollout_in_progress,
		nodewright_rollout_completed,
		nodewright_rollout_progress_percent,
		nodewright_rollout_current_batch,
		nodewright_rollout_consecutive_failures,
		nodewright_rollout_should_stop,
	}
)

func zeroOutSkyhookMetrics(skyhook SkyhookNodes) {
	skyhookName := skyhook.GetSkyhook().Name

	// Clean up node status metrics
	for _, status := range v1alpha1.Statuses {
		nodewright_node_status_count.Delete(skyhookName, string(status))
	}

	// Clean up target count metric
	nodewright_node_target_count.Delete(skyhookName)

	// Clean up skyhook state metrics
	for _, status := range v1alpha1.Statuses {
		nodewright_status.Delete(skyhookName, string(status))
	}

	for _, _package := range skyhook.GetSkyhook().Spec.Packages {
		zeroOutSkyhookPackageMetrics(skyhook.GetSkyhook().Name, _package.Name, _package.Version)
	}

	// Clean up all rollout metrics for this skyhook
	zeroOutSkyhookRolloutMetrics(skyhook)
}

func zeroOutSkyhookPackageMetrics(skyhookName, packageName, packageVersion string) {
	for _, state := range v1alpha1.States {
		nodewright_package_state_count.Delete(skyhookName, packageName, packageVersion, string(state))
	}

	for _, stage := range v1alpha1.Stages {
		nodewright_package_stage_count.Delete(skyhookName, packageName, packageVersion, string(stage))
	}
}

func ResetSkyhookMetricsToZero(skyhook SkyhookNodes) {
	skyhookName := skyhook.GetSkyhook().Name

	for _, status := range v1alpha1.Statuses {
		SetNodeStatusMetrics(skyhookName, status, 0)
	}

	for _, status := range v1alpha1.Statuses {
		SetSkyhookStatusMetrics(skyhookName, status, false)
	}

	for _, pkg := range skyhook.GetSkyhook().Spec.Packages {
		for _, state := range v1alpha1.States {
			SetPackageStateMetrics(skyhookName, pkg.Name, pkg.Version, state, 0)
		}
		for _, stage := range v1alpha1.Stages {
			SetPackageStageMetrics(skyhookName, pkg.Name, pkg.Version, stage, 0)
		}
	}

	// Reset rollout metrics to zero
	ResetRolloutMetricsToZero(skyhook)
}

func SetNodeStatusMetrics(skyhookName string, status v1alpha1.Status, count float64) {
	nodewright_node_status_count.Set(count, skyhookName, string(status))
}

func SetSkyhookStatusMetrics(skyhookName string, state v1alpha1.Status, active bool) {
	value := float64(0)
	if active {
		value = 1
	}
	nodewright_status.Set(value, skyhookName, string(state))
}

func SetPackageStateMetrics(skyhookName, packageName, packageVersion string, state v1alpha1.State, count float64) {
	nodewright_package_state_count.Set(count, skyhookName, packageName, packageVersion, string(state))
}

func SetPackageStageMetrics(skyhookName, packageName, packageVersion string, stage v1alpha1.Stage, count float64) {
	nodewright_package_stage_count.Set(count, skyhookName, packageName, packageVersion, string(stage))
}

func SetNodeTargetCountMetrics(skyhookName string, count float64) {
	nodewright_node_target_count.Set(count, skyhookName)
}

// zeroOutRolloutMetricsForCompartment removes rollout metrics for a specific compartment
func zeroOutRolloutMetricsForCompartment(skyhookName, policyName, compartmentName, strategy string) {
	nodewright_rollout_matched_nodes.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_ceiling.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_in_progress.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_completed.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_progress_percent.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_current_batch.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_consecutive_failures.Delete(skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_should_stop.Delete(skyhookName, policyName, compartmentName, strategy)
}

// zeroOutSkyhookRolloutMetrics removes all rollout metrics for a skyhook
// This is called when a Skyhook is deleted
func zeroOutSkyhookRolloutMetrics(skyhook SkyhookNodes) {
	// Get the policy name from the skyhook spec
	policyName := skyhook.GetSkyhook().Spec.DeploymentPolicy
	if policyName == "" {
		policyName = LegacyPolicyName
	}

	// Clean up metrics for all compartments
	for compartmentName, compartment := range skyhook.GetCompartments() {
		strategy := getStrategyType(compartment)
		zeroOutRolloutMetricsForCompartment(skyhook.GetSkyhook().Name, policyName, compartmentName, strategy)
	}

	// Also clean up metrics from CompartmentStatuses in case compartments were removed
	if skyhook.GetSkyhook().Status.CompartmentStatuses != nil {
		for compartmentName := range skyhook.GetSkyhook().Status.CompartmentStatuses {
			// We don't have the exact strategy here, so we'll need to try to delete with all possible strategy types
			for _, strategyType := range []string{"fixed", "linear", "exponential", "unknown"} {
				zeroOutRolloutMetricsForCompartment(skyhook.GetSkyhook().Name, policyName, compartmentName, strategyType)
			}
		}
	}
}

// getStrategyType returns the strategy type name for a compartment
func getStrategyType(compartment *wrapper.Compartment) string {
	strategyType := wrapper.GetStrategyType(compartment.Strategy)
	return string(strategyType)
}

// ResetRolloutMetricsToZero resets rollout metrics to zero for all compartments in the skyhook
// This follows the same pattern as ResetSkyhookMetricsToZero for consistency
func ResetRolloutMetricsToZero(skyhook SkyhookNodes) {
	policyName := skyhook.GetSkyhook().Spec.DeploymentPolicy
	if policyName == "" {
		policyName = LegacyPolicyName
	}

	// Reset metrics for all current compartments
	for compartmentName, compartment := range skyhook.GetCompartments() {
		strategy := getStrategyType(compartment)
		emptyStatus := v1alpha1.CompartmentStatus{
			Matched:         0,
			Ceiling:         0,
			InProgress:      0,
			Completed:       0,
			ProgressPercent: 0,
			BatchState:      nil,
		}
		SetRolloutMetrics(skyhook.GetSkyhook().Name, policyName, compartmentName, strategy, emptyStatus)
	}
}

// SetRolloutMetrics sets the rollout metrics for a specific compartment
func SetRolloutMetrics(skyhookName, policyName, compartmentName, strategy string, status v1alpha1.CompartmentStatus) {
	nodewright_rollout_matched_nodes.Set(float64(status.Matched), skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_ceiling.Set(float64(status.Ceiling), skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_in_progress.Set(float64(status.InProgress), skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_completed.Set(float64(status.Completed), skyhookName, policyName, compartmentName, strategy)
	nodewright_rollout_progress_percent.Set(float64(status.ProgressPercent), skyhookName, policyName, compartmentName, strategy)

	// Set batch state metrics if present
	if status.BatchState != nil {
		nodewright_rollout_current_batch.Set(float64(status.BatchState.CurrentBatch), skyhookName, policyName, compartmentName, strategy)
		nodewright_rollout_consecutive_failures.Set(float64(status.BatchState.ConsecutiveFailures), skyhookName, policyName, compartmentName, strategy)

		shouldStop := float64(0)
		if status.BatchState.ShouldStop {
			shouldStop = 1
		}
		nodewright_rollout_should_stop.Set(shouldStop, skyhookName, policyName, compartmentName, strategy)
	} else {
		// Set to 0 if no batch state
		nodewright_rollout_current_batch.Set(0, skyhookName, policyName, compartmentName, strategy)
		nodewright_rollout_consecutive_failures.Set(0, skyhookName, policyName, compartmentName, strategy)
		nodewright_rollout_should_stop.Set(0, skyhookName, policyName, compartmentName, strategy)
	}
}

func init() {
	// Default on: an operator that upgrades without setting PublishLegacyMetrics keeps
	// exporting the deprecated series, so existing dashboards survive the rename.
	legacyMetricsEnabled.Store(true)

	for _, m := range allMetrics {
		metrics.Registry.MustRegister(m.collectors()...)
	}
}
