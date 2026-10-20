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
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// doneContainerName replaces the forever-running pause main container. A Job records
// completion only when its pod reaches Succeeded, which needs every container to
// terminate, so the main container exits 0 immediately.
const doneContainerName = "done"

// agentVersionArgument makes the agent print its version and exit 0 without touching the host.
const agentVersionArgument = "--version"

// The stage and check containers of an artifact stage Job read the package from the artifact
// envPackageRef pins (registry/repository@sha256:hex), and the preflight container inspects the
// reference envPreflightRef names, by tag or by digest. Both look registry credentials up first in
// the files envRegistryAuthFiles lists, the .dockerconfigjson of each imagePullSecrets entry of the
// package ServiceAccount, in that order. The agent implements these names: never change them, and
// never set them on interrupt Jobs.
const (
	envPackageRef        = "NODEWRIGHT_PACKAGE_REF"
	envPreflightRef      = "NODEWRIGHT_PREFLIGHT_REF"
	envRegistryAuthFiles = "NODEWRIGHT_REGISTRY_AUTH_FILES"

	// volumeNameRegistryAuth and mountPathRegistryAuth are suffixed with the index of the pull
	// secret. The mount is outside mountPathRoot and /skyhook-package, so it can neither shadow the
	// host nor land in the copy of a package.
	volumeNameRegistryAuth = "nodewright-registry-auth"
	mountPathRegistryAuth  = "/var/run/secrets/nodewright/registry-auth"

	// preflightArgument is the agent mode the preflight container runs.
	preflightArgument = "preflight"
)

// interruptContainerMemory is fixed rather than taken from the package's resources because
// the interrupt container runs the agent plus whatever the interrupt executes (reboot,
// systemctl, or a package's interrupt script), not the package itself. 64Mi left too little
// headroom on 64K-page arm64 kernels, where cgroup v2 also charges kernel memory to the pod,
// and an OOM kill during reboot left the node cordoned and never rebooted. The node is
// drained by the time this runs, so the larger reservation costs nothing in practice.
const interruptContainerMemory = "256Mi"

// jobLabels is the label set a package/interrupt Job carries and its child pods inherit
// (via the pod template), so existing CLI label queries keep working. The node label
// falls back to a bounded safe name when the node name exceeds the 63-char label-value
// limit; the pod template's nodeName stays authoritative for the full name.
func jobLabels(skyhook *wrapper.Skyhook, _package *v1alpha1.Package, stage v1alpha1.Stage, nodeName string, interrupt bool) map[string]string {
	labels := map[string]string{
		fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):       skyhook.Name,
		fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX):    fmt.Sprintf("%s-%s", _package.Name, _package.Version),
		fmt.Sprintf("%s/stage", v1alpha1.METADATA_PREFIX):      string(stage),
		fmt.Sprintf("%s/node", v1alpha1.METADATA_PREFIX):       nodeLabelValue(nodeName),
		fmt.Sprintf("%s/generation", v1alpha1.METADATA_PREFIX): strconv.FormatInt(skyhook.Generation, 10),
	}
	if interrupt {
		labels[fmt.Sprintf("%s/interrupt", v1alpha1.METADATA_PREFIX)] = interruptLabelValue
	}
	return labels
}

// nodeLabelValue is the value the "<prefix>/node" label carries: the node name, or a bounded
// safe name when it exceeds the 63-char label-value limit. JobExists and jobLabels must agree
// on this transform so node-scoped label queries resolve; the pod template's nodeName stays
// authoritative for the full name.
func nodeLabelValue(nodeName string) string {
	if len(nodeName) > 63 {
		return generateSafeName(63, nodeName)
	}
	return nodeName
}

// setJobPackage stamps the package annotation on both the Job and its pod template. The template
// copy is load-bearing: the in-flight erroring path (pod_controller.go) reads the package off the
// child pod, which inherits only the template's metadata — without it, GetPackage(childPod) is nil
// and pod-evidence erroring is silently lost.
func setJobPackage(job *batchv1.Job, skyhook *v1alpha1.NodeWright, image string, stage v1alpha1.Stage, _package *v1alpha1.Package) error {
	if err := SetPackages(job, skyhook, image, stage, _package); err != nil {
		return err
	}
	return SetPackages(&job.Spec.Template, skyhook, image, stage, _package)
}

