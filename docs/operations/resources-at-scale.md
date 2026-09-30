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
 
## cpu

| item    | function |
----------|----------- 
| request | max(500m, $limit/2) |
| limit  | max(N*1.6* max(P * 0.4, 1), 1000) |

## Exported metric series during the metrics deprecation window

Between operator v0.18.0 and v0.20.0 every metric is published twice, once under the current
`nodewright_*` name and once under the deprecated `skyhook_*` name (see
[docs/observability/metrics.md](../observability/metrics.md)). That roughly doubles the operator's exported series count
for the duration of the window.

This affects the Prometheus side rather than the operator: series count drives scrape payload size and
Prometheus storage, and the equations above are driven by node and package count, not by series count.
No change to the requests/limits below is needed. The legacy half disappears in v0.20.0.

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
