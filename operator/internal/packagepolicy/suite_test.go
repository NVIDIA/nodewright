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
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// The code under test is the chart's package-policy template, not a Go package,
// so this suite renders the chart with `helm template` and applies the result to
// envtest's real kube-apiserver, which is what evaluates the CEL.

var (
	ctx      context.Context
	cancel   context.CancelFunc
	adminCfg *rest.Config
	admin    client.Client
	testEnv  *envtest.Environment
)

func TestPackagePolicy(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Package Policy Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	adminCfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(adminCfg).NotTo(BeNil())

	admin, err = client.New(adminCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	By("letting every authenticated user write NodeWright and Skyhook CRs, so the policy is the only gate under test")
	writer := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "packagepolicy-test-writer"},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"nodewright.nvidia.com"},
				Resources: []string{"nodewrights"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"skyhook.nvidia.com"},
				Resources: []string{"skyhooks"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
			},
		},
	}
	Expect(admin.Create(ctx, writer)).To(Succeed())
	Expect(admin.Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "packagepolicy-test-writer"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: writer.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.GroupKind, Name: "system:authenticated"}},
	})).To(Succeed())
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	Expect(testEnv.Stop()).To(Succeed())
})
