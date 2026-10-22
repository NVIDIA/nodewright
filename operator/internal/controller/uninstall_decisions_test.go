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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The uninstall of a package decided from the preflight results its nodes recorded: which node
// holds an uninstall back, how the functions that plan uninstalls read that per node, the
// decisions of a pass, the UninstallUnsupported Warning and condition, and what the deletion of a
// NodeWright waits on. No spec shares the suite's manager or apiserver.
var _ = Describe("uninstall decisions", func() {
	const (
		nodewrightName = "decisions"
		remedy         = "Set uninstall.apply or uninstall.enabled to false, or upgrade the package to a version whose artifact declares uninstall supported."
	)

	var (
		spec        = wrapper.UninstallDecisionSpec
		unsupported = wrapper.UninstallDecisionUnsupported
	)

	pinned := "registry.example/tuning@sha256:" + strings.Repeat("a", 64)
	supportedArtifact := &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}
	unsupportedArtifact := &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "unsupported"}
	image := &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}

	applied := func() *v1alpha1.Uninstall { return &v1alpha1.Uninstall{Enabled: true, Apply: true} }
	enabled := func() *v1alpha1.Uninstall { return &v1alpha1.Uninstall{Enabled: true} }

	packageOf := func(name, version string, uninstall *v1alpha1.Uninstall) v1alpha1.Package {
		return v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{Name: name, Version: version},
			Image:      "registry.example/" + name,
			Uninstall:  uninstall,
		}
	}

	nodeWrightOf := func(deleting bool, packages ...v1alpha1.Package) *v1alpha1.NodeWright {
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: nodewrightName, UID: "decisions-uid", Generation: 3},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
		}
		if deleting {
			nw.DeletionTimestamp = ptr(metav1.Now())
		}
		for _, p := range packages {
			nw.Spec.Packages[p.Name] = p
		}
		return nw
	}

	type entry struct {
		pkg    v1alpha1.Package
		stage  v1alpha1.Stage
		state  v1alpha1.State
		result *v1alpha1.PreflightResult
	}

	// nodeHolding returns a real wrapper node of nw whose state holds every entry, with its
	// preflight result.
	nodeHolding := func(nw *v1alpha1.NodeWright, name string, entries ...entry) wrapper.SkyhookNode {
		GinkgoHelper()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		only, err := wrapper.NewSkyhookNodeOnly(node, nw.Name)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(only.Upsert(e.pkg.PackageRef, e.pkg.Image, e.state, e.stage, 0, e.pkg.ContainerSHA)).To(Succeed())
			Expect(only.SetPreflight(e.pkg.PackageRef, e.result)).To(Succeed())
		}
		sn, err := wrapper.NewSkyhookNode(node, nw)
		Expect(err).NotTo(HaveOccurred())
		return sn
	}

	snapshotOf := func(nw *v1alpha1.NodeWright, nodes ...wrapper.SkyhookNode) *skyhookNodes {
		return &skyhookNodes{skyhook: wrapper.NewSkyhookWrapper(nw), nodes: nodes}
	}

	// decided is a snapshot whose decisions are set by hand, as resolveUninstallDecisions sets them.
	decided := func(nw *v1alpha1.NodeWright, decisions map[string]wrapper.UninstallDecision, nodes ...wrapper.SkyhookNode) *skyhookNodes {
		sn := snapshotOf(nw, nodes...)
		sn.skyhook.UninstallDecisions = decisions
		return sn
	}

	stageOf := func(node wrapper.SkyhookNode, _package v1alpha1.Package) v1alpha1.Stage {
		GinkgoHelper()
		status, found := node.PackageStatus(_package.GetUniqueName())
		Expect(found).To(BeTrue())
		return status.Stage
	}

	DescribeTable("uninstallHeldBackOn holds a node back only where it holds the package, outside its uninstall cycle, from an artifact declaring uninstall unsupported",
		func(stage v1alpha1.Stage, result *v1alpha1.PreflightResult, want bool) {
			tuning := packageOf("tuning", "1.0.0", applied())
			state := v1alpha1.NodeState{tuning.GetUniqueName(): v1alpha1.PackageStatus{
				Name: tuning.Name, Version: tuning.Version, Image: tuning.Image, Stage: stage, State: v1alpha1.StateComplete, Preflight: result,
			}}
			Expect(uninstallHeldBackOn(state, &tuning)).To(Equal(want))
			Expect(uninstallHeldBackOn(v1alpha1.NodeState{}, &tuning)).To(BeFalse())
		},
		Entry("an artifact declaring uninstall unsupported", v1alpha1.StageConfig, unsupportedArtifact, true),
		Entry("an artifact declaring uninstall supported", v1alpha1.StageConfig, supportedArtifact, false),
		Entry("a container image", v1alpha1.StageConfig, image, false),
		Entry("a reference that could not be inspected", v1alpha1.StageConfig, &v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "timeout"}, false),
		Entry("no result, as installed with the option off", v1alpha1.StageConfig, nil, false),
		Entry("unsupported, in its uninstall stage", v1alpha1.StageUninstall, unsupportedArtifact, false),
		Entry("unsupported, at its uninstall interrupt", v1alpha1.StageUninstallInterrupt, unsupportedArtifact, false),
	)

	Describe("HandleUninstallRequests", func() {
		DescribeTable("starts an uninstall at a complete install stage unless the node's artifact declares uninstall unsupported",
			func(deleting bool, result *v1alpha1.PreflightResult, started bool) {
				uninstall := applied()
				if deleting {
					uninstall = enabled()
				}
				tuning := packageOf("tuning", "1.0.0", uninstall)
				nw := nodeWrightOf(deleting, tuning)
				node := nodeHolding(nw, "node", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, result})

				toUninstall, err := HandleUninstallRequests(snapshotOf(nw, node))
				Expect(err).NotTo(HaveOccurred())
				if started {
					Expect(toUninstall).To(ConsistOf(HaveField("PackageRef", tuning.PackageRef)))
					Expect(stageOf(node, tuning)).To(Equal(v1alpha1.StageUninstall))
					return
				}
				Expect(toUninstall).To(BeEmpty())
				Expect(stageOf(node, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(node.Changed()).To(BeFalse())
			},
			Entry("uninstall.apply, installed with the option off", false, nil, true),
			Entry("uninstall.apply, a container image", false, image, true),
			Entry("uninstall.apply, an artifact declaring uninstall supported", false, supportedArtifact, true),
			Entry("uninstall.apply, an artifact declaring uninstall unsupported", false, unsupportedArtifact, false),
			Entry("deletion, an artifact declaring uninstall supported", true, supportedArtifact, true),
			Entry("deletion, an artifact declaring uninstall unsupported", true, unsupportedArtifact, false),
		)

		It("decides each node from its own result", func() {
			tuning := packageOf("tuning", "1.0.0", applied())
			nw := nodeWrightOf(false, tuning)
			heldBack := nodeHolding(nw, "node-a", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact})
			supported := nodeHolding(nw, "node-b", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact})
			legacy := nodeHolding(nw, "node-c", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, nil})

			toUninstall, err := HandleUninstallRequests(snapshotOf(nw, heldBack, supported, legacy))
			Expect(err).NotTo(HaveOccurred())
			Expect(toUninstall).To(ConsistOf(HaveField("PackageRef", tuning.PackageRef)))
			Expect(stageOf(heldBack, tuning)).To(Equal(v1alpha1.StageConfig))
			Expect(heldBack.Changed()).To(BeFalse())
			Expect(stageOf(supported, tuning)).To(Equal(v1alpha1.StageUninstall))
			Expect(stageOf(legacy, tuning)).To(Equal(v1alpha1.StageUninstall))
		})

		DescribeTable("carries an uninstall cycle on whatever the node's result",
			func(stage v1alpha1.Stage, state v1alpha1.State, readded bool) {
				tuning := packageOf("tuning", "1.0.0", applied())
				nw := nodeWrightOf(false, tuning)
				node := nodeHolding(nw, "node", entry{tuning, stage, state, unsupportedArtifact})

				toUninstall, err := HandleUninstallRequests(snapshotOf(nw, node))
				Expect(err).NotTo(HaveOccurred())
				if readded {
					Expect(toUninstall).To(ConsistOf(HaveField("PackageRef", tuning.PackageRef)))
					Expect(stageOf(node, tuning)).To(Equal(stage))
					return
				}
				Expect(toUninstall).To(BeEmpty())
				_, found := node.PackageStatus(tuning.GetUniqueName())
				Expect(found).To(BeFalse())
			},
			Entry("re-adding an entry at StageUninstall", v1alpha1.StageUninstall, v1alpha1.StateInProgress, true),
			Entry("re-adding an uninstall interrupt in progress", v1alpha1.StageUninstallInterrupt, v1alpha1.StateInProgress, true),
			Entry("removing a complete uninstall interrupt", v1alpha1.StageUninstallInterrupt, v1alpha1.StateComplete, false),
		)

		DescribeTable("removes an entry at the preflight stage, since nothing of the package is on the node",
			func(deleting bool, state v1alpha1.State) {
				uninstall := applied()
				if deleting {
					uninstall = enabled()
				}
				tuning := packageOf("tuning", "1.0.0", uninstall)
				nw := nodeWrightOf(deleting, tuning)
				node := nodeHolding(nw, "node", entry{tuning, v1alpha1.StagePreflight, state, unsupportedArtifact})

				toUninstall, err := HandleUninstallRequests(snapshotOf(nw, node))
				Expect(err).NotTo(HaveOccurred())
				Expect(toUninstall).To(BeEmpty())
				_, found := node.PackageStatus(tuning.GetUniqueName())
				Expect(found).To(BeFalse())
				Expect(node.Status()).To(Equal(v1alpha1.StatusInProgress))
			},
			Entry("uninstall.apply, preflight in progress", false, v1alpha1.StateInProgress),
			Entry("uninstall.apply, preflight complete", false, v1alpha1.StateComplete),
			Entry("uninstall.apply, preflight held erroring by a rejection", false, v1alpha1.StateErroring),
			Entry("deletion, preflight complete", true, v1alpha1.StateComplete),
		)

		It("leaves an entry at the preflight stage beside another version, as for an upgrade under way", func() {
			older := packageOf("tuning", "1.0.0", applied())
			tuning := packageOf("tuning", "2.0.0", applied())
			nw := nodeWrightOf(false, tuning)
			node := nodeHolding(nw, "node",
				entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil},
				entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete, supportedArtifact})

			toUninstall, err := HandleUninstallRequests(snapshotOf(nw, node))
			Expect(err).NotTo(HaveOccurred())
			Expect(toUninstall).To(BeEmpty())
			Expect(stageOf(node, tuning)).To(Equal(v1alpha1.StagePreflight))
			Expect(stageOf(node, older)).To(Equal(v1alpha1.StageConfig))
			Expect(node.Changed()).To(BeFalse())
		})
	})

	Describe("resolveUninstallDecisions", func() {
		var (
			recorder *events.FakeRecorder
			r        *SkyhookReconciler
		)

		BeforeEach(func() {
			// Verbose so each message carries the action; sized past every event a spec records,
			// since a full channel blocks Eventf.
			recorder = &events.FakeRecorder{Events: make(chan string, 20), Verbose: true}
			c := fake.NewClientBuilder().Build()
			var err error
			r, err = NewSkyhookReconciler(runtime.NewScheme(), c, c, k8sfake.NewClientset(), recorder, SkyhookOperatorOptions{
				Namespace:            "nodewright",
				CopyDirRoot:          "/var/lib/skyhook",
				AgentLogRoot:         "/var/log/skyhook",
				RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
				AgentImage:           "ghcr.io/nvidia/nodewright/agent:1.2.3",
				PauseImage:           "registry.k8s.io/pause:3.10",
				MaxInterval:          10 * time.Minute,
				PackageArtifacts:     true,
				JobOperatorOptions:   JobOperatorOptions{JobTTLSucceeded: time.Hour, JobTTLFailed: 24 * time.Hour, JobStageTimeout: time.Hour},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		recorded := func() []string {
			var got []string
			for {
				select {
				case event := <-recorder.Events:
					got = append(got, event)
				default:
					return got
				}
			}
		}

		tuning := packageOf("tuning", "1.0.0", applied())

		DescribeTable("decides unsupported only when every node holding the package outside its uninstall cycle holds it back",
			func(want wrapper.UninstallDecision, nodes ...[]entry) {
				nw := nodeWrightOf(false, tuning)
				var wrapped []wrapper.SkyhookNode
				for i, entries := range nodes {
					wrapped = append(wrapped, nodeHolding(nw, fmt.Sprintf("node-%d", i), entries...))
				}
				sn := snapshotOf(nw, wrapped...)

				r.resolveUninstallDecisions(ctx, sn)
				Expect(sn.GetSkyhook().UninstallDecision(tuning.Name)).To(Equal(want))
			},
			Entry("no node holds it", spec),
			Entry("one node holds it back", unsupported,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}}),
			Entry("every node holds it back", unsupported,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}},
				[]entry{{tuning, v1alpha1.StageApply, v1alpha1.StateInProgress, unsupportedArtifact}}),
			Entry("one node of two holds it back", spec,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}},
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact}}),
			Entry("one node holds it back, beside one in its uninstall cycle", unsupported,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}},
				[]entry{{tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress, unsupportedArtifact}}),
			Entry("the only node holding it is in its uninstall cycle", spec,
				[]entry{{tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress, unsupportedArtifact}}),
			Entry("no node holds it back", spec,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact}},
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, nil}}),
		)

		It("replaces the decisions of an earlier pass", func() {
			nw := nodeWrightOf(false, tuning)
			sn := decided(nw, map[string]wrapper.UninstallDecision{tuning.Name: unsupported, "gone": unsupported},
				nodeHolding(nw, "node", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact}))

			r.resolveUninstallDecisions(ctx, sn)
			Expect(sn.GetSkyhook().UninstallDecisions).To(BeEmpty())
		})

		It("warns of a requested uninstall some node holds back, naming the package and the remedy", func() {
			nw := nodeWrightOf(false, tuning)
			sn := snapshotOf(nw,
				nodeHolding(nw, "node-a", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}),
				nodeHolding(nw, "node-b", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact}))

			r.resolveUninstallDecisions(ctx, sn)
			Expect(recorded()).To(ConsistOf(And(
				HavePrefix("Warning UninstallUnsupported tuning:1.0.0 "),
				ContainSubstring("package [tuning:1.0.0] was installed from an artifact that declares uninstall unsupported"),
				ContainSubstring(remedy),
			)))
		})

		It("warns no more once an earlier pass's UninstallUnsupported condition lists the package, and warns of one it does not list", func() {
			other := packageOf("other", "2.0.0", applied())
			nw := nodeWrightOf(false, tuning, other)
			updateUninstallUnsupportedCondition(snapshotOf(nw,
				nodeHolding(nw, "node-a", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact})))
			sn := snapshotOf(nw, nodeHolding(nw, "node-a",
				entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact},
				entry{other, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}))

			r.resolveUninstallDecisions(ctx, sn)
			Expect(recorded()).To(ConsistOf(HavePrefix("Warning UninstallUnsupported other:2.0.0 ")))
		})

		DescribeTable("records no Warning",
			func(uninstall *v1alpha1.Uninstall, result *v1alpha1.PreflightResult) {
				p := packageOf("tuning", "1.0.0", uninstall)
				nw := nodeWrightOf(false, p)
				sn := snapshotOf(nw, nodeHolding(nw, "node", entry{p, v1alpha1.StageConfig, v1alpha1.StateComplete, result}))

				r.resolveUninstallDecisions(ctx, sn)
				Expect(recorded()).To(BeEmpty())
			},
			Entry("while no uninstall is requested", enabled(), unsupportedArtifact),
			Entry("for an artifact declaring uninstall supported", applied(), supportedArtifact),
			Entry("for a package installed with the option off", applied(), nil),
		)
	})

	DescribeTable("HasUninstallWork counts a request only when the decision is not unsupported",
		func(deleting bool, uninstall *v1alpha1.Uninstall, stage v1alpha1.Stage, decision wrapper.UninstallDecision, want bool) {
			tuning := packageOf("tuning", "1.0.0", uninstall)
			nw := nodeWrightOf(deleting, tuning)
			state := v1alpha1.StateComplete
			if stage == v1alpha1.StageUninstall {
				state = v1alpha1.StateInProgress
			}
			node := nodeHolding(nw, "node", entry{tuning, stage, state, unsupportedArtifact})

			hasWork, err := decided(nw, map[string]wrapper.UninstallDecision{tuning.Name: decision}, node).HasUninstallWork()
			Expect(err).NotTo(HaveOccurred())
			Expect(hasWork).To(Equal(want))
		},
		Entry("uninstall.apply, spec", false, applied(), v1alpha1.StageConfig, spec, true),
		Entry("uninstall.apply, unsupported", false, applied(), v1alpha1.StageConfig, unsupported, false),
		Entry("deletion, spec", true, enabled(), v1alpha1.StageConfig, spec, true),
		Entry("deletion, unsupported", true, enabled(), v1alpha1.StageConfig, unsupported, false),
		Entry("in its uninstall cycle, unsupported", false, enabled(), v1alpha1.StageUninstall, unsupported, true),
	)

	DescribeTable("UpdateBlockedCondition skips a dependent going away unless its decision is unsupported and its own cycle has not begun",
		func(dependentStage v1alpha1.Stage, decision wrapper.UninstallDecision, blocked bool) {
			dep := packageOf("dep-a", "1.0.0", applied())
			dependent := packageOf("pkg-b", "2.0.0", applied())
			dependent.DependsOn = map[string]string{dep.Name: dep.Version}
			nw := nodeWrightOf(false, dep, dependent)
			node := nodeHolding(nw, "node",
				entry{dep, v1alpha1.StageUninstall, v1alpha1.StateInProgress, nil},
				entry{dependent, dependentStage, v1alpha1.StateInProgress, unsupportedArtifact})
			sn := decided(nw, map[string]wrapper.UninstallDecision{dep.Name: spec, dependent.Name: decision}, node)

			Expect(sn.UpdateBlockedCondition()).To(Succeed())
			condition := meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionBlocked)
			if !blocked {
				Expect(condition).To(BeNil())
				return
			}
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal("DependencyUninstalled"))
			Expect(condition.Message).To(Equal("package pkg-b is blocked: dependency dep-a is being uninstalled"))
		},
		Entry("spec", v1alpha1.StageApply, spec, false),
		Entry("unsupported", v1alpha1.StageApply, unsupported, true),
		Entry("unsupported, the dependent in its own uninstall cycle", v1alpha1.StageUninstall, unsupported, false),
	)

	Describe("updateUninstallUnsupportedCondition", func() {
		conditionOf := func(sn *skyhookNodes) *metav1.Condition {
			return meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionUninstallUnsupported)
		}

		It("lists one requested package some node holds back", func() {
			tuning := packageOf("tuning", "1.0.0", applied())
			nw := nodeWrightOf(false, tuning)
			sn := snapshotOf(nw,
				nodeHolding(nw, "node-a", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}),
				nodeHolding(nw, "node-b", entry{tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress, supportedArtifact}))

			updateUninstallUnsupportedCondition(sn)
			condition := conditionOf(sn)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("ArtifactUninstallUnsupported"))
			Expect(condition.ObservedGeneration).To(Equal(int64(3)))
			Expect(condition.Message).To(Equal("1 package artifact declares uninstall unsupported: tuning:1.0.0. " + remedy))
			Expect(meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.LegacySkyhookConditionType(wrapper.SkyhookConditionUninstallUnsupported))).To(BeNil())
		})

		It("lists each requested package some node holds back once, sorted, and no package whose uninstall is not requested", func() {
			a := packageOf("a", "1.0.0", applied())
			b := packageOf("b", "2.0.0", applied())
			c := packageOf("c", "1.0.0", enabled())
			nw := nodeWrightOf(false, a, b, c)
			sn := snapshotOf(nw,
				nodeHolding(nw, "node-1",
					entry{b, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact},
					entry{a, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact},
					entry{c, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}),
				nodeHolding(nw, "node-2", entry{a, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}))

			updateUninstallUnsupportedCondition(sn)
			Expect(conditionOf(sn)).To(HaveField("Message", "2 package artifacts declare uninstall unsupported: a:1.0.0, b:2.0.0. "+remedy))
		})

		It("lists a package the deletion of the NodeWright would uninstall", func() {
			tuning := packageOf("tuning", "1.0.0", enabled())
			nw := nodeWrightOf(true, tuning)
			sn := snapshotOf(nw, nodeHolding(nw, "node", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}))

			updateUninstallUnsupportedCondition(sn)
			Expect(conditionOf(sn)).To(HaveField("Message", "1 package artifact declares uninstall unsupported: tuning:1.0.0. "+remedy))
		})

		It("names at most ReadyConditionNodeListLimit packages and counts the rest", func() {
			var packages []v1alpha1.Package
			var entries []entry
			for i := range wrapper.ReadyConditionNodeListLimit + 1 {
				p := packageOf(fmt.Sprintf("p-%02d", i), "1.0.0", applied())
				packages = append(packages, p)
				entries = append(entries, entry{p, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact})
			}
			nw := nodeWrightOf(false, packages...)
			sn := snapshotOf(nw, nodeHolding(nw, "node", entries...))

			updateUninstallUnsupportedCondition(sn)
			named := make([]string, 0, wrapper.ReadyConditionNodeListLimit)
			for _, p := range packages[:wrapper.ReadyConditionNodeListLimit] {
				named = append(named, p.Name+":1.0.0")
			}
			Expect(conditionOf(sn)).To(HaveField("Message",
				fmt.Sprintf("11 package artifacts declare uninstall unsupported: %s, and 1 more. %s", strings.Join(named, ", "), remedy)))
		})

		DescribeTable("is removed when it lists nothing",
			func(uninstall *v1alpha1.Uninstall, stage v1alpha1.Stage, result *v1alpha1.PreflightResult) {
				tuning := packageOf("tuning", "1.0.0", uninstall)
				nw := nodeWrightOf(false, tuning)
				nw.Status.Conditions = []metav1.Condition{{
					Type: wrapper.SkyhookConditionUninstallUnsupported, Status: metav1.ConditionTrue,
					Reason: wrapper.SkyhookReasonArtifactUninstallUnsupported, Message: "stale", LastTransitionTime: metav1.Now(),
				}}
				sn := snapshotOf(nw, nodeHolding(nw, "node", entry{tuning, stage, v1alpha1.StateComplete, result}))

				updateUninstallUnsupportedCondition(sn)
				Expect(conditionOf(sn)).To(BeNil())
				Expect(sn.GetSkyhook().Updated).To(BeTrue())
			},
			Entry("once uninstall.apply is false", enabled(), v1alpha1.StageConfig, unsupportedArtifact),
			Entry("once uninstall.enabled is false", &v1alpha1.Uninstall{}, v1alpha1.StageConfig, unsupportedArtifact),
			Entry("for an artifact declaring uninstall supported", applied(), v1alpha1.StageConfig, supportedArtifact),
			Entry("for a package installed with the option off", applied(), v1alpha1.StageConfig, nil),
			Entry("for a package whose uninstall interrupt runs", applied(), v1alpha1.StageUninstallInterrupt, unsupportedArtifact),
		)
	})

	Describe("deletionWaitsOn", func() {
		tuning := packageOf("tuning", "1.0.0", enabled())

		DescribeTable("waits only on a node that holds the package and can uninstall it",
			func(want bool, nodes ...[]entry) {
				nw := nodeWrightOf(true, tuning)
				var wrapped []wrapper.SkyhookNode
				for i, entries := range nodes {
					wrapped = append(wrapped, nodeHolding(nw, fmt.Sprintf("node-%d", i), entries...))
				}

				pending, err := deletionWaitsOn(snapshotOf(nw, wrapped...), &tuning)
				Expect(err).NotTo(HaveOccurred())
				Expect(pending).To(Equal(want))
			},
			Entry("no node holds it", false, []entry{}),
			Entry("a node holds it, installed with the option off", true,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, nil}}),
			Entry("a node holds it from an artifact declaring uninstall supported", true,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact}}),
			Entry("the only node holding it holds it back", false,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}}),
			Entry("one node holds it back, another can uninstall it", true,
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupportedArtifact}},
				[]entry{{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supportedArtifact}}),
			Entry("a node whose uninstall cycle has begun", true,
				[]entry{{tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress, unsupportedArtifact}}),
		)

		It("returns an error for a node whose state cannot be read", func() {
			nw := nodeWrightOf(true, tuning)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "malformed", Annotations: map[string]string{
				nodeStateAnnotationKey(nodewrightName): "{not-valid-json",
			}}}
			sn, err := wrapper.NewSkyhookNode(node, nw)
			Expect(err).NotTo(HaveOccurred())

			_, err = deletionWaitsOn(snapshotOf(nw, sn), &tuning)
			Expect(err).To(MatchError(ContainSubstring("malformed")))
		})
	})
})
