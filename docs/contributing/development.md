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

## Kubernetes Test Versions

`operator/versions.yaml` is the source of truth for the Kubernetes versions used by envtest, local kind clusters, and GitHub Actions test matrices. `operator/versions.sh` is the only reader for that file; it uses `yq` and derives every consumer name from the key's path (see the header comment in `versions.yaml`). The Go toolchain version is not here: it lives in `go.mod`, which the Make build (image build-arg) and CI (`setup-go` `go-version-file`) both read directly.

- `envtest.k8s.version` controls `setup-envtest` binary assets under `operator/bin/k8s/`. It is nested three deep so the derived name is `ENVTEST_K8S_VERSION`, which the test lib expects.
- `kind.nodeImage` controls the default `kindest/node` image for local kind clusters. Make also exposes this as `KIND_VERSION` and `KIND_NODE_IMAGE_VERSION` for local overrides.
- `kind.binary` controls the Kind CLI/action version and is intentionally separate from Kubernetes node image versions.
- `ci.kindNodeImages` and `ci.primaryKindNodeImage` feed the GitHub Actions matrix.

For a development shell, install the local reader dependency and load the `UPPER_SNAKE` values into your environment. `source` works in bash; in other shells (zsh, etc.) eval the exports instead:

```bash
make -C operator yq
source operator/versions.sh                 # bash
eval "$(operator/versions.sh --export)"     # any shell
```

Note the exported names are derived from the yaml path (`kind.nodeImage` → `KIND_NODEIMAGE`) and are not the same as the Make override variables (`KIND_VERSION`, `KIND_NODE_IMAGE_VERSION`, …). To override a Make default, set that Make variable directly, e.g. `make KIND_VERSION=1.34.0 …`.

For scripts, read one key (a yq path) or print the GitHub Actions output shape:

```bash
operator/versions.sh --get envtest.k8s.version
operator/versions.sh --print
```

`make unit-tests` installs the configured envtest assets and exports `KUBEBUILDER_ASSETS` for the Go test process. If you run `go test` or `ginkgo` directly, set `KUBEBUILDER_ASSETS` yourself or controller-runtime will use its default `/usr/local/kubebuilder` lookup path.

These versions often match, but they are allowed to diverge. Kind does not publish every Kubernetes patch version as a `kindest/node` image, so validate a new node image before using it:

```bash
cd operator
make validate-kind-node-image KIND_NODE_IMAGE_VERSION=1.35.0
```

When updating supported test versions, edit `operator/versions.yaml` first, then run the validation target and the relevant tests.

The check retries a failed registry read a few times before giving up (see [Retrying Registry Reads](#retrying-registry-reads)), so a tag that genuinely does not exist takes about fifteen seconds to report rather than failing instantly.

## Retrying Registry Reads

Docker Hub, ghcr.io and nvcr.io all reset connections often enough that an unretried read fails a whole CI job, and the error it prints usually blames the image ("is not published") rather than the network. `scripts/retry.sh` wraps those reads so only a sustained outage stops a run:

```bash
scripts/retry.sh -- docker manifest inspect kindest/node:v1.35.0
scripts/retry.sh --attempts 5 --delay 10 -- oras repo tags nvcr.io/nvidia/distroless/static
```

It re-runs the command until it succeeds, doubling the delay between attempts, and exits with the command's own status once the attempts run out. Only the successful attempt's stdout is emitted, so wrapping a command whose output is captured or piped to `jq` stays safe. Use it for reads only — a push or a tag move is not safe to repeat. `make validate-kind-node-image` and `scripts/latest-distroless.sh` both call it already; override the path with `RETRY=` if you need to.

Workflow steps that download a pinned binary pass curl's own `--retry ... --retry-all-errors` instead, and `.github/actions/resolve-oci-digest` retries by default.

## Distroless Base Images

The operator and agent images build `FROM` NVIDIA's distroless base (`nvcr.io/nvidia/distroless/static`). CI picks the version with `scripts/latest-distroless.sh`, which asks the registry directly instead of reading `https://developer.download.nvidia.com/distroless-oss/versions.json`. That file advertises a release days before the matching image is pushed, so a build that trusts it fails with a 404 on the base image for as long as the two are out of step; the registry's tag list cannot be ahead of the images it lists.

The distroless repositories are public, so the script reads them with [`oras`](https://oras.land) anonymously and no NGC credentials are involved. Each `oras` call goes through `scripts/retry.sh`, so a reset connection does not fail the build. CI installs `oras` through `.github/actions/setup-oras`; install it locally to run the script yourself.

```bash
scripts/latest-distroless.sh --repo nvcr.io/nvidia/distroless/static --major 4
scripts/latest-distroless.sh --repo nvcr.io/nvidia/distroless/static --major 4 --print
```

`--major` is required. A new distroless major is a base-OS change that should be reviewed, not something a build picks up on its own, so raising it is a deliberate edit to the workflow.

The script also resolves the tag to a digest, which CI passes to `docker buildx build` as `DISTROLESS_DIGEST_SUFFIX=@sha256:…`. Every architecture's build then pins to the byte-identical base even if the tag is re-pushed mid-run, and the `org.opencontainers.image.base.name` label records exactly what was built on. That build arg defaults to empty, so a build that does not set it still resolves the base by tag.

Local image builds do not call the script: `operator/Makefile` and `agent/Makefile` each carry a `DISTROLESS_VERSION ?=` default so a build works offline. Refresh it with the command above when it drifts, or override it per build (`make docker-build DISTROLESS_VERSION=4.0.8`).

## Local Cluster

Bring up the kind cluster and local registry:

```bash
cd operator
make create-kind-cluster
```

`make create-kind-cluster` renders `config/local-dev/ctlptl-config.yaml` into `reporting/ctlptl-config.yaml`, deletes any existing cluster defined by that rendered config, and then creates the kind cluster plus a local registry at `localhost:5005`. It also runs `make setup-kind-cluster`, which is idempotent and labels the test node and creates the `skyhook` namespace pull secret.

Do not run `ctlptl apply -f config/local-dev/ctlptl-config.yaml` directly; the tracked file is a Make-rendered template.

## Webhook Iteration

Webhook development uses the operator pod, so rebuilding and restarting the deployment is the main iteration loop:

```bash
cd operator
make rollout-local
```

The local registry removes the need for `kind load docker-image` or registry-pinned chart value edits while iterating on operator or webhook code.

## Teardown

Delete the cluster and registry with the rendered ctlptl config:

```bash
cd operator
make delete-kind-cluster
```
