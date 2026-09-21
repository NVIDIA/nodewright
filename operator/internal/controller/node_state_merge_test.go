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
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// countingReader stands in for the apiserver-direct reader, recording that it was consulted and
// serving a node the cached client does not have. That difference is what proves which branch of
// readNodeForPatch a given attempt took.
type countingReader struct {
	client.Reader
	node  *corev1.Node
	calls int
}

func (c *countingReader) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	c.calls++
	if c.node == nil {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, "missing")
	}
	node, ok := obj.(*corev1.Node)
	if !ok {
		return apierrors.NewBadRequest("not a node")
	}
	c.node.DeepCopyInto(node)
	return nil
}

var _ = Describe("node state delta merge", func() {
	status := func(name string, state v1alpha1.State, stage v1alpha1.Stage) v1alpha1.PackageStatus {
		return v1alpha1.PackageStatus{Name: name, Version: "1.0.0", Image: "img", State: state, Stage: stage}
	}

	Describe("computeNodeStateDelta", func() {
		It("reports only the entries the pass actually changed", func() {
			before := v1alpha1.NodeState{
				"a|1.0.0": status("a", v1alpha1.StateInProgress, v1alpha1.StageApply),
				"b|1.0.0": status("b", v1alpha1.StateInProgress, v1alpha1.StageApply),
			}
			after := v1alpha1.NodeState{
				"a|1.0.0": status("a", v1alpha1.StateInProgress, v1alpha1.StageApply), // untouched
				"b|1.0.0": status("b", v1alpha1.StateComplete, v1alpha1.StageApply),   // changed
			}

			delta := computeNodeStateDelta(before, after)
			Expect(delta).To(HaveLen(1))
			Expect(delta).To(HaveKey("b|1.0.0"))
			Expect(delta["b|1.0.0"].State).To(Equal(v1alpha1.StateComplete))
		})

		It("records an added entry and a removed one", func() {
			before := v1alpha1.NodeState{"gone|1.0.0": status("gone", v1alpha1.StateComplete, v1alpha1.StageConfig)}
			after := v1alpha1.NodeState{"new|1.0.0": status("new", v1alpha1.StateInProgress, v1alpha1.StageApply)}

			delta := computeNodeStateDelta(before, after)
			Expect(delta).To(HaveLen(2))
			Expect(delta["new|1.0.0"]).ToNot(BeNil())
			Expect(delta["gone|1.0.0"]).To(BeNil(), "a nil value marks a removal")
		})
	})

	Describe("apply", func() {
		// The whole point: an entry the pass never touched keeps whatever another writer put
		// there, rather than being restamped from the pass's snapshot.
		It("preserves a concurrent write to an entry the pass did not touch", func() {
			before := v1alpha1.NodeState{
				"a|1.0.0": status("a", v1alpha1.StateInProgress, v1alpha1.StageApply),
				"b|1.0.0": status("b", v1alpha1.StateInProgress, v1alpha1.StageApply),
			}
			after := before.DeepCopy()
			after["b|1.0.0"] = status("b", v1alpha1.StateComplete, v1alpha1.StageApply)

			// meanwhile JobReconciler recorded package a complete
			current := before.DeepCopy()
			current["a|1.0.0"] = status("a", v1alpha1.StateComplete, v1alpha1.StageApply)

			merged := computeNodeStateDelta(before, after).apply(current)

			Expect(merged["a|1.0.0"].State).To(Equal(v1alpha1.StateComplete), "concurrent completion must survive")
			Expect(merged["b|1.0.0"].State).To(Equal(v1alpha1.StateComplete), "the pass's own change must land")
		})

		It("keeps an entry another NodeWright added after the snapshot", func() {
			before := v1alpha1.NodeState{"a|1.0.0": status("a", v1alpha1.StateInProgress, v1alpha1.StageApply)}
			after := v1alpha1.NodeState{"a|1.0.0": status("a", v1alpha1.StateComplete, v1alpha1.StageApply)}
			current := v1alpha1.NodeState{
				"a|1.0.0": status("a", v1alpha1.StateInProgress, v1alpha1.StageApply),
				"c|2.0.0": status("c", v1alpha1.StateInProgress, v1alpha1.StageConfig),
			}

			merged := computeNodeStateDelta(before, after).apply(current)
			Expect(merged).To(HaveKey("c|2.0.0"))
			Expect(merged["a|1.0.0"].State).To(Equal(v1alpha1.StateComplete))
		})

		It("applies a removal even against a changed current value", func() {
			before := v1alpha1.NodeState{"a|1.0.0": status("a", v1alpha1.StateInProgress, v1alpha1.StageUninstall)}
			after := v1alpha1.NodeState{}
			current := v1alpha1.NodeState{"a|1.0.0": status("a", v1alpha1.StateErroring, v1alpha1.StageUninstall)}

			merged := computeNodeStateDelta(before, after).apply(current)
			Expect(merged).ToNot(HaveKey("a|1.0.0"))
		})
	})
})

