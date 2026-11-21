# Kyverno Policy Examples for NodeWright

This directory contains example [Kyverno](https://kyverno.io/) policies for use with NodeWright. These are **not installed by default** and are provided as templates for users to adapt to their own security needs.

To restrict which packages, registries and versions may run, and who may change them, start with the chart's built-in [package policy](../package-policy.md) instead: it needs no policy engine, normalises short image names, and covers both the `NodeWright` and legacy `Skyhook` kinds. Use Kyverno for rules outside that model.

Any policy written against kind `Skyhook` must also match kind `NodeWright`, or it does not apply to NodeWright CRs at all.

- `disable_packages.yaml`: Example policy to restrict or disable certain NodeWright packages/images.
- `skyhook-viewer-binding.yaml`: Example RBAC binding for Kyverno to view NodeWright resources.

**Note:**

- This directory was previously at the repo root and has been moved to `docs/security/kyverno/` for clarity.
- If you use these policies, ensure you enable the `skyhook-viewer-role` in your Helm values and bind Kyverno to that role.

See the main [README](../../README.md) for more information about NodeWright. 
