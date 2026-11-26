/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	runtimecache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("SecretCertWatcher", Ordered, func() {
	var (
		watcher *SecretCertWatcher
		tmpDir  string
		secret  *corev1.Secret
		cert    *webhookCert
	)

	BeforeAll(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "webhook-test-*")
		Expect(err).NotTo(HaveOccurred())

		// Create a test secret with valid cert data
		cert, err = generateCert("test-service", "test-namespace", 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())

		secret = cert.ToSecret("test-webhook-secret", "test-namespace", "test-service")
	})

	AfterAll(func() {
		_ = os.RemoveAll(tmpDir)
	})

	BeforeEach(func() {
		watcher = NewSecretCertWatcher(
			newFakeClientBuilder().WithObjects(secret).Build(),
			"test-namespace",
			"test-webhook-secret",
			tmpDir,
		)
	})

	It("should sync secret to disk on initial start", func() {
		_, err := watcher.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-webhook-secret", Namespace: "test-namespace"}})
		Expect(err).NotTo(HaveOccurred())

		// Verify files were written
		certData, err := os.ReadFile(filepath.Join(tmpDir, "tls.crt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(certData).To(Equal(secret.Data["tls.crt"]))

		keyData, err := os.ReadFile(filepath.Join(tmpDir, "tls.key"))
		Expect(err).NotTo(HaveOccurred())
		Expect(keyData).To(Equal(secret.Data["tls.key"]))
	})

	It("should handle secret updates", func() {
		// Initial sync
		_, err := watcher.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-webhook-secret", Namespace: "test-namespace"}})
		Expect(err).NotTo(HaveOccurred())

		// Generate new cert
		newCert, err := generateCert("test-service", "test-namespace", 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())

		// Update secret
		secret = newCert.ToSecret("test-webhook-secret", "test-namespace", "test-service")
		err = watcher.client.Update(context.Background(), secret)
		Expect(err).NotTo(HaveOccurred())

		// Simulate update event
		_, err = watcher.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-webhook-secret", Namespace: "test-namespace"}})
		Expect(err).NotTo(HaveOccurred())

		// Verify files were updated
		certData, err := os.ReadFile(filepath.Join(tmpDir, "tls.crt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(certData).To(Equal(secret.Data["tls.crt"]))

		keyData, err := os.ReadFile(filepath.Join(tmpDir, "tls.key"))
		Expect(err).NotTo(HaveOccurred())
		Expect(keyData).To(Equal(secret.Data["tls.key"]))
	})

	It("should handle missing secret gracefully", func() {
		// Create watcher with empty client
		watcher.client = newFakeClientBuilder().Build()

		_, err := watcher.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-webhook-secret", Namespace: "test-namespace"}})
		Expect(err).NotTo(HaveOccurred()) // A missing Secret is not an error
	})

	It("should handle filesystem errors", func() {
		// Remove write permissions from directory
		err := os.Chmod(tmpDir, 0o444)
		Expect(err).NotTo(HaveOccurred())

		_, err = watcher.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-webhook-secret", Namespace: "test-namespace"}})
		Expect(err).To(HaveOccurred())

		// Restore permissions
		err = os.Chmod(tmpDir, 0o755)
		Expect(err).NotTo(HaveOccurred())
	})
})

// These run against envtest's apiserver, which enforces RBAC, as a user bound to a Role shaped
// like the chart's: create on any Secret, every other verb on the serving cert by name. That
// pairing is what the cache config has to satisfy, so it is tested as a pair.
var _ = Describe("WebhookSecretCacheConfig", func() {
	const (
		secretName = "webhook-cert"
		userName   = "nodewright-secret-scope"
	)
	var (
		namespace string
		userCfg   *rest.Config
	)

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "secret-scope-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, ns))).To(Succeed())
		})

		Expect(k8sClient.Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "manager-role", Namespace: namespace},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create"}},
				{
					APIGroups:     []string{""},
					Resources:     []string{"secrets"},
					ResourceNames: []string{secretName},
					Verbs:         []string{"delete", "get", "list", "patch", "update", "watch"},
				},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "manager-rolebinding", Namespace: namespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "manager-role"},
			Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: userName}},
		})).To(Succeed())

		// Another Secret in the same namespace, standing in for Helm release history and
		// image pull secrets, which the operator must neither cache nor be able to read.
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.nodewright.v1", Namespace: namespace},
		})).To(Succeed())

		userCfg = rest.CopyConfig(cfg)
		userCfg.Impersonate = rest.ImpersonationConfig{UserName: userName}
	})

	startCache := func(byObject runtimecache.ByObject) runtimecache.Cache {
		c, err := runtimecache.New(userCfg, runtimecache.Options{
			Scheme:   scheme.Scheme,
			ByObject: map[client.Object]runtimecache.ByObject{&corev1.Secret{}: byObject},
		})
		Expect(err).NotTo(HaveOccurred())
		cacheCtx, stop := context.WithCancel(ctx)
		DeferCleanup(stop)
		// Informers start lazily, and WaitForCacheSync only waits on the ones that exist.
		_, err = c.GetInformer(ctx, &corev1.Secret{}, runtimecache.BlockUntilSynced(false))
		Expect(err).NotTo(HaveOccurred())
		go func() {
			defer GinkgoRecover()
			Expect(c.Start(cacheCtx)).To(Succeed())
		}()
		return c
	}

	It("syncs under the scoped Role, sees the serving cert created, and caches nothing else", func() {
		c := startCache(WebhookSecretCacheConfig(namespace, secretName))

		syncCtx, cancelSync := context.WithTimeout(ctx, 30*time.Second)
		defer cancelSync()
		Expect(c.WaitForCacheSync(syncCtx)).To(BeTrue())

		// A fresh install: the informer is already running when the operator creates the
		// Secret, so the cache has to pick it up from the watch.
		cert, err := generateCert("test-service", namespace, 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, cert.ToSecret(secretName, namespace, "test-service"))).To(Succeed())

		Eventually(func() error {
			return c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &corev1.Secret{})
		}).WithTimeout(30 * time.Second).Should(Succeed())

		secrets := &corev1.SecretList{}
		Expect(c.List(ctx, secrets, client.InNamespace(namespace))).To(Succeed())
		Expect(secrets.Items).To(HaveLen(1))
		Expect(secrets.Items[0].Name).To(Equal(secretName))
	})

	It("cannot sync a namespace-wide Secret informer under the scoped Role", func() {
		c := startCache(runtimecache.ByObject{Namespaces: map[string]runtimecache.Config{namespace: {}}})

		syncCtx, cancelSync := context.WithTimeout(ctx, 3*time.Second)
		defer cancelSync()
		Expect(c.WaitForCacheSync(syncCtx)).To(BeFalse())
	})
})

