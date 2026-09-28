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
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// packageAnnotationKey is the annotation every package executor carries to round-trip
// the package it runs: a Pod today, and a batch/v1 Job plus its pod template once
// stages run as Jobs.
const packageAnnotationKey = v1alpha1.METADATA_PREFIX + "/package"

// autoTaintAnnotationKey is the annotation marking a node the operator auto-tainted. It embeds
// the runtime-required taint key, so SkyhookOperatorOptions.Validate rejects a key that would
// make it an invalid annotation key.
func autoTaintAnnotationKey(taintKey string) string {
	return v1alpha1.METADATA_PREFIX + "/autoTaint_" + taintKey
}

// isNil reports whether obj is nil, catching both a nil interface and a typed-nil
// pointer (e.g. a (*corev1.Pod)(nil)). These helpers take an interface, so a plain
// obj == nil misses the typed-nil case, which would then panic in GetAnnotations.
func isNil(obj metav1.Object) bool {
	if obj == nil {
		return true
	}
	v := reflect.ValueOf(obj)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

type PackageSkyhook struct {
	v1alpha1.PackageRef `json:",inline"`
	Skyhook             string         `json:"skyhook"`
	Stage               v1alpha1.Stage `json:"stage"`
	Image               string         `json:"image"`
	ContainerSHA        string         `json:"containerSHA,omitempty"`
	Invalid             bool           `json:"invalid,omitempty"`
}

// GetPackage returns the package from the object's annotations. These helpers take
// metav1.Object rather than client.Object because they only touch metadata and must
// also accept a *corev1.PodTemplateSpec (the Job pod template), which satisfies
// metav1.Object but not client.Object (it has no runtime.Object methods).
func GetPackage(obj metav1.Object) (*PackageSkyhook, error) {
	if isNil(obj) {
		return nil, nil
	}
	s, ok := obj.GetAnnotations()[packageAnnotationKey]
	if !ok {
		return nil, nil
	}
	ret := &PackageSkyhook{}
	err := json.Unmarshal([]byte(s), &ret)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling package: %w", err)
	}

	return ret, nil
}

// SetPackages sets the package in the object's annotations
func SetPackages(obj metav1.Object, skyhook *v1alpha1.NodeWright, image string, stage v1alpha1.Stage, _package *v1alpha1.Package) error {
	if isNil(obj) || _package == nil {
		return nil
	}

	strk := &PackageSkyhook{
		Skyhook:      skyhook.Name,
		Stage:        stage,
		PackageRef:   _package.PackageRef,
		Image:        image,
		ContainerSHA: _package.ContainerSHA,
	}

	data, err := json.Marshal(strk)
	if err != nil {
		return fmt.Errorf("error marshalling package: %w", err)
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[packageAnnotationKey] = string(data)
	obj.SetAnnotations(annotations)

	return nil
}

// InvalidatePackage invalidates a package and updates the object, which will trigger the executor to be deleted
func InvalidatePackage(obj metav1.Object) error {
	if isNil(obj) {
		return nil
	}

	pkg, err := GetPackage(obj)
	if err != nil {
		return fmt.Errorf("error getting package: %w", err)
	}
	// No package annotation to invalidate: GetPackage returns nil when absent.
	if pkg == nil {
		return nil
	}

	pkg.Invalid = true

	data, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("error marshalling package: %w", err)
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[packageAnnotationKey] = string(data)
	obj.SetAnnotations(annotations)

	return nil
}

// IsInvalidPackage returns true if the package is invalid
func IsInvalidPackage(obj metav1.Object) (bool, error) {
	if isNil(obj) {
		return false, nil
	}

	pkg, err := GetPackage(obj)
	if err != nil {
		return false, fmt.Errorf("error getting package: %w", err)
	}
	// No package annotation: an object without one is not an invalid package.
	if pkg == nil {
		return false, nil
	}
	return pkg.Invalid, nil
}
