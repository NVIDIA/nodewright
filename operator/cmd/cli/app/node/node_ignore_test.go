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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	"github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

var _ = Describe("Node Ignore Command", func() {
	Describe("NewIgnoreCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewIgnoreCmd(ctx)

			Expect(cmd.Use).To(Equal("ignore <node-name...>"))
			Expect(cmd.Short).To(ContainSubstring("Ignore node"))
		})

		It("should require at least one node argument", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewIgnoreCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetArgs([]string{})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires at least 1 arg"))
		})
	})

	Describe("NewUnignoreCmd", func() {
		It("should create command with correct properties", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewUnignoreCmd(ctx)

			Expect(cmd.Use).To(Equal("unignore <node-name...>"))
			Expect(cmd.Short).To(ContainSubstring("Remove ignore label"))
		})

		It("should require at least one node argument", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewUnignoreCmd(ctx)

			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetArgs([]string{})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires at least 1 arg"))
		})
	})

	Describe("runIgnore", func() {
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
		})

		Context("ignore operation", func() {
			It("should show no nodes when pattern doesn't match", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"nonexistent"}, cliCtx, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("No nodes matched"))
			})

			It("should add ignore label to node", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "worker-1",
						Labels: map[string]string{},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("Successfully ignored 1 node"))

				// Verify label was added
				updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedNode.Labels[utils.NodeIgnoreLabel]).To(Equal("true"))
			})

			It("should skip already ignored nodes", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							utils.NodeIgnoreLabel: "true",
						},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("already ignored"))
			})

			It("should ignore multiple nodes", func() {
				for _, name := range []string{"worker-1", "worker-2", "worker-3"} {
					node := &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Name:   name,
							Labels: map[string]string{},
						},
					}
					_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
					Expect(err).NotTo(HaveOccurred())
				}

				err := runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1", "worker-2", "worker-3"}, cliCtx, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("Successfully ignored 3 node"))

				// Verify all labels were added
				for _, name := range []string{"worker-1", "worker-2", "worker-3"} {
					updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(updatedNode.Labels[utils.NodeIgnoreLabel]).To(Equal("true"))
				}
			})

			It("should respect dry-run flag", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "worker-1",
						Labels: map[string]string{},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				// Enable dry-run
				cliCtx.GlobalFlags.DryRun = true

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("[dry-run]"))

				// Verify label was NOT added
				updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				_, exists := updatedNode.Labels[utils.NodeIgnoreLabel]
				Expect(exists).To(BeFalse())
			})

			It("should handle nodes with nil labels", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						// Labels is nil
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("Successfully ignored 1 node"))

				// Verify label was added
				updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedNode.Labels[utils.NodeIgnoreLabel]).To(Equal("true"))
			})
		})

		Context("unignore operation", func() {
			It("should remove ignore label from node", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							utils.NodeIgnoreLabel: "true",
						},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("Successfully unignored 1 node"))

				// Verify label was removed
				updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				_, exists := updatedNode.Labels[utils.NodeIgnoreLabel]
				Expect(exists).To(BeFalse())
			})

			It("should skip nodes that are not ignored", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "worker-1",
						Labels: map[string]string{},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("not ignored"))
			})

			It("should unignore multiple nodes", func() {
				for _, name := range []string{"worker-1", "worker-2", "worker-3"} {
					node := &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Name: name,
							Labels: map[string]string{
								utils.NodeIgnoreLabel: "true",
							},
						},
					}
					_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
					Expect(err).NotTo(HaveOccurred())
				}

				err := runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1", "worker-2", "worker-3"}, cliCtx, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("Successfully unignored 3 node"))

				// Verify all labels were removed
				for _, name := range []string{"worker-1", "worker-2", "worker-3"} {
					updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), name, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					_, exists := updatedNode.Labels[utils.NodeIgnoreLabel]
					Expect(exists).To(BeFalse())
				}
			})

			It("should respect dry-run flag for unignore", func() {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-1",
						Labels: map[string]string{
							utils.NodeIgnoreLabel: "true",
						},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				// Enable dry-run
				cliCtx.GlobalFlags.DryRun = true

				err = runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1"}, cliCtx, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(output.String()).To(ContainSubstring("[dry-run]"))

				// Verify label was NOT removed
				updatedNode, err := mockKube.CoreV1().Nodes().Get(gocontext.Background(), "worker-1", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedNode.Labels[utils.NodeIgnoreLabel]).To(Equal("true"))
			})
		})

		It("should print action summary", func() {
			for _, name := range []string{"worker-1", "worker-2"} {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   name,
						Labels: map[string]string{},
					},
				}
				_, err := mockKube.CoreV1().Nodes().Create(gocontext.Background(), node, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
			}

			err := runIgnore(gocontext.Background(), cmd, kubeClient, []string{"worker-1", "worker-2"}, cliCtx, true)
			Expect(err).NotTo(HaveOccurred())

			outputStr := output.String()
			Expect(outputStr).To(ContainSubstring("Ignoring 2 node"))
			Expect(outputStr).To(ContainSubstring("worker-1"))
			Expect(outputStr).To(ContainSubstring("worker-2"))
		})
	})
})
