# Releases

This page is the archive of every NodeWright release, formerly Skyhook, and where its artifacts were published. Artifacts have moved over the project's history, from Skyhook paths on GHCR and NGC to the NodeWright paths, and older locations are frozen rather than deleted, so each version below names the exact locations that still serve it.

- **Cutting a release**, from the release branch through the release candidates to the final tags: [NodeWright Release Process](release-process.md).
- **How versions are numbered, when releases happen and what an older version is supported for**: [Releasing NodeWright](../../RELEASE.md).
- **Each location and the version ranges it holds**, with the history of how they changed: [Artifact Locations](../operations/artifact-locations.md).

The tables list final releases only. Release candidates are published to the same current locations as their component's final releases and are listed on the [GitHub Releases](https://github.com/NVIDIA/nodewright/releases) page as pre-releases. Releases are also staged in NGC's private registry for promotion to the public NGC catalog; those staging copies are not meant to be pulled from and are not listed. A **Released** date links to the version's GitHub Release where one exists.

Each table has one column per location, and a check mark means that location serves the version. The tables were built from the registries themselves, not from the documented ranges. The `nvcr.io/nvidia/nodewright` and `helm.ngc.nvidia.com/nvidia/nodewright` columns are the public NGC locations NodeWright is moving to; they fill in as releases are promoted from NGC staging. When you cut a release, add its row here, and tick a promoted location once its release appears there.

## Operator

| Column | Location |
| --- | --- |
| ghcr skyhook | `ghcr.io/nvidia/skyhook/operator` |
| nvcr skyhook | `nvcr.io/nvidia/skyhook/operator` |
| ghcr nodewright | `ghcr.io/nvidia/nodewright/operator` |
| nvcr nodewright | `nvcr.io/nvidia/nodewright/operator` (public NGC catalog; the first release is pending promotion) |

