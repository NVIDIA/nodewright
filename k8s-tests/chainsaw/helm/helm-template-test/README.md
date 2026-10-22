# Helm Template Rendering Tests

These tests validate that the Helm chart templates render correctly under various value configurations. Unlike the other helm tests in this directory, these tests do **not** require a running cluster -- they only use `helm template` and run in ~8 seconds.

## What It Tests

### Render Correctness

1. **default-limitrange** -- LimitRange renders with correct default values (500m/512Mi limits, 250m/256Mi requests)
2. **default-cleanup-skyhooks-job-resources** -- NodeWright cleanup job has correct resource limits/requests
3. **default-cleanup-webhook-job-resources** -- Webhook cleanup job has correct resource limits/requests
4. **limitrange-disabled-null-fallback-resources** -- `limitRange: null` still produces fallback resource limits in cleanup jobs
5. **limitrange-disabled-null-no-limitrange** -- `limitRange: null` omits the LimitRange resource
6. **limitrange-disabled-false-no-limitrange** -- `limitRange: false` omits the LimitRange resource
7. **webhook-disabled-no-webhook-cleanup-job** -- `webhook.enable: false` omits the webhook cleanup job
8. **cleanup-disabled-no-skyhook-cleanup-job** -- `cleanup.enabled: false` omits the nodewright cleanup job

### Validation Error Paths

1. **reject-default-namespace** -- Deploying to the `default` namespace produces an error
2. **reject-selector-and-affinity-conflict** -- Setting both `controllerManager.selectors` and `controllerManager.nodeAffinity.matchExpressions` produces an error
3. **reject-pdb-gte-replicas** -- `podDisruptionBudget.minAvailable >= replicas` produces an error
4. **reject-removed-image-pull-secret** -- Setting the removed `imagePullSecret` produces an error naming `imagePullSecrets`
5. **reject-invalid-runtime-required-taint** -- every `runtimeRequiredTaint` the operator would refuse to start with (DNS-prefixed or 54-character key, 64-character value, missing or unknown effect, deletion, malformed key or value) fails `values.schema.json`
6. **reject-null-runtime-required-taint** -- a null `runtimeRequiredTaint`, which Helm drops before validating and which would reach the operator as an empty env var, fails `values.schema.json` as a missing property
7. **accept-valid-runtime-required-taint** -- valid values, including the 53-character key and 63-character value boundaries, pass the schema and reach the Deployment env

### Feature Toggles

1. **rbac-roles-disabled-by-default** -- Viewer and editor ClusterRoles are absent by default
2. **rbac-viewer-role-enabled** -- `rbac.createSkyhookViewerRole: true` renders the viewer ClusterRole
3. **rbac-editor-role-enabled** -- `rbac.createSkyhookEditorRole: true` renders the editor ClusterRole
4. **image-pull-secrets** -- `imagePullSecrets` renders on every ServiceAccount (controller-manager, selector-migration, package) and on no pod template; the package ServiceAccount has `automountServiceAccountToken: false`
5. **operator-env-names-the-rendered-package-serviceaccount** -- `PACKAGE_SERVICE_ACCOUNT` names the rendered package ServiceAccount, with and without `fullnameOverride`
6. **host-network-disabled-by-default** -- `hostNetwork: true` is absent by default
7. **host-network-enabled** -- `useHostNetwork: true` renders `hostNetwork: true` in the Deployment
8. **metrics-binding-disabled-by-default** -- Prometheus ClusterRoleBinding is absent by default
9. **metrics-binding-enabled** -- `metrics.addServiceAccountBinding: true` renders the binding

### Cleanup Job Scheduling Parity

1. **cleanup-jobs-inherit-tolerations-skyhooks** -- Tolerations propagate to nodewright cleanup job
2. **cleanup-jobs-inherit-tolerations-webhook** -- Tolerations propagate to webhook cleanup job
3. **cleanup-jobs-inherit-selectors-skyhooks** -- Node selectors propagate to nodewright cleanup job
4. **cleanup-jobs-inherit-selectors-webhook** -- Node selectors propagate to webhook cleanup job
5. **cleanup-jobs-inherit-node-affinity-skyhooks** -- Node affinity propagates to nodewright cleanup job
6. **cleanup-jobs-inherit-node-affinity-webhook** -- Node affinity propagates to webhook cleanup job

## Template Scoping With `-s`

All tests use `helm template -s templates/<file>.yaml` (`--show-only`) to render a single template file instead of the entire chart. This is important because:

- **Isolation** -- assertions only see the output of one template, so a `contains()` check can't accidentally match content from a different resource
- **Absence detection** -- when a template is entirely gated by `{{- if }}` and the condition is false, `-s` causes helm to exit with an error (`could not find template`). This gives us a clean way to assert a resource is not rendered (Pattern 2 below)
- **Smaller output** -- structural assertions with `chainsaw assert` operate on a single resource instead of the full chart, making failures easier to diagnose

The only exceptions are the validation error tests, which render the full chart because the `{{- fail }}` directives in `validations.yaml` fire during full rendering regardless of `-s`.

## Assertion Patterns

