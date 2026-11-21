#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Measure the operator's memory against kwok fake nodes, for the sizing in
# docs/operations/resources-at-scale.md. kwok simulates the nodes, the workload pods and the
# package Jobs' pods, so nothing runs on the nodes and the cluster needs no capacity for them:
# what is measured is the operator itself, its informer caches and its reconcile path.
#
# Prerequisites:
#   - kubectl, curl and jq, with the current context set to the test cluster.
#   - kwok in the cluster, with the fast stages, which also complete Job pods at once:
#       kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.8.0/kwok.yaml
#       kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.8.0/stage-fast.yaml
#   - The operator running with PPROF_PORT set and its pprof endpoint reachable at --pprof-url:
#     a manager run locally with PPROF_PORT=127.0.0.1:6060, or a port-forward to the leader pod
#     as described in docs/operations/resources-at-scale.md#profiling-the-operator.
#
# A run creates the nodes and the workload, records the operator's memory while it is idle,
# samples it while a NodeWright rolls out to every node, records it again once the NodeWright is
# complete, and then deletes everything it created. Each recording appends a CSV row to --output.

set -euo pipefail

NODE_PREFIX="scale-kwok-node-"
NODE_LABEL_KEY="nodewright.nvidia.com/scale-test"
NODE_LABEL_VALUE="kwok"
LOAD_NAMESPACE="scale-test-load"
NODEWRIGHT_NAME="scale-test-kwok"
KWOK_TAINT_KEY="kwok.x-k8s.io/node"

NODES=""
PODS_PER_NODE=10
PACKAGES=1
NAMESPACE="${NAMESPACE:-nodewright}"
PPROF_URL="http://127.0.0.1:6060"
PID=""
OUTPUT="scale_test_kwok_results.csv"
TIMEOUT=3600
SETTLE=30
KEEP=0

usage() {
  cat <<EOF
Usage: $0 --nodes N [options]

Options:
  --nodes N          Number of kwok fake nodes (required)
  --pods-per-node K  Workload pods per node, besides DaemonSet pods (default: ${PODS_PER_NODE})
  --packages P       Packages in the NodeWright (default: ${PACKAGES})
  --namespace NS     Operator namespace, where the package Jobs run (default: ${NAMESPACE})
  --pprof-url URL    The operator's pprof endpoint (default: ${PPROF_URL})
  --pid PID          PID of a manager running on this machine, to also record its RSS
  --output FILE      CSV to append results to (default: ${OUTPUT})
  --timeout SECONDS  How long to wait for each step (default: ${TIMEOUT})
  --settle SECONDS   How long to wait after completion before the last recording (default: ${SETTLE})
  --keep             Leave the nodes, workload and NodeWright in place
  --help             This help
EOF
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nodes)         NODES="$2"; shift 2 ;;
    --pods-per-node) PODS_PER_NODE="$2"; shift 2 ;;
    --packages)      PACKAGES="$2"; shift 2 ;;
    --namespace)     NAMESPACE="$2"; shift 2 ;;
    --pprof-url)     PPROF_URL="$2"; shift 2 ;;
    --pid)           PID="$2"; shift 2 ;;
    --output)        OUTPUT="$2"; shift 2 ;;
    --timeout)       TIMEOUT="$2"; shift 2 ;;
    --settle)        SETTLE="$2"; shift 2 ;;
    --keep)          KEEP=1; shift ;;
    --help)          usage ;;
    *) echo "Unknown option: $1" >&2; usage 1 ;;
  esac
done

if ! [[ "${NODES}" =~ ^[1-9][0-9]*$ ]]; then
  echo "--nodes must be a positive integer" >&2
  usage 1
