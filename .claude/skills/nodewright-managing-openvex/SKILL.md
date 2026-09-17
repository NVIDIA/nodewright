---
name: nodewright-managing-openvex
description: |
  Triage findings from the Image Vulnerability Scan workflow and maintain
  `.openvex.json`, the repository's OpenVEX v0.2.0 document. Use when asked to
  suppress a CVE, mark a finding not affected, write or edit a VEX statement,
  review Grype code scanning alerts on the operator or agent image, or work out
  why a suppression did not take effect. Suppression happens in `.openvex.json`
  and never by dismissing a code scanning alert, because #607 will publish this
  document as signed evidence attested onto every released image.
user-invocable: true
version: 0.1.0
---

# Managing `.openvex.json`

Operating instructions for triaging container image vulnerability findings and
maintaining the repository's OpenVEX document.

## Files this skill owns

| Path | Role |
| --- | --- |
| `.openvex.json` | The OpenVEX v0.2.0 document. The **only** suppression mechanism. Currently `statements: []`. |
| `.grype.yaml` | Scan config: path excludes, plus an `ignore:` list that is **not** for suppression (see below). |
| `.github/workflows/vuln-scan-images.yaml` | Weekly Thursday 09:00 UTC scan of the published `:latest` operator and agent images; uploads SARIF under categories `grype-operator` and `grype-agent`. Never fails a build. |
| `.claude/skills/nodewright-managing-openvex/vex-state.yaml` | Working notes and open verifications for this skill. |

## Step 1 (always first): read `vex-state.yaml`

Read `.claude/skills/nodewright-managing-openvex/vex-state.yaml` before doing
anything else. It records checks that could not finish in an earlier session,
negative results nobody should repeat, and decisions not to act. Doing the
triage without it means redoing work that was already done, or silently
reversing a decision someone made deliberately.

## Step 2: suppress in `.openvex.json`, never in the Security tab

Do **not** dismiss a code scanning alert to make a finding go away.

Grype applies `.openvex.json` at scan time through the workflow's `vex:` input,
so a suppressed finding never becomes an alert in the first place. A UI
dismissal is invisible to the document, so the two drift apart. That matters
beyond tidiness: #607 will publish `.openvex.json` as *signed* evidence attested
onto every released image. That has not shipped yet, so nothing consumers can
fetch carries this document today. Once it does, a suppression living only in
the GitHub UI makes the signed artifact the wrong one, and the signed artifact
is the one consumers read.

`.grype.yaml`'s `ignore:` list is not the alternative. It is reserved for the
opposite case: a finding that **is** reachable in our code and has no fixed
version upstream yet, which is accepted risk rather than "not affected". Its own
header states the four things every entry must record. If you are reaching for
it, reread that header first.

## Step 3: check `main` before writing any statement

The workflow scans the moving `:latest` tag on purpose, because the subject of
the scan is the artifact users actually pull. That artifact lags `main`.

So a finding can mean either of two very different things:

1. **Unfixed.** The dependency is still vulnerable on `main`. Fix it, or write a
   VEX statement if we are genuinely not affected.
2. **Already fixed on `main`, not yet released.** Nothing is wrong with the code.
   The remedy is cutting a release.

Issue #629 is exactly case 2: the released operator image reports 2 HIGH that
`operator/go.mod` already fixed. Writing a VEX statement for that would be
false. The document would assert we are not affected when we simply have not
shipped the fix.

Check before writing:

Which file answers the question depends on where the package came from, and for
these images most findings are **not** direct dependencies. Check the artifact
type in the scan output first (`.matches[].artifact.type`) and follow it:

```bash
# Go modules (artifact.type == "go-module"): operator, and the Go agent.
grep -n '<module-path>' operator/go.mod agent/go/go.mod
git log --oneline -5 -- operator/go.mod agent/go/go.mod

# Python packages (artifact.type == "python") in the agent venv.
grep -rn '<package-name>' agent/skyhook-agent/pyproject.toml agent/vendor/

# deb packages and the CPython binary (artifact.type == "deb" or "binary") come
# from the base image, not from our source. Nothing in this repo pins their
# versions: `scripts/latest-distroless.sh` resolves the newest base at build
# time, so "is it fixed on main?" means "does a newer base carry the fix?".
grep -n 'DISTROLESS_VERSION\|FROM nvcr.io' containers/agent.Dockerfile containers/operator.Dockerfile
```

For a base-image finding, compare the base directly rather than guessing. Scan
the base tag the released image was built on (its
`org.opencontainers.image.base.name` label) against the newest available tag. If
the newer base clears it, the remedy is a rebuild and release, not a statement.
That is #628.

If `main` already carries the fix, stop. Record the finding on the release
issue, not in `.openvex.json`.

## Step 4: get the vulnerability ID right

