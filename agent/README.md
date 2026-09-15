
A basic example of using a container overlay

## Development

### To build locally:

1. `make test`
1. `make build`

### Development workflow

1. Do code changes
1. Write unit tests for code changes
1. Run `make test` to run the tests
1. Run `make fmt` to format the code
1. Push code to and make an MR

### Go agent execution contracts

The Go rewrite under `agent/go` shares execution policy between steps and
interrupts through `execution.Config`. A `Config` composes the host root mount,
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

Each Go interrupt owns its command construction and execution. The `Interrupt`
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

The Go entrypoint accepts the current operator forms:

```text
agent MODE ROOT_MOUNT COPY_DIR
agent interrupt ROOT_MOUNT COPY_DIR INTERRUPT_DATA
agent --version
```

It also accepts the legacy forms, which default `ROOT_MOUNT` to `/root`:

```text
agent MODE COPY_DIR
agent interrupt COPY_DIR INTERRUPT_DATA
```

SIGTERM cancels the active step or interrupt and prevents later steps from
starting. A failed operation or runtime error exits with status 1; malformed
arguments exit with status 2.

`agent --version` prints the semantic version embedded in the binary at build
time, falling back to the embedded Git SHA or `unknown` when build metadata is
omitted. It exits without preparing or executing a package.

The entrypoint preserves the legacy agent's dashed startup banner because
operator diagnostics and end-to-end tests consume that output.
Before each step that runs, it also prints the legacy-compatible execution
header: `MODE PATH ARGUMENTS RETURNCODES IDEMPOTENCE ON_HOST`.

### Container Image Build

The production legacy image continues to build from
`containers/agent.Dockerfile`. During pre-cutover validation, CI builds and
smoke-tests the Go agent separately from `containers/agent-go.Dockerfile`, but
does not publish it. Agent release tags continue to publish only the production
legacy image until the full cutover.

## Environment variables

There are a number of environment variables that can be used to control how the agent works.

1. `COPY_RESOLV` if set to `"false"` it will NOT copy the container's `/etc/resolv.conf` to the host.
1. `OVERLAY_ALWAYS_RUN_STEP` if set to `"true"` it will ignore any step flags and always run every step. A warning is logged if it sees a flag file.
1. `SKYHOOK_AGENT_WRITE_LOGS` defaults to `"true"`. Step and interrupt output is streamed directly to stdout/stderr and also written under `SKYHOOK_LOG_DIR`. Set it to `"false"` to stream without retaining host log files.

`SKYHOOK_AGENT_BUFFER_LIMIT` is printed in the startup banner for legacy output
compatibility, but it has no effect in the Go agent. The Go agent streams
command output directly and does not buffer it.

The following environment variable is required and is expected to be set by
the NodeWright operator. It is not recommended that it be changed manually.

1. `SKYHOOK_RESOURCE_ID` is used to determine if an interrupt should be rerun. Interrupts are only run once per `SKYHOOK_RESOURCE_ID`. The NodeWright operator makes this unique per package configuration.

The following environment variables are optional and use the documented defaults when unset:

1. `SKYHOOK_DATA_DIR` is the package data source used by legacy invocations when the operator has not already populated `COPY_DIR`. It defaults to `/skyhook-package`.
1. `SKYHOOK_ROOT_DIR` is the host state root for flags, interrupt markers, and history. It defaults to `/etc/skyhook`.
1. `SKYHOOK_LOG_DIR` is the host log root. It defaults to `/var/log/skyhook`.

The following environment variable is optional:

1. `SKYHOOK_NODE_ORDER` is a zero-indexed monotonic position of this node in the rollout. The first batch's nodes get `0, 1, 2, ...` and subsequent batches continue from where the previous batch left off. Useful for kubeadm upgrade workflows where the first node (`SKYHOOK_NODE_ORDER=0`) runs a different command than subsequent nodes. See [Node Order Within a Rollout](../docs/architecture/ordering.md#node-order-within-a-rollout) for details.
