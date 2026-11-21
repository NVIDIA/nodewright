# Operator Resources at Scale

As cluster size and package count increase the operator requires more CPU and Memory to efficiently operate. This is especially true for memory as the operator can being to OOM at large cluster sizes.

## Scaling Equations

The following have been validated on cluster size up to 1k nodes.

For the equations below the variables are as follows:

 * `N` : number of nodes
 * `P` : number of packages

## memory

| item    | function |
----------|----------- 
| request | max(256Mi, N * 0.4) |
| limit   | max(N *.8* max(P * 0.4, 1), 512) |

## Soft memory limit

Left to its defaults (`GOGC=100`), the Go runtime lets the heap grow to about twice the live heap before it collects, and it does not know about the container's memory limit. Without a soft limit, the container limit has to cover roughly twice what the operator actually holds, and the first sign of a limit set too low is the kernel OOM-killing the pod.

The manager sets Go's soft memory limit at startup. It reads the container's memory limit from the cgroup filesystem (`/sys/fs/cgroup/memory.max` on cgroup v2, `/sys/fs/cgroup/memory/memory.limit_in_bytes` on cgroup v1) and sets the soft limit to `GOMEMLIMIT_FRACTION` of it, `0.9` by default (chart: `controllerManager.manager.env.memoryLimitFraction`). As the heap approaches the soft limit, the runtime collects more often instead of letting the heap double. The rest of the limit (1 − fraction, 10% at the default) is headroom for memory the runtime does not manage; on a cluster of a few thousand nodes that was under 200 MB of a 5 GB RSS. The value used is logged at startup as `soft memory limit`, with the source (`cgroup v2`, `cgroup v1`, `GOMEMLIMIT` or `none`) and both limits in bytes.

* A fraction outside (0, 1] fails the manager at startup.
* A container with no memory limit is left alone: there is nothing to take a fraction of.
* An explicit `GOMEMLIMIT` on the container always wins: the fraction is not applied, but it is still validated, so a value outside (0, 1] fails startup either way. `GOMEMLIMIT=off` disables the soft limit entirely. The chart has no field for it; set it with kustomize or a Helm post-renderer.

