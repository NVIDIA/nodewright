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

package node

import (
	"bytes"
	gocontext "context"
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	mockdynamic "github.com/NVIDIA/nodewright/operator/internal/mocks/dynamic"
)

var _ = Describe("Node Reset Command", func() {
	Describe("NewResetCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			Expect(cmd.Use).To(Equal("reset <node-name...>"))
			Expect(cmd.Short).To(ContainSubstring("Reset all package state"))
		})

		It("should require skyhook flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetArgs([]string{"worker-1"})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("skyhook"))
		})

		It("should require at least one node argument", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetArgs([]string{"--skyhook", "my-skyhook"})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires at least 1 arg"))
		})

		It("should have confirm flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			confirmFlag := cmd.Flags().Lookup("confirm")
			Expect(confirmFlag).NotTo(BeNil())
			Expect(confirmFlag.Shorthand).To(Equal("y"))
		})
	})

	Describe("runNodeReset", func() {
		var (
			output     *bytes.Buffer
			mockKube   *fake.Clientset
			kubeClient *client.Client
			cmd        *cobra.Command
			cliCtx     *context.CLIContext
		)

		BeforeEach(func() {
			output = &bytes.Buffer{}
			mockKube = fake.NewClientset()
			kubeClient = client.NewWithClientsAndConfig(mockKube, nil, nil)
			cliCtx = context.NewCLIContext(context.NewCLIConfig(context.WithOutputWriter(output)))

			cmd = &cobra.Command{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetIn(strings.NewReader("y\n")) // Default to 'yes' for prompts
		})

		It("should show no nodes when pattern doesn't match", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"nonexistent"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No nodes matched"))
		})

		It("should show no state when nodes don't have skyhook", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No nodes have state"))
		})

		It("should reset node state with confirmation", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + "my-skyhook"
			drainStartAnnotationKey := drainStartAnnotationPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey:           string(nodeStateJSON),
						drainStartAnnotationKey: "2026-06-02T12:00:00Z",
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			// Verify annotation was removed
			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode.Annotations[drainStartAnnotationKey]
			Expect(exists).To(BeFalse())
		})

		It("should reset drain metadata before node state exists", func() {
			drainStartAnnotationKey := drainStartAnnotationPrefix + "my-skyhook"
			statusAnnotationKey := statusAnnotationPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						drainStartAnnotationKey: "2026-06-02T12:00:00Z",
						statusAnnotationKey:     "erroring",
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[drainStartAnnotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode.Annotations[statusAnnotationKey]
			Expect(exists).To(BeFalse())
		})

		It("should reset a node with only a status label", func() {
			statusLabelKey := statusLabelPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Labels: map[string]string{
						statusLabelKey: "erroring",
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Labels[statusLabelKey]
			Expect(exists).To(BeFalse())
		})

		It("should reset a node with an invalid node state annotation", func() {
			annotationKey := nodeStateAnnotationPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey: "invalid-json",
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeFalse())
		})

		It("should respect dry-run flag", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey: string(nodeStateJSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			// Enable dry-run
			cliCtx.GlobalFlags.DryRun = true

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("[dry-run]"))

			// Verify annotation was NOT removed
			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeTrue())
		})

		It("should abort when user declines confirmation", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey: string(nodeStateJSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			// Set input to 'n' (decline)
			cmd.SetIn(strings.NewReader("n\n"))

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: false}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Aborted"))

			// Verify annotation was NOT removed
			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeTrue())
		})

		It("should reset multiple nodes", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + "my-skyhook"

			for _, name := range []string{"worker-1", "worker-2", "worker-3"} {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
						Annotations: map[string]string{
							annotationKey: string(nodeStateJSON),
						},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
			}

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err := runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1", "worker-2", "worker-3"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 3 node"))

			// Verify all annotations were removed
			for _, name := range []string{"worker-1", "worker-2", "worker-3"} {
				updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				_, exists := updatedNode.Annotations[annotationKey]
				Expect(exists).To(BeFalse())
			}
		})

		It("should print summary before reset", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
				"pkg2|1.0": {Name: "pkg2", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + "my-skyhook"

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey: string(nodeStateJSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &nodeResetOptions{skyhookName: "my-skyhook", confirm: true}
			err = runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("NodeWright: my-skyhook"))
			Expect(outputStr).To(ContainSubstring("Nodes to reset"))
			Expect(outputStr).To(ContainSubstring("worker-1"))
			Expect(outputStr).To(ContainSubstring("2 packages"))
		})
	})

	// Mirrors the NodeWright-wide reset: the operator only releases a cordon it holds an
	// annotation for, so stripping it while the NodeWright exists strands the node cordoned.
	Describe("cordon annotation", func() {
		var (
			fakeKube    *fake.Clientset
			mockDynamic *mockdynamic.Interface
			mockNSRes   *mockdynamic.NamespaceableResourceInterface
			kubeClient  *client.Client
			cliCtx      *context.CLIContext
			cmd         *cobra.Command
			out         *bytes.Buffer
		)
		const name = "demo"
		cordonKey := cordonAnnotationPrefix + name
		nodeStateKey := nodeStateAnnotationPrefix + name

		BeforeEach(func() {
			fakeKube = fake.NewClientset()
			mockDynamic = &mockdynamic.Interface{}
			mockNSRes = &mockdynamic.NamespaceableResourceInterface{}
			mockDynamic.On("Resource", schema.GroupVersionResource{
				Group: "nodewright.nvidia.com", Version: "v1alpha1", Resource: "nodewrights",
			}).Return(mockNSRes)
			kubeClient = client.NewWithClientsAndConfig(fakeKube, mockDynamic, nil)
			cliCtx = context.NewCLIContext(nil)
			out = &bytes.Buffer{}
			cmd = &cobra.Command{}
			cmd.SetOut(out)
			cmd.SetErr(out)

			n := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "worker-1",
					Annotations: map[string]string{nodeStateKey: "{}", cordonKey: "true"},
				},
				Spec: corev1.NodeSpec{Unschedulable: true},
			}
			_, err := fakeKube.CoreV1().Nodes().Create(gocontext.Background(), n, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		})

		annotationsAfter := func() map[string]string {
			got, err := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			return got.Annotations
		}

		It("keeps the cordon while the NodeWright exists so the re-run releases it", func() {
			u := &unstructured.Unstructured{}
			u.SetName(name)
			mockNSRes.On("Get", mock.Anything, name, mock.Anything, mock.Anything).Return(u, nil)

			opts := &nodeResetOptions{skyhookName: name, confirm: true}
			Expect(runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)).To(Succeed())

			Expect(annotationsAfter()).NotTo(HaveKey(nodeStateKey))
			Expect(annotationsAfter()).To(HaveKeyWithValue(cordonKey, "true"))
		})

		It("removes the cordon of a NodeWright that no longer exists", func() {
			mockNSRes.On("Get", mock.Anything, name, mock.Anything, mock.Anything).Return(
				(*unstructured.Unstructured)(nil),
				apierrors.NewNotFound(schema.GroupResource{Group: "nodewright.nvidia.com", Resource: "nodewrights"}, name),
			)

			opts := &nodeResetOptions{skyhookName: name, confirm: true}
			Expect(runNodeReset(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, opts, cliCtx)).To(Succeed())

			Expect(annotationsAfter()).NotTo(HaveKey(nodeStateKey))
			Expect(annotationsAfter()).NotTo(HaveKey(cordonKey))
		})
	})
})
