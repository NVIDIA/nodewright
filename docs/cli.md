# Skyhook CLI

kubectl plugin for managing Skyhook deployments, packages, and nodes.

## Overview

The Skyhook CLI (`kubectl skyhook`) provides SRE tooling for managing Skyhook operators and their packages across Kubernetes cluster nodes. It supports inspecting node/package state, forcing re-runs, managing node lifecycle, and retrieving logs.

## Compatibility

### Minimum Operator Version

The CLI requires **operator version v0.8.0 or later** for full functionality of all commands.

### Command Compatibility Matrix

| Command | v0.7.x and earlier | v0.8.0+ |
|---------|-------------------|---------|
| `version` | ✅ Full | ✅ Full |
| `node status` | ✅ Full | ✅ Full |
| `node list` | ✅ Full | ✅ Full |
| `node reset` | ✅ Full | ✅ Full |
| `node ignore/unignore` | ✅ Full | ✅ Full |
| `package status` | ✅ Full | ✅ Full |
| `package rerun` | ✅ Full | ✅ Full |
| `package logs` | ✅ Full | ✅ Full |
| `reset` | ✅ Full | ✅ Full |
| `deployment-policy reset` | ❌ Not supported | ✅ Full |
| `pause` | ❌ Not supported | ✅ Full |
| `resume` | ❌ Not supported | ✅ Full |
| `disable` | ❌ Not supported | ✅ Full |
| `enable` | ❌ Not supported | ✅ Full |

### Breaking Change: Pause/Disable Mechanism

In operator versions **v0.7.x and earlier**, pausing and disabling a Skyhook was done via spec fields:

```yaml
spec:
  pause: true  # Old method - no longer used by operator
```

Starting with **v0.8.0**, the operator uses **annotations** instead:

```yaml
metadata:
  annotations:
    skyhook.nvidia.com/pause: "true"
    skyhook.nvidia.com/disable: "true"
```

The CLI's `pause`, `resume`, `disable`, and `enable` commands set these annotations. If you're running an older operator (v0.7.x or earlier), these commands will appear to succeed but the operator won't recognize the annotations - you'll need to edit the Skyhook spec directly using `kubectl edit`.

## Installation

```bash
# Build from source
make build-cli

# Install as kubectl plugin
cp bin/kubectl-skyhook /usr/local/bin/

# Verify installation
kubectl skyhook version
```

## Usage Structure

### Basic Command Structure
```
kubectl skyhook [global-flags] <command> [subcommand] [flags] [arguments]
```

### Global Flags
- `-h, --help` - Show help for any command
- `--version` - Show version information
- `-n, --namespace` - Kubernetes namespace (default: "skyhook")
- `-o, --output` - Output format: table|json|yaml|wide
- `-v, --verbose` - Enable verbose output
- `--dry-run` - Preview changes without applying them
- `--kubeconfig` - Path to kubeconfig file

## Commands

### Version Command

Show plugin and operator versions.

```bash
# Show both plugin and operator versions
kubectl skyhook version

# Show only plugin version (no cluster query)
kubectl skyhook version --client-only

# With custom timeout
kubectl skyhook version --timeout 10s
```

### Pause/Resume Commands

Control Skyhook processing state.

