---
summary: "Level-triggered idempotent reconcile, the dal and wrapper layers, watch instead of poll"
paths:
  - "operator/internal/**"
  - "operator/cmd/manager/**"
---
# Operator controller

## Read before editing

- `docs/architecture/operator-status.md`: Status, State and Stage definitions.
- `docs/architecture/interrupt-flow.md`: cordon, drain, interrupt and uncordon sequence, and `podNonInterruptLabels`.
- `docs/architecture/ordering.md`: priority, sequencing (`node` vs `all`), `SKYHOOK_NODE_ORDER`.
- `docs/user-guide/runtime-required.md` and `docs/user-guide/taints.md`: runtime-required taint flow, `AutoTaintNewNodes`, taint semantics.
- `docs/user-guide/deployment-policy.md`: cluster-scoped rollout shaping.
- `docs/operations/resource-management.md` and `docs/operations/resources-at-scale.md`: resource-override validation and scale characteristics.
- `docs/observability/metrics.md`: the metrics `operator/internal/controller/metrics.go` exports; a metric change updates it.

Node annotations, status shape, and finalizer and label names are read by the CLI. Changing one follows `.claude/rules/cli-contract.md`.

A `//+kubebuilder:rbac` or webhook marker, or an env option on the manager (`operator/cmd/manager/main.go`), changes what `operator/config/` deploys. Mirror it into `chart/` per `.claude/rules/chart-sync.md`.

## Layout (`operator/internal/`)

- `operator/internal/controller/nodewright_controller.go`: the primary reconcile loop (about 2500 lines). Owns the NodeWright lifecycle, package-pod orchestration per node, the interrupt and reboot flow, and finalizer handling.
- `operator/internal/controller/cluster_state_v2.go`: the cluster-state snapshot reconcile uses (which packages exist on which nodes, and the stage of each). There is no v1 any more; the name is historical.
- `operator/internal/controller/pod_controller.go`: watches package pods and maps their completion back to node state annotations.
- `operator/internal/controller/webhook_controller.go`: self-managed webhook certificates and the validating and mutating webhook wiring.
- `operator/internal/controller/event_handler.go`: event filters deciding when CR, node or pod changes enqueue a reconcile.
- `operator/internal/wrapper/`: domain objects (`operator/internal/wrapper/nodewright.go`, `operator/internal/wrapper/node.go`, `operator/internal/wrapper/compartment.go`) that wrap the raw CRs to expose intent, such as "does this node still need an interrupt?". Controllers work through these rather than raw types.
- `operator/internal/graph/dependency_graph.go`: topological ordering of packages from `dependsOn`.
- `operator/internal/dal/`: data-access layer over the controller-runtime client. It is the seam tests mock against, so don't reach for `client.Client` directly when `dal` already exposes the call.
- `zz.migration.<version>.go` (in `operator/internal/controller/` and `operator/internal/wrapper/`): **hand-written** one-shot migration shims that upgrade CRs from an older schema. Despite the `zz.` prefix, `make generate` does not produce them. Don't delete, regenerate or move them. Add a new migration as a new `zz.migration.<version>.go` beside the others.

## Reconcile pattern

- **Level-triggered, not edge-triggered.** Reconcile must be correct given only the current state of the world. Never assume which event caused this invocation, that the previous reconcile ran, or that you'll be called again soon unless you requeue. If a node annotation is missing, work out why from the spec and cluster state.
- **Idempotent.** Running reconcile twice on the same inputs produces the same result and no double side effects. Check before creating a pod, drain, taint or annotation; compare before mutating status.
- **Desired vs. observed.** Intent lives in the NodeWright and DeploymentPolicy specs; observation lives in the cluster (Nodes, Pods, annotations), summarized by `operator/internal/controller/cluster_state_v2.go`. Status is where the operator publishes its observations (conditions, the rollout summary, per-node status). Don't read those back as authoritative; re-derive them from the cluster. Read status back only for history the cluster cannot reconstruct, such as each node's previous boot ID in `Status.NodeBootIds`, which reboot detection compares against the current one.
- **No reconciler-owned state.** Don't cache progress on the reconciler struct between reconciles: pods restart and leadership moves. Persist what must survive to the apiserver (Node annotations, the status subresource, finalizers).
- **Requeue, don't loop.** Return `ctrl.Result{RequeueAfter: 30*time.Second}` to re-check later, or return the error to retry. Never `time.Sleep` inside reconcile.
- **Finalizers for cleanup.** Anything created outside the owner-reference tree (host-side mutations, taints, Node annotations) must be reversed through the `nodewright.nvidia.com/nodewright` finalizer. The finalizer block in `operator/internal/controller/nodewright_controller.go` is the template.
- **Owner references for Kubernetes-native cleanup.** Pods, ConfigMaps and the like that belong to a CR get it as owner, through `controllerutil.SetControllerReference`, so they are garbage-collected.
- **Status writes go through the subresource**, with `.Status().Update()` or `.Status().Patch()`, so status and spec updates don't race the webhook.
- **Errors escape to the queue.** Returning an error triggers controller-runtime's exponential backoff. Don't swallow errors to avoid retries; if an error is expected (such as a transient conflict), log it and return `nil` with an explicit `RequeueAfter`.

## Kubernetes patterns

- **Watch, don't poll.** Use `ctrl.Watches` / `source.Kind` and enqueue from events. Interrupt-budget and backoff requeues are fine; a goroutine polling the apiserver is not.
- **Create-or-update for mutable resources.** Use `controllerutil.CreateOrUpdate` (or `CreateOrPatch`) for ConfigMaps, RBAC and other mutable kinds. A Pod spec is mostly immutable, so a changed pod is replaced, not updated. `IgnoreAlreadyExists` silently keeps stale state and is only right for immutable kinds (a Namespace, a ServiceAccount you never mutate).

## Design principles

- Partial failure is the steady state: nodes come and go, pods die, webhook certificates rotate. Design for timeouts, bounded retries and idempotent reconciles.
- Observability is mandatory: structured `logr` messages, metrics through `operator/internal/controller/metrics.go`, events through the event recorder.
- Boring first: prefer proven controller-runtime idioms over clever abstractions.

## Running the operator locally

From `operator/`:

- `make run` starts the controller in the background against the current kubeconfig (`ENABLE_WEBHOOKS=false` by default) and writes its PID to `operator/reporting/int/run.PID`. Always `make kill` before running it again, or two managers fight for leadership.
- `make run` injects the agentless image. Set `RUN_AGENT_IMAGE` for a real agent; `AGENT_IMAGE` is ignored there.
- `make install` / `make uninstall` apply or remove the CRDs on the current cluster.
- `make build` builds the manager and CLI and also runs manifests, generate, fmt, vet and lint.
