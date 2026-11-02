# Webhook bootstrap lease

This document describes why the operator's webhook cert bootstrap runs under a
**dedicated** leader-election lease, separate from the main reconcile lease, and
what guarantees that design does (and does not) provide.

## Background: the v0.7.x → v0.15.x deadlock

Starting in v0.8.0 the operator self-bootstraps its admission-webhook TLS:
on startup the leader generates a CA + serving cert, writes `Secret/webhook-cert`
in the operator namespace, and patches the `caBundle` of the
`MutatingWebhookConfiguration` and `ValidatingWebhookConfiguration`. Earlier
versions (≤ v0.7.x) relied on a different bootstrap model and have no knowledge
of `webhook-cert`.

On an in-place rollout from v0.7.x to a self-bootstrapping version, the cluster
can enter the following dead state:

1. The new pod (v0.15.x) starts as a **follower**: the leader-election lease
   is still held by an old v0.7.x pod.
2. The new pod's webhook controller is gated on leader election, so it never
   creates `webhook-cert`. Its readiness probe (which waits for the secret)
   never goes green.
3. The old leader has no concept of `webhook-cert` and never creates it. The
   old leader also has nothing that would cause it to release the lease.
4. Meanwhile, Argo/Helm applying the new manifests has reset the
   `MutatingWebhookConfiguration` `caBundle` to empty, so the apiserver no
   longer trusts the v0.7.x pods either. With `failurePolicy: Fail`, all
   CREATE/UPDATE on NodeWright CRs fail with
   `x509: certificate signed by unknown authority`.

Net result: the new pod waits forever for a secret that requires leadership to
create, the old pod holds leadership forever because the new pod never goes
Ready, and Argo cannot reconcile any further NodeWright config changes.

The only field operations that broke the cycle were manual:

- Delete the old-version pods to force a lease handover, *or*
- Delete the lease object directly (`Lease/3c22c1ae.nvidia.com`).

## Design: a second lease, owned by a second manager

The webhook bootstrap is now driven by a **dedicated `ctrl.Manager`** with its
own leader-election lease name (`nodewright-webhook-bootstrap.nvidia.com`), separate from the
main reconcile manager's lease (`3c22c1ae.nvidia.com`).

```
┌────────────────────────────────────────────────────────────────────────┐
│ operator process (one per pod)                                         │
│                                                                        │
│  ┌────────────────────────────────────┐                                │
│  │ reconcile manager                  │  lease: 3c22c1ae.nvidia.com    │
│  │  - SkyhookReconciler (leader-gated)│                                │
│  │  - SecretCertWatcher (every pod)   │                                │
│  │  - webhook server (every pod)      │                                │
│  │  - readyz / healthz                │                                │
│  └────────────────────────────────────┘                                │
│                                                                        │
│  ┌────────────────────────────────────┐                                │
│  │ webhook bootstrap manager          │  lease: nodewright-webhook-bootstrap.nvidia.com │
│  │  - WebhookController (leader-gated)│                                │
│  │     creates webhook-cert Secret    │                                │
│  │     patches caBundle on            │                                │
│  │     {Validating,Mutating}WebhookConfiguration │                     │
│  └────────────────────────────────────┘                                │
└────────────────────────────────────────────────────────────────────────┘
```

`SecretCertWatcher` (`NeedLeaderElection() == false`) continues to run on the
reconcile manager on every pod. It watches `webhook-cert` via the
reconcile manager's cache and syncs it to the local disk so this
pod's webhook server can serve TLS. That part is unchanged.

Both managers share the same `rest.Config` and `scheme` and start under the
same signal handler via `errgroup.WithContext`. If either manager exits with
an error the process exits non-zero.

### Invariants this preserves

- **Single writer to `webhook-cert` and to the webhook configuration caBundle.**
  Exactly one pod holds `nodewright-webhook-bootstrap.nvidia.com` at a time. There is no
  split-brain risk between operator versions that both understand this lease.
