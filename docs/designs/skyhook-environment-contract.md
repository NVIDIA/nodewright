# Package environment variable contract

This design records the v1 compatibility decision for the `SKYHOOK_*`
environment variables supplied to package steps by the NodeWright agent.

## Decision

Keep the existing `SKYHOOK_*` names as stable package-author contract surface.
Do not rename them or require a `NODEWRIGHT_*` replacement in v1.

The variables are read directly by package step scripts, not only by the
NodeWright agent. Renaming them would silently turn a non-empty value into an
unset variable for every existing package image. Dual-exporting a second name
would reduce migration pain, but would still create two names for the same
contract, require precedence rules when users set both, and provide little
benefit while the current names are already documented and working.

This decision does not rename the `skyhook` namespace, filesystem paths,
metrics, image names, or other compatibility surfaces. Those require their own
decisions and migration plans.

## Stable variables

| Variable | Contract |
| --- | --- |
| `SKYHOOK_RESOURCE_ID` | Identifies the package configuration used to gate interrupt reruns. |
| `SKYHOOK_AGENT_WRITE_LOGS` | Enables or disables retaining step and interrupt output under the agent log root. |
| `SKYHOOK_AGENT_BUFFER_LIMIT` | Sets the agent output buffer limit. |
| `SKYHOOK_DATA_DIR` | Package data source for legacy invocations; defaults to `/skyhook-package`. |
| `SKYHOOK_ROOT_DIR` | Host state root for flags, interrupt markers, and history; defaults to `/etc/skyhook`. |
| `SKYHOOK_LOG_DIR` | Host log root; defaults to `/var/log/skyhook`. |
| `SKYHOOK_NODE_ORDER` | Zero-indexed monotonic position of a node in a rollout. |
| `SKYHOOK_DIR` | Host path supplied to package steps for the copied package directory. |
| `SKYHOOK_NAME` | Legacy NodeWright/Skyhook name variable retained for package compatibility. |
| `SKYHOOK_CONFIGMAP_DIR` | ConfigMap mount path supplied to agent execution. |
| `SKYHOOK_AGENT_MODE` | Selects the agent execution mode. |
| `SKYHOOK_VAR` | Legacy agent variable retained for package compatibility. |
| `SKYHOOK_YAML` | Legacy package/configuration variable retained for compatibility. |
| `SKYHOOK_COMPLETE_TIMEOUT` | Legacy completion timeout variable retained for compatibility. |

The agent may add new variables in future releases, but an existing variable
must not be removed or changed from its documented meaning without a versioned
compatibility plan.

## Migration rule

If a future major release proposes new names, it must first provide:

1. a package-author migration guide;
2. a compatibility period in which old package images continue to work;
3. explicit precedence rules for old and new names;
4. tests covering both environments; and
5. a release-note warning that the package contract is changing.

Until those conditions are met, documentation and code should use the
`SKYHOOK_*` names, including `SKYHOOK_NODE_ORDER` in rollout examples.
