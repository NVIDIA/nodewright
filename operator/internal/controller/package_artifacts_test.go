// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
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

package controller

import (
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	artifactclient "github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/artifact/preflight"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The helpers of package artifacts and the planning of the preflight stage, in isolation. No spec
// shares the suite's manager or apiserver.
var _ = Describe("package artifact preflight", func() {
	const (
		namespace      = "nodewright"
		serviceAccount = "nodewright-package"
		nodewrightName = "artifacts"
	)

	digest := "sha256:" + strings.Repeat("a", 64)
	pinned := "registry.example/tuning@" + digest

	It("cuts the package action of a Warning to 128 bytes and leaves a shorter one whole", func() {
		short := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"}}
		Expect(packageEventAction(short)).To(Equal("tuning:1.0.0"))

		long := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{
			Name:    strings.Repeat("n", 43),
			Version: "1.0.0-" + strings.Repeat("r", 100),
		}}
		full := long.Name + ":" + long.Version
		Expect(len(full)).To(BeNumerically(">", 128))
		Expect(packageEventAction(long)).To(HaveLen(128))
		Expect(packageEventAction(long)).To(Equal(full[:128]))
	})

	packageOf := func(image, version, containerSHA string) *v1alpha1.Package {
		return &v1alpha1.Package{
			PackageRef:   v1alpha1.PackageRef{Name: "tuning", Version: version},
			Image:        image,
			ContainerSHA: containerSHA,
		}
	}

	DescribeTable("packageReference names the image at its containerSHA, or else at its version",
		func(image, version, containerSHA, want string) {
			reference, err := packageReference(packageOf(image, version, containerSHA))
			Expect(err).NotTo(HaveOccurred())
			Expect(reference).To(Equal(want))
		},
		Entry("a tag", "ghcr.io/nvidia/tuning", "1.0.0", "", "ghcr.io/nvidia/tuning:1.0.0"),
		Entry("a containerSHA, whatever the version", "ghcr.io/nvidia/tuning", "1.0.0", digest, "ghcr.io/nvidia/tuning@"+digest),
		Entry("a Docker Hub image, normalised as the kubelet normalises it", "alpine", "3.20", "", "docker.io/library/alpine:3.20"),
	)

	DescribeTable("packageReference refuses a reference no registry could serve, naming the package",
		func(image, version, containerSHA string) {
			_, err := packageReference(packageOf(image, version, containerSHA))
			Expect(err).To(MatchError(artifactclient.ErrInvalidReference))
			Expect(err).To(MatchError(ContainSubstring("tuning|" + version)))
		},
		Entry("an image with a tag of its own", "ghcr.io/nvidia/tuning:latest", "1.0.0", ""),
		Entry("a version that is no OCI tag", "ghcr.io/nvidia/tuning", "1.0.0+build", ""),
		Entry("a containerSHA that is no digest", "ghcr.io/nvidia/tuning", "1.0.0", "sha256:abc"),
	)

	DescribeTable("preflightResultFrom records the reference and every field of an outcome",
		func(outcome preflight.Outcome, want *v1alpha1.PreflightResult) {
			want.Reference = "registry.example/tuning:1.0.0"
			Expect(preflightResultFrom("registry.example/tuning:1.0.0", outcome)).To(Equal(want))
		},
		Entry("an artifact", preflight.Outcome{Kind: preflight.KindArtifact, Pinned: pinned, Uninstall: artifact.UninstallUnsupported},
			&v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "unsupported"}),
		Entry("a container image", preflight.Outcome{Kind: preflight.KindImage, Pinned: pinned},
			&v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}),
		Entry("a rejected artifact", preflight.Outcome{Kind: preflight.KindRejected, Reason: preflight.ReasonHostFiles},
			&v1alpha1.PreflightResult{Kind: "rejected", Reason: "host-files"}),
		Entry("a reference that could not be inspected", preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorUnauthorized},
			&v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "unauthorized"}),
	)

	DescribeTable("preflightAccepted and preflightUninstallUnsupported",
		func(result *v1alpha1.PreflightResult, accepted, unsupported bool) {
			Expect(preflightAccepted(result)).To(Equal(accepted))
			Expect(preflightUninstallUnsupported(result)).To(Equal(unsupported))
		},
		Entry("no result, as for a package installed with the option off", nil, false, false),
		Entry("a container image", &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}, false, false),
		Entry("an artifact declaring uninstall supported", &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}, true, false),
		Entry("an artifact declaring uninstall unsupported", &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "unsupported"}, true, true),
		Entry("a rejected artifact", &v1alpha1.PreflightResult{Kind: "rejected", Reason: "host-files"}, false, false),
		Entry("a reference that could not be inspected", &v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "timeout"}, false, false),
	)

	Describe("packagePullSecrets", func() {
		account := func(namespace string, pullSecrets ...string) *corev1.ServiceAccount {
			sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: serviceAccount}}
			for _, name := range pullSecrets {
				sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: name})
			}
			return sa
		}

		reconcilerWith := func(objects ...client.Object) *SkyhookReconciler {
			GinkgoHelper()
			scheme := runtime.NewScheme()
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), &events.FakeRecorder{}, SkyhookOperatorOptions{
				Namespace:             namespace,
				PackageServiceAccount: serviceAccount,
				CopyDirRoot:           "/var/lib/skyhook",
				AgentLogRoot:          "/var/log/skyhook",
				RuntimeRequiredTaint:  "skyhook.nvidia.com=runtime-required:NoSchedule",
				AgentImage:            "ghcr.io/nvidia/nodewright/agent:1.2.3",
				PauseImage:            "registry.k8s.io/pause:3.10",
				MaxInterval:           10 * time.Minute,
				JobOperatorOptions:    JobOperatorOptions{JobTTLSucceeded: time.Hour, JobTTLFailed: 24 * time.Hour, JobStageTimeout: time.Hour},
			})
			Expect(err).NotTo(HaveOccurred())
			return r
		}

		It("returns the imagePullSecrets of the package ServiceAccount, in order", func() {
			r := reconcilerWith(account(namespace, "regcred", "mirror"))
			Expect(r.packagePullSecrets(ctx)).To(Equal([]string{"regcred", "mirror"}))
		})

		It("returns none for a ServiceAccount that lists none", func() {
			r := reconcilerWith(account(namespace))
			Expect(r.packagePullSecrets(ctx)).To(BeEmpty())
		})

		It("reads the ServiceAccount of the operator namespace only", func() {
			r := reconcilerWith(account(namespace, "regcred"), account("default", "elsewhere"))
			Expect(r.packagePullSecrets(ctx)).To(Equal([]string{"regcred"}))
		})

		It("fails, naming the ServiceAccount, when it does not exist", func() {
			r := reconcilerWith(account("default", "elsewhere"))
			_, err := r.packagePullSecrets(ctx)
			Expect(err).To(MatchError(ContainSubstring(namespace + "/" + serviceAccount)))
		})
	})

	type entry struct {
		pkg    v1alpha1.Package
		stage  v1alpha1.Stage
		state  v1alpha1.State
		result *v1alpha1.PreflightResult
	}

	packageNamed := func(name, version string) v1alpha1.Package {
		return v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: "registry.example/" + name}
	}
	tuningAt := func(version string) v1alpha1.Package {
		return v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: version}, Image: "registry.example/tuning"}
	}

	nodeWrightOf := func(packages ...v1alpha1.Package) *v1alpha1.NodeWright {
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: nodewrightName, Generation: 2},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
		}
		for _, p := range packages {
			nw.Spec.Packages[p.Name] = p
		}
		return nw
	}

	// nodeOf returns a Node whose state holds every entry, each with its preflight result.
	nodeOf := func(name string, entries ...entry) *corev1.Node {
		GinkgoHelper()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		sn, err := wrapper.NewSkyhookNodeOnly(node, nodewrightName)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(sn.Upsert(e.pkg.PackageRef, e.pkg.Image, e.state, e.stage, 0, e.pkg.ContainerSHA)).To(Succeed())
			Expect(sn.SetPreflight(e.pkg.PackageRef, e.result)).To(Succeed())
		}
		return node
	}

	stateOf := func(nw *v1alpha1.NodeWright, nodes ...*corev1.Node) SkyhookNodes {
		GinkgoHelper()
		list := &corev1.NodeList{}
		for _, node := range nodes {
			list.Items = append(list.Items, *node)
		}
		cs, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, list, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		return cs.skyhooks[0]
	}

	Describe("updatePackageArtifactRejectedCondition", func() {
		rejected := func(reason string) *v1alpha1.PreflightResult {
			return &v1alpha1.PreflightResult{Kind: "rejected", Reason: reason}
		}
		conditionOf := func(sn SkyhookNodes) *metav1.Condition {
			return meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionPackageArtifactRejected)
		}

		It("names one package some node rejected, with the reason", func() {
			tuning := tuningAt("1.0.0")
			sn := stateOf(nodeWrightOf(tuning), nodeOf("node-a", entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateErroring, rejected("host-files")}))

			updatePackageArtifactRejectedCondition(sn)
			condition := conditionOf(sn)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("ArtifactRejected"))
			Expect(condition.ObservedGeneration).To(Equal(int64(2)))
			Expect(condition.Message).To(Equal("1 package artifact rejected: tuning:1.0.0 (host-files)"))
		})

		It("names each spec package once, sorted, however many nodes rejected it", func() {
			a, b, c := packageNamed("a", "1.0.0"), packageNamed("b", "2.0.0"), packageNamed("c", "1.0.0")
			accepted := &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}
			sn := stateOf(nodeWrightOf(a, b, c),
				nodeOf("node-a",
					entry{b, v1alpha1.StagePreflight, v1alpha1.StateErroring, rejected("interrupt")},
					entry{a, v1alpha1.StagePreflight, v1alpha1.StateErroring, rejected("host-files")},
					entry{c, v1alpha1.StageApply, v1alpha1.StateComplete, accepted}),
				nodeOf("node-b", entry{a, v1alpha1.StagePreflight, v1alpha1.StateErroring, rejected("host-files")}))

			updatePackageArtifactRejectedCondition(sn)
			Expect(conditionOf(sn)).To(HaveField("Message", "2 package artifacts rejected: a:1.0.0 (host-files), b:2.0.0 (interrupt)"))
		})

		It("names at most ReadyConditionNodeListLimit packages and counts the rest", func() {
			var packages []v1alpha1.Package
			var entries []entry
			for i := range wrapper.ReadyConditionNodeListLimit + 2 {
				p := packageNamed(fmt.Sprintf("p-%02d", i), "1.0.0")
				packages = append(packages, p)
				entries = append(entries, entry{p, v1alpha1.StagePreflight, v1alpha1.StateErroring, rejected("host-files")})
			}
			sn := stateOf(nodeWrightOf(packages...), nodeOf("node-a", entries...))

			updatePackageArtifactRejectedCondition(sn)
			listed := make([]string, 0, wrapper.ReadyConditionNodeListLimit)
			for _, p := range packages[:wrapper.ReadyConditionNodeListLimit] {
				listed = append(listed, p.Name+":1.0.0 (host-files)")
			}
			Expect(conditionOf(sn)).To(HaveField("Message",
				fmt.Sprintf("%d package artifacts rejected: %s, and 2 more", len(packages), strings.Join(listed, ", "))))
		})

		DescribeTable("is removed when no node holds a rejected result for a spec package",
			func(entries ...entry) {
				tuning := tuningAt("1.0.0")
				nw := nodeWrightOf(tuning)
				nw.Status.Conditions = []metav1.Condition{{
					Type: wrapper.SkyhookConditionPackageArtifactRejected, Status: metav1.ConditionTrue,
					Reason: wrapper.SkyhookReasonArtifactRejected, Message: "stale", LastTransitionTime: metav1.Now(),
				}}
				sn := stateOf(nw, nodeOf("node-a", entries...))

				updatePackageArtifactRejectedCondition(sn)
				Expect(conditionOf(sn)).To(BeNil())
				Expect(sn.GetSkyhook().Updated).To(BeTrue())
			},
			Entry("once a rerun preflight accepted it", entry{tuningAt("1.0.0"), v1alpha1.StagePreflight, v1alpha1.StateComplete,
				&v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}}),
			Entry("before its preflight has reported", entry{tuningAt("1.0.0"), v1alpha1.StagePreflight, v1alpha1.StateInProgress, nil}),
			Entry("when only a version the spec no longer names was rejected", entry{tuningAt("0.9.0"), v1alpha1.StagePreflight,
				v1alpha1.StateErroring, &v1alpha1.PreflightResult{Kind: "rejected", Reason: "host-files"}}),
		)
	})

	Describe("applyPackageStage", func() {
		older := tuningAt("1.0.0")
		tuning := tuningAt("2.0.0")
		const reference, other = "registry.example/tuning:2.0.0", "registry.example/old-tuning:2.0.0"
		imageAt := func(reference string) *v1alpha1.PreflightResult {
			return &v1alpha1.PreflightResult{Reference: reference, Kind: "image", Pinned: pinned}
		}

		DescribeTable("starts an install at preflight with the option on, and takes a complete preflight to apply or upgrade",
			func(packageArtifacts bool, want v1alpha1.Stage, entries ...entry) {
				nw := nodeWrightOf(tuning)
				sn, err := wrapper.NewSkyhookNode(nodeOf("node-a", entries...), nw)
				Expect(err).NotTo(HaveOccurred())
				Expect(applyPackageStage(sn, &tuning, packageArtifacts)).To(Equal(want))
			},
			Entry("a first install, with the option on", true, v1alpha1.StagePreflight),
			Entry("a first install, with the option off", false, v1alpha1.StageApply),
			Entry("a preflight in progress", true, v1alpha1.StagePreflight,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateInProgress, nil}),
			Entry("a preflight held erroring by a rejection", true, v1alpha1.StagePreflight,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateErroring, &v1alpha1.PreflightResult{Kind: "rejected", Reason: "host-files"}}),
			Entry("a complete preflight", true, v1alpha1.StageApply,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, imageAt(reference)}),
			Entry("a complete preflight, with the option since turned off", false, v1alpha1.StageApply,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, imageAt(reference)}),
			Entry("a complete preflight beside a lower version", true, v1alpha1.StageUpgrade,
				entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil},
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, imageAt(reference)}),
			Entry("an install begun with the option off, which runs no preflight once it is on", true, v1alpha1.StageConfig,
				entry{tuning, v1alpha1.StageApply, v1alpha1.StateComplete, nil}),
		)

		DescribeTable("runs preflight again for an image or unknown result found at another reference, until apply or upgrade completes",
			func(packageArtifacts bool, want v1alpha1.Stage, entries ...entry) {
				nw := nodeWrightOf(tuning)
				sn, err := wrapper.NewSkyhookNode(nodeOf("node-a", entries...), nw)
				Expect(err).NotTo(HaveOccurred())
				Expect(applyPackageStage(sn, &tuning, packageArtifacts)).To(Equal(want))
			},
			Entry("a complete preflight", true, v1alpha1.StagePreflight,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, imageAt(other)}),
			Entry("a complete preflight that could not inspect its reference", true, v1alpha1.StagePreflight,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, &v1alpha1.PreflightResult{Reference: other, Kind: "unknown", ErrorClass: "not-found"}}),
			Entry("a complete preflight, with the option off", false, v1alpha1.StageApply,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, imageAt(other)}),
			Entry("an apply in progress", true, v1alpha1.StagePreflight,
				entry{tuning, v1alpha1.StageApply, v1alpha1.StateInProgress, imageAt(other)}),
			Entry("an erroring upgrade beside a lower version", true, v1alpha1.StagePreflight,
				entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil},
				entry{tuning, v1alpha1.StageUpgrade, v1alpha1.StateErroring, imageAt(other)}),
			Entry("not once apply has completed", true, v1alpha1.StageConfig,
				entry{tuning, v1alpha1.StageApply, v1alpha1.StateComplete, imageAt(other)}),
			Entry("not at config", true, v1alpha1.StageConfig,
				entry{tuning, v1alpha1.StageConfig, v1alpha1.StateInProgress, imageAt(other)}),
			Entry("not for an accepted artifact, whose Jobs are pinned to its digest", true, v1alpha1.StageApply,
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete,
					&v1alpha1.PreflightResult{Reference: other, Kind: "artifact", Pinned: pinned, Uninstall: "supported"}}),
		)

		DescribeTable("takes an invalid reference, recorded as none, for the same reference while it stays invalid",
			func(image string, want v1alpha1.Stage) {
				invalid := tuning
				invalid.Image = image
				sn, err := wrapper.NewSkyhookNode(nodeOf("node-a", entry{invalid, v1alpha1.StagePreflight, v1alpha1.StateComplete,
					&v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "invalid-reference"}}), nodeWrightOf(invalid))
				Expect(err).NotTo(HaveOccurred())
				Expect(applyPackageStage(sn, &invalid, true)).To(Equal(want))
			},
			Entry("still invalid", "registry.example/tuning:latest", v1alpha1.StageApply),
			Entry("corrected", "registry.example/tuning", v1alpha1.StagePreflight),
		)
	})

	Describe("HandleVersionChange", func() {
		older := tuningAt("1.0.0")
		tuning := tuningAt("2.0.0")

		DescribeTable("starts an upgrade at preflight with the option on, and at upgrade with it off",
			func(packageArtifacts bool, want v1alpha1.Stage) {
				sn := stateOf(nodeWrightOf(tuning), nodeOf("node-a", entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil}))

				_, err := HandleVersionChange(sn, packageArtifacts)
				Expect(err).NotTo(HaveOccurred())
				_, node := sn.GetNode("node-a")
				status, found := node.PackageStatus(tuning.GetUniqueName())
				Expect(found).To(BeTrue())
				Expect(status.Stage).To(Equal(want))
				Expect(status.State).To(Equal(v1alpha1.StateInProgress))
				Expect(status.Preflight).To(BeNil())
				_, found = node.PackageStatus(older.GetUniqueName())
				Expect(found).To(BeTrue(), "the old version stays until its upgrade completes")
			},
			Entry("on", true, v1alpha1.StagePreflight),
			Entry("off", false, v1alpha1.StageUpgrade),
		)

		It("leaves an upgrade already at preflight, with its result, alone", func() {
			result := &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}
			sn := stateOf(nodeWrightOf(tuning), nodeOf("node-a",
				entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil},
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, result}))

			_, err := HandleVersionChange(sn, true)
			Expect(err).NotTo(HaveOccurred())
			_, node := sn.GetNode("node-a")
			Expect(node.Changed()).To(BeFalse())
			status, _ := node.PackageStatus(tuning.GetUniqueName())
			Expect(status.Stage).To(Equal(v1alpha1.StagePreflight))
			Expect(status.State).To(Equal(v1alpha1.StateComplete))
			Expect(status.Preflight).To(Equal(result))
		})
	})
})