// createJobFromPackage builds the batch/v1 Job that runs a package stage on a node. It
// wraps the pod this operator builds today (createPodFromPackage) so nothing about the
// executor's shape drifts, then applies the Job differences: the pause container becomes
// exit-0, restartPolicy becomes Never (each attempt a fresh, archivable pod), and
// disruptions are declaratively ignored.
func createJobFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage) *batchv1.Job {
	pod := createPodFromPackage(opts, _package, skyhook, nodeName, stage)
	return jobFromPod(opts, pod, skyhook, _package, stage, nodeName, false, false)
}

// createArtifactJobFromPackage builds the stage Job of a package whose preflight accepted its
// artifact, with pinned the digest preflight recorded. It is the Job of createJobFromPackage less
// the init-copy container, whose package image an artifact cannot run, with pinned and the pull
// secrets handed to the stage and check containers, which pull the package themselves, and the
// agent as the done container.
func createArtifactJobFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage, pinned string, pullSecrets []string) *batchv1.Job {
	pod := createArtifactPodFromPackage(opts, _package, skyhook, nodeName, stage, pinned, pullSecrets)
	return jobFromPod(opts, pod, skyhook, _package, stage, nodeName, false, true)
}

// createStageJob builds the stage Job of a package from result, its preflight result on the node:
// the artifact Job, pinned to the recorded digest, for an accepted artifact, and the Job of
// createJobFromPackage otherwise, which is also the Job of a package that had no preflight.
func createStageJob(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage, result *v1alpha1.PreflightResult, pullSecrets []string) *batchv1.Job {
	if preflightAccepted(result) {
		return createArtifactJobFromPackage(opts, _package, skyhook, nodeName, stage, result.Pinned, pullSecrets)
	}
	return createJobFromPackage(opts, _package, skyhook, nodeName, stage)
}

// createArtifactPodFromPackage is the pod of an artifact stage Job: that of createPodFromPackage
// less the init-copy container, with the stage and check containers given pinned and pullSecrets.
func createArtifactPodFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage, pinned string, pullSecrets []string) *corev1.Pod {
	pod := createPodFromPackage(opts, _package, skyhook, nodeName, stage)
	copyContainer := packageCopyContainerName(_package)
	pod.Spec.InitContainers = slices.DeleteFunc(pod.Spec.InitContainers, func(c corev1.Container) bool {
		return c.Name == copyContainer
	})
	for i := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[i]
		// Cloned before appending: createPodFromPackage gives the stage and check containers one
		// env slice, itself appended onto _package.Env, so an append in place could write into the
		// other container or into the NodeWright spec.
		container.Env = append(slices.Clone(container.Env), corev1.EnvVar{Name: envPackageRef, Value: pinned})
	}
	addRegistryAuth(pod, pullSecrets)
	return pod
}

// createPreflightJobFromPackage builds the preflight Job of a package: one agent container that
// inspects reference with the node's credentials and reports what it found in its termination
// message, and the agent as the done container. No package image is pulled and no package code
// runs.
func createPreflightJobFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName, reference string, pullSecrets []string) *batchv1.Job {
	pod := createPreflightPodFromPackage(opts, _package, skyhook, nodeName, reference, pullSecrets)
	return jobFromPod(opts, pod, skyhook, _package, v1alpha1.StagePreflight, nodeName, false, true)
}

// createPreflightPodFromPackage is the pod of a preflight Job: that of createPodFromPackage with
// its init containers replaced by the preflight container, which mounts only the host root.
func createPreflightPodFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName, reference string, pullSecrets []string) *corev1.Pod {
	pod := createPodFromPackage(opts, _package, skyhook, nodeName, v1alpha1.StagePreflight)
	pod.Spec.Volumes = slices.DeleteFunc(pod.Spec.Volumes, func(v corev1.Volume) bool {
		return v.Name != volumeNameRootMount
	})
	pod.Spec.InitContainers = []corev1.Container{{
		Name:            preflightContainerName(_package),
		Image:           getAgentImage(opts, _package),
		ImagePullPolicy: corev1.PullAlways,
		Args:            []string{preflightArgument, mountPathRoot},
		Env:             append(slices.Clone(_package.Env), corev1.EnvVar{Name: envPreflightRef, Value: reference}),
		SecurityContext: &corev1.SecurityContext{
			Privileged: ptr(true),
		},
		VolumeMounts: []corev1.VolumeMount{{
			Name:             volumeNameRootMount,
			MountPath:        mountPathRoot,
			MountPropagation: ptr(corev1.MountPropagationHostToContainer),
		}},
		// The result is read from the termination message, so it must only ever be the file
		// the agent writes: FallbackToLogsOnError would put log lines where the result goes.
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}}
	addRegistryAuth(pod, pullSecrets)
	setPodResources(pod, _package.Resources)
	return pod
}

