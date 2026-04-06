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

package pkg

import (
	"bytes"
	gocontext "context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
	mockdynamic "github.com/NVIDIA/nodewright/operator/internal/mocks/dynamic"
)

const testSkyhookName = "my-skyhook"

var _ = Describe("Package Status Command", func() {
	Describe("formatPackageList", func() {
		It("should format single package", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"pkg1": {PackageRef: v1alpha1.PackageRef{Version: "1.0.0"}},
					},
				},
			}
			result := formatPackageList(skyhook)
			Expect(result).To(Equal("pkg1:1.0.0"))
		})

		It("should format multiple packages sorted alphabetically", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"zebra": {PackageRef: v1alpha1.PackageRef{Version: "1.0"}},
						"alpha": {PackageRef: v1alpha1.PackageRef{Version: "2.0"}},
						"beta":  {PackageRef: v1alpha1.PackageRef{Version: "3.0"}},
					},
				},
			}
			result := formatPackageList(skyhook)
			Expect(result).To(Equal("alpha:2.0, beta:3.0, zebra:1.0"))
		})

		It("should handle empty packages", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{},
				},
			}
			result := formatPackageList(skyhook)
			Expect(result).To(BeEmpty())
		})
	})

	Describe("outputPackageStatusTableOrWide", func() {
		It("should output table format with headers", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"pkg1": {PackageRef: v1alpha1.PackageRef{Version: "1.0"}},
					},
				},
			}
			skyhook.Name = testSkyhookName

			statuses := []nodePackageStatus{
				newNodePackageStatus("node1", v1alpha1.PackageStatus{
					Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete,
				}),
			}
			output := &bytes.Buffer{}

			err := outputPackageStatusTableOrWide(output, skyhook, statuses, false)
			Expect(err).NotTo(HaveOccurred())

			result := output.String()
			Expect(result).To(ContainSubstring("Skyhook: my-skyhook"))
			Expect(result).To(ContainSubstring("NODE"))
			Expect(result).To(ContainSubstring("PACKAGE"))
			Expect(result).To(ContainSubstring("VERSION"))
			Expect(result).To(ContainSubstring("STAGE"))
			Expect(result).To(ContainSubstring("STATE"))
			Expect(result).To(ContainSubstring("node1"))
			Expect(result).To(ContainSubstring("pkg1"))
		})

		It("should include packages header", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"pkg1": {PackageRef: v1alpha1.PackageRef{Version: "1.0"}},
						"pkg2": {PackageRef: v1alpha1.PackageRef{Version: "2.0"}},
					},
				},
			}
			skyhook.Name = testSkyhookName

			output := &bytes.Buffer{}
			err := outputPackageStatusTableOrWide(output, skyhook, []nodePackageStatus{}, false)
			Expect(err).NotTo(HaveOccurred())

			Expect(output.String()).To(ContainSubstring("Packages:"))
		})

		It("should include RESTARTS and IMAGE columns in wide output", func() {
			skyhook := &v1alpha1.Skyhook{
				Spec: v1alpha1.SkyhookSpec{
					Packages: map[string]v1alpha1.Package{
						"pkg1": {PackageRef: v1alpha1.PackageRef{Version: "1.0"}},
					},
				},
			}
			skyhook.Name = testSkyhookName

			statuses := []nodePackageStatus{
				newNodePackageStatus("node1", v1alpha1.PackageStatus{
					Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply,
					State: v1alpha1.StateComplete, Restarts: 3, Image: "nginx:latest",
				}),
			}
			output := &bytes.Buffer{}

			err := outputPackageStatusTableOrWide(output, skyhook, statuses, true)
			Expect(err).NotTo(HaveOccurred())

			result := output.String()
			Expect(result).To(ContainSubstring("RESTARTS"))
			Expect(result).To(ContainSubstring("IMAGE"))
			Expect(result).To(ContainSubstring("3"))
			Expect(result).To(ContainSubstring("nginx:latest"))
		})
	})

	Describe("runStatus", func() {
		var (
			output      *bytes.Buffer
			fakeKube    *fake.Clientset
			mockDynamic *mockdynamic.Interface
			mockNSRes   *mockdynamic.NamespaceableResourceInterface
			kubeClient  *client.Client
			cliCtx      *context.CLIContext
		)

		BeforeEach(func() {
			output = &bytes.Buffer{}
			fakeKube = fake.NewClientset()
			mockDynamic = &mockdynamic.Interface{}
			mockNSRes = &mockdynamic.NamespaceableResourceInterface{}
			kubeClient = client.NewWithClientsAndConfig(fakeKube, mockDynamic, nil)
			cliCtx = context.NewCLIContext(context.NewCLIConfig(context.WithOutputWriter(output)))
		})

		createSkyhookUnstructured := func() *unstructured.Unstructured {
			return &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "skyhook.nvidia.com/v1alpha1",
					"kind":       "Skyhook",
					"metadata": map[string]interface{}{
						"name": testSkyhookName,
					},
					"spec": map[string]interface{}{
						"packages": map[string]interface{}{
							"pkg1": map[string]interface{}{
								"version": "1.0.0",
							},
						},
					},
				},
			}
		}

		It("should return status for nodes with annotations", func() {
			// Setup mock
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			// Create node with annotation
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0.0": {Name: "pkg1", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"skyhook.nvidia.com/nodeState_my-skyhook": string(nodeStateJSON),
					},
				},
			}
			_, err := fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			err = runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("node1"))
			Expect(output.String()).To(ContainSubstring("pkg1"))
		})

		It("should output JSON format", func() {
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			nodeState := v1alpha1.NodeState{
				"pkg1|1.0.0": {Name: "pkg1", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"skyhook.nvidia.com/nodeState_my-skyhook": string(nodeStateJSON),
					},
				},
			}
			_, _ = fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatJSON
			err := runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			var result []nodePackageStatus
			err = json.Unmarshal(output.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[0].NodeName).To(Equal("node1"))
		})

		It("should show message when no status found", func() {
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			// Node without annotation
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
				},
			}
			_, _ = fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			err := runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No package status found"))
		})

		It("should skip nodes with invalid JSON annotations", func() {
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			// Node with invalid JSON
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"skyhook.nvidia.com/nodeState_my-skyhook": "invalid-json",
					},
				},
			}
			_, _ = fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			err := runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No package status found"))
		})

		It("should filter by package name", func() {
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			nodeState := v1alpha1.NodeState{
				"pkg1|1.0.0": {Name: "pkg1", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
				"pkg2|2.0.0": {Name: "pkg2", Version: "2.0.0", Stage: v1alpha1.StageConfig, State: v1alpha1.StateInProgress},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"skyhook.nvidia.com/nodeState_my-skyhook": string(nodeStateJSON),
					},
				},
			}
			_, _ = fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			err := runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("pkg1"))
			Expect(output.String()).NotTo(ContainSubstring("pkg2"))
		})

		It("should output wide format with image", func() {
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			nodeState := v1alpha1.NodeState{
				"pkg1|1.0.0": {Name: "pkg1", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete, Image: "nginx:latest"},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"skyhook.nvidia.com/nodeState_my-skyhook": string(nodeStateJSON),
					},
				},
			}
			_, _ = fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatWide
			err := runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("IMAGE"))
			Expect(output.String()).To(ContainSubstring("nginx:latest"))
		})

		It("should sort results by node name", func() {
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, testSkyhookName, mock.Anything).Return(createSkyhookUnstructured(), nil)

			nodeState := v1alpha1.NodeState{
				"pkg1|1.0.0": {Name: "pkg1", Version: "1.0.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			// Create nodes in reverse order
			for _, name := range []string{"node-z", "node-a"} {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
						Annotations: map[string]string{
							"skyhook.nvidia.com/nodeState_my-skyhook": string(nodeStateJSON),
						},
					},
				}
				_, _ = fakeKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			}

			opts := &statusOptions{
				skyhookName: testSkyhookName,
				packageName: "pkg1",
			}

			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatJSON
			err := runStatus(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			var result []nodePackageStatus
			err = json.Unmarshal(output.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(2))
			Expect(result[0].NodeName).To(Equal("node-a"))
			Expect(result[1].NodeName).To(Equal("node-z"))
		})
	})

	Describe("NewStatusCmd", func() {
		var statusCmd *cobra.Command

		BeforeEach(func() {
			testCtx := context.NewCLIContext(nil)
			statusCmd = NewStatusCmd(testCtx)
		})

		It("should require --skyhook flag", func() {
			statusCmd.SetArgs([]string{"pkg1"})
			err := statusCmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("skyhook"))
		})

		It("should require exactly one argument", func() {
			freshCtx := context.NewCLIContext(nil)
			freshCmd := NewStatusCmd(freshCtx)
			freshCmd.SetArgs([]string{"--skyhook", "test"})
			err := freshCmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("accepts 1 arg"))
		})

		It("should have node flag", func() {
			nodeFlag := statusCmd.Flags().Lookup("node")
			Expect(nodeFlag).NotTo(BeNil())
		})

		It("should have correct command metadata", func() {
			Expect(statusCmd.Use).To(Equal("status <package-name>"))
			Expect(statusCmd.Short).To(ContainSubstring("Query package status"))
		})
	})
})
