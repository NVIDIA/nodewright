# Resource Management in NodeWright

NodeWright provides flexible resource management for the pods it creates: it uses a namespace LimitRange for defaults when one is present (the chart installs one by default, which you can disable) and allows per-package overrides. This document explains how resource defaults and overrides work, and what validation rules are enforced.

---

## 1. Namespace Defaults with LimitRange

By default, NodeWright uses a [Kubernetes LimitRange](https://kubernetes.io/docs/concepts/policy/limit-range/) to set default CPU and memory requests/limits for all containers in the namespace where NodeWright operates.

**Example LimitRange:**
```yaml
apiVersion: v1
kind: LimitRange
metadata:
  name: skyhook-default-limits
  namespace: <your-namespace>
spec:
  limits:
    - type: Container
      default:
        cpu: 500m
        memory: 512Mi
      defaultRequest:
        cpu: 250m
        memory: 256Mi
```

- If a pod/container does **not** specify its own resources, these defaults are applied.
- You can configure these values via the Helm chart or Kustomize overlays.

---

## 2. Per-Package Resource Overrides

You can override the default resource requests/limits for each package in your NodeWright Custom Resource (CR). This is done in the `resources` field for each package:

**Example:**
```yaml
spec:
  packages:
    mypackage:
      version: 1.0.0
      image: ghcr.io/nvidia/skyhook-packages/shellscript
      resources:
        cpuRequest: "200m"
        cpuLimit: "400m"
        memoryRequest: "128Mi"
        memoryLimit: "256Mi"
```

- If **any** of the four fields (`cpuRequest`, `cpuLimit`, `memoryRequest`, `memoryLimit`) are set, **all four must be set** and must be positive values.
- If no override is set, the namespace's LimitRange applies.
- Interrupt pods are the exception: they run the agent and the interrupt (`reboot`, service restarts, or an interrupt script), not the package, so they always use a fixed `500m` CPU and `256Mi` memory (request equal to limit). Neither the LimitRange defaults nor a per-package override changes them. The LimitRange NodeWright installs sets no `max`; if you add a `max.memory` to the namespace, it must be at least `256Mi`, or Kubernetes rejects interrupt pods at admission and the interrupt never runs.

---

## 3. Validation Rules

NodeWright enforces the following validation rules (via webhook) for resource overrides:

- If any of the four resource fields are set, **all four must be set**.
- All values must be **positive**.
- `cpuLimit` must be **greater than or equal to** `cpuRequest`.
- `memoryLimit` must be **greater than or equal to** `memoryRequest`.

**Examples:**

| Valid? | cpuRequest | cpuLimit | memoryRequest | memoryLimit | Reason |
|--------|-----------|----------|--------------|-------------|--------|
| ✅     | 200m      | 400m     | 128Mi        | 256Mi       | All set, valid |
| ❌     | 200m      |          | 128Mi        | 256Mi       | Not all fields set |
| ❌     | 200m      | 100m     | 128Mi        | 256Mi       | cpuLimit < cpuRequest |
| ❌     | 200m      | 400m     | 128Mi        | 64Mi        | memoryLimit < memoryRequest |
| ❌     | 0         | 400m     | 128Mi        | 256Mi       | Zero value |

If a resource override is invalid, the NodeWright CR will be **rejected** by the webhook.

---

## 4. Best Practices

- Use LimitRange to set sensible defaults for your namespace.
- Only set per-package overrides if you need different resource requirements for a specific package.
- Review your resource settings to avoid overcommitting or underutilizing cluster resources.
- If you change LimitRange defaults, new pods will use the new defaults unless overridden.

---

## 5. Troubleshooting

- If your NodeWright CR is rejected, check that all four resource fields are set and valid if you are using overrides.
- Use `kubectl describe limitrange -n <namespace>` to see the current defaults.
- Use `kubectl describe skyhook <name>` to see the status and any error messages.

---

## 6. Disabling Resource Defaults (No Limits)

If you do **not** want any default resource requests or limits applied to your Skyhook-managed pods/containers, you can simply **omit the LimitRange** from your namespace:

- **Helm:** Set `limitRange: {}` or remove the `limitRange` section from your `values.yaml`.

If there is **no LimitRange** and you do **not** set resource requests/limits in your package overrides, then:

- Your pods/containers will run with **no resource requests or limits**.
- This means they will be scheduled as "BestEffort" pods, which may be evicted first under resource pressure and may not get guaranteed CPU/memory.

**Note:**

- Disabling resource limits is not recommended for production clusters, as it can lead to resource contention and unpredictable scheduling.
- Only do this if you have a specific reason and understand the implications.

---

## 7. Special Case: Uninstall Pod Resources

Uninstall pods in NodeWright **do not use per-package resource overrides**.  
Instead, their resource requests/limits are determined only by the namespace defaults:

- If a LimitRange is present in the namespace, uninstall pods will use those default CPU and memory requests/limits.
- If there is no LimitRange, uninstall pods will run as "BestEffort" (no resource requests/limits).
- Any `resources:` overrides set for the original package are **not applied** to the uninstall pod.

**Note:**

- Ensure defaults are big enough for uninstall processes if using the uninstall package life cycle.

---

For more information, see the [Kubernetes documentation on resource management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/) and [LimitRange](https://kubernetes.io/docs/concepts/policy/limit-range/). 
