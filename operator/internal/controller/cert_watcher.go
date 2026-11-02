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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerruntime "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SecretCertWatcher is a controller-runtime controller that watches the webhook
// Secret and syncs its cert/key to disk. It runs on every pod regardless of
// leadership (NeedLeaderElection: false). A failed write returns an error so
// controller-runtime requeues it with automatic backoff.
type SecretCertWatcher struct {
	client     client.Client
	namespace  string
	secretName string
	certDir    string
}

func NewSecretCertWatcher(client client.Client, namespace, secretName, certDir string) *SecretCertWatcher {
	return &SecretCertWatcher{
		client:     client,
		namespace:  namespace,
		secretName: secretName,
		certDir:    certDir,
	}
}

func (s *SecretCertWatcher) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if req.Name != s.secretName || req.Namespace != s.namespace {
		return reconcile.Result{}, nil
	}
	secret := &corev1.Secret{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: s.secretName, Namespace: s.namespace}, secret); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	equal, err := compareCertOnDiskToSecret(s.certDir, secret)
	if err == nil && equal {
		return reconcile.Result{}, nil
	}
	if err := writeCertAndKey(secret.Data["tls.crt"], secret.Data["tls.key"], s.certDir); err != nil {
		return reconcile.Result{}, fmt.Errorf("syncing webhook cert to disk: %w", err)
	}
	return reconcile.Result{}, nil
}

func (s *SecretCertWatcher) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		WithOptions(controllerruntime.Options{NeedLeaderElection: ptr(false)}).
		Complete(s)
}
