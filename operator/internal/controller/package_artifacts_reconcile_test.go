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
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/preflight"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A NodeWright with package artifacts on, end to end through the heavy pass and the Job
// controller: the preflight Job a pass builds first, the result the Job controller reads from the
// pod it completed with, and every later stage Job a pass builds from that result. Fake client
// only, driven through BuildState as a reconcile reads the world, so no spec shares the suite's
// manager or apiserver. A Job completes as the Job controller completes it: a Succeeded child pod
// with the Job's template, then the Job Complete.
var _ = Describe("package artifact preflight end to end", func() {
	const (
		namespace      = "nodewright"
		skyhookName    = "artifacts"
		nodeName       = "artifact-node"
		agentImage     = "ghcr.io/nvidia/nodewright/agent:1.2.3"
		serviceAccount = "nodewright-package"
		remedy         = "Set uninstall.apply or uninstall.enabled to false, or upgrade the package to a version whose artifact declares uninstall supported."
	)

	pullSecrets := []string{"regcred", "mirror"}
	digestOf := func(hex string) string { return "sha256:" + strings.Repeat(hex, 64) }
	pinnedA := "registry.example/tuning@" + digestOf("a")
	pinnedB := "registry.example/tuning@" + digestOf("b")
	cordonKey := fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, skyhookName)

	validOpts := func() SkyhookOperatorOptions {
		return SkyhookOperatorOptions{
			Namespace:             namespace,
			PackageServiceAccount: serviceAccount,
			CopyDirRoot:           "/var/lib/skyhook",
			AgentLogRoot:          "/var/log/skyhook",
			RuntimeRequiredTaint:  "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:            agentImage,
			PauseImage:            "registry.k8s.io/pause:3.10",
			MaxInterval:           10 * time.Minute,
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour,
				JobTTLFailed:    24 * time.Hour,
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
	}

	type harness struct {
		r        *SkyhookReconciler
		jr       *JobReconciler
		c        client.Client
		recorder *events.FakeRecorder
	}

	// newHarness builds both reconcilers over one fake client seeded with objects and the package
	// ServiceAccount, whose imagePullSecrets are pullSecrets. The client gives each object it
	// creates a UID, as the apiserver does, since the Job controller finds the pods of a Job by it.
	newHarness := func(on bool, objects ...client.Object) harness {
		GinkgoHelper()
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		account := &corev1.ServiceAccount{
			ObjectMeta:       metav1.ObjectMeta{Namespace: namespace, Name: serviceAccount},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: pullSecrets[0]}, {Name: pullSecrets[1]}},
		}
		var uids atomic.Int64
		c := interceptor.NewClient(fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(append(objects, account)...).
			WithStatusSubresource(&v1alpha1.NodeWright{}).
			WithIndex(&corev1.Pod{}, fieldSelectorNodeName, func(obj client.Object) []string {
				pod, ok := obj.(*corev1.Pod)
				if !ok {
					return nil
				}
				return []string{pod.Spec.NodeName}
			}).
			Build(), interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetUID() == "" {
					obj.SetUID(types.UID(fmt.Sprintf("uid-%d", uids.Add(1))))
				}
				return c.Create(ctx, obj, opts...)
			},
		})
		// Verbose so each message carries type, reason, action and note. Sized past every event a
		// spec records, and drained after every pass, since a full channel blocks Eventf.
		recorder := &events.FakeRecorder{Events: make(chan string, 500), Verbose: true}
		opts := validOpts()
		opts.PackageArtifacts = on
		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), recorder, opts)
		Expect(err).NotTo(HaveOccurred())
		jr := NewJobReconciler(c, c, k8sfake.NewClientset(), recorder, opts.JobOperatorOptions)
		return harness{r: r, jr: jr, c: c, recorder: recorder}
	}

	drainEvents := func(h harness) []string {
		var got []string
		for {
			select {
			case event := <-h.recorder.Events:
				got = append(got, event)
			default:
				return got
			}
		}
	}

	withPrefix := func(events []string, prefix string) []string {
		var got []string
		for _, event := range events {
			if strings.HasPrefix(event, prefix) {
				got = append(got, event)
			}
		}
		return got
	}

	storedNodeWright := func(h harness) *v1alpha1.NodeWright {
		GinkgoHelper()
		nw := &v1alpha1.NodeWright{}
		Expect(h.c.Get(ctx, types.NamespacedName{Name: skyhookName}, nw)).To(Succeed())
		return nw
	}

	storedNode := func(h harness, name string) *corev1.Node {
		GinkgoHelper()
		node := &corev1.Node{}
		Expect(h.c.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
		return node
	}

	stateOf := func(h harness) *clusterState {
		GinkgoHelper()
		var nodes corev1.NodeList
		Expect(h.c.List(ctx, &nodes)).To(Succeed())
		state, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*storedNodeWright(h)}}, &nodes, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		return state
	}

	// entryOf returns the persisted node-state entry of _package on the node.
	entryOf := func(h harness, node string, _package v1alpha1.Package) (*v1alpha1.PackageStatus, bool) {
		GinkgoHelper()
		sn, err := wrapper.NewSkyhookNodeOnly(storedNode(h, node), skyhookName)
		Expect(err).NotTo(HaveOccurred())
		return sn.PackageStatus(_package.GetUniqueName())
	}

	expectEntry := func(h harness, node string, _package v1alpha1.Package, stage v1alpha1.Stage, state v1alpha1.State) *v1alpha1.PackageStatus {
		GinkgoHelper()
		status, found := entryOf(h, node, _package)
		Expect(found).To(BeTrue())
		Expect(status.Stage).To(Equal(stage))
		Expect(status.State).To(Equal(state))
		return status
	}

	// nodeStatusOf is the Status of the node named node for the NodeWright.
	nodeStatusOf := func(h harness, node string) v1alpha1.Status {
		GinkgoHelper()
		sn, err := wrapper.NewSkyhookNodeOnly(storedNode(h, node), skyhookName)
		Expect(err).NotTo(HaveOccurred())
		return sn.Status()
	}

	conditionOf := func(h harness, conditionType string) *metav1.Condition {
		GinkgoHelper()
		return meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, conditionType)
	}

	cordoned := func(h harness, node string) bool {
		GinkgoHelper()
		persisted := storedNode(h, node)
		_, annotated := persisted.Annotations[cordonKey]
		return persisted.Spec.Unschedulable || annotated
	}

	type passOutcome struct {
		events   []string
		released bool
	}

	// pass does for the one NodeWright what a reconcile does: resolveUninstallDecisions,
	// refreshSkyhookConditions, HandleFinalizer, then RunSkyhookPackages unless the finalizer
	// released the NodeWright.
	pass := func(h harness) passOutcome {
		GinkgoHelper()
		state := stateOf(h)
		sn := state.skyhooks[0]
		h.r.resolveUninstallDecisions(ctx, sn)
		Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
		released, err := h.r.HandleFinalizer(ctx, sn, state)
		Expect(err).NotTo(HaveOccurred())
		if !released {
			_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), sn)
			Expect(err).NotTo(HaveOccurred())
		}
		return passOutcome{events: drainEvents(h), released: released}
	}

	sweep := func(h harness) bool {
		GinkgoHelper()
		update, err := h.r.ValidateRunningPackages(ctx, stateOf(h).skyhooks[0])
		Expect(err).NotTo(HaveOccurred())
		return update
	}

	jobsOf := func(h harness) []batchv1.Job {
		GinkgoHelper()
		var jobs batchv1.JobList
		Expect(h.c.List(ctx, &jobs, client.InNamespace(namespace))).To(Succeed())
		return jobs.Items
	}

	jobOf := func(h harness, name string) batchv1.Job {
		GinkgoHelper()
		var job batchv1.Job
		Expect(h.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &job)).To(Succeed())
		return job
	}

	gone := func(h harness, name string) bool {
		GinkgoHelper()
		var job batchv1.Job
		err := h.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &job)
		if apierrors.IsNotFound(err) {
			return true
		}
		Expect(err).NotTo(HaveOccurred())
		return job.DeletionTimestamp != nil
	}

	invalid := func(h harness, name string) bool {
		GinkgoHelper()
		job := jobOf(h, name)
		isInvalid, err := IsInvalidPackage(&job)
		Expect(err).NotTo(HaveOccurred())
		return isInvalid
	}

	referenceOf := func(_package v1alpha1.Package) string {
		GinkgoHelper()
		reference, err := packageReference(&_package)
		Expect(err).NotTo(HaveOccurred())
		return reference
	}

	// expected stamps what ApplyPackage stamps on a built Job, so it compares with a created one.
	expected := func(h harness, job *batchv1.Job, _package v1alpha1.Package, stage v1alpha1.Stage) *batchv1.Job {
		GinkgoHelper()
		Expect(setJobPackage(job, storedNodeWright(h), _package.Image, stage, &_package)).To(Succeed())
		return job
	}
	skyhookOf := func(h harness) *wrapper.Skyhook { return wrapper.NewSkyhookWrapper(storedNodeWright(h)) }
	preflightJobFor := func(h harness, _package v1alpha1.Package, node string) *batchv1.Job {
		GinkgoHelper()
		return expected(h, createPreflightJobFromPackage(h.r.opts, &_package, skyhookOf(h), node, referenceOf(_package), pullSecrets),
			_package, v1alpha1.StagePreflight)
	}
	legacyJobFor := func(h harness, _package v1alpha1.Package, node string, stage v1alpha1.Stage) *batchv1.Job {
		GinkgoHelper()
		return expected(h, createJobFromPackage(h.r.opts, &_package, skyhookOf(h), node, stage), _package, stage)
	}
	artifactJobFor := func(h harness, _package v1alpha1.Package, node string, stage v1alpha1.Stage, pinned string) *batchv1.Job {
		GinkgoHelper()
		return expected(h, createArtifactJobFromPackage(h.r.opts, &_package, skyhookOf(h), node, stage, pinned, pullSecrets), _package, stage)
	}

	jsonOf := func(v any) string {
		GinkgoHelper()
		data, err := json.Marshal(v)
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	// expectJob compares through JSON, since a Job read back from the client carries its
	// quantities in canonical form, and leaves out the TTL the Job controller sets once it is done.
	expectJob := func(h harness, want *batchv1.Job) batchv1.Job {
		GinkgoHelper()
		got := jobOf(h, want.Name)
		Expect(got.Labels).To(Equal(want.Labels))
		spec := got.Spec.DeepCopy()
		spec.TTLSecondsAfterFinished = nil
		Expect(jsonOf(spec)).To(MatchJSON(jsonOf(want.Spec)))
		return got
	}

	// completeJob completes the Job named name, as the Job controller would once its pod
	// succeeded, and has the JobReconciler reconcile it. It returns the events that recorded.
	completeJob := func(h harness, name string) []string {
		GinkgoHelper()
		job := jobOf(h, name)
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
		Expect(h.c.Status().Update(ctx, &job)).To(Succeed())
		_, err := h.jr.JobReconcile(ctx, &job)
		Expect(err).NotTo(HaveOccurred())
		return drainEvents(h)
	}

	// completePreflight completes the preflight Job of _package on node with a Succeeded child pod
	// whose preflight container terminated with message, as the agent leaves it.
	completePreflight := func(h harness, _package v1alpha1.Package, node, message string) []string {
		GinkgoHelper()
		job := jobOf(h, preflightJobFor(h, _package, node).Name)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      job.Name + "-" + string(job.UID),
				Labels:    map[string]string{batchControllerUIDLabel: string(job.UID)},
			},
			Spec: job.Spec.Template.Spec,
		}
		Expect(h.c.Create(ctx, pod)).To(Succeed())
		pod.Status = corev1.PodStatus{Phase: corev1.PodSucceeded, InitContainerStatuses: []corev1.ContainerStatus{{
			Name:  preflightContainerName(&_package),
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}},
		}}}
		Expect(h.c.Status().Update(ctx, pod)).To(Succeed())
		return completeJob(h, job.Name)
	}

	// resultFor is what the agent writes for outcome at the reference of _package.
	resultFor := func(_package v1alpha1.Package, outcome preflight.Outcome) string {
		GinkgoHelper()
		data, err := preflight.Encode(preflight.Result{Reference: referenceOf(_package), Outcome: outcome, Detail: "the detail"})
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	// supportedArtifact is the outcome of an accepted artifact declaring uninstall supported.
	supportedArtifact := func(pinned string) preflight.Outcome {
		return preflight.Outcome{Kind: preflight.KindArtifact, Pinned: pinned, Uninstall: artifact.UninstallSupported}
	}
	imageOutcome := preflight.Outcome{Kind: preflight.KindImage, Pinned: pinnedA}
	rejectedOutcome := preflight.Outcome{Kind: preflight.KindRejected, Reason: preflight.ReasonHostFiles}

	// supported and unsupported are the results of an accepted artifact declaring uninstall so.
	supported := func(pinned string) *v1alpha1.PreflightResult {
		return &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}
	}
	unsupported := func(pinned string) *v1alpha1.PreflightResult {
		return &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "unsupported"}
	}
	image := &v1alpha1.PreflightResult{Kind: "image", Pinned: pinnedA}

	// foundAt is result as a preflight of reference records it.
	foundAt := func(reference string, result *v1alpha1.PreflightResult) *v1alpha1.PreflightResult {
		found := *result
		found.Reference = reference
		return &found
	}

	tuningAt := func(version string) v1alpha1.Package {
		return v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: version}, Image: "registry.example/tuning"}
	}

	newNodeWright := func(packages ...v1alpha1.Package) *v1alpha1.NodeWright {
		nw := &v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: skyhookName, UID: "artifacts-uid", Generation: 1},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
		}
		for _, p := range packages {
			nw.Spec.Packages[p.Name] = p
		}
		return nw
	}

	// editTuning edits the spec of the tuning package in the stored NodeWright.
	editTuning := func(h harness, edit func(*v1alpha1.Package)) v1alpha1.Package {
		GinkgoHelper()
		current := storedNodeWright(h)
		edited := current.Spec.Packages["tuning"]
		edit(&edited)
		current.Spec.Packages["tuning"] = edited
		Expect(h.c.Update(ctx, current)).To(Succeed())
		return edited
	}

	newNode := func(name string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	type entry struct {
		pkg    v1alpha1.Package
		stage  v1alpha1.Stage
		state  v1alpha1.State
		result *v1alpha1.PreflightResult
	}

	// nodeWith returns a node whose state holds every entry, with its preflight result.
	nodeWith := func(name string, entries ...entry) *corev1.Node {
		GinkgoHelper()
		node := newNode(name)
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(sn.Upsert(e.pkg.PackageRef, e.pkg.Image, e.state, e.stage, 0, e.pkg.ContainerSHA)).To(Succeed())
			Expect(sn.SetPreflight(e.pkg.PackageRef, e.result)).To(Succeed())
		}
		return node
	}

	It("builds the preflight Job first, as the package ServiceAccount with its pull secrets, and nothing else until it reports", func() {
		tuning := tuningAt("1.0.0")
		h := newHarness(true, newNodeWright(tuning), newNode(nodeName))

		p := pass(h)
		Expect(jobsOf(h)).To(HaveLen(1))
		built := expectJob(h, preflightJobFor(h, tuning, nodeName))
		spec := built.Spec.Template.Spec
		Expect(spec.InitContainers).To(ConsistOf(HaveField("Env", ContainElements(
			corev1.EnvVar{Name: "NODEWRIGHT_PREFLIGHT_REF", Value: "registry.example/tuning:1.0.0"},
			corev1.EnvVar{Name: "NODEWRIGHT_REGISTRY_AUTH_FILES", Value: "/var/run/secrets/nodewright/registry-auth/0/.dockerconfigjson:" +
				"/var/run/secrets/nodewright/registry-auth/1/.dockerconfigjson"},
		))))
		Expect(spec.ServiceAccountName).To(Equal(serviceAccount))
		Expect(spec.AutomountServiceAccountToken).To(HaveValue(BeFalse()))
		Expect(spec.ImagePullSecrets).To(BeEmpty())
		Expect(expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateInProgress).Preflight).To(BeNil())
		Expect(p.events).To(ContainElement(And(HavePrefix("Normal Apply ApplyPackage "), ContainSubstring("stage [preflight]"))))

		pass(h)
		Expect(jobsOf(h)).To(HaveLen(1))
		Expect(jobOf(h, built.Name).UID).To(Equal(built.UID))
		Expect(sweep(h)).To(BeFalse())
	})

	DescribeTable("builds every later stage Job from the result the preflight reported",
		// message is what the preflight container left for the reference; warning is the error
		// class the apply Job's InspectionFailed Warning names, or "" for none.
		func(message func(tuning v1alpha1.Package) string, recorded *v1alpha1.PreflightResult, artifactJobs bool, warning string) {
			tuning := tuningAt("1.0.0")
			h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
			pass(h)

			reported := completePreflight(h, tuning, nodeName, message(tuning))
			Expect(expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete).Preflight).To(Equal(foundAt(referenceOf(tuning), recorded)))
			eventType := "Normal"
			if recorded.Kind == "unknown" {
				eventType = "Warning"
			}
			Expect(withPrefix(reported, eventType+" Preflight Preflight Package [tuning:1.0.0] ")).To(HaveLen(1))

			jobFor := func(stage v1alpha1.Stage) *batchv1.Job {
				if artifactJobs {
					return artifactJobFor(h, tuning, nodeName, stage, recorded.Pinned)
				}
				return legacyJobFor(h, tuning, nodeName, stage)
			}

			p := pass(h)
			apply := jobFor(v1alpha1.StageApply)
			expectJob(h, apply)
			Expect(expectEntry(h, nodeName, tuning, v1alpha1.StageApply, v1alpha1.StateInProgress).Preflight).To(Equal(foundAt(referenceOf(tuning), recorded)))
			failed := withPrefix(p.events, "Warning PackageArtifactInspectionFailed ")
			if warning == "" {
				Expect(failed).To(BeEmpty())
			} else {
				Expect(failed).To(ConsistOf(And(
					HavePrefix("Warning PackageArtifactInspectionFailed tuning:1.0.0 package [tuning:1.0.0] reference [registry.example/tuning:1.0.0] "),
					ContainSubstring("could not be inspected ("+warning+"), so it runs as a container image"),
				)))
			}

			completeJob(h, apply.Name)
			p = pass(h)
			expectJob(h, jobFor(v1alpha1.StageConfig))
			Expect(withPrefix(p.events, "Warning PackageArtifactInspectionFailed ")).To(BeEmpty())
			Expect(jobsOf(h)).To(HaveLen(3))
			Expect(sweep(h)).To(BeFalse())
			Expect(invalid(h, apply.Name)).To(BeFalse())
		},
		Entry("an accepted artifact, as artifact Jobs pinned to its digest",
			func(tuning v1alpha1.Package) string {
				return resultFor(tuning, supportedArtifact(pinnedA))
			},
			supported(pinnedA), true, ""),
		Entry("a container image, as the Jobs the option off builds",
			func(tuning v1alpha1.Package) string { return resultFor(tuning, imageOutcome) },
			image, false, ""),
		Entry("a reference the agent could not inspect, as the option-off Jobs with a Warning",
			func(tuning v1alpha1.Package) string {
				return resultFor(tuning, preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorUnauthorized})
			},
			&v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "unauthorized"}, false, "unauthorized"),
		Entry("no message, as an agent without the preflight mode leaves it, as unknown",
			func(v1alpha1.Package) string { return "" },
			&v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "no-result"}, false, "no-result"),
		Entry("a message that is no result, as unknown",
			func(v1alpha1.Package) string { return "exec /agent: exec format error" },
			&v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "invalid-result"}, false, "invalid-result"),
	)

	It("holds a rejected artifact at preflight, in the condition and with its node erroring, until its Job expires, and runs preflight again then", func() {
		tuning := tuningAt("1.0.0")
		h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
		pass(h)

		reported := completePreflight(h, tuning, nodeName, resultFor(tuning, rejectedOutcome))
		Expect(withPrefix(reported, "Warning Preflight ")).To(ConsistOf(ContainSubstring("is a package artifact that is rejected (host-files): the detail")))
		status := expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateErroring)
		Expect(nodeStatusOf(h, nodeName)).To(Equal(v1alpha1.StatusErroring))
		Expect(status.Preflight).To(Equal(&v1alpha1.PreflightResult{Reference: referenceOf(tuning), Kind: "rejected", Reason: "host-files", Rejections: 1}))
		held := jobOf(h, preflightJobFor(h, tuning, nodeName).Name)
		Expect(held.Spec.TTLSecondsAfterFinished).To(HaveValue(BeEquivalentTo(int32(preflightRejectedRetry.Seconds()))))

		for range 2 {
			pass(h)
			Expect(jobsOf(h)).To(HaveLen(1))
			Expect(jobOf(h, held.Name).UID).To(Equal(held.UID))
			Expect(sweep(h)).To(BeFalse())
			expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateErroring)
			Expect(nodeStatusOf(h, nodeName)).To(Equal(v1alpha1.StatusErroring))
			condition := conditionOf(h, wrapper.SkyhookConditionPackageArtifactRejected)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Message).To(Equal("1 package artifact rejected: tuning:1.0.0 (host-files)"))
		}

		// The TTL controller deletes the Job once preflightRejectedRetry has passed.
		Expect(h.c.Delete(ctx, &held)).To(Succeed())
		pass(h)
		Expect(jobOf(h, held.Name).UID).NotTo(Equal(held.UID))
		expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateInProgress)
		Expect(nodeStatusOf(h, nodeName)).To(Equal(v1alpha1.StatusErroring), "the retry does not hide the rejection")

		completePreflight(h, tuning, nodeName, resultFor(tuning, supportedArtifact(pinnedA)))
		pass(h)
		Expect(conditionOf(h, wrapper.SkyhookConditionPackageArtifactRejected)).To(BeNil())
		expectJob(h, artifactJobFor(h, tuning, nodeName, v1alpha1.StageApply, pinnedA))
		Expect(nodeStatusOf(h, nodeName)).To(Equal(v1alpha1.StatusInProgress))
	})

	It("records a reference no registry could serve as unknown with no preflight Job, and runs the option-off Jobs with a Warning", func() {
		tuning := tuningAt("1.0.0")
		tuning.ContainerSHA = "sha256:not-a-digest"
		h := newHarness(true, newNodeWright(tuning), newNode(nodeName))

		pass(h)
		Expect(jobsOf(h)).To(BeEmpty())
		status := expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateComplete)
		Expect(status.Preflight).To(Equal(&v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "invalid-reference"}))

		p := pass(h)
		Expect(jobsOf(h)).To(HaveLen(1))
		expectJob(h, legacyJobFor(h, tuning, nodeName, v1alpha1.StageApply))
		Expect(withPrefix(p.events, "Warning PackageArtifactInspectionFailed ")).To(ConsistOf(ContainSubstring("(invalid-reference)")))
	})

	It("upgrades through a preflight of the new version, then its upgrade Job from the result, which removes the old version", func() {
		older, tuning := tuningAt("1.0.0"), tuningAt("2.0.0")
		h := newHarness(true, newNodeWright(tuning), nodeWith(nodeName, entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil}))

		pass(h)
		Expect(jobsOf(h)).To(HaveLen(1))
		expectJob(h, preflightJobFor(h, tuning, nodeName))
		expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateInProgress)
		expectEntry(h, nodeName, older, v1alpha1.StageConfig, v1alpha1.StateComplete)

		completePreflight(h, tuning, nodeName, resultFor(tuning, supportedArtifact(pinnedB)))
		pass(h)
		upgrade := artifactJobFor(h, tuning, nodeName, v1alpha1.StageUpgrade, pinnedB)
		expectJob(h, upgrade)
		Expect(expectEntry(h, nodeName, tuning, v1alpha1.StageUpgrade, v1alpha1.StateInProgress).Preflight).To(Equal(foundAt(referenceOf(tuning), supported(pinnedB))))

		completeJob(h, upgrade.Name)
		_, found := entryOf(h, nodeName, older)
		Expect(found).To(BeFalse())
		pass(h)
		expectJob(h, artifactJobFor(h, tuning, nodeName, v1alpha1.StageConfig, pinnedB))
	})

	It("runs no preflight while the option is off: a first install starts at apply, an upgrade at upgrade", func() {
		tuning := tuningAt("1.0.0")
		h := newHarness(false, newNodeWright(tuning), newNode(nodeName))
		p := pass(h)
		Expect(jobsOf(h)).To(HaveLen(1))
		expectJob(h, legacyJobFor(h, tuning, nodeName, v1alpha1.StageApply))
		Expect(expectEntry(h, nodeName, tuning, v1alpha1.StageApply, v1alpha1.StateInProgress).Preflight).To(BeNil())
		Expect(withPrefix(p.events, "Warning ")).To(BeEmpty())

		older, upgraded := tuningAt("1.0.0"), tuningAt("2.0.0")
		h = newHarness(false, newNodeWright(upgraded), nodeWith(nodeName, entry{older, v1alpha1.StageConfig, v1alpha1.StateComplete, nil}))
		pass(h)
		Expect(jobsOf(h)).To(HaveLen(1))
		expectJob(h, legacyJobFor(h, upgraded, nodeName, v1alpha1.StageUpgrade))
		expectEntry(h, nodeName, upgraded, v1alpha1.StageUpgrade, v1alpha1.StateInProgress)
	})

	Describe("an interrupt package", func() {
		driverAt := func(version string) v1alpha1.Package {
			return v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "driver", Version: version},
				Image:      "registry.example/driver",
				Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
			}
		}
		driverImage := preflight.Outcome{Kind: preflight.KindImage, Pinned: "registry.example/driver@" + digestOf("d")}

		It("neither cordons nor drains its node until its preflight completes, and cordons before its apply then", func() {
			driver := driverAt("1.0.0")
			h := newHarness(true, newNodeWright(driver), newNode(nodeName))
			for range 2 {
				pass(h)
				Expect(cordoned(h, nodeName)).To(BeFalse())
				Expect(jobsOf(h)).To(HaveLen(1))
				expectJob(h, preflightJobFor(h, driver, nodeName))
			}

			completePreflight(h, driver, nodeName, resultFor(driver, driverImage))
			pass(h)
			Expect(cordoned(h, nodeName)).To(BeTrue())
			Expect(jobsOf(h)).To(HaveLen(1), "no apply Job before the node is drained")
		})

		It("cordons before its upgrade once the preflight of the new version completes", func() {
			older, driver := driverAt("1.0.0"), driverAt("2.0.0")
			h := newHarness(true, newNodeWright(driver), nodeWith(nodeName, entry{older, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete, nil}))
			pass(h)
			Expect(cordoned(h, nodeName)).To(BeFalse())
			Expect(jobsOf(h)).To(HaveLen(1))

			completePreflight(h, driver, nodeName, resultFor(driver, driverImage))
			pass(h)
			Expect(cordoned(h, nodeName)).To(BeTrue())
			Expect(jobsOf(h)).To(HaveLen(1), "no upgrade Job before the node is drained")
		})

		It("runs the agent as its interrupt Job's done container on a node that recorded an artifact, after the option is turned off", func() {
			driver := driverAt("1.0.0")
			h := newHarness(false, newNodeWright(driver),
				nodeWith(nodeName, entry{driver, v1alpha1.StageConfig, v1alpha1.StateComplete, supported("registry.example/driver@" + digestOf("d"))}))
			var interrupts []batchv1.Job
			for range 4 {
				pass(h)
				interrupts = slices.DeleteFunc(jobsOf(h), func(job batchv1.Job) bool { return !isInterruptJob(&job) })
				if len(interrupts) > 0 {
					break
				}
			}
			Expect(interrupts).To(HaveLen(1))
			Expect(interrupts[0].Spec.Template.Spec.Containers).To(ConsistOf(And(
				HaveField("Name", doneContainerName),
				HaveField("Image", agentImage),
				HaveField("Command", BeNil()),
			)))
		})
	})

	Describe("uninstall", func() {
		requested := func(_package v1alpha1.Package) v1alpha1.Package {
			_package.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
			return _package
		}

		DescribeTable("starts on a node as the Job its preflight result gives it",
			func(result *v1alpha1.PreflightResult, artifactJob bool) {
				tuning := requested(tuningAt("1.0.0"))
				h := newHarness(true, newNodeWright(tuning), nodeWith(nodeName, entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, result}))

				p := pass(h)
				Expect(expectEntry(h, nodeName, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress).Preflight).To(Equal(result))
				want := legacyJobFor(h, tuning, nodeName, v1alpha1.StageUninstall)
				if artifactJob {
					want = artifactJobFor(h, tuning, nodeName, v1alpha1.StageUninstall, result.Pinned)
				}
				Expect(jobsOf(h)).To(HaveLen(1))
				expectJob(h, want)
				Expect(withPrefix(p.events, "Warning ")).To(BeEmpty())
				Expect(conditionOf(h, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
			},
			Entry("an artifact declaring uninstall supported, pinned to the digest installed", supported(pinnedA), true),
			Entry("a container image", image, false),
			Entry("a package installed with the option off", nil, false),
		)

		It("is held back on a node whose artifact declares uninstall unsupported, with the condition and one Warning when it first lists the package", func() {
			tuning := requested(tuningAt("1.0.0"))
			h := newHarness(true, newNodeWright(tuning), nodeWith(nodeName, entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupported(pinnedA)}))

			for i := range 3 {
				p := pass(h)
				expectEntry(h, nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
				Expect(jobsOf(h)).To(BeEmpty())
				if i == 0 {
					Expect(withPrefix(p.events, "Warning UninstallUnsupported tuning:1.0.0 ")).To(ConsistOf(ContainSubstring(remedy)))
				} else {
					Expect(withPrefix(p.events, "Warning UninstallUnsupported ")).To(BeEmpty())
				}
				Expect(conditionOf(h, wrapper.SkyhookConditionUninstallUnsupported)).To(HaveField("Message",
					"1 package artifact declares uninstall unsupported: tuning:1.0.0. "+remedy))
			}
		})

		It("is decided per node: each node that can run it gets its Job, and the node holding it back is left as it is", func() {
			tuning := requested(tuningAt("1.0.0"))
			h := newHarness(true, newNodeWright(tuning),
				nodeWith("node-a", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupported(pinnedA)}),
				nodeWith("node-b", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supported(pinnedB)}),
				nodeWith("node-c", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, nil}))

			for i := range 2 {
				p := pass(h)
				expectEntry(h, "node-a", tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
				expectEntry(h, "node-b", tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
				expectEntry(h, "node-c", tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
				Expect(jobsOf(h)).To(HaveLen(2))
				expectJob(h, artifactJobFor(h, tuning, "node-b", v1alpha1.StageUninstall, pinnedB))
				expectJob(h, legacyJobFor(h, tuning, "node-c", v1alpha1.StageUninstall))
				Expect(withPrefix(p.events, "Warning UninstallUnsupported tuning:1.0.0 ")).To(HaveLen(1 - i))
				Expect(conditionOf(h, wrapper.SkyhookConditionUninstallUnsupported)).To(HaveField("Message",
					"1 package artifact declares uninstall unsupported: tuning:1.0.0. "+remedy))
			}
		})

		It("never cordons a node that holds back the uninstall of an interrupt package another node runs", func() {
			driver := requested(v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "driver", Version: "1.0.0"},
				Image:      "registry.example/driver",
				Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
			})
			h := newHarness(true, newNodeWright(driver),
				nodeWith("node-a", entry{driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete, unsupported("registry.example/driver@" + digestOf("a"))}),
				nodeWith("node-b", entry{driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete, supported("registry.example/driver@" + digestOf("b"))}))

			pass(h)
			Expect(cordoned(h, "node-b")).To(BeTrue())
			Expect(cordoned(h, "node-a")).To(BeFalse())
			expectEntry(h, "node-a", driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete)
			Expect(jobsOf(h)).To(BeEmpty())
		})

		// A node with work left is not skipped by the pass, so this is where a package another node
		// is uninstalling reaches the node holding its uninstall back.
		It("leaves the package alone on a node holding its uninstall back while that node installs another package", func() {
			tuning := requested(tuningAt("1.0.0"))
			driver := requested(v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "driver", Version: "1.0.0"},
				Image:      "registry.example/driver",
				Interrupt:  &v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
			})
			base := v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "base", Version: "1.0.0"}, Image: "registry.example/base"}
			// node-a is in the batch in flight, installing base.
			installing := nodeWith("node-a",
				entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupported(pinnedA)},
				entry{driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete, unsupported("registry.example/driver@" + digestOf("a"))},
				entry{base, v1alpha1.StagePreflight, v1alpha1.StateInProgress, nil})
			sn, err := wrapper.NewSkyhookNodeOnly(installing, skyhookName)
			Expect(err).NotTo(HaveOccurred())
			sn.SetStatus(v1alpha1.StatusInProgress)
			h := newHarness(true, newNodeWright(tuning, driver, base),
				installing,
				nodeWith("node-b",
					entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supported(pinnedB)},
					entry{driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete, supported("registry.example/driver@" + digestOf("b"))}))

			pass(h)
			expectEntry(h, "node-a", tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
			expectEntry(h, "node-a", driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete)
			Expect(cordoned(h, "node-a")).To(BeFalse())
			Expect(cordoned(h, "node-b")).To(BeTrue())
			var names []string
			for _, job := range jobsOf(h) {
				names = append(names, job.Name)
			}
			Expect(names).To(ConsistOf(
				preflightJobFor(h, base, "node-a").Name,
				preflightJobFor(h, base, "node-b").Name,
				artifactJobFor(h, tuning, "node-b", v1alpha1.StageUninstall, pinnedB).Name,
			))
		})

		It("removes an entry that never got past preflight, and retires its preflight Job", func() {
			tuning := tuningAt("1.0.0")
			h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
			pass(h)
			running := jobOf(h, preflightJobFor(h, tuning, nodeName).Name)

			editTuning(h, func(p *v1alpha1.Package) { p.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true} })
			pass(h)
			_, found := entryOf(h, nodeName, tuning)
			Expect(found).To(BeFalse())
			Expect(jobsOf(h)).To(HaveLen(1))
			Expect(sweep(h)).To(BeTrue())
			Expect(invalid(h, running.Name)).To(BeTrue())

			pass(h)
			Expect(jobsOf(h)).To(HaveLen(1))
			_, found = entryOf(h, nodeName, tuning)
			Expect(found).To(BeFalse())
		})

		Describe("on the deletion of the NodeWright", func() {
			deleting := func(_package v1alpha1.Package) *v1alpha1.NodeWright {
				nw := newNodeWright(_package)
				nw.DeletionTimestamp = ptr(metav1.Now())
				nw.Finalizers = []string{SkyhookFinalizer}
				return nw
			}
			tuning := tuningAt("1.0.0")
			tuning.Uninstall = &v1alpha1.Uninstall{Enabled: true}

			It("releases the NodeWright past a node that holds the uninstall back, which keeps its node state", func() {
				h := newHarness(true, deleting(tuning), nodeWith(nodeName, entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupported(pinnedA)}))

				Expect(pass(h).released).To(BeTrue())
				Expect(jobsOf(h)).To(BeEmpty())
				status, found := entryOf(h, nodeName, tuning)
				Expect(found).To(BeTrue())
				Expect(status.Preflight).To(Equal(unsupported(pinnedA)))
			})

			It("waits for the uninstall a node can run, and not for the node holding it back", func() {
				h := newHarness(true, deleting(tuning),
					nodeWith("node-a", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, unsupported(pinnedA)}),
					nodeWith("node-b", entry{tuning, v1alpha1.StageConfig, v1alpha1.StateComplete, supported(pinnedB)}))

				Expect(pass(h).released).To(BeFalse())
				Expect(conditionOf(h, wrapper.SkyhookConditionDeletionBlocked)).To(BeNil())
				uninstall := artifactJobFor(h, tuning, "node-b", v1alpha1.StageUninstall, pinnedB)
				Expect(jobsOf(h)).To(HaveLen(1))
				expectJob(h, uninstall)

				completeJob(h, uninstall.Name)
				_, found := entryOf(h, "node-b", tuning)
				Expect(found).To(BeFalse())
				Expect(pass(h).released).To(BeTrue())
				expectEntry(h, "node-a", tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
			})

			It("waits for a node at preflight only until the pass removes its entry", func() {
				h := newHarness(true, deleting(tuning), nodeWith(nodeName, entry{tuning, v1alpha1.StagePreflight, v1alpha1.StateInProgress, nil}))

				Expect(pass(h).released).To(BeFalse())
				_, found := entryOf(h, nodeName, tuning)
				Expect(found).To(BeFalse())
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(pass(h).released).To(BeTrue())
			})
		})
	})

	Describe("staleness", func() {
		DescribeTable("treats an unfinished stage Job as stale unless it is the Job the node's preflight result gives it",
			// pinned is the digest the existing Job pins, or "" for a legacy Job.
			func(pinned string, result *v1alpha1.PreflightResult, stale bool) {
				tuning := tuningAt("1.0.0")
				nw := newNodeWright(tuning)
				skyhook := wrapper.NewSkyhookWrapper(nw)
				built := createJobFromPackage(validOpts(), &tuning, skyhook, nodeName, v1alpha1.StageApply)
				if pinned != "" {
					built = createArtifactJobFromPackage(validOpts(), &tuning, skyhook, nodeName, v1alpha1.StageApply, pinned, pullSecrets)
				}
				Expect(setJobPackage(built, nw, tuning.Image, v1alpha1.StageApply, &tuning)).To(Succeed())
				h := newHarness(true, nw, nodeWith(nodeName, entry{tuning, v1alpha1.StageApply, v1alpha1.StateInProgress, result}), built)

				Expect(sweep(h)).To(Equal(stale))
				Expect(invalid(h, built.Name)).To(Equal(stale))
			},
			Entry("an artifact Job pinned to the recorded digest", pinnedA, supported(pinnedA), false),
			Entry("an artifact Job pinned to another digest", pinnedB, supported(pinnedA), true),
			Entry("a legacy Job for an accepted artifact", "", supported(pinnedA), true),
			Entry("an artifact Job for a container image", pinnedA, image, true),
			Entry("a legacy Job for a container image", "", image, false),
			Entry("a legacy Job for a package installed with the option off", "", nil, false),
		)

		It("treats an unfinished preflight Job as stale once the package names another reference, and builds one for that", func() {
			tuning := tuningAt("1.0.0")
			h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
			pass(h)
			old := jobOf(h, preflightJobFor(h, tuning, nodeName).Name)

			edited := editTuning(h, func(p *v1alpha1.Package) { p.ContainerSHA = digestOf("c") })
			Expect(sweep(h)).To(BeTrue())
			Expect(invalid(h, old.Name)).To(BeTrue())
			// JobReconcile reaps an invalid Job.
			invalidated := jobOf(h, old.Name)
			_, err := h.jr.JobReconcile(ctx, &invalidated)
			Expect(err).NotTo(HaveOccurred())
			Expect(gone(h, old.Name)).To(BeTrue())

			pass(h)
			built := expectJob(h, preflightJobFor(h, edited, nodeName))
			Expect(built.Spec.Template.Spec.InitContainers[0].Env).To(ContainElement(
				corev1.EnvVar{Name: "NODEWRIGHT_PREFLIGHT_REF", Value: "registry.example/tuning@" + digestOf("c")}))
		})

		It("deletes the Job holding a rejected preflight at once when the package is edited, so preflight runs for the edit", func() {
			tuning := tuningAt("1.0.0")
			h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
			pass(h)
			completePreflight(h, tuning, nodeName, resultFor(tuning, rejectedOutcome))
			held := jobOf(h, preflightJobFor(h, tuning, nodeName).Name)
			Expect(sweep(h)).To(BeFalse())

			edited := editTuning(h, func(p *v1alpha1.Package) { p.ContainerSHA = digestOf("c") })
			Expect(sweep(h)).To(BeTrue())
			Expect(gone(h, held.Name)).To(BeTrue())

			pass(h)
			expectJob(h, preflightJobFor(h, edited, nodeName))
			expectEntry(h, nodeName, edited, v1alpha1.StagePreflight, v1alpha1.StateInProgress)
		})

		It("keeps a completed preflight Job while its result stands", func() {
			tuning := tuningAt("1.0.0")
			h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
			pass(h)
			completePreflight(h, tuning, nodeName, resultFor(tuning, imageOutcome))
			name := preflightJobFor(h, tuning, nodeName).Name

			Expect(sweep(h)).To(BeFalse())
			pass(h)
			Expect(sweep(h)).To(BeFalse())
			Expect(gone(h, name)).To(BeFalse())
		})

		It("runs preflight again when an apply built from an unknown result meets an edited reference", func() {
			tuning := tuningAt("1.0.0")
			h := newHarness(true, newNodeWright(tuning), newNode(nodeName))
			pass(h)
			completePreflight(h, tuning, nodeName, resultFor(tuning, preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorNotFound}))
			pass(h)
			apply := expectJob(h, legacyJobFor(h, tuning, nodeName, v1alpha1.StageApply))

			edited := editTuning(h, func(p *v1alpha1.Package) { p.Image = "registry.example/tuning-fixed" })
			Expect(sweep(h)).To(BeTrue())
			Expect(invalid(h, apply.Name)).To(BeTrue())
			invalidated := jobOf(h, apply.Name)
			_, err := h.jr.JobReconcile(ctx, &invalidated)
			Expect(err).NotTo(HaveOccurred())
			Expect(gone(h, apply.Name)).To(BeTrue())

			old := preflightJobFor(h, tuning, nodeName).Name
			pass(h)
			Expect(gone(h, old)).To(BeTrue(), "the completed preflight Job of the old reference holds the name of the new one")
			expectEntry(h, nodeName, edited, v1alpha1.StageApply, v1alpha1.StateInProgress)
			pass(h)
			expectJob(h, preflightJobFor(h, edited, nodeName))
			expectEntry(h, nodeName, edited, v1alpha1.StagePreflight, v1alpha1.StateInProgress)

			fixed := "registry.example/tuning-fixed@" + digestOf("b")
			completePreflight(h, edited, nodeName, resultFor(edited, supportedArtifact(fixed)))
			pass(h)
			expectJob(h, artifactJobFor(h, edited, nodeName, v1alpha1.StageApply, fixed))
		})

		It("runs preflight again once an apply built from an unknown result fails, and an uninstall then waits for the install rather than dropping it", func() {
			tuning := tuningAt("1.0.0")
			unknown := foundAt(referenceOf(tuning), &v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "unreachable"})
			h := newHarness(true, newNodeWright(tuning), nodeWith(nodeName, entry{tuning, v1alpha1.StageApply, v1alpha1.StateErroring, unknown}))

			pass(h)
			expectJob(h, preflightJobFor(h, tuning, nodeName))
			Expect(expectEntry(h, nodeName, tuning, v1alpha1.StagePreflight, v1alpha1.StateInProgress).Preflight).To(HaveField("InstallStarted", BeTrue()))

			requested := editTuning(h, func(p *v1alpha1.Package) { p.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true} })
			pass(h)
			expectEntry(h, nodeName, requested, v1alpha1.StagePreflight, v1alpha1.StateInProgress)

			completePreflight(h, requested, nodeName, resultFor(requested, imageOutcome))
			Expect(expectEntry(h, nodeName, requested, v1alpha1.StagePreflight, v1alpha1.StateComplete).Preflight).To(
				Equal(&v1alpha1.PreflightResult{Reference: referenceOf(requested), Kind: "image", Pinned: pinnedA, InstallStarted: true}))
			pass(h)
			expectEntry(h, nodeName, requested, v1alpha1.StageApply, v1alpha1.StateInProgress)
			expectJob(h, legacyJobFor(h, requested, nodeName, v1alpha1.StageApply))
		})
	})
})
