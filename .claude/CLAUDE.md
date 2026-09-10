# CLAUDE.md / AGENTS.md

This file provides guidance to coding agents (Claude Code, Cursor, Codex, etc.) when working with code in this repository.

The canonical file lives at `.claude/CLAUDE.md`. The root-level `AGENTS.md` is a symlink to this file so agents that expect the cross-agent `AGENTS.md` convention find the same content.

## Repository overview

Skyhook (being renamed to NodeWright) is a Kubernetes-aware package manager for safely modifying host infrastructure at scale. It coordinates the node lifecycle (cordon → drain → apply package → interrupt/reboot → uncordon) as controlled rollouts gated by interruption budgets and deployment policies.

Rename status: the project is transitioning from Skyhook → NodeWright. Components already moved to `nodewright`: the install namespace (`nodewright` for new installs; existing installs stay in `skyhook` forever, since namespaces cannot be renamed in place), the Go module (`github.com/NVIDIA/nodewright/operator`), the Helm chart (`name: nodewright`, distributed at `oci://ghcr.io/nvidia/nodewright/charts/nodewright`), the operator image (`ghcr.io/nvidia/nodewright/operator`), the agent image (`ghcr.io/nvidia/nodewright/agent`, since `agent/v6.4.2`), the primary CRD group (`nodewright.nvidia.com/v1alpha1 NodeWright`; the legacy `skyhook.nvidia.com` group is kept read-only for a migration window), and the CLI plugin (`kubectl nodewright`, binary `kubectl-nodewright`). Don't "fix" `nodewright` references back to `skyhook`, and don't preemptively rename what hasn't moved yet.

## Required reading: `docs/` (load every session)

`docs/` is the authoritative source for domain concepts and behavioral contracts — most traps in this codebase are explained there, not in code comments. **At the start of every session, before writing any code in this repo, read all of the files below into context.** The whole set is ~2000 lines / ~100KB — negligible for modern context windows, and skipping it almost guarantees a wrong-shaped PR that violates an unstated contract.

Required (read on session start):

- `docs/README.md` — docs index
- `docs/architecture/operator-status.md` — **Status / State / Stage** vocabulary (distinct concepts; conflating them causes subtle bugs)
- `docs/architecture/interrupt-flow.md` — cordon / drain / interrupt / uncordon sequence and `podNonInterruptLabels` semantics
- `docs/user-guide/runtime-required.md` — runtime-required taint flow and `AutoTaintNewNodes`
- `docs/architecture/ordering.md` — priority, sequencing (`node` vs `all`), `SKYHOOK_NODE_ORDER`
- `docs/user-guide/deployment-policy.md` — cluster-scoped rollout shaping
- `docs/operations/resource-management.md` — strict resource-override validation (all 4 fields or none)
- `docs/operations/versioning.md` — per-component semver, strictly enforced
- `docs/user-guide/taints.md` — taint semantics
- `docs/user-guide/providing-secrets.md` — how packages consume secrets
- `docs/operations/kubernetes-support.md` — supported K8s versions
- `docs/operations/resources-at-scale.md` — scale characteristics
- `docs/contributing/release-process.md`, `docs/contributing/releases.md` — release process and history
- `docs/user-guide/cli.md` — CLI reference (needed before touching `operator/cmd/cli/`)

Read on demand (not required up-front):

- `docs/designs/` — design docs for existing features (consult before changing an existing feature)
- `docs/plans/` — in-flight plans (check before starting new work in an area)
- `docs/security/kyverno/`, `docs/observability/` — policy and observability surfaces

If a doc above is silent on a question you need to answer, say so explicitly rather than guess.

## Three components, three toolchains

- **`operator/`** — Go controller-manager (controller-runtime, Kubebuilder v4). Go 1.26.5, vendored (`GOFLAGS=-mod=vendor`). Also hosts the CLI (`cmd/cli`) built as a kubectl plugin.
- **`agent/skyhook-agent/`** — Python 3.10+ package (hatch-managed). Runs inside every package container; reads `/skyhook-package/config.json` and executes lifecycle steps (apply / config / interrupt / post-interrupt / upgrade / uninstall). Tests via pytest, vendored deps under `agent/vendor/`.
- **`chart/`** — Helm chart. Generated from `operator/config/` via `helmify` (`make generate-helm`) but hand-edited after; don't regenerate blindly.

