# NodeWright Package Artifact Specification, Version 1

This page specifies version 1 of the NodeWright package artifact: a NodeWright package stored in an OCI registry as an artifact rather than as a container image. It is written for package authors and for anyone writing a tool that reads or checks packages. The Go module `github.com/NVIDIA/nodewright/artifact` implements it, and the constant, error and reason names below are that module's.

> **Note:** No operator or agent release reads package artifacts yet. Until one does, NodeWright runs every package as a container image, as the rest of this guide describes. This page specifies the format ahead of that support, and the `artifact` module, which classifies manifests, parses metadata and checks layer entries, is its only implementation. Where a rule below says what an operator, agent or other reader does with a package, it states what an implementation must do, not what a released one does.

## Media types

| Constant | Value | Identifies |
|---|---|---|
| `ArtifactTypePackage` | `application/vnd.nvidia.nodewright.package.v1` | The manifest's `artifactType` |
| `MediaTypeMetadata` | `application/vnd.nvidia.nodewright.package.config.v1+json` | The config blob, which holds the package metadata |
| `MediaTypePayload` | `application/vnd.nvidia.nodewright.package.payload.v1.tar+gzip` | A payload layer |
| `MediaTypeHostFiles` | `application/vnd.nvidia.nodewright.package.hostfiles.v1.tar+gzip` | A host-files layer |
| `AnnotationDestination` | `com.nvidia.nodewright.package.destination` | The layer annotation naming a host-files layer's destination |

