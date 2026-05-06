# Local Development

## ctlptl Setup

The local development targets install the pinned `ctlptl` binary through `operator/deps.mk` into `operator/bin`. No global install is required when using the Makefile.

If you want a standalone `ctlptl` on your `PATH`, install the same version pinned in `operator/deps.mk`:

```bash
go install github.com/tilt-dev/ctlptl/cmd/ctlptl@v0.9.3
```

Homebrew is also available for manual use:

```bash
brew install tilt-dev/tap/ctlptl
```

Keep manual installs in sync with `CTLPTL_VERSION` in `operator/deps.mk`.

## Local Cluster

Bring up the kind cluster and local registry:

```bash
cd operator
make create-kind-cluster
```

`make create-kind-cluster` applies `config/local-dev/ctlptl-config.yaml`, which creates the kind cluster and a local registry at `localhost:5005`. It also runs `make setup-kind-cluster`, which is idempotent and labels the test node and creates the `skyhook` namespace pull secret.

## Webhook Iteration

Webhook development uses the operator pod, so rebuilding and restarting the deployment is the main iteration loop:

```bash
cd operator
make rollout-local
```

The local registry removes the need for `kind load docker-image` or registry-pinned chart value edits while iterating on operator or webhook code.

## Teardown

Delete the cluster and registry with the same ctlptl config:

```bash
cd operator
make delete-kind-cluster
```
