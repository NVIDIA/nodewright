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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("artifact job builders", func() {
	const (
		nodeName     = "worker-7"
		pullSecret   = "regcred"
		agentImage   = "ghcr.io/nvidia/skyhook/agent:1.2.3"
		packageImage = "ghcr.io/nvidia/skyhook-packages/tuning"
		authDir      = "/var/run/secrets/nodewright/registry-auth"
	)

	digestOf := func(hex string) string { return "sha256:" + strings.Repeat(hex, 64) }
	pinned := packageImage + "@" + digestOf("a")

	var (
		opts    SkyhookOperatorOptions
		skyhook *wrapper.Skyhook
		pkg     *v1alpha1.Package
	)

	BeforeEach(func() {
		opts = SkyhookOperatorOptions{
			Namespace:            "skyhook",
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           agentImage,
			PauseImage:           "registry.k8s.io/pause:3.10",
			JobOperatorOptions: JobOperatorOptions{
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
		// Every field that reaches the pod, so the deep equality below covers it.
		pkg = &v1alpha1.Package{
			PackageRef:       v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"},
			Image:            packageImage,
			Env:              []corev1.EnvVar{{Name: "TUNING_PROFILE", Value: "throughput"}},
			ConfigMap:        map[string]string{"profile.conf": "throughput", "apply.sh": "echo apply"},
			GracefulShutdown: &metav1.Duration{Duration: 45 * time.Second},
			Resources: &v1alpha1.ResourceRequirements{
				CPURequest:    resource.MustParse("100m"),
				CPULimit:      resource.MustParse("200m"),
				MemoryRequest: resource.MustParse("64Mi"),
				MemoryLimit:   resource.MustParse("128Mi"),
			},
		}
		skyhook = wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-init", UID: "abc", Generation: 4},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"tuning": *pkg}},
		})
	})

	Describe("createArtifactJobFromPackage", func() {
		DescribeTable("differs from the createJobFromPackage Job only in its init containers, done container and registry credentials",
			func(imagePullSecret string) {
				opts.ImagePullSecret = imagePullSecret
				legacy := createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
				job := createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned)

				// The init-copy container is gone; the stage and check containers gain the pinned
				// reference and, with a pull secret, its file and read-only mount.
				legacyInits := legacy.Spec.Template.Spec.InitContainers
				Expect(legacyInits).To(HaveLen(3))
				Expect(legacyInits[0].Name).To(Equal("tuning-init"))
				inits := job.Spec.Template.Spec.InitContainers
				Expect(inits).To(HaveLen(2))
				Expect(inits[0].Name).To(Equal("tuning-apply"))
				Expect(inits[1].Name).To(Equal("tuning-applycheck"))
				for i, container := range inits {
					want := legacyInits[i+1].DeepCopy()
					want.Env = append(want.Env, corev1.EnvVar{Name: "NODEWRIGHT_PACKAGE_REF", Value: pinned})
					if imagePullSecret != "" {
						want.Env = append(want.Env, corev1.EnvVar{Name: "NODEWRIGHT_REGISTRY_AUTH_FILE", Value: authDir + "/.dockerconfigjson"})
						want.VolumeMounts = append(want.VolumeMounts, corev1.VolumeMount{Name: "nodewright-registry-auth", MountPath: authDir, ReadOnly: true})
					}
					Expect(container).To(Equal(*want), container.Name)
				}

				wantVolumes := slices.Clone(legacy.Spec.Template.Spec.Volumes)
				if imagePullSecret != "" {
					wantVolumes = append(wantVolumes, corev1.Volume{
						Name: "nodewright-registry-auth",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: pullSecret,
							Items:      []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: ".dockerconfigjson"}},
							Optional:   ptr(true),
						}},
					})
				}
				Expect(job.Spec.Template.Spec.Volumes).To(Equal(wantVolumes))

				// The done container runs the agent with no command and mounts nothing.
				done := job.Spec.Template.Spec.Containers
				Expect(done).To(HaveLen(1))
				Expect(done[0].Command).To(BeNil())
				Expect(done[0]).To(Equal(corev1.Container{
					Name:      "done",
					Image:     agentImage,
					Args:      []string{"--version"},
					Resources: legacy.Spec.Template.Spec.Containers[0].Resources,
				}))

				rest := job.DeepCopy()
				rest.Spec.Template.Spec.InitContainers = legacyInits
				rest.Spec.Template.Spec.Containers = legacy.Spec.Template.Spec.Containers
				rest.Spec.Template.Spec.Volumes = legacy.Spec.Template.Spec.Volumes
				Expect(rest).To(Equal(legacy))
			},
			Entry("without an image pull secret", ""),
			Entry("with an image pull secret", pullSecret),
		)

		It("gives each agent container its own env and mounts, and never writes into the package env", func() {
			opts.ImagePullSecret = pullSecret
			// With spare capacity, createPodFromPackage appends the agent variables into the array
			// of the package env itself, so an append in place would land there too.
			pkg.Env = append(make([]corev1.EnvVar, 0, 16), pkg.Env...)

			job := createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned)
			stage, check := job.Spec.Template.Spec.InitContainers[0], job.Spec.Template.Spec.InitContainers[1]
			Expect(&stage.Env[0]).NotTo(BeIdenticalTo(&check.Env[0]))
			Expect(&stage.VolumeMounts[0]).NotTo(BeIdenticalTo(&check.VolumeMounts[0]))
			Expect(pkg.Env[:cap(pkg.Env)]).NotTo(ContainElement(HaveField("Name", "NODEWRIGHT_PACKAGE_REF")))
		})
	})

	It("leaves createJobFromPackage as it is with PackageArtifacts on", func() {
		off := createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
		opts.PackageArtifacts = true
		Expect(createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)).To(Equal(off))
	})

	It("runs the agent as the done container of every interrupt Job while PackageArtifacts is on, and nothing else changes", func() {
		// With a pull secret and no cache: the option alone decides, and the artifact
		// variables and credentials never reach an interrupt Job.
		opts.ImagePullSecret = pullSecret
		for _, override := range []string{"", "ghcr.io/nvidia/nodewright/agent:7.0.0"} {
			pkg.AgentImageOverride = override
			wantAgent := agentImage
			if override != "" {
				wantAgent = override
			}
			for _, stage := range []v1alpha1.Stage{v1alpha1.StageInterrupt, v1alpha1.StageUninstallInterrupt} {
				for _, interruptType := range []v1alpha1.InterruptType{v1alpha1.REBOOT, v1alpha1.SERVICE, v1alpha1.RESTART_ALL_SERVICES, v1alpha1.NOOP} {
					what := fmt.Sprintf("%s at %s with override %q", interruptType, stage, override)
					interrupt := &v1alpha1.Interrupt{Type: interruptType}
					opts.PackageArtifacts = false
					off := createInterruptJobFromPackage(opts, interrupt, "args", pkg, skyhook, nodeName, stage)
					opts.PackageArtifacts = true
					on := createInterruptJobFromPackage(opts, interrupt, "args", pkg, skyhook, nodeName, stage)

					offDone := off.Spec.Template.Spec.Containers
					Expect(offDone).To(HaveLen(1), what)
					Expect(offDone[0].Name).To(Equal("done"), what)
					Expect(offDone[0].Image).To(Equal(packageImage+":1.0.0"), what)
					Expect(offDone[0].Command).To(Equal([]string{"/bin/sh", "-c", "exit 0"}), what)
					Expect(offDone[0].Args).To(BeNil(), what)

					onDone := on.Spec.Template.Spec.Containers
					Expect(onDone).To(HaveLen(1), what)
					Expect(onDone[0].Command).To(BeNil(), what)
					Expect(onDone[0]).To(Equal(corev1.Container{
						Name:      "done",
						Image:     wantAgent,
						Args:      []string{"--version"},
						Resources: offDone[0].Resources,
					}), what)
					for _, c := range slices.Concat(on.Spec.Template.Spec.InitContainers, on.Spec.Template.Spec.Containers) {
						Expect(c.Image).NotTo(HavePrefix(packageImage), what)
						Expect(slices.Concat(c.Command, c.Args)).NotTo(ContainElement(ContainSubstring("/bin/sh")), what)
						Expect(c.Env).NotTo(ContainElement(HaveField("Name", HavePrefix("NODEWRIGHT_"))), what)
					}

					rest := on.DeepCopy()
					rest.Spec.Template.Spec.Containers = off.Spec.Template.Spec.Containers
					Expect(rest).To(Equal(off), what)
				}
			}
		}
	})

	Describe("jobMatchesPackage", func() {
		const stage = v1alpha1.StageConfig

		var (
			reg   *registrytest.Registry
			cache *packageArtifactCache
			// jobPinned is a digest no registry holds, so it differs from every outcome.
			jobPinned string
		)

		nodewright := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "gpu-init"}}

		BeforeEach(func() {
			reg = registrytest.New(registrytest.Options{})
			DeferCleanup(reg.Close)
			cache = newPackageArtifactCache(fake.NewClientBuilder().Build(), &events.FakeRecorder{}, opts.Namespace, "")
			cache.httpClient = reg.Client()
			pkg = &v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"},
				Image:      reg.Host() + "/tuning",
			}
			skyhook = wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-init", UID: "abc", Generation: 4},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"tuning": *pkg}},
			})
			jobPinned = reg.Host() + "/tuning@" + digestOf("a")
		})

		// variants are the options a compare is asked under: the option off with the cache, on
		// with no cache, and on with the cache.
		variants := func() map[string]SkyhookOperatorOptions {
			off, noCache, on := opts, opts, opts
			off.artifactCache = cache
			noCache.PackageArtifacts = true
			on.PackageArtifacts = true
			on.artifactCache = cache
			return map[string]SkyhookOperatorOptions{"option off": off, "no cache": noCache, "option on": on}
		}

		// expectMatch asserts the answer of every variant, and that none asks the registry.
		expectMatch := func(job *batchv1.Job, current *v1alpha1.Package, want bool) {
			reg.ResetRequests()
			for name, o := range variants() {
				Expect(jobMatchesPackage(o, current, *job, skyhook, stage)).To(Equal(want), name)
			}
			Expect(reg.Requests()).To(BeEmpty())
		}

		lookUp := func(current *v1alpha1.Package) packageArtifactOutcome {
			return cache.lookup(ctx, nodewright, current)
		}

		pushPackage := func(tag string, spec registrytest.PackageSpec) ocispec.Descriptor {
			spec.Uninstall = artifact.UninstallSupported
			spec.Config = json.RawMessage(`{}`)
			spec.Payload = []registrytest.Layer{{Gzip: []byte("payload")}}
			desc, _, err := registrytest.PushPackage(ctx, reg.Repository("tuning"), tag, spec)
			Expect(err).NotTo(HaveOccurred())
			return desc
		}

		// The seeds store an outcome for current and assert what the cache then answers, so a
		// seed that did not take fails here rather than passing the compare for the wrong reason.
		seedMiss := func(current *v1alpha1.Package) {
			_, ok := cache.cached(current)
			Expect(ok).To(BeFalse())
		}
		seedUnknown := func(current *v1alpha1.Package) {
			// Nothing is pushed, so the inspection fails and only the failure is stored.
			Expect(lookUp(current).Kind).To(Equal(packageArtifactUnknown))
			seedMiss(current)
		}
		seedLegacy := func(current *v1alpha1.Package) {
			dir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(dir, "hello"), []byte("hello"), 0o644)).To(Succeed())
			_, err := registrytest.PushImage(ctx, reg.Repository("tuning"), current.Version, dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(lookUp(current).Kind).To(Equal(packageArtifactLegacy))
			outcome, ok := cache.cached(current)
			Expect(ok).To(BeTrue())
			Expect(outcome.Kind).To(Equal(packageArtifactLegacy))
		}
		seedAccepted := func(current *v1alpha1.Package) {
			desc := pushPackage(current.Version, registrytest.PackageSpec{})
			Expect(lookUp(current).Kind).To(Equal(packageArtifactAccepted))
			outcome, ok := cache.cached(current)
			Expect(ok).To(BeTrue())
			Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
			Expect(outcome.Pinned).To(Equal(reg.Host() + "/tuning@" + desc.Digest.String()))
			Expect(outcome.Pinned).NotTo(Equal(jobPinned))
		}
		seedNoPayload := func(current *v1alpha1.Package) {
			metadata, err := registrytest.PushBlob(ctx, reg.Repository("tuning"), artifact.MediaTypeMetadata, []byte(`{"uninstall":"supported","config":{}}`))
			Expect(err).NotTo(HaveOccurred())
			manifest, err := json.Marshal(ocispec.Manifest{
				Versioned:    specs.Versioned{SchemaVersion: 2},
				MediaType:    ocispec.MediaTypeImageManifest,
				ArtifactType: artifact.ArtifactTypePackage,
				Config:       metadata,
				Layers:       []ocispec.Descriptor{},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = registrytest.PushManifest(ctx, reg.Repository("tuning"), ocispec.MediaTypeImageManifest, manifest, current.Version)
			Expect(err).NotTo(HaveOccurred())
			Expect(lookUp(current)).To(Equal(packageArtifactOutcome{Kind: packageArtifactRejected, Reason: "invalid-artifact: no-payload"}))
			outcome, ok := cache.cached(current)
			Expect(ok).To(BeTrue())
			Expect(outcome.Kind).To(Equal(packageArtifactRejected))
		}
		seedHostFiles := func(current *v1alpha1.Package) {
			pushPackage(current.Version, registrytest.PackageSpec{HostFiles: []registrytest.HostFilesLayer{
				{Layer: registrytest.Layer{Gzip: []byte("host files")}, Destination: "/etc/nodewright"},
			}})
			Expect(lookUp(current)).To(Equal(packageArtifactOutcome{Kind: packageArtifactRejected, Reason: "host-files"}))
			outcome, ok := cache.cached(current)
			Expect(ok).To(BeTrue())
			Expect(outcome.Kind).To(Equal(packageArtifactRejected))
		}

		artifactJob := func(built *v1alpha1.Package, pinned string) *batchv1.Job {
			o := opts
			o.PackageArtifacts = true
			o.ImagePullSecret = "regcred"
			return createArtifactJobFromPackage(o, built, skyhook, nodeName, stage, pinned)
		}

		DescribeTable("keep an artifact Job that matches, whatever the option and the cache answer",
			func(seed func(*v1alpha1.Package)) {
				job := artifactJob(pkg, jobPinned)
				seed(pkg)
				expectMatch(job, pkg, true)
			},
			Entry("a miss", seedMiss),
			Entry("unknown", seedUnknown),
			Entry("legacy", seedLegacy),
			Entry("accepted at another digest", seedAccepted),
			Entry("rejected from a classification error", seedNoPayload),
			Entry("rejected for host files", seedHostFiles),
		)

		It("keep an artifact Job built with another image pull secret, or none", func() {
			job := artifactJob(pkg, jobPinned)
			o := opts
			o.ImagePullSecret = ""
			Expect(jobMatchesPackage(o, pkg, *job, skyhook, stage)).To(BeTrue())
			o.ImagePullSecret = "rotated"
			Expect(jobMatchesPackage(o, pkg, *job, skyhook, stage)).To(BeTrue())
		})

		It("treat setting containerSHA to another digest than the Job pins as stale", func() {
			desc := pushPackage(pkg.Version, registrytest.PackageSpec{})
			outcome := lookUp(pkg)
			Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
			job := artifactJob(pkg, outcome.Pinned)
			expectMatch(job, pkg, true)

			current := pkg.DeepCopy()
			current.ContainerSHA = desc.Digest.String()
			expectMatch(job, current, true)
			current.ContainerSHA = digestOf("b")
			expectMatch(job, current, false)
		})

		It("treat changing containerSHA to another digest as stale", func() {
			built := pkg.DeepCopy()
			built.ContainerSHA = digestOf("a")
			job := artifactJob(built, jobPinned)
			expectMatch(job, built, true)

			current := built.DeepCopy()
			current.ContainerSHA = digestOf("b")
			expectMatch(job, current, false)
		})

		It("keep an artifact Job whose containerSHA was removed, even when the tag now resolves elsewhere", func() {
			built := pkg.DeepCopy()
			built.ContainerSHA = digestOf("a")
			job := artifactJob(built, jobPinned)
			seedAccepted(pkg)
			expectMatch(job, pkg, true)
		})

		It("treat a version change as stale", func() {
			job := artifactJob(pkg, jobPinned)
			current := pkg.DeepCopy()
			current.Version = "2.0.0"
			seedAccepted(current)
			expectMatch(job, current, false)
		})

		It("treat an image change as stale through the reference name alone", func() {
			job := artifactJob(pkg, jobPinned)
			current := pkg.DeepCopy()
			current.Image = reg.Host() + "/tuning-relocated"
			// The package label and the agent images are unchanged.
			Expect(job.Labels[v1alpha1.METADATA_PREFIX+"/package"]).To(Equal("tuning-1.0.0"))
			expectMatch(job, current, false)
		})

		It("treat a pinned reference the package could not have built as stale", func() {
			for _, ref := range []string{"", "not a reference", pkg.Image + ":1.0.0", reg.Host() + "/other@" + digestOf("a")} {
				job := artifactJob(pkg, jobPinned)
				for i := range job.Spec.Template.Spec.InitContainers {
					env := job.Spec.Template.Spec.InitContainers[i].Env
					env[slices.IndexFunc(env, func(e corev1.EnvVar) bool { return e.Name == "NODEWRIGHT_PACKAGE_REF" })].Value = ref
				}
				expectMatch(job, pkg, false)
			}
		})

		It("compare a legacy Job whose package env names NODEWRIGHT_PACKAGE_REF as a legacy Job", func() {
			pkg.Env = []corev1.EnvVar{{Name: "NODEWRIGHT_PACKAGE_REF", Value: "set by the package"}}
			job := createJobFromPackage(opts, pkg, skyhook, nodeName, stage)
			expectMatch(job, pkg, true)

			edited := pkg.DeepCopy()
			edited.Env[0].Value = "edited"
			expectMatch(job, edited, false)
		})

		It("compare the pinned reference the operator set on an artifact Job whose package env names NODEWRIGHT_PACKAGE_REF", func() {
			built := pkg.DeepCopy()
			built.Env = []corev1.EnvVar{{Name: "NODEWRIGHT_PACKAGE_REF", Value: "set by the package"}}
			built.ContainerSHA = digestOf("a")
			job := artifactJob(built, jobPinned)
			expectMatch(job, built, true)

			current := built.DeepCopy()
			current.ContainerSHA = digestOf("b")
			expectMatch(job, current, false)
		})

		DescribeTable("turn a legacy Job stale only while the option is on and the cache answers accepted or rejected",
			func(seed func(*v1alpha1.Package), stale bool) {
				o := opts
				o.PackageArtifacts = true
				job := createJobFromPackage(o, pkg, skyhook, nodeName, stage)
				seed(pkg)
				// Edited so the legacy compare itself mismatches, with the same reference.
				edited := pkg.DeepCopy()
				edited.Env = []corev1.EnvVar{{Name: "EDITED", Value: "true"}}

				reg.ResetRequests()
				for name, variant := range variants() {
					want := !stale || !variant.PackageArtifacts || variant.artifactCache == nil
					Expect(jobMatchesPackage(variant, pkg, *job, skyhook, stage)).To(Equal(want), name)
					Expect(jobMatchesPackage(variant, edited, *job, skyhook, stage)).To(BeFalse(), name)
				}
				Expect(reg.Requests()).To(BeEmpty())
			},
			Entry("accepted", seedAccepted, true),
			Entry("rejected from a classification error", seedNoPayload, true),
			Entry("rejected for host files", seedHostFiles, true),
			Entry("legacy", seedLegacy, false),
			Entry("unknown", seedUnknown, false),
			Entry("a miss", seedMiss, false),
		)

		It("compare interrupt Jobs as before, whatever the option and the cache answer", func() {
			seedAccepted(pkg)
			bumped := pkg.DeepCopy()
			bumped.Version = "2.0.0"
			for _, built := range variants() {
				for _, interruptStage := range []v1alpha1.Stage{v1alpha1.StageInterrupt, v1alpha1.StageUninstallInterrupt} {
					job := createInterruptJobFromPackage(built, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, nodeName, interruptStage)
					reg.ResetRequests()
					for name, o := range variants() {
						Expect(jobMatchesPackage(o, pkg, *job, skyhook, interruptStage)).To(BeTrue(), name)
						Expect(jobMatchesPackage(o, bumped, *job, skyhook, interruptStage)).To(BeFalse(), name)
					}
					Expect(reg.Requests()).To(BeEmpty())
				}
			}
		})
	})

	Describe("API admission (envtest)", func() {
		// As the admission spec of job_builder_test.go, for the Job shapes PackageArtifacts adds:
		// the Secret volume and its mounts, and an agent done container with no command.
		It("creates an artifact stage Job with an image pull secret and an interrupt Job with the option on", func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: opts.Namespace}}
			if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).ToNot(HaveOccurred())
			}

			opts.ImagePullSecret = pullSecret
			opts.PackageArtifacts = true
			const node = "worker-artifact-admission"
			built := []*batchv1.Job{
				createArtifactJobFromPackage(opts, pkg, skyhook, node, v1alpha1.StageApply, pinned),
				createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, node, v1alpha1.StageInterrupt),
			}
			for _, job := range built {
				Expect(k8sClient.Create(ctx, job)).To(Succeed(), "apiserver must admit job %s", job.Name)
				// Background: envtest runs no garbage collector, and the default Orphan policy
				// leaves a finalizer only the collector removes.
				DeferCleanup(func(j *batchv1.Job) {
					_ = k8sClient.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground))
				}, job)
			}
		})
	})
})
