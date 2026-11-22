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

NODE_LABEL_KEY="nodewright.nvidia.com/scale-test"
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
  --pods-per-node K  Create N times K workload pods across the fake nodes (default: ${PODS_PER_NODE})
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
if ! [[ "${PODS_PER_NODE}" =~ ^(0|[1-9][0-9]*)$ && "${SETTLE}" =~ ^(0|[1-9][0-9]*)$ &&
        "${TIMEOUT}" =~ ^[1-9][0-9]*$ ]]; then
  echo "--pods-per-node and --settle must be nonnegative integers and --timeout must be a positive integer" >&2
  usage 1
fi
# Deployment replicas are int32. Check before creating resources or multiplying the inputs.
max_replicas=2147483647
if [[ "${PODS_PER_NODE}" != 0 ]] &&
   (( ${#NODES} > ${#max_replicas} || ${#PODS_PER_NODE} > ${#max_replicas} ||
      PODS_PER_NODE > max_replicas / NODES )); then
  echo "--nodes times --pods-per-node exceeds the Deployment limit of ${max_replicas} replicas" >&2
  exit 1
fi
workload_pods=$((NODES * PODS_PER_NODE))
if [[ -n "${PID}" && ! "${PID}" =~ ^[1-9][0-9]*$ ]]; then
  echo "--pid must identify one process by its numeric PID" >&2
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
request_timeout=$(( TIMEOUT < 30 ? TIMEOUT : 30 ))
curl_args=(-fsS --connect-timeout 5 --max-time "${request_timeout}")
get() {
  kubectl --request-timeout="${request_timeout}s" get "$@"
}
if ! get stages.kwok.x-k8s.io pod-complete >/dev/null; then
  echo "kwok with its fast stages is not installed in this cluster; see the prerequisites at the top of $0" >&2
  exit 1
fi
if ! curl "${curl_args[@]}" -o /dev/null "${PPROF_URL}/debug/pprof/"; then
  echo "no pprof endpoint at ${PPROF_URL}; start the operator with PPROF_PORT set" >&2
  exit 1
fi

log() {
  printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2
}

# memstats prints HeapAlloc, HeapInuse and Sys in MiB, comma-separated. With gc=1 the runtime
# collects first, so HeapAlloc is the live heap; with gc=0 it is the heap at this instant.
memstats() {
  curl "${curl_args[@]}" "${PPROF_URL}/debug/pprof/heap?debug=1&gc=$1" |
    awk '/^# HeapAlloc = [0-9]+$/ {a=$4}
         /^# HeapInuse = [0-9]+$/ {i=$4}
         /^# Sys = [0-9]+$/       {s=$4}
         END {
           if (!length(a) || !length(i) || !length(s)) exit 1
           printf "%.0f,%.0f,%.0f", a/1048576, i/1048576, s/1048576
         }'
}

rss_mib() {
  if [[ -n "${PID}" ]]; then
    ps -o rss= -p "${PID}" | awk 'NF {print int($1/1024)}'
  fi
}

record() {
  local phase="$1" stats="$2" rss="$3" pods jobs
  pods="$(get pods -A --no-headers | wc -l | tr -d ' ')"
  jobs="$(get jobs -n "${NAMESPACE}" --no-headers | wc -l | tr -d ' ')"
  if [[ ! -s "${OUTPUT}" ]]; then
    echo "nodes,pods_per_node,packages,phase,heap_alloc_mib,heap_inuse_mib,sys_mib,rss_mib,pods,jobs,timestamp" >> "${OUTPUT}"
  fi
  echo "${NODES},${PODS_PER_NODE},${PACKAGES},${phase},${stats},${rss},${pods},${jobs},$(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "${OUTPUT}"
}

record_collected() {
  local phase="$1" stats rss
  stats="$(memstats 1)" || { log "could not read a complete heap profile for ${phase}"; exit 1; }
  rss="$(rss_mib)"
  record "${phase}" "${stats}" "${rss}"
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
  local ready
  ready="$(get nodes -l "${NODE_LABEL_KEY}=${RUN_NAME}" -o json |
    jq '[.items[] | select(.status.conditions[]? | .type == "Ready" and .status == "True")] | length')" || return 1
  [[ "${ready}" == "${NODES}" ]]
}

load_ready() {
  local ready
  ready="$(get deployment scale-test-load -n "${RUN_NAME}" -o jsonpath='{.status.readyReplicas}')" || return 1
  [[ "${ready:-0}" == "${workload_pods}" ]]
}

