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

package node

import (
	"bytes"
	gocontext "context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

var _ = Describe("Node List Command", func() {
	Describe("NewListCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewListCmd(ctx)

			Expect(cmd.Use).To(Equal("list"))
			Expect(cmd.Short).To(ContainSubstring("List all nodes"))
		})

		It("should require skyhook flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewListCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetArgs([]string{})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("skyhook"))
		})

	})

	Describe("outputNodeListTableOrWide", func() {
		It("should output table with summary", func() {
			entries := []nodeListEntry{
				{NodeName: "node1", Status: "complete", PackagesComplete: 3, PackagesTotal: 3},
				{NodeName: "node2", Status: "erroring", PackagesComplete: 1, PackagesTotal: 3},
				{NodeName: "node3", Status: "in_progress", PackagesComplete: 2, PackagesTotal: 3},
			}
			output := &bytes.Buffer{}

			err := outputNodeListTableOrWide(output, "my-skyhook", entries, false)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("Skyhook: my-skyhook"))
			Expect(outputStr).To(ContainSubstring("Summary: 3 nodes"))
			Expect(outputStr).To(ContainSubstring("1 complete"))
			Expect(outputStr).To(ContainSubstring("1 erroring"))
			Expect(outputStr).To(ContainSubstring("NODE"))
			Expect(outputStr).To(ContainSubstring("STATUS"))
			Expect(outputStr).To(ContainSubstring("PACKAGES"))
		})

		It("should uppercase ERROR status", func() {
			entries := []nodeListEntry{
				{NodeName: "node1", Status: "erroring", PackagesComplete: 1, PackagesTotal: 3},
			}
			output := &bytes.Buffer{}

			err := outputNodeListTableOrWide(output, "my-skyhook", entries, false)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("ERROR"))
		})

		It("should show RESTARTS in wide output", func() {
			entries := []nodeListEntry{
				{NodeName: "node1", Status: "complete", PackagesComplete: 3, PackagesTotal: 3, Restarts: 5},
			}
			output := &bytes.Buffer{}

			err := outputNodeListTableOrWide(output, "my-skyhook", entries, true)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("RESTARTS"))
			Expect(outputStr).To(ContainSubstring("5"))
		})
	})

	Describe("runNodeList", func() {
		var (
			output     *bytes.Buffer
			mockKube   *fake.Clientset
			kubeClient *client.Client
			cliCtx     *context.CLIContext
		)

		BeforeEach(func() {
			output = &bytes.Buffer{}
			mockKube = fake.NewClientset()
			kubeClient = client.NewWithClientsAndConfig(mockKube, nil, nil)
			cliCtx = context.NewCLIContext(context.NewCLIConfig(context.WithOutputWriter(output)))
		})

		It("should show no nodes when none have the skyhook", func() {
			// Create node without Skyhook annotations
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeListOptions{skyhookName: "my-skyhook"}
			err = runNodeList(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No nodes found"))
		})

		It("should list nodes with the specified skyhook", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			for _, name := range []string{"worker-1", "worker-2"} {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
						Annotations: map[string]string{
							nodeStateAnnotationPrefix + "my-skyhook": string(nodeStateJSON),
						},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
			}

			opts := &nodeListOptions{skyhookName: "my-skyhook"}
			err := runNodeList(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("worker-1"))
			Expect(outputStr).To(ContainSubstring("worker-2"))
			Expect(outputStr).To(ContainSubstring("2 nodes"))
		})

		It("should only list nodes with matching skyhook", func() {
			nodeState1 := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeState1JSON, _ := json.Marshal(nodeState1)

			nodeState2 := v1alpha1.NodeState{
				"pkg2|1.0": {Name: "pkg2", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeState2JSON, _ := json.Marshal(nodeState2)

			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						nodeStateAnnotationPrefix + "skyhook-a": string(nodeState1JSON),
					},
				},
			}
			node2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-2",
					Annotations: map[string]string{
						nodeStateAnnotationPrefix + "skyhook-b": string(nodeState2JSON),
					},
				},
			}

			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node1, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, err = mockKube.CoreV1().Nodes().Create(gocontext.Background(), node2, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeListOptions{skyhookName: "skyhook-a"}
			err = runNodeList(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("worker-1"))
			Expect(outputStr).NotTo(ContainSubstring("worker-2"))
		})

		It("should output JSON format", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						nodeStateAnnotationPrefix + "my-skyhook": string(nodeStateJSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeListOptions{skyhookName: "my-skyhook"}
			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatJSON
			err = runNodeList(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			var result struct {
				SkyhookName string          `json:"skyhookName"`
				Nodes       []nodeListEntry `json:"nodes"`
			}
			err = json.Unmarshal(output.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.SkyhookName).To(Equal("my-skyhook"))
			Expect(result.Nodes).To(HaveLen(1))
		})

		It("should calculate status correctly", func() {
			completeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", State: v1alpha1.StateComplete},
			}
			completeJSON, _ := json.Marshal(completeState)

			errorState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", State: v1alpha1.StateErroring},
			}
			errorJSON, _ := json.Marshal(errorState)

			inProgressState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", State: v1alpha1.StateInProgress},
			}
			inProgressJSON, _ := json.Marshal(inProgressState)

			nodes := []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "node-complete",
						Annotations: map[string]string{nodeStateAnnotationPrefix + "my-skyhook": string(completeJSON)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "node-error",
						Annotations: map[string]string{nodeStateAnnotationPrefix + "my-skyhook": string(errorJSON)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "node-inprogress",
						Annotations: map[string]string{nodeStateAnnotationPrefix + "my-skyhook": string(inProgressJSON)},
					},
				},
			}

			for _, n := range nodes {
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), n, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
			}

			opts := &nodeListOptions{skyhookName: "my-skyhook"}
			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatJSON
			err := runNodeList(gocontext.Background(), kubeClient, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			var result struct {
				Nodes []nodeListEntry `json:"nodes"`
			}
			err = json.Unmarshal(output.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())

			statusMap := make(map[string]string)
			for _, n := range result.Nodes {
				statusMap[n.NodeName] = n.Status
			}
			Expect(statusMap["node-complete"]).To(Equal("complete"))
			Expect(statusMap["node-error"]).To(Equal("erroring"))
			Expect(statusMap["node-inprogress"]).To(Equal("in_progress"))
		})
	})
})
