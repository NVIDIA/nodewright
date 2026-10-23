# Publishing Package Artifacts

A NodeWright package can be published to an OCI registry as a package artifact instead of a container image. This page shows package authors how to build and push one, and how to convert an existing image package, and shows cluster operators how to turn artifact packages on. The [Package Artifact Specification](package-artifact-spec.md) defines the format, and [Package artifacts](../architecture/lifecycle.md#package-artifacts) describes what the operator does with one.

## Before you start

The package tree is exactly what a legacy package image carried under `/skyhook-package`:

- `config.json`, a regular file at its root, not a symlink.
- `skyhook_dir/`, the step scripts.
- `root_dir/`, the static files.

`configmaps/` and `node-metadata/` are overlaid at run time: the agent lays the package's configMap keys and the node metadata over the payload, as the `SKYHOOK_DATA_DIR` entry under [Environment variables](../../agent/README.md#environment-variables) in the agent README describes, and a mounted file wins over a payload file of the same path.

The commands below need `tar`, `gzip`, `jq` and `oras`, and `crane` to convert an image package. They read these shell variables:

| Variable | Holds |
|---|---|
| `PACKAGE_DIR` | The path of the package tree |
| `UNINSTALL` | `supported` or `unsupported` |
| `ARTIFACT_REF` | The reference to push to, `registry/repository:version` |
| `IMAGE_REF` | The image package to convert |

Run them in order from a working directory outside the package tree, where they write `payload.tar.gz`, `metadata.json` and an OCI image layout, and stop at the first one that fails: each command reads what the one before it wrote.

CI runs the commands that pack the payload, write the metadata, push into a local layout and convert an image package, with the `oras` and `crane` versions pinned in [`.github/actions/setup-oras`](../../.github/actions/setup-oras/action.yml) and [`.github/actions/setup-crane`](../../.github/actions/setup-crane/action.yml), and adds `--insecure` to `crane export` because its test registry's certificate is self-signed. It runs neither the registry push, which it checks is the local-layout push with `ARTIFACT_REF` as its destination, nor `oras login`, `oras manifest fetch` or `oras resolve`.

## Pack the payload

```sh payload
COPYFILE_DISABLE=1 tar -c -z -f payload.tar.gz -C "$PACKAGE_DIR" .
```

This writes the tree as one gzip-compressed tar, `payload.tar.gz`. `COPYFILE_DISABLE=1` keeps the macOS `tar` from adding an AppleDouble `._*` entry beside each file that has extended attributes; GNU `tar` ignores it.

The tree holds only regular files, directories and symlinks. A hardlink, a device or a FIFO breaks the [tar rules](package-artifact-spec.md#layers), so the agent refuses the payload when it lays it out; replace a hardlink with a copy.

## Write the metadata

```sh metadata
jq --arg uninstall "$UNINSTALL" '{uninstall: $uninstall, config: .}' "${PACKAGE_DIR:?}/config.json" > metadata.json
```

This writes the package [metadata](package-artifact-spec.md#metadata), `metadata.json`: an object whose `uninstall` is `UNINSTALL` and whose `config` is the content of the package's `config.json`, with no `interrupt` field, because the operator rejects an artifact that declares an interrupt (see [Limitations](#limitations)). `:?` stops the command when `PACKAGE_DIR` is empty, rather than reading `/config.json`.

`UNINSTALL` is exactly `supported` or `unsupported`:

- `supported` leaves `uninstall.enabled` the opt-in, as for an image package: the operator runs the package's uninstall only when `uninstall.enabled` is true.
- `unsupported` means the operator never starts the package's uninstall, even with `uninstall.enabled: true`.

[Precedence](uninstall.md#precedence) has the whole table. The metadata is at most 1 MiB, the `MaxMetadataSize` [bound](package-artifact-spec.md#bounds).

## Push the artifact

`ARTIFACT_REF` is the package's `image` and `version` in the NodeWright, joined by a colon: `registry/repository:version`, such as `registry.example.com/packages/tuning:1.0.0`. Log in to its registry, then push:

```sh
oras login registry.example.com
```

```sh push
oras push \
  --artifact-type application/vnd.nvidia.nodewright.package.v1 \
  --config metadata.json:application/vnd.nvidia.nodewright.package.config.v1+json \
  "$ARTIFACT_REF" \
  payload.tar.gz:application/vnd.nvidia.nodewright.package.payload.v1.tar+gzip
```

The three media types are the `ArtifactTypePackage`, `MediaTypeMetadata` and `MediaTypePayload` constants of the [specification](package-artifact-spec.md#media-types). `oras push` prints the digest of the manifest it pushed.

To check the artifact locally, run the same command with an OCI image layout in the working directory as its destination, then print the manifest it wrote:

```sh push-layout
oras push \
  --artifact-type application/vnd.nvidia.nodewright.package.v1 \
  --config metadata.json:application/vnd.nvidia.nodewright.package.config.v1+json \
  --oci-layout package-layout:check \
  payload.tar.gz:application/vnd.nvidia.nodewright.package.payload.v1.tar+gzip
```

```sh
oras manifest fetch --oci-layout package-layout:check
```

## Convert an image package

`IMAGE_REF` is the image package's `image` and `version` (`registry/repository:version`), or its `image` and `containerSHA` (`registry/repository@sha256:...`). Take its package tree out into a new `PACKAGE_DIR`:

```sh convert
mkdir "$PACKAGE_DIR" &&
  crane export --platform linux/amd64 "$IMAGE_REF" - | tar -x -p -f - --strip-components=1 -C "$PACKAGE_DIR" skyhook-package
```

`crane export` writes the image's filesystem as one tar, and `tar` extracts only its `skyhook-package` directory into `PACKAGE_DIR`, keeping file modes. `mkdir` has no `-p`, and `&&` runs the export only when `mkdir` succeeds, so a `PACKAGE_DIR` left from an earlier attempt fails the conversion instead of merging stale files into the new tree. One artifact serves every node, so `--platform` picks the platform whose `/skyhook-package` becomes the package; name another one for an image published only for it.

Then [pack the payload](#pack-the-payload), [write the metadata](#write-the-metadata) and [push](#push-the-artifact) under a **new** version, and set the package's `version` in the NodeWright to it. Never push the artifact under the image's own tag: nodes that already ran that version never run it again, a rollout under way mixes the image and the artifact, and on a cluster with the option off its Jobs fail at the image pull, because an artifact is not a runnable image.

## Re-push a tag

The operator resolves a version's tag when it is about to build a stage Job of the package, at most once every 30 seconds, and pins that Job to the digest it found (see [Resolution and the cache](../architecture/lifecycle.md#resolution-and-the-cache)). New content pushed under the tag of a version therefore reaches every stage built more than 30 seconds after the push, on every node, while a Job already built keeps its digest. Nodes that already completed the version do not run it again, and a rollout under way can run both digests. Publish changed content under a new version instead. A tag whose artifact was rejected is resolved again within 30 seconds, so a corrected push to it is picked up then; one the registry refused or lacked is read again within the retry wait.

For immutable content, set the package's `containerSHA` to the artifact's manifest digest, which `oras push` prints and `oras resolve` reports:

```sh
oras resolve registry.example.com/packages/tuning:1.0.0
```

## Enable package artifacts

Artifact packages run only while the operator's package artifacts option is on: the chart value `controllerManager.manager.env.packageArtifacts` set to `"true"`, which is the `PACKAGE_ARTIFACTS` environment variable in `operator/config/manager` for a kustomize install.

```yaml
controllerManager:
  manager:
    env:
      packageArtifacts: "true"
```

The operator then resolves each package's reference to its manifest digest before it builds a stage Job, and inspects each digest once, keeping what it found in a memory-backed cache on its pod, sized by the chart value `controllerManager.manager.packageArtifactCacheSizeLimit` (64Mi by default). That adds outbound HTTPS from the operator pod to every package registry, container-image packages included. Until a digest is inspected, normally within seconds, its package gets no Job. A container-image package runs as before, with its Jobs pinned to the digest, as long as the operator can read its registry: every package's registry, container-image packages included, must then be reachable from the operator pod over HTTPS, with a certificate the operator trusts, or the package gets no Job.

**Prerequisite:** the operator's agent image and every package's `agentImageOverride` must be the Go agent, `agent/v7.0.0` or newer. With the option on, every interrupt Job ends with a container that runs `agent --version`, which the Python agent (v6.x) lacks, so those Jobs would never complete.

## Registry credentials

List the pull Secrets in the chart value `imagePullSecrets`, in the order to try them; the older `imagePullSecret` still names one, which comes first. For a kustomize install, set the comma-separated `IMAGE_PULL_SECRETS` in `operator/config/manager`, and list the same Secrets under the Deployment's `imagePullSecrets`; the operator still reads `IMAGE_PULL_SECRET`, which comes first.

```yaml
imagePullSecrets:
  - name: registry-credentials
  - name: other-registry-credentials
```

| Source | Operator resolution and inspection | Agent pull of an artifact | Kubelet pull of an image package |
|---|---|---|---|
| Each pull Secret's `.dockerconfigjson`, in order | Yes, read at every request | Yes, mounted and listed in `NODEWRIGHT_REGISTRY_AUTH_FILES` | Yes, from the pod's `imagePullSecrets` |
| The node's Docker config files and the kubelet's credential provider plugins | No | No | Yes |
| Anonymous | When no Secret holds a credential for the registry | When no Secret holds a credential for the registry | As the kubelet does |

With the option on, every package in a private registry, a container-image package included, therefore needs its pull Secret listed: the operator reads each package's reference before it builds a Job. A registry the nodes reach only through their own identity, such as ECR with the instance role, ACR through the kubelet identity or Artifact Registry with the node service account, needs a Secret that holds a credential for it. A credential that expires, such as an ECR token, needs something that refreshes its Secret; the operator reads the Secret at every request, so a refreshed one applies without a restart. A Secret that is missing or not of type `kubernetes.io/dockerconfigjson` is passed over with a `PackageArtifactPullSecret` Warning. [Registry credentials](../architecture/lifecycle.md#registry-credentials) has the details.

When the operator cannot read a reference, the package gets no Job, never a container-image Job in its place, and a `PackageArtifactInspectionFailed` Warning names the reference and the error:

- **The registry timed out or could not be reached**, or answered a 5xx, 429 or 408: the package waits, and the operator reads the reference again after 30 seconds. A registry the operator can never reach, such as one its egress rules deny, looks the same, so the package waits, with a Warning once per outage.
- **The registry refused every credential (`unauthorized`), has no such reference (`not-found`), presented a certificate the operator does not trust (`tls`), speaks plain HTTP (`plain-http`), has a host name that does not resolve (`no-such-host`), or refused the request (`refused`)**: the package is blocked. The `Blocked` condition, reason `PackageArtifactUnusable`, names it with the error, such as `1 package blocked: tuning:1.0.0 (unauthorized)`, and each node whose next stage needs it reports `blocked`. The operator reads the reference again after 5 minutes, then at doubling intervals up to an hour, so a Secret added or fixed later is picked up without a change to the NodeWright.

See [What the pass decides](../architecture/lifecycle.md#what-the-pass-decides) and [Why an unreadable reference gets no Job](../architecture/package-artifacts.md#why-an-unreadable-reference-gets-no-job).

## Minimum versions

- **Operator:** the first operator release after `operator/v0.20.0`, and the first chart release after `chart/v0.20.0` for the `packageArtifacts` and `imagePullSecrets` values.
- **Agent:** the first agent release after `agent/v7.0.0` for artifact packages, and `agent/v7.0.0` or newer for every agent image once the option is on.

The operator checks no agent version, so an agent that is too old shows only once its Jobs run: an artifact stage fails, or an interrupt Job never completes.

## Image and artifact packages side by side

A package's reference stays its `image` plus `version`, or plus `containerSHA`, and the NodeWright resource needs no change. The operator tells an artifact from an image by the manifest at that reference, so one NodeWright can hold both kinds of package. With the option off, the operator makes no registry request and runs every package as an image, so an artifact package's Jobs fail at the image pull.

## Limitations

- **Credentials** come only from the pull Secrets: no node Docker configs, no kubelet credential provider plugins, and no Docker credential helpers.
- **Registries** are reached at their own host, from the operator pod and from the agent: containerd registry mirrors and `hosts.toml` are not used.
- **HTTPS only.** With the option on, a package in a registry served over plain HTTP, an artifact or a container image, is blocked as `plain-http`.
- **No node proxy or private CA.** The operator and the agent reach the registry themselves, trusting only the CA bundles of their own images and the proxy environment of their own pods, never the node's container runtime configuration. With the option on, a package in a registry that the nodes trust through a private CA configured on the node, container images included, is blocked as `tls`, and one the nodes reach only through a node proxy waits as for an outage. The chart offers no proxy passthrough for the operator.
- **A tag is resolved at each stage.** A version without `containerSHA` runs whatever its tag names when each stage Job is built, as resolved at most 30 seconds before, so a re-pushed tag can give one rollout two digests (see [Re-push a tag](#re-push-a-tag)).
- **A payload is read at each stage.** Each stage that lays an artifact out, `apply`, `config`, `post-interrupt`, `upgrade` and `uninstall`, reads its payload from the registry again, while the kubelet fetches an image package's layers once per node. Check stages and interrupts reuse the copy already laid out.
- **No multi-platform artifacts.** An artifact has one payload for every node, so an image whose `/skyhook-package` differs by platform cannot become one artifact.
- **Host-files layers and interrupt declarations** parse, but the operator rejects an artifact that has either, which blocks the package, and the agent refuses one, until a later release supports them.

## What each mistake produces

Each reason below is one of the [specification's](package-artifact-spec.md#classification). The operator blocks the package and names the reason in the `Blocked` condition, reason `PackageArtifactUnusable`, such as `tuning:1.0.0 (invalid-artifact: metadata-schema)`, and in the `PackageArtifactRejected` Warning.

| Mistake | Result |
|---|---|
| `UNINSTALL` empty, unset or misspelled | `metadata-schema`: the operator rejects the artifact and blocks the package |
| Pushing what a failed command left, such as the empty `metadata.json` that `jq` leaves when `config.json` is missing | `descriptor`: the operator rejects the artifact and blocks the package |
| A payload without `config.json`, or with one that is not JSON-equal to the metadata `config` | `config-mismatch`, found only when the agent lays the payload out: the operator accepts the artifact, and the stage fails |

See also [Package Artifact Design](../architecture/package-artifacts.md), the design behind how the operator and the agent read a package artifact.
