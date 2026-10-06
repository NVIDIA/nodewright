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

package v1alpha1

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NVIDIA/nodewright/operator/internal/graph"
	semver "github.com/NVIDIA/nodewright/operator/internal/version"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	validPackageName = regexp.MustCompile(`^[a-z][-a-z0-9]{0,41}[a-z]$`)
)

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *NodeWright) SetupWebhookWithManager(mgr ctrl.Manager) error {
	skyhookWebhook := &NodeWrightWebhook{
		Client: mgr.GetClient(),
	}
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithDefaulter(skyhookWebhook).
		WithValidator(skyhookWebhook).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-nodewright-nvidia-com-v1alpha1-nodewright,mutating=true,failurePolicy=fail,sideEffects=None,groups=nodewright.nvidia.com,resources=nodewrights,verbs=create;update,versions=v1alpha1,name=mnodewright.kb.io,admissionReviewVersions=v1

// NodeWrightWebhook validates NodeWright resources at admission time.
// Includes a client for validating references to DeploymentPolicies.
// +kubebuilder:object:generate=false
type NodeWrightWebhook struct {
	Client client.Client
}

var _ admission.Defaulter[*NodeWright] = &NodeWrightWebhook{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *NodeWrightWebhook) Default(ctx context.Context, skyhook *NodeWright) error {

	logf.FromContext(ctx).Info("default", "name", skyhook.Name)

	// TODO(user): fill in your defaulting logic.
	// Things we might want to default:
	//  - InterruptionBudget
	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-nodewright-nvidia-com-v1alpha1-nodewright,mutating=false,failurePolicy=fail,sideEffects=None,groups=nodewright.nvidia.com,resources=nodewrights,verbs=create;update,versions=v1alpha1,name=vnodewright.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*NodeWright] = &NodeWrightWebhook{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *NodeWrightWebhook) ValidateCreate(ctx context.Context, skyhook *NodeWright) (admission.Warnings, error) {

	logf.FromContext(ctx).Info("validate create", "name", skyhook.Name)

	var warnings admission.Warnings

	if err := skyhook.Validate(); err != nil {
		return warnings, fmt.Errorf("validating NodeWright %q: %w", skyhook.Name, err)
	}

	return warnings, r.validateDeploymentPolicyExists(ctx, skyhook)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *NodeWrightWebhook) ValidateUpdate(ctx context.Context, oldNodeWright, newNodeWright *NodeWright) (admission.Warnings, error) {

	logf.FromContext(ctx).Info("validate update", "name", newNodeWright.Name)

	var warnings admission.Warnings

	if err := newNodeWright.Validate(); err != nil {
		return warnings, fmt.Errorf("validating NodeWright %q: %w", newNodeWright.Name, err)
	}

	// Uninstall-specific validations require the old object
	if oldNodeWright != nil {
		// Check for removal of uninstall-enabled packages
		for name, oldPkg := range oldNodeWright.Spec.Packages {
			if _, stillExists := newNodeWright.Spec.Packages[name]; !stillExists {
				if oldPkg.UninstallEnabled() {
					if !isPackageFullyUninstalled(oldNodeWright, name) {
						return warnings, fmt.Errorf(
							"package %q has uninstall.enabled=true; set uninstall.apply=true "+
								"and wait for completion before removing from spec", name)
					}
				}
			}
		}

		// Reject version downgrade unless the package has already been explicitly
		// uninstalled on all nodes. The user must have flipped uninstall.apply=true
		// on the old spec AND waited for the uninstall to complete (package absent
		// from every tracked node's state) before changing the version.
		for name, oldPkg := range oldNodeWright.Spec.Packages {
			newPkg, exists := newNodeWright.Spec.Packages[name]
			if !exists {
				continue
			}
			if newPkg.Version == oldPkg.Version {
				continue
			}
			if !semver.IsValid(newPkg.Version) || !semver.IsValid(oldPkg.Version) {
				continue // not comparable; Validate() rejects invalid formats separately
			}
			if semver.Compare(newPkg.Version, oldPkg.Version) != -1 {
				continue // upgrade or equal — allowed
			}

			if !oldPkg.UninstallEnabled() {
				continue // explicit uninstall not required for this package
			}

			// Downgrade. Required:
			//   (a) OLD spec already had uninstall.apply=true, AND
			//   (b) package absent from all nodes (uninstall complete per D2).
			if !oldPkg.IsUninstalling() {
				return warnings, fmt.Errorf(
					"package %q: downgrade not allowed — set uninstall.apply=true first, "+
						"wait for uninstall to complete, then change version", name)
			}
			if !isPackageFullyUninstalled(oldNodeWright, name) {
				return warnings, fmt.Errorf(
					"package %q: downgrade not allowed — uninstall has not yet completed "+
						"on all nodes. Wait for the uninstall to finish, then change version", name)
			}
		}

		// Warn (not reject) on cancel: apply going from true to false
		for name, oldPkg := range oldNodeWright.Spec.Packages {
			newPkg, exists := newNodeWright.Spec.Packages[name]
			if exists && oldPkg.IsUninstalling() && !newPkg.IsUninstalling() {
				warnings = append(warnings, fmt.Sprintf(
					"package %q: uninstall cancelled — nodes where uninstall completed will need to re-install", name))
			}
		}
	}

	if err := r.validateDeploymentPolicyExists(ctx, newNodeWright); err != nil {
		return warnings, err
	}

	return warnings, nil
}

// isPackageFullyUninstalled checks whether a package has been uninstalled from all nodes
// by examining the NodeState in the NodeWright status (which mirrors node annotations).
//
// A NodeWright with zero tracked nodes (empty or nil Status.NodeState) is treated
// as fully uninstalled: there's nothing to uninstall, so there's nothing to
// block. Otherwise a NodeWright whose NodeSelector matches no nodes — or that has
// never been reconciled — would be permanently un-editable.
func isPackageFullyUninstalled(skyhook *NodeWright, packageName string) bool {
	pkg, exists := skyhook.Spec.Packages[packageName]
	if !exists {
		return false
	}
	uniqueName := pkg.GetUniqueName()
	for _, nodeState := range skyhook.Status.NodeState {
		if _, inState := nodeState[uniqueName]; inState {
			return false // still present on at least one node
		}
	}
	return true
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *NodeWrightWebhook) ValidateDelete(ctx context.Context, skyhook *NodeWright) (admission.Warnings, error) {

	logf.FromContext(ctx).Info("validate delete", "name", skyhook.Name)

	// I do yet know if we need to do any valuations on delete,
	// if so guessing they would be different than update and create anyways
	return nil, nil
}

func validateResourceOverrides(name string, res *ResourceRequirements) error {
	if res == nil {
		return nil
	}
	anySet := !res.CPURequest.IsZero() || !res.CPULimit.IsZero() || !res.MemoryRequest.IsZero() || !res.MemoryLimit.IsZero()
	allSet := !res.CPURequest.IsZero() && !res.CPULimit.IsZero() && !res.MemoryRequest.IsZero() && !res.MemoryLimit.IsZero()
	if anySet && !allSet {
		return fmt.Errorf("package %q: if any resource override is set, all of cpuRequest, cpuLimit, memoryRequest, memoryLimit must be set", name)
	}
	if allSet {
		if res.CPULimit.Cmp(res.CPURequest) < 0 {
			return fmt.Errorf("package %q: cpuLimit (%s) must be >= cpuRequest (%s)", name, res.CPULimit.String(), res.CPURequest.String())
		}
		if res.MemoryLimit.Cmp(res.MemoryRequest) < 0 {
			return fmt.Errorf("package %q: memoryLimit (%s) must be >= memoryRequest (%s)", name, res.MemoryLimit.String(), res.MemoryRequest.String())
		}
		if res.CPURequest.Sign() <= 0 || res.CPULimit.Sign() <= 0 || res.MemoryRequest.Sign() <= 0 || res.MemoryLimit.Sign() <= 0 {
			return fmt.Errorf("package %q: all resource values must be positive", name)
		}
	}
	return nil
}

func (r *NodeWright) Validate() error {

	if err := r.Spec.InterruptionBudget.Validate(); err != nil {
		return fmt.Errorf("interruption budget: %w", err)
	}

	if err := r.Spec.DrainConfig.Validate(); err != nil {
		return fmt.Errorf("drain config: %w", err)
	}

	// DeploymentPolicy and InterruptionBudget are mutually exclusive
	if r.Spec.DeploymentPolicy != "" && (r.Spec.InterruptionBudget.Percent != nil || r.Spec.InterruptionBudget.Count != nil) {
		return fmt.Errorf("deploymentPolicy and interruptionBudget are mutually exclusive")
	}

	if _, err := metav1.LabelSelectorAsSelector(&r.Spec.PodNonInterruptLabels); err != nil {
		return fmt.Errorf("pod non-interrupt labels are not valid: %w", err)
	}

	if _, err := metav1.LabelSelectorAsSelector(&r.Spec.NodeSelector); err != nil {
		return fmt.Errorf("node selectors are not valid: %w", err)
	}

	names := make(map[string]string)
	for name, v := range r.Spec.Packages {
		// test for package names to be unique and that the name and package key match
		if v.Name != name {
			return fmt.Errorf("error package %s's name was set to %s. Do not explicitly set the name in the package's definition", name, v.Name)
		}

		key := v.Name
		if val, ok := names[key]; ok {
			return fmt.Errorf("error duplicate packages different versions [%s:%s] and [%s:%s]", key, v.Version, key, val)
		}
		names[key] = v.Version

		// test name is valid RFC 1123
		if !validPackageName.MatchString(key) {
			return fmt.Errorf("package name [%s] is not valid. must match [%s]", key, validPackageName.String())
		}

		// test to make sure that the config interrupts are for valid packages
		for pattern := range v.ConfigInterrupts {
			// exact key present
			if _, exists := v.ConfigMap[pattern]; exists {
				continue
			}

			// Only '*' is supported as a glob meta character
			isGlob := strings.Contains(pattern, "*")
			if isGlob {
				matchedAny := false
				for key := range v.ConfigMap {
					if ok, err := filepath.Match(pattern, key); err == nil && ok {
						matchedAny = true
						break
					}
				}
				if matchedAny {
					continue
				}
				return fmt.Errorf("error config interrupt glob %q does not match any configMap keys", pattern)
			}

			// not a glob and not an exact key
			return fmt.Errorf("error config interrupt for key that doesn't exist: %s doesn't exist as a configmap", pattern)
		}

		// image must be a bare registry/repository reference. version is the operator's
		// ordering key (it drives upgrade/downgrade detection) and containerSHA pins the
		// exact bytes the kubelet pulls, so neither a tag nor a digest may be embedded in
		// image. Both are rejected rather than silently stripped: an inline tag was once
		// absorbed as a migration off the pre-semver scheme, but that migration is done,
		// so tags and digests now behave identically. tag/digest are non-nil whenever
		// their separator is present, so an empty separator ("repo@", "repo:") is
		// rejected too rather than slipping through to an invalid pull reference.
		// (Emptiness and whitespace are enforced declaratively by the CRD's Pattern.)
		_, tag, digest := splitImageReference(v.Image)
		if digest != nil {
			return fmt.Errorf(
				"error package %s's image '%s' contains an inline digest. Do not embed a digest in the image; pin the image bytes using the package's containerSHA field instead",
				name,
				v.Image,
			)
		}
		if tag != nil {
			return fmt.Errorf(
				"error package %s's image '%s' contains an inline tag '%s'. Do not embed a tag in the image; the package version supplies the tag",
				name,
				v.Image,
				*tag,
			)
		}

		if !semver.IsValid(v.Version) {
			return fmt.Errorf("error version string for %s is invalid: %s", v.Name, v.Version)
		}

		if err := validateResourceOverrides(name, v.Resources); err != nil {
			return err
		}

		// Validate uninstall configuration
		if v.Uninstall != nil && v.Uninstall.Apply && !v.Uninstall.Enabled {
			return fmt.Errorf("package %q: uninstall.apply requires uninstall.enabled to be true", name)
		}

		// stageTimeout bounds one attempt (the stage Job's pod template activeDeadlineSeconds; on
		// interrupt Jobs, the Job's own). 0 removes the time bound, so only the retry budget limits
		// the stage (nothing, for a reboot interrupt), and an attempt that hangs never spends it;
		// see the field docs. Negatives are meaningless.
		if v.StageTimeout != nil && v.StageTimeout.Duration < 0 {
			return fmt.Errorf("package %q: stageTimeout must be greater than or equal to 0", name)
		}
	}

	var graph graph.DependencyGraph[*Package]

	var err error
	graph, err = r.Spec.BuildGraph()
	if err != nil {
		return fmt.Errorf("error trying to validate NodeWright spec building graph: %w", err)
	}

	err = graph.Valid()
	if err != nil {
		return fmt.Errorf("error trying to validate NodeWright spec graph is invalid: %w", err)
	}

	return nil
}

// validateDeploymentPolicyExists checks if the referenced DeploymentPolicy exists
func (r *NodeWrightWebhook) validateDeploymentPolicyExists(ctx context.Context, skyhook *NodeWright) error {
	// Skip validation if no deployment policy is specified
	if skyhook.Spec.DeploymentPolicy == "" {
		return nil
	}

	// Check if the DeploymentPolicy exists (cluster-scoped, no namespace)
	policy := &DeploymentPolicy{}
	err := r.Client.Get(ctx, types.NamespacedName{
		Name: skyhook.Spec.DeploymentPolicy,
	}, policy)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("deploymentPolicy %q not found", skyhook.Spec.DeploymentPolicy)
		}
		return fmt.Errorf("error checking if deploymentPolicy %q exists: %w", skyhook.Spec.DeploymentPolicy, err)
	}

	return nil
}
