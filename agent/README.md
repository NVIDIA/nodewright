

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

### Container Image Build 

1. Do code changes
1. Run `test` and `format` from above
1. If using private registry set registry address and image path using `REGISTRY` and `AGENT_IMAGE` environment variables
1. Run `make docker-build` to build the container

## Environment variables
There are a number of environment variables that can be used to control how the controller works
1. `COPY_RESOLV` if set to `"false"` it will NOT copy the container's `/etc/resolv.conf` to the host.
1. `OVERLAY_ALWAYS_RUN_STEP` if set to `"true"` it will ignore any step flags and always run every step. A warning will be printed to stdout if it sees a flag file.
1. `SKYHOOK_AGENT_BUFFER_LIMIT` defaults to 8KB. This is how much of the log of each step it will read before syncing the data to stdout/stderr and the log file. It is recommended to keep this somewhat low to avoid excessive delay between a step emitting some information and seeing it in the docker logs or in the log file.

The following are enviroment variables expected to be set by either the build system or skyhook-operator. It is not recommended they be changed manually.
1. `OVERLAY_FRAMEWORK_VERSION` this the version of the current overlay. It is expected that this gets set by the docker build system. It is required to be able to manage the history file. It must be in the format of `{package name}-{version}`
1. `SKYHOOK_RESOURCE_ID` this is used to determine if an interrupt should be rerun. Interrupts are only run once per `SKYHOOK_RESOURCE_ID`. Skyhook operator should make this unique per conifguration of the package.
2. `SKYHOOK_NODE_ORDER` zero-indexed monotonic position of this node in the rollout. The first batch's nodes get `0, 1, 2, ...` and subsequent batches continue from where the previous batch left off. Useful for kubeadm upgrade workflows where the first node (`SKYHOOK_NODE_ORDER=0`) runs a different command than subsequent nodes. See [Node Order Within a Rollout](../docs/ordering_of_skyhooks.md#node-order-within-a-rollout) for details.