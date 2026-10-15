# Package Artifact Design

This page is the design behind [Package artifacts](lifecycle.md#package-artifacts):
which code reads a package artifact, in which binary, and why each choice was
made. It does not restate what other pages document:

- [Lifecycle of a NodeWright](lifecycle.md#package-artifacts) describes what the
  operator does with an artifact: the inspection, its outcomes, caching and
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
| `artifact/client` | `NewReference`, `ParsePinned`, `LookupCredential`, `NewRemote`, `NewLayout`, `Inspect`, `ExtractLayer` and `Layout` | Registries, OCI image layouts, and the directory a payload is laid out in |
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

- **The operator** inspects a package's reference, reading the manifest and, for
  an artifact, its metadata, never a layer. From that it chooses the stage Job,
  pins it to the inspected digest, and decides whether the package's uninstall
  may run. It uses `NewReference`, `LookupCredential` over its image pull
  Secret, `NewRemote` and `Inspect`, in
  [`package_artifacts.go`](../../operator/internal/controller/package_artifacts.go),
  [`job_builder.go`](../../operator/internal/controller/job_builder.go) and
  [`uninstall_decisions.go`](../../operator/internal/controller/uninstall_decisions.go).
  With `PACKAGE_ARTIFACTS` off, none of it runs.
- **The agent** parses the pinned reference the operator gave it with
  `ParsePinned`, looks its credentials up in the order
  [`agent/README.md`](../../agent/README.md#package-artifacts) gives, inspects
  it, and lays the payload out with `Layout`, in
  [`agent/internal/agent/artifact.go`](../../agent/internal/agent/artifact.go).
  `Layout` builds the payload in a staging directory, checks it, and only then
  renames or swaps it into place, so a failure leaves the destination as it was.
  The destination is `COPY_DIR`, where the legacy `<package>-init` container
  copied a package image, so every step path stays the same (see
  [Side-loading](lifecycle.md#side-loading-what-a-script-can-actually-see)).
- **The CLI** lives in the operator module, under `operator/cmd/cli`, so it
  already vendors the library. It is to validate a package with the same calls:
  `NewLayout` for a local OCI image layout or `NewRemote` for a registry, then
  `Inspect`, then `Layout` into a scratch directory. No command does this yet;
  see [CLI validation](#cli-validation).

---

## Inspection, caching, fallback, Jobs and uninstall

### Why an inspection reads no layer

The operator decides on every reconcile pass, and everything it decides on is in
the manifest and the metadata blob: the form of the package, its uninstall
declaration, its host-files layers and its interrupt. The metadata carries the
package's `config.json` so that its steps can be read without a layer. Layers
are the payload, which can be of any size and which only the node needs. The
specification [bounds](../user-guide/package-artifact-spec.md#bounds) the
manifest and the metadata, so an inspection costs the operator little whatever
the package, and the operator never downloads a payload it does not run.

### What is cached, and why

The operator is level-triggered, so without a cache every pass would ask every
registry about every package it decides on. What the cache keeps follows from
what can change:

- **The content at a digest cannot change**, so the answer for a digest is kept
  for the life of the process.
- **A tag can move**, so its resolution is refreshed after a while, and a
  corrected push to the same tag is picked up without a change to the spec.
- **A failure is remembered**, under the reference, and under the registry host
  when the failure is the host's own (a timeout, no connection, or a registry
  that keeps answering it is unavailable). One registry that is down then costs
  one bounded inspection per interval, not one per package per pass.

The cache holds only what registries answered. The progress of a package lives
on the Node, as for any package, so a restart or a leader election that empties
the cache loses nothing the next inspection cannot recover. The durations and
bounds are in [Caching](lifecycle.md#caching).

### Why a failed inspection falls back to the unchanged Job

An inspection that fails tells the operator only that it could not reach the
registry or authenticate to it. The nodes may still be able to: the operator
uses only its image pull Secret, while the kubelet also has the node's
credentials (see [Registry credentials](lifecycle.md#registry-credentials)). So
the unknown outcome gets the Job the operator built before package artifacts
existed. Existing container-image packages keep running when only the operator
has lost the registry, and a reference that is an artifact after all fails
visibly at the image pull, because an artifact is not a runnable image, before
anything runs on the host. Treating an unknown outcome as a rejection would
instead stop every image package for the length of a registry outage.

### Why an existing Job keeps the reference it was built with

Deciding whether a Job is stale makes no registry request. An outage, a slow
registry or a re-pushed tag therefore never recreates or interrupts a running
stage: an artifact Job runs on the digest it was pinned to, and the next Job
built pins whatever the tag resolves to then. The work and check containers of
one Job get the same `NODEWRIGHT_PACKAGE_REF`, so a check step always checks the
digest its work step applied. What does replace a Job is a change to the
package in the spec, such as another repository or `containerSHA`, or a cached
answer that the reference of an image Job is an artifact;
[Staleness](lifecycle.md#staleness) lists the rules.

### Why an uninstall is deferred rather than guessed

At the uninstall stage the operator does the opposite of the install-side
fallback. An artifact's `uninstall` declaration bounds what the spec may do (see
[Precedence](../user-guide/uninstall.md#precedence)), and an uninstall cannot be
undone once it has run on a host. Falling back to the spec on an unknown outcome
could run an uninstall the artifact declares unsupported, so the operator starts
none until an inspection succeeds, even for a reference that turns out to be a
container image (see
[Deferred uninstall](../user-guide/uninstall.md#deferred-uninstall)). The
install-side fallback is safe only because an image package then runs exactly
as it did before package artifacts, and an artifact fails before it runs.

---

## Provenance

From the operator's inspection to the agent's layout, every read is of the same
content:

- `Inspect` returns the resolved manifest `Descriptor`, with its media type,
  digest and size, and `Pinned`, the repository and that digest
  (`registry/repository@sha256:<hex>`).
- Every later read is by digest, and is checked against its descriptor:
  content that does not match fails with `ErrDigestMismatch`
  ([`client.go`](../../artifact/client/client.go)). The manifest names the
  digest of the metadata and of each layer, so its digest covers them all.
- The operator hands the agent exactly that digest in `NODEWRIGHT_PACKAGE_REF`,
  and `ParsePinned` refuses anything but a digest, so the agent cannot follow a
  tag that moved after the inspection.

A verifier of the signatures or attestations that the OCI referrers API lists
for that digest can therefore run in two places: in the operator, between
`Inspect` and building the Job, where `classifyPackageArtifact` turns an
inspection into an outcome, and in the agent, before `Layout`, where
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
