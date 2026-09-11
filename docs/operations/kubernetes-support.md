# Kubernetes Version Support

This document outlines NodeWright's approach to supporting different Kubernetes versions.

## What Version Should I Use?

Use one of the four CI-tested versions. The operator runs package work as `batch/v1` Jobs, and several of the Job features it depends on are recent enough that older clusters lose real safety properties — quietly.

What we **CI-test and officially support is the latest four Kubernetes minor versions** (see [Support Policy](#support-policy)).

| Kubernetes Version | Status |
|--------------------|--------|
| 1.37, 1.36, 1.35, 1.34 | ✅ Supported and CI-tested |
| 1.31 – 1.33 | 🟡 Untested, expected to work — every Job feature we use is at least beta-on-by-default |
| 1.29 – 1.30 | 🟡 Untested; the deadline log snapshot and the unreachable-node signal are lost (see below) |
| 1.23 – 1.28 | ⚠️ Degrades silently — see [What older clusters lose](#what-older-clusters-lose) |
| Older than 1.23 | ❌ Unverified |

### Why "silently"

An apiserver that does not know a field **drops it and returns success**. It does not reject the Job. So on an older cluster the operator creates what looks like a healthy Job, the field it was relying on is simply absent, and the behaviour it guaranteed is gone with no error, event, or log line anywhere. Nothing surfaces until the situation that field existed to handle actually occurs.

The same is true of feature gates: a field that is *alpha* in a given minor is off unless the cluster operator turned it on, so the version alone does not tell you it is active.

## What older clusters lose

Read top-down and the losses accumulate: 1.26 loses everything listed for 1.26 **and** everything below it in the table.

| Going below | What stops working | What that costs NodeWright |
|---|---|---|
| **1.34** | — (support floor; everything below is untested) | Nothing known. This is where CI coverage stops, not where features stop. |
| **1.31** | The `FailureTarget` Job condition is not set for a deadline expiry. 1.29–1.30 report it directly as `JobFailed`; the delayed-terminal behaviour that raises `FailureTarget` first arrives in 1.31 | Everything the operator hangs off that condition stops: the `last-logs` deadline snapshot never fires, and the stale-`FailureTarget` path that surfaces `erroring` for a Job wedged on an unreachable node never triggers either. The Job still fails correctly; only the evidence and the unreachable-node signal are lost. |
| **1.29** | `podReplacementPolicy` becomes alpha (off by default) in 1.28 | Replacement pods can start while the previous attempt is still terminating. Both mount the host root and share one `copyDir` on that node, so two `cp -r` runs can write the same host directory concurrently. The agent's flag files make *re-execution* idempotent, but they are not a lock and do not order those writes — a step script can read a file another attempt is mid-way through overwriting. Treat this as a possible-corruption configuration, not a benign race. |
| **1.27** | `batch.kubernetes.io/controller-uid` and `batch.kubernetes.io/job-name` pod labels (added in 1.27) | The operator finds a Job's own pods by controller UID, so without those labels it finds none. Failed-attempt pruning no-ops, and archive pods **accumulate** instead of being trimmed to two per stage. (The `last-logs` snapshot is already gone by this point — see the 1.31 row.) Stage **completion is unaffected** — it is read from the Job's `Complete` condition, never from pods. |
| **1.26** | `podFailurePolicy` and the `DisruptionTarget` pod condition become alpha (off by default) in 1.25 | **The sharpest loss.** The `Ignore`-on-`DisruptionTarget` rule disappears, so evictions, preemptions and taint-manager kills start counting toward `backoffLimit` like genuine failures. While `backoffLimit` is effectively unlimited that only costs an archive slot; once it is a finite budget, a couple of unrelated disruptions can exhaust the retries and park a package that never failed. |
| **1.23** | Job tracking with finalizers is not yet on by default | Attempt accounting becomes unreliable: failures can be missed or double-counted, so `backoffLimit` no longer means what it says. |
| **1.22** | `ttlSecondsAfterFinished` (TTL-after-finished GA'd in 1.23) | Finished Jobs are never garbage collected. Retained Jobs and their pods accumulate until something else removes them. |
| **1.21** | `spec.suspend` (Job suspend, beta-on in 1.22) | `nodewright.nvidia.com/pause` loses its teeth: it blocks new stages from being scheduled but cannot stop a stage that is already running. |

> [!NOTE]
> The version numbers above track upstream feature-gate graduation, not NodeWright behaviour we have measured — none of these clusters are in CI. Treat the table as "where to look first" when something misbehaves on an old cluster, not as a tested compatibility promise.

If you are below 1.34 and something in that list matters to you, the honest answer is to upgrade rather than to reason about which degradations you can tolerate.

## Support Policy

**Latest-four rolling window:** we test and support the **four most recent Kubernetes minor versions**. As a new minor release arrives and [kind](https://kind.sigs.k8s.io/) stops publishing a `kindest/node` image for the oldest one, we drop the oldest and add the newest. The exact tested patch versions live in `operator/versions.yaml` (`ci.kindNodeImages`), and the CI matrix is bounded by the node images the pinned kind (`kind.binary`) actually publishes.

Currently tested: **1.37, 1.36, 1.35, 1.34** (kind v0.33.0). 1.33 and older dropped when kind v0.33.0 stopped publishing their node images.

### Our Strategy

- **Support the latest four Kubernetes minor versions**, bounded by kind's published `kindest/node` images
- **Wait 4+ weeks** before adopting brand new Kubernetes versions (let them stabilize)
- **Older releases** remain available for users on older Kubernetes clusters
- **Clear compatibility** - each release has a defined K8s support window

### What This Means

- **✅ Fully Supported:** We test and support these K8s versions in the current NodeWright release
- **⚠️ Use older NodeWright:** Your K8s version is supported, but use an older NodeWright release
- **❌ Not Supported:** Upgrade your Kubernetes cluster or use a much older NodeWright version

### When Versions Change

**For new Kubernetes releases:**

1. Wait **4+ weeks** after K8s release for ecosystem stability
2. Add to the CI testing matrix in `operator/versions.yaml`
3. Include in next NodeWright release

**For EOL Kubernetes versions:**

1. Stop including in new NodeWright releases
2. Existing NodeWright versions continue to work
3. Users should upgrade K8s and then upgrade NodeWright

## Upgrade Strategy

### Our Approach

- Update Kubernetes client libraries when we add support for new versions
- Test on both supported Kubernetes versions before each release
- Provide clear migration guidance when dropping version support

### For Users

We understand many installations run slightly older Kubernetes versions. Our strategy balances staying current while giving users time to upgrade:

- **6-week notice** before dropping support for a Kubernetes version
- **Clear documentation** about which NodeWright version to use for your Kubernetes version
- **Gradual transitions** rather than sudden jumps when possible

## Version Selection Guide

Use the **latest release**. It is CI-tested against the latest four Kubernetes minor versions (currently 1.34 through 1.37).

Below that range the operator will very likely still *run* — but "runs" and "behaves as documented" diverge as you go back, because the Job features it leans on drop out silently rather than failing loudly. [What older clusters lose](#what-older-clusters-lose) says which property goes at which version. Down to 1.31 the losses are theoretical (every field is at least beta-on-by-default); 1.29–1.30 lose the deadline evidence path; from 1.28 down the losses are structural.

Upgrade into 1.34 – 1.37 when you can for the fully supported, CI-tested experience.

## FAQ

### Why don't you support EOL Kubernetes versions in new releases?

As a small project, we focus our efforts on actively maintained Kubernetes versions. This allows us to:

- Ensure better quality and security
- Adopt new Kubernetes features when they're stable  
- Keep our testing matrix manageable
- Provide clearer upgrade paths

### What if I'm stuck on an older Kubernetes version?

**You can still use NodeWright!** Just use an older NodeWright version that was built for your K8s version:

- Older releases continue to work and don't disappear
- Check our release notes for which NodeWright version supports your K8s version
- Plan your Kubernetes upgrade timeline, then upgrade NodeWright afterward

### Why wait 4 weeks before supporting new Kubernetes versions?

We've learned that brand new Kubernetes versions often have:

- Ecosystem compatibility issues
- Updated client library dependencies  
- Undiscovered bugs that get fixed in patch releases

Waiting 4+ weeks lets the ecosystem stabilize and gives us confidence in supporting the new version.

### How do you test compatibility?

For each NodeWright release, we test against all supported Kubernetes versions using:

- GitHub Actions matrix builds with multiple K8s versions. The exact tested patch versions are owned by `ci.kindNodeImages` in `operator/versions.yaml`.
- Local testing with [kind](https://kind.sigs.k8s.io/)
- Basic functionality and integration tests

## Notes

This is a living document that will evolve as the project grows. Our current approach supports all actively maintained Kubernetes versions (the latest four minor versions) while providing reasonable predictability for users.

For questions about Kubernetes support, please open an issue in our [GitHub repository](https://github.com/NVIDIA/skyhook).