- **Webhook server runs on every pod.** The webhook server, its readiness
  probe, and `SecretCertWatcher` are all on the reconcile manager, not gated on
  the bootstrap lease — so all pods can serve admission traffic as soon as
  `webhook-cert` exists locally.
- **`failurePolicy: Fail` on the admission webhooks remains in effect.** We
  do not need to relax admission to make this work.

### What this fixes

Any *future* upgrade discontinuity between two versions that both understand
the dedicated bootstrap lease: a new pod can win
`nodewright-webhook-bootstrap.nvidia.com` and complete the cert bootstrap independently of
who holds the main reconcile lease. Readiness flips green, Service endpoints
rotate, the rollout completes.

### Corollary: never look the webhook configurations up by name

The dedicated lease only helps when a new pod can *win* it. It cannot help when the
old pod is alive, holding the bootstrap lease, and stuck — which is exactly what a
name-based lookup of the webhook configurations produces the moment those objects are
renamed:

1. The chart renames `{Validating,Mutating}WebhookConfiguration`.
2. The old pod's `WebhookController` hard-errors on `... not found`, so its readiness
   probe never goes green.
3. The rolling update will not terminate an un-Ready old pod, so the old pod keeps
   renewing the bootstrap lease.
4. The new pod stays a follower, never mints the cert, never patches the caBundle.
   `helm upgrade` fails with `Pending termination`, and the release stays wedged.

