# CI test pools

The nodewright chainsaw e2e suite (`k8s-tests/chainsaw/nodewright/`) is split into labeled pools that run as parallel CI jobs to keep wall-clock per matrix row under ~18 min.

## Pools

Every test under `k8s-tests/chainsaw/nodewright/*/chainsaw-test.yaml` has a top-level `metadata.labels.pool: <name>`:

- `core` — fast smoke and feature tests; **also the fallback for any
  test missing a `pool:` label**.
- `interrupt` — cordon/drain/taint/runtime-required flows.
- `uninstall` — uninstall, upgrade, downgrade lifecycle.
- `lifecycle` — pause/disable/delete/finalizer/state.

## Running locally

```bash
# All tests (default — same as before pools existed):
make e2e-tests

# A single pool:
POOL=interrupt make e2e-tests
```

The `POOL` variable is honored by `operator/Makefile`'s `e2e-tests`
target. `POOL=core` uses a `pool notin (...)` selector so unlabeled
tests still run.

## Suites that are not pools

Not every matrix row is a chainsaw pool. Pools only subdivide
`k8s-tests/chainsaw/nodewright/`; the other rows in the `tests` matrix are
separate suites selected by `test-suite` and run via their own make target
(`unit-tests`, `helm-tests`, `cli-e2e`, `deployment-policy`, `migration`).

Two of them, `helm-tests` and `migration`, build their cluster with
`make create-kind-cluster` (ctlptl) rather than `helm/kind-action`, because they
need ctlptl's local image registry to serve the operator image under test. The
three cluster-setup steps key off a shared list, so adding another registry-needing
suite means adding its name to that list in all three places.

Both also start from the same `v0.17.1` pre-rename baseline: `migration` for the API
group rename, `helm-tests`' `helm-upgrade-rollback-test` for the in-cluster resource
name rename. `migration` materializes that chart with `git archive chart/v0.17.1`, so
it needs **full history with tags** (`fetch-depth: 0`, `fetch-tags: true`, already set
for the whole `tests` job) and fails early with an explicit message rather than a bare
git error when the tag is missing. `helm-upgrade-rollback-test` pulls the published
chart from the registry instead, and only reads git tags when asked to resolve the
newest release rather than a pinned one.

### `operator-agent`

Runs `k8s-tests/operator-agent/` — the only suite that exercises the **real agent** rather than
the `agentless` package image, so the only one that proves a package's scripts run on the host.

It covers `apply`, `config`, `interrupt`, `post-interrupt` and `uninstall`, each with its `-check`
counterpart, plus log retention, `SKYHOOK_AGENT_WRITE_LOGS=false`, and the `check_results` /
`<stage>_ALL_CHECKED` summary artifacts. `upgrade` is **not** covered: the `shellscript` package
these scenarios use declares no `upgrade` mode in any published version, so an upgrade scenario
needs a package that supports one.

`agent-ci.yaml` already runs it when the *agent* changes, against a freshly built agent. This row
covers the other direction: the operator is what builds the pod the agent runs in — its args,
mounts, copy dir and `config.json` — and until this row existed, an operator change could break
that contract without any suite noticing.

During the Python-to-Go agent rewrite the suite is the **parity contract**, so it runs against
both agents and the scenarios are shared, never forked — a scenario is the behaviour the operator
depends on, not something either implementation gets its own copy of. Which agents run is decided
by the paths a PR touches, because each workflow owns one image:

| Paths changed | `agent-ci.yaml` (Python) | `agent-go-ci.yaml` (Go) |
|---|---|---|
| `agent/**` excluding `agent/go/**` | ✅ | — |
| `agent/go/**` | — | ✅ |
| `k8s-tests/operator-agent/**` | ✅ | ✅ |

Both workflows dump agent pod logs and the agent's on-node state under `/etc/skyhook` and
`/var/log/skyhook` on failure, via `.github/actions/dump-operator-agent-diagnostics`. A parity
failure is only useful if both sides are diagnosed from the same evidence.

