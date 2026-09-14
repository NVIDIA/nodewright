# NodeWright (formerly Skyhook)

[![Pipeline Status](https://github.com/NVIDIA/nodewright/actions/workflows/operator-ci.yaml/badge.svg)](https://github.com/NVIDIA/nodewright/actions/workflows/operator-ci.yaml)
[![Coverage Status](https://coveralls.io/repos/github/NVIDIA/nodewright/badge.svg)](https://coveralls.io/github/NVIDIA/nodewright)

**NodeWright** is a Kubernetes-aware package manager for cluster administrators to safely modify and maintain underlying host declaratively at scale.

**New here?** Start with the **[Quickstart](docs/getting-started/quickstart.md)** to install NodeWright and run a package on one node in about five minutes, or the **[Overview](docs/getting-started/overview.md)** for what NodeWright is and when to reach for it. Full docs live in [`docs/`](docs/README.md).

> **Note:** NodeWright is being renamed from Skyhook, and the rename has now landed for the core surfaces. The Helm chart, operator image, CLI (`kubectl nodewright`), and the CRDs (`nodewright.nvidia.com/v1alpha1`, Kind `NodeWright`; `DeploymentPolicy` moves to the same group) are published under `nodewright`. Existing `skyhook.nvidia.com`/`Skyhook` resources keep working during the transition: the operator auto-imports them to NodeWright and preserves per-node state (no package re-run), and legacy writes emit a deprecation warning. See the [migration guide](docs/getting-started/migration.md).
>
> **Distribution change (v0.16.0+):** NodeWright is now distributed exclusively through GitHub Container Registry (`ghcr.io`) — both the container images and the Helm chart (as an OCI artifact). Publication to `nvcr.io` / the NGC Helm repository (`helm.ngc.nvidia.com`) is paused and is planned to return in a future release. **Existing users installing from NGC need to switch to the OCI install below.** See [Distribution: ghcr.io only (for now)](docs/contributing/release-process.md#distribution-ghcrio-only-for-now) for the full story.

## Why NodeWright?

Managing and updating Kubernetes clusters is challenging. While Kubernetes advocates treating compute as disposable, but certain scenarios make this difficult:

- **Updating hosts without re-imaging:**
  - Limited excess hardware/capacity for rolling replacements
  - Long node replacement times (example can be hours in some cloud providers)
- **OS image management:**
  - Maintain a common base image with workload-specific overlays instead of multiple OS images
- **Workload sensitivity:**
  - Some workloads can't be moved, are difficult to move, or take a long time to migrate

## What is NodeWright?

NodeWright functions like a package manager but for your entire Kubernetes cluster, with three main components:

1. **NodeWright Operator** - Manages installing, updating, and removing packages
2. **NodeWright Custom Resource** - Declarative definitions of changes to apply
3. **Packages** - The actual modifications you want to implement

## Where and When to use NodeWright

NodeWright works in any Kubernetes environment (self-managed, on-prem, cloud) and shines when you need:

- Kubernetes-aware scheduling that protects important workloads
- Rolling or simultaneous updates across your cluster
- Declarative configuration management for host-level changes

## Benefits

 - **Native Kubernetes integration** - Packages are standard Kubernetes resources compatible with GitOps tools like ArgoCD, Helm, and Flux
 - **Autoscaling support** - Ensure newly created nodes are properly configured before schedulable
 - **First-class upgrades** - Deploys changes with minimal disruption, waiting for running workloads to complete when needed

## Key Features

- **Interruption Budget:** percent of nodes or count
- **Node Selectors:** selectors for which nodes to apply too (node labels)
- **Pod Non Interrupt Labels:**  labels for pods to **never** interrupt
- **Package Interrupt:** service (containerd, cron, any thing systemd), or reboot
- **Additional Tolerations:**  are tolerations added to the packages
- [**Runtime Required**](docs/user-guide/runtime-required.md): requires node to come into the cluster with a taint, and will do work prior to removing custom taint.
- **Resource Management:** NodeWright uses Kubernetes [LimitRange](https://kubernetes.io/docs/concepts/policy/limit-range/) to set default CPU and memory requests/limits for all containers in its namespace. You can override these defaults per-package in your NodeWright CR. Strict validation is enforced: if you set any resource override, you must set all four fields (cpuRequest, cpuLimit, memoryRequest, memoryLimit), and limits must be >= requests. See [docs/operations/resource-management.md](docs/operations/resource-management.md) for details and examples.
- [**Explicit Uninstall**](docs/user-guide/uninstall.md): controlled, explicit uninstall of packages from nodes with `uninstall.enabled` and `uninstall.apply` fields, webhook guards, finalizer-driven cleanup on CR deletion, and cancel support.

## Pre-built Packages

There are a few pre-built generalist packages available at [NVIDIA/nodewright-packages](https://github.com/NVIDIA/nodewright-packages)

## Installation

```bash
# Replace <chart-version> with a published chart version from
# https://github.com/NVIDIA/nodewright/pkgs/container/nodewright%2Fcharts%2Fnodewright
helm install nodewright oci://ghcr.io/nvidia/nodewright/charts/nodewright \
  --version <chart-version> \
  --namespace nodewright \
  --create-namespace
```

Then run a package on one node with the [Quickstart](docs/getting-started/quickstart.md).

Image pull secrets, private registries, cleanup options, and uninstall are all
covered in [Installation](docs/getting-started/installation.md).

## Monitoring and Troubleshooting

### Watch NodeWright apply packages

```
kubectl get pods -w -n nodewright
```
There will be a pod for each lifecycle stage (apply, config, etc.) per package per node matching the selector.

### Check NodeWright resource status

```bash
# Check overall status
kubectl get nodewrights

# Get detailed status of a specific NodeWright
kubectl describe nodewright <nodewright-name>
```
The Status will show the overall package status as well as the status of each node

### Check node annotations for package state

```bash
# View node state annotations for a specific NodeWright
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.metadata.annotations.nodewright\.nvidia\.com/nodeState_<nodewright-name>}{"\n"}{end}'
```

### Stages

The operator will apply steps in a package throughout different lifecycle stages. This ensures that the right steps are applied in the right situations and in the correct order.

- Upgrade: This stage runs whenever a package's version is upgraded in the NodeWright CR.
- Uninstall: This stage runs only when explicitly requested — either by setting `uninstall.apply: true` on a package with `uninstall.enabled: true`, or during NodeWright CR deletion (finalizer-driven) for `uninstall.enabled: true` packages. See [Explicit Uninstall](docs/user-guide/uninstall.md).
- Apply: This stage will always be ran at least once.
- Config: This stage will run when a configmap is changed and on the first SCR application.
- Interrupt: This stage will run when a package has an interrupt defined or a key's value in a packages configmap changes which has a config interrupt defined.
- Post-Interrupt: This stage will run when a package's interrupt has finished.

The stages are applied in this order:

**Without Interrupts:**

- Uninstall -> Apply -> Config (No Upgrade)
- Upgrade -> Config (With Upgrade)

**With Interrupts:**
For packages that require interrupts, the node is first cordoned and drained to ensure workloads are safely evacuated before package operations begin:

- Uninstall -> Apply -> Config -> Interrupt -> Post-Interrupt (No Upgrade)
- Upgrade -> Config -> Interrupt -> Post-Interrupt (With Upgrade)

This ensures that when operations like kernel module unloading or system reboots are required, they happen after workloads have been safely removed and any necessary pre-interrupt package operations have completed.

**NOTE**: Uninstall is explicit. Removing a package from the SCR does **not** automatically run the uninstall stage. For packages with `uninstall.enabled: true`, the webhook rejects removal until `uninstall.apply: true` has been set and the uninstall has completed on all nodes; packages with `uninstall.enabled: false` (or unset) can be removed without an uninstall pod, and their prior node state is preserved as a marker. See [Explicit Uninstall](docs/user-guide/uninstall.md).

**Semantic versioning is strictly enforced in the operator** in order to support upgrade and uninstall. Semantic versioning allows the
operator to know which way the package is going while also enforcing best versioning practices.

**For detailed information about our versioning strategy, git tagging conventions, and component release process, see [docs/operations/versioning.md](docs/operations/versioning.md) and [docs/contributing/release-process.md](docs/contributing/release-process.md).**

**For definitions of Status, State, and Stage concepts used throughout the operator, see [docs/architecture/operator-status.md](docs/architecture/operator-status.md).**

## Packages

Part of how the operator works is the [NodeWright agent](agent/README.md). Packages have to be created in way so the operator knows how to use them. This is where the agent comes into play, more on that later. A package is a container that meets these requirements:

- Container shall have `bash`, so needs to be at least something like busybox/alpine
- Config that is valid, jsonschema is used to valid this config. The agent has a tool build in to valid the config. This tool should be used to test packages before publishing.
- The file system structure needs to adhere to:

```
/skyhook-package
├── skyhook_dir/{steps}
├── root_dir/{static files}
└── config.json
```

When a NodeWright's `configMap` is set, the operator projects each key as an individual file at `/skyhook-package/configmaps/<key>`. These files overlay any files the package image baked in under that directory rather than replacing the whole directory: a package can ship default files there and a user's `configMap` overrides only the keys it supplies. (Because the files are mounted via `subPath`, live ConfigMap edits are not propagated into a running pod, but package pods are recreated per stage and on version bumps.)

## Examples

See the [examples/](examples/) directory for sample manifests, usage patterns, and demo configurations to help you get started with NodeWright.

## Kyverno Policy Examples

See [docs/security/kyverno/README.md](docs/security/kyverno/README.md) for example Kyverno policies and guidance on restricting images or packages in NodeWright resources.

## [NodeWright Operator](operator/README.md)

The operator is a Kubernetes operator that monitors cluster events and coordinates the installation and lifecycle of NodeWright packages.

## [NodeWright Agent](agent/README.md)

The agent is what does the operator's work and is a separate container from the package. The agent knows how to read a package (/skyhook_package/config.json) and implements the [lifecycle](#stages) packages go through.

## [NodeWright CLI](docs/user-guide/cli.md)

A kubectl plugin for managing NodeWright deployments, packages, and nodes. Provides SRE tooling for inspecting node/package state, forcing re-runs, managing node lifecycle, and retrieving logs.

### Quick Install

```bash
# Build from source
cd operator
make build-cli

# Install as kubectl plugin
cp bin/nodewright /usr/local/bin/kubectl-nodewright # or another directory in $PATH with write access

# Verify installation
kubectl nodewright version
```

See the [full CLI documentation](docs/user-guide/cli.md) for detailed usage and examples.

## Contributing

- Start here: [CONTRIBUTING.md](CONTRIBUTING.md)
- Code of Conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

## Governance & Maintainers

- Governance: [GOVERNANCE.md](GOVERNANCE.md) - roles, how decisions get made, how maintainers join and leave
- Maintainers: [MAINTAINERS.md](MAINTAINERS.md) - current roster and how to become one
- Review ownership: [`.github/CODEOWNERS`](.github/CODEOWNERS)

## Security

Please report security vulnerabilities through [NVIDIA's Security Vulnerability Form](https://www.nvidia.com/object/submit-security-vulnerability.html). Do **not** file public GitHub issues for security reports. See [SECURITY.md](SECURITY.md) for details.

## Support

- **Support Level:** Maintained
- **How to get help:** [GitHub Issues](https://github.com/NVIDIA/nodewright/issues)
- See [SUPPORT.md](SUPPORT.md) for more details.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
