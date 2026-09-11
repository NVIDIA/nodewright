# NodeWright Versioning Strategy

NodeWright uses independent versioning for three components, all following [Semantic Versioning](https://semver.org/):

1. **Operator** - Kubernetes operator that manages NodeWright resources
2. **Agent** - Container that executes package operations on nodes  
3. **Chart** - Helm chart for deploying the operator
   1. NOTE: the versioning of the chat also includes versioning of the expected agent version. 
   2. NOTE: If changing either the operator or agent this will generally include a chart release too.

## Git Tagging Convention

```bash
operator/v{version}    # Operator releases
agent/v{version}       # Agent releases  
chart/v{version}       # Chart releases
```

## Component Versioning

### Operator & Agent

- **Independent versioning** with their own release cycles
- **Semantic versioning**: MAJOR.MINOR.PATCH
- **Compatibility**: Maintained through well-defined interfaces

During validation of the Go agent rewrite, an `agent/vx.y.z` tag continues to
publish only the production legacy agent image. CI builds and smoke-tests the
Go agent container for relevant changes, but release automation does not
publish that image before the full cutover. The Go implementation will continue
the existing Agent version stream rather than introduce a separate component
version.

### Helm Chart  

- **Independent from operator/agent** (starting at v0.8.0 these will start to diverge in version number)
- **Chart version** (`version`): Tracks chart template/config changes
- **App version** (`appVersion`): Recommended stable operator version

## Chart Behavior

### Chart.yaml

example:
```yaml
version: v1.0.0        # Chart version (independent)
appVersion: v0.7.0   # Recommended operator version
```

### Image Tag Defaults

```yaml
# values.yaml
image:
  tag: ""  # Empty = defaults to Chart.AppVersion

# Template renders as:
image: "ghcr.io/nvidia/skyhook/operator:0.7.0"
```

### Image Pinning: Tag vs Digest

- You can specify either a tag or a digest for images.
- If both are provided, the **digest takes precedence** and determines the image pulled. The rendered image reference may display as `:tag@sha256:...`, but the digest controls selection.

## Release Branching Strategy

NodeWright uses **release branches** to manage patches and maintenance releases:

```bash
release/v0.8.x    # Contains operator v0.8.0 + agent v6.3.0 + chart v0.8.x
release/v0.9.x    # Contains operator v0.9.0 + (agent v6.3.0*) + chart v0.9.x
release/v0.10.x   # Contains operator v0.10.0 + (agent v6.3.0*) + chart v0.10.x
```
*Agent versions may not change every release - operator drives the release cycle

### Why Release Branches:

- **Operator-centric releases** - most releases are driven by operator features and bugs
- **Chart defines compatibility** - each branch contains a tested, compatible set of all components  
- **Agent follows operator** - agent changes typically only require chart patch releases
- **Simplified patches** - fix bugs in the context of the full integrated system
- **Connected git history** - preserves relationships between operator, agent, and chart changes

### Branch Workflow:

1. **Main development** happens on `main` branch
2. **Release preparation** creates `release/v{MAJOR.MINOR}.x` branch (typically driven by operator changes)
3. **Patch releases** are developed and tagged from release branches
4. **Agent-only changes** usually result in chart patch releases (no new release branch)
5. **Critical fixes** may be backported from `main` to release branches

## Go Module Support

The operator supports Go module imports for external projects:

```bash
# External projects can import the operator
go get github.com/NVIDIA/nodewright/operator@v0.8.0
```

**Module mapping**: Tag `operator/v0.8.0` maps to module `github.com/NVIDIA/nodewright/operator@v0.8.0`

## Quick Reference

> These examples use `nodewright`, the default install namespace for new installs. Substitute your own if you installed elsewhere; installs predating the namespace rename are in `skyhook`.

```bash
# Check deployed versions
kubectl get deployment -n nodewright -o jsonpath='{.items[0].spec.template.spec.containers[0].image}'
helm list -n nodewright

# Override operator version
helm install skyhook ./chart --set controllerManager.manager.image.tag="0.8.0"

# Check release branches
git branch -r | grep release/
```

## Changelogs

Each component has a machine-generated `CHANGELOG.md` (regenerated from git history by `scripts/gen-changelog.sh`; do not hand-edit) and a human-authored `RELEASE_NOTES.md` for behavior changes and upgrade steps. See the "Changelogs and Release Notes" section of [release-process.md](../contributing/release-process.md) for the tooling and the generate-vs-curate split.

## Release Process

For step-by-step instructions on how to release components, see [release-process.md](../contributing/release-process.md).

**CI/CD triggers on git tags:**

- `operator/vx.y.z` → publishes operator image
- `agent/vx.y.z` → publishes agent image  
- `chart/vx.y.z` → publishes helm chart

**Chart versioning:**

- **PATCH**: Bug fixes, docs
- **MINOR**: New features, config options  
- **MAJOR**: Breaking changes to chart, or compatibility with agent or operator.
