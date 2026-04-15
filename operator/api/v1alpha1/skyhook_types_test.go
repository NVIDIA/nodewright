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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Skyhook Types", func() {
	It("Should set packages name with the key", func() {
		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
			},
			"bar": Package{
				PackageRef: PackageRef{
					Version: "2.3",
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		Expect(packages["foo"].Name).To(BeEquivalentTo("foo"))
		Expect(packages["bar"].Name).To(BeEquivalentTo("bar"))
	})

	It("Should get package's unique name", func() {
		refs := map[string]*PackageRef{
			"dogs": {
				Name:    "dogs",
				Version: "1.2.3",
			},
			"cats": {
				Name:    "cats",
				Version: "3",
			},
			"ducks": {
				Name:    "ducks",
				Version: "3.1-2",
			},
		}

		Expect(refs["dogs"].GetUniqueName()).To(BeEquivalentTo("dogs|1.2.3"))
		Expect(refs["cats"].GetUniqueName()).To(BeEquivalentTo("cats|3"))
		Expect(refs["ducks"].GetUniqueName()).To(BeEquivalentTo("ducks|3.1-2"))
	})

	It("Should be equal", func() {

		nodeState := NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
			},
			"bar|2.3": PackageStatus{
				Name:    "bar",
				Version: "2.3",
			},
		}

		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
			},
			"bar": Package{
				PackageRef: PackageRef{
					Version: "2.3",
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		Expect(nodeState.Contains(packages)).To(BeTrue())

		packages = Packages{
			"foob": Package{
				PackageRef: PackageRef{
					// Name:    "foo", //TODO: might need something about keys and names not matching and what wins where...
					// not all code paths are doing it the same, could cause issues
					Version: "1.2.3",
				},
			},
			"bar": Package{
				PackageRef: PackageRef{
					// Name:    "bar",
					Version: "2.3",
				},
			},
		}

		Expect(nodeState.Contains(packages)).To(BeFalse())
	})

	It("should check interrupt", func() {
		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
				Interrupt: &Interrupt{
					Type: REBOOT,
				},
			},
			"car": Package{
				PackageRef: PackageRef{
					Version: "2",
				},
			},
			"dog": Package{
				PackageRef: PackageRef{
					Version: "3.2.1",
				},
			},
			"ducks": Package{
				PackageRef: PackageRef{
					Version: "3",
				},
				Interrupt: &Interrupt{
					Type: REBOOT,
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		nodeState := NodeState{}

		interrupts := map[string][]*Interrupt{}

		Expect(nodeState.HasInterrupt(packages["foo"], interrupts, nil)).To(BeEquivalentTo(true))
		Expect(nodeState.HasInterrupt(packages["ducks"], interrupts, nil)).To(BeEquivalentTo(true))
		Expect(nodeState.HasInterrupt(packages["car"], interrupts, nil)).To(BeEquivalentTo(false))
		Expect(nodeState.HasInterrupt(packages["dog"], interrupts, nil)).To(BeEquivalentTo(false))

		configUpdates := map[string][]string{}

		Expect(nodeState.HasInterrupt(packages["foo"], interrupts, configUpdates)).To(BeEquivalentTo(true))
		Expect(nodeState.HasInterrupt(packages["ducks"], interrupts, configUpdates)).To(BeEquivalentTo(true))
		Expect(nodeState.HasInterrupt(packages["car"], interrupts, configUpdates)).To(BeEquivalentTo(false))
		Expect(nodeState.HasInterrupt(packages["dog"], interrupts, configUpdates)).To(BeEquivalentTo(false))

		configUpdates = map[string][]string{
			"dog": {
				"blah",
			},
			"ducks": {
				"blah",
			},
		}

		interrupts = map[string][]*Interrupt{
			"dog": {
				&Interrupt{
					Type: REBOOT,
				},
			},
		}

		Expect(nodeState.HasInterrupt(packages["foo"], interrupts, configUpdates)).To(BeEquivalentTo(true))
		Expect(nodeState.HasInterrupt(packages["ducks"], interrupts, configUpdates)).To(BeEquivalentTo(false))
		Expect(nodeState.HasInterrupt(packages["car"], interrupts, configUpdates)).To(BeEquivalentTo(false))
		Expect(nodeState.HasInterrupt(packages["dog"], interrupts, configUpdates)).To(BeEquivalentTo(true))
	})

	It("should get completed", func() {
		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
			},
			"car": Package{
				PackageRef: PackageRef{
					Version: "2",
				},
			},
			"dog": Package{
				PackageRef: PackageRef{
					Version: "3.2.1",
				},
			},
			"ducks": Package{
				PackageRef: PackageRef{
					Version: "3",
				},
				Interrupt: &Interrupt{
					Type: REBOOT,
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		nodeState := NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				State:   StateComplete,
			},
			"bar|2": PackageStatus{
				Name:    "bar",
				Version: "2",
				State:   StateComplete,
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
			},
			"dog|3.2.1": PackageStatus{
				Name:    "dog",
				Version: "3.2.1",
				State:   StateComplete,
				Stage:   StageConfig,
			},
			"ducks|3": PackageStatus{
				Name:    "ducks",
				Version: "3",
				State:   StateComplete,
				Stage:   StageConfig,
			},
		}

		interrupts := map[string][]*Interrupt{}
		configUpdates := map[string][]string{}

		Expect(nodeState.GetComplete(packages, interrupts, nil)).To(BeEquivalentTo([]string{"dog|3.2.1"}))
		Expect(nodeState.GetComplete(packages, interrupts, configUpdates)).To(BeEquivalentTo([]string{"dog|3.2.1"}))

		configUpdates = map[string][]string{
			"dog": {
				"blah",
			},
			"ducks": {
				"blah",
			},
		}
		interrupts = map[string][]*Interrupt{
			"dog": {
				&Interrupt{
					Type: REBOOT,
				},
			},
		}
		Expect(nodeState.GetComplete(packages, interrupts, configUpdates)).To(BeEquivalentTo([]string{"ducks|3"}))
	})

	It("Should be complete", func() {

		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
			},
			"bar": Package{
				PackageRef: PackageRef{
					Version: "2",
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		nodeState := NodeState{}
		stage := StageConfig

		// using this method to test upsert too
		Expect(nodeState.Upsert(PackageRef{
			Name:    "foo",
			Version: "1.2.3",
		}, "", StateComplete, stage, 2, "")).To(BeTrue())
		Expect(nodeState.Upsert(PackageRef{
			Name:    "bar",
			Version: "2.3",
		}, "", StateComplete, stage, 2, "")).To(BeTrue())
		Expect(nodeState.Upsert(PackageRef{ // replace
			Name:    "bar",
			Version: "2",
		}, "", StateComplete, stage, 2, "")).To(BeTrue())
		Expect(nodeState.Upsert(PackageRef{ // exists
			Name:    "bar",
			Version: "2",
		}, "", StateComplete, stage, 2, "")).To(BeFalse())

		interrupts := map[string][]*Interrupt{}
		configUpdates := map[string][]string{}
		Expect(nodeState.IsComplete(packages, interrupts, configUpdates)).To(BeTrue())
		nodeState = NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				State:   StateComplete,
				Stage:   StageConfig,
			},
			"bar|2": PackageStatus{
				Name:    "bar",
				Version: "2",
				State:   StateComplete,
				Stage:   StageConfig,
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
				Stage:   StageConfig,
			},
		}
		Expect(nodeState.IsComplete(packages, interrupts, configUpdates)).To(BeTrue())

		interrupts = map[string][]*Interrupt{
			"foo": {
				{
					Type: REBOOT,
				},
			},
		}
		configUpdates = map[string][]string{
			"foo": {
				"changed",
			},
		}
		Expect(nodeState.IsComplete(packages, interrupts, configUpdates)).To(BeFalse())

		interrupts = map[string][]*Interrupt{}
		configUpdates = map[string][]string{}
		nodeState = NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				State:   StateComplete,
			},
			"bar|2.3": PackageStatus{
				Name:    "bar",
				Version: "2.3", // bad version
				State:   StateComplete,
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
			},
		}
		Expect(nodeState.IsComplete(packages, interrupts, configUpdates)).To(BeFalse())

		nodeState = NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				State:   StateComplete,
			},
			"bar|2": PackageStatus{ // bad status
				Name:    "bar",
				Version: "2",
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
			},
		}
		Expect(nodeState.IsComplete(packages, interrupts, configUpdates)).To(BeFalse())

		nodeState = NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				State:   StateComplete,
				Stage:   StageUninstall,
			},
			"bar|2": PackageStatus{
				Name:    "bar",
				Version: "2",
				State:   StateComplete,
				Stage:   StageConfig,
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
				Stage:   StageConfig,
			},
		}
		Expect(nodeState.IsComplete(packages, interrupts, configUpdates)).To(BeFalse())
	})

	It("interrupt should be complete after post apply", func() {

		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
				Interrupt: &Interrupt{
					Type: SERVICE,
				},
			},
			"bar": Package{
				PackageRef: PackageRef{
					Version: "2",
				},
				DependsOn: map[string]string{
					"foo": "1.2.3",
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		nodeState := NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				Stage:   StageInterrupt,
				State:   StateComplete,
			},
			"bar|2": PackageStatus{
				Name:    "bar",
				Version: "2",
				Stage:   StageConfig,
				State:   StateComplete,
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
			},
		}

		interrupts := map[string][]*Interrupt{}
		configUpdates := map[string][]string{}
		Expect(nodeState.GetComplete(packages, interrupts, configUpdates)).To(BeEquivalentTo([]string{"bar|2"}))

		nodeState = NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				Stage:   StagePostInterrupt,
				State:   StateComplete,
			},
			"bar|2": PackageStatus{
				Name:    "bar",
				Version: "2",
				Stage:   StageConfig,
				State:   StateComplete,
			},
			"kitties|3.2.1": PackageStatus{ // state can have more then current setup of packages
				Name:    "kitties",
				Version: "3.2.1",
				State:   StateUnknown, // in this cause, status does not matter
			},
		}
		Expect(nodeState.GetComplete(packages, interrupts, configUpdates)).To(BeEquivalentTo([]string{"bar|2", "foo|1.2.3"}))

	})

	It("package should be complete", func() {

		packages := Packages{
			"foo": Package{
				PackageRef: PackageRef{
					Version: "1.2.3",
				},
				Interrupt: &Interrupt{
					Type:     SERVICE,
					Services: []string{"cron"},
				},
			},
			"bar": Package{
				PackageRef: PackageRef{
					Version: "2",
				},
			},
		}

		// Simulating serialization of packages
		packages.Names()

		nodeState := NodeState{
			"foo|1.2.3": PackageStatus{
				Name:    "foo",
				Version: "1.2.3",
				Stage:   StageConfig,
				State:   StateComplete,
			},
			"bar|2": PackageStatus{
				Name:    "bar",
				Version: "2",
				Stage:   StageConfig,
				State:   StateComplete,
			},
		}

		interrupts := map[string][]*Interrupt{}
		configUpdates := map[string][]string{}
		Expect(nodeState.IsPackageComplete(packages["foo"], interrupts, configUpdates)).To(BeEquivalentTo(false))
		Expect(nodeState.IsPackageComplete(packages["bar"], interrupts, configUpdates)).To(BeEquivalentTo(true))

		Expect(nodeState.IsPackageComplete(packages["foo"], nil, nil)).To(BeEquivalentTo(false))
		Expect(nodeState.IsPackageComplete(packages["bar"], nil, nil)).To(BeEquivalentTo(true))

		interrupts = map[string][]*Interrupt{
			"bar": {
				{
					Type:     SERVICE,
					Services: []string{"cron"},
				},
			},
		}
		configUpdates = map[string][]string{
			"bar": {
				"key",
				"bogus",
			},
			"foo": {
				"key",
			},
		}

		Expect(nodeState.IsPackageComplete(packages["foo"], interrupts, configUpdates)).To(BeEquivalentTo(true))
		Expect(nodeState.IsPackageComplete(packages["bar"], interrupts, configUpdates)).To(BeEquivalentTo(false))
	})

	It("Should detect IsPaused correctly", func() {
		s := &Skyhook{
			ObjectMeta: metav1.ObjectMeta{},
		}
		// Case 1: Annotations is nil
		Expect(s.IsPaused()).To(BeFalse())

		// Case 2: Annotations is empty map
		s.Annotations = map[string]string{}
		Expect(s.IsPaused()).To(BeFalse())

		// Case 3: Key not present
		s.Annotations = map[string]string{"other": "true"}
		Expect(s.IsPaused()).To(BeFalse())

		// Case 4: Key present with value "true"
		s.Annotations = map[string]string{METADATA_PREFIX + "/pause": "true"}
		Expect(s.IsPaused()).To(BeTrue())

		// Case 5: Key present with value "false"
		s.Annotations = map[string]string{METADATA_PREFIX + "/pause": "false"}
		Expect(s.IsPaused()).To(BeFalse())
	})

	It("Should return false for UninstallEnabled on nil package", func() {
		var pkg *Package
		Expect(pkg.UninstallEnabled()).To(BeFalse())
	})

	It("Should return false for UninstallEnabled when Uninstall is nil", func() {
		pkg := &Package{
			PackageRef: PackageRef{Name: "test", Version: "1.0"},
		}
		Expect(pkg.UninstallEnabled()).To(BeFalse())
	})

	It("Should return false for UninstallEnabled when Enabled is false", func() {
		pkg := &Package{
			PackageRef: PackageRef{Name: "test", Version: "1.0"},
			Uninstall:  &Uninstall{Enabled: false, Apply: false},
		}
		Expect(pkg.UninstallEnabled()).To(BeFalse())
	})

	It("Should return true for UninstallEnabled when Enabled is true", func() {
		pkg := &Package{
			PackageRef: PackageRef{Name: "test", Version: "1.0"},
			Uninstall:  &Uninstall{Enabled: true, Apply: false},
		}
		Expect(pkg.UninstallEnabled()).To(BeTrue())
	})

	It("Should return true for IsUninstalling only when both Enabled and Apply are true", func() {
		pkg := &Package{
			PackageRef: PackageRef{Name: "test", Version: "1.0"},
			Uninstall:  &Uninstall{Enabled: true, Apply: true},
		}
		Expect(pkg.IsUninstalling()).To(BeTrue())
	})

	It("Should return false for IsUninstalling when Enabled is false and Apply is true", func() {
		pkg := &Package{
			PackageRef: PackageRef{Name: "test", Version: "1.0"},
			Uninstall:  &Uninstall{Enabled: false, Apply: true},
		}
		Expect(pkg.IsUninstalling()).To(BeFalse())
	})

	It("Should preserve Uninstall field through DeepCopy", func() {
		original := &Package{
			PackageRef: PackageRef{Name: "test", Version: "1.0"},
			Image:      "test-image",
			Uninstall:  &Uninstall{Enabled: true, Apply: true},
		}
		copied := original.DeepCopy()
		Expect(copied.Uninstall).ToNot(BeNil())
		Expect(copied.Uninstall.Enabled).To(BeTrue())
		Expect(copied.Uninstall.Apply).To(BeTrue())

		// Verify it's a deep copy (mutating copy doesn't affect original)
		copied.Uninstall.Apply = false
		Expect(original.Uninstall.Apply).To(BeTrue())
	})

	It("Should reject uninstall.apply=true with enabled=false via Validate", func() {
		skyhook := &Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: SkyhookSpec{
				Packages: Packages{
					"my-pkg": Package{
						PackageRef: PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &Uninstall{Enabled: false, Apply: true},
					},
				},
			},
		}
		err := skyhook.Validate()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("uninstall.apply requires uninstall.enabled"))
	})

	It("Should allow uninstall.apply=true with enabled=true via Validate", func() {
		skyhook := &Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: SkyhookSpec{
				Packages: Packages{
					"my-pkg": Package{
						PackageRef: PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
						Uninstall:  &Uninstall{Enabled: true, Apply: true},
					},
				},
			},
		}
		err := skyhook.Validate()
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should pass validation with nil Uninstall field", func() {
		skyhook := &Skyhook{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: SkyhookSpec{
				Packages: Packages{
					"my-pkg": Package{
						PackageRef: PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Image:      "my-image",
					},
				},
			},
		}
		err := skyhook.Validate()
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should detect isPackageFullyUninstalled correctly", func() {
		skyhook := &Skyhook{
			Spec: SkyhookSpec{
				Packages: Packages{
					"my-pkg": Package{
						PackageRef: PackageRef{Name: "my-pkg", Version: "1.0.0"},
						Uninstall:  &Uninstall{Enabled: true, Apply: true},
					},
				},
			},
			Status: SkyhookStatus{
				NodeState: map[string]NodeState{
					"node-1": {
						"my-pkg|1.0.0": PackageStatus{
							Name: "my-pkg", Version: "1.0.0",
							Stage: StageUninstall, State: StateInProgress,
						},
					},
				},
			},
		}
		// Still present on node-1
		Expect(isPackageFullyUninstalled(skyhook, "my-pkg")).To(BeFalse())

		// Remove from node state (uninstall completed)
		delete(skyhook.Status.NodeState["node-1"], "my-pkg|1.0.0")
		Expect(isPackageFullyUninstalled(skyhook, "my-pkg")).To(BeTrue())
	})

	It("Should detect IsDisabled correctly", func() {
		s := &Skyhook{
			ObjectMeta: metav1.ObjectMeta{},
		}
		// Case 1: Annotations is nil
		Expect(s.IsDisabled()).To(BeFalse())

		// Case 2: Annotations is empty map
		s.Annotations = map[string]string{}
		Expect(s.IsDisabled()).To(BeFalse())

		// Case 3: Key not present
		s.Annotations = map[string]string{"other": "true"}
		Expect(s.IsDisabled()).To(BeFalse())

		// Case 4: Key present with value "true"
		s.Annotations = map[string]string{METADATA_PREFIX + "/disable": "true"}
		Expect(s.IsDisabled()).To(BeTrue())

		// Case 5: Key present with value "false"
		s.Annotations = map[string]string{METADATA_PREFIX + "/disable": "false"}
		Expect(s.IsDisabled()).To(BeFalse())
	})

})
