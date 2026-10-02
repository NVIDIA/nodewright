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
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	mockdynamic "github.com/NVIDIA/nodewright/operator/internal/mocks/dynamic"
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

			Expect(cmd.Use).To(Equal("reset <nodewright-name>"))
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
			drainStartAnnotationKey := drainStartAnnotationPrefix + skyhookName
			versionAnnotationKey := versionAnnotationPrefix + skyhookName
			statusLabelKey := statusLabelPrefix + skyhookName

			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "worker-1",
					Annotations: map[string]string{
						annotationKey:           string(nodeStateJSON),
						statusAnnotationKey:     "complete",
						cordonAnnotationKey:     "true",
						drainStartAnnotationKey: "2026-06-02T12:00:00Z",
						versionAnnotationKey:    "1.0.0",
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
			// No dynamic client, so the CLI cannot confirm the NodeWright is gone and keeps
			// its cordon for the operator to release.
			_, exists = updatedNode1.Annotations[cordonAnnotationKey]
			Expect(exists).To(BeTrue())
			_, exists = updatedNode1.Annotations[drainStartAnnotationKey]
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

		It("should reset nodes with drain metadata before node state exists", func() {
			drainStartAnnotationKey := drainStartAnnotationPrefix + skyhookName
			statusAnnotationKey := statusAnnotationPrefix + skyhookName

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

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[drainStartAnnotationKey]
			Expect(exists).To(BeFalse())
			_, exists = updatedNode.Annotations[statusAnnotationKey]
			Expect(exists).To(BeFalse())
		})

		It("should reset nodes with only a status label", func() {
			statusLabelKey := statusLabelPrefix + skyhookName

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

			opts := &resetOptions{confirm: true}
			err = runReset(gocontext.Background(), cmd, kubeClient, skyhookName, opts, cliCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Labels[statusLabelKey]
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
			Expect(output.String()).To(ContainSubstring("Successfully reset 1 node"))

			updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, exists := updatedNode.Annotations[annotationKey]
			Expect(exists).To(BeFalse())
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

		It("returns an error when listing nodes fails", func() {
			fakeKube := fake.NewClientset()
			fakeKube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("apiserver unreachable")
			})
			failingClient := client.NewWithClientsAndConfig(fakeKube, nil, nil)

			opts := &resetOptions{confirm: true, skipBatchReset: true}
			err := runReset(gocontext.Background(), cmd, failingClient, skyhookName, opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("apiserver unreachable"))
		})
	})

	// The operator only releases a cordon it holds a cordon_<name> annotation for, and reset
	// never clears spec.unschedulable. Stripping the annotation while the NodeWright still
	// exists would leave the node cordoned after the re-run completes.
	Describe("cordon annotation", func() {
		var (
			fakeKube    *fake.Clientset
			mockDynamic *mockdynamic.Interface
			kubeClient  *client.Client
			cliCtx      *context.CLIContext
			cmd         *cobra.Command
			out         *bytes.Buffer
			mockNSRes   *mockdynamic.NamespaceableResourceInterface
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
		})

		nodeWrightExists := func() {
			u := &unstructured.Unstructured{}
			u.SetName(name)
			mockNSRes.On("Get", mock.Anything, name, mock.Anything, mock.Anything).Return(u, nil)
		}
		nodeWrightGone := func() {
			mockNSRes.On("Get", mock.Anything, name, mock.Anything, mock.Anything).Return(
				(*unstructured.Unstructured)(nil),
				apierrors.NewNotFound(schema.GroupResource{Group: "nodewright.nvidia.com", Resource: "nodewrights"}, name),
			)
		}
		addNode := func(annotations map[string]string) {
			n := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Annotations: annotations},
				Spec:       corev1.NodeSpec{Unschedulable: true},
			}
			_, err := fakeKube.CoreV1().Nodes().Create(gocontext.Background(), n, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}
		annotationsAfter := func() map[string]string {
			got, err := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			return got.Annotations
		}

		It("keeps the cordon while the NodeWright exists so the re-run releases it", func() {
			nodeWrightExists()
			addNode(map[string]string{nodeStateKey: "{}", cordonKey: "true"})

			opts := &resetOptions{confirm: true, skipBatchReset: true}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, name, opts, cliCtx)).To(Succeed())

			Expect(annotationsAfter()).NotTo(HaveKey(nodeStateKey))
			Expect(annotationsAfter()).To(HaveKeyWithValue(cordonKey, "true"))
		})

		It("removes the cordon of a NodeWright that no longer exists", func() {
			nodeWrightGone()
			addNode(map[string]string{nodeStateKey: "{}", cordonKey: "true"})

			opts := &resetOptions{confirm: true, skipBatchReset: true}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, name, opts, cliCtx)).To(Succeed())

			Expect(annotationsAfter()).NotTo(HaveKey(nodeStateKey))
			Expect(annotationsAfter()).NotTo(HaveKey(cordonKey))
		})

		// Orphaned Cordon Recovery relies on this: the NodeWright was force-deleted and only its
		// cordon annotation is left on the node.
		It("lists and strips a node whose only metadata is an orphaned cordon", func() {
			nodeWrightGone()
			addNode(map[string]string{cordonKey: "true"})

			opts := &resetOptions{confirm: true, skipBatchReset: true}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, name, opts, cliCtx)).To(Succeed())

			Expect(out.String()).To(ContainSubstring("Successfully reset 1 node"))
			Expect(annotationsAfter()).NotTo(HaveKey(cordonKey))
		})

		It("does not count a kept cordon as state to reset", func() {
			nodeWrightExists()
			addNode(map[string]string{cordonKey: "true"})

			opts := &resetOptions{confirm: true, skipBatchReset: true}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, name, opts, cliCtx)).To(Succeed())

			Expect(out.String()).To(ContainSubstring("No nodes have state"))
			Expect(annotationsAfter()).To(HaveKeyWithValue(cordonKey, "true"))
		})
	})

	Describe("--package", func() {
		var (
			fakeKube    *fake.Clientset
			mockDynamic *mockdynamic.Interface
			kubeClient  *client.Client
			cliCtx      *context.CLIContext
			cmd         *cobra.Command
			out         *bytes.Buffer
		)

		skyhookGVR := schema.GroupVersionResource{
			Group:    "nodewright.nvidia.com",
			Version:  "v1alpha1",
			Resource: "nodewrights",
		}

		BeforeEach(func() {
			fakeKube = fake.NewClientset()
			mockDynamic = &mockdynamic.Interface{}
			kubeClient = client.NewWithClientsAndConfig(fakeKube, mockDynamic, nil)
			cliCtx = context.NewCLIContext(nil)
			cmd = NewResetCmd(cliCtx)
			out = &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetIn(bytes.NewBufferString("y\n"))
		})

		setupSkyhookCR := func(name, version string) {
			sk := &v1alpha1.NodeWright{}
			sk.Name = name
			sk.Annotations = map[string]string{
				"nodewright.nvidia.com/version": version,
			}
			u := &unstructured.Unstructured{}
			raw, err := json.Marshal(sk)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(raw, &u.Object)).To(Succeed())
			u.SetGroupVersionKind(schema.GroupVersionKind{Group: "nodewright.nvidia.com", Version: "v1alpha1", Kind: "NodeWright"})
			mockNSRes := &mockdynamic.NamespaceableResourceInterface{}
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, name, mock.Anything, mock.Anything).Return(u, nil)
		}

		addNode := func(name string, ns v1alpha1.NodeState) {
			raw, _ := json.Marshal(ns)
			n := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
					Annotations: map[string]string{
						"nodewright.nvidia.com/nodeState_demo": string(raw),
					},
				},
			}
			_, err := fakeKube.CoreV1().Nodes().Create(gocontext.Background(), n, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}

		It("removes only the named package entry from each node", func() {
			setupSkyhookCR("demo", "v0.15.0")
			addNode("n1", v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
				"pkg2|2.0": {Name: "pkg2", Version: "2.0", Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete},
			})
			opts := &resetOptions{confirm: true, skipBatchReset: true, pkg: "pkg1"}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, "demo", opts, cliCtx)).To(Succeed())

			got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
			var after v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(got.Annotations["nodewright.nvidia.com/nodeState_demo"]), &after)).To(Succeed())
			_, hasPkg1 := after["pkg1|1.0"]
			_, hasPkg2 := after["pkg2|2.0"]
			Expect(hasPkg1).To(BeFalse())
			Expect(hasPkg2).To(BeTrue())
		})

		It("with name:version only removes if version matches", func() {
			setupSkyhookCR("demo", "v0.15.0")
			addNode("n1", v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			})
			opts := &resetOptions{confirm: true, skipBatchReset: true, pkg: "pkg1:9.9"}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, "demo", opts, cliCtx)).To(Succeed())

			got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
			var after v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(got.Annotations["nodewright.nvidia.com/nodeState_demo"]), &after)).To(Succeed())
			_, hasPkg1 := after["pkg1|1.0"]
			Expect(hasPkg1).To(BeTrue())
			Expect(out.String()).To(ContainSubstring("no matching package"))
		})

		It("removes the entire annotation when the last entry is removed", func() {
			setupSkyhookCR("demo", "v0.15.0")
			addNode("n1", v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			})
			opts := &resetOptions{confirm: true, skipBatchReset: true, pkg: "pkg1"}
			Expect(runReset(gocontext.Background(), cmd, kubeClient, "demo", opts, cliCtx)).To(Succeed())
			got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
			_, exists := got.Annotations["nodewright.nvidia.com/nodeState_demo"]
			Expect(exists).To(BeFalse())
		})

		DescribeTable("rejects malformed --package",
			func(value string) {
				addNode("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0"}})
				opts := &resetOptions{confirm: true, skipBatchReset: true, pkg: value}
				err := runReset(gocontext.Background(), cmd, kubeClient, "demo", opts, cliCtx)
				Expect(err).To(HaveOccurred())
			},
			Entry("empty name with version", ":1.0"),
			Entry("empty version with colon", "pkg1:"),
		)

		It("rejects --package against an operator older than the supported floor", func() {
			setupSkyhookCR("demo", "v0.7.0")
			addNode("n1", v1alpha1.NodeState{
				"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete},
			})
			opts := &resetOptions{confirm: true, skipBatchReset: true, pkg: "pkg1"}
			err := runReset(gocontext.Background(), cmd, kubeClient, "demo", opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("does not support"))
			Expect(err.Error()).To(ContainSubstring("v0.7.0"))
			Expect(err.Error()).To(ContainSubstring("v0.7.5"))
		})
	})
})
