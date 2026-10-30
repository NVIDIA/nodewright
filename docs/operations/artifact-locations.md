# Artifact Locations

NodeWright, formerly Skyhook, has published its images, Helm chart and CLI to several places over its history: NGC and GitHub Container Registry in parallel, then GitHub Container Registry only, then a rename that moved every GitHub-hosted path. This page lists where each artifact lives today and everywhere it has lived before, so you can find an older version, tell where an image you are running came from, or update a pinned reference.

Older locations are **frozen, not deleted**: they still serve the versions they hold, but receive nothing new. Install new versions from the current locations.

## Where to get NodeWright today

| Artifact | Location |
| --- | --- |
| Operator image | `ghcr.io/nvidia/nodewright/operator` |
| Agent image | `ghcr.io/nvidia/nodewright/agent` |
| Helm chart (OCI) | `oci://ghcr.io/nvidia/nodewright/charts/nodewright` |
| CLI (`kubectl nodewright`) | `ghcr.io/nvidia/nodewright/cli` (OCI artifact, one binary per platform), and [GitHub Releases](https://github.com/NVIDIA/nodewright/releases) `cli/*` tags, archives named `nodewright_<version>_<os>_<arch>` |

The chart's defaults in `chart/values.yaml` pull the images from `ghcr.io`, pinned by digest. Release signatures, SBOMs and provenance are attached to the GHCR images; see [Verify release signatures and attestations](../contributing/release-process.md#verify-release-signatures-and-attestations).

Releases are also staged to NGC: operator and agent images and the CLI artifact are copied from GHCR to NGC's private registry with the same digests, and the chart package is uploaded to an NGC Helm repository. NVIDIA's internal release process promotes them from there to the public `nvcr.io/nvidia` catalog and NGC Helm repository. The staging locations are not meant to be pulled from directly; see [Distribution: ghcr.io, staged to NGC](../contributing/release-process.md#distribution-ghcrio-staged-to-ngc).

## Operator image

| Location | Versions | Status |
| --- | --- | --- |
| `ghcr.io/nvidia/skyhook/operator` | v0.0.0, v0.7.5 to v0.14.0 | Frozen; `latest` is v0.14.0 |
| `nvcr.io/nvidia/skyhook/operator` | v0.7.5 to v0.15.0 (v0.7.5, v0.7.6, v0.8.0, v0.9.0, v0.10.0, v0.11.0, v0.11.1, v0.12.0, v0.12.1, v0.13.0, v0.14.0, v0.15.0) | Frozen; the chart default from chart v0.8.1 to v0.15.1 |
| `ghcr.io/nvidia/nodewright/operator` | v0.15.0 onward | **Current** |

## Agent image

The agent was a Python program through v6.x and was rewritten in Go for v7.0.0. The image path did not change with the rewrite.

| Location | Versions | Status |
| --- | --- | --- |
| `ghcr.io/nvidia/skyhook/agent` | v0.0.1, v6.1.4 to v6.4.1 | Frozen; `latest` is v6.4.1 |
| `nvcr.io/nvidia/skyhook/agent` | v6.1.4, v6.3.0, v6.3.1, v6.4.0, v6.4.1 | Frozen; the chart default from chart v0.8.1 to v0.15.1 |
| `ghcr.io/nvidia/nodewright/agent` | v6.4.2 (the last Python agent) onward, including v7.0.0 | **Current** |

## Helm chart

| Location | Chart name | Versions | Status |
| --- | --- | --- | --- |
| NGC Helm repository `https://helm.ngc.nvidia.com/nvidia/skyhook` | `skyhook-operator` | v0.7.6, v0.8.1 to v0.15.1 | Frozen; NGC does not allow chart versions to be deleted |
| `oci://ghcr.io/nvidia/nodewright/charts/nodewright` | `nodewright` | v0.16.0 onward | **Current** |

Which registry a chart version pulls its images from by default:

| Chart versions | Default image registry |
| --- | --- |
| v0.8.0 | `ghcr.io/nvidia/skyhook/{operator,agent}` |
| v0.8.1 to v0.15.1 | `nvcr.io/nvidia/skyhook/{operator,agent}` |
| v0.16.0 onward | `ghcr.io/nvidia/nodewright/{operator,agent}` |

To move a release installed from the NGC Helm repository onto the OCI chart, see the migration note in [Installation](../getting-started/installation.md).

## CLI

The CLI is built for Linux, macOS and Windows on amd64 and arm64. Every release is attached to its GitHub Release as archives with a `checksums.txt`; releases after `cli/v0.5.0` are also pushed, as the same binaries, to `ghcr.io/nvidia/nodewright/cli:<version>`, an index with one artifact per platform whose single layer is `kubectl-nodewright` (`.exe` on Windows). Only the OCI artifact is signed and carries an SBOM and VEX; see [Installation](../user-guide/cli.md#installation).

| Releases | Archive names | Installed as |
| --- | --- | --- |
| cli/v0.1.0 to cli/v0.2.0 | `skyhook_<version>_<os>_<arch>` | `kubectl-skyhook` (`kubectl skyhook`) |
| cli/v0.3.0 onward | `nodewright_<version>_<os>_<arch>` | `kubectl-nodewright` (`kubectl nodewright`) |

## Agentless test image

The agentless image is a test double the operator's tests and `make run` use in place of the real agent. It is not a release artifact and is not meant for clusters.

| Location | Status |
| --- | --- |
| `ghcr.io/nvidia/skyhook/agentless` | Frozen |
| `ghcr.io/nvidia/nodewright/agentless` | **Current**; same tags, rebuilt |

## How the locations changed

1. **Open source (January 2025).** The repository was published as `NVIDIA/skyhook`, and CI started building images to `ghcr.io/nvidia/skyhook/*`.
2. **NGC alongside GHCR (operator v0.7.5, chart v0.7.6).** Releases were also published to `nvcr.io/nvidia/skyhook/*` and the chart to the NGC Helm repository, and from chart v0.8.1 the chart's defaults pointed at `nvcr.io`. GHCR kept receiving builds alongside.
3. **Repository rename (between operator v0.14.0 and v0.15.0).** The GitHub repository became `NVIDIA/nodewright`, and because GHCR paths follow the repository name, new images landed under `ghcr.io/nvidia/nodewright/*`. Operator v0.15.0 is therefore on `nvcr.io` and on the `nodewright` GHCR path, but not on the `skyhook` one.
4. **GHCR only (v0.16.0).** NGC publication paused. The chart was renamed from `skyhook-operator` to `nodewright`, moved to OCI on GHCR, and its defaults moved to `ghcr.io/nvidia/nodewright/*`. Installs from the NGC Helm repository have to switch to the OCI chart.
5. **CLI rename (cli/v0.3.0).** `kubectl skyhook` became `kubectl nodewright`.
6. **Go agent (agent v7.0.0, first pinned by chart v0.20.0).** Same image path; `ghcr.io/nvidia/nodewright/agent:v6.4.2` is the last Python agent.
7. **NGC staging.** Releases are staged to NGC again for promotion to the public NGC catalog: operator and agent images are copied from GHCR with the same digests, and the chart package, the same bytes pushed to GHCR, is uploaded to an NGC Helm repository.
8. **CLI on OCI.** After `cli/v0.5.0`, CLI releases are also pushed to `ghcr.io/nvidia/nodewright/cli`, signed, with an SBOM and VEX per platform, and copied with that evidence to NGC staging; `scripts/install-cli.sh` installs from GHCR.

## Things to watch for

- **The same tag on two registries can be a different image.** Before v0.16.0, the `nvcr.io` and `ghcr.io` images were built separately, so for example `operator:v0.15.0` has a different digest on each. When you compare or pin images from that period, pin the digest from the registry you actually pull from. The NGC staging copies are byte-identical to their GHCR originals.
- **Signatures and attestations exist only where they were published.** This repository's Sigstore signatures, SBOM, VEX and provenance attestations cover GHCR releases made after signing was introduced, and the NGC staging copies of them. The older `skyhook` paths were published before that, so verifying against them finds no evidence from this repository's workflows.
- **Versions with no published artifact.** chart v0.8.0 and chart/v0.15.1-test.1 have git tags but no published chart, and operator v0.16.0-rc3 has a GitHub pre-release but no image.
