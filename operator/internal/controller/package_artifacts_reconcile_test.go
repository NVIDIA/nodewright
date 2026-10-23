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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	skyhookv1 "github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/drain"
	operatorversion "github.com/NVIDIA/nodewright/operator/internal/version"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The reconcile side of package artifacts: which stage Job a package gets from its inspection
// outcome, the Warnings and condition that report a refusal, and how the Jobs built that way
// fare in the staleness sweeps. Fake client only, driven through BuildState as a reconcile
// reads the world, so no spec shares the suite's manager or apiserver.
var _ = Describe("package artifact stage decision", func() {
	const (
		namespace   = "nodewright"
		skyhookName = "artifacts"
		nodeName    = "artifact-node"
		version     = "1.0.0"
		pullSecret  = "registry-credentials"
		username    = "test-user"
		password    = "test-password-placeholder"
		agentImage  = "ghcr.io/nvidia/nodewright/agent:1.2.3"
	)

	cordonKey := fmt.Sprintf("%s/cordon_%s", v1alpha1.METADATA_PREFIX, skyhookName)

	var fakeClock *testingclock.FakePassiveClock

	BeforeEach(func() {
		fakeClock = testingclock.NewFakePassiveClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	})

	stepPast := func(d time.Duration) {
		fakeClock.SetTime(fakeClock.Now().Add(d + time.Second))
	}

	validOpts := func() SkyhookOperatorOptions {
		return SkyhookOperatorOptions{
			Namespace:            namespace,
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           agentImage,
			PauseImage:           "registry.k8s.io/pause:3.10",
			MaxInterval:          10 * time.Minute,
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour,
				JobTTLFailed:    24 * time.Hour,
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
	}

	// setup is how a spec's reconciler is built. An inspector is injected whatever off says.
	type setup struct {
		off        bool
		httpClient *http.Client
		pullSecret string
		// timeout bounds one inspection; zero is packageArtifactRequestTimeout.
		timeout time.Duration
		funcs   interceptor.Funcs
	}

	type harness struct {
		r        *SkyhookReconciler
		c        client.Client
		recorder *events.FakeRecorder
	}

	newHarness := func(s setup, objects ...client.Object) harness {
		GinkgoHelper()
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		// The legacy kind, which Reconcile lists first for its migration hold.
		Expect(skyhookv1.AddToScheme(scheme)).To(Succeed())
		c := interceptor.NewClient(fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objects...).
			WithStatusSubresource(&v1alpha1.NodeWright{}).
			WithIndex(&corev1.Pod{}, fieldSelectorNodeName, func(obj client.Object) []string {
				pod, ok := obj.(*corev1.Pod)
				if !ok {
					return nil
				}
				return []string{pod.Spec.NodeName}
			}).
			Build(), s.funcs)
		// Verbose so each message carries type, reason, action and note. Sized past every event a
		// spec records, and drained after every pass, since a full channel blocks Eventf.
		recorder := &events.FakeRecorder{Events: make(chan string, 500), Verbose: true}
		opts := validOpts()
		opts.PackageArtifacts = !s.off
		opts.ImagePullSecret = s.pullSecret
		cache, err := newPackageArtifactCache(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		inspector := newPackageArtifactInspector(c, recorder, namespace, opts.PullSecrets(), cache)
		inspector.httpClient = s.httpClient
		inspector.clock = fakeClock
		inspector.requestTimeout = s.timeout
		inspector.resolveTimeout = s.timeout
		opts.artifactInspector = inspector
		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), recorder, opts)
		Expect(err).NotTo(HaveOccurred())
		return harness{r: r, c: c, recorder: recorder}
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

	warnings := func(events []string, reason string) []string {
		var got []string
		for _, event := range events {
			if strings.HasPrefix(event, corev1.EventTypeWarning+" "+reason+" ") {
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

	stateOf := func(h harness) *clusterState {
		GinkgoHelper()
		var nodes corev1.NodeList
		Expect(h.c.List(ctx, &nodes)).To(Succeed())
		state, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*storedNodeWright(h)}}, &nodes, &v1alpha1.DeploymentPolicyList{})
		Expect(err).NotTo(HaveOccurred())
		return state
	}

	// inspected resolves the package artifacts of the NodeWright as persisted, which Reconcile does
	// first, and processes every inspection that asks for, as the inspector's workers would between
	// passes. It returns the requeue resolvePackageArtifacts asked for.
	inspected := func(h harness) *ctrl.Result {
		GinkgoHelper()
		result := resolvedPackageArtifacts(h.r, stateOf(h).skyhooks[0])
		h.r.opts.artifactInspector.drain(ctx)
		return result
	}

	// decided is the state as persisted once every inspection a pass asks for is recorded, with the
	// recorded results resolved and the uninstall decisions made, which Reconcile does first, so
	// that HandleFinalizer and RunSkyhookPackages read them as they do there.
	decided := func(h harness) *clusterState {
		GinkgoHelper()
		inspected(h)
		state := stateOf(h)
		resolvedPackageArtifacts(h.r, state.skyhooks[0])
		h.r.resolveUninstallDecisions(ctx, state.skyhooks[0])
		return state
	}

	// pass makes one RunSkyhookPackages pass over the NodeWright and Nodes as persisted, once the
	// uninstall decisions are resolved, and returns the events it recorded with its error.
	pass := func(h harness) ([]string, error) {
		GinkgoHelper()
		state := decided(h)
		_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), state.skyhooks[0])
		return drainEvents(h), err
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

	// gone reports whether the Job is deleted, or being deleted: foreground deletion leaves it
	// visible until its children are gone.
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
		var job batchv1.Job
		Expect(h.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &job)).To(Succeed())
		isInvalid, err := IsInvalidPackage(&job)
		Expect(err).NotTo(HaveOccurred())
		return isInvalid
	}

	// blockedCondition is the Blocked condition of an unusable package artifact on the NodeWright as
	// persisted, or nil.
	blockedCondition := func(h harness) *metav1.Condition {
		GinkgoHelper()
		condition := meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionBlocked)
		if condition == nil || condition.Reason != wrapper.SkyhookReasonPackageArtifactUnusable {
			return nil
		}
		return condition
	}

	// nodeStatus is the status of the node nodeName for the NodeWright as persisted.
	nodeStatus := func(h harness) v1alpha1.Status {
		GinkgoHelper()
		node := &corev1.Node{}
		Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).NotTo(HaveOccurred())
		return sn.Status()
	}

	skyhookNodeOf := func(h harness, node *corev1.Node) wrapper.SkyhookNode {
		GinkgoHelper()
		sn, err := wrapper.NewSkyhookNode(node, storedNodeWright(h))
		Expect(err).NotTo(HaveOccurred())
		return sn
	}

	// applyPackage applies _package on node once every inspection a pass asks for is recorded.
	applyPackage := func(h harness, node *corev1.Node, _package v1alpha1.Package) error {
		GinkgoHelper()
		inspected(h)
		return h.r.ApplyPackage(ctx, GinkgoLogr, nil, skyhookNodeOf(h, node), &_package, false)
	}

	// expected stamps what ApplyPackage stamps on a built Job, so it compares with a created one.
	expected := func(h harness, job *batchv1.Job, _package v1alpha1.Package, stage v1alpha1.Stage) *batchv1.Job {
		GinkgoHelper()
		Expect(setJobPackage(job, storedNodeWright(h), _package.Image, stage, &_package)).To(Succeed())
		return job
	}
	legacyJob := func(h harness, _package v1alpha1.Package, node string, stage v1alpha1.Stage) *batchv1.Job {
		GinkgoHelper()
		return expected(h, createJobFromPackage(h.r.opts, &_package, wrapper.NewSkyhookWrapper(storedNodeWright(h)), node, stage), _package, stage)
	}
	// imageJob is the Job of a container-image package, its package image pinned to the digest a
	// pass resolved, as the option on builds it.
	imageJob := func(h harness, _package v1alpha1.Package, node string, stage v1alpha1.Stage, pinned string) *batchv1.Job {
		GinkgoHelper()
		return expected(h, createStageJob(h.r.opts, &_package, wrapper.NewSkyhookWrapper(storedNodeWright(h)), node, stage,
			packageArtifactOutcome{Kind: packageArtifactImage, Pinned: pinned}), _package, stage)
	}
	artifactJob := func(h harness, _package v1alpha1.Package, node string, stage v1alpha1.Stage, pinned string) *batchv1.Job {
		GinkgoHelper()
		return expected(h, createArtifactJobFromPackage(h.r.opts, &_package, wrapper.NewSkyhookWrapper(storedNodeWright(h)), node, stage, pinned), _package, stage)
	}

	jsonOf := func(v any) string {
		GinkgoHelper()
		data, err := json.Marshal(v)
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	// expectJob compares through JSON, since a Job read back from the client carries its
	// quantities in canonical form.
	expectJob := func(got batchv1.Job, want *batchv1.Job) {
		GinkgoHelper()
		Expect(got.Name).To(Equal(want.Name))
		Expect(got.Labels).To(Equal(want.Labels))
		Expect(jsonOf(got.Spec)).To(MatchJSON(jsonOf(want.Spec)))
	}

	jobOf := func(h harness, name string) batchv1.Job {
		GinkgoHelper()
		var job batchv1.Job
		Expect(h.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &job)).To(Succeed())
		return job
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

	newNode := func(name string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	// nodeWith returns a node whose state holds _package at stage and state.
	nodeWith := func(name string, _package v1alpha1.Package, stage v1alpha1.Stage, state v1alpha1.State) *corev1.Node {
		GinkgoHelper()
		node := newNode(name)
		sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
		Expect(err).NotTo(HaveOccurred())
		Expect(sn.Upsert(_package.PackageRef, _package.Image, state, stage, 0, _package.ContainerSHA)).To(Succeed())
		return node
	}

	newRegistry := func(opts registrytest.Options) *registrytest.Registry {
		reg := registrytest.New(opts)
		DeferCleanup(reg.Close)
		return reg
	}

	packageAt := func(reg *registrytest.Registry, name, repository string) v1alpha1.Package {
		return v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: reg.Host() + "/" + repository}
	}

	pinnedAt := func(reg *registrytest.Registry, repository string, desc ocispec.Descriptor) string {
		return reg.Host() + "/" + repository + "@" + desc.Digest.String()
	}

	// pushPackage pushes a package under the tag version, filling in what spec leaves out.
	pushPackage := func(reg *registrytest.Registry, repository string, spec registrytest.PackageSpec) ocispec.Descriptor {
		GinkgoHelper()
		if spec.Uninstall == "" {
			spec.Uninstall = artifact.UninstallSupported
		}
		if spec.Config == nil {
			spec.Config = json.RawMessage(`{}`)
		}
		if spec.Payload == nil {
			spec.Payload = []registrytest.Layer{{Gzip: []byte("payload")}}
		}
		desc, _, err := registrytest.PushPackage(ctx, reg.Repository(repository), version, spec)
		Expect(err).NotTo(HaveOccurred())
		return desc
	}

	pushImage := func(reg *registrytest.Registry, repository string) ocispec.Descriptor {
		GinkgoHelper()
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "hello"), []byte("hello"), 0o644)).To(Succeed())
		desc, err := registrytest.PushImage(ctx, reg.Repository(repository), version, dir)
		Expect(err).NotTo(HaveOccurred())
		return desc
	}

	// pushPackageManifest pushes a package manifest of artifactType with no payload layer.
	pushPackageManifest := func(reg *registrytest.Registry, repository, artifactType, tag string) ocispec.Descriptor {
		GinkgoHelper()
		metadata, err := registrytest.PushBlob(ctx, reg.Repository(repository), artifact.MediaTypeMetadata, []byte(`{"uninstall":"supported","config":{}}`))
		Expect(err).NotTo(HaveOccurred())
		data, err := json.Marshal(ocispec.Manifest{
			Versioned:    specs.Versioned{SchemaVersion: 2},
			MediaType:    ocispec.MediaTypeImageManifest,
			ArtifactType: artifactType,
			Config:       metadata,
			Layers:       []ocispec.Descriptor{},
		})
		Expect(err).NotTo(HaveOccurred())
		desc, err := registrytest.PushManifest(ctx, reg.Repository(repository), ocispec.MediaTypeImageManifest, data, tag)
		Expect(err).NotTo(HaveOccurred())
		return desc
	}
	pushNoPayload := func(reg *registrytest.Registry, repository, tag string) ocispec.Descriptor {
		GinkgoHelper()
		return pushPackageManifest(reg, repository, artifact.ArtifactTypePackage, tag)
	}
	pushV2Package := func(reg *registrytest.Registry, repository, tag string) ocispec.Descriptor {
		GinkgoHelper()
		return pushPackageManifest(reg, repository, "application/vnd.nvidia.nodewright.package.v2", tag)
	}

	hostFiles := registrytest.PackageSpec{HostFiles: []registrytest.HostFilesLayer{
		{Layer: registrytest.Layer{Gzip: []byte("host files")}, Destination: "/etc/nodewright"},
	}}
	declaresInterrupt := registrytest.PackageSpec{Interrupt: &artifact.Interrupt{
		Sense: artifact.InterruptStep{Path: "sense.sh", Arguments: []string{}},
		Run:   artifact.InterruptStep{Path: "run.sh", Arguments: []string{}, OnHost: true},
	}}

	pullSecretFor := func(reg *registrytest.Registry, secretPassword string) *corev1.Secret {
		GinkgoHelper()
		data, err := json.Marshal(map[string]any{
			"auths": map[string]any{reg.Host(): map[string]string{"username": username, "password": secretPassword}},
		})
		Expect(err).NotTo(HaveOccurred())
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pullSecret},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: data},
		}
	}

	hasEnv := func(job batchv1.Job, name string) bool {
		return slices.ContainsFunc(job.Spec.Template.Spec.InitContainers, func(c corev1.Container) bool {
			return slices.ContainsFunc(c.Env, func(env corev1.EnvVar) bool { return env.Name == name })
		})
	}

	It("cuts the package action of a Warning to 128 bytes and leaves a shorter one whole", func() {
		short := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: version}}
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

	It("asks no registry and builds the current Jobs while the option is off and nothing is recorded", func() {
		reg := newRegistry(registrytest.Options{})
		pushPackage(reg, "tuning", registrytest.PackageSpec{})
		pushPackage(reg, "host", hostFiles)
		pushPackage(reg, "cold", registrytest.PackageSpec{})
		tuning := packageAt(reg, "tuning", "tuning")
		host := packageAt(reg, "host", "host")
		cold := packageAt(reg, "cold", "cold")

		nw := newNodeWright(tuning, host, cold)
		// A condition left from when the option was on, which the option being off removes.
		nw.Status.Conditions = []metav1.Condition{{
			Type: wrapper.SkyhookConditionBlocked, Status: metav1.ConditionTrue,
			Reason: wrapper.SkyhookReasonPackageArtifactUnusable, Message: "1 package blocked: host:1.0.0 (host-files)",
			LastTransitionTime: metav1.Now(),
		}}
		h := newHarness(setup{off: true, httpClient: reg.Client()}, nw, newNode(nodeName))

		state := stateOf(h)
		Expect(h.r.refreshSkyhookConditions(ctx, state, state.skyhooks[0])).To(Succeed())
		Expect(meta.FindStatusCondition(state.skyhooks[0].GetSkyhook().Status.Conditions, wrapper.SkyhookConditionBlocked)).To(BeNil())

		events, err := pass(h)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).NotTo(ContainElement(HavePrefix(corev1.EventTypeWarning + " PackageArtifact")))
		jobs := jobsOf(h)
		Expect(jobs).To(HaveLen(3))
		for _, p := range []v1alpha1.Package{tuning, host, cold} {
			want := legacyJob(h, p, nodeName, v1alpha1.StageApply)
			expectJob(jobOf(h, want.Name), want)
		}

		Expect(sweep(h)).To(BeFalse())
		for _, job := range jobs {
			Expect(invalid(h, job.Name)).To(BeFalse())
		}

		applyNode := newNode("apply-node")
		Expect(applyPackage(h, applyNode, tuning)).To(Succeed())
		want := legacyJob(h, tuning, applyNode.Name, v1alpha1.StageApply)
		expectJob(jobOf(h, want.Name), want)

		interruptNode := newNode("interrupt-node")
		interrupt := &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
		Expect(h.r.Interrupt(ctx, skyhookNodeOf(h, interruptNode), &tuning, interrupt, v1alpha1.StageInterrupt)).To(Succeed())
		args, err := interrupt.ToArgs()
		Expect(err).NotTo(HaveOccurred())
		wantInterrupt := expected(h, createInterruptJobFromPackage(h.r.opts, interrupt, args, &tuning,
			wrapper.NewSkyhookWrapper(storedNodeWright(h)), interruptNode.Name, v1alpha1.StageInterrupt), tuning, v1alpha1.StageInterrupt)
		interruptJob := jobOf(h, wantInterrupt.Name)
		expectJob(interruptJob, wantInterrupt)
		Expect(interruptJob.Spec.Template.Spec.Containers[0].Image).To(Equal(getPackageImage(&tuning)))

		Expect(reg.Requests()).To(BeEmpty())
	})

	Describe("the stage Job ApplyPackage builds", func() {
		It("is the option-off Job for a container image, its package image pinned to the digest the tag resolves to", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushImage(reg, "tuning")
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)

			built := map[bool]batchv1.Job{}
			for _, off := range []bool{true, false} {
				h := newHarness(setup{off: off, httpClient: reg.Client()}, newNodeWright(tuning), node.DeepCopy())
				Expect(applyPackage(h, node.DeepCopy(), tuning)).To(Succeed())
				jobs := jobsOf(h)
				Expect(jobs).To(HaveLen(1))
				built[off] = jobs[0]
				Expect(warnings(drainEvents(h), EventsReasonPackageArtifactInspectionFailed)).To(BeEmpty())
			}
			on, off := built[false], built[true]
			Expect(on.Labels).To(Equal(off.Labels))
			Expect(on.Annotations).To(Equal(off.Annotations))
			Expect(on.OwnerReferences).To(Equal(off.OwnerReferences))
			pinned := pinnedAt(reg, "tuning", desc)
			Expect(on.Spec.Template.Spec.InitContainers[0].Image).To(Equal(pinned))
			Expect(on.Spec.Template.Spec.Containers[0].Image).To(Equal(pinned))
			Expect(off.Spec.Template.Spec.InitContainers[0].Image).To(Equal(getPackageImage(&tuning)))
			unpinned := on.DeepCopy()
			unpinned.Spec.Template.Spec.InitContainers[0].Image = off.Spec.Template.Spec.InitContainers[0].Image
			unpinned.Spec.Template.Spec.Containers[0].Image = off.Spec.Template.Spec.Containers[0].Image
			Expect(unpinned.Spec).To(Equal(off.Spec))
		})

		It("is the artifact Job pinned to the digest a tag resolves to", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)

			Expect(applyPackage(h, node, tuning)).To(Succeed())
			want := artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", desc))
			Expect(jobsOf(h)).To(HaveLen(1))
			expectJob(jobOf(h, want.Name), want)
		})

		It("is the artifact Job pinned to a containerSHA, with no request naming the tag", func() {
			reg := newRegistry(registrytest.Options{})
			pinned := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			// The tag moves to another package, so resolving it would pin that one.
			moved := pushPackage(reg, "tuning", registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte("moved")}}})
			Expect(moved.Digest).NotTo(Equal(pinned.Digest))
			tuning := packageAt(reg, "tuning", "tuning")
			tuning.ContainerSHA = pinned.Digest.String()
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)

			Expect(applyPackage(h, node, tuning)).To(Succeed())
			want := artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", pinned))
			expectJob(jobOf(h, want.Name), want)
			Expect(reg.Requests()).NotTo(BeEmpty())
			for _, request := range reg.Requests() {
				Expect(request.Path).NotTo(HaveSuffix("/" + version))
			}
		})

		It("is none for a reference the registry refuses the pull Secret for, which blocks the node with a Warning once and the condition", func() {
			// The pull Secret names the registry with a wrong password, so the resolution reaches
			// the registry and is refused there.
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client(), pullSecret: pullSecret}, newNodeWright(tuning), node, pullSecretFor(reg, "wrong-password"))

			events, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			requests := reg.Requests()
			Expect(requests).NotTo(BeEmpty())
			Expect(requests[len(requests)-1].Status).To(Equal(http.StatusUnauthorized))
			Expect(jobsOf(h)).To(BeEmpty())
			Expect(applyPackage(h, node, tuning)).To(Succeed())
			Expect(jobsOf(h)).To(BeEmpty())
			Expect(nodeStatus(h)).To(Equal(v1alpha1.StatusBlocked))
			Expect(warnings(append(events, drainEvents(h)...), EventsReasonPackageArtifactInspectionFailed)).To(ConsistOf(And(
				HavePrefix("Warning PackageArtifactInspectionFailed tuning:1.0.0 "),
				ContainSubstring("[tuning:1.0.0]"),
				ContainSubstring("["+getPackageImage(&tuning)+"]"),
				ContainSubstring("unauthorized"),
				ContainSubstring("blocked"),
			)))
			Expect(blockedCondition(h)).To(HaveField("Message", HavePrefix("1 package blocked: tuning:1.0.0 (unauthorized). ")))
		})

		It("gives credentials end to end: an artifact Job with the pull Secret, no Job without", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)

			with := newHarness(setup{httpClient: reg.Client(), pullSecret: pullSecret}, newNodeWright(tuning), node.DeepCopy(), pullSecretFor(reg, password))
			Expect(applyPackage(with, node.DeepCopy(), tuning)).To(Succeed())
			want := artifactJob(with, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", desc))
			got := jobOf(with, want.Name)
			expectJob(got, want)
			Expect(hasEnv(got, envRegistryAuthFiles)).To(BeTrue())
			Expect(drainEvents(with)).NotTo(ContainElement(HavePrefix("Warning ")))

			without := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node.DeepCopy())
			Expect(applyPackage(without, node.DeepCopy(), tuning)).To(Succeed())
			Expect(jobsOf(without)).To(BeEmpty())
			Expect(warnings(drainEvents(without), EventsReasonPackageArtifactInspectionFailed)).To(ConsistOf(ContainSubstring("unauthorized")))
		})
	})

	It("keeps a node its package blocks blocked across passes, writing neither the node nor the condition again", func() {
		reg := newRegistry(registrytest.Options{})
		pushPackage(reg, "tuning", hostFiles)
		tuning := packageAt(reg, "tuning", "tuning")
		h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), newNode(nodeName))

		_, err := pass(h)
		Expect(err).NotTo(HaveOccurred())
		Expect(nodeStatus(h)).To(Equal(v1alpha1.StatusBlocked))
		// Set in the past, so a condition removed and set again in a later pass shows.
		stored := storedNodeWright(h)
		since := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		for i := range stored.Status.Conditions {
			if stored.Status.Conditions[i].Type == wrapper.SkyhookConditionBlocked {
				stored.Status.Conditions[i].LastTransitionTime = since
			}
		}
		Expect(h.c.Status().Update(ctx, stored)).To(Succeed())
		condition := blockedCondition(h)
		Expect(condition).NotTo(BeNil())
		Expect(condition.LastTransitionTime.Time).To(BeTemporally("==", since.Time))
		node := &corev1.Node{}
		Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())

		// Reconcile's order, the condition refresh included.
		for range 2 {
			state := stateOf(h)
			sn := state.skyhooks[0]
			resolvedPackageArtifacts(h.r, sn)
			h.r.resolveUninstallDecisions(ctx, sn)
			Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
			_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), sn)
			Expect(err).NotTo(HaveOccurred())
			again := &corev1.Node{}
			Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, again)).To(Succeed())
			Expect(again.ResourceVersion).To(Equal(node.ResourceVersion))
			Expect(blockedCondition(h).LastTransitionTime.Time).To(BeTemporally("==", since.Time))
		}
		// The NodeWright's status follows from its nodes' as a pass starts.
		Expect(storedNodeWright(h).Status.Status).To(Equal(v1alpha1.StatusBlocked))
	})

	It("keeps a node blocked by a digest the registry refuses across its retry, writing neither the node nor the condition again, and runs it once the Secret is fixed", func() {
		reg := newRegistry(registrytest.Options{Username: username, Password: password})
		desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
		tuning := packageAt(reg, "tuning", "tuning")
		tuning.ContainerSHA = desc.Digest.String()
		h := newHarness(setup{httpClient: reg.Client(), pullSecret: pullSecret}, newNodeWright(tuning), newNode(nodeName), pullSecretFor(reg, "wrong-password"))

		_, err := pass(h)
		Expect(err).NotTo(HaveOccurred())
		Expect(nodeStatus(h)).To(Equal(v1alpha1.StatusBlocked))
		Expect(blockedCondition(h)).To(HaveField("Message", HavePrefix("1 package blocked: tuning:1.0.0 (unauthorized). ")))
		// Set in the past, so a condition removed and set again in a later pass shows.
		stored := storedNodeWright(h)
		since := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		for i := range stored.Status.Conditions {
			if stored.Status.Conditions[i].Type == wrapper.SkyhookConditionBlocked {
				stored.Status.Conditions[i].LastTransitionTime = since
			}
		}
		Expect(h.c.Status().Update(ctx, stored)).To(Succeed())
		node := &corev1.Node{}
		Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())

		// Due: the first pass queues the inspection, which the inspector makes before the second.
		stepPast(packageArtifactRetry)
		for attempt := range 2 {
			state := stateOf(h)
			sn := state.skyhooks[0]
			resolvedPackageArtifacts(h.r, sn)
			h.r.resolveUninstallDecisions(ctx, sn)
			Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
			_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), sn)
			Expect(err).NotTo(HaveOccurred())
			again := &corev1.Node{}
			Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, again)).To(Succeed())
			Expect(again.ResourceVersion).To(Equal(node.ResourceVersion), "attempt %d", attempt)
			Expect(blockedCondition(h).LastTransitionTime.Time).To(BeTemporally("==", since.Time), "attempt %d", attempt)
			h.r.opts.artifactInspector.drain(ctx)
		}
		Expect(jobsOf(h)).To(BeEmpty())

		secret := &corev1.Secret{}
		Expect(h.c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pullSecret}, secret)).To(Succeed())
		secret.Data = pullSecretFor(reg, password).Data
		Expect(h.c.Update(ctx, secret)).To(Succeed())
		stepPast(packageArtifactRetryMax)
		_, err = pass(h)
		Expect(err).NotTo(HaveOccurred())
		Expect(jobsOf(h)).To(HaveLen(1))
		Expect(blockedCondition(h)).To(BeNil())
	})

	Describe("a rejected artifact", func() {
		DescribeTable("gets no Job, blocks its node, with a Warning and the condition, which clear once a corrected tag is pushed",
			// push returns the containerSHA to pin, or "" to name the tag.
			func(push func(reg *registrytest.Registry) string, reason string) {
				reg := newRegistry(registrytest.Options{})
				tuning := packageAt(reg, "tuning", "tuning")
				tuning.ContainerSHA = push(reg)
				node := newNode(nodeName)
				h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)

				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(warnings(events, EventsReasonPackageArtifactRejected)).To(ConsistOf(And(
					HavePrefix("Warning PackageArtifactRejected tuning:1.0.0 "),
					ContainSubstring("[tuning:1.0.0]"),
					ContainSubstring("["+reg.Host()+"/tuning@sha256:"),
					ContainSubstring("is rejected: "+reason),
				)))
				condition := blockedCondition(h)
				Expect(condition).NotTo(BeNil())
				Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				Expect(condition.Message).To(Equal("1 package blocked: tuning:1.0.0 (" + reason + "). " +
					"A package artifact in a private registry needs its pull Secret in imagePullSecrets."))
				Expect(nodeStatus(h)).To(Equal(v1alpha1.StatusBlocked))

				// ApplyPackage alone builds nothing for it and leaves the report to the decision.
				Expect(applyPackage(h, node, tuning)).To(Succeed())
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(drainEvents(h)).To(BeEmpty())

				if tuning.ContainerSHA != "" {
					return // the content at a digest never changes
				}
				// The first pass once the tag's resolution is packageArtifactResolvedFor old resolves
				// it again, with no other wait.
				corrected := pushPackage(reg, "tuning", registrytest.PackageSpec{})
				stepPast(packageArtifactResolvedFor)
				events, err = pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings(events, EventsReasonPackageArtifactRejected)).To(BeEmpty())
				want := artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", corrected))
				Expect(jobsOf(h)).To(HaveLen(1))
				expectJob(jobOf(h, want.Name), want)
				Expect(blockedCondition(h)).To(BeNil())
				Expect(nodeStatus(h)).NotTo(Equal(v1alpha1.StatusBlocked))
			},
			Entry("a manifest with no payload layer under a tag", func(reg *registrytest.Registry) string {
				pushNoPayload(reg, "tuning", version)
				return ""
			}, "invalid-artifact: no-payload"),
			Entry("a manifest with no payload layer under a containerSHA", func(reg *registrytest.Registry) string {
				return pushNoPayload(reg, "tuning", "").Digest.String()
			}, "invalid-artifact: no-payload"),
			Entry("an unsupported version under a tag", func(reg *registrytest.Registry) string {
				pushV2Package(reg, "tuning", version)
				return ""
			}, "unsupported-version"),
			Entry("an unsupported version under a containerSHA", func(reg *registrytest.Registry) string {
				return pushV2Package(reg, "tuning", "").Digest.String()
			}, "unsupported-version"),
			Entry("host files", func(reg *registrytest.Registry) string {
				pushPackage(reg, "tuning", hostFiles)
				return ""
			}, "host-files"),
			Entry("an interrupt", func(reg *registrytest.Registry) string {
				pushPackage(reg, "tuning", declaresInterrupt)
				return ""
			}, "interrupt"),
		)

		It("names at most ReadyConditionNodeListLimit packages in the condition and counts the rest", func() {
			reg := newRegistry(registrytest.Options{})
			var packages []v1alpha1.Package
			for i := range wrapper.ReadyConditionNodeListLimit + 2 {
				repository := fmt.Sprintf("host-%02d", i)
				pushPackage(reg, repository, hostFiles)
				packages = append(packages, packageAt(reg, "pkg-"+repository, repository))
			}
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(packages...), newNode(nodeName))
			skyhook := decided(h).skyhooks[0].GetSkyhook()
			for i := range packages {
				Expect(skyhook.PackageArtifacts[packageArtifactKey(&packages[i])].Kind).To(Equal("rejected"))
			}

			h.r.updatePackageArtifactUnusableCondition(skyhook)
			condition := meta.FindStatusCondition(skyhook.Status.Conditions, wrapper.SkyhookConditionBlocked)
			Expect(condition).NotTo(BeNil())
			listed := make([]string, 0, wrapper.ReadyConditionNodeListLimit)
			for _, p := range packages[:wrapper.ReadyConditionNodeListLimit] {
				listed = append(listed, p.Name+":"+p.Version+" (host-files)")
			}
			Expect(condition.Message).To(Equal(fmt.Sprintf("%d packages blocked: %s, and 2 more. ", len(packages), strings.Join(listed, ", ")) +
				"A package artifact in a private registry needs its pull Secret in imagePullSecrets."))
		})
	})

	Describe("uninstall unsupported", func() {
		// uninstalling returns tuning with an uninstall requested, on a node already at its
		// uninstall stage with no Job: an artifact declaring uninstall unsupported reaches that
		// stage only from a cycle begun before it turned unsupported, since its decision starts none.
		uninstalling := func(reg *registrytest.Registry) (v1alpha1.Package, *corev1.Node) {
			tuning := packageAt(reg, "tuning", "tuning")
			tuning.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
			return tuning, nodeWith(nodeName, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
		}

		It("refuses the uninstall stage of an artifact declaring it", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			tuning, node := uninstalling(reg)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)

			events, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobsOf(h)).To(BeEmpty())
			Expect(warnings(events, EventsReasonPackageArtifactUninstallUnsupported)).To(ConsistOf(And(
				HavePrefix("Warning PackageArtifactUninstallUnsupported tuning:1.0.0 "),
				ContainSubstring("[tuning:1.0.0]"),
				ContainSubstring("["+desc.Digest.String()+"]"),
				ContainSubstring("uninstall unsupported"),
			)))
			// It is accepted, so nothing reports it blocked.
			Expect(blockedCondition(h)).To(BeNil())
		})

		It("runs the apply stage of the same artifact as an artifact Job", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			tuning := packageAt(reg, "tuning", "tuning")
			tuning.Uninstall = &v1alpha1.Uninstall{Enabled: true}
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), newNode(nodeName))

			events, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			Expect(events).NotTo(ContainElement(HavePrefix("Warning ")))
			want := artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", desc))
			Expect(jobsOf(h)).To(HaveLen(1))
			expectJob(jobOf(h, want.Name), want)
		})

		It("runs the uninstall stage of an artifact supporting it as an artifact Job", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallSupported})
			tuning, node := uninstalling(reg)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)

			events, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			Expect(events).NotTo(ContainElement(HavePrefix("Warning ")))
			want := artifactJob(h, tuning, nodeName, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", desc))
			Expect(jobsOf(h)).To(HaveLen(1))
			expectJob(jobOf(h, want.Name), want)
		})

		DescribeTable("deleting the NodeWright",
			// warning is the Warning of a refused uninstall, or "" for one that gets its Job.
			func(spec registrytest.PackageSpec, warning string) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", spec)
				tuning := packageAt(reg, "tuning", "tuning")
				tuning.Uninstall = &v1alpha1.Uninstall{Enabled: true}
				nw := newNodeWright(tuning)
				nw.DeletionTimestamp = ptr(metav1.Now())
				nw.Finalizers = []string{SkyhookFinalizer}
				// The node has begun the uninstall, as for uninstalling.
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress))

				finalize := func() bool {
					GinkgoHelper()
					state := decided(h)
					handled, err := h.r.HandleFinalizer(ctx, state.skyhooks[0], state)
					Expect(err).NotTo(HaveOccurred())
					return handled
				}
				deletionBlocked := func() *metav1.Condition {
					GinkgoHelper()
					return meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionDeletionBlocked)
				}

				// As in a reconcile, the decisions are made once, and the finalizer runs before the
				// pass that builds the uninstall Job.
				state := decided(h)
				handled, err := h.r.HandleFinalizer(ctx, state.skyhooks[0], state)
				Expect(err).NotTo(HaveOccurred())
				Expect(handled).To(BeFalse())
				_, err = h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), state.skyhooks[0])
				Expect(err).NotTo(HaveOccurred())
				events := drainEvents(h)
				Expect(finalize()).To(BeFalse())

				if warning == "" {
					Expect(jobsOf(h)).To(HaveLen(1))
					Expect(deletionBlocked()).To(BeNil())
					Expect(warnings(drainEvents(h), "DeletionBlocked")).To(BeEmpty())
					return
				}
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(warnings(events, warning)).To(HaveLen(1))
				condition := deletionBlocked()
				Expect(condition).NotTo(BeNil())
				Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				Expect(condition.Reason).To(Equal("PackageArtifactRefused"))
				Expect(condition.Message).To(HavePrefix("Uninstall cannot complete for tuning:1.0.0: "))
				Expect(warnings(drainEvents(h), "DeletionBlocked")).To(ConsistOf(ContainSubstring("tuning:1.0.0")))
				Expect(storedNodeWright(h).Finalizers).To(ConsistOf(SkyhookFinalizer))

				// uninstall.enabled=false releases the package, so the deletion completes.
				current := storedNodeWright(h)
				released := current.Spec.Packages[tuning.Name]
				released.Uninstall = &v1alpha1.Uninstall{}
				current.Spec.Packages[tuning.Name] = released
				Expect(h.c.Update(ctx, current)).To(Succeed())
				Expect(finalize()).To(BeTrue())
			},
			Entry("is blocked, with DeletionBlocked, by an artifact declaring uninstall unsupported once a node has begun the uninstall",
				registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported}, EventsReasonPackageArtifactUninstallUnsupported),
			Entry("is blocked, with DeletionBlocked, by a rejected artifact", hostFiles, EventsReasonPackageArtifactRejected),
			Entry("waits, with no DeletionBlocked, for the uninstall Job of an artifact supporting it", registrytest.PackageSpec{}, ""),
		)
	})

	DescribeTable("a refused package with a reboot interrupt neither cordons nor drains its node, while another package gets its Job",
		func(refusedSpec registrytest.PackageSpec, uninstall, serial bool) {
			reg := newRegistry(registrytest.Options{})
			// Named so the pass reaches driver ahead of tuning.
			driver := packageAt(reg, "a-driver", "driver")
			driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
			pushPackage(reg, "driver", refusedSpec)
			tuning := packageAt(reg, "b-tuning", "tuning")
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})

			node := newNode(nodeName)
			if uninstall {
				// Begun before the artifact turned unsupported, since the decision starts none.
				driver.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
				node = nodeWith(nodeName, driver, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
			}
			nw := newNodeWright(driver, tuning)
			nw.Spec.Serial = serial
			workload := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "workload",
					Namespace:       "default",
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "workload-rs", Controller: ptr(true)}},
				},
				Spec:   corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "workload", Image: "busybox"}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			var evictions atomic.Int64
			h := newHarness(setup{httpClient: reg.Client(), funcs: interceptor.Funcs{
				SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
					if subResourceName == "eviction" {
						evictions.Add(1)
					}
					return c.SubResource(subResourceName).Create(ctx, obj, subResource, opts...)
				},
			}}, nw, node, workload)

			// Without the refusal, the first pass cordons and the second drains. The refusal is
			// reported once.
			for i := range 2 {
				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				if i == 0 {
					Expect(events).To(ContainElement(HavePrefix("Warning PackageArtifact")))
				} else {
					Expect(events).NotTo(ContainElement(HavePrefix("Warning PackageArtifact")))
				}

				persisted := &corev1.Node{}
				Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, persisted)).To(Succeed())
				Expect(persisted.Spec.Unschedulable).To(BeFalse())
				Expect(persisted.Annotations).NotTo(HaveKey(cordonKey))
				Expect(evictions.Load()).To(BeZero())
				Expect(h.c.Get(ctx, client.ObjectKeyFromObject(workload), &corev1.Pod{})).To(Succeed())

				jobs := jobsOf(h)
				Expect(jobs).To(HaveLen(1))
				expectJob(jobs[0], artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", desc)))
			}
		},
		Entry("rejected at apply", hostFiles, false, false),
		Entry("rejected at apply, serial", hostFiles, false, true),
		Entry("uninstall unsupported at uninstall", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported}, true, false),
		Entry("uninstall unsupported at uninstall, serial", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported}, true, true),
	)

	DescribeTable("a refused package with a reboot interrupt leaves the node's interrupt to another package",
		func(refusedSpec registrytest.PackageSpec, uninstall bool) {
			reg := newRegistry(registrytest.Options{})
			// Named so it sorts ahead of tuning, and a reboot, so it would win the interrupt.
			driver := packageAt(reg, "a-driver", "driver")
			driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
			pushPackage(reg, "driver", refusedSpec)
			tuning := packageAt(reg, "b-tuning", "tuning")
			tuning.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.SERVICE, Services: []string{"tuned"}}
			pushPackage(reg, "tuning", registrytest.PackageSpec{})

			// tuning has run its config stage, so its interrupt is next.
			node := nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
			if uninstall {
				// Begun before the artifact turned unsupported, since the decision starts none.
				driver.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
				sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
				Expect(err).NotTo(HaveOccurred())
				Expect(sn.Upsert(driver.PackageRef, driver.Image, v1alpha1.StateInProgress, v1alpha1.StageUninstall, 0, "")).To(Succeed())
			}
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(driver, tuning), node)

			events, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			Expect(events).To(ContainElement(HavePrefix("Warning PackageArtifact")))

			jobs := jobsOf(h)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/package", "b-tuning-"+version))
			Expect(jobs[0].Spec.Template.Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/interrupt", interruptLabelValue))

			persisted := &corev1.Node{}
			Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, persisted)).To(Succeed())
			status, found := skyhookNodeOf(h, persisted).PackageStatus(tuning.GetUniqueName())
			Expect(found).To(BeTrue())
			Expect(status.Stage).To(Equal(v1alpha1.StageInterrupt))
			Expect(status.State).To(Equal(v1alpha1.StateInProgress))
		},
		Entry("rejected at apply", hostFiles, false),
		Entry("uninstall unsupported at uninstall", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported}, true),
	)

	DescribeTable("a refused package with a reboot interrupt never holds its node's drain-blocked conditions",
		func(refusedSpec registrytest.PackageSpec, uninstall bool) {
			reg := newRegistry(registrytest.Options{})
			driver := packageAt(reg, "driver", "driver")
			driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
			pushPackage(reg, "driver", refusedSpec)

			node := newNode(nodeName)
			if uninstall {
				// Begun before the artifact turned unsupported, since the decision starts none.
				driver.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
				node = nodeWith(nodeName, driver, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
			}
			// Recorded by a drain for driver before its artifact was refused.
			drainBlockedKey := fmt.Sprintf("%s/drainBlocked_%s", v1alpha1.METADATA_PREFIX, skyhookName)
			metav1.SetMetaDataAnnotation(&node.ObjectMeta, drainBlockedKey, jsonOf([]drain.BlockedPod{
				{Namespace: "default", Name: "gone", Reason: drain.BlockReasonPodDisruptionBudget},
			}))
			golden := map[string]string{"workload": "golden"}
			nw := newNodeWright(driver)
			nw.Spec.PodNonInterruptLabels = metav1.LabelSelector{MatchLabels: golden}
			// Holds the drain driver would wait on, were it not refused.
			workload := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "default", Labels: golden},
				Spec:       corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "workload", Image: "busybox"}}},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			}
			h := newHarness(setup{httpClient: reg.Client()}, nw, node, workload)

			// The pass decides the refusal; the conditions then read it from the cache.
			events, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			Expect(events).To(ContainElement(HavePrefix("Warning PackageArtifact")))

			state := decided(h)
			sn := state.skyhooks[0]
			Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())

			// A Blocked condition, if any, is the refused package's own, not a drain wait.
			if blocked := meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionBlocked); blocked != nil {
				Expect(blocked.Reason).To(Equal(wrapper.SkyhookReasonPackageArtifactUnusable))
			}
			Expect(meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionDrainBlocked)).To(BeNil())
			Expect(warnings(drainEvents(h), EventsReasonSkyhookDrain)).To(BeEmpty())
			_, skyhookNode := sn.GetNode(nodeName)
			Expect(skyhookNode.GetNode().Annotations).NotTo(HaveKey(drainBlockedKey))
		},
		Entry("rejected at apply", hostFiles, false),
		Entry("uninstall unsupported at uninstall", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported}, true),
	)

	// unrecordedPass is a pass whose inspections are not recorded yet: Reconcile's resolves, then
	// RunSkyhookPackages, with the inspector's queue left as they leave it.
	unrecordedPass := func(h harness) {
		GinkgoHelper()
		state := stateOf(h)
		sn := state.skyhooks[0]
		resolvedPackageArtifacts(h.r, sn)
		h.r.resolveUninstallDecisions(ctx, sn)
		Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
		_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), sn)
		Expect(err).NotTo(HaveOccurred())
	}

	It("gives a package pending inspection no Job and neither cordons nor drains its node for its reboot, while another package gets its Job", func() {
		reg := newRegistry(registrytest.Options{})
		driver := packageAt(reg, "a-driver", "driver")
		driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
		desc := pushPackage(reg, "driver", registrytest.PackageSpec{})
		tuning := packageAt(reg, "b-tuning", "tuning")
		image := pushImage(reg, "tuning")
		nw := newNodeWright(driver, tuning)
		workload := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "workload",
				Namespace:       "default",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "workload-rs", Controller: ptr(true)}},
			},
			Spec:   corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "workload", Image: "busybox"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		var evictions atomic.Int64
		h := newHarness(setup{httpClient: reg.Client(), funcs: interceptor.Funcs{
			SubResourceCreate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
				if subResourceName == "eviction" {
					evictions.Add(1)
				}
				return c.SubResource(subResourceName).Create(ctx, obj, subResource, opts...)
			},
		}}, nw, newNode(nodeName), workload)
		// What tuning's digest is, as an inspection earlier cached it.
		Expect(h.r.opts.artifactInspector.cache.put(ctx, image.Digest.String(), packageArtifactCacheEntry{Kind: packageArtifactImage})).To(Succeed())

		// Were driver not pending, the first pass would cordon and the second drain.
		for range 2 {
			unrecordedPass(h)

			persisted := &corev1.Node{}
			Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, persisted)).To(Succeed())
			Expect(persisted.Spec.Unschedulable).To(BeFalse())
			Expect(persisted.Annotations).NotTo(HaveKey(cordonKey))
			Expect(evictions.Load()).To(BeZero())
			Expect(h.c.Get(ctx, client.ObjectKeyFromObject(workload), &corev1.Pod{})).To(Succeed())
			jobs := jobsOf(h)
			Expect(jobs).To(HaveLen(1))
			expectJob(jobs[0], imageJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", image)))
		}
		// Pending is no failure: nothing reports it blocked.
		Expect(blockedCondition(h)).To(BeNil())
		Expect(nodeStatus(h)).NotTo(Equal(v1alpha1.StatusBlocked))
		Expect(h.r.ApplyPackage(ctx, GinkgoLogr, nil, skyhookNodeOf(h, newNode(nodeName)), &driver, false)).To(Succeed())
		Expect(jobsOf(h)).To(HaveLen(1))

		// Once its result is recorded, driver cordons and drains its node, and gets its artifact Job.
		h.r.opts.artifactInspector.drain(ctx)
		Expect(h.c.Delete(ctx, workload)).To(Succeed())
		want := artifactJob(h, driver, nodeName, v1alpha1.StageApply, pinnedAt(reg, "driver", desc))
		for range 3 {
			_, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
		}
		expectJob(jobOf(h, want.Name), want)
	})

	DescribeTable("runs the interrupt of a package at its interrupt stage on the node it cordoned, whatever its artifact, which it does not resolve",
		func(cached bool) {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "tuning", hostFiles)
			tuning := packageAt(reg, "tuning", "tuning")
			tuning.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.SERVICE, Services: []string{"tuned"}}
			// tuning has run its config stage on the node it cordoned, so its interrupt is next.
			node := nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
			sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
			Expect(err).NotTo(HaveOccurred())
			Expect(sn.Cordon()).To(BeTrue())
			nw := newNodeWright(tuning)
			h := newHarness(setup{httpClient: reg.Client()}, nw, node)
			if cached {
				h.r.opts.artifactInspector.resolve(ctx, nw, &tuning)
				h.r.opts.artifactInspector.drain(ctx)
				Expect(h.r.opts.artifactInspector.resolve(ctx, nw, &tuning).Kind).To(Equal(packageArtifactRejected))
			}
			reg.ResetRequests()

			unrecordedPass(h)
			Expect(reg.Requests()).To(BeEmpty())

			jobs := jobsOf(h)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/package", "tuning-"+version))
			Expect(jobs[0].Spec.Template.Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/interrupt", interruptLabelValue))
			persisted := &corev1.Node{}
			Expect(h.c.Get(ctx, types.NamespacedName{Name: nodeName}, persisted)).To(Succeed())
			status, found := skyhookNodeOf(h, persisted).PackageStatus(tuning.GetUniqueName())
			Expect(found).To(BeTrue())
			Expect(status.Stage).To(Equal(v1alpha1.StageInterrupt))
			Expect(status.State).To(Equal(v1alpha1.StateInProgress))
		},
		Entry("never inspected", false),
		Entry("cached as rejected", true),
	)

	It("waits for one black-holed registry once in a pass, withholds its packages without blocking the node, and still builds the rest", func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		var (
			mu       sync.Mutex
			held     []net.Conn
			closed   bool
			accepted atomic.Int64
		)
		// Accept and hold every connection without ever writing, so a client waits for the TLS
		// handshake until its context gives up.
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				accepted.Add(1)
				mu.Lock()
				if closed {
					_ = conn.Close()
				} else {
					held = append(held, conn)
				}
				mu.Unlock()
			}
		}()
		DeferCleanup(func() {
			_ = listener.Close()
			mu.Lock()
			defer mu.Unlock()
			closed = true
			for _, conn := range held {
				_ = conn.Close()
			}
		})

		reachable := newRegistry(registrytest.Options{})
		desc := pushPackage(reachable, "d", registrytest.PackageSpec{})
		host := listener.Addr().String()
		blackHoled := []v1alpha1.Package{
			{PackageRef: v1alpha1.PackageRef{Name: "a", Version: version}, Image: host + "/a"},
			{PackageRef: v1alpha1.PackageRef{Name: "b", Version: version}, Image: host + "/b"},
			{PackageRef: v1alpha1.PackageRef{Name: "c", Version: version}, Image: host + "/c"},
		}
		d := packageAt(reachable, "d", "d")
		// The client's own timeout is longer than the request timeout, so the context deadline
		// decides.
		h := newHarness(setup{
			httpClient: &http.Client{Transport: reachable.Client().Transport, Timeout: time.Minute},
			timeout:    100 * time.Millisecond,
		}, newNodeWright(append(slices.Clone(blackHoled), d)...), newNode(nodeName))

		events, err := pass(h)
		Expect(err).NotTo(HaveOccurred())
		Eventually(accepted.Load).Should(Equal(int64(1)))
		Consistently(accepted.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(int64(1)))

		Expect(jobsOf(h)).To(HaveLen(1))
		failed := warnings(events, EventsReasonPackageArtifactInspectionFailed)
		Expect(failed).To(HaveLen(len(blackHoled)))
		for _, p := range blackHoled {
			Expect(failed).To(ContainElement(And(
				HavePrefix("Warning PackageArtifactInspectionFailed "+p.Name+":"+version+" "),
				ContainSubstring("["+getPackageImage(&p)+"]"),
				ContainSubstring("(timeout)"),
				ContainSubstring("waits"),
			)))
		}
		want := artifactJob(h, d, nodeName, v1alpha1.StageApply, pinnedAt(reachable, "d", desc))
		expectJob(jobOf(h, want.Name), want)
		Expect(nodeStatus(h)).NotTo(Equal(v1alpha1.StatusBlocked))
		Expect(blockedCondition(h)).To(BeNil())
	})

	It("keeps the Warnings of one pass distinct per package", func() {
		open := newRegistry(registrytest.Options{})
		pushPackage(open, "host", hostFiles)
		pushNoPayload(open, "no-payload", version)
		// No pull Secret, so every inspection of this registry is refused.
		auth := newRegistry(registrytest.Options{Username: username, Password: password})
		pushPackage(auth, "x", registrytest.PackageSpec{})
		pushPackage(auth, "y", registrytest.PackageSpec{})
		packages := []v1alpha1.Package{
			packageAt(open, "p-host", "host"),
			packageAt(open, "p-no-payload", "no-payload"),
			packageAt(auth, "p-x", "x"),
			packageAt(auth, "p-y", "y"),
		}
		wantReasons := map[string]string{
			"p-host":       EventsReasonPackageArtifactRejected,
			"p-no-payload": EventsReasonPackageArtifactRejected,
			"p-x":          EventsReasonPackageArtifactInspectionFailed,
			"p-y":          EventsReasonPackageArtifactInspectionFailed,
		}
		// Every httptest TLS server presents the same certificate, so one client trusts both.
		h := newHarness(setup{httpClient: open.Client()}, newNodeWright(packages...), newNode(nodeName))

		events, err := pass(h)
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, event := range events {
			if strings.HasPrefix(event, "Warning ") {
				got = append(got, event)
			}
		}
		Expect(got).To(HaveLen(len(packages)))
		keys := map[string]bool{}
		for _, p := range packages {
			action := p.Name + ":" + version
			i := slices.IndexFunc(got, func(event string) bool { return strings.Split(event, " ")[2] == action })
			Expect(i).NotTo(Equal(-1), action)
			fields := strings.SplitN(got[i], " ", 4)
			Expect(fields[1]).To(Equal(wantReasons[p.Name]), action)
			Expect(fields[3]).To(ContainSubstring("["+action+"]"), action)
			key := fields[1] + " " + fields[2]
			Expect(keys).NotTo(HaveKey(key))
			keys[key] = true
		}
	})

	It("runs the agent as the done container of every interrupt Job, whatever the artifact, and never asks a registry", func() {
		open := newRegistry(registrytest.Options{})
		pushPackage(open, "artifact", registrytest.PackageSpec{})
		pushImage(open, "legacy")
		auth := newRegistry(registrytest.Options{Username: username, Password: password})
		pushPackage(auth, "artifact", registrytest.PackageSpec{})

		for _, ref := range []struct {
			reg        *registrytest.Registry
			repository string
			kind       packageArtifactKind
		}{
			{open, "artifact", packageArtifactAccepted},
			{open, "legacy", packageArtifactImage},
			{auth, "artifact", packageArtifactUnreadable},
		} {
			tuning := packageAt(ref.reg, "tuning", ref.repository)
			tuning.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
			for _, interruptType := range []v1alpha1.InterruptType{v1alpha1.REBOOT, v1alpha1.SERVICE, v1alpha1.RESTART_ALL_SERVICES, v1alpha1.NOOP} {
				what := fmt.Sprintf("%s for %s", interruptType, ref.kind)
				node := newNode(nodeName)
				// A cold cache: an inspection would show in the request log.
				h := newHarness(setup{httpClient: ref.reg.Client()}, newNodeWright(tuning), node)
				ref.reg.ResetRequests()

				Expect(h.r.Interrupt(ctx, skyhookNodeOf(h, node), &tuning, &v1alpha1.Interrupt{Type: interruptType}, v1alpha1.StageInterrupt)).To(Succeed(), what)
				jobs := jobsOf(h)
				Expect(jobs).To(HaveLen(1), what)
				pod := jobs[0].Spec.Template.Spec
				Expect(pod.Containers).To(HaveLen(1), what)
				done := pod.Containers[0]
				Expect(done.Name).To(Equal("done"), what)
				Expect(done.Image).To(Equal(agentImage), what)
				Expect(done.Args).To(Equal([]string{"--version"}), what)
				Expect(done.Command).To(BeNil(), what)
				for _, c := range slices.Concat(pod.InitContainers, pod.Containers) {
					Expect(c.Image).NotTo(HavePrefix(tuning.Image), what)
					Expect(slices.Concat(c.Command, c.Args)).NotTo(ContainElement(ContainSubstring("/bin/sh")), what)
				}
				Expect(ref.reg.Requests()).To(BeEmpty(), what)

				// The registry answers what the entry claims, so the shape above held for it.
				h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), &tuning)
				h.r.opts.artifactInspector.drain(ctx)
				Expect(h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), &tuning).Kind).To(Equal(ref.kind), what)
			}
		}
	})

	Describe("staleness", func() {
		const stage = v1alpha1.StageApply

		failedTerminally := func(job *batchv1.Job) *batchv1.Job {
			job.Status.Conditions = []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonBackoffLimitExceeded,
			}}
			job.Annotations[annotationStateRecorded] = annotationValueTrue
			return job
		}

		pkgSkyhook := func(_package v1alpha1.Package) *PackageSkyhook {
			return &PackageSkyhook{PackageRef: _package.PackageRef, Skyhook: skyhookName, Stage: stage}
		}

		// expectKept asserts every staleness path keeps the Job: handleExistingJob, the sweep, and
		// for a finished Job, the timed-out carve-out of shouldDeleteFinishedJob.
		expectKept := func(h harness, node *corev1.Node, _package v1alpha1.Package, name string) {
			GinkgoHelper()
			existing := jobOf(h, name)
			Expect(h.r.handleExistingJob(ctx, &existing, &_package, skyhookNodeOf(h, node), stage)).To(Succeed())
			Expect(gone(h, name)).To(BeFalse())
			Expect(sweep(h)).To(BeFalse())
			Expect(gone(h, name)).To(BeFalse())
			Expect(invalid(h, name)).To(BeFalse())
			if jobFinished(&existing) {
				state := stateOf(h)
				_, sn := state.skyhooks[0].GetNode(node.Name)
				nodeState, err := sn.State()
				Expect(err).NotTo(HaveOccurred())
				Expect(h.r.shouldDeleteFinishedJob(&existing, pkgSkyhook(_package), nodeState, state.skyhooks[0])).To(BeFalse())
			}
		}

		// expectReplaced asserts every staleness path replaces the Job built for built, now that the
		// spec names current: for a finished Job the timed-out carve-out of shouldDeleteFinishedJob
		// no longer holds, handleExistingJob deletes it, and the sweep deletes or invalidates it.
		// Each of the last two acts on the Job, so each gets a fresh world.
		expectReplaced := func(world func() harness, node *corev1.Node, built, current v1alpha1.Package, name string) {
			GinkgoHelper()
			h := world()
			existing := jobOf(h, name)
			if jobFinished(&existing) {
				state := stateOf(h)
				_, sn := state.skyhooks[0].GetNode(node.Name)
				nodeState, err := sn.State()
				Expect(err).NotTo(HaveOccurred())
				Expect(h.r.shouldDeleteFinishedJob(&existing, pkgSkyhook(built), nodeState, state.skyhooks[0])).To(BeTrue())
			}
			Expect(h.r.handleExistingJob(ctx, &existing, &current, skyhookNodeOf(h, node), stage)).To(Succeed())
			Expect(gone(h, name)).To(BeTrue())

			h = world()
			Expect(sweep(h)).To(BeTrue())
			if jobFinished(&existing) {
				Expect(gone(h, name)).To(BeTrue())
			} else {
				Expect(invalid(h, name)).To(BeTrue())
			}
		}

		// Each failing variant traces its failure through the outcome the pass resolves.
		DescribeTable("keep an unfinished artifact Job, and a timed-out one whose entry is erroring, whatever the pass resolves, and with the option off",
			func(variant func() (setup, *registrytest.Registry, []client.Object, func(harness)), finished bool) {
				s, reg, objects, traced := variant()
				tuning := packageAt(reg, "tuning", "tuning")
				state := v1alpha1.StateInProgress
				if finished {
					state = v1alpha1.StateErroring
				}
				node := nodeWith(nodeName, tuning, stage, state)
				nw := newNodeWright(tuning)

				// The Job pins a digest of the repository the package names.
				built := createArtifactJobFromPackage(validOpts(), &tuning, wrapper.NewSkyhookWrapper(nw), nodeName, stage,
					reg.Host()+"/tuning@sha256:"+strings.Repeat("a", 64))
				Expect(setJobPackage(built, nw, tuning.Image, stage, &tuning)).To(Succeed())
				if finished {
					built = failedTerminally(built)
				}
				h := newHarness(s, append(objects, nw, node, built)...)

				// The pass resolves nothing for an unfinished Job, and withholds a finished one's next Job.
				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(events).NotTo(ContainElement(HavePrefix("Warning PackageArtifactRejected")))
				traced(h)
				Expect(jobsOf(h)).To(HaveLen(1))

				expectKept(h, node, tuning, built.Name)
			},
			Entry("with a cold cache and a registry failing every request", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{})
				var calls atomic.Int64
				failing := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("injected failure")
				})}
				return setup{httpClient: failing}, reg, nil, func(h harness) {
					outcome := h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), ptr(packageAt(reg, "tuning", "tuning")))
					Expect(calls.Load()).To(BeNumerically(">", 0))
					Expect(outcome.Kind).To(Equal(packageArtifactUnavailable))
					Expect(outcome.ErrorClass).To(Equal(packageArtifactErrorTransport))
				}
			}, false),
			Entry("with an unauthorized pull Secret", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				return setup{httpClient: reg.Client(), pullSecret: pullSecret}, reg, []client.Object{pullSecretFor(reg, "wrong-password")}, func(h harness) {
					outcome := h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), ptr(packageAt(reg, "tuning", "tuning")))
					requests := reg.Requests()
					Expect(requests).NotTo(BeEmpty())
					Expect(requests[len(requests)-1].Status).To(Equal(http.StatusUnauthorized))
					Expect(outcome.ErrorClass).To(Equal(packageArtifactErrorUnauthorized))
				}
			}, false),
			Entry("with the option off", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				return setup{off: true, httpClient: reg.Client()}, reg, nil, func(h harness) {
					Expect(reg.Requests()).To(BeEmpty())
				}
			}, false),
			Entry("timed out, with a cold cache and a registry failing every request", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{})
				var calls atomic.Int64
				failing := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("injected failure")
				})}
				return setup{httpClient: failing}, reg, nil, func(h harness) {
					h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), ptr(packageAt(reg, "tuning", "tuning")))
					Expect(calls.Load()).To(BeNumerically(">", 0))
				}
			}, true),
			Entry("timed out, with an unauthorized pull Secret", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				return setup{httpClient: reg.Client(), pullSecret: pullSecret}, reg, []client.Object{pullSecretFor(reg, "wrong-password")}, func(h harness) {
					h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), ptr(packageAt(reg, "tuning", "tuning")))
					requests := reg.Requests()
					Expect(requests).NotTo(BeEmpty())
					Expect(requests[len(requests)-1].Status).To(Equal(http.StatusUnauthorized))
				}
			}, true),
			Entry("timed out, with the option off", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				return setup{off: true, httpClient: reg.Client()}, reg, nil, func(h harness) {
					Expect(reg.Requests()).To(BeEmpty())
				}
			}, true),
		)

		It("keeps a running Job pinned to the digest it was built with after the tag is re-pushed, and pins the next Job to the new digest", func() {
			reg := newRegistry(registrytest.Options{})
			first := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)
			_, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			kept := artifactJob(h, tuning, nodeName, stage, pinnedAt(reg, "tuning", first))
			expectJob(jobOf(h, kept.Name), kept)

			second := pushPackage(reg, "tuning", registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte("second")}}})
			stepPast(packageArtifactResolvedFor)
			_, err = pass(h)
			Expect(err).NotTo(HaveOccurred())
			expectKept(h, node, tuning, kept.Name)
			expectJob(jobOf(h, kept.Name), kept)

			// The next node resolves the tag again: its first pass queues the new digest, and the
			// next one builds its Job from it.
			other := newNode("other-node")
			Expect(applyPackage(h, other, tuning)).To(Succeed())
			Expect(applyPackage(h, other, tuning)).To(Succeed())
			want := artifactJob(h, tuning, other.Name, stage, pinnedAt(reg, "tuning", second))
			expectJob(jobOf(h, want.Name), want)
		})

		It("keeps a running artifact Job after its tag is re-pushed as a container image, and builds the next Job as an image Job", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)
			_, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			kept := artifactJob(h, tuning, nodeName, stage, pinnedAt(reg, "tuning", desc))

			image := pushImage(reg, "tuning")
			stepPast(packageArtifactResolvedFor)
			_, err = pass(h)
			Expect(err).NotTo(HaveOccurred())
			expectKept(h, node, tuning, kept.Name)
			expectJob(jobOf(h, kept.Name), kept)

			other := newNode("other-node")
			Expect(applyPackage(h, other, tuning)).To(Succeed())
			Expect(applyPackage(h, other, tuning)).To(Succeed())
			want := imageJob(h, tuning, other.Name, stage, pinnedAt(reg, "tuning", image))
			expectJob(jobOf(h, want.Name), want)
		})

		DescribeTable("treat an artifact Job, and a timed-out one whose entry is erroring, as stale once the package names another artifact",
			func(edit func(reg *registrytest.Registry, p *v1alpha1.Package), finished bool) {
				reg := newRegistry(registrytest.Options{})
				desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
				tuning := packageAt(reg, "tuning", "tuning")
				tuning.ContainerSHA = desc.Digest.String()
				state := v1alpha1.StateInProgress
				if finished {
					state = v1alpha1.StateErroring
				}
				node := nodeWith(nodeName, tuning, stage, state)
				nw := newNodeWright(tuning)
				built := createArtifactJobFromPackage(validOpts(), &tuning, wrapper.NewSkyhookWrapper(nw), nodeName, stage, pinnedAt(reg, "tuning", desc))
				Expect(setJobPackage(built, nw, tuning.Image, stage, &tuning)).To(Succeed())
				if finished {
					built = failedTerminally(built)
				}
				world := func(spec v1alpha1.Package) harness {
					return newHarness(setup{httpClient: reg.Client()}, newNodeWright(spec), node.DeepCopy(), built.DeepCopy())
				}

				expectKept(world(tuning), node, tuning, built.Name)

				edited := tuning
				edit(reg, &edited)
				expectReplaced(func() harness { return world(edited) }, node, tuning, edited, built.Name)
			},
			Entry("a containerSHA change", func(reg *registrytest.Registry, p *v1alpha1.Package) {
				p.ContainerSHA = pushPackage(reg, "tuning", registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte("other")}}}).Digest.String()
			}, false),
			Entry("a version change", func(_ *registrytest.Registry, p *v1alpha1.Package) {
				p.Version = "2.0.0"
			}, false),
			Entry("a containerSHA change, timed out", func(reg *registrytest.Registry, p *v1alpha1.Package) {
				p.ContainerSHA = pushPackage(reg, "tuning", registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte("other")}}}).Digest.String()
			}, true),
			Entry("a version change, timed out", func(_ *registrytest.Registry, p *v1alpha1.Package) {
				p.Version = "2.0.0"
			}, true),
		)

		DescribeTable("keep a legacy Job, and a timed-out one whose entry is erroring, whatever the pass resolves for its package",
			func(push func(reg *registrytest.Registry) ocispec.Descriptor, kind packageArtifactKind, finished bool) {
				reg := newRegistry(registrytest.Options{})
				tuning := packageAt(reg, "tuning", "tuning")
				state := v1alpha1.StateInProgress
				if finished {
					state = v1alpha1.StateErroring
				}
				node := nodeWith(nodeName, tuning, stage, state)
				nw := newNodeWright(tuning)
				legacy := createJobFromPackage(validOpts(), &tuning, wrapper.NewSkyhookWrapper(nw), nodeName, stage)
				Expect(setJobPackage(legacy, nw, tuning.Image, stage, &tuning)).To(Succeed())
				if finished {
					legacy = failedTerminally(legacy)
				}
				push(reg)
				h := newHarness(setup{httpClient: reg.Client()}, nw.DeepCopy(), node.DeepCopy(), legacy.DeepCopy())
				h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), &tuning)
				h.r.opts.artifactInspector.drain(ctx)
				Expect(h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), &tuning).Kind).To(Equal(kind))

				expectKept(h, node, tuning, legacy.Name)
			},
			Entry("an artifact, unfinished", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushPackage(reg, "tuning", registrytest.PackageSpec{})
			}, packageArtifactAccepted, false),
			Entry("an artifact, timed out", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushPackage(reg, "tuning", registrytest.PackageSpec{})
			}, packageArtifactAccepted, true),
			Entry("rejected, unfinished", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushNoPayload(reg, "tuning", version)
			}, packageArtifactRejected, false),
			Entry("rejected, timed out", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushNoPayload(reg, "tuning", version)
			}, packageArtifactRejected, true),
		)

		It("neither invalidates nor recreates an unchanged artifact Job across repeated sweeps", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)
			_, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			want := artifactJob(h, tuning, nodeName, stage, pinnedAt(reg, "tuning", desc))
			first := jobOf(h, want.Name)
			expectJob(first, want)

			for range 3 {
				// A pass resolves nothing for a package whose Job runs.
				reg.ResetRequests()
				_, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(reg.Requests()).To(BeEmpty())
				Expect(sweep(h)).To(BeFalse())

				current := jobOf(h, want.Name)
				Expect(current.UID).To(Equal(first.UID))
				Expect(current.ResourceVersion).To(Equal(first.ResourceVersion))
				Expect(invalid(h, want.Name)).To(BeFalse())
			}
		})
	})

	// What each package's artifact lets its uninstall do, decided first in the pass and read by
	// every site that plans an uninstall.
	Describe("uninstall decisions", func() {
		const remedy = "Set uninstall.apply or uninstall.enabled to false, or point the package at an artifact that declares uninstall supported."

		drainBlockedKey := fmt.Sprintf("%s/drainBlocked_%s", v1alpha1.METADATA_PREFIX, skyhookName)

		type passOutcome struct {
			events []string
			// requeue is what resolvePackageArtifacts returned once its inspections were recorded.
			requeue  *ctrl.Result
			released bool
			// nodewright is the NodeWright as the pass left it in memory, which outlives one the
			// finalizer released.
			nodewright *wrapper.Skyhook
		}

		// decidedPass does for the one NodeWright what Reconcile does, once every inspection it asks
		// for is recorded: resolvePackageArtifacts, resolveUninstallDecisions,
		// refreshSkyhookConditions, HandleFinalizer, then RunSkyhookPackages unless the finalizer
		// released it.
		decidedPass := func(h harness) passOutcome {
			GinkgoHelper()
			inspected(h)
			state := stateOf(h)
			sn := state.skyhooks[0]
			requeue := resolvedPackageArtifacts(h.r, sn)
			h.r.resolveUninstallDecisions(ctx, sn)
			Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
			released, err := h.r.HandleFinalizer(ctx, sn, state)
			Expect(err).NotTo(HaveOccurred())
			if !released {
				_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), sn)
				Expect(err).NotTo(HaveOccurred())
			}
			return passOutcome{events: drainEvents(h), requeue: requeue, released: released, nodewright: sn.GetSkyhook()}
		}

		conditionOf := func(p passOutcome, conditionType string) *metav1.Condition {
			return meta.FindStatusCondition(p.nodewright.Status.Conditions, conditionType)
		}

		storedNode := func(h harness, name string) *corev1.Node {
			GinkgoHelper()
			node := &corev1.Node{}
			Expect(h.c.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
			return node
		}

		// entryOf returns the persisted node-state entry of _package on the node.
		entryOf := func(h harness, name string, _package v1alpha1.Package) (*v1alpha1.PackageStatus, bool) {
			GinkgoHelper()
			sn, err := wrapper.NewSkyhookNodeOnly(storedNode(h, name), skyhookName)
			Expect(err).NotTo(HaveOccurred())
			return sn.PackageStatus(_package.GetUniqueName())
		}

		stageOn := func(h harness, name string, _package v1alpha1.Package) v1alpha1.Stage {
			GinkgoHelper()
			status, found := entryOf(h, name, _package)
			Expect(found).To(BeTrue())
			return status.Stage
		}

		// warningReasons counts the Warnings of events by reason.
		warningReasons := func(events []string) map[string]int {
			got := map[string]int{}
			for _, event := range events {
				if fields := strings.SplitN(event, " ", 3); len(fields) == 3 && fields[0] == corev1.EventTypeWarning {
					got[fields[1]]++
				}
			}
			return got
		}

		jobsOn := func(h harness, name string) []batchv1.Job {
			GinkgoHelper()
			return slices.DeleteFunc(jobsOf(h), func(job batchv1.Job) bool { return jobNodeName(&job) != name })
		}

		failing := roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("injected failure")
		})

		// counted sends every request through transport and counts it in calls.
		counted := func(transport http.RoundTripper, calls *atomic.Int64) *http.Client {
			return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return transport.RoundTrip(req)
			})}
		}

		// uninstalling returns _package with an uninstall requested, by uninstall.apply or by the
		// deletion of nw, and with uninstall.enabled set to enabled.
		uninstalling := func(_package v1alpha1.Package, enabled, deletion bool) (v1alpha1.Package, *v1alpha1.NodeWright) {
			_package.Uninstall = &v1alpha1.Uninstall{Enabled: enabled, Apply: enabled && !deletion}
			nw := newNodeWright(_package)
			if deletion {
				nw.DeletionTimestamp = ptr(metav1.Now())
				nw.Finalizers = []string{SkyhookFinalizer}
			}
			return _package, nw
		}

		expectRequeueWithin := func(p passOutcome, window time.Duration) {
			GinkgoHelper()
			Expect(p.requeue).NotTo(BeNil())
			Expect(p.requeue.RequeueAfter).To(And(BeNumerically(">", 0), BeNumerically("<=", window)))
		}

		DescribeTable("decides the uninstall of a package from its artifact",
			func(kind string, enabled, deletion bool) {
				options := registrytest.Options{}
				if kind == "unreadable" {
					// No pull Secret, so every request to it is refused.
					options = registrytest.Options{Username: username, Password: password}
				}
				reg := newRegistry(options)
				var calls atomic.Int64
				transport := reg.Client().Transport
				s := setup{}
				var desc ocispec.Descriptor
				switch kind {
				case "off":
					// Declares unsupported, which the option off never learns.
					s.off = true
					pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				case "legacy":
					desc = pushImage(reg, "tuning")
				case "unreadable":
					pushPackage(reg, "tuning", registrytest.PackageSpec{})
				case "supported":
					desc = pushPackage(reg, "tuning", registrytest.PackageSpec{})
				case "unsupported":
					desc = pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				case "unknown":
					pushPackage(reg, "tuning", registrytest.PackageSpec{})
					transport = failing
				case "rejected":
					pushPackage(reg, "tuning", hostFiles)
				}
				s.httpClient = counted(transport, &calls)
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), enabled, deletion)
				// Complete at its install stage, so the spec alone would start the uninstall.
				h := newHarness(s, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				p := decidedPass(h)

				_, kept := entryOf(h, nodeName, tuning)
				Expect(kept).To(BeTrue())
				Expect(p.released).To(Equal(deletion && (!enabled || kind == "unsupported")))
				if p.released {
					Expect(conditionOf(p, wrapper.SkyhookConditionDeletionBlocked)).To(BeNil())
				}

				// A reference that could not be read starts no uninstall.
				starts := enabled && (kind == "off" || kind == "legacy" || kind == "supported")
				if starts {
					Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
					want := legacyJob(h, tuning, nodeName, v1alpha1.StageUninstall)
					switch kind {
					case "supported":
						want = artifactJob(h, tuning, nodeName, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", desc))
					case "legacy":
						want = imageJob(h, tuning, nodeName, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", desc))
					}
					Expect(jobsOf(h)).To(HaveLen(1))
					expectJob(jobOf(h, want.Name), want)
				} else {
					Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
					Expect(jobsOf(h)).To(BeEmpty())
				}
				if deletion && !p.released {
					// Waits for the uninstall, which a blocked package never finishes.
					condition := conditionOf(p, wrapper.SkyhookConditionDeletionBlocked)
					if kind == "rejected" || kind == "unreadable" {
						Expect(condition).To(HaveField("Reason", "PackageArtifactRefused"))
					} else {
						Expect(condition).To(BeNil())
					}
				}

				// Only a package whose uninstall is decided is resolved, and only with the option on;
				// a reference that could not be read is read again once its wait is over.
				resolved := enabled && kind != "off"
				if resolved {
					Expect(calls.Load()).NotTo(BeZero())
				} else {
					Expect(calls.Load()).To(BeZero())
				}
				switch {
				case resolved && kind == "unknown":
					expectRequeueWithin(p, packageArtifactUnavailableWait)
				case resolved && kind == "unreadable":
					expectRequeueWithin(p, packageArtifactRetry)
				case resolved:
					// The tag is resolved again once the digest it resolved to stops answering for it,
					// so a push to it is picked up then.
					expectRequeueWithin(p, packageArtifactResolvedFor)
				default:
					Expect(p.requeue).To(BeNil())
				}
				blocked := conditionOf(p, wrapper.SkyhookConditionBlocked)
				Expect(blocked != nil && blocked.Reason == wrapper.SkyhookReasonPackageArtifactUnusable).To(
					Equal(resolved && (kind == "rejected" || kind == "unreadable")))
				// The Warning each failed resolution or inspection records once.
				inspection := map[string]int{}
				switch {
				case resolved && (kind == "unknown" || kind == "unreadable"):
					inspection[EventsReasonPackageArtifactInspectionFailed] = 1
				case resolved && kind == "rejected":
					inspection[EventsReasonPackageArtifactRejected] = 1
				}

				reasons := warningReasons(p.events)
				unsupportedCondition := conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)
				switch {
				case !enabled, starts, kind == "unknown":
					Expect(reasons).To(Equal(inspection))
					Expect(unsupportedCondition).To(BeNil())
				case kind == "unsupported":
					Expect(reasons).To(Equal(map[string]int{EventsReasonUninstallUnsupported: 1}))
					Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(ConsistOf(And(
						HavePrefix("Warning UninstallUnsupported tuning:1.0.0 "),
						ContainSubstring("[tuning:1.0.0]"),
						ContainSubstring("["+desc.Digest.String()+"]"),
						ContainSubstring("declares uninstall unsupported"),
						ContainSubstring("not started"),
						ContainSubstring(remedy),
					)))
					Expect(unsupportedCondition).NotTo(BeNil())
					Expect(unsupportedCondition.Status).To(Equal(metav1.ConditionTrue))
					Expect(unsupportedCondition.Reason).To(Equal("ArtifactUninstallUnsupported"))
					Expect(unsupportedCondition.Message).To(Equal("1 package artifact declares uninstall unsupported: tuning:1.0.0 (not started). " + remedy))
				default: // rejected or unreadable
					want := inspection
					want[EventsReasonUninstallDeferred] = 1
					if deletion {
						want["DeletionBlocked"] = 1
					}
					Expect(reasons).To(Equal(want))
					why := And(ContainSubstring("is rejected (host-files)"), ContainSubstring("Push a package artifact this operator runs"))
					if kind == "unreadable" {
						why = And(ContainSubstring("could not be read (unauthorized)"), ContainSubstring("pull Secret"))
					}
					Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(ConsistOf(And(
						HavePrefix("Warning UninstallDeferred tuning:1.0.0 "),
						ContainSubstring("[tuning:1.0.0]"),
						ContainSubstring("["+getPackageImage(&tuning)+"]"),
						why,
						ContainSubstring("deferred"),
						ContainSubstring("uninstall.enabled to false"),
					)))
					Expect(unsupportedCondition).To(BeNil())
				}
			},
			Entry("uninstall.enabled false, the option off, uninstall.apply", "off", false, false),
			Entry("uninstall.enabled false, the option off, deletion", "off", false, true),
			Entry("uninstall.enabled false, a container image, uninstall.apply", "legacy", false, false),
			Entry("uninstall.enabled false, a container image, deletion", "legacy", false, true),
			Entry("uninstall.enabled false, declaring uninstall supported, uninstall.apply", "supported", false, false),
			Entry("uninstall.enabled false, declaring uninstall supported, deletion", "supported", false, true),
			Entry("uninstall.enabled false, declaring uninstall unsupported, uninstall.apply", "unsupported", false, false),
			Entry("uninstall.enabled false, declaring uninstall unsupported, deletion", "unsupported", false, true),
			Entry("uninstall.enabled false, unavailable, uninstall.apply", "unknown", false, false),
			Entry("uninstall.enabled false, unavailable, deletion", "unknown", false, true),
			Entry("uninstall.enabled false, unreadable, uninstall.apply", "unreadable", false, false),
			Entry("uninstall.enabled false, unreadable, deletion", "unreadable", false, true),
			Entry("uninstall.enabled false, rejected, uninstall.apply", "rejected", false, false),
			Entry("uninstall.enabled false, rejected, deletion", "rejected", false, true),
			Entry("uninstall.enabled true, the option off, uninstall.apply", "off", true, false),
			Entry("uninstall.enabled true, the option off, deletion", "off", true, true),
			Entry("uninstall.enabled true, a container image, uninstall.apply", "legacy", true, false),
			Entry("uninstall.enabled true, a container image, deletion", "legacy", true, true),
			Entry("uninstall.enabled true, declaring uninstall supported, uninstall.apply", "supported", true, false),
			Entry("uninstall.enabled true, declaring uninstall supported, deletion", "supported", true, true),
			Entry("uninstall.enabled true, declaring uninstall unsupported, uninstall.apply", "unsupported", true, false),
			Entry("uninstall.enabled true, declaring uninstall unsupported, deletion", "unsupported", true, true),
			Entry("uninstall.enabled true, unavailable, uninstall.apply", "unknown", true, false),
			Entry("uninstall.enabled true, unavailable, deletion", "unknown", true, true),
			Entry("uninstall.enabled true, unreadable, uninstall.apply", "unreadable", true, false),
			Entry("uninstall.enabled true, unreadable, deletion", "unreadable", true, true),
			Entry("uninstall.enabled true, rejected, uninstall.apply", "rejected", true, false),
			Entry("uninstall.enabled true, rejected, deletion", "rejected", true, true),
		)

		DescribeTable("with nothing recorded yet, the first pass starts no uninstall an artifact declares unsupported",
			func(deletion bool) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, deletion)
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				p := decidedPass(h)
				Expect(reg.Requests()).NotTo(BeEmpty())
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(p.released).To(Equal(deletion))
			},
			Entry("under uninstall.apply", false),
			Entry("under deletion", true),
		)

		It("defers a requested uninstall while the package's reference is pending inspection", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
			h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

			// A pass whose inspection is not recorded yet.
			state := stateOf(h)
			sn := state.skyhooks[0]
			Expect(resolvedPackageArtifacts(h.r, sn)).To(Equal(&ctrl.Result{RequeueAfter: packageArtifactPendingRequeue}))
			h.r.resolveUninstallDecisions(ctx, sn)
			Expect(sn.GetSkyhook().UninstallDecision(tuning.Name)).To(Equal(wrapper.UninstallDecisionDeferred))
			Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
			_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), sn)
			Expect(err).NotTo(HaveOccurred())
			Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
			Expect(jobsOf(h)).To(BeEmpty())
			Expect(warningReasons(drainEvents(h))).To(BeEmpty())
			Expect(blockedCondition(h)).To(BeNil())

			decidedPass(h)
			Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
			Expect(jobsOf(h)).To(HaveLen(1))
		})

		It("records the UninstallUnsupported Warning once, while the condition lists the package", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
			h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

			Expect(warnings(decidedPass(h).events, EventsReasonUninstallUnsupported)).To(HaveLen(1))
			p := decidedPass(h)
			Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(BeEmpty())
			Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(
				HaveField("Message", HavePrefix("1 package artifact declares uninstall unsupported: tuning:1.0.0 (not started). ")))
		})

		// Such a pass resolves nothing, so it knows nothing to add or remove either condition by.
		DescribeTable("leaves the Blocked and UninstallUnsupported conditions as they are while the NodeWright holds, and records no Warning again once it runs",
			func(annotation string) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				pushPackage(reg, "rejected", hostFiles)
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
				nw.Spec.Packages["rejected"] = packageAt(reg, "rejected", "rejected")
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				p := decidedPass(h)
				Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(HaveLen(1))
				stored := storedNodeWright(h)
				blocked := meta.FindStatusCondition(stored.Status.Conditions, wrapper.SkyhookConditionBlocked)
				Expect(blocked).To(HaveField("Reason", wrapper.SkyhookReasonPackageArtifactUnusable))
				unsupported := meta.FindStatusCondition(stored.Status.Conditions, wrapper.SkyhookConditionUninstallUnsupported)
				Expect(unsupported).NotTo(BeNil())

				stored.Annotations = map[string]string{v1alpha1.METADATA_PREFIX + "/" + annotation: "true"}
				Expect(h.c.Update(ctx, stored)).To(Succeed())
				for range 2 {
					state := stateOf(h)
					sn := state.skyhooks[0]
					resolvedPackageArtifacts(h.r, sn)
					h.r.resolveUninstallDecisions(ctx, sn)
					Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())
					_, errs := h.r.SaveNodesAndSkyhook(ctx, state, sn)
					Expect(errs).To(BeEmpty())
					conditions := storedNodeWright(h).Status.Conditions
					Expect(meta.FindStatusCondition(conditions, wrapper.SkyhookConditionBlocked)).To(And(
						HaveField("Reason", blocked.Reason), HaveField("Message", blocked.Message)))
					Expect(meta.FindStatusCondition(conditions, wrapper.SkyhookConditionUninstallUnsupported)).To(
						HaveField("Message", unsupported.Message))
					Expect(warningReasons(drainEvents(h))).To(BeEmpty())
				}

				stored = storedNodeWright(h)
				stored.Annotations = nil
				Expect(h.c.Update(ctx, stored)).To(Succeed())
				p = decidedPass(h)
				Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(BeEmpty())
				Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).NotTo(BeNil())
			},
			Entry("paused", "pause"),
			Entry("disabled", "disable"),
		)

		DescribeTable("through a registry outage, defers the uninstall with no Job, and decides it from the artifact once the registry answers",
			func(deletion bool, declared artifact.Uninstall) {
				reg := newRegistry(registrytest.Options{})
				desc := pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: declared})
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, deletion)
				h := newHarness(setup{httpClient: &http.Client{Transport: failing}}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				p := decidedPass(h)
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(BeEmpty())
				Expect(warnings(p.events, EventsReasonPackageArtifactInspectionFailed)).To(ConsistOf(ContainSubstring("(transport)")))
				Expect(conditionOf(p, wrapper.SkyhookConditionBlocked)).To(BeNil())
				Expect(conditionOf(p, wrapper.SkyhookConditionDeletionBlocked)).To(BeNil())
				expectRequeueWithin(p, packageArtifactUnavailableWait)
				Expect(p.released).To(BeFalse())

				h.r.opts.artifactInspector.httpClient = reg.Client()
				stepPast(packageArtifactUnavailableWait)
				p = decidedPass(h)
				Expect(warnings(p.events, EventsReasonPackageArtifactInspectionFailed)).To(BeEmpty())
				if declared == artifact.UninstallSupported {
					Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
					want := artifactJob(h, tuning, nodeName, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", desc))
					Expect(jobsOf(h)).To(HaveLen(1))
					expectJob(jobOf(h, want.Name), want)
					return
				}
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(HaveField("Message", ContainSubstring("tuning:1.0.0 (not started)")))
			},
			Entry("under uninstall.apply, an artifact declaring uninstall supported", false, artifact.UninstallSupported),
			Entry("under uninstall.apply, an artifact declaring uninstall unsupported", false, artifact.UninstallUnsupported),
			Entry("under deletion, an artifact declaring uninstall supported", true, artifact.UninstallSupported),
			Entry("under deletion, an artifact declaring uninstall unsupported", true, artifact.UninstallUnsupported),
		)

		Describe("a package that comes to name an artifact declaring uninstall unsupported, mid-cycle", func() {
			// The nodes are named for their part. Sorted by name, pending is offered a slot of the
			// batch ahead of started: the budget admits two once complete counts as completed, and
			// started's Job, which no spec here lets finish, holds its node.
			const (
				// started is uninstalling, with an unfinished artifact uninstall Job pinned to the
				// supported artifact, and cordoned for the reboot interrupt.
				started = "node-started"
				// pending is uninstalling with no Job yet, and not cordoned.
				pending = "node-pending"
				// complete is at the complete install stage of a package with an interrupt.
				complete = "node-complete"
			)

			// midCycle returns that world: the cycle began at the supported artifact, and the package
			// now names, by its tag, one declaring uninstall unsupported, which is what is recorded.
			midCycle := func(deletion bool) (harness, *registrytest.Registry, v1alpha1.Package, ocispec.Descriptor, batchv1.Job) {
				GinkgoHelper()
				reg := newRegistry(registrytest.Options{})
				supported := pushPackage(reg, "tuning", registrytest.PackageSpec{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				driver := packageAt(reg, "tuning", "tuning")
				driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
				tuning, nw := uninstalling(driver, true, deletion)

				a := nodeWith(started, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
				a.Spec.Unschedulable = true
				metav1.SetMetaDataAnnotation(&a.ObjectMeta, cordonKey, "true")
				b := nodeWith(pending, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress)
				c := nodeWith(complete, tuning, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete)
				aJob := createArtifactJobFromPackage(validOpts(), &tuning, wrapper.NewSkyhookWrapper(nw), started, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", supported))
				Expect(setJobPackage(aJob, nw, tuning.Image, v1alpha1.StageUninstall, &tuning)).To(Succeed())

				h := newHarness(setup{httpClient: reg.Client()}, nw, a, b, c, aJob)
				inspected(h)
				Expect(h.r.opts.artifactInspector.resolve(ctx, storedNodeWright(h), &tuning).Uninstall).To(Equal(artifact.UninstallUnsupported))
				drainEvents(h)
				return h, reg, tuning, supported, jobOf(h, aJob.Name)
			}

			// rebuilt is a fresh harness of s over the world h has persisted, as after a restart.
			rebuilt := func(h harness, s setup) harness {
				GinkgoHelper()
				var nodes corev1.NodeList
				Expect(h.c.List(ctx, &nodes)).To(Succeed())
				objects := []client.Object{storedNodeWright(h)}
				for i := range nodes.Items {
					objects = append(objects, &nodes.Items[i])
				}
				for _, job := range jobsOf(h) {
					objects = append(objects, job.DeepCopy())
				}
				return newHarness(s, objects...)
			}

			It("records the PackageArtifactUninstallUnsupported Warning once, in Reconcile's order, on a NodeWright that has its finalizer", func() {
				h, _, _, _, _ := midCycle(false)
				current := storedNodeWright(h)
				current.Finalizers = append(current.Finalizers, SkyhookFinalizer)
				Expect(h.c.Update(ctx, current)).To(Succeed())

				Expect(warnings(decidedPass(h).events, EventsReasonPackageArtifactUninstallUnsupported)).To(HaveLen(1))
				Expect(warnings(decidedPass(h).events, EventsReasonPackageArtifactUninstallUnsupported)).To(BeEmpty())
			})

			It("keeps the started node's uninstall Job and cordon, gives the pending node neither, and starts no uninstall on the complete node", func() {
				h, _, tuning, _, aJob := midCycle(false)

				p := decidedPass(h)
				Expect(sweep(h)).To(BeFalse())

				kept := jobOf(h, aJob.Name)
				Expect(kept.UID).To(Equal(aJob.UID))
				Expect(kept.ResourceVersion).To(Equal(aJob.ResourceVersion))
				Expect(invalid(h, aJob.Name)).To(BeFalse())
				a := storedNode(h, started)
				Expect(a.Spec.Unschedulable).To(BeTrue())
				Expect(a.Annotations).To(HaveKey(cordonKey))

				Expect(jobsOn(h, pending)).To(BeEmpty())
				b := storedNode(h, pending)
				Expect(b.Spec.Unschedulable).To(BeFalse())
				Expect(b.Annotations).NotTo(HaveKey(cordonKey))
				Expect(b.Annotations).NotTo(HaveKey(drainBlockedKey))
				Expect(stageOn(h, pending, tuning)).To(Equal(v1alpha1.StageUninstall))

				Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StagePostInterrupt))
				Expect(jobsOn(h, complete)).To(BeEmpty())

				Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(HaveField("Message",
					"1 package artifact declares uninstall unsupported: tuning:1.0.0 (stalled). "+remedy))
				expectRequeueWithin(p, packageArtifactResolvedFor)
			})

			// cancel persists uninstall as the uninstall of tuning.
			cancel := func(h harness, tuning v1alpha1.Package, uninstall *v1alpha1.Uninstall) {
				GinkgoHelper()
				current := storedNodeWright(h)
				cancelled := current.Spec.Packages[tuning.Name]
				cancelled.Uninstall = uninstall
				current.Spec.Packages[tuning.Name] = cancelled
				Expect(h.c.Update(ctx, current)).To(Succeed())
			}

			DescribeTable("is cancelled, resetting each node that has begun it to StageApply",
				func(uninstall *v1alpha1.Uninstall) {
					h, _, tuning, _, _ := midCycle(false)
					p := decidedPass(h)
					Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).NotTo(BeNil())

					cancel(h, tuning, uninstall)
					p = decidedPass(h)
					Expect(stageOn(h, started, tuning)).To(Equal(v1alpha1.StageApply))
					Expect(stageOn(h, pending, tuning)).To(Equal(v1alpha1.StageApply))
					Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StagePostInterrupt))
					Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
					Expect(warnings(p.events, EventsReasonPackageArtifactUninstallUnsupported)).To(BeEmpty())
				},
				Entry("by uninstall.apply=false", &v1alpha1.Uninstall{Enabled: true}),
				Entry("by uninstall.enabled=false", &v1alpha1.Uninstall{}),
			)

			It("under deletion, blocks it, and uninstall.enabled=false cancels the cycle and releases the finalizer", func() {
				h, _, tuning, _, _ := midCycle(true)

				p := decidedPass(h)
				Expect(p.released).To(BeFalse())
				Expect(meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionDeletionBlocked)).To(
					HaveField("Reason", "PackageArtifactRefused"))
				Expect(jobsOn(h, pending)).To(BeEmpty())
				Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StagePostInterrupt))

				cancel(h, tuning, &v1alpha1.Uninstall{})
				p = decidedPass(h)
				Expect(p.released).To(BeTrue())
				Expect(stageOn(h, started, tuning)).To(Equal(v1alpha1.StageApply))
				Expect(stageOn(h, pending, tuning)).To(Equal(v1alpha1.StageApply))
				Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StagePostInterrupt))
				Expect(jobsOn(h, pending)).To(BeEmpty())
			})

			It("under deletion with the option off, uninstall.enabled=false releases the finalizer and leaves each node's entry as the cycle left it", func() {
				h, reg, tuning, _, _ := midCycle(true)
				h = rebuilt(h, setup{off: true, httpClient: reg.Client()})

				cancel(h, tuning, &v1alpha1.Uninstall{})
				p := decidedPass(h)
				Expect(p.released).To(BeTrue())
				for _, name := range []string{started, pending} {
					status, found := entryOf(h, name, tuning)
					Expect(found).To(BeTrue())
					Expect(status.Stage).To(Equal(v1alpha1.StageUninstall))
					Expect(status.State).To(Equal(v1alpha1.StateInProgress))
				}
				Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StagePostInterrupt))
			})

			DescribeTable("returns the decision to spec",
				// back moves the world on from the unsupported artifact and returns the harness to
				// pass with, the package as it now is, and whether the uninstall Jobs it builds are
				// artifact Jobs.
				func(back func(h harness, reg *registrytest.Registry, tuning v1alpha1.Package, supported ocispec.Descriptor) (harness, v1alpha1.Package, bool)) {
					h, reg, tuning, supported, _ := midCycle(false)
					p := decidedPass(h)
					Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).NotTo(BeNil())

					h, tuning, artifactJobs := back(h, reg, tuning, supported)
					// The pending and complete nodes are cordoned, then drained, before their uninstall Jobs.
					for range 4 {
						p = decidedPass(h)
						if len(jobsOn(h, pending)) > 0 {
							break
						}
					}
					want := legacyJob(h, tuning, pending, v1alpha1.StageUninstall)
					if artifactJobs {
						want = artifactJob(h, tuning, pending, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", supported))
					}
					Expect(jobsOn(h, pending)).To(HaveLen(1))
					expectJob(jobOf(h, want.Name), want)
					Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StageUninstall))
					Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
					Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(BeEmpty())
				},
				Entry("once the package names the supported artifact by its containerSHA", func(h harness, _ *registrytest.Registry, tuning v1alpha1.Package, supported ocispec.Descriptor) (harness, v1alpha1.Package, bool) {
					current := storedNodeWright(h)
					tuning.ContainerSHA = supported.Digest.String()
					current.Spec.Packages[tuning.Name] = tuning
					Expect(h.c.Update(ctx, current)).To(Succeed())
					return h, tuning, true
				}),
				Entry("once the option is off", func(h harness, reg *registrytest.Registry, tuning v1alpha1.Package, _ ocispec.Descriptor) (harness, v1alpha1.Package, bool) {
					return rebuilt(h, setup{off: true, httpClient: reg.Client()}), tuning, false
				}),
			)
		})

		DescribeTable("runs an uninstall interrupt whatever the decision",
			func(state v1alpha1.State) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				driver := packageAt(reg, "tuning", "tuning")
				driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
				tuning, nw := uninstalling(driver, true, false)
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageUninstallInterrupt, state))

				p := decidedPass(h)
				Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(BeEmpty())
				if state == v1alpha1.StateComplete {
					_, found := entryOf(h, nodeName, tuning)
					Expect(found).To(BeFalse())
					Expect(jobsOf(h)).To(BeEmpty())
					return
				}
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstallInterrupt))
				jobs := jobsOf(h)
				Expect(jobs).To(HaveLen(1))
				Expect(jobs[0].Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/package", tuning.Name+"-"+version))
				Expect(jobs[0].Spec.Template.Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/interrupt", interruptLabelValue))
			},
			Entry("building the interrupt Job of one in progress", v1alpha1.StateInProgress),
			Entry("removing the entry of one complete", v1alpha1.StateComplete),
		)

		// Its interrupt Job is not running: it failed, was removed, or the stage was rerun. The
		// registry refuses every request, so reading the reference would block the package.
		DescribeTable("runs the interrupt of a package mid-interrupt without reading its reference",
			func(state v1alpha1.State) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				driver := packageAt(reg, "tuning", "tuning")
				driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
				h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(driver), nodeWith(nodeName, driver, v1alpha1.StageInterrupt, state))
				reg.ResetRequests()

				p := decidedPass(h)
				Expect(reg.Requests()).To(BeEmpty())
				Expect(warningReasons(p.events)).To(BeEmpty())
				Expect(nodeStatus(h)).NotTo(Equal(v1alpha1.StatusBlocked))
				Expect(conditionOf(p, wrapper.SkyhookConditionBlocked)).To(BeNil())
				Expect(stageOn(h, nodeName, driver)).To(Equal(v1alpha1.StageInterrupt))
				jobs := jobsOf(h)
				Expect(jobs).To(HaveLen(1))
				Expect(jobs[0].Spec.Template.Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/interrupt", interruptLabelValue))
			},
			Entry("in progress", v1alpha1.StateInProgress),
			Entry("erroring", v1alpha1.StateErroring),
		)

		It("reports no DeletionBlocked for a node mid uninstall-interrupt whose reference cannot be read", func() {
			// No pull Secret, so every request to it is refused.
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "tuning", registrytest.PackageSpec{})
			driver := packageAt(reg, "tuning", "tuning")
			driver.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
			tuning, nw := uninstalling(driver, true, true)
			h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageUninstallInterrupt, v1alpha1.StateInProgress))

			p := decidedPass(h)
			Expect(p.released).To(BeFalse())
			Expect(conditionOf(p, wrapper.SkyhookConditionDeletionBlocked)).To(BeNil())
			Expect(warningReasons(p.events)).NotTo(HaveKey("DeletionBlocked"))
			Expect(nodeStatus(h)).NotTo(Equal(v1alpha1.StatusBlocked))
			jobs := jobsOf(h)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Spec.Template.Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/interrupt", interruptLabelValue))
		})

		It("never re-applies a package whose uninstall completed once its artifact declares uninstall unsupported", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
			// The uninstall completed, so the node holds no entry.
			h := newHarness(setup{httpClient: reg.Client()}, nw, newNode(nodeName))

			p := decidedPass(h)
			_, found := entryOf(h, nodeName, tuning)
			Expect(found).To(BeFalse())
			Expect(jobsOf(h)).To(BeEmpty())
			Expect(warningReasons(p.events)).To(BeEmpty())
			Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
			Expect(p.requeue).To(BeNil())
			// No node holds the package, so nothing is decided, and nothing resolved.
			Expect(p.nodewright.UninstallDecisions).To(BeEmpty())
			Expect(p.nodewright.PackageArtifacts).To(BeEmpty())
		})

		DescribeTable("a package at StageUninstall with no Job gets none",
			func(httpClient func(reg *registrytest.Registry) *http.Client, spec registrytest.PackageSpec, warning string, decision wrapper.UninstallDecision) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", spec)
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
				h := newHarness(setup{httpClient: httpClient(reg)}, nw, nodeWith(nodeName, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress))

				p := decidedPass(h)
				Expect(p.nodewright.UninstallDecision(tuning.Name)).To(Equal(decision))
				Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(BeEmpty())
				Expect(warnings(p.events, warning)).To(HaveLen(1))
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
				Expect(jobsOf(h)).To(BeEmpty())
			},
			Entry("while its reference is unavailable",
				func(*registrytest.Registry) *http.Client { return &http.Client{Transport: failing} },
				registrytest.PackageSpec{}, EventsReasonPackageArtifactInspectionFailed, wrapper.UninstallDecisionDeferred),
			Entry("when its artifact is rejected",
				func(reg *registrytest.Registry) *http.Client { return reg.Client() },
				hostFiles, EventsReasonPackageArtifactRejected, wrapper.UninstallDecisionDeferred),
		)

		Describe("the UninstallUnsupported condition", func() {
			It("clears once uninstall.apply is false", func() {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))
				Expect(conditionOf(decidedPass(h), wrapper.SkyhookConditionUninstallUnsupported)).NotTo(BeNil())

				current := storedNodeWright(h)
				cancelled := current.Spec.Packages[tuning.Name]
				cancelled.Uninstall = &v1alpha1.Uninstall{Enabled: true}
				current.Spec.Packages[tuning.Name] = cancelled
				Expect(h.c.Update(ctx, current)).To(Succeed())

				p := decidedPass(h)
				Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
				Expect(warningReasons(p.events)).To(BeEmpty())
				Expect(p.requeue).To(BeNil())
			})

			It("is removed with the option off and nothing recorded, which starts the uninstall", func() {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
				// Left from when the option was on.
				nw.Status.Conditions = []metav1.Condition{{
					Type: wrapper.SkyhookConditionUninstallUnsupported, Status: metav1.ConditionTrue,
					Reason:             wrapper.SkyhookReasonArtifactUninstallUnsupported,
					Message:            "1 package artifact declares uninstall unsupported: tuning:1.0.0 (not started). " + remedy,
					LastTransitionTime: metav1.Now(),
				}}
				h := newHarness(setup{off: true, httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				p := decidedPass(h)
				Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
				Expect(meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionUninstallUnsupported)).To(BeNil())
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
				Expect(reg.Requests()).To(BeEmpty())
			})
		})
	})

	Describe("each reconcile", func() {
		// reconcile runs Reconcile once and then every inspection it asked for, as the inspector's
		// workers would before the next one.
		reconcile := func(h harness) {
			GinkgoHelper()
			_, err := h.r.Reconcile(ctx, ctrl.Request{})
			Expect(err).NotTo(HaveOccurred())
			h.r.opts.artifactInspector.drain(ctx)
			drainEvents(h)
		}

		// seed caches an entry for the digest of what was pushed to repository, as a run of the
		// operator before a container restart left it.
		seed := func(h harness, reg *registrytest.Registry, repository string, desc ocispec.Descriptor) {
			GinkgoHelper()
			Expect(h.r.opts.artifactInspector.cache.put(ctx, desc.Digest.String(), packageArtifactCacheEntry{
				Kind: packageArtifactAccepted, Uninstall: artifact.UninstallSupported, Repository: reg.Host() + "/" + repository, InspectedAt: fakeClock.Now(),
			})).To(Succeed())
		}

		cachedIn := func(h harness, desc ocispec.Descriptor) bool {
			GinkgoHelper()
			_, ok, err := h.r.opts.artifactInspector.cache.get(desc.Digest.String())
			Expect(err).NotTo(HaveOccurred())
			return ok
		}

		// migrated stamps the NodeWright and the node with the operator's version, as on a cluster
		// this operator already migrated, so Reconcile goes past its migrations.
		migrated := func(nw *v1alpha1.NodeWright, node *corev1.Node) (*v1alpha1.NodeWright, *corev1.Node) {
			nw.Annotations = map[string]string{fmt.Sprintf("%s/version", v1alpha1.METADATA_PREFIX): operatorversion.VERSION}
			node.Annotations = map[string]string{fmt.Sprintf("%s/version_%s", v1alpha1.METADATA_PREFIX, nw.Name): operatorversion.VERSION}
			return nw, node
		}

		inspections := func(reg *registrytest.Registry) int {
			count := 0
			for _, request := range reg.Requests() {
				if request.Method == http.MethodGet && strings.Contains(request.Path, "/manifests/") {
					count++
				}
			}
			return count
		}

		It("removes first the cache entries no NodeWright names, and never the one its pass needs", func() {
			reg := newRegistry(registrytest.Options{})
			pushed := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			stale := pushPackage(reg, "stale", registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte("stale")}}})
			tuning := packageAt(reg, "tuning", "tuning")
			nw, node := migrated(newNodeWright(tuning), newNode(nodeName))
			h := newHarness(setup{httpClient: reg.Client()}, nw, node)
			seed(h, reg, "stale", stale)
			reg.ResetRequests()

			// The passes go on long past the time a resolution answers for the tag: once its Job
			// runs, none resolves it again, and its last resolution, whatever its age, keeps the
			// entry the next stage needs.
			for range 6 {
				reconcile(h)
				stepPast(packageArtifactResolvedFor)
			}

			Expect(cachedIn(h, stale)).To(BeFalse())
			Expect(cachedIn(h, pushed)).To(BeTrue())
			Expect(inspections(reg)).To(Equal(1))
			Expect(jobsOf(h)).To(ContainElement(HaveField("ObjectMeta.Labels", HaveKeyWithValue("nodewright.nvidia.com/package", "tuning-1.0.0"))))
		})

		It("empties the cache once no NodeWright is left", func() {
			reg := newRegistry(registrytest.Options{})
			stale := pushPackage(reg, "stale", registrytest.PackageSpec{})
			h := newHarness(setup{httpClient: reg.Client()}, newNode(nodeName))
			seed(h, reg, "stale", stale)

			reconcile(h)
			Expect(cachedIn(h, stale)).To(BeFalse())
		})

		It("touches no cache entry with the option off", func() {
			reg := newRegistry(registrytest.Options{})
			stale := pushPackage(reg, "stale", registrytest.PackageSpec{})
			h := newHarness(setup{off: true, httpClient: reg.Client()}, newNode(nodeName))
			seed(h, reg, "stale", stale)

			reconcile(h)
			Expect(cachedIn(h, stale)).To(BeTrue())
		})
	})
})

// roundTripperFunc is an http.RoundTripper made of a function.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// resolvedPackageArtifacts is the requeue r.resolvePackageArtifacts asks for, which must not fail.
func resolvedPackageArtifacts(r *SkyhookReconciler, skyhook SkyhookNodes) *ctrl.Result {
	GinkgoHelper()
	result, err := r.resolvePackageArtifacts(ctx, skyhook)
	Expect(err).NotTo(HaveOccurred())
	return result
}
