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

package packagepolicy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	nwv1 "github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// scenarioLabel scopes each rendered policy to the CRs of one scenario, so
// several policies can be installed in the same apiserver without interfering.
const scenarioLabel = "packagepolicy.test/scenario"

// Policies and RBAC grants reach the apiserver's admission and authorization
// caches asynchronously, so specs that wait on them poll for this long.
const (
	rbacWait = 30 * time.Second
	poll     = 250 * time.Millisecond
)

func helmBin() string {
	if h := os.Getenv("HELM"); h != "" {
		return h
	}
	return filepath.Join("..", "..", "bin", "helm")
}

func chartDir() string {
	return filepath.Join("..", "..", "..", "chart")
}

// helmTemplate renders the package-policy template for release with the given
// values. It returns the combined output and the command's error, so callers can
// assert on render failures too.
func helmTemplate(release, values string, extraArgs ...string) (string, error) {
	f, err := os.CreateTemp(GinkgoT().TempDir(), "values-*.yaml")
	Expect(err).NotTo(HaveOccurred())
	_, err = f.WriteString(values)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.Close()).To(Succeed())

	args := append([]string{"template", release, chartDir(), "-n", "nodewright",
		"-s", "templates/package-policy.yaml", "-f", f.Name()}, extraArgs...)
	out, err := exec.CommandContext(ctx, helmBin(), args...).CombinedOutput()
	return string(out), err
}

// renderPolicy renders the policy for scenario. The scenario doubles as
// fullnameOverride so each scenario's policy has its own name, which also makes
// the exempt operator ServiceAccount operatorSA(scenario).
func renderPolicy(scenario, values string) []*unstructured.Unstructured {
	out, err := helmTemplate("test-release", values, "--set", "fullnameOverride="+scenario)
	Expect(err).NotTo(HaveOccurred(), out)
	return decodeYAMLDocs([]byte(out))
}

func operatorSA(scenario string) client.Client {
	return serviceAccount("nodewright", scenario+"-controller-manager")
}

func decodeYAMLDocs(b []byte) []*unstructured.Unstructured {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var objs []*unstructured.Unstructured
	for {
		u := &unstructured.Unstructured{}
		err := dec.Decode(&u.Object)
		if errors.Is(err, io.EOF) {
			return objs
		}
		Expect(err).NotTo(HaveOccurred())
		if len(u.Object) > 0 {
			objs = append(objs, u)
		}
	}
}

// installPolicy scopes the rendered ValidatingAdmissionPolicy to scenario's CRs
// and creates it together with its binding.
func installPolicy(scenario string, objs []*unstructured.Unstructured) {
	for _, o := range objs {
		if o.GetKind() == "ValidatingAdmissionPolicy" {
			Expect(unstructured.SetNestedStringMap(o.Object,
				map[string]string{scenarioLabel: scenario},
				"spec", "matchConstraints", "objectSelector", "matchLabels")).To(Succeed())
		}
		Expect(admin.Create(ctx, o)).To(Succeed())
	}
}

// as returns a client authenticated as the given user and groups.
func as(name string, groups ...string) client.Client {
	c, _ := asWithWarnings(name, groups...)
	return c
}

// warnings collects the admission warnings returned to one client.
type warnings struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnings) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, text)
}

func (w *warnings) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

func asWithWarnings(name string, groups ...string) (client.Client, *warnings) {
	user, err := testEnv.AddUser(envtest.User{Name: name, Groups: groups}, adminCfg)
	Expect(err).NotTo(HaveOccurred())
	cfg := user.Config()
	w := &warnings{}
	cfg.WarningHandler = w
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	return c, w
}

var nameSeq atomic.Int64

// uniqueName returns a CR name not used by any other spec.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, nameSeq.Add(1))
}

