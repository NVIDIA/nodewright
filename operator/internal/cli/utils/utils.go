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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"golang.org/x/mod/semver"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/skyhook/operator/api/v1alpha1"
)

// Output format constants
const (
	OutputFormatTable = "table"
	OutputFormatJSON  = "json"
	OutputFormatYAML  = "yaml"
	OutputFormatWide  = "wide"
)

// MatchNodes matches node patterns against a list of available nodes.
// Patterns can be exact node names or regex patterns.
func MatchNodes(patterns []string, availableNodes []string) ([]string, error) {
	matched := make(map[string]bool)

	for _, pattern := range patterns {
		// Check if it's a regex pattern (contains regex metacharacters)
		isRegex := strings.ContainsAny(pattern, ".*+?^${}[]|()\\")

		if isRegex {
			re, err := regexp.Compile("^" + pattern + "$")
			if err != nil {
				return nil, fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
			}

			for _, node := range availableNodes {
				if re.MatchString(node) {
					matched[node] = true
				}
			}
		} else {
			// Exact match
			for _, node := range availableNodes {
				if node == pattern {
					matched[node] = true
				}
			}
		}
	}

	result := make([]string, 0, len(matched))
	for node := range matched {
		result = append(result, node)
	}

	return result, nil
}

// UnstructuredToSkyhook converts an unstructured object to a Skyhook.
func UnstructuredToSkyhook(u *unstructured.Unstructured) (*v1alpha1.Skyhook, error) {
	data, err := u.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshaling unstructured: %w", err)
	}

	var skyhook v1alpha1.Skyhook
	if err := json.Unmarshal(data, &skyhook); err != nil {
		return nil, fmt.Errorf("unmarshaling to skyhook: %w", err)
	}

	return &skyhook, nil
}

// Skyhook annotation and label keys
const (
	PauseAnnotation   = v1alpha1.METADATA_PREFIX + "/pause"
	DisableAnnotation = v1alpha1.METADATA_PREFIX + "/disable"
	NodeIgnoreLabel   = v1alpha1.METADATA_PREFIX + "/ignore"
	VersionAnnotation = v1alpha1.METADATA_PREFIX + "/version"
)

// SetSkyhookAnnotation sets an annotation on a Skyhook CR using dynamic client
// Note: Skyhook is a cluster-scoped resource (not namespaced)
func SetSkyhookAnnotation(ctx context.Context, dynamicClient dynamic.Interface, skyhookName, annotation, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annotation, value)

	gvr := v1alpha1.GroupVersion.WithResource("skyhooks")
	_, err := dynamicClient.Resource(gvr).Patch(
		ctx,
		skyhookName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching skyhook %q: %w", skyhookName, err)
	}

	return nil
}

// RemoveSkyhookAnnotation removes an annotation from a Skyhook CR using dynamic client
// Note: Skyhook is a cluster-scoped resource (not namespaced)
func RemoveSkyhookAnnotation(ctx context.Context, dynamicClient dynamic.Interface, skyhookName, annotation string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, annotation)

	gvr := v1alpha1.GroupVersion.WithResource("skyhooks")
	_, err := dynamicClient.Resource(gvr).Patch(
		ctx,
		skyhookName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching skyhook %q: %w", skyhookName, err)
	}

	return nil
}

// SetNodeAnnotation sets an annotation on a Node using merge patch
func SetNodeAnnotation(ctx context.Context, kubeClient kubernetes.Interface, nodeName, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, key, value)

	_, err := kubeClient.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching node %q: %w", nodeName, err)
	}

	return nil
}

// RemoveNodeAnnotation removes an annotation from a Node using merge patch
func RemoveNodeAnnotation(ctx context.Context, kubeClient kubernetes.Interface, nodeName, key string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, key)

	_, err := kubeClient.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching node %q: %w", nodeName, err)
	}

	return nil
}

// SetNodeLabel sets a label on a Node using merge patch
func SetNodeLabel(ctx context.Context, kubeClient kubernetes.Interface, nodeName, key, value string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)

	_, err := kubeClient.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching node %q: %w", nodeName, err)
	}

	return nil
}

// RemoveNodeLabel removes a label from a Node using merge patch
func RemoveNodeLabel(ctx context.Context, kubeClient kubernetes.Interface, nodeName, key string) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, key)

	_, err := kubeClient.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching node %q: %w", nodeName, err)
	}

	return nil
}