// preflightContainerName names the container of a preflight pod, as the stage container of any
// other stage is named.
func preflightContainerName(_package *v1alpha1.Package) string {
	return fmt.Sprintf("%s-%s", trunstr(_package.Name, 43), v1alpha1.StagePreflight)
}

// addRegistryAuth gives every init container of pod the .dockerconfigjson of each of pullSecrets,
// mounted read-only from an optional Secret volume, and their paths in order in
// envRegistryAuthFiles. A missing Secret or key leaves no file, which the agent passes over, as the
// kubelet passes over a missing pull secret.
func addRegistryAuth(pod *corev1.Pod, pullSecrets []string) {
	paths := make([]string, 0, len(pullSecrets))
	mounts := make([]corev1.VolumeMount, 0, len(pullSecrets))
	for i, name := range pullSecrets {
		volume := fmt.Sprintf("%s-%d", volumeNameRegistryAuth, i)
		mountPath := fmt.Sprintf("%s/%d", mountPathRegistryAuth, i)
		paths = append(paths, mountPath+"/"+corev1.DockerConfigJsonKey)
		mounts = append(mounts, corev1.VolumeMount{Name: volume, MountPath: mountPath, ReadOnly: true})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: volume,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: name,
					Items:      []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: corev1.DockerConfigJsonKey}},
					Optional:   ptr(true),
				},
			},
		})
	}
	for i := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[i]
		container.Env = append(slices.Clone(container.Env), corev1.EnvVar{Name: envRegistryAuthFiles, Value: strings.Join(paths, ":")})
		container.VolumeMounts = append(slices.Clone(container.VolumeMounts), mounts...)
	}
}

// setPackageServiceAccount runs pod as the package ServiceAccount, with no API token, since no
// package pod calls the API. The pod lists no imagePullSecrets of its own, so ServiceAccount
// admission gives it the ServiceAccount's, and the kubelet pulls its images as for any pod.
func setPackageServiceAccount(opts SkyhookOperatorOptions, pod *corev1.Pod) {
	pod.Spec.ServiceAccountName = opts.PackageServiceAccount
	pod.Spec.AutomountServiceAccountToken = ptr(false)
}

// createInterruptJobFromPackage builds the interrupt Job. Interrupt Jobs keep
// restartPolicy OnFailure (a reboot interrupt kills its own pod by design; in-place
// restart after the node returns is the proven recovery) and therefore no
// podFailurePolicy: the API forbids combining them.
//
// Every interrupt type except a reboot is stamped with a restart allowance of JOB_BACKOFF_LIMIT,
// the retry budget a package stage gets, which the pod watch enforces. A reboot is left
// unstamped and bounded by its deadline alone: its own shutdown can kill the agent, once or
// several times while a slow shutdown proceeds, and none of that is a failure.
func createInterruptJobFromPackage(opts SkyhookOperatorOptions, _interrupt *v1alpha1.Interrupt, argEncode string, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage) *batchv1.Job {
	pod := createInterruptPodForPackage(opts, _interrupt, argEncode, _package, skyhook, nodeName, stage)
	job := jobFromPod(opts, pod, skyhook, _package, stage, nodeName, true, opts.PackageArtifacts)
	if _interrupt.Type != v1alpha1.REBOOT {
		job.Annotations[annotationRestartAllowance] = strconv.FormatInt(int64(opts.JobBackoffLimit), 10)
	}
	return job
}

