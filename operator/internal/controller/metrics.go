/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	// LegacyPolicyName is used when no deployment policy is specified (backward compatibility)
	LegacyPolicyName = "legacy"
)

var (
	// skyhook metrics
	skyhook_status = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_status",
			Help: "Binary metric indicating the status of the Skyhook Custom Resource (1 if in that status, 0 otherwise)",
		},
		[]string{"skyhook_name", "status"},
	)

	// node metrics
	skyhook_node_status_count = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_node_status_count",
			Help: "Number of nodes in the cluster by status for the Skyhook Custom Resource",
		},
		[]string{"skyhook_name", "status"},
	)

	skyhook_node_target_count = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_node_target_count",
			Help: "Total number of nodes targeted by this Skyhook Custom Resource",
		},
		[]string{"skyhook_name"},
	)

	// package metrics
	skyhook_package_state_count = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_package_state_count",
			Help: "Number of nodes in the cluster by state for this package",
		},
		[]string{"skyhook_name", "package_name", "package_version", "state"},
	)

	skyhook_package_stage_count = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_package_stage_count",
			Help: "Number of nodes in the cluster by stage for this package",
		},
		[]string{"skyhook_name", "package_name", "package_version", "stage"},
	)

	skyhook_package_restarts_count = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_package_restarts_count",
			Help: "Number of restarts for this package on this node",
		},
		[]string{"skyhook_name", "package_name", "package_version"},
	)

	// rollout metrics (per-compartment)
	skyhook_rollout_matched_nodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_matched_nodes",
			Help: "Number of nodes matched by this compartment's selector",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_ceiling = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_ceiling",
			Help: "Maximum number of nodes that can be in progress at once in this compartment",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_in_progress = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_in_progress",
			Help: "Number of nodes currently in progress in this compartment",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_completed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_completed",
			Help: "Number of nodes completed in this compartment",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_progress_percent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_progress_percent",
			Help: "Percentage of nodes completed in this compartment (0-100)",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_current_batch = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_current_batch",
			Help: "Current batch number in the rollout strategy (0 if no batch processing)",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_consecutive_failures = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_consecutive_failures",
			Help: "Number of consecutive batch failures in this compartment",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)

	skyhook_rollout_should_stop = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "skyhook_rollout_should_stop",
			Help: "Binary metric indicating if rollout should be stopped due to failures (1 = stopped, 0 = continuing)",
		},
		[]string{"skyhook_name", "policy_name", "compartment_name", "strategy"},
	)
)

func zeroOutSkyhookMetrics(skyhook SkyhookNodes) {
	skyhookName := skyhook.GetSkyhook().Name

	// Clean up node status metrics
	for _, status := range v1alpha1.Statuses {
		skyhook_node_status_count.DeleteLabelValues(skyhookName, string(status))
	}

	// Clean up target count metric
	skyhook_node_target_count.DeleteLabelValues(skyhookName)

	// Clean up skyhook state metrics
	for _, status := range v1alpha1.Statuses {
		skyhook_status.DeleteLabelValues(skyhookName, string(status))
	}

	for _, _package := range skyhook.GetSkyhook().Spec.Packages {
		zeroOutSkyhookPackageMetrics(skyhook.GetSkyhook().Name, _package.Name, _package.Version)
	}

	// Clean up all rollout metrics for this skyhook
	zeroOutSkyhookRolloutMetrics(skyhook)
}

func zeroOutSkyhookPackageMetrics(skyhookName, packageName, packageVersion string) {
	skyhook_package_restarts_count.DeleteLabelValues(skyhookName, packageName, packageVersion)

	for _, state := range v1alpha1.States {
		skyhook_package_state_count.DeleteLabelValues(skyhookName, packageName, packageVersion, string(state))
	}

	for _, stage := range v1alpha1.Stages {
		skyhook_package_stage_count.DeleteLabelValues(skyhookName, packageName, packageVersion, string(stage))
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
	skyhook_node_status_count.WithLabelValues(skyhookName, string(status)).Set(count)
}

func SetSkyhookStatusMetrics(skyhookName string, state v1alpha1.Status, active bool) {
	value := float64(0)
	if active {
		value = 1
	}
	skyhook_status.WithLabelValues(skyhookName, string(state)).Set(value)
}

func SetPackageStateMetrics(skyhookName, packageName, packageVersion string, state v1alpha1.State, count float64) {
	skyhook_package_state_count.WithLabelValues(skyhookName, packageName, packageVersion, string(state)).Set(count)
}

func SetPackageStageMetrics(skyhookName, packageName, packageVersion string, stage v1alpha1.Stage, count float64) {
	skyhook_package_stage_count.WithLabelValues(skyhookName, packageName, packageVersion, string(stage)).Set(count)
}

func SetPackageRestartsMetrics(skyhookName, packageName, packageVersion string, restarts int32) {
	skyhook_package_restarts_count.WithLabelValues(skyhookName, packageName, packageVersion).Set(float64(restarts))
}

func SetNodeTargetCountMetrics(skyhookName string, count float64) {
	skyhook_node_target_count.WithLabelValues(skyhookName).Set(count)
}

// zeroOutRolloutMetricsForCompartment removes rollout metrics for a specific compartment
func zeroOutRolloutMetricsForCompartment(skyhookName, policyName, compartmentName, strategy string) {
	skyhook_rollout_matched_nodes.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_ceiling.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_in_progress.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_completed.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_progress_percent.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_current_batch.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_consecutive_failures.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
	skyhook_rollout_should_stop.DeleteLabelValues(skyhookName, policyName, compartmentName, strategy)
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
	skyhook_rollout_matched_nodes.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.Matched))
	skyhook_rollout_ceiling.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.Ceiling))
	skyhook_rollout_in_progress.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.InProgress))
	skyhook_rollout_completed.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.Completed))
	skyhook_rollout_progress_percent.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.ProgressPercent))

	// Set batch state metrics if present
	if status.BatchState != nil {
		skyhook_rollout_current_batch.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.BatchState.CurrentBatch))
		skyhook_rollout_consecutive_failures.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(float64(status.BatchState.ConsecutiveFailures))

		shouldStop := float64(0)
		if status.BatchState.ShouldStop {
			shouldStop = 1
		}
		skyhook_rollout_should_stop.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(shouldStop)
	} else {
		// Set to 0 if no batch state
		skyhook_rollout_current_batch.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(0)
		skyhook_rollout_consecutive_failures.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(0)
		skyhook_rollout_should_stop.WithLabelValues(skyhookName, policyName, compartmentName, strategy).Set(0)
	}
}

func init() {
	metrics.Registry.MustRegister(
		skyhook_status,
		skyhook_node_status_count,
		skyhook_node_target_count,
		skyhook_package_state_count,
		skyhook_package_stage_count,
		skyhook_package_restarts_count,
		skyhook_rollout_matched_nodes,
		skyhook_rollout_ceiling,
		skyhook_rollout_in_progress,
		skyhook_rollout_completed,
		skyhook_rollout_progress_percent,
		skyhook_rollout_current_batch,
		skyhook_rollout_consecutive_failures,
		skyhook_rollout_should_stop,
	)
}