// OutputJSON writes data as indented JSON to the writer
func OutputJSON(out io.Writer, data any) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling json: %w", err)
	}
	_, _ = fmt.Fprintln(out, string(jsonData))
	return nil
}

// OutputYAML writes data as YAML to the writer
func OutputYAML(out io.Writer, data any) error {
	yamlData, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling yaml: %w", err)
	}
	_, _ = fmt.Fprint(out, string(yamlData))
	return nil
}

// TableConfig defines the column configuration for table/wide output
// T is the type of items being displayed
type TableConfig[T any] struct {
	// Headers for table mode (always shown)
	Headers []string
	// Extract returns column values for table mode
	Extract func(T) []string
	// WideHeaders are additional headers appended in wide mode (optional)
	WideHeaders []string
	// WideExtract returns additional column values for wide mode (optional)
	WideExtract func(T) []string
}

// OutputTable writes items as a table using the provided config
func OutputTable[T any](out io.Writer, cfg TableConfig[T], items []T) error {
	return outputTableInternal(out, cfg, items, false)
}

// OutputWide writes items as a wide table (table columns + wide columns)
func OutputWide[T any](out io.Writer, cfg TableConfig[T], items []T) error {
	return outputTableInternal(out, cfg, items, true)
}

// OutputTableWithHeader writes items as a table with a header line above
func OutputTableWithHeader[T any](out io.Writer, headerLine string, cfg TableConfig[T], items []T) error {
	_, _ = fmt.Fprintln(out, headerLine)
	_, _ = fmt.Fprintln(out)
	return outputTableInternal(out, cfg, items, false)
}

// OutputWideWithHeader writes items as a wide table with a header line above
func OutputWideWithHeader[T any](out io.Writer, headerLine string, cfg TableConfig[T], items []T) error {
	_, _ = fmt.Fprintln(out, headerLine)
	_, _ = fmt.Fprintln(out)
	return outputTableInternal(out, cfg, items, true)
}

func outputTableInternal[T any](out io.Writer, cfg TableConfig[T], items []T, wide bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	// Build headers
	headers := cfg.Headers
	if wide && len(cfg.WideHeaders) > 0 {
		headers = append(headers, cfg.WideHeaders...)
	}

	// Write headers
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))

	// Write separator
	seps := make([]string, len(headers))
	for i, h := range headers {
		seps[i] = strings.Repeat("-", len(h))
	}
	_, _ = fmt.Fprintln(tw, strings.Join(seps, "\t"))

	// Write rows
	for _, item := range items {
		row := cfg.Extract(item)
		if wide && cfg.WideExtract != nil {
			row = append(row, cfg.WideExtract(item)...)
		}
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	}

	return tw.Flush()
}

// Operator version discovery constants
const (
	DefaultNamespace = "skyhook"
	// MinAnnotationSupportVersion is the minimum operator version that supports annotation-based pause/disable
	MinAnnotationSupportVersion = "v0.8.0"
)

// CompareVersions compares two semver versions.
// Returns -1 if v1 < v2, 0 if equal, 1 if v1 > v2.
// Handles "v" prefix automatically.
// Returns 0 if either version is invalid (non-semver like "dev").
func CompareVersions(v1, v2 string) int {
	// Handle empty versions
	if v1 == "" {
		if v2 == "" {
			return 0
		}
		return -1
	}
	if v2 == "" {
		return 1
	}

	// Ensure "v" prefix
	if v1[0] != 'v' {
		v1 = "v" + v1
	}
	if v2[0] != 'v' {
		v2 = "v" + v2
	}

	// If either version is invalid semver, return 0 (treat as equal/unknown)
	if !semver.IsValid(v1) || !semver.IsValid(v2) {
		return 0
	}

	return semver.Compare(v1, v2)
}

// IsValidVersion checks if a version string is a valid semver.
func IsValidVersion(v string) bool {
	if v == "" {
		return false
	}
	if v[0] != 'v' {
		v = "v" + v
	}
	return semver.IsValid(v)
}

// GetSkyhook fetches a Skyhook CR by name using the dynamic client.
func GetSkyhook(ctx context.Context, dynamicClient dynamic.Interface, name string) (*v1alpha1.Skyhook, error) {
	gvr := v1alpha1.GroupVersion.WithResource("skyhooks")
	obj, err := dynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting skyhook %q: %w", name, err)
	}
	return UnstructuredToSkyhook(obj)
}

