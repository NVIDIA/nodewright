# NodeWright agent

The agent is the Go binary the operator runs in every package pod. It reads
`config.json` from the package copy directory `COPY_DIR`, runs the package's
lifecycle steps and interrupts against the host through the mounted root
filesystem, and records what it has completed on the host so a retry or a later
stage can skip it.

For a package image, the pod's package-image init container fills `COPY_DIR`
from `/skyhook-package`, and the agent copies `SKYHOOK_DATA_DIR` itself only
when `COPY_DIR` is absent. For a
[package artifact](../docs/user-guide/package-artifact-spec.md), the agent pulls
the artifact and lays it out itself, as [Package artifacts](#package-artifacts)
describes, so the pod has no package-image init container and the package needs
no shell or `cp`.

## Development

The module is `github.com/NVIDIA/nodewright/agent`, vendored, with its tooling
installed into `bin/` by `deps.mk` on first use. It requires
`github.com/NVIDIA/nodewright/artifact` through a `replace` directive to
`../artifact` and vendors it, so after changing `artifact/` run `go mod vendor`
in `agent/` and commit the result; agent CI fails on a stale copy. The image
build needs only `agent/`.

```bash
make test lint          # exactly what CI's agent lanes run
make test               # Ginkgo/Gomega unit tests with coverage, written to reporting/
make lint               # golangci-lint plus the license-header check
make build              # bin/agent
make fmt                # gofmt plus license headers
make generate-mocks     # regenerate the mockery mocks after editing a mocked interface
make docker-build       # local container image from ../containers/agent.Dockerfile
```

### Development workflow

1. Make the change and write or update its unit tests.
1. Run `make test lint`.
1. Run `make fmt`.
1. Open a pull request.

### Execution contracts

Steps and interrupts share execution policy through `execution.Config`. A `Config` composes the host root mount,
the package directories inside that host, and the stdout and stderr writers
that receive raw command output. Non-host steps resolve those directories
through the mounted host root before execution. Operations report
`execution.Status`:
`execution.StatusSuccess` means the operation satisfied its execution policy,
while `execution.StatusFailed` means it did not.

The agent reserves `STEP_ROOT` and `SKYHOOK_DIR` for every step and
`PREVIOUS_VERSION` and `CURRENT_VERSION` for upgrade steps. Runtime values
overwrite matching keys from a package's configured `env`. For host steps,
`STEP_ROOT` and `SKYHOOK_DIR` are host-absolute paths. For non-host steps, they
are paths inside the agent container, resolved through the mounted host root.

Each interrupt owns its command construction and execution. The `Interrupt`
contract exposes `Type` for the wire identity, `Run` for execution using an
`execution.Config`, and `Serialize` for the operator-facing representation.
The orchestration layer uses the legacy agent's indexed completion-marker names
for each interrupt command and resource ID, so an agent upgrade resumes after
the last completed command. Node restarts use an indexed pending marker
containing the host boot ID: a changed boot ID promotes the marker to complete,
while an unchanged boot ID retries the restart. This keeps reboot completion
independent of the signal used to terminate the agent or its child process.

Successful steps write both the legacy-compatible completion marker and the
Go-native fingerprint marker. Either marker prevents a step from running again,
which preserves idempotence when moving between agent implementations.

The entrypoint accepts the current operator forms:

```text
agent MODE ROOT_MOUNT COPY_DIR
agent interrupt ROOT_MOUNT COPY_DIR INTERRUPT_DATA
agent preflight ROOT_MOUNT
agent --version
```

It also accepts the legacy forms, which default `ROOT_MOUNT` to `/root`:

```text
agent MODE COPY_DIR
agent interrupt COPY_DIR INTERRUPT_DATA
```

SIGTERM lets the active step or interrupt operation run to completion and
prevents later ones from starting, so a package's `gracefulShutdown` is the
time its script has to finish. A failed operation or runtime error exits with
status 1; malformed arguments exit with status 2. A `MODE` this version does
not know, in one of the forms above, exits 0 without doing anything, so an
older agent given a newer stage is a no-op; an agent older than
[`preflight`](#preflight) given `preflight ROOT_MOUNT` exits 0 without writing
a result.

`agent --version` prints the semantic version embedded in the binary at build
time, falling back to the embedded Git SHA or `unknown` when build metadata is
omitted. It exits 0 without preparing or executing a package and without
touching the host or the registry, even when `NODEWRIGHT_PACKAGE_REF` is set.
Because the distroless agent image has no shell, the operator runs it with args
`[--version]` and no command as the exit-0 `done` container of every artifact
stage Job, of every interrupt Job while its `PACKAGE_ARTIFACTS` option is on,
and of the interrupt Job of a package whose preflight accepted an artifact on
the node, even after the option is turned off.

The entrypoint preserves the legacy agent's dashed startup banner because
operator diagnostics and end-to-end tests consume that output.
Before each step that runs, it also prints the legacy-compatible execution
header: `MODE PATH ARGUMENTS RETURNCODES IDEMPOTENCE ON_HOST`.

### Package artifacts

When `NODEWRIGHT_PACKAGE_REF` is set, the agent installs the package from the
[package artifact](../docs/user-guide/package-artifact-spec.md) it names rather
than from a package image.

The agent takes registry credentials from these sources, in the order the
kubelet takes them for an image pull:

1. Each file `NODEWRIGHT_REGISTRY_AUTH_FILES` lists, in order: the
   `.dockerconfigjson` of each image pull Secret.
1. The node's Docker configs that the kubelet reads, resolved through the root
   mount: `/var/lib/kubelet/config.json`, `/root/.docker/config.json` and
   `/.docker/config.json`.
1. The kubelet's image credential provider plugins, as
   [Credential provider plugins](#credential-provider-plugins) describes.
1. Anonymous access, only when no source holds a credential for the registry.

A source that is missing, unreadable or malformed, or that has no entry for the
registry, is skipped, and credential helpers are not run. Each credential found
is tried in order while the registry refuses the previous one. The agent logs
every source, with the reason for each one it skipped, and never logs a secret.

The kubelet merges all of a pod's pull secrets into one keyring ordered by how
specifically each key matches the image, and all of its plugins' answers into
another. The agent takes each file and each plugin on its own, in order, so
when two pull secrets, or two plugins, both hold a credential for the registry,
the agent tries the first listed first, where the kubelet tries the one with
the more specific key first.

#### Credential provider plugins

The agent finds the kubelet among the host's processes, which it sees with
`hostPID`, by an `argv[0]` named `kubelet` in the mount and PID namespaces of
process 1: any container on the node can name a process `kubelet`, but its
processes run in a mount namespace of their own. It reads the
`--image-credential-provider-config` and `--image-credential-provider-bin-dir`
flags the kubelet was started with, in the `--flag=value` or `--flag value`
form; the KubeletConfiguration file cannot set either. Either one under the
kubelet's pod directory, `<--root-dir>/pods` (`/var/lib/kubelet/pods` by
default), where pods write their volumes, is refused. It reads the
`CredentialProviderConfig`, a JSON or YAML file or a directory of them, of
`apiVersion` `kubelet.config.k8s.io/v1`, `v1beta1` or `v1alpha1`, through the
root mount.

For each provider, in the configured order, whose `matchImages` match the
image name, it runs `<bin dir>/<name>` chrooted into the root mount, with the
configured `args`, the kubelet's environment plus the configured `env`, and a
`CredentialProviderRequest` of the provider's `apiVersion` on stdin whose
`image` is the image name without tag or digest, as the kubelet does. A run is
killed after the kubelet's one minute, or sooner when the agent's own time is
up. The `auth` entries of the response are matched as the entries of a Docker
config, labelled `credential provider <name>` in the logs; `cacheKeyType` and
`cacheDuration` are checked as the kubelet checks them, and nothing is cached.

A plugin is skipped, with the reason logged, when:

- its provider sets `tokenAttributes`, since the plugin then needs a service
  account token of the pod an image is pulled for, and the agent has no such
  pod;
- it fails, times out, or answers what the kubelet would refuse. The reason
  never quotes the answer, which holds the credential.

Without a kubelet, or with no plugins configured, the agent logs why once and
goes on with the other sources. A kubelet that is not a process named
`kubelet`, such as one embedded in another binary, is not found.

`apply`, `config`, `uninstall`, `upgrade` and `post-interrupt` lay the payload
out and replace `COPY_DIR` atomically, carrying over everything `COPY_DIR`
already holds that the payload lacks. As with the merging `cp` of an image pod,
a payload file wins at its path, and a file a step left in `COPY_DIR` in an
earlier stage is still there in a later one.
The `-check` modes and `interrupt` reuse an existing `COPY_DIR` without
contacting the registry, and lay the payload out only when it is absent.
A registry request that sends nothing for 5 minutes, neither its response nor
a byte of its body, fails the stage, as containerd by default cancels an image
pull that makes no progress for as long; a layer that keeps arriving, however
slowly, is not cut off.

The agent refuses each of the following, exiting 1 before it writes anything
to `COPY_DIR`:

- A container image reference.
- An unsupported or invalid artifact, with the reason the `artifact` module
  gives.
- An artifact that declares host-files layers or an interrupt. This agent
  version supports neither, and never silently ignores them.
- `uninstall` of an artifact whose metadata declares uninstall unsupported, and
  `uninstall-check` of one when it has to lay the payload out. The artifact's
  other modes still run.

Everything after layout is unchanged, the startup banner included. The agent
logs the artifact reference and digest through `slog`.

### Preflight

`agent preflight ROOT_MOUNT` inspects the reference `NODEWRIGHT_PREFLIGHT_REF`
names, with the registry credentials the node's kubelet would use, and writes
what it found as a `nodewright.nvidia.com/preflight/v1` document, the `Result`
of the `artifact/preflight` package, to `/dev/termination-log`, where the
operator reads it from the container's termination message. It takes no
`COPY_DIR`, reads no `config.json`, prints no startup banner and changes
nothing on the host.

- `NODEWRIGHT_PREFLIGHT_REF` is the reference to inspect, as
  `registry/repository:tag` or `registry/repository@sha256:<hex>`. The result
  echoes it exactly as given.
- `NODEWRIGHT_REGISTRY_AUTH_FILES` and the other credential sources are those
  of [Package artifacts](#package-artifacts).

The result is one of `artifact`, with the digest the reference resolved to and
the artifact's uninstall declaration; `image`, with the digest; `rejected`,
with the reason, for an artifact this version cannot install; or `unknown`,
with the error class, for a reference that could not be inspected, such as
`invalid-reference` for an empty or malformed one. It names the source of the
credential the registry accepted, never the credential. The whole preflight
gets 90 seconds, each registry request 10 seconds, and a SIGTERM ends it with
an `unknown` result of class `canceled`.

`preflight` exits 0 whenever it writes a result, whatever the result is, 1
when it cannot encode or write one, and 2 for other arguments.

### Container image build

`containers/agent.Dockerfile` compiles a static `CGO_ENABLED=0` binary from the
vendored module and copies it into `nvcr.io/nvidia/distroless/static`, running
as root so it can chroot into the host. CI (`.github/workflows/agent-ci.yaml`)
builds it for `linux/amd64` and `linux/arm64`, smoke-tests each with
`agent --version`, pushes a multi-arch manifest to `ghcr.io/nvidia/nodewright/agent`,
and on an `agent/v*` tag also signs it, attaches an SBOM and attests provenance.
`make docker-build` builds the same image locally for the current platform.

The Python implementation shipped as `agent/v6.x`; the Go implementation
continues the same version stream from `agent/v7.0.0`. Both write and honour
the same on-host state, so a node can move between the two in either direction
without re-running completed steps or interrupts, with one exception: the Go
agent records a started `node_restart` as a pending marker until the host boot
ID changes, and the Python agent does not read that marker, so a reboot the Go
agent started but had not yet confirmed runs once more after a rollback to
`v6.x`.

## Environment variables

There are a number of environment variables that can be used to control how the agent works.

1. `COPY_RESOLV` if set to `"false"` it will NOT copy the container's `/etc/resolv.conf` to the host.
1. `OVERLAY_ALWAYS_RUN_STEP` if set to `"true"` it will ignore any step flags and always run every step. A warning is logged if it sees a flag file.
1. `SKYHOOK_AGENT_WRITE_LOGS` defaults to `"true"`. Step and interrupt output is streamed directly to stdout/stderr and also written under `SKYHOOK_LOG_DIR`. Set it to `"false"` to stream without retaining host log files.

The following environment variable is required and is expected to be set by
the NodeWright operator. It is not recommended that it be changed manually.

1. `SKYHOOK_RESOURCE_ID` is used to determine if an interrupt should be rerun. Interrupts are only run once per `SKYHOOK_RESOURCE_ID`. The NodeWright operator makes this unique per package configuration.

The following environment variables are optional and use the documented defaults when unset:

1. `SKYHOOK_DATA_DIR` is the package data source used by legacy invocations when the operator has not already populated `COPY_DIR`. It defaults to `/skyhook-package`. For a [package artifact](#package-artifacts), its files are instead overlaid onto the laid-out payload, following symlinks such as the keys of a configMap volume; in an artifact pod it holds only the mounted `configmaps/` and `node-metadata/`. A mounted file wins over a payload file of the same path. No directory mode is taken from it: a directory keeps the mode the payload or the existing `COPY_DIR` gave it, and one neither has, such as `node-metadata/` on the first layout, is created 0755. If it is absent there is nothing to overlay. The legacy `/etc/nvidia-bootstrap/node-files` copy does not happen on this path.
1. `SKYHOOK_ROOT_DIR` is the host state root for flags, interrupt markers, and history. It defaults to `/etc/skyhook`.
1. `SKYHOOK_LOG_DIR` is the host log root. It defaults to `/var/log/skyhook`.

The following environment variables are optional:

1. `NODEWRIGHT_PACKAGE_REF` names the [package artifact](#package-artifacts) to install, as `registry/repository@sha256:<hex>` with the digest the package's preflight recorded on the node. The operator sets it only for package artifacts, on the stage and check agent containers of a stage Job, and never on interrupt Jobs. When it is unset, nothing changes: the agent takes the package-image path.
1. `NODEWRIGHT_REGISTRY_AUTH_FILES` lists, separated by `:`, the paths inside the container to read-only Docker config JSON files, the `.dockerconfigjson` of each image pull Secret, in the order they are tried. A path that does not exist is skipped. The operator sets it for a package artifact's stage containers and for `preflight`.
1. `NODEWRIGHT_PREFLIGHT_REF` names the reference [`preflight`](#preflight) inspects. Only `preflight` reads it.
1. `SKYHOOK_NODE_ORDER` is a zero-indexed monotonic position of this node in the rollout. The first batch's nodes get `0, 1, 2, ...` and subsequent batches continue from where the previous batch left off. Useful for kubeadm upgrade workflows where the first node (`SKYHOOK_NODE_ORDER=0`) runs a different command than subsequent nodes. See [Node Order Within a Rollout](../docs/architecture/ordering.md#node-order-within-a-rollout) for details.
