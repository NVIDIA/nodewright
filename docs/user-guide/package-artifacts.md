# Publishing Package Artifacts

A NodeWright package can be published to an OCI registry as a package artifact instead of a container image. This page shows package authors how to build and push one, and how to convert an existing image package, and shows cluster operators how to turn artifact packages on. The [Package Artifact Specification](package-artifact-spec.md) defines the format, and [Package artifacts](../architecture/lifecycle.md#package-artifacts) describes what the operator does with one.

## Before you start

The package tree is exactly what a legacy package image carried under `/skyhook-package`:

- `config.json`, a regular file at its root, not a symlink.
- `skyhook_dir/`, the step scripts.
- `root_dir/`, the static files.

`configmaps/` and `node-metadata/` are overlaid at run time: the agent lays the package's configMap keys and the node metadata over the payload, as [Package artifacts](../../agent/README.md#package-artifacts) in the agent README describes, and a mounted file wins over a payload file of the same path.

The commands below need `tar`, `gzip`, `jq` and `oras`, and `crane` to convert an image package. CI runs them as this page writes them, with the `oras` and `crane` versions pinned in [`.github/actions/setup-oras`](../../.github/actions/setup-oras/action.yml) and [`.github/actions/setup-crane`](../../.github/actions/setup-crane/action.yml). They read these shell variables:

| Variable | Holds |
|---|---|
| `PACKAGE_DIR` | The path of the package tree |
| `UNINSTALL` | `supported` or `unsupported` |
| `ARTIFACT_REF` | The reference to push to, `registry/repository:version` |
| `IMAGE_REF` | The image package to convert |

Run them in order from a working directory outside the package tree, where they write `payload.tar.gz`, `metadata.json` and an OCI image layout, and stop at the first one that fails: each command reads what the one before it wrote.

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
mkdir "$PACKAGE_DIR"
crane export --platform linux/amd64 "$IMAGE_REF" - | tar -x -p -f - --strip-components=1 -C "$PACKAGE_DIR" skyhook-package
```

`crane export` writes the image's filesystem as one tar, and `tar` extracts only its `skyhook-package` directory into `PACKAGE_DIR`, keeping file modes. `mkdir` has no `-p`, so a `PACKAGE_DIR` left from an earlier attempt fails the conversion instead of merging stale files into the new tree. One artifact serves every node, so `--platform` picks the platform whose `/skyhook-package` becomes the package; name another one for an image published only for it.

Then [pack the payload](#pack-the-payload), [write the metadata](#write-the-metadata) and [push](#push-the-artifact) under a **new** version, and set the package's `version` in the NodeWright to it. Never push the artifact under the image's own tag. With package artifacts on, a tag that now names an artifact replaces the in-flight image Jobs of that version (see [Staleness](../architecture/lifecycle.md#staleness)), and on a cluster with the option off those Jobs fail at the image pull, because an artifact is not a runnable image.

## Re-push a tag

A Job already created keeps the reference it was built with, and an artifact Job is built with the digest its tag resolved to. New content pushed under an existing tag therefore reaches only the Jobs built after the operator resolves the tag again, which it does within 5 minutes (see [Caching](../architecture/lifecycle.md#caching) and [Staleness](../architecture/lifecycle.md#staleness)), so for a while nodes can run different content under one version.

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

The operator then inspects each package's reference at its registry before it builds the package's stage Job, which adds outbound HTTPS from the operator pod to every package registry.

**Prerequisite:** the operator's agent image and every package's `agentImageOverride` must be the Go agent, `agent/v7.0.0` or newer. With the option on, every interrupt Job ends with a container that runs `agent --version`, which the Python agent (v6.x) lacks, so those Jobs would never complete.

## Registry credentials

| Source | Operator inspection | Agent pull |
|---|---|---|
| The `.dockerconfigjson` of the `imagePullSecret` Secret (`IMAGE_PULL_SECRET`) | Yes | Yes, mounted at `NODEWRIGHT_REGISTRY_AUTH_FILE` |
| The node's Docker config files `/var/lib/kubelet/config.json`, `/root/.docker/config.json` and `/.docker/config.json` | No | Yes, after the Secret |
| Anonymous | When the Secret is unset or holds no credential for the registry | When no source holds a credential for the registry |

A registry that nodes authenticate to only through their own files therefore needs its credential in the Secret as well, or the operator cannot inspect its packages. [Registry credentials](../architecture/lifecycle.md#registry-credentials) has the details.

When the operator cannot inspect a reference, because the registry is unreachable, refuses its credentials, has no such reference or timed out, it builds the unchanged legacy Job and records a `PackageArtifactInspectionFailed` Warning. Such a reference is most likely an existing image package, which the kubelet can still pull with the node's credentials, and an artifact then fails visibly at the image pull. An uninstall instead waits until an inspection succeeds, with an `UninstallDeferred` Warning: whether it may run rests on what the artifact declares, and an uninstall cannot be undone. See [Deferred uninstall](uninstall.md#deferred-uninstall).

## Minimum versions

- **Operator:** the first operator release after `operator/v0.20.0`, and the first chart release after `chart/v0.20.0` for the `packageArtifacts` value.
- **Agent:** the first agent release after `agent/v7.0.0` for artifact packages, and `agent/v7.0.0` or newer for every agent image once the option is on.

The operator checks no agent version, so an agent that is too old shows only once its Jobs run: an artifact stage fails, or an interrupt Job never completes.

## Image and artifact packages side by side

A package's reference stays its `image` plus `version`, or plus `containerSHA`, and the NodeWright resource needs no change. The operator tells an artifact from an image by the manifest at that reference, so one NodeWright can hold both kinds of package. With the option off, the operator makes no registry request and runs every package as an image.

## Limitations

- **Credentials** come only from the sources above: no kubelet credential provider plugins, and no Docker credential helpers.
- **Registries** are reached at their own host: containerd registry mirrors and `hosts.toml` are not used.
- **HTTPS only.** A registry served over plain HTTP cannot hold artifact packages.
- **No multi-platform artifacts.** An artifact has one payload for every node, so an image whose `/skyhook-package` differs by platform cannot become one artifact.
- **Host-files layers and interrupt declarations** parse, but the operator rejects an artifact that has either, with `PackageArtifactRejected`, and the agent refuses one, until a later release supports them.

## What each mistake produces

Each reason below is one of the [specification's](package-artifact-spec.md#classification), and the operator names it in the `PackageArtifactRejected` condition and Warning, such as `tuning:1.0.0 (invalid-artifact: metadata-schema)`.

| Mistake | Result |
|---|---|
| `UNINSTALL` empty, unset or misspelled | `metadata-schema`: the operator rejects the artifact with `PackageArtifactRejected` |
| Pushing what a failed command left, such as the empty `metadata.json` that `jq` leaves when `config.json` is missing | `descriptor`: the operator rejects the artifact with `PackageArtifactRejected` |
| A payload without `config.json`, or with one that is not JSON-equal to the metadata `config` | `config-mismatch`, found only when the agent lays the payload out: the operator accepts the artifact, and the stage fails |

See also [Package Artifact Design](../architecture/package-artifacts.md), the design behind how the operator and the agent read a package artifact.
