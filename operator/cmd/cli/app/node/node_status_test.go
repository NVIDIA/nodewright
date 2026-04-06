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

var _ = Describe("Node Status Command", func() {
	Describe("NewStatusCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewStatusCmd(ctx)

			Expect(cmd.Use).To(Equal("status [node-name...]"))
			Expect(cmd.Short).To(ContainSubstring("Skyhook activity"))
		})

		It("should have skyhook flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewStatusCmd(ctx)

			skyhookFlag := cmd.Flags().Lookup("skyhook")
			Expect(skyhookFlag).NotTo(BeNil())
			Expect(skyhookFlag.Usage).To(ContainSubstring("Filter by Skyhook"))
		})
	})

	Describe("outputNodeStatusTable", func() {
		It("should output table with headers", func() {
			summaries := []nodeSkyhookSummary{
				{NodeName: "node1", SkyhookName: "skyhook1", Status: "complete", PackagesComplete: 3, PackagesTotal: 3},
			}
			output := &bytes.Buffer{}

			err := outputNodeStatusTable(output, summaries)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("NODE"))
			Expect(outputStr).To(ContainSubstring("SKYHOOK"))
			Expect(outputStr).To(ContainSubstring("STATUS"))
			Expect(outputStr).To(ContainSubstring("PACKAGES"))
			Expect(outputStr).To(ContainSubstring("node1"))
			Expect(outputStr).To(ContainSubstring("skyhook1"))
			Expect(outputStr).To(ContainSubstring("complete"))
		})

		It("should show multiple nodes", func() {
			summaries := []nodeSkyhookSummary{
				{NodeName: "node1", SkyhookName: "skyhook1", Status: "complete", PackagesComplete: 3, PackagesTotal: 3},
				{NodeName: "node2", SkyhookName: "skyhook1", Status: "in_progress", PackagesComplete: 1, PackagesTotal: 3},
			}
			output := &bytes.Buffer{}

			err := outputNodeStatusTable(output, summaries)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("node1"))
			Expect(outputStr).To(ContainSubstring("node2"))
		})
	})

	Describe("outputNodeStatusWide", func() {
		It("should include package details", func() {
			summaries := []nodeSkyhookSummary{
				{
					NodeName:    "node1",
					SkyhookName: "skyhook1",
					Status:      "complete",
					Packages: []nodeSkyhookPkgStatus{
						{Name: "pkg1", Version: "1.0", Stage: "apply", State: "complete", Restarts: 0, Image: "img:1.0"},
					},
				},
			}
			output := &bytes.Buffer{}

			err := outputNodeStatusWide(output, summaries)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("PACKAGE"))
			Expect(outputStr).To(ContainSubstring("VERSION"))
			Expect(outputStr).To(ContainSubstring("STAGE"))
			Expect(outputStr).To(ContainSubstring("STATE"))
			Expect(outputStr).To(ContainSubstring("RESTARTS"))
			Expect(outputStr).To(ContainSubstring("IMAGE"))
			Expect(outputStr).To(ContainSubstring("pkg1"))
			Expect(outputStr).To(ContainSubstring("img:1.0"))
		})

		It("should show restarts count", func() {
			summaries := []nodeSkyhookSummary{
				{
					NodeName:    "node1",
					SkyhookName: "skyhook1",
					Status:      "erroring",
					Packages: []nodeSkyhookPkgStatus{
						{Name: "pkg1", Version: "1.0", Stage: "apply", State: "erroring", Restarts: 5, Image: "img:1.0"},
					},
				},
			}
			output := &bytes.Buffer{}

			err := outputNodeStatusWide(output, summaries)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("5"))
		})
	})

	Describe("runNodeStatus", func() {
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

		It("should show no activity when node has no annotations", func() {
			// Create node without Skyhook annotations
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeStatusOptions{}
			err = runNodeStatus(gocontext.Background(), kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No Skyhook activity found"))
		})

		It("should show status for node with Skyhook annotations", func() {
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

			opts := &nodeStatusOptions{}
			err = runNodeStatus(gocontext.Background(), kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("worker-1"))
			Expect(outputStr).To(ContainSubstring("my-skyhook"))
			Expect(outputStr).To(ContainSubstring("complete"))
		})

		It("should filter by skyhook name", func() {
			nodeState1 := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeState1JSON, _ := json.Marshal(nodeState1)

			nodeState2 := v1alpha1.NodeState{
				"pkg2|1.0": {Name: "pkg2", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateInProgress},
			}
			nodeState2JSON, _ := json.Marshal(nodeState2)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						nodeStateAnnotationPrefix + "skyhook-a": string(nodeState1JSON),
						nodeStateAnnotationPrefix + "skyhook-b": string(nodeState2JSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeStatusOptions{skyhookName: "skyhook-a"}
			err = runNodeStatus(gocontext.Background(), kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("skyhook-a"))
			Expect(outputStr).NotTo(ContainSubstring("skyhook-b"))
		})

		It("should show all nodes when no pattern specified", func() {
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

			opts := &nodeStatusOptions{}
			err := runNodeStatus(gocontext.Background(), kubeClient, []string{}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("worker-1"))
			Expect(outputStr).To(ContainSubstring("worker-2"))
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

			opts := &nodeStatusOptions{}
			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatJSON
			err = runNodeStatus(gocontext.Background(), kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			var result []nodeSkyhookSummary
			err = json.Unmarshal(output.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
		})

		It("should calculate status correctly", func() {
			// Test complete status
			completeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
				"pkg2|1.0": {Name: "pkg2", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			completeJSON, _ := json.Marshal(completeState)

			// Test error status
			errorState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
				"pkg2|1.0": {Name: "pkg2", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateErroring},
			}
			errorJSON, _ := json.Marshal(errorState)

			// Test in_progress status
			inProgressState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
				"pkg2|1.0": {Name: "pkg2", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateInProgress},
			}
			inProgressJSON, _ := json.Marshal(inProgressState)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						nodeStateAnnotationPrefix + "complete-skyhook":   string(completeJSON),
						nodeStateAnnotationPrefix + "error-skyhook":      string(errorJSON),
						nodeStateAnnotationPrefix + "inprogress-skyhook": string(inProgressJSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeStatusOptions{}
			cliCtx.GlobalFlags.OutputFormat = utils.OutputFormatJSON
			err = runNodeStatus(gocontext.Background(), kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			var result []nodeSkyhookSummary
			err = json.Unmarshal(output.Bytes(), &result)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(3))

			// Find each status
			statusMap := make(map[string]string)
			for _, r := range result {
				statusMap[r.SkyhookName] = r.Status
			}
			Expect(statusMap["complete-skyhook"]).To(Equal("complete"))
			Expect(statusMap["error-skyhook"]).To(Equal("erroring"))
			Expect(statusMap["inprogress-skyhook"]).To(Equal("in_progress"))
		})
	})
})
