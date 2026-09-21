# Configuration Updates

Changing a package's `configMap` in a NodeWright resource does not replace the
package ConfigMap immediately in every situation. The operator waits for a
safe point so that all nodes in the package's selection move to the new
configuration together.

## When the update is applied

The operator applies a changed `configMap` when either of these conditions is
true:

- Every selected node has finished the package and has no package Job or pod
  still running.
- At least one selected node is already erroring in the `config`, `interrupt`,
  or `post-interrupt` stage. The operator removes the in-flight executor for
  that stage so it can be recreated with the new configuration.

If neither condition is true, the operator records that the update is pending
and leaves the existing generated ConfigMap in place. It retries the update
after approximately 30 seconds and continues reconciling the package so the
running work can reach a safe point.

## Why the operator waits

Applying new configuration while a package is running would terminate a step
partway through its work and immediately restart it with different inputs. A
step may have completed only part of its change, and rerunning it is not
necessarily safe or idempotent. Waiting for the current run to finish or fail
keeps the next run's starting point known.

The gate is evaluated across the whole node selection, not just the nodes that
are currently free. Updating as individual nodes become available would leave
one part of the fleet using the old configuration and another part using the
new one. Waiting for the selection to settle moves the fleet from one
configuration to the next as a unit.

## How pods receive the new files

Each `configMap` key is mounted as an individual file under
`/skyhook-package/configmaps/<key>` using a Kubernetes `subPath` mount. This
preserves files shipped in the package image, but it also means that a running
pod does not receive live ConfigMap updates. A new configuration is visible
when the operator recreates the stage pod with the updated ConfigMap, or when
the package is recreated for a version change.

## If the package is stuck

Editing the NodeWright resource is not a way to force an in-progress package to
restart. If a package is genuinely stuck, use one of the normal recovery
actions instead:

- Run `kubectl nodewright reset <nodewright-name> --confirm` to clear the node
  state annotations and allow the lifecycle to start again.
- If the package needs to be retried without resetting the whole NodeWright,
  run `kubectl nodewright package rerun <package-name> --nodewright
  <nodewright-name> --node <node-name> --stage config --confirm`. This clears
  the package state for the selected node, allowing the next reconcile to apply
  the updated configuration.

Use the recovery action appropriate for your rollout and verify the resulting
node and package state before repeating the configuration change.

## Configuration interrupts

When a changed key has a matching `configInterrupts` entry, the package still
enters the `config` stage first and then follows the configured interrupt flow.
For a key whose change requires an interrupt, that flow includes
**Apply/Upgrade → Config → Interrupt → Post-Interrupt** after the node has been
cordoned and drained. Keys without a matching interrupt can be applied without
that additional interruption.

See [The NodeWright Custom Resource](custom-resource.md#configmap-and-configinterrupts)
for the `configMap` and `configInterrupts` syntax, and [Interrupt Flow and
Ordering](../architecture/interrupt-flow.md) for the complete lifecycle.
