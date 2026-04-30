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

package wrapper

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewSkyhookWrapper(s *v1alpha1.Skyhook) *Skyhook {
	return &Skyhook{
		Skyhook: s,
	}
}

type Skyhook struct {
	*v1alpha1.Skyhook
	// nodes []*corev1.Node
	// Updated is set to true when the skyhook has been updated, used to track changes to the skyhook
	// and to determine if the skyhook needs to be updated in the API
	// this is used to avoid unnecessary API calls
	Updated bool
}

func (s *Skyhook) ResourceID() string {
	return fmt.Sprintf("%s-%s-%d", s.GetName(), s.GetUID(), s.GetGeneration())
}

func (s *Skyhook) SetStatus(status v1alpha1.Status) {

	if s.Status.Status != status {
		s.Status.Status = status
		s.Updated = true
	}

	switch status {
	case v1alpha1.StatusComplete:
		s.Status.ObservedGeneration = s.Generation // sort of the big... complete stamp
		s.Updated = true
	case v1alpha1.StatusUnknown:
		if s.Status.NodeState != nil {
			s.Status.NodeState = nil
			s.Updated = true
		}
	}

}

func (s *Skyhook) SetNodeStatus(nodeName string, status v1alpha1.Status) {

	if s.Status.NodeStatus == nil {
		s.Status.NodeStatus = make(map[string]v1alpha1.Status)
	}

	v, ok := s.Status.NodeStatus[nodeName]
	if !ok || v != status {
		s.Status.NodeStatus[nodeName] = status
		s.Updated = true
	}
}

func (s *Skyhook) SetNodeState(nodeName string, nodestate v1alpha1.NodeState) {

	if s.Status.NodeState == nil {
		s.Status.NodeState = make(map[string]v1alpha1.NodeState)
	}

	state, ok := s.Status.NodeState[nodeName]
	if !ok || !state.Equal(&nodestate) {
		s.Status.NodeState[nodeName] = nodestate
		s.Updated = true
	}
}

func (s *Skyhook) GetComplete(node string) {
	//nodeState := s.Status.NodeState[node]

}

// AddConfigUpdates Adds the specified package and key to the config updates
func (s *Skyhook) AddConfigUpdates(_package string, newKeys ...string) {
	if s.Status.ConfigUpdates == nil {
		s.Status.ConfigUpdates = make(map[string][]string, 0)
	}

	for _, newKey := range newKeys {
		found := false

		// check to see if key already exists
		configUpdates := s.Status.ConfigUpdates[_package]
		for _, oldKey := range configUpdates {
			if newKey == oldKey {
				found = true
			}
		}

		// if key doesn't already exist then add it
		if !found {
			s.Status.ConfigUpdates[_package] = append(s.Status.ConfigUpdates[_package], newKey)
			s.Updated = true
		}
	}
}

// RemoveConfigUpdates removes all changes for specified package in the config updates
func (s *Skyhook) RemoveConfigUpdates(_package string) {
	if s.Status.ConfigUpdates[_package] != nil {
		delete(s.Status.ConfigUpdates, _package)
	}

	s.Updated = true
}

// GetConfigUpdates gets the config updates
func (s *Skyhook) GetConfigUpdates() map[string][]string {
	return s.Status.ConfigUpdates
}

// GetConfigInterrupts gets all the config interrupts needed based on the current config updates
func (s *Skyhook) GetConfigInterrupts() map[string][]*v1alpha1.Interrupt {
	interrupts := make(map[string][]*v1alpha1.Interrupt)

	for _pkg := range s.Spec.Packages {
		_package := s.Spec.Packages[_pkg]

		// Track duplicates to avoid adding the same interrupt multiple times per package
		seen := make(map[string]struct{})

		for _, update := range s.Status.ConfigUpdates[_package.Name] {
			for pattern, interrupt := range _package.ConfigInterrupts {
				// filepath.Match treats a non-glob pattern as a literal
				if ok, err := filepath.Match(pattern, update); err == nil && ok {
					if interrupts[_package.Name] == nil {
						interrupts[_package.Name] = make([]*v1alpha1.Interrupt, 0)
					}

					key := fmt.Sprintf("%s|%s", interrupt.Type, strings.Join(interrupt.Services, ","))
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}

					interrupts[_package.Name] = append(interrupts[_package.Name], &interrupt)
				}
			}
		}
	}

	return interrupts
}

func (s *Skyhook) GetCompleteNodes() string {
	return s.Status.CompleteNodes
}

func (s *Skyhook) SetCompleteNodes(completeNodes string) {
	s.Status.CompleteNodes = completeNodes
	s.Updated = true
}

func (s *Skyhook) GetPackageList() string {
	return s.Status.PackageList
}

func (s *Skyhook) SetPackageList(packageList string) {
	s.Status.PackageList = packageList
	s.Updated = true
}

func (s *Skyhook) GetNodesInProgress() int {
	return s.Status.NodesInProgress
}

func (s *Skyhook) SetNodesInProgress(nodesInProgress int) {
	s.Status.NodesInProgress = nodesInProgress
	s.Updated = true
}

func (s *Skyhook) AddCondition(cond metav1.Condition) {
	_ = AddSkyhookCondition(s, cond)
}

func (s *Skyhook) SetVersion() {

	if version.VERSION == "" {
		return
	}

	current := s.GetVersion()
	if current == version.VERSION { // if has not changed, do nothing and not set updated
		return
	}

	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[fmt.Sprintf("%s/version", v1alpha1.METADATA_PREFIX)] = version.VERSION
	s.Updated = true
}

func (s *Skyhook) GetVersion() string {
	version, ok := s.Annotations[fmt.Sprintf("%s/version", v1alpha1.METADATA_PREFIX)]
	if !ok {
		return ""
	}
	return version
}

func (s *Skyhook) Migrate(logger logr.Logger) error {

	from := s.GetVersion()
	to := version.VERSION

	if from == to {
		return nil
	}

	if from == "" { // from before versioning... means v0.4.0
		if err := migrateSkyhookTo_0_5_0(s, logger); err != nil {
			return err
		}
		s.SetVersion()
	}

	return nil
}

// RemoveNodePriority removes a node from NodePriority and increments NodeOrderOffset.
// If the node is not in NodePriority, this is a no-op (offset is not bumped).
func (s *Skyhook) RemoveNodePriority(name string) {
	if s.Status.NodePriority == nil {
		return
	}
	if _, ok := s.Status.NodePriority[name]; !ok {
		return
	}
	delete(s.Status.NodePriority, name)
	s.Status.NodeOrderOffset++
	s.Updated = true
}

// NodeOrder returns the monotonic order for a node based on its position in
// NodePriority (sorted by timestamp, name tiebreaker) plus NodeOrderOffset.
// Returns 0 if the node is not found in NodePriority.
func (s *Skyhook) NodeOrder(nodeName string) int {
	if s.Status.NodePriority == nil {
		return 0
	}

	type entry struct {
		name string
		time metav1.Time
	}
	entries := make([]entry, 0, len(s.Status.NodePriority))
	for n, t := range s.Status.NodePriority {
		entries = append(entries, entry{n, t})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].time.Equal(&entries[j].time) {
			return entries[i].time.Before(&entries[j].time)
		}
		return entries[i].name < entries[j].name
	})
	for i, e := range entries {
		if e.name == nodeName {
			return s.Status.NodeOrderOffset + i
		}
	}
	return 0
}
