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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var (
	skyhooklog       = logf.Log.WithName("skyhook-resource")
	validPackageName = regexp.MustCompile(`(?m)^[a-z][-a-z0-9]{0,41}[a-z]$`)
)

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *Skyhook) SetupWebhookWithManager(mgr ctrl.Manager) error {
	skyhookWebhook := &SkyhookWebhook{
		Client: mgr.GetClient(),
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithDefaulter(skyhookWebhook).
		WithValidator(skyhookWebhook).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-skyhook-nvidia-com-v1alpha1-skyhook,mutating=true,failurePolicy=fail,sideEffects=None,groups=skyhook.nvidia.com,resources=skyhooks,verbs=create;update,versions=v1alpha1,name=mskyhook.kb.io,admissionReviewVersions=v1

// SkyhookWebhook validates Skyhook resources at admission time.
// Includes a client for validating references to DeploymentPolicies.
// +kubebuilder:object:generate=false
type SkyhookWebhook struct {
	Client client.Client
}

var _ admission.CustomDefaulter = &SkyhookWebhook{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *SkyhookWebhook) Default(ctx context.Context, obj runtime.Object) error {

	skyhook, ok := obj.(*Skyhook)
	if !ok {
		return fmt.Errorf("object is not a Skyhook")
	}

	skyhooklog.Info("default", "name", skyhook.Name)

	// TODO(user): fill in your defaulting logic.
	// Things we might want to default:
	//  - InterruptionBudget
	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-skyhook-nvidia-com-v1alpha1-skyhook,mutating=false,failurePolicy=fail,sideEffects=None,groups=skyhook.nvidia.com,resources=skyhooks,verbs=create;update,versions=v1alpha1,name=vskyhook.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &SkyhookWebhook{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *SkyhookWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {

	skyhook, ok := obj.(*Skyhook)
	if !ok {
		return nil, fmt.Errorf("object is not a Skyhook")
	}

	skyhooklog.Info("validate create", "name", skyhook.Name)

	if err := skyhook.Validate(); err != nil {
		return nil, err
	}

	return nil, r.validateDeploymentPolicyExists(ctx, skyhook)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SkyhookWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {

	skyhook, ok := newObj.(*Skyhook)
	if !ok {
		return nil, fmt.Errorf("object is not a Skyhook")
	}

	skyhooklog.Info("validate update", "name", skyhook.Name)

	if err := skyhook.Validate(); err != nil {
		return nil, err
	}

	var warnings admission.Warnings

	// Uninstall-specific validations require the old object
	if oldObj != nil {
		oldSkyhook, ok := oldObj.(*Skyhook)
		if ok {
			// Check for removal of uninstall-enabled packages
			for name, oldPkg := range oldSkyhook.Spec.Packages {
				if _, stillExists := skyhook.Spec.Packages[name]; !stillExists {
					if oldPkg.UninstallEnabled() {
						if !isPackageFullyUninstalled(oldSkyhook, name) {
							return nil, fmt.Errorf(
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
			for name, oldPkg := range oldSkyhook.Spec.Packages {
				newPkg, exists := skyhook.Spec.Packages[name]
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
					return nil, fmt.Errorf(
						"package %q: downgrade not allowed — set uninstall.apply=true first, "+
							"wait for uninstall to complete, then change version", name)
				}
				if !isPackageFullyUninstalled(oldSkyhook, name) {
					return nil, fmt.Errorf(
						"package %q: downgrade not allowed — uninstall has not yet completed "+
							"on all nodes. Wait for the uninstall to finish, then change version", name)
				}
			}

			// Warn (not reject) on cancel: apply going from true to false
			for name, oldPkg := range oldSkyhook.Spec.Packages {
				newPkg, exists := skyhook.Spec.Packages[name]
				if exists && oldPkg.IsUninstalling() && !newPkg.IsUninstalling() {
					warnings = append(warnings, fmt.Sprintf(
						"package %q: uninstall cancelled — nodes where uninstall completed will need to re-install", name))
				}
			}
		}
	}

	if err := r.validateDeploymentPolicyExists(ctx, skyhook); err != nil {
		return warnings, err
	}

	return warnings, nil
}

// isPackageFullyUninstalled checks whether a package has been uninstalled from all nodes
// by examining the NodeState in the Skyhook status (which mirrors node annotations).
//
// A Skyhook with zero tracked nodes (empty or nil Status.NodeState) is treated
// as fully uninstalled: there's nothing to uninstall, so there's nothing to
// block. Otherwise a Skyhook whose NodeSelector matches no nodes — or that has
// never been reconciled — would be permanently un-editable.
func isPackageFullyUninstalled(skyhook *Skyhook, packageName string) bool {
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
func (r *SkyhookWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	skyhook, ok := obj.(*Skyhook)
	if !ok {
		return nil, fmt.Errorf("object is not a Skyhook")
	}

	skyhooklog.Info("validate delete", "name", skyhook.Name)

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

func (r *Skyhook) Validate() error {

	if err := r.Spec.InterruptionBudget.Validate(); err != nil {
		return err
	}

	// DeploymentPolicy and InterruptionBudget are mutually exclusive
	if r.Spec.DeploymentPolicy != "" && (r.Spec.InterruptionBudget.Percent != nil || r.Spec.InterruptionBudget.Count != nil) {
		return fmt.Errorf("deploymentPolicy and interruptionBudget are mutually exclusive")
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

		image, version, found := strings.Cut(v.Image, ":")
		if found && version != v.Version {
			return fmt.Errorf(
				"error package %s's image tag was set to '%s' for '%s' and doesn't match the pacakge's version '%s'. Do not explicitly set the image's tag in the package's definition (The package version will be set as the tag)",
				name,
				version,
				image,
				v.Version,
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
	}

	var graph graph.DependencyGraph[*Package]

	var err error
	graph, err = r.Spec.BuildGraph()
	if err != nil {
		return fmt.Errorf("error trying to validate skyhook spec building graph: %s", err)
	}

	err = graph.Valid()
	if err != nil {
		return fmt.Errorf("error trying to validate skyhook spec graph is invalid: %s", err)
	}

	return nil
}

// validateDeploymentPolicyExists checks if the referenced DeploymentPolicy exists
func (r *SkyhookWebhook) validateDeploymentPolicyExists(ctx context.Context, skyhook *Skyhook) error {
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
