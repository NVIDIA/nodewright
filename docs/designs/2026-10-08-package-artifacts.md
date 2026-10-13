# Package artifacts (OCI/ORAS) design

This document is the design for NodeWright packages as OCI artifacts: epic #194 brought up to date against main at 6b58530c, tracked in #819. It is the plan the work is built from, as approved on 2026-10-08: the requirements, why the work is split the way it is, the contracts between the parts, each part's brief and acceptance, and the alternatives that were rejected. Each part has a sub-issue of #819 and lands as its own PR, stacked on the parts it builds on: #828 (artifact-spec), #829 (artifact-client), #830 (agent-streaming-install), #831 (operator-artifact-pods), then operator-uninstall-capability and artifact-docs-migration.

The normative package format is the package artifact specification, `docs/user-guide/package-artifact-spec.md`, which artifact-spec adds in #828; this document explains the system around it.

## Requirements

Source: GitHub epic https://github.com/NVIDIA/nodewright/issues/194 (filed 2026-04-07, milestone v1). This design is that epic brought up to date against origin/main at 6b58530c (2026-10-08). Background: the epic, ROADMAP.md (the paragraph "The shared core: Go agent and OCI/ORAS packages" and the v1 acceptance line "a package distributed as a signed OCI artifact is inspected by the operator and applied by the Go agent end-to-end"), docs/designs/uninstall-enhancement.md and agent/README.md.

### Already done

