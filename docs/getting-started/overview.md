# NodeWright Overview

**NodeWright** is a Kubernetes-aware package manager for cluster administrators to safely modify and maintain underlying hosts declaratively at scale.

## Why NodeWright?

Managing and updating Kubernetes clusters is challenging. While Kubernetes advocates treating compute as disposable, certain scenarios make this difficult:

- **Updating hosts without re-imaging:** Limited excess hardware/capacity for rolling replacements, or long node replacement times (can be hours in some cloud providers).
- **OS image management:** Maintain a common base image with workload-specific overlays instead of multiple OS images.
- **Workload sensitivity:** Some workloads can't be moved, are difficult to move, or take a long time to migrate.

## What is NodeWright?

NodeWright functions like a package manager but for your entire Kubernetes cluster, with three main components:

1. **NodeWright Operator** — Manages installing, updating, and removing packages.
2. **[NodeWright Custom Resource](../user-guide/custom-resource.md)** — Declarative definitions of changes to apply.
3. **Packages** — The actual modifications you want to implement.

NodeWright works in any Kubernetes environment (self-managed, on-prem, cloud) and shines when you need:

- Kubernetes-aware scheduling that protects important workloads
- Rolling or simultaneous updates across your cluster
- Declarative configuration management for host-level changes

## Benefits

- **Native Kubernetes integration** — Packages are standard Kubernetes resources compatible with GitOps tools like ArgoCD, Helm, and Flux.
- **Autoscaling support** — Ensure newly created nodes are properly configured before schedulable.
- **First-class upgrades** — Deploys changes with minimal disruption, waiting for running workloads to complete when needed.

## Key Features

- **Interruption Budget:** percent of nodes or count
- **Node Selectors:** selectors for which nodes to apply to (node labels)
- **Pod Non Interrupt Labels:** labels for pods to **never** interrupt
- **Package Interrupt:** service (containerd, cron, anything systemd), or reboot
- **Additional Tolerations:** tolerations added to the packages
- [**Runtime Required**](../user-guide/runtime-required.md): requires node to come into the cluster with a taint, and will do work prior to removing custom taint
- [**Resource Management**](../operations/resource-management.md): CPU/memory resources using LimitRange, per-package overrides, and validation rules
- [**Explicit Uninstall**](../user-guide/uninstall.md): controlled uninstall of packages from nodes with webhook guards, finalizer-driven cleanup, and cancel support

## Pre-built Packages

There are pre-built generalist packages available at [NVIDIA/nodewright-packages](https://github.com/NVIDIA/nodewright-packages).

## Next Steps

- [Installation](installation.md) — Install NodeWright via Helm
- [Migration from Skyhook](migration.md) — Transition guide from Skyhook to NodeWright
- [Custom Resource Reference](../user-guide/custom-resource.md) — Every field on the NodeWright CR
- [CLI Reference](../user-guide/cli.md) — `kubectl nodewright` commands
