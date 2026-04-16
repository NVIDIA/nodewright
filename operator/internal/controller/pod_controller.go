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
	"context"
	"errors"
	"fmt"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// this is a sudo controller, it used to be one, but now are just functions of the skyhook controller
// the reason for this was to less issues around race conditions since they would be handled by one controller
// not sure that actually helped to be honest, but was the reason its acts like one.

// moved here for easier testing, vs being anonymous inline
func podHandlerFunc(ctx context.Context, o client.Object) []reconcile.Request {
	// logger := log.FromContext(ctx)

	pod := o.(*corev1.Pod)

	// if is skyhook package pod, then we care

	if labels.Set(pod.Labels).Has(fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX)) {
		// logger.Info("pod event", "name", o.GetName(), "labels", o.GetLabels(), "phase", pod.Status.Phase)

		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Name:      fmt.Sprintf("pod---%s", pod.Name), // adding prefix to help track pod events from other events
			Namespace: pod.Namespace,
		}}}
	}
	return nil
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.0/pkg/reconcile

func (r *SkyhookReconciler) PodReconcile(ctx context.Context, pod *corev1.Pod) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("pod-reconcile")

	// check if the package is invalid, if it is then delete the pod and return
	if invalid, err := r.HandleInvalidPackage(ctx, pod); invalid || err != nil {
		if err != nil {
			logger.Error(err, "error handling invalid package", "pod", pod.Name)
		}
		return ctrl.Result{}, err
	}

	containerName, state, restarts := containerExitedSuccessfully(pod)
	switch state {
	case containerStateSuccess:
		// only update node state once on success to mitigate race conditions. If
		// the pod has been marked for deletion then we know this has run once already
		if pod.DeletionTimestamp == nil {
			requeue, err := r.UpdateNodeState(ctx, pod, v1alpha1.StateComplete, containerName, restarts)
			if err != nil {
				logger.Error(err, "error updating node state", "pod", pod.Name)
				if requeue {
					return ctrl.Result{}, err
				}
			}

			// now delete pod
			err = r.Delete(ctx, pod)
			if err != nil {
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	case containerStateFailed:
		requeue, err := r.UpdateNodeState(ctx, pod, v1alpha1.StateErroring, containerName, restarts)
		if err != nil {
			logger.Error(err, "error updating node state", "pod", pod.Name)
			if requeue {
				return ctrl.Result{}, err
			}
		}
	default:
		// nothing to do
		// logger.Info("nothing to do yet", "state", state, "pod", pod.Name)
	}
	return ctrl.Result{}, nil
}

// HandleInvalidPackage deletes invalid packages
func (r *SkyhookReconciler) HandleInvalidPackage(ctx context.Context, pod *corev1.Pod) (bool, error) {
	invalid, err := IsInvalidPackage(pod)
	if err != nil {
		return false, err
	}

	if invalid {
		err := r.Delete(ctx, pod)
		if err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

// UpdateNodeState returns error and if to requeue, not all errors should be requeued, and some times there is no error but should be requeued
func (r *SkyhookReconciler) UpdateNodeState(ctx context.Context, pod *corev1.Pod, state v1alpha1.State, containerName string, restarts int32) (bool, error) {
	packagePtr, err := GetPackage(pod)
	if err != nil {
		return false, fmt.Errorf("error getting package from pod: %w", err)
	}

	if packagePtr == nil {
		return false, errors.New("there is no package, this pod is missing important info, or is labeled bad")
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, &node); err != nil {
		return true, err
	}

	patch := client.StrategicMergeFrom(node.DeepCopy())

	skyhookNode, err := wrapper.NewSkyhookNodeOnly(&node, packagePtr.Skyhook)
	if err != nil {
		return false, fmt.Errorf("error creating node wrapper: %w", err)
	}

	updated := false
	if state == v1alpha1.StateComplete {
		updated, err = r.HandleCompletePod(ctx, skyhookNode, packagePtr, containerName)
		if err != nil {
			return false, fmt.Errorf("error updating state for complete pod: %w", err)
		}
	}

	if !updated {
		err = skyhookNode.Upsert(packagePtr.PackageRef, packagePtr.Image, state, packagePtr.Stage, restarts, packagePtr.ContainerSHA)
		if err != nil {
			return false, err
		}
	}

	if v1alpha1.StateToStatus(state) == v1alpha1.StatusErroring {
		skyhookNode.SetStatus(v1alpha1.StateToStatus(state))
	}

	if skyhookNode.Changed() {
		if err := r.Patch(ctx, &node, patch); err != nil {
			return true, fmt.Errorf("error updating node with state from pod: %w", err)
		}

		r.recorder.Eventf(&node, EventTypeNormal, EventsReasonSkyhookApply, "Package [%s:%s] state %s on [skyhook:%s]", packagePtr.Name, packagePtr.Version, state, packagePtr.Skyhook)
	}

	return false, nil
}

// HandleCompletePod handles the complete pod, this is called when the pod has exited successfully
// and we need to update the node state to complete and handles special cases like interrupts, upgrades, and uninstalls
func (r *SkyhookReconciler) HandleCompletePod(ctx context.Context, skyhookNode wrapper.SkyhookNodeOnly, packagePtr *PackageSkyhook, containerName string) (bool, error) {
	updated := false

	if containerName == InterruptContainerName {
		// in this one case do we need a skyhook instance to get packages
		// kind of sucks, but does not update, just reads so that is better
		// seems safer to leave it this way unfortunately.
		// by passing in packages we can not update load packages
		skyhook, err := r.dal.GetSkyhook(ctx, packagePtr.Skyhook)
		if err != nil {
			return false, err
		}

		upgraded, err := wrapper.Convert(skyhookNode, skyhook)
		if err != nil {
			return false, fmt.Errorf("error converting node wrapper: %w", err)
		}

		// progress forward any skipped packages that this interrupt completed
		upgraded.ProgressSkipped()
	} else if packagePtr.Stage == v1alpha1.StageUpgrade {
		nodeState, err := skyhookNode.State()
		if err != nil {
			return false, fmt.Errorf("error getting node state: %w", err)
		}

		// go through and remove all the old node states for the package
		// after upgrade has finished
		for _, packageStatus := range nodeState {
			if packageStatus.Name == packagePtr.Name && packageStatus.Version != packagePtr.Version {
				packageStatusRef := v1alpha1.PackageRef{
					Name:    packageStatus.Name,
					Version: packageStatus.Version,
				}

				err = skyhookNode.RemoveState(packageStatusRef)
				if err != nil {
					return false, fmt.Errorf("error removing node state: %w", err)
				}
			}
		}
	} else if packagePtr.Stage == v1alpha1.StageUninstall {
		skyhook, err := r.dal.GetSkyhook(ctx, packagePtr.Skyhook)
		if err != nil {
			return false, err
		}

		if skyhook != nil {
			_package, exists := skyhook.Spec.Packages[packagePtr.Name]
			if exists && _package.Version == packagePtr.Version {
				// EXPLICIT or FINALIZER UNINSTALL: same version in spec as the
				// completing pod means this was an uninstall (not a downgrade).
				// Remove state — absent = uninstalled per D2.
				err = skyhookNode.RemoveState(packagePtr.PackageRef)
				if err != nil {
					return false, fmt.Errorf("error removing uninstalled package state: %w", err)
				}
				zeroOutSkyhookPackageMetrics(packagePtr.Skyhook, packagePtr.Name, packagePtr.Version)
				updated = true
			} else if exists {
				// DOWNGRADE: spec has a different (newer) version — seed the new
				// version forward and remove the old one (existing behavior).
				err = skyhookNode.Upsert(_package.PackageRef, _package.Image, v1alpha1.StateComplete, v1alpha1.StageUninstall, 0, _package.ContainerSHA)
				if err != nil {
					return false, fmt.Errorf("error updating node status: %w", err)
				}
				err = skyhookNode.RemoveState(packagePtr.PackageRef)
				if err != nil {
					return false, fmt.Errorf("error removing old package: %w", err)
				}
				updated = true
			} else {
				// Package removed from spec — just clean up
				err = skyhookNode.RemoveState(packagePtr.PackageRef)
				if err != nil {
					return false, fmt.Errorf("error removing old package: %w", err)
				}
				updated = true
			}
		}
	}

	return updated, nil
}

const (
	containerStateSuccess string = "Success"
	containerStateWaiting string = "Waiting"
	containerStateRunning string = "Running"
	containerStateFailed  string = "Failed"
)

func containerExitedSuccessfully(pod *corev1.Pod) (string, string, int32) {

	// can be either
	// apply and check
	// or just interrupt
	// need to check all passed or all failed

	checkStatus := func(status corev1.ContainerStatus) (string, int32) {
		if status.State.Terminated != nil {
			if status.State.Terminated.ExitCode == 0 {
				return containerStateSuccess, status.RestartCount
			}
			return containerStateFailed, status.RestartCount // TODO: is this always true? or should it be configuration?
		}
		if status.State.Running != nil {
			return containerStateRunning, status.RestartCount
		}
		if status.State.Waiting != nil {
			if status.State.Waiting.Reason == "CrashLoopBackOff" {
				return containerStateFailed, status.RestartCount
			}
			return containerStateWaiting, status.RestartCount
		}
		return "", int32(0)
	}

	state := ""
	restarts := int32(0)
	name := ""
	for _, status := range pod.Status.InitContainerStatuses {

		state, restarts = checkStatus(status)
		name = status.Name

		if state == containerStateFailed {
			return name, state, restarts
		}
	}

	return name, state, restarts
}