Every package media type starts with `application/vnd.nvidia.nodewright.package.`, the package family prefix that [classification](#classification) keys on.

## Manifest

A v1 package is an OCI image manifest (`application/vnd.oci.image.manifest.v1+json`):

- `config` is required and is the metadata descriptor, with media type `MediaTypeMetadata`. This is the pattern Helm charts follow on OCI registries: a reader learns what the package is from one small blob, without fetching a layer.
- `artifactType` is optional. When present, it must be `ArtifactTypePackage`.
- `layers` holds one or more payload layers and any number of host-files layers, in any order.
- Every descriptor, the config and each layer, has a well-formed `sha256`, `sha384` or `sha512` digest and a positive size.

`oras push --config <file>:application/vnd.nvidia.nodewright.package.config.v1+json` produces the shape without `artifactType`; adding `--artifact-type application/vnd.nvidia.nodewright.package.v1` produces the shape with it.

The manifest may carry these informational annotations:

| Annotation | Meaning |
|---|---|
| `org.opencontainers.image.title` | Human-readable name of the package |
| `org.opencontainers.image.version` | Version of the package |
| `org.opencontainers.image.source` | URL of the package's source |
| `org.opencontainers.image.description` | Human-readable description of the package |

None of them changes behaviour. Every other manifest annotation, and every layer annotation except `AnnotationDestination`, is ignored, including the `org.opencontainers.image.title` that oras writes on each layer. Manifest fields this page does not name are ignored too.

A minimal manifest:

```json manifest
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.nvidia.nodewright.package.v1",
  "config": {
    "mediaType": "application/vnd.nvidia.nodewright.package.config.v1+json",
    "digest": "sha256:22af2f00d75dcfbace980e1c8560018ff027cf25ccbe4d1782c25ded0b1dfa57",
    "size": 412
  },
  "layers": [
    {
      "mediaType": "application/vnd.nvidia.nodewright.package.payload.v1.tar+gzip",
      "digest": "sha256:c737795717282ead2a418a1c8463457acc8fb3ac9eb3814f0374a285b07dfe5a",
      "size": 1536
    }
  ]
}
```

## Metadata

The config blob is the package metadata: one JSON object whose bytes match the config descriptor's size and digest. Its schema is [`metadata-schema.json`](../../artifact/schemas/v1/metadata-schema.json).

| Field | Required | Type | Meaning |
|---|---|---|---|
| `uninstall` | yes | `"supported"` or `"unsupported"` | Whether the package supports being uninstalled |
| `config` | yes | object | The package's `config.json` |
| `interrupt` | no | object | The [interrupt declaration](#interrupt-declaration) |

Unknown fields are rejected at every level, including inside `interrupt` and its steps, so a misspelled field fails rather than being dropped. The one exception is the inside of `config`, which the agent validates as `config.json`.

`config` carries the package's `config.json` so that a reader can see a package's steps without fetching a layer. The payload must have `config.json` at its root as a regular file, not a symlink or a directory, and `config` must be JSON-equal to it: the same values, with key order, whitespace and number spelling (`1`, `1.0`, `1e0`) ignored. A reader must enforce this when it lays out the payload; parsing the metadata does not check it.

A minimal valid metadata document:

```json valid
{
  "uninstall": "supported",
  "config": {
    "schema_version": "v1",
    "package_name": "hello",
    "package_version": "1.0.0",
    "expected_config_files": [],
    "modes": {
      "apply": [
        {
          "name": "hello",
          "path": "hello.sh",
          "arguments": [],
          "returncodes": [0],
          "on_host": true,
          "idempotence": true,
          "upgrade_step": false
        }
      ]
    }
  }
}
```

Each rejected document below is fenced with the reason it fails with. They shorten `config` to `{}`, whose contents the metadata rules never inspect.

Metadata that is not a JSON object:

```json invalid malformed-metadata
["supported", {}]
```

No `uninstall`:

```json invalid metadata-schema
{"config": {}}
```

An `uninstall` value other than the two allowed:

```json invalid metadata-schema
{"uninstall": "optional", "config": {}}
```

No `config`:

```json invalid metadata-schema
{"uninstall": "supported"}
```

A `config` that is not an object:

```json invalid metadata-schema
{"uninstall": "supported", "config": "config.json"}
```

A misspelled top-level field:

```json invalid metadata-schema
{"uninstall": "supported", "config": {}, "interupt": {}}
```

## Interrupt declaration

A package may declare how it senses that it needs an interrupt and how it runs one:

```json
"interrupt": {
  "sense": {"path": "interrupt/needs-reboot.sh", "arguments": [], "on_host": true},
  "run": {"path": "interrupt/reboot.sh", "arguments": [], "on_host": true}
}
```

`sense` and `run` are both required. Each is a step with exactly three fields, all required:

- `path` is the executable, relative to `skyhook_dir` like the step paths in `config.json`. It is non-empty, has no leading or trailing `/`, and has no empty, `.` or `..` segment.
- `arguments` is an array of strings passed to the executable. It may be empty.
- `on_host` means what it means in `config.json`: `true` runs the executable on the host, `false` runs it inside the agent container.

`sense` answers whether an interrupt is required: exit code 0 means it is, 1 means it is not, and any other exit code fails the step. `test -f /var/run/reboot-required` follows this convention, so a sense script that runs only that command is a valid sense step. `run` performs the interrupt, and exit code 0 is success.

In version 1 a package can declare an interrupt and parsing the metadata validates the declaration, but version 1 defines no way to run it: an operator or agent that implements only version 1 must refuse a package that declares one. The `interrupt` field of a package in the NodeWright spec (see [the custom resource reference](custom-resource.md#interrupt)) is planned to become an override of this declaration, as the [ROADMAP](../../ROADMAP.md) describes under bring-your-own interrupts.

An interrupt without `run`:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "interrupt/needs-reboot.sh", "arguments": [], "on_host": true}
  }
}
```

A step without `on_host`:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "interrupt/needs-reboot.sh", "arguments": []},
    "run": {"path": "interrupt/reboot.sh", "arguments": [], "on_host": true}
  }
}
```

A step with a field from `config.json` steps that interrupt steps do not have:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "interrupt/needs-reboot.sh", "arguments": [], "on_host": true},
    "run": {"path": "interrupt/reboot.sh", "arguments": [], "returncodes": [0], "on_host": true}
  }
}
```

`arguments` that are not an array:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "interrupt/needs-reboot.sh", "arguments": "--quiet", "on_host": true},
    "run": {"path": "interrupt/reboot.sh", "arguments": [], "on_host": true}
  }
}
```

An absolute step path:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "/usr/bin/needs-reboot", "arguments": [], "on_host": true},
    "run": {"path": "interrupt/reboot.sh", "arguments": [], "on_host": true}
  }
}
```

A step path with a `..` segment:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "interrupt/needs-reboot.sh", "arguments": [], "on_host": true},
    "run": {"path": "../reboot.sh", "arguments": [], "on_host": true}
  }
}
```

A step path with a leading `./`, which layer entry names accept but step paths do not:

