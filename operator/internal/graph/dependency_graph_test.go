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

package graph

import (
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDependencyGraph(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "DAG Suite")
}

var _ = Describe("DAG tests", func() {

	It("test building when created in broken order", func() {
		/* Graph structure:
		  A   E
		  |
		  B
		 / \
		C   D
		*/

		d := New[struct{ a string }]()

		payload := struct{ a string }{a: "bar"}

		Expect(d.Add("A", payload)).Should(Succeed())
		Expect(d.Add("C", payload, "B")).Should(Succeed())
		Expect(d.Add("B", payload, "A")).Should(Succeed())
		Expect(d.Add("D", payload, "A", "B")).Should(Succeed())
		Expect(d.Add("E", payload)).Should(Succeed())

		dag, ok := d.(*dag[struct{ a string }])
		Expect(ok).To(BeTrue())

		// make sure the relationships are correct
		Expect(dag.vertices["A"].edges[0] == dag.vertices["B"]).To(BeTrue())
		Expect(dag.vertices["B"].edges[0] == dag.vertices["C"]).To(BeTrue())

		By("walk the graph")

		completed := make([]string, 0)
		step, err := d.Next(completed...)
		Expect(err).To(BeNil())
		Expect(ok).To(BeTrue())
		Expect(step).To(BeEquivalentTo([]string{"A", "E"}))
		completed = append(completed, step...)

		step, _ = d.Next(completed...)
		Expect(step).To(BeEquivalentTo([]string{"B"}))
		completed = append(completed, step...)

		step, _ = d.Next(completed...)
		Expect(step).To(BeEquivalentTo([]string{"C", "D"}))
		completed = append(completed, step...)

		payloads := d.Get(step...)
		Expect(payloads).To(BeEquivalentTo([]struct{ a string }{payload, payload}))

		By("if we go again, should get nothing, we are at the end")
		step, _ = d.Next(completed...)
		Expect(step).To(BeEmpty())

		By("testing printing does not error")

		err = PrintGraph[struct{ a string }](GinkgoWriter, dag)
		Expect(err).To(BeNil())
	})

	It("test error on dup vertex", func() {
		/* Graph structure:
		   A -> A (duplicate, should fail)
		*/

		d := New[*string]()

		Expect(d.Add("A", nil)).Should(Succeed())
		Expect(d.Add("A", nil)).ShouldNot(Succeed())
	})

	It("returns an error when printing an invalid graph", func() {
		d := New[struct{}]()
		Expect(d.Add("A", struct{}{}, "missing")).To(Succeed())

		err := PrintGraph(GinkgoWriter, d)
		Expect(err).To(MatchError(ContainSubstring("graph is not valid")))
	})

	It("walking something even more complicated should work", func() {
		/* Graph structure:
			   A   Z
			   |
			   B
			  / \
			 C   D
			/ \
		       E   G
		       \   /
		         F
		*/

		var d DependencyGraph[*string]

		// setting this test to make sure creating and walking works
		// we are creating the graph in random orders to make sure it works in all orders

		spt := func(s string) *string {
			return &s
		}

		adds := make(map[string]func(), 0)

		adds["F"] = func() {
			//root 1
			Expect(d.Add("F", spt("F"), "E", "B")).Should(Succeed())
		}

		adds["G"] = func() {
			// root 2
			Expect(d.Add("G", spt("G"), "C")).Should(Succeed())
		}

		adds["Z"] = func() {
			// root 3 and leaf
			Expect(d.Add("Z", spt("Z"))).Should(Succeed())
		}

		adds["D"] = func() {
			// root 4
			Expect(d.Add("D", spt("D"), "B", "A")).Should(Succeed())
		}

		adds["E"] = func() {
			// level 2
			Expect(d.Add("E", spt("E"), "C")).Should(Succeed())
		}

		adds["C"] = func() {
			// level 3
			Expect(d.Add("C", spt("C"), "B")).Should(Succeed())
		}

		adds["B"] = func() {
			// level 4
			Expect(d.Add("B", spt("B"), "A")).Should(Succeed())
		}

		adds["A"] = func() {
			// leaf 5
			Expect(d.Add("A", spt("A"))).Should(Succeed())
		}

		order := make([]string, 0)
		for f := range adds {
			order = append(order, f)
		}

		// do in many random orders
		for i := 1; i < len(order); i++ {
			d = New[*string]()

			rand.Shuffle(len(order), func(i, j int) {
				order[i], order[j] = order[j], order[i]
			})

			GinkgoLogr.Info("order", "vertex order", order)
			for _, f := range order {
				adds[f]()
			}

			By("walk the graph we should get the following")
			Expect(PrintGraph(GinkgoWriter, d)).To(Succeed())

			uspta := func(as []*string) []string {
				ret := make([]string, 0)
				for _, s := range as {
					ret = append(ret, *s)
				}
				return ret
			}

			completed := make([]string, 0)
			step, err := d.Next(completed...)
			Expect(err).To(BeNil())
			Expect(step).To(BeEquivalentTo([]string{"A", "Z"}))
			Expect(uspta(d.Get(step...))).To(BeEquivalentTo([]string{"A", "Z"}))
			completed = append(completed, step...)

			step, _ = d.Next(completed...)
			Expect(step).To(BeEquivalentTo([]string{"B"}))
			Expect(uspta(d.Get(step...))).To(BeEquivalentTo([]string{"B"}))
			completed = append(completed, step...)

			step, _ = d.Next(completed...)
			Expect(step).To(BeEquivalentTo([]string{"C", "D"}))
			Expect(uspta(d.Get(step...))).To(BeEquivalentTo([]string{"C", "D"}))
			completed = append(completed, step...)

			step, _ = d.Next(completed...)
			Expect(step).To(BeEquivalentTo([]string{"E", "G"}))
			Expect(uspta(d.Get(step...))).To(BeEquivalentTo([]string{"E", "G"}))
			completed = append(completed, step...)

			step, _ = d.Next(completed...)
			Expect(step).To(BeEquivalentTo([]string{"F"}))
			Expect(uspta(d.Get(step...))).To(BeEquivalentTo([]string{"F"}))
		}
	})

	It("error on walking broken graph", func() {
		/* Graph structure:
		   bar -> foo (foo doesn't exist)
		*/

		d := New[*string]()
		Expect(d.Add("bar", nil, "foo")).Should(Succeed())
		completed := make([]string, 0)
		_, err := d.Next(completed...)
		Expect(err).ToNot(BeNil())
	})

	It("test walking empty graph", func() {
		d := New[*string]()
		completed := make([]string, 0)
		step, err := d.Next(completed...)
		Expect(err).To(BeNil())
		Expect(step).To(BeEmpty())
		completed = append(completed, step...)

		step, err = d.Next(completed...)
		Expect(err).To(BeNil())
		Expect(step).To(BeEmpty())
	})

	It("test walking graph with one vertex", func() {
		/* Graph structure:
		   foo
		*/

		d := New[*string]()
		Expect(d.Add("foo", nil)).Should(Succeed())
		complete := make([]string, 0)
		step, err := d.Next(complete...)
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"foo"}))
	})

	It("should not return duplicates of the same from next", func() {
		/* Graph structure:
		   A   B
		    \ /
		     C
		*/

		d := New[*string]()
		Expect(d.Add("A", nil)).Should(Succeed())
		Expect(d.Add("B", nil)).Should(Succeed())
		Expect(d.Add("C", nil, "A", "B")).Should(Succeed())

		step, err := d.Next()
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"A", "B"}))

		step, err = d.Next("A")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"B"}))

		step, err = d.Next("A", "B")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"C"}))
	})

	It("should return the correct next when called with a multiple step from with a partial last step", func() {
		/* Graph structure:
		   A   B
		    \ /
		     C
		    / \
		   D   E
		    \ /
		     F
		*/

		d := New[*string]()
		Expect(d.Add("A", nil)).Should(Succeed())
		Expect(d.Add("B", nil)).Should(Succeed())
		Expect(d.Add("C", nil, "A", "B")).Should(Succeed())
		Expect(d.Add("D", nil, "C")).Should(Succeed())
		Expect(d.Add("E", nil, "C")).Should(Succeed())
		Expect(d.Add("F", nil, "E", "D")).Should(Succeed())

		Expect(PrintGraph(GinkgoWriter, d)).To(Succeed())

		complete := []string{"A", "B", "C", "E"}
		step, err := d.Next(complete...)
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"D"}))
	})

	It("should still offer a dependency-free vertex once more vertices are complete than there are dependency-free vertices", func() {
		/* Graph structure:
		   A   B   Z
		      / \
		     C   D

		   A, B and Z have no dependencies. Walking outward from the completed
		   set only ever reaches children of completed vertices, and Z is nobody's
		   child, so Z has to be found by scanning rather than by walking.
		*/

		d := New[*string]()
		Expect(d.Add("A", nil)).Should(Succeed())
		Expect(d.Add("B", nil)).Should(Succeed())
		Expect(d.Add("Z", nil)).Should(Succeed())
		Expect(d.Add("C", nil, "B")).Should(Succeed())
		Expect(d.Add("D", nil, "B")).Should(Succeed())

		// four complete, three dependency-free vertices, and Z has never run
		step, err := d.Next("A", "B", "C", "D")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"Z"}))

		// level-triggered: asking again with the same completed set is stable
		step, err = d.Next("A", "B", "C", "D")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"Z"}))

		step, err = d.Next("A", "B", "C", "D", "Z")
		Expect(err).To(BeNil())
		Expect(step).To(BeEmpty())
	})

	It("should offer dependency-free and newly unblocked vertices in the same step", func() {
		/* Graph structure:
		   A   B   Z
		      / \
		     C   D
		*/

		d := New[*string]()
		Expect(d.Add("A", nil)).Should(Succeed())
		Expect(d.Add("B", nil)).Should(Succeed())
		Expect(d.Add("Z", nil)).Should(Succeed())
		Expect(d.Add("C", nil, "B")).Should(Succeed())
		Expect(d.Add("D", nil, "B")).Should(Succeed())

		// B unblocks C and D; A and Z were runnable all along and must not be dropped
		step, err := d.Next("B")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"A", "C", "D", "Z"}))
	})

	It("should keep the dependencies of a vertex that was forward-referenced before it was added", func() {
		/* Graph structure:
		      A
		     / \
		    P   Q
		     \ /
		      Z
		      |
		      X

		   X names Z before Z is added, so Z exists as a placeholder when its own
		   Add promotes it. BuildGraph ranges a Go map, so this order happens at
		   random in production.

		   A sits above P and Q so that the interesting step (only P complete) has
		   a completed set larger than the single dependency-free vertex; with a
		   smaller completed set the answer comes from the dependency-free vertices
		   and Z's parents are never consulted.
		*/

		d := New[*string]()
		Expect(d.Add("X", nil, "Z")).Should(Succeed()) // creates the placeholder for Z
		Expect(d.Add("Z", nil, "P", "Q")).Should(Succeed())
		Expect(d.Add("P", nil, "A")).Should(Succeed())
		Expect(d.Add("Q", nil, "A")).Should(Succeed())
		Expect(d.Add("A", nil)).Should(Succeed())

		step, err := d.Next()
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"A"}))

		step, err = d.Next("A")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"P", "Q"}))

		// Z must wait for Q, not race ahead on an empty parent set
		step, err = d.Next("A", "P")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"Q"}))

		step, err = d.Next("A", "P", "Q")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"Z"}))

		step, err = d.Next("A", "P", "Q", "Z")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"X"}))
	})

	It("should walk the graph one vertex at a time", func() {
		/* Graph structure:
		   A   B
		    \ /
		     C
		    / \
		   D   E
		    \ /
		     F
		*/

		d := New[*string]()
		spt := func(s string) *string { return &s }

		Expect(d.Add("A", spt("A"))).Should(Succeed())
		Expect(d.Add("B", spt("B"))).Should(Succeed())
		Expect(d.Add("C", spt("C"), "A", "B")).Should(Succeed())
		Expect(d.Add("D", spt("D"), "C")).Should(Succeed())
		Expect(d.Add("E", spt("E"), "C")).Should(Succeed())
		Expect(d.Add("F", spt("F"), "D", "E")).Should(Succeed())

		Expect(PrintGraph(GinkgoWriter, d)).To(Succeed())

		// Walk one at a time, starting with no completed vertices
		step, err := d.Next()
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"A", "B"}))

		// Complete A first
		step, err = d.Next("A")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"B"}))

		// Now complete B
		step, err = d.Next("A", "B")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"C"}))

		// Complete C
		step, err = d.Next("A", "B", "C")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"D", "E"}))

		// Complete D
		step, err = d.Next("A", "B", "C", "D")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"E"}))

		// Complete E
		step, err = d.Next("A", "B", "C", "D", "E")
		Expect(err).To(BeNil())
		Expect(step).To(BeEquivalentTo([]string{"F"}))

		// Complete F - should get empty list as we're done
		step, err = d.Next("A", "B", "C", "D", "E", "F")
		Expect(err).To(BeNil())
		Expect(step).To(BeEmpty())

		// Verify we get the correct payloads at each step
		Expect(*d.Get("A")[0]).To(Equal("A"))
		Expect(*d.Get("B")[0]).To(Equal("B"))
		Expect(*d.Get("C")[0]).To(Equal("C"))
		Expect(*d.Get("D")[0]).To(Equal("D"))
		Expect(*d.Get("E")[0]).To(Equal("E"))
		Expect(*d.Get("F")[0]).To(Equal("F"))
	})

	// Property checks over generated graphs. The hand-written specs above pin
	// specific shapes; these pin the contract itself, so a future rewrite of Next
	// cannot satisfy the examples while still losing or mis-ordering a vertex.
	// Graphs are generated from GinkgoRandomSeed(), which Ginkgo prints, so any
	// failure is reproducible with --ginkgo.seed=<printed>.
	Describe("properties over generated graphs", func() {

		// randomDAG builds an acyclic graph over names v0..v{n-1}, only ever pointing
		// an edge from a lower index to a higher one, which makes cycles impossible by
		// construction. addOrder decides the order Add is called in, so a vertex can be
		// forward-referenced before it exists (the placeholder path).
		// orderRng is deliberately separate from rng: the order-independence spec needs
		// the same logical graph rebuilt with a different Add order, which is impossible
		// if one stream drives both the edges and the shuffle.
		randomDAG := func(rng *rand.Rand, orderRng *rand.Rand, n int) (DependencyGraph[*string], map[string][]string, []string) {
			names := make([]string, n)
			deps := make(map[string][]string, n)
			for i := range names {
				names[i] = fmt.Sprintf("v%d", i)
			}
			for i := 1; i < n; i++ {
				for j := range i {
					if rng.Intn(3) == 0 {
						deps[names[i]] = append(deps[names[i]], names[j])
					}
				}
			}

			addOrder := append([]string(nil), names...)
			orderRng.Shuffle(len(addOrder), func(a, b int) { addOrder[a], addOrder[b] = addOrder[b], addOrder[a] })

			d := New[*string]()
			for _, name := range addOrder {
				n := name
				Expect(d.Add(n, &n, deps[n]...)).To(Succeed())
			}
			return d, deps, names
		}

		// The contract in both directions. Soundness alone (never offer something
		// unrunnable) is not enough: the strand was a completeness failure, where a
		// runnable vertex was silently dropped.
		It("offers exactly the vertices whose dependencies are complete", func() {
			rng := rand.New(rand.NewSource(GinkgoRandomSeed()))

			for range 300 {
				d, deps, names := randomDAG(rng, rng, 2+rng.Intn(8))

				// an arbitrary completed set, including sets that are not downward
				// closed: resets and hand-edited annotations produce exactly that
				from := make([]string, 0, len(names))
				for _, name := range names {
					if rng.Intn(2) == 0 {
						from = append(from, name)
					}
				}

				want := make([]string, 0, len(names))
				for _, name := range names {
					if slices.Contains(from, name) {
						continue
					}
					runnable := true
					for _, parent := range deps[name] {
						if !slices.Contains(from, parent) {
							runnable = false
							break
						}
					}
					if runnable {
						want = append(want, name)
					}
				}

				got, err := d.Next(from...)
				Expect(err).ToNot(HaveOccurred())
				Expect(got).To(ConsistOf(want),
					"frontier wrong for completed set %v (deps %v)", from, deps)
			}
		})

		// A spec update adds packages to a NodeWright that is already part-way
		// rolled out, so the walk starts from a completed set it did not produce
		// itself. Starting from empty cannot reach that state, and it is the state
		// the e2e stall was in. Once a vertex stops being offered it is never run
		// again, so the walk must never go empty while work remains.
		It("converges from a completed set it did not build itself", func() {
			rng := rand.New(rand.NewSource(GinkgoRandomSeed()))

			for range 300 {
				d, deps, names := randomDAG(rng, rng, 3+rng.Intn(7))

				// pre-existing progress: any downward-closed subset, as if these
				// packages were already complete when new ones were added to the spec
				complete := make([]string, 0, len(names))
				for _, name := range names {
					if rng.Intn(2) != 0 {
						continue
					}
					satisfied := true
					for _, parent := range deps[name] {
						if !slices.Contains(complete, parent) {
							satisfied = false
							break
						}
					}
					if satisfied {
						complete = append(complete, name)
					}
				}

				for len(complete) < len(names) {
					step, err := d.Next(complete...)
					Expect(err).ToNot(HaveOccurred())
					Expect(step).ToNot(BeEmpty(),
						"stalled with %d/%d complete: have %v, deps %v", len(complete), len(names), complete, deps)

					// finish an arbitrary non-empty subset, leaving the rest in flight,
					// which is what asynchronous stage Jobs do
					for i, name := range step {
						if rng.Intn(2) == 0 || i == len(step)-1 {
							complete = append(complete, name)
						}
					}
				}
			}
		})

		It("gives the same answer whatever order the graph was built in", func() {
			rng := rand.New(rand.NewSource(GinkgoRandomSeed()))

			for range 100 {
				seed := rng.Int63()
				n := 2 + rng.Intn(8)
				d, _, names := randomDAG(rand.New(rand.NewSource(seed)), rand.New(rand.NewSource(rng.Int63())), n)

				from := make([]string, 0, len(names))
				for _, name := range names {
					if rng.Intn(2) == 0 {
						from = append(from, name)
					}
				}

				want, err := d.Next(from...)
				Expect(err).ToNot(HaveOccurred())

				for range 5 {
					// same deps seed, different Add order: forward references land on the
					// placeholder path, which must not change the answer
					other, _, _ := randomDAG(rand.New(rand.NewSource(seed)), rand.New(rand.NewSource(rng.Int63())), n)
					got, err := other.Next(from...)
					Expect(err).ToNot(HaveOccurred())
					Expect(got).To(Equal(want), "answer depended on Add order")
				}
			}
		})

		It("returns results in a stable sorted order", func() {
			rng := rand.New(rand.NewSource(GinkgoRandomSeed()))

			for range 100 {
				d, _, names := randomDAG(rng, rng, 2+rng.Intn(8))

				from := make([]string, 0, len(names))
				for _, name := range names {
					if rng.Intn(2) == 0 {
						from = append(from, name)
					}
				}

				step, err := d.Next(from...)
				Expect(err).ToNot(HaveOccurred())
				Expect(sort.StringsAreSorted(step)).To(BeTrue(), "unsorted result %v", step)

				again, err := d.Next(from...)
				Expect(err).ToNot(HaveOccurred())
				Expect(again).To(Equal(step), "repeat call changed the answer")
			}
		})
	})
})