The root `Makefile` just fans out into `operator/` and `agent/` subdirectories. Most real targets live in `operator/Makefile`.

## Common commands

All operator commands run from `operator/`:

```bash
make build              # build manager + CLI (also runs manifests/generate/fmt/vet/lint)
make build-manager      # operator binary only → bin/manager
make build-cli          # kubectl-nodewright → bin/nodewright

make unit-tests         # ginkgo unit tests + envtest (fake apiserver), writes to reporting/
make test               # full suite: manifests, generate, fmt, vet, lint, unit + e2e + cli-e2e + helm + operator-agent
make e2e-tests          # chainsaw e2e against current cluster (set POOL=<name> to run one pool — see docs/contributing/ci-test-pools.md)
make watch-tests        # ginkgo watch mode

make run                # runs controller as background process against current kubeconfig (ENABLE_WEBHOOKS=false by default)
make kill               # stops the backgrounded manager

make manifests          # regenerate CRDs/RBAC/webhooks from kubebuilder markers — REQUIRED after editing api/
make generate           # regenerate zz_generated.deepcopy.go — REQUIRED after editing api/ types
make generate-mocks     # regenerate mockery mocks — REQUIRED after editing mocked interfaces
make fmt                # gofmt + license headers
make lint               # golangci-lint + license-check
make lint-fix           # golangci-lint --fix

make install            # apply CRDs to current cluster (kustomize config/crd)
make uninstall          # remove CRDs
make create-kind-cluster         # fresh kind cluster with docker creds wired up
make create-deployment-policy-cluster  # 15-node kind cluster, needed for deployment-policy e2e
```

Run a single Go test with the Ginkgo focus flag (tests use Ginkgo/Gomega, not stdlib `testing.T`):

```bash
cd operator
./bin/ginkgo run --focus "my describe text" ./internal/controller/...
# or for packages with stdlib Go tests:
go test -mod=vendor -run TestName ./internal/graph/...
```

Agent commands run from `agent/`:

```bash
make venv               # one-time: create ./venv and install hatch
make test               # hatch test with coverage
make build              # hatch build → dist/
```