`vulnerability.name` in a statement must equal grype's **primary** ID, the
`.matches[].vulnerability.id` field. For ecosystem advisories (Go, PyPI, npm)
that is usually the **GHSA**, with the CVE appearing only under
`relatedVulnerabilities`.

OpenVEX matches by exact string, across `@id`, `name`, and `aliases`. A statement
naming only a CVE will not match a finding whose primary ID is a GHSA: it will
look correct and suppress nothing. Read the alias warning below before reaching
for `aliases` to fix that.

Pull the primary ID and its aliases out of a scan:

```bash
GRYPE_DB_VALIDATE_AGE=false GRYPE_CHECK_FOR_APP_UPDATE=false \
  grype ghcr.io/nvidia/nodewright/operator:latest -o json \
  | jq -r '.matches[]
      | select(.vulnerability.severity | ascii_downcase | IN("high","critical"))
      | "\(.vulnerability.id)\t\(.vulnerability.severity)\t\(.artifact.name)@\(.artifact.version)\taliases=\([.relatedVulnerabilities[]?.id] | join(","))"'
```

The first column is what goes in `vulnerability.name`.

**`aliases` is also a match key, so do not treat it as searchable decoration.**
go-vex matches a statement against a finding by checking `@id`, `name`, and
every entry in `aliases`. Adding the CVE alongside a GHSA therefore widens the
statement to any *other* finding in the image whose primary ID is that CVE, in
a package you never reasoned about. That is live here: in the agent image most
HIGH+ findings are Debian packages whose primary ID is a CVE, while the Go and
PyPI ones are GHSA-primary with CVE aliases. List an alias only when you mean
the statement to cover it.

Narrow the blast radius with `subcomponents` when a statement is about one
package rather than the whole image. Grype passes the matched package's purl as
the subcomponent identifier, so a product with no `subcomponents` matches the
entire image for that vulnerability ID.

`GRYPE_DB_VALIDATE_AGE=false GRYPE_CHECK_FOR_APP_UPDATE=false` is a **local-only
workaround**: this sandbox blocks grype's database and update hosts, so without
it grype refuses a stale local DB. It must never appear in a workflow. CI has
network access and a stale database there would silently under-report.

## Step 5: get the product PURL right

Grype derives the OCI product PURL from the **registry repository basename**,
not from the `org.opencontainers.image.title` label. For these images that makes
the expected values:

- `pkg:oci/operator`
- `pkg:oci/agent`

`pkg:oci/operator` is **verified**: a test statement carrying it dropped the
image's HIGH+ count from 2 to 1. `pkg:oci/agent` is inferred rather than
measured, but follows by the same code path: grype derives
`pkg:oci/<basename>@sha256:...?repository_url=...` from the image's
`RepoDigests`, and a version-less, qualifier-less purl matches it.

A mismatched PURL silently suppresses nothing while looking perfectly correct in
review, which is the worst failure mode this document has.

Carry the PURL in **both** `@id` and `identifiers.purl` on every product, with
the same value. OpenVEX v0.2.0 has no `products[].purl` field: a product is a
Component carrying `@id`, `identifiers`, and `hashes`, so the purl lives at
`identifiers.purl`. Grype will in practice also match a `@id` that begins with
`pkg:`, so writing both is belt and braces rather than strictly required, but it
is what the sibling project's working document does on every statement and it
costs nothing.

**Every statement must be verified empirically before it is merged, not just
the first one.** This is not a one-time bootstrap. A mistyped `status` is the
clearest reason why: `"not-affected"` with a hyphen instead of an underscore
makes grype exit 0 with no warning and suppress nothing. So do a wrong purl, a
CVE where the primary ID is a GHSA, and a typo'd package name. None of them
produce an error; all of them produce a document that reads correctly and does
nothing.

The measurement is the only thing that distinguishes a working statement from a
decorative one. Do not verify by reading; verify by measuring the HIGH+ count
with and without `--vex` against the same image:

```bash
IMG=ghcr.io/nvidia/nodewright/operator:latest
COUNT='[.matches[] | select(.vulnerability.severity | ascii_downcase | IN("high","critical"))] | length'

before=$(GRYPE_DB_VALIDATE_AGE=false GRYPE_CHECK_FOR_APP_UPDATE=false \
  grype "$IMG" -o json | jq "$COUNT")
after=$(GRYPE_DB_VALIDATE_AGE=false GRYPE_CHECK_FOR_APP_UPDATE=false \
  grype "$IMG" --vex .openvex.json -o json | jq "$COUNT")

echo "before=$before after=$after"
```

`after` must be lower than `before` by exactly the number of findings the new
statement covers. If the counts are identical, the statement matched nothing:
suspect the PURL first, then the vulnerability ID. Do not merge a statement that
has not moved the count.

