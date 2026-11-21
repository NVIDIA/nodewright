# Package Policy

Creating or updating a NodeWright is equivalent to root on every node its selector matches: `spec.packages[].image` names a container the operator runs privileged, with the host root filesystem mounted. RBAC on the NodeWright CRD is all or nothing, so anyone who can write a NodeWright can run any image on any node.

The package policy narrows that. It is an opt-in block of chart values that the chart renders into one [ValidatingAdmissionPolicy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/) and its binding, so the kube-apiserver itself refuses package changes the policy does not allow. With it you can say, for example:

- nothing from Docker Hub, and never the `shellscript` package;
- only packages `a` and `b` from registry `x` may run;
- only the GPU operator's ServiceAccount may add, change or remove the GPU driver packages.

The operator does not change, and the chart creates no Roles or bindings for it. It needs no other policy engine.

## Requirements and defaults

- **Off by default.** With `packagePolicy.enabled: false` the chart renders exactly what it renders without this feature, on any Kubernetes version.
- **Kubernetes 1.30 or newer when enabled**, for `admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy. The chart fails the install with a message naming the version it found. When you render offline with `helm template`, pass `--kube-version` for the target cluster; Argo CD and Flux pass it for you.
- **Nothing is restricted beyond what you write.** Enabled with no `deny` entries and no `allow` rules, everything is allowed, as before, with one exception: a package whose `image` carries its own tag or digest, or whose `name` differs from its key, is refused (see [Matching](#matching)). The NodeWright webhook refuses both too.

## The model

```yaml
packagePolicy:
  enabled: true
  mode: Deny                  # Deny | Warn | Audit
  deny:                       # always wins, applies to everyone
  - registries: ['docker\.io']
  - images: ['.*/shellscript']
  allow:                      # empty: anything not denied is allowed
    <name>:                   # one or more: adding or changing what no allow rule covers is DENIED
      # who may use the rule: anyone: true, or subjects and/or rbac: true
      anyone: false
      subjects: [{kind: ServiceAccount, namespace: ..., name: ...}]
      rbac: false
      # what the rule covers: registries and/or images are required
      registries: [...]
      images: [...]
      versions: [...]         # default: any
      shas: [...]             # exact sha256:<64 hex> digests; implies requireDigest
      operations: [...]       # default: [Add, Update, Remove]; starting an uninstall needs Remove
      agentImages: [...]      # default: agentImageOverride not allowed
      requireDigest: false    # true: containerSHA must be set
