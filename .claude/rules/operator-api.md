---
summary: "Run make manifests generate, never hand-edit generated files, update docs and CLI with a field"
paths:
  - "operator/api/**"
  - "operator/config/crd/**"
---
# CRD types and generated code

## Read before editing

- `docs/user-guide/custom-resource.md`: the field-by-field reference users read. A field change updates it in the same PR.
- `docs/operations/resource-management.md`: resource overrides are validated strictly, all four fields or none.
- `docs/security/kyverno/`: example policies users copy, which match CR fields; check them when a field they match changes.
- `docs/operations/versioning.md`: a CRD change usually means a version bump for the operator, and sometimes the chart and CLI.

## The CRDs

- **NodeWright** (`nodewright.nvidia.com/v1alpha1`, in `operator/api/nodewright/v1alpha1/`), and the legacy **Skyhook** (`skyhook.nvidia.com`, in `operator/api/v1alpha1/`, read-only for the migration window), both cluster-scoped. The desired state: a DAG of `packages` (container image, version, configMap, optional `dependsOn`), node selector, interruption budget, additional tolerations, the runtime-required flag, priority and sequencing, and an optional `DeploymentPolicy` reference. Because both are cluster-scoped, the install namespace only decides where the operator and its package pods live, never which CRs a client sees.
- **DeploymentPolicy** (`nodewright.nvidia.com/v1alpha1`, in `operator/api/nodewright/v1alpha1/`, and the legacy `skyhook.nvidia.com` copy in `operator/api/v1alpha1/`, read-only for the migration window), cluster-scoped: rollout shape, meaning batch sizing, pause and resume windows, and cross-CR ordering.

## Rules

- The types are kubebuilder-annotated (`//+kubebuilder:…`). After any change, run `make manifests generate` from `operator/`. The generated `zz_generated.deepcopy.go`, the CRD YAML under `operator/config/crd/bases/` and the webhook configuration are all consumed at build time.
- Never edit `zz_generated.*` or `operator/config/crd/bases/*` by hand. Change the source and regenerate.
- A new or changed field is a contract change. Check whether the CLI should surface it (see `.claude/rules/cli-contract.md`), and mirror any CRD change into `chart/` (see `.claude/rules/chart-sync.md`).
