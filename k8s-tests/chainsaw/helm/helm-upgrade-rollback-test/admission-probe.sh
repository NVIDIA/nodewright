#!/bin/bash

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

## Applies a fixture the validating webhook must reject and checks it was rejected by
## validation, not by TLS or a refused connection. Deliberately without `-e`: the apply
## is expected to fail.
##
## Ready does not mean the webhook Service is reachable yet (endpoints and kube-proxy lag
## the pod), and both the upgrade and the rollback recreate that Service, so retry while
## the call cannot connect (#555).
##
## Usage: admission-probe.sh <fixture> <webhook name> <CR kind> <CR name> <TLS failure hint>

FIXTURE=$1
WEBHOOK=$2
KIND=$3
NAME=$4
TLS_HINT=$5

for i in $(seq 1 30); do
  kubectl apply -f "${FIXTURE}" 2>err.txt
  ec=$?
  if [ $ec -eq 0 ] || ! grep -qi "connection refused\|no endpoints available" err.txt; then
    break
  fi
  echo "webhook not reachable yet (attempt $i), retrying"
  sleep 2
done
cat err.txt
if [ $ec -eq 0 ]; then
  echo "ERROR: invalid CR was accepted; the validating webhook is not being reached"
  kubectl delete "${KIND}" "${NAME}" --ignore-not-found
  exit 1
fi
if grep -qi "x509\|tls\|certificate" err.txt; then
  echo "ERROR: webhook failed on TLS, not validation; ${TLS_HINT}"
  exit 1
fi
if ! grep -q "admission webhook \"${WEBHOOK}\" denied the request" err.txt; then
  echo "ERROR: rejected for the wrong reason"
  exit 1
fi
echo "✓ admission reached the webhook and was denied by validation"
