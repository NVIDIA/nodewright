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

package v1alpha1

import (
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NodeWright Webhook", func() {
	var nodewrightWebhook *NodeWrightWebhook

	BeforeEach(func() {
		nodewrightWebhook = &NodeWrightWebhook{
			Client: k8sClient,
		}
	})

	Context("When creating NodeWright under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {

			// TODO(user): Add your logic here

		})
	})

	Context("When creating NodeWright under Validating Webhook", func() {
		cyclicNodeWright := func() *NodeWright {
			return &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "legacy-dependency-cycle", Finalizers: []string{"nodewright.nvidia.com/nodewright"}},
				Spec: NodeWrightSpec{
					InterruptionBudget: InterruptionBudget{Percent: ptr.To(25)},
					Packages: Packages{
						"pkg-a": {PackageRef: PackageRef{Name: "pkg-a", Version: "1.2.3"}, Image: "example/a", DependsOn: map[string]string{"pkg-b": "2.1.4"}},
						"pkg-b": {PackageRef: PackageRef{Name: "pkg-b", Version: "2.1.4"}, Image: "example/b", DependsOn: map[string]string{"pkg-a": "1.2.3"}},
					},
				},
			}
		}

		DescribeTable("recovers an existing dependency cycle with admission enabled", func(deleting, repair bool) {
			configuration := &admissionv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: testEnv.WebhookInstallOptions.ValidatingWebhooks[0].Name}, configuration)).To(Succeed())
			original := configuration.DeepCopy()
			restore := func() {
				GinkgoHelper()
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(configuration), configuration)).To(Succeed())
				configuration.Webhooks = original.DeepCopy().Webhooks
				Expect(k8sClient.Update(ctx, configuration)).To(Succeed())
			}
			DeferCleanup(restore)
			// Seed a legacy object without cycle validation on CREATE, then restore
			// the real rules before exercising UPDATE and finalizer admission.
			for i := range configuration.Webhooks {
				if configuration.Webhooks[i].Name == "vnodewright.kb.io" {
					configuration.Webhooks[i].Rules[0].Operations = []admissionv1.OperationType{admissionv1.Update}
				}
			}
			Expect(k8sClient.Update(ctx, configuration)).To(Succeed())
			nodewright := cyclicNodeWright()
			Eventually(func() error { return k8sClient.Create(ctx, nodewright) }, "10s", "100ms").Should(Succeed())
			DeferCleanup(func() {
				remaining := &NodeWright{}
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(nodewright), remaining)
				Expect(client.IgnoreNotFound(err)).To(Succeed())
				if err == nil {
					delete(remaining.Spec.Packages["pkg-b"].DependsOn, "pkg-a")
					remaining.Finalizers = nil
					Expect(k8sClient.Update(ctx, remaining)).To(Succeed())
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, remaining))).To(Succeed())
				}
			})
			restore()
			// A fresh CREATE proves the restored webhook is active; UPDATE must now
			// accept unchanged packages, so it cannot serve as this readiness probe.
			Eventually(func() error {
				probe := cyclicNodeWright()
				probe.Name = "dependency-cycle-admission-probe"
				return k8sClient.Create(ctx, probe, client.DryRunAll)
			}, "10s", "100ms").Should(MatchError(ContainSubstring("dependency cycle")))
			if !repair {
				Expect(k8sClient.Patch(ctx, nodewright, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":["nodewright.nvidia.com/nodewright","nodewright.nvidia.com/test-cleanup"]}}`)))).To(Succeed())
			}
			if deleting {
				Expect(k8sClient.Delete(ctx, nodewright)).To(Succeed())
			}
			if repair {
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodewright), nodewright)).To(Succeed())
				delete(nodewright.Spec.Packages["pkg-b"].DependsOn, "pkg-a")
				Expect(k8sClient.Update(ctx, nodewright, client.DryRunAll)).To(Succeed())
				Expect(k8sClient.Update(ctx, nodewright)).To(Succeed())
			} else {
				for _, annotation := range []string{"pause", "disable", "version", "legacy-migrated-at"} {
					patch := `{"metadata":{"annotations":{"nodewright.nvidia.com/` + annotation + `":"true"}}}`
					Expect(k8sClient.Patch(ctx, nodewright, client.RawPatch(types.MergePatchType, []byte(patch)), client.DryRunAll)).To(Succeed())
					Expect(k8sClient.Patch(ctx, nodewright, client.RawPatch(types.MergePatchType, []byte(patch)))).To(Succeed())
				}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodewright), nodewright)).To(Succeed())
				Expect(nodewright.Spec.Packages).To(Equal(cyclicNodeWright().Spec.Packages))
			}
			Expect(k8sClient.Patch(ctx, nodewright, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`)))).To(Succeed())
			if deleting {
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodewright), &NodeWright{}))).To(BeTrue())
			} else {
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodewright), nodewright)).To(Succeed())
				Expect(nodewright.Finalizers).To(BeEmpty())
			}
		},
			Entry("metadata and finalizers before deletion", false, false),
			Entry("metadata and finalizers while terminating", true, false),
			Entry("repairs the graph before deletion", false, true),
			Entry("repairs the graph while terminating", true, true),
		)

		DescribeTable("rejects changed packages retaining a dependency cycle", func(change func(Packages)) {
			old := cyclicNodeWright()
			old.Spec.Packages["pkg-c"] = Package{PackageRef: PackageRef{Name: "pkg-c", Version: "1.0.0"}, Image: "example/c"}
			updated := old.DeepCopy()
			change(updated.Spec.Packages)
			Expect(updated.Spec.Packages).NotTo(Equal(old.Spec.Packages))
			Expect(updated.Validate()).To(MatchError(ContainSubstring("dependency cycle")))
			_, err := nodewrightWebhook.ValidateUpdate(ctx, old, updated)
			Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
		},
			Entry("image", func(packages Packages) {
				pkg := packages["pkg-a"]
				pkg.Image = "example/changed"
				packages["pkg-a"] = pkg
			}),
			Entry("version", func(packages Packages) {
				pkg := packages["pkg-a"]
				pkg.Version = "1.2.4"
				packages["pkg-a"] = pkg
				packages["pkg-b"].DependsOn["pkg-a"] = "1.2.4"
			}),
			Entry("uninstall", func(packages Packages) {
				pkg := packages["pkg-a"]
				pkg.Uninstall = &Uninstall{Enabled: true, Apply: true}
				packages["pkg-a"] = pkg
			}),
			Entry("dependsOn", func(packages Packages) {
				packages["pkg-a"].DependsOn["pkg-c"] = "1.0.0"
			}),
			Entry("unrelated package", func(packages Packages) {
				pkg := packages["pkg-c"]
				pkg.Image = "example/changed"
				packages["pkg-c"] = pkg
			}),
		)

		It("rejects a dependency cycle on create and update without an old object", func() {
			nodewright := cyclicNodeWright()
			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
			_, err = nodewrightWebhook.ValidateUpdate(ctx, nil, nodewright)
			Expect(err).To(MatchError(ContainSubstring("dependency cycle")))
		})

		It("allows valid non-package changes with an unchanged dependency cycle", func() {
			old := cyclicNodeWright()
			updated := old.DeepCopy()
			updated.Spec.InterruptionBudget.Percent = ptr.To(50)
			_, err := nodewrightWebhook.ValidateUpdate(ctx, old, updated)
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("preserves other validation with an unchanged dependency cycle", func(change func(*NodeWright), message string) {
			old := cyclicNodeWright()
			updated := old.DeepCopy()
			change(updated)
			Expect(updated.Spec.Packages).To(Equal(old.Spec.Packages))
			_, err := nodewrightWebhook.ValidateUpdate(ctx, old, updated)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
			Entry("interruption budget", func(n *NodeWright) {
				n.Spec.InterruptionBudget.Count = ptr.To(1)
			}, "interruption budget"),
			Entry("drain config", func(n *NodeWright) {
				n.Spec.DrainConfig = &DrainConfig{Timeout: &metav1.Duration{Duration: -time.Second}}
			}, "drain config"),
			Entry("node selector", func(n *NodeWright) {
				n.Spec.NodeSelector.MatchLabels = map[string]string{"-invalid": "value"}
			}, "node selectors are not valid"),
			Entry("pod selector", func(n *NodeWright) {
				n.Spec.PodNonInterruptLabels.MatchLabels = map[string]string{"-invalid": "value"}
			}, "pod non-interrupt labels are not valid"),
			Entry("mutually exclusive policy and budget", func(n *NodeWright) {
				n.Spec.DeploymentPolicy = "missing-cycle-policy"
			}, "deploymentPolicy and interruptionBudget are mutually exclusive"),
			Entry("missing policy", func(n *NodeWright) {
				n.Spec.InterruptionBudget = InterruptionBudget{}
				n.Spec.DeploymentPolicy = "missing-cycle-policy"
			}, `deploymentPolicy "missing-cycle-policy" not found`),
		)

		DescribeTable("preserves uninstall and downgrade gates after dependency cycle recovery", func(repair bool, operation string, apply bool, message string) {
			old := cyclicNodeWright()
			if !repair {
				delete(old.Spec.Packages["pkg-b"].DependsOn, "pkg-a")
			}
			old.Spec.Packages["pkg-c"] = Package{
				PackageRef: PackageRef{Name: "pkg-c", Version: "2.0.0"},
				Image:      "example/c",
				Uninstall:  &Uninstall{Enabled: true, Apply: apply},
			}
			old.Status.NodeState = map[string]NodeState{
				"node-1": {"pkg-c|2.0.0": {Name: "pkg-c", Version: "2.0.0", Stage: StageUninstall, State: StateInProgress}},
			}
			updated := old.DeepCopy()
			delete(updated.Spec.Packages["pkg-b"].DependsOn, "pkg-a")
			if operation == "remove" {
				delete(updated.Spec.Packages, "pkg-c")
			} else {
				pkg := updated.Spec.Packages["pkg-c"]
				pkg.Version = "1.0.0"
				pkg.Uninstall.Apply = true
				updated.Spec.Packages["pkg-c"] = pkg
			}
			Expect(updated.Validate()).To(Succeed())
			_, err := nodewrightWebhook.ValidateUpdate(ctx, old, updated)
			Expect(err).To(MatchError(ContainSubstring(message)))

			old.Status.NodeState = map[string]NodeState{"node-1": {}}
			pkg := old.Spec.Packages["pkg-c"]
			pkg.Uninstall.Apply = true
			old.Spec.Packages["pkg-c"] = pkg
			_, err = nodewrightWebhook.ValidateUpdate(ctx, old, updated)
			Expect(err).NotTo(HaveOccurred())
		},
			Entry("repair cannot remove an installed package", true, "remove", true, "wait for completion before removing from spec"),
			Entry("DAG cannot remove an installed package", false, "remove", true, "wait for completion before removing from spec"),
			Entry("repair cannot start uninstall and downgrade together", true, "downgrade", false, "set uninstall.apply=true first"),
			Entry("DAG cannot start uninstall and downgrade together", false, "downgrade", false, "set uninstall.apply=true first"),
			Entry("repair cannot downgrade before uninstall completes", true, "downgrade", true, "uninstall has not yet completed"),
			Entry("DAG cannot downgrade before uninstall completes", false, "downgrade", true, "uninstall has not yet completed"),
		)

		It("rejects dependency cycles on create and update through admission", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "dependency-cycle"},
				Spec: NodeWrightSpec{
					InterruptionBudget: InterruptionBudget{Percent: ptr.To(25)},
					Packages: Packages{
						"pkg-a": {PackageRef: PackageRef{Name: "pkg-a", Version: "1.2.3"}, Image: "ghcr.io/nvidia/skyhook/agentless", DependsOn: map[string]string{"pkg-b": "2.1.4"}},
						"pkg-b": {PackageRef: PackageRef{Name: "pkg-b", Version: "2.1.4"}, Image: "ghcr.io/nvidia/skyhook/agentless", DependsOn: map[string]string{"pkg-a": "1.2.3"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodewright)).To(MatchError(ContainSubstring("dependency cycle: pkg-a|1.2.3 -> pkg-b|2.1.4 -> pkg-a|1.2.3")))

			delete(nodewright.Spec.Packages["pkg-b"].DependsOn, "pkg-a")
			Expect(k8sClient.Create(ctx, nodewright)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, nodewright)).To(Succeed()) })
			pkg := nodewright.Spec.Packages["pkg-b"]
			pkg.DependsOn = map[string]string{"pkg-a": "1.2.3"}
			nodewright.Spec.Packages["pkg-b"] = pkg
			Expect(k8sClient.Update(ctx, nodewright)).To(MatchError(ContainSubstring("dependency cycle")))
		})

		It("Should deny if missing a depends on", func() {

			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foobar": {
							PackageRef: PackageRef{
								Name:    "foobar",
								Version: "1.0.0",
							},
							Image:     "ghcr.io/org/pkg",
							DependsOn: map[string]string{"CATS": "2.3"}, // missing
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateUpdate(ctx, nil, nodewright)
			Expect(err).ToNot(BeNil())

		})
		It("stays writable during the migration bridge (no legacy read-only rejection)", func() {
			// Regression guard: the legacy Skyhook/DeploymentPolicy are read-only during the
			// bridge, but NodeWright is the writable source of truth. The read-only check
			// (legacyReadOnlyError) lives only in the legacy api/v1alpha1 package and must
			// never be mirrored here, so a plain spec-changing update must be accepted.
			old := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "writable"},
				Spec: NodeWrightSpec{
					InterruptionBudget: InterruptionBudget{Percent: ptr.To(25)},
					Packages: Packages{
						"foobar": {
							PackageRef: PackageRef{Name: "foobar", Version: "1.0.0"},
							Image:      "ghcr.io/org/pkg",
						},
					},
				},
			}
			updated := old.DeepCopy()
			updated.Spec.InterruptionBudget.Percent = ptr.To(50)

			_, err := nodewrightWebhook.ValidateUpdate(ctx, old, updated)
			Expect(err).To(BeNil())
		})
		It("Should deny if duplicate packages", func() {

			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foobar": {
							PackageRef: PackageRef{
								Name:    "foobar",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
						"foobar2": {
							PackageRef: PackageRef{
								Name:    "foobar", // dup
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateUpdate(ctx, nil, nodewright)
			Expect(err).ToNot(BeNil())
		})

		It("Should deny if a package's name is explicitly set and changed", func() {

			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
						"bar": {
							PackageRef: PackageRef{
								Name:    "bar",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(BeNil())

			nodewright = &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "changed",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
						"bar": {
							PackageRef: PackageRef{
								Name:    "changed",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
					},
				},
			}

			_, err = nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).ToNot(BeNil())
		})

		It("Should deny an inline image tag even when it matches the version", func() {

			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "testing",
						},
					},
				},
			}

			// A bare image (no tag) is accepted.
			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(BeNil())

			// A tag is rejected even though it matches the version: version owns the tag,
			// it is not duplicated in image.
			nodewright.Spec.Packages["foo"] = Package{
				PackageRef: PackageRef{
					Name:    "foo",
					Version: "1.0.0",
				},
				Image: "testing:1.0.0",
			}

			_, err = nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("tag"))
			Expect(err.Error()).To(ContainSubstring("testing"))
		})

		It("Should allow registry ports in package images", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "localhost:5000/org/pkg",
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(BeNil())
		})

		It("Should deny an inline tag after a registry port but not the port itself", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "localhost:5000/org/pkg:1.0.0",
						},
					},
				},
			}

			// The colon in the registry port must not be mistaken for a tag separator:
			// only the trailing ":1.0.0" is the tag, and it is rejected.
			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("tag"))
			Expect(err.Error()).To(ContainSubstring("1.0.0"))
			Expect(err.Error()).To(ContainSubstring("localhost:5000/org/pkg"))
		})

		It("Should reject an inline image digest and point at containerSHA", func() {
			digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "localhost:5000/org/pkg@" + digest,
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("digest"))
			Expect(err.Error()).To(ContainSubstring("containerSHA"))
		})

		It("Should reject empty inline tag or digest separators", func() {
			mk := func(image string) *NodeWright {
				return &NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "test"},
					Spec: NodeWrightSpec{
						Packages: Packages{
							"foo": {
								PackageRef: PackageRef{Name: "foo", Version: "1.0.0"},
								Image:      image,
							},
						},
					},
				}
			}

			// A trailing separator with nothing after it is still invalid: it would
			// otherwise compose into a broken reference like "repo::1.0.0".
			_, err := nodewrightWebhook.ValidateCreate(ctx, mk("ghcr.io/org/pkg:"))
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("tag"))

			_, err = nodewrightWebhook.ValidateCreate(ctx, mk("ghcr.io/org/pkg@"))
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("digest"))
		})

		// Emptiness, whitespace, and digest format are enforced declaratively by the
		// CRD's OpenAPI Pattern (apiserver-side), so they are exercised through a real
		// create rather than a direct ValidateCreate call.
		It("Should reject an empty or whitespace image via the CRD schema", func() {
			mk := func(name, image string) *NodeWright {
				return &NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: NodeWrightSpec{
						Packages: Packages{
							"foo": {
								PackageRef: PackageRef{Name: "foo", Version: "1.0.0"},
								Image:      image,
							},
						},
					},
				}
			}

			Expect(k8sClient.Create(ctx, mk("empty-image", ""))).ToNot(Succeed())
			Expect(k8sClient.Create(ctx, mk("ws-image", "   "))).ToNot(Succeed())
			Expect(k8sClient.Create(ctx, mk("padded-image", " ghcr.io/org/pkg "))).ToNot(Succeed())
		})

		It("Should validate containerSHA format via the CRD schema", func() {
			mk := func(name, sha string) *NodeWright {
				return &NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: NodeWrightSpec{
						Packages: Packages{
							"foo": {
								PackageRef:   PackageRef{Name: "foo", Version: "1.0.0"},
								Image:        "ghcr.io/org/pkg",
								ContainerSHA: sha,
							},
						},
					},
				}
			}

			good := "sha256:" + "a" + strings.Repeat("b", 63)
			sh := mk("good-sha", good)
			Expect(k8sClient.Create(ctx, sh)).To(Succeed())
			Expect(k8sClient.Delete(ctx, sh)).To(Succeed())

			Expect(k8sClient.Create(ctx, mk("no-prefix", strings.Repeat("a", 64)))).ToNot(Succeed())
			Expect(k8sClient.Create(ctx, mk("short-sha", "sha256:abc"))).ToNot(Succeed())
			Expect(k8sClient.Create(ctx, mk("upper-sha", "sha256:"+strings.Repeat("A", 64)))).ToNot(Succeed())
		})

		It("should validate that the configInterrupts are for valid configMaps", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
							ConfigMap: map[string]string{
								"key": "value",
								"dog": "value",
							},
							ConfigInterrupts: map[string]Interrupt{
								"dog": {
									Type: REBOOT,
								},
							},
						},
						"bar": {
							PackageRef: PackageRef{
								Name:    "bar",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
							ConfigMap: map[string]string{
								"key": "value",
								"dog": "value",
							},
							ConfigInterrupts: map[string]Interrupt{
								"cat": {
									Type: REBOOT,
								},
							},
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateUpdate(ctx, nil, nodewright)
			Expect(err).ToNot(BeNil())

			nodewright = &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{
								Name:    "foo",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
							ConfigMap: map[string]string{
								"key": "value",
								"dog": "value",
							},
							ConfigInterrupts: map[string]Interrupt{
								"key": {
									Type:     SERVICE,
									Services: []string{"cron"},
								},
							},
						},
						"bar": {
							PackageRef: PackageRef{
								Name:    "bar",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
							ConfigMap: map[string]string{
								"key": "value",
								"dog": "value",
							},
							ConfigInterrupts: map[string]Interrupt{
								"key": {
									Type: REBOOT,
								},
								"dog": {
									Type:     SERVICE,
									Services: []string{"cat"},
								},
							},
						},
					},
				},
			}

			_, err = nodewrightWebhook.ValidateUpdate(ctx, nil, nodewright)
			Expect(err).To(BeNil())
		})

		It("should allow glob patterns in configInterrupts that match at least one key", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{Name: "foo", Version: "1.0.0"},
							Image:      "ghcr.io/org/pkg",
							ConfigMap: map[string]string{
								"config.sh":  "abc",
								"upgrade.sh": "def",
							},
							ConfigInterrupts: map[string]Interrupt{
								"*.sh": {Type: SERVICE, Services: []string{"kubelet"}},
							},
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(BeNil())
		})

		It("should reject glob patterns in configInterrupts that match no keys", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foo": {
							PackageRef: PackageRef{Name: "foo", Version: "1.0.0"},
							Image:      "ghcr.io/org/pkg",
							ConfigMap: map[string]string{
								"config.txt": "abc",
							},
							ConfigInterrupts: map[string]Interrupt{
								"*.sh": {Type: SERVICE, Services: []string{"kubelet"}},
							},
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).ToNot(BeNil())
		})

		It("Should deny if ambiguous version match", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"cats": {
							PackageRef: PackageRef{
								Name:    "cats",
								Version: "2.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
						"cats2": {
							PackageRef: PackageRef{
								Name:    "cats", // dup
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
						"foobar": {
							PackageRef: PackageRef{
								Name:    "foobar", // dup
								Version: "1.0.0",
							},
							Image:     "ghcr.io/org/pkg",
							DependsOn: map[string]string{"cats": "1.0.0"},
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateUpdate(ctx, nil, nodewright)
			Expect(err).ToNot(BeNil())
		})

		It("Should deny if invalid version string is provided", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"cats": {
							PackageRef: PackageRef{
								Name:    "cats",
								Version: "2.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
						"foobar": {
							PackageRef: PackageRef{
								Name:    "foobar", // dup
								Version: "2024/07/06",
							},
							Image:     "ghcr.io/org/pkg",
							DependsOn: map[string]string{"cats": "2.0.0"},
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).ToNot(BeNil())

			nodewright = &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"cats": {
							PackageRef: PackageRef{
								Name:    "cats",
								Version: "2.1.1",
							},
							Image: "ghcr.io/org/pkg",
						},
						"foobar": {
							PackageRef: PackageRef{
								Name:    "foobar", // dup
								Version: "2024.7.6",
							},
							Image:     "ghcr.io/org/pkg",
							DependsOn: map[string]string{"cats": "2.1.1"},
						},
					},
				},
			}

			_, err = nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(BeNil())
		})

		It("Should admit graph is valid", func() {

			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					Packages: Packages{
						"foobar": {
							PackageRef: PackageRef{
								Name:    "foobar",
								Version: "1.0.0",
							},
							Image: "ghcr.io/org/pkg",
						},
					},
				},
			}

			_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
			Expect(err).To(BeNil())
		})

		It("Should validate node selectors", func() {

			// https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set
			tests := []struct {
				labels map[string]string
				valid  bool
			}{
				{labels: map[string]string{"foo": ""}, valid: true},
				{labels: map[string]string{"foo": "bar"}, valid: true},
				{labels: map[string]string{"-foo": "bar"}, valid: false},
				{labels: map[string]string{"_foo": "bar"}, valid: false},
				{labels: map[string]string{"foo": "-bar"}, valid: false},
				{labels: map[string]string{"foo": "123123123112312312311231231231123123123112312312311231231231123123"}, valid: false},
			}

			for _, t := range tests {
				nodewright := &NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "test"},
					Spec: NodeWrightSpec{
						NodeSelector: metav1.LabelSelector{MatchLabels: t.labels},
					},
				}
				err := nodewright.Validate()

				if t.valid {
					Expect(err).To(BeNil())
				} else {
					Expect(err).ToNot(BeNil())
				}
			}

		})

		It("should validate resource override requirements", func() {
			basePkg := Package{
				PackageRef: PackageRef{Name: "foo", Version: "1.0.0"},
				Image:      "alpine",
			}
			mkNodeWright := func(res *ResourceRequirements) *NodeWright {
				return &NodeWright{
					ObjectMeta: metav1.ObjectMeta{Name: "test"},
					Spec: NodeWrightSpec{
						Packages: Packages{
							"foo": func() Package { p := basePkg; p.Resources = res; return p }(),
						},
					},
				}
			}

			// 1. All unset (valid)
			Expect(mkNodeWright(&ResourceRequirements{}).Validate()).To(Succeed())

			// 2. All set and valid
			res := &ResourceRequirements{
				CPURequest:    resource.MustParse("100m"),
				CPULimit:      resource.MustParse("200m"),
				MemoryRequest: resource.MustParse("128Mi"),
				MemoryLimit:   resource.MustParse("256Mi"),
			}
			Expect(mkNodeWright(res).Validate()).To(Succeed())

			// 3. Only some set (invalid). Use value copies (*res) per case so a mutation
			// does not leak into `res` and the later sub-cases.
			res3 := *res
			res3.CPULimit = resource.Quantity{} // unset
			Expect(mkNodeWright(&res3).Validate()).NotTo(Succeed())

			// 4. Limit < request (invalid)
			res4 := *res
			res4.CPULimit = resource.MustParse("50m")
			Expect(mkNodeWright(&res4).Validate()).NotTo(Succeed())
			res4 = *res
			res4.MemoryLimit = resource.MustParse("64Mi")
			Expect(mkNodeWright(&res4).Validate()).NotTo(Succeed())

			// 5. Negative or zero values (invalid)
			res5 := *res
			res5.CPURequest = resource.MustParse("0")
			Expect(mkNodeWright(&res5).Validate()).NotTo(Succeed())
			res5 = *res
			res5.MemoryLimit = resource.MustParse("-1Mi")
			Expect(mkNodeWright(&res5).Validate()).NotTo(Succeed())
		})

		It("should validate drain config durations", func() {
			nodewright := &NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: NodeWrightSpec{
					DrainConfig: &DrainConfig{
						Timeout:     &metav1.Duration{Duration: time.Minute},
						GracePeriod: &metav1.Duration{Duration: 30 * time.Second},
					},
				},
			}
			Expect(nodewright.Validate()).To(Succeed())

			nodewright.Spec.DrainConfig.Timeout = &metav1.Duration{Duration: -time.Second}
			Expect(nodewright.Validate()).NotTo(Succeed())

			nodewright.Spec.DrainConfig.Timeout = nil
			nodewright.Spec.DrainConfig.GracePeriod = &metav1.Duration{Duration: -time.Second}
			Expect(nodewright.Validate()).NotTo(Succeed())
		})
	})

	It("packages should UnmarshalJSON without rewriting the image", func() {
		// The pre-semver tag-absorb migration was removed: unmarshalling no longer
		// rewrites image, it only defaults the package name from the map key. An
		// embedded tag survives here and is rejected later by the webhook.
		js := `{"foo": {"version":"1.2.2", "image":"bar:1.2.2"}}`

		var ret Packages
		Expect(json.Unmarshal([]byte(js), &ret)).Should(Succeed())

		Expect(ret["foo"].Name).To(Equal("foo"))
		Expect(ret["foo"].Image).To(Equal("bar:1.2.2"))
	})

	It("should validate the package name is a valid RFC 1123 name", func() {

		nodewright := &NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: NodeWrightSpec{
				Packages: Packages{
					"11": {
						PackageRef: PackageRef{Version: "1"},
						Image:      "ghcr.io/org/pkg",
					},
				},
			},
		}

		_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
		Expect(err).ToNot(BeNil())

	})

	It("should validate the deployment policy", func() {
		nodewright := &NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: NodeWrightSpec{
				DeploymentPolicy: "foobar",
				InterruptionBudget: InterruptionBudget{
					Percent: ptr.To(25),
				},
			},
		}

		_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deploymentPolicy and interruptionBudget are mutually exclusive"))
	})

	It("should reject nodewright with non-existent deployment policy", func() {
		nodewright := &NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodewright",
			},
			Spec: NodeWrightSpec{
				DeploymentPolicy: "non-existent-policy",
				Packages: Packages{
					"test-pkg": {
						PackageRef: PackageRef{
							Name:    "test-pkg",
							Version: "1.0.0",
						},
						Image: "ghcr.io/org/pkg",
					},
				},
			},
		}

		_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deploymentPolicy \"non-existent-policy\" not found"))
	})

	It("should accept nodewright when deployment policy exists", func() {
		// Create a cluster-scoped deployment policy first
		policy := &DeploymentPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-policy-webhook",
			},
			Spec: DeploymentPolicySpec{
				Default: PolicyDefault{
					Budget: DeploymentBudget{
						Percent: ptr.To(100),
					},
					Strategy: &DeploymentStrategy{
						Fixed: &FixedStrategy{},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		nodewright := &NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodewright-valid",
			},
			Spec: NodeWrightSpec{
				DeploymentPolicy: "test-policy-webhook",
				Packages: Packages{
					"test-pkg": {
						PackageRef: PackageRef{
							Name:    "test-pkg",
							Version: "1.0.0",
						},
						Image: "ghcr.io/org/pkg",
					},
				},
			},
		}

		_, err := nodewrightWebhook.ValidateCreate(ctx, nodewright)
		Expect(err).NotTo(HaveOccurred())

		// Cleanup
		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
	})

	It("should reject nodewright update to reference non-existent deployment policy", func() {
		// Create a NodeWright without deployment policy
		nodewright := &NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodewright-update",
			},
			Spec: NodeWrightSpec{
				Packages: Packages{
					"test-pkg": {
						PackageRef: PackageRef{
							Name:    "test-pkg",
							Version: "1.0.0",
						},
						Image: "ghcr.io/org/pkg",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodewright)).To(Succeed())

		// Try to update it to reference a non-existent policy
		updatedNodeWright := nodewright.DeepCopy()
		updatedNodeWright.Spec.DeploymentPolicy = "does-not-exist"

		_, err := nodewrightWebhook.ValidateUpdate(ctx, nodewright, updatedNodeWright)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deploymentPolicy \"does-not-exist\" not found"))

		// Cleanup
		Expect(k8sClient.Delete(ctx, nodewright)).To(Succeed())
	})

	It("should allow nodewright update to reference valid deployment policy", func() {
		// Create a deployment policy
		policy := &DeploymentPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "valid-policy-for-update",
			},
			Spec: DeploymentPolicySpec{
				Default: PolicyDefault{
					Budget: DeploymentBudget{
						Percent: ptr.To(100),
					},
					Strategy: &DeploymentStrategy{
						Fixed: &FixedStrategy{},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		// Create a NodeWright without deployment policy
		nodewright := &NodeWright{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodewright-update-valid",
			},
			Spec: NodeWrightSpec{
				Packages: Packages{
					"test-pkg": {
						PackageRef: PackageRef{
							Name:    "test-pkg",
							Version: "1.0.0",
						},
						Image: "ghcr.io/org/pkg",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodewright)).To(Succeed())

		// Update it to reference the valid policy - should succeed
		updatedNodeWright := nodewright.DeepCopy()
		updatedNodeWright.Spec.DeploymentPolicy = "valid-policy-for-update"

		_, err := nodewrightWebhook.ValidateUpdate(ctx, nodewright, updatedNodeWright)
		Expect(err).NotTo(HaveOccurred())

		// Cleanup
		Expect(k8sClient.Delete(ctx, nodewright)).To(Succeed())
		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
	})
})
