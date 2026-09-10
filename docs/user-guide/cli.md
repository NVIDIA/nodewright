# NodeWright CLI

kubectl plugin for managing NodeWright deployments, packages, and nodes.

## Overview

The NodeWright CLI (`kubectl nodewright`) provides SRE tooling for managing NodeWright deployments and their packages across Kubernetes cluster nodes. It supports inspecting node/package state, forcing re-runs, managing node lifecycle, and retrieving logs.

## Compatibility

### Required API Group

This CLI targets the `nodewright.nvidia.com` API group and the `NodeWright`
kind, and it reads and writes `nodewright.nvidia.com/*` node annotations. It
requires an operator that serves `nodewright.nvidia.com`; older Skyhook-only
operators that serve just `skyhook.nvidia.com` and the `Skyhook` kind are
unsupported. Upgrade the operator to a NodeWright-capable version before using
this CLI.

Every cluster-backed command runs a preflight check on the served API groups.
If the cluster serves only the legacy `skyhook.nvidia.com` group and not
`nodewright.nvidia.com`, the command fails fast with a clear, actionable error
naming both groups and telling you to upgrade to a NodeWright-capable operator,
rather than a confusing `NotFound`.

### Namespace resolution

The install namespace for new deployments moved from `skyhook` to `nodewright` with
the rename. Namespaces cannot be renamed in place, and Helm cannot move a release
between namespaces, so an install that predates the rename legitimately stays in
`skyhook` indefinitely. The CLI therefore does **not** simply default to
`nodewright`.

When `--namespace` is **not** passed, the CLI resolves the operator's namespace in
this order and caches the answer for the invocation:

1. `nodewright` — if a NodeWright operator Deployment is there, use it.
2. `skyhook` — if the operator is there instead, use it and print a one-line note
   to stderr saying the legacy namespace was used and that new installs default to
   `nodewright`. Pass `--namespace skyhook` to silence the note.
3. A cluster-wide Deployment sweep, for installs in some other namespace. This
   needs cluster-scoped list permission; if you don't have it, the step is skipped
   silently rather than failing.
4. If nothing is found, `nodewright` is used so the command's own lookup produces
   the specific error (for example, "no operator deployment found in namespace
   \"nodewright\"").

When `--namespace` **is** passed, it is used verbatim with no discovery and no
note. Nothing else about the CLI is version-gated on this: the namespace is only
used to locate the operator Deployment and the package pods.

The `NodeWright` and `DeploymentPolicy` CRDs are cluster-scoped, so `--namespace`
never affects which CRs a command sees.

| Situation | `--namespace` omitted | `--namespace` passed |
|-----------|----------------------|----------------------|
| Operator in `nodewright` | ✅ Discovered | ✅ Used as given |
| Operator in `skyhook` (pre-rename install) | ✅ Discovered, with a deprecation note | ✅ Used as given |
| Operator in some other namespace | ✅ Discovered when the user can list Deployments cluster-wide, otherwise falls back to `nodewright` and the command reports the miss | ✅ Used as given |
| No operator installed | ⚠️ Falls back to `nodewright`; the command reports the miss | ⚠️ Command reports the miss |

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
| `reset --package` | ✅ Full (v0.7.5+) | ✅ Full |
| `update-state` | ✅ Full (v0.7.5+) — see note | ✅ Full — see note |
| `deployment-policy reset` | ❌ Not supported | ✅ Full |
| `pause` | ❌ Not supported | ✅ Full — stop strength varies, see note |
| `resume` | ❌ Not supported | ✅ Full |
| `disable` | ❌ Not supported | ✅ Full |
| `enable` | ❌ Not supported | ✅ Full |

