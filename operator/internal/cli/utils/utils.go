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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
)

// Output format constants
const (
	OutputFormatTable = "table"
	OutputFormatJSON  = "json"
	OutputFormatYAML  = "yaml"
	OutputFormatWide  = "wide"
)

// RegisterNodeWrightNameFlag adds the `--nodewright` flag (bound to *dest) that
// names the target NodeWright CR. It also keeps the legacy `--skyhook` flag
// working as a deprecated back-compat alias: the CR was renamed Skyhook ->
// NodeWright, so scripts that still pass `--skyhook` work but print a migration
// warning. When required is true, exactly one of the two must be supplied.
func RegisterNodeWrightNameFlag(cmd *cobra.Command, dest *string, usage string, required bool) {
	cmd.Flags().StringVar(dest, "nodewright", "", usage)
	cmd.Flags().StringVar(dest, "skyhook", "", usage)
	_ = cmd.Flags().MarkDeprecated("skyhook", "use --nodewright instead")
	cmd.MarkFlagsMutuallyExclusive("nodewright", "skyhook")
	if required {
		cmd.MarkFlagsOneRequired("nodewright", "skyhook")
	}
}

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
func UnstructuredToSkyhook(u *unstructured.Unstructured) (*v1alpha1.NodeWright, error) {
	data, err := u.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshaling unstructured: %w", err)
	}

	var skyhook v1alpha1.NodeWright
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

	gvr := v1alpha1.GroupVersion.WithResource("nodewrights")
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

	gvr := v1alpha1.GroupVersion.WithResource("nodewrights")
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