// jobFromPod turns a package/interrupt pod into its Job. The old raw-pod builders remain
// the source of the pod spec (shared until they are removed with the legacy path), so this
// only expresses the Job-specific differences.
//
// The main container exits 0 so the pod can reach Succeeded. Without agentDone it runs exit 0 in
// a shell from the package image, as a legacy stage Job always does: the init-copy container has
// already pulled that image and run shellBinary from it, so it has a shell, which the distroless
// agent image lacks. With agentDone it is the agent image the init containers ran, with no command
// and agentVersionArgument, which the Go agent answers with exit 0 without touching the host. An
// artifact stage Job needs it because its package image is an artifact, which no container can
// run. Every interrupt Job takes it while PackageArtifacts is on, chosen by the option rather than
// by an outcome, so that a cold cache or a failed inspection can never bring back the package
// image for an artifact, which would leave the interrupt sitting until its deadline.
func jobFromPod(opts SkyhookOperatorOptions, pod *corev1.Pod, skyhook *wrapper.Skyhook, _package *v1alpha1.Package, stage v1alpha1.Stage, nodeName string, interrupt, agentDone bool) *batchv1.Job {
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		if container.Name != pauseContainerName {
			continue
		}
		container.Name = doneContainerName
		if agentDone {
			container.Image = getAgentImage(opts, _package)
			container.Args = []string{agentVersionArgument}
		} else {
			container.Image = getPackageImage(_package)
			container.Command = []string{shellBinary, "-c", "exit 0"}
		}
	}

	// Without these, DefaultTolerationSeconds admission adds 300s variants and the taint
	// manager evicts a node-pinned pod once a reboot-class interrupt keeps the node
	// NotReady past the default timeout: a pointless replacement + hostPath re-copy for a
	// node that is coming back. Deliberately unbounded (no tolerationSeconds): these pods
	// are node-bound host agents, so eviction elsewhere is never useful.
	pod.Spec.Tolerations = append(pod.Spec.Tolerations,
		corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
		corev1.Toleration{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
	)

	labels := jobLabels(skyhook, _package, stage, nodeName, interrupt)

	spec := batchv1.JobSpec{
		Parallelism: ptr(int32(1)),
		Completions: ptr(int32(1)),
		// Replace only after the previous pod is fully terminated so two executors never
		// overlap on the shared hostPath mounts.
		PodReplacementPolicy: ptr(batchv1.Failed),
		// TTLSecondsAfterFinished is deliberately unset at creation; JobReconcile sets it
		// by outcome at completion time so failure logs outlive success logs.
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       pod.Spec,
		},
	}

	// From the package's stageTimeout, else the operator default. A value of 0 disables the
	// time bound: the deadline fields are omitted, because a literal 0 would insta-fail every
	// Job. The retry budget still applies.
	timeout := effectiveStageTimeout(opts, _package)

	if interrupt {
		spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
		// Interrupt Jobs keep the unbounded limit and a whole-stage deadline, deliberately
		// unlike package Jobs. Under OnFailure backoffLimit counts container restarts rather
		// than failed pods, so a finite limit would be spent by the in-place restart that *is*
		// the reboot recovery; and the bound has to span the reboot, which a per-attempt clock
		// cannot, because a pod's StartTime does not reset when the kubelet restarts its
		// containers after the node returns. The pod watch bounds failed restarts of every
		// interrupt but a reboot instead; see createInterruptJobFromPackage.
		spec.BackoffLimit = ptr(int32(math.MaxInt32))
		if timeout > 0 {
			spec.ActiveDeadlineSeconds = ptr(deadlineSeconds(timeout))
		}
	} else {
		spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
		spec.PodFailurePolicy = &batchv1.PodFailurePolicy{
			Rules: []batchv1.PodFailurePolicyRule{{
				Action: batchv1.PodFailurePolicyActionIgnore,
				OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{
					Type:   corev1.DisruptionTarget,
					Status: corev1.ConditionTrue,
				}},
			}},
		}
		// A timeout is a retryable failure like any other, so the bound is per attempt, on the
		// pod template: an expired pod is Failed and replaced rather than failing the Job
		// outright. backoffLimit is what finally gives up.
		//
		// Deliberately no Job-level deadline here. A second clock over the same work can only
		// disagree with the first — derived from the retry budget it is redundant, and set
		// independently it truncates the budget into a DeadlineExceeded that reads as a hang.
		// The cost is that the one case a per-attempt clock cannot see stays unbounded: that
		// clock runs from pod.Status.StartTime, so a pod the kubelet never acknowledges never
		// starts it, never fails, and never spends an attempt. Nothing the operator sets can
		// bound a pod the kubelet has not accepted; see the stageTimeout docs.
		spec.BackoffLimit = ptr(opts.JobBackoffLimit)
		if timeout > 0 {
			spec.Template.Spec.ActiveDeadlineSeconds = ptr(deadlineSeconds(timeout))
		}
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: opts.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				// Full provenance; ResourceID is routinely too long for a label value.
				fmt.Sprintf("%s/resource-id", v1alpha1.METADATA_PREFIX): skyhook.ResourceID(),
			},
		},
		Spec: spec,
	}
}

