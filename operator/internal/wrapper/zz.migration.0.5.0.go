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

package wrapper

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/go-logr/logr"
)

func migrateNodeTo_0_5_0(node *skyhookNode, logger logr.Logger) error {
	nodeState, err := node.State()
	if err != nil {
		return err
	}
	for _, packageStatus := range nodeState {
		packageStatusRef := v1alpha1.PackageRef{
			Name:    packageStatus.Name,
			Version: packageStatus.Version,
		}

		if packageStatus.Image == "" {
			_package, exists := node.skyhook.Spec.Packages[packageStatus.Name]
			if exists && packageStatus.Version == _package.Version {

				// upsert to migrate
				err := node.Upsert(packageStatusRef, _package.Image, packageStatus.State, packageStatus.Stage, packageStatus.Restarts, "")
				if err != nil {
					return err
				}

				delete(node.nodeState, _package.Name) // remove old state, its not valid anymore
				if err := node.SetState(node.nodeState); err != nil {
					return err
				}
				node.skyhook.SetNodeState(node.Node.Name, node.nodeState)
				node.updated = true
			} else {
				logger.Info("node state %s for package %s:%s on %s removed: unable to resolve image", node.Name, packageStatus.Name, packageStatus.Version, node.skyhook.Name)

				updated := false

				// check if the package is versioned
				if node.nodeState.Get(packageStatusRef.GetUniqueName()) != nil {
					delete(node.nodeState, packageStatusRef.GetUniqueName()) // remove old state, for versioned package
					updated = true
				}

				// in previous versions, the package name was not versioned, so we need to remove the old state for that
				if node.nodeState.Get(_package.Name) != nil {
					delete(node.nodeState, _package.Name) // remove old state, for none versioned package
					updated = true
				}

				// update the node state if we removed any old state
				if updated {
					if err := node.SetState(node.nodeState); err != nil {
						return err
					}
					node.skyhook.SetNodeState(node.Node.Name, node.nodeState)
					node.updated = true
				}
			}
		}
	}

	for idx, cond := range node.GetNode().Status.Conditions {
		// if the condition type does not match the expected type, remove it
		_type := string(cond.Type)
		if strings.HasPrefix(_type, fmt.Sprintf("%s/%s/", v1alpha1.METADATA_PREFIX, node.skyhookName)) {
			lastpart := strings.Split(_type, "/")[2]
			switch lastpart {
			case "Erroring", "NotReady":
				continue
			default:
				// remove these, not valid anymore
				node.GetNode().Status.Conditions = append(node.GetNode().Status.Conditions[:idx], node.GetNode().Status.Conditions[idx+1:]...)
			}
		}
	}

	return nil
}

//nolint:unparam
func migrateSkyhookTo_0_5_0(skyhook *Skyhook, logger logr.Logger) error {

	// nothing to do here for this version, but is part of the migration pattern
	return nil
}