// GetSkyhookVersion extracts the operator version from a Skyhook's annotation.
// Returns empty string if the annotation is not present.
func GetSkyhookVersion(skyhook *v1alpha1.Skyhook) string {
	if skyhook == nil || skyhook.Annotations == nil {
		return ""
	}
	return skyhook.Annotations[VersionAnnotation]
}

// DiscoverOperatorVersion queries the cluster to find the Skyhook operator version.
// It searches for deployments that look like the Skyhook operator (by checking labels
// or container images for "skyhook") and extracts the version.
func DiscoverOperatorVersion(ctx context.Context, kube kubernetes.Interface, namespace string) (string, error) {
	if kube == nil {
		return "", fmt.Errorf("nil kubernetes client")
	}
	if namespace == "" {
		namespace = DefaultNamespace
	}

	// List all deployments in the namespace and find the Skyhook operator
	deployments, err := kube.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listing deployments in namespace %q: %w", namespace, err)
	}

	for _, deployment := range deployments.Items {
		if !isSkyhookOperatorDeployment(&deployment) {
			continue
		}

		// Try to get version from Helm label (preferred for Helm deployments)
		if v := deployment.Labels["app.kubernetes.io/version"]; strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}

		// Fallback: parse version from container image tag (works for kustomize deployments)
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if tag := ExtractImageTag(container.Image); tag != "" && tag != "latest" {
				return tag, nil
			}
		}
	}

	return "", fmt.Errorf("unable to determine operator version; no skyhook operator deployment found in namespace %q", namespace)
}

// isSkyhookOperatorDeployment checks if a deployment looks like the Skyhook operator
// by examining container images for "skyhook" (most reliable), then labels as fallback.
func isSkyhookOperatorDeployment(deployment *appsv1.Deployment) bool {
	// Check container images for "skyhook" (most reliable - image name won't change)
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if strings.Contains(strings.ToLower(container.Image), "skyhook") {
			return true
		}
	}

	// Fallback: check labels for "skyhook"
	for key, value := range deployment.Labels {
		if strings.Contains(strings.ToLower(key), "skyhook") ||
			strings.Contains(strings.ToLower(value), "skyhook") {
			return true
		}
	}

	return false
}

// ExtractImageTag extracts the tag from a container image reference.
// Examples:
//   - "ghcr.io/nvidia/skyhook/operator:v1.2.3" -> "v1.2.3"
//   - "ghcr.io/nvidia/skyhook/operator:v1.2.3@sha256:..." -> "v1.2.3"
//   - "ghcr.io/nvidia/skyhook/operator" -> ""
func ExtractImageTag(image string) string {
	// Remove digest if present (e.g., @sha256:...)
	if idx := strings.Index(image, "@"); idx > 0 {
		image = image[:idx]
	}

	// Split on ":" to get tag
	parts := strings.Split(image, ":")
	if len(parts) < 2 {
		return ""
	}

	return strings.TrimSpace(parts[len(parts)-1])
}

// PatchSkyhookStatusRaw patches the skyhook status with raw JSON bytes.
// Use this when you need explicit null values to clear omitempty fields.
func PatchSkyhookStatusRaw(ctx context.Context, dynamicClient dynamic.Interface, skyhookName string, patchBytes []byte) error {
	gvr := v1alpha1.GroupVersion.WithResource("skyhooks")
	_, err := dynamicClient.Resource(gvr).Patch(
		ctx,
		skyhookName,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
		"status",
	)
	if err != nil {
		return fmt.Errorf("patching skyhook %q status: %w", skyhookName, err)
	}
	return nil
}

// PatchSkyhookStatus patches the status subresource of a Skyhook CR using the dynamic client.
// This is used to update status fields without triggering a spec update.
func PatchSkyhookStatus(ctx context.Context, dynamicClient dynamic.Interface, skyhookName string, status v1alpha1.SkyhookStatus) error {
	statusBytes, err := json.Marshal(map[string]interface{}{
		"status": status,
	})
	if err != nil {
		return fmt.Errorf("marshaling status: %w", err)
	}

	gvr := v1alpha1.GroupVersion.WithResource("skyhooks")
	_, err = dynamicClient.Resource(gvr).Patch(
		ctx,
		skyhookName,
		types.MergePatchType,
		statusBytes,
		metav1.PatchOptions{},
		"status",
	)
	if err != nil {
		return fmt.Errorf("patching skyhook %q status: %w", skyhookName, err)
	}

	return nil
}
