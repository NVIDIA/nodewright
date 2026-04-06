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
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

type PackageSkyhook struct {
	v1alpha1.PackageRef `json:",inline"`
	Skyhook             string         `json:"skyhook"`
	Stage               v1alpha1.Stage `json:"stage"`
	Image               string         `json:"image"`
	ContainerSHA        string         `json:"containerSHA,omitempty"`
	Invalid             bool           `json:"invalid,omitempty"`
}

// GetPackage returns the package from the pod annotations
func GetPackage(pod *corev1.Pod) (*PackageSkyhook, error) {
	if pod == nil {
		return nil, nil
	}
	s, ok := pod.Annotations[fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)]
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

// SetPackages sets the package in the pod annotations
func SetPackages(pod *corev1.Pod, skyhook *v1alpha1.Skyhook, image string, stage v1alpha1.Stage, _package *v1alpha1.Package) error {
	if pod == nil || _package == nil {
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

	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)] = string(data)

	return nil
}

// InvalidatePackage invalidates a package and updates the pod, which will trigger the pod to be deleted
func InvalidatePackage(pod *corev1.Pod) error {
	if pod == nil {
		return nil
	}

	pkg, err := GetPackage(pod)
	if err != nil {
		return fmt.Errorf("error getting package: %w", err)
	}

	pkg.Invalid = true

	data, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("error marshalling package: %w", err)
	}

	pod.Annotations[fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)] = string(data)

	return nil
}

// IsInvalidPackage returns true if the package is invalid
func IsInvalidPackage(pod *corev1.Pod) (bool, error) {
	if pod == nil {
		return false, nil
	}

	pkg, err := GetPackage(pod)
	if err != nil {
		return false, fmt.Errorf("error getting package: %w", err)
	}
	return pkg.Invalid, nil
}
