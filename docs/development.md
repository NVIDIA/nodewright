# Local Development

## ctlptl Setup

Install ctlptl once before creating the local development cluster:

```bash
brew install tilt-dev/tap/ctlptl
```

The generic install path from ctlptl is:

```bash
go install github.com/tilt-dev/ctlptl/cmd/ctlptl@v0.9.3
```

Keep this version in sync with the `ctlptl` version pinned in CI.

## Local Cluster

Bring up the kind cluster and local registry:

```bash
cd operator
make create-kind-cluster
make setup-kind-cluster
```

`make create-kind-cluster` applies `operator/config/local-dev/ctlptl-config.yaml`, which creates the kind cluster and a local registry at `localhost:5005`. `make setup-kind-cluster` is idempotent and labels the test node and creates the `skyhook` namespace pull secret.

## Webhook Iteration

Webhook development uses the operator pod, so rebuilding and restarting the deployment is the main iteration loop:

```bash
cd operator
export LOCAL_OPERATOR_IMG=localhost:5005/skyhook-operator:testing
make docker-build
docker tag ghcr.io/nvidia/skyhook/operator:testing $LOCAL_OPERATOR_IMG
docker push $LOCAL_OPERATOR_IMG
kubectl rollout restart deploy/skyhook-operator -n skyhook
```

The local registry removes the need for `kind load docker-image` or registry-pinned chart value edits while iterating on operator or webhook code.

## Teardown

Delete the cluster and registry with the same ctlptl config:

```bash
ctlptl delete -f operator/config/local-dev/ctlptl-config.yaml
```
