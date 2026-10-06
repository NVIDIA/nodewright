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

package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	mockdynamic "github.com/NVIDIA/nodewright/operator/internal/mocks/dynamic"
)

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Utils CLI Tests Suite")
}

var _ = Describe("CLI Utility Functions", func() {
	Describe("MatchNodes", func() {
		It("should match nodes exactly", func() {
			nodes := []string{"node1", "node2", "node3"}
			patterns := []string{"node1", "node2"}
			matched, err := MatchNodes(patterns, nodes)
			Expect(err).NotTo(HaveOccurred())
			Expect(matched).To(ConsistOf("node1", "node2"))
		})

		It("should match nodes with regex patterns", func() {
			nodes := []string{"node1", "node2", "node3"}
			patterns := []string{"node.*"}
			matched, err := MatchNodes(patterns, nodes)
			Expect(err).NotTo(HaveOccurred())
			Expect(matched).To(ConsistOf("node1", "node2", "node3"))
		})

		It("should return an error for invalid regex patterns", func() {
			nodes := []string{"node1", "node2", "node3"}
			patterns := []string{"[invalid"}
			_, err := MatchNodes(patterns, nodes)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("UnstructuredToSkyhook", func() {
		It("should convert an unstructured object to a Skyhook", func() {
			u := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "nodewright.nvidia.com/v1alpha1",
					"kind":       "NodeWright",
					"metadata": map[string]interface{}{
						"name":      "test-skyhook",
						"namespace": "default",
					},
					"spec": map[string]interface{}{},
				},
			}
			skyhook, err := UnstructuredToSkyhook(u)
			Expect(err).NotTo(HaveOccurred())
			Expect(skyhook.Name).To(Equal("test-skyhook"))
		})

		It("should handle unstructured with packages", func() {
			u := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "nodewright.nvidia.com/v1alpha1",
					"kind":       "NodeWright",
					"metadata": map[string]interface{}{
						"name": "test-skyhook",
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
			skyhook, err := UnstructuredToSkyhook(u)
			Expect(err).NotTo(HaveOccurred())
			Expect(skyhook.Spec.Packages).To(HaveKey("pkg1"))
		})

	})

	Describe("CompareVersions", func() {
		It("should return -1 when v1 < v2", func() {
			Expect(CompareVersions("v0.7.6", "v0.8.0")).To(Equal(-1))
			Expect(CompareVersions("v0.7.0", "v0.7.6")).To(Equal(-1))
			Expect(CompareVersions("v1.0.0", "v2.0.0")).To(Equal(-1))
		})

		It("should return 0 when v1 == v2", func() {
			Expect(CompareVersions("v0.8.0", "v0.8.0")).To(Equal(0))
			Expect(CompareVersions("v1.2.3", "v1.2.3")).To(Equal(0))
		})

		It("should return 1 when v1 > v2", func() {
			Expect(CompareVersions("v0.8.0", "v0.7.6")).To(Equal(1))
			Expect(CompareVersions("v1.0.0", "v0.9.9")).To(Equal(1))
			Expect(CompareVersions("v2.0.0", "v1.0.0")).To(Equal(1))
		})

		It("should handle versions without v prefix", func() {
			Expect(CompareVersions("0.7.6", "0.8.0")).To(Equal(-1))
			Expect(CompareVersions("0.8.0", "v0.8.0")).To(Equal(0))
			Expect(CompareVersions("v0.8.0", "0.7.6")).To(Equal(1))
		})

		It("should handle empty versions", func() {
			Expect(CompareVersions("", "v0.8.0")).To(Equal(-1))
			Expect(CompareVersions("v0.8.0", "")).To(Equal(1))
			Expect(CompareVersions("", "")).To(Equal(0))
		})

		It("should return 0 for invalid semver versions like dev", func() {
			// Invalid versions should return 0 (unknown/equal) not -1
			Expect(CompareVersions("dev", "v0.8.0")).To(Equal(0))
			Expect(CompareVersions("vdev", "v0.8.0")).To(Equal(0))
			Expect(CompareVersions("latest", "v0.8.0")).To(Equal(0))
			Expect(CompareVersions("v0.8.0", "dev")).To(Equal(0))
		})
	})

	Describe("IsValidVersion", func() {
		It("should return true for valid semver versions", func() {
			Expect(IsValidVersion("v0.8.0")).To(BeTrue())
			Expect(IsValidVersion("v1.2.3")).To(BeTrue())
			Expect(IsValidVersion("0.8.0")).To(BeTrue()) // without v prefix
			Expect(IsValidVersion("v1.0.0-alpha")).To(BeTrue())
		})

		It("should return false for invalid versions", func() {
			Expect(IsValidVersion("")).To(BeFalse())
			Expect(IsValidVersion("dev")).To(BeFalse())
			Expect(IsValidVersion("latest")).To(BeFalse())
			Expect(IsValidVersion("vdev")).To(BeFalse())
		})
	})

	Describe("ExtractImageTag", func() {
		It("should extract tag from image with tag", func() {
			Expect(ExtractImageTag("ghcr.io/nvidia/skyhook/operator:v1.2.3")).To(Equal("v1.2.3"))
			Expect(ExtractImageTag("nginx:1.19")).To(Equal("1.19"))
		})

		It("should extract tag from image with tag and digest", func() {
			Expect(ExtractImageTag("ghcr.io/nvidia/skyhook/operator:v1.2.3@sha256:abc123")).To(Equal("v1.2.3"))
		})

		It("should return empty string for image without tag", func() {
			Expect(ExtractImageTag("ghcr.io/nvidia/skyhook/operator")).To(Equal(""))
			Expect(ExtractImageTag("nginx")).To(Equal(""))
		})

		It("should handle image with only digest", func() {
			Expect(ExtractImageTag("ghcr.io/nvidia/skyhook/operator@sha256:abc123")).To(Equal(""))
		})
	})

	Describe("ResetCompartmentBatchStates (API method)", func() {
		It("should handle skyhook with nil CompartmentStatuses", func() {
			skyhook := &v1alpha1.NodeWright{
				Status: v1alpha1.NodeWrightStatus{
					CompartmentStatuses: nil,
				},
			}
			result := skyhook.ResetCompartmentBatchStates()
			Expect(result).To(BeFalse())
			Expect(skyhook.Status.CompartmentStatuses).To(BeNil())
		})

		It("should handle skyhook with empty CompartmentStatuses", func() {
			skyhook := &v1alpha1.NodeWright{
				Status: v1alpha1.NodeWrightStatus{
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{},
				},
			}
			result := skyhook.ResetCompartmentBatchStates()
			Expect(result).To(BeFalse())
			Expect(skyhook.Status.CompartmentStatuses).To(BeEmpty())
		})

		It("should reset batch state for a single compartment", func() {
			skyhook := &v1alpha1.NodeWright{
				Status: v1alpha1.NodeWrightStatus{
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"default": {
							Matched:         10,
							Ceiling:         3,
							InProgress:      2,
							Completed:       5,
							ProgressPercent: 50,
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:        3,
								ConsecutiveFailures: 2,
								CompletedNodes:      5,
								FailedNodes:         1,
								ShouldStop:          true,
								LastBatchSize:       4,
								LastBatchFailed:     true,
							},
						},
					},
				},
			}

			result := skyhook.ResetCompartmentBatchStates()
			Expect(result).To(BeTrue())

			Expect(skyhook.Status.CompartmentStatuses).To(HaveKey("default"))
			compartment := skyhook.Status.CompartmentStatuses["default"]

			// Verify non-batch fields are preserved
			Expect(compartment.Matched).To(Equal(10))
			Expect(compartment.Ceiling).To(Equal(3))
			Expect(compartment.InProgress).To(Equal(2))
			Expect(compartment.Completed).To(Equal(5))
			Expect(compartment.ProgressPercent).To(Equal(50))

			// Verify batch state is reset to fresh state
			Expect(compartment.BatchState).NotTo(BeNil())
			Expect(compartment.BatchState.CurrentBatch).To(Equal(1))
			Expect(compartment.BatchState.ConsecutiveFailures).To(Equal(0))
			Expect(compartment.BatchState.CompletedNodes).To(Equal(0))
			Expect(compartment.BatchState.FailedNodes).To(Equal(0))
			Expect(compartment.BatchState.ShouldStop).To(BeFalse())
			Expect(compartment.BatchState.LastBatchSize).To(Equal(0))
			Expect(compartment.BatchState.LastBatchFailed).To(BeFalse())
		})

		It("should reset batch state for multiple compartments", func() {
			skyhook := &v1alpha1.NodeWright{
				Status: v1alpha1.NodeWrightStatus{
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"compartment-a": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:        5,
								ConsecutiveFailures: 3,
								CompletedNodes:      20,
								FailedNodes:         5,
								ShouldStop:          true,
								LastBatchSize:       10,
								LastBatchFailed:     true,
							},
						},
						"compartment-b": {
							BatchState: &v1alpha1.BatchProcessingState{
								CurrentBatch:        2,
								ConsecutiveFailures: 0,
								CompletedNodes:      10,
								FailedNodes:         0,
								ShouldStop:          false,
								LastBatchSize:       5,
								LastBatchFailed:     false,
							},
						},
					},
				},
			}

			result := skyhook.ResetCompartmentBatchStates()
			Expect(result).To(BeTrue())

			// Verify both compartments are reset
			for _, compartmentName := range []string{"compartment-a", "compartment-b"} {
				Expect(skyhook.Status.CompartmentStatuses).To(HaveKey(compartmentName))
				compartment := skyhook.Status.CompartmentStatuses[compartmentName]

				Expect(compartment.BatchState).NotTo(BeNil())
				Expect(compartment.BatchState.CurrentBatch).To(Equal(1))
				Expect(compartment.BatchState.ConsecutiveFailures).To(Equal(0))
				Expect(compartment.BatchState.CompletedNodes).To(Equal(0))
				Expect(compartment.BatchState.FailedNodes).To(Equal(0))
				Expect(compartment.BatchState.ShouldStop).To(BeFalse())
				Expect(compartment.BatchState.LastBatchSize).To(Equal(0))
				Expect(compartment.BatchState.LastBatchFailed).To(BeFalse())
			}
		})

		It("should handle compartment without existing batch state", func() {
			skyhook := &v1alpha1.NodeWright{
				Status: v1alpha1.NodeWrightStatus{
					CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
						"default": {
							Matched:    10,
							BatchState: nil,
						},
					},
				},
			}

			result := skyhook.ResetCompartmentBatchStates()
			Expect(result).To(BeTrue())

			compartment := skyhook.Status.CompartmentStatuses["default"]
			Expect(compartment.BatchState).NotTo(BeNil())
			Expect(compartment.BatchState.CurrentBatch).To(Equal(1))
		})
	})

	Describe("PatchBatchStates", func() {
		// Every BatchProcessingState field is omitempty, so a body marshalled from the struct
		// drops a reset's zeros and shouldStop=false, and the merge patch keeps the stored stop.
		It("sends every batch state field of every compartment, zeros included", func() {
			skyhook := &v1alpha1.NodeWright{Status: v1alpha1.NodeWrightStatus{
				CompartmentStatuses: map[string]v1alpha1.CompartmentStatus{
					"__default__": {BatchState: &v1alpha1.BatchProcessingState{CurrentBatch: 4, FailedNodes: 1, ShouldStop: true}},
					"gpu":         {BatchState: &v1alpha1.BatchProcessingState{ConsecutiveFailures: 2, LastBatchFailed: true}},
				},
			}}
			skyhook.ResetCompartmentBatchStates()

			res := &mockdynamic.NamespaceableResourceInterface{}
			dyn := &mockdynamic.Interface{}
			dyn.On("Resource", v1alpha1.GroupVersion.WithResource("nodewrights")).Return(res)
			var sent []byte
			res.On("Patch", mock.Anything, "demo", types.MergePatchType, mock.Anything, mock.Anything, "status").
				Run(func(args mock.Arguments) { sent = args.Get(3).([]byte) }).
				Return(&unstructured.Unstructured{}, nil)

			Expect(PatchBatchStates(context.Background(), dyn, "demo", skyhook.Status.CompartmentStatuses)).To(Succeed())

			var body struct {
				Status struct {
					CompartmentStatuses map[string]struct {
						BatchState map[string]any `json:"batchState"`
					} `json:"compartmentStatuses"`
				} `json:"status"`
			}
			Expect(json.Unmarshal(sent, &body)).To(Succeed())
			Expect(body.Status.CompartmentStatuses).To(HaveLen(2))
			for name, cs := range body.Status.CompartmentStatuses {
				Expect(cs.BatchState).To(HaveLen(reflect.TypeOf(v1alpha1.BatchProcessingState{}).NumField()),
					"%s: a field missing from the patch keeps its stored value", name)
				Expect(cs.BatchState).To(HaveKeyWithValue("shouldStop", false))
				Expect(cs.BatchState).To(HaveKeyWithValue("currentBatch", BeNumerically("==", 1)))
				Expect(cs.BatchState).To(HaveKeyWithValue("failedNodes", BeNumerically("==", 0)))
			}
		})
	})

	Describe("CordonKeeperForReset", func() {
		var (
			res *mockdynamic.NamespaceableResourceInterface
			dyn *mockdynamic.Interface
			cmd *cobra.Command
			out *bytes.Buffer
		)
		gpuNode := map[string]string{"pool": "gpu"}
		cpuNode := map[string]string{"pool": "cpu"}

		BeforeEach(func() {
			res = &mockdynamic.NamespaceableResourceInterface{}
			dyn = &mockdynamic.Interface{}
			dyn.On("Resource", v1alpha1.GroupVersion.WithResource("nodewrights")).Return(res)
			out = &bytes.Buffer{}
			cmd = &cobra.Command{}
			cmd.SetErr(out)
		})

		nodeWrightSelecting := func(matchLabels map[string]string) *unstructured.Unstructured {
			nw := &v1alpha1.NodeWright{}
			nw.Name = "demo"
			nw.Spec.NodeSelector = metav1.LabelSelector{MatchLabels: matchLabels}
			raw, err := json.Marshal(nw)
			Expect(err).NotTo(HaveOccurred())
			u := &unstructured.Unstructured{}
			Expect(json.Unmarshal(raw, &u.Object)).To(Succeed())
			return u
		}

		It("keeps the cordon only on nodes the NodeWright still selects", func() {
			res.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(nodeWrightSelecting(gpuNode), nil)

			keep := CordonKeeperForReset(context.Background(), cmd, dyn, "demo")

			Expect(keep(gpuNode)).To(BeTrue())
			Expect(keep(cpuNode)).To(BeFalse(), "the operator never revisits a node its selector no longer matches")
		})

		It("keeps the cordon on every node for an empty selector", func() {
			res.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(nodeWrightSelecting(nil), nil)

			Expect(CordonKeeperForReset(context.Background(), cmd, dyn, "demo")(cpuNode)).To(BeTrue())
		})

		It("removes the cordon everywhere once the NodeWright is gone", func() {
			res.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(
				(*unstructured.Unstructured)(nil),
				apierrors.NewNotFound(v1alpha1.GroupVersion.WithResource("nodewrights").GroupResource(), "demo"),
			)

			Expect(CordonKeeperForReset(context.Background(), cmd, dyn, "demo")(gpuNode)).To(BeFalse())
		})

		// Only a confirmed NotFound may strip: a stripped cordon cannot be recovered, a kept one
		// is removed by re-running reset once the lookup works.
		It("keeps the cordon and warns when the NodeWright cannot be read", func() {
			res.On("Get", mock.Anything, "demo", mock.Anything, mock.Anything).Return(
				(*unstructured.Unstructured)(nil),
				apierrors.NewForbidden(v1alpha1.GroupVersion.WithResource("nodewrights").GroupResource(), "demo", errors.New("rbac")),
			)

			keep := CordonKeeperForReset(context.Background(), cmd, dyn, "demo")

			Expect(keep(cpuNode)).To(BeTrue())
			Expect(out.String()).To(ContainSubstring(`could not read NodeWright "demo"`))
		})

		It("keeps the cordon without a dynamic client", func() {
			Expect(CordonKeeperForReset(context.Background(), cmd, nil, "demo")(cpuNode)).To(BeTrue())
		})
	})

	Describe("SetNodeAnnotation", func() {
		It("preserves arbitrary JSON characters in the value", func() {
			kube := fake.NewClientset()
			_, err := kube.CoreV1().Nodes().Create(context.Background(),
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			value := `{"a":"b\"c","x":"yz","unicode":"café"}`
			Expect(SetNodeAnnotation(context.Background(), kube, "n1", "nodewright.nvidia.com/nodeState_demo", value)).To(Succeed())

			got, err := kube.CoreV1().Nodes().Get(context.Background(), "n1", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Annotations["nodewright.nvidia.com/nodeState_demo"]).To(Equal(value))
		})
	})

	Describe("ConfirmYN", func() {
		DescribeTable("recognises y/n responses",
			func(input string, expected bool) {
				cmd := &cobra.Command{}
				out := &bytes.Buffer{}
				cmd.SetOut(out)
				cmd.SetIn(bytes.NewBufferString(input))
				ok, err := ConfirmYN(cmd, "Continue?")
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(Equal(expected))
				Expect(out.String()).To(ContainSubstring("Continue?"))
			},
			Entry("yes lowercase", "y\n", true),
			Entry("yes word", "yes\n", true),
			Entry("YES uppercase", "YES\n", true),
			Entry("no lowercase", "n\n", false),
			Entry("empty defaults to no", "\n", false),
			Entry("garbage defaults to no", "potato\n", false),
		)
	})

	Describe("ListNodesWithSkyhookState", func() {
		var kube *fake.Clientset
		BeforeEach(func() {
			kube = fake.NewClientset()
		})

		addNode := func(name, skyhook, annotationJSON string) {
			ann := map[string]string{}
			if skyhook != "" {
				ann["nodewright.nvidia.com/nodeState_"+skyhook] = annotationJSON
			}
			_, err := kube.CoreV1().Nodes().Create(context.Background(),
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: ann}},
				metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}

		It("returns a map keyed by node name with parsed NodeState", func() {
			addNode("n1", "demo", `{"pkg1|1.0":{"name":"pkg1","version":"1.0","stage":"apply","state":"complete"}}`)
			addNode("n2", "demo", `{}`)
			addNode("n3", "", "")
			addNode("n4", "other", `{"x|1":{"name":"x","version":"1"}}`)

			got, err := ListNodesWithSkyhookState(context.Background(), kube, "demo", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(2))
			Expect(got).To(HaveKey("n1"))
			Expect(got).To(HaveKey("n2"))
			Expect(got["n1"]).To(HaveKey("pkg1|1.0"))
			Expect(got["n2"]).To(BeEmpty())
		})

		It("skips nodes with malformed annotations and surfaces an error", func() {
			addNode("n1", "demo", `not json`)
			addNode("n2", "demo", `{"pkg1|1.0":{"name":"pkg1","version":"1.0"}}`)

			got, err := ListNodesWithSkyhookState(context.Background(), kube, "demo", "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("n1"))
			Expect(got).To(HaveKey("n2"))
			Expect(got).NotTo(HaveKey("n1"))
		})

		It("honours a label selector", func() {
			_, err := kube.CoreV1().Nodes().Create(context.Background(),
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{
					Name:        "n1",
					Labels:      map[string]string{"role": "gpu"},
					Annotations: map[string]string{"nodewright.nvidia.com/nodeState_demo": `{"p|1":{"name":"p","version":"1"}}`},
				}}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			_, err = kube.CoreV1().Nodes().Create(context.Background(),
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{
					Name:        "n2",
					Annotations: map[string]string{"nodewright.nvidia.com/nodeState_demo": `{"p|1":{"name":"p","version":"1"}}`},
				}}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			got, err := ListNodesWithSkyhookState(context.Background(), kube, "demo", "role=gpu")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got).To(HaveKey("n1"))
		})
	})

	Describe("RegisterNodeWrightNameFlag", func() {
		build := func(required bool) (*cobra.Command, *string) {
			var v string
			c := &cobra.Command{
				Use: "x", SilenceUsage: true, SilenceErrors: true,
				RunE: func(*cobra.Command, []string) error { return nil },
			}
			RegisterNodeWrightNameFlag(c, &v, "Name of the NodeWright CR", required)
			return c, &v
		}

		It("binds the --nodewright flag", func() {
			c, v := build(true)
			c.SetArgs([]string{"--nodewright", "a"})
			Expect(c.Execute()).To(Succeed())
			Expect(*v).To(Equal("a"))
		})

		It("accepts the deprecated --skyhook alias", func() {
			c, v := build(true)
			c.SetArgs([]string{"--skyhook", "b"})
			Expect(c.Execute()).To(Succeed())
			Expect(*v).To(Equal("b"))
		})

		It("requires one of the two when required", func() {
			c, _ := build(true)
			c.SetArgs([]string{})
			Expect(c.Execute()).To(HaveOccurred())
		})

		It("does not require the flag when optional", func() {
			c, v := build(false)
			c.SetArgs([]string{})
			Expect(c.Execute()).To(Succeed())
			Expect(*v).To(BeEmpty())
		})

		It("rejects setting both at once", func() {
			c, _ := build(true)
			c.SetArgs([]string{"--nodewright", "a", "--skyhook", "b"})
			Expect(c.Execute()).To(HaveOccurred())
		})

		It("hides the deprecated --skyhook alias from help", func() {
			c, _ := build(true)
			f := c.Flags().Lookup("skyhook")
			Expect(f).NotTo(BeNil())
			Expect(f.Hidden).To(BeTrue())
		})
	})

	Describe("isSkyhookOperatorDeployment", func() {
		dep := func(image string, labels map[string]string) *appsv1.Deployment {
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}},
					},
				},
			}
		}

		It("matches the current nodewright operator image", func() {
			Expect(isSkyhookOperatorDeployment(dep("ghcr.io/nvidia/nodewright/operator:v1", nil))).To(BeTrue())
		})

		It("matches the legacy skyhook operator image (transition)", func() {
			Expect(isSkyhookOperatorDeployment(dep("ghcr.io/nvidia/skyhook/operator:v1", nil))).To(BeTrue())
		})

		It("matches via labels when the image is unrelated", func() {
			Expect(isSkyhookOperatorDeployment(dep("busybox:latest", map[string]string{"app.kubernetes.io/name": "nodewright"}))).To(BeTrue())
		})

		It("does not match an unrelated deployment", func() {
			Expect(isSkyhookOperatorDeployment(dep("busybox:latest", map[string]string{"app": "other"}))).To(BeFalse())
		})
	})

	Describe("ResolveOperatorNamespace", func() {
		operatorIn := func(namespace string) *appsv1.Deployment {
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "controller-manager",
					Namespace: namespace,
					Labels:    map[string]string{"control-plane": "controller-manager"},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "ghcr.io/nvidia/nodewright/operator:v1"}}},
					},
				},
			}
		}

		// Same substring the loose image/label heuristic keys on, but not a
		// controller-manager: it must not decide the namespace.
		lookalikeIn := func(namespace string) *appsv1.Deployment {
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "billing-nodewright-exporter", Namespace: namespace},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "registry.example/billing-nodewright-exporter:v1"}}},
					},
				},
			}
		}

		It("prefers the nodewright namespace", func() {
			kube := fake.NewClientset(operatorIn(DefaultNamespace), operatorIn(LegacyDefaultNamespace))

			namespace, found, legacy := ResolveOperatorNamespace(context.Background(), kube)
			Expect(namespace).To(Equal(DefaultNamespace))
			Expect(found).To(BeTrue())
			Expect(legacy).To(BeFalse())
		})

		It("falls back to the legacy skyhook namespace and flags it", func() {
			kube := fake.NewClientset(operatorIn(LegacyDefaultNamespace))

			namespace, found, legacy := ResolveOperatorNamespace(context.Background(), kube)
			Expect(namespace).To(Equal(LegacyDefaultNamespace))
			Expect(found).To(BeTrue())
			Expect(legacy).To(BeTrue())
		})

		It("finds an install in an arbitrary namespace via the cluster-wide sweep", func() {
			kube := fake.NewClientset(operatorIn("platform-tools"))

			namespace, found, legacy := ResolveOperatorNamespace(context.Background(), kube)
			Expect(namespace).To(Equal("platform-tools"))
			Expect(found).To(BeTrue())
			Expect(legacy).To(BeFalse())
		})

		It("ignores a non-controller-manager lookalike and picks the real namespace", func() {
			kube := fake.NewClientset(lookalikeIn(DefaultNamespace), operatorIn(LegacyDefaultNamespace))

			namespace, found, legacy := ResolveOperatorNamespace(context.Background(), kube)
			Expect(namespace).To(Equal(LegacyDefaultNamespace))
			Expect(found).To(BeTrue())
			Expect(legacy).To(BeTrue())
		})

		It("reports the default when no operator is installed", func() {
			kube := fake.NewClientset()

			namespace, found, legacy := ResolveOperatorNamespace(context.Background(), kube)
			Expect(namespace).To(Equal(DefaultNamespace))
			Expect(found).To(BeFalse())
			Expect(legacy).To(BeFalse())
		})

		It("reports the default when there is no client", func() {
			namespace, found, _ := ResolveOperatorNamespace(context.Background(), nil)
			Expect(namespace).To(Equal(DefaultNamespace))
			Expect(found).To(BeFalse())
		})
	})

	Describe("output helpers", func() {
		type row struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Node  string `json:"node"`
		}

		cfg := TableConfig[row]{
			Headers:     []string{"NAME", "STATE"},
			Extract:     func(r row) []string { return []string{r.Name, r.State} },
			WideHeaders: []string{"NODE"},
			WideExtract: func(r row) []string { return []string{r.Node} },
		}

		rows := []row{
			{Name: "tuning", State: "complete", Node: "node-1"},
			{Name: "shellscript", State: "erroring", Node: "node-2"},
		}

		Describe("OutputJSON", func() {
			It("should write indented JSON", func() {
				out := &bytes.Buffer{}
				Expect(OutputJSON(out, rows)).To(Succeed())
				Expect(out.String()).To(ContainSubstring(`"name": "tuning"`))
			})

			It("should report a value it cannot marshal", func() {
				out := &bytes.Buffer{}
				Expect(OutputJSON(out, make(chan int))).To(MatchError(ContainSubstring("marshaling json")))
			})
		})

		Describe("OutputYAML", func() {
			It("should write YAML", func() {
				out := &bytes.Buffer{}
				Expect(OutputYAML(out, rows)).To(Succeed())
				Expect(out.String()).To(ContainSubstring("name: tuning"))
				Expect(out.String()).To(ContainSubstring("state: erroring"))
			})

			It("should report a value it cannot marshal", func() {
				out := &bytes.Buffer{}
				Expect(OutputYAML(out, make(chan int))).To(MatchError(ContainSubstring("marshaling yaml")))
			})
		})

		Describe("OutputTable", func() {
			It("should write the narrow headers, a rule and one line per row", func() {
				out := &bytes.Buffer{}
				Expect(OutputTable(out, cfg, rows)).To(Succeed())

				lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
				Expect(lines).To(HaveLen(4))
				Expect(lines[0]).To(MatchRegexp(`^NAME\s+STATE$`))
				Expect(lines[1]).To(MatchRegexp(`^----\s+-----$`))
				Expect(lines[2]).To(MatchRegexp(`^tuning\s+complete$`))
				Expect(lines[3]).To(MatchRegexp(`^shellscript\s+erroring$`))
			})

			It("should write headers and rule even with no rows", func() {
				out := &bytes.Buffer{}
				Expect(OutputTable(out, cfg, nil)).To(Succeed())

				lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
				Expect(lines).To(HaveLen(2))
				Expect(lines[0]).To(MatchRegexp(`^NAME\s+STATE$`))
				Expect(lines[1]).To(MatchRegexp(`^----\s+-----$`))
			})
		})

		Describe("OutputWide", func() {
			It("should append the wide columns", func() {
				out := &bytes.Buffer{}
				Expect(OutputWide(out, cfg, rows)).To(Succeed())

				lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
				Expect(lines[0]).To(MatchRegexp(`^NAME\s+STATE\s+NODE$`))
				Expect(lines[2]).To(MatchRegexp(`^tuning\s+complete\s+node-1$`))
			})

			It("should fall back to the narrow row when no wide extractor is configured", func() {
				narrow := TableConfig[row]{
					Headers: []string{"NAME", "STATE"},
					Extract: cfg.Extract,
				}

				out := &bytes.Buffer{}
				Expect(OutputWide(out, narrow, rows)).To(Succeed())

				lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
				Expect(lines).To(HaveLen(4))
				Expect(lines[0]).To(MatchRegexp(`^NAME\s+STATE$`))
				Expect(lines[2]).To(MatchRegexp(`^tuning\s+complete$`))
				Expect(out.String()).ToNot(ContainSubstring("node-1"))
			})
		})

		Describe("OutputTableWithHeader", func() {
			It("should print the header line and a blank line above the table", func() {
				out := &bytes.Buffer{}
				Expect(OutputTableWithHeader(out, "NodeWright: demo", cfg, rows)).To(Succeed())

				lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
				Expect(lines[0]).To(Equal("NodeWright: demo"))
				Expect(lines[1]).To(BeEmpty())
				Expect(lines[2]).To(MatchRegexp(`^NAME\s+STATE$`))
				Expect(out.String()).ToNot(ContainSubstring("NODE"))
			})
		})

		Describe("OutputWideWithHeader", func() {
			It("should print the header line above the wide table", func() {
				out := &bytes.Buffer{}
				Expect(OutputWideWithHeader(out, "NodeWright: demo", cfg, rows)).To(Succeed())

				lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
				Expect(lines[0]).To(Equal("NodeWright: demo"))
				Expect(lines[1]).To(BeEmpty())
				Expect(lines[2]).To(MatchRegexp(`^NAME\s+STATE\s+NODE$`))
			})
		})
	})
})