```

Order never matters. An Add or Update that matches a deny entry is refused, whichever allow rules also match it (removals and updates that only start an uninstall never are, see [Retiring packages](#retiring-packages)); a change two allow rules match is allowed if either permits it.

### Matching

- `registries`, `images`, `versions` and `agentImages` are [RE2](https://github.com/google/re2/wiki/Syntax) regexes, and **always match the whole string**: the chart wraps each one as `^(?:...)$`, so `nvcr\.io` never matches `evil.example/nvcr.io/x`. Do not add `^` or `$` yourself. Escape dots (`nvcr\.io`); in YAML, single-quote the patterns.
- `shas` match `containerSHA` exactly.
- Within an allow rule or deny entry, every field it sets must match; within a list, any entry may match. `images: [a, b]` with `versions: [x, y]` allows every combination, so pin different versions per image with two allow rules.
- **Image names are normalised before matching**, following the reference grammar the container runtime uses. A first path segment that has no `.` or `:`, is not `localhost` and is all lower case is a Docker Hub path, so `myorg/pkg` matches as `docker.io/myorg/pkg` and `ubuntu` as `docker.io/library/ubuntu`. Hosts are lower-cased, a `:443` port is dropped, and `index.docker.io`, `registry-1.docker.io` and `registry.hub.docker.com` all become `docker.io`. `registries` patterns match the host; `images` patterns match `host/path`; `agentImages` patterns match the normalised override including its tag or digest.
- An `image` with a tag or digest of its own (`nvcr.io/x/a:1.2`, `nvcr.io/x/a@sha256:...`) is refused on Add and Update whatever the rules say, as the NodeWright webhook also does: the tag comes from `version` and the digest from `containerSHA`. So is a package whose `name` field is set to something other than its key in `spec.packages`. The policy refuses both itself, so it does not depend on the webhook running.

An invalid regex fails `helm install` with `error parsing regexp`, and an unknown key fails it too, so a typo such as `imgaes:` cannot produce an allow rule with no constraints.

**Matching is on strings, so a deny list is a filter, not a guarantee.** It catches the spellings above, but a registry reachable under another hostname, another port, or a mirror is a different string. `versions` constrains the tag as written, and does not decide what runs once `containerSHA` is set, since the operator then pulls by digest. A version may carry a leading `v`; write patterns that allow for it. The guarantee is an allow list, enforced with `mode: Deny`, in which every allow rule covering an image sets `shas`: rules combine as a union, so a second rule covering the same image without `shas` admits it unpinned to whoever that rule grants.

### Deny-list mode and allow-list mode

`allow` is the switch between two modes:

- **No allow rules: deny-list mode.** Anything not denied is allowed.
- **One or more allow rules: allow-list mode.** Adding or changing a package is allowed only if some allow rule covers it for a requester who may use that rule; retiring one follows [Retiring packages](#retiring-packages). Allow rules combine as a union.

**Adding the first allow rule therefore refuses every package no allow rule covers, for everyone.** Adding one narrow `gpu-drivers` rule to a deny-only policy also blocks every other package, unless another allow rule (for example an `anyone: true` rule) covers it. Add the first allow rule with `mode: Warn` and watch for warnings before enforcing.

### Who may use an allow rule

Each allow rule chooses how it is granted:

| Grant | Who | Changing it |
|---|---|---|
| `subjects` | The named ServiceAccounts, Users and Groups, written like RoleBinding subjects. **The default way to grant, needing no RBAC objects.** | Helm upgrade |
| `anyone: true` | Anyone who can write NodeWright CRs | Helm upgrade |
| `rbac: true` | Anyone RBAC grants the virtual verb `use` on the rule | Live, by ClusterRoleBinding |

`subjects` and `rbac: true` may be combined on one rule; either admits the requester. `anyone: true` cannot be combined with either, and a rule with no grant at all fails the install.

Subjects take `kind`, `name`, `namespace` for a ServiceAccount, and optionally the `apiGroup` a RoleBinding would use (`""` for a ServiceAccount, `rbac.authorization.k8s.io` for a User or Group), so subjects can be copied from an existing binding. They are matched by name against the request's user info: a `ServiceAccount` as `system:serviceaccount:<namespace>:<name>`, a `User` as the username, a `Group` against the user's groups. `{kind: Group, name: 'system:serviceaccounts:<namespace>'}` means every ServiceAccount in a namespace. Names need not exist yet: a ServiceAccount created later is matched as soon as it makes a request.

What a component such as the GPU operator needs, by how the allow rule covering its packages is granted:

| Rule grants by | What the GPU operator needs |
|---|---|
| `subjects: [{kind: ServiceAccount, namespace: gpu-operator, name: gpu-operator}]` | Only its normal RBAC to write `nodewrights`. |
| `anyone: true` | Only its normal RBAC to write `nodewrights`. |
| `rbac: true` | Its normal RBAC, **plus** a ClusterRole granting `use` on `packagerules` with `resourceNames: [<rule name>]`, bound to its ServiceAccount by a ClusterRoleBinding. |

Named subjects suit a component with a fixed identity; `rbac: true` suits groups of people whose membership changes.

#### RBAC grants (`rbac: true`)

The rule's key in `allow` is the name RBAC checks: for an allow rule named `network`, the policy asks whether the requester may `use` `packagerules/network` in the API group `packagepolicy.nodewright.nvidia.com`. No such API is served; RBAC grants a verb on any resource name. The check is cluster-scoped, so only a ClusterRoleBinding grants it; a RoleBinding in a namespace does not. You write the roles; the chart creates none:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: network-team-nodewright}           # any name
rules:
- apiGroups: [packagepolicy.nodewright.nvidia.com]
  resources: [packagerules]
  resourceNames: [network]                           # must equal the allow rule's name
  verbs: [use]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: network-team-nodewright}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: network-team-nodewright}
subjects:
- {apiGroup: rbac.authorization.k8s.io, kind: Group, name: network-team}
```

To check whether a subject may use the rule, ask the apiserver with a SubjectAccessReview, which prints `true` or `false`. `kubectl auth can-i` cannot answer this: it resolves the resource through discovery, and since no API serves `packagerules`, it checks the wrong resource and answers `no` even for a subject the role grants.

```bash
kubectl create -o jsonpath='{.status.allowed}{"\n"}' -f - <<EOF
apiVersion: authorization.k8s.io/v1
kind: SubjectAccessReview
spec:
  user: alice
  groups: [network-team]
  resourceAttributes:
    group: packagepolicy.nodewright.nvidia.com
    resource: packagerules
    name: network
    verb: use
EOF
```

