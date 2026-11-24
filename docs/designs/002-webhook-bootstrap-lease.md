# ADR-002: Webhook bootstrap lease

## Status

**Accepted**, 2026-05-22 (#243). Implemented. The design was kept current with the code, most recently in #944, until it was recorded as an ADR on 2026-11-24 and condensed from the [original design](https://github.com/NVIDIA/nodewright/blob/882b699d/docs/designs/webhook-bootstrap-lease.md), which has the implementation notes.

## Decision summary

The operator's webhook certificate bootstrap (minting `Secret/webhook-cert` and patching the `caBundle` of the mutating and validating webhook configurations) runs on a dedicated `ctrl.Manager` with its own leader-election lease, `nodewright-webhook-bootstrap.nvidia.com`, separate from the reconcile lease `3c22c1ae.nvidia.com`. A pod that cannot win the reconcile lease can still bootstrap the certificate, go Ready and let a rollout complete.

Three rules keep that lease usable: never look the webhook configurations up by name, never hold the bootstrap lease you cannot use, and make the serving certificate outlive the Service name. `failurePolicy: Fail` is unchanged, and upgrades from v0.7.x are not fixed.

## Context

From v0.8.0 the leader self-bootstraps webhook TLS; v0.7.x has no notion of `webhook-cert`. On an in-place rollout from v0.7.x to v0.15.x, an old pod holds `3c22c1ae.nvidia.com`, on which the bootstrap was gated, so the new pod never creates `webhook-cert` and its readiness probe never goes green. The old leader never creates the Secret either, and holds the lease forever because the new pod never goes Ready. Meanwhile Argo or Helm has reset the `caBundle` to empty, so with `failurePolicy: Fail` every CREATE and UPDATE on NodeWright CRs fails with `x509: certificate signed by unknown authority`. Only manual action broke the cycle: deleting the old pods or `Lease/3c22c1ae.nvidia.com`.

## Decision

**A second manager owns the bootstrap.** `WebhookController` runs leader-gated on a bootstrap manager holding `nodewright-webhook-bootstrap.nvidia.com`, built only when `ENABLE_WEBHOOKS=true`. The webhook server, readiness probe and `SecretCertWatcher` stay on the reconcile manager on every pod. This keeps a single writer to `webhook-cert` and the `caBundle`, admission served by every pod once it has the certificate, and `failurePolicy: Fail`.

**The lease IDs are frozen.** `reconcileLeaseID` and `webhookBootstrapLeaseID` are not renamed without a coordinated upgrade plan: renaming the bootstrap lease reintroduces exactly the deadlock this design avoids.

**Never look the webhook configurations up by name.** The operator patches every configuration labelled `nodewright.nvidia.com/webhook-config` whose `clientConfig` dials its own webhook Service, since its `caBundle` signs only that Service's certificate. A name lookup turns a chart rename into a hard error on the old pod, which then never goes Ready, so the rolling update never terminates it and it keeps the bootstrap lease. The chart must keep applying the label. The upgrade that introduced label discovery still ran a name-based operator, so the chart's `selectorMigration` pre-upgrade hook deletes a Deployment lacking the `WEBHOOK_SERVICE_NAME` env var and lets Helm recreate it.

**Never hold the bootstrap lease you cannot use.** When no configuration dials the operator's Service but one dials another Service in its namespace, the release has moved on without this pod: it stops the bootstrap manager, dropping the lease, and contends again after a short delay. Otherwise any webhook Service name change (the `skyhook-operator` to `nodewright` rename, or an ordinary `webhook.serviceName` or `fullnameOverride` change) recreates the deadlock: the lease holder owns nothing, stays un-Ready and so is never terminated, and the pod that could patch the `caBundle` stays a follower. It is safe automatically: nothing is deleted or changed (if no other pod takes the lease, this one takes it back), only the bootstrap manager stops, and the condition must hold for `supersededGracePeriod`, so a helm upgrade's momentary view of the previous configurations is not mistaken for the durable state a rollback leaves. Detection lists configurations without the label filter, because the release rolled back to may predate the label (#469); every manager ClusterRole version grants that `list` cluster-wide without `resourceNames`.

**The serving certificate must outlive the Service name.** Helm leaves the operator-owned `Secret/webhook-cert` in place across upgrade and rollback. Forward, the operator remints when the certificate's SANs no longer cover its Service. Backward, the operator on the far side of a rollback may be an older binary without that check, so the certificate also carries a SAN for the pre-rename Service name (`legacyWebhookServiceName`, a `RENAME-SHIM` removed with the rename window). Without it the rollback converges and then fails admission closed with an x509 error, which is worse than failing loudly.

## Non-goals

Fixing the upgrade from v0.7.x, which has no knowledge of the bootstrap lease. Those installs still need the manual recovery in the [migration guide](../getting-started/migration.md#faq).

## Alternatives

- **Keep the bootstrap on the reconcile lease:** an old leader holding that lease blocks the bootstrap indefinitely.
- **Look the webhook configurations up by name:** a rename wedges the rollout on the old pod.
- **Rely on reminting alone for rollback:** the operator rolled back to may lack the remint check.
- **Filter superseded-release detection by the label:** the rollback target may predate the label.

## Consequences

- Upgrades between versions that both understand the bootstrap lease need no manual action, and future webhook configuration renames are safe.
- `webhook-cert` has two informers, one per manager's cache: the accepted cost of the split.
- A rollback across a Service name change is slower by the grace period before the old pod releases the lease.
- A future major operator version that changes the webhook bootstrap model puts a manual recovery runbook in its release notes.

## References

- #243 (the deadlock and the dedicated lease), #469 (rollback to a release that predates the label), #944 (last revision before recording as an ADR).
