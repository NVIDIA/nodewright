# Skyhook Documentation

This directory contains user and operator documentation for Skyhook. Here you'll find guides, examples, and reference material to help you deploy, configure, and secure Skyhook in your Kubernetes cluster.

## Available Documentation

- [Kyverno Policy Examples](kyverno/README.md):
  Example Kyverno policies for restricting images or packages in Skyhook resources.

- **Features**
  - [Providing Secrets to Packages](providing_secrets_to_packages.md):
    How to securely provide secrets to Skyhook-managed packages.

  - [Runtime Required](runtime_required.md):
    How to use the runtime required taint and feature in Skyhook.

  - [Interrupt Flow and Ordering](interrupt_flow.md):
    Detailed explanation of how Skyhook handles packages with interrupts, including the interrupt sequence.

  - [Strict Ordering](ordering_of_skyhooks.md): How and why the operator applies each Skyhook Custom Resource in a deterministic sequential order.

  - [Deployment Policy and Compartments](deployment_policy.md): 
    Fine-grained rollout control with compartments, budgets, and strategies. Includes overlap resolution, safety mechanisms, and migration from interruptionBudget.

- **Resources**
  - [Resource Management](resource_management.md):
  How Skyhook manages CPU/memory resources using LimitRange, per-package overrides, and validation rules.

  - [Operator Resources At Scale](operator_resources_at_scale.md): Considerations for how cpu and memory have to change for the Operator pods as cluster nodes and skyhook packages change.

  - [Operator Status Definitions](operator-status-definitions.md): Definitions of Status, State, Stage, and Condition concepts used throughout the Skyhook operator.

- **Process**
  - [Releases](releases.md):
      Release notes and upgrade information for Skyhook.
