// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
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

package wrapper_test

import (
	"fmt"
	"testing"

	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type namedNode struct {
	wrapper.SkyhookNode
	node *corev1.Node
}

func (n namedNode) GetNode() *corev1.Node { return n.node }

func BenchmarkCompartmentAddNode(b *testing.B) {
	for _, size := range []int{1000, 5000, 20000} {
		nodes := make([]wrapper.SkyhookNode, size)
		for i := range nodes {
			name := fmt.Sprintf("ip-10-%d-%d-%d.us-west-2.compute.internal", i>>16&255, i>>8&255, i&255)
			nodes[i] = namedNode{node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}}
		}
		b.Run(fmt.Sprintf("nodes=%d", size), func(b *testing.B) {
			c := &wrapper.Compartment{}
			for b.Loop() {
				c.ClearNodes()
				for _, n := range nodes {
					c.AddNode(n)
				}
			}
		})
	}
}