create_nodes() {
  # Leave the original 250 pod slots available for package Jobs and cluster DaemonSets.
  local i pod_capacity=$((PODS_PER_NODE + 250))
  for ((i = 1; i <= NODES; i++)); do
    cat <<EOF
---
apiVersion: v1
kind: Node
metadata:
  name: ${RUN_NAME}-${i}
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
  labels:
    kubernetes.io/arch: amd64
    kubernetes.io/os: linux
    kubernetes.io/hostname: ${RUN_NAME}-${i}
    ${NODE_LABEL_KEY}: ${RUN_NAME}
spec:
  taints:
  - key: ${KWOK_TAINT_KEY}
    value: fake
    effect: NoSchedule
status:
  allocatable: {cpu: "64", memory: 512Gi, pods: "${pod_capacity}"}
  capacity: {cpu: "64", memory: 512Gi, pods: "${pod_capacity}"}
EOF
  done | kubectl create -f - >/dev/null
}

create_load() {
  kubectl create -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: scale-test-load
  namespace: ${RUN_NAME}
spec:
  replicas: ${workload_pods}
  selector:
    matchLabels: {app: scale-test-load}
  template:
    metadata:
      labels: {app: scale-test-load}
    spec:
      nodeSelector: {${NODE_LABEL_KEY}: ${RUN_NAME}}
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
  name: ${RUN_NAME}
spec:
  nodeSelectors:
    matchLabels:
      ${NODE_LABEL_KEY}: ${RUN_NAME}
  additionalTolerations:
  - {key: ${KWOK_TAINT_KEY}, operator: Equal, value: fake, effect: NoSchedule}
  packages:
EOF
    # Package names have to end in a letter, so they count pkg-a, pkg-b and so on.
    for ((i = 0; i < PACKAGES; i++)); do
      printf '    pkg-%s:\n      version: "1.0.0"\n      image: ghcr.io/nvidia/skyhook/agentless\n' "${LETTERS:i:1}"
    done
  } | kubectl create -f - >/dev/null
}

cleanup() {
  log "deleting the NodeWright, Jobs, workload and nodes for ${RUN_NAME}"
  kubectl delete nodewright "${RUN_NAME}" --ignore-not-found --timeout=300s >/dev/null || true
  kubectl -n "${NAMESPACE}" delete jobs -l "nodewright.nvidia.com/name=${RUN_NAME}" --ignore-not-found --timeout=300s >/dev/null || true
  kubectl delete namespace "${RUN_NAME}" --ignore-not-found --timeout=600s >/dev/null || true
  kubectl delete nodes -l "${NODE_LABEL_KEY}=${RUN_NAME}" --ignore-not-found --timeout=300s >/dev/null || true
}

# The API server reserves a unique namespace even when two runs start together.
RUN_NAME="$(kubectl create -f - -o jsonpath='{.metadata.name}' <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  generateName: scale-kwok-
EOF
)"
if (( KEEP == 0 )); then
  trap cleanup EXIT
fi

log "${RUN_NAME}: creating ${NODES} nodes and ${workload_pods} workload pods"
create_nodes
wait_for "${NODES} ready nodes" nodes_ready
create_load
wait_for "${workload_pods} ready workload pods" load_ready
sleep "${SETTLE}"
record_collected idle

log "rolling out ${PACKAGES} package(s) to ${NODES} nodes"
create_nodewright
applied="${SECONDS}" peak_inuse=0 peak_sys=0 peak_rss=0 peak_stats="" deadline=$((SECONDS + TIMEOUT))
while true; do
  if (( SECONDS > deadline )); then
    echo "timed out waiting for the NodeWright to complete" >&2
    exit 1
  fi
  status="$(get nodewright "${RUN_NAME}" -o jsonpath='{.status.status}{" "}{.status.completeNodes}')" || { sleep 5; continue; }
  [[ "${status}" == "complete ${NODES}/${NODES}" ]] && break
  if (( SECONDS - applied > 120 )) && [[ -z "${status%% *}" ]]; then
    echo "the NodeWright has no status after 120s: check the operator log for a reconcile error" >&2
    exit 1
  fi
  # A sample the endpoint failed to serve is skipped rather than ending the run.
  stats="$(memstats 0)" || { sleep 5; continue; }
  IFS=, read -r _ inuse sys <<<"${stats}"
  rss="$(rss_mib)"
  if [[ -z "${peak_stats}" ]] || (( inuse > peak_inuse )); then
    peak_inuse="${inuse}" peak_stats="${stats}"
  fi
  (( sys > peak_sys )) && peak_sys="${sys}"
  [[ -n "${rss}" ]] && (( rss > peak_rss )) && peak_rss="${rss}"
  sleep 5
done
if [[ -n "${peak_stats}" ]]; then
  IFS=, read -r peak_alloc _ _ <<<"${peak_stats}"
  record rollout_peak "${peak_alloc},${peak_inuse},${peak_sys}" "${PID:+${peak_rss}}"
else
  log "no successful rollout sample; leaving peak memory fields empty"
  record rollout_peak ",," ""
fi

sleep "${SETTLE}"
record_collected complete
