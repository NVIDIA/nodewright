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
| request | max(256Mi, N * 0.45) |
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
example `127.0.0.1:6060`, and reach it with a port-forward. The Deployment is named after the
chart's fullname (`nodewright` by default; `nameOverride` and `fullnameOverride` change it), not
the release name:

```bash
kubectl -n <namespace> port-forward deploy/nodewright-controller-manager 6060:6060
go tool pprof -top -sample_index=inuse_space http://127.0.0.1:6060/debug/pprof/heap
go tool pprof -top -sample_index=alloc_space http://127.0.0.1:6060/debug/pprof/heap
curl -s 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30' > cpu.pprof
```

Two cautions:

* The endpoint is unauthenticated and the chart's NetworkPolicy allows all ingress, so bind to
  `127.0.0.1` only. A value such as `:6060` exposes heap and CPU profiles to every pod in the cluster.
* Changing the env var restarts the manager and discards the heap you wanted to inspect. Enable it
  ahead of time on clusters where memory growth is expected, then profile once it has grown.

## cpu

| item    | function |
----------|----------- 
| request | max(500m, $limit/2) |
| limit  | max(N*1.6* max(P * 0.4, 1), 1000) |

## Exported metric series during the metrics deprecation window

Between operator v0.18.0 and v0.21.0 every metric is published twice, once under the current
`nodewright_*` name and once under the deprecated `skyhook_*` name (see
[docs/observability/metrics.md](../observability/metrics.md)). That roughly doubles the operator's exported series count
for the duration of the window.

This affects the Prometheus side rather than the operator: series count drives scrape payload size and
Prometheus storage, and the equations above are driven by node and package count, not by series count.
No change to the requests/limits below is needed. The legacy half disappears in v0.21.0.

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
