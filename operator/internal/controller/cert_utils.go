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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nodewright/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// webhookCert contains the certificate data and expiration for webhook TLS
type webhookCert struct {
	CABytes         []byte
	TLSCert, TLSKey string
	Expiration      time.Time
}

func (c *webhookCert) ToSecret(name, namespace, serviceName string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				fmt.Sprintf("%s/expiration", v1alpha1.METADATA_PREFIX): c.Expiration.Format(time.RFC3339),
				fmt.Sprintf("%s/service", v1alpha1.METADATA_PREFIX):    serviceName,
			},
		},
		Data: map[string][]byte{
			"ca.crt":  c.CABytes,
			"tls.crt": []byte(c.TLSCert),
			"tls.key": []byte(c.TLSKey),
		},
	}
	return secret
}

// generateCert generates a new CA and a new cert signed by the CA.
// The CA is valid for the given duration.
// The cert is valid for the given duration and has the given DNS names.
func generateCert(serviceName, namespace string, duration time.Duration) (*webhookCert, error) {

	// create a new CA
	serialNumber, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, err
	}

	timeNow := time.Now()
	ca := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"NVIDIA"},
			CommonName:   "webhooks-ca",
		},
		SignatureAlgorithm: x509.SHA256WithRSA,
		NotBefore:          timeNow,
		NotAfter:           timeNow.Add(duration),
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, err
	}

	caCert, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, err
	}

	caPEM := new(bytes.Buffer)
	if err := pem.Encode(caPEM, &pem.Block{Type: "CERTIFICATE", Bytes: caCert}); err != nil {
		return nil, err
	}

	caPrivKeyPEM := new(bytes.Buffer)
	if err := pem.Encode(caPrivKeyPEM, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey)}); err != nil {
		return nil, err
	}

	// create a new cert from the CA
	serialNumber, err = rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, err
	}

	dnsNames := []string{
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		fmt.Sprintf("%s.%s.svc", serviceName, namespace),
	}
	cert := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"NVIDIA"},
			CommonName:   dnsNames[0],
		},
		NotBefore:   timeNow,
		NotAfter:    timeNow.Add(duration),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		DNSNames:    dnsNames,
	}

	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, err
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, err
	}

	certPEM := new(bytes.Buffer)
	if err := pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		return nil, err
	}

	certPrivKeyPEM := new(bytes.Buffer)
	if err := pem.Encode(certPrivKeyPEM, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey)}); err != nil {
		return nil, err
	}

	return &webhookCert{
		CABytes:    caPEM.Bytes(),
		TLSCert:    certPEM.String(),
		TLSKey:     certPrivKeyPEM.String(),
		Expiration: cert.NotAfter,
	}, nil
}

// ensureDummyCert ensures that a dummy cert is present in the certDir
// this is used to test the webhook server without having to create a real cert
func ensureDummyCert(certDir string) error {
	if certDir == "" {
		return fmt.Errorf("certDir is required")
	}

	// generate a dummy cert, this will be replaced shortly after startup with real cert
	cert, err := generateCert("foo", "bar", 10*time.Minute)
	if err != nil {
		return err
	}

	err = writeCertAndKey([]byte(cert.TLSCert), []byte(cert.TLSKey), certDir)
	if err != nil {
		return err
	}

	return nil
}

// Write cert and key to disk if CertDir is set, make sure the directory exists, and the files are 600. Will also sync from the secret to the disk.
func writeCertAndKey(certPEM, keyPEM []byte, certDir string) error {
	if certDir == "" {
		return fmt.Errorf("certDir is required")
	}
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return fmt.Errorf("failed to create certDir: %w", err)
	}

	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("failed to write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("failed to write key: %w", err)
	}
	return nil
}

// certExistsOnDisk returns an error if the cert or key is not present on disk
func certExistsOnDisk(certDir string) error {
	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")

	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return fmt.Errorf("cert file does not exist")
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return fmt.Errorf("key file does not exist")
	}

	return nil
}

// CompareCertOnDiskToSecret compares the cert and key on disk to the secret
// returns true if the cert and key on disk are different from the secret
func compareCertOnDiskToSecret(certDir string, secret *corev1.Secret) (bool, error) {
	// check if the cert and key are present on disk
	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")

	if err := certExistsOnDisk(certDir); err != nil {
		return false, fmt.Errorf("failed to check if cert and key exist on disk: %w", err)
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return false, fmt.Errorf("failed to read cert: %w", err)
	}
	secretCert, ok := secret.Data["tls.crt"]
	if !ok {
		return false, fmt.Errorf("secret missing tls.crt")
	}
	if !bytes.Equal(certPEM, secretCert) {
		return false, nil
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return false, fmt.Errorf("failed to read key: %w", err)
	}
	secretKey, ok := secret.Data["tls.key"]
	if !ok {
		return false, fmt.Errorf("secret missing tls.key")
	}
	if !bytes.Equal(keyPEM, secretKey) {
		return false, nil
	}

	return true, nil
}