```json invalid metadata-schema
{
  "uninstall": "supported",
  "config": {},
  "interrupt": {
    "sense": {"path": "./interrupt/needs-reboot.sh", "arguments": [], "on_host": true},
    "run": {"path": "interrupt/reboot.sh", "arguments": [], "on_host": true}
  }
}
```

## Layers

Every layer is a gzip-compressed tar archive.

**Payload layers** (`MediaTypePayload`). A package has one or more. They apply in manifest order, and a later entry overwrites an earlier one at the same path. Together they form exactly the tree a legacy image carried under `/skyhook-package`, with `config.json` at its root. A payload layer must not carry `AnnotationDestination`.

**Host-files layers** (`MediaTypeHostFiles`). A package has any number. Each must carry `AnnotationDestination`, an absolute host path other than `/` with no trailing `/` and no empty, `.` or `..` segment. Its entries follow the tar rules below, relative to the destination. Status: host-files layers are declared and validated but not yet placed by the agent.

Any other layer media type is invalid.

Every entry of either kind of layer follows these tar rules:

- Its name is non-empty, not absolute, and has no `..` segment, even one that would clean away, as in `a/../b`. No segment is longer than 255 bytes, the longest file name Linux filesystems accept.
- A `./` prefix is accepted, and names are cleaned, so `./etc//motd` is `etc/motd`. The root entry, `./` or `.`, must be a directory.
- Its type is a regular file (`0`, or the legacy `\0`), a directory (`5`) or a symlink (`2`). A symlink's target may be absolute or relative and point anywhere, but it is non-empty and at most 4095 bytes, the longest Linux accepts.
- Every other type is rejected, including hardlinks (`1`), character devices (`3`), block devices (`4`), FIFOs (`6`), contiguous files (`7`) and PAX global headers (`g`). PAX extended headers (`x`) and GNU long-name and long-link records (`L`, `K`) are not entries: they belong to the entry that follows them.
- Laying out a layer never follows a symlink. An entry whose path passes through a symlink, or through anything else that is not a directory, is rejected, whether an earlier entry or layer laid it down or it was there before.

Laying out a layer also follows these rules:

- A later entry replaces whatever is at its path: a file, a symlink, or a directory and its contents. The one exception is a directory entry over an existing directory, which keeps the directory and its contents and sets only its mode. A symlink at the path of an entry is replaced, never written through.
- A symlink is created with its target exactly as the entry gives it.
- A regular file or directory gets exactly the permission bits of its entry's mode, whatever the umask, and the setuid, setgid and sticky bits are dropped. Directory modes are set only once every entry of the layer is written, and for payload layers only once every payload layer is written, so a directory of mode `0555` still receives its children, including those a later payload layer carries.
- Ownership and modification times are not preserved.
- A parent directory the layer does not name is created with mode `0755`.
- The mode of the root entry is ignored: the directory the layer is laid out in keeps its own.

A layer whose bytes do not match its descriptor's size and digest is an integrity error, `ErrDigestMismatch` of the module's `client` package, not an invalid artifact. It takes precedence over every reason: a reader verifies the whole layer before it reports that the layer is not a gzip-compressed tar or that an entry breaks a rule.

## Classification

A reader classifies a manifest from its bytes and its media type as the registry reported it, falling back to the document's own `mediaType` when the registry reported none. The first row that matches decides:

| # | Manifest | Outcome |
|---|---|---|
| 1 | Larger than `MaxManifestSize`, not a JSON object, a field of the wrong JSON type, or a reported media type that disagrees with the document's `mediaType` | `ErrMalformedManifest` |
| 2 | A media type other than `application/vnd.oci.image.manifest.v1+json`, `application/vnd.oci.image.index.v1+json`, `application/vnd.docker.distribution.manifest.v2+json` or `application/vnd.docker.distribution.manifest.list.v2+json`, or no media type at all | `ErrUnsupportedForm` |
| 3 | An OCI image index or Docker manifest list whose `artifactType` is in the family | `ErrUnsupportedForm` |
| 4 | Any other OCI image index or Docker manifest list | legacy |
| 5 | An OCI or Docker schema 2 image manifest with no family media type in its `artifactType`, config or layers | legacy |
| 6 | An image manifest with a family media type of a version other than v1 in any of those places | `ErrUnsupportedVersion` |
| 7 | A Docker schema 2 image manifest | `ErrUnsupportedForm` |
| 8 | An OCI image manifest that breaks a [manifest rule](#manifest) | `ErrInvalidArtifact` |
| 9 | Any other OCI image manifest | package |

Legacy means a package container image, the form packages took before this specification.

The family and version rule:

- A media type is in the family when it starts with `application/vnd.nvidia.nodewright.package.`. The trailing dot is part of the prefix, so `application/vnd.nvidia.nodewright.packages.v1` is not in the family.
- A layer's media type counts on its own. A manifest with package layers under any other config, such as the empty config `oras push` writes without `--config`, is an invalid package, never a legacy image.
- A family media type's version is the first dot-separated segment after the prefix that is `v` followed by digits, ignoring any `+suffix`, so `application/vnd.nvidia.nodewright.package.payload.v10.v1.tar+gzip` is version v10. A family media type with no such segment is of no other version, so it reaches the v1 rules and fails them as an unknown type.

Each failure is one of four errors:

| Error | Meaning |
|---|---|
| `ErrMalformedManifest` | The bytes cannot be read as a manifest. |
| `ErrUnsupportedForm` | The manifest is in a form that cannot hold a v1 package. |
| `ErrUnsupportedVersion` | The manifest is a package of a version other than v1. |
| `ErrInvalidArtifact` | The manifest is a v1 package that breaks a v1 rule. It carries one of the reasons below. |

Rules are checked in the order of the table below, each across the whole manifest, so the reason reported never depends on layer order. Classification checks the first seven. Parsing the metadata blob checks `metadata-too-large` again on the blob itself, then the next three. Laying out a package checks `layer-entry` and then `config-mismatch`.

| Reason | Rule broken |
|---|---|
| `missing-metadata` | The config's media type is not `MediaTypeMetadata`. |
| `artifact-type` | `artifactType` is present and is not `ArtifactTypePackage`. |
| `media-type` | A layer is neither a payload nor a host-files layer. |
| `descriptor` | The config or a layer has a malformed digest, a digest of an unsupported algorithm, or a size that is not positive. |
| `metadata-too-large` | The config descriptor's size, or the metadata blob, exceeds `MaxMetadataSize`. |
| `no-payload` | There is no payload layer. |
| `destination` | A host-files layer has no valid `AnnotationDestination`, or a payload layer has one. |
| `metadata-mismatch` | The metadata blob's size or digest differs from the config descriptor's. |
| `malformed-metadata` | The metadata blob is not exactly one JSON object. |
| `metadata-schema` | The metadata object breaks a [metadata](#metadata) or [interrupt](#interrupt-declaration) rule. |
| `layer-entry` | A layer is not a gzip-compressed tar, or one of its entries breaks a [tar rule](#layers). |
| `config-mismatch` | The laid-out payload has no regular file `config.json` at its root, or its `config.json` is not JSON-equal to the metadata `config`. |

## Bounds

| Constant | Value | Applies to |
|---|---|---|
| `MaxManifestSize` | 4 MiB (4194304 bytes) | The manifest |
| `MaxMetadataSize` | 1 MiB (1048576 bytes) | The config descriptor's size and the metadata blob |

Both bounds are inclusive: a manifest or blob of exactly the bound is accepted, and one byte more is rejected.

- **Manifest.** 4 MiB is the default manifest read limit of oras-go and the manifest size registries must accept, so a manifest within it can be stored in any registry and read by oras-go, and a reader never buffers more than that before deciding what it holds.
- **Metadata.** A reader holds the metadata in memory whole to parse it. The bound is checked against the config descriptor's size before the blob is fetched and against the blob itself afterwards, so a broken or hostile descriptor cannot make a reader fetch or allocate without limit. 1 MiB leaves room for any realistic `config.json`.

## Introducing v2

- A new version introduces new media types, with v2 in place of v1 in every family media type, for example `application/vnd.nvidia.nodewright.package.v2` and `application/vnd.nvidia.nodewright.package.config.v2+json`.
- A v1 reader answers `ErrUnsupportedVersion` for any image manifest that carries a v2 family media type, and `ErrUnsupportedForm` for an index or manifest list whose `artifactType` is one, as rows 6 and 3 of the [classification](#classification) table say. It never falls back to treating either as legacy, so a newer package fails plainly on a reader that implements only v1 instead of being run as something it is not.
- Because v1 rejects unknown fields, any change to the metadata, even an added optional field, needs a new version. So does a new layer media type.