The soft limit changes how to size the memory limit, not whether it matters. The limit still has to cover the live heap (the `inuse_space` heap profile in [Profiling the operator](#profiling-the-operator) is the heap as of the last collection); it no longer has to cover twice the live heap. Size it as live heap ÷ fraction, plus margin for growth.

### Size CPU and memory together

Collecting more often costs CPU. On a cluster of a few thousand nodes, the garbage collector was already 22 to 35 percent of the leader's CPU samples, with the leader using 1.3 to 1.9 cores, before any soft limit. A soft limit close to the live heap pushes the collector harder, and if the live heap grows past the soft limit, the runtime collects nearly continuously (it caps the collector at about 50% of CPU) for as long as the heap stays there. That shows up as the leader's CPU climbing toward its limit and reconciles slowing down, not as an OOM kill: the pod is OOM-killed only if memory use goes on to reach the container's hard limit.

So when a soft limit lets you lower the memory limit, do not lower the CPU request with it; if anything, raise it. If you override `controllerManager.manager.resources` instead of using `estimatedNodeCount` and `estimatedPackageCount`, change CPU and memory together.

These costs apply to the leader. A standby replica does not reconcile, so its heap stays near the size of its Pod cache and it allocates almost nothing.

## Profiling the operator

When the operator's memory grows beyond what the equations above predict, `go_memstats_*` on the
metrics port says how much is allocated but not which allocations. The manager can serve Go's
`net/http/pprof` endpoints for that. It is off by default.

Set `PPROF_PORT` (chart: `controllerManager.manager.env.pprofPort`) to a loopback address, for
example `127.0.0.1:6060`, and port-forward to the leader pod. The chart runs two replicas and
`deploy/` picks either one; both build the informer cache, but only the leader runs reconciles,
so a profile of the standby misses reconcile-path allocations. The part of the reconcile Lease
holder before the `_` is the pod name with pod networking and the node name with host networking.
The commands below handle both cases and ignore pods that are not Running, Ready and
non-terminating.

In one terminal, select the leader and leave the port-forward running:

```bash
NAMESPACE='<namespace>'
LEASE=3c22c1ae.nvidia.com
LEADER_ID=$(kubectl -n "${NAMESPACE}" get lease "${LEASE}" -o jsonpath='{.spec.holderIdentity}')
LEADER_HOST=${LEADER_ID%%_*}

ready_controller_pod() {
  kubectl -n "${NAMESPACE}" get pods -l control-plane=controller-manager \
    --field-selector "$1,status.phase=Running" \
    -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,DELETING:.metadata.deletionTimestamp' \
    --no-headers | awk '$2 == "True" && $3 == "<none>" { print $1; exit }'
}

LEADER_POD=$(ready_controller_pod "metadata.name=${LEADER_HOST}")
if [ -z "${LEADER_POD}" ]; then
  LEADER_POD=$(ready_controller_pod "spec.nodeName=${LEADER_HOST}")
fi
if [ -z "${LEADER_POD}" ]; then
  LEADER_POD=$(kubectl -n "${NAMESPACE}" get pods -l control-plane=controller-manager \
    --field-selector status.phase=Running \
    -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,DELETING:.metadata.deletionTimestamp' \
    --no-headers | awk -v host="${LEADER_HOST}" \
      '$2 == "True" && $3 == "<none>" && index($1, host) == 1 { print $1; exit }')
fi
if [ -z "${LEADER_POD}" ]; then
  echo "No ready leader pod found" >&2
else
  printf 'Leader identity: %s\nLeader pod: %s\n' "${LEADER_ID}" "${LEADER_POD}"
  kubectl -n "${NAMESPACE}" port-forward "pod/${LEADER_POD}" 6060:6060
fi
```

The prefix fallback covers a pod hostname truncated to 63 characters. If no pod is found and the
nodes use a kubelet hostname override, compare `LEADER_HOST` with the operating-system hostname of
the nodes running the controller-manager pods, then set `LEADER_POD` to the pod on the matching node
and run the `port-forward` command.

In a second terminal, copy the full identity printed above, capture the profiles and check that the
same process still holds the Lease. If either check fails, stop the port-forward and repeat the pod
selection and capture.

```bash
NAMESPACE='<namespace>'
LEASE=3c22c1ae.nvidia.com
PROFILED_LEADER='<leader identity printed by the first terminal>'
CURRENT_LEADER=$(kubectl -n "${NAMESPACE}" get lease "${LEASE}" -o jsonpath='{.spec.holderIdentity}')
if [ "${CURRENT_LEADER}" != "${PROFILED_LEADER}" ]; then
  echo "Leader changed before capture; repeat the selection" >&2
else
  go tool pprof -top -sample_index=inuse_space http://127.0.0.1:6060/debug/pprof/heap
  go tool pprof -top -sample_index=alloc_space http://127.0.0.1:6060/debug/pprof/heap
  curl -s 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30' > cpu.pprof

  CURRENT_LEADER=$(kubectl -n "${NAMESPACE}" get lease "${LEASE}" -o jsonpath='{.spec.holderIdentity}')
  if [ "${CURRENT_LEADER}" != "${PROFILED_LEADER}" ]; then
    echo "Leader changed during capture; discard the profiles and repeat the selection" >&2
  fi
fi
```

Cautions:

* The endpoint is unauthenticated and the chart's NetworkPolicy allows all ingress, so bind to
  `127.0.0.1` only. A value such as `:6060` exposes heap and CPU profiles to every pod in the cluster.
  With `useHostNetwork: true`, loopback is the node's rather than the pod's: the host and other
  host-network pods on that node can reach the endpoint. A non-loopback bind also exposes the
  endpoint on the node's addresses.
* Determine whether leader election is enabled from
  `controllerManager.manager.env.leaderElection`, not from Lease presence: a stale Lease can remain
  after election is disabled. When it is disabled, run one replica, port-forward directly to that
  pod or its Deployment, and run the three profile capture commands without the Lease checks.
* Changing the env var restarts the manager and discards the heap you wanted to inspect. Enable it
  ahead of time on clusters where memory growth is expected, then profile once it has grown.

## cpu

| item    | function |
----------|----------- 
| request | max(500m, $limit/2) |
| limit  | max(N*1.6* max(P * 0.4, 1), 1000) |

## Exported metric series during the metrics deprecation window

Between operator v0.18.0 and v0.22.0 every metric is published twice, once under the current
`nodewright_*` name and once under the deprecated `skyhook_*` name (see
[docs/observability/metrics.md](../observability/metrics.md)). That roughly doubles the operator's exported series count
for the duration of the window.

This affects the Prometheus side rather than the operator: series count drives scrape payload size and
Prometheus storage, and the equations above are driven by node and package count, not by series count.
No change to the requests/limits below is needed. The legacy half disappears in v0.22.0.

If the extra series are unwelcome and your dashboards and alerts already use the `nodewright_*` names,
set `PUBLISH_LEGACY_METRICS=false` (chart: `controllerManager.manager.env.publishLegacyMetrics`) to
unregister the deprecated collectors at startup and halve the count immediately.

## Helm chart

The chart is already setup with the above equations. You can use the `estimatedPackageCount` and `estimatedNodeCount`. The default values in the chart of:
```
limits:
  cpu: 1000m
  memory: 512Mi
requests:
  cpu: 500m
  memory: 256Mi
```
Is sufficient to get to ~800 nodes and 1 - 3 packages or ~500 nodes a 4+ packages.

The chart also sets `controllerManager.manager.env.memoryLimitFraction` (default `"0.9"`); see [Soft memory limit](#soft-memory-limit).

## Package execution Jobs

Package stages run as `batch/v1` Jobs — one per (NodeWright, package, stage, node), each `parallelism: 1`,
`completions: 1`. A package that does not interrupt runs two of them per node (apply, config); one that
interrupts runs four (apply, config, interrupt, post-interrupt), and uninstall/upgrade add their own.

Two things follow for sizing.

**Cache.** The operator watches Jobs through a **namespace-scoped** informer (only the operator's own
namespace), giving it a second cache holding every package-stage Job in that namespace — in flight *and*
retained. It is a cache of its own, not extra entries in the pod cache: package pods continue to be
cached cluster-wide, because drain has to see every pod on a node. The two costs add.
The package-pod controller filters that shared cache's events to the configured operator namespace
and requires both `nodewright.nvidia.com/name` and `nodewright.nvidia.com/package` labels. Pods in
other namespaces remain visible to drain but do not enter the package-pod workqueue.

**Retention.** A finished Job is not deleted immediately: `ttlSecondsAfterFinished` is set by outcome
from `jobTtlSucceeded` (1h default) and `jobTtlFailed` (24h default), both with a hard floor of one
minute. So the cached Job count is in-flight plus retained, and neither term alone is the bound:

 * in flight is capped by how many nodes execute at once — interruption budget, and any DeploymentPolicy
   batch sizing on top;
 * retained is whatever completed inside the TTL windows. A single NodeWright whose rollout finishes
   inside `jobTtlSucceeded` peaks around `N × P × stages-per-package`; a longer rollout sheds its oldest
   Jobs as it goes, and NodeWrights rolling out concurrently each add their own population.

Each retained Job also keeps at most **two genuine** failed child pods — the first genuine failure and
the most recent. Two things sit outside that cap: a disruption casualty (a pod carrying
`DisruptionTarget` — evicted, preempted, or lost with its node) has no failure verdict, so it is neither
counted nor pruned; and the pruner runs on active Jobs, so a Job that goes terminal before its next
reconcile can keep extras. Successful child pods are deleted with their Job.

If the operator's memory is the constraint at scale, `jobTtlSucceeded` is the first lever — successful
stages are the bulk of the population and the least interesting to keep. Note that the equations above
were derived before package execution moved to Jobs and have not been re-measured with the Jobs informer
in the cache, so treat them as a floor.

## Measuring at scale with kwok

`scripts/scale_test_kwok.sh` measures the operator against [kwok](https://kwok.sigs.k8s.io/) fake nodes, so a sizing run needs no machines for the nodes. kwok simulates the nodes, a workload of pods on them, and the pods of every package Job, which it completes at once. What the script measures is the operator itself: its informer caches, and its reconcile path while a NodeWright rolls out to every node.

It needs kwok in the cluster with its fast stages, and the operator running with `PPROF_PORT` set. The comments at the top of the script give the commands. A run then looks like this:

```bash
scripts/scale_test_kwok.sh --nodes 500 --pods-per-node 30 --packages 2 --output results.csv
```

A run records the operator's memory three times: idle with the nodes and the workload in place, at the peak of the rollout, and once the NodeWright is complete with its Jobs retained. Each row has the live heap after a collection, the heap in use and the memory the Go runtime holds (`Sys`), all read from the pprof endpoint. With `--pid`, a row also has the RSS of a manager running on the same machine. The script deletes everything it created when it ends.

kwok's objects are smaller than real ones: a fake node carries no image list in its status, and the workload pods have one container and no volumes. A run therefore shows how memory grows with nodes, pods, packages and retained Jobs, but it understates the absolute figures on a real cluster of the same size. Check the equations above against a real cluster before relying on numbers measured this way.
