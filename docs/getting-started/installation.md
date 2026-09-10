# Installation

NodeWright is installed via Helm as an OCI artifact from GitHub Container Registry.

## Prerequisites

- Kubernetes cluster (tested on v1.30+)
- Helm 3.8+ installed (required for native OCI support)
- Container registry access credentials (if using private registries)

## Configure Image Pull Secrets (if needed)

If you're using private container registries, create the namespace and secret before installing so the release can reference it:

```bash
kubectl create namespace nodewright

kubectl create secret generic node-init-secret \
  --from-file=.dockerconfigjson=${HOME}/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  --namespace nodewright
```

NodeWright currently uses a single shared image pull secret for all packages, and agent/operator containers. If you need access to multiple registries, combine the credentials into one `dockerconfigjson` secret with multiple registry auths.

## Install NodeWright

```bash
# The chart is distributed as an OCI artifact on GitHub Container Registry.
# Helm 3.8+ supports OCI natively — no `helm repo add` needed.
helm install nodewright oci://ghcr.io/nvidia/nodewright/charts/nodewright \
  --version <chart-version> \  # latest: https://github.com/NVIDIA/nodewright/releases?q=chart
  --namespace nodewright \
  --create-namespace \
  --set imagePullSecret=node-init-secret
```

Omit `--set imagePullSecret=node-init-secret` if you're pulling from public registries only.

> **Where things live:** chart at `oci://ghcr.io/nvidia/nodewright/charts/nodewright`, operator image at `ghcr.io/nvidia/nodewright/operator`, agent image at `ghcr.io/nvidia/nodewright/agent`.
> **Migrating from `helm repo add skyhook https://helm.ngc.nvidia.com/...`?** Run `helm repo remove skyhook`, then point your existing release at the OCI chart. Because the release already exists, this is an **upgrade, not an install** — `helm install` fails on a name that is already in use:
>
> ```bash
> helm upgrade <release-name> oci://ghcr.io/nvidia/nodewright/charts/nodewright \
>   --version <chart-version> --namespace <existing-namespace>  # latest: https://github.com/NVIDIA/nodewright/releases?q=chart
> ```
>
> Keeping the old release name (e.g. `skyhook`) is fine — the chart works either way.
>
> **Already installed in the `skyhook` namespace?** Stay there. The documented namespace for **new** installs moved from `skyhook` to `nodewright`, but Kubernetes namespaces cannot be renamed in place and Helm cannot move a release between namespaces, so there is nothing to migrate and no deadline. `kubectl nodewright` finds the operator in either namespace automatically. See [Install namespace](migration.md#install-namespace-skyhook---nodewright).

## Verify Installation

```bash
# Check that the operator is running
kubectl get pods -n nodewright

# Wait for the deployment to be available
kubectl wait --for=condition=Available deployment -l control-plane=controller-manager -n nodewright --timeout=300s

# Wait for the operator pod to be ready
kubectl wait --for=condition=Ready pod -l control-plane=controller-manager -n nodewright --timeout=300s

# Verify the CRDs are installed
kubectl get crd | grep nodewright
```

## Uninstalling

By default, the Helm chart includes a pre-delete hook that automatically cleans up all NodeWright and DeploymentPolicy resources before uninstalling:

```bash
helm uninstall nodewright --namespace nodewright
```

The pre-delete hook will:

- Delete all NodeWright resources, and legacy Skyhook resources if any remain
- Delete DeploymentPolicy resources in both API groups
- Complete quickly if no resources exist
- Wait for finalizers to be processed if resources exist

Each delete is best-effort — a failing one does not fail the hook. The **job as a
whole** is bounded by `cleanup.jobTimeoutSeconds` (default 120), and that bound is
a hard deadline: if a finalizer keeps the job waiting past it, the job is killed
and marked `Failed`, which fails the pre-delete hook and so fails
`helm uninstall`. If that happens, clean up by hand using the commands below and
re-run the uninstall with `--no-hooks`.

### Cleanup options

Set these at install time with `helm install`, or on an existing release with
`helm upgrade`. When changing a setting on a release you already have, pass
**both** of these or you will change more than you meant to:

- `--version` pinned to the chart version you are already running (`helm list -n nodewright`
  shows it). Without it, `helm upgrade` against this OCI chart fails outright — GHCR
  doesn't support resolving an unpinned "latest" install or upgrade.
- `--reuse-values`, so the values you set at install time (an
  `imagePullSecret`, say) survive. Without it Helm starts from the chart
  defaults and applies only your `--set` flags.

```bash
# Disable automatic cleanup and manage resources manually
helm upgrade nodewright oci://ghcr.io/nvidia/nodewright/charts/nodewright \
  --version <chart-version> --reuse-values \  # installed: helm list -n nodewright
  --namespace nodewright --set cleanup.enabled=false
```

```bash
# Adjust the cleanup job timeout
helm upgrade nodewright oci://ghcr.io/nvidia/nodewright/charts/nodewright \
  --version <chart-version> --reuse-values \  # installed: helm list -n nodewright
  --namespace nodewright --set cleanup.jobTimeoutSeconds=180
```

### Manual cleanup

If you disabled automatic cleanup, or need to clean up by hand:

```bash
# Delete all NodeWright and DeploymentPolicy resources first. This mirrors what
# the pre-delete hook does: a migrated cluster can still hold legacy resources,
# and their finalizers block uninstall just as the current ones do.
kubectl delete nodewrights --all --ignore-not-found
kubectl delete skyhooks --all --ignore-not-found
kubectl delete deploymentpolicies.nodewright.nvidia.com --all --ignore-not-found
kubectl delete deploymentpolicies.skyhook.nvidia.com --all --ignore-not-found

# Then uninstall the chart
helm uninstall nodewright --namespace nodewright
```

**Why cleanup matters:** uninstalling while NodeWright CRs with finalizers still exist can leave resources in a broken state that causes problems on reinstall.

For more details on explicit package uninstall, see [Uninstall](../user-guide/uninstall.md).

## Related

- [Kubernetes support matrix](../operations/kubernetes-support.md)
- [Versioning](../operations/versioning.md)
- [Migration from Skyhook](migration.md)
