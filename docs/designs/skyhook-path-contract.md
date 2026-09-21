# On-host path compatibility contract

This design records the v1 decision for the paths used by NodeWright package
images and the agent's persistent state.

## Decision

Keep the existing paths as stable compatibility surface in v1. Do not rename
them or require a `/nodewright-*` replacement.

`/skyhook-package` is read by every existing package image, including package
step scripts that use the path directly. Renaming it would break already-built
images without negotiation. `/var/lib/skyhook`, `/var/log/skyhook`, and
`/etc/skyhook` contain persistent flags, logs, and history; renaming them would
also strand state on every existing node and could cause work to run again.

The historical `skyhook` component of these paths is therefore a compatibility
identifier, not a promise that every internal name will be renamed. This
decision is independent of the `SKYHOOK_*` environment-variable decision in
[the environment contract design](skyhook-environment-contract.md).

## Stable paths

| Path | Purpose |
| --- | --- |
| `/skyhook-package` | Package payload root and `config.json` source. |
| `/skyhook-package/configmaps` | ConfigMap data mounted for package execution. |
| `/skyhook-package/node-metadata` | Node metadata mounted for package execution. |
| `/etc/skyhook` | Agent state root for flags, interrupt markers, and history. |
| `/var/log/skyhook` | Agent log root. |
| `/var/lib/skyhook` | Host copy directory for package contents. |

The operator and agent must continue to use these paths consistently. New
documentation should describe them as stable package/runtime contract rather
than suggesting that a rename is pending.

## Future migration rule

A future major release may revisit these names, but only with all of the
following:

1. a package-image compatibility window that supports old and new paths;
2. dual mounts or a well-defined fallback for `/skyhook-package`;
3. a one-shot, restart-safe migration for persistent state directories;
4. an explicit rollback plan that preserves flags and logs; and
5. release notes and package-author documentation describing the breaking
   change.

Until then, a symlink or alternate mount must not be introduced as an
unreviewed partial migration: it could hide missing state or make old and new
writers operate on different directories.
