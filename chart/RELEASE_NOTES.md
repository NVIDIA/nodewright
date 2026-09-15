# Release Notes

Human-authored highlights, behavior changes, and upgrade steps for the Helm chart.
For the full commit-level log see CHANGELOG.md.

## Unreleased

### Behavior Changes

- **Controller-manager replicas now prefer separate nodes.** A soft
  `podAntiAffinity` on `kubernetes.io/hostname` is applied to the
  controller-manager Deployment. Kubernetes never rebalances running pods, so on
  a dedicated nodegroup that grew after the operator was first scheduled both
  replicas would sit on the node they originally landed on and a single drain
  took the operator offline. Being a preference rather than a requirement, it
  never blocks scheduling, so single-node clusters are unaffected. Existing
  installs pick this up on the next rollout; no action required.

## chart/v0.18.0 - 2026-08-17

### Breaking Changes

- **In-cluster resource names are now templated off the chart name and render
  `nodewright-*` instead of `skyhook-operator-*`** (#440). These objects are
  renamed on upgrade:

  | Before | After |
  | --- | --- |
  | `skyhook-operator-manager-role` / `-manager-rolebinding` | `nodewright-manager-role` / `-manager-rolebinding` |
  | `skyhook-operator-leader-election-role` / `-rolebinding` | `nodewright-leader-election-role` / `-rolebinding` |
  | `skyhook-operator-metrics-reader` / `-metrics-reader-rolebinding` | `nodewright-metrics-reader` / `-metrics-reader-rolebinding` |
  | `skyhook-operator-controller-manager-metrics-service` | `nodewright-controller-manager-metrics-service` |
  | `skyhook-operator-webhook-service` | `nodewright-webhook-service` |
  | `skyhook-operator-validating-webhook` / `-mutating-webhook` | `nodewright-validating-webhook` / `-mutating-webhook` |

  The `app.kubernetes.io/created-by` and `app.kubernetes.io/part-of` labels move
  from `skyhook-operator` to `nodewright` for the same reason. The
  controller-manager Deployment name and `spec.selector` are **unchanged**, so
  the immutable-selector failure fixed in #285 does not recur; every renamed
  object is created new and the old one removed, and none of them has an
  immutable field that a rename would trip.

  Four consequences worth planning for:

  1. **This chart requires an operator built from #440 or later.** The webhook
     Service name is now passed to the operator as `WEBHOOK_SERVICE_NAME`, and
     the operator finds its webhook configurations by the
     `nodewright.nvidia.com/webhook-config` label rather than by name. An older
     operator ignores both, keeps looking for `skyhook-operator-*`, and never
     injects a caBundle, which leaves admission failing closed. Do not pin
     `controllerManager.manager.image.tag` to a pre-#440 operator with this chart.
  2. **The controller-manager Deployment is deleted and recreated during the one
     upgrade that crosses the rename.** A pre-#440 operator looks its webhook
     configurations up by name, so renaming them makes the running pod error, stay
     un-Ready, and hold the webhook bootstrap lease forever; the rolling update
     then never terminates it and `helm upgrade` wedges on `Pending termination`.
     The existing `selectorMigration` pre-upgrade hook now also detects this case
     (the live Deployment has no `WEBHOOK_SERVICE_NAME` env var) and deletes the
     Deployment so Helm can recreate it. This costs a short operator gap on that
     one upgrade and is a no-op on every normal upgrade. If you run with
     `selectorMigration.enabled: false`, do it by hand instead:
     `kubectl -n <ns> delete deploy <fullname>-controller-manager` before upgrading.
  3. **A brief admission gap during that same upgrade.** Helm creates the renamed
     webhook configurations with an empty `caBundle` and the operator fills it in
     once the new pod is running, so for roughly the length of the rollout,
     `create`/`update` on NodeWright, Skyhook, and DeploymentPolicy resources is
     rejected. Nothing already running is affected; retry the apply. If you cannot
     tolerate the gap, pin the old names with `fullnameOverride: skyhook-operator`,
     which keeps every legacy name including the webhook configurations.
  4. **`nodewright-metrics-reader` is the one renamed object you bind to yourself.**
     Re-point any ClusterRoleBinding at the new name; the rule is identical, so
     nothing else changes. A binding left on the old name fails as an empty scrape
     rather than a visible error, so make the swap when you upgrade. See
     [docs/observability/metrics.md](../docs/observability/metrics.md).

  Anyone who already sets `fullnameOverride` or `nameOverride` keeps their own
  prefix on all of the above, unchanged.

### New Features

- **Package stage execution moved to `batch/v1` Jobs**, with four new operator
  env values under `controllerManager.manager.env`:

  | Value | Default | What it does |
  | --- | --- | --- |
  | `jobStageTimeout` | `"1h"` | Default per-attempt deadline for a package stage, when the package sets no `stageTimeout` of its own. `"0"` removes the time bound. |
  | `jobBackoffLimit` | `"3"` | Retries after the first attempt before a stage's Job goes terminal — so at most four attempts. Spending the budget is not by itself a timeout: the stage is surfaced as `erroring` only if a retained attempt genuinely failed, since attempts the kubelet refused to admit spend the budget without ever running the package. |
  | `jobTtlSucceeded` | `"1h"` | How long a succeeded stage Job (and its logs) is kept. Minimum `"1m"`. |
  | `jobTtlFailed` | `"24h"` | How long a failed stage Job is kept, so failure logs outlive success logs. Minimum `"1m"`. |

  The chart also gains `batch/job` RBAC for the operator; no values change is
  required to upgrade.

### Bug Fixes (continued)

- **`helm rollback` no longer destroys every NodeWright.** The chart ships its CRDs
  under `templates/` (they interpolate the conversion-webhook Service name), so Helm
  manages them as release resources. Rolling back to a pre-rename revision dropped
  `nodewrights.nodewright.nvidia.com` and `deploymentpolicies.nodewright.nvidia.com`
  from the rendered manifest, and Helm deleted them — cascade-deleting every object
  they defined. Both CRDs now carry `helm.sh/resource-policy: keep` (#464).

  `keep` suppresses **deletion only**. Helm still creates and patches these CRDs on
  install and upgrade, so schema changes apply exactly as before. Two consequences:

  - `helm uninstall` now leaves the two CRDs (and therefore any surviving `NodeWright`
    / `DeploymentPolicy` objects) in the cluster. Remove them explicitly if you want a
    full teardown: `kubectl delete crd nodewrights.nodewright.nvidia.com
    deploymentpolicies.nodewright.nvidia.com`.
  - A kept CRD keeps its `meta.helm.sh/release-name` annotation. Reinstalling under the
    **same** release name and namespace adopts it; reinstalling under a **different**
    release name fails with `invalid ownership metadata`. Delete the CRDs first if you
    are renaming the release.

- **The webhook serving certificate is reminted when the webhook Service is
  renamed.** `Secret/webhook-cert` is operator-owned, not chart-owned, so it
  survives a chart upgrade. The operator only reminted on expiry or a
  cert-on-disk mismatch, so after a Service rename it kept serving a
  year-valid certificate whose SAN was the old DNS name and every admission
  call failed with
  `x509: certificate is valid for skyhook-operator-webhook-service..., not
  nodewright-webhook-service`. The operator now also remints when the Service
  recorded on the Secret no longer matches its configured `WEBHOOK_SERVICE_NAME`.

- **The pre-delete cleanup job can actually delete the webhook configurations.**
  The manager ClusterRole granted only `get` and `update` on them, so the job's
  `kubectl delete ...webhookconfiguration` was silently RBAC-denied and the
  trailing `|| true` swallowed the error. `delete` is now granted, and the job
  sweeps the pre-rename names as well, since an orphaned webhook configuration
  with `failurePolicy: Fail` and no operator behind it rejects every matching
  API call cluster-wide.

- **`controllerManager.manager.env.runtimeRequiredTaint` now defaults to
  `nodewright.nvidia.com=runtime-required:NoSchedule`** (was
  `skyhook.nvidia.com=runtime-required:NoSchedule`). This is a coordinated change
  with your provisioning stack, not a cosmetic default bump: the taint key is
  named by cluster autoscaler / Karpenter node pools, machine templates,
  `--register-with-taints` kubelet arguments, and your own workload tolerations.

  The operator recognises both keys for the rename deprecation window — it applies
  only the configured one, but tolerates and removes the legacy key as well — so
  upgrading the chart without touching your provisioning config is safe. Update
  that config before operator v0.20.0, or pin the old value explicitly:

  ```yaml
  controllerManager:
    manager:
      env:
        runtimeRequiredTaint: skyhook.nvidia.com=runtime-required:NoSchedule
  ```

  See [docs/user-guide/runtime-required.md](../docs/user-guide/runtime-required.md#taint-key-rename-skyhooknvidiacom---nodewrightnvidiacom).

### Bug Fixes

- **The kustomize manager manifest actually sets the runtime-required taint
  again.** `operator/config/manager/manager.yaml` set `RUNTIME_REQUIRED_TAINT_KEY`,
  which the operator does not read, so a kustomize-based install silently fell back
  to the built-in default and ignored the value in the manifest. The variable is now
  spelled `RUNTIME_REQUIRED_TAINT`, matching the operator and the chart.

- **Helm upgrade no longer fails on the immutable Deployment selector after the
  skyhook -> nodewright rename.** A release first installed from a pre-rename
  chart (chart name `skyhook-operator`) could not be upgraded, because the
  rename changed `app.kubernetes.io/name` inside the controller-manager
  Deployment's `spec.selector`, which Kubernetes forbids mutating
  (`spec.selector: field is immutable`). The chart now sources the selector from
  a single shared helper (`chart.managerSelectorLabels`) and ships a
  `pre-upgrade` hook (`selectorMigration.enabled`, default `true`) that inspects
  the live Deployment and deletes it only when its selector does not match the
  desired one, so Helm can recreate it. The hook is a no-op (no downtime) on
  normal upgrades and is scoped to the controller-manager Deployment via its own
  narrowly-permissioned ServiceAccount (#285).

### Other Changes

- **The operator pod now contains only the `manager` container.** Secure metrics
  are served directly by controller-runtime on port `8443`; the separate metrics
  proxy image, resources, values, and RBAC objects have been removed. Prometheus
  clients need a bearer token authorized for the `nodewright-metrics-reader`
  ClusterRole (renamed from `skyhook-operator-metrics-reader` in the same release,
  see the resource-rename entry above).

- **Maintenance jobs moved off `bitnami/kubectl` to `alpine/kubectl`.** The
  webhook and skyhook cleanup pre-delete hooks and the new selector-migration
  pre-upgrade hook now use a maintained, versioned, multi-arch `alpine/kubectl`
  image, pinned by tag and digest. The previous image's free versioned tags were
  withdrawn on 2025-08-28, leaving only an unversioned `:latest` with no
  security maintenance. If you had overridden `webhook.removalImage` to point at
  a private mirror, remirror from the new source (#207).
  
- **The documented install namespace for new installs is now `nodewright`.** This is a
  documentation and example change only: the chart has always sourced the namespace from
  `.Release.Namespace` and installs cleanly into any namespace. **An existing release in
  the `skyhook` namespace needs no action** — a namespace cannot be renamed in place and
  Helm cannot move a release between namespaces, so `helm upgrade` there keeps working
  unchanged and is supported indefinitely.

### Upgrade notes

- **Run `helm upgrade` only when no package work is in flight.** The operator
  that precedes this chart executes packages as raw pods, and the two execution
  models are never allowed to overlap: the new operator withholds all
  reconciliation while any pre-rename `Skyhook` is still rolling out. Upgrading
  mid-rollout therefore leaves that node **cordoned and stalled** until you
  `helm rollback` and let the rollout finish, or delete the legacy `Skyhook`.
  Confirm every `Skyhook` reads `complete` first, and do not unpause or enable a
  migrated `NodeWright` until its pre-upgrade package pods are gone. See the
  operator release notes and
  [docs/getting-started/migration.md](../docs/getting-started/migration.md).
- **A crash-looping package now gives up after ~70 seconds instead of retrying
  for up to an hour.** `jobBackoffLimit` bounds retries per stage; if you rely on
  a package self-healing through transient environment problems, raise it before
  upgrading. See the operator release notes for the full behavior change.
- **`jobTtlSucceeded` / `jobTtlFailed` must be at least `"1m"`.** Setting `"0"`
  to disable retention makes the operator fail validation at startup.
- Remove any overrides under `controllerManager.kubeRbacProxy`; that values
  block no longer exists. Metrics remain available on HTTPS port `8443`.
  If you override `controllerManager.manager.env.metricsPort`, update it from
  `:8080` to `:8443`; the value remains supported, but the Service now targets
  `8443` exclusively.
  Scrapers that used the chart's former unauthenticated HTTP `:8080` service
  port must switch to `:8443`, disable TLS certificate verification, and
  provide a Kubernetes bearer token authorized for the `/metrics`
  non-resource URL. controller-runtime generates an in-memory self-signed
  certificate for each operator pod, so there is no stable CA to trust.
  The chart no longer adds `prometheus.io/*` discovery annotations by default,
  because standard annotation-based jobs cannot supply that authentication or
  trust configuration and would continuously report a failed target. Configure
  an authenticated endpoints-discovery job as shown in
  `docs/observability/prometheus_values.yaml` instead.

- No action is required: the selector-migration hook runs automatically on
  `helm upgrade` and only recreates the Deployment when its selector is stale.
  To perform the recreate manually instead, set `selectorMigration.enabled=false`
  and, before upgrading, delete the Deployment so the upgrade recreates it:

  ```bash
  # Simplest: brief control-plane gap, but clean (the pod is removed too).
  kubectl delete deployment <release>-controller-manager -n <namespace>
  ```

  For no control-plane gap, orphan the pod instead, then remove the leftover
  old pod once the new one is Ready (two managers run briefly; leader election
  keeps only one active):

  ```bash
  kubectl delete deployment <release>-controller-manager -n <namespace> --cascade=orphan
  ```
