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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	runtimecache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SecretCertWatcher watches a Secret and syncs its certs to disk
// This should be registered as a Runnable with the manager, so it runs on all pods
// regardless of leadership.
// This is needed because we need to make sure all pods keep there certs up to date with the secret,
// otherwise you can errors from the dumby cert that is created to make things happy enough to start.
// If you see errors about a service foo.bar and tls, then wait, it should update soon.
type SecretCertWatcher struct {
	client     client.Client
	cache      runtimecache.Cache
	namespace  string
	secretName string
	certDir    string
}

// WebhookSecretCacheConfig scopes a manager's Secret informer to the one Secret the operator
// reads, the webhook serving cert. Every manager that caches Secrets must use it: the Role
// grants list and watch on that name only, and the apiserver allows a list or watch under
// resourceNames only when the request carries this metadata.name field selector, so an
// informer without it is rejected and never syncs.
func WebhookSecretCacheConfig(namespace, secretName string) runtimecache.ByObject {
	return runtimecache.ByObject{
		Namespaces: map[string]runtimecache.Config{
			namespace: {FieldSelector: fields.OneTermEqualSelector("metadata.name", secretName)},
		},
	}
}

func NewSecretCertWatcher(client client.Client, cache runtimecache.Cache, namespace, secretName, certDir string) *SecretCertWatcher {
	return &SecretCertWatcher{
		client:     client,
		cache:      cache,
		namespace:  namespace,
		secretName: secretName,
		certDir:    certDir,
	}
}

func (s *SecretCertWatcher) Start(ctx context.Context) error {
	if cache := s.cache.WaitForCacheSync(ctx); !cache {
		return fmt.Errorf("failed to wait for cache to sync")
	}

	informer, err := s.cache.GetInformer(ctx, &corev1.Secret{})
	if err != nil {
		return err
	}
	// Initial sync
	err = s.syncSecretToDisk(ctx)
	if err != nil {
		return fmt.Errorf("failed to do initial sync of secret to disk: %w", err)
	}
	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			secret, ok := obj.(*corev1.Secret)
			if ok && secret.Name == s.secretName && secret.Namespace == s.namespace {
				err := s.syncSecretToDisk(ctx)
				if err != nil {
					log.FromContext(ctx).Error(err, "failed to sync secret to disk")
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			secret, ok := newObj.(*corev1.Secret)
			if ok && secret.Name == s.secretName && secret.Namespace == s.namespace {
				err := s.syncSecretToDisk(ctx)
				if err != nil {
					log.FromContext(ctx).Error(err, "failed to sync secret to disk")
				}
			}
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler to informer: %w", err)
	}
	<-ctx.Done() // block forever
	return nil
}

func (s *SecretCertWatcher) syncSecretToDisk(ctx context.Context) error {
	secret := &corev1.Secret{}
	err := s.client.Get(ctx, types.NamespacedName{Name: s.secretName, Namespace: s.namespace}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			log.FromContext(ctx).Info("secret not found, waiting for it to be created")
			return nil
		}
		return err
	}

	err = certExistsOnDisk(s.certDir)
	if err != nil {
		return writeCertAndKey(secret.Data["tls.crt"], secret.Data["tls.key"], s.certDir)
	}

	equal, err := compareCertOnDiskToSecret(s.certDir, secret)
	if err != nil {
		return err
	}
	if !equal {
		return writeCertAndKey(secret.Data["tls.crt"], secret.Data["tls.key"], s.certDir)
	}

	return nil
}

// NeedLeaderElection implements the Runnable interface, runs on all pods
func (r *SecretCertWatcher) NeedLeaderElection() bool {
	return false
}
