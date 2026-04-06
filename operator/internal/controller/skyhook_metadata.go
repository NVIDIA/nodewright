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

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
)

// PackageMetadata defines the intermediary contract for a single package that the agent can consume
type PackageMetadata struct {
	Name               string                        `json:"name"`
	Version            string                        `json:"version"`
	Image              string                        `json:"image"`
	AgentImageOverride string                        `json:"agentImageOverride,omitempty"`
	Interrupt          *v1alpha1.Interrupt           `json:"interrupt,omitempty"`
	ConfigInterrupts   map[string]v1alpha1.Interrupt `json:"configInterrupts,omitempty"`
}

// SkyhookMetadata defines the node metadata contract exposed to the agent
type SkyhookMetadata struct {
	AgentVersion string                     `json:"agentVersion"`
	Packages     map[string]PackageMetadata `json:"packages"`
}

// NewSkyhookMetadata builds the intermediary SkyhookMetadata from the CR spec and operator options
func NewSkyhookMetadata(opts SkyhookOperatorOptions, s *wrapper.Skyhook) SkyhookMetadata {
	packages := make(map[string]PackageMetadata)
	for name, p := range s.Spec.Packages {
		packages[name] = PackageMetadata{
			Name:               p.Name,
			Version:            p.Version,
			Image:              p.Image,
			AgentImageOverride: p.AgentImageOverride,
			Interrupt:          p.Interrupt,
			ConfigInterrupts:   p.ConfigInterrupts,
		}
	}

	return SkyhookMetadata{
		AgentVersion: opts.AgentVersion(),
		Packages:     packages,
	}
}

// Marshal returns the JSON encoding for inclusion in the node metadata configmap
func (m SkyhookMetadata) Marshal() ([]byte, error) {
	return json.Marshal(m)
}