E2E tests use [chainsaw](https://kyverno.github.io/chainsaw/) against a real cluster, driven from `k8s-tests/chainsaw/{skyhook,cli,helm,deployment-policy}`. They require a kind cluster set up via `make create-kind-cluster` (or the 15-node variant for deployment-policy). `operator-agent-tests` additionally requires `AGENT_IMAGE=…` to be set.

## Architecture

### CRDs (see `operator/api/v1alpha1/`)

- **Skyhook** / **NodeWright** (cluster-scoped) — desired state: a DAG of `packages` (container image + version + configMap + optional `dependsOn`), node selector, interruption budget, additional tolerations, runtime-required flag, priority/sequencing, optional `DeploymentPolicy` reference. Both CRDs are cluster-scoped, so the install namespace only ever determines where the operator and its package pods live, never which CRs a client sees.
- **DeploymentPolicy** (cluster-scoped) — rollout shape: batch sizing, pause/resume windows, cross-Skyhook ordering.

CRD types are kubebuilder-annotated (`//+kubebuilder:…`). Any change to these files **must** be followed by `make manifests generate` — the generated `zz_generated.deepcopy.go`, CRD YAML under `config/crd/bases/`, and webhook config are all consumed at build time.

### Controller layout (`operator/internal/`)

- `controller/skyhook_controller.go` — primary reconcile loop (~2500 lines); owns Skyhook lifecycle, package-pod orchestration per node, interrupt/reboot flow, finalizer handling.
- `controller/cluster_state_v2.go` — cluster-state snapshot used by reconcile (what packages exist on which nodes, what stage each is in). This is the "v2" state model; there's no v1 anymore, the name is historical.
- `controller/pod_controller.go` — watches package pods and maps their completion back to node state annotations.
- `controller/webhook_controller.go` — self-managed webhook certs + validating/mutating webhook wiring.
- `controller/event_handler.go` — event filters deciding when SCR/node/pod changes enqueue a reconcile.
- `wrapper/` — higher-level domain objects (`skyhook.go`, `node.go`, `compartment.go`) that wrap the raw CRs to expose intent (e.g., "does this node still need interrupt?"). Controllers work through these wrappers rather than raw types.
- `graph/dependency_graph.go` — topological ordering of packages based on `dependsOn`.
- `dal/` — data-access layer abstraction over controller-runtime client (makes mocking in tests easier).
- `zz.migration.*.go` (e.g., `controller/zz.migration.0.5.0.go`, `wrapper/zz.migration.0.5.0.go`) — **hand-written** one-shot migration shims that upgrade existing CRs from an older schema. Despite the `zz.` prefix these are **not** generated by `make generate` — don't delete them, don't attempt to regenerate them, and don't move them just because the filename sorts oddly. If you need a new migration, add a new `zz.migration.<version>.go` alongside the existing ones.

### Status / State / Stage (vocabulary — don't conflate)

Three distinct concepts, intentionally named to look similar. Mixing them up causes subtle bugs. Full definitions in `docs/architecture/operator-status.md`; summary:

- **Status** — high-level health of a Skyhook or a node (e.g., `complete`, `blocked`, `waiting`, `paused`, `disabled`, `in_progress`, `erroring`, `unknown`). Derived from the collective States of the node's packages.
- **State** — per-package execution outcome on a node (`complete`, `in_progress`, `skipped`, `erroring`, `unknown`).
- **Stage** — which lifecycle phase a package is currently in (`uninstall`, `upgrade`, `apply`, `config`, `interrupt`, `post-interrupt`, each with its `*-check` counterpart).

In prose, code, and logs: Status describes *the Skyhook or node as a whole*, State describes *one package on one node*, Stage describes *which phase of that package is running*. A field, variable, or log key named for one of the three must exclusively carry that concept.

### Package lifecycle stages

Packages run through stages in this order (from `README.md` §Stages):

- Without interrupts: **Uninstall → Apply → Config** (or **Upgrade → Config** on version bump)
- With interrupts: add **Interrupt → Post-Interrupt** at the end, with the node cordoned and drained before Interrupt.

Semantic versioning is strictly enforced so the operator can detect upgrade vs. downgrade vs. fresh-apply. State is persisted as annotations on the Node (`nodewright.nvidia.com/nodeState_<name>`, where `<name>` is the CR's `metadata.name`), not on the NodeWright CR.

### Agent (Python)

The agent is a container entrypoint the operator injects alongside every package. It:

- Reads `/skyhook-package/config.json` (validated against `schemas/`)
- Dispatches to the requested stage/step
- Uses `chroot_exec.py` to run step scripts inside the host root mount
- Writes completion flag files so subsequent stages skip already-done work
- Gates interrupt re-runs on `SKYHOOK_RESOURCE_ID` (unique per package config)

Relevant env vars are documented in `agent/README.md`.

### CLI (`operator/cmd/cli/`)

kubectl plugin (`kubectl nodewright …`). Subcommands under `app/`: `node`, `package`, `deploymentpolicy`, plus `reset`, `lifecycle` (pause/resume/disable/enable), `version`, `migrate`. Commands go through `operator/internal/cli/client` rather than talking to the apiserver directly. Recent operators use annotations (`nodewright.nvidia.com/pause`, `nodewright.nvidia.com/disable`) for pause/disable — not spec fields (the legacy Skyhook uses the `skyhook.nvidia.com/*` equivalents).

## Secrets and credentials

Hard boundaries. These are not style preferences — violating one means a rotation, not a revert.

- **Never commit credentials, API keys, tokens, passwords, kubeconfigs, or private keys.** Nothing secret belongs in a tracked file: not in Go or Python source, not in test fixtures, not in `chart/values.yaml`, not in chainsaw manifests under `k8s-tests/`, not in `.github/workflows/`, and not in a commit message. Examples use an obvious placeholder (`REPLACE_ME`, `<your-token>`), never a real value that "happens to be expired".
- **Never read a real credential into your output.** `~/.kube/config`, `~/.docker/config.json`, `~/.ssh/`, `~/.aws/`, and any `.env` are off limits — refer to them by path, don't cat them into the transcript, a file, or a PR description.
- **Secret-bearing environment variables stay out of tracked files.** CI reads them via `${{ secrets.* }}`; local runs read them from your shell. Don't add a `.env` with real values, and never give a secret-bearing variable a hardcoded fallback default.
- **Cluster secrets are mounted at runtime, never baked in.** Packages receive them through the mechanism in `docs/user-guide/providing-secrets.md` — a Kubernetes Secret projected into the package pod. Never inline a secret into a package `configMap`, a `NodeWright` CR, or a container image layer.
- **Found a committed secret? Stop and report it.** Do not quietly delete it in a follow-up commit — the value survives in git history and has to be rotated regardless. Report it through `SECURITY.md`; do not open a public issue.

## Code style (Go)

### Working rules

1. **Read before writing** — never edit a file you haven't read.
2. **Tests must pass** — `make unit-tests` (or full `make test`) before considering a change done; don't skip or disable tests to make CI green.
3. **Use project patterns; justify new ones in the PR description.** Before writing code, find the nearest existing example in the same package and match it — error-wrapping style, `dal`/`wrapper` indirection, Ginkgo spec structure, controller-runtime idioms, constant placement, etc. If the pattern you're about to introduce **doesn't already exist in the target package**, stop and ask whether it should — and if you proceed, **call it out explicitly in the PR description** (what you introduced, why the existing patterns didn't fit, and why the new one should become the next convention). Silent divergence is rejected in review.
4. **3-strike rule** — after three failed attempts at the same fix, stop and reassess rather than piling on more code.
5. **Don't add features that weren't asked for** — no preemptive abstractions, config knobs, or scope creep.
6. **Prefer `Edit` over `Write`** — only create new files when the code genuinely doesn't belong in an existing one.
7. **Prefer the Makefile over raw commands.** `make build` / `make test` / `make fmt` / `make lint` / `make manifests generate` / `make run` encode `-mod=vendor`, license-header formatting, envtest + ginkgo setup, CRD/deepcopy/mock generation sequencing, logging / PID / reporting paths, and more — raw `go build` / `go test` / `golangci-lint run` invocations routinely skip some of these and produce drift. Run `make help` (from repo root or `operator/`) to see available targets before reaching for a raw command. Drop to a raw command only for narrow cases the Makefile doesn't cover — e.g., `ginkgo --focus "text"` for a single spec, or `go test -run TestName` for a single stdlib test. If a workflow isn't in the Makefile and you'll repeat it, add a target rather than documenting the raw command.

### Errors

- Skyhook uses stdlib `errors` + `fmt.Errorf("…: %w", err)` (no `pkg/errors` sentinel-code wrapper). When you wrap, use `%w` so `errors.Is` / `errors.As` continue to work.
- **Never return a bare `err`** from a function that does more than one thing — add context: `fmt.Errorf("reconciling node %s: %w", node.Name, err)`.
- **Don't ignore `Close()` on writable handles.** `errcheck` is on; check the error on `os.Create`, `os.OpenFile(..., O_RDWR|O_WRONLY)`, etc. Capture it:
  ```go
  closeErr := f.Close()
  if err == nil { err = closeErr }
  ```
- **K8s helpers**: `client.IgnoreNotFound(err)` and `controllerutil.SetControllerReference` returns are idiomatic and don't need wrapping.

### Logging

Skyhook uses **controller-runtime's `logr.Logger`** (obtained via `log.FromContext(ctx)` or the `Logger` field on the reconciler). Do **not** introduce `slog` or `log` (stdlib) for new code — stay consistent with the surrounding controller. Log with key/value pairs:
```go
log := log.FromContext(ctx)
log.Info("reconciling skyhook", "name", sh.Name, "generation", sh.Generation)
log.Error(err, "failed to cordon node", "node", node.Name)
```
No `fmt.Println` / `fmt.Printf` in non-test code (already the convention — grep confirms zero occurrences).

### Context and concurrency

- **Plumb the reconcile `ctx` through**; don't create `context.Background()` inside the reconcile path. `context.Background()` is fine for: package `init`, long-running background goroutines owned by `main`, and test setup.
- Any HTTP / external I/O needs a bounded context: `ctx, cancel := context.WithTimeout(ctx, …); defer cancel()`.
- **Never use `http.DefaultClient`** (zero timeout). Build a client with `Timeout:` set, or use the rest client wired up by controller-runtime.
- **Check `ctx.Done()` in long-running loops** so reconcile cancellation propagates.
- For parallel fan-out, prefer `golang.org/x/sync/errgroup` (already in `go.sum`) over manual `sync.WaitGroup`.

### Operator / reconcile pattern

Skyhook is a Kubernetes operator. Internalize these before touching the controller:

- **Level-triggered, not edge-triggered.** Reconcile must be correct given only the current state of the world. Never assume a particular event caused this invocation, never assume the previous reconcile ran, never assume you'll be called again "soon" unless you explicitly requeue. If a node's annotation is missing, figure out why from the spec + cluster state — don't try to remember what you did last time.
- **Idempotent.** Running reconcile twice with identical inputs must produce the same result and no double side-effects. Before creating a pod, drain, taint, or annotation, check if it already exists. Before mutating status, compare to the observed value.
- **Desired state (spec) vs. observed state (cluster).** The reconciler's one job is to converge observed → desired. Intent lives in `SkyhookSpec` / `DeploymentPolicySpec`; observation lives in the cluster (Nodes, Pods, annotations) and is summarized by `cluster_state_v2.go`. Status is an *observation* the operator publishes — never read your own status back as authoritative state; re-derive from the cluster.
- **No reconciler-owned state.** Don't cache progress on the `SkyhookReconciler` struct between reconciles — pods restart, leadership changes, memory vanishes. Persist anything that must survive to the apiserver (annotations on the Node, status subresource, finalizers).
- **Requeue, don't loop.** Need to re-check in 30s? Return `ctrl.Result{RequeueAfter: 30*time.Second}`. Need to retry on error? Return the error. Don't `time.Sleep` inside reconcile.
- **Finalizers for cleanup.** Anything the operator creates outside the owner-reference tree (host-side mutations, taints, annotations on Node objects) must be reversed via the `nodewright.nvidia.com/nodewright` finalizer. The finalizer block in `skyhook_controller.go` is the template.
- **Owner references for K8s-native cleanup.** Pods, ConfigMaps, etc. that belong to a Skyhook CR should have it as their owner reference so they GC automatically. `controllerutil.SetControllerReference` is the right call.
- **Status writes go through the subresource.** Use `.Status().Update()` / `.Status().Patch()`, not a plain `Update()`, so status and spec updates don't race the webhook.
- **Errors escape to the queue.** Returning an error from Reconcile triggers controller-runtime's exponential backoff requeue — that's the mechanism. Don't swallow errors to "avoid retries"; if an error is expected (e.g., transient conflict), log it and return `nil` with an explicit RequeueAfter.

### Kubernetes patterns

- **Watch, don't poll.** controller-runtime gives you informers — use `ctrl.Watches` / `source.Kind` and enqueue reconciles from events rather than loops with `time.Sleep` / `ticker`. Interrupt-budget and backoff *requeue* are fine (`ctrl.Result{RequeueAfter:…}`), but a goroutine polling the apiserver is not.
- **Create-or-update for mutable resources.** `IgnoreAlreadyExists` silently keeps stale state around. Use `controllerutil.CreateOrUpdate` (or `CreateOrPatch`) for ConfigMaps, RBAC, pods you re-generate. `IgnoreAlreadyExists` is only appropriate for immutable kinds (Namespace, a fresh ServiceAccount you don't mutate).
- **Use the `dal` and `wrapper` layers.** Don't reach for `controller-runtime` `client.Client` directly from new controller code if `internal/dal` already exposes the call — it's the seam tests mock against.

### Constants

No `pkg/defaults` package here — but the principle still applies: extract repeated literals (timeouts, annotation keys, label values, image names) into named constants in the nearest appropriate file. The annotation and finalizer names already live in `controller/annotations.go` and `controller/skyhook_controller.go`; follow suit.

### Comments

**Default to no comments.** Well-named identifiers, small functions, and idiomatic control flow should tell the story. **Add a comment when — and only when — the code is doing something odd, surprising, or that breaks a pattern a reader would otherwise assume.** In that case the comment **must** explain both:

- **What** is unusual (so the reader knows this isn't the pattern to copy), and
- **Why** it's done this way (the hidden constraint, subtle invariant, ordering dependency, known bug, webhook race, performance reason, upstream library quirk, etc.).

Examples of comments worth writing:

```go
// Patch instead of Update here: the node's status may have been mutated
// by the kubelet between our Get and Update, and a full Update would
// stomp the kubelet's changes.

// Skip the finalizer wait on the DeploymentPolicy: cluster-scoped CRs
// have no owner references back to the operator deployment, so if the
// operator is being uninstalled first, nothing will ever release it.

// We intentionally do NOT cache this in the reconciler — leader-election
// rollover would keep stale values across pod restarts.
```

Examples to **not** write:

- **Narration that restates the code:** `// increment i` next to `i++`. Delete.
- **PR / issue / fix references without substance:** `// added for PR #123` or `// fix for the bug last week`. These rot; put the PR description in the PR, and explain the *reason* in the comment (e.g., "apiserver returns empty status on first create; re-read once").
- **TODOs without a name, date, or concrete trigger.** If you must leave one, say who, when, and under what condition it should be revisited.

If you find yourself writing a long comment to explain a clever block, consider whether a rename, an extraction, or simply *not being clever* would remove the need for the comment altogether. Comments are a signal that the code couldn't explain itself — a last resort, not a decoration.

### Tests

- **Ginkgo/Gomega**, not stdlib `t.Run` — match the file you're adding to. `suite_test.go` under each package sets up envtest. New spec blocks go under existing `Describe` where possible. Table-driven tests inside Ginkgo are fine (and common in this repo) — use `DescribeTable` / `Entry`, or just iterate a slice of cases inside an `It`. The rule is "don't reach for `t.Run`", not "no tables".
- Run a single describe with `ginkgo --focus "text"`.
- Unit tests use **envtest** (a fake apiserver); e2e tests use **chainsaw** against a kind cluster. Don't mix the two — a test that needs real pods running belongs under `k8s-tests/chainsaw/`, not `internal/controller`.
- For mocking, regenerate with `make generate-mocks` after editing an interface — hand-written mocks under `internal/controller/mock/` will drift.

### Anti-patterns

| Anti-pattern | Correct approach |
| --- | --- |
| Edit a file you haven't read | `Read` before `Edit` |
| Add flags / knobs / abstractions not requested | Build only what was asked |
| Invent a new error/logging/testing style | Match the nearest existing file |
| Return bare `err` | Wrap with `fmt.Errorf("…: %w", err)` |
| `context.Background()` inside reconcile | Pass `ctx` from Reconcile through |
| `http.DefaultClient` | `&http.Client{Timeout: …}` |
| Polling loop against the apiserver | `Watches` + event-driven enqueue |
| `IgnoreAlreadyExists` on a mutable resource | `controllerutil.CreateOrUpdate` |
| Magic literal timeouts / annotation keys scattered around | Named constant near the owner |
| Ignore `Close()` on writable handles | Capture + check `closeErr` |
| Skip `make fmt` before commit | License headers + gofmt are part of the gate |
| Edit `zz_generated.*` or `config/crd/bases/*` by hand | Change the source, then `make manifests generate` |
| Comment that narrates what the code does | Delete it — or rename the code so it explains itself |
| Odd / pattern-breaking code without a `// why:` comment | Add a comment that names the constraint or invariant forcing the deviation |

### Decision framework

When choosing between approaches, prioritize:

1. **Testability** — can it be covered by envtest without a real cluster?
2. **Readability** — will the next person on pager-duty understand it at 3am?
3. **Consistency** — does it match neighboring code?
4. **Simplicity** — is it the smallest solution that works?
5. **Reversibility** — can we back it out without a migration?

### Design principles

- Partial failure is the steady state — nodes come and go, pods die, webhook certs rotate. Design for timeouts, bounded retries, and idempotent reconciles.
- Observability is mandatory — structured `logr` messages, metrics via `controller/metrics.go`, events via the event recorder.
- Boring first — prefer proven controller-runtime idioms over clever abstractions.

## Issue and PR metadata (labels, type, priority)

- **`.github/labels.yml` is the desired, closed set of labels.** It is the only source of truth, synced to GitHub with `make labels`. Because that sync creates and updates but never deletes, GitHub may currently carry undeclared extras; treat any label not defined in the file as one that should not exist, never as precedent for adding more.
- **Never create a label.** No `gh label create`, no `gh issue create --label <something-new>`, no label creation through `gh api`. `make labels` does not prune, so a label invented once lingers on the repo forever until someone deletes it by hand. If a label you want is genuinely missing, add it to `.github/labels.yml` in a PR and stop there; do not create it on GitHub and backfill the file later.
- **Priority is not a label.** It is a native GitHub issue Field (Urgent / High / Medium / Low) in the issue sidebar. **Maintainers set it during triage; agents and issue reporters do not set it at all.** Never apply `priority/*`, `P0` / `P1` / `P2`, or any severity label, and never write the Priority field from automation. If you think something is urgent, say so in the issue body and let a maintainer decide.
- **Type is not a label either.** Bug / Enhancement / Documentation / Task / Epic / Initiative are native GitHub Issue Types, assigned by the issue forms via their `type:` key. `doc` is the one exception, and only because the PR path labeler applies it to docs-only PRs.
- **When filing an issue, apply no labels yourself.** `.github/workflows/triage.yaml` adds `needs-triage` and infers `component/*` from the title and body; `.github/labeler.yml` handles PR path labels. Let that automation run rather than pre-labeling.

## Conventions and gotchas

- **Vendoring**: Go deps are vendored. `make build`/`make test` pass `-mod=vendor` via `GOFLAGS`; `go get` is still fine but follow with `go mod vendor`.
- **License headers**: every source file needs an Apache-2.0 header. `make license-fmt` (which runs as part of `make fmt`) shells out to [`addlicense`](https://github.com/google/addlicense) with `scripts/license-header.tmpl` — don't copy-paste headers by hand. It only **adds** headers to files that lack one; it never rewrites an existing header, so the copyright year is the year of first publication and is not bumped on edit (matching Google/Kubernetes practice). Files carrying a generated-code marker (`// Code generated … DO NOT EDIT.`) are skipped, which is a content check, not a filename check: a hand-written `zz_generated.*` would still get a header, and a generated file without the marker would too. `make license-header-check` is the CI gate; it fails if a tracked source file has no header, or has one that isn't `SPDX-License-Identifier: Apache-2.0`. New Go files get `//` line comments rather than the `/* */` block used by older files: a block comment above a `//go:build` constraint is not legal, so gofmt hoists the constraint and the two tools fight forever.
- **Commits**: Conventional Commits format. Every commit must be both signed off and cryptographically signed: `git commit -s -S`. `-s` adds the DCO `Signed-off-by` trailer, `-S` attaches the GPG/SSH signature that `main`'s `required_signatures` ruleset and the Commit Requirements workflow check for. They are independent, so neither flag substitutes for the other, and both are needed on amends too (`git commit --amend -s -S --no-edit`). Signing needs an unsandboxed shell. No pseudonyms.
- **Force-pushing a PR branch**: review fixes are folded into the existing commit (`git commit --amend -s -S --no-edit`) and force-pushed, so the branch's remote history is rewritten routinely. Always push with `git pushf`, the repository alias for `git push --force-with-lease --force-if-includes`. Never use a bare `git push --force`: it overwrites whatever is on the remote without looking. `--force-with-lease` refuses when the remote ref has moved since your last fetch, and `--force-if-includes` additionally refuses when your local branch has not incorporated what you last fetched, which catches the case where you fetched but never rebased. A `stale info` rejection is that guard working and **is not a cue to escalate to `--force`**. Fetch, then find out what moved: a maintainer merging `main` into the branch from the GitHub UI, a co-author's commit, or a bot's fixup all land on the branch and all look identical at this point. Fetch, then find out what moved and integrate it. Note that `--force-if-includes` tests **reachability, not content**: if you *rebase* onto `main` rather than merging the remote tip, the remote's commit stops being an ancestor and `git pushf` keeps refusing even though your tree is correct. Either merge the remote tip so it stays in history and push normally, or, once `git diff <remote-ref> HEAD` shows the only difference is your own work, name the ref you inspected explicitly: `git push --force-with-lease=<branch>:<sha-you-inspected>`. That is still a lease, not a bare `--force`: it overwrites only if the remote is exactly the commit you looked at.
- **Docs are part of every PR.** If your change alters user-visible behavior (CRD field, CLI command/flag, env var, annotation, metric, lifecycle semantics, install/upgrade flow) or an architectural concept captured in `docs/`, update the affected doc page **in the same PR** — not a follow-up. If a doc page is wrong or stale because of your change, fix it. If your change makes an existing doc obsolete, delete the stale section rather than leaving two sources of truth. **A behavior-changing PR without a corresponding `docs/` update will be rejected in review** — treat this as a blocking requirement, not a nicety. Docs-only changes are fine on their own, but code changes without docs are not.
- **`RELEASE_NOTES.md` is not a changelog, and most PRs do not belong in it.** Each component's `CHANGELOG.md` is generated from commit history and already records every change, so a hand-written note is duplication unless the reader has to **do** something or will be **surprised**: an action required at upgrade time, a deprecation with a deadline, a behavior they will notice and did not ask for, a new knob worth discovering. A bug fix that restores intended behavior needs no entry, however involved the fix was. Default to not adding one; the commit message and the linked issue carry the reasoning. Every entry that is only narrative makes the entries that matter harder to find.
- **Container runtime**: the operator Makefile defaults to `podman` locally, `docker` in CI. Override via `DOCKER_CMD=docker`.
- **`make run` is a background process**: it writes its PID to `reporting/int/run.PID`. Always pair with `make kill` before re-running; otherwise you'll have two managers fighting for leadership.
- **`operator/config/` ↔ `chart/` must stay in sync**: `operator/config/` (kustomize) and `chart/` (Helm) describe the same deployment surface through two tools. Any change to one **must be mirrored into the other in the same PR** — a new RBAC rule, env var, volume mount, resource limit, webhook wiring, or CRD under `operator/config/` is only half-done until the equivalent edit lands under `chart/templates/` (and `chart/values.yaml` if it's a knob). `make generate-helm` can draft the mirror by running `helmify` over the kustomize output, but it overwrites hand-tuned chart files — read the diff carefully, revert the parts it gets wrong (templating variables, comments, ordering), and never commit the output blindly. For small changes, it's usually faster to hand-mirror than to re-generate.
- **CLI ↔ operator contract must stay in sync.** The CLI (`operator/cmd/cli/`) is a client of the operator — it reads/writes CRs, annotations (`nodewright.nvidia.com/pause`, `…/disable`, `…/nodeState_*`, etc.), status fields, and relies on well-known label/finalizer names. When an operator change touches any of these surfaces (renamed annotation, new CRD field the CLI should surface, changed status shape, removed feature, new lifecycle operation), update the CLI **in the same PR**. A PR that changes a contract surface in `operator/` without a corresponding CLI update — or a justification for why the CLI is unaffected — should be rejected in review.
- **CLI must remain backward-compatible with older operators where at all possible.** The CLI is distributed independently of the operator (users upgrade on different schedules) and is expected to work against any supported operator version. When adding a feature that depends on new operator behavior: feature-detect via `skyhook version` / CR schema presence / annotation probe, and fall back gracefully or emit a clear error naming the minimum operator version required. Do not silently no-op. The compatibility matrix in `docs/user-guide/cli.md` (e.g., the v0.7.x vs v0.8.0 pause/disable table) is authoritative — every command with version-gated behavior **must** appear there.
- **When breaking CLI backward compatibility is unavoidable**: (1) emit a runtime warning from the CLI when it detects an incompatible operator (naming the operator version it saw and the minimum required), (2) update the `docs/user-guide/cli.md` compatibility matrix and note the break in prose, (3) record the break in `operator/cmd/cli/CHANGELOG.md` with a migration note, (4) bump the CLI major/minor per `docs/operations/versioning.md`. Silent breakage is not acceptable — operators in production may lag the CLI by months, and a CLI that misbehaves without explanation is worse than one that refuses to run.
- **Per-component version tags.** Components are released independently — tags are prefixed with the component name: `operator/v0.15.0`, `cli/v0.3.0`, `agent/v6.4.0`, `chart/v0.15.0`. `git tag --list 'operator/*' --sort=-v:refname | head -1` is how the operator Makefile computes `GIT_TAG_LAST`. A plain `git log --tags` without this knowledge is confusing. Each component also has its own `CHANGELOG.md` (root for chart+agent, `operator/` and `operator/cmd/cli/` for the Go components). See `docs/operations/versioning.md` / `docs/contributing/release-process.md`.
- **`make test` in `operator/` is heavy**: it runs unit + four flavors of e2e and expects a running cluster. For quick iteration use `make unit-tests` (or `make watch-tests`).