| Version | Released | ghcr skyhook | nvcr skyhook | ghcr nodewright | nvcr nodewright |
| --- | --- | :---: | :---: | :---: | :---: |
| v0.21.0 | [2026-11-06](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.21.0) |  |  | ✓ |  |
| v0.20.0 | [2026-10-07](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.20.0) |  |  | ✓ |  |
| v0.19.1 | [2026-10-05](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.19.1) |  |  | ✓ |  |
| v0.19.0 | [2026-08-31](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.19.0) |  |  | ✓ |  |
| v0.18.0 | [2026-08-17](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.18.0) |  |  | ✓ |  |
| v0.17.0 | [2026-06-12](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.17.0) |  |  | ✓ |  |
| v0.16.1 | [2026-05-22](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.16.1) |  |  | ✓ |  |
| v0.16.0 | [2026-05-22](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.16.0) |  |  | ✓ |  |
| v0.15.0 | [2026-04-06](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.15.0) |  | ✓ | ✓ |  |
| v0.14.0 | 2026-03-10 | ✓ | ✓ |  |  |
| v0.13.0 | 2026-03-03 | ✓ | ✓ |  |  |
| v0.12.1 | [2026-02-10](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.12.1) | ✓ | ✓ |  |  |
| v0.12.0 | [2026-02-06](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.12.0) | ✓ | ✓ |  |  |
| v0.11.1 | [2026-01-13](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.11.1) | ✓ | ✓ |  |  |
| v0.11.0 | [2025-12-30](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.11.0) | ✓ | ✓ |  |  |
| v0.10.0 | [2025-12-04](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.10.0) | ✓ | ✓ |  |  |
| v0.9.0 | [2025-08-08](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.9.0) | ✓ | ✓ |  |  |
| v0.8.0 | [2025-06-06](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.8.0) | ✓ | ✓ |  |  |
| v0.7.6 | [2025-03-19](https://github.com/NVIDIA/nodewright/releases/tag/operator/v0.7.6) | ✓ | ✓ |  |  |
| v0.7.5 | 2025-02-28 | ✓ | ✓ |  |  |
| v0.0.0 | 2025-02-20 | ✓ |  |  |  |

## Agent

| Column | Location |
| --- | --- |
| ghcr skyhook | `ghcr.io/nvidia/skyhook/agent` |
| nvcr skyhook | `nvcr.io/nvidia/skyhook/agent` |
| ghcr nodewright | `ghcr.io/nvidia/nodewright/agent` |
| nvcr nodewright | `nvcr.io/nvidia/nodewright/agent` (public NGC catalog; the first release is pending promotion) |

| Version | Released | ghcr skyhook | nvcr skyhook | ghcr nodewright | nvcr nodewright |
| --- | --- | :---: | :---: | :---: | :---: |
| v7.0.0 | [2026-10-05](https://github.com/NVIDIA/nodewright/releases/tag/agent/v7.0.0) |  |  | ✓ |  |
| v6.4.2 | [2026-05-22](https://github.com/NVIDIA/nodewright/releases/tag/agent/v6.4.2) |  |  | ✓ |  |
| v6.4.1 | 2026-03-04 | ✓ | ✓ |  |  |
| v6.4.0 | [2026-02-06](https://github.com/NVIDIA/nodewright/releases/tag/agent/v6.4.0) | ✓ | ✓ |  |  |
| v6.3.1 | [2025-08-25](https://github.com/NVIDIA/nodewright/releases/tag/agent/v6.3.1) | ✓ | ✓ |  |  |
| v6.3.0 | [2025-07-30](https://github.com/NVIDIA/nodewright/releases/tag/agent/v6.3.0) | ✓ | ✓ |  |  |
| v6.2.0 | [2025-06-06](https://github.com/NVIDIA/nodewright/releases/tag/agent/v6.2.0) | ✓ |  |  |  |
| v6.1.5 | [2025-03-31](https://github.com/NVIDIA/nodewright/releases/tag/agent/v6.1.5) | ✓ |  |  |  |
| v6.1.4 | 2025-02-28 | ✓ | ✓ |  |  |
| v0.0.1 | 2025-02-21 | ✓ |  |  |  |

## Helm chart

| Column | Location |
| --- | --- |
| NGC Helm `skyhook-operator` | NGC Helm repository `https://helm.ngc.nvidia.com/nvidia/skyhook`, chart `skyhook-operator` |
| ghcr OCI `nodewright` | `oci://ghcr.io/nvidia/nodewright/charts/nodewright` |
| NGC Helm `nodewright` | NGC Helm repository `https://helm.ngc.nvidia.com/nvidia/nodewright`, chart `nodewright` (public NGC catalog; the first release is pending promotion) |
| nvcr OCI `nodewright` | `oci://nvcr.io/nvidia/nodewright/nodewright` (public NGC catalog; the first release is pending promotion) |

| Version | Released | NGC Helm `skyhook-operator` | ghcr OCI `nodewright` | NGC Helm `nodewright` | nvcr OCI `nodewright` |
| --- | --- | :---: | :---: | :---: | :---: |
| v0.20.0 | [2026-10-07](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.20.0) |  | ✓ |  |  |
| v0.19.1 | [2026-10-05](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.19.1) |  | ✓ |  |  |
| v0.19.0 | [2026-09-04](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.19.0) |  | ✓ |  |  |
| v0.18.0 | [2026-08-17](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.18.0) |  | ✓ |  |  |
| v0.17.1 | [2026-06-26](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.17.1) |  | ✓ |  |  |
| v0.17.0 | [2026-06-12](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.17.0) |  | ✓ |  |  |
| v0.16.1 | [2026-05-26](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.16.1) |  | ✓ |  |  |
| v0.16.0 | [2026-05-22](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.16.0) |  | ✓ |  |  |
| v0.15.1 | [2026-04-14](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.15.1) | ✓ |  |  |  |
| v0.15.0 | [2026-04-06](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.15.0) | ✓ |  |  |  |
| v0.14.0 | [2026-03-10](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.14.0) | ✓ |  |  |  |
| v0.13.1 | [2026-03-05](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.13.1) | ✓ |  |  |  |
| v0.13.0 | [2026-03-04](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.13.0) | ✓ |  |  |  |
| v0.12.1 | [2026-03-03](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.12.1) | ✓ |  |  |  |
| v0.12.0 | [2026-02-07](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.12.0) | ✓ |  |  |  |
| v0.11.1 | [2026-01-13](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.11.1) | ✓ |  |  |  |
| v0.11.0 | [2025-12-30](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.11.0) | ✓ |  |  |  |
| v0.10.1 | [2025-12-22](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.10.1) | ✓ |  |  |  |
| v0.10.0 | [2025-12-05](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.10.0) | ✓ |  |  |  |
| v0.9.2 | [2025-08-28](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.9.2) | ✓ |  |  |  |
| v0.9.1 | [2025-08-27](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.9.1) | ✓ |  |  |  |
| v0.9.0 | [2025-08-08](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.9.0) | ✓ |  |  |  |
| v0.8.1 | [2025-08-01](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.8.1) | ✓ |  |  |  |
| v0.8.0 | [2025-06-06](https://github.com/NVIDIA/nodewright/releases/tag/chart/v0.8.0) |  |  |  |  |
| v0.7.6 | 2025-04-11 (no git tag) | ✓ |  |  |  |

Chart v0.8.0 was never packaged: its GitHub Release carries source only, and no registry holds it. Chart v0.7.6 is in the NGC Helm repository but has no git tag, so its date comes from the repository index.

## CLI

| Column | Location |
| --- | --- |
| GitHub Release archives | Archives `<prefix>_<version>_<os>_<arch>` attached to the GitHub Release; the cell names the prefix |
| ghcr OCI | `ghcr.io/nvidia/nodewright/cli` |
| nvcr OCI | `nvcr.io/nvidia/nodewright/cli` (public NGC catalog; the first release is pending promotion) |

| Version | Released | GitHub Release archives | ghcr OCI | nvcr OCI |
| --- | --- | :---: | :---: | :---: |
| v0.5.0 | [2026-10-06](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.5.0) | `nodewright_` |  |  |
| v0.4.0 | [2026-08-31](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.4.0) | `nodewright_` |  |  |
| v0.3.0 | [2026-08-17](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.3.0) | `nodewright_` |  |  |
| v0.2.0 | [2026-06-12](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.2.0) | `skyhook_` |  |  |
| v0.1.2 | [2026-03-04](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.1.2) | `skyhook_` |  |  |
| v0.1.1 | [2026-01-13](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.1.1) | `skyhook_` |  |  |
| v0.1.0 | [2026-01-13](https://github.com/NVIDIA/nodewright/releases/tag/cli/v0.1.0) | `skyhook_` |  |  |