// SetNodeAnnotation sets an annotation on a Node using a merge patch. The
// value may contain arbitrary characters (including JSON-as-string): the
// patch is built by JSON-encoding the value rather than fmt %q quoting,
// which produces Go-style escapes that aren't valid JSON for multi-byte
// runes.
func SetNodeAnnotation(ctx context.Context, kubeClient kubernetes.Interface, nodeName, key, value string) error {
	keyJSON, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("encoding annotation key for node %q: %w", nodeName, err)
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding annotation value for node %q: %w", nodeName, err)
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%s:%s}}}`, keyJSON, valueJSON)

	_, err = kubeClient.CoreV1().Nodes().Patch(
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
	// DefaultNamespace is the namespace new NodeWright installs use, and the single
	// source of truth for the CLI's --namespace default (internal/cli/context seeds
	// the flag from it).
	DefaultNamespace = "nodewright"
	// LegacyDefaultNamespace is the namespace Skyhook installs used before the rename.
	// Namespaces cannot be renamed in place, so an existing install legitimately stays
	// here; the CLI discovers it rather than forcing a reinstall. See
	// ResolveOperatorNamespace.
	LegacyDefaultNamespace = "skyhook"
	// MinAnnotationSupportVersion is the minimum operator version that supports annotation-based pause/disable
	MinAnnotationSupportVersion = "v0.8.0"
	// MinNodeStateSupportVersion is the lowest operator version known to use
	// the current map[string]PackageStatus shape for the
	// nodewright.nvidia.com/nodeState_<name> annotation. The shape itself has
	// been stable since this version; what has evolved in later releases is
	// the set of recognized stage / state values (e.g. uninstall and
	// uninstall-interrupt were added in v0.16.0). Users targeting an older
	// operator must pick stage / state values that operator recognises.
	MinNodeStateSupportVersion = "v0.7.5"
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
func GetSkyhook(ctx context.Context, dynamicClient dynamic.Interface, name string) (*v1alpha1.NodeWright, error) {
	gvr := v1alpha1.GroupVersion.WithResource("nodewrights")
	obj, err := dynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting NodeWright %q: %w", name, err)
	}
	return UnstructuredToSkyhook(obj)
}

// KeepCordonOnReset reports whether a reset should leave the NodeWright's cordon_<name>
// annotation on its nodes. Reset never clears spec.unschedulable, and the operator only
// releases a cordon it holds that annotation for, so the annotation is removed only once
// the NodeWright is confirmed gone; otherwise its re-run could never uncordon the node.
// When existence cannot be established the annotation is kept, since a leftover annotation
// is harmless while a stripped one strands the node cordoned.
func KeepCordonOnReset(ctx context.Context, cmd *cobra.Command, dynamicClient dynamic.Interface, name string) bool {
	if dynamicClient == nil {
		return true
	}
	_, err := dynamicClient.Resource(v1alpha1.GroupVersion.WithResource("nodewrights")).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not confirm NodeWright %q exists, keeping its cordon annotation: %v\n", name, err)
	}
	return true
}

// CheckNodeStateOperatorVersion rejects the call when the running operator is
// older than MinNodeStateSupportVersion. The check first reads the version
// annotation written by the operator onto the Skyhook CR; when that's missing
// or non-semver (e.g. "dev") it falls back to inspecting the operator
// Deployment. If neither source yields a valid version we warn but allow the
// edit to proceed — better than refusing every command in clusters where the
// CLI can't see the operator's namespace.
func CheckNodeStateOperatorVersion(ctx context.Context, cmd *cobra.Command, kube kubernetes.Interface, namespace string, skyhook *v1alpha1.NodeWright) error {
	opVersion := GetSkyhookVersion(skyhook)
	if opVersion == "" || !IsValidVersion(opVersion) {
		deployVersion, derr := DiscoverOperatorVersion(ctx, kube, namespace)
		if derr == nil && IsValidVersion(deployVersion) {
			opVersion = deployVersion
		} else {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: unable to determine operator version; cannot verify compatibility (requires %s+)\n", MinNodeStateSupportVersion)
		}
	}
	if IsValidVersion(opVersion) && CompareVersions(opVersion, MinNodeStateSupportVersion) < 0 {
		return fmt.Errorf("operator version %s does not support this command; minimum supported version is %s", opVersion, MinNodeStateSupportVersion)
	}
	return nil
}

// GetSkyhookVersion extracts the operator version from a Skyhook's annotation.
// Returns empty string if the annotation is not present.
func GetSkyhookVersion(skyhook *v1alpha1.NodeWright) string {
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

	return "", fmt.Errorf("unable to determine operator version; no nodewright operator deployment found in namespace %q", namespace)
}

// ResolveOperatorNamespace works out which namespace the operator is installed in
// when the user did not pass --namespace.
//
// WHY this exists rather than a plain default: the install namespace moved from
// "skyhook" to "nodewright" with the rename, and a namespace cannot be renamed in
// place. Existing installs legitimately stay in "skyhook" forever, so defaulting
// blindly to "nodewright" would answer "not found" for every one of them with no
// explanation. Probing is two namespaced List calls at worst, and needs no
// cluster-wide RBAC.
//
// Resolution order: DefaultNamespace, then LegacyDefaultNamespace, then a
// best-effort cluster-wide sweep for installs in some other namespace. When
// nothing answers, DefaultNamespace is returned so callers still produce their
// own, more specific error. found reports whether an operator was actually
// located, and legacy reports that the answer came from LegacyDefaultNamespace so
// the caller can warn.
func ResolveOperatorNamespace(ctx context.Context, kube kubernetes.Interface) (namespace string, found bool, legacy bool) {
	if kube == nil {
		return DefaultNamespace, false, false
	}

	for _, candidate := range []string{DefaultNamespace, LegacyDefaultNamespace} {
		if hasOperatorDeployment(ctx, kube, candidate) {
			return candidate, true, candidate == LegacyDefaultNamespace
		}
	}

	// Last resort. This needs cluster-wide list permission, which a scoped user may
	// not have, so a failure here is not an error: fall through to the default and
	// let the caller's own lookup report what is actually missing.
	deployments, err := kube.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, controllerManagerSelector)
	if err != nil {
		return DefaultNamespace, false, false
	}
	for i := range deployments.Items {
		if isSkyhookOperatorDeployment(&deployments.Items[i]) {
			return deployments.Items[i].Namespace, true, false
		}
	}

	return DefaultNamespace, false, false
}

// controllerManagerSelector narrows a Deployment list to the operator's own
// controller-manager, which both the chart and the kustomize overlay label.
//
// WHY this is stricter than DiscoverOperatorVersion's substring match: that match
// accepts any image or label merely CONTAINING "nodewright" or "skyhook", which is
// tolerable when the caller already named the namespace. Namespace resolution has
// no such anchor, so an unrelated deployment (say a "billing-nodewright-exporter"
// in the nodewright namespace) would otherwise decide the namespace and send every
// subsequent lookup to the wrong place.
var controllerManagerSelector = metav1.ListOptions{LabelSelector: "control-plane=controller-manager"}

func hasOperatorDeployment(ctx context.Context, kube kubernetes.Interface, namespace string) bool {
	deployments, err := kube.AppsV1().Deployments(namespace).List(ctx, controllerManagerSelector)
	if err != nil {
		return false
	}
	for i := range deployments.Items {
		if isSkyhookOperatorDeployment(&deployments.Items[i]) {
			return true
		}
	}
	return false
}

// isSkyhookOperatorDeployment checks if a deployment looks like the operator by
// examining container images (most reliable), then labels as fallback. It matches
// both "nodewright" (current) and "skyhook" (legacy): the operator image and names
// moved from skyhook -> nodewright, and legacy installs may still carry either.
func isSkyhookOperatorDeployment(deployment *appsv1.Deployment) bool {
	looksLikeOperator := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "nodewright") || strings.Contains(s, "skyhook")
	}

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if looksLikeOperator(container.Image) {
			return true
		}
	}

	for key, value := range deployment.Labels {
		if looksLikeOperator(key) || looksLikeOperator(value) {
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
	gvr := v1alpha1.GroupVersion.WithResource("nodewrights")
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

// ConfirmYN prints prompt to cmd.OutOrStdout and reads a single line from
// cmd.InOrStdin. Returns true only for "y" or "yes" (case-insensitive).
// Empty input and unrecognised input both return false (safe default).
func ConfirmYN(cmd *cobra.Command, prompt string) (bool, error) {
	_, _ = fmt.Fprint(cmd.OutOrStdout(), prompt)
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

// ListNodesWithSkyhookState returns the parsed NodeState for every node that
// has the nodewright.nvidia.com/nodeState_<skyhookName> annotation. If
// labelSelector is non-empty it is applied at the apiserver. Nodes with a
// malformed annotation are excluded from the result and accumulated into the
// returned error (so callers see partial success rather than a silent skip).
//
// On list-from-apiserver failure the returned map is nil; on parse-only
// failures the map is non-nil (possibly empty) and contains the nodes that
// did parse. Callers may use map identity (== nil) to distinguish hard
// failures from soft parse warnings.
func ListNodesWithSkyhookState(ctx context.Context, kubeClient kubernetes.Interface, skyhookName, labelSelector string) (map[string]v1alpha1.NodeState, error) {
	opts := metav1.ListOptions{}
	if labelSelector != "" {
		opts.LabelSelector = labelSelector
	}
	nodes, err := kubeClient.CoreV1().Nodes().List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	key := v1alpha1.METADATA_PREFIX + "/nodeState_" + skyhookName
	result := map[string]v1alpha1.NodeState{}
	var parseErrs []string

	for _, node := range nodes.Items {
		raw, ok := node.Annotations[key]
		if !ok {
			continue
		}
		var state v1alpha1.NodeState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			parseErrs = append(parseErrs, fmt.Sprintf("%s: %v", node.Name, err))
			continue
		}
		result[node.Name] = state
	}

	if len(parseErrs) > 0 {
		return result, fmt.Errorf("malformed nodeState annotation on %d node(s): %s", len(parseErrs), strings.Join(parseErrs, "; "))
	}
	return result, nil
}

// PatchBatchStates writes each compartment's batch state to the NodeWright's status with every
// field spelled out. Marshalling BatchProcessingState would not do: all of its fields are
// omitempty, so a reset's zeros and shouldStop=false would be left out of the body, and a merge
// patch keeps the stored value of any field the body does not mention.
func PatchBatchStates(ctx context.Context, dynamicClient dynamic.Interface, skyhookName string, compartments map[string]v1alpha1.CompartmentStatus) error {
	statuses := make(map[string]any, len(compartments))
	for name, cs := range compartments {
		bs := cs.BatchState
		if bs == nil {
			continue
		}
		statuses[name] = map[string]any{"batchState": map[string]any{
			"currentBatch":        bs.CurrentBatch,
			"consecutiveFailures": bs.ConsecutiveFailures,
			"completedNodes":      bs.CompletedNodes,
			"failedNodes":         bs.FailedNodes,
			"shouldStop":          bs.ShouldStop,
			"lastBatchSize":       bs.LastBatchSize,
			"lastBatchFailed":     bs.LastBatchFailed,
		}}
	}
	patch, err := json.Marshal(map[string]any{"status": map[string]any{"compartmentStatuses": statuses}})
	if err != nil {
		return fmt.Errorf("marshaling batch states: %w", err)
	}
	return PatchSkyhookStatusRaw(ctx, dynamicClient, skyhookName, patch)
}
