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

package dal

import (
	"context"
	"fmt"

	skyhookv1alpha1 "github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func New(c client.Client) DAL {
	return &dal{client: c}
}

// DAL gives a typed interface to the kubernetes interface which is generic ano not typed
// I find this to be more readable and using the generated mock is easier too
// get and list are hard to mock, update is not an issue, but might as well live here too
type DAL interface {
	GetSkyhook(ctx context.Context, name string, opts ...client.ListOption) (*skyhookv1alpha1.Skyhook, error)
	GetSkyhooks(ctx context.Context, opts ...client.ListOption) (*skyhookv1alpha1.SkyhookList, error)
	GetNode(ctx context.Context, nodeName string) (*corev1.Node, error)
	GetNodes(ctx context.Context, opts ...client.ListOption) (*corev1.NodeList, error)
	GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error)
	GetPods(ctx context.Context, opts ...client.ListOption) (*corev1.PodList, error)
	GetDeploymentPolicies(ctx context.Context, opts ...client.ListOption) (*skyhookv1alpha1.DeploymentPolicyList, error)
	GetDeploymentPolicy(ctx context.Context, name string) (*skyhookv1alpha1.DeploymentPolicy, error)
}

type dal struct {
	client client.Client
}

func (e *dal) GetSkyhook(ctx context.Context, name string, opts ...client.ListOption) (*skyhookv1alpha1.Skyhook, error) {
	var skyhook skyhookv1alpha1.Skyhook

	// nodes does have namespace so leaving blank
	if err := e.client.Get(ctx, types.NamespacedName{Name: name}, &skyhook); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting skyhook [%s]: %w", name, err)
	}

	return &skyhook, nil
}

func (e *dal) GetSkyhooks(ctx context.Context, opts ...client.ListOption) (*skyhookv1alpha1.SkyhookList, error) {

	var skyhook skyhookv1alpha1.SkyhookList
	if err := e.client.List(ctx, &skyhook, opts...); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting skyhooks: %w", err)
	}

	if len(skyhook.Items) == 0 {
		return nil, nil
	}

	return &skyhook, nil
}

func (e *dal) GetNode(ctx context.Context, nodeName string) (*corev1.Node, error) {
	var node corev1.Node

	// nodes does have namespace so leaving blank
	if err := e.client.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting node [%s]: %w", nodeName, err)
	}

	return &node, nil
}

func (e *dal) GetNodes(ctx context.Context, opts ...client.ListOption) (*corev1.NodeList, error) {
	var nodes corev1.NodeList
	if err := e.client.List(ctx, &nodes, opts...); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return nil, nil
	}

	return &nodes, nil
}

func (e *dal) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	var pod corev1.Pod

	if err := e.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting pod [%s|%s]: %w", namespace, name, err)
	}

	return &pod, nil
}

func (e *dal) GetPods(ctx context.Context, opts ...client.ListOption) (*corev1.PodList, error) {
	var pods corev1.PodList
	if err := e.client.List(ctx, &pods, opts...); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, nil
	}

	return &pods, nil
}

func (e *dal) GetDeploymentPolicies(ctx context.Context, opts ...client.ListOption) (*skyhookv1alpha1.DeploymentPolicyList, error) {
	var policies skyhookv1alpha1.DeploymentPolicyList
	if err := e.client.List(ctx, &policies, opts...); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting deployment policies: %w", err)
	}

	return &policies, nil
}

func (e *dal) GetDeploymentPolicy(ctx context.Context, name string) (*skyhookv1alpha1.DeploymentPolicy, error) {
	var policy skyhookv1alpha1.DeploymentPolicy

	// DeploymentPolicy is cluster-scoped, so no namespace is needed
	if err := e.client.Get(ctx, types.NamespacedName{Name: name}, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error getting deployment policy [%s]: %w", name, err)
	}

	return &policy, nil
}
