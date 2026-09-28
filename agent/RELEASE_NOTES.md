# Release Notes

Human-authored highlights, behavior changes, and upgrade steps for the agent.
For the full commit-level log see CHANGELOG.md.

## Unreleased

### Behavior Changes

- **The CycloneDX SBOM moved from the multi-platform index digest to each platform manifest digest, and an OpenVEX document is now published alongside it.** An SBOM describes exactly one root filesystem, so one attached to a multi-platform index described neither child truthfully, and a consumer who resolved `linux/amd64` and enumerated referrers on that manifest found nothing.

  **You will notice this if you verify release artifacts.** `cosign verify-attestation --type cyclonedx <index-digest>` was the documented command and now finds nothing on this release. Resolve the platform manifest and verify against that instead:

  ```bash
  INDEX=$(crane digest "$REPO:$TAG")
  PLATFORM_DIGEST=$(crane digest --platform linux/amd64 "$REPO@$INDEX")
  cosign verify-attestation --type cyclonedx ... "$REPO@$PLATFORM_DIGEST"
  cosign verify-attestation --type openvex   ... "$REPO@$PLATFORM_DIGEST"
  ```

  The signature and the SLSA provenance have not moved: both still verify against the index digest, which is what a tag resolves to and what you pull. Tags published before this release keep the old layout, with the SBOM on the index and no OpenVEX document. `SECURITY.md` carries the full recipe.