These tests use three different assertion patterns depending on what's being checked. This section documents each pattern so new tests follow the same conventions.

### Pattern 1: Structural YAML assertion (`chainsaw assert` with heredoc)

Use when you need to verify specific fields exist at the correct YAML path in a rendered resource. This is the strongest assertion -- it uses chainsaw's own partial-match engine to compare a rendered resource against an expected YAML fragment.

```yaml
- script:
    env:
    - name: CHART
      value: ($CHART)
    - name: CHAINSAW_BIN
      value: ($CHAINSAW_BIN)
    content: |
      helm template test-release ${CHART} -n nodewright \
        -s templates/cleanup-skyhooks-job.yaml > /tmp/rendered.yaml
      cat <<'EXPECTED' | ${CHAINSAW_BIN} assert \
        --resource /tmp/rendered.yaml --file - --no-color --timeout 5s
      apiVersion: batch/v1
      kind: Job
      spec:
        template:
          spec:
            tolerations:
            - key: dedicated
              value: system-cpu
      EXPECTED
```

Key details:

- `-s templates/foo.yaml` renders only that one template (scoped output)
- The rendered YAML is written to a temp file (`/tmp/rendered.yaml`)
- The expected YAML is piped via heredoc to `chainsaw assert --file -`
- The expected YAML is a **partial match** -- only specified fields are checked
- On failure, chainsaw shows a diff with exact field paths (e.g., `spec.template.spec.tolerations[0].key`)

Used for: resource limits, tolerations, selectors, affinity, RBAC role structure, LimitRange values, ServiceAccount `imagePullSecrets`.

### Pattern 2: Absence check (`$error != null`)

Use when you need to verify a gated template produces **no output** when its condition is false. When `helm template -s` targets a template that is entirely gated by `{{- if }}` and the condition evaluates to false, helm exits with an error (`could not find template`).

```yaml
- script:
    env:
    - name: CHART
      value: ($CHART)
    content: |
      helm template test-release ${CHART} -n nodewright --set-json 'limitRange=null' \
        -s templates/limitrange.yaml
    check:
      ($error != null): true
```

Used for: disabled LimitRange, disabled webhook job, disabled cleanup job, disabled RBAC roles.

### Pattern 3: String contains (`contains($stdout, ...)`)

Use for simple presence/absence checks where structural assertion would be overkill -- typically a single boolean toggle or a string value in a large template. Always scope with `-s` to a specific template so the match can't accidentally hit the wrong resource.

```yaml
- script:
    env:
    - name: CHART
      value: ($CHART)
    content: |
      helm template test-release ${CHART} -n nodewright \
        --set 'useHostNetwork=true' \
        -s templates/deployment.yaml
    check:
      "(contains($stdout, 'hostNetwork: true'))": true
```

YAML quoting note: if the string inside `contains()` has a colon followed by a space (e.g., `'kind: LimitRange'`), the entire check key must be double-quoted or the YAML parser will error.

Used for: hostNetwork toggle, metrics ClusterRoleBinding presence, validation error messages.

### Pattern 4: Validation error check (`contains($stderr/stdout, ...)` with `|| true`)

Use when verifying that `helm template` **fails with a specific error message** (from `{{- fail }}` in `validations.yaml`). The `|| true` prevents the script from failing so chainsaw can inspect the output.

```yaml
- script:
    env:
    - name: CHART
      value: ($CHART)
    content: |
      helm template test-release ${CHART} -n default 2>&1 || true
    check:
      (contains($stdout, 'not allowed for security reasons')): true
```

Note: `2>&1` merges stderr into stdout since helm writes errors to stderr, and chainsaw's `check` only inspects `$stdout`.

Used for: namespace validation, selector/affinity conflict, PDB validation, removed `imagePullSecret`, and `values.schema.json` violations (runtime-required taint key).

## How It Differs From Other Helm Tests

The other tests in `k8s-tests/chainsaw/helm/` (e.g., `helm-chart-test`, `helm-scale-test`, `helm-node-affinity-test`) are **integration tests** that:

- Install the chart into a real cluster with `helm install`
- Apply NodeWright/DeploymentPolicy CRs
- Assert on live cluster state (running pods, scheduled workloads)
- Require a running operator and Kind cluster

This test is a **template rendering test** that:

- Only runs `helm template` (no cluster state changes)
- Validates the YAML output is structurally correct
- Runs in ~8 seconds with no cluster dependencies (though chainsaw does need a kubeconfig)
- Catches bugs like nil-guard issues, missing scheduling config, and broken validations before they hit a real cluster

Both types are complementary -- template tests catch rendering bugs fast, integration tests catch runtime behavior.

## Background

The cleanup job templates (`cleanup-webhook-job.yaml`, `cleanup-skyhooks-job.yaml`) had two issues:

1. They referenced `.Values.limitRange.default.cpu` without a nil guard, so setting `limitRange` to `null` or `false` caused a Helm render error
2. They didn't inherit the operator's scheduling constraints (tolerations, selectors, affinity), so cleanup jobs could fail to schedule on constrained clusters

These tests ensure those regressions don't reoccur, along with covering other chart configuration paths.
