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

package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// TestNewVersionCmd_ClientOnly verifies the version command works with --client-only flag
func TestNewVersionCmd_ClientOnly(t *testing.T) {
	ctx := cliContext.NewCLIContext(nil)
	cmd := NewVersionCmd(ctx)

	// Set client-only flag
	if err := cmd.Flags().Set("client-only", "true"); err != nil {
		t.Fatalf("failed to set client-only flag: %v", err)
	}

	// Capture output
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	// Set command args to avoid test flags being parsed
	cmd.SetArgs([]string{})

	// Execute command
	if err := cmd.Execute(); err != nil {
		t.Fatalf("command execution failed: %v", err)
	}

	output := buf.String()

	// Should contain plugin version
	if !strings.Contains(output, "Skyhook plugin:") {
		t.Error("output should contain 'Skyhook plugin:'")
	}

	// Should NOT contain operator version (client-only mode)
	if strings.Contains(output, "Skyhook operator:") {
		t.Error("output should not contain 'Skyhook operator:' in client-only mode")
	}
}

// TestDiscoverOperatorVersion tests the operator version discovery logic
func TestDiscoverOperatorVersion(t *testing.T) {
	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		wantErr    bool
		wantVer    string
	}{
		{
			name:    "no deployment",
			wantErr: true,
		},
		{
			name: "version from label (Helm deployment)",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skyhook-operator-controller-manager",
					Namespace: "skyhook",
					Labels: map[string]string{
						"app.kubernetes.io/version": "v0.9.0",
						"app.kubernetes.io/name":    "skyhook-operator", // needed for detection
					},
				},
			},
			wantErr: false,
			wantVer: "v0.9.0",
		},
		{
			name: "version from image tag (kustomize deployment)",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skyhook-operator-controller-manager",
					Namespace: "skyhook",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "ghcr.io/nvidia/skyhook/operator:v1.2.3",
								},
							},
						},
					},
				},
			},
			wantErr: false,
			wantVer: "v1.2.3",
		},
		{
			name: "version from image tag with digest",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skyhook-operator-controller-manager",
					Namespace: "skyhook",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "ghcr.io/nvidia/skyhook/operator:v2.0.0@sha256:abc123",
								},
							},
						},
					},
				},
			},
			wantErr: false,
			wantVer: "v2.0.0",
		},
		{
			name: "missing version label and no tag",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skyhook-operator-controller-manager",
					Namespace: "skyhook",
					Labels: map[string]string{
						"app.kubernetes.io/name": "skyhook-operator", // needed for detection
					},
				},
			},
			wantErr: true,
		},
		{
			name: "detection by image name (no skyhook label)",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-named-deployment",
					Namespace: "skyhook",
					Labels: map[string]string{
						"app.kubernetes.io/version": "v1.5.0",
						"app":                       "my-operator", // no skyhook in labels
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "nvcr.io/nvidia/skyhook/operator:v1.5.0", // detected by image
								},
							},
						},
					},
				},
			},
			wantErr: false,
			wantVer: "v1.5.0",
		},
		{
			name: "latest tag should fail",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skyhook-operator-controller-manager",
					Namespace: "skyhook",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "ghcr.io/nvidia/skyhook/operator:latest",
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := fake.NewClientset()

			if tt.deployment != nil {
				_, err := clientset.AppsV1().Deployments("skyhook").Create(
					context.Background(),
					tt.deployment,
					metav1.CreateOptions{},
				)
				if err != nil {
					t.Fatalf("failed to create test deployment: %v", err)
				}
			}

			version, err := utils.DiscoverOperatorVersion(context.Background(), clientset, "skyhook")

			if (err != nil) != tt.wantErr {
				t.Errorf("discoverOperatorVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && version != tt.wantVer {
				t.Errorf("discoverOperatorVersion() = %v, want %v", version, tt.wantVer)
			}
		})
	}
}

// TestExtractImageTag tests the image tag extraction logic
func TestExtractImageTag(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "image with tag",
			image: "ghcr.io/nvidia/skyhook/operator:v1.2.3",
			want:  "v1.2.3",
		},
		{
			name:  "image with tag and digest",
			image: "ghcr.io/nvidia/skyhook/operator:v1.2.3@sha256:abc123def456",
			want:  "v1.2.3",
		},
		{
			name:  "image without tag",
			image: "ghcr.io/nvidia/skyhook/operator",
			want:  "",
		},
		{
			name:  "latest tag",
			image: "ghcr.io/nvidia/skyhook/operator:latest",
			want:  "latest",
		},
		{
			name:  "tag with build metadata",
			image: "example.com/app:v2.0.0-rc1+build.123",
			want:  "v2.0.0-rc1+build.123",
		},
		{
			name:  "empty string",
			image: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utils.ExtractImageTag(tt.image)
			if got != tt.want {
				t.Errorf("extractImageTag(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}
