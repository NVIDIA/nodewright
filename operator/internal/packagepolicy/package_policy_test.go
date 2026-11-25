// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packagepolicy

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	shaGood  = "sha256:" + strings.Repeat("1", 64)
	shaOther = "sha256:" + strings.Repeat("2", 64)
	shaBad   = "sha256:" + strings.Repeat("b", 64)
)

const (
	driver  = "nvcr.io/nvidia/gpu-operator/driver"
	toolkit = "nvcr.io/nvidia/gpu-operator/toolkit"
	network = "nvcr.io/nvidia/network-operator/sriov"
	tuning  = "nvcr.io/nvidia/nodewright-packages/tuning"
)

func one(name string, p map[string]any) map[string]any {
	return map[string]any{name: p}
}

var _ = Describe("package policy", func() {
	Context("deny list only", Ordered, func() {
		const scenario = "denyonly"
		var dave client.Client

		create := func(image string, extra ...func(map[string]any)) error {
			return dave.Create(ctx, nodeWright(uniqueName(scenario), scenario, one("p", pkg(image, "1.0.0", extra...))))
		}

		BeforeAll(func() {
			installPolicy(scenario, renderPolicy(scenario, fmt.Sprintf(`
packagePolicy:
  enabled: true
  deny:
  - registries: ['docker\.io']
  - images: ['.*/shellscript']
  - shas: [%q]
`, shaBad)))
			dave = as("dave")
			waitForPolicy(dave, func() *unstructured.Unstructured {
				return nodeWright(uniqueName(scenario), scenario, one("p", pkg("ghcr.io/nvidia/nodewright-packages/shellscript", "1.0.0")))
			})
		})

		It("allows a package nothing denies", func() {
			Expect(create("nvcr.io/x/a")).To(Succeed())
		})

		DescribeTable("normalises image references as the runtime resolves them before matching registries",
			func(image string) { expectDenied(create(image)) },
			Entry("org/name", "myorg/pkg"),
			Entry("single segment", "ubuntu"),
			Entry("docker.io without library/", "docker.io/ubuntu"),
			Entry("index.docker.io", "index.docker.io/library/ubuntu"),
			Entry("registry-1.docker.io", "registry-1.docker.io/x/y"),
			Entry("registry.hub.docker.com", "registry.hub.docker.com/library/ubuntu"),
			Entry("default port", "docker.io:443/x/y"),
			Entry("upper-case host", "Docker.IO/x/y"),
		)

		It("treats a first segment that is not all lower case as a host, as the runtime does", func() {
			Expect(create("MyOrg/pkg")).To(Succeed())
		})

		DescribeTable("refuses an inline tag or digest in image without relying on the webhook",
			func(image string) { expectDenied(create(image)) },
			Entry("tag", "nvcr.io/x/a:1.2.3"),
			Entry("digest", "nvcr.io/x/a@"+shaGood),
			Entry("single segment with tag", "busybox:1"),
		)

		It("refuses a package whose name differs from its key without relying on the webhook", func() {
			p := pkg("nvcr.io/x/a", "1.0.0")
			p["name"] = "other"
			err := dave.Create(ctx, nodeWright(uniqueName(scenario), scenario, one("p", p)))
			expectDenied(err)
			Expect(err.Error()).To(ContainSubstring("Add p=nvcr.io/x/a:1.0.0"))
		})

		It("keeps a registry port, which is not a tag", func() {
			Expect(create("localhost:5000/x/a")).To(Succeed())
		})

		It("applies registry and image deny entries to agent overrides", func() {
			expectDenied(create("nvcr.io/x/a", withAgent("docker.io/evil/agent:1")))
			expectDenied(create("nvcr.io/x/a", withAgent("evil/agent:1")))
			expectDenied(create("nvcr.io/x/a", withAgent("ghcr.io/x/shellscript:1@"+shaGood)))
			Expect(create("nvcr.io/x/a", withAgent("ghcr.io/nvidia/nodewright/agent:v7.0.0"))).To(Succeed())
		})

		It("matches whole strings only", func() {
			Expect(create("docker.io.evil.example/x")).To(Succeed())
			expectDenied(create("ghcr.io/nvidia/nodewright-packages/shellscript"))
			Expect(create("ghcr.io/x/shellscript-extra")).To(Succeed())
		})

		It("denies by digest", func() {
			expectDenied(create("nvcr.io/x/a", withSHA(shaBad)))
			Expect(create("nvcr.io/x/a", withSHA(shaGood))).To(Succeed())
		})

		It("never blocks retiring a denied package that predates the policy, but refuses bringing it back", func() {
			cr := nodeWright(uniqueName(scenario), scenario, one("sh", pkg("ghcr.io/nvidia/nodewright-packages/shellscript", "1.0.0")))
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())

			expectDenied(mutate(dave, cr, setPackageField("sh", "1.0.1", "version")))
			Expect(mutate(dave, cr, setPackageField("sh", map[string]any{"enabled": true, "apply": true}, "uninstall"))).To(Succeed())
			// Cancelling an uninstall re-installs the package, so it is not retiring.
			expectDenied(mutate(dave, cr, setPackageField("sh", false, "uninstall", "apply")))
			expectDenied(mutate(dave, cr, setPackageField("sh", map[string]any{"enabled": true, "apply": false}, "uninstall")))
			expectDenied(mutate(dave, cr, removeUninstall("sh")))
			Expect(mutate(dave, cr, removePackage("sh"))).To(Succeed())
		})
	})

	Context("rules", Ordered, func() {
		const scenario = "rules"
		var dave, gpu client.Client

		create := func(c client.Client, pkgs map[string]any) error {
			return c.Create(ctx, nodeWright(uniqueName(scenario), scenario, pkgs))
		}
		created := func(c client.Client, pkgs map[string]any) *unstructured.Unstructured {
			cr := nodeWright(uniqueName(scenario), scenario, pkgs)
			Expect(c.Create(ctx, cr)).To(Succeed())
			return cr
		}

		BeforeAll(func() {
			installPolicy(scenario, renderPolicy(scenario, fmt.Sprintf(`
packagePolicy:
  enabled: true
  deny:
  - images: ['.*/shellscript']
  allow:
    platform-admins:
      subjects: [{kind: Group, name: platform-admins}]
      registries: ['nvcr\.io']
    gpu-driver:
      subjects: [{kind: ServiceAccount, namespace: gpu-operator, name: gpu-operator}]
      images: ['nvcr\.io/nvidia/gpu-operator/driver']
      versions: ['25\.\d+\.\d+']
    network:
      rbac: true
      images: ['nvcr\.io/nvidia/network-operator/.+']
    net-a: {rbac: true, images: ['nvcr\.io/nvidia/network-operator/.+']}
    net-b: {rbac: true, images: ['nvcr\.io/nvidia/network-operator/.+']}
    net-c: {rbac: true, images: ['nvcr\.io/nvidia/network-operator/.+']}
    tuning:
      anyone: true
      images: ['nvcr\.io/nvidia/nodewright-packages/tuning']
    pinned:
      subjects: [{kind: User, name: bob}]
      images: ['ghcr\.io/x/pinned']
      shas: [%q]
    digest:
      subjects: [{kind: User, name: bob, apiGroup: rbac.authorization.k8s.io}]
      images: ['ghcr\.io/x/digest']
      requireDigest: true
    agent:
      subjects: [{kind: User, name: bob}]
      images: ['ghcr\.io/x/agentpkg']
      agentImages: ['ghcr\.io/nvidia/nodewright/.+']
    retire:
      subjects: [{kind: User, name: kim}]
      images: ['ghcr\.io/x/retire']
      versions: ['2\..*']
    rita:
      subjects: [{kind: User, name: rita}]
      images: ['ghcr\.io/x/rita']
      operations: [Add, Remove]
    escaping:
      anyone: true
      images: ['ghcr\.io/x/q''"&\.a']
    ci:
      subjects: [{kind: Group, name: 'system:serviceaccounts:ci'}]
      images: ['ghcr\.io/x/ci']
    addonly:
      subjects: [{kind: User, name: carol}]
      images: ['ghcr\.io/x/carol']
      operations: [Add]
    both:
      subjects: [{kind: User, name: gina}]
      rbac: true
      images: ['ghcr\.io/x/both']
`, shaGood)))
			dave = as("dave")
			gpu = serviceAccount("gpu-operator", "gpu-operator")
			waitForPolicy(dave, func() *unstructured.Unstructured {
				return nodeWright(uniqueName(scenario), scenario, one("p", pkg("ghcr.io/x/other", "1.0.0")))
			})
		})

		It("names the requester and each refused change", func() {
			err := create(dave, one("p", pkg("ghcr.io/x/other", "1.0.0")))
			expectDenied(err)
			Expect(err.Error()).To(ContainSubstring("not permitted for dave: Add p=ghcr.io/x/other:1.0.0"))
		})

		It("grants a named ServiceAccount and checks versions", func() {
			Expect(create(gpu, one("d", pkg(driver, "25.3.0")))).To(Succeed())
			expectDenied(create(gpu, one("d", pkg(driver, "24.1.0"))))
			expectDenied(create(gpu, one("t", pkg(toolkit, "1.0.0"))))
		})

		It("grants a named group by registry", func() {
			alice := as("alice", "platform-admins")
			Expect(create(alice, one("p", pkg("nvcr.io/any/x", "1.0.0")))).To(Succeed())
			expectDenied(create(alice, one("p", pkg("ghcr.io/x/y", "1.0.0"))))
		})

		It("grants a ServiceAccount through its namespace group", func() {
			deployer := serviceAccount("ci", "deployer")
			Expect(create(deployer, one("p", pkg("ghcr.io/x/ci", "1.0.0")))).To(Succeed())
		})

		It("lets anyone use an anyone rule and denies what no rule covers", func() {
			Expect(create(dave, one("p", pkg(tuning, "1.0.0")))).To(Succeed())
			expectDenied(create(dave, one("p", pkg("ghcr.io/x/other", "1.0.0"))))
		})

		It("lets a deny entry win over a granted allow rule", func() {
			alice := as("alice", "platform-admins")
			expectDenied(create(alice, one("p", pkg("nvcr.io/x/shellscript", "1.0.0"))))
		})

		It("honours RBAC grants on rbac rules only", func() {
			erin := as("erin")
			err := create(erin, one("n", pkg(network, "1.0.0")))
			expectDenied(err)
			Expect(err.Error()).To(ContainSubstring("not permitted for erin without RBAC use on packagerules.packagepolicy.nodewright.nvidia.com: Add n=" + network + ":1.0.0 (net-a or net-b or net-c or network)"))
			grantUse("erin", "network", "gpu-driver")
			Eventually(func() error { return create(erin, one("n", pkg(network, "1.0.0"))) }, rbacWait, poll).Should(Succeed())
			// The access check docs/security/package-policy.md tells admins to run.
			sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
				User: "erin",
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Group: "packagepolicy.nodewright.nvidia.com", Resource: "packagerules", Name: "network", Verb: "use",
				},
			}}
			Expect(admin.Create(ctx, sar)).To(Succeed())
			Expect(sar.Status.Allowed).To(BeTrue())
			expectDenied(create(erin, one("d", pkg(driver, "25.3.0"))))
		})

		It("admits many packages under one rbac rule in a single write", func() {
			ivan := as("ivan")
			grantUse("ivan", "network")
			pkgs := map[string]any{}
			for i := range 5 {
				pkgs[fmt.Sprintf("n%d", i)] = pkg(network, "1.0.0")
			}
			Eventually(func() error { return create(ivan, pkgs) }, rbacWait, poll).Should(Succeed())
		})

		It("consults every rbac rule covering a package, granted only on the last", func() {
			judy := as("judy")
			grantUse("judy", "network")
			Eventually(func() error {
				return create(judy, map[string]any{"n0": pkg(network, "1.0.0"), "n1": pkg(network, "1.0.1")})
			}, rbacWait, poll).Should(Succeed())
		})

		It("grants every rbac rule to a role without resourceNames", func() {
			henry := as("henry")
			grantUse("henry")
			Eventually(func() error { return create(henry, one("n", pkg(network, "1.0.0"))) }, rbacWait, poll).Should(Succeed())
		})

		It("lets wildcard roles pass rbac rules, and only those", func() {
			frank := as("frank")
			bind("frank", "cluster-admin")
			Eventually(func() error { return create(frank, one("n", pkg(network, "1.0.0"))) }, rbacWait, poll).Should(Succeed())
			expectDenied(create(frank, one("d", pkg(driver, "25.3.0"))))
		})

		It("admits either subjects or RBAC on a rule with both", func() {
			Expect(create(as("gina"), one("b", pkg("ghcr.io/x/both", "1.0.0")))).To(Succeed())
			harry := as("harry")
			grantUse("harry", "both")
			Eventually(func() error { return create(harry, one("b", pkg("ghcr.io/x/both", "1.0.0"))) }, rbacWait, poll).Should(Succeed())
		})

		It("matches exact digests and requires one", func() {
			bob := as("bob")
			Expect(create(bob, one("p", pkg("ghcr.io/x/pinned", "1.0.0", withSHA(shaGood))))).To(Succeed())
			expectDenied(create(bob, one("p", pkg("ghcr.io/x/pinned", "1.0.0", withSHA(shaOther)))))
			expectDenied(create(bob, one("p", pkg("ghcr.io/x/pinned", "1.0.0"))))
		})

		It("requires a digest under requireDigest without shas", func() {
			bob := as("bob")
			expectDenied(create(bob, one("p", pkg("ghcr.io/x/digest", "1.0.0"))))
			Expect(create(bob, one("p", pkg("ghcr.io/x/digest", "1.0.0", withSHA(shaOther))))).To(Succeed())
		})

		It("allows agent overrides only through agentImages, and never past a deny entry", func() {
			bob := as("bob")
			Expect(create(bob, one("a", pkg("ghcr.io/x/agentpkg", "1.0.0", withAgent("ghcr.io/nvidia/nodewright/agent:v7.0.0"))))).To(Succeed())
			expectDenied(create(bob, one("a", pkg("ghcr.io/x/agentpkg", "1.0.0", withAgent("evil.example/agent:1")))))
			expectDenied(create(bob, one("a", pkg("ghcr.io/x/agentpkg", "1.0.0", withAgent("ghcr.io/nvidia/nodewright/shellscript:1.0.0")))))
			expectDenied(create(bob, one("p", pkg("ghcr.io/x/pinned", "1.0.0", withSHA(shaGood), withAgent("ghcr.io/nvidia/nodewright/agent:v7.0.0")))))
		})

		It("carries quotes, ampersands and backslashes in patterns into CEL intact", func() {
			Expect(create(dave, one("p", pkg(`ghcr.io/x/q'"&.a`, "1.0.0")))).To(Succeed())
			expectDenied(create(dave, one("p", pkg(`ghcr.io/x/q'"&xa`, "1.0.0"))))
		})

		It("gates each operation separately", func() {
			carol := as("carol")
			cr := created(carol, one("c", pkg("ghcr.io/x/carol", "1.0.0")))
			expectDenied(mutate(carol, cr, setPackageField("c", "1.0.1", "version")))
			expectDenied(mutate(carol, cr, removePackage("c")))
		})

		It("checks an image swap against the old image too", func() {
			cr := created(gpu, one("drv", pkg(driver, "25.1.0")))
			expectDenied(mutate(dave, cr, func(u *unstructured.Unstructured) {
				setPackageField("drv", tuning, "image")(u)
				setPackageField("drv", "1.0.0", "version")(u)
			}))
		})

		It("treats a non-package spec change as an update of every package", func() {
			cr := created(gpu, one("drv", pkg(driver, "25.1.0")))
			expectDenied(mutate(dave, cr, func(u *unstructured.Unstructured) {
				Expect(unstructured.SetNestedField(u.Object, map[string]any{"matchLabels": map[string]any{"a": "c"}}, "spec", "nodeSelectors")).To(Succeed())
			}))
			Expect(mutate(dave, cr, func(u *unstructured.Unstructured) {
				u.SetAnnotations(map[string]string{"nodewright.nvidia.com/pause": "true"})
			})).To(Succeed())
		})

		It("refuses an uninstall-only update of a covered package to a non-grantee", func() {
			cr := created(gpu, one("drv", pkg(driver, "25.1.0")))
			expectDenied(mutate(dave, cr, setPackageField("drv", map[string]any{"enabled": true, "apply": true}, "uninstall")))
		})

		It("keeps retiring a package with the rule that covers its image, whatever versions it now allows", func() {
			// ghcr.io/x/retire is covered by the version-narrowed retire rule alone,
			// so dave is refused only because that rule's images still cover 1.0.0.
			cr := nodeWright(uniqueName(scenario), scenario, one("r", pkg("ghcr.io/x/retire", "1.0.0")))
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			expectDenied(mutate(dave, cr, setPackageField("r", map[string]any{"enabled": true, "apply": true}, "uninstall")))
			expectDenied(mutate(dave, cr, removePackage("r")))
			kim := as("kim")
			Expect(mutate(kim, cr, setPackageField("r", map[string]any{"enabled": true, "apply": true}, "uninstall"))).To(Succeed())
			Expect(mutate(kim, cr, removePackage("r"))).To(Succeed())
		})

		It("does not evaluate packages a typed client's full Update leaves alone", func() {
			// A Go client sends every package with name filled from its key, and
			// struct fields such as interruptionBudget as {}, where an object
			// created from YAML stores neither. It also drops the empty lists a
			// YAML author may have written inside a struct field.
			net := pkg(network, "1.0.0")
			net["interrupt"] = map[string]any{"type": "reboot", "services": []any{}}
			cr := nodeWright(uniqueName(scenario), scenario, map[string]any{"drv": pkg(driver, "25.1.0"), "net": net})
			Expect(unstructured.SetNestedSlice(cr.Object, []any{}, "spec", "nodeSelectors", "matchExpressions")).To(Succeed())
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			typed := typedServiceAccount("gpu-operator", "gpu-operator")
			nw := &nwv1.NodeWright{}
			Expect(typed.Get(ctx, client.ObjectKeyFromObject(cr), nw)).To(Succeed())
			drv := nw.Spec.Packages["drv"]
			drv.Version = "25.2.0"
			nw.Spec.Packages["drv"] = drv
			Expect(typed.Update(ctx, nw)).To(Succeed())
		})

		It("refuses a typed client's full Update that rewrites a package it may not change, but not its merge patch", func() {
			// Go writes a duration of 5m back as 5m0s, so the full Update reads as
			// a change to net; a merge patch between two typed objects carries
			// only the version.
			net := pkg(network, "1.0.0")
			net["gracefulShutdown"] = "5m"
			cr := nodeWright(uniqueName(scenario), scenario, map[string]any{"drv": pkg(driver, "25.1.0"), "net": net})
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			typed := typedServiceAccount("gpu-operator", "gpu-operator")
			nw := &nwv1.NodeWright{}
			Expect(typed.Get(ctx, client.ObjectKeyFromObject(cr), nw)).To(Succeed())
			orig := nw.DeepCopy()
			drv := nw.Spec.Packages["drv"]
			drv.Version = "25.2.0"
			nw.Spec.Packages["drv"] = drv
			expectDenied(typed.Update(ctx, nw.DeepCopy()))
			Expect(typed.Patch(ctx, nw, client.MergeFrom(orig))).To(Succeed())
		})

		It("still treats clearing a field as a change", func() {
			p := pkg(driver, "25.1.0")
			p["configMap"] = map[string]any{"a": "b"}
			cr := nodeWright(uniqueName(scenario), scenario, one("drv", p))
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			expectDenied(mutate(dave, cr, setPackageField("drv", map[string]any{}, "configMap")))
			expectDenied(mutate(dave, cr, func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "packages", "drv", "configMap")
			}))
		})

		It("treats an empty value added to or removed from a package's configMap as a change", func() {
			p := pkg(driver, "25.1.0")
			p["configMap"] = map[string]any{"a": "b"}
			cr := nodeWright(uniqueName(scenario), scenario, one("drv", p))
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			expectDenied(mutate(dave, cr, setPackageField("drv", "", "configMap", "flag")))

			p = pkg(driver, "25.1.0")
			p["configMap"] = map[string]any{"a": "b", "flag": ""}
			cr = nodeWright(uniqueName(scenario), scenario, one("drv", p))
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			expectDenied(mutate(dave, cr, func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "packages", "drv", "configMap", "flag")
			}))
		})

		It("treats removing an empty-valued node selector label as a change to every package", func() {
			cr := nodeWright(uniqueName(scenario), scenario, one("drv", pkg(driver, "25.1.0")))
			Expect(unstructured.SetNestedStringMap(cr.Object, map[string]string{"a": "b", "approved": ""}, "spec", "nodeSelectors", "matchLabels")).To(Succeed())
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			expectDenied(mutate(dave, cr, func(u *unstructured.Unstructured) {
				unstructured.RemoveNestedField(u.Object, "spec", "nodeSelectors", "matchLabels", "approved")
			}))
		})

		It("lets a rule with Remove but not Update start an uninstall", func() {
			rita := as("rita")
			cr := created(rita, one("r", pkg("ghcr.io/x/rita", "1.0.0")))
			expectDenied(mutate(rita, cr, setPackageField("r", "1.0.1", "version")))
			Expect(mutate(rita, cr, setPackageField("r", map[string]any{"enabled": true, "apply": true}, "uninstall"))).To(Succeed())
			Expect(mutate(rita, cr, removePackage("r"))).To(Succeed())
		})

		It("does not evaluate packages that did not change", func() {
			cr := created(gpu, map[string]any{"drv": pkg(driver, "25.1.0"), "tun": pkg(tuning, "1.0.0")})
			Expect(mutate(dave, cr, setPackageField("tun", "1.0.1", "version"))).To(Succeed())
		})

		It("treats deleting a CR as removing every package in it", func() {
			cr := created(gpu, one("drv", pkg(driver, "25.1.0")))
			expectDenied(dave.Delete(ctx, cr))
			Expect(gpu.Delete(ctx, cr)).To(Succeed())
		})

		It("lets anyone retire a package no rule covers, but not change it or cancel its uninstall", func() {
			cr := nodeWright(uniqueName(scenario), scenario, one("old", pkg("ghcr.io/x/legacy", "1.0.0")))
			Expect(operatorSA(scenario).Create(ctx, cr)).To(Succeed())
			expectDenied(mutate(dave, cr, setPackageField("old", "1.0.1", "version")))
			Expect(mutate(dave, cr, setPackageField("old", map[string]any{"enabled": true, "apply": true}, "uninstall"))).To(Succeed())
			expectDenied(mutate(dave, cr, setPackageField("old", false, "uninstall", "apply")))
			expectDenied(mutate(dave, cr, removeUninstall("old")))
			Expect(mutate(dave, cr, removePackage("old"))).To(Succeed())
		})

		It("enforces legacy Skyhook creates too", func() {
			expectDenied(dave.Create(ctx, asSkyhook(nodeWright(uniqueName(scenario), scenario, one("p", pkg("ghcr.io/x/other", "1.0.0"))))))
			Expect(dave.Create(ctx, asSkyhook(nodeWright(uniqueName(scenario), scenario, one("p", pkg(tuning, "1.0.0")))))).To(Succeed())
		})

		It("exempts the operator's updates of legacy Skyhooks, even ones that change spec", func() {
			op := operatorSA(scenario)
			legacy := asSkyhook(nodeWright(uniqueName(scenario), scenario, one("old", pkg("ghcr.io/x/legacy", "1.0.0"))))
			Expect(op.Create(ctx, legacy)).To(Succeed())
			// Stands in for the operator removing the pre-rename finalizer: a full
			// Update of the Go object, whose re-serialized spec can differ from the
			// stored one.
			Expect(mutate(op, legacy, func(u *unstructured.Unstructured) {
				setPackageField("old", "old", "name")(u)
				u.SetFinalizers([]string{"skyhook.nvidia.com/skyhook"})
			})).To(Succeed())
			Expect(mutate(op, legacy, func(u *unstructured.Unstructured) { u.SetFinalizers(nil) })).To(Succeed())
		})

		It("does not evaluate deleting a legacy Skyhook, which removes nothing from nodes", func() {
			legacy := asSkyhook(nodeWright(uniqueName(scenario), scenario, one("drv", pkg(driver, "25.1.0"))))
			Expect(operatorSA(scenario).Create(ctx, legacy)).To(Succeed())
			Expect(dave.Delete(ctx, legacy)).To(Succeed())
		})

		It("exempts the operator's ServiceAccount from everything but changing a spec", func() {
			op := operatorSA(scenario)
			cr := nodeWright(uniqueName(scenario), scenario, one("drv", pkg(driver, "25.1.0")))
			Expect(op.Create(ctx, cr)).To(Succeed())
			Expect(mutate(op, cr, func(u *unstructured.Unstructured) {
				u.SetAnnotations(map[string]string{"nodewright.nvidia.com/mirrored-from": cr.GetName()})
				u.SetFinalizers([]string{"nodewright.nvidia.com/nodewright"})
			})).To(Succeed())
			// The migration mirror rewriting an existing NodeWright is the one
			// spec-changing write the operator makes; it is checked like any other.
			expectDenied(mutate(op, cr, setPackageField("drv", "26.0.0", "version")))
			expectDenied(mutate(op, cr, setPackageField("other", pkg("ghcr.io/x/anything", "1.0.0"))))
			Expect(mutate(op, cr, func(u *unstructured.Unstructured) { u.SetFinalizers(nil) })).To(Succeed())
			Expect(op.Delete(ctx, cr)).To(Succeed())
		})
	})

	Context("warn mode", Ordered, func() {
		const scenario = "warn"

		It("admits a violating write with a warning", func() {
			installPolicy(scenario, renderPolicy(scenario, `
packagePolicy:
  enabled: true
  mode: Warn
  deny:
  - images: ['.*/shellscript']
`))
			c, w := asWithWarnings("walter")
			Eventually(func() []string {
				Expect(c.Create(ctx, nodeWright(uniqueName(scenario), scenario, one("p", pkg("ghcr.io/nvidia/nodewright-packages/shellscript", "1.0.0"))))).To(Succeed())
				return w.all()
			}, rbacWait, poll).Should(ContainElement(ContainSubstring("not permitted for walter")))
		})
	})

	Context("audit mode", Ordered, func() {
		const scenario = "audit"

		It("admits a violating write", func() {
			installPolicy(scenario, renderPolicy(scenario, `
packagePolicy:
  enabled: true
  mode: Audit
  deny:
  - images: ['.*/shellscript']
`))
			// Audit leaves no trace a client can see, so first prove the policy is
			// enforced by a Deny binding added beside it, then remove that binding.
			denyBinding := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingAdmissionPolicyBinding",
				"metadata":   map[string]any{"name": scenario + "-probe"},
				"spec":       map[string]any{"policyName": scenario + "-package-policy", "validationActions": []any{"Deny"}},
			}}
			Expect(admin.Create(ctx, denyBinding)).To(Succeed())
			aud := as("audrey")
			mk := func() *unstructured.Unstructured {
				return nodeWright(uniqueName(scenario), scenario, one("p", pkg("ghcr.io/nvidia/nodewright-packages/shellscript", "1.0.0")))
			}
			waitForPolicy(aud, mk)
			Expect(admin.Delete(ctx, denyBinding)).To(Succeed())
			Eventually(func() error { return aud.Create(ctx, mk()) }, rbacWait, poll).Should(Succeed())
		})
	})

	Context("at scale", Ordered, func() {
		const scenario = "scale"

		It("evaluates 50 packages against 50 rules within the CEL cost budget", func() {
			var rules strings.Builder
			for i := range 50 {
				fmt.Fprintf(&rules, "    r%d: {anyone: true, images: ['ghcr\\.io/x/pkg%d']}\n", i, i)
			}
			installPolicy(scenario, renderPolicy(scenario, "packagePolicy:\n  enabled: true\n  allow:\n"+rules.String()))
			dave := as("dave")
			waitForPolicy(dave, func() *unstructured.Unstructured {
				return nodeWright(uniqueName(scenario), scenario, one("p", pkg("ghcr.io/x/other", "1.0.0")))
			})
			pkgs := map[string]any{}
			for i := range 50 {
				pkgs[fmt.Sprintf("p%d", i)] = pkg("ghcr.io/x/pkg49", "1.0.0")
			}
			Expect(dave.Create(ctx, nodeWright(uniqueName(scenario), scenario, pkgs))).To(Succeed())
		})
	})
})