- The Go agent rewrite (epic sub-issues #213 to #222) is complete and cut over. The agent is the Go module at agent/ (github.com/NVIDIA/nodewright/agent), built by containers/agent.Dockerfile and tested by .github/workflows/agent-ci.yaml. There is no Python agent left to replace.
- Explicit uninstall opt-in exists in the API: Package.uninstall.enabled and Package.uninstall.apply in operator/api/v1alpha1/skyhook_types.go, per docs/designs/uninstall-enhancement.md. That design deliberately left capability discovery from the artifact out of scope. This design adds that discovery.
- Package image references are frozen to a bare registry/repository form (#282): Package.image carries no tag or digest, Package.version supplies the tag, Package.containerSHA pins a digest, and the webhook rejects inline tags and digests.

### How a package reaches a node today (origin/main)

createPodFromPackage in operator/internal/controller/job_builder.go builds one pod per node and stage. Its first init container is the PACKAGE IMAGE (getPackageImage in skyhook_controller.go: image:version, or image@containerSHA), privileged with the host root mounted, running `sh -c "mkdir -p /root/${SKYHOOK_DIR} && cp -r /skyhook-package/* /root/${SKYHOOK_DIR}"`. The next init containers are the AGENT IMAGE (getAgentImage) running the stage and then its check. The main container is pause. The agent (agent/internal/agent/package.go, ensurePackageData and loadConfig) reads config.json from the copied tree and runs the package's steps and interrupts against the host. So every package must be a runnable image that ships a shell and cp, and the operator learns nothing about a package (its steps, interrupts, uninstall support) without running it. ROADMAP.md says the OCI/ORAS package format "has not started".

### What to deliver

1. A versioned package artifact specification: an OCI artifact (artifactType, layer media types, manifest annotations) that carries the package payload as streamable layer(s) plus a machine-readable metadata/capabilities document the operator can read without pulling the payload. At minimum it declares whether uninstall is supported, and it carries the package's config.json content or an equivalent. Publish it as a document under docs/ for package authors, with the Go types and schema that implement it, and state how a maintainer builds and pushes one with oras or compatible tooling instead of docker build.
2. One shared Go package library used by both the operator and the agent (ROADMAP.md: "one shared Go library that all three binaries use"). It resolves a package reference (Package.image plus the version tag or the containerSHA digest), authenticates to the registry with the same credentials the package pods use today (image pull secrets or the node's registry credentials), reads the manifest and metadata WITHOUT pulling payload layers, and streams the payload layers onto the host staging directory. The repository has two vendored Go modules (operator/ and agent/) and no go.work; put the library where the module layout supports it and state why. The CLI's use of this library is a later roadmap item: shape the API so the CLI can adopt it unchanged, but add no CLI commands here.
3. The agent's streaming install path: for an artifact package the agent pulls the artifact itself and lays it out where ensurePackageData expects it today, so no package-image init container, and no shell or cp inside a package, is needed. Keep parity with every current stage and interrupt (apply, config, check, uninstall, upgrade, interrupts). Do not change on-host paths, completion markers or SKYHOOK_\* environment names; #437, #438 and #442 decide those separately.
4. Operator integration: for an artifact package, build the pod from the agent image alone, passing the reference and credentials the agent needs. When planning uninstall work, read uninstall support from the artifact metadata and behave deterministically when it is unsupported. Define and document the precedence between the artifact's declaration and Package.uninstall.enabled (for example, enabled: true against an artifact that declares uninstall unsupported must be surfaced, not silently run). Surface the discovered capability in CR status conditions or events. Keep pod-construction changes small and localized: the package-execution-as-Jobs migration (#223, docs/designs/2026-07-10-package-execution-as-jobs.md) is in flight on another branch and touches the same builder.
5. Migration: existing image-based packages keep working during a dual-support period. The reference stays Package.image with no CRD field rename; the operator and agent tell an artifact from a legacy image by inspecting the manifest, never by a new spec field. Document the conversion path for existing packages. The packages themselves live in https://github.com/NVIDIA/skyhook-packages; the conversion tooling or instructions belong in this repository.
6. CI, tests and docs: unit tests for the library, the agent path and the operator path run cluster-free against an in-process OCI registry or an OCI image layout. chainsaw, kind and e2e suites may be updated but cannot be run here and are not acceptance. .github/workflows/agent-ci.yaml and operator-ci.yaml must cover the new code. Package-author and architecture docs (docs/user-guide, docs/architecture, docs/index.yml, README references to skyhook-packages) describe the new format, the required metadata and the migration.

7. Design for two follow-ups the format and the discovery path must anticipate. Their declaration, validation and discovery are in scope here; executing them at runtime is not (see Out of scope), but nothing built here may preclude it.
   - Host files and binaries: a package can declare additional files or binaries, carried as their own artifact layer(s) distinct from the package payload, that the agent places on the host at declared destinations for the package's steps to use during execution. The manifest must tell payload layers and host-file layers apart and carry each layer's destination, so the agent's streaming path can later lay them out with the same code.
   - Dynamic interrupts: a package can declare an interrupt of its own, with both a sense step (does this package need to interrupt the node this time?) and the logic that executes the interrupt, instead of only the fixed set the operator and agent share today (operator/api/v1alpha1 InterruptType; agent/internal/interrupts: no_op, node_restart, service_restart, restart_all_services, script_interrupt). ROADMAP.md describes the runtime half as "Bring-your-own, discovered interrupts" and "Preflight stage and dynamic cordon/drain", with the spec's Package.interrupt becoming an override of what the package declares. Here, the artifact metadata must be able to carry such a declaration and the shared library must parse, validate and expose it to the operator and the agent.

### Acceptance (the epic's, brought current)

- A published, versioned spec for the OCI package artifact.
- The Go agent applies at least one representative package end to end from an OCI artifact with no package-image init copy step, demonstrated by a test against an in-process registry.
- The operator reads uninstall support from artifact metadata and behaves deterministically when uninstall is unsupported.
- Image-based packages still work unchanged, with tests proving both paths.
- CI builds and tests everything new; docs are updated for package authors.

### Out of scope

- CRD shape changes beyond what references and capabilities strictly need; no new API group work; no non-OCI storage backends.
- Runtime provenance verification (cosign, the OCI referrers API). ROADMAP.md says it lands with the OCI work, but it is a separate design. The library must expose the resolved descriptor and digest so a verifier can be added without reshaping the API.
- Client-side package validation in the CLI (roadmap). Renaming skyhook-named on-host paths, directories or SKYHOOK_\* environment variables (#437, #438, #442).
- Anything on the package-as-Jobs branch (#223).
- Executing dynamic interrupts, the preflight stage, dynamic cordon/drain, and placing declared host files or binaries at runtime (ROADMAP.md items). This design makes them declarable and discoverable; follow-up designs make them run.

### Conventions binding every part

- Both modules vendor their dependencies. Adding oras-go or go-containerregistry means updating that module's go.mod, go.sum and vendor/, and THIRD_PARTY_NOTICES.md, the way the repository's existing dependency bumps do.
- `make vet lint unit-tests` in operator/ and `make test lint` in agent/ are the gates. A behaviour change lands with its docs in the same change. CHANGELOG.md files are generated; release notes go in the RELEASE_NOTES.md files.
- Do not commit controller-gen's version-annotation rewrites under operator/config/crd/bases unless the CRD itself changed.

## Why it is split this way

The split follows the module boundary the requirements force. The shared library is a new third Go module at artifact/ (github.com/NVIDIA/nodewright/artifact) that operator/ and agent/ consume through require plus replace => ../artifact plus go mod vendor. Three reasons: both consumers vendor, and their container builds use the module directory as the Docker context, which vendor mode supports without the replacement directory. A separate module also keeps the library’s dependency graph to oras-go and the OCI spec packages, so the agent never inherits the operator’s Kubernetes graph and the operator never depends on the agent’s module or release stream. And it follows the repository’s one-module-per-component pattern (operator, agent, tools). The two library parts share artifact/go.mod, so they run in sequence: artifact-spec covers the pure format (types, schema, classification, the published v1 spec, module tooling and CI), then artifact-client adds oras-go resolution, credentials, payload-free inspection, atomic streaming layout and in-process registry test helpers. The two consumers then run in parallel on disjoint files, against a contract fixed here (NODEWRIGHT_PACKAGE_REF, NODEWRIGHT_REGISTRY_AUTH_FILE, and agent --version as the exit-0 container): agent-streaming-install and operator-artifact-pods. Uninstall precedence is its own part after operator-artifact-pods because it consumes that part’s inspection accessor. artifact-docs-migration comes last because it documents and executes the final commands and names, and it owns the cross-cutting files: the root THIRD_PARTY_NOTICES rollup that both consumers would otherwise race on, docs/index.yml, README, ROADMAP and the renovate grouping. Premises checked against origin/main 6b58530c: the Go agent cutover (787c695f, #685), uninstall.enabled/apply (71ca22b8, #200) and the bare image-reference rule (44dba453, #283) are present. The Jobs migration (#223) is not in flight: it merged (3c13b4a5, #459; issue closed 2026-08-15), so the operator parts change main’s Job builder directly. No OCI package code exists yet. Earlier review fixes stay: with PACKAGE_ARTIFACTS on, every interrupt Job runs the agent image with --version as its exit-0 container, chosen without reading any inspection outcome, and staleness judges an existing Job by its own kind, so an unknown outcome or a re-pushed tag never deletes a running Job. This revision resolves the uninstall finding where it arises. Starting a cycle is the commitment point: RunSkyhookPackages calls HandleUninstallRequests before any stage Job is built, and from then on a StageUninstall entry is re-added whatever the spec says. So the capability is decided at the start, and the spec is never used as a guess. The accessor inspects on a cache miss, so a restart with a reachable registry decides from the artifact. With the option on, an unknown or rejected outcome defers the start with an event and a requeue. Execution has two backstops: operator-artifact-pods builds no uninstall-stage Job for an artifact declaring unsupported (and cordons or drains no node for it), and the agent refuses one. A cycle that a later re-push leaves at StageUninstall without a Job stalls visibly until the user cancels it through the existing spec path or pushes a supported artifact; StageUninstallInterrupt always completes. operator-uninstall-capability grows to cover those transitions and stays under the cap.

## Seams

- artifact-spec -> artifact-client: the artifact/ module itself (module path, go 1.27.1, Apache-2.0 LICENSE, Makefile/lint tooling, artifact-ci.yaml) and the pure spec API. That API is the exported media-type and annotation constants; manifest classification into package artifact, legacy image (a result, not an error), unsupported (other version, multi-platform index) or invalid; and metadata validation returning a typed description (uninstall capability supported|unsupported, raw config JSON, ordered payload layer descriptors, host-file layer descriptors with destinations, optional sense/run interrupt declaration). It also carries the documented size bounds and tar entry rules that the client enforces.
- artifact-client -> agent-streaming-install and operator-artifact-pods: reference building from Package.image plus version or containerSHA (digest wins; Docker Hub short names normalised as the kubelet does), and parsing of a digest-pinned reference back into normalised repository and digest; ordered docker-config credential sources (JSON content or file paths, first source with an entry for the host wins, anonymous fallback, no credential helpers); HTTPS targets with an injectable http.Client, and OCI-layout targets. Also Inspect, which never fetches payload blobs and returns the classification, the resolved manifest descriptor and a digest-pinned registry/repository@sha256 reference; atomic, digest-verified Layout into a directory, with the payload config.json checked JSON-equal to the metadata; error classes matchable with errors.Is/As (not found, unauthorized, transport, unsupported, invalid); and artifact/registrytest (an in-process TLS registry with credentials and a request log, plus artifact, legacy-image and index builders).
- operator-artifact-pods &lt;-> agent-streaming-install (built in parallel): the operator sets NODEWRIGHT_PACKAGE_REF=&lt;registry/repository@sha256 of the inspected digest> only for package artifacts, on the stage and check agent containers and never on interrupt Jobs. It sets NODEWRIGHT_REGISTRY_AUTH_FILE=&lt;path to the IMAGE_PULL_SECRET .dockerconfigjson mounted read-only> only when a pull secret is configured. Artifact stage Jobs drop the package-image init container and keep every other mount, including /skyhook-package/configmaps/&lt;key> and /skyhook-package/node-metadata. The agent image with args [--version] and no command is the exit-0 done container of every artifact stage Job and, with PACKAGE_ARTIFACTS on, of every interrupt Job whatever the package kind; it must exit 0 without touching the host or the registry, even with NODEWRIGHT_PACKAGE_REF set. In return, the agent lays the payload out into the copy directory, overlays the mounted configmaps/ and node-metadata/, and refuses legacy references and artifacts that declare host files or interrupts, which also guards against version skew. Neither side runs an uninstall of an artifact whose metadata declares it unsupported: the operator builds no such Job, and the agent refuses an uninstall invocation (or an uninstall-check that has to lay out) for one, exiting 1 before layout.
- operator-artifact-pods -> operator-uninstall-capability: the PACKAGE_ARTIFACTS option, and named constants for the inspection request timeout, the tag-resolution TTL and the failure backoff window. An operator-internal accessor returns a package’s inspection outcome through the shared cache, as one of: disabled (option off, no request), legacy, artifact (accepted, with the parsed description including the uninstall capability, and the pinned digest), rejected, or unknown (failed or timed out, with the error class). On a cache miss it inspects synchronously within the request timeout; a cached result, or a cached failure inside its backoff window, is returned with no request; a failed tag refresh keeps the last good result. Also: the staleness rule that judges an existing Job by its own kind; the uninstall-stage backstop (no Job, and no cordon or drain, for an accepted artifact declaring uninstall unsupported, which is not a reconcile error); and the condition and event pattern (PackageArtifactRejected beside the SkyhookCondition\* constants in operator/internal/wrapper).
- agent-streaming-install and operator-uninstall-capability -> artifact-docs-migration: the final names and rules that the how-to documents and its test executes. These are the media types in the spec doc, the two NODEWRIGHT\_ variables, PACKAGE_ARTIFACTS and the packageArtifacts chart value, the credential order, the Go-agent prerequisite of packageArtifacts (agent --version as the exit-0 container), the rule that an existing Job keeps the reference it was built with when a tag is re-pushed, the PackageArtifactRejected and UninstallUnsupported conditions, the UninstallDeferred event, the agent’s refusal to uninstall an artifact declaring it unsupported, and the precedence table in uninstall.md (decisions spec, unsupported and deferred, and the stalled-cycle remedy). Also the agent/THIRD_PARTY_NOTICES.md and operator/THIRD_PARTY_NOTICES.md that the consumers regenerated, from which the docs part rebuilds the root rollup.
- artifact-spec -> artifact-docs-migration: docs/user-guide/package-artifact-spec.md and its docs/index.yml entry (the docs part adds sibling pages and cross-links), and .github/workflows/artifact-ci.yaml (the docs part extends it with oras and crane setup).

## Parts

### artifact-spec (#820) — Create the shared artifact/ Go module and publish the v1 package artifact specification

Why: NodeWright packages are container images today. The operator’s stage pod (operator/internal/controller/job_builder.go, createPodFromPackage) runs the package image as an init container that executes sh -c `cp -r /skyhook-package/* /root/$SKYHOOK_DIR`. That copies config.json, the step scripts (skyhook_dir/), an optional root_dir overlay and the configmaps/ defaults onto the host, and then the Go agent (agent/, module github.com/NVIDIA/nodewright/agent) runs the stage. Every package must therefore be a runnable image with a shell, and the operator learns nothing about a package without running it. Epic NVIDIA/nodewright#194 turns packages into OCI artifacts read by one shared Go library that the operator, the agent and later the CLI all use. This part creates that library’s module and the versioned package artifact specification. Other parts build on it: artifact-client adds registry access and layout (oras-go) to the same module, agent-streaming-install and operator-artifact-pods consume it, and artifact-docs-migration writes the author how-to.

Module (decided; do not revisit). Create artifact/ as a third Go module: module path github.com/NVIDIA/nodewright/artifact, go directive 1.27.1 as in operator/go.mod and agent/go.mod, and an Apache-2.0 LICENSE copied from the repository root. go mod vendor copies module-root LICENSE files, which lets the consumers’ go-licenses check and scripts/generate-notices.py cover the module without script changes. The module is not vendored itself (like tools/); operator/ and agent/ will later require it with replace => ../artifact and vendor it. It must import no k8s.io, sigs.k8s.io, operator/ or agent/ package, so the CLI can adopt it unchanged. Allowed dependencies: github.com/opencontainers/image-spec and github.com/opencontainers/go-digest for manifest and descriptor types, and github.com/santhosh-tekuri/jsonschema/v6 (already used by agent/internal/config) if you validate against the embedded schema. This part adds no registry, network or tar code.

Specification v1. Define it in exported Go constants and types, in a JSON Schema for the metadata document embedded in the module, and in the doc.

(1) Media types: one vendor family with an explicit v1 segment. Proposed names: artifactType application/vnd.nvidia.nodewright.package.v1; metadata config blob application/vnd.nvidia.nodewright.package.config.v1+json; payload layer application/vnd.nvidia.nodewright.package.payload.v1.tar+gzip; host-file layer application/vnd.nvidia.nodewright.package.hostfiles.v1.tar+gzip; layer annotation com.nvidia.nodewright.package.destination. You may refine the names; later code uses your constants, never literals.

(2) Manifest: an OCI image manifest whose config descriptor carries the metadata media type. That descriptor is required because the metadata lives there (the Helm-chart-on-OCI pattern). artifactType is optional, but when present it must be the package artifact type. Do not require a shape the oras CLI cannot produce: oras push sets the config with --config &lt;file>:&lt;mediaType> and artifactType with --artifact-type. List the optional, informational org.opencontainers.image.\* manifest annotations you recognise (title, version, source, description) and state that none of them changes behaviour.

(3) Metadata document (JSON):

- Required uninstall capability, with exactly the values supported and unsupported (no default, no third value).
- Required config: the package’s config.json content as a JSON object. It must be JSON-equal to the config.json at the payload root. artifact-client’s layout enforces the equality; you specify it.
- Optional dynamic interrupt declaration with a sense step and a run step. The sense step decides whether this package needs to interrupt the node this time; define its exit-code meaning. The run step executes the interrupt. Each step names an executable by a clean relative path inside the payload, plus arguments and an on-host flag, in the vocabulary of config.json steps (agent/internal/config/schemas/v1/step-schema.json).
- State in prose that the NodeWright spec’s Package.interrupt is planned to become an override of this declaration (ROADMAP: bring-your-own interrupts), and that operators and agents implementing only v1 refuse a package that declares one.
- Unknown fields are rejected, so typos fail loudly.

(4) Layers:

- One or more payload layers, each a gzip-compressed tar with entries relative to the package root. ./-prefixed names, as produced by tar -C dir -czf - ., are accepted. Layers apply in manifest order, with later entries overwriting earlier ones, and together they form exactly the tree a legacy image carried under /skyhook-package.
- Allowed entry types: regular files, directories, and symlinks (created, never followed while extracting). Rejected: absolute names, .. segments, hardlinks, and device, fifo and socket entries.
- Zero or more host-file layers with their own media type. Each must carry the destination annotation: an absolute, clean host path other than / with no .. segments. The same tar rules apply. The doc marks host-file layers as declared and validated but not yet placed by the agent.
- Any other layer media type makes the artifact invalid.

(5) Bounds: a maximum manifest size and a maximum metadata size that consumers will read. Pick them, document them, and export them as constants.

(6) Classification of a manifest given its bytes and media type (OCI manifest, Docker schema 2 manifest, OCI index, Docker manifest list):

- Package artifact: the config media type or artifactType is in the package family.
- Legacy image: otherwise. This is a result, not an error.
- Unsupported form: an index or manifest list whose artifactType is in the package family. Multi-platform package artifacts are not defined in v1, so refuse them and never route them to legacy.
- Unsupported-version error: a family media type with a version other than v1.
- Invalid-artifact error: a family manifest with missing, oversized, malformed or schema-invalid metadata, or with an invalid layer.

API (pure: no network or filesystem):

- Classify a manifest, and parse and validate a metadata blob together with its manifest.
- Return a typed description exposing the uninstall capability, the raw config JSON, the ordered payload layer descriptors, the host-file layer descriptors with their destinations, and the optional interrupt declaration.
- Use image-spec types for the descriptors, so a later provenance verifier can use them.
- Make errors distinguishable with errors.Is/As: unsupported form, unsupported version, and invalid artifact with a reason.

Doc: write docs/user-guide/package-artifact-spec.md, titled as version 1 of the NodeWright package artifact specification, for package authors and tool writers. Cover:

- media types and the manifest shape
- the metadata fields, with a link to the schema file under artifact/
- the layer and tar rules
- the host-file and interrupt declarations (declared and validated, not yet executed)
- classification and bounds
- how a future v2 would be introduced

Add the page under User Guide in docs/index.yml. Keep one minimal valid example and the invalid examples the tests use. The build, push and conversion how-to belongs to artifact-docs-migration.

Tooling and CI:

- artifact/Makefile with test, lint (golangci-lint plus the license-header check) and fmt targets, mirroring agent/Makefile. Add artifact/deps.mk, artifact/.golangci.yml and artifact/.gitignore, mirroring agent’s.
- Root Makefile: include artifact in test, fmt, license-fmt and license-header-check, the way agent and tools are included.
- New .github/workflows/artifact-ci.yaml, modeled on tools-ci.yaml. It triggers on pull_request and on push to main, filtered on artifact/\*\* and the workflow file. It has one job that runs make test and make lint in artifact/ (setup-go from artifact/go.mod, cache-dependency-path artifact/go.sum), plus the ci-gate job every workflow publishes.
- .github/workflows/codeql.yaml builds the new module, without -mod=vendor.

Tests:

- One table case for every classification and validation rule. Assert each error by sentinel or type, never by message text.
- The embedded JSON Schema and the Go validation agree on the same fixtures.
- A test reads docs/user-guide/package-artifact-spec.md, extracts every fenced JSON example the doc marks valid or invalid, and asserts the library accepts and rejects them accordingly. That way the examples authors copy are the ones the validator was tested against.

Verify: cd artifact && make test lint; make license-header-check at the repository root; and actionlint on the new workflow (lint-ci runs it). Behaviour changes land with their docs. Do not add a CHANGELOG.md, because those are generated.

- scope: artifact/, docs/user-guide/package-artifact-spec.md, docs/index.yml, Makefile, .github/workflows/artifact-ci.yaml, .github/workflows/codeql.yaml
- acceptance: artifact/ is a Go module, github.com/NVIDIA/nodewright/artifact: go 1.27.1, an Apache-2.0 LICENSE, no k8s, operator or agent imports, and not itself vendored. Its exported constants, types, embedded JSON Schema and pure classify/validate API implement spec v1 as briefed:
- uninstall supported|unsupported, required
- config JSON, required
- payload layers, and host-file layers with validated destinations
- an optional sense/run interrupt declaration
- legacy classification, and refusal of unknown versions and family indexes

The errors are typed. docs/user-guide/package-artifact-spec.md is published and listed in docs/index.yml, and a test validates its fenced examples. cd artifact && make test lint passes. artifact-ci.yaml (with ci-gate) runs on artifact/\*\* changes. codeql.yaml and the root Makefile cover the module.

### artifact-client (#821) — Add oras-go registry access, payload-free inspection and atomic streaming layout to the shared library

Why: epic NVIDIA/nodewright#194 moves NodeWright packages from runnable container images to OCI artifacts, which the operator inspects and the Go agent streams. Today a package-image init container copies the package onto the host with sh -c `cp -r /skyhook-package/* ...`. The shared module artifact/ (github.com/NVIDIA/nodewright/artifact) already holds the v1 package artifact specification:

- media-type constants and the metadata document
- pure classification of a manifest as package artifact, legacy image, unsupported or invalid
- the typed description: uninstall capability, config JSON, payload and host-file layers, and the interrupt declaration

The normative doc is docs/user-guide/package-artifact-spec.md. The module is not itself vendored and imports no Kubernetes packages. This part adds registry access and layout to that module. Other parts (agent-streaming-install, operator-artifact-pods) will vendor it into agent/ and operator/. Keep the API free of Kubernetes types so the CLI can adopt it unchanged later.

Dependency (decided): oras.land/oras-go/v2, using a release at least two weeks old, recorded in artifact/go.mod and go.sum. go-containerregistry was rejected because of its heavier dependency graph and image-centric API.

Behaviour:

(1) Reference.

- Build the reference from Package.image (a bare registry/repository; reject an inline tag or digest, as the webhook does), the version tag, and an optional containerSHA digest.
- The digest wins over the tag, matching getPackageImage in operator/internal/controller/skyhook_controller.go.
- Normalise Docker Hub short names the way the kubelet does: docker.io as the default registry, library/ for single-segment names, and registry-1.docker.io as the API host. The library and the kubelet must resolve the same repository.
- Parse a digest-pinned reference string, in the registry/repository@sha256:&lt;hex> form Inspect returns, back into its normalised repository and digest, and reject any other form (a tag, a missing or malformed digest). operator-artifact-pods uses this to compare an existing Job’s pinned reference with the package’s repository and containerSHA without a registry request.

(2) Credentials.

- Credential sources come from docker config JSON content (the .dockerconfigjson of a kubernetes.io/dockerconfigjson Secret) or from docker config file paths, consulted in the order given.
- The first source with an entry for the registry host wins.
- A source without an entry, a missing file or a malformed file is skipped, with a reason the caller can log. Never log the secret itself.
- With no entry anywhere, access is anonymous.
- Never execute credential helpers (credsStore, credHelpers).
- Support basic and token auth as the oras-go auth client handles them.

(3) Targets: a remote repository over HTTPS with an injectable *http.Client, and an OCI image layout directory (used by the documented-commands test and the future CLI). In production the client uses the default transport, which honours HTTP(S)_PROXY. There is no plain-HTTP or insecure-TLS option.

(4) Inspect.

- Resolve the reference (tag to digest), fetch the manifest within the spec’s size bound, and classify it with the spec API. For a package artifact, fetch and validate the metadata blob within its bound.
- Never fetch payload or host-file layers.
- The result exposes the classification, the resolved manifest descriptor (media type, digest, size), a digest-pinned reference string registry/repository@sha256:&lt;hex> that another process can use, and the parsed description.
- Failures are distinguishable with errors.Is/As: not found, unauthorized or forbidden, transport (network, TLS, timeout), unsupported, and invalid artifact.
- Contexts and deadlines are honoured.

(5) Layout. Given an inspected package artifact and a destination directory:

- Stream each payload layer in manifest order, verifying size and digest while streaming, with no whole-layer buffering.
- Gunzip and untar under the spec’s entry rules into a staging directory beside the destination, on the same filesystem.
- Check that the resulting root config.json is JSON-equal to the metadata config.
- Install the staging directory at the destination atomically, replacing any existing directory.

Required properties:

- On any failure (digest mismatch, rejected entry, config mismatch, cancellation), the destination is left exactly as it was and the staging directory is removed.
- Two concurrent layouts into one destination never produce a mixed tree.
- Symlinks are created but never followed during extraction. An entry whose path passes through an extracted symlink is rejected.
- Permission bits, including executable bits, are preserved; ownership is not.
- Layout from an OCI layout target behaves identically.

Export the single-layer extraction (any layer descriptor into any directory) separately, so a follow-up can reuse it for host-file layers. The package layout itself does not place host-file layers.

(6) Test helpers. Add an exported package (for example artifact/registrytest) used only by tests, both here and in agent/ and operator/. It provides:

- An in-process registry on an httptest TLS server. It serves the distribution API subset the client uses (ping, manifest GET and HEAD by tag and digest, blob GET and HEAD) from an in-memory store, with optional credential enforcement and a request log so tests can assert which blobs were fetched.
- Builders that push a package artifact (from a directory plus metadata: uninstall capability, optional interrupt declaration, optional host-file layers), a legacy single-platform image, and a multi-platform index. Pushing again under an existing tag re-points the tag, so consumers can test re-pushes.

Keep it small.

Tests (cluster-free, against the helpers and OCI layout directories):

- Reference construction, including digest precedence and Docker Hub normalisation.
- Parsing a pinned reference round-trips the string Inspect returns, and rejects a tag, a missing digest and a malformed digest.
- Credential ordering, skipping and anonymous fallback against a registry that requires auth: the right credentials succeed, wrong ones give unauthorized, and a source lacking the host falls through.
- Inspect fetches no payload blob, asserted on the request log.
- Classification of a legacy image and of an index, served through the registry.
- Layout reproduces the source tree: paths, contents, executable bits and symlinks.
- Every failure path leaves a pre-existing destination byte-identical. Seed the destination with content that differs from the payload, so a broken rollback fails the test.
- Tar cases: ./-prefixed names, pax headers, the . directory entry, absolute names, .., symlink escape, hardlink, and device node.
- A registry serving tampered bytes gives a digest error.
- A config.json mismatch fails.
- Two goroutines laying out into one destination.
- OCI layout parity.

Doc: change docs/user-guide/package-artifact-spec.md only where the implementation pins something the spec left open. Make no other doc changes here.

Verify: cd artifact && make test lint. Change nothing outside artifact/ and that doc.

- scope: artifact/, docs/user-guide/package-artifact-spec.md
- acceptance: artifact/ depends on oras-go v2 and exposes:
- reference building (digest precedence, Docker Hub normalisation) and parsing of a digest-pinned reference into repository and digest
- ordered docker-config credential sources with anonymous fallback and no helper execution
- HTTPS and OCI-layout targets
- an Inspect that never fetches payload blobs (proven by request-log tests) and returns the classification, the resolved descriptor and a digest-pinned reference, with typed errors
- an atomic, digest-verified, streaming Layout with secure tar rules and a config.json equality check, whose failures leave the destination unchanged
- a separately exported single-layer extraction
- the artifact/registrytest helpers, including tag re-pushes

All briefed tests pass under cd artifact && make test lint.

### agent-streaming-install (#822) — Agent pulls package artifacts itself and lays them out with no package-image init copy

Why: epic NVIDIA/nodewright#194 moves NodeWright packages from runnable container images to OCI artifacts.

How a stage pod works today:

- The operator runs the package image as an init container that executes sh -c `mkdir -p /root/${SKYHOOK_DIR} && cp -r /skyhook-package/* /root/${SKYHOOK_DIR}`.
- That copies the image’s package tree onto the host copy directory, together with the ConfigMap files the operator mounts at /skyhook-package/configmaps/&lt;key> (subPath mounts that override image defaults) and at /skyhook-package/node-metadata.
- The Go agent (agent/, invoked as agent MODE ROOT_MOUNT COPY_DIR) resolves copyRoot through the host root mount and finds it populated, so ensurePackageData (agent/internal/agent/package.go) returns early.
- The agent then runs loadConfig, prepareHost and the stage’s steps.

An artifact package has no package-image init container. The agent pulls the artifact itself and lays it out where ensurePackageData expects it. The shared module artifact/ (github.com/NVIDIA/nodewright/artifact) provides:

- reference building and docker-config credentials
- payload-free inspect with classification
- an atomic streaming layout with a config.json equality check
- typed errors
- test helpers in artifact/registrytest: an in-process TLS registry with credentials and a request log, plus artifact, legacy-image and index builders

The format is defined in docs/user-guide/package-artifact-spec.md.

Contract with the operator. It is fixed: operator-artifact-pods implements the other side in parallel, so do not change it.

- NODEWRIGHT_PACKAGE_REF is set only for package artifacts, on the stage and check agent containers of a stage Job, and never on interrupt Jobs. Its value is registry/repository@sha256:&lt;hex>, the digest the operator inspected.
- NODEWRIGHT_REGISTRY_AUTH_FILE is set only when the operator has an image pull secret. It is the path, inside the container, to a read-only docker config JSON file (the Secret’s .dockerconfigjson).
- Artifact pods keep every mount the agent containers have today (the host root at ROOT_MOUNT, /skyhook-package/node-metadata and /skyhook-package/configmaps/&lt;key>) and keep the agent arguments. Only the package-image init container is gone.
- agent --version (agent/internal/agent/agent.go, present since the Go cutover) keeps printing the version and exiting 0 without touching the host or the registry, even when NODEWRIGHT_PACKAGE_REF is set. The operator runs it, with args [--version] and no command, as the exit-0 done container of every artifact stage Job and, when its PACKAGE_ARTIFACTS option is on, of every interrupt Job whatever the package kind, because the distroless agent image has no shell.
- The operator never builds an uninstall-stage Job for an artifact whose metadata declares uninstall unsupported. The agent refuses one anyway (below), because it is where the uninstall steps run and it reads the exact pinned digest.
- New variables use the NODEWRIGHT\_ prefix. Do not add, rename or change any SKYHOOK_\* variable, on-host path or completion marker; #437, #438 and #442 decide those.

Behaviour:

(1) With NODEWRIGHT_PACKAGE_REF unset, the agent behaves exactly as today, and the existing tests pass unmodified.

(2) With it set:

- Credentials come from, in order: the NODEWRIGHT_REGISTRY_AUTH_FILE file; then the docker config files the kubelet reads for node credentials, resolved through the root mount (/var/lib/kubelet/config.json, /root/.docker/config.json, /.docker/config.json); then anonymous access. Log which source was used and why the others were skipped, never secret values.
- Inspect the reference.
- A legacy image fails with an error saying the reference is a container image rather than a package artifact.
- Unsupported or invalid artifacts fail with the library’s reason.
- An artifact that declares host-file layers or a dynamic interrupt fails with an error naming the feature as unsupported by this agent version. Never silently ignore it: steps may depend on those files, and a declared interrupt would be skipped.
- An uninstall invocation, or an uninstall-check invocation that has to lay out, for an artifact whose metadata declares uninstall unsupported fails before layout, with an error naming that declaration. Other modes of the same artifact are unaffected.
- All of these exit with ExitFailure (1), like any execution failure.

(3) Layout rule. A stage invocation (apply, config, uninstall, upgrade, post-interrupt) lays the payload out fresh into copyRoot with the library’s atomic layout, mirroring the per-pod init copy. A -check or interrupt invocation reuses an existing copyRoot without contacting the registry, and lays out only when copyRoot is absent.

(4) After layout, overlay the SKYHOOK_DATA_DIR tree onto copyRoot. It defaults to /skyhook-package, which in an artifact pod holds only the mounted configmaps/ and node-metadata/. Mounted files win over payload files of the same path, which reproduces what the legacy cp produced. Do not copy the legacy node-files directory on this path: operator-driven legacy runs never reach that copy, because copyRoot already exists.

(5) Everything after layout is unchanged: config loading, prepareHost, steps, interrupts, flags, history and logs. The dashed startup banner is a compatibility contract consumed by operator diagnostics and tests, so do not add or reorder its fields; log the reference and digest through slog instead.

Vendoring:

- agent/go.mod requires github.com/NVIDIA/nodewright/artifact with replace => ../artifact. Run go mod vendor.
- Regenerate agent/THIRD_PARTY_NOTICES.md with make notices-agent from the repository root. Leave the root THIRD_PARTY_NOTICES.md to artifact-docs-migration.
- make license-check in agent/ must pass with the new dependencies.
- containers/agent.Dockerfile builds with agent/ as the build context and -mod=vendor. Prove the build needs nothing outside agent/: copy agent/ alone to a temporary directory and run CGO_ENABLED=0 go build -mod=vendor ./cmd/agent there.

CI: in .github/workflows/agent-ci.yaml, add artifact/\*\* to the pull_request and push path filters. Add a step that fails when the agent/vendor copy of the artifact module is stale: run go mod vendor, then git diff --exit-code on agent/vendor, go.mod and go.sum. Keep this step out of make lint, which must stay runnable without module downloads.

Tests (cluster-free, with artifact/registrytest):

- End to end; this is the acceptance test. Push as an artifact a representative package: config.json with apply and apply-check steps, a config step that reads a configmaps/ file, uninstall and upgrade steps, a root_dir overlay file, and executable scripts. Run the agent through New().Run, like the existing agent_test.go cases, with ROOT a temp dir, no pre-populated copyRoot, SKYHOOK_DATA_DIR a temp dir holding configmaps/ and node-metadata/, and only NODEWRIGHT_PACKAGE_REF set. Assert exit 0, the side effects of the steps, and completion flags where the legacy path writes them.
- Parity: apply, apply-check, config, config-check, uninstall, uninstall-check, upgrade, upgrade-check, post-interrupt, post-interrupt-check and interrupt give the same result through the artifact path as through the legacy data-dir path for the same tree. The copyRoot built from an artifact equals the one the legacy copy builds from the same tree plus mounts (paths, contents, executable bits, symlinks), including a configmaps key that overrides a payload default.
- Credentials, against an auth-requiring registry: the auth file works; without it, a node config at &lt;root>/var/lib/kubelet/config.json works; an auth file with no entry for the host falls through to the node config; with no credentials, the run fails with unauthorized and exits 1.
- Refusals: a legacy image, invalid metadata, a declared host-file layer and a declared interrupt each exit 1, with the reason on stderr and no copyRoot left behind. For an artifact declaring uninstall unsupported, uninstall, and uninstall-check without a copyRoot, each exit 1 the same way, while apply of the same artifact exits 0, so refusing every mode fails the test.
- Layout rule: a check invocation over an existing copyRoot makes no registry request (the request log stays empty). A stage invocation replaces a stale copyRoot: seed it with a file absent from the payload and assert the file is gone.
- --version: with NODEWRIGHT_PACKAGE_REF set to a registrytest reference, agent --version exits 0, prints the version, makes no registry request (the request log stays empty) and creates nothing under ROOT.
- Seed every fallback test so the result of the fallback differs from that of the normal path.

Docs:

- agent/README.md: the opening paragraph that says the agent reads /skyhook-package/config.json, the two variables, the credential order, the layout rule, the refusals (including uninstall of an artifact that declares it unsupported), and the --version guarantee, including its use as the exit-0 container.
- agent/RELEASE_NOTES.md: an Unreleased entry saying artifact packages require this agent version or newer, and that an older agent given an artifact pod fails reading config.json. This is an upgrade action, which .claude/CLAUDE.md admits to release notes.

No test asserts README text.

Verify: cd agent && make test lint; make license-check in agent/; and the out-of-tree vendor build above.

- scope: agent/, .github/workflows/agent-ci.yaml
- acceptance: With NODEWRIGHT_PACKAGE_REF unset the agent behaves exactly as before, and the existing tests pass unmodified. With it set:
- A test against artifact/registrytest applies a representative package end to end, with no pre-populated copy directory and no init copy.
- Parity tests cover apply, config, uninstall, upgrade, post-interrupt, their checks and interrupt, plus tree equality with the legacy copy, including configmap overrides.
- Credential order, refusals (legacy image, invalid, host files, interrupt, and uninstall of an artifact declaring it unsupported) and the layout rule are tested.
- agent --version is tested to exit 0 with no registry request and no host write while NODEWRIGHT_PACKAGE_REF is set.

The agent vendors the artifact module: agent/THIRD_PARTY_NOTICES.md is regenerated, license-check passes, and the out-of-tree vendor build succeeds. agent-ci.yaml triggers on artifact/\*\* and checks vendor staleness. The agent README and RELEASE_NOTES are updated. cd agent && make test lint passes.

### operator-artifact-pods (#823) — Operator inspects package references and builds agent-only Jobs for package artifacts behind an opt-in option

Why: epic NVIDIA/nodewright#194 moves NodeWright packages to OCI artifacts.

How package Jobs are built and judged today (read this code and its comments before changing it):

- In operator/internal/controller/job_builder.go, createJobFromPackage wraps createPodFromPackage and then jobFromPod. createInterruptJobFromPackage wraps createInterruptPodForPackage and the same jobFromPod.
- Every stage Job runs the package image (getPackageImage in skyhook_controller.go: image:version or image@containerSHA) as a privileged init container. It executes sh -c `mkdir -p /root/${SKYHOOK_DIR} && cp -r /skyhook-package/* /root/${SKYHOOK_DIR}`. Next, the agent image (getAgentImage, honouring agentImageOverride) runs as the stage and check init containers.
- An interrupt Job has one init container, the agent image running the interrupt. Its pod pulls the package image only for its done container.
- jobFromPod turns the pause container of both kinds of Job into an exit-0 done container that runs the package image with /bin/sh -c `exit 0`. Its comment gives the reason: the init copy has already pulled that image and proved it has a shell, while a minimal agent image may not. job_builder_test.go notes that the transform is shared.
- Stage Jobs are created at one site in skyhook_controller.go (createJobFromPackage plus setJobPackage). Interrupt Jobs, for package, config-update and uninstall interrupts, are created at one other (createInterruptJobFromPackage).
- Staleness: jobMatchesPackage and podMatchesPackage rebuild the expected pod and compare the package label, the init-container count, names and images, the env (minus the getAgentConfigEnvVars names) and the resources. They do not compare the done container, volumes or imagePullSecrets. A mismatch deletes or invalidates the Job at three sites: handleExistingJob (AlreadyExists on create); jobIsStale, through jobSpecMatchesPackage, in the ValidateRunningPackages sweep, followed by InvalidPackage; and shouldDeleteFinishedJob, whose timed-out carve-out holds only while the spec matches.

The Jobs migration (#223) is merged on main (3c13b4a5, #459). Change main’s builder directly, keeping the change small and localized.

The shared module artifact/ (github.com/NVIDIA/nodewright/artifact) provides:

- reference building and docker-config credentials, and parsing of a digest-pinned reference back into repository and digest
- payload-free inspect, returning a classification, the resolved descriptor and a digest-pinned reference
- typed errors
- artifact/registrytest: an in-process TLS registry with credentials and a request log, plus artifact, legacy-image and index builders, and tag re-pushes

The format is defined in docs/user-guide/package-artifact-spec.md.

Decisions (fixed):

(1) Opt-in. Add a PACKAGE_ARTIFACTS bool to SkyhookOperatorOptions (env, default false) and a chart value controllerManager.manager.env.packageArtifacts (default `false`). Wire it through chart/templates/deployment.yaml and operator/config/manager/manager.yaml, and document it in chart/values.yaml and chart/README.md. When the option is off, the operator makes no registry request and every Job is identical to today’s. Reason: inspection adds an outbound registry dependency to the operator, and clusters that do not use artifacts must not pay for it.

(2) When the option is on, inspect each package’s reference (image plus version, or containerSHA) when building its stage Job.

- Credentials come from the .dockerconfigjson of the IMAGE_PULL_SECRET Secret, read through the existing namespace-scoped Secret cache of the manager. operator/cmd/manager/main.go already scopes Secrets to the operator namespace, and the namespaced Role already grants get.
- No RBAC verb changes, but correct the Role comments in chart/templates/manager-rbac.yaml that say Secrets are only the webhook cert.
- A Secret of another type, or one without that key, yields a Warning event and anonymous access.

(3) Stage Job outcomes:

- A valid package artifact that declares neither host-file layers nor a dynamic interrupt gets an artifact stage Job.
- A legacy image gets today’s stage Job, unchanged.
- An inspection failure (not found, unauthorized, transport, timeout) gets today’s stage Job unchanged, plus a Warning event on the NodeWright naming the package, the reference and the error class. The kubelet may still pull a legacy image with node credentials, and an artifact will fail visibly at pull.
- An unsupported or invalid artifact, or one that declares host-file layers or a dynamic interrupt, gets no Job. It gets a Warning event and a status condition naming the package and the reason; the proposed type is PackageArtifactRejected, next to the SkyhookCondition\* constants in operator/internal/wrapper. The condition clears when no package is rejected, and reconcile requeues so a corrected push is picked up.
- Uninstall stage: an accepted artifact whose description declares uninstall unsupported gets no uninstall-stage Job, plus a Warning event naming the package, the digest and the declaration. This is the backstop under operator-uninstall-capability, which decides whether an uninstall starts at all. Every other outcome at the uninstall stage follows the rules above, including the inspection-failure fallback, whose package-image init container cannot run an artifact and so cannot run its uninstall steps.
- Getting no Job is not a reconcile error. An ApplyPackage error aborts the pass for the whole NodeWright, so the pass must instead continue with other packages and nodes. Decide it before ProcessInterrupt for a package whose current stage runs a stage Job, so no node is cordoned or drained for a package that gets none. Interrupt stages are unaffected, and an existing Job is judged only by rule (7).

(4) Cache:

- Each request has a bounded timeout (a named constant).
- Results for a digest are cached for the process lifetime.
- Tag-to-digest resolution is cached for a bounded TTL (a named constant), so Jobs built later notice a re-pushed tag. A failed refresh keeps the last good resolution.
- Failures are cached for a bounded backoff window, so an unreachable registry costs at most one timed-out request per reference per window.
- Staleness checks and the validation sweep go through the same cache. They consult it only for a legacy stage Job with the option on (rule 7), and never wait on the registry for a reference with a cached result or a cached failure inside its window.

(5) An artifact stage Job differs from a legacy stage Job only in these ways:

- There is no package-image init container.
- The stage and check agent containers gain NODEWRIGHT_PACKAGE_REF=registry/repository@sha256:&lt;the inspected digest>.
- When IMAGE_PULL_SECRET is set, those containers also gain NODEWRIGHT_REGISTRY_AUTH_FILE, pointing at the .dockerconfigjson of that Secret, mounted read-only into them.
- The done container runs the agent image with args [--version] and no command. The agent prints its version and exits 0 without touching the host; the distroless agent image has no shell.

Mounts, ConfigMap subPath mounts, package env, resources, tolerations, names, labels, annotations and imagePullSecrets are unchanged.

(6) Interrupt Jobs. With the option on, the done container of every interrupt Job is the agent image (getAgentImage: the image its interrupt container already ran) with args [--version] and no command. This holds for every package kind and every interrupt type, and the builder reads no inspection outcome for it, so neither a cold cache nor a registry failure can put the package image back. Nothing else about interrupt Jobs changes: no new env, volumes or containers. With the option off, interrupt Jobs are identical to today’s. Why not choose by kind: an interrupt Job built while the outcome is unknown (an operator restart, then a failed inspection) would fall back to the package image. An artifact cannot run as a container, so that pod would never reach Succeeded, and the interrupt stage, a reboot included, would sit until its deadline and fail. This creates a prerequisite that is documented, not detected: with the option on, the operator’s agent image and every agentImageOverride must be a Go agent (agent/v7.x, since 787c695f), because the Python agent/v6.x has no --version.

(7) Staleness. The kind of an existing stage Job is read from the Job itself, never from the current outcome: it is an artifact Job when its agent containers carry NODEWRIGHT_PACKAGE_REF.

- A legacy Job is compared exactly as today.
- An artifact Job matches when everything podMatchesPackage compares today matches the expected artifact Job, its NODEWRIGHT_PACKAGE_REF names the normalised registry/repository of the package, and, when the package sets containerSHA, its digest equals containerSHA. With a tag, the digest is not compared. NODEWRIGHT_REGISTRY_AUTH_FILE is not compared, as imagePullSecrets are not compared today. A version, image or containerSHA change is a mismatch for both kinds, as today. Judging an artifact Job needs no inspection outcome and no registry request.
- The one outcome-driven mismatch: a legacy Job for a package whose cached outcome is a package artifact, accepted or rejected. Its package-image init container cannot pull an artifact, and every retry pod pulls the same reference again, so it is replaced: invalidated when unfinished, deleted when terminally failed so the stage re-runs. This includes a legacy Job that was running when its version tag was re-pushed as an artifact.
- Everything else leaves an existing Job alone: the option turned off, an unknown outcome (a cold cache after a restart, an inspection failure, a wrong pull Secret), a new digest pushed under the same tag, and an artifact tag re-pushed as a container image. Such a Job finishes with the reference it was built with, and the next Job built for the package pins the current digest. That matches a legacy image:tag Job, whose image string does not change when its tag is re-pushed. Authors pin containerSHA when they need immutable content.
- Interrupt Jobs compare as today. The done container is not compared, so neither the option nor any outcome makes an interrupt Job stale.

handleExistingJob, jobIsStale and shouldDeleteFinishedJob all apply this rule through jobMatchesPackage. Keep one compare, not a variant per caller.

(8) For operator-uninstall-capability, provide an operator-internal accessor that returns the inspection outcome of a package through the same cache, as one of:

- disabled: the option is off, and no request is made
- legacy
- artifact: accepted, with its parsed description (including the uninstall capability) and the pinned digest
- rejected, with the reason
- unknown: the inspection failed or timed out, with the error class

On a cache miss it inspects synchronously, bounded by the request timeout, exactly as building a stage Job does. A cached result, or a cached failure inside its backoff window, is returned with no request. A failed tag refresh keeps the last good result. operator-uninstall-capability calls it from uninstall planning, which runs before any stage Job is built, and requeues by the TTL and backoff constants.

Contract with the agent. agent-streaming-install implements it in parallel, so do not change it:

- The two variables above, with exactly those meanings, set only on artifact stage and check containers, never on interrupt Jobs.
- The agent lays the payload out into the copy directory and overlays the mounted configmaps/ and node-metadata/.
- The agent refuses legacy references and artifacts that declare host files or interrupts.
- agent --version exits 0 without touching the host or the registry. It is the done container of artifact stage Jobs and, with the option on, of every interrupt Job.
- Neither side runs an uninstall of an artifact declaring it unsupported: the operator builds no such Job (rule 3), and the agent refuses one.

Artifact packages need the agent release that carries that change. The operator does not detect agent versions, so say so in the release notes.

Vendoring:

- operator/go.mod requires github.com/NVIDIA/nodewright/artifact with replace => ../artifact. Run go mod vendor.
- Run make notices-operator from the repository root. Leave the root rollup to artifact-docs-migration.
- make license-check in operator/ must pass.
- containers/operator.Dockerfile builds with operator/ as the context and -mod=vendor. Prove it needs nothing outside operator/ by building a copy of operator/ alone with go build -mod=vendor ./cmd/manager.

CI: in .github/workflows/operator-ci.yaml, add artifact/\*\* to the path filters. Add a vendor-staleness step: go mod vendor, then git diff --exit-code on operator/vendor, go.mod and go.sum.

Tests (cluster-free, using the vendored artifact/registrytest). The controller envtest suites share one live manager and apiserver across specs: drive the functions under test directly or give new specs nodes the manager does not select, clean up every object a spec creates (finalizers included, since envtest runs no garbage collector), and run every new or changed spec under several random ginkgo --seed values. A seed-dependent failure is a leak or race to fix at its source.

- Option off: the existing builder and controller tests pass unmodified, and the registry request log stays empty even with a reachable registry.
- Option on, stage Jobs: the stage Job of a legacy image deep-equals the option-off stage Job. An artifact stage Job differs only in the listed ways; assert the init containers, env, Secret volume and mounts, and the image, args and command of the done container. containerSHA pins that digest with no tag resolution. A tag is resolved and pinned.
- Option on, interrupt Jobs: for an artifact, a legacy image and an unknown outcome (a registry that fails every request), and for each interrupt type, the done container has the agent image (the agentImageOverride when set), args [--version] and no command, and no container in the pod references the package image or /bin/sh. Building them makes no registry request. With the option off, the interrupt Job still matches today’s, package image and /bin/sh included.
- An inspection failure yields the legacy stage Job plus a Warning event. Seed the test so the legacy and artifact Jobs differ, so that removing the fallback fails it.
- Invalid, unsupported, host-file and interrupt artifacts yield no Job, plus the condition and an event. The condition clears after the artifact is fixed.
- Uninstall stage: an accepted artifact declaring uninstall unsupported gets no uninstall-stage Job and a Warning event. The same artifact’s apply stage gets an artifact Job, and a supported artifact’s uninstall stage gets one, so refusing every stage, or none, fails the test.
- No-Job outcomes: for a package with an interrupt, neither this refusal nor a rejection cordons or drains the node, and neither returns a reconcile error. Another package on the same node still gets its Job in the same pass.
- Accessor: with the option off it returns disabled and makes no request. A cache miss inspects once. A cached failure inside its window returns unknown with no request. A failed tag refresh returns the last good result.
- Cache: a second build for a cached digest makes no request; the failure window bounds request counts; TTL expiry re-resolves; a failed refresh keeps the last good result.
- Staleness, through each of handleExistingJob, jobIsStale in the sweep, and the timed-out carve-out of shouldDeleteFinishedJob. Seed every case so the existing Job differs from the Job the current outcome would build, so that judging by the current outcome fails the test:
  - An unfinished artifact Job, and a terminally failed artifact Job whose entry is erroring, are kept: with a cold cache and a registry failing every request, with an unauthorized pull Secret, and with the option turned off.
  - A tag re-pushed to a new digest: the unfinished artifact Job pinned to the old digest is kept, and a Job built afterwards for another node pins the new digest.
  - An artifact tag re-pushed as a container image: the unfinished artifact Job is kept.
  - A containerSHA change and a version change make an artifact Job stale.
  - A legacy Job for a package whose cached outcome becomes a package artifact is invalidated when unfinished, and deleted when terminally failed with its entry erroring; the next Job built is an artifact Job.
  - An unchanged artifact Job is not recreated across repeated sweeps.
- Credentials: an auth-requiring registry succeeds with the pull Secret; without it, unauthorized leads to the fallback.

Docs:

- docs/architecture/lifecycle.md: the artifact stage pod beside the init-copy description; the done containers (artifact stage Jobs and, with the option on, every interrupt Job); credentials; the staleness rule (an unknown outcome or a re-pushed tag never recreates a Job, containerSHA pins content, and a legacy fallback Job for a known artifact is replaced); and that no node is cordoned or drained for a package that gets no Job (a rejected artifact, or an uninstall its artifact declares unsupported). Leave the diagram sources and PNGs alone; they describe the legacy path, which still exists.
- docs/architecture/operator-status.md: the new condition.
- chart/values.yaml comments and chart/README.md: the option and its Go-agent prerequisite.
- operator/RELEASE_NOTES.md and chart/RELEASE_NOTES.md, Unreleased: opt-in artifact packages, the minimum agent version, the Go-agent prerequisite when the option is on, and the new outbound registry access when the option is enabled. These are a new knob and upgrade actions, which .claude/CLAUDE.md admits to release notes.

No test asserts these doc texts.

The CLI is unaffected: no label, annotation, finalizer or status-field shape changes, and the condition is additive. Say so in the commit message, as .claude/CLAUDE.md asks of operator contract changes.

Verify: cd operator && make vet lint unit-tests; make license-check in operator/; the out-of-tree vendor build; and, if helm is installed, helm template rendering the chart with packageArtifacts true and false. make unit-tests regenerates manifests: do not commit version-annotation rewrites under operator/config/crd/bases, because no CRD changes here. Comment-only edits in Go files or chart templates are code changes and run the gates.

- scope: operator/go.mod, operator/go.sum, operator/vendor/, operator/Makefile, operator/internal/controller/, operator/internal/wrapper/, operator/cmd/manager/, operator/config/manager/manager.yaml, operator/THIRD_PARTY_NOTICES.md, operator/RELEASE_NOTES.md, chart/values.yaml, chart/templates/deployment.yaml, chart/templates/manager-rbac.yaml, chart/README.md, chart/RELEASE_NOTES.md, docs/architecture/lifecycle.md, docs/architecture/operator-status.md, .github/workflows/operator-ci.yaml
- acceptance: PACKAGE_ARTIFACTS (default off; chart value packageArtifacts) gates all inspection.
- With the option off, every Job is identical to today’s: the existing tests pass unmodified and no registry requests are made. With the option on, the stage Job of a legacy image is identical to today’s.
- Artifact stage Jobs drop the package-image init container, carry NODEWRIGHT_PACKAGE_REF (the inspected digest) and NODEWRIGHT_REGISTRY_AUTH_FILE with the mounted pull Secret, and use agent --version as the done container.
- With the option on, the done container of every interrupt Job is the agent image with --version for every outcome, unknown included; no interrupt Job references the package image or /bin/sh, and building one makes no registry request.
- An inspection failure falls back to the legacy stage Job with a Warning event.
- Rejected artifacts (invalid, unsupported, host files, interrupt) get no Job, plus PackageArtifactRejected and an event.
- An accepted artifact declaring uninstall unsupported gets no uninstall-stage Job. No-Job outcomes are not reconcile errors and never cordon or drain a node.
- Staleness judges a Job by its own kind. An unknown outcome, a tag re-pushed to a new digest and an artifact tag re-pushed as an image never invalidate or delete an existing Job; containerSHA and version changes do; a legacy Job for a package known to be an artifact is replaced. Tests cover these through handleExistingJob, the sweep and the finished-Job carve-out.
- Caching behaves as briefed. The inspection-outcome accessor returns disabled, legacy, artifact, rejected or unknown, and inspects synchronously on a cache miss.

The operator vendors the artifact module (notices, license-check, out-of-tree build). operator-ci.yaml triggers on artifact/\*\* and checks vendor staleness. lifecycle.md (done containers, staleness rule), operator-status.md, the chart docs (with the Go-agent prerequisite) and the RELEASE_NOTES are updated. cd operator && make vet lint unit-tests passes, with no stray CRD annotation rewrites.

### operator-uninstall-capability (#824) — Operator reads uninstall support from artifact metadata with a defined precedence over uninstall.enabled

Why: package uninstall is an explicit opt-in today (docs/designs/uninstall-enhancement.md; 71ca22b8, #200). Package.uninstall.enabled declares support, and uninstall.apply requests a run. That design left capability discovery from the artifact out of scope. Epic NVIDIA/nodewright#194 adds it: the metadata of a package artifact declares uninstall supported or unsupported (docs/user-guide/package-artifact-spec.md).

What operator-artifact-pods already provides:

- the opt-in PACKAGE_ARTIFACTS option, and named constants for the inspection request timeout, the tag-resolution TTL and the failure backoff window
- an operator-internal accessor returning a package’s inspection outcome through the shared cache: disabled (option off, no request), legacy, artifact (accepted, with its parsed description including the uninstall capability, and the pinned digest), rejected, or unknown (failed or timed out, with the error class). On a cache miss it inspects synchronously within the request timeout. A cached result, or a cached failure inside its backoff window, is returned with no request, and a failed tag refresh keeps the last good result.
- Job staleness that judges an existing Job by its own kind: an unknown outcome or a re-pushed tag never invalidates a Job, and an artifact Job keeps the digest it pinned
- the uninstall-stage backstop: no uninstall-stage Job for an accepted artifact declaring uninstall unsupported, with a Warning event. Like a rejection, it is not a reconcile error, and no node is cordoned or drained for it. Every other outcome at that stage builds as at any stage, including the legacy fallback on an inspection failure.
- the PackageArtifactRejected condition pattern in operator/internal/wrapper

The agent (agent-streaming-install) also refuses to uninstall an artifact that declares it unsupported.

How uninstall is planned today. Read this code and its comments before changing it:

- skyhook_controller.go: RunSkyhookPackages calls HandleUninstallRequests first, before any stage Job is built. For a package whose spec requests uninstall (IsUninstalling(), or the NodeWright is being deleted and UninstallEnabled()), it starts a cycle on each node whose entry sits at a Complete install stage, by writing StageUninstall/in_progress. From then on, its StageUninstall and StageUninstallInterrupt branches re-add the package whatever the spec says.
- HandleCancelledUninstalls resets an active StageUninstall entry to StageApply once the spec no longer requests uninstall, except for an enabled package during deletion.
- shouldSkipApplyForUninstall keeps an uninstalled (absent) package from being re-applied.
- HandleFinalizer waits while any enabled package is present in node state on any node, and in that case blocks deletion of a paused or disabled NodeWright.
- cluster_state_v2.go: HasUninstallWork, UpdateBlockedCondition and UpdateUninstallConditions.
- The spec-only API helpers Package.UninstallEnabled() and IsUninstalling() (operator/api/nodewright/v1alpha1/nodewright_types.go, and skyhook_types.go of the legacy group), NodeState.IsComplete, and the webhook’s removal and downgrade rules.

Decision (fixed). One controller-side helper gives each package an uninstall decision:

- spec: the option is off, the reference is a legacy image, or it is an accepted artifact declaring supported. Everything behaves exactly as today.
- unsupported: an accepted artifact declaring unsupported. uninstall.enabled stays the opt-in, and the artifact declaration is a ceiling.
- deferred: the option is on and the outcome is unknown or rejected. The spec is never used as a guess.

Resolve the decisions once per NodeWright per Reconcile pass, before refreshSkyhookConditions and HandleFinalizer run, and give the same decisions to every site below, so one pass never mixes them. Consult the accessor only for packages whose spec requests uninstall or that are in an uninstall cycle on some node. The same spec and outcome always give the same decision.

Rules:

(1) Start. HandleUninstallRequests starts a cycle only when the spec requests it and the decision is spec.

- unsupported: start nothing. Emit a Warning event per package naming the package, version, digest and declaration. Set the NodeWright condition UninstallUnsupported (True, listing each package, whether its cycle is not started or stalled, and the remedy). Requeue no later than the tag-resolution TTL, so a re-push is noticed.
- deferred: start nothing, and do not treat the package as disabled either. Emit a Warning event, reason UninstallDeferred, naming the package, the reference and the error class or rejection reason. Requeue no later than the failure backoff window, even when the NodeWright is otherwise complete. Once the outcome resolves, the start follows the resolved decision. The message names the remedies for a failure that persists: give the operator access to the registry through the pull Secret, set enabled false, or turn the option off.

Neither is a reconcile error: the pass continues with other packages and nodes.

(2) A cycle already under way.

- StageUninstallInterrupt runs to completion whatever the decision. The uninstall steps already ran on the host, interrupt Jobs never read the artifact, and today’s code already refuses to cancel this phase.
- StageUninstall with an existing uninstall Job: the staleness rule alone judges the Job, which finishes with the reference it was built with. An artifact Job pins a digest that declared supported when the Job was built.
- StageUninstall with no Job, under unsupported: the cycle stalls. The backstop builds no Job and cordons or drains nothing, the entry stays at StageUninstall/in_progress, and UninstallUnsupported lists it as stalled. A node already cordoned for it stays cordoned. The cycle resumes when the decision becomes spec again. The user cancels it with the existing spec cancel path (uninstall.apply or uninstall.enabled set false), which resets the entry to StageApply as today. Do not auto-cancel.
- StageUninstall with no Job, under deferred: the stage builder’s own rules apply. An inspection failure builds the legacy fallback Job, whose package-image init container cannot run an artifact. A rejected artifact gets no Job, under PackageArtifactRejected.

(3) Finalizer. HandleFinalizer waits for an enabled package present in node state, as today, with one exception: it does not wait for a package whose decision is unsupported while no node has it in an uninstall cycle. That package’s node state is kept, as for an enabled-false package. The finalizer waits for a deferred package and for a stalled cycle, and the condition message says that setting enabled false releases a stalled one.

(4) Uninstall work. HasUninstallWork, and UpdateBlockedCondition’s skip for a package that is going away, count a spec request only when the decision is not unsupported. A deferred request still counts as work, so the NodeWright keeps reconciling.

(5) These stay spec-only, with a comment where the reason is not obvious:

- HandleCancelledUninstalls: cancelling is a spec action.
- shouldSkipApplyForUninstall, NodeState.IsComplete and UpdateBlockedCondition’s dependency-done test: a capability change never re-applies or undoes a completed (absent) uninstall.
- UpdateUninstallConditions: it reads only cycles.
- The API helpers and the webhook.

(6) Events and the condition. On the first successful inspection of a digest for a package of a NodeWright, emit one Normal event naming the package, version, digest and declared uninstall capability. When the capability is supported and enabled is false, the event also says that enabled opts in. Do not re-emit it every reconcile; a restart may emit it once more. enabled false with unsupported emits nothing else. UninstallUnsupported clears when no package is in conflict any more: enabled or apply was set false, the artifact changed, or the option was turned off.

Admission stays spec-only. The webhook makes no registry calls, and its removal and downgrade rules keep reading enabled from the spec. Document the consequence: removing a package whose artifact declares unsupported while enabled is true requires setting enabled false first, because the webhook requires an enabled package to finish uninstalling before it can be removed.

Tests (cluster-free, with the vendored artifact/registrytest). Drive HandleUninstallRequests, HandleFinalizer, RunSkyhookPackages and the condition updates directly.

- Decision matrix: enabled true and false, times option off, legacy, supported, unsupported, unknown (a registry failing every request) and rejected, times uninstall.apply and finalizer deletion. For each case assert:
  - whether StageUninstall is written and whether an uninstall Job is created
  - whether node state is retained and whether the finalizer is released
  - the condition, the events and the requeue

  Seed each case so the effective decision differs from the spec-only decision wherever the precedence applies, so a regression to spec-only fails.

- Restart: a fresh reconciler with a cold cache, against a reachable registry serving an unsupported artifact, under apply true and under deletion. The first pass writes no StageUninstall and creates no Job, and under deletion the finalizer is released.
- Outage: a cold cache and a registry failing every request. No StageUninstall is written, UninstallDeferred is emitted, the result requeues within the backoff window, and the finalizer keeps waiting. After recovery, a supported artifact starts the cycle and an unsupported one never does.
- Tag re-push from supported to unsupported mid-cycle, over three nodes:
  - a node at StageUninstall with a running artifact uninstall Job keeps it
  - a node at StageUninstall with no Job gets no Job, is not cordoned or drained, and is listed as stalled
  - a node at a Complete install stage does not start

  Under deletion, the finalizer waits for the stalled node. Setting enabled false then cancels it through the existing path and releases the finalizer.

- Re-push from unsupported back to supported: the stalled cycle resumes and the condition clears. Re-pushing the artifact as a legacy image, or turning the option off, returns the decision to the spec.
- A node at StageUninstallInterrupt under unsupported completes, and its entry is removed.
- A completed (absent) uninstall is not re-applied when the artifact turns unsupported while apply is still true.
- The existing uninstall, finalizer and webhook tests pass unmodified.

The controller envtest suites share one live manager and apiserver across specs. Drive the functions under test directly, or give new specs nodes the manager does not select. Clean up every object a spec creates, finalizers included, since envtest runs no garbage collector. Run every new or changed spec under several random ginkgo --seed values.

Docs:

- docs/user-guide/uninstall.md: the precedence table (decisions spec, unsupported and deferred, against enabled and apply, under apply and under deletion), deferral (with the option on, a reference the operator cannot inspect defers uninstall even for a legacy image, and the remedies), the stalled cycle and its remedy, the condition and events, and how to remove a package whose artifact declares unsupported.
- docs/designs/uninstall-enhancement.md: a short addendum saying discovery is now specified, linking uninstall.md and the spec. Keep the original text.
- docs/architecture/operator-status.md: UninstallUnsupported.
- operator/RELEASE_NOTES.md: an Unreleased entry, because the artifact can now override enabled true, and with the option on an unknown outcome defers uninstall. Users will notice both.

No test asserts these doc texts.

Verify: cd operator && make vet lint unit-tests. There are no CRD changes, so do not commit version-annotation rewrites under operator/config/crd/bases. Comment-only edits in Go files are code changes and run the gates.

- scope: operator/internal/controller/, operator/internal/wrapper/, operator/RELEASE_NOTES.md, docs/user-guide/uninstall.md, docs/designs/uninstall-enhancement.md, docs/architecture/operator-status.md
- acceptance: The controller plans uninstall through one helper that gives each package one decision per pass: spec (option off, legacy, or an artifact declaring supported), unsupported, or deferred (option on, with an unknown or rejected outcome).
- A cycle starts only under spec. unsupported never starts one and is surfaced by UninstallUnsupported plus a Warning event. deferred starts none, emits UninstallDeferred and requeues within the backoff window.
- After a restart, a reachable registry decides from the artifact on the first pass, and an outage defers.
- StageUninstallInterrupt completes and a running uninstall Job finishes. A StageUninstall entry with no Job under unsupported stalls (no Job, cordon or drain), is listed in the condition, and the existing spec cancel resolves it.
- The finalizer does not wait for an unsupported package outside a cycle, and waits for deferred and stalled ones.
- Cancellation, completed uninstalls, NodeState.IsComplete and the webhook stay spec-only. Legacy and option-off behave exactly as before.
- Discovery emits one Normal event per digest.

The decision matrix and the restart, outage, re-push and option transitions are tested. uninstall.md, the design addendum, operator-status.md and RELEASE_NOTES document the behaviour. cd operator && make vet lint unit-tests passes, with no stray CRD annotation rewrites.

### artifact-docs-migration (#825) — Author how-to, conversion path and architecture docs for package artifacts, with the documented commands executed by tests

Why: epic NVIDIA/nodewright#194 ships packages as OCI artifacts. This branch already has:

- The shared module artifact/ (github.com/NVIDIA/nodewright/artifact): v1 spec types and schema, the oras-go client, inspect, atomic layout and artifact/registrytest. It has its own workflow, .github/workflows/artifact-ci.yaml, and a normative spec at docs/user-guide/package-artifact-spec.md.
- Agent streaming: NODEWRIGHT_PACKAGE_REF and NODEWRIGHT_REGISTRY_AUTH_FILE, with the credential order pull-secret file, then node docker config files, then anonymous. The agent refuses to uninstall an artifact that declares uninstall unsupported.
- Operator integration behind the opt-in PACKAGE_ARTIFACTS option (chart value controllerManager.manager.env.packageArtifacts): fallback to the legacy stage Job when inspection fails; agent --version as the exit-0 container of artifact stage Jobs and, with the option on, of every interrupt Job, which makes a Go agent (agent/v7.x or newer) a prerequisite of the option (chart/README.md); existing Jobs keeping the reference they were built with when a tag is re-pushed (docs/architecture/lifecycle.md); the PackageArtifactRejected and UninstallUnsupported conditions and the UninstallDeferred event; and the uninstall decisions (spec, unsupported, deferred) with the stalled-cycle remedy in docs/user-guide/uninstall.md.
- Regenerated agent/ and operator/ notices files.

Read those before writing.

This part gives package authors and operators the how-to, the conversion path and the architecture picture; proves the documented commands work; and closes the cross-cutting loose ends. The packages themselves live in NVIDIA/nodewright-packages (renamed from NVIDIA/skyhook-packages). Converting them is out of scope; the instructions belong here.

Deliver:

(1) docs/user-guide/package-artifacts.md, for authors and cluster operators, covering:

- The package tree: the same as the /skyhook-package of a legacy image.
- Building the payload layer, with an exact tar command.
- Generating the metadata document from config.json, uninstall declaration included, with an exact jq command, and what declaring unsupported does (link the precedence in uninstall.md).
- Pushing with an exact oras push command that uses the artifact type, config media type and payload media type of the spec, plus the --oci-layout variant for local checks.
- Converting an existing image package: extract /skyhook-package with an exact crane export | tar command, then build and push. Publish the artifact under a new version: re-pushing an existing version tag as an artifact replaces the in-flight image Jobs of that version.
- Re-pushing: a Job already created keeps the reference it was built with, so new content under a re-pushed tag reaches only Jobs built afterwards. Pin containerSHA for immutable content.
- Enabling packageArtifacts, and its prerequisite: the agent image of the operator and every agentImageOverride must be a Go agent (agent/v7.x or newer).
- Credentials: the pull Secret serves the operator and the agent; node docker config files serve the agent only. Explain why the operator falls back to the legacy stage Job when it cannot inspect, and why an uninstall instead waits (UninstallDeferred) until it can.
- The minimum agent and operator versions.
- The dual-support period: Package.image plus version or containerSHA stays the reference, no CR changes, and artifacts are detected from the manifest.
- Limitations: no kubelet credential-provider plugins, no containerd mirror or hosts.toml configuration, no plain-HTTP registries, no multi-platform artifacts, and host files and dynamic interrupts are declared but refused until a later release.

(2) A test in artifact/ that executes the documented commands verbatim.

- Extract the commands from fenced blocks the doc marks for testing, so doc and test cannot drift.
- Run the tar, jq and oras push --oci-layout commands against a fixture package. Then inspect and lay out the result with the library, and compare it to the source tree.
- Run the crane export conversion against an image the test pushes to artifact/registrytest. Add only the TLS-trust flag that registry needs.
- A missing tool skips loudly locally, naming the tool. artifact-ci.yaml installs oras and crane with ./.github/actions/setup-oras and ./.github/actions/setup-crane, and sets an environment variable that turns those skips into failures, so CI never skips.
- Also feed the degraded forms of the documented invocations: an unset or empty variable in a documented command, a package directory without config.json, and metadata generated from a different config.json than the one in the payload. Assert the library rejects each with the documented error.

(3) docs/architecture/package-artifacts.md, covering:

- the shared library, and why it is a separate module consumed through replace and vendor
- the roles of the operator, the agent and the future CLI
- inspection, the cache, the fallback, why an existing Job keeps the reference it was built with, and why an uninstall is deferred rather than started on an unknown outcome
- how the resolved descriptor and digest leave room for runtime provenance verification
- the follow-ups: host files, dynamic interrupts and preflight, CLI validation, and verification

(4) Cross-links:

- docs/index.yml: add both pages.
- README.md: the Pre-built Packages section, plus a short pointer to the author guide.
- docs/getting-started/overview.md: replace the stale NVIDIA/skyhook-packages link with NVIDIA/nodewright-packages.
- docs/user-guide/custom-resource.md: Package.image may name a package artifact or a container image, detected from the manifest when packageArtifacts is enabled.
- docs/user-guide/package-artifact-spec.md: link the how-to.

(5) ROADMAP.md: move the OCI/ORAS package format from not started to partially shipped. State what shipped (spec v1, the shared library, agent streaming, the operator opt-in, uninstall discovery) and what remains (runtime provenance, CLI validation, executing host files and declared interrupts, preflight). Add a Revision History row.

(6) Notices and dependency automation:

- Root THIRD_PARTY_NOTICES.md: run make notices-rollup; make notices-check must pass.
- .github/workflows/merge-gate.yaml: add artifact/go.mod, artifact/go.sum and artifact/LICENSE to the deps and notices path filters.
- .github/renovate.json5: add a packageRule that groups the third-party dependencies of the artifact module (oras.land/oras-go/v2, github.com/opencontainers/\*\*) across artifact/, operator/ and agent/, so the library and both vendor trees move in one PR.

If a documented command exposes a defect in artifact/, fix it there with a test.

Verify:

- cd artifact && make test lint, with oras and crane installed, and once with them absent to see the loud skips.
- make notices-check.
- The markdownlint and lychee internal-link checks that lint-ci.yaml runs.
- actionlint.
- make renovate-config-check if docker is available; otherwise CI runs it in validate-renovate.

No test asserts README or roadmap text.

- scope: artifact/, .github/workflows/artifact-ci.yaml, .github/workflows/merge-gate.yaml, .github/renovate.json5, docs/user-guide/package-artifacts.md, docs/architecture/package-artifacts.md, docs/user-guide/package-artifact-spec.md, docs/user-guide/custom-resource.md, docs/getting-started/overview.md, docs/index.yml, README.md, ROADMAP.md, THIRD_PARTY_NOTICES.md
- acceptance: docs/user-guide/package-artifacts.md (build, push, convert, re-push behaviour, enable with its Go-agent prerequisite, credentials, versions, limitations) and docs/architecture/package-artifacts.md are published and linked from docs/index.yml, README.md, custom-resource.md and the spec. A test executes the documented tar, jq, oras and crane commands verbatim from the doc, plus their degraded forms. It skips loudly only when a tool is absent locally, and never in CI, where artifact-ci.yaml installs the tools. The stale link in overview.md is fixed and ROADMAP.md is updated. The root THIRD_PARTY_NOTICES.md is regenerated and make notices-check passes. The merge-gate filters and the renovate grouping cover the artifact module.

## Dependency graph

- **artifact-spec** — no dependencies
- **artifact-client** — after artifact-spec
- **agent-streaming-install** — after artifact-client
- **operator-artifact-pods** — after artifact-client
- **operator-uninstall-capability** — after operator-artifact-pods
- **artifact-docs-migration** — after agent-streaming-install, operator-uninstall-capability

May run in parallel (no dependency path between them): agent-streaming-install and operator-artifact-pods; agent-streaming-install and operator-uninstall-capability

## Explicit non-goals

- Redoing the Go agent: the rewrite (#213-#222) is cut over on main (787c695f, #685), and no Python agent remains.
- Changing the uninstall API: Package.uninstall.enabled and apply exist since 71ca22b8 (#200) and keep their meaning. This design adds discovery, not fields.
- Changing image-reference rules: the bare registry/repository Package.image with version or containerSHA (44dba453, #283) is reused as it is.
- Coordinating with a package-as-Jobs branch: #223 merged to main (3c13b4a5, #459, closed 2026-08-15), so the operator parts change main’s Job builder and nothing targets feature/package-as-jobs. ROADMAP’s stale `in progress` wording for that item is not corrected here.
- Any CRD change: no artifactRef field, no rename of Package.image, no status field for capabilities (they surface through conditions and events), and no new API group. Artifacts are detected from the manifest, never from a spec field.
- Always-on inspection: rejected in favour of the opt-in PACKAGE_ARTIFACTS operator option (default false). Inspection gives the operator an outbound registry dependency, with timeouts, that every cluster would pay, including air-gapped ones that never use artifacts.
- Other library placements: go.work was rejected because it would change how both modules vendor, and every CI and Docker build with them. Putting the library inside operator/ was rejected because the agent would inherit the Kubernetes module graph. Putting it inside agent/ was rejected because the operator would depend on the agent module and its dependency bumps.
- go-containerregistry: rejected for oras-go v2, which has a lighter dependency graph, and the epic and roadmap name ORAS.
- Other precedence rules for uninstall. Spec-always-wins was rejected because it would run an uninstall the artifact says is unsupported. Artifact-always-wins was rejected because it would run uninstall without the explicit opt-in the uninstall design requires. A third `noop` capability value is also not added. Using the spec alone when the option is on and the outcome is unknown or rejected was rejected in review: starting a cycle is the commitment point (RunSkyhookPackages calls HandleUninstallRequests before any stage Job is built, and a StageUninstall entry is then re-added whatever the spec says), so such a start is deferred with an event and a requeue.
- Falling back to the spec for inspection failures that look persistent (unauthorized, not found), as stage Jobs do: rejected for uninstall. Such a failure can hide an artifact that declares unsupported (a pull Secret broken or rotated after install), and starting the cycle is the commitment point. The cost is documented, not hidden: with PACKAGE_ARTIFACTS on, a package the operator cannot inspect, a legacy image included, cannot start an uninstall until the operator gets registry access, enabled is set false, or the option is turned off. Stage Jobs keep their fallback, which can never run an artifact’s uninstall steps because the kubelet cannot run an artifact.
- Auto-cancelling an uninstall cycle that a later re-push leaves at StageUninstall against an artifact declaring unsupported: rejected. The existing cancel path resets the entry to StageApply and re-runs apply, and the interrupt of a package that has one, which the user did not ask for. The cycle stalls visibly instead (UninstallUnsupported, no Job, no cordon or drain), and the spec cancel the condition names does the reset on request. HandleCancelledUninstalls, NodeState.IsComplete and the handling of a completed (absent) uninstall stay spec-only: a capability change never re-applies or undoes a finished uninstall.
- Stopping a running uninstall Job, or a node at StageUninstallInterrupt, because the capability changed. The Job finishes with the digest it pinned, which declared uninstall supported when the Job was built. The interrupt phase follows uninstall steps that already ran on the host, and today’s code already refuses to cancel it.
- Executing what is only declared: placing host-file layers, running declared interrupts, the preflight stage, and dynamic cordon/drain. Spec v1 declares, validates and exposes these, and the operator and agent refuse such artifacts rather than silently ignoring them.
- Choosing an interrupt Job’s exit-0 container from the inspected package kind, as review suggested: rejected. An interrupt Job built while the outcome is unknown (an operator restart, then a failed inspection) would fall back to the package image and hang an artifact package’s interrupt again. With PACKAGE_ARTIFACTS on, every interrupt Job instead runs the agent image its interrupt container already pulled, with --version, and reads no outcome. The cost is a prerequisite that is documented, not detected: with the option on, the operator’s agent image and every agentImageOverride must be a Go agent (agent/v7.x, since 787c695f), because the Python agent/v6.x has no --version. With the option off, interrupt Jobs are unchanged.
- Recreating Jobs for registry-side changes under an unchanged spec. Turning the option off, an unknown outcome (a cold cache after a restart, an inspection failure, a wrong pull Secret), a new digest pushed under the same tag, and an artifact tag re-pushed as an image never invalidate or delete an existing Job. It finishes with the reference it was built with, as a legacy image:tag Job does when its tag is re-pushed. The one exception is a legacy Job for a package now known to be an artifact, which cannot pull. Authors who need immutable content pin containerSHA, whose change is a spec change and does recreate.
- Runtime provenance verification (cosign, OCI referrers): this is a separate design. The library exposes the resolved descriptor and digest for it.
- CLI commands and client-side package validation, and moving config.json schema validation from agent/internal/config into the library.
- Kubelet registry features the new pull path lacks: kubelet image credential-provider plugins, containerd mirror and hosts.toml configuration, plain-HTTP or insecure registries, and docker credential helpers. These are documented as limitations, and legacy image packages keep using the kubelet.
- Multi-platform (index) package artifacts: not defined in spec v1. They are recognised and refused, never routed to the legacy path.
- Operator detection of agent versions: an older agent given an artifact Job fails closed (config.json is missing), and an agent without --version (the Python agent/v6.x) fails the exit-0 container. The minimum agent version and the Go-agent prerequisite of packageArtifacts are documented instead.
- Renaming skyhook-named on-host paths, SKYHOOK_\* variables, completion markers or directories (#437, #438, #442). New variables use the NODEWRIGHT\_ prefix.
- Converting or republishing the packages in NVIDIA/nodewright-packages (formerly skyhook-packages); cutting agent, operator or chart releases; and bumping the chart’s agent pin.
- New chainsaw, kind or e2e suites (they cannot run here and are not acceptance), regenerating the architecture diagrams, a component label for artifact/, and surfacing ImagePullBackOff (#306).
