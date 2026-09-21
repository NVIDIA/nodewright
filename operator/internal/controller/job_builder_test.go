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

package controller

import (
	"math"
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
)

var _ = Describe("job builders", func() {

	var (
		opts     SkyhookOperatorOptions
		skyhook  *wrapper.Skyhook
		pkg      *v1alpha1.Package
		nodeName string
	)

	prefix := func(suffix string) string { return v1alpha1.METADATA_PREFIX + "/" + suffix }

	BeforeEach(func() {
		opts = SkyhookOperatorOptions{
			Namespace:            "skyhook",
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
			PauseImage:           "registry.k8s.io/pause:3.10",
			JobOperatorOptions: JobOperatorOptions{
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
		skyhook = wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-init", UID: "abc", Generation: 4},
		})
		pkg = &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"},
			Image:      "ghcr.io/nvidia/skyhook-packages/tuning",
		}
		nodeName = "worker-7"
	})

	Describe("createJobFromPackage (package Job)", func() {

		var job *batchv1.Job

		It("stamps package metadata on both the Job and its pod template", func() {
			job := &batchv1.Job{
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{}},
			}
			image := getPackageImage(pkg)

			Expect(setJobPackage(job, skyhook.NodeWright, image, v1alpha1.StageApply, pkg)).To(Succeed())

			jobPackage, err := GetPackage(job)
			Expect(err).ToNot(HaveOccurred())
			templatePackage, err := GetPackage(&job.Spec.Template)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobPackage).To(Equal(templatePackage))
			Expect(jobPackage).To(Equal(&PackageSkyhook{
				PackageRef: pkg.PackageRef,
				Skyhook:    skyhook.Name,
				Stage:      v1alpha1.StageApply,
				Image:      image,
			}))
		})

		JustBeforeEach(func() {
			job = createJobFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
		})

		It("uses the package pod name and the operator namespace", func() {
			pod := createPodFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
			Expect(job.Name).To(Equal(pod.Name))
			Expect(job.Namespace).To(Equal("skyhook"))
		})

		It("carries the full label set on the Job and the pod template, no interrupt label", func() {
			for _, labels := range []map[string]string{job.Labels, job.Spec.Template.Labels} {
				Expect(labels).To(HaveKeyWithValue(prefix("name"), "gpu-init"))
				Expect(labels).To(HaveKeyWithValue(prefix("package"), "tuning-1.0.0"))
				Expect(labels).To(HaveKeyWithValue(prefix("stage"), "apply"))
				Expect(labels).To(HaveKeyWithValue(prefix("node"), nodeName))
				Expect(labels).To(HaveKeyWithValue(prefix("generation"), "4"))
				Expect(labels).ToNot(HaveKey(prefix("interrupt")))
			}
		})

		It("records the full resource-id as an annotation", func() {
			Expect(job.Annotations).To(HaveKeyWithValue(prefix("resource-id"), skyhook.ResourceID()))
		})

		It("sets a run-to-completion spec with TTL unset at creation", func() {
			Expect(*job.Spec.Parallelism).To(Equal(int32(1)))
			Expect(*job.Spec.Completions).To(Equal(int32(1)))
			Expect(*job.Spec.PodReplacementPolicy).To(Equal(batchv1.Failed))
			Expect(job.Spec.TTLSecondsAfterFinished).To(BeNil())
		})

		It("uses restartPolicy Never and ignores DisruptionTarget disruptions", func() {
			Expect(job.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
			Expect(job.Spec.PodFailurePolicy).ToNot(BeNil())
			Expect(job.Spec.PodFailurePolicy.Rules).To(HaveLen(1))
			rule := job.Spec.PodFailurePolicy.Rules[0]
			Expect(rule.Action).To(Equal(batchv1.PodFailurePolicyActionIgnore))
			Expect(rule.OnPodConditions).To(HaveLen(1))
			Expect(rule.OnPodConditions[0].Type).To(Equal(corev1.DisruptionTarget))
		})

		It("replaces the pause container with an exit-0 container on the package image", func() {
			// The package image (not the agent image): the init-copy container already runs
			// /bin/sh from it, so the package image is guaranteed to have a shell; a minimal
			// agent image may not, which would StartError the exit-0 container and hang the Job.
			containers := job.Spec.Template.Spec.Containers
			Expect(containers).To(HaveLen(1))
			Expect(containers[0].Name).To(Equal("done"))
			Expect(containers[0].Image).To(Equal("ghcr.io/nvidia/skyhook-packages/tuning:1.0.0"))
			Expect(containers[0].Command).To(Equal([]string{"/bin/sh", "-c", "exit 0"}))
		})

		It("preserves the init-container chain and node pinning from the pod builder", func() {
			pod := createPodFromPackage(opts, pkg, skyhook, nodeName, v1alpha1.StageApply)
			Expect(job.Spec.Template.Spec.InitContainers).To(Equal(pod.Spec.InitContainers))
			Expect(job.Spec.Template.Spec.NodeName).To(Equal(nodeName))
		})

		It("adds unbounded not-ready and unreachable NoExecute tolerations", func() {
			byKey := map[string]corev1.Toleration{}
			for _, t := range job.Spec.Template.Spec.Tolerations {
				byKey[t.Key] = t
			}
			for _, key := range []string{"node.kubernetes.io/not-ready", "node.kubernetes.io/unreachable"} {
				t, ok := byKey[key]
				Expect(ok).To(BeTrue(), "expected toleration for %s", key)
				Expect(t.Operator).To(Equal(corev1.TolerationOpExists))
				Expect(t.Effect).To(Equal(corev1.TaintEffectNoExecute))
				Expect(t.TolerationSeconds).To(BeNil())
			}
		})

		Describe("stage bounds", func() {
			It("bounds one attempt on the pod template, with the retry budget as the only other limit", func() {
				Expect(*job.Spec.BackoffLimit).To(Equal(int32(3)))
				Expect(job.Spec.Template.Spec.ActiveDeadlineSeconds).ToNot(BeNil())
				Expect(*job.Spec.Template.Spec.ActiveDeadlineSeconds).To(Equal(int64(3600)))
			})

			It("sets no Job-level deadline, so a second clock can never disagree with the per-attempt one", func() {
				Expect(job.Spec.ActiveDeadlineSeconds).To(BeNil())
			})

			When("the package sets its own stageTimeout", func() {
				BeforeEach(func() { pkg.StageTimeout = &metav1.Duration{Duration: 2 * time.Hour} })
				It("uses the package value per attempt", func() {
					Expect(*job.Spec.Template.Spec.ActiveDeadlineSeconds).To(Equal(int64(7200)))
					Expect(job.Spec.ActiveDeadlineSeconds).To(BeNil())
				})
			})

			When("the package removes the time bound with 0", func() {
				BeforeEach(func() { pkg.StageTimeout = &metav1.Duration{Duration: 0} })
				It("omits the deadline but keeps the retry budget", func() {
					Expect(job.Spec.Template.Spec.ActiveDeadlineSeconds).To(BeNil())
					Expect(job.Spec.ActiveDeadlineSeconds).To(BeNil())
					Expect(*job.Spec.BackoffLimit).To(Equal(int32(3)))
				})
			})

			When("the operator default is also 0", func() {
				BeforeEach(func() { opts.JobStageTimeout = 0 })
				It("omits the deadline", func() {
					Expect(job.Spec.Template.Spec.ActiveDeadlineSeconds).To(BeNil())
					Expect(job.Spec.ActiveDeadlineSeconds).To(BeNil())
				})
			})

			When("the package sets a positive sub-second stageTimeout", func() {
				BeforeEach(func() { pkg.StageTimeout = &metav1.Duration{Duration: 500 * time.Millisecond} })
				It("rounds up to at least one second, never 0 (which would insta-fail)", func() {
					Expect(job.Spec.Template.Spec.ActiveDeadlineSeconds).ToNot(BeNil())
					Expect(*job.Spec.Template.Spec.ActiveDeadlineSeconds).To(Equal(int64(1)))
				})
			})

			When("the stageTimeout is larger than the apiserver accepts", func() {
				BeforeEach(func() { pkg.StageTimeout = &metav1.Duration{Duration: 700000 * time.Hour} })
				It("clamps to int32, which the apiserver takes, rather than erroring every create", func() {
					Expect(*job.Spec.Template.Spec.ActiveDeadlineSeconds).To(Equal(int64(math.MaxInt32)))
				})
			})
		})

		When("the package sets gracefulShutdown and the operator sets an image pull secret", func() {
			BeforeEach(func() {
				opts.ImagePullSecret = "regcred"
				pkg.GracefulShutdown = &metav1.Duration{Duration: 45 * time.Second}
			})
			It("carries both through to the pod template", func() {
				Expect(job.Spec.Template.Spec.TerminationGracePeriodSeconds).ToNot(BeNil())
				Expect(*job.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(45)))
				Expect(job.Spec.Template.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "regcred"}))
			})
		})

		When("the node name exceeds the label-value limit", func() {
			BeforeEach(func() { nodeName = "worker-" + strings.Repeat("x", 70) })
			It("hashes the node label but keeps the full nodeName on the template", func() {
				Expect(job.Labels[prefix("node")]).To(Equal(generateSafeName(63, nodeName)))
				Expect(len(job.Labels[prefix("node")])).To(BeNumerically("<=", 63))
				Expect(job.Spec.Template.Spec.NodeName).To(Equal(nodeName))
			})
		})
	})

	Describe("createInterruptJobFromPackage (interrupt Job)", func() {

		var job *batchv1.Job

		JustBeforeEach(func() {
			job = createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, nodeName, v1alpha1.StageInterrupt)
		})

		It("uses the interrupt name formula and carries the interrupt label", func() {
			Expect(job.Name).To(Equal(generateSafeName(63, skyhook.Name, string(v1alpha1.StageInterrupt), string(v1alpha1.REBOOT), nodeName)))
			Expect(job.Labels).To(HaveKeyWithValue(prefix("interrupt"), "True"))
			Expect(job.Spec.Template.Labels).To(HaveKeyWithValue(prefix("interrupt"), "True"))
		})

		It("keeps restartPolicy OnFailure with no podFailurePolicy", func() {
			Expect(job.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
			Expect(job.Spec.PodFailurePolicy).To(BeNil())
		})

		It("keeps unbounded backoff and bounds the whole stage, unlike a package Job", func() {
			// Under OnFailure backoffLimit counts container restarts, so a finite limit would be
			// spent by the in-place restart that *is* the reboot recovery; and the bound has to
			// span the reboot, which a per-attempt clock cannot (StartTime does not reset).
			Expect(*job.Spec.BackoffLimit).To(Equal(int32(math.MaxInt32)))
			Expect(job.Spec.ActiveDeadlineSeconds).ToNot(BeNil())
			Expect(*job.Spec.ActiveDeadlineSeconds).To(Equal(int64(3600)))
			Expect(job.Spec.Template.Spec.ActiveDeadlineSeconds).To(BeNil())
		})

		It("still replaces the pause container with an exit-0 container", func() {
			// the exit-0 command itself is asserted in the package-Job spec above; the
			// transform is shared, so here just confirm the swap happened.
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal(doneContainerName))
		})
	})

	Describe("API admission (envtest)", func() {
		// The struct-level specs above cannot see apiserver validation of the Job field
		// combinations (podFailurePolicy x restartPolicy x podReplacementPolicy x
		// activeDeadlineSeconds). Create both Job kinds against the envtest apiserver so an
		// admission regression fails here instead of only at e2e / wiring time.
		It("creates both a package and an interrupt Job against the apiserver", func() {
			// These specs use their own opts, so the namespace they name is not the one the
			// suite provisions from the operator default; create it rather than depend on it.
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: opts.Namespace}}
			if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).ToNot(HaveOccurred())
			}

			built := []*batchv1.Job{
				createJobFromPackage(opts, pkg, skyhook, "worker-admission", v1alpha1.StageApply),
				createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "args", pkg, skyhook, "worker-admission", v1alpha1.StageInterrupt),
			}
			for _, job := range built {
				Expect(k8sClient.Create(ctx, job)).To(Succeed(), "apiserver must admit job %s", job.Name)
				DeferCleanup(func(j *batchv1.Job) {
					_ = k8sClient.Delete(ctx, j)
				}, job)
			}
		})
	})
})