If the count does not move, the statement is decorative. Do not merge it.

## Step 6: write the statement to the v0.2.0 contract

`status` must be one of exactly:

- `not_affected`
- `affected`
- `fixed`
- `under_investigation`

Two hard requirements:

- A `not_affected` statement **must** carry a `justification` **or** an
  `impact_statement`.
- An `affected` statement **must** carry an `action_statement`.

If a `justification` is present it must be one of exactly these five. There is
no free-text justification:

- `component_not_present`
- `vulnerable_code_not_present`
- `vulnerable_code_not_in_execute_path`
- `vulnerable_code_cannot_be_controlled_by_adversary`
- `inline_mitigations_already_exist`

If none of the five is honestly true, use an `impact_statement` and say why in
prose, or do not write the statement at all. Picking the nearest-sounding
justification is how a signed document ends up asserting something false.

Shape:

```json
{
  "vulnerability": {
    "name": "GHSA-xxxx-xxxx-xxxx",
    "aliases": ["CVE-2026-NNNNN"]
  },
  "products": [
    {
      "@id": "pkg:oci/operator",
      "identifiers": { "purl": "pkg:oci/operator" }
    }
  ],
  "status": "not_affected",
  "justification": "vulnerable_code_not_in_execute_path",
  "impact_statement": "One or two sentences naming the specific call path we do not take."
}
```

## Step 7: keep document-level fields as identifiers, not prose

`@id`, `author`, `role`, `timestamp`, `version`, and `tooling` are metadata
fields. They identify the document. They are not a place to explain reasoning.

`tooling` in particular. A sibling NVIDIA project let that field grow into an
8,010-byte changelog, which then shipped, signed, on seven release images before
anyone noticed (NVIDIA/aicr#2706). Ours reads
`manual curation (.claude/skills/nodewright-managing-openvex)` and should stay
that length.

Working notes, open questions, and rejected options go in `vex-state.yaml`.
Reasoning a reader needs goes in the statement's `impact_statement`, which is
the field designed for it. Nothing narrative goes in a document-level field.

On every change to the document:

- Bump `version` (integer, monotonic).
- Update `timestamp` to the current UTC time in RFC 3339.
- Leave `@id`, `author`, `role`, and `tooling` alone.

## Step 8: triage by severity, not by alert count

The uploaded SARIF contains **every finding with an available fix**, not only
HIGH and above. `severity-cutoff: high` in the workflow neither filters the
report nor grades it: the SARIF level is a pure function of the finding's own
severity (critical and high become `error`, medium becomes `warning`, anything
lower becomes `note`). All `severity-cutoff` does is set grype's `--fail-on`,
which is inert while `fail-build` is false. It is the knob #630 will flip, and
until then changing it has no observable effect.

Grype sets a `security-severity` (CVSS) property on each rule, so GitHub grades
alerts correctly and the Security tab can filter by severity. Use that filter.

**Triage HIGH and CRITICAL.** Low and Medium findings do not need a statement.
Writing one for every Low alert is how the document stops being reviewable.

Filter existing alerts by category and severity:

```bash
gh api -X GET repos/NVIDIA/nodewright/code-scanning/alerts \
  -f state=open -f tool_name=grype --paginate \
  | jq -r '.[]
      | select(.rule.security_severity_level | IN("high","critical"))
      | "\(.most_recent_instance.category)\t\(.rule.security_severity_level)\t\(.rule.id)"' \
  | sort | uniq -c
```

## Step 9 (always last): update `vex-state.yaml`

Before finishing, write back to
`.claude/skills/nodewright-managing-openvex/vex-state.yaml`:

- **Delete** any `deferred_verifications` entry whose check has now been run
  **and passed**. A failed check is not a resolution; it is the finding you are
  now triaging.
- **Delete** any `known_negatives` or `deliberate_exclusions` entry whose
  `invalidated_by` condition has fired. Do not rewrite it as history.
- **Add** an entry for anything this session could not finish, already ruled out,
  or deliberately chose not to do.

Every entry must state what would invalidate it. An entry that cannot state that
is not state; it is prose, and it belongs in a PR description or nowhere. The
file's header explains the lifecycle rules in full.

## Quick reference: local scan

```bash
GRYPE_DB_VALIDATE_AGE=false GRYPE_CHECK_FOR_APP_UPDATE=false \
  grype ghcr.io/nvidia/nodewright/agent:latest \
    --config .grype.yaml \
    --vex .openvex.json \
    --only-fixed \
    -o table
```

Run the workflow on demand rather than waiting for Thursday:

```bash
gh workflow run "Image Vulnerability Scan" --repo NVIDIA/nodewright
gh run list --workflow "Image Vulnerability Scan" --repo NVIDIA/nodewright --limit 3
```