func effectiveStageTimeout(opts SkyhookOperatorOptions, _package *v1alpha1.Package) time.Duration {
	if _package.StageTimeout != nil {
		return _package.StageTimeout.Duration
	}
	return opts.JobStageTimeout
}

// deadlineSeconds rounds a timeout up to whole seconds. Truncating a positive sub-second timeout
// to 0 would insta-fail every Job, so any positive value yields at least a 1s deadline. Clamped
// because the apiserver rejects an activeDeadlineSeconds beyond int32, which would turn every
// Create into an error loop rather than a validation failure the user sees.
func deadlineSeconds(timeout time.Duration) int64 {
	seconds := math.Ceil(timeout.Seconds())
	if seconds > math.MaxInt32 {
		return math.MaxInt32
	}
	return int64(seconds)
}

// packageTolerations returns the tolerations on every package pod. Node selection and
// introspection block a node on exactly the taints these do not tolerate.
func packageTolerations(skyhook *wrapper.Skyhook, runtimeRequiredTolerations []corev1.Toleration) []corev1.Toleration {
	return append(append([]corev1.Toleration{ // tolerate all cordon
		{
			Key:      TaintUnschedulable,
			Operator: corev1.TolerationOpExists,
		},
	}, runtimeRequiredTolerations...), skyhook.Spec.AdditionalTolerations...)
}

// createPodFromPackage creates a pod spec for a skyhook pod for a given package
func createPodFromPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage) *corev1.Pod {
	// Generate consistent names that won't exceed k8s limits
	volumeName := generateSafeName(63, "metadata", nodeName)
	configMapName := generateSafeName(253, skyhook.Name, nodeName, "metadata")

	volumes := []corev1.Volume{
		{
			Name: volumeNameRootMount,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
		{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:             volumeNameRootMount,
			MountPath:        mountPathRoot,
			MountPropagation: ptr(corev1.MountPropagationHostToContainer),
		},
		{
			Name:      volumeName,
			MountPath: "/skyhook-package/node-metadata",
		},
	}

	if len(_package.ConfigMap) > 0 {
		volumes = append(volumes, corev1.Volume{
			Name: _package.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: strings.ToLower(fmt.Sprintf("%s-%s-%s", skyhook.Name, _package.Name, _package.Version)),
					},
				},
			},
		})

		// Mount each configMap key as its own subPath rather than mounting the
		// whole configMap as a directory. A directory mount replaces the entire
		// path, hiding any files the package image baked in under
		// mountPathConfigMaps; per-key subPath mounts overlay individual files
		// on top of the image content instead. Trade-off: subPath mounts do not
		// receive live configMap updates, which is fine here because package
		// pods are recreated per stage / version bump. Keys are iterated in
		// sorted order so the generated pod spec is deterministic.
		configMapKeys := make([]string, 0, len(_package.ConfigMap))
		for key := range _package.ConfigMap {
			configMapKeys = append(configMapKeys, key)
		}
		sort.Strings(configMapKeys)

		for _, key := range configMapKeys {
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      _package.Name,
				MountPath: fmt.Sprintf("%s/%s", mountPathConfigMaps, key),
				SubPath:   key,
				ReadOnly:  true,
			})
		}
	}

	copyDir := fmt.Sprintf("%s/%s/%s-%s-%s-%d",
		opts.CopyDirRoot,
		skyhook.Name,
		_package.Name,
		_package.Version,
		skyhook.UID,
		skyhook.Generation,
	)
	applyargs := []string{strings.ToLower(string(stage)), mountPathRoot, copyDir}
	checkargs := []string{strings.ToLower(string(stage) + "-check"), mountPathRoot, copyDir}

	agentEnvs := append(
		_package.Env,
		getAgentConfigEnvVars(opts, _package.Name, _package.Version, skyhook.ResourceID(), skyhook.Name, skyhook.NodeOrder(nodeName))...,
	)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateSafeName(63, skyhook.Name, _package.Name, _package.Version, string(stage), nodeName),
			Namespace: opts.Namespace,
			Labels: map[string]string{
				fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):    skyhook.Name,
				fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX): fmt.Sprintf("%s-%s", _package.Name, _package.Version),
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyOnFailure,
			InitContainers: []corev1.Container{
				{
					Name:            packageCopyContainerName(_package),
					Image:           getPackageImage(_package),
					ImagePullPolicy: corev1.PullAlways,
					Command:         []string{shellBinary},
					Args: []string{
						"-c",
						"mkdir -p /root/${SKYHOOK_DIR} && cp -r /skyhook-package/* /root/${SKYHOOK_DIR}",
					},
					Env: []corev1.EnvVar{
						{
							Name:  "SKYHOOK_DIR",
							Value: copyDir,
						},
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
				},
				{
					Name:            fmt.Sprintf("%s-%s", trunstr(_package.Name, 43), stage),
					Image:           getAgentImage(opts, _package),
					ImagePullPolicy: corev1.PullAlways,
					Args:            applyargs,
					Env:             agentEnvs,
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
				},
				{
					Name:            fmt.Sprintf("%s-%scheck", trunstr(_package.Name, 43), stage),
					Image:           getAgentImage(opts, _package),
					ImagePullPolicy: corev1.PullAlways,
					Args:            checkargs,
					Env:             agentEnvs,
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
				},
			},
			Containers: []corev1.Container{
				{
					Name:  pauseContainerName,
					Image: opts.PauseImage,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
					},
				},
			},
			Volumes:     volumes,
			HostPID:     true,
			HostNetwork: true,
			Tolerations: packageTolerations(skyhook, opts.GetRuntimeRequiredTolerations()),
		},
	}
	setPackageServiceAccount(opts, pod)
	if _package.GracefulShutdown != nil {
		pod.Spec.TerminationGracePeriodSeconds = ptr(int64(_package.GracefulShutdown.Duration.Seconds()))
	}
	setPodResources(pod, _package.Resources)
	return pod
}