// waitForPolicy blocks until scenario's policy is enforced, by retrying a
// create that the policy must refuse. Policies reach the admission plugin
// asynchronously after they are created.
func waitForPolicy(c client.Client, mk func() *unstructured.Unstructured) {
	Eventually(func() error {
		o := mk()
		err := c.Create(ctx, o)
		if err == nil {
			Expect(admin.Delete(ctx, o)).To(Succeed())
			return errors.New("policy not enforced yet")
		}
		return err
	}, rbacWait, poll).Should(MatchError(ContainSubstring("not permitted")))
}

// mutate re-reads obj as admin, applies change, and writes it back as c.
func mutate(c client.Client, obj *unstructured.Unstructured, change func(*unstructured.Unstructured)) error {
	latest := &unstructured.Unstructured{}
	latest.SetGroupVersionKind(obj.GroupVersionKind())
	Expect(admin.Get(ctx, client.ObjectKeyFromObject(obj), latest)).To(Succeed())
	change(latest)
	return c.Update(ctx, latest)
}

func setPackageField(name string, value any, fields ...string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		Expect(unstructured.SetNestedField(u.Object, value, append([]string{"spec", "packages", name}, fields...)...)).To(Succeed())
	}
}

func removeUninstall(name string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		unstructured.RemoveNestedField(u.Object, "spec", "packages", name, "uninstall")
	}
}

func removePackage(name string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		unstructured.RemoveNestedField(u.Object, "spec", "packages", name)
	}
}

// grantUse binds a ClusterRole granting the virtual `use` verb on the given
// package rules (all of them when names is empty) to user.
func grantUse(user string, names ...string) {
	name := uniqueName("use-" + user)
	Expect(admin.Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{"packagepolicy.nodewright.nvidia.com"},
			Resources:     []string{"packagerules"},
			ResourceNames: names,
			Verbs:         []string{"use"},
		}},
	})).To(Succeed())
	bind(user, name)
}

func bind(user, role string) {
	Expect(admin.Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("bind-" + user)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user}},
	})).To(Succeed())
}

// typedServiceAccount returns a client for a ServiceAccount that reads and
// writes NodeWrights as Go structs, as controller-runtime writers such as other
// operators do, rather than as unstructured maps.
func typedServiceAccount(namespace, name string) client.Client {
	user, err := testEnv.AddUser(envtest.User{
		Name:   "system:serviceaccount:" + namespace + ":" + name,
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace},
	}, adminCfg)
	Expect(err).NotTo(HaveOccurred())
	s := runtime.NewScheme()
	Expect(nwv1.AddToScheme(s)).To(Succeed())
	c, err := client.New(user.Config(), client.Options{Scheme: s})
	Expect(err).NotTo(HaveOccurred())
	return c
}

func serviceAccount(namespace, name string) client.Client {
	return as("system:serviceaccount:"+namespace+":"+name,
		"system:serviceaccounts", "system:serviceaccounts:"+namespace)
}

// pkg builds one spec.packages entry.
func pkg(image, version string, extra ...func(map[string]any)) map[string]any {
	p := map[string]any{"image": image, "version": version}
	for _, e := range extra {
		e(p)
	}
	return p
}

func withSHA(sha string) func(map[string]any) {
	return func(p map[string]any) { p["containerSHA"] = sha }
}

func withAgent(image string) func(map[string]any) {
	return func(p map[string]any) { p["agentImageOverride"] = image }
}

// asSkyhook turns a CR built by nodeWright into the legacy Skyhook kind, whose
// spec has the same shape.
func asSkyhook(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetAPIVersion("skyhook.nvidia.com/v1alpha1")
	u.SetKind("Skyhook")
	return u
}

func nodeWright(name, scenario string, pkgs map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "nodewright.nvidia.com/v1alpha1",
		"kind":       "NodeWright",
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]any{scenarioLabel: scenario},
		},
		"spec": map[string]any{
			"nodeSelectors": map[string]any{"matchLabels": map[string]any{"a": "b"}},
			"packages":      pkgs,
		},
	}}
	return u
}

func expectDenied(err error) {
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsForbidden(err) || apierrors.IsInvalid(err)).To(BeTrue(), err.Error())
	Expect(err.Error()).To(ContainSubstring("not permitted"))
}