var _ = Describe("saveNodeChanges", func() {
	const skyhookName = "gpu-init"
	const nodeName = "worker-1"
	key := nodeStateAnnotationKey(skyhookName)

	status := func(name string, state v1alpha1.State) v1alpha1.PackageStatus {
		return v1alpha1.PackageStatus{Name: name, Version: "1.0.0", Image: "img", State: state, Stage: v1alpha1.StageApply}
	}
	stateJSON := func(state v1alpha1.NodeState) string {
		raw, err := json.Marshal(state)
		Expect(err).ToNot(HaveOccurred())
		return string(raw)
	}

	// This is the regression the change exists for: the heavy pass computes its result from a
	// snapshot, JobReconciler records a completion in between, and then the pass writes. Restamping
	// the whole snapshot value reverted that completion, and nothing re-recorded it because the Job
	// was already marked state-recorded.
	It("does not revert a completion recorded after the pass took its snapshot", func() {
		snapshot := v1alpha1.NodeState{
			"a|1.0.0": status("a", v1alpha1.StateInProgress),
			"b|1.0.0": status("b", v1alpha1.StateInProgress),
		}
		original := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:        nodeName,
			Annotations: map[string]string{key: stateJSON(snapshot)},
			Labels:      map[string]string{"keep": "yes", "pass-drops": "soon"},
		}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{
			{Key: "nodewright.nvidia.com", Value: "runtime-required", Effect: corev1.TaintEffectNoSchedule},
		}}}

		// what is actually stored by the time the pass writes: package a completed by JobReconcile,
		// plus a pile of metadata this Skyhook has no business touching that landed after the
		// snapshot — a foreign label, another NodeWright's state key, a foreign taint, and a cordon
		// somebody else applied.
		storedState := snapshot.DeepCopy()
		storedState["a|1.0.0"] = status("a", v1alpha1.StateComplete)
		stored := original.DeepCopy()
		stored.Annotations[key] = stateJSON(storedState)
		stored.Annotations["nodewright.nvidia.com/nodeState_other"] = "{}"
		stored.Labels["foo"] = "bar"
		stored.Spec.Unschedulable = true
		stored.Spec.Taints = append(stored.Spec.Taints, corev1.Taint{
			Key: "ToBeDeletedByClusterAutoscaler", Value: "1", Effect: corev1.TaintEffectNoSchedule,
			TimeAdded: &metav1.Time{Time: time.Unix(123, 0)},
		})

		scr := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: "skyhook"}}

		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored, scr).Build()

		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), events.NewFakeRecorder(10),
			SkyhookOperatorOptions{
				Namespace:            "skyhook",
				CopyDirRoot:          "/var/lib/skyhook",
				AgentLogRoot:         "/var/log/skyhook",
				RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
				AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
				PauseImage:           "registry.k8s.io/pause:3.10",
				MaxInterval:          10 * time.Minute,
				JobOperatorOptions: JobOperatorOptions{
					JobTTLSucceeded: time.Hour,
					JobTTLFailed:    24 * time.Hour,
					JobStageTimeout: time.Hour,
				},
			})
		Expect(err).ToNot(HaveOccurred())

		// the pass advanced package b, still believing a is in_progress, and made its own label edits
		passNode := original.DeepCopy()
		passState := snapshot.DeepCopy()
		passState["b|1.0.0"] = status("b", v1alpha1.StateComplete)
		passNode.Annotations[key] = stateJSON(passState)
		passNode.Labels["pass-adds"] = "1"
		delete(passNode.Labels, "pass-drops")
		passNode.Spec.Taints = nil // the pass removed its runtime-required taint
		sn, err := wrapper.NewSkyhookNode(passNode, scr)
		Expect(err).ToNot(HaveOccurred())

		Expect(r.saveNodeChanges(ctx, original, sn, skyhookName)).To(Succeed())

		var got corev1.Node
		Expect(c.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
		final, err := parseNodeState(&got, key)
		Expect(err).ToNot(HaveOccurred())

		Expect(final["a|1.0.0"].State).To(Equal(v1alpha1.StateComplete), "concurrent completion must survive the pass write")
		Expect(final["b|1.0.0"].State).To(Equal(v1alpha1.StateComplete), "the pass's own transition must land")

		// The diff is taken against the pass's own snapshot, so anything in neither side is not in
		// the patch and cannot be disturbed. These assertions are what pins that: basing the diff on
		// the fresh read instead makes every one of them a deletion the pass never intended, and
		// then needs a hand-rolled replay of each metadata surface to undo it. Verified — swapping
		// the base back to the fresh read fails this spec.
		Expect(got.Labels).To(HaveKeyWithValue("foo", "bar"), "a foreign label must survive")
		Expect(got.Annotations).To(HaveKey("nodewright.nvidia.com/nodeState_other"), "another NodeWright's key must survive")
		Expect(got.Spec.Taints).To(HaveLen(1), "a foreign taint must survive")
		Expect(got.Spec.Taints[0].Key).To(Equal("ToBeDeletedByClusterAutoscaler"))
		Expect(got.Spec.Taints[0].TimeAdded).To(Equal(&metav1.Time{Time: time.Unix(123, 0)}), "foreign taint metadata must survive")
		Expect(got.Spec.Unschedulable).To(BeTrue(), "a cordon this pass never touched must survive")

		Expect(got.Labels).To(HaveKeyWithValue("pass-adds", "1"), "the pass's own addition must land")
		Expect(got.Labels).ToNot(HaveKey("pass-drops"), "the pass's own deletion must land")

		// The wrapper caches a parsed copy of node state and State() serves it whenever it is
		// non-nil, so the merged annotation has to invalidate it. Without that, IsComplete,
		// NextStage and UpdateCondition keep answering from the pass's pre-merge map — and the
		// condition patch that runs next in SaveNodesAndSkyhook publishes the wrong answer.
		cached, err := sn.State()
		Expect(err).ToNot(HaveOccurred())
		Expect(cached["a|1.0.0"].State).To(Equal(v1alpha1.StateComplete),
			"the wrapper must re-read the merged annotation, not serve its pre-merge cache")

		// State() re-parses when the cache is nil, so asserting only through it would pass even
		// with the cache nilled out. IsComplete, NextStage, GetComplete and PackageStatus read the
		// cache field DIRECTLY, so they are what actually pins the re-seed: a nil cache reads as
		// "no package has any state", a completed node reports incomplete, and the rollout stalls.
		// That escaped unit coverage once and only cli-e2e caught it.
		gotStatus, found := sn.PackageStatus("a|1.0.0")
		Expect(found).To(BeTrue(), "the direct-cache accessors must see the merged state")
		Expect(gotStatus.State).To(Equal(v1alpha1.StateComplete))
	})

	// Reset() deletes the annotation outright and means it. Merging is keyed on the pass having
	// kept the key, because a delta of pure removals would otherwise be re-marshalled and written
	// back as "{}" — resurrecting the key the pass just wiped, and leaving a node that reads as
	// tracked-with-no-packages instead of untracked.
	It("carries the pass's deletion instead of re-merging when the annotation was wiped", func() {
		snapshot := v1alpha1.NodeState{"a|1.0.0": status("a", v1alpha1.StateComplete)}
		original := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: nodeName, Annotations: map[string]string{key: stateJSON(snapshot)},
		}}
		stored := original.DeepCopy()

		scr := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: "skyhook"}}

		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored, scr).Build()

		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), events.NewFakeRecorder(10),
			SkyhookOperatorOptions{
				Namespace:            "skyhook",
				CopyDirRoot:          "/var/lib/skyhook",
				AgentLogRoot:         "/var/log/skyhook",
				RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
				AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
				PauseImage:           "registry.k8s.io/pause:3.10",
				MaxInterval:          10 * time.Minute,
				JobOperatorOptions: JobOperatorOptions{
					JobTTLSucceeded: time.Hour,
					JobTTLFailed:    24 * time.Hour,
					JobStageTimeout: time.Hour,
				},
			})
		Expect(err).ToNot(HaveOccurred())

		// what Reset() leaves behind: the key gone from the pass's object
		passNode := original.DeepCopy()
		delete(passNode.Annotations, key)
		sn, err := wrapper.NewSkyhookNode(passNode, scr)
		Expect(err).ToNot(HaveOccurred())

		Expect(r.saveNodeChanges(ctx, original, sn, skyhookName)).To(Succeed())

		var got corev1.Node
		Expect(c.Get(ctx, types.NamespacedName{Name: nodeName}, &got)).To(Succeed())
		Expect(got.Annotations).ToNot(HaveKey(key), "the pass's wipe must land, not come back as {}")
	})
})

