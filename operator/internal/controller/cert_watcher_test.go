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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
			fake.NewClientBuilder().WithObjects(secret).Build(),
			nil, // Mock cache
			"test-namespace",
			"test-webhook-secret",
			tmpDir,
		)
	})

	It("should sync secret to disk on initial start", func() {
		err := watcher.syncSecretToDisk(context.Background())
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
		err := watcher.syncSecretToDisk(context.Background())
		Expect(err).NotTo(HaveOccurred())

		// Generate new cert
		newCert, err := generateCert("test-service", "test-namespace", 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())

		// Update secret
		secret = newCert.ToSecret("test-webhook-secret", "test-namespace", "test-service")
		err = watcher.client.Update(context.Background(), secret)
		Expect(err).NotTo(HaveOccurred())

		// Simulate update event
		err = watcher.syncSecretToDisk(context.Background())
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
		watcher.client = fake.NewClientBuilder().Build()

		err := watcher.syncSecretToDisk(context.Background())
		Expect(err).NotTo(HaveOccurred()) // Should not error, just log
	})

	It("should handle filesystem errors", func() {
		// Remove write permissions from directory
		err := os.Chmod(tmpDir, 0o444)
		Expect(err).NotTo(HaveOccurred())

		err = watcher.syncSecretToDisk(context.Background())
		Expect(err).To(HaveOccurred())

		// Restore permissions
		err = os.Chmod(tmpDir, 0o755)
		Expect(err).NotTo(HaveOccurred())
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

	It("never exposes a partial file or leaves a mismatched pair under concurrent writers", func() {
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

		// Once all writers are done, tls.crt and tls.key must come from the same writer.
		crt, err := os.ReadFile(filepath.Join(dir, "tls.crt"))
		Expect(err).NotTo(HaveOccurred())
		key, err := os.ReadFile(filepath.Join(dir, "tls.key"))
		Expect(err).NotTo(HaveOccurred())
		matched := false
		for i := 0; i < writers; i++ {
			c, k := pair(i)
			if bytes.Equal(crt, c) {
				Expect(bytes.Equal(key, k)).To(BeTrue(), "tls.key does not belong to the same writer as tls.crt")
				matched = true
			}
		}
		Expect(matched).To(BeTrue(), "tls.crt does not match any writer's payload")

		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		for _, e := range entries {
			Expect(strings.HasPrefix(e.Name(), ".")).To(BeFalse(), "leftover temp file %s", e.Name())
		}
	})
})
