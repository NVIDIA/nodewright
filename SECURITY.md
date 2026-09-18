## Security

NVIDIA is dedicated to the security and trust of our software products and services, including all source code repositories managed through our organization.

If you need to report a security issue, please use the appropriate contact points outlined below. **Please do not report security vulnerabilities through GitHub.** If a potential security issue is inadvertently reported via a public issue or pull request, NVIDIA maintainers may limit public discussion and redirect the reporter to the appropriate private disclosure channels.

## Reporting Potential Security Vulnerability in an NVIDIA Product

To report a potential security vulnerability in any NVIDIA product:
- Web: [Security Vulnerability Submission Form](https://www.nvidia.com/object/submit-security-vulnerability.html)
- E-Mail: psirt@nvidia.com
    - We encourage you to use the following PGP key for secure email communication: [NVIDIA public PGP Key for communication](https://www.nvidia.com/en-us/security/pgp-key)
    - Please include the following information:
   	 - Product/Driver name and version/branch that contains the vulnerability
     - Type of vulnerability (code execution, denial of service, buffer overflow, etc.)
   	 - Instructions to reproduce the vulnerability
   	 - Proof-of-concept or exploit code
   	 - Potential impact of the vulnerability, including how an attacker could exploit the vulnerability

While NVIDIA currently does not have a bug bounty program, we do offer acknowledgement when an externally reported security issue is addressed under our coordinated vulnerability disclosure policy. Please visit our [Product Security Incident Response Team (PSIRT)](https://www.nvidia.com/en-us/security/psirt-policies/) policies page for more information.

## Coordinated Disclosure

PSIRT owns triage, severity assessment, and the disclosure date for reports filed through the channels above. What that means in this repository, while a report is under embargo:

- Maintainers will not discuss the report in public issues, pull requests, or discussions.
- A fix will not be merged with a commit message, pull request description, or test name that reveals the vulnerability ahead of the coordinated date.
- No release will be tagged or announced in a way that advertises the issue before PSIRT publishes.
- The patched release and the advisory ship together on the coordinated date, and the `CHANGELOG.md` entry links to the advisory.
- Reporters who ask to remain anonymous are credited as "an anonymous reporter" rather than by name.

## NVIDIA Product Security

For all security-related concerns, please visit NVIDIA's Product Security portal at https://www.nvidia.com/en-us/security

## Supported Versions

NodeWright releases its components independently, each following [Semantic Versioning](https://semver.org/). Security fixes are released against the latest minor of each component. Critical fixes may be backported to the most recent prior release branch (`release/v{MAJOR.MINOR}.x`), at the maintainers' discretion; see [docs/operations/versioning.md](docs/operations/versioning.md).

| Component | Tag prefix | Supported |
|---|---|---|
| Operator | `operator/v` | Latest minor |
| Helm chart | `chart/v` | Latest minor |
| Agent | `agent/v` | Latest minor |
| CLI (`kubectl nodewright`) | `cli/v` | Latest minor |

The current version of each line is whatever the newest tag with that prefix says; this file deliberately does not restate it.

Older minors are not patched. If you are running one, upgrade to the latest release of that component. The full list is on the [releases page](https://github.com/NVIDIA/nodewright/releases).

Kubernetes version support is a separate policy: we CI-test and support the **latest four Kubernetes minor versions**. See [docs/operations/kubernetes-support.md](docs/operations/kubernetes-support.md) for the current window and what happens when it moves.

## Verifying Release Artifacts

Every released container image and the Helm chart is signed with [Sigstore cosign](https://docs.sigstore.dev/) in keyless mode, carries [SLSA build provenance](https://slsa.dev/), and has a CycloneDX SBOM attached as an attestation. The container images additionally carry an [OpenVEX](https://openvex.dev/) document. Each signing job verifies its own output before finishing.

**Each artifact is signed by the workflow that builds it, gated on its own tag family, so the certificate identity differs per artifact.** A single identity pattern will not verify everything:

| Artifact | Signing workflow | Tag family |
|---|---|---|
| Operator image | `.github/workflows/operator-ci.yaml` | `refs/tags/operator/` |
| Agent image | `.github/workflows/agent-ci.yaml` | `refs/tags/agent/` |
| Helm chart | `.github/workflows/release.yml` | `refs/tags/chart/` |

Verify by digest, not by tag. A tag can be repointed between the moment you verify it and the moment you pull it; a digest cannot. This is also what the signing workflows themselves do.

### Which digest carries which evidence

The container images are multi-platform. The tag resolves to an **index**, and that index has one **platform manifest** per architecture (`linux/amd64`, `linux/arm64`). Each piece of evidence is attached to the subject it is actually true about, so for an image the evidence is split across two kinds of digest, and no single digest carries all four checks:

| Evidence | Subject | cosign invocation |
|---|---|---|
| Signature | Index digest | `cosign verify` |
| SLSA build provenance | Index digest | `cosign verify-attestation --type https://slsa.dev/provenance/v1` |
| CycloneDX SBOM | Each platform manifest digest | `cosign verify-attestation --type cyclonedx` |
| OpenVEX | Each platform manifest digest | `cosign verify-attestation --type openvex` |

A signature and a provenance statement describe the artifact as a whole, and the index is what you pull, so they belong on the index. An SBOM and a VEX document each describe exactly one root filesystem, and the amd64 and arm64 images do not share one, so an SBOM attached to the index would describe neither child truthfully.

The practical consequence: **verifying the signature against a platform digest fails, and looking for the SBOM on the index digest finds nothing.** Both are expected. Resolve both kinds of digest and point each check at the right one.

The OpenVEX document may legitimately contain zero statements. An empty `statements` array asserts that the maintainers claim no exceptions to what a scanner reports; it does not assert that the image is free of vulnerabilities. A non-empty document lists the vulnerabilities the maintainers have assessed, each with its status and justification, bound to the platform manifest it covers.

### Verification commands

These commands assume **cosign v3**, which is what the release workflows install. On v3 `--new-bundle-format` already defaults to `true`, so you do not need to pass it. On an older cosign, add `--new-bundle-format=true` to every `verify` and `verify-attestation` call below, or upgrade.

```bash
REPO=ghcr.io/nvidia/nodewright/operator
ISSUER=https://token.actions.githubusercontent.com
# Operator image: pins the signing workflow AND the operator tag family.
# Swap both per the table above to verify the chart or the agent instead.
IDENTITY='^https://github\.com/NVIDIA/nodewright/\.github/workflows/operator-ci\.yaml@refs/tags/operator/.*$'

# Resolve the tag to an immutable index digest once
INDEX=$(crane digest "$REPO:<tag>")

# Signature (keyless, GitHub Actions OIDC identity): index digest
cosign verify --certificate-oidc-issuer="$ISSUER" \
  --certificate-identity-regexp="$IDENTITY" "$REPO@$INDEX"

# SLSA build provenance: index digest
cosign verify-attestation --type https://slsa.dev/provenance/v1 \
  --certificate-oidc-issuer="$ISSUER" \
  --certificate-identity-regexp="$IDENTITY" "$REPO@$INDEX"

# SBOM and VEX: one platform manifest digest per architecture
for PLATFORM in linux/amd64 linux/arm64; do
  PLATFORM_DIGEST=$(crane digest --platform "$PLATFORM" "$REPO:<tag>")

  cosign verify-attestation --type cyclonedx \
    --certificate-oidc-issuer="$ISSUER" \
    --certificate-identity-regexp="$IDENTITY" "$REPO@$PLATFORM_DIGEST"

  cosign verify-attestation --type openvex \
    --certificate-oidc-issuer="$ISSUER" \
    --certificate-identity-regexp="$IDENTITY" "$REPO@$PLATFORM_DIGEST"
done
```

These are the same checks the signing workflow runs against its own output before it finishes. Pin the **index** digest you verified in your Helm values or image reference, so the artifact you checked is the artifact that runs. The platform digests are for verification only: pinning one would tie your deployment to a single architecture.

### Resolving digests without crane

`docker buildx imagetools inspect` resolves both kinds of digest. The index digest:

```bash
INDEX=$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$REPO:<tag>")
```

The platform manifest digests are listed under `Manifests:` in the plain `docker buildx imagetools inspect "$REPO:<tag>"` output, one entry per `Platform:`; take the `Name:` digest of the platform you want. For scripting, read them out of the raw index instead:

```bash
docker buildx imagetools inspect --raw "$REPO:<tag>" \
  | jq -r '.manifests[] | select(.platform.os == "linux") | "\(.platform.architecture) \(.digest)"'
```

### Helm chart

The chart is verified against a **single** digest, because it is one OCI artifact with no platform children: there is nothing to split, so its signature, CycloneDX SBOM and SLSA provenance all hang on that one digest, exactly as they did before the images were split. Do not "fix" the chart instructions to chase platform digests; there are none to chase.

```bash
CHART=ghcr.io/nvidia/nodewright/charts/nodewright
ISSUER=https://token.actions.githubusercontent.com
IDENTITY='^https://github\.com/NVIDIA/nodewright/\.github/workflows/release\.yml@refs/tags/chart/.*$'

DIGEST=$(crane digest "$CHART:<tag>")
SUBJECT="$CHART@$DIGEST"

cosign verify --certificate-oidc-issuer="$ISSUER" \
  --certificate-identity-regexp="$IDENTITY" "$SUBJECT"

cosign verify-attestation --type cyclonedx \
  --certificate-oidc-issuer="$ISSUER" \
  --certificate-identity-regexp="$IDENTITY" "$SUBJECT"

cosign verify-attestation --type https://slsa.dev/provenance/v1 \
  --certificate-oidc-issuer="$ISSUER" \
  --certificate-identity-regexp="$IDENTITY" "$SUBJECT"
```

The identity regexp is deliberately narrow: it pins the signer to one workflow and one tag family in this repository, not merely to the NVIDIA organization. Loosening it to `refs/tags/` would let a signature produced by any other release path satisfy the check. Swap the workflow and tag family per the table above when verifying the chart or the agent.

Artifacts released before the Skyhook to NodeWright repository rename carry a `NVIDIA/skyhook` certificate identity; substitute that path when verifying older releases.

A verification failure on a published artifact is itself a security report. Route it through the channels above.