var _ = Describe("saveNodeChanges conflict retry", func() {
	const skyhookName = "gpu-init"
	const nodeName = "worker-1"
	key := nodeStateAnnotationKey(skyhookName)

	status := func(name string, state v1alpha1.State) v1alpha1.PackageStatus {
		return v1alpha1.PackageStatus{Name: name, Version: "1.0.0", Image: "img", State: state, Stage: v1alpha1.StageApply}
	}
	stateJSON := func(state v1alpha1.NodeState) string {
		raw, err := json.Marshal(state)
		Expect(err).ToNot(HaveOccurred())
		return string(raw)
	}
	opts := func() SkyhookOperatorOptions {
		return SkyhookOperatorOptions{
			Namespace: "skyhook", CopyDirRoot: "/var/lib/skyhook", AgentLogRoot: "/var/log/skyhook",
			RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
			AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
			PauseImage:           "registry.k8s.io/pause:3.10", MaxInterval: 10 * time.Minute,
			JobOperatorOptions: JobOperatorOptions{
				JobTTLSucceeded: time.Hour, JobTTLFailed: 24 * time.Hour, JobStageTimeout: time.Hour,
			},
		}
	}

	// The optimistic lock only helps if a conflict is actually retried AND the retry re-derives
	// against a fresh read. Both were previously uncovered: nothing in the suite forced a
	// conflict, so RetryOnConflict and the uncached branch of readNodeForPatch never ran.
	It("retries a conflict, re-reads uncached, and re-merges against what it finds", func() {
		snapshot := v1alpha1.NodeState{
			"a|1.0.0": status("a", v1alpha1.StateInProgress),
			"b|1.0.0": status("b", v1alpha1.StateInProgress),
		}
		original := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: nodeName, ResourceVersion: "100",
			Annotations: map[string]string{key: stateJSON(snapshot)},
		}}

		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

		scr := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: "skyhook"}}
		stored := original.DeepCopy()
		base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored, scr).Build()

		// Attempt 0 conflicts, as it would when another writer landed first.
		patches := 0
		c := interceptor.NewClient(base, interceptor.Funcs{
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, p client.Patch, o ...client.PatchOption) error {
				patches++
				if patches == 1 {
					return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, nodeName,
						fmt.Errorf("simulated concurrent write"))
				}
				return cl.Patch(ctx, obj, p, o...)
			},
		})

		// What only the apiserver knows: JobReconciler completed package a.
		uncachedState := snapshot.DeepCopy()
		uncachedState["a|1.0.0"] = status("a", v1alpha1.StateComplete)
		fromAPIServer := original.DeepCopy()
		fromAPIServer.Annotations[key] = stateJSON(uncachedState)
		reader := &countingReader{node: fromAPIServer}

		r, err := NewSkyhookReconciler(scheme, c, reader, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts())
		Expect(err).ToNot(HaveOccurred())

		// The pass advanced package b only.
		passNode := original.DeepCopy()
		passState := snapshot.DeepCopy()
		passState["b|1.0.0"] = status("b", v1alpha1.StateComplete)
		passNode.Annotations[key] = stateJSON(passState)
		sn, err := wrapper.NewSkyhookNode(passNode, scr)
		Expect(err).ToNot(HaveOccurred())

		Expect(r.saveNodeChanges(ctx, original, sn, skyhookName)).To(Succeed())

		Expect(patches).To(BeNumerically(">=", 2), "the conflict must be retried, not surfaced")
		Expect(reader.calls).To(Equal(1), "attempt 0 reads cached; only the retry goes to the apiserver")

		final, err := parseNodeState(sn.GetNode(), key)
		Expect(err).ToNot(HaveOccurred())
		Expect(final["a|1.0.0"].State).To(Equal(v1alpha1.StateComplete),
			"the retry must merge onto the uncached read, keeping the completion only it could see")
		Expect(final["b|1.0.0"].State).To(Equal(v1alpha1.StateComplete), "the pass's own transition must still land")
	})

	// CodeRabbit asked for a test asserting the node still changed here. That was right for the
	// original code, which diffed the object against a copy of itself — an empty patch that
	// silently dropped the write. The branch now returns an error instead, because BuildState
	// tracks and adds a node in the same step so a nil snapshot means a broken invariant, and the
	// one thing it must not do is look like success.
	It("errors rather than silently dropping the write when a node has no snapshot", func() {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

		scr := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: "skyhook"}}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, scr).Build()

		r, err := NewSkyhookReconciler(scheme, c, c, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts())
		Expect(err).ToNot(HaveOccurred())

		sn, err := wrapper.NewSkyhookNode(node.DeepCopy(), scr)
		Expect(err).ToNot(HaveOccurred())

		err = r.saveNodeChanges(ctx, nil, sn, skyhookName)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no tracked snapshot"))
	})

	It("stops without error when the retry finds the node deleted", func() {
		original := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: nodeName, ResourceVersion: "100",
			Annotations: map[string]string{key: stateJSON(v1alpha1.NodeState{"a|1.0.0": status("a", v1alpha1.StateInProgress)})},
		}}

		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

		scr := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: skyhookName, Namespace: "skyhook"}}
		base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(original.DeepCopy(), scr).Build()
		c := interceptor.NewClient(base, interceptor.Funcs{
			Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
				return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, nodeName, fmt.Errorf("gone"))
			},
		})

		reader := &countingReader{node: nil} // apiserver says NotFound
		r, err := NewSkyhookReconciler(scheme, c, reader, k8sfake.NewClientset(), events.NewFakeRecorder(10), opts())
		Expect(err).ToNot(HaveOccurred())

		passNode := original.DeepCopy()
		sn, err := wrapper.NewSkyhookNode(passNode, scr)
		Expect(err).ToNot(HaveOccurred())

		// A node that went away mid-pass has no state to resurrect; that is not an error.
		Expect(r.saveNodeChanges(ctx, original, sn, skyhookName)).To(Succeed())
		Expect(reader.calls).To(BeNumerically(">=", 1))
	})
})
