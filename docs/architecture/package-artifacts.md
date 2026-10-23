# Package Artifact Design

This page is the design behind [Package artifacts](lifecycle.md#package-artifacts):
which code reads a package artifact, in which binary, and why each choice was
made. It does not restate what other pages document:

- [Lifecycle of a NodeWright](lifecycle.md#package-artifacts) describes what the
  operator does with an artifact: resolution, the cache, the outcomes and
  staleness.
- [Uninstall](../user-guide/uninstall.md#uninstall-support-declared-by-a-package-artifact)
  describes how an artifact's declaration bounds an uninstall.
- The [package artifact specification](../user-guide/package-artifact-spec.md)
  defines the format.
- [`agent/README.md`](../../agent/README.md#package-artifacts) describes what the
  agent does with an artifact.

---

## The shared library

Every binary that reads a package artifact does so through one Go module,
`github.com/NVIDIA/nodewright/artifact`, in [`artifact/`](../../artifact/go.mod).
It has three packages:

| Package | What it holds | What it touches |
|---|---|---|
| `artifact` | `Classify`, `Manifest.ParseMetadata` and `TarEntryName`, with the media types, bounds, errors and reasons the specification names | Nothing: it takes bytes and descriptors, and never reaches a registry, the network or the filesystem |
| `artifact/client` | `NewReference`, `ParsePinned`, `LookupCredential`, `NewRemote`, `NewLayout`, `Resolve`, `Inspect`, `ExtractLayer` and `Layout` | Registries, OCI image layouts, and the directory a payload is laid out in |
| `artifact/registrytest` | An in-process registry, and helpers that push packages, images and indexes to it | Tests only |

The root package is the specification as code. Because it is pure, each rule is
a function of its input, and
[`specdoc_test.go`](../../artifact/specdoc_test.go) checks the examples on the
specification page against it, so they cannot drift from the code.

### Why a separate module

The operator and the agent both read artifacts, and neither may import the
other. The agent, a static binary that never calls the Kubernetes API, must
also not pull in Kubernetes. A package inside either module would break one of
those rules, so the library is a module of its own, and the depguard rule in
[`artifact/.golangci.yml`](../../artifact/.golangci.yml) keeps it that way: it
denies `k8s.io`, `sigs.k8s.io`, and both the operator and the agent modules.

### Why `replace` and `vendor`

The repository has no `go.work`, and the operator and the agent both build with
`-mod=vendor`, their container images included. So each requires the library at
the zero pseudo-version, `v0.0.0-00010101000000-000000000000`, through
`replace github.com/NVIDIA/nodewright/artifact => ../artifact`, and
`go mod vendor` copies it into its `vendor/` tree. The library therefore ships at
the same commit as the binary that uses it, and has no release or version of its
own. Every module it shared with the agent was pinned at the agent's version,
so requiring the library moved no version in the operator, and in the agent
only `golang.org/x/sync`, an indirect dependency that oras-go requires at the
next minor version.

The cost is that every change under `artifact/` must be re-vendored in both
modules and committed with it. CI enforces that: operator-ci runs
`make -C operator vendor-check` ([`operator/Makefile`](../../operator/Makefile)),
agent-ci runs the same check for `agent/`
([`agent-ci.yaml`](../../.github/workflows/agent-ci.yaml)), and artifact-ci
([`artifact-ci.yaml`](../../.github/workflows/artifact-ci.yaml)) tests and lints
the module itself, also whenever the specification page changes.

---

## Roles

Each binary uses the library in its own role:

- **The operator** resolves a package's reference to its manifest digest, and
  inspects each digest once, reading the manifest and, for an artifact, its
  metadata, never a layer, into a cache keyed by digest. It uses `NewReference`,
  `LookupCredential` over its pull Secrets, `NewRemote`, `Resolve` and `Inspect`,
  in [`package_artifact_inspector.go`](../../operator/internal/controller/package_artifact_inspector.go)
  and [`package_artifact_cache.go`](../../operator/internal/controller/package_artifact_cache.go).
  From what the cache holds for the digest, the reconcile pass chooses the stage
  Job, pins it to the digest, and decides whether the package's uninstall may
  run, in [`package_artifacts.go`](../../operator/internal/controller/package_artifacts.go),
  [`job_builder.go`](../../operator/internal/controller/job_builder.go) and
  [`uninstall_decisions.go`](../../operator/internal/controller/uninstall_decisions.go).
  With `PACKAGE_ARTIFACTS` off, nothing is resolved and the cache is not read.
- **The agent** parses the pinned reference the operator gave it with
  `ParsePinned`, looks its credentials up in the pull Secret files the operator
  mounted, in the order
  [`agent/README.md`](../../agent/README.md#package-artifacts) gives, inspects
  it, and lays the payload out with `Layout`, in
  [`agent/internal/agent/artifact.go`](../../agent/internal/agent/artifact.go).
  `Layout` builds the payload in a staging directory, checks it, carries over
  what the destination already holds beyond the payload, and only then renames
  or swaps it into place, so a failure leaves the destination as it was. A
  staging directory that a killed layout leaves is removed by the next layout
  of the same destination, which skips any that a running layout holds a lock
  on. The destination is `COPY_DIR`, where the legacy `<package>-init`
  container copied a package image with a merging `cp -r`, so every step path
  stays the same, and a file a step left there in an earlier stage is still
  there (see
  [Side-loading](lifecycle.md#side-loading-what-a-script-can-actually-see)).
- **The CLI** lives in the operator module, under `operator/cmd/cli`, so it
  already vendors the library. It is to validate a package with the same calls:
  `NewLayout` for a local OCI image layout or `NewRemote` for a registry, then
  `Inspect`, then `Layout` into a scratch directory. No command does this yet;
  see [CLI validation](#cli-validation).

---

## Resolution, the cache, Jobs and uninstall

### Why an inspection reads no layer

Everything the operator decides on is in the manifest and the metadata blob: the
form of the package, its uninstall declaration, its host-files layers and its
interrupt. The metadata carries the package's `config.json` so that its steps
can be read without a layer. Layers are the payload, which can be of any size and
which only the node needs. The specification
[bounds](../user-guide/package-artifact-spec.md#bounds) the manifest and the
metadata, so an inspection costs the operator little whatever the package, and
the operator never downloads a payload it does not run.

### Why the cache is keyed by digest, and held in memory

The operator is level-triggered, so every pass needs what each package's
reference is, and asking the registry what a digest holds on every pass would
cost an inspection per package per pass. A digest names its content, so what an
inspection finds at a digest is true for as long as anyone asks: an entry keyed
by digest never goes stale, needs no invalidation, and two NodeWrights naming
the same content share it. What can change is which digest a tag names, which
one manifest request answers, so the pass resolves the tag and reads the cache
by the digest it got. An entry is removed once no NodeWright names its digest,
at the start of the next reconcile, so the cache holds about what the cluster's
NodeWrights use rather than every digest the operator has seen; a full one is
still emptied and filled again.

The cache is a memory-backed `emptyDir` on the operator pod rather than
anything persisted to the apiserver, because nothing in it needs to survive: a
cold cache, after a pod restart or a change of leader, is filled again by
inspecting the digests the passes ask for, and the outcome of each digest is the
same as before. The NodeWright's status therefore stays a published observation,
which the operator never reads back, and the inspector writes nothing to the
apiserver but Events. The cost of an empty cache is a short wait, with no Job,
for the packages whose digests are inspected again, and a registry outage at
that moment holds those packages rather than changing what they run.

### Why the tag is resolved at each stage

A pass resolves a tag only when some unfinished node would build a stage Job of
the package next, or when it decides the package's uninstall, and a node whose
Job runs costs nothing. The digest a tag names answers for it for 30 seconds,
for every package and NodeWright naming it, so a tag costs at most one request
per 30 seconds however large the fleet, although an unfinished NodeWright's pass
runs every 2 seconds. Resolving at each stage makes every Job run the digest the
tag named at most 30 seconds before the Job was built, pinned, so the work and
check containers of one Job, and the kubelet pulling a container image, never
see a tag move under them. Across stages it does not hold one digest: a tag
re-pushed mid-rollout reaches the stages built more than 30 seconds after the
push. Holding the
first digest for the rest of a rollout would need a record that outlives the
pod, which the cache deliberately is not; `containerSHA` is how a NodeWright pins
one digest for good.

### Why resolution runs in the pass and inspection outside it

The reconcile pass runs in one worker, so a registry that is slow or drops
connections would stall every NodeWright for as long as a request waited. A
resolution is one manifest request, bounded by 5 seconds; the pass makes one
per tag not resolved in the last 30 seconds, for every NodeWright at once. It
reads the registry hosts at once, and on each host one reference first, then
the rest 8 at a time. A host that could not be connected to opens its 30-second
window on that first reference, and every reference on it asked for in the
window is answered unavailable with no request, so a black-holed registry costs
one timeout per window, not one per package. A failure once connected, an error
status or a repository that does not answer in time, is the reference's own: the
host stays open to the others, so one repository that keeps failing never holds
back the healthy ones beside it. Once one of the 8 times out, the rest of the
host's references go at once, so a host whose repositories take the connection
and never answer costs a pass about three timeouts, not one per tag.
The operator's client never retries, so a host failing every repository costs
one round trip per reference. An inspection reads two blobs and
is bounded by 10 seconds, so it runs in the inspector, a manager runnable on the
leader with a few workers of its own: the pass queues the digest, gives the
package no Job, and runs again shortly to read the entry.

### Why pull Secrets are the only credentials

The operator uses only Secrets the chart names: they appear in every package
pod's spec, they behave the same on every distribution, and they do not change
when a kubelet's flags or file layout do. The agent pulls the payload with the
same Secrets, so what the operator could inspect the node can pull. The cost is
that a registry the nodes reach only through their own identity, such as ECR
with the instance role, needs a pull Secret while the option is on, for
container-image packages too, since the operator resolves every package it
builds a Job for.

### Why an unreadable reference gets no Job

An inspection or a resolution that fails tells the operator only that it could
not read the reference, not what the reference is. Running it as a container
image in its place would run an artifact as an image, which fails at the pull
with nothing gained, and would hide from the operator the very credential it
needs to run artifacts. So a reference the operator cannot read gets no Job, and
what happens next depends on why:

- **A failure that passes by itself**, a timeout, a host that could not be
  reached, or a 5xx, 429 or 408, makes the package wait. It is read again after
  30 seconds, so a registry blip delays a rollout and breaks nothing. A registry
  the operator can never reach, such as one its egress rules deny, looks the
  same: the package waits, with a Warning once per outage.
- **A failure that does not**, a refused credential, a missing tag, content that
  does not match its digest, a certificate the operator does not trust, a
  registry that speaks plain HTTP, a host name that does not resolve, or a 4xx
  about the request itself, fails the same way at every attempt, so it blocks
  the package, with the `Blocked`
  condition naming it and its nodes reporting `blocked`, so the cause is visible
  where an operator looks first. It is read again after 5 minutes, doubling up to
  an hour, so a Secret fixed later, or a push that comes later, is picked up
  without a change to the spec while a fleet holding one bad reference does not
  cost a request every pass.

The failures and their waits are kept in the inspector's memory; losing them on a
restart only reads a reference again sooner.

### Why an existing Job keeps the reference it was built with

Deciding whether a Job is stale reads neither the cache nor a registry. A Job
pinned to a digest of the package's repository matches the package whatever its
tag names now, so an outage, a slow registry or a re-pushed tag never recreates
or interrupts a running stage. The work and check containers of one Job get the
same `NODEWRIGHT_PACKAGE_REF`, so a check step always checks the digest its work
step applied. What does replace a Job is a change to the package in the spec,
such as another repository or `containerSHA`;
[Staleness](lifecycle.md#staleness) lists the rules.

### Why an uninstall waits for a readable reference

An uninstall cycle commits the node: the operator writes a `StageUninstall`
entry into its node state and, for a package with an interrupt, cordons and
drains it before the uninstall Job runs. A cycle that then cannot run stalls
there, as one of an artifact that declares uninstall unsupported does (see
[Stalled uninstall](../user-guide/uninstall.md#stalled-uninstall)). So the
operator starts no uninstall cycle for a package whose digest is pending
inspection, whose registry cannot be read, or that is blocked (see
[Deferred uninstall](../user-guide/uninstall.md#deferred-uninstall)).

---

## Provenance

From the operator's inspection to the agent's layout, every read is of the same
content:

- `Resolve` returns the manifest `Descriptor` a tag names, with its media type,
  digest and size, and the operator reads everything after it by that digest.
  `Inspect` returns the same `Descriptor`, and `Pinned`, the repository and that
  digest (`registry/repository@sha256:<hex>`).
- Every later read is by digest, and is checked against its descriptor:
  content that does not match fails with `ErrDigestMismatch`
  ([`client.go`](../../artifact/client/client.go)). The manifest names the
  digest of the metadata and of each layer, so its digest covers them all.
- The operator hands the agent exactly that digest in `NODEWRIGHT_PACKAGE_REF`,
  and `ParsePinned` refuses anything but a digest, so the agent cannot follow a
  tag that moved after the pass resolved it. The kubelet pulls a container-image
  package at the same digest, which the operator writes into its Job.

A verifier of the signatures or attestations that the OCI referrers API lists
for that digest can therefore run in two places: in the operator's inspector,
between `Inspect` and caching the outcome, where `classifyPackageArtifact` turns
an inspection into an outcome, and in the agent, before `Layout`, where
`refuseUnsupportedPackage` refuses what the agent cannot install. What it
verifies is exactly what runs. It starts from the `Descriptor` and `Pinned`
that `Inspect` already returns, so it can be added without changing the
signature of `Inspect` or `Layout`, and it checks the signatures a publisher
attaches to the manifest digest as they are. Nothing verifies today; runtime
verification is planned under Verifiable provenance in the
[ROADMAP](../../ROADMAP.md#3-production-hardening).

---

## Follow-ups

Each follow-up below builds on something that already exists.

### Host files

**Exists.** `Package.HostFiles` holds each host-files layer with its
`Destination`, parsed and validated against the
[specification](../user-guide/package-artifact-spec.md#layers). The operator
rejects a package that declares one, and the agent refuses it, so neither
ignores host files silently. `Layout` lays out only the payload.

**Plugs in.** Placing a host-files layer can reuse `ExtractLayer`
([`extract.go`](../../artifact/client/extract.go)) on a root opened at the
layer's destination. It already lays out either kind of layer relative to that
root and never follows a symlink beneath it.

### Dynamic interrupts and preflight

**Exists.** `Package.Interrupt` holds the package's
[interrupt declaration](../user-guide/package-artifact-spec.md#interrupt-declaration),
its `sense` and `run` steps, parsed and validated. Every inspection already
reads it, and today the operator rejects a package that declares one and the
agent refuses it.

**Plugs in.** The package's
[`interrupt`](../user-guide/custom-resource.md#interrupt) field in the
NodeWright spec is to become an override of the declaration. A new `preflight`
stage runs `sense` before cordon and drain, so a node is cordoned and drained
only when a package reports that it needs an interrupt, which `run` then
performs. The [ROADMAP](../../ROADMAP.md#2-feature-completeness) describes both.

### CLI validation

**Exists.** The calls [Roles](#roles) lists, vendored into the operator module
the CLI is built from.

**Plugs in.** A command under `operator/cmd/cli/app` can open the package with
`NewLayout` or `NewRemote`, inspect it, and lay it out into a scratch
directory. `Inspect` checks the manifest and the metadata, and `Layout` checks
every layer entry and `config.json`, so the command reports the errors and
reasons the operator and the agent would, before the package reaches a cluster.

### Verification

**Exists.** The pinning [Provenance](#provenance) describes: one digest from
`Inspect` to `Layout`, every read checked against it.

**Plugs in.** A verifier at the two points Provenance names can refuse a
package whose signatures or attestations do not verify, as the operator refuses
a rejected artifact and the agent refuses one it does not support.

---

See also [Publishing Package Artifacts](../user-guide/package-artifacts.md),
which shows how to build, push and convert a package artifact, and how to turn
package artifacts on.
