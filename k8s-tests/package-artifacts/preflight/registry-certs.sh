#!/bin/sh

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

## Issues a throwaway CA and a certificate for localhost from it, for e2e-registry, and hands
## them out: the key pair as the e2e-registry-tls Secret, the CA as the e2e-registry-ca ConfigMap
## for the pusher, and the CA on the test node at /etc/nodewright-e2e/ca.crt, which the test's
## packages name in SSL_CERT_FILE. The agent trusts only the CA bundle of its own image, so this
## is how it trusts the registry; see Limitations in docs/user-guide/package-artifacts.md.
## Config files rather than -addext, which LibreSSL on macOS lacks.

set -eu

node=$1
namespace=nodewright
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat > "$work/ca.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions = v3_ca
prompt = no
[dn]
CN = nodewright-e2e-registry-ca
[v3_ca]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
EOF

cat > "$work/server.ext" <<EOF
basicConstraints = CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:localhost,IP:127.0.0.1
EOF

openssl req -x509 -newkey rsa:2048 -nodes -days 2 -config "$work/ca.cnf" \
  -keyout "$work/ca.key" -out "$work/ca.crt" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" \
  -keyout "$work/tls.key" -out "$work/tls.csr" 2>/dev/null
openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
  -days 2 -extfile "$work/server.ext" -out "$work/tls.crt" 2>/dev/null

kubectl create secret tls e2e-registry-tls -n "$namespace" --cert "$work/tls.crt" --key "$work/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl create configmap e2e-registry-ca -n "$namespace" --from-file=ca.crt="$work/ca.crt" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl exec -i "${node}-debugger" -- sh -c 'mkdir -p /host/etc/nodewright-e2e && cat > /host/etc/nodewright-e2e/ca.crt' < "$work/ca.crt"