var _ = Describe("writeCertAndKey", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("writes cert and key with 0600 and leaves no temp files behind", func() {
		Expect(writeCertAndKey([]byte("cert-data"), []byte("key-data"), dir)).To(Succeed())

		for name, want := range map[string]string{"tls.crt": "cert-data", "tls.key": "key-data"} {
			path := filepath.Join(dir, name)
			got, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(want))

			info, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		}

		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(2))
	})

	It("replaces existing files", func() {
		Expect(writeCertAndKey([]byte("old-cert"), []byte("old-key"), dir)).To(Succeed())
		Expect(writeCertAndKey([]byte("new-cert"), []byte("new-key"), dir)).To(Succeed())

		got, err := os.ReadFile(filepath.Join(dir, "tls.crt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("new-cert"))

		got, err = os.ReadFile(filepath.Join(dir, "tls.key"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("new-key"))
	})

	It("never exposes a partial file under concurrent writers", func() {
		const writers, rounds = 8, 40
		// Large payloads so a non-atomic write could be observed half done.
		pair := func(i int) ([]byte, []byte) {
			return bytes.Repeat([]byte(fmt.Sprintf("cert-%d\n", i)), 8192),
				bytes.Repeat([]byte(fmt.Sprintf("key-%d\n", i)), 8192)
		}
		isWholeFile := func(data []byte, pick func(i int) []byte) bool {
			for i := 0; i < writers; i++ {
				if bytes.Equal(data, pick(i)) {
					return true
				}
			}
			return false
		}
		certOf := func(i int) []byte { c, _ := pair(i); return c }
		keyOf := func(i int) []byte { _, k := pair(i); return k }

		var wg sync.WaitGroup
		writeErrs := make(chan error, writers)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				c, k := pair(w)
				for r := 0; r < rounds; r++ {
					if err := writeCertAndKey(c, k, dir); err != nil {
						writeErrs <- err
						return
					}
				}
			}(w)
		}

		stop := make(chan struct{})
		torn := make(chan string, 1)
		var readers sync.WaitGroup
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for name, pick := range map[string]func(int) []byte{"tls.crt": certOf, "tls.key": keyOf} {
					data, err := os.ReadFile(filepath.Join(dir, name))
					if os.IsNotExist(err) {
						continue
					}
					if err != nil || !isWholeFile(data, pick) {
						select {
						case torn <- fmt.Sprintf("%s was partial or unreadable (len=%d, err=%v)", name, len(data), err):
						default:
						}
						return
					}
				}
			}
		}()

		wg.Wait()
		close(stop)
		readers.Wait()
		close(writeErrs)

		for err := range writeErrs {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(torn).To(BeEmpty())

		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(strings.HasPrefix(e.Name(), ".")).To(BeFalse(), "leftover temp file %s", e.Name())
		}
	})
})