// packageCopyContainerName names the init container that copies a package out of its image.
func packageCopyContainerName(_package *v1alpha1.Package) string {
	return fmt.Sprintf("%s-init", trunstr(_package.Name, 43))
}

// createInterruptPodForPackage returns the pod spec for an interrupt pod given an package
func createInterruptPodForPackage(opts SkyhookOperatorOptions, _interrupt *v1alpha1.Interrupt, argEncode string, _package *v1alpha1.Package, skyhook *wrapper.Skyhook, nodeName string, stage v1alpha1.Stage) *corev1.Pod {
	copyDir := fmt.Sprintf("%s/%s/%s-%s-%s-%d",
		opts.CopyDirRoot,
		skyhook.Name,
		_package.Name,
		_package.Version,
		skyhook.UID,
		skyhook.Generation,
	)

	volumes := []corev1.Volume{
		{
			Name: volumeNameRootMount,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
		{
			// node names in different CSPs might include dots which isn't allowed in volume names
			// so we have to replace all dots with dashes
			Name: generateSafeName(63, skyhook.Name, nodeName, "metadata"),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						// Must match how the ConfigMap is actually named at creation
						// (skyhook_controller.go), which hashes and truncates -- an ad-hoc
						// format string here resolves to a ConfigMap that does not exist.
						Name: generateSafeName(253, skyhook.Name, nodeName, "metadata"),
					},
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:             volumeNameRootMount,
			MountPath:        mountPathRoot,
			MountPropagation: ptr(corev1.MountPropagationHostToContainer),
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generateSafeName(63, skyhook.Name, string(stage), string(_interrupt.Type), nodeName),
			Namespace: opts.Namespace,
			Labels: map[string]string{
				fmt.Sprintf("%s/name", v1alpha1.METADATA_PREFIX):      skyhook.Name,
				fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX):   fmt.Sprintf("%s-%s", _package.Name, _package.Version),
				fmt.Sprintf("%s/interrupt", v1alpha1.METADATA_PREFIX): interruptLabelValue,
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyOnFailure,
			InitContainers: []corev1.Container{
				{
					Name:  InterruptContainerName,
					Image: getAgentImage(opts, _package),
					Args:  []string{"interrupt", mountPathRoot, copyDir, argEncode},
					Env:   getAgentConfigEnvVars(opts, _package.Name, _package.Version, skyhook.ResourceID(), skyhook.Name, skyhook.NodeOrder(nodeName)),
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr(true),
					},
					VolumeMounts: volumeMounts,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse(interruptContainerMemory),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse(interruptContainerMemory),
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  pauseContainerName,
					Image: opts.PauseImage,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("20Mi"),
						},
					},
				},
			},
			HostPID:     true,
			HostNetwork: true,
			Tolerations: packageTolerations(skyhook, opts.GetRuntimeRequiredTolerations()),
			Volumes:     volumes,
		},
	}
	setPackageServiceAccount(opts, pod)
	return pod
}

