# skyhook

[![Pipeline Status](https://github.com/NVIDIA/skyhook/actions/workflows/operator-ci.yaml/badge.svg)](https://github.com/NVIDIA/skyhook/actions/workflows/operator-ci.yaml)
[![Coverage Status](https://coveralls.io/repos/github/NVIDIA/skyhook/badge.svg)](https://coveralls.io/github/NVIDIA/skyhook)
[![Go Report Card](https://goreportcard.com/badge/github.com/NVIDIA/skyhook/operator)](https://goreportcard.com/report/github.com/NVIDIA/skyhook/operator)

**Skyhook** is a Kubernetes-aware package manager for cluster administrators to safely modify and maintain underlying host declaratively at scale.

## Why Skyhook?

Managing and updating Kubernetes clusters is challenging. While Kubernetes advocates treating compute as disposable, but certain scenarios make this difficult:

- **Updating hosts without re-imaging:**
  - Limited excess hardware/capacity for rolling replacements
  - Long node replacement times (example can be hours in some cloud providers)
- **OS image management:**
  - Maintain a common base image with workload-specific overlays instead of multiple OS images
- **Workload sensitivity:**
  - Some workloads can't be moved, are difficult to move, or take a long time to migrate

## What is Skyhook?

Skyhook functions like a package manager but for your entire Kubernetes cluster, with three main components:

1. **Skyhook Operator** - Manages installing, updating, and removing packages
2. **Skyhook Custom Resource (SCR)** - Declarative definitions of changes to apply
3. **Packages** - The actual modifications you want to implement

## Where and When to use Skyhook

Skyhook works in any Kubernetes environment (self-managed, on-prem, cloud) and shines when you need:

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
- [**Runtime Required**](docs/runtime_required.md): requires node to come into the cluster with a taint, and will do work prior to removing custom taint.
- **Resource Management:** Skyhook uses Kubernetes [LimitRange](https://kubernetes.io/docs/concepts/policy/limit-range/) to set default CPU and memory requests/limits for all containers in its namespace. You can override these defaults per-package in your Skyhook CR. Strict validation is enforced: if you set any resource override, you must set all four fields (cpuRequest, cpuLimit, memoryRequest, memoryLimit), and limits must be >= requests. See [docs/resource_management.md](docs/resource_management.md) for details and examples.

## Pre-built Packages

There are a few pre-built generalist packages available at [NVIDIA/skyhook-packages](https://github.com/NVIDIA/skyhook-packages)


## Installation via Helm

Install Skyhook quickly using Helm without downloading the repository:

### Prerequisites
- Kubernetes cluster (tested on v1.30+)
- Helm 3.x installed
- Container registry access credentials (if using private registries)

### Install Skyhook

```bash
# Add the NVIDIA Helm repository
helm repo add skyhook https://helm.ngc.nvidia.com/nvidia/skyhook
helm repo update
helm search repo skyhook ## should show the latest version

# basic install
helm install skyhook skyhook/skyhook-operator \
  --version v0.14.0 \
  --namespace skyhook \
  --create-namespace
```

### Configure Image Pull Secrets (if needed)

If you're using private container registries, create the necessary secrets:

```bash
kubectl create secret generic node-init-secret \
  --from-file=.dockerconfigjson=${HOME}/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  --namespace skyhook
```

**Note:** Skyhook currently uses a single shared image pull secret for all packages, and agent/operator containers. If you need access to multiple registries, combine the credentials into one `dockerconfigjson` secret with multiple registry auths.

### Verify Installation

```bash
# Check that the operator is running
kubectl get pods -n skyhook

# or Wait for the deployment to be available first
kubectl wait --for=condition=Available deployment -l control-plane=controller-manager -n skyhook --timeout=300s

# Then wait for the operator pod to be ready
kubectl wait --for=condition=Ready pod -l control-plane=controller-manager -n skyhook --timeout=300s

# Verify the Ready condition
kubectl get pods -l control-plane=controller-manager -n skyhook -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}'

# Verify the CRDs are installed
kubectl get crd | grep skyhook

# Verify packages are working
kubectl apply -f - <<EOF
apiVersion: skyhook.nvidia.com/v1alpha1
kind: Skyhook
metadata:
  name: skyhook-sample
spec:
  nodeSelectors:
    matchExpressions:
      - key: node-role.kubernetes.io/control-plane
        operator: DoesNotExist
  packages:
    something-important:
      version: 1.1.1
      image: ghcr.io/nvidia/skyhook-packages/shellscript
      configMap:
        apply.sh: |-
          #!/bin/bash
          echo "hello world" > /skyhook-hello-world
          sleep 10
        apply_check.sh: |-
          #!/bin/bash
                     cat /skyhook-hello-world | wc -l | grep -q 1
           sleep 10
EOF

# Wait for the Skyhook to complete
kubectl wait --for=jsonpath='{.status.status}'=complete skyhook/skyhook-sample --timeout=300s

# Check the status
kubectl describe skyhook skyhook-sample
```

### Uninstalling

**Automatic Cleanup (Default):** By default, the Helm chart includes a pre-delete hook that automatically cleans up all Skyhook and DeploymentPolicy resources before uninstalling:

```bash
# Uninstall the chart (cleanup happens automatically)
helm uninstall skyhook --namespace skyhook
```

The pre-delete hook will:
- Delete all Skyhook resources
- Delete all DeploymentPolicy resources  
- Complete quickly if no resources exist
- Wait for finalizers to be processed if resources exist
- Proceed with uninstall even if cleanup times out (job deadline: 2 minutes)

**Configuration Options:**

To disable automatic cleanup and manage resources manually:

```bash
helm install skyhook ./chart --namespace skyhook --set cleanup.enabled=false
```

To adjust the job timeout:

```bash
helm install skyhook ./chart --namespace skyhook \
  --set cleanup.jobTimeoutSeconds=180
```

**Manual Cleanup (if needed):**

If you disabled automatic cleanup or need to clean up resources manually:

```bash
# Delete all Skyhook resources first
kubectl delete skyhooks --all

# Delete all DeploymentPolicy resources
kubectl delete deploymentpolicies --all

# Then uninstall the chart
helm uninstall skyhook --namespace skyhook
```

**Why cleanup matters:** If you uninstall while Skyhook CRs with finalizers still exist, it can leave resources in a broken state that may cause reinstall issues.

## Monitoring and Troubleshooting

### Watch Skyhook apply packages
```
kubectl get pods -w -n skyhook
```
There will be a pod for each lifecycle stage (apply, config, etc.) per package per node matching the selector.

### Check Skyhook resource status
```bash
# Check overall status
kubectl get skyhooks

# Get detailed status of a specific Skyhook
kubectl describe skyhook <skyhook-name>
```
The Status will show the overall package status as well as the status of each node

### Check node annotations for package state
```bash
# View node state annotations for a specific Skyhook
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.metadata.annotations.skyhook\.nvidia\.com/nodeState_<skyhook-name>}{"\n"}{end}'
```

### Stages
The operator will apply steps in a package throughout different lifecycle stages. This ensures that the right steps are applied in the right situations and in the correct order.
- Upgrade: This stage will be ran whenever a package's version is upgraded in the SCR.
- Uninstall: This stage will be ran whenever a package's version is downgraded or it's removed from the SCR.
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

**NOTE**: If a package is removed from the SCR, then the uninstall stage for that package will solely be run.

**Semantic versioning is strictly enforced in the operator** in order to support upgrade and uninstall. Semantic versioning allows the
operator to know which way the package is going while also enforcing best versioning practices.

**For detailed information about our versioning strategy, git tagging conventions, and component release process, see [docs/versioning.md](docs/versioning.md) and [docs/release-process.md](docs/release-process.md).**

**For definitions of Status, State, and Stage concepts used throughout the operator, see [docs/operator-status-definitions.md](docs/operator-status-definitions.md).**

## Packages
Part of how the operator works is the [skyhook-agent](agent/README.md). Packages have to be created in way so the operator knows how to use them. This is where the agent comes into play, more on that later. A package is a container that meets these requirements:

- Container shall have `bash`, so needs to be at least something like busybox/alpine
- Config that is valid, jsonschema is used to valid this config. The agent has a tool build in to valid the config. This tool should be used to test packages before publishing.
- The file system structure needs to adhere to:
```
/skyhook-package
├── skyhook_dir/{steps}
├── root_dir/{static files}
└── config.json
```

## Examples

See the [examples/](examples/) directory for sample manifests, usage patterns, and demo configurations to help you get started with Skyhook.

## Kyverno Policy Examples

See [docs/kyverno/README.md](docs/kyverno/README.md) for example Kyverno policies and guidance on restricting images or packages in Skyhook resources.

## [Skyhook-Operator](operator/README.md)
The operator is a kbuernetes operator that monitors cluster events and coordinates the installation and lifecycle of Skyhook packages.

## [Skyhook Agent](agent/README.md)
The agent is what does the operators work and is a separate container from the package. The agent knowns how to read a package (/skyhook_package/config.json) is what implements the [lifecycle](#stages) packages go though.

## [Skyhook CLI](docs/cli.md)
A kubectl plugin for managing Skyhook deployments, packages, and nodes. Provides SRE tooling for inspecting node/package state, forcing re-runs, managing node lifecycle, and retrieving logs.

### Quick Install
```bash
# Build from source
make build-cli

# Install as kubectl plugin
cp bin/kubectl-skyhook /usr/local/bin/

# Verify installation
kubectl skyhook version
```

See the [full CLI documentation](docs/cli.md) for detailed usage and examples.