var _ = Describe("pod builders", func() {

	var opts SkyhookOperatorOptions

	prefix := func(suffix string) string { return v1alpha1.METADATA_PREFIX + "/" + suffix }

	BeforeEach(func() {
		opts = SkyhookOperatorOptions{
			Namespace:            "skyhook",
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
			PauseImage:           "registry.k8s.io/pause:3.10",
		}
	})

	It("Pods should always tolerate runtime required taint", func() {
		pod := createPodFromPackage(
			opts,
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				NodeWright: &v1alpha1.NodeWright{
					Spec: v1alpha1.NodeWrightSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageApply,
		)
		Expect(pod.Spec.Tolerations).To(ContainElements(opts.GetRuntimeRequiredTolerations()))
	})

	It("Interrupt pods should tolerate runtime required taint when it is runtime required", func() {
		pod := createInterruptPodForPackage(
			opts,
			&v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
			"argEncode",

			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				NodeWright: &v1alpha1.NodeWright{
					Spec: v1alpha1.NodeWrightSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageInterrupt,
		)
		Expect(pod.Spec.Tolerations).To(ContainElements(opts.GetRuntimeRequiredTolerations()))
	})

	It("Pods should not have imagePullSecrets when ImagePullSecret is empty", func() {
		emptyOpts := SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			ImagePullSecret:      "", // Empty - no pull secret
			CopyDirRoot:          "/tmp",
			ReapplyOnReboot:      true,
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
		}

		pod := createPodFromPackage(
			emptyOpts,
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				NodeWright: &v1alpha1.NodeWright{
					Spec: v1alpha1.NodeWrightSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageApply,
		)
		Expect(pod.Spec.ImagePullSecrets).To(BeEmpty())
	})

	It("Interrupt pods should not have imagePullSecrets when ImagePullSecret is empty", func() {
		emptyOpts := SkyhookOperatorOptions{
			Namespace:            "skyhook",
			MaxInterval:          time.Second * 61,
			ImagePullSecret:      "", // Empty - no pull secret
			CopyDirRoot:          "/tmp",
			ReapplyOnReboot:      true,
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "foo:bar",
			PauseImage:           "foo:bar",
		}

		pod := createInterruptPodForPackage(
			emptyOpts,
			&v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
			"argEncode",
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{
					Name:    "foo",
					Version: "1.1.2",
				},
				Image: "foo/bar",
			},
			&wrapper.Skyhook{
				NodeWright: &v1alpha1.NodeWright{
					Spec: v1alpha1.NodeWrightSpec{
						RuntimeRequired: true,
					},
				},
			},
			"node1",
			v1alpha1.StageInterrupt,
		)
		Expect(pod.Spec.ImagePullSecrets).To(BeEmpty())
	})

	It("Pods should have imagePullSecrets when ImagePullSecret is set", func() {
		opts.ImagePullSecret = "regcred"
		pod := createPodFromPackage(
			opts,
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "foo", Version: "1.1.2"},
				Image:      "foo/bar",
			},
			&wrapper.Skyhook{NodeWright: &v1alpha1.NodeWright{}},
			"node1",
			v1alpha1.StageApply,
		)
		Expect(pod.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "regcred"}))
	})

	It("Interrupt pods should have imagePullSecrets when ImagePullSecret is set", func() {
		opts.ImagePullSecret = "regcred"
		pod := createInterruptPodForPackage(
			opts,
			&v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
			"argEncode",
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "foo", Version: "1.1.2"},
				Image:      "foo/bar",
			},
			&wrapper.Skyhook{NodeWright: &v1alpha1.NodeWright{}},
			"node1",
			v1alpha1.StageInterrupt,
		)
		Expect(pod.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "regcred"}))
	})

	It("Pods carry the package gracefulShutdown as the pod terminationGracePeriodSeconds", func() {
		pod := createPodFromPackage(
			opts,
			&v1alpha1.Package{
				PackageRef:       v1alpha1.PackageRef{Name: "foo", Version: "1.1.2"},
				Image:            "foo/bar",
				GracefulShutdown: &metav1.Duration{Duration: 45 * time.Second},
			},
			&wrapper.Skyhook{NodeWright: &v1alpha1.NodeWright{}},
			"node1",
			v1alpha1.StageApply,
		)
		Expect(pod.Spec.TerminationGracePeriodSeconds).ToNot(BeNil())
		Expect(*pod.Spec.TerminationGracePeriodSeconds).To(Equal(int64(45)))
	})

	It("Interrupt pods use the interrupt name formula, interrupt label, and a single privileged root mount", func() {
		skyhook := &wrapper.Skyhook{
			NodeWright: &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-init"},
			},
		}
		pod := createInterruptPodForPackage(
			opts,
			&v1alpha1.Interrupt{Type: v1alpha1.REBOOT},
			"argEncode",
			&v1alpha1.Package{
				PackageRef: v1alpha1.PackageRef{Name: "foo", Version: "1.1.2"},
				Image:      "foo/bar",
			},
			skyhook,
			"node1",
			v1alpha1.StageInterrupt,
		)

		Expect(pod.Name).To(Equal(generateSafeName(63, skyhook.Name, string(v1alpha1.StageInterrupt), string(v1alpha1.REBOOT), "node1")))
		Expect(pod.Labels).To(HaveKeyWithValue(prefix("interrupt"), interruptLabelValue))

		Expect(pod.Spec.InitContainers).To(HaveLen(1))
		interruptContainer := pod.Spec.InitContainers[0]
		Expect(interruptContainer.SecurityContext).ToNot(BeNil())
		Expect(*interruptContainer.SecurityContext.Privileged).To(BeTrue())
		Expect(interruptContainer.VolumeMounts).To(HaveLen(1))
		Expect(interruptContainer.VolumeMounts[0].MountPath).To(Equal(mountPathRoot))
	})

	It("should correctly identify if a pod matches a package", func() {

		// Create a test package
		testPackage := &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "test-package",
				Version: "1.2.3",
			},
			Image: "test-image:1.2.3",
			Resources: &v1alpha1.ResourceRequirements{
				CPURequest:    resource.MustParse("100m"),
				CPULimit:      resource.MustParse("200m"),
				MemoryRequest: resource.MustParse("64Mi"),
				MemoryLimit:   resource.MustParse("128Mi"),
			},
		}

		// Create a test skyhook
		testSkyhook := &wrapper.Skyhook{
			NodeWright: &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.NodeWrightSpec{
					Packages: v1alpha1.Packages{
						"test-package": *testPackage,
					},
				},
			},
		}

		// Stage to test
		testStage := v1alpha1.StageApply

		// Create actual pods that would be created by the operator functions
		// First using CreatePodFromPackage
		actualPod := createPodFromPackage(opts, testPackage, testSkyhook, "test-node", testStage)

		// Verify that the pod matches the package according to PodMatchesPackage
		matches := podMatchesPackage(opts, testPackage, *actualPod, testSkyhook, testStage)
		Expect(matches).To(BeTrue(), "PodMatchesPackage should recognize the pod it created")

		// Now let's modify the package version and see if it correctly identifies non-matches
		modifiedPackage := testPackage.DeepCopy()
		modifiedPackage.Version = "1.2.4"

		matches = podMatchesPackage(opts, modifiedPackage, *actualPod, testSkyhook, testStage)
		Expect(matches).To(BeFalse(), "PodMatchesPackage should not match when package version changed")

		// Test with different stage
		matches = podMatchesPackage(opts, testPackage, *actualPod, testSkyhook, v1alpha1.StageConfig)
		Expect(matches).To(BeFalse(), "PodMatchesPackage should not match when stage changed")

		// Test with interrupt pods
		interruptPod := createInterruptPodForPackage(
			opts,
			&v1alpha1.Interrupt{
				Type: v1alpha1.REBOOT,
			},
			"argEncode",
			testPackage,
			testSkyhook,
			"test-node",
			testStage,
		)

		// Verify that the interrupt pod matches the package
		matches = podMatchesPackage(opts, testPackage, *interruptPod, testSkyhook, testStage)
		Expect(matches).To(BeTrue(), "PodMatchesPackage should recognize the interrupt pod it created")
	})

	It("should mount each configMap key as a subPath so image defaults are not clobbered", func() {
		testPackage := &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "foo",
				Version: "1.1.2",
			},
			Image: "foo/bar",
			ConfigMap: map[string]string{
				"b.sh": "echo b",
				"a.sh": "echo a",
				"c.sh": "echo c",
			},
		}
		testSkyhook := &wrapper.Skyhook{
			NodeWright: &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: "test-skyhook"},
			},
		}

		pod := createPodFromPackage(opts, testPackage, testSkyhook, "node1", v1alpha1.StageApply)

		container := pod.Spec.InitContainers[0]

		// The configMap must never be mounted as a bare directory: that hides
		// any files the package image baked in at /skyhook-package/configmaps.
		for _, vm := range container.VolumeMounts {
			if vm.MountPath == "/skyhook-package/configmaps" {
				Fail("configMap must not be mounted as a directory; expected per-key subPath mounts")
			}
		}

		// Collect the configMap mounts (those backed by the package volume).
		configMapMounts := make([]corev1.VolumeMount, 0)
		for _, vm := range container.VolumeMounts {
			if vm.Name == testPackage.Name {
				configMapMounts = append(configMapMounts, vm)
			}
		}

		Expect(configMapMounts).To(HaveLen(3))
		// Sorted-key order for a deterministic pod spec.
		Expect(configMapMounts[0]).To(Equal(corev1.VolumeMount{
			Name:      testPackage.Name,
			MountPath: "/skyhook-package/configmaps/a.sh",
			SubPath:   "a.sh",
			ReadOnly:  true,
		}))
		Expect(configMapMounts[1]).To(Equal(corev1.VolumeMount{
			Name:      testPackage.Name,
			MountPath: "/skyhook-package/configmaps/b.sh",
			SubPath:   "b.sh",
			ReadOnly:  true,
		}))
		Expect(configMapMounts[2]).To(Equal(corev1.VolumeMount{
			Name:      testPackage.Name,
			MountPath: "/skyhook-package/configmaps/c.sh",
			SubPath:   "c.sh",
			ReadOnly:  true,
		}))

		// The underlying configMap volume is still mounted exactly once.
		configMapVolumes := 0
		for _, v := range pod.Spec.Volumes {
			if v.Name == testPackage.Name {
				configMapVolumes++
				Expect(v.ConfigMap).NotTo(BeNil())
			}
		}
		Expect(configMapVolumes).To(Equal(1))
	})
})

