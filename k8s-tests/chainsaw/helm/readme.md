## Helm Tests

This directory holds all the tests for the nodewright operator's helm chart. It covers template rendering, that tolerations and node affinity set in the chart actually take effect, that the admission webhooks reject invalid resources, that the opt-in package policy is enforced on a real install and still lets `helm uninstall` clean up (`helm-package-policy-test`), and that the operator deploys successfully under an overridden name.

`helm-upgrade-rollback-test` is the odd one out: it installs the last pre-rename chart release from the registry, upgrades the working tree's chart over it, and then rolls back. It exists because in-cluster resource names can only be renamed safely once, and a local render cannot reproduce the pre-rename objects the upgrade has to clean up or the rollback has to restore. Its rollback half is the regression test for #469, where rolling back across the rename wedged with no Ready operator pod, so a failure there means that has come back.

## Test Image

The image that is used by these tests should be `ghcr.io/nvidia/skyhook/operator:test` (NOTE: this does not exist in the current github CI, this needs to be fixed) since this will be built in CI every time a commit is pushed to Gitlab and will make sure that you current changes to the operator are compatible with the helm chart still. 

**NOTE:** When you run the helm chart tests locally it may be using an outdated version of the test image since it hasn't been pushed and built by the CI. Be careful in the assumptions you make as your changes to the operator may pass the helm chart tests locally but fail in CI.