> **Note on `pause` stop strength:** the command works on every v0.8.0+ operator,
> but *how hard it stops* is version-dependent, so the column above is about
> availability only. On operators that run package stages as Jobs, pause suspends
> the stage that is currently executing. On v0.8.0+ operators that predate that
> change — and, briefly, for a stage still running as a pre-upgrade pod during the
> upgrade itself — pause blocks all *new* stage scheduling but lets an in-flight
> stage finish. (On v0.7.x and earlier the command does not exist at all; use
> `spec.pause`.) `disable` never stops in-flight work on any version. See
> [Emergency Stop](#emergency-stop) for the full semantics and for what `resume`
> re-runs.
>
> **Note on `update-state` and `reset --package`:** These commands edit the
> `nodewright.nvidia.com/nodeState_<nodewright>` annotation in-place. The
> annotation's `map[string]PackageStatus` shape has been stable since
> **operator v0.7.5**, and the CLI refuses to run against anything older.
> What *has* evolved across operator releases is the set of recognised
> stage and state values — for example `uninstall` and `uninstall-interrupt`
> were added in v0.16.0. Picking a stage/state your operator doesn't
> recognise will leave the package in a state the operator can't progress
> from. Confirm the stage/state values are valid for your operator before
> running these commands.

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
    nodewright.nvidia.com/pause: "true"
    nodewright.nvidia.com/disable: "true"
```

The CLI's `pause`, `resume`, `disable`, and `enable` commands set these annotations. If you're running an older operator (v0.7.x or earlier), these commands will appear to succeed but the operator won't recognize the annotations - you'll need to edit the Skyhook spec directly using `kubectl edit`.

## Installation

```bash
# Build from source
cd operator
make build-cli

# Install as kubectl plugin
cp bin/nodewright /usr/local/bin/kubectl-nodewright # or another directory in $PATH with write access

# Verify installation
kubectl nodewright version
```

## Usage Structure

### Basic Command Structure

```
kubectl nodewright [global-flags] <command> [subcommand] [flags] [arguments]
```

### Global Flags

- `-h, --help` - Show help for any command
- `--version` - Show version information
- `-n, --namespace` - Kubernetes namespace the operator is installed in. When omitted, the CLI discovers it (see [Namespace resolution](#namespace-resolution)); the fallback default is `nodewright`.
- `-o, --output` - Output format: table|json|yaml|wide
- `-v, --verbose` - Enable verbose output
- `--dry-run` - Preview changes without applying them
- `--kubeconfig` - Path to kubeconfig file

## Commands

### Version Command

Show plugin and operator versions.

```bash
# Show both plugin and operator versions
kubectl nodewright version

# Show only plugin version (no cluster query)
kubectl nodewright version --client-only

# With custom timeout
kubectl nodewright version --timeout 10s
```

### Pause/Resume Commands

Control NodeWright processing state.

> **Note:** Requires operator v0.8.0+. See [Compatibility](#compatibility) for details.

```bash
# Pause a Skyhook (stops processing new nodes)
kubectl nodewright pause my-skyhook
kubectl nodewright pause my-skyhook --confirm  # Skip confirmation

# Resume a paused Skyhook
kubectl nodewright resume my-skyhook
```

### Disable/Enable Commands

Completely disable or re-enable a NodeWright.

> **Note:** Requires operator v0.8.0+. See [Compatibility](#compatibility) for details.

```bash
# Disable a Skyhook completely
kubectl nodewright disable my-skyhook
kubectl nodewright disable my-skyhook --confirm

# Re-enable a disabled Skyhook
kubectl nodewright enable my-skyhook
```

### Reset Command

Reset all package state for a NodeWright, causing re-execution from the beginning.

```bash
# Reset all nodes for a Skyhook (also resets batch state by default)
kubectl nodewright reset gpu-init --confirm

# Preview changes without applying (dry-run)
kubectl nodewright reset gpu-init --dry-run

# Reset nodes only, preserve deployment policy batch state
kubectl nodewright reset gpu-init --skip-batch-reset --confirm

# Reset only a single package across all tracked nodes
kubectl nodewright reset gpu-init --package pkg1:1.0 --confirm --skip-batch-reset
```

| Flag | Description |
|------|-------------|
| `--confirm, -y` | Skip confirmation prompt |
| `--skip-batch-reset` | Skip resetting deployment policy batch state |
| `--package <name>[:<version>]` | Reset only this package's state on each node |

> **Note:** By default, `reset` also resets the deployment policy batch state so the next rollout starts from batch 1, and clears node ordering state (`NodeOrderOffset` and `NodePriority`) so `SKYHOOK_NODE_ORDER` restarts from `0`. Use `--skip-batch-reset` to preserve the existing batch and ordering state.

#### `--package <name>[:<version>]`

When `--package` is set, `reset` removes only the named package's entry from
each node's `nodeState` annotation instead of removing the whole annotation:

- If `<version>` is supplied (e.g. `pkg1:1.0`), only entries whose recorded
  version matches are removed; nodes with the package at a different version
  are left untouched and reported in the command output.
- If the package was the last entry in the annotation, the entire annotation
  is removed (matching the behavior of a full reset).
- Batch state is **deliberately not reset** on this path regardless of
  `--skip-batch-reset`: restarting the full rollout from batch 1 to recover a
  single package would be disproportionate. Pair with
  `kubectl nodewright deployment-policy reset` explicitly if you also want to
  restart batches.

### Update-State Command

Edit the recorded state of a single package on NodeWright-managed nodes.
`update-state` performs a surgical edit to the per-node
`nodewright.nvidia.com/nodeState_<nodewright>` annotation — replacing (or, with
`--add`, inserting) one entry in the `map[string]PackageStatus` value. It is
an **administrator escape hatch** for recovering from stuck rollouts and
deliberately does *not* validate that the requested `(stage, state)`
combination is one the operator could legally produce, nor does it gate
destructive stages (`uninstall`, `uninstall-interrupt`) behind extra
prompts.

```bash
kubectl nodewright update-state <skyhook-name> <package> <version> <stage> <state>
```

> **⚠️ Pause the NodeWright before running `update-state`.**
>
> `update-state` performs a read-modify-write on the node-state annotation and
> uses a merge patch with no resource-version check. The patch rewrites the
> **entire** annotation value, not just the targeted package's entry — so a
> concurrent operator (or CLI) edit to any *other* package's entry in the
> same annotation will also be clobbered. If the operator reconciles the
> node between the CLI's read and write, the operator can immediately
> overwrite the manual edit — at best wasting the operation, at worst racing
> the operator into an inconsistent state. Always pause the NodeWright
> (`kubectl nodewright pause <name> --confirm`) before running this command,
> and resume only when finished.
>
> `update-state` also requires the package + version to still be present in
> `skyhook.Spec.Packages`. It cannot edit an *orphaned* node-state entry
> left behind after the package was removed from the spec — use `reset
> --package <name>[:<version>]` for that.

```bash
# Mark pkg1@1.0 as complete on every node that already tracks this Skyhook
kubectl nodewright update-state gpu-init pkg1 1.0 config complete --confirm

# Same, but only on one node
kubectl nodewright update-state gpu-init pkg1 1.0 config complete --node worker-1 --confirm

# Narrow to a label-selected set of nodes
kubectl nodewright update-state gpu-init pkg1 1.0 interrupt in_progress -l role=gpu --confirm

# Preview the changes without writing them
kubectl nodewright update-state gpu-init pkg1 1.0 config erroring --dry-run
```

| Flag | Description |
|------|-------------|
| `--node` | Limit the update to specific node(s); repeat for multiple |
| `--selector, -l` | Limit the update to nodes matching a label selector |
| `--confirm, -y` | Skip confirmation prompt |
| `--add` | Create a fresh `nodeState` entry on nodes that do not yet have one for this package; requires `--node` or `--selector` |

The global `--dry-run` flag is honored: the command prints the set of nodes
it would patch and exits without writing.

By default `update-state` targets only nodes that already have a `nodeState`
entry for the named NodeWright. If `--node` names a node that does not exist
or has no state for the NodeWright, the command warns and skips that node
rather than failing.

#### `--add`

`--add` creates a fresh `nodeState` entry on nodes that do not yet have one
for the given `<package>@<version>` — useful for bootstrapping state on a
node the operator has not visited yet, or for re-creating an entry that was
manually deleted.

`--add` **requires** either `--node` or `--selector` so the scope of the
creation is explicit. Without one of these flags, `--add` would apply to
every node in the cluster that matches the NodeWright's selector, which is
far too broad for a creation operation — the CLI rejects this combination
with an error.

If a targeted node already has an entry for `<package>@<version>`, `--add`
warns and skips that node (use `update-state` without `--add` if you intend
to overwrite the existing entry).

### Migrating manifests to NodeWright

There is no dedicated migrate command: the conversion is a mechanical group and
kind swap with no change to the spec body. Both `Skyhook` and `DeploymentPolicy`
change API group to `nodewright.nvidia.com/v1alpha1`; `Skyhook` additionally
changes its kind to `NodeWright`, while `DeploymentPolicy` keeps its kind.

For a source manifest, that is:

```bash
sed -e '/^ *apiVersion:/ s|skyhook\.nvidia\.com/|nodewright.nvidia.com/|' \
    -e 's|^\( *kind: *\)Skyhook[[:space:]]*$|\1NodeWright|' skyhook.yaml > nodewright.yaml
```

**Rewrite `apiVersion` and `kind` only.** A blanket
`s|skyhook\.nvidia\.com/|nodewright.nvidia.com/|g` is tempting but wrong: it also
rewrites key names inside the spec body that are **yours**, not the operator's.
`nodeSelectors.matchLabels` and `podNonInterruptLabels` refer to labels on your
own nodes and pods; the rename does not touch those labels, so rewriting the
selector points the CR at a key nothing carries and it silently matches nothing.
It also makes the re-apply a real spec change rather than the no-op adoption of
the mirrored object that the migration flow expects.

The one thing to rename by hand, if your manifest sets it, is the operator's own
lifecycle annotations on the CR: `skyhook.nvidia.com/pause` and
`skyhook.nvidia.com/disable` become `nodewright.nvidia.com/*`.

Leave everything else alone. In particular, do not rewrite the runtime-required
**taint** key here: it is the bare string `skyhook.nvidia.com` (no trailing
slash), so a blanket group swap would corrupt it, and the taint on a node is not
something a CR manifest edit can change. The default key did move to
`nodewright.nvidia.com`, but the operator tolerates and removes both keys for the
deprecation window, so an `additionalTolerations` entry naming the legacy key
stays valid. Migrating it is a change to your **provisioning** config (autoscaler,
node pool, `--register-with-taints`), not to your CRs; see
[docs/user-guide/runtime-required.md](runtime-required.md#taint-key-rename-skyhooknvidiacom---nodewrightnvidiacom).

The operator's mirror controller already converts the live objects in the
cluster automatically; the edit above is only for the manifests in your source
of truth (git / GitOps). See
[docs/getting-started/migration.md](../getting-started/migration.md) for the full flow.

### Deployment Policy Commands

Manage deployment policy batch state.

> **Note:** Requires operator v0.8.0+.

```bash
# Reset batch state for a Skyhook (starts rollout from batch 1)
kubectl nodewright deployment-policy reset gpu-init --confirm

# Preview what would be reset (dry-run)
kubectl nodewright deployment-policy reset gpu-init --dry-run

# Using the short alias
kubectl nodewright dp reset gpu-init --confirm
```

The `deployment-policy reset` command resets the batch processing state for all compartments in the specified NodeWright, including:

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

See [Deployment Policy documentation](deployment-policy.md) for details on auto-reset configuration.

### Node Commands

Manage NodeWright nodes across the cluster.

```bash
# List all nodes targeted by a Skyhook
kubectl nodewright node list --nodewright my-nodewright
kubectl nodewright node list --nodewright my-nodewright -o json

# Show all Skyhook activity on specific node(s)
kubectl nodewright node status worker-1
kubectl nodewright node status worker-1 worker-2
kubectl nodewright node status "worker-.*"  # Regex pattern
kubectl nodewright node status worker-1 --nodewright my-nodewright  # Filter by NodeWright

# Reset all package state on node(s)
kubectl nodewright node reset worker-1 --nodewright my-nodewright --confirm
kubectl nodewright node reset "node-.*" --nodewright my-nodewright --dry-run

# Ignore/unignore nodes from processing
kubectl nodewright node ignore worker-1
kubectl nodewright node ignore "test-node-.*"
kubectl nodewright node unignore worker-1
```

#### Node Status Columns

`node status` reports two cordon columns alongside the rollout state. They appear
in the default table, in `-o wide`, and as fields in `-o json` / `-o yaml`:

| Column | JSON field | Meaning |
|--------|------------|---------|
| `CORDONED` | `cordoned` | The node's `spec.unschedulable`, whatever set it — a NodeWright interrupt mid-flight, a persistent runtime-required cordon, or a `kubectl cordon` of your own. |
| `RUNTIME-REQUIRED-CORDON` | `runtimeRequiredCordon` | The node carries the `nodewright.nvidia.com/runtimeRequiredCordon` annotation, so the cordon came from a NodeWright with `spec.runtimeRequired: true` and `spec.runtimeRequiredCordonAfter: true`. |

Read them together to tell a transient cordon from one that is waiting on you:

- `CORDONED=true`, `RUNTIME-REQUIRED-CORDON=false` — the operator uncordons this
  node itself once every NodeWright holding a `nodewright.nvidia.com/cordon_*`
  annotation on it has finished, unless the cordon is one you applied yourself.
- `CORDONED=true`, `RUNTIME-REQUIRED-CORDON=true` — the cordon will not clear on
  its own. It survives later NodeWright interrupts and `kubectl nodewright reset`,
  and only an external actor can release it. See
  [runtime-required.md](runtime-required.md#what-happens-when-the-taint-is-removed)
  for the patch that clears both the annotation and the cordon.

Against an operator that predates `spec.runtimeRequiredCordonAfter` the annotation
is never set, so `RUNTIME-REQUIRED-CORDON` reads `false` on every node. That is
accurate, not degraded — no such cordon can exist — so the columns need no operator
version gate.

#### Node Flags

| Command | Flag | Description |
|---------|------|-------------|
| `list` | `--nodewright` | NodeWright name (required) |
| `status` | `--nodewright` | Filter by NodeWright name |
| `reset` | `--nodewright` | NodeWright name (required) |
| `reset` | `--confirm, -y` | Skip confirmation prompt |

### Package Commands

Manage NodeWright packages.

```bash
# Query package status across nodes
kubectl nodewright package status my-package --nodewright my-nodewright
kubectl nodewright package status my-package --nodewright my-nodewright --node worker-1
kubectl nodewright package status my-package --nodewright my-nodewright -o wide

# Force package re-run on specific nodes
kubectl nodewright package rerun my-package --nodewright my-nodewright --node worker-1
kubectl nodewright package rerun my-package --nodewright my-nodewright --node "worker-.*" --confirm
kubectl nodewright package rerun my-package --nodewright my-nodewright --node worker-1 --stage config

# Get package logs
kubectl nodewright package logs my-package --nodewright my-nodewright
kubectl nodewright package logs my-package --nodewright my-nodewright --node worker-1
kubectl nodewright package logs my-package --nodewright my-nodewright --stage apply
kubectl nodewright package logs my-package --nodewright my-nodewright -f  # Follow
kubectl nodewright package logs my-package --nodewright my-nodewright --tail 100
```

#### Package Flags

| Command | Flag | Description |
|---------|------|-------------|
| `status` | `--nodewright` | NodeWright name (required) |
| `status` | `--node` | Filter by node pattern (repeatable) |
| `rerun` | `--nodewright` | NodeWright name (required) |
| `rerun` | `--node` | Node pattern (required, repeatable) |
| `rerun` | `--stage` | Re-run from stage: apply, config, interrupt, post-interrupt |
| `rerun` | `--confirm, -y` | Skip confirmation prompt |
| `logs` | `--nodewright` | NodeWright name (required) |
| `logs` | `--node` | Filter by node name |
| `logs` | `--stage` | Filter by stage |
| `logs` | `-f, --follow` | Follow log output |
| `logs` | `--tail` | Lines from end (-1 for all) |

## Help System

```bash
# General help
kubectl nodewright --help

# Command group help
kubectl nodewright node --help
kubectl nodewright package --help
kubectl nodewright deployment-policy --help

# Specific command help
kubectl nodewright node reset --help
kubectl nodewright package rerun --help
kubectl nodewright deployment-policy reset --help
```

## Common Usage Patterns

### Debugging a Failed Package

```bash
# 1. Check package status
kubectl nodewright package status my-package --nodewright my-nodewright -o wide

# 2. View logs for the failed package
kubectl nodewright package logs my-package --nodewright my-nodewright --node worker-1

# 3. Fix the issue, then force re-run
kubectl nodewright package rerun my-package --nodewright my-nodewright --node worker-1 --confirm
```

### Node Maintenance

```bash
# 1. Ignore node before maintenance
kubectl nodewright node ignore worker-1

# 2. Perform maintenance...

# 3. Unignore and reset to re-run all packages
kubectl nodewright node unignore worker-1
kubectl nodewright node reset worker-1 --nodewright my-nodewright --confirm
```

### Cluster-Wide Status Check

```bash
# List all nodes for a Skyhook
kubectl nodewright node list --nodewright my-nodewright

# Check status of all nodes
kubectl nodewright node status

# Check specific Skyhook across all nodes
kubectl nodewright node status --nodewright my-nodewright -o json
```

### Resetting a Rollout

```bash
# 1. Full reset: nodes + batch state (starts everything fresh)
kubectl nodewright reset my-skyhook --confirm

# 2. Or reset only batch state (keep node state, restart batch progression)
kubectl nodewright deployment-policy reset my-skyhook --confirm

# 3. Or reset only nodes (keep batch progression)
kubectl nodewright reset my-skyhook --skip-batch-reset --confirm
```

### Surgical Recovery

When a single package on a single node is wedged and a full reset is too
disruptive, pause the NodeWright, edit the recorded state directly, then
resume:

```bash
# 1. Pause so the operator doesn't clobber the manual edit
kubectl nodewright pause my-skyhook --confirm

# 2. Mark the wedged package as complete (or set whatever stage/state
#    you need to unblock the rollout)
kubectl nodewright update-state my-skyhook my-package 1.0 config complete \
    --node worker-1 --confirm

# 3. Resume processing
kubectl nodewright resume my-skyhook --confirm
```

For a single-package reset across all nodes (without disturbing the
deployment policy batch state), prefer `reset --package`:

```bash
kubectl nodewright reset my-skyhook --package my-package:1.0 --confirm
```

### Emergency Stop

> **Note:** Requires operator v0.8.0+. For older operators, use `kubectl edit skyhook my-skyhook` and set `spec.pause: true`.

**`pause` and `disable` stop different things.** `pause` is the one that can halt work already running: on operators that run package stages as Jobs it suspends the executing stage, and on older ones it blocks new scheduling only — see the version note below. `disable` takes the NodeWright out of processing so no *new* work is scheduled, but it does not stop a stage already under way; that stage runs to completion.

They compose safely in either order: disabling a paused NodeWright leaves its suspended stages suspended rather than resuming them. Those stages resume only once **both** annotations are cleared — re-enabling on its own does nothing while the NodeWright is still paused. If you want everything stopped *now*, `pause` is the command.

```bash
# Pause all processing
kubectl nodewright pause my-skyhook --confirm

# Or prevent new work being scheduled
kubectl nodewright disable my-skyhook --confirm
```

> **How hard pause stops depends on the operator version.** On operators that run
> package stages as Jobs, pause suspends the stage that is *currently executing* —
> its pod is signaled to stop and nothing new starts until you `resume`, at which
> point the stage re-runs from the start of its current phase (the agent skips
> already-completed steps). On earlier operators — and, briefly, for a stage still
> running as a pre-upgrade pod during an operator upgrade — a stage already in
> flight finishes its current run before pause takes hold; pause still blocks all
> *new* stage scheduling. If you need a running stage to stop immediately, confirm
> the operator executes packages as Jobs.

## Output Formats

All status commands support multiple output formats:

```bash
# Table (default) - human-readable
kubectl nodewright node list --nodewright my-nodewright

# Wide - table with additional columns
kubectl nodewright node list --nodewright my-nodewright -o wide

# JSON - machine-readable
kubectl nodewright node list --nodewright my-nodewright -o json

# YAML - machine-readable
kubectl nodewright node list --nodewright my-nodewright -o yaml
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