var _ = Describe("podMatchesPackage", func() {
	var (
		opts        SkyhookOperatorOptions
		expectedPod *corev1.Pod
		actualPod   *corev1.Pod
		skyhook     *wrapper.Skyhook
		package_    *v1alpha1.Package
	)

	BeforeEach(func() {
		opts = SkyhookOperatorOptions{
			Namespace:            "skyhook",
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
			PauseImage:           "registry.k8s.io/pause:3.10",
		}

		// Setup common test objects
		nodeName := "testNode"
		stage := v1alpha1.StageApply
		package_ = &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{
				Name:    "test-package",
				Version: "1.0.0",
			},
			Image: "test-image",
		}

		skyhook = &wrapper.Skyhook{
			NodeWright: &v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-skyhook",
				},
				Spec: v1alpha1.NodeWrightSpec{
					Packages: map[string]v1alpha1.Package{
						"test-package": *package_,
					},
				},
			},
		}

		// Create base pod structure, too much work to do it again
		expectedPod = createPodFromPackage(opts, package_, skyhook, nodeName, stage)
		actualPod = expectedPod.DeepCopy()
	})

	It("should match when resources are identical", func() {
		// Setup: Add resources to package and expected pod
		newPackage := *package_
		newPackage.Resources = &v1alpha1.ResourceRequirements{
			CPURequest:    resource.MustParse("100m"),
			CPULimit:      resource.MustParse("200m"),
			MemoryRequest: resource.MustParse("128Mi"),
			MemoryLimit:   resource.MustParse("256Mi"),
		}
		skyhook.Spec.Packages["test-package"] = newPackage

		expectedResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}

		// Set resources for all init containers in expected pod
		for i := range expectedPod.Spec.InitContainers {
			expectedPod.Spec.InitContainers[i].Resources = expectedResources
		}

		// Test: Set actual pod resources to match expected
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = expectedResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	It("should not match when resources differ", func() {
		// Setup: Add resources to package and expected pod
		newPackage := *package_
		newPackage.Resources = &v1alpha1.ResourceRequirements{
			CPURequest:    resource.MustParse("100m"),
			CPULimit:      resource.MustParse("200m"),
			MemoryRequest: resource.MustParse("128Mi"),
			MemoryLimit:   resource.MustParse("256Mi"),
		}
		skyhook.Spec.Packages["test-package"] = newPackage

		expectedResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}

		// Set resources for all init containers in expected pod
		for i := range expectedPod.Spec.InitContainers {
			expectedPod.Spec.InitContainers[i].Resources = expectedResources
		}

		// Test: Set different CPU request in actual pod for all init containers
		differentResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"), // Different CPU request
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = differentResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should match when no resources are specified and pod has no overrides", func() {
		// Setup: Ensure no resources in package
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Test: Ensure pod has no resource overrides for any init container
		emptyResources := corev1.ResourceRequirements{}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = emptyResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	It("should not match when no resources are specified but pod has requests", func() {
		// Setup: Ensure no resources in package
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Test: Add resource requests to all init containers
		requestResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = requestResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should not match when no resources are specified but pod has limits", func() {
		// Setup: Ensure no resources in package
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Test: Add resource limits to all init containers
		limitResources := corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Resources = limitResources
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("should ignore SKYHOOK_RESOURCE_ID env var", func() {
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Setup: Add SKYHOOK_RESOURCE_ID env var to all init containers
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Env = append(actualPod.Spec.InitContainers[i].Env, corev1.EnvVar{
				Name:  "SKYHOOK_RESOURCE_ID",
				Value: "SOME_VALUE",
			})
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeTrue())
	})

	It("should not ignore non static env vars", func() {
		newPackage := *package_
		newPackage.Resources = nil
		skyhook.Spec.Packages["test-package"] = newPackage

		// Setup: Add SKYHOOK_RESOURCE_ID env var to all init containers
		for i := range actualPod.Spec.InitContainers {
			actualPod.Spec.InitContainers[i].Env = append(actualPod.Spec.InitContainers[i].Env, corev1.EnvVar{
				Name:  "SOME_ENV_VAR",
				Value: "SOME_VALUE",
			})
		}

		// Set the package in the pod annotations
		err := SetPackages(actualPod, skyhook.NodeWright, newPackage.Image, v1alpha1.StageApply, &newPackage)
		Expect(err).ToNot(HaveOccurred())

		Expect(podMatchesPackage(opts, &newPackage, *actualPod, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})
})

var _ = Describe("jobMatchesPackage", func() {

	const node = "worker-7"

	var (
		opts    SkyhookOperatorOptions
		skyhook *wrapper.Skyhook
		pkg     *v1alpha1.Package
	)

	BeforeEach(func() {
		pkg = &v1alpha1.Package{
			PackageRef: v1alpha1.PackageRef{Name: "tuning", Version: "1.0.0"},
			Image:      "ghcr.io/nvidia/skyhook-packages/tuning",
		}
		opts = SkyhookOperatorOptions{
			Namespace:            "skyhook",
			CopyDirRoot:          "/var/lib/skyhook",
			AgentLogRoot:         "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
			PauseImage:           "registry.k8s.io/pause:3.10",
			JobOperatorOptions: JobOperatorOptions{
				JobStageTimeout: time.Hour,
				JobBackoffLimit: 3,
			},
		}
		skyhook = wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-init", UID: "abc", Generation: 4},
			Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{"tuning": *pkg}},
		})
	})

	It("matches a package Job the operator just built for the stage", func() {
		job := createJobFromPackage(opts, pkg, skyhook, node, v1alpha1.StageConfig)
		Expect(jobMatchesPackage(opts, pkg, *job, skyhook, v1alpha1.StageConfig)).To(BeTrue())
	})

	It("does not match once the package version changes", func() {
		job := createJobFromPackage(opts, pkg, skyhook, node, v1alpha1.StageApply)
		bumped := *pkg
		bumped.Version = "2.0.0"
		Expect(jobMatchesPackage(opts, &bumped, *job, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("matches an interrupt Job for the same package", func() {
		// argEncode is deliberately not part of the match: podMatchesPackage does not
		// compare init-container Args, so its value here is arbitrary.
		job := createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "encoded-interrupt-args", pkg, skyhook, node, v1alpha1.StageInterrupt)
		Expect(jobMatchesPackage(opts, pkg, *job, skyhook, v1alpha1.StageInterrupt)).To(BeTrue())
	})

	It("does not match an interrupt Job once the package version drifts", func() {
		// interrupt Jobs run at the uninstall-interrupt stage too, not only interrupt
		job := createInterruptJobFromPackage(opts, &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}, "", pkg, skyhook, node, v1alpha1.StageUninstallInterrupt)
		bumped := *pkg
		bumped.Version = "2.0.0"
		Expect(jobMatchesPackage(opts, &bumped, *job, skyhook, v1alpha1.StageUninstallInterrupt)).To(BeFalse())
	})

	It("does not match once the package image changes (same name/version)", func() {
		job := createJobFromPackage(opts, pkg, skyhook, node, v1alpha1.StageApply)
		moved := *pkg
		moved.Image = "ghcr.io/nvidia/skyhook-packages/tuning-relocated"
		Expect(jobMatchesPackage(opts, &moved, *job, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})

	It("does not match (and does not panic) when the init-container count differs", func() {
		job := createJobFromPackage(opts, pkg, skyhook, node, v1alpha1.StageApply)

		// an extra actual init container would index past the expected slice
		extra := job.DeepCopy()
		extra.Spec.Template.Spec.InitContainers = append(extra.Spec.Template.Spec.InitContainers, corev1.Container{Name: "rogue"})
		Expect(jobMatchesPackage(opts, pkg, *extra, skyhook, v1alpha1.StageApply)).To(BeFalse())

		// a missing actual init container must not silently match
		missing := job.DeepCopy()
		inits := missing.Spec.Template.Spec.InitContainers
		missing.Spec.Template.Spec.InitContainers = inits[:len(inits)-1]
		Expect(jobMatchesPackage(opts, pkg, *missing, skyhook, v1alpha1.StageApply)).To(BeFalse())
	})
})
