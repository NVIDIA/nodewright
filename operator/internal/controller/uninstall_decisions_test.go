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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The uninstall decisions in isolation: how an outcome maps to a decision, how the functions that
// plan uninstalls read one set by hand, the UninstallUnsupported condition, the requeue of a pass,
// and the UninstallCapability event. No spec shares the suite's manager or apiserver.
var _ = Describe("uninstall decisions", func() {
	const nodewrightName = "decisions"

	var (
		spec        = wrapper.UninstallDecisionSpec
		unsupported = wrapper.UninstallDecisionUnsupported
		deferred    = wrapper.UninstallDecisionDeferred
	)

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
		pkg   v1alpha1.Package
		stage v1alpha1.Stage
		state v1alpha1.State
	}

	// nodeHolding returns a real wrapper node of nw whose state holds every entry.
	nodeHolding := func(nw *v1alpha1.NodeWright, name string, entries ...entry) wrapper.SkyhookNode {
		GinkgoHelper()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		only, err := wrapper.NewSkyhookNodeOnly(node, nw.Name)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(only.Upsert(e.pkg.PackageRef, e.pkg.Image, e.state, e.stage, 0, e.pkg.ContainerSHA)).To(Succeed())
		}
		sn, err := wrapper.NewSkyhookNode(node, nw)
		Expect(err).NotTo(HaveOccurred())
		return sn
	}

	// snapshotOf is what a pass sees once resolveUninstallDecisions has set decisions.
	snapshotOf := func(nw *v1alpha1.NodeWright, decisions map[string]wrapper.UninstallDecision, nodes ...wrapper.SkyhookNode) *skyhookNodes {
		skyhook := wrapper.NewSkyhookWrapper(nw)
		skyhook.UninstallDecisions = decisions
		return &skyhookNodes{skyhook: skyhook, nodes: nodes}
	}

	stageOf := func(node wrapper.SkyhookNode, _package v1alpha1.Package) v1alpha1.Stage {
		GinkgoHelper()
		status, found := node.PackageStatus(_package.GetUniqueName())
		Expect(found).To(BeTrue())
		return status.Stage
	}

	DescribeTable("uninstallDecisionFor",
		func(outcome packageArtifactOutcome, want wrapper.UninstallDecision) {
			Expect(uninstallDecisionFor(outcome)).To(Equal(want))
		},
		Entry("the option off", packageArtifactOutcome{Kind: packageArtifactDisabled}, spec),
		Entry("a container image", packageArtifactOutcome{Kind: packageArtifactLegacy, Pinned: "registry.example/tuning@sha256:aa"}, spec),
		Entry("an artifact declaring uninstall supported", packageArtifactOutcome{
			Kind: packageArtifactAccepted, Pinned: "registry.example/tuning@sha256:aa", Package: &artifact.Package{Uninstall: artifact.UninstallSupported},
		}, spec),
		Entry("an artifact declaring uninstall unsupported", packageArtifactOutcome{
			Kind: packageArtifactAccepted, Pinned: "registry.example/tuning@sha256:aa", Package: &artifact.Package{Uninstall: artifact.UninstallUnsupported},
		}, unsupported),
		Entry("a rejected artifact", rejectedPackageArtifact(packageArtifactReasonHostFiles), deferred),
		Entry("an artifact that could not be inspected", unknownPackageArtifact(packageArtifactErrorTransport), deferred),
	)

	Describe("HandleUninstallRequests", func() {
		DescribeTable("starts an uninstall at a complete install stage only for the decision spec",
			func(deleting bool, decision wrapper.UninstallDecision, started bool) {
				uninstall := applied()
				if deleting {
					uninstall = enabled()
				}
				tuning := packageOf("tuning", "1.0.0", uninstall)
				nw := nodeWrightOf(deleting, tuning)
				node := nodeHolding(nw, "node", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete})

				toUninstall, err := HandleUninstallRequests(snapshotOf(nw, map[string]wrapper.UninstallDecision{tuning.Name: decision}, node))
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
			Entry("uninstall.apply, spec", false, spec, true),
			Entry("uninstall.apply, unsupported", false, unsupported, false),
			Entry("uninstall.apply, deferred", false, deferred, false),
			Entry("deletion, spec", true, spec, true),
			Entry("deletion, unsupported", true, unsupported, false),
			Entry("deletion, deferred", true, deferred, false),
		)

		DescribeTable("carries an uninstall cycle on under unsupported",
			func(stage v1alpha1.Stage, state v1alpha1.State, readded bool) {
				tuning := packageOf("tuning", "1.0.0", applied())
				nw := nodeWrightOf(false, tuning)
				node := nodeHolding(nw, "node", entry{tuning, stage, state})

				toUninstall, err := HandleUninstallRequests(snapshotOf(nw, map[string]wrapper.UninstallDecision{tuning.Name: unsupported}, node))
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
	})

	DescribeTable("HasUninstallWork counts a request only when the decision is not unsupported",
		func(deleting bool, uninstall *v1alpha1.Uninstall, stage v1alpha1.Stage, decision wrapper.UninstallDecision, want bool) {
			tuning := packageOf("tuning", "1.0.0", uninstall)
			nw := nodeWrightOf(deleting, tuning)
			state := v1alpha1.StateComplete
			if stage == v1alpha1.StageUninstall {
				state = v1alpha1.StateInProgress
			}
			node := nodeHolding(nw, "node", entry{tuning, stage, state})

			hasWork, err := snapshotOf(nw, map[string]wrapper.UninstallDecision{tuning.Name: decision}, node).HasUninstallWork()
			Expect(err).NotTo(HaveOccurred())
			Expect(hasWork).To(Equal(want))
		},
		Entry("uninstall.apply, spec", false, applied(), v1alpha1.StageConfig, spec, true),
		Entry("uninstall.apply, unsupported", false, applied(), v1alpha1.StageConfig, unsupported, false),
		Entry("uninstall.apply, deferred", false, applied(), v1alpha1.StageConfig, deferred, true),
		Entry("deletion, spec", true, enabled(), v1alpha1.StageConfig, spec, true),
		Entry("deletion, unsupported", true, enabled(), v1alpha1.StageConfig, unsupported, false),
		Entry("deletion, deferred", true, enabled(), v1alpha1.StageConfig, deferred, true),
		Entry("in its uninstall cycle, unsupported", false, enabled(), v1alpha1.StageUninstall, unsupported, true),
	)

	DescribeTable("UpdateBlockedCondition skips a dependent going away only when its decision is not unsupported",
		func(decision wrapper.UninstallDecision, blocked bool) {
			dep := packageOf("dep-a", "1.0.0", applied())
			dependent := packageOf("pkg-b", "2.0.0", applied())
			dependent.DependsOn = map[string]string{dep.Name: dep.Version}
			nw := nodeWrightOf(false, dep, dependent)
			node := nodeHolding(nw, "node",
				entry{dep, v1alpha1.StageUninstall, v1alpha1.StateInProgress},
				entry{dependent, v1alpha1.StageApply, v1alpha1.StateInProgress})
			sn := snapshotOf(nw, map[string]wrapper.UninstallDecision{dep.Name: spec, dependent.Name: decision}, node)

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
		Entry("spec", spec, false),
		Entry("unsupported", unsupported, true),
		Entry("deferred", deferred, false),
	)

	Describe("updateUninstallUnsupportedCondition", func() {
		conditionOf := func(sn *skyhookNodes) *metav1.Condition {
			return meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionUninstallUnsupported)
		}
		const remedy = " Set uninstall.apply or uninstall.enabled to false, or point the package at an artifact that declares uninstall supported."

		It("lists one package not started", func() {
			tuning := packageOf("tuning", "1.0.0", applied())
			nw := nodeWrightOf(false, tuning)
			sn := snapshotOf(nw, map[string]wrapper.UninstallDecision{tuning.Name: unsupported},
				nodeHolding(nw, "node", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete}))

			updateUninstallUnsupportedCondition(sn)
			condition := conditionOf(sn)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("ArtifactUninstallUnsupported"))
			Expect(condition.ObservedGeneration).To(Equal(int64(3)))
			Expect(condition.Message).To(Equal("1 package artifact declares uninstall unsupported: tuning:1.0.0 (not started)." + remedy))
			Expect(meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.LegacySkyhookConditionType(wrapper.SkyhookConditionUninstallUnsupported))).To(BeNil())
		})

		It("lists two packages, one stalled at StageUninstall on some node and one not started", func() {
			a := packageOf("a", "1.0.0", applied())
			b := packageOf("b", "2.0.0", applied())
			nw := nodeWrightOf(false, a, b)
			sn := snapshotOf(nw, map[string]wrapper.UninstallDecision{a.Name: unsupported, b.Name: unsupported},
				nodeHolding(nw, "node-1", entry{a, v1alpha1.StageUninstall, v1alpha1.StateInProgress}, entry{b, v1alpha1.StageConfig, v1alpha1.StateComplete}),
				nodeHolding(nw, "node-2", entry{a, v1alpha1.StageConfig, v1alpha1.StateComplete}, entry{b, v1alpha1.StageConfig, v1alpha1.StateComplete}))

			updateUninstallUnsupportedCondition(sn)
			Expect(conditionOf(sn)).To(HaveField("Message",
				"2 package artifacts declare uninstall unsupported: a:1.0.0 (stalled), b:2.0.0 (not started)."+remedy))
		})

		It("names at most ReadyConditionNodeListLimit packages and counts the rest", func() {
			var packages []v1alpha1.Package
			decisions := map[string]wrapper.UninstallDecision{}
			var entries []entry
			for i := range wrapper.ReadyConditionNodeListLimit + 1 {
				p := packageOf(fmt.Sprintf("p-%02d", i), "1.0.0", applied())
				packages = append(packages, p)
				decisions[p.Name] = unsupported
				entries = append(entries, entry{p, v1alpha1.StageConfig, v1alpha1.StateComplete})
			}
			nw := nodeWrightOf(false, packages...)
			sn := snapshotOf(nw, decisions, nodeHolding(nw, "node", entries...))

			updateUninstallUnsupportedCondition(sn)
			named := make([]string, 0, wrapper.ReadyConditionNodeListLimit)
			for _, p := range packages[:wrapper.ReadyConditionNodeListLimit] {
				named = append(named, p.Name+":1.0.0 (not started)")
			}
			Expect(conditionOf(sn)).To(HaveField("Message",
				fmt.Sprintf("11 package artifacts declare uninstall unsupported: %s, and 1 more.%s", strings.Join(named, ", "), remedy)))
		})

		DescribeTable("is removed when it lists nothing",
			func(uninstall *v1alpha1.Uninstall, decision wrapper.UninstallDecision, stage v1alpha1.Stage) {
				tuning := packageOf("tuning", "1.0.0", uninstall)
				nw := nodeWrightOf(false, tuning)
				nw.Status.Conditions = []metav1.Condition{{
					Type: wrapper.SkyhookConditionUninstallUnsupported, Status: metav1.ConditionTrue,
					Reason: wrapper.SkyhookReasonArtifactUninstallUnsupported, Message: "stale", LastTransitionTime: metav1.Now(),
				}}
				sn := snapshotOf(nw, map[string]wrapper.UninstallDecision{tuning.Name: decision},
					nodeHolding(nw, "node", entry{tuning, stage, v1alpha1.StateComplete}))

				updateUninstallUnsupportedCondition(sn)
				Expect(conditionOf(sn)).To(BeNil())
				Expect(sn.GetSkyhook().Updated).To(BeTrue())
			},
			Entry("once uninstall.apply is false", enabled(), unsupported, v1alpha1.StageConfig),
			Entry("once uninstall.enabled is false", &v1alpha1.Uninstall{}, unsupported, v1alpha1.StageConfig),
			Entry("for an artifact declaring uninstall supported", applied(), spec, v1alpha1.StageConfig),
			Entry("for a deferred package", applied(), deferred, v1alpha1.StageConfig),
			Entry("for a package whose uninstall interrupt runs", applied(), unsupported, v1alpha1.StageUninstallInterrupt),
		)
	})

	DescribeTable("passResult",
		func(result *ctrl.Result, retryPending bool, decisions *ctrl.Result, want time.Duration) {
			Expect(passResult(result, retryPending, 10*time.Minute, decisions)).To(Equal(ctrl.Result{RequeueAfter: want}))
		},
		Entry("keeps a 30s pending retry", nil, true, &ctrl.Result{RequeueAfter: 5 * time.Minute}, pendingRetryInterval),
		Entry("brings the 10m MaxInterval in to 5m", nil, false, &ctrl.Result{RequeueAfter: 5 * time.Minute}, 5*time.Minute),
		Entry("gives a result with no RequeueAfter 5m", &ctrl.Result{}, false, &ctrl.Result{RequeueAfter: 5 * time.Minute}, 5*time.Minute),
		Entry("keeps a sooner result", &ctrl.Result{RequeueAfter: 2 * time.Second}, false, &ctrl.Result{RequeueAfter: 5 * time.Minute}, 2*time.Second),
		Entry("with nil decisions, keeps a pending retry", nil, true, nil, pendingRetryInterval),
		Entry("with nil decisions, keeps the MaxInterval", nil, false, nil, 10*time.Minute),
		Entry("with nil decisions, keeps a result", &ctrl.Result{RequeueAfter: 2 * time.Second}, false, nil, 2*time.Second),
		Entry("with nil decisions, keeps a result with no RequeueAfter", &ctrl.Result{}, false, nil, time.Duration(0)),
	)

	Describe("the UninstallCapability event", func() {
		var (
			fakeClock *testingclock.FakePassiveClock
			recorder  *events.FakeRecorder
			reg       *registrytest.Registry
			r         *SkyhookReconciler
		)
		nw := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: nodewrightName, UID: "decisions-uid"}}

		BeforeEach(func() {
			fakeClock = testingclock.NewFakePassiveClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
			// Verbose so each message carries the action; sized past every event a spec records,
			// since a full channel blocks Eventf.
			recorder = &events.FakeRecorder{Events: make(chan string, 20), Verbose: true}
			reg = registrytest.New(registrytest.Options{})
			DeferCleanup(reg.Close)

			c := fake.NewClientBuilder().Build()
			cache := newPackageArtifactCache(c, recorder, "nodewright", "")
			cache.httpClient = reg.Client()
			cache.clock = fakeClock
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
				artifactCache:        cache,
				JobOperatorOptions:   JobOperatorOptions{JobTTLSucceeded: time.Hour, JobTTLFailed: 24 * time.Hour, JobStageTimeout: time.Hour},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		push := func(name string, uninstall artifact.Uninstall, payload string) ocispec.Descriptor {
			GinkgoHelper()
			desc, _, err := registrytest.PushPackage(ctx, reg.Repository(name), "1.0.0", registrytest.PackageSpec{
				Uninstall: uninstall,
				Config:    json.RawMessage(`{}`),
				Payload:   []registrytest.Layer{{Gzip: []byte(payload)}},
			})
			Expect(err).NotTo(HaveOccurred())
			return desc
		}

		packageNamed := func(name string, uninstall *v1alpha1.Uninstall) *v1alpha1.Package {
			return &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: "1.0.0"}, Image: reg.Host() + "/" + name, Uninstall: uninstall}
		}

		// announcements drains the recorder and returns its UninstallCapability events.
		announcements := func() []string {
			var got []string
			for {
				select {
				case event := <-recorder.Events:
					if strings.HasPrefix(event, EventTypeNormal+" "+EventsReasonUninstallCapability+" ") {
						got = append(got, event)
					}
				default:
					return got
				}
			}
		}

		It("is recorded once per digest, and once more, under another action, for a new digest", func() {
			first := push("tuning", artifact.UninstallSupported, "first")
			tuning := packageNamed("tuning", enabled())
			for range 2 {
				Expect(r.packageArtifactOutcome(ctx, nw, tuning).Kind).To(Equal(packageArtifactAccepted))
			}
			Expect(announcements()).To(ConsistOf(And(
				HavePrefix("Normal UninstallCapability tuning@"+first.Digest.String()+" "),
				ContainSubstring("[tuning:1.0.0]"),
				ContainSubstring("["+first.Digest.String()+"]"),
				ContainSubstring("declares uninstall supported"),
			)))

			second := push("tuning", artifact.UninstallUnsupported, "second")
			Expect(second.Digest).NotTo(Equal(first.Digest))
			fakeClock.SetTime(fakeClock.Now().Add(packageArtifactTagTTL + time.Second))
			for range 2 {
				Expect(r.packageArtifactOutcome(ctx, nw, tuning).Pinned).To(HaveSuffix("@" + second.Digest.String()))
			}
			Expect(announcements()).To(ConsistOf(And(
				HavePrefix("Normal UninstallCapability tuning@"+second.Digest.String()+" "),
				ContainSubstring("["+second.Digest.String()+"]"),
				ContainSubstring("declares uninstall unsupported"),
			)))
		})

		It("keeps the whole name and digest in an action of at most 128 bytes for a 43-character name", func() {
			name := strings.Repeat("n", 43)
			desc := push(name, artifact.UninstallSupported, "long")
			Expect(r.packageArtifactOutcome(ctx, nw, packageNamed(name, enabled())).Kind).To(Equal(packageArtifactAccepted))

			got := announcements()
			Expect(got).To(HaveLen(1))
			action := strings.SplitN(got[0], " ", 4)[2]
			Expect(len(action)).To(BeNumerically("<=", eventActionLengthLimit))
			Expect(action).To(Equal(name + "@" + desc.Digest.String()))
		})

		DescribeTable("says that uninstall.enabled opts in only for an artifact declaring uninstall supported while it is false",
			func(declared artifact.Uninstall, uninstall *v1alpha1.Uninstall, optIn bool) {
				push("tuning", declared, "payload")
				Expect(r.packageArtifactOutcome(ctx, nw, packageNamed("tuning", uninstall)).Kind).To(Equal(packageArtifactAccepted))

				got := announcements()
				Expect(got).To(ConsistOf(ContainSubstring("declares uninstall " + string(declared))))
				if optIn {
					Expect(got[0]).To(ContainSubstring("setting uninstall.enabled to true opts the package in"))
				} else {
					Expect(got[0]).NotTo(ContainSubstring("uninstall.enabled"))
				}
			},
			Entry("supported, enabled false", artifact.UninstallSupported, nil, true),
			Entry("supported, enabled true", artifact.UninstallSupported, enabled(), false),
			Entry("unsupported, enabled false", artifact.UninstallUnsupported, nil, false),
		)
	})
})
