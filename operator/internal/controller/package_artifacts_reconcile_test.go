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
	"github.com/NVIDIA/nodewright/operator/internal/drain"
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

	// setup is how a spec's reconciler is built. A cache is injected whatever off says.
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
		cache := newPackageArtifactCache(c, recorder, namespace, s.pullSecret)
		cache.httpClient = s.httpClient
		cache.clock = fakeClock
		cache.requestTimeout = s.timeout
		opts.artifactCache = cache
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

	// pass makes one RunSkyhookPackages pass over the NodeWright and Nodes as persisted, and
	// returns the events it recorded with its error.
	pass := func(h harness) ([]string, error) {
		GinkgoHelper()
		state := stateOf(h)
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

	rejectedCondition := func(h harness) *metav1.Condition {
		GinkgoHelper()
		return meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionPackageArtifactRejected)
	}

	skyhookNodeOf := func(h harness, node *corev1.Node) wrapper.SkyhookNode {
		GinkgoHelper()
		sn, err := wrapper.NewSkyhookNode(node, storedNodeWright(h))
		Expect(err).NotTo(HaveOccurred())
		return sn
	}

	applyPackage := func(h harness, node *corev1.Node, _package v1alpha1.Package) error {
		GinkgoHelper()
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

	It("asks no registry and builds the current Jobs while the option is off, even with a cache injected", func() {
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
			Type: wrapper.SkyhookConditionPackageArtifactRejected, Status: metav1.ConditionTrue,
			Reason: wrapper.SkyhookReasonArtifactRejected, Message: "1 package artifact rejected: host:1.0.0 (host-files)",
			LastTransitionTime: metav1.Now(),
		}}
		h := newHarness(setup{off: true, httpClient: reg.Client()}, nw, newNode(nodeName))

		// The cache holds an accepted and a rejected outcome, which the option on would act on,
		// while cold is a miss, which the option on would inspect.
		Expect(h.r.opts.artifactCache.lookup(ctx, nw, &tuning).Kind).To(Equal(packageArtifactAccepted))
		Expect(h.r.opts.artifactCache.lookup(ctx, nw, &host).Kind).To(Equal(packageArtifactRejected))
		reg.ResetRequests()

		state := stateOf(h)
		Expect(h.r.refreshSkyhookConditions(ctx, state, state.skyhooks[0])).To(Succeed())
		Expect(meta.FindStatusCondition(state.skyhooks[0].GetSkyhook().Status.Conditions, wrapper.SkyhookConditionPackageArtifactRejected)).To(BeNil())

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
		It("is the option-off Job for a legacy image", func() {
			reg := newRegistry(registrytest.Options{})
			pushImage(reg, "tuning")
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
				if !off {
					outcome, ok := h.r.opts.artifactCache.cached(&tuning)
					Expect(ok).To(BeTrue())
					Expect(outcome.Kind).To(Equal(packageArtifactLegacy))
				}
			}
			on, off := built[false], built[true]
			Expect(on.Labels).To(Equal(off.Labels))
			Expect(on.Annotations).To(Equal(off.Annotations))
			Expect(on.OwnerReferences).To(Equal(off.OwnerReferences))
			Expect(on.Spec).To(Equal(off.Spec))
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

		It("falls back to the option-off Job with a Warning when a real artifact cannot be inspected", func() {
			// The pull Secret names the registry with a wrong password, so the inspection reaches
			// the registry and is refused there.
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client(), pullSecret: pullSecret}, newNodeWright(tuning), node, pullSecretFor(reg, "wrong-password"))

			Expect(applyPackage(h, node, tuning)).To(Succeed())
			requests := reg.Requests()
			Expect(requests).NotTo(BeEmpty())
			Expect(requests[len(requests)-1].Status).To(Equal(http.StatusUnauthorized))

			jobs := jobsOf(h)
			Expect(jobs).To(HaveLen(1))
			want := legacyJob(h, tuning, nodeName, v1alpha1.StageApply)
			expectJob(jobs[0], want)
			notWanted := artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", desc))
			Expect(jsonOf(jobs[0].Spec)).NotTo(MatchJSON(jsonOf(notWanted.Spec)))

			Expect(warnings(drainEvents(h), EventsReasonPackageArtifactInspectionFailed)).To(ConsistOf(And(
				HavePrefix("Warning PackageArtifactInspectionFailed tuning:1.0.0 "),
				ContainSubstring("[tuning:1.0.0]"),
				ContainSubstring("["+getPackageImage(&tuning)+"]"),
				ContainSubstring("unauthorized"),
			)))
		})

		It("gives credentials end to end: an artifact Job with the pull Secret, the unauthorized fallback without", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)

			with := newHarness(setup{httpClient: reg.Client(), pullSecret: pullSecret}, newNodeWright(tuning), node.DeepCopy(), pullSecretFor(reg, password))
			Expect(applyPackage(with, node.DeepCopy(), tuning)).To(Succeed())
			want := artifactJob(with, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", desc))
			got := jobOf(with, want.Name)
			expectJob(got, want)
			Expect(hasEnv(got, envRegistryAuthFile)).To(BeTrue())
			Expect(drainEvents(with)).NotTo(ContainElement(HavePrefix("Warning ")))

			without := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node.DeepCopy())
			Expect(applyPackage(without, node.DeepCopy(), tuning)).To(Succeed())
			expectJob(jobOf(without, want.Name), legacyJob(without, tuning, nodeName, v1alpha1.StageApply))
			Expect(warnings(drainEvents(without), EventsReasonPackageArtifactInspectionFailed)).To(ConsistOf(ContainSubstring("unauthorized")))
		})
	})

	Describe("a rejected artifact", func() {
		DescribeTable("gets no Job, a Warning and the condition, which clear once a corrected tag is past the TTL",
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
					ContainSubstring("["+getPackageImage(&tuning)+"]"),
					ContainSubstring("is rejected: "+reason),
				)))
				condition := rejectedCondition(h)
				Expect(condition).NotTo(BeNil())
				Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				Expect(condition.Reason).To(Equal("ArtifactRejected"))
				Expect(condition.Message).To(Equal("1 package artifact rejected: tuning:1.0.0 (" + reason + ")"))

				// ApplyPackage alone builds nothing for it and leaves the report to the decision.
				Expect(applyPackage(h, node, tuning)).To(Succeed())
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(drainEvents(h)).To(BeEmpty())

				if tuning.ContainerSHA != "" {
					return // the content at a digest never changes
				}
				corrected := pushPackage(reg, "tuning", registrytest.PackageSpec{})
				stepPast(packageArtifactTagTTL)
				events, err = pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings(events, EventsReasonPackageArtifactRejected)).To(BeEmpty())
				want := artifactJob(h, tuning, nodeName, v1alpha1.StageApply, pinnedAt(reg, "tuning", corrected))
				Expect(jobsOf(h)).To(HaveLen(1))
				expectJob(jobOf(h, want.Name), want)
				Expect(rejectedCondition(h)).To(BeNil())
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
			nw := newNodeWright(packages...)
			h := newHarness(setup{httpClient: reg.Client()}, nw)
			for i := range packages {
				Expect(h.r.opts.artifactCache.lookup(ctx, nw, &packages[i]).Kind).To(Equal(packageArtifactRejected))
			}

			skyhook := wrapper.NewSkyhookWrapper(nw)
			h.r.updatePackageArtifactRejectedCondition(skyhook)
			condition := meta.FindStatusCondition(skyhook.Status.Conditions, wrapper.SkyhookConditionPackageArtifactRejected)
			Expect(condition).NotTo(BeNil())
			listed := make([]string, 0, wrapper.ReadyConditionNodeListLimit)
			for _, p := range packages[:wrapper.ReadyConditionNodeListLimit] {
				listed = append(listed, p.Name+":"+p.Version+" (host-files)")
			}
			Expect(condition.Message).To(Equal(fmt.Sprintf("%d package artifacts rejected: %s, and 2 more", len(packages), strings.Join(listed, ", "))))
		})
	})

	Describe("uninstall unsupported", func() {
		// uninstalling returns tuning with an uninstall requested, on a node where its install is
		// complete, so the pass starts its uninstall stage.
		uninstalling := func(reg *registrytest.Registry) (v1alpha1.Package, *corev1.Node) {
			tuning := packageAt(reg, "tuning", "tuning")
			tuning.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
			return tuning, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete)
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
			// It is accepted, so nothing reports it rejected.
			Expect(rejectedCondition(h)).To(BeNil())
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
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				finalize := func() bool {
					GinkgoHelper()
					state := stateOf(h)
					handled, err := h.r.HandleFinalizer(ctx, state.skyhooks[0], state)
					Expect(err).NotTo(HaveOccurred())
					return handled
				}
				deletionBlocked := func() *metav1.Condition {
					GinkgoHelper()
					return meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionDeletionBlocked)
				}

				// As in a reconcile, the finalizer decides before the pass that starts the uninstall.
				Expect(finalize()).To(BeFalse())
				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
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
			Entry("is blocked, with DeletionBlocked, by an artifact declaring uninstall unsupported",
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
				driver.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
				node = nodeWith(nodeName, driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete)
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

			// Without the decision, the first pass cordons and the second drains.
			for range 2 {
				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(events).To(ContainElement(HavePrefix("Warning PackageArtifact")))

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
				driver.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
				sn, err := wrapper.NewSkyhookNodeOnly(node, skyhookName)
				Expect(err).NotTo(HaveOccurred())
				Expect(sn.Upsert(driver.PackageRef, driver.Image, v1alpha1.StateComplete, v1alpha1.StagePostInterrupt, 0, "")).To(Succeed())
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
				driver.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
				node = nodeWith(nodeName, driver, v1alpha1.StagePostInterrupt, v1alpha1.StateComplete)
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

			state := stateOf(h)
			sn := state.skyhooks[0]
			Expect(h.r.refreshSkyhookConditions(ctx, state, sn)).To(Succeed())

			Expect(meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionBlocked)).To(BeNil())
			Expect(meta.FindStatusCondition(sn.GetSkyhook().Status.Conditions, wrapper.SkyhookConditionDrainBlocked)).To(BeNil())
			Expect(warnings(drainEvents(h), EventsReasonSkyhookDrain)).To(BeEmpty())
			_, skyhookNode := sn.GetNode(nodeName)
			Expect(skyhookNode.GetNode().Annotations).NotTo(HaveKey(drainBlockedKey))
		},
		Entry("rejected at apply", hostFiles, false),
		Entry("uninstall unsupported at uninstall", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported}, true),
	)

	It("waits for one black-holed registry once in a pass, and still builds every Job", func() {
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

		Expect(jobsOf(h)).To(HaveLen(4))
		failed := warnings(events, EventsReasonPackageArtifactInspectionFailed)
		Expect(failed).To(HaveLen(len(blackHoled)))
		for _, p := range blackHoled {
			want := legacyJob(h, p, nodeName, v1alpha1.StageApply)
			expectJob(jobOf(h, want.Name), want)
			Expect(failed).To(ContainElement(And(
				HavePrefix("Warning PackageArtifactInspectionFailed "+p.Name+":"+version+" "),
				ContainSubstring("["+getPackageImage(&p)+"]"),
				ContainSubstring("(timeout)"),
			)))
		}
		want := artifactJob(h, d, nodeName, v1alpha1.StageApply, pinnedAt(reachable, "d", desc))
		expectJob(jobOf(h, want.Name), want)
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
			{open, "legacy", packageArtifactLegacy},
			{auth, "artifact", packageArtifactUnknown},
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
				Expect(h.r.packageArtifactOutcome(ctx, storedNodeWright(h), &tuning).Kind).To(Equal(ref.kind), what)
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

		// Each failing variant traces its failure to the cache through the outcome it leaves.
		DescribeTable("keep an unfinished artifact Job, and a timed-out one whose entry is erroring, when the operator would now build the legacy Job",
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

				// The pass decides the package, which is where the cache meets the registry. For a
				// finished Job its create hits the existing name, so handleExistingJob decides.
				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(events).NotTo(ContainElement(HavePrefix("Warning PackageArtifactRejected")))
				traced(h)
				Expect(jobsOf(h)).To(HaveLen(1))

				expectKept(h, node, tuning, built.Name)
				// What the operator would build now differs from the Job kept.
				want := legacyJob(h, tuning, nodeName, stage)
				Expect(jsonOf(jobOf(h, built.Name).Spec)).NotTo(MatchJSON(jsonOf(want.Spec)))
			},
			Entry("with a cold cache and a registry failing every request", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{})
				var calls atomic.Int64
				failing := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return nil, errors.New("injected failure")
				})}
				return setup{httpClient: failing}, reg, nil, func(h harness) {
					Expect(calls.Load()).To(BeNumerically(">", 0))
					Expect(h.r.packageArtifactOutcome(ctx, storedNodeWright(h), ptr(packageAt(reg, "tuning", "tuning")))).To(Equal(unknownPackageArtifact(packageArtifactErrorTransport)))
				}
			}, false),
			Entry("with an unauthorized pull Secret", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				return setup{httpClient: reg.Client(), pullSecret: pullSecret}, reg, []client.Object{pullSecretFor(reg, "wrong-password")}, func(h harness) {
					requests := reg.Requests()
					Expect(requests).NotTo(BeEmpty())
					Expect(requests[len(requests)-1].Status).To(Equal(http.StatusUnauthorized))
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
					Expect(calls.Load()).To(BeNumerically(">", 0))
				}
			}, true),
			Entry("timed out, with an unauthorized pull Secret", func() (setup, *registrytest.Registry, []client.Object, func(harness)) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "tuning", registrytest.PackageSpec{})
				return setup{httpClient: reg.Client(), pullSecret: pullSecret}, reg, []client.Object{pullSecretFor(reg, "wrong-password")}, func(h harness) {
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

		It("keeps an unfinished artifact Job pinned to the old digest of a re-pushed tag, while a Job built afterwards pins the new one", func() {
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
			stepPast(packageArtifactTagTTL)
			_, err = pass(h)
			Expect(err).NotTo(HaveOccurred())
			outcome, ok := h.r.opts.artifactCache.cached(&tuning)
			Expect(ok).To(BeTrue())
			Expect(outcome.Pinned).To(Equal(pinnedAt(reg, "tuning", second)))

			expectKept(h, node, tuning, kept.Name)
			expectJob(jobOf(h, kept.Name), kept)

			other := newNode("other-node")
			Expect(applyPackage(h, other, tuning)).To(Succeed())
			want := artifactJob(h, tuning, other.Name, stage, pinnedAt(reg, "tuning", second))
			expectJob(jobOf(h, want.Name), want)
		})

		It("keeps an unfinished artifact Job whose tag was re-pushed as a container image", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "tuning", registrytest.PackageSpec{})
			tuning := packageAt(reg, "tuning", "tuning")
			node := newNode(nodeName)
			h := newHarness(setup{httpClient: reg.Client()}, newNodeWright(tuning), node)
			_, err := pass(h)
			Expect(err).NotTo(HaveOccurred())
			kept := artifactJob(h, tuning, nodeName, stage, pinnedAt(reg, "tuning", desc))

			pushImage(reg, "tuning")
			stepPast(packageArtifactTagTTL)
			_, err = pass(h)
			Expect(err).NotTo(HaveOccurred())
			outcome, ok := h.r.opts.artifactCache.cached(&tuning)
			Expect(ok).To(BeTrue())
			Expect(outcome.Kind).To(Equal(packageArtifactLegacy))

			expectKept(h, node, tuning, kept.Name)
			expectJob(jobOf(h, kept.Name), kept)
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

		DescribeTable("replace a legacy Job once the package has a cached artifact outcome",
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
				// world is a fresh harness whose cache holds, once the package is pushed, its outcome.
				pushed := false
				world := func() harness {
					h := newHarness(setup{httpClient: reg.Client()}, nw.DeepCopy(), node.DeepCopy(), legacy.DeepCopy())
					if pushed {
						Expect(h.r.packageArtifactOutcome(ctx, nw, &tuning).Kind).To(Equal(kind))
					}
					return h
				}

				// Kept while the cache holds nothing for the package.
				expectKept(world(), node, tuning, legacy.Name)

				desc := push(reg)
				pushed = true
				expectReplaced(world, node, tuning, tuning, legacy.Name)

				h := world()
				Expect(sweep(h)).To(BeTrue())
				if !finished {
					// JobReconcile reaps an invalid Job.
					Expect(h.c.Delete(ctx, legacy)).To(Succeed())
				}

				events, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				if kind == packageArtifactAccepted {
					want := artifactJob(h, tuning, nodeName, stage, pinnedAt(reg, "tuning", desc))
					Expect(jobsOf(h)).To(HaveLen(1))
					expectJob(jobOf(h, want.Name), want)
					Expect(rejectedCondition(h)).To(BeNil())
					return
				}
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(warnings(events, EventsReasonPackageArtifactRejected)).To(HaveLen(1))
				Expect(rejectedCondition(h)).NotTo(BeNil())
			},
			Entry("accepted, unfinished", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushPackage(reg, "tuning", registrytest.PackageSpec{})
			}, packageArtifactAccepted, false),
			Entry("accepted, timed out", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushPackage(reg, "tuning", registrytest.PackageSpec{})
			}, packageArtifactAccepted, true),
			Entry("rejected from a classification error, unfinished", func(reg *registrytest.Registry) ocispec.Descriptor {
				return pushNoPayload(reg, "tuning", version)
			}, packageArtifactRejected, false),
			Entry("rejected from a classification error, timed out", func(reg *registrytest.Registry) ocispec.Descriptor {
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
				// Past the TTL, so each pass resolves the tag again, to the same digest.
				stepPast(packageArtifactTagTTL)
				reg.ResetRequests()
				_, err := pass(h)
				Expect(err).NotTo(HaveOccurred())
				Expect(reg.Requests()).NotTo(BeEmpty())
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
			// requeue is what resolveUninstallDecisions returned.
			requeue  *ctrl.Result
			released bool
			// nodewright is the NodeWright as the pass left it in memory, which outlives one the
			// finalizer released.
			nodewright *wrapper.Skyhook
		}

		// decidedPass does for the one NodeWright what Reconcile does: resolveUninstallDecisions,
		// refreshSkyhookConditions, HandleFinalizer, then RunSkyhookPackages unless the finalizer
		// released it.
		decidedPass := func(h harness) passOutcome {
			GinkgoHelper()
			state := stateOf(h)
			sn := state.skyhooks[0]
			requeue := h.r.resolveUninstallDecisions(ctx, sn)
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
				reg := newRegistry(registrytest.Options{})
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
					pushImage(reg, "tuning")
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

				starts := enabled && (kind == "off" || kind == "legacy" || kind == "supported")
				if starts {
					Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
					want := legacyJob(h, tuning, nodeName, v1alpha1.StageUninstall)
					if kind == "supported" {
						want = artifactJob(h, tuning, nodeName, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", desc))
					}
					Expect(jobsOf(h)).To(HaveLen(1))
					expectJob(jobOf(h, want.Name), want)
				} else {
					Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
					Expect(jobsOf(h)).To(BeEmpty())
				}
				if deletion && !p.released {
					// Waits for the uninstall, which a rejected artifact never finishes.
					condition := conditionOf(p, wrapper.SkyhookConditionDeletionBlocked)
					if kind == "rejected" {
						Expect(condition).To(HaveField("Reason", "PackageArtifactRefused"))
					} else {
						Expect(condition).To(BeNil())
					}
				}

				reasons := warningReasons(p.events)
				unsupportedCondition := conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)
				switch {
				case !enabled:
					Expect(reasons).To(BeEmpty())
					Expect(unsupportedCondition).To(BeNil())
					Expect(conditionOf(p, wrapper.SkyhookConditionPackageArtifactRejected)).To(BeNil())
					Expect(p.requeue).To(BeNil())
					Expect(calls.Load()).To(BeZero())
				case starts:
					Expect(reasons).To(BeEmpty())
					Expect(unsupportedCondition).To(BeNil())
					Expect(p.requeue).To(BeNil())
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
					expectRequeueWithin(p, packageArtifactTagTTL)
				default: // unknown or rejected
					want := map[string]int{EventsReasonUninstallDeferred: 1}
					problem := "could not be inspected (transport)"
					if kind == "rejected" {
						problem = "is rejected (host-files)"
						Expect(conditionOf(p, wrapper.SkyhookConditionPackageArtifactRejected)).To(HaveField("Status", metav1.ConditionTrue))
						if deletion {
							want["DeletionBlocked"] = 1
						}
					}
					Expect(reasons).To(Equal(want))
					Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(ConsistOf(And(
						HavePrefix("Warning UninstallDeferred tuning:1.0.0 "),
						ContainSubstring("[tuning:1.0.0]"),
						ContainSubstring("["+getPackageImage(&tuning)+"]"),
						ContainSubstring(problem),
						ContainSubstring("deferred"),
						ContainSubstring("IMAGE_PULL_SECRET"),
						ContainSubstring("uninstall.enabled to false"),
						ContainSubstring("PACKAGE_ARTIFACTS off"),
					)))
					Expect(unsupportedCondition).To(BeNil())
					expectRequeueWithin(p, packageArtifactFailureBackoff)
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
			Entry("uninstall.enabled false, not inspected, uninstall.apply", "unknown", false, false),
			Entry("uninstall.enabled false, not inspected, deletion", "unknown", false, true),
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
			Entry("uninstall.enabled true, not inspected, uninstall.apply", "unknown", true, false),
			Entry("uninstall.enabled true, not inspected, deletion", "unknown", true, true),
			Entry("uninstall.enabled true, rejected, uninstall.apply", "rejected", true, false),
			Entry("uninstall.enabled true, rejected, deletion", "rejected", true, true),
		)

		DescribeTable("after a restart, with a cold cache, the first pass starts no uninstall an artifact declares unsupported",
			func(deletion bool) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, deletion)
				h := newHarness(setup{httpClient: reg.Client()}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))
				_, cached := h.r.opts.artifactCache.cached(&tuning)
				Expect(cached).To(BeFalse())

				p := decidedPass(h)
				Expect(reg.Requests()).NotTo(BeEmpty())
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(p.released).To(Equal(deletion))
			},
			Entry("under uninstall.apply", false),
			Entry("under deletion", true),
		)

		DescribeTable("through a registry outage, defers the uninstall, then decides it once the registry answers",
			func(deletion bool, declared artifact.Uninstall) {
				reg := newRegistry(registrytest.Options{})
				desc := pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: declared})
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, deletion)
				h := newHarness(setup{httpClient: &http.Client{Transport: failing}}, nw, nodeWith(nodeName, tuning, v1alpha1.StageConfig, v1alpha1.StateComplete))

				p := decidedPass(h)
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(ConsistOf(ContainSubstring("(transport)")))
				expectRequeueWithin(p, packageArtifactFailureBackoff)
				Expect(p.released).To(BeFalse())
				if deletion {
					Expect(storedNodeWright(h).Finalizers).To(ConsistOf(SkyhookFinalizer))
				}

				h.r.opts.artifactCache.httpClient = reg.Client()
				stepPast(packageArtifactFailureBackoff)
				p = decidedPass(h)
				Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(BeEmpty())
				if declared == artifact.UninstallSupported {
					Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
					want := artifactJob(h, tuning, nodeName, v1alpha1.StageUninstall, pinnedAt(reg, "tuning", desc))
					Expect(jobsOf(h)).To(HaveLen(1))
					expectJob(jobOf(h, want.Name), want)
					Expect(p.released).To(BeFalse())
					return
				}
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageConfig))
				Expect(jobsOf(h)).To(BeEmpty())
				Expect(warnings(p.events, EventsReasonUninstallUnsupported)).To(HaveLen(1))
				Expect(p.released).To(Equal(deletion))
			},
			Entry("under uninstall.apply, an artifact declaring uninstall supported", false, artifact.UninstallSupported),
			Entry("under uninstall.apply, an artifact declaring uninstall unsupported", false, artifact.UninstallUnsupported),
			Entry("under deletion, an artifact declaring uninstall supported", true, artifact.UninstallSupported),
			Entry("under deletion, an artifact declaring uninstall unsupported", true, artifact.UninstallUnsupported),
		)

		Describe("a tag re-pushed from an artifact declaring uninstall supported to one declaring it unsupported, mid-cycle", func() {
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

			// midCycle returns that world, with the cache holding the tag at the supported artifact,
			// which the tag is then re-pushed from.
			midCycle := func(deletion bool) (harness, *registrytest.Registry, v1alpha1.Package, ocispec.Descriptor, batchv1.Job) {
				GinkgoHelper()
				reg := newRegistry(registrytest.Options{})
				supported := pushPackage(reg, "tuning", registrytest.PackageSpec{})
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
				Expect(h.r.packageArtifactOutcome(ctx, nw, &tuning).Package.Uninstall).To(Equal(artifact.UninstallSupported))
				drainEvents(h)
				pushPackage(reg, "tuning", registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
				stepPast(packageArtifactTagTTL)
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
				expectRequeueWithin(p, packageArtifactTagTTL)
			})

			It("under deletion, blocks it, and uninstall.enabled=false cancels the pending node's cycle and releases the finalizer", func() {
				h, _, tuning, _, _ := midCycle(true)

				p := decidedPass(h)
				Expect(p.released).To(BeFalse())
				Expect(meta.FindStatusCondition(storedNodeWright(h).Status.Conditions, wrapper.SkyhookConditionDeletionBlocked)).To(
					HaveField("Reason", "PackageArtifactRefused"))
				Expect(jobsOn(h, pending)).To(BeEmpty())
				Expect(stageOn(h, complete, tuning)).To(Equal(v1alpha1.StagePostInterrupt))

				current := storedNodeWright(h)
				released := current.Spec.Packages[tuning.Name]
				released.Uninstall = &v1alpha1.Uninstall{}
				current.Spec.Packages[tuning.Name] = released
				Expect(h.c.Update(ctx, current)).To(Succeed())

				// Driven directly: in a Reconcile pass the finalizer runs first, and already releases
				// a package whose uninstall.enabled is false.
				state := stateOf(h)
				_, err := h.r.RunSkyhookPackages(ctx, state, NewNodePicker(GinkgoLogr, h.r.opts.GetRuntimeRequiredTolerations()), state.skyhooks[0])
				Expect(err).NotTo(HaveOccurred())
				Expect(stageOn(h, pending, tuning)).To(Equal(v1alpha1.StageApply))

				state = stateOf(h)
				handled, err := h.r.HandleFinalizer(ctx, state.skyhooks[0], state)
				Expect(err).NotTo(HaveOccurred())
				Expect(handled).To(BeTrue())
			})

			DescribeTable("returns the decision to spec",
				// back moves the world on from the unsupported artifact and returns the harness to
				// pass with, and whether the uninstall Jobs it builds are artifact Jobs.
				func(back func(h harness, reg *registrytest.Registry) (harness, bool)) {
					h, reg, tuning, supported, _ := midCycle(false)
					p := decidedPass(h)
					Expect(conditionOf(p, wrapper.SkyhookConditionUninstallUnsupported)).NotTo(BeNil())

					h, artifactJobs := back(h, reg)
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
				Entry("once the tag is re-pushed to the supported artifact", func(h harness, reg *registrytest.Registry) (harness, bool) {
					pushPackage(reg, "tuning", registrytest.PackageSpec{})
					stepPast(packageArtifactTagTTL)
					return h, true
				}),
				Entry("once the tag is re-pushed as a container image", func(h harness, reg *registrytest.Registry) (harness, bool) {
					pushImage(reg, "tuning")
					stepPast(packageArtifactTagTTL)
					return h, false
				}),
				Entry("once the option is off", func(h harness, reg *registrytest.Registry) (harness, bool) {
					return rebuilt(h, setup{off: true, httpClient: reg.Client()}), false
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
		})

		DescribeTable("a deferred package at StageUninstall with no Job",
			// built reports whether the pass builds the fallback uninstall Job.
			func(httpClient func(reg *registrytest.Registry) *http.Client, spec registrytest.PackageSpec, warning string, built bool) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "tuning", spec)
				tuning, nw := uninstalling(packageAt(reg, "tuning", "tuning"), true, false)
				h := newHarness(setup{httpClient: httpClient(reg)}, nw, nodeWith(nodeName, tuning, v1alpha1.StageUninstall, v1alpha1.StateInProgress))

				p := decidedPass(h)
				Expect(p.nodewright.UninstallDecision(tuning.Name)).To(Equal(wrapper.UninstallDecisionDeferred))
				Expect(warnings(p.events, EventsReasonUninstallDeferred)).To(BeEmpty())
				Expect(warnings(p.events, warning)).To(HaveLen(1))
				Expect(stageOn(h, nodeName, tuning)).To(Equal(v1alpha1.StageUninstall))
				if !built {
					Expect(jobsOf(h)).To(BeEmpty())
					return
				}
				want := legacyJob(h, tuning, nodeName, v1alpha1.StageUninstall)
				Expect(jobsOf(h)).To(HaveLen(1))
				expectJob(jobOf(h, want.Name), want)
			},
			Entry("builds the legacy fallback Job when its artifact could not be inspected",
				func(*registrytest.Registry) *http.Client { return &http.Client{Transport: failing} },
				registrytest.PackageSpec{}, EventsReasonPackageArtifactInspectionFailed, true),
			Entry("builds none when its artifact is rejected",
				func(reg *registrytest.Registry) *http.Client { return reg.Client() },
				hostFiles, EventsReasonPackageArtifactRejected, false),
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

			It("is removed with the option off, which starts the uninstall", func() {
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
})

// roundTripperFunc is an http.RoundTripper made of a function.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
