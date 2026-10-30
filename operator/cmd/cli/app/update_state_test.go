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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
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

var _ = Describe("UpdateState Command", func() {
	Describe("NewUpdateStateCmd", func() {
		It("creates the command with the expected use string and arg count", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewUpdateStateCmd(ctx)
			Expect(cmd.Use).To(Equal("update-state <nodewright-name> <package> <version> <stage> <state>"))
			Expect(cmd.Args).NotTo(BeNil())
			Expect(cmd.Args(cmd, []string{"a", "b", "c", "d"})).To(HaveOccurred())
			Expect(cmd.Args(cmd, []string{"a", "b", "c", "d", "e"})).NotTo(HaveOccurred())
		})

		It("registers --node, --selector, --confirm, --add flags", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewUpdateStateCmd(ctx)
			Expect(cmd.Flags().Lookup("node")).NotTo(BeNil())
			Expect(cmd.Flags().Lookup("selector")).NotTo(BeNil())
			Expect(cmd.Flags().Lookup("confirm")).NotTo(BeNil())
			Expect(cmd.Flags().Lookup("add")).NotTo(BeNil())
		})

		It("marks --node and --selector mutually exclusive", func() {
			ctx := context.NewCLIContext(nil)
			cmd := NewUpdateStateCmd(ctx)
			cmd.SetArgs([]string{"sh", "p", "1.0", "config", "complete", "--node", "n1", "--selector", "role=gpu"})
			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			// cobra's MarkFlagsMutuallyExclusive emits this exact phrasing
			Expect(err.Error()).To(MatchRegexp(`(?i)none of the others can be`))
		})
	})

	Describe("runUpdateState", func() {
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
			out = &bytes.Buffer{}
			cmd = &cobra.Command{}
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetIn(bytes.NewBufferString("y\n"))
		})

		setupSkyhookCR := func(packages map[string]string) {
			sk := &v1alpha1.NodeWright{}
			sk.Name = "demo"
			sk.Spec.Packages = v1alpha1.Packages{}
			for name, version := range packages {
				sk.Spec.Packages[name] = v1alpha1.Package{
					PackageRef: v1alpha1.PackageRef{Name: name, Version: version},
					Image:      "example.com/" + name,
				}
			}
			u := &unstructured.Unstructured{}
			raw, err := json.Marshal(sk)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(raw, &u.Object)).To(Succeed())
			u.SetGroupVersionKind(schema.GroupVersionKind{Group: "nodewright.nvidia.com", Version: "v1alpha1", Kind: "NodeWright"})

			mockNSRes := &mockdynamic.NamespaceableResourceInterface{}
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(u, nil)
		}

		addNodeWithState := func(name string, state v1alpha1.NodeState) {
			raw, _ := json.Marshal(state)
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

		It("rejects an invalid stage", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})
			opts := &updateStateOptions{confirm: true}
			err := runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "bogus", "complete"}, opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid stage"))
		})

		It("rejects an invalid state", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})
			opts := &updateStateOptions{confirm: true}
			err := runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "weird"}, opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid state"))
		})

		It("rejects a package not present in the NodeWright spec", func() {
			setupSkyhookCR(map[string]string{"other": "9.9"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0"}})
			opts := &updateStateOptions{confirm: true}
			err := runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "complete"}, opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found in spec"))
		})

		It("updates the matching entry on every tracked node by default", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete, Image: "img:1", ContainerSHA: "sha", Restarts: 3}})
			addNodeWithState("n2", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})

			opts := &updateStateOptions{confirm: true}
			Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "interrupt", "in_progress"}, opts, cliCtx)).To(Succeed())

			for _, name := range []string{"n1", "n2"} {
				got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), name, metav1.GetOptions{})
				var ns v1alpha1.NodeState
				Expect(json.Unmarshal([]byte(got.Annotations["nodewright.nvidia.com/nodeState_demo"]), &ns)).To(Succeed())
				entry, ok := ns["pkg1|1.0"]
				Expect(ok).To(BeTrue())
				Expect(string(entry.Stage)).To(Equal("interrupt"))
				Expect(string(entry.State)).To(Equal("in_progress"))
			}

			n1, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
			var n1ns v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(n1.Annotations["nodewright.nvidia.com/nodeState_demo"]), &n1ns)).To(Succeed())
			Expect(n1ns["pkg1|1.0"].Image).To(Equal("img:1"))
			Expect(n1ns["pkg1|1.0"].ContainerSHA).To(Equal("sha"))
			Expect(n1ns["pkg1|1.0"].Restarts).To(Equal(int32(3)))
		})

		It("respects --node to narrow the targets", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})
			addNodeWithState("n2", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})

			opts := &updateStateOptions{confirm: true, nodes: []string{"n1"}}
			Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "erroring"}, opts, cliCtx)).To(Succeed())

			n1, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
			var ns1 v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(n1.Annotations["nodewright.nvidia.com/nodeState_demo"]), &ns1)).To(Succeed())
			Expect(string(ns1["pkg1|1.0"].State)).To(Equal("erroring"))

			n2, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n2", metav1.GetOptions{})
			var ns2 v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(n2.Annotations["nodewright.nvidia.com/nodeState_demo"]), &ns2)).To(Succeed())
			Expect(string(ns2["pkg1|1.0"].State)).To(Equal("complete"))
		})

		It("warns when --node names a node that does not exist", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0"}})
			opts := &updateStateOptions{confirm: true, nodes: []string{"missing"}}
			Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "complete"}, opts, cliCtx)).To(Succeed())
			Expect(out.String()).To(ContainSubstring(`node "missing" not found`))
		})

		It("warns when a node has the package at a different version", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|9.9": {Name: "pkg1", Version: "9.9"}})
			opts := &updateStateOptions{confirm: true}
			Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "complete"}, opts, cliCtx)).To(Succeed())
			Expect(out.String()).To(ContainSubstring(`pkg1 at version "9.9"`))
		})

		It("dry-run short-circuits when zero targets", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			cliCtx.GlobalFlags.DryRun = true
			opts := &updateStateOptions{confirm: true}
			Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "erroring"}, opts, cliCtx)).To(Succeed())
			Expect(out.String()).To(ContainSubstring("no matching nodes"))
			Expect(out.String()).NotTo(ContainSubstring("[dry-run]"))
		})

		It("dry-run with targets does not modify nodes", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})
			cliCtx.GlobalFlags.DryRun = true

			opts := &updateStateOptions{confirm: true}
			Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "erroring"}, opts, cliCtx)).To(Succeed())
			Expect(out.String()).To(ContainSubstring("[dry-run]"))

			got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
			var ns v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(got.Annotations["nodewright.nvidia.com/nodeState_demo"]), &ns)).To(Succeed())
			Expect(string(ns["pkg1|1.0"].Stage)).To(Equal("apply"))
			Expect(string(ns["pkg1|1.0"].State)).To(Equal("complete"))
		})

		It("returns an error when listing nodes fails", func() {
			setupSkyhookCR(map[string]string{"pkg1": "1.0"})
			fakeKube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("apiserver unreachable")
			})

			opts := &updateStateOptions{confirm: true}
			err := runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "complete"}, opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("apiserver unreachable"))
		})

		It("rejects update-state against an operator older than the supported floor", func() {
			sk := &v1alpha1.NodeWright{}
			sk.Name = "demo"
			sk.Annotations = map[string]string{"nodewright.nvidia.com/version": "v0.7.0"}
			sk.Spec.Packages = v1alpha1.Packages{
				"pkg1": {PackageRef: v1alpha1.PackageRef{Name: "pkg1", Version: "1.0"}, Image: "example.com/pkg1"},
			}
			u := &unstructured.Unstructured{}
			raw, err := json.Marshal(sk)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(raw, &u.Object)).To(Succeed())
			u.SetGroupVersionKind(schema.GroupVersionKind{Group: "nodewright.nvidia.com", Version: "v1alpha1", Kind: "NodeWright"})
			mockNSRes := &mockdynamic.NamespaceableResourceInterface{}
			mockDynamic.On("Resource", skyhookGVR).Return(mockNSRes)
			mockNSRes.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(u, nil)

			opts := &updateStateOptions{confirm: true}
			err = runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "config", "complete"}, opts, cliCtx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("does not support"))
			Expect(err.Error()).To(ContainSubstring("v0.7.0"))
			Expect(err.Error()).To(ContainSubstring("v0.7.5"))
		})

		Describe("--add", func() {
			It("requires --node or --selector", func() {
				setupSkyhookCR(map[string]string{"pkg1": "1.0"})
				opts := &updateStateOptions{confirm: true, add: true}
				err := runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "apply", "in_progress"}, opts, cliCtx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("--add requires --node or --selector"))
			})

			It("creates a fresh entry on the targeted node", func() {
				setupSkyhookCR(map[string]string{"pkg1": "1.0"})
				_, err := fakeKube.CoreV1().Nodes().Create(gocontext.Background(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
					metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())

				opts := &updateStateOptions{confirm: true, add: true, nodes: []string{"n1"}}
				Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "apply", "in_progress"}, opts, cliCtx)).To(Succeed())

				got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
				raw, ok := got.Annotations["nodewright.nvidia.com/nodeState_demo"]
				Expect(ok).To(BeTrue())
				var ns v1alpha1.NodeState
				Expect(json.Unmarshal([]byte(raw), &ns)).To(Succeed())
				entry, ok := ns["pkg1|1.0"]
				Expect(ok).To(BeTrue())
				Expect(entry.Name).To(Equal("pkg1"))
				Expect(entry.Version).To(Equal("1.0"))
				Expect(entry.Image).To(Equal("example.com/pkg1"))
				Expect(string(entry.Stage)).To(Equal("apply"))
				Expect(string(entry.State)).To(Equal("in_progress"))
			})

			It("warns and skips when the entry already exists", func() {
				setupSkyhookCR(map[string]string{"pkg1": "1.0"})
				addNodeWithState("n1", v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageApply, State: v1alpha1.StateComplete}})
				opts := &updateStateOptions{confirm: true, add: true, nodes: []string{"n1"}}
				Expect(runUpdateState(gocontext.Background(), cmd, kubeClient, []string{"demo", "pkg1", "1.0", "apply", "in_progress"}, opts, cliCtx)).To(Succeed())
				Expect(out.String()).To(ContainSubstring(`entry already exists`))

				got, _ := fakeKube.CoreV1().Nodes().Get(gocontext.Background(), "n1", metav1.GetOptions{})
				var ns v1alpha1.NodeState
				Expect(json.Unmarshal([]byte(got.Annotations["nodewright.nvidia.com/nodeState_demo"]), &ns)).To(Succeed())
				Expect(string(ns["pkg1|1.0"].State)).To(Equal("complete"))
			})
		})
	})

	Describe("buildUpdateStateTarget interruptPending", func() {
		pkgWith := func(interrupt *v1alpha1.Interrupt) v1alpha1.Package {
			return v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "pkg1", Version: "1.0"}, Interrupt: interrupt}
		}
		pending := func(pkg v1alpha1.Package, addMode bool) bool {
			states := map[string]v1alpha1.NodeState{}
			if !addMode {
				states["n1"] = v1alpha1.NodeState{"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StagePostInterrupt, State: v1alpha1.StateComplete}}
			}
			target, ok := buildUpdateStateTarget(&cobra.Command{}, "n1", states, pkg, v1alpha1.StageApply, v1alpha1.StateInProgress, addMode)
			Expect(ok).To(BeTrue())
			var ns v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(target.nextRaw), &ns)).To(Succeed())
			return ns["pkg1|1.0"].InterruptPending
		}

		It("sets the flag on apply only for a package that declares an interrupt", func() {
			withInterrupt := pkgWith(&v1alpha1.Interrupt{Type: v1alpha1.REBOOT})
			Expect(pending(withInterrupt, false)).To(BeTrue())
			Expect(pending(withInterrupt, true)).To(BeTrue())
			Expect(pending(pkgWith(nil), false)).To(BeFalse())
			Expect(pending(pkgWith(nil), true)).To(BeFalse())
		})

		pendingAt := func(pkg v1alpha1.Package, start bool, stage v1alpha1.Stage, state v1alpha1.State) bool {
			states := map[string]v1alpha1.NodeState{"n1": {"pkg1|1.0": {Name: "pkg1", Version: "1.0", Stage: v1alpha1.StageConfig, State: v1alpha1.StateComplete, InterruptPending: start}}}
			target, ok := buildUpdateStateTarget(&cobra.Command{}, "n1", states, pkg, stage, state, false)
			Expect(ok).To(BeTrue())
			var ns v1alpha1.NodeState
			Expect(json.Unmarshal([]byte(target.nextRaw), &ns)).To(Succeed())
			return ns["pkg1|1.0"].InterruptPending
		}

		It("clears the flag on post-interrupt and on interrupt complete, and keeps it on config", func() {
			reboot := pkgWith(&v1alpha1.Interrupt{Type: v1alpha1.REBOOT})
			Expect(pendingAt(reboot, true, v1alpha1.StagePostInterrupt, v1alpha1.StateInProgress)).To(BeFalse())
			Expect(pendingAt(reboot, true, v1alpha1.StageInterrupt, v1alpha1.StateComplete)).To(BeFalse())
			Expect(pendingAt(reboot, true, v1alpha1.StageConfig, v1alpha1.StateInProgress)).To(BeTrue())
			Expect(pendingAt(reboot, false, v1alpha1.StageConfig, v1alpha1.StateInProgress)).To(BeFalse())
		})

		It("sets the flag on an unfinished interrupt only for a package that declares one", func() {
			reboot := pkgWith(&v1alpha1.Interrupt{Type: v1alpha1.REBOOT})
			Expect(pendingAt(reboot, false, v1alpha1.StageInterrupt, v1alpha1.StateInProgress)).To(BeTrue())
			Expect(pendingAt(reboot, false, v1alpha1.StageInterrupt, v1alpha1.StateErroring)).To(BeTrue())
			Expect(pendingAt(pkgWith(nil), true, v1alpha1.StageInterrupt, v1alpha1.StateInProgress)).To(BeFalse())
		})
	})
})