fi
LETTERS="abcdefghijklmnopqrstuvwxyz"
if ! [[ "${PACKAGES}" =~ ^[1-9][0-9]*$ ]] || (( PACKAGES > ${#LETTERS} )); then
  echo "--packages must be between 1 and ${#LETTERS}" >&2
  usage 1
fi
for tool in kubectl curl jq; do
  command -v "${tool}" >/dev/null || { echo "${tool} is required" >&2; exit 1; }
done
if ! kubectl get stages.kwok.x-k8s.io pod-complete >/dev/null 2>&1; then
  echo "kwok with its fast stages is not installed in this cluster; see the prerequisites at the top of $0" >&2
  exit 1
fi
if ! curl -fsS -o /dev/null "${PPROF_URL}/debug/pprof/"; then
  echo "no pprof endpoint at ${PPROF_URL}; start the operator with PPROF_PORT set" >&2
  exit 1
fi

log() {
  printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2
}

# memstats prints HeapAlloc, HeapInuse and Sys in MiB, comma-separated. With gc=1 the runtime
# collects first, so HeapAlloc is the live heap; with gc=0 it is the heap at this instant.
memstats() {
  curl -fsS "${PPROF_URL}/debug/pprof/heap?debug=1&gc=$1" |
    awk '/^# HeapAlloc = /{a=$4} /^# HeapInuse = /{i=$4} /^# Sys = /{s=$4}
         END{printf "%.0f,%.0f,%.0f", a/1048576, i/1048576, s/1048576}'
}

rss_mib() {
  if [[ -n "${PID}" ]]; then
    echo $(( $(ps -o rss= -p "${PID}") / 1024 ))
  fi
}

record() {
  local phase="$1" stats="$2" rss="$3" pods jobs
  pods="$(kubectl get pods -A --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  jobs="$(kubectl get jobs -n "${NAMESPACE}" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ ! -s "${OUTPUT}" ]]; then
    echo "nodes,pods_per_node,packages,phase,heap_alloc_mib,heap_inuse_mib,sys_mib,rss_mib,pods,jobs,timestamp" >> "${OUTPUT}"
  fi
  echo "${NODES},${PODS_PER_NODE},${PACKAGES},${phase},${stats},${rss},${pods},${jobs},$(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "${OUTPUT}"
}

# wait_for runs "$@" every 5s until it succeeds or TIMEOUT passes.
wait_for() {
  local what="$1" deadline=$((SECONDS + TIMEOUT))
  shift
  until "$@"; do
    if (( SECONDS > deadline )); then
      echo "timed out waiting for ${what}" >&2
      exit 1
    fi
    sleep 5
  done
}

nodes_ready() {
  [[ "$(kubectl get nodes -l "${NODE_LABEL_KEY}=${NODE_LABEL_VALUE}" -o json |
    jq '[.items[] | select(.status.conditions[]? | .type == "Ready" and .status == "True")] | length')" -ge "${NODES}" ]]
}

load_ready() {
  [[ "$(kubectl -n "${LOAD_NAMESPACE}" get deployment scale-test-load -o jsonpath='{.status.readyReplicas}')" == "$((NODES * PODS_PER_NODE))" ]]
}

nodewright_complete() {
  [[ "$(kubectl get nodewright "${NODEWRIGHT_NAME}" -o jsonpath='{.status.status}{" "}{.status.completeNodes}')" == "complete ${NODES}/${NODES}" ]]
}

create_nodes() {
  local i
  for ((i = 1; i <= NODES; i++)); do
    cat <<EOF
---
apiVersion: v1
kind: Node
metadata:
  name: ${NODE_PREFIX}${i}
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
  labels:
    kubernetes.io/arch: amd64
    kubernetes.io/os: linux
    kubernetes.io/hostname: ${NODE_PREFIX}${i}
    ${NODE_LABEL_KEY}: ${NODE_LABEL_VALUE}
spec:
  taints:
  - key: ${KWOK_TAINT_KEY}
    value: fake
    effect: NoSchedule
status:
  allocatable: {cpu: "64", memory: 512Gi, pods: "250"}
  capacity: {cpu: "64", memory: 512Gi, pods: "250"}
EOF
  done | kubectl apply -f - >/dev/null
}

create_load() {
  kubectl create namespace "${LOAD_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: scale-test-load
  namespace: ${LOAD_NAMESPACE}
spec:
  replicas: $((NODES * PODS_PER_NODE))
  selector:
    matchLabels: {app: scale-test-load}
  template:
    metadata:
      labels: {app: scale-test-load}
    spec:
      nodeSelector: {${NODE_LABEL_KEY}: ${NODE_LABEL_VALUE}}
      tolerations:
      - {key: ${KWOK_TAINT_KEY}, operator: Exists, effect: NoSchedule}
      containers:
      - {name: load, image: registry.k8s.io/pause:3.10}
EOF
}

create_nodewright() {
  local i
  {
    cat <<EOF
apiVersion: nodewright.nvidia.com/v1alpha1
kind: NodeWright
metadata:
  name: ${NODEWRIGHT_NAME}
spec:
  nodeSelectors:
    matchLabels:
      ${NODE_LABEL_KEY}: ${NODE_LABEL_VALUE}
  additionalTolerations:
  - {key: ${KWOK_TAINT_KEY}, operator: Equal, value: fake, effect: NoSchedule}
  packages:
EOF
    # Package names have to end in a letter, so they count pkg-a, pkg-b and so on.
    for ((i = 0; i < PACKAGES; i++)); do
      printf '    pkg-%s:\n      version: "1.0.0"\n      image: ghcr.io/nvidia/skyhook/agentless\n' "${LETTERS:i:1}"
    done
  } | kubectl apply -f - >/dev/null
}

cleanup() {
  log "deleting the NodeWright, its Jobs, the workload and the nodes"
  kubectl delete nodewright "${NODEWRIGHT_NAME}" --ignore-not-found --timeout=300s >/dev/null || true
  kubectl -n "${NAMESPACE}" delete jobs -l "nodewright.nvidia.com/name=${NODEWRIGHT_NAME}" --ignore-not-found >/dev/null || true
  kubectl delete namespace "${LOAD_NAMESPACE}" --ignore-not-found --timeout=600s >/dev/null || true
  kubectl delete nodes -l "${NODE_LABEL_KEY}=${NODE_LABEL_VALUE}" --ignore-not-found >/dev/null || true
}

if (( KEEP == 0 )); then
  trap cleanup EXIT
fi

log "creating ${NODES} nodes and $((NODES * PODS_PER_NODE)) workload pods"
create_nodes
wait_for "${NODES} ready nodes" nodes_ready
create_load
wait_for "$((NODES * PODS_PER_NODE)) ready workload pods" load_ready
sleep "${SETTLE}"
record idle "$(memstats 1)" "$(rss_mib)"

log "rolling out ${PACKAGES} package(s) to ${NODES} nodes"
create_nodewright
applied="${SECONDS}" peak_inuse=0 peak_sys=0 peak_rss=0 peak_stats="" deadline=$((SECONDS + TIMEOUT))
until nodewright_complete; do
  if (( SECONDS > deadline )); then
    echo "timed out waiting for the NodeWright to complete" >&2
    exit 1
  fi
  if (( SECONDS - applied > 120 )) && [[ -z "$(kubectl get nodewright "${NODEWRIGHT_NAME}" -o jsonpath='{.status.status}')" ]]; then
    echo "the NodeWright has no status after 120s: check the operator log for a reconcile error" >&2
    exit 1
  fi
  # A sample the endpoint failed to serve is skipped rather than ending the run.
  stats="$(memstats 0)" || { sleep 5; continue; }
  IFS=, read -r _ inuse sys <<<"${stats}"
  rss="$(rss_mib)"
  if (( inuse > peak_inuse )); then
    peak_inuse="${inuse}" peak_stats="${stats}"
  fi
  (( sys > peak_sys )) && peak_sys="${sys}"
  [[ -n "${rss}" ]] && (( rss > peak_rss )) && peak_rss="${rss}"
  sleep 5
done
IFS=, read -r peak_alloc _ _ <<<"${peak_stats:-0,0,0}"
record rollout_peak "${peak_alloc},${peak_inuse},${peak_sys}" "$([[ -n "${PID}" ]] && echo "${peak_rss}")"

sleep "${SETTLE}"
record complete "$(memstats 1)" "$(rss_mib)"
