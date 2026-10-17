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
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("WebhookController", Ordered, func() {
	var (
		secretName           string
		namespace            string
		serviceName          string
		validatingConfigName string
		mutatingConfigName   string
		tmpDir               string
		cachedCert           *webhookCert
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
		validatingConfigName = "test-validating-webhook"
		mutatingConfigName = "test-mutating-webhook"
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

	Describe("owned webhook configuration discovery", func() {
		var controller *WebhookController

		BeforeEach(func() {
			controller = &WebhookController{namespace: namespace, opts: WebhookControllerOptions{SecretName: secretName, ServiceName: serviceName}}
		})

		It("finds configurations by label regardless of their name", func() {
			controller.Client = fake.NewClientBuilder().WithObjects(
				validatingWebhookConfig("any-name-at-all", namespace, serviceName, nil),
				mutatingWebhookConfig("some-other-name", namespace, serviceName, nil),
			).Build()

			validating, err := controller.ownedValidatingWebhookConfigurations(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(validating).To(HaveLen(1))
			Expect(validating[0].Name).To(Equal("any-name-at-all"))

			mutating, err := controller.ownedMutatingWebhookConfigurations(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(mutating).To(HaveLen(1))
			Expect(mutating[0].Name).To(Equal("some-other-name"))
		})

		It("ignores configurations that target another namespace", func() {
			// The webhook configurations are cluster-scoped, so a second install elsewhere
			// matches the same label; without the namespace filter the two operators would
			// overwrite each other's caBundle.
			controller.Client = fake.NewClientBuilder().WithObjects(
				validatingWebhookConfig("ours", namespace, serviceName, nil),
				validatingWebhookConfig("theirs", "other-namespace", serviceName, nil),
			).Build()

			validating, err := controller.ownedValidatingWebhookConfigurations(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(validating).To(HaveLen(1))
			Expect(validating[0].Name).To(Equal("ours"))
		})

		It("ignores configurations that dial a different Service in the same namespace", func() {
			// Two installs can share a namespace. The caBundle only signs the cert for
			// r.opts.ServiceName, so injecting it into a configuration that dials another
			// Service would break that Service's admission rather than just being untidy.
			controller.Client = fake.NewClientBuilder().WithObjects(
				validatingWebhookConfig("ours", namespace, serviceName, nil),
				validatingWebhookConfig("theirs", namespace, "someone-elses-webhook-service", nil),
			).Build()

			validating, err := controller.ownedValidatingWebhookConfigurations(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(validating).To(HaveLen(1))
			Expect(validating[0].Name).To(Equal("ours"))

			changed, err := controller.updateValidatingWebhookConfiguration(context.Background(), cachedCert.CABytes)
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).To(BeTrue())

			theirs := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			Expect(controller.Get(context.Background(), types.NamespacedName{Name: "theirs"}, theirs)).To(Succeed())
			Expect(theirs.Webhooks[0].ClientConfig.CABundle).To(BeEmpty())
		})

		It("ignores configurations without the marker label", func() {
			unlabelled := validatingWebhookConfig("unlabelled", namespace, serviceName, nil)
			unlabelled.Labels = nil
			controller.Client = fake.NewClientBuilder().WithObjects(unlabelled).Build()

			_, err := controller.ownedValidatingWebhookConfigurations(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("creation is handled by the Helm chart"))
		})

		It("leaves a foreign webhook inside an owned configuration alone", func() {
			// Ownership is claimed per configuration when ANY webhook dials our Service,
			// but the caBundle only signs that one Service's cert, so a sibling webhook
			// pointing elsewhere must not be written. Mutation scope has to match the
			// scope that decided ownership.
			conf := validatingWebhookConfig("mixed", namespace, serviceName, nil)
			conf.Webhooks = append(conf.Webhooks, admissionregistrationv1.ValidatingWebhook{
				Name: "foreign.example.com",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{Name: "someone-elses-svc", Namespace: namespace},
				},
			})
			controller.Client = fake.NewClientBuilder().WithObjects(conf).Build()

			_, err := controller.updateValidatingWebhookConfiguration(context.Background(), cachedCert.CABytes)
			Expect(err).NotTo(HaveOccurred())

			got := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			Expect(controller.Get(context.Background(), types.NamespacedName{Name: "mixed"}, got)).To(Succeed())
			Expect(got.Webhooks[0].ClientConfig.CABundle).To(Equal(cachedCert.CABytes), "our webhook gets the CA")
			Expect(got.Webhooks[1].ClientConfig.CABundle).To(BeEmpty(), "the foreign webhook must be untouched")
		})

		It("annotates a Forbidden update with the RBAC coupling that causes it", func() {
			// Label discovery removes the name coupling from the lookup but not from the
			// manager ClusterRole's resourceNames, so this is the one drift that can still
			// happen. The bare apierror says nothing about why.
			forbidden := apierrors.NewForbidden(
				schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: "validatingwebhookconfigurations"},
				"renamed-config", fmt.Errorf("not allowed"))

			err := updateWebhookConfigurationsErr("ValidatingWebhookConfiguration", "renamed-config", forbidden)
			Expect(err.Error()).To(ContainSubstring("resourceNames"))
			Expect(err.Error()).To(ContainSubstring("renamed-config"))
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "the apierror must stay unwrapped-detectable")
		})

		It("injects the CA into every owned configuration, not just the first", func() {
			// A chart upgrade that renames a configuration leaves both the old and the new
			// object present for a moment; patching only one leaves the other failing closed.
			controller.Client = fake.NewClientBuilder().WithObjects(
				validatingWebhookConfig("old-name", namespace, serviceName, nil),
				validatingWebhookConfig("new-name", namespace, serviceName, nil),
				mutatingWebhookConfig("mutating", namespace, serviceName, nil),
			).Build()

			changed, err := controller.updateValidatingWebhookConfiguration(context.Background(), cachedCert.CABytes)
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).To(BeTrue())

			for _, name := range []string{"old-name", "new-name"} {
				conf := &admissionregistrationv1.ValidatingWebhookConfiguration{}
				Expect(controller.Get(context.Background(), types.NamespacedName{Name: name}, conf)).To(Succeed())
				Expect(conf.Webhooks[0].ClientConfig.CABundle).To(Equal(cachedCert.CABytes))
			}

			// The mutating path carries the same loop, so cover it here rather than
			// leaving the fixture's mutating configuration asserted-on-by-nobody.
			changed, err = controller.updateMutatingWebhookConfiguration(context.Background(), cachedCert.CABytes)
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).To(BeTrue())

			mut := &admissionregistrationv1.MutatingWebhookConfiguration{}
			Expect(controller.Get(context.Background(), types.NamespacedName{Name: "mutating"}, mut)).To(Succeed())
			Expect(mut.Webhooks[0].ClientConfig.CABundle).To(Equal(cachedCert.CABytes))
		})
	})

	Describe("relinquishing the webhook bootstrap lease", func() {
		var controller *WebhookController

		BeforeEach(func() {
			controller = &WebhookController{
				namespace:    namespace,
				opts:         WebhookControllerOptions{SecretName: secretName, ServiceName: serviceName},
				relinquished: make(chan struct{}, 1),
			}
		})

		It("keeps erroring when nothing else is installed here", func() {
			// Nothing dials anything: the chart's configurations are simply absent. That is
			// a real error and must stay one, or a broken install looks like a healthy pod
			// that has politely stood aside.
			controller.Client = fake.NewClientBuilder().Build()

			_, err := controller.handleNoOwnedConfigurations(context.Background(), errNoOwnedWebhookConfigurations)
			Expect(err).To(MatchError(errNoOwnedWebhookConfigurations))
			Expect(controller.relinquished).NotTo(Receive())
		})

		It("waits out the grace period before standing aside", func() {
			// A helm upgrade that renames the Service applies the Deployment and the webhook
			// configurations in one pass, so this state can appear for a moment on a healthy
			// upgrade. Reacting immediately would make that a self-inflicted outage.
			controller.Client = fake.NewClientBuilder().WithObjects(
				validatingWebhookConfig("theirs", namespace, "some-other-webhook-service", nil),
			).Build()

			result, err := controller.handleNoOwnedConfigurations(context.Background(), errNoOwnedWebhookConfigurations)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(supersededPollInterval))
			Expect(controller.relinquished).NotTo(Receive())
			Expect(controller.supersededSince).NotTo(BeZero())
		})

		It("stands aside once the condition has held for the grace period", func() {
			controller.Client = fake.NewClientBuilder().WithObjects(
				validatingWebhookConfig("theirs", namespace, "some-other-webhook-service", nil),
			).Build()
			controller.supersededSince = time.Now().Add(-2 * supersededGracePeriod)

			_, err := controller.handleNoOwnedConfigurations(context.Background(), errNoOwnedWebhookConfigurations)
			Expect(err).NotTo(HaveOccurred())
			Expect(controller.relinquished).To(Receive())
		})

		It("stands aside for a configuration that carries no marker label", func() {
			// The release rolled back to may predate the label entirely, which is exactly
			// the case in #469. Detection cannot use the ownership filter for that reason.
			unlabelled := validatingWebhookConfig("theirs", namespace, "some-other-webhook-service", nil)
			unlabelled.Labels = nil
			controller.Client = fake.NewClientBuilder().WithObjects(unlabelled).Build()
			controller.supersededSince = time.Now().Add(-2 * supersededGracePeriod)

			_, err := controller.handleNoOwnedConfigurations(context.Background(), errNoOwnedWebhookConfigurations)
			Expect(err).NotTo(HaveOccurred())
			Expect(controller.relinquished).To(Receive())
		})

		It("ignores webhook services in other namespaces", func() {
			// Another operator in another namespace is not our replacement.
			elsewhere := validatingWebhookConfig("elsewhere", "some-other-namespace", "some-other-webhook-service", nil)
			controller.Client = fake.NewClientBuilder().WithObjects(elsewhere).Build()
			controller.supersededSince = time.Now().Add(-2 * supersededGracePeriod)

			_, err := controller.handleNoOwnedConfigurations(context.Background(), errNoOwnedWebhookConfigurations)
			Expect(err).To(MatchError(errNoOwnedWebhookConfigurations))
			Expect(controller.relinquished).NotTo(Receive())
			Expect(controller.supersededSince).To(BeZero(), "the debounce resets when the condition stops holding")
		})

		It("discards a stale relinquish signal", func() {
			// A reconcile still in flight while the previous manager shuts down can leave a
			// signal buffered. Acted on, it would cancel the NEXT manager before it ran a
			// single reconcile, and the loop would never hold the lease long enough to do
			// anything, even once the condition cleared.
			controller.relinquished <- struct{}{}
			controller.DrainRelinquished()
			Expect(controller.relinquished).NotTo(Receive())

			// And it stays safe to call when there is nothing to discard.
			controller.DrainRelinquished()
		})

		It("watches configurations that dial this namespace even without the marker label", func() {
			// What the detection reads, the watch has to deliver. A rollback restores
			// configurations from a release that predates the label, and those are exactly
			// the ones supersedingService is looking for.
			unlabelled := validatingWebhookConfig("theirs", namespace, "some-other-webhook-service", nil)
			unlabelled.Labels = nil
			Expect(controller.watchedWebhookConfiguration(unlabelled)).To(BeTrue())

			labelled := validatingWebhookConfig("ours", namespace, serviceName, nil)
			Expect(controller.watchedWebhookConfiguration(labelled)).To(BeTrue())

			mutatingUnlabelled := mutatingWebhookConfig("theirs", namespace, "some-other-webhook-service", nil)
			mutatingUnlabelled.Labels = nil
			Expect(controller.watchedWebhookConfiguration(mutatingUnlabelled)).To(BeTrue())
		})

		It("ignores unlabelled configurations belonging to other namespaces", func() {
			// Otherwise every unrelated admission component in the cluster wakes this
			// controller.
			elsewhere := validatingWebhookConfig("elsewhere", "some-other-namespace", "some-other-webhook-service", nil)
			elsewhere.Labels = nil
			Expect(controller.watchedWebhookConfiguration(elsewhere)).To(BeFalse())
		})

		It("finds a superseding service declared only on a mutating configuration", func() {
			controller.Client = fake.NewClientBuilder().WithObjects(
				mutatingWebhookConfig("theirs", namespace, "some-other-webhook-service", nil),
			).Build()

			other, err := controller.supersedingService(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(other).To(Equal("some-other-webhook-service"))
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

		// The chart's webhook configs also carry the nodewright mirror webhooks,
		// whose names the operator does not own (getXWebhookRules returns nil).
		// The operator must still inject the caBundle into them (they point at
		// its service) while leaving their chart-defined rules untouched.
		It("injects the caBundle into unowned webhooks without clobbering their rules", func() {
			chartRules := deploymentPolicyMutatingRules()
			// Keep an independent copy of the expected rules and give each webhook its own
			// slice, so an accidental in-place mutation by the update helpers is caught
			// (a shared reference would compare equal to itself and hide the bug).
			expectedRules := make([]admissionregistrationv1.RuleWithOperations, len(chartRules))
			for i := range chartRules {
				chartRules[i].DeepCopyInto(&expectedRules[i])
			}

			validating := &admissionregistrationv1.ValidatingWebhook{
				Name:         "validate-nodewright.nvidia.com",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{},
				Rules:        append([]admissionregistrationv1.RuleWithOperations(nil), chartRules...),
			}
			Expect(validatingWebhookNeedsUpdate(validating, []byte("the-ca"), nil)).To(BeTrue())
			Expect(validating.ClientConfig.CABundle).To(Equal([]byte("the-ca")))
			Expect(validating.Rules).To(Equal(expectedRules))
			// Idempotent once the caBundle matches.
			Expect(validatingWebhookNeedsUpdate(validating, []byte("the-ca"), nil)).To(BeFalse())

			mutating := &admissionregistrationv1.MutatingWebhook{
				Name:         "mutate-nodewright.nvidia.com",
				ClientConfig: admissionregistrationv1.WebhookClientConfig{},
				Rules:        append([]admissionregistrationv1.RuleWithOperations(nil), chartRules...),
			}
			Expect(mutatingWebhookNeedsUpdate(mutating, []byte("the-ca"), nil)).To(BeTrue())
			Expect(mutating.ClientConfig.CABundle).To(Equal([]byte("the-ca")))
			Expect(mutating.Rules).To(Equal(expectedRules))
			Expect(mutatingWebhookNeedsUpdate(mutating, []byte("the-ca"), nil)).To(BeFalse())
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
			validatingWebhook := validatingWebhookConfig(validatingConfigName, namespace, serviceName, secret.Data["ca.crt"])
			mutatingWebhook := mutatingWebhookConfig(mutatingConfigName, namespace, serviceName, secret.Data["ca.crt"])

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

		It("should return error when a NON-FIRST webhook has a stale CA bundle", func() {
			// The API server dials each webhook independently, so a first-entry-only
			// readiness check reports the pod ready while later webhooks reject everything.
			stale, err := generateCert("stale-service", namespace, 24*time.Hour)
			Expect(err).NotTo(HaveOccurred())

			validatingWebhook := validatingWebhookConfig(validatingConfigName, namespace, serviceName, secret.Data["ca.crt"])
			validatingWebhook.Webhooks = append(validatingWebhook.Webhooks, admissionregistrationv1.ValidatingWebhook{
				Name: deploymentPolicyValidatingWebhookName,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					Service:  &admissionregistrationv1.ServiceReference{Name: serviceName, Namespace: namespace},
					CABundle: stale.CABytes,
				},
			})
			mutatingWebhook := mutatingWebhookConfig(mutatingConfigName, namespace, serviceName, secret.Data["ca.crt"])

			controller.Client = fake.NewClientBuilder().
				WithObjects(secret, validatingWebhook, mutatingWebhook).
				Build()

			err = writeCertAndKey(secret.Data["tls.crt"], secret.Data["tls.key"], tmpDir)
			Expect(err).NotTo(HaveOccurred())

			err = controller.WebhookSecretReadyzCheck(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(deploymentPolicyValidatingWebhookName))
		})

		It("should return error when webhook configurations are missing", func() {
			err := controller.WebhookSecretReadyzCheck(nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no ValidatingWebhookConfiguration labelled"))
			Expect(err.Error()).To(ContainSubstring("creation is handled by the Helm chart"))
		})

		It("should return error when webhook configuration CA bundle doesn't match", func() {
			// Create webhook configurations with different CA bundle
			differentCert, err := generateCert("different-service", "different-namespace", 24*time.Hour)
			Expect(err).NotTo(HaveOccurred())

			secretWithDifferentCA := secret.DeepCopy()
			secretWithDifferentCA.Data["ca.crt"] = differentCert.CABytes

			validatingWebhook := validatingWebhookConfig(validatingConfigName, namespace, serviceName, secretWithDifferentCA.Data["ca.crt"])
			mutatingWebhook := mutatingWebhookConfig(mutatingConfigName, namespace, serviceName, secretWithDifferentCA.Data["ca.crt"])

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

		It("should remint the certificate when the webhook Service has been renamed", func() {
			// The cert Secret is operator-owned, not chart-owned, so it survives a chart
			// upgrade that renames the webhook Service. A long-lived cert carrying the old
			// SAN would then be reused and the API server would reject the webhook call with
			// an x509 error until the cert expired.
			cert, err := generateCert("old-webhook-service", namespace, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())

			secret := cert.ToSecret(secretName, namespace, "old-webhook-service")
			controller.Client = fake.NewClientBuilder().WithObjects(secret).Build()

			err = writeCertAndKey([]byte(cert.TLSCert), []byte(cert.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			updated, err := controller.CheckOrUpdateWebhookCertSecret(context.Background(), secret)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())
			Expect(secret.Annotations[serviceAnnotationKey]).To(Equal(serviceName))

			block, _ := pem.Decode(secret.Data["tls.crt"])
			Expect(block).NotTo(BeNil())
			parsed, err := x509.ParseCertificate(block.Bytes)
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed.DNSNames).To(ContainElements(
				fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
				fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			))
			Expect(parsed.DNSNames).NotTo(ContainElement(
				fmt.Sprintf("old-webhook-service.%s.svc", namespace),
			))
		})

		It("should carry a SAN for the pre-rename Service name as well", func() {
			// RENAME-SHIM: a rollback to a pre-rename release leaves this cert in place,
			// serving a Service the older operator names skyhook-operator-webhook-service.
			// That operator has no remint-on-Service-change check, so the cert it inherits
			// has to already be valid for the name the API server will dial (#469).
			cert, err := generateCert(serviceName, namespace, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())

			block, _ := pem.Decode([]byte(cert.TLSCert))
			Expect(block).NotTo(BeNil())
			parsed, err := x509.ParseCertificate(block.Bytes)
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed.DNSNames).To(ContainElements(
				fmt.Sprintf("%s.%s.svc.cluster.local", legacyWebhookServiceName, namespace),
				fmt.Sprintf("%s.%s.svc", legacyWebhookServiceName, namespace),
			))
		})

		It("should remint a certificate that predates the pre-rename SAN", func() {
			// Clusters upgraded before this shipped carry a single-name cert. They have to
			// be reminted on upgrade, or their rollback is still broken. The check reads the
			// SANs rather than the service annotation, which is what makes this detectable.
			cert, err := generateCert(serviceName, namespace, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())

			legacyOnly, err := generateCertWithDNSNames([]string{
				fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
				fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			}, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())

			secret := cert.ToSecret(secretName, namespace, serviceName)
			secret.Data["tls.crt"] = []byte(legacyOnly.TLSCert)
			secret.Data["tls.key"] = []byte(legacyOnly.TLSKey)
			controller.Client = fake.NewClientBuilder().WithObjects(secret).Build()

			err = writeCertAndKey([]byte(legacyOnly.TLSCert), []byte(legacyOnly.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			updated, err := controller.CheckOrUpdateWebhookCertSecret(context.Background(), secret)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())

			block, _ := pem.Decode(secret.Data["tls.crt"])
			Expect(block).NotTo(BeNil())
			parsed, err := x509.ParseCertificate(block.Bytes)
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed.DNSNames).To(ContainElement(
				fmt.Sprintf("%s.%s.svc", legacyWebhookServiceName, namespace),
			))
		})

		It("should not remint the certificate when nothing has changed", func() {
			cert, err := generateCert(serviceName, namespace, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())

			secret := cert.ToSecret(secretName, namespace, serviceName)
			controller.Client = fake.NewClientBuilder().WithObjects(secret).Build()

			err = writeCertAndKey([]byte(cert.TLSCert), []byte(cert.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			updated, err := controller.CheckOrUpdateWebhookCertSecret(context.Background(), secret)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeFalse())
		})

		It("should return an error and leave the Secret untouched when the cert cannot be written to disk", func() {
			diskCert, err := generateCert(serviceName, namespace, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())
			secretCert, err := generateCert(serviceName, namespace, certValidityDurationYear)
			Expect(err).NotTo(HaveOccurred())

			secret := secretCert.ToSecret(secretName, namespace, serviceName)
			controller.Client = fake.NewClientBuilder().WithObjects(secret).Build()

			// Disk holds a different cert than the Secret, so rotation is triggered.
			err = writeCertAndKey([]byte(diskCert.TLSCert), []byte(diskCert.TLSKey), tmpDir)
			Expect(err).NotTo(HaveOccurred())

			// Turn tls.key into a directory so the write fails even when tests run as root.
			keyPath := filepath.Join(tmpDir, "tls.key")
			Expect(os.Remove(keyPath)).To(Succeed())
			Expect(os.Mkdir(keyPath, 0o700)).To(Succeed())

			origCert := append([]byte(nil), secret.Data["tls.crt"]...)

			updated, err := controller.CheckOrUpdateWebhookCertSecret(context.Background(), secret)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("writing rotated webhook cert"))
			Expect(updated).To(BeFalse())
			Expect(secret.Data["tls.crt"]).To(Equal(origCert))
		})
	})
})

// validatingWebhookConfig builds a chart-shaped ValidatingWebhookConfiguration: carrying the
// marker label the operator selects on, and pointing its clientConfig at a Service in
// namespace. The name is a free parameter precisely because the operator must not care
// about it.
func validatingWebhookConfig(name, namespace, serviceName string, caBundle []byte) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{webhookConfigLabelKey: "true"},
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: skyhookValidatingWebhookName,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service:  &admissionregistrationv1.ServiceReference{Name: serviceName, Namespace: namespace},
				CABundle: caBundle,
			},
			Rules: skyhookRules(),
		}},
	}
}

func mutatingWebhookConfig(name, namespace, serviceName string, caBundle []byte) *admissionregistrationv1.MutatingWebhookConfiguration {
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{webhookConfigLabelKey: "true"},
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: skyhookMutatingWebhookName,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service:  &admissionregistrationv1.ServiceReference{Name: serviceName, Namespace: namespace},
				CABundle: caBundle,
			},
			Rules: skyhookRules(),
		}},
	}
}
