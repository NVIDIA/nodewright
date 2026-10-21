# Package Artifact Design

This page is the design behind [Package artifacts](lifecycle.md#package-artifacts):
which code reads a package artifact, in which binary, and why each choice was
made. It does not restate what other pages document:

- [Lifecycle of a NodeWright](lifecycle.md#package-artifacts) describes what the
  operator does with an artifact: the preflight stage, its outcomes, registry
  credentials and staleness.
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
| `artifact/client` | `NewReference`, `ParseReference`, `ParsePinned`, `LookupCredential`, `MatchImage`, `NewRemote`, `NewLayout`, `Inspect`, `ExtractLayer` and `Layout` | Registries, OCI image layouts, and the directory a payload is laid out in |
| `artifact/preflight` | The preflight result document (`Result`, `Encode`, `Decode`) and `Classify`, which turns an inspection, or the error that ended it, into an outcome | Nothing: it encodes and decodes bytes |
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

- **The operator** never reads an artifact. It builds a package's preflight Job,
  reads the result from the pod status with `preflight.Decode`, records it in
  node state, and builds the package's later Jobs on that node from it, pinning
  an artifact Job to the recorded digest. It decides whether a node may start the
  package's uninstall from the same record. See
  [`package_artifacts.go`](../../operator/internal/controller/package_artifacts.go),
  [`job_builder.go`](../../operator/internal/controller/job_builder.go),
  [`job_controller.go`](../../operator/internal/controller/job_controller.go) and
  [`uninstall_decisions.go`](../../operator/internal/controller/uninstall_decisions.go).
  With `PACKAGE_ARTIFACTS` off, it builds no preflight Job.
- **The agent, in its preflight mode,** parses the reference the operator gave it
  with `ParseReference`, looks its credentials up as the kubelet would, inspects
  it with `Inspect`, classifies it with `preflight.Classify`, and writes the
  result to its termination message with `preflight.Encode`, in
  [`agent/internal/agent/preflight.go`](../../agent/internal/agent/preflight.go).
- **The agent, at an artifact stage,** parses the pinned reference the operator
  gave it with `ParsePinned`, looks its credentials up in the order
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

## Preflight, fallback, Jobs and uninstall

### Why the agent inspects, on the node

A package's container image is pulled by the kubelet of each node, with that
node's credentials: the pod's pull secrets, the node's Docker configs and the
kubelet's credential provider plugins, which reach a cloud registry through the
node's own identity and can differ between node pools. Only code on the node can
use all of them, so the inspection runs there, in the package's `preflight`
stage, and an artifact package authenticates exactly as the same package
published as an image would. The operator then needs no registry access, no
credentials and no cache of registry answers.

### Why an inspection reads no layer

Everything the operator decides on is in the manifest and the metadata blob: the
form of the package, its uninstall declaration, its host-files layers and its
interrupt. The metadata carries the package's `config.json` so that its steps can
be read without a layer. Layers are the payload, which can be of any size and
which only the stage that installs it needs. The specification
[bounds](../user-guide/package-artifact-spec.md#bounds) the manifest and the
metadata, so a preflight costs little whatever the package.

### Why the result travels in the termination message

The agent must hand the operator a structured result, and a package pod has no
API access: it runs without a ServiceAccount token, and giving a privileged host
pod write access to the API would widen what a compromised package can do. The
container termination message is the one channel the kubelet already carries
from a container to the pod status the operator reads. The result is a versioned
JSON document, defined once in `artifact/preflight` for both binaries; its
container sets `terminationMessagePolicy: File`, never `FallbackToLogsOnError`,
so no log line can stand in for a result; and it fits the kubelet's 4096-byte
limit by construction, dropping its free-text detail if it has to. The operator
reads nothing else: a missing, malformed, oversized or unknown-version document,
or one for another reference, is an unknown outcome.

### Why the result is recorded per node, and kept

The result is written to the package's entry in node state, which outlives the
pod, the Job and the operator process, and every later Job of that version on
that node is built from it. A node therefore never mixes content within one
version: its apply, config, post-interrupt and uninstall Jobs all run the digest
its preflight resolved, and a tag re-pushed in between reaches the node only at
its next install. Per node, because nodes can disagree: a node pool that cannot
reach the registry records an unknown outcome while another records the
artifact.

### Why a failed preflight falls back to the unchanged Job

A failed inspection tells the operator only that the node could not reach the
registry or authenticate to it, for the reference it was given. Since the
inspection used the kubelet's own credentials, such a reference is most likely
an existing container-image package that the kubelet can still pull, or one
whose registry is down. So the unknown outcome gets the Job the operator built
before package artifacts existed: image packages keep running through an outage
of the inspection path, and a reference that is an artifact after all fails
visibly at the image pull, before anything runs on the host. An agent without
the preflight mode leaves no result, and lands on the same path.

### Why a rejection holds the package rather than falling back

A rejected artifact is known not to be a runnable image, so its Job could only
fail. The package stays at its `preflight` stage, erroring, with its node
erroring as for a failed stage, so the node goes no further with it and a
DeploymentPolicy counts it as failed. Preflight runs again after 5 minutes,
which picks up an artifact corrected under the same tag, and the wait doubles
for each further rejection of the same reference on that node, up to an hour, so
a fleet holding one rejected artifact does not run a preflight Job on every node
every 5 minutes until the spec is fixed.

### Why an uninstall uses the recorded result

An uninstall must remove what was installed. Inspecting the reference again
could find different content under the tag, or none, and would make the
uninstall depend on the registry. So the uninstall runs no preflight: it is
pinned to the digest the node recorded, follows that artifact's uninstall
declaration, and runs as a container image wherever the package was installed as
one, including before the option was turned on.

---

## Provenance

From the operator's inspection to the agent's layout, every read is of the same
content:

- `Inspect`, in the agent's preflight, returns the resolved manifest
  `Descriptor`, with its media type, digest and size, and `Pinned`, the
  repository and that digest (`registry/repository@sha256:<hex>`), which the
  operator records on the node after checking that it names the requested
  repository, and the requested digest when there is one.
- Every later read is by digest, and is checked against its descriptor:
  content that does not match fails with `ErrDigestMismatch`
  ([`client.go`](../../artifact/client/client.go)). The manifest names the
  digest of the metadata and of each layer, so its digest covers them all.
- The operator hands every later stage exactly that digest in
  `NODEWRIGHT_PACKAGE_REF`, and `ParsePinned` refuses anything but a digest, so
  the agent cannot follow a tag that moved after the preflight.

A verifier of the signatures or attestations that the OCI referrers API lists
for that digest can therefore run in two places, both in the agent: in the
preflight, between `Inspect` and `preflight.Classify`, so that a package that
does not verify is rejected before any stage runs, and before `Layout`, where
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
[specification](../user-guide/package-artifact-spec.md#layers). Preflight
rejects a package that declares one, and the agent refuses it at a stage, so
neither ignores host files silently. `Layout` lays out only the payload.

**Plugs in.** Placing a host-files layer can reuse `ExtractLayer`
([`extract.go`](../../artifact/client/extract.go)) on a root opened at the
layer's destination. It already lays out either kind of layer relative to that
root and never follows a symlink beneath it.

### Dynamic interrupts and preflight

**Exists.** `Package.Interrupt` holds the package's
[interrupt declaration](../user-guide/package-artifact-spec.md#interrupt-declaration),
its `sense` and `run` steps, parsed and validated. Every preflight already
reads it, and today preflight rejects a package that declares one and the agent
refuses it. The `preflight` stage itself exists, runs before cordon and drain,
and reports a versioned result the operator records per node.

**Plugs in.** The package's
[`interrupt`](../user-guide/custom-resource.md#interrupt) field in the
NodeWright spec is to become an override of the declaration. The preflight can
run `sense` and report its answer in a later version of its result, so a node is
cordoned and drained only when a package reports that it needs an interrupt,
which `run` then performs. The [ROADMAP](../../ROADMAP.md#2-feature-completeness)
describes both.

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