> **Note:** Requires operator v0.8.0+. See [Compatibility](#compatibility) for details.

```bash
# Pause a Skyhook (stops processing new nodes)
kubectl skyhook pause my-skyhook
kubectl skyhook pause my-skyhook --confirm  # Skip confirmation

# Resume a paused Skyhook
kubectl skyhook resume my-skyhook
```

### Disable/Enable Commands

Completely disable or re-enable a Skyhook.

> **Note:** Requires operator v0.8.0+. See [Compatibility](#compatibility) for details.

```bash
# Disable a Skyhook completely
kubectl skyhook disable my-skyhook
kubectl skyhook disable my-skyhook --confirm

# Re-enable a disabled Skyhook
kubectl skyhook enable my-skyhook
```

### Reset Command

Reset all package state for a Skyhook, causing re-execution from the beginning.

```bash
# Reset all nodes for a Skyhook (also resets batch state by default)
kubectl skyhook reset gpu-init --confirm

# Preview changes without applying (dry-run)
kubectl skyhook reset gpu-init --dry-run

# Reset nodes only, preserve deployment policy batch state
kubectl skyhook reset gpu-init --skip-batch-reset --confirm
```

| Flag | Description |
|------|-------------|
| `--confirm, -y` | Skip confirmation prompt |
| `--skip-batch-reset` | Skip resetting deployment policy batch state |

> **Note:** By default, `reset` also resets the deployment policy batch state so the next rollout starts from batch 1, and clears node ordering state (`NodeOrderOffset` and `NodePriority`) so `SKYHOOK_NODE_ORDER` restarts from `0`. Use `--skip-batch-reset` to preserve the existing batch and ordering state.

### Deployment Policy Commands

Manage deployment policy batch state.

> **Note:** Requires operator v0.8.0+.

```bash
# Reset batch state for a Skyhook (starts rollout from batch 1)
kubectl skyhook deployment-policy reset gpu-init --confirm

# Preview what would be reset (dry-run)
kubectl skyhook deployment-policy reset gpu-init --dry-run

# Using the short alias
kubectl skyhook dp reset gpu-init --confirm
```

The `deployment-policy reset` command resets the batch processing state for all compartments in the specified Skyhook, including:
- Current batch number (reset to 1)
- Consecutive failure count
- Completed and failed node counts
- Stop flag
- Node ordering state (`NodeOrderOffset` and `NodePriority`) — `SKYHOOK_NODE_ORDER` restarts from `0`

| Flag | Description |
|------|-------------|
| `--confirm, -y` | Skip confirmation prompt |

**When to use**:
- After a rollout completes and you want to start a new rollout fresh
- When batch processing is stuck and needs to be reset
- Before re-running a rollout with the same deployment policy

See [Deployment Policy documentation](deployment_policy.md) for details on auto-reset configuration.

### Node Commands

Manage Skyhook nodes across the cluster.

```bash
# List all nodes targeted by a Skyhook
kubectl skyhook node list --skyhook my-skyhook
kubectl skyhook node list --skyhook my-skyhook -o json

# Show all Skyhook activity on specific node(s)
kubectl skyhook node status worker-1
kubectl skyhook node status worker-1 worker-2
kubectl skyhook node status "worker-.*"  # Regex pattern
kubectl skyhook node status worker-1 --skyhook my-skyhook  # Filter by Skyhook

# Reset all package state on node(s)
kubectl skyhook node reset worker-1 --skyhook my-skyhook --confirm
kubectl skyhook node reset "node-.*" --skyhook my-skyhook --dry-run

# Ignore/unignore nodes from processing
kubectl skyhook node ignore worker-1
kubectl skyhook node ignore "test-node-.*"
kubectl skyhook node unignore worker-1
```

#### Node Flags

| Command | Flag | Description |
|---------|------|-------------|
| `list` | `--skyhook` | Skyhook name (required) |
| `status` | `--skyhook` | Filter by Skyhook name |
| `reset` | `--skyhook` | Skyhook name (required) |
| `reset` | `--confirm, -y` | Skip confirmation prompt |

### Package Commands

Manage Skyhook packages.

```bash
# Query package status across nodes
kubectl skyhook package status my-package --skyhook my-skyhook
kubectl skyhook package status my-package --skyhook my-skyhook --node worker-1
kubectl skyhook package status my-package --skyhook my-skyhook -o wide

# Force package re-run on specific nodes
kubectl skyhook package rerun my-package --skyhook my-skyhook --node worker-1
kubectl skyhook package rerun my-package --skyhook my-skyhook --node "worker-.*" --confirm
kubectl skyhook package rerun my-package --skyhook my-skyhook --node worker-1 --stage config

# Get package logs
kubectl skyhook package logs my-package --skyhook my-skyhook
kubectl skyhook package logs my-package --skyhook my-skyhook --node worker-1
kubectl skyhook package logs my-package --skyhook my-skyhook --stage apply
kubectl skyhook package logs my-package --skyhook my-skyhook -f  # Follow
kubectl skyhook package logs my-package --skyhook my-skyhook --tail 100
```

#### Package Flags

| Command | Flag | Description |
|---------|------|-------------|
| `status` | `--skyhook` | Skyhook name (required) |
| `status` | `--node` | Filter by node pattern (repeatable) |
| `rerun` | `--skyhook` | Skyhook name (required) |
| `rerun` | `--node` | Node pattern (required, repeatable) |
| `rerun` | `--stage` | Re-run from stage: apply, config, interrupt, post-interrupt |
| `rerun` | `--confirm, -y` | Skip confirmation prompt |
| `logs` | `--skyhook` | Skyhook name (required) |
| `logs` | `--node` | Filter by node name |
| `logs` | `--stage` | Filter by stage |
| `logs` | `-f, --follow` | Follow log output |
| `logs` | `--tail` | Lines from end (-1 for all) |

## Help System

```bash
# General help
kubectl skyhook --help

# Command group help
kubectl skyhook node --help
kubectl skyhook package --help
kubectl skyhook deployment-policy --help

# Specific command help
kubectl skyhook node reset --help
kubectl skyhook package rerun --help
kubectl skyhook deployment-policy reset --help
```

## Common Usage Patterns

### Debugging a Failed Package
```bash
# 1. Check package status
kubectl skyhook package status my-package --skyhook my-skyhook -o wide

# 2. View logs for the failed package
kubectl skyhook package logs my-package --skyhook my-skyhook --node worker-1

# 3. Fix the issue, then force re-run
kubectl skyhook package rerun my-package --skyhook my-skyhook --node worker-1 --confirm
```

### Node Maintenance
```bash
# 1. Ignore node before maintenance
kubectl skyhook node ignore worker-1

# 2. Perform maintenance...

# 3. Unignore and reset to re-run all packages
kubectl skyhook node unignore worker-1
kubectl skyhook node reset worker-1 --skyhook my-skyhook --confirm
```

### Cluster-Wide Status Check
```bash
# List all nodes for a Skyhook
kubectl skyhook node list --skyhook my-skyhook

# Check status of all nodes
kubectl skyhook node status

# Check specific Skyhook across all nodes
kubectl skyhook node status --skyhook my-skyhook -o json
```

### Resetting a Rollout
```bash
# 1. Full reset: nodes + batch state (starts everything fresh)
kubectl skyhook reset my-skyhook --confirm

# 2. Or reset only batch state (keep node state, restart batch progression)
kubectl skyhook deployment-policy reset my-skyhook --confirm

# 3. Or reset only nodes (keep batch progression)
kubectl skyhook reset my-skyhook --skip-batch-reset --confirm
```

### Emergency Stop

> **Note:** Requires operator v0.8.0+. For older operators, use `kubectl edit skyhook my-skyhook` and set `spec.pause: true`.

```bash
# Pause all processing
kubectl skyhook pause my-skyhook --confirm

# Or disable completely
kubectl skyhook disable my-skyhook --confirm
```

## Output Formats

All status commands support multiple output formats:

```bash
# Table (default) - human-readable
kubectl skyhook node list --skyhook my-skyhook

# Wide - table with additional columns
kubectl skyhook node list --skyhook my-skyhook -o wide

# JSON - machine-readable
kubectl skyhook node list --skyhook my-skyhook -o json

# YAML - machine-readable
kubectl skyhook node list --skyhook my-skyhook -o yaml
```

## Architecture

### Package Structure
```
operator/cmd/cli/app/           # CLI commands
├── cli.go                      # Root command (NewSkyhookCommand)
├── version.go                  # Version command
├── reset.go                    # Reset command (nodes + batch state)
├── lifecycle.go                # Pause, resume, disable, enable commands
├── deploymentpolicy/           # Deployment policy subcommands
│   ├── deploymentpolicy.go     # Parent command
│   └── deploymentpolicy_reset.go  # Batch state reset
├── node/                       # Node subcommands
│   ├── node.go                 # Parent command
│   ├── node_list.go
│   ├── node_status.go
│   ├── node_reset.go
│   └── node_ignore.go          # Ignore and unignore commands
└── package/                    # Package subcommands
    ├── package.go              # Parent command
    ├── package_status.go
    ├── package_rerun.go
    └── package_logs.go

operator/internal/cli/          # Shared CLI utilities
├── client/                     # Kubernetes client wrapper
├── context/                    # CLI context and global flags
└── utils/                      # Shared utilities
```

### Command Creation Flow
```
main()
  └── cli.Execute()
      └── NewSkyhookCommand(ctx)
          ├── NewVersionCmd(ctx)
          ├── NewResetCmd(ctx)
          ├── NewPauseCmd(ctx)
          ├── NewResumeCmd(ctx)
          ├── NewDisableCmd(ctx)
          ├── NewEnableCmd(ctx)
          ├── deploymentpolicy.NewDeploymentPolicyCmd(ctx)
          │   └── NewResetCmd(ctx)
          ├── node.NewNodeCmd(ctx)
          │   ├── NewListCmd(ctx)
          │   ├── NewStatusCmd(ctx)
          │   ├── NewResetCmd(ctx)
          │   ├── NewIgnoreCmd(ctx)
          │   └── NewUnignoreCmd(ctx)
          └── pkg.NewPackageCmd(ctx)
              ├── NewStatusCmd(ctx)
              ├── NewRerunCmd(ctx)
              └── NewLogsCmd(ctx)
```

### Testing
```bash
# Run CLI tests
make test-cli

# Run all tests
make test
```