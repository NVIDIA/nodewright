# helm-upgrade-rollback-test

The standing test for moving a real release forward and back: install the previously published chart, upgrade to the working tree, roll back to where we started, roll forward again, uninstall. Both directions have to work unattended, because `docs/getting-started/migration.md` offers rollback as the escape hatch when an upgrade goes wrong.

This is meant to be permanent and version-agnostic. It is the place to put any claim of the form "upgrading from the last release does X" or "rolling back from this release does Y", regardless of what is changing in a given cycle.

It was written red, as the reproducer for [#469](https://github.com/NVIDIA/nodewright/issues/469), and the operator-side fix that turns it green landed with it.

## Running just this one

```bash
# the whole suite, minus this test
CHAINSAW_ARGS="--exclude-test-regex chainsaw/helm-upgrade-rollback" make helm-tests

# only this test (after a `make push-local-image`)
LOCAL_OPERATOR_IMG=localhost:5005/skyhook-operator:testing \
  ./bin/chainsaw test --test-dir ../k8s-tests/chainsaw/helm/helm-upgrade-rollback-test
```

Chainsaw matches `--include-test-regex` / `--exclude-test-regex` against the **full** test
name, which is `chainsaw/helm-upgrade-rollback` and not the bare `helm-upgrade-rollback`
that the run log prints. A pattern without that prefix silently matches nothing, and
chainsaw reports `no tests to run` rather than complaining.

## Steps

| Step | Claim |
| --- | --- |
| 01-install-previous-released-chart | The previous published chart installs and goes Ready |
| 02-upgrade-to-branch-chart | The upgrade lands the expected surface, with both webhook configurations carrying a caBundle and dialling the right Service |
| 03-legacy-names-are-gone | Objects the upgrade is supposed to remove are actually removed |
| 04-admission-works-after-upgrade | An invalid CR is rejected by validation, not by TLS |
| 05-rollback-to-the-previous-release | `helm rollback --wait` converges, the CRDs survive (#464), and objects the baseline does not define are gone |
| 06-admission-works-after-rollback | An invalid CR is rejected by validation, not by TLS, against the rolled-back operator |
| 07-roll-forward-again | Upgrading a second time still works |
| 08-uninstall-leaves-nothing-behind | Uninstall removes the RBAC, webhook configurations, and cert Secret |

Steps 4 and 6 exist because readiness only compares the caBundle: an operator can report Ready while admission is broken, so each direction needs an admission probe of its own. Both run `admission-probe.sh`, which retries while the freshly recreated webhook Service refuses connections (#555) and then checks the rejection came from validation, not TLS.

**Step order matters.** Every upgrade claim sits above the rollback deliberately. Chainsaw abandons a test at its first failed step, so an assertion written below step 5 stops running the moment the rollback regresses, and nothing tells you it stopped. Keep new upgrade claims above it.

Step 5 is the slow one: the operator being rolled away waits out a grace period before releasing the webhook bootstrap lease, so the rollback takes roughly ninety seconds longer than a plain one.

## What step 5 is actually testing

The trigger is not the rename as such, it is that **the webhook Service name differs between the two releases**. Ownership of a webhook configuration is "it dials the Service I was told to serve", so the moment that name changes, the running operator owns nothing:

1. The rollback restores RBAC that grants nothing on `nodewright.nvidia.com`, and webhook configurations with no caBundle and no `nodewright.nvidia.com/webhook-config` label.
2. The post-rename pod holds the webhook bootstrap lease and recognises none of them, so nobody injects the caBundle.
3. The rollback-target pod cannot go Ready without that caBundle, and cannot take the lease until the post-rename pod terminates, which waits on it being Ready.

The operator now relinquishes the bootstrap lease when it owns nothing and some other Service is dialled in its namespace. That part is **not** rename-specific and stays: `webhook.serviceName` and `fullnameOverride` are ordinary chart values that produce the same mismatch on an ordinary upgrade. Reasoning in [docs/designs/webhook-bootstrap-lease.md](../../../../docs/designs/webhook-bootstrap-lease.md).

Step 6 covers the other half, which is rename-specific. `Secret/webhook-cert` is operator-owned and survives the rollback, and the operator on the far side has no remint-on-Service-change check (that arrived with #463), so the certificate it inherits has to already carry a SAN for the name it will serve. Without it step 5 converges and admission still fails closed with x509, which is worse than failing loudly.

A `pre-rollback` hook in the chart could not have fixed either half: Helm renders hooks from the revision being rolled *to*, and `v0.17.1` predates the hook.

Manual workaround, for a rollback away from an operator that predates the fix:

```bash
kubectl -n <ns> delete deploy <fullname>-controller-manager
kubectl -n <ns> delete secret webhook-cert
helm rollback <release> <rev> -n <ns> --wait
```

Pass the revision explicitly. `helm rollback` with no revision targets the previous *deployed* revision, which after a failed attempt is the newer release rather than the baseline.

## Baseline

`install-previous-chart.sh` pulls from `oci://ghcr.io/nvidia/nodewright/charts/nodewright`, the same artifact users install, so this runs between two real releases rather than between two local renders. Called with no version it resolves the newest non-prerelease `chart/v*` tag, which is the intended steady state for this test. It is currently called with an explicit `v0.17.1` for the reason in the next section.

## Rename-specific parts, to be deleted later

Everything below is scaffolding for the one-time `skyhook-operator-*` to `nodewright-*` resource-name rename (#440 / #463). It is marked `RENAME-SHIM` here and in the operator, so `grep -rn RENAME-SHIM` finds all of it, including `legacyWebhookServiceName` in `operator/internal/controller/cert_utils.go`. When the rename window closes, delete these and the test keeps working as a plain last-release-to-current upgrade and rollback:

- **The pinned `v0.17.1` baseline** in step 1. It is the last chart released before the rename, the same pin `k8s-tests/migration` uses. Advancing it to a post-rename chart makes the rename claims assert nothing, which is why it is not resolved from the newest release yet. Deleting the pin is the last step: drop the argument and the script resolves the newest release on its own.
- **`assert-legacy-names.yaml`** and the whole **`03-legacy-names-are-gone`** step, which enumerate the nine pre-rename objects.
- **The `skyhook-operator-*` assertions** in `assert-rolled-back.yaml`, and the `operator:v0.17.0` image match, which pins the baseline's appVersion.
- **`invalid-skyhook.yaml`** in step 6. It is on the legacy API group because the rolled-back operator serves nothing else; with a post-rename baseline it becomes another `invalid-nodewright.yaml`.
- **The `skyhook-operator-metrics-reader` check** in step 8.

## Teardown

`cleanup.sh` runs from every step's `catch` as well as the last step's `finally`. A failing step aborts the test, and a leftover release breaks the tests that follow in this directory: they install under different release names into the same namespace, and the kept CRDs stay stamped with this one's release, which fails the next install with "invalid ownership metadata".