// jobMatchesPackage reports whether an existing stage Job still matches what the operator
// would build for this package+stage now. It is the Job analogue of podMatchesPackage, used
// to decide on an AlreadyExists race or a validation sweep whether a Job is stale and must
// be replaced.
//
// podMatchesPackage compares only the package label, the interrupt label and the init containers'
// names and envPackageRef (to pick which expected executor to build), and per-init-container
// name/image/env/resources, all of which live on parts of the pod the Job builder copies through
// unchanged (the init-container chain and the template labels). The Job-specific differences
// (the done container, whichever image it runs, restartPolicy, extra tolerations,
// podFailurePolicy) are pod-level fields podMatchesPackage never reads, so evaluating it on the
// Job's pod template gives the same answer as on the equivalent raw pod. Reuse it rather than
// duplicate (and risk drifting) the compare.
func jobMatchesPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, job batchv1.Job, skyhook *wrapper.Skyhook, stage v1alpha1.Stage) bool {
	templatePod := corev1.Pod{
		ObjectMeta: job.Spec.Template.ObjectMeta,
		Spec:       job.Spec.Template.Spec,
	}
	return podMatchesPackage(opts, _package, templatePod, skyhook, stage)
}

// podMatchesPackage reports whether a package or interrupt pod still matches what the operator
// would build for this package and stage now. Which compare applies is read from the pod and the
// stage, never from a preflight result: the interrupt label picks the interrupt compare, the
// preflight stage the preflight compare, and a pod isArtifactPod recognises the artifact compare;
// any other pod is a legacy stage pod.
//
// A preflight pod is compared with its envPreflightRef, so a changed image, version or
// containerSHA makes it stale. An artifact pod is compared with the artifact pod the operator
// would build now, with envPackageRef left out: which digest it pins comes from the preflight
// result on its node, which stageJobMatchesPreflight compares. envRegistryAuthFiles is left out of
// both, since the pull secrets of the package ServiceAccount are read when a Job is built.
func podMatchesPackage(opts SkyhookOperatorOptions, _package *v1alpha1.Package, pod corev1.Pod, skyhook *wrapper.Skyhook, stage v1alpha1.Stage) bool {
	var expectedPod *corev1.Pod

	// need to differentiate whether the pod is for an interrupt or not so we know
	// what to expect and how to compare them
	isInterrupt := false
	isArtifact := false
	_, limitRange := pod.Annotations["kubernetes.io/limit-ranger"]

	switch {
	case pod.Labels[fmt.Sprintf("%s/interrupt", v1alpha1.METADATA_PREFIX)] == interruptLabelValue:
		expectedPod = createInterruptPodForPackage(opts, &v1alpha1.Interrupt{}, "", _package, skyhook, "", stage)
		isInterrupt = true
	case stage == v1alpha1.StagePreflight:
		reference, err := packageReference(_package)
		if err != nil {
			return false
		}
		expectedPod = createPreflightPodFromPackage(opts, _package, skyhook, "", reference, nil)
	case isArtifactPod(_package, pod):
		expectedPod = createArtifactPodFromPackage(opts, _package, skyhook, "", stage, "", nil)
		isArtifact = true
	default:
		expectedPod = createPodFromPackage(opts, _package, skyhook, "", stage)
	}

	actualPod := pod.DeepCopy()

	// check to see whether the name or the version of the package changed
	packageLabel := fmt.Sprintf("%s/package", v1alpha1.METADATA_PREFIX)
	if actualPod.Labels[packageLabel] != expectedPod.Labels[packageLabel] {
		return false
	}

	// A differing count is itself a mismatch, and checking it up front is what keeps the
	// indexing below in bounds: the loop walks the actual containers but indexes the
	// expected slice, so an actual pod carrying an extra init container (an admission
	// webhook injecting one, say) would otherwise panic the reconcile.
	if len(actualPod.Spec.InitContainers) != len(expectedPod.Spec.InitContainers) {
		return false
	}

	// compare initContainers since this is where a lot of the important info lives
	for i := range actualPod.Spec.InitContainers {
		expectedContainer := expectedPod.Spec.InitContainers[i]
		actualContainer := actualPod.Spec.InitContainers[i]

		if expectedContainer.Name != actualContainer.Name {
			return false
		}

		if expectedContainer.Image != actualContainer.Image {
			return false
		}

		// compare the containers env vars except for the ones that are inserted
		// by the operator by default as the SKYHOOK_RESOURCE_ID will change every
		// time the skyhook is updated and would cause every pod to be removed
		// TODO: This is ignoring all the static env vars that are set by operator config.
		// It probably should be just SKYHOOK_RESOURCE_ID that is ignored. Otherwise,
		// a user will have to manually delete the pod to update the package when operator is updated.
		dummyAgentEnv := getAgentConfigEnvVars(opts, "", "", "", "", 0)
		excludedEnvs := make([]string, len(dummyAgentEnv))
		for i, env := range dummyAgentEnv {
			excludedEnvs[i] = env.Name
		}
		excludedEnvs = append(excludedEnvs, envRegistryAuthFiles)
		if isArtifact {
			excludedEnvs = append(excludedEnvs, envPackageRef)
		}
		expectedFilteredEnv := FilterEnv(expectedContainer.Env, excludedEnvs...)
		actualFilteredEnv := FilterEnv(actualContainer.Env, excludedEnvs...)
		if !reflect.DeepEqual(expectedFilteredEnv, actualFilteredEnv) {
			return false
		}

		if !isInterrupt { // dont compare these since they are not configured on interrupt
			// compare resource requests and limits (CPU, memory, etc.)
			expectedResources := expectedContainer.Resources
			actualResources := actualContainer.Resources
			if skyhook.Spec.Packages[_package.Name].Resources != nil {
				// Semantic, not reflect: apimachinery rewrites a quantity into its canonical
				// form when the pod is serialized ("4000m" -> "4", "8192Mi" -> "8Gi"), so a
				// pod read back from the apiserver is never byte-equal to the one we built.
				// reflect.DeepEqual compares Quantity's unexported cached string and reports
				// a mismatch that isn't one, which invalidates and recreates the Job forever.
				if !apiequality.Semantic.DeepEqual(expectedResources, actualResources) {
					return false
				}
			} else {
				// If CR has no resources specified, ensure pod has no resource overrides
				if !limitRange {
					if actualResources.Requests != nil || actualResources.Limits != nil {
						return false
					}
				}
			}
		}
	}

	return true
}

