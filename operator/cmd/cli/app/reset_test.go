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

package app

import (
	"bytes"
	gocontext "context"
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
)

// Helper function to create a test node with nodeState annotation
func createTestNodeWithState(kube *fake.Clientset, nodeName, skyhookName string) (string, error) {
	nodeState := v1alpha1.NodeState{
		"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
	}
	nodeStateJSON, _ := json.Marshal(nodeState)
	annotationKey := nodeStateAnnotationPrefix + skyhookName

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				annotationKey: string(nodeStateJSON),
			},
		},
	}
	_, err := kube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return annotationKey, nil
}

// Helper function to verify annotation was removed
func verifyAnnotationRemoved(kube *fake.Clientset, nodeName, annotationKey string) error {
	updatedNode, err := kube.CoreV1().Nodes().Get(gocontext.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_, exists := updatedNode.Annotations[annotationKey]
	if exists {
		return fmt.Errorf("annotation %s still exists on node %s", annotationKey, nodeName)
	}
	return nil
}

var _ = Describe("Reset Command", func() {
	Describe("NewResetCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			Expect(cmd.Use).To(Equal("reset <skyhook-name>"))
			Expect(cmd.Short).To(ContainSubstring("Reset all nodes"))
		})

		It("should require exactly one argument", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			err := cmd.Args(cmd, []string{})
			Expect(err).To(HaveOccurred())

			err = cmd.Args(cmd, []string{"skyhook1", "skyhook2"})
			Expect(err).To(HaveOccurred())
		})

		It("should have confirm flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			confirmFlag := cmd.Flags().Lookup("confirm")
			Expect(confirmFlag).NotTo(BeNil())
			Expect(confirmFlag.Shorthand).To(Equal("y"))
		})

		It("should have skip-batch-reset flag", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			skipBatchResetFlag := cmd.Flags().Lookup("skip-batch-reset")
			Expect(skipBatchResetFlag).NotTo(BeNil())
			Expect(skipBatchResetFlag.DefValue).To(Equal("false"))
		})

		It("should mention batch reset in help text", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewResetCmd(ctx)

			Expect(cmd.Long).To(ContainSubstring("batch state"))
			Expect(cmd.Long).To(ContainSubstring("--skip-batch-reset"))
		})
	})

	Describe("runReset", func() {
		var (
			output      *bytes.Buffer
			mockKube    *fake.Clientset
			kubeClient  *client.Client
			cmd         *cobra.Command
			cliCtx      *context.CLIContext
			skyhookName = "my-skyhook"
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

		It("should show no nodes when skyhook has no state", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, "my-skyhook", opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No nodes have state"))
		})

		It("should reset all nodes with state for skyhook", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + skyhookName
			statusAnnotationKey := statusAnnotationPrefix + skyhookName
			cordonAnnotationKey := cordonAnnotationPrefix + skyhookName
			versionAnnotationKey := versionAnnotationPrefix + skyhookName
			statusLabelKey := statusLabelPrefix + skyhookName

			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey:        string(nodeStateJSON),
						statusAnnotationKey:  "complete",
						cordonAnnotationKey:  "true",
						versionAnnotationKey: "1.0.0",
					},
					Labels: map[string]string{
						statusLabelKey: "complete",
					},
				},
			}
			node2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-2",
					Annotations: map[string]string{
						annotationKey: string(nodeStateJSON),
					},
				},
			}
			_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node1, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, err = mockKube.CoreV1().Nodes().Create(gocontext.Background(), node2, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 2 node(s)"))

			// Verify all annotations and labels were removed from node1
			updatedNode1, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode1.Annotations[annotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode1.Annotations[statusAnnotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode1.Annotations[cordonAnnotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode1.Annotations[versionAnnotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode1.Labels[statusLabelKey]
			Expect(exists).To(BeFalse())

			// Verify annotation was removed from node2
			updatedNode2, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-2", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists = updatedNode2.Annotations[annotationKey]
			Expect(exists).To(BeFalse())
		})

		It("should respect dry-run flag", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + skyhookName

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

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("[dry-run]"))

			// Verify annotation was NOT removed
			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeTrue())
		})

		It("should prompt for confirmation when confirm flag is not set", func() {
			annotationKey, err := createTestNodeWithState(mockKube, "worker-1", skyhookName)
			Expect(err).NotTo(HaveOccurred())

			opts := &resetOptions{confirm: false}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Continue?"))

			// Verify annotation was removed after confirmation
			err = verifyAnnotationRemoved(mockKube, "worker-1", annotationKey)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should abort when user declines confirmation", func() {
			nodeState := v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			}
			nodeStateJSON, _ := json.Marshal(nodeState)
			annotationKey := nodeStateAnnotationPrefix + skyhookName

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

			// Set input to 'n' for no
			cmd.SetIn(strings.NewReader("n\n"))

			opts := &resetOptions{confirm: false}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Aborted"))

			// Verify annotation was NOT removed
			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeTrue())
		})

		It("should handle nodes with invalid annotation gracefully", func() {
			annotationKey := nodeStateAnnotationPrefix + skyhookName

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

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("No nodes have state"))
		})

		It("should continue even if some annotations/labels don't exist", func() {
			// Node only has nodeState annotation, not the others
			annotationKey, err := createTestNodeWithState(mockKube, "worker-1", skyhookName)
			Expect(err).NotTo(HaveOccurred())

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			// Verify nodeState annotation was removed
			err = verifyAnnotationRemoved(mockKube, "worker-1", annotationKey)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
