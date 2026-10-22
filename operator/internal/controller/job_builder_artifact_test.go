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
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("artifact and preflight job builders", func() {
	const (
		nodeName       = "worker-7"
		agentImage     = "ghcr.io/nvidia/skyhook/agent:1.2.3"
		packageImage   = "ghcr.io/nvidia/skyhook-packages/tuning"
		serviceAccount = "nodewright-package"
		authDir        = "/var/run/secrets/nodewright/registry-auth"
	)

	digestOf := func(hex string) string { return "sha256:" + strings.Repeat(hex, 64) }
	pinned := packageImage + "@" + digestOf("a")
	reference := packageImage + ":1.0.0"

	// What addRegistryAuth gives a pod for the pull secrets regcred and mirror, in that order.
	pullSecrets := []string{"regcred", "mirror"}
	authVolumes := []corev1.Volume{
		{Name: "nodewright-registry-auth-0", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: "regcred",
			Items:      []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: ".dockerconfigjson"}},
			Optional:   ptr(true),
		}}},
		{Name: "nodewright-registry-auth-1", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: "mirror",
			Items:      []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: ".dockerconfigjson"}},
			Optional:   ptr(true),
		}}},
	}
	authMounts := []corev1.VolumeMount{
		{Name: "nodewright-registry-auth-0", MountPath: authDir + "/0", ReadOnly: true},
		{Name: "nodewright-registry-auth-1", MountPath: authDir + "/1", ReadOnly: true},
	}
	authFiles := authDir + "/0/.dockerconfigjson:" + authDir + "/1/.dockerconfigjson"

	var (
		opts    SkyhookOperatorOptions
		skyhook *wrapper.Skyhook
		pkg     *v1alpha1.Package
	)

	BeforeEach(func() {
		opts = SkyhookOperatorOptions{
			Namespace:             "skyhook",
			PackageServiceAccount: serviceAccount,
			CopyDirRoot:           "/var/lib/skyhook",
			AgentLogRoot:          "/var/log/skyhook",
			RuntimeRequiredTaint:  "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:            agentImage,
			PauseImage:            "registry.k8s.io/pause:3.10",
			JobOperatorOptions: JobOperatorOptions{
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
		// Every field that reaches the pod, so the deep equalities below cover it.
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
		DescribeTable("differs from the createJobFromPackage Job only in its init containers, done container and registry auth",
			func(secrets []string, volumes []corev1.Volume, mounts []corev1.VolumeMount, files string) {
				legacy := createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
				job := createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned, secrets)

				// The init-copy container is gone; the stage and check containers gain the pinned
				// reference, the auth file paths and a read-only mount per pull secret.
				legacyInits := legacy.Spec.Template.Spec.InitContainers
				Expect(legacyInits).To(HaveLen(3))
				Expect(legacyInits[0].Name).To(Equal("tuning-init"))
				inits := job.Spec.Template.Spec.InitContainers
				Expect(inits).To(HaveLen(2))
				Expect(inits[0].Name).To(Equal("tuning-apply"))
				Expect(inits[1].Name).To(Equal("tuning-applycheck"))
				for i, container := range inits {
					want := legacyInits[i+1].DeepCopy()
					want.Env = append(want.Env,
						corev1.EnvVar{Name: "NODEWRIGHT_PACKAGE_REF", Value: pinned},
						corev1.EnvVar{Name: "NODEWRIGHT_REGISTRY_AUTH_FILES", Value: files})
					want.VolumeMounts = append(want.VolumeMounts, mounts...)
					Expect(container).To(Equal(*want), container.Name)
				}

				Expect(job.Spec.Template.Spec.Volumes).To(Equal(append(slices.Clone(legacy.Spec.Template.Spec.Volumes), volumes...)))

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
			Entry("with no pull secrets", nil, nil, nil, ""),
			Entry("with two pull secrets, in order", pullSecrets, authVolumes, authMounts, authFiles),
		)

		It("gives each agent container its own env and mounts, and never writes into the package env", func() {
			// With spare capacity, createPodFromPackage appends the agent variables into the array
			// of the package env itself, so an append in place would land there too.
			pkg.Env = append(make([]corev1.EnvVar, 0, 16), pkg.Env...)

			job := createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned, pullSecrets)
			stage, check := job.Spec.Template.Spec.InitContainers[0], job.Spec.Template.Spec.InitContainers[1]
			Expect(&stage.Env[0]).NotTo(BeIdenticalTo(&check.Env[0]))
			Expect(&stage.VolumeMounts[0]).NotTo(BeIdenticalTo(&check.VolumeMounts[0]))
			Expect(pkg.Env[:cap(pkg.Env)]).NotTo(ContainElement(HaveField("Name", HavePrefix("NODEWRIGHT_"))))
		})
	})

	DescribeTable("createStageJob builds the artifact Job from an accepted artifact, and the createJobFromPackage Job from anything else",
		func(result *v1alpha1.PreflightResult, artifact bool) {
			job := createStageJob(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig, result, pullSecrets)
			if artifact {
				Expect(job).To(Equal(createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig, pinned, pullSecrets)))
				return
			}
			Expect(job).To(Equal(createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig)))
		},
		Entry("an accepted artifact, pinned to the recorded digest",
			&v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}, true),
		Entry("an accepted artifact declaring uninstall unsupported",
			&v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "unsupported"}, true),
		Entry("a container image", &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}, false),
		Entry("a reference that could not be inspected", &v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "unauthorized"}, false),
		Entry("a rejected artifact", &v1alpha1.PreflightResult{Kind: "rejected", Reason: "host-files"}, false),
		Entry("no result, as for a package installed with the option off", nil, false),
	)

	Describe("createPreflightJobFromPackage", func() {
		It("runs one agent container that inspects the reference with the pull secrets and reports through its termination message", func() {
			legacy := createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StagePreflight)
			job := createPreflightJobFromPackage(opts, pkg, skyhook, nodeName, reference, pullSecrets)
			spec := job.Spec.Template.Spec

			legacyInits := legacy.Spec.Template.Spec.InitContainers
			Expect(spec.InitContainers).To(Equal([]corev1.Container{{
				Name:            "tuning-preflight",
				Image:           agentImage,
				ImagePullPolicy: corev1.PullAlways,
				Args:            []string{"preflight", "/root"},
				Env: []corev1.EnvVar{
					{Name: "TUNING_PROFILE", Value: "throughput"},
					{Name: "NODEWRIGHT_PREFLIGHT_REF", Value: reference},
					{Name: "NODEWRIGHT_REGISTRY_AUTH_FILES", Value: authFiles},
				},
				SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
				VolumeMounts: append([]corev1.VolumeMount{{
					Name: "root-mount", MountPath: "/root", MountPropagation: ptr(corev1.MountPropagationHostToContainer),
				}}, authMounts...),
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				Resources:                legacyInits[1].Resources,
			}}))

			// Only the host root, of every volume a stage pod mounts, and the pull secrets.
			Expect(spec.Volumes).To(Equal(append([]corev1.Volume{legacy.Spec.Template.Spec.Volumes[0]}, authVolumes...)))
			Expect(spec.Volumes[0].Name).To(Equal("root-mount"))

			Expect(spec.Containers).To(Equal([]corev1.Container{{
				Name:      "done",
				Image:     agentImage,
				Args:      []string{"--version"},
				Resources: legacy.Spec.Template.Spec.Containers[0].Resources,
			}}))
			for _, c := range slices.Concat(spec.InitContainers, spec.Containers) {
				Expect(c.Image).NotTo(HavePrefix(packageImage))
			}

			Expect(job.Labels).To(HaveKeyWithValue(v1alpha1.METADATA_PREFIX+"/stage", "preflight"))
			rest := job.DeepCopy()
			rest.Spec.Template.Spec.InitContainers = legacyInits
			rest.Spec.Template.Spec.Containers = legacy.Spec.Template.Spec.Containers
			rest.Spec.Template.Spec.Volumes = legacy.Spec.Template.Spec.Volumes
			Expect(rest).To(Equal(legacy))
		})

		It("runs the package's agent image override", func() {
			pkg.AgentImageOverride = "ghcr.io/nvidia/nodewright/agent:7.0.0"
			spec := createPreflightJobFromPackage(opts, pkg, skyhook, nodeName, reference, nil).Spec.Template.Spec
			Expect(spec.InitContainers[0].Image).To(Equal(pkg.AgentImageOverride))
			Expect(spec.Containers[0].Image).To(Equal(pkg.AgentImageOverride))
		})

		It("gives no auth files and mounts no Secret without pull secrets", func() {
			spec := createPreflightJobFromPackage(opts, pkg, skyhook, nodeName, reference, nil).Spec.Template.Spec
			Expect(spec.InitContainers[0].Env).To(ContainElement(corev1.EnvVar{Name: "NODEWRIGHT_REGISTRY_AUTH_FILES", Value: ""}))
			Expect(spec.InitContainers[0].VolumeMounts).To(HaveLen(1))
			Expect(spec.Volumes).To(HaveLen(1))
		})

		It("never writes into the package env", func() {
			pkg.Env = append(make([]corev1.EnvVar, 0, 16), pkg.Env...)
			createPreflightJobFromPackage(opts, pkg, skyhook, nodeName, reference, pullSecrets)
			Expect(pkg.Env[:cap(pkg.Env)]).NotTo(ContainElement(HaveField("Name", HavePrefix("NODEWRIGHT_"))))
		})
	})

	It("runs every package, preflight and interrupt pod as the package ServiceAccount, with no token and no imagePullSecrets", func() {
		jobs := map[string]*batchv1.Job{
			"legacy":    createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply),
			"artifact":  createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned, pullSecrets),
			"preflight": createPreflightJobFromPackage(opts, pkg, skyhook, nodeName, reference, pullSecrets),
			"interrupt": createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, nodeName, v1alpha1.StageInterrupt, nil),
		}
		for name, job := range jobs {
			spec := job.Spec.Template.Spec
			Expect(spec.ServiceAccountName).To(Equal(serviceAccount), name)
			Expect(spec.AutomountServiceAccountToken).To(HaveValue(BeFalse()), name)
			Expect(spec.ImagePullSecrets).To(BeEmpty(), name)
		}
	})

	It("gives no registry auth to a stage Job built for anything but an accepted artifact", func() {
		job := createStageJob(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}, pullSecrets)
		for _, c := range job.Spec.Template.Spec.InitContainers {
			Expect(c.Env).NotTo(ContainElement(HaveField("Name", HavePrefix("NODEWRIGHT_"))), c.Name)
		}
		Expect(job.Spec.Template.Spec.Volumes).NotTo(ContainElement(HaveField("Name", HavePrefix("nodewright-registry-auth"))))
	})

	It("leaves createJobFromPackage as it is with PackageArtifacts on", func() {
		off := createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
		opts.PackageArtifacts = true
		Expect(createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)).To(Equal(off))
	})

	It("runs the agent as the done container of an accepted artifact's interrupt Job after PackageArtifacts is turned off, and the package image for any other result", func() {
		for _, result := range []*v1alpha1.PreflightResult{
			nil,
			{Kind: "image", Pinned: packageImage + "@sha256:" + strings.Repeat("a", 64)},
			{Kind: "unknown", ErrorClass: "unreachable"},
			{Kind: "rejected", Reason: "host-files"},
			{Kind: "artifact", Pinned: packageImage + "@sha256:" + strings.Repeat("b", 64), Uninstall: "supported"},
		} {
			done := createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, nodeName, v1alpha1.StageInterrupt, result).
				Spec.Template.Spec.Containers
			Expect(done).To(HaveLen(1), "%+v", result)
			if preflightAccepted(result) {
				Expect(done[0].Image).To(Equal(agentImage), "%+v", result)
				Expect(done[0].Command).To(BeNil(), "%+v", result)
				continue
			}
			Expect(done[0].Image).To(Equal(packageImage+":1.0.0"), "%+v", result)
		}
	})

	It("runs the agent as the done container of every interrupt Job while PackageArtifacts is on, and nothing else changes", func() {
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
					off := createInterruptJobFromPackage(opts, interrupt, "args", pkg, skyhook, nodeName, stage, nil)
					opts.PackageArtifacts = true
					on := createInterruptJobFromPackage(opts, interrupt, "args", pkg, skyhook, nodeName, stage, nil)

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
		preflightJob := func(built *v1alpha1.Package, secrets []string) *batchv1.Job {
			GinkgoHelper()
			ref, err := packageReference(built)
			Expect(err).NotTo(HaveOccurred())
			return createPreflightJobFromPackage(opts, built, skyhook, nodeName, ref, secrets)
		}

		It("keeps a preflight Job whatever pull secrets it was built with", func() {
			for _, secrets := range [][]string{nil, pullSecrets} {
				Expect(jobMatchesPackage(opts, pkg, *preflightJob(pkg, secrets), skyhook, v1alpha1.StagePreflight)).To(BeTrue())
			}
		})

		DescribeTable("treats a preflight Job as stale once the package it inspects for changes",
			func(edit func(*v1alpha1.Package)) {
				job := preflightJob(pkg, pullSecrets)
				current := pkg.DeepCopy()
				edit(current)
				Expect(jobMatchesPackage(opts, current, *job, skyhook, v1alpha1.StagePreflight)).To(BeFalse())
			},
			Entry("a version change", func(p *v1alpha1.Package) { p.Version = "2.0.0" }),
			Entry("an image change", func(p *v1alpha1.Package) { p.Image = packageImage + "-relocated" }),
			Entry("a containerSHA set", func(p *v1alpha1.Package) { p.ContainerSHA = digestOf("b") }),
			Entry("a containerSHA that is no digest", func(p *v1alpha1.Package) { p.ContainerSHA = "sha256:abc" }),
			Entry("an env change", func(p *v1alpha1.Package) { p.Env = []corev1.EnvVar{{Name: "EDITED", Value: "true"}} }),
			Entry("an agent image override", func(p *v1alpha1.Package) { p.AgentImageOverride = "ghcr.io/nvidia/nodewright/agent:7.0.0" }),
		)

		It("treats a preflight Job built for a containerSHA as stale once it is removed", func() {
			built := pkg.DeepCopy()
			built.ContainerSHA = digestOf("b")
			job := preflightJob(built, nil)
			Expect(jobMatchesPackage(opts, built, *job, skyhook, v1alpha1.StagePreflight)).To(BeTrue())
			Expect(jobMatchesPackage(opts, pkg, *job, skyhook, v1alpha1.StagePreflight)).To(BeFalse())
		})

		It("keeps an artifact Job whatever digest it pins and whatever pull secrets it was built with", func() {
			for _, job := range []*batchv1.Job{
				createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig, pinned, pullSecrets),
				createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig, packageImage+"@"+digestOf("b"), nil),
			} {
				Expect(jobMatchesPackage(opts, pkg, *job, skyhook, v1alpha1.StageConfig)).To(BeTrue())
			}
		})

		DescribeTable("treats an artifact Job as stale once the package changes",
			func(edit func(*v1alpha1.Package)) {
				job := createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig, pinned, pullSecrets)
				current := pkg.DeepCopy()
				edit(current)
				Expect(jobMatchesPackage(opts, current, *job, skyhook, v1alpha1.StageConfig)).To(BeFalse())
			},
			Entry("a version change", func(p *v1alpha1.Package) { p.Version = "2.0.0" }),
			Entry("an env change", func(p *v1alpha1.Package) { p.Env = []corev1.EnvVar{{Name: "EDITED", Value: "true"}} }),
			Entry("an agent image override", func(p *v1alpha1.Package) { p.AgentImageOverride = "ghcr.io/nvidia/nodewright/agent:7.0.0" }),
		)

		It("compares a legacy Job whose package env names NODEWRIGHT_PACKAGE_REF as a legacy Job", func() {
			pkg.Env = []corev1.EnvVar{{Name: "NODEWRIGHT_PACKAGE_REF", Value: "set by the package"}}
			job := createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageConfig)
			Expect(jobMatchesPackage(opts, pkg, *job, skyhook, v1alpha1.StageConfig)).To(BeTrue())

			edited := pkg.DeepCopy()
			edited.Env[0].Value = "edited"
			Expect(jobMatchesPackage(opts, edited, *job, skyhook, v1alpha1.StageConfig)).To(BeFalse())
		})

		It("compares interrupt Jobs as before, whatever the option", func() {
			bumped := pkg.DeepCopy()
			bumped.Version = "2.0.0"
			for _, on := range []bool{false, true} {
				opts.PackageArtifacts = on
				for _, stage := range []v1alpha1.Stage{v1alpha1.StageInterrupt, v1alpha1.StageUninstallInterrupt} {
					job := createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, nodeName, stage, nil)
					Expect(jobMatchesPackage(opts, pkg, *job, skyhook, stage)).To(BeTrue())
					Expect(jobMatchesPackage(opts, bumped, *job, skyhook, stage)).To(BeFalse())
				}
			}
		})
	})

	Describe("stageJobMatchesPreflight", func() {
		accepted := &v1alpha1.PreflightResult{Kind: "artifact", Pinned: pinned, Uninstall: "supported"}

		DescribeTable("matches a stage Job to the Job createStageJob would build from the node's preflight result",
			func(build func() *batchv1.Job, result *v1alpha1.PreflightResult, want bool) {
				Expect(stageJobMatchesPreflight(build(), pkg.Name, result)).To(Equal(want))
			},
			Entry("an artifact Job pinned to the recorded digest", func() *batchv1.Job {
				return createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned, pullSecrets)
			}, accepted, true),
			Entry("an artifact Job pinned to another digest", func() *batchv1.Job {
				return createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, packageImage+"@"+digestOf("b"), pullSecrets)
			}, accepted, false),
			Entry("an artifact Job for a container image", func() *batchv1.Job {
				return createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned, nil)
			}, &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}, false),
			Entry("an artifact Job with no result", func() *batchv1.Job {
				return createArtifactJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply, pinned, nil)
			}, nil, false),
			Entry("a legacy Job for an accepted artifact", func() *batchv1.Job {
				return createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
			}, accepted, false),
			Entry("a legacy Job for a container image", func() *batchv1.Job {
				return createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
			}, &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}, true),
			Entry("a legacy Job for a reference that could not be inspected", func() *batchv1.Job {
				return createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
			}, &v1alpha1.PreflightResult{Kind: "unknown", ErrorClass: "timeout"}, true),
			Entry("a legacy Job with no result", func() *batchv1.Job {
				return createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
			}, nil, true),
			Entry("a legacy Job whose package env names NODEWRIGHT_PACKAGE_REF, for a container image", func() *batchv1.Job {
				withEnv := pkg.DeepCopy()
				withEnv.Env = []corev1.EnvVar{{Name: "NODEWRIGHT_PACKAGE_REF", Value: "set by the package"}}
				return createJobFromPackage(opts, withEnv, skyhook, nodeName, v1alpha1.StageApply)
			}, &v1alpha1.PreflightResult{Kind: "image", Pinned: pinned}, true),
		)
	})

	Describe("API admission (envtest)", func() {
		// As the admission spec of job_builder_test.go, for the Job shapes PackageArtifacts adds:
		// the registry-auth Secret volumes and mounts, the termination message policy of the
		// preflight container, and an agent done container with no command.
		It("creates a preflight Job and an artifact stage Job with pull secrets, and an interrupt Job with the option on", func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: opts.Namespace}}
			if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).ToNot(HaveOccurred())
			}

			opts.PackageArtifacts = true
			const node = "worker-artifact-admission"
			built := []*batchv1.Job{
				createPreflightJobFromPackage(opts, pkg, skyhook, node, reference, pullSecrets),
				createArtifactJobFromPackage(opts, pkg, skyhook, node, v1alpha1.StageApply, pinned, pullSecrets),
				createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, node, v1alpha1.StageInterrupt, nil),
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
