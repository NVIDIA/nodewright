# NodeWright Documentation

This directory contains user and operator documentation for NodeWright. Here you'll find guides, examples, and reference material to help you deploy, configure, and secure NodeWright in your Kubernetes cluster.

## Getting Started

- [Overview](getting-started/overview.md): What NodeWright is and how it works.
- [Quickstart](getting-started/quickstart.md): Install NodeWright and run a hello-world package on one node.
- [Installation](getting-started/installation.md): Install NodeWright via Helm.
- [Migration from Skyhook](getting-started/migration.md): Transition guide from Skyhook to NodeWright.

## Architecture

- [Operator Status Definitions](architecture/operator-status.md): Definitions of Status, State, Stage, and Condition concepts used throughout the operator.
- [Lifecycle of a NodeWright](architecture/lifecycle.md): How a NodeWright moves from apply to complete, including the check step paired with every work step in a package's stages.
- [Interrupt Flow and Ordering](architecture/interrupt-flow.md): How NodeWright handles packages with interrupts, including the interrupt sequence.
- [Strict Ordering](architecture/ordering.md): How and why the operator applies each NodeWright Custom Resource in a deterministic sequential order.

## User Guide

- [CLI Reference](user-guide/cli.md): `kubectl nodewright` commands and usage.
- [The NodeWright Custom Resource](user-guide/custom-resource.md): Field-by-field reference for the CR, plus targeting with node selectors, deployment policies, and the ignore label.
- [Deployment Policy and Compartments](user-guide/deployment-policy.md): Fine-grained rollout control with compartments, budgets, and strategies.
- [Providing Secrets to Packages](user-guide/providing-secrets.md): How to securely provide secrets to NodeWright-managed packages.
- [Configuration Updates](user-guide/config-updates.md): How `configMap` changes are gated, propagated, and recovered.
- [Runtime Required](user-guide/runtime-required.md): How to use the runtime required taint and feature.
- [Taints](user-guide/taints.md): Taint management in NodeWright.
- [Uninstall](user-guide/uninstall.md): Controlled uninstall of packages from nodes.

## Operations

- [Kubernetes Support](operations/kubernetes-support.md): Supported Kubernetes versions and compatibility.
- [Resource Management](operations/resource-management.md): How NodeWright manages CPU/memory resources using LimitRange.
- [Operator Resources at Scale](operations/resources-at-scale.md): CPU and memory considerations as cluster and package counts change.
- [Versioning](operations/versioning.md): Version scheme and compatibility.

## Observability

- [Metrics](observability/metrics.md): Prometheus metrics, Grafana dashboards, and monitoring setup.

## Security

- [Kyverno Policy Examples](security/kyverno/README.md): Example Kyverno policies for restricting images or packages.
