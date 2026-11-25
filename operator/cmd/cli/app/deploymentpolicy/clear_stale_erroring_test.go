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

package deploymentpolicy

import (
	"bytes"
	gocontext "context"
	"encoding/json"
	"errors"

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

var _ = Describe("deployment-policy reset --clear-stale-erroring", func() {
	const (
		statusKey    = "nodewright.nvidia.com/status_demo"
		nodeStateKey = "nodewright.nvidia.com/nodeState_demo"
		drainKey     = "nodewright.nvidia.com/drainStart_demo"
	)

	var (
		kube    *fake.Clientset
		dynamic *mockdynamic.Interface
		cmd     *cobra.Command
		out     *bytes.Buffer
		errOut  *bytes.Buffer
		cliCtx  *context.CLIContext
		opts    *resetOptions
		calls   []string // node and batch patches, in order
		batch   []byte   // last batch state patch
	)

	setupNodeWright := func(annotations map[string]string, compartments map[string]v1alpha1.CompartmentStatus) {
		nw := &v1alpha1.NodeWright{}
		nw.Name = "demo"
		nw.Annotations = annotations
		nw.Status.CompartmentStatuses = compartments
		raw, err := json.Marshal(nw)
		Expect(err).NotTo(HaveOccurred())
		u := &unstructured.Unstructured{}
		Expect(json.Unmarshal(raw, &u.Object)).To(Succeed())
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "nodewright.nvidia.com", Version: "v1alpha1", Kind: "NodeWright"})

		res := &mockdynamic.NamespaceableResourceInterface{}
		dynamic.On("Resource", schema.GroupVersionResource{Group: "nodewright.nvidia.com", Version: "v1alpha1", Resource: "nodewrights"}).Return(res)
		res.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(u, nil)
		res.On("Patch", mock.Anything, "demo", mock.Anything, mock.Anything, mock.Anything, "status").
			Run(func(args mock.Arguments) {
				calls = append(calls, "batch")
				batch = args.Get(3).([]byte)
			}).Return(u, nil)
	}

	stopped := map[string]v1alpha1.CompartmentStatus{
		"__default__": {BatchState: &v1alpha1.BatchProcessingState{CurrentBatch: 3, FailedNodes: 1, ShouldStop: true}},
	}

	state := func(s v1alpha1.State) string {
		raw, err := json.Marshal(v1alpha1.NodeState{
			"pkg|1.0": {Name: "pkg", Version: "1.0", Stage: v1alpha1.StageInterrupt, State: s},
		})
		Expect(err).NotTo(HaveOccurred())
		return string(raw)
	}

	addNode := func(name string, annotations map[string]string) {
		labels := map[string]string{"role": "gpu"}
		if s, ok := annotations[statusKey]; ok {
			labels[statusKey] = s
		}
		_, err := kube.CoreV1().Nodes().Create(gocontext.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: name, ResourceVersion: "42", Annotations: annotations, Labels: labels,
		}}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	}

	addStale := func(name string) {
		addNode(name, map[string]string{statusKey: "erroring", nodeStateKey: state(v1alpha1.StateComplete)})
	}

	getNode := func(name string) *corev1.Node {
		node, err := kube.CoreV1().Nodes().Get(gocontext.Background(), name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		return node
	}

	// setAnnotation writes through the tracker: a reactor runs under the fake client's lock, so
	// calling the client from one would deadlock.
	setAnnotation := func(name, annotation, value string) {
		nodes := corev1.SchemeGroupVersion.WithResource("nodes")
		obj, err := kube.Tracker().Get(nodes, "", name)
		Expect(err).NotTo(HaveOccurred())
		node := obj.(*corev1.Node).DeepCopy()
		node.Annotations[annotation] = value
		Expect(kube.Tracker().Update(nodes, node, "")).To(Succeed())
	}

	run := func() error {
		return runDeploymentPolicyReset(gocontext.Background(), cmd, client.NewWithClientsAndConfig(kube, dynamic, nil), "demo", opts, cliCtx)
	}

	conflict := func() error {
		return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "n1", errors.New("the object has been modified"))
	}

	BeforeEach(func() {
		calls, batch = nil, nil
		kube = fake.NewClientset()
		kube.PrependReactor("patch", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			calls = append(calls, action.(ktesting.PatchAction).GetName())
			return false, nil, nil
		})
		dynamic = &mockdynamic.Interface{}
		cmd = &cobra.Command{}
		out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
		cmd.SetOut(out)
		cmd.SetErr(errOut)
		cliCtx = context.NewCLIContext(nil)
		opts = &resetOptions{confirm: true, clearStaleErroring: true}
	})

	It("clears stale nodes before resetting batch state and keeps package state", func() {
		setupNodeWright(nil, stopped)
		addStale("n2")
		addStale("n1")
		before := getNode("n1").Annotations[nodeStateKey]

		Expect(run()).To(Succeed())

		Expect(calls).To(Equal([]string{"n1", "n2", "batch"}))
		for _, name := range []string{"n1", "n2"} {
			node := getNode(name)
			Expect(node.Annotations).To(HaveKeyWithValue(statusKey, "waiting"))
			Expect(node.Labels).To(HaveKeyWithValue(statusKey, "waiting"))
			Expect(node.Labels).To(HaveKeyWithValue("role", "gpu"))
			Expect(node.Annotations).To(HaveKeyWithValue(nodeStateKey, before))
		}
		Expect(string(batch)).To(ContainSubstring(`"shouldStop":false`))
		Expect(errOut.String()).To(BeEmpty())
	})

	It("ties the patch to the node it checked", func() {
		setupNodeWright(nil, stopped)
		addStale("n1")
		var patch []byte
		kube.PrependReactor("patch", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			patch = action.(ktesting.PatchAction).GetPatch()
			return false, nil, nil
		})

		Expect(run()).To(Succeed())
		Expect(string(patch)).To(ContainSubstring(`"resourceVersion":"42"`))
	})

	DescribeTable("leaves a node that may have really failed alone",
		func(annotations map[string]string, reason string) {
			annotations[statusKey] = "erroring"
			setupNodeWright(nil, stopped)
			addNode("bad", annotations)
			addStale("stale")

			Expect(run()).To(Succeed())

			Expect(calls).To(Equal([]string{"stale", "batch"}))
			Expect(getNode("bad").Annotations).To(HaveKeyWithValue(statusKey, "erroring"))
			Expect(getNode("bad").Labels).To(HaveKeyWithValue(statusKey, "erroring"))
			Expect(out.String()).To(ContainSubstring("bad: " + reason))
			Expect(errOut.String()).To(ContainSubstring("1 node(s) left alone"))
		},
		Entry("erroring package", map[string]string{nodeStateKey: `{"a|1":{"state":"complete"},"b|1":{"stage":"apply","state":"erroring"}}`}, "package b|1 is erroring at stage apply"),
		Entry("unknown package", map[string]string{nodeStateKey: `{"a|1":{"stage":"config","state":"unknown"}}`}, "package a|1 is unknown at stage config"),
		Entry("package without a state", map[string]string{nodeStateKey: `{"a|1":{"state":"complete"},"b|1":{"stage":"apply"}}`}, "package b|1 has no state"),
		Entry("null package", map[string]string{nodeStateKey: `{"a|1":null}`}, "package a|1 has no state"),
		Entry("drain started", map[string]string{nodeStateKey: `{"a|1":{"state":"complete"}}`, drainKey: "2026-11-01T00:00:00Z"}, "a drain was started on it"),
		Entry("empty drain start", map[string]string{nodeStateKey: `{"a|1":{"state":"complete"}}`, drainKey: ""}, "a drain was started on it"),
		Entry("no package state", map[string]string{}, "it has no package state"),
		Entry("empty package state", map[string]string{nodeStateKey: `{}`}, "it has no package state"),
		Entry("unreadable package state", map[string]string{nodeStateKey: `{`}, "its package state cannot be read"),
	)

	It("ignores nodes that are not erroring and other NodeWrights' nodes", func() {
		setupNodeWright(nil, stopped)
		addNode("waiting", map[string]string{statusKey: "waiting", nodeStateKey: state(v1alpha1.StateComplete)})
		addNode("other", map[string]string{"nodewright.nvidia.com/status_demo2": "erroring", "nodewright.nvidia.com/nodeState_demo2": state(v1alpha1.StateComplete)})

		Expect(run()).To(Succeed())
		Expect(calls).To(Equal([]string{"batch"}))
		Expect(out.String()).To(ContainSubstring(`No node is erroring for NodeWright "demo"`))
	})

	It("changes nothing on --dry-run and reports both groups", func() {
		cliCtx.GlobalFlags.DryRun = true
		setupNodeWright(nil, stopped)
		addStale("stale")
		addNode("bad", map[string]string{statusKey: "erroring", nodeStateKey: state(v1alpha1.StateErroring)})

		Expect(run()).To(Succeed())
		Expect(calls).To(BeEmpty())
		Expect(getNode("stale").Annotations).To(HaveKeyWithValue(statusKey, "erroring"))
		Expect(out.String()).To(ContainSubstring("Stale erroring nodes to set to waiting (1):\n  - stale"))
		Expect(out.String()).To(ContainSubstring("Erroring nodes left alone (1):\n  - bad: package pkg|1.0 is erroring"))
	})

	It("changes nothing when the prompt is declined", func() {
		opts.confirm = false
		cmd.SetIn(bytes.NewBufferString("n\n"))
		setupNodeWright(nil, stopped)
		addStale("n1")

		Expect(run()).To(Succeed())
		Expect(calls).To(BeEmpty())
		Expect(out.String()).To(ContainSubstring("Aborted"))
	})

	DescribeTable("refuses while the operator hides erroring nodes",
		func(annotation string) {
			setupNodeWright(map[string]string{annotation: "true"}, stopped)
			addStale("n1")

			Expect(run()).To(MatchError(ContainSubstring("is paused or disabled")))
			Expect(calls).To(BeEmpty())
		},
		Entry("paused", "nodewright.nvidia.com/pause"),
		Entry("disabled", "nodewright.nvidia.com/disable"),
	)

	It("reads the node again on a conflict and clears it", func() {
		setupNodeWright(nil, stopped)
		addStale("n1")
		failures := 1
		kube.PrependReactor("patch", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
			if failures > 0 {
				failures--
				return true, nil, conflict()
			}
			return false, nil, nil
		})

		Expect(run()).To(Succeed())
		Expect(calls).To(Equal([]string{"n1", "batch"}))
		Expect(getNode("n1").Annotations).To(HaveKeyWithValue(statusKey, "waiting"))
	})

	DescribeTable("checks the node again on a conflict and leaves it alone if it is no longer stale",
		func(key, value, reason string) {
			setupNodeWright(nil, stopped)
			addStale("n1")
			first := true
			kube.PrependReactor("patch", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
				Expect(first).To(BeTrue(), "must not patch again")
				first = false
				setAnnotation("n1", key, value)
				return true, nil, conflict()
			})

			Expect(run()).To(Succeed())
			Expect(calls).To(Equal([]string{"batch"}))
			Expect(getNode("n1").Annotations).To(HaveKeyWithValue(key, value))
			Expect(out.String()).To(ContainSubstring("Left node n1 alone: " + reason))
			Expect(errOut.String()).To(ContainSubstring("1 node(s) left alone"))
		},
		Entry("package failed meanwhile", nodeStateKey, `{"pkg|1.0":{"stage":"apply","state":"erroring"}}`, "package pkg|1.0 is erroring"),
		Entry("drain started meanwhile", drainKey, "2026-11-01T00:00:00Z", "a drain was started on it"),
		Entry("status moved on meanwhile", statusKey, "in_progress", `its status is now "in_progress"`),
	)

	It("tries every node and does not reset batch state if one cannot be cleared", func() {
		setupNodeWright(nil, stopped)
		addStale("n1")
		addStale("n2")
		kube.PrependReactor("patch", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.(ktesting.PatchAction).GetName() == "n1" {
				return true, nil, errors.New("forbidden")
			}
			return false, nil, nil
		})

		err := run()
		Expect(err).To(MatchError(And(ContainSubstring("batch state was not reset"), ContainSubstring(`"n1": forbidden`))))
		Expect(calls).To(Equal([]string{"n2"}))
		Expect(out.String()).NotTo(ContainSubstring("Successfully reset"))
	})

	It("returns an error without changes when nodes cannot be listed", func() {
		setupNodeWright(nil, stopped)
		kube.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("forbidden")
		})

		Expect(run()).To(MatchError(ContainSubstring("listing nodes: forbidden")))
		Expect(calls).To(BeEmpty())
	})

	It("clears stale nodes of a NodeWright without compartment statuses", func() {
		setupNodeWright(nil, nil)
		addStale("n1")

		Expect(run()).To(Succeed())
		Expect(getNode("n1").Annotations).To(HaveKeyWithValue(statusKey, "waiting"))
	})

	It("does not read or change nodes without the flag, even while paused", func() {
		opts.clearStaleErroring = false
		setupNodeWright(map[string]string{"nodewright.nvidia.com/pause": "true"}, stopped)
		addStale("n1")
		kube.ClearActions()

		Expect(run()).To(Succeed())
		Expect(kube.Actions()).To(BeEmpty())
		Expect(calls).To(Equal([]string{"batch"}))
		Expect(getNode("n1").Annotations).To(HaveKeyWithValue(statusKey, "erroring"))
	})

	It("is off by default", func() {
		flag := NewResetCmd(context.NewCLIContext(nil)).Flags().Lookup("clear-stale-erroring")
		Expect(flag).NotTo(BeNil())
		Expect(flag.DefValue).To(Equal("false"))
	})
})