The operator therefore selects these objects by the
`nodewright.nvidia.com/webhook-config` label (filtered to configurations whose
`clientConfig` dials its own webhook Service, since the caBundle it injects only
signs that Service's certificate) and patches every match, so a rename is
invisible to it. **Do not reintroduce a name-based lookup.** The chart must keep
applying that label.

This makes *future* renames safe. It could not save the one upgrade that introduced it
(the operator already running was the name-based one), so the chart's
`selectorMigration` pre-upgrade hook detects a pre-label-discovery operator — the live
Deployment has no `WEBHOOK_SERVICE_NAME` env var — and deletes the Deployment so Helm
recreates it.

### Corollary: never hold the bootstrap lease you cannot use

Label discovery removes the *name* coupling, but not the coupling to the webhook Service.
Ownership is "configurations that dial the Service I was told to serve", because the
caBundle only signs a certificate for that Service. So the moment the release moves to a
different webhook Service name, the running pod owns nothing, and the original deadlock
comes straight back in a new costume:

1. The release changes the webhook Service name. That is the `skyhook-operator` →
   `nodewright` rename, but also any change to `webhook.serviceName` or
   `fullnameOverride`, which are ordinary chart values.
2. The pod holding the bootstrap lease finds no configuration dialling its Service, so its
   readiness probe never goes green.
3. The rolling update will not terminate an un-Ready pod, so it keeps renewing the lease.
4. The pod that *can* reach the current configurations stays a follower, never patches the
   caBundle, and never goes Ready either. Nothing breaks the cycle.

The operator therefore **relinquishes the bootstrap lease when it can no longer do the
bootstrap job**. When no configuration dials its Service, it looks for one that dials some
*other* Service in its own namespace. If it finds one, the release has moved on without
this pod, and holding the lease is what is blocking recovery: the bootstrap manager is
stopped, which drops the lease, and after a short delay a fresh one contends again.

Three properties make this safe to do automatically:

- **It is not destructive.** Nothing is deleted and no state changes. If no other pod takes
  the lease, this one takes it back and carries on where it left off.
- **It is scoped to the manager that holds the lease.** The reconcile manager, the webhook
  server, and readiness are untouched; a pod that stands aside keeps serving admission.
- **It waits.** A helm upgrade applies the Deployment and the webhook configurations in one
  pass, so a healthy pod can observe the previous release's configurations for a moment.
  The condition must hold continuously for `supersededGracePeriod` first, which separates
  that blink from the durable state left by a rollback.

Detection deliberately lists webhook configurations **without** the label filter that
ownership uses: the release being rolled back to may predate that label entirely, which is
exactly the case in [#469](https://github.com/NVIDIA/nodewright/issues/469). Every version
of the manager ClusterRole grants `list` cluster-wide and without `resourceNames`, so this
keeps working under the RBAC a rollback restores.

### Corollary: the serving certificate must outlive the Service name

`Secret/webhook-cert` is operator-owned, so Helm does not touch it on upgrade or rollback.
A certificate minted for one Service name is therefore still sitting there, valid for up to
a year, after the release has moved to a different one. The operator remints when the
certificate's SANs no longer cover the Service it is serving, which handles the forward
direction.

The backward direction cannot be handled that way, because the operator on the other side
of a rollback is an older binary that may have no such check. The certificate it inherits
has to *already* be valid for the name it will serve. That is why the minted certificate
carries a SAN for the pre-rename Service name as well as the current one
(`legacyWebhookServiceName` in `cert_utils.go`, a `RENAME-SHIM` to be removed with the
rename window). Without it the rollback converges and then fails admission closed with an
x509 error, which is strictly worse than failing loudly.

### What this does NOT fix

This design **cannot** retroactively fix the v0.7.x → v0.15.x upgrade
specifically, because v0.7.x has no knowledge of the new lease. Operators
on v0.7.x still need the manual workaround:

```bash
# The operator's namespace. A v0.7.x install predates the skyhook -> nodewright
# namespace default, so it is almost certainly still `skyhook`; `nodewright` is
# the default only for installs created after that change.
NS=skyhook

kubectl -n "$NS" get pods                                                   # identify old-version pods
kubectl -n "$NS" delete pod <old-pod-1> <old-pod-2>                         # free the lease
# Or, equivalently:
kubectl -n "$NS" delete lease 3c22c1ae.nvidia.com

# Verify recovery:
# The object names below are the pre-rename ones, which is what a v0.7.x-era install has.
# On a chart from the resource-name rename onward they are nodewright-*; select the webhook
# configuration by label instead of by name there:
#   kubectl get mutatingwebhookconfiguration -l nodewright.nvidia.com/webhook-config
kubectl -n "$NS" get secret webhook-cert -w
kubectl get mutatingwebhookconfiguration skyhook-operator-mutating-webhook \
  -o jsonpath='{.webhooks[0].clientConfig.caBundle}' | wc -c                # must be > 0
kubectl -n "$NS" rollout status deploy/skyhook-operator-controller-manager
```

The runbook above should be added to release notes for any future major
operator version that changes the webhook bootstrap model.

## Implementation notes

- Lease names live as constants in `operator/cmd/manager/main.go`:
  `reconcileLeaseID`, `webhookBootstrapLeaseID`. **Do not rename them** without
  a coordinated upgrade plan — renaming the bootstrap lease re-introduces
  exactly the deadlock this design avoids.
- The bootstrap manager is only constructed when `ENABLE_WEBHOOKS=true`.
- The bootstrap manager disables its own metrics endpoint
  (`metricsserver.Options{BindAddress: "0"}`) and does not bind a health-probe
  port; the reconcile manager already owns those.
- The bootstrap manager runs its own cache, which adds informers for `Secret/webhook-cert` and the two webhook configurations only. The reconcile manager's cache also watches `Secret/webhook-cert`, for `SecretCertWatcher`. Two informers on the same Secret is the cost of the split and is considered acceptable.
- Both Secret informers select `webhook-cert` with a `metadata.name` field selector (`WebhookSecretCacheConfig` in `cert_watcher.go`), so each caches that one object and nothing else in the namespace. The namespaced Role relies on this: it grants `get`, `list`, `watch`, `update`, `patch` and `delete` on that Secret by `resourceNames`, and the apiserver authorizes a `list` or `watch` against `resourceNames` only when the request carries a matching `metadata.name` field selector. An informer without the selector is forbidden and never syncs, so the cache config and the Role must change together. `create` cannot be restricted by name and has a rule of its own.
- `WebhookSecretReadyzCheck` reads through the webhook controller's uncached client and is registered on the reconcile manager's health-probe server. The check reports NotReady until `webhook-cert` exists, matches the cert on disk, and its CA matches the caBundle of every webhook this operator owns, which is the desired behavior.
