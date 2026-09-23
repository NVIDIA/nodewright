<!--
  SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
  SPDX-License-Identifier: Apache-2.0
-->

# Contributing

Want to contribute to Skyhook (NodeWright)? We welcome bug reports, feature requests, and pull requests.

## Code of Conduct

This project is governed by the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating you are expected to uphold it. Report unacceptable behavior to GitHub_Conduct@nvidia.com. See [Community standards](#community-standards) for how reports are handled.

## Governance

Maintainers, decision-making, and the process for becoming a maintainer are documented in [GOVERNANCE.md](GOVERNANCE.md) and [MAINTAINERS.md](MAINTAINERS.md). Path-level review ownership is in [`.github/CODEOWNERS`](.github/CODEOWNERS).

## Filing Issues

- **Bug reports**: Use the [bug report template](https://github.com/NVIDIA/nodewright/issues/new?template=bug_report_form.yml).
- **Feature requests**: Use the [feature request template](https://github.com/NVIDIA/nodewright/issues/new?template=feature_request_form.yml).
- **Questions**: Use [GitHub Discussions](https://github.com/NVIDIA/nodewright/discussions).
- **Security vulnerabilities**: Do **not** file a public issue. See [SECURITY.md](SECURITY.md).

## Claiming an Issue

Want to work on an issue? Claim it so others know it is taken. Comment on the issue and a bot will handle the assignment:

- `/assign`: assign the issue to yourself (only if it is currently unassigned).
- `/assign @user`: assign one other person (only if the issue is unassigned).
- `/unassign`: release your claim on the issue.

We use a single-owner model: an issue is assigned to at most one person. `/assign` is refused while the issue already has an assignee, and `/unassign` only ever removes your own claim, so nobody can drop someone else's. GitHub only lets you assign the commenter/self, someone who has commented on the issue, a user with write access, or an org member with read access; if a requested user cannot be assigned, the bot replies to say so.

## Pull Requests

### Open an issue first

Non-trivial changes start with an issue, not a pull request. Open one, or find an existing one and claim it with `/assign`. The issue is where we confirm the change is wanted, agree on an approach, and tell you if something similar is already in flight — all of which are cheaper to sort out before you have a branch. A pull request that arrives with no linked issue may be closed and asked to start as one.

Two exceptions:

- **Trivial changes** — typo and formatting fixes, broken links, comment corrections — can go straight to a pull request.
- **Security vulnerabilities never start with a public issue.** Report them through [SECURITY.md](SECURITY.md) instead. PSIRT owns triage and the disclosure date and will coordinate the fix with you, including the pull request itself, so that nothing reveals the issue before the embargo lifts.

Reference the issue in your PR description (`closes #1234`) so it closes on merge. Note that this ONLY applies to public issues and PRs closing security MUST NOT reference the non-public security issue.

1. Fork the repository and create a branch from `main`.
2. Make your changes, then run the tests and linters locally. This is a requirement, not a suggestion; see [Running the CI checks locally](#running-the-ci-checks-locally) for the commands and for why running them yourself is faster than waiting on CI.
3. Run `make fmt` to format code and add license headers.
4. When you bump a Go dependency, run `make notices` and commit the refreshed `THIRD_PARTY_NOTICES.md` files alongside your change. See [`docs/contributing/release-process.md`](docs/contributing/release-process.md) for the workflow.
5. Commit with a [Conventional Commits](https://www.conventionalcommits.org/) message, signed and signed off: `git commit -s -S` (see [Developer Certificate of Origin and commit signing](#developer-certificate-of-origin-and-commit-signing)).
6. Open a pull request against `main`. The PR template will guide you through the checklist.

### Running the CI checks locally

**Use the Makefile.** CI does: the operator workflow runs `make $MAKE_TARGETS` from `operator/`, and its unit lane is literally `vet lint unit-tests`. Running `make vet lint unit-tests` on your machine runs the gate, not an approximation of it. Run all three: `make unit-tests` on its own does not lint, and only `make test` (the heavy full suite) pulls in `fmt vet lint` for you. If your change touches no code, none of this applies; say so in the pull request and move on.

The targets also do a lot of work you would otherwise have to remember: they pass `-mod=vendor`, apply license headers, sequence CRD, deepcopy and mock generation, and install the tooling they need (envtest, ginkgo, golangci-lint) into `operator/bin/`, so a missing tool is never a reason to skip a suite. Raw `go test`, `go build` and `golangci-lint run` skip some of that, which is how you end up debugging a failure that has nothing to do with your change. `make help` lists what is available.

On a fork this is also the fast path rather than the slow one. Workflow runs from a fork wait for a maintainer to approve them by hand, so a round trip through CI costs you hours where the same checks locally cost you minutes.

Run these from the component directory you touched:

```bash
# Operator / CLI (from operator/)
make vet lint unit-tests # exactly what CI's unit lane runs
make unit-tests          # ginkgo unit tests + envtest — the fast inner loop
make lint                # golangci-lint + license check
make fmt                 # gofmt + license headers; CI fails if this leaves a diff
make test                # the full suite: unit + e2e + cli-e2e + helm + operator-agent

# Agent (from agent/)
make test lint           # exactly what CI's agent lanes run
make test                # ginkgo unit tests with coverage
make lint                # golangci-lint + license check
make fmt                 # gofmt + license headers

# Repo-wide (from the root)
make license-header-check   # the same gate CI runs
```

`make unit-tests` and `make lint` need nothing beyond Go and the Makefile. The e2e suites are the exception, and they need two things you install yourself: a working `kind` (pinned in [`operator/versions.yaml`](operator/versions.yaml)) and a running container runtime. `DOCKER_CMD` defaults to `docker`; pass `DOCKER_CMD=podman` if that is what you run. The Makefile downloads ctlptl and chainsaw, but not those.

`make test` in `operator/` is heavy — it runs four flavors of e2e and expects that cluster. It also needs `AGENT_IMAGE` set to the agent image pinned in `chart/values.yaml`, which is what CI does: the Makefile defaults it to an agentless image that passes every agent test without executing anything, so a bare `make test` silently skips the agent contract. For iteration, `make unit-tests` is usually what you want; let CI run the rest.

### Dependency updates

Renovate owns Go module, Go toolchain, and container dependency updates. Dependabot owns GitHub Actions updates because the token used by the self-hosted Renovate workflow cannot modify workflow files.

The `go` directives in `operator/go.mod` and `agent/go.mod` are the source of truth for the toolchain used by CI and release builds. Renovate updates both directives in one standalone pull request, then runs `go mod tidy`, `go mod vendor`, and `make notices` so vendored builds and third-party notices remain reproducible.

Before changing `.github/renovate.json5`, run `make renovate-config-check`. The Renovate workflow can also be dispatched manually with `dryRun` enabled to inspect proposed updates without creating branches or pull requests.

Renovate uses the repository's `GITHUB_TOKEN`, not a PAT or GitHub App. GitHub therefore holds CI runs from Renovate-created pull requests for maintainer approval before they start.

### How your pull request gets reviewed

- **Reviewers are assigned automatically** from [`.github/CODEOWNERS`](.github/CODEOWNERS) based on the paths you touched. You do not need to find a reviewer yourself.
- **One approval from a code owner** for the affected paths is enough for most changes. Changes to a public contract — a CRD field, CLI flag, annotation, metric, or lifecycle semantics — additionally require approval from a maintainer who did not author the change.
- **All required checks must pass** before merge, and the branch must be up to date with `main`. Every gating workflow publishes a check named `ci-gate`; GitHub composes them into a single required status.
- **Decisions are made by lazy consensus.** A change is accepted if no maintainer raises a blocking objection within a reasonable review window — at least five business days for non-trivial changes. A maintainer blocking a change must give a concrete technical rationale and an actionable path forward. The full process, including how to escalate a disagreement, is in [GOVERNANCE.md](GOVERNANCE.md#decision-making).
- **If your PR goes quiet**, comment on it — a ping is welcome and is the fastest way to get it moving. A bot also nudges the author on PRs with no activity for 7 days.

Review is a conversation, not a gate to get past. If you disagree with a review comment, say so and explain why; reviewers are expected to engage with the reasoning rather than insist.

### Stay with your pull request

Opening the pull request is the start of the work, not the end of it. Every one costs a maintainer time they do not get back: someone reads the change, thinks about it, and writes a review. The most useful thing you can do is make that time count. Answer review comments, rebase when asked, and say something if you get stuck or lose interest in a change. A pull request that is opened and abandoned is worse than one that was never opened, because the review still happened.

If it goes quiet from our side, ping it. If it goes quiet from yours, a bot nudges you after 7 days of inactivity, the pull request is marked stale at 14 days, and it is closed 7 days after that. Closing is not a judgment on the change and you can reopen at any time; it keeps the queue honest about what is actually moving.

The same consideration applies to how many you open at once. A few pull requests you are actively shepherding through review will land sooner than a queue that neither you nor the maintainers can keep up with, and a long queue makes the changes that matter harder to find. If you have a batch of changes in mind, get the first few merged before opening the rest.

### AI-Assisted Contributions Policy

We welcome the use of AI tools (e.g., Claude, GitHub Copilot, ChatGPT) to help you write code, brainstorm, or refactor. However, we maintain a strict human-in-the-loop policy for all submissions:

- **Full accountability**: By submitting a PR, you (the human author) accept full responsibility for the code: its correctness, security, maintainability, and license compliance. "The AI wrote it" is not an acceptable explanation for bugs or security flaws.
- **Understand what you submit**: Do not submit AI-generated code you do not fully understand. Reviewers expect you to explain and defend every line of code in your PR.
- **Follow the project rules**: Coding assistants must follow the guidance in [`.claude/CLAUDE.md`](.claude/CLAUDE.md) (symlinked as [`AGENTS.md`](AGENTS.md)), including running `make fmt`, `make test`, and keeping `docs/` in sync.
- **Say so, and say what you verified**: If an AI tool wrote a meaningful part of the change, note that in the pull request, and state which tests and linters you ran and which you could not. Both belong in the pull request body, not in a reply after someone asks. We are not trying to discourage the tooling; we are trying to know how much of the verification burden has already been carried, because a change nobody has run is a change the reviewer has to run.

## Extending NodeWright

Most new host behavior does **not** require changing the operator. NodeWright's primary extension point is a **package**: a container image the operator runs on each selected node, whose steps the agent executes through the lifecycle stages (uninstall, upgrade, apply, config, interrupt, post-interrupt).

### Writing a package

1. **Start with an existing generalist package.** [`NVIDIA/nodewright-packages`](https://github.com/NVIDIA/nodewright-packages) publishes ready-made ones. The `shellscript` package runs commands you supply in the CR's `configMap` — no image build at all. [`examples/simple/scr.yaml`](examples/simple/scr.yaml) is a working example.
2. **Build your own when you need more.** A package image ships its step scripts plus a `/skyhook-package/config.json` that the agent validates and dispatches on. The agent runs steps inside the host root mount and writes completion flags so finished stages are skipped on re-run.
3. **Version it with SemVer.** This is not cosmetic: the operator compares package versions to decide upgrade vs. downgrade vs. fresh apply, so a package that misreports its version will take the wrong lifecycle path.
4. **Consume secrets at runtime**, never baked into the image — see [`docs/user-guide/providing-secrets.md`](docs/user-guide/providing-secrets.md).

Package *contents* live with their authors rather than in this repository — see [Project Scope](GOVERNANCE.md#project-scope).

### Extension points inside this repository

If you are changing NodeWright itself rather than writing a package:

- **A new interrupt type** implements the agent's `Interrupt` contract — `Type` for the wire identity, `Run` for execution against an `execution.Config`, `Serialize` for the operator-facing form. Retry state and completion flags stay outside the interrupt. See the execution-contract notes in [`agent/README.md`](agent/README.md).
- **A new CLI subcommand** goes under `operator/cmd/cli/app/` and talks to the cluster through `operator/internal/cli/client`, not the apiserver directly. Anything version-gated must also land in the compatibility matrix in [`docs/user-guide/cli.md`](docs/user-guide/cli.md).
- **A new CRD field** requires `make manifests generate`, mirrored edits in both `operator/config/` and `chart/`, and a docs update in the same PR.

In every case, find the nearest existing example and match it. A new pattern that has no precedent in the target package should be called out explicitly in your PR description — what you introduced, why the existing patterns didn't fit, and why it should become the convention.

## Developer Certificate of Origin and commit signing

Every commit, from every contributor and maintainers included, must be both **signed off** and **cryptographically signed**:

```bash
git commit -s -S -m "your message"
```

- `-s` (lowercase) adds a `Signed-off-by` trailer, certifying the [Developer Certificate of Origin 1.1](#what-you-are-certifying) reproduced below.
- `-S` (uppercase) attaches a GPG or SSH signature, proving the commit came from you.

The two are independent, and neither implies the other. Use both, on every commit. A branch ruleset enforces **Require signed commits** on `main`, and the [Commit Requirements](.github/workflows/commit-requirements.yaml) workflow comments on any pull request carrying a commit that is missing either one.

The `Signed-off-by` trailer should name the commit author, and must be a real name:

```
Signed-off-by: Joe Smith <joe.smith@email.com>
```

Sorry, no pseudonyms or anonymous contributions.

What the automated check actually verifies is narrower than the rule above: for each commit not authored by a bot, that the signature verified, and that a `Signed-off-by` trailer is present unless the commit is a merge. Merge commits are exempt from sign-off because GitHub's "Update branch" and merge buttons author them with no trailer and no way to add one, and a merge introduces no new authorship to certify; they still have to be signed. It does not compare the trailer's address to the author's, because GitHub's `NNN+user@users.noreply.github.com` aliases make that mismatch common and harmless. Reviewers still enforce the rest.

### One-time setup

Do this once and `-S` becomes automatic, so the only flag you have to remember is `-s`.

Your commit identity, which is what lands in the `Signed-off-by` trailer:

```bash
git config user.name "Your Name"
git config user.email "your.email@example.com"
```

Use an address GitHub has verified on your account, otherwise GitHub will not mark the commit as verified. This is separate from your signing key and does not have to match it.

Then a signing key, which GitHub must also know about. Follow GitHub's guide to [generate a GPG or SSH signing key and add it to your account](https://docs.github.com/en/authentication/managing-commit-signature-verification), then point git at it. For SSH:

```bash
git config gpg.format ssh
git config user.signingkey ~/.ssh/id_ed25519.pub   # the public key, not the private one
```

For GPG, leave `gpg.format` unset and use the key ID from `gpg --list-secret-keys --keyid-format=long`:

```bash
git config user.signingkey <key-id>
```

Finally, sign every commit without having to remember `-S`:

```bash
git config commit.gpgsign true
```

### Forgot to sign or sign off?

Fix the most recent commit and re-push:

```bash
git commit --amend -s -S --no-edit
git push --force-with-lease origin your-branch
```

Fix every commit on the branch at once. Check first whether the branch contains merge commits, because a plain rebase drops them and takes any merge-only content with them:

```bash
git log --oneline --merges origin/main..HEAD   # empty output means the branch is linear
```

For a linear branch:

```bash
git rebase --exec 'git commit --amend -s -S --no-edit' origin/main
```

For a branch with merge commits, preserve the topology:

```bash
git rebase --rebase-merges --exec 'git commit --amend -s -S --no-edit' origin/main
```

Either way, confirm the rewrite changed nothing but the signatures before you push:

```bash
git range-diff @{u}...HEAD
git push --force-with-lease origin your-branch
```

Re-signing rewrites every commit, which outdates the inline comments reviewers have already left. If the pull request is already under review, say on it that you force-pushed.

### What you are certifying

By signing off you certify the following, from [developercertificate.org](https://developercertificate.org/):

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.
1 Letterman Drive
Suite D4700
San Francisco, CA, 94129

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.

Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

## Code Style

We use [Conventional Commits](https://www.conventionalcommits.org/) for our commit messages.

### Go (Operator / CLI / Agent)

We use [gofmt](https://pkg.go.dev/cmd/gofmt) for Golang code style.
For testing, we use [Ginkgo](https://github.com/onsi/ginkgo) and [Gomega](https://github.com/onsi/gomega).

## License Header

We use `scripts/format_license.py` to add the license header to the code.

```bash
make license-fmt
```

This adds the license header based on the LICENSE file and removes/replaces any existing header. Component Makefiles also run this automatically via `make fmt`.

## Community standards

We enforce the [Code of Conduct](CODE_OF_CONDUCT.md) in every project space: issues, pull requests, discussions, and any venue where someone is representing NodeWright.

### Reporting

Send reports to GitHub_Conduct@nvidia.com. Include what happened, where, when, and links if the incident is public. You do not need to be the target of the behavior to report it.

### Response timeline

- **Acknowledgement within 3 business days.** You get a confirmation that the report was received and who is handling it.
- **Resolution within 14 business days** for most reports. If an investigation needs longer, we tell you that before day 14 and give an updated estimate.
- **Immediate action** for ongoing harassment, threats, or doxxing, ahead of the full investigation.

Reporter identity is shared only with the people investigating. Outcomes follow the [Enforcement Guidelines](CODE_OF_CONDUCT.md#enforcement-guidelines) ladder: correction, warning, temporary ban, permanent ban.

### Out of scope

The following are handled elsewhere, not through a conduct report:

- **Security vulnerabilities**: see [SECURITY.md](SECURITY.md). Do not file a public issue.
- **Technical disagreements**, including rejected pull requests and design decisions you disagree with. Escalate through the process in [GOVERNANCE.md](GOVERNANCE.md#decision-making).
- **Conduct in venues unrelated to NodeWright**, unless it creates a credible safety risk for someone in this community.
- **NVIDIA employment or HR matters**, which go through NVIDIA's internal channels.