// stageJobMatchesPreflight reports whether job, the Job of a stage other than preflight and the
// interrupts, is the Job createStageJob builds from result, the preflight result of its package on
// its node: an artifact Job pinned to the recorded digest for an accepted artifact, and a legacy
// Job otherwise.
func stageJobMatchesPreflight(job *batchv1.Job, packageName string, result *v1alpha1.PreflightResult) bool {
	pod := corev1.Pod{ObjectMeta: job.Spec.Template.ObjectMeta, Spec: job.Spec.Template.Spec}
	if !isArtifactPod(&v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: packageName}}, pod) {
		return !preflightAccepted(result)
	}
	if !preflightAccepted(result) {
		return false
	}
	for _, container := range pod.Spec.InitContainers {
		if pinned, ok := operatorPackageRef(container); ok && pinned != result.Pinned {
			return false
		}
	}
	return true
}

// isArtifactPod reports whether pod is an artifact stage pod: one with no init-copy container for
// _package and with envPackageRef on an init container. The env alone does not tell, because the
// stage and check containers of a legacy stage pod carry the package's own env, which may name
// envPackageRef too.
func isArtifactPod(_package *v1alpha1.Package, pod corev1.Pod) bool {
	copyContainer := packageCopyContainerName(_package)
	if slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool { return c.Name == copyContainer }) {
		return false
	}
	return slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool {
		return slices.ContainsFunc(c.Env, func(env corev1.EnvVar) bool { return env.Name == envPackageRef })
	})
}

// operatorPackageRef returns the value of the last envPackageRef of container: the one the operator
// appends after the package's own env, which is also the one the container gets, since the kubelet
// gives a repeated name its last value.
func operatorPackageRef(container corev1.Container) (string, bool) {
	for i := len(container.Env) - 1; i >= 0; i-- {
		if container.Env[i].Name == envPackageRef {
			return container.Env[i].Value, true
		}
	}
	return "", false
}