Once the cutover (#222) removes the Python agent, the Python row and its path filter go with it.

It resolves `AGENT_IMAGE` from `chart/values.yaml` rather than pinning a version in the workflow,
so bumping the agent in one place cannot leave this row testing an older one. The suite refuses to
run without an explicit `AGENT_IMAGE`, because `operator/Makefile`'s global default is the
`agentless` image, which would pass every case while executing nothing.

### `migration`

Runs `k8s-tests/migration/run.sh`: installs the last pre-rename release
(`chart/v0.17.1` at operator `v0.17.0`) and upgrades to the commit under test,
asserting per-node state migrates and no package re-runs. See
`k8s-tests/migration/README.md`.

Two requirements it does not share with the other suites:

- **Full history with tags.** It materializes the old chart with
  `git archive chart/v0.17.1`, so the checkout needs `fetch-depth: 0` and
  `fetch-tags: true` (both already set for the whole `tests` job). Without them
  the run fails early with an explicit message rather than a bare git error.
- **A clean cluster.** Phase 1 refuses to run if `nodewright.nvidia.com` CRDs
  already exist, since a pre-rename baseline is the point. It gets a fresh
  cluster per run, so this only bites when running locally against a reused one.

It runs on the primary Kind node image only. The upgrade path it exercises is the
operator's own, not Kubernetes-version sensitive, and the run is long enough
(cluster create, two helm installs, a real package rollout, a prune, two operator
restarts) that a matrix over every supported version would not pay for itself.
This row is the longest in the matrix and exceeds the ~18 min per-row target the
pools are sized against.

## Adding a new pool

Three-file change, no branch-protection edits:

1. Add `pool: <newpool>` to the relevant `chainsaw-test.yaml` files' `metadata.labels` block.
2. Append `<newpool>` to the `pool:` matrix list in `.github/workflows/operator-ci.yaml`.
3. Append `<newpool>` to `POOL_NON_DEFAULT` in `operator/Makefile` so the `core` pool's `notin (...)` selector excludes it.

Worst case if step 3 is forgotten: the new pool's tests run twice (once in their pool, once in `core`). Noisy but not silently broken.

## Code coverage

Per-row coverage artifacts are named `coverage-<test-suite>[-<pool>]-k8s-<version>`. The `upload-coverage` job globs `coverage-*` and concatenates the per-row `cover.out` files (Go covdata `mode: set` format auto-deduplicates lines), producing a single combined profile uploaded to Coveralls — equivalent to the pre-pool combined profile.

### Excluded paths

`COVERAGE_EXCLUDE` in `operator/Makefile` lists paths whose statements are dropped before the total is reported. It currently holds one entry: `zz_generated.deepcopy.go`.

**Why exclude it:** controller-gen writes those files and nobody should hand-write a test for `DeepCopyInto`, but they are ~916 statements — about a quarter of all uncovered code in the repo — so counting them costs roughly 4 points and obscures the coverage of code people actually write. The legacy `api/v1alpha1` copy sits near 29% purely because that CRD is read-only.

**Why it lives in the Makefile:** Coveralls has no exclusion mechanism for Go profiles. There is no `.coveralls.yml` key for it and the `coverallsapp/github-action` has no such input — it counts whatever profile it is handed. So the filtering has to happen before upload, and `COVERAGE_EXCLUDE` is the single definition. `make merge-coverage` applies it to each per-suite artifact, and the `upload-coverage` job calls `make filter-coverage` again on the combined profile so a suite that starts uploading a raw profile cannot quietly reintroduce the generated files.

**What does not belong here:** hand-written code. An exclusion moves the number without changing what is tested. If a piece of code is genuinely not worth testing, say so in a comment where it lives; do not hide it from the total.

**If you edit `COVERAGE_EXCLUDE`,** note that `filter-coverage` refuses any pattern that leaves nothing behind — an invalid expression, or one broad enough to match every line — and leaves `cover.out` untouched rather than uploading an empty profile that would report 0%. Setting it to empty is allowed and simply reports the unfiltered profile.

## Gate jobs

Every workflow that gates merge publishes a check named **`ci-gate`**.
GitHub composes same-named required checks across workflows, so branch
protection on `main` requires only the single `ci-gate` name and every
`ci-gate` that posts must pass.

Workflows publishing `ci-gate`:

- `lint-ci.yaml` — runs on every PR (no path filter). Guarantees a
  `ci-gate` is always posted, even on doc-only changes.
- `operator-ci.yaml`, `agent-ci.yaml`, `agent-go-ci.yaml` — wrapper job
  that depends on the matrix and image-build jobs. Posts `ci-gate` only
  when the workflow's paths trigger.
- `commit-linting.yaml`, `security-checkov.yaml`,
  `agentless-container.yaml` — single-job workflows wrap their worker
  in a `ci-gate` job for uniformity.

The `if: always()` + `jq -e '... result == "success"'` pattern is the
standard fix for GitHub Actions' "skipped == green" pitfall. Each gate's
`needs:` only lists jobs that always run — conditionally-skipped jobs
(`create-manifest`/`upload-coverage` on fork PRs / tag builds) are
deliberately excluded so legitimate skips don't fail the gate.