- **Renaming an `rbac: true` rule silently revokes its RBAC grants**, because existing roles still name the old key. This fails closed, but update the roles with the rename.
- **A role without `resourceNames`** grants every `rbac: true` rule. Useful for a platform-admin role; dangerous if written by accident.
- **Any role with wildcard rules** (`apiGroups: ["*"], resources: ["*"], verbs: ["*"]`, such as `cluster-admin`) passes the check for every `rbac: true` rule. Allow rules without `rbac: true` never consult RBAC, so they are unaffected, and a leftover role cannot reopen a rule switched to `subjects`.
- **Each RBAC check is costly in CEL.** The policy makes at most one check per `rbac: true` rule per request, however many packages the write touches, and Kubernetes' CEL budget allows roughly 28 of them per request. The budget counts every `rbac: true` rule the request consults, across all the packages it touches, so keep the total number of `rbac: true` rules well below that; a request that exceeds the budget is refused.

### What counts as a change

The policy compares the old and new object and evaluates only packages that changed:

- **Add**: a key in `spec.packages` that was not there before.
- **Update**: any change to an existing package entry: image, version, `containerSHA`, `configMap`, `env`, interrupt settings, resources, `uninstall`, `agentImageOverride` and so on. **Any change to a `spec` field other than `packages`** (`nodeSelectors`, the interruption budget, tolerations, the deployment policy and so on) counts as an Update of **every** package in the CR, because those fields decide where and how fast every package rolls out. Annotations, such as `nodewright.nvidia.com/pause`, and status do not count.
- **Remove**: a key that was removed, and every package of a CR that is deleted.
- **Image swap**: an Update that changes `image` under an existing key is also checked as a Remove of the old image, so nobody can take over another team's package entry by repointing it.

Packages that did not change are never evaluated, so enabling the policy cannot block an edit because of a package that was already there and is left alone. A field holding a zero value (`false`, `0`, `""`, `{}`, `[]`) counts as absent, and so does a package `name` equal to its key, as do empty lists and maps inside `interrupt`, `uninstall`, `nodeSelectors` and `podNonInterruptLabels`. Everything else is compared exactly. In user data such as `configMap` and `dependsOn`, adding a key with an empty value is a change, and clearing a field that had a value is always a change.

**Typed clients should write with a patch.** A client that writes the whole object from Go types, such as a controller-runtime operator doing a `Get` and an `Update`, re-serialises every package, including the ones it did not touch. The rules above absorb the fields it fills in, but not a value Go writes back in another form: a duration of `5m` comes back as `5m0s`, a quantity of `0.5` as `500m` and `1024Mi` as `1Gi`, and an `env` entry's `value: ""` is dropped. When a CR holds such a value, a full Update counts that package as changed (every package, for a `drainConfig` duration) and is refused unless the writer may change it. A merge patch, such as controller-runtime's `client.MergeFrom`, sends only what the writer changed and avoids this; so does an unstructured client, which writes values back as stored. Server-side apply avoids it too, since it sends only the packages in its field manager's intent, but that intent is the manager's whole say, so it should include every package the manager applied before. Leaving one out drops each of its fields that no other manager owns: if that is all of them, the package is removed and checked as a Remove; otherwise it loses those fields, which is checked as an Update.

### Retiring packages

So that packages deployed before the policy can always be removed:

- Deny entries never block a Remove, nor an Update that only starts an uninstall: it changes nothing but the `uninstall` block and leaves `uninstall.enabled` and `uninstall.apply` both `true`.
- Cancelling an uninstall is not retiring. Setting `uninstall.apply` back to `false`, or removing the `uninstall` block, re-installs the package (see [Uninstall](../user-guide/uninstall.md)), so it is checked as an ordinary Update.
- In allow-list mode, removing a package that **no allow rule covers**, or starting its uninstall, is allowed for anyone who can write the CR. A package an allow rule covers needs that rule's grant, with `Remove` in its `operations` for both removing it and starting its uninstall, since starting an uninstall retires the package too.
- For retiring, an allow rule covers every package its `registries` and `images` match, whatever its `versions` and `shas` currently allow. Narrowing a rule to newer versions therefore never hands the older packages it used to allow to every CR writer, and the rule's grantees can still remove them.

### Agent overrides and digests

