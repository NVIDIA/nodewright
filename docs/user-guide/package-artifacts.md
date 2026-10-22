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

This writes the package [metadata](package-artifact-spec.md#metadata), `metadata.json`: an object whose `uninstall` is `UNINSTALL` and whose `config` is the content of the package's `config.json`, with no `interrupt` field, because preflight rejects an artifact that declares an interrupt (see [Limitations](#limitations)). `:?` stops the command when `PACKAGE_DIR` is empty, rather than reading `/config.json`.

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

Then [pack the payload](#pack-the-payload), [write the metadata](#write-the-metadata) and [push](#push-the-artifact) under a **new** version, and set the package's `version` in the NodeWright to it. Clear its `containerSHA` too, or set it to the artifact's digest: a set `containerSHA` names the content instead of the version, so the package would go on running the old image. Never push the artifact under the image's own tag. With package artifacts on, nodes whose preflight already recorded that version as an image keep running it as one, while nodes whose preflight runs later install the artifact, so one version would mean different content on different nodes. On a cluster with the option off, the next Jobs of that version fail at the image pull, because an artifact is not a runnable image.

## Re-push a tag

Each node's preflight resolves the tag once, when the package's install or upgrade starts there, and every Job of that version on that node is pinned to the digest it recorded, uninstall included (see [What preflight decides](../architecture/lifecycle.md#what-preflight-decides)). New content pushed under an existing tag therefore reaches only nodes whose preflight runs after the push, so nodes can run different content under one version; a node that already installed the version keeps its content until the package's next install there.

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

Each package's install on a node, and each upgrade, then starts with a `preflight` stage: an agent pod on that node inspects the package's reference with the node's own credentials and reports what it found, and the operator builds the package's Jobs on that node from the result. The operator itself contacts no registry. See [Package artifacts](../architecture/lifecycle.md#package-artifacts) for what each result does.

**Prerequisite:** the operator's agent image and every package's `agentImageOverride` must be the Go agent with the `preflight` mode, the first agent release after `agent/v7.0.0`. With the option on, every interrupt and preflight Job ends with a container that runs `agent --version`, which the Python agent (v6.x) lacks, so those Jobs would never complete. An older Go agent reports no preflight result, so every package runs as a container image, with a `PackageArtifactInspectionFailed` Warning.

## Registry credentials

The preflight pod and the artifact stage pods authenticate as the kubelet does when it pulls a container-image package on the same node, so turning a package from an image into an artifact needs no credential change. They try these sources in order:

| Source | How it reaches the agent |
|---|---|
| The `imagePullSecrets` of the package ServiceAccount, in order | Each Secret's `.dockerconfigjson`, mounted read-only and listed in `NODEWRIGHT_REGISTRY_AUTH_FILES`. Package pods run as this ServiceAccount (`PACKAGE_SERVICE_ACCOUNT`; the chart creates `<fullname>-package` and fills it from the `imagePullSecrets` value), so the kubelet pulls package images with the same Secrets |
| The node's Docker config files `/var/lib/kubelet/config.json`, `/root/.docker/config.json` and `/.docker/config.json` | Read from the host root |
| The kubelet's credential provider plugins, such as those for ECR, ACR and Artifact Registry | Found from the running kubelet's flags and run on the host, as the kubelet runs them |
| Anonymous | When no source holds a credential for the registry |

[Registry credentials](../architecture/lifecycle.md#registry-credentials) and [`agent/README.md`](../../agent/README.md#package-artifacts) have the details.

When preflight cannot inspect a reference, because the registry is unreachable, refuses every credential, has no such reference or timed out, the package runs as a container image on that node, with a `PackageArtifactInspectionFailed` Warning on the NodeWright and a `Preflight` Warning on the node naming the error. Such a reference is most likely an existing image package, which the kubelet pulls as before; an artifact then fails visibly at the image pull. Once that `apply` or `upgrade` Job fails, preflight runs again before it is retried.

## Minimum versions

- **Operator:** the first operator release after `operator/v0.20.0`, and the first chart release after `chart/v0.20.0` for the `packageArtifacts` value.
- **Agent:** the first agent release after `agent/v7.0.0`, which has the `preflight` mode, for every agent image once the option is on.

The operator checks no agent version, so an agent that is too old shows only once its Jobs run: a preflight reports no result and the package runs as a container image, or an interrupt or preflight Job never completes.

## Image and artifact packages side by side

A package's reference stays its `image` plus `version`, or plus `containerSHA`, and the NodeWright resource needs no change. Preflight tells an artifact from an image by the manifest at that reference, so one NodeWright can hold both kinds of package. With the option off, no package gets a preflight and every package runs as an image, except one a preflight already recorded as an artifact on a node, which keeps running as one there.

## Limitations

- **Credentials** come only from the sources above: no Docker credential helpers, and no credential provider plugin that needs a ServiceAccount token (`tokenAttributes`), since the kubelet mints that token per pod. Plugins are found only for a kubelet that runs as its own `kubelet` binary in the host's mount and PID namespaces, not one built into another binary such as k3s or RKE2, or one that runs in a container. A credential provider config or plugin directory under the kubelet's pod directory (`<root-dir>/pods`, where pod volumes live) is refused.
- **Registries** are reached at their own host: containerd registry mirrors and `hosts.toml` are not used.
- **HTTPS only.** A registry served over plain HTTP cannot hold artifact packages.
- **No node proxy or private CA.** The agent reaches the registry itself, trusting only the CA bundle of its own image and the proxy environment of its pod (the package's `env`), never the node's container runtime configuration. A registry that the nodes reach through a proxy or a private CA configured on the node is unreachable for artifacts.
- **One result per version and node.** Preflight resolves the reference once per install or upgrade on a node; a re-pushed tag, or for an accepted artifact a changed `image` or `containerSHA` without a new `version`, reaches that node only at its next install. Where preflight found an image or could not inspect the reference, a changed `image` or `containerSHA` is inspected again until the version's `apply` or `upgrade` completes on the node, and so is a reference that could not be inspected once that `apply` or `upgrade` fails; after that, give the change a new `version` (see [Staleness](../architecture/lifecycle.md#staleness)).
- **A payload is read at each stage.** Each stage that lays an artifact out, `apply`, `config`, `post-interrupt`, `upgrade` and `uninstall`, reads its payload from the registry again, while the kubelet fetches an image package's layers once per node. Check stages and interrupts reuse the copy already laid out.
- **No multi-platform artifacts.** An artifact has one payload for every node, so an image whose `/skyhook-package` differs by platform cannot become one artifact.
- **Host-files layers and interrupt declarations** parse, but preflight rejects an artifact that has either, with `PackageArtifactRejected`, and the agent refuses one, until a later release supports them.

## What each mistake produces

Each reason below is one of the [specification's](package-artifact-spec.md#classification), and the operator names it in the `PackageArtifactRejected` condition, such as `tuning:1.0.0 (invalid-artifact: metadata-schema)`, and in the `Preflight` Warning on each node that rejected it.

| Mistake | Result |
|---|---|
| `UNINSTALL` empty, unset or misspelled | `metadata-schema`: preflight rejects the artifact with `PackageArtifactRejected` |
| Pushing what a failed command left, such as the empty `metadata.json` that `jq` leaves when `config.json` is missing | `descriptor`: preflight rejects the artifact with `PackageArtifactRejected` |
| A payload without `config.json`, or with one that is not JSON-equal to the metadata `config` | `config-mismatch`, found only when the agent lays the payload out: preflight accepts the artifact, and the stage fails |

See also [Package Artifact Design](../architecture/package-artifacts.md), the design behind how the operator and the agent read a package artifact.
