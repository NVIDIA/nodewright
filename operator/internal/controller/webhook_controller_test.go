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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/skyhook/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("WebhookController", Ordered, func() {
	var (
		secretName  string
		namespace   string
		serviceName string
		tmpDir      string
		cachedCert  *webhookCert
	)

	BeforeAll(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "skyhook-test-*")
		Expect(err).NotTo(HaveOccurred())

		// Generate a single certificate to be reused across tests
		cachedCert, err = generateCert("test-service", "test-namespace", 24*time.Hour)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		err := os.RemoveAll(tmpDir)
		Expect(err).NotTo(HaveOccurred())
	})

	BeforeEach(func() {
		secretName = "test-webhook-secret"
		namespace = "test-namespace"
		serviceName = "test-service"
	})

	Describe("generateCert", func() {
		It("should generate a valid certificate and key", func() {
			cert, err := generateCert(serviceName, namespace, 24*time.Second)
			Expect(err).ToNot(HaveOccurred())
			Expect(cert).ToNot(BeNil())
			Expect(cert.TLSCert).ToNot(BeEmpty())
			Expect(cert.TLSKey).ToNot(BeEmpty())
			Expect(cert.CABytes).ToNot(BeEmpty())
			Expect(cert.Expiration).ToNot(BeNil())
		})
	})

	Describe("Secret creation for webhook cert", func() {
		It("should create a Secret with the correct data", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
					Annotations: map[string]string{
						"expiration": cachedCert.Expiration.Format(time.RFC3339),
					},
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"ca.crt":  cachedCert.CABytes,
					"tls.crt": []byte(cachedCert.TLSCert),
					"tls.key": []byte(cachedCert.TLSKey),
				},
			}
			Expect(secret).ToNot(BeNil())
			Expect(secret.Data["ca.crt"]).To(Equal(cachedCert.CABytes))
			Expect(secret.Data["tls.crt"]).To(Equal([]byte(cachedCert.TLSCert)))
			Expect(secret.Data["tls.key"]).To(Equal([]byte(cachedCert.TLSKey)))
			Expect(secret.Annotations["expiration"]).ToNot(BeEmpty())
		})
	})

	Describe("webhookValidatingWebhookConfiguration", func() {
		It("should create a ValidatingWebhookConfiguration with the correct CABundle", func() {
			secret := &corev1.Secret{
				Data: map[string][]byte{
					"ca.crt": cachedCert.CABytes,
				},
			}
			conf := webhookValidatingWebhookConfiguration(namespace, serviceName, secret)
			Expect(conf).ToNot(BeNil())
			Expect(conf.Webhooks).ToNot(BeEmpty())
			Expect(conf.Webhooks[0].ClientConfig.CABundle).To(Equal(cachedCert.CABytes))
		})
	})

	Describe("webhookMutatingWebhookConfiguration", func() {
		It("should create a MutatingWebhookConfiguration with the correct CABundle", func() {
			secret := &corev1.Secret{
				Data: map[string][]byte{
					"ca.crt": cachedCert.CABytes,
				},
			}
			conf := webhookMutatingWebhookConfiguration(namespace, serviceName, secret)
			Expect(conf).ToNot(BeNil())
			Expect(conf.Webhooks).ToNot(BeEmpty())
			Expect(conf.Webhooks[0].ClientConfig.CABundle).To(Equal(cachedCert.CABytes))
		})
	})

	Describe("webhook update logic", func() {
		It("should detect CABundle changes and non-changes for validating webhook", func() {
			tests := []struct {
				name          string
				oldCABundles  [][]byte
				newCABundles  [][]byte
				expectChanged bool
			}{
				{
					name:          "different CABundle",
					oldCABundles:  [][]byte{[]byte("old-ca")},
					newCABundles:  [][]byte{[]byte("new-ca")},
					expectChanged: true,
				},
				{
					name:          "same CABundle",
					oldCABundles:  [][]byte{[]byte("same-ca")},
					newCABundles:  [][]byte{[]byte("same-ca")},
					expectChanged: false,
				},
				{
					name:          "multiple webhooks, one changed",
					oldCABundles:  [][]byte{[]byte("ca1"), []byte("ca2")},
					newCABundles:  [][]byte{[]byte("ca1"), []byte("ca3")},
					expectChanged: true,
				},
				{
					name:          "different number of webhooks",
					oldCABundles:  [][]byte{[]byte("ca1")},
					newCABundles:  [][]byte{[]byte("ca1"), []byte("ca2")},
					expectChanged: true,
				},
			}

			for _, tt := range tests {
				oldConf := &admissionregistrationv1.ValidatingWebhookConfiguration{}
				for _, ca := range tt.oldCABundles {
					oldConf.Webhooks = append(oldConf.Webhooks, admissionregistrationv1.ValidatingWebhook{
						ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: ca},
					})
				}
				newConf := &admissionregistrationv1.ValidatingWebhookConfiguration{}
				for _, ca := range tt.newCABundles {
					newConf.Webhooks = append(newConf.Webhooks, admissionregistrationv1.ValidatingWebhook{
						ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: ca},
					})
				}
				changed := compareValidatingWebhookConfigurations(oldConf, newConf)
				Expect(changed).To(Equal(tt.expectChanged), "case: %s", tt.name)
			}
		})

		It("should detect CABundle changes and non-changes for mutating webhook", func() {
			tests := []struct {
				name          string
				oldCABundles  [][]byte
				newCABundles  [][]byte
				expectChanged bool
			}{
				{
					name:          "different CABundle",
					oldCABundles:  [][]byte{[]byte("old-ca")},
					newCABundles:  [][]byte{[]byte("new-ca")},
					expectChanged: true,
				},
				{
					name:          "same CABundle",
					oldCABundles:  [][]byte{[]byte("same-ca")},
					newCABundles:  [][]byte{[]byte("same-ca")},
					expectChanged: false,
				},
				{
					name:          "multiple webhooks, one changed",
					oldCABundles:  [][]byte{[]byte("ca1"), []byte("ca2")},
					newCABundles:  [][]byte{[]byte("ca1"), []byte("ca3")},
					expectChanged: true,
				},
				{
					name:          "different number of webhooks",
					oldCABundles:  [][]byte{[]byte("ca1")},
					newCABundles:  [][]byte{[]byte("ca1"), []byte("ca2")},
					expectChanged: true,
				},
			}

			for _, tt := range tests {
				oldConf := &admissionregistrationv1.MutatingWebhookConfiguration{}
				for _, ca := range tt.oldCABundles {
					oldConf.Webhooks = append(oldConf.Webhooks, admissionregistrationv1.MutatingWebhook{
						ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: ca},
					})
				}
				newConf := &admissionregistrationv1.MutatingWebhookConfiguration{}
				for _, ca := range tt.newCABundles {
					newConf.Webhooks = append(newConf.Webhooks, admissionregistrationv1.MutatingWebhook{
						ClientConfig: admissionregistrationv1.WebhookClientConfig{CABundle: ca},
					})
				}
				changed := compareMutatingWebhookConfigurations(oldConf, newConf)
				Expect(changed).To(Equal(tt.expectChanged), "case: %s", tt.name)
			}
		})
	})

	Describe("webhook rule generation", func() {
		It("should include CREATE, UPDATE, and DELETE for deploymentPolicy validating rules", func() {
			rules := deploymentPolicyValidatingRules()
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].Operations).To(ConsistOf(
				admissionregistrationv1.Create,
				admissionregistrationv1.Update,
				admissionregistrationv1.Delete,
			))
			Expect(rules[0].Rule.Resources).To(Equal([]string{"deploymentpolicies"}))
		})

		It("should include CREATE and UPDATE for deploymentPolicy mutating rules", func() {
			rules := deploymentPolicyMutatingRules()
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].Operations).To(ConsistOf(
				admissionregistrationv1.Create,
				admissionregistrationv1.Update,
			))
		})

		It("should include CREATE and UPDATE for skyhook rules", func() {
			rules := skyhookRules()
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].Operations).To(ConsistOf(
				admissionregistrationv1.Create,
				admissionregistrationv1.Update,
			))
		})
	})

	Describe("webhook rules comparison", func() {
		It("should detect when rules are different", func() {
			oldRules := []admissionregistrationv1.RuleWithOperations{
				{
					Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{v1alpha1.GroupVersion.Group},
						APIVersions: []string{v1alpha1.GroupVersion.Version},
						Resources:   []string{"skyhooks"},
					},
				},
			}

			webhook := admissionregistrationv1.ValidatingWebhook{
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: []byte("existing-ca"),
				},
				Rules: oldRules,
			}

			caBundle := []byte("new-ca")
			expectedRules := skyhookRules()

			needsUpdate := validatingWebhookNeedsUpdate(&webhook, caBundle, expectedRules)
			Expect(needsUpdate).To(BeTrue(), "should detect rules mismatch")
			Expect(webhook.Rules).To(Equal(expectedRules), "rules should be updated")
		})

		It("should not update when rules are identical", func() {
			expectedRules := skyhookRules()

			webhook := admissionregistrationv1.ValidatingWebhook{
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: []byte("existing-ca"),
				},
				Rules: expectedRules,
			}

			caBundle := []byte("existing-ca")

			needsUpdate := validatingWebhookNeedsUpdate(&webhook, caBundle, expectedRules)
			Expect(needsUpdate).To(BeFalse(), "should not detect changes when rules are identical")
		})

		It("should update CABundle when empty", func() {
			expectedRules := skyhookRules()

			webhook := admissionregistrationv1.MutatingWebhook{
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: nil, // Empty CABundle
				},
				Rules: expectedRules,
			}

			caBundle := []byte("new-ca")

			needsUpdate := mutatingWebhookNeedsUpdate(&webhook, caBundle, expectedRules)
			Expect(needsUpdate).To(BeTrue(), "should detect empty CABundle")
			Expect(webhook.ClientConfig.CABundle).To(Equal(caBundle), "CABundle should be updated")
		})

		It("should update CABundle when stale (non-empty but wrong)", func() {
			expectedRules := skyhookRules()
			correctCA := []byte("correct-ca")
			staleCA := []byte("stale-ca-from-previous-cert")

			validatingWebhook := admissionregistrationv1.ValidatingWebhook{
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: staleCA,
				},
				Rules: expectedRules,
			}
			needsUpdate := validatingWebhookNeedsUpdate(&validatingWebhook, correctCA, expectedRules)
			Expect(needsUpdate).To(BeTrue(), "should detect stale validating CABundle")
			Expect(validatingWebhook.ClientConfig.CABundle).To(Equal(correctCA))

			mutatingWebhook := admissionregistrationv1.MutatingWebhook{
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					CABundle: staleCA,
				},
				Rules: expectedRules,
			}
			needsUpdate = mutatingWebhookNeedsUpdate(&mutatingWebhook, correctCA, expectedRules)
			Expect(needsUpdate).To(BeTrue(), "should detect stale mutating CABundle")
			Expect(mutatingWebhook.ClientConfig.CABundle).To(Equal(correctCA))
		})
	})

	Describe("Disk and Secret-to-Disk Sync Logic", func() {
		It("should write and read cert and key files correctly", func() {
			err := writeCertAndKey([]byte(cachedCert.TLSCert), []byte(cachedCert.TLSKey), tmpDir)
			Expect(err).ToNot(HaveOccurred())
			writtenCert, err := os.ReadFile(filepath.Join(tmpDir, "tls.crt"))
			Expect(err).ToNot(HaveOccurred())
			Expect(writtenCert).To(Equal([]byte(cachedCert.TLSCert)))
			writtenKey, err := os.ReadFile(filepath.Join(tmpDir, "tls.key"))
			Expect(err).ToNot(HaveOccurred())
			Expect(writtenKey).To(Equal([]byte(cachedCert.TLSKey)))
		})
	})

	Describe("WebhookSecretReadyzCheck", func() {
		var (
			controller *WebhookController
			secret     *corev1.Secret
			tmpDir     string
		)

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "webhook-test-*")
			Expect(err).NotTo(HaveOccurred())

			// Create a test secret with valid cert data
			cert, err := generateCert(serviceName, namespace, 24*time.Hour)
			Expect(err).NotTo(HaveOccurred())

			secret = cert.ToSecret(secretName, namespace, serviceName)
			// Write cert to disk
			err = writeCertAndKey([]byte(cert.TLSCert), []byte(cert.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			// Create controller with test client
			controller = &WebhookController{
				Client:    fake.NewClientBuilder().WithObjects(secret).Build(),
				namespace: namespace,
				certDir:   tmpDir,
				opts: WebhookControllerOptions{
					SecretName:  secretName,
					ServiceName: serviceName,
				},
			}
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmpDir)
		})

		It("should return nil when all checks pass", func() {
			// Create webhook configurations
			validatingWebhook := webhookValidatingWebhookConfiguration(namespace, serviceName, secret)
			mutatingWebhook := webhookMutatingWebhookConfiguration(namespace, serviceName, secret)

			// Add webhook configurations to the fake client
			controller.Client = fake.NewClientBuilder().
				WithObjects(secret, validatingWebhook, mutatingWebhook).
				Build()

			// Write cert to disk
			err := writeCertAndKey(secret.Data["tls.crt"], secret.Data["tls.key"], tmpDir)
			Expect(err).NotTo(HaveOccurred())

			err = controller.WebhookSecretReadyzCheck(nil)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return error when secret is missing", func() {
			// Create controller with empty client
			controller.Client = fake.NewClientBuilder().Build()

			err := controller.WebhookSecretReadyzCheck(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("should return error when cert on disk doesn't match secret", func() {
			// Write different cert to disk
			differentCert, err := generateCert("different-service", "different-namespace", 24*time.Hour)
			Expect(err).NotTo(HaveOccurred())
			err = writeCertAndKey([]byte(differentCert.TLSCert), []byte(differentCert.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			err = controller.WebhookSecretReadyzCheck(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not ready"))
		})

		It("should return error when webhook configurations are missing", func() {
			err := controller.WebhookSecretReadyzCheck(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("should return error when webhook configuration CA bundle doesn't match", func() {
			// Create webhook configurations with different CA bundle
			differentCert, err := generateCert("different-service", "different-namespace", 24*time.Hour)
			Expect(err).NotTo(HaveOccurred())

			secretWithDifferentCA := secret.DeepCopy()
			secretWithDifferentCA.Data["ca.crt"] = differentCert.CABytes

			validatingWebhook := webhookValidatingWebhookConfiguration(namespace, serviceName, secretWithDifferentCA)
			mutatingWebhook := webhookMutatingWebhookConfiguration(namespace, serviceName, secretWithDifferentCA)

			controller.Client = fake.NewClientBuilder().
				WithObjects(secret, validatingWebhook, mutatingWebhook).
				Build()

			// Write original cert to disk
			err = writeCertAndKey(secret.Data["tls.crt"], secret.Data["tls.key"], tmpDir)
			Expect(err).NotTo(HaveOccurred())

			err = controller.WebhookSecretReadyzCheck(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ca bundle is not equal"))
		})
	})

	Describe("Certificate Management", func() {
		var (
			controller *WebhookController
			tmpDir     string
		)

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "webhook-test-*")
			Expect(err).NotTo(HaveOccurred())

			controller = &WebhookController{
				Client:    fake.NewClientBuilder().Build(),
				namespace: namespace,
				certDir:   tmpDir,
				opts: WebhookControllerOptions{
					SecretName:  secretName,
					ServiceName: serviceName,
				},
			}
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmpDir)
		})

		It("should create new certificate when secret doesn't exist", func() {
			secret, err := controller.GetOrCreateWebhookCertSecret(context.Background(), secretName, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(secret).NotTo(BeNil())
			Expect(secret.Data["ca.crt"]).NotTo(BeEmpty())
			Expect(secret.Data["tls.crt"]).NotTo(BeEmpty())
			Expect(secret.Data["tls.key"]).NotTo(BeEmpty())
			Expect(secret.Annotations[fmt.Sprintf("%s/expiration", v1alpha1.METADATA_PREFIX)]).NotTo(BeEmpty())
			Expect(secret.Annotations[fmt.Sprintf("%s/service", v1alpha1.METADATA_PREFIX)]).To(Equal(serviceName))
		})

		It("should update certificate when it's about to expire", func() {
			// Create initial secret with short-lived cert
			cert, err := generateCert(serviceName, namespace, 1*time.Hour)
			Expect(err).NotTo(HaveOccurred())

			secret := cert.ToSecret(secretName, namespace, serviceName)

			controller.Client = fake.NewClientBuilder().WithObjects(secret).Build()

			// Write cert to disk
			err = writeCertAndKey([]byte(cert.TLSCert), []byte(cert.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			updated, err := controller.CheckOrUpdateWebhookCertSecret(context.Background(), secret)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())

		})
	})
})