- `agentImageOverride` points a package at an arbitrary agent image. A deny entry that sets only `registries` and/or `images` applies to the override too, matched against its host and its repository without tag or digest; one that sets `versions` or `shas` does not. In allow-list mode, an Add or Update that sets an override must also match the covering allow rule's `agentImages`; a rule without `agentImages` refuses overrides.
- `requireDigest: true`, or any `shas`, requires `containerSHA` on Add and Update. `version` is a tag and can be repointed at the registry; with `containerSHA` set, the operator pulls by digest, so `shas` pins exactly what runs. A deny entry with `shas` never matches a package without `containerSHA`, so deny-by-digest is effective only together with an allow rule that requires a digest.

### What the policy covers

NodeWrights (`nodewright.nvidia.com`) on create, update and delete, and legacy Skyhooks (`skyhook.nvidia.com`) on create and update. The legacy group refuses edits to existing objects but accepts creates, and the operator mirrors a new Skyhook into a NodeWright, so legacy creates are checked. Deleting a legacy Skyhook is not checked: the mirror never deletes the NodeWright, so it removes nothing from any node.

The operator's own ServiceAccount is exempt, **except when it updates a NodeWright's spec**:

- Its other NodeWright writes are finalizer, annotation and status patches, and its migration mirror's creates copy a legacy Skyhook, whose creation the policy checks while it is enabled. A legacy Skyhook created before the policy was enabled, admitted under `mode: Warn`, or created before a deny entry was added was never checked against today's rules, and until [#1071](https://github.com/NVIDIA/nodewright/issues/1071) the mirror re-creates a deleted NodeWright from it; see [Rolling it out](#rolling-it-out).
- The chart's uninstall cleanup Job runs as the same ServiceAccount, which keeps `helm uninstall` able to delete every CR.
- Its updates of legacy Skyhooks, such as removing the pre-rename finalizer from one being deleted, are exempt too: the mirror never copies a legacy Skyhook that is being deleted, so they change nothing that runs.
- The NodeWright spec change it makes is the mirror re-syncing an existing NodeWright from a legacy Skyhook. That is checked like any other write, so nobody can point the mirror at another team's NodeWright to rewrite its packages. In allow-list mode such a re-sync is refused unless an allow rule grants it (an `anyone: true` rule covering the packages does, since it applies to the operator too); the mirror then retries with backoff and logs the refusal. Existing legacy Skyhooks refuse edits after migration, so outside that attack a re-sync should not arise.

## Examples

```yaml
# 1. Nothing from Docker Hub, never shellscript. Deny-only, no grants needed.
packagePolicy:
  enabled: true
  deny:
  - registries: ['docker\.io']
  - images: ['.*/shellscript']
```

```yaml
# 2. Only packages a and b from registry x, for anyone who can write NodeWright CRs.
packagePolicy:
  enabled: true
  allow:
    approved:
      anyone: true
      images: ['nvcr\.io/nvidia/nodewright-packages/(a|b)']
```

```yaml
# 3. Only the GPU operator's ServiceAccount may manage the driver packages.
packagePolicy:
  enabled: true
  allow:
    gpu-drivers:
      subjects:
      - {kind: ServiceAccount, namespace: gpu-operator, name: gpu-operator}
      images: ['nvcr\.io/nvidia/gpu-operator/(driver|toolkit)']
      versions: ['25\.\d+\.\d+']
```

```yaml
# 4. Many allow rules: platform admins may use anything from nvcr.io; the GPU operator may
#    manage its driver at 25.x and its toolkit at 1.x only; the network team is granted live
#    through RBAC; everyone may use the tuning package.
packagePolicy:
  enabled: true
  deny:
  - images: ['.*/shellscript']
  allow:
    platform-admins:
      subjects: [{kind: Group, name: platform-admins}]
      registries: ['nvcr\.io']
    gpu-driver:
      subjects: [{kind: ServiceAccount, namespace: gpu-operator, name: gpu-operator}]
      images: ['nvcr\.io/nvidia/gpu-operator/driver']
      versions: ['25\.\d+\.\d+']
    gpu-toolkit:
      subjects: [{kind: ServiceAccount, namespace: gpu-operator, name: gpu-operator}]
      images: ['nvcr\.io/nvidia/gpu-operator/toolkit']
      versions: ['1\.\d+\.\d+']
    network:
      rbac: true
      images: ['nvcr\.io/nvidia/network-operator/.+']
    tuning:
      anyone: true
      images: ['nvcr\.io/nvidia/nodewright-packages/tuning']
```

## Rolling it out

1. Audit your existing NodeWrights: the policy only checks changes, so packages already deployed keep running until someone changes them.
2. Delete migrated legacy Skyhooks, as the [migration guide](../getting-started/migration.md#safe-deletion-ordering) describes, and do it again whenever you tighten the rules. Until [#1071](https://github.com/NVIDIA/nodewright/issues/1071) makes migration one-time, the operator's mirror re-creates a deleted NodeWright from its legacy Skyhook as the exempt operator, and a legacy Skyhook created before the policy was enabled, admitted under `mode: Warn`, or created before a deny entry was added was never checked against today's rules.
3. Enable the policy with `mode: Warn`. Violating writes are admitted, and the writer sees a warning such as `package(s) not permitted for alice: Add p=ghcr.io/x/y:1.0.0`. `mode: Audit` instead records the violation as an annotation on the request's audit event, which only appears if the cluster's audit policy logs these requests at `Metadata` level or above; use `Warn` where you cannot read the audit log.
4. When no unexpected warnings remain, switch to `mode: Deny`. Use `Warn` again whenever you add the first allow rule to a deny-only policy.

## Reading a denial

Denials name the requester and every refused change:

```text
package(s) not permitted for alice: Add p=ghcr.io/x/y:1.0.0
package(s) not permitted for erin without RBAC use on packagerules.packagepolicy.nodewright.nvidia.com: Add n=nvcr.io/nvidia/network-operator/sriov:1.0.0 (network)
```

The second form appears when only an `rbac: true` rule could allow the change; it names the allow rules the requester needs `use` on.

Invalid values fail `helm install` or `helm upgrade` before anything changes in the cluster. The schema's messages name the failing path:

| Message | Meaning |
|---|---|
| `at '/packagePolicy/allow/<name>': 'anyOf' failed` | The allow rule sets neither `registries` nor `images`, or grants nobody (no `anyone: true`, `subjects` or `rbac: true`). |
| `at '/packagePolicy/allow/<name>': 'not' failed` | The allow rule combines `anyone: true` with `subjects` or `rbac: true`. |
| `additional properties '<key>' not allowed` | A misspelled or unknown key. |
| `invalid propertyName '<name>'` | An allow rule name that is not a DNS label (lowercase letters, digits and `-`). |
| `error parsing regexp` | A pattern RE2 cannot compile. |

## Security notes

- **A grant is to a name.** Anyone who can create a ServiceAccount named `gpu-operator` in namespace `gpu-operator` inherits every allow rule that names it, whether through `subjects` or an RBAC binding, and so does anyone who can act as an existing one: create a Pod that runs as it and use the Pod's token, or request a token for it. Restrict ServiceAccount creation, Pod creation and `serviceaccounts/token` in granted namespaces as tightly as NodeWright itself.
- **`anyone` means anyone who can write NodeWright CRs.** RBAC on the CRD remains the outer gate.
- **Who can change the policy** is whoever can upgrade the Helm release or edit the ValidatingAdmissionPolicy and its binding, which needs cluster-level RBAC. Manage the release through GitOps. For `rbac: true` rules, whoever can create bindings for the `use` verb can grant them, and holders of wildcard roles already have them; audit both before marking a rule `rbac: true`.
- **The policy governs package changes, not where a CR targets.** Changing `nodeSelectors` or the interruption budget counts as an Update of every package in the CR, but a requester granted an allow rule may still point their own packages at any node the CR's selector can reach.
- **DeploymentPolicy objects and node labels are outside the policy.** A NodeWright's rollout shape comes from the DeploymentPolicy it references, and which nodes it selects comes from node labels; neither is a NodeWright write. Control them with RBAC on `deploymentpolicies` and on nodes.
- **The garbage collector is checked like any other requester.** Deleting a NodeWright is a Remove of every package in it, made by the collector (`system:serviceaccount:kube-system:generic-garbage-collector`). In allow-list mode with `mode: Deny`, it can delete a NodeWright only when every package in it is one that no allow rule covers, or one covered by a rule with `Remove` that grants the collector: an `anyone: true` rule, or a `subjects` or `rbac: true` rule that matches it, including through a group such as `system:serviceaccounts`. Otherwise a NodeWright with an `ownerReference` stays after its owner is deleted, and you delete it yourself. Do not exempt the collector or grant it on purpose: anyone who can write a CR could then add an `ownerReference` to another team's NodeWright and remove its packages by deleting the owner.
- **It does not verify image signatures.** Use a signature policy such as Sigstore policy-controller for that.
- Anyone who needs more than this model can still write their own admission policy; this is the built-in baseline.
