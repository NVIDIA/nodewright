---
summary: "Mirror every change between kustomize and Helm in the same PR"
paths:
  - "operator/config/**"
  - "chart/**"
---
# Kustomize and Helm stay in sync

## Read before editing

- `docs/operations/kubernetes-support.md`: supported Kubernetes versions, which bound what the manifests may use.
- `docs/operations/versioning.md`: the chart is versioned separately from the operator.

## Rules

`operator/config/` (kustomize) and `chart/` (Helm) describe the same deployment surface through two tools. A change to one **must be mirrored into the other in the same PR**. A new RBAC rule, env var, volume mount, resource limit, webhook wiring or CRD under `operator/config/` is half done until the matching edit lands under `chart/templates/`, plus `chart/values.yaml` when it is a knob.

The chart was generated from `operator/config/` with `helmify` (`make generate-helm` in `operator/`) and has been hand-edited since. `make generate-helm` can draft a mirror, but it overwrites hand-tuned chart files: read the diff, revert what it gets wrong (templating variables, comments, ordering), and never commit its output blindly. For small changes, mirroring by hand is usually faster.

`chart/LICENSE` and `chart/NOTICE` are regular-file copies of the root files, not symlinks (`make chart-license-check` enforces it).
