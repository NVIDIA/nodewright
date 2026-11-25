# Package Policy Install Test

Installs the chart with `packagePolicy` enabled on a real cluster and checks what only a real install shows: that Helm applies the ValidatingAdmissionPolicy and its binding, that the apiserver enforces them next to the running operator and its webhook, and that `helm uninstall` still cleans up under the policy. What the policy decides in every case is covered by the envtest suite in `operator/internal/packagepolicy`; rendering and values by `helm-template-test`.

## Steps

1. **install-with-the-policy** -- installs the chart with `values.yaml` (one deny entry, one allow rule for the user `policy-test-user`) and asserts the operator is running and the policy and binding exist. `rbac.yaml` lets the test user write NodeWright CRs, so the policy is the only gate under test.
2. **refuses-a-package-no-allow-rule-covers** -- the test user's NodeWright with an image no allow rule covers is refused with the policy's message. Retries until the policy and the operator's webhook are both serving.
3. **admits-a-granted-package-that-the-operator-then-reconciles** -- the test user's NodeWright with a granted image is admitted, and the operator adds its finalizer, which shows its own writes pass the policy.
4. **lets-a-deny-entry-win-over-a-granted-allow-rule** -- a `shellscript` image, which the allow rule's images also match, is refused by the deny entry.
5. **uninstall-still-cleans-up-every-cr** -- `helm delete` runs the pre-delete cleanup Job, which deletes every CR as the operator's ServiceAccount while the policy is still installed. Helm keeps the NodeWright CRD, so any CR left afterwards means the policy refused the cleanup. The policy and binding are gone afterwards.

The NodeWright CRs select no node, so the operator reconciles them without running packages.
