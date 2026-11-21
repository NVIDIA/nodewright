---
name: nodewright-cross-review
description: |
  Multi-agent review of a NodeWright/Skyhook change using Claude Code, Codex,
  and CodeRabbit. Runs parallel reviews with integration impact analysis, then
  one cross-review round to a 2-of-3 consensus, with every confirmed finding
  adversarially verified by a fresh agent. Reviews a PR by number, the PR for
  the current branch, or a branch that has no PR yet. Never runs the reviewed
  commit's code, and posts only when asked, as a pending draft review that a
  human submits. Use when asked for a thorough cross-review or multi-reviewer
  analysis. Requires the Codex plugin;
  CodeRabbit is best-effort. Claude Code only: uses the Workflow and Agent
  tools, which are not available in other agents.
user-invocable: true
# Reviewer lanes must not reach a skill that posts. code-review permits `gh pr comment`
# and the CodeRabbit skill auto-triggers on review tasks, so denying Skill closes the
# automatic path that removing the explicit nested call did not.
disallowed-tools: Skill
argument-hint: "[PR-number-or-URL] (omit to review the current branch)"
version: 0.1.0
---

# NodeWright Cross-Review: Multi-Agent Review with Consensus

Three reviewers (Claude Code, Codex, CodeRabbit) plus a targeted integration impact
analysis, cross-reviewed to 2-of-3 consensus, with every confirmed finding
adversarially verified by a fresh agent. Orchestration runs as a **Workflow**
(`scripts/workflow.mjs`).

**Claude Code only.** If the `Workflow` tool is unavailable, stop and say why. Do not
fall back to another review command. `/code-review` in particular posts its result to
the PR (see Phase 2), which this skill never does without an explicit request.

**When in doubt, stop.** Every check below either passes or ends the review with an
explanation. The skill never executes the reviewed commit's code, and never posts to
the PR unless you explicitly ask (Phase 5).

**Provenance.** Derived from the `aicr-cross-review` skill in `NVIDIA/aicr`, which is
where the consensus mechanics and the operational notes below were worked out. Claims
in this file marked "measured" were measured *there*, against the same CLIs on the same
machine, and have not been re-measured against this repo. The repository-specific parts
(review focus, integration surfaces, PR classification, the repo context handed to every
lane) are new here. The generic multi-mode `cross-agents:cross` plugin is a separate,
earlier lineage; this skill is not built on it.

## Input

Raw arguments: `$ARGUMENTS`

`$ARGUMENTS` is **optional**. It selects one of three modes, resolved in Phase 0:

| `$ARGUMENTS` | Mode | What is reviewed |
| --- | --- | --- |
| A PR number or URL | `pr` | That PR's head commit on `NVIDIA/nodewright` |
| Empty, current branch has a PR | `pr` | That PR's head commit |
| Empty, current branch has no PR | `local` | The committed work on the branch, against its merge-base with the PR base branch |

Validate the argument's *shape* in Phase 0 first, and reject a URL for another
repository. Then pass the validated value to `gh` **unchanged**: `gh` accepts a bare
number or a pull URL directly, so nothing needs rewriting. What is prohibited is
transforming the value, not checking it.

**Uncommitted work is never reviewed, in either mode.** Every lane reads a diff pinned to
a commit, and the CodeRabbit lane is explicitly forbidden from `-t uncommitted`; a dirty
tree would hand each lane a different view of the code. If the branch has uncommitted
changes you want reviewed, commit them first and re-run.

## Phase 0: Pre-flight and mode resolution

Only the required lanes are hard requirements. Claude, Codex and integration analysis
must work; if one fails at runtime the review reports `incomplete` and stops.
CodeRabbit is best-effort: a missing or unauthenticated CLI is not an error, its vote
slot just records `NONE`.

```bash
for tool in gh git; do
  which "$tool" >/dev/null || { echo "$tool not found — install it and retry."; exit 1; }
done
# `find`, not a bare glob. The `||` branch DOES fire on an unmatched glob under zsh, so
# the remedy below still prints — but the `2>/dev/null` cannot suppress zsh's own
# "no matches found", because that error comes from expansion and `ls` never runs to own
# the redirection. The result is a confusing shell error immediately above the real
# message, in exactly the case this check exists for. `find` prints nothing and exits 0,
# which is why the CodeRabbit lane already uses it for its own directory sweep.
[ -n "$(find ~/.claude/plugins/cache/openai-codex/codex \
          -path '*/scripts/codex-companion.mjs' -print -quit 2>/dev/null)" ] \
  || { echo "Codex companion not found. Install the Codex plugin (Settings → Extensions → Codex)."; exit 1; }
echo "Pre-flight OK."
```

If either check fails, stop and report which tool is missing. Do not fall back to
another review command.

**Resolve the mode, before anything else needs it.**

Every `gh` call and the ref fetch are scoped to `NVIDIA/nodewright` **literally**, written
out in each command. Two reasons: a contributor PR arrives from a fork, which has neither
the PR nor `refs/pull/*`; and a shell variable would not survive anyway, since each Bash
call is a fresh shell.

**Validate `$ARGUMENTS` yourself before it reaches a shell.** It is a template
placeholder the harness substitutes textually, not a shell variable, so shell quoting is
not by itself a defense: by the time the shell parses the command the value is already
part of the source text, and a value containing `;`, a backtick or `$(...)` is executed.
Accept exactly two shapes and stop on anything else:

- a bare PR number: digits only
- a pull URL **for this repository**: `https://github.com/NVIDIA/nodewright/pull/<digits>`

Reject a URL naming any other owner or repository rather than passing it through. Every
later command in this skill is scoped to `NVIDIA/nodewright` literally, so a URL for a
different project would either fail confusingly or resolve against the wrong repository.

Pass the validated value to `gh` **unchanged**. Do not normalize a URL into a number or a
number into a URL: `gh` accepts either form, and a rewrite is a transformation that can
only introduce a mistake.

Then run one of these two forms, substituting the validated value literally. Do not
build a single command with an empty argument in it: `gh pr view ""` passes an empty
positional and breaks the current-branch resolution the no-argument mode depends on.

```bash
# With a validated argument:
gh pr view <validated-value> --repo NVIDIA/nodewright \
  --json number,title,body,baseRefName,headRefName,headRefOid,files

# With no argument (resolves the PR for the checked-out branch):
gh pr view --repo NVIDIA/nodewright \
  --json number,title,body,baseRefName,headRefName,headRefOid,files
```

- **It succeeds** (with or without an argument): mode is `pr`. Take `<n>` = `.number` and
  use that numeric value for every later temp path, scoped ref name and `gh` call, never
  the raw argument. Keep the rest of the response; Phase 1 does not re-fetch it.
- **It fails and `$ARGUMENTS` was non-empty**: stop. A bad PR reference is a typo, not a
  reason to silently review something else.
- **It fails and `$ARGUMENTS` was empty**: mode is `local`. Confirm you are on a branch
  with commits of its own before continuing:

  ```bash
  git -C "<repo-path>" rev-parse --abbrev-ref HEAD    # not "HEAD" (detached) and not "main"
  git -C "<repo-path>" rev-parse HEAD
  ```

  If the branch is `main`, or detached, or has no commits beyond the base, stop and say
  so. There is nothing to review and no sensible base to diff against.

**Self-review guard.** In `pr` mode, from the `files` list just fetched: if any changed
path is under `.claude/skills/nodewright-cross-review/`, **stop**. The scripts you would
execute are the ones under review. Ask for a trusted checkout. In `local` mode, run the
same check against `git diff --name-only <base>...HEAD`. This catches the accidental case
only; `SKILL.md` lives inside the reviewed repo, so it is not a security boundary.

## Phase 1: Setup

**Batch A, one parallel message:**

1. Pin `HEAD_SHA`. In `pr` mode that is `headRefOid` from the Phase 0 response; in
   `local` mode it is the `git rev-parse HEAD` above. Every reviewer reviews this exact
   commit. `<n>` is already resolved in Phase 0; do not re-fetch.
2. Worktree hygiene: `git worktree prune`, then `git worktree list | wc -l`. If the
   count still exceeds ~15, **stop** and ask the user to clean up before retrying.
   Do not remove worktrees yourself: a clean detached-HEAD worktree may be another
   session's active review, and this repo keeps long-lived worktrees under
   `.claude/worktrees/`. (Each worktree adds sandbox deny-list paths; at ~70 the profile
   exceeded the OS spawn-arg limit and every sandboxed Bash call failed with `E2BIG`.
   Recovery needs a fresh session.)

**Batch B, after A** (needs `HEAD_SHA` and the base branch name). `gh pr diff` takes no
SHA argument, so pin the diff with `git fetch`. Refs and the diff file are
**session-scoped**: two sessions reviewing the same change must not share, overwrite, or
delete each other's pinned input.

In `pr` mode:

```bash
set -euo pipefail   # a failed fetch or diff must abort, not leave an empty diff file
BASE="<baseRefName>"                    # from Phase 0 — never hardcode "main"
DIFFPATH=$(mktemp "${TMPDIR:-/tmp}/nw-cross-review-pr<n>.XXXXXX")   # must end in X on macOS
SID=${DIFFPATH##*.}                     # reuse mktemp's unique suffix to scope the refs
PRREF="refs/cr/pr<n>-$SID"; BASEREF="refs/cr/base<n>-$SID"
# Echo the names BEFORE the first command that can fail, not merely before the diff.
# mktemp has ALREADY created the file, and a partial fetch can create $PRREF and then
# fail on $BASEREF (e.g. the base branch no longer exists on the canonical repo). Under
# `set -e` an echo placed after either one never runs, leaving a temp file and a ref
# whose random suffix nobody recorded and Phase 5 cannot clean up.
echo "DIFFPATH=$DIFFPATH"; echo "PRREF=$PRREF"; echo "BASEREF=$BASEREF"
# Fetch from the canonical repo by URL, not from `origin`: a contributor PR lives on a
# fork, and refs/pull/* exist only on the canonical repository.
git -C "<repo-path>" fetch "https://github.com/NVIDIA/nodewright.git" \
  "+refs/pull/<n>/head:$PRREF" "+refs/heads/$BASE:$BASEREF"
# Head moved → stop. Clean the refs we just created before exiting; set -e would
# otherwise abort before the names are ever printed, leaving them unreclaimable.
if [ "$(git -C "<repo-path>" rev-parse "$PRREF")" != "<HEAD_SHA>" ]; then
  git -C "<repo-path>" update-ref -d "$PRREF"; git -C "<repo-path>" update-ref -d "$BASEREF"
  rm -f "$DIFFPATH"; echo "HEAD moved since setup — restart the review"; exit 1
fi
git -C "<repo-path>" diff "$BASEREF...$PRREF" > "$DIFFPATH"
test -s "$DIFFPATH"                     # a real PR diff is never empty
# repoNotes source, pinned to the BASE ref — a fork PR must not be able to rewrite
# the instructions fed to the reviewer.
git -C "<repo-path>" show "$BASEREF":.claude/CLAUDE.md
# The area rules the root guide indexes, also from the base ref.
for f in $(git -C "<repo-path>" ls-tree --name-only "$BASEREF" .claude/rules/); do
  echo "=== $f"; git -C "<repo-path>" show "$BASEREF:$f"
done
# BASE_SHA is the base branch tip. Its only consumer is CodeRabbit's --base-commit,
# and the CLI resolves the merge-base itself, so this stays consistent with the
# three-dot diff above without a second baseline to keep in sync.
echo "BASE_SHA=$(git -C "<repo-path>" rev-parse "$BASEREF")"
```

In `local` mode the shape is identical; only the head side changes. There is no
`refs/pull/*` to fetch, so the head is the local commit and only the base is fetched.
The base is still fetched **from the canonical URL**, not from `origin`, and the notes are
still read from the fetched base ref rather than the working copy. That is deliberate: it
keeps one code path correct instead of two, and it means a local branch that has quietly
edited `.claude/CLAUDE.md` cannot rewrite the instructions its own review runs under.

```bash
set -euo pipefail
BASE="main"                             # or the branch this work is intended to merge into
DIFFPATH=$(mktemp "${TMPDIR:-/tmp}/nw-cross-review-local.XXXXXX")
SID=${DIFFPATH##*.}
BASEREF="refs/cr/base-local-$SID"
# Same rule as the pr block: echo before the fetch, not after it.
echo "DIFFPATH=$DIFFPATH"; echo "BASEREF=$BASEREF"
git -C "<repo-path>" fetch "https://github.com/NVIDIA/nodewright.git" "+refs/heads/$BASE:$BASEREF"
git -C "<repo-path>" diff "$BASEREF...<HEAD_SHA>" > "$DIFFPATH"
test -s "$DIFFPATH"                     # empty means the branch has no committed work
git -C "<repo-path>" show "$BASEREF":.claude/CLAUDE.md
# The area rules the root guide indexes, also from the base ref.
for f in $(git -C "<repo-path>" ls-tree --name-only "$BASEREF" .claude/rules/); do
  echo "=== $f"; git -C "<repo-path>" show "$BASEREF:$f"
done
echo "BASE_SHA=$(git -C "<repo-path>" rev-parse "$BASEREF")"
# Report but do not review: uncommitted changes are outside the pinned commit.
git -C "<repo-path>" status --porcelain
```

If `git status --porcelain` is non-empty, say so in the final report: the reviewers saw
the committed work only, and the dirty files are named so the reader knows what was not
covered.

Capture `DIFFPATH`, `BASE_SHA`, `BASEREF` and (in `pr` mode) `PRREF`. Shell variables do
not persist between Bash calls and Phase 5 needs the ref names.

Then build `repoNotes` for the Claude reviewer only (never fed to Codex, per the
lean-context rule): distill the base-pinned `.claude/CLAUDE.md`, the base-pinned area
rules whose `paths:` frontmatter matches a changed file, and any local overlay into 3 to 6
lines of the rules most likely to catch defects in the changed paths. The
workflow already hands every lane a standing repository-context block (components,
the partial Skyhook to NodeWright rename, the Status/State/Stage distinction, the
vendored and generated trees to skip), so `repoNotes` should carry what is specific to
*this* change, not repeat that.

Note that the repo's root `AGENTS.md` is a symlink to `.claude/CLAUDE.md`, so there is one
root file here, not two. The area rules in `.claude/rules/*.md` are regular files.

The check below reduces accidental exposure, but it is **not a trust boundary**:
reviewer subagents load the checkout's `CLAUDE.md` hierarchy automatically, before any
guard here runs. Treat `repoNotes` as a relevance digest, not a sanitiser.

**For an untrusted or fork PR, run this skill from a session started in a trusted
checkout**, the same operational remedy as the self-review guard in Phase 0. Git
overwrites *ignored* files during checkout without complaint, so checking out a fork
that force-added an ignored overlay silently replaces yours.

```bash
for f in AGENTS.local.md CLAUDE.local.md .claude/CLAUDE.local.md; do
  [ -e "<repo-path>/$f" ] || continue
  # Skip symlinks first. The tracked-status check applies to the link, not its target,
  # so an untracked symlink pointing at a PR-tracked file would otherwise be reported
  # TRUSTED while resolving to PR-controlled instructions.
  [ -L "<repo-path>/$f" ] && { echo "SKIP $f — symlink"; continue; }
  if git -C "<repo-path>" ls-files --error-unmatch -- "$f" >/dev/null 2>&1; then
    echo "SKIP $f — tracked by this change, not a trusted local overlay"
  else
    echo "TRUSTED $f"      # regular untracked file: safe to read
  fi
done
```

Read only the paths reported `TRUSTED`.

## Phase 1.5: Classify and extract the change list

**Classify** the change: `code-change` | `design-doc` | `config-change` |
`documentation-only`. `design-doc` covers `docs/designs/` and `docs/plans/`;
`config-change` covers `chart/`, `operator/config/`, `.github/workflows/` and similar
when no Go or Python behavior changes with them.

**Extract a bounded change list** so integration analysis verifies specific items
instead of fishing across a repo with two vendored trees in it:

- Exported Go functions, types, constants, and interfaces added, removed, or modified
- CRD fields and kubebuilder markers under `operator/api/`
- Annotation, label, taint, and finalizer keys (`nodewright.nvidia.com/*`,
  `skyhook.nvidia.com/*`), and any `nodeState_*` shape change
- Status, State, or Stage values added, removed, or renamed
- Helm values and template keys (`chart/values.yaml`, `chart/templates/`) and their
  `operator/config/` counterparts
- CLI commands and flags under `operator/cmd/cli/`
- Agent package-config keys and `agent/skyhook-agent/.../schemas/` changes
- Workflow inputs and triggers under `.github/workflows/`; Makefile and `*.mk` targets
- File or manifest paths renamed or restructured
- Behaviorally significant defaults changed (timeouts, requeue intervals, image tags,
  versions, namespaces, interruption-budget defaults)

> **This skill never runs the PR's code.** No build, test, generator, coverage, or
> `make` target; every reviewer prompt forbids it. Only trusted tools run (`git`, `gh`,
> the CodeRabbit CLI, the Codex companion). If a reviewer suspects generated-artifact
> drift, that is reported as a finding against the diff, not settled by running
> `make manifests generate`. CI is what actually runs things; see Phase 3.

## Phase 2: Run the review workflow

```
Workflow({
  scriptPath: "<skill-dir>/scripts/workflow.mjs",
  args: {
    mode: "pr" | "local",
    pr: <number>,                       // omit in local mode
    branch: "<branch name>",            // local mode; harmless in pr mode
    repo: "NVIDIA/nodewright",
    repoPath: "<local checkout path>",
    headSha: "<HEAD_SHA>",
    baseSha: "<BASE_SHA>",
    diffPath: "<DIFFPATH>",
    prType: "<classification>",
    changeList: ["<item 1>", "<item 2>"],
    repoNotes: "<3-6 line digest, optional>"
  }
})
```

Pass `changeList` as a real JSON array, not a stringified one. Every lane is
`general-purpose` and inherits the session model, so there is no model argument to pass.

`mode` defaults to `pr`, and `pr` is required in that mode; the script rejects the
combination rather than guessing. Everything downstream of the header is mode-independent
by design: both modes pin a commit and hand every lane the same saved diff, so there is
one consensus path to keep correct rather than two.

**What the workflow does** (`scripts/workflow.mjs` is the single source of truth for
the consensus mechanics):

- **Review** — Claude Code (reviews the pinned diff directly; it deliberately does
  *not* delegate to the `code-review` command, whose step 8 instructs its agent to
  `gh pr comment` the result back to the PR), Codex (background dispatch, a 9-min
  bounded wait plus one continuation wait when the job is still running, about 18 min
  for a live job), CodeRabbit (CLI against a detached worktree at `HEAD_SHA`, explicit
  600000 ms timeout; the Bash tool caps any single call at 10 minutes, which is why
  Codex exceeds it by waiting twice rather than waiting longer), and integration
  analysis (bounded to `changeList`). Every lane is a `general-purpose` agent. All
  parallel, schema-validated, and none may execute the reviewed commit's code.
- **Repo context** — every lane receives a standing block naming the three components,
  the partial Skyhook to NodeWright rename (a mixed vocabulary is expected and is not a
  finding), the Status/State/Stage distinction (using one for another *is* a finding),
  the vendored and generated trees not to review, the `zz.migration.*.go` exception, and
  the pathspec that excludes `operator/vendor` and `agent/vendor` from repo-wide greps.
  Most of that block removes work rather than adding it, which is why it is affordable
  even in the context-starved Codex lane.
- **Merge** — dedupe by `path:line:normalized-summary:consumerPath:consumerLine`;
  duplicates merge to the highest severity and union their sources; a finding citing a
  file the reporter never listed in `filesChecked` is flagged for extra scrutiny.

  **Two lanes wording one defect differently stay separate candidates, by design.** Keying
  on location alone was tried and reverted: it did merge those duplicates, but the
  evaluation schema permits exactly one verdict per candidate id, so a merged pair of
  *distinct* same-line defects has no correct verdict. Confirming the real one also
  confirms the false one, and refuting the false one dismisses the real one. Retaining
  both summaries prevented data loss but not mis-adjudication, which is the worse failure.

  Instead, candidates sharing a location (`path:line` **and** the same
  `consumerPath`/`consumerLine`) are **flagged** as possible duplicates. The consumer half
  matters: one changed declaration breaking two callers is deliberately two candidates, and
  hinting that they might be duplicates would push reviewers to collapse a distinction the
  key exists to preserve. The flag reaches the cross-review candidate list and the refuter
  prompt, so reviewers decide whether the two are one defect and evaluate them
  consistently. Equivalence stays an explicit judgement rather than an assumption from a
  shared line number.

  Merging also stops once candidates are presented: a late finding that merged into an
  already-evaluated id would inherit votes cast before it existed. Late findings always
  become their own candidate and, being unpresented, stay contested for the human.

- **Cross-review (one round, Claude + Codex only)** — each re-reviews independently
  first (anti-anchoring), then returns AGREE/DISAGREE/OPEN_QUESTION per candidate.
  CodeRabbit does *not* take part: its CLI is a slow blocking cloud call and it
  reviews Git changes generically, so it cannot adjudicate our candidate ids and a
  second run over the same commit adds no signal. Its round-1 findings still stand as its
  AGREE votes, so it can still corroborate a split it independently reported. Anything
  still split afterwards is reported as contested for you to settle.
- **Consensus rule** — confirmed = 2 of the 3 reviewer slots AGREE **with evidence**;
  integration analysis is never a reviewer slot. A round-1 finding whose evidence is
  blank or whitespace-only is dropped at intake, so it never registers its reporter as
  a source. In the cross-review round an unevidenced AGREE/DISAGREE instead aborts the
  run (`incomplete`), because dropping it would leave the reviewer's round-1 source vote
  to decide the tally.
- **Verify** — every confirmed finding goes to a fresh adversarial refuter
  (REFUTED → dismissed; UNVERIFIABLE, no result, or a verdict without a citation →
  the `unresolved` array). `consensusReached` is true only when both `contested` and
  `unresolved` are empty: a finding that reached consensus but failed verification is
  an open question, not a settled one.

  **Read `adjudication` on every contested entry.** The bucket holds two different states
  and they need different things from you. `evaluated` means the finding was presented,
  the reviewers voted, and they did not reach 2-of-3: a genuine split, so break the tie.
  `raised-late` means it was raised *during* the cross-review round, after candidates were
  presented, so nobody cross-evaluated it and its only position is its reporter's; it just
  needs reading. Measured on a real run: 8 of 8 contested findings were `raised-late`, each
  with a single AGREE and NONE elsewhere, so the count read as eight disagreements when
  there were none. `consensusReached` counts both, deliberately: a late finding is
  unadjudicated, and letting it report consensus would be the same overstatement this
  skill exists to avoid.
- **Report incomplete and stop** — Claude, Codex and integration analysis are required
  in round 1, and Claude and Codex must each return exactly one evaluation per
  candidate in the cross-review round. A missing lane, a missing evaluation, a
  duplicate, or an unknown candidate id returns `status: "incomplete"` with the reason
  and raw unverified findings. There is no degraded-consensus mode. CodeRabbit is the
  only best-effort lane: when it does not run, its vote slot records `NONE`, which
  raises the bar (Claude and Codex must then agree) rather than lowering it.

  One deliberate exception, at the level of a **finding** rather than a lane. An
  integration finding claims a specific consumer breaks, so one lacking
  `consumerPath`/`consumerLine` cannot be verified and never enters consensus; it is
  dropped on its own, with a `log()` naming what went, rather than failing the run. The
  earlier all-or-nothing rule was disproportionate: on a real run the lane returned
  several findings, one of them a genuine evidenced defect, plus a stale-comment finding
  that legitimately has no consumer, and the review reported `incomplete` with all four
  lanes `ok` and no report produced. The run still stops when **every** integration
  finding is unusable, which is the case that motivated the check: silently dropping the
  lane's only finding once yielded `consensusReached: true` while a required lane had
  contributed nothing.

  "Unusable" is measured on what survives `intake()`, not on the coordinate check alone.
  `intake()` independently drops a finding whose `evidence` is blank, so gating on
  coordinates let a coordinate-complete, whitespace-evidence finding pass the filter and
  then vanish inside `intake()`: zero candidates, `status: ok`, `consensusReached: true`.
  That is the same false-clean, in a narrower form. One rule now covers both drop reasons:
  a non-empty integration result that yields no accepted finding stops the run, and the
  message says how many went for each reason.

  **Coordinates are validated by a single shared rule**, `hasCoords`, applied to a
  finding's own `path`/`line` in `intake()` (every lane, not just integration) and to
  `consumerPath`/`consumerLine` for the integration pair. The response schema **requires**
  `path` and `line` and leaves `consumerPath`/`consumerLine` optional, deliberately, since
  only integration findings carry a consumer, but it constrains none of the four, so
  `""`, `"   "`, `0` and `-1` all satisfy it and all passed a truthiness/null check.
  Tightening the schema instead would fail a whole lane on one bad field, which is the
  all-or-nothing behavior this section exists to remove. It is one helper rather than two
  call-site conditions for a specific reason: every earlier version of this guard fixed the
  pair it was shown and left the other, and a shared rule is what stops the next field pair
  from repeating that.

  **The zero-survivor rule applies to every required round-1 batch**, not just integration.
  Claude's and Codex's round-1 findings go through `intakeBatch`, which reports what
  survived. A required lane whose round-1 findings are *all* malformed contributed
  nothing, and letting them vanish silently is the same false-clean one lane over. Mixed
  batches still proceed. CodeRabbit is exempt: a total loss there records `NONE` and
  raises the bar, exactly as a lane that never ran. Rejection reasons are counted
  separately (`unlocatable` vs `unevidenced`) rather than inferred from a subtraction, so
  the message names the defect the reader should go looking for.

  **It deliberately does NOT apply to cross-review `newFindings`.** Those go through the
  same `intakeBatch` gate, so a malformed late finding still never enters the tally, but a
  total loss there is not fatal. The rule tests "this lane contributed nothing", which
  round 1 can assert because findings are the lane's whole output. In the cross-review
  round the lane's output is its *evaluations*, and the completeness gate has already
  returned `incomplete` unless the lane evaluated every presented candidate; `newFindings`
  are supplementary and volunteered. Aborting there would throw away a full set of
  adjudications and the verification round over one imprecise extra finding: the same
  disproportionate total loss seen once before, one round over. Malformed late findings are
  dropped individually and each drop is logged with its reason.

**Operational notes:**

- The workflow runs in the background; wait for its completion notification.
- If it dies mid-run, **resume, don't restart**:
  `Workflow({scriptPath: ..., resumeFromRunId: "<wf_...>"})`. Completed lanes replay
  from cache. Empty or odd result → read `<transcriptDir>/journal.jsonl` first.
- The Codex lane fails in three distinct ways, and the dispatch protocol treats them
  differently:
  - **Lookup miss** — the status call exits 1 with empty stdout and `No job found` on
    stderr. Companion state is keyed by workspace root and each Bash call is a fresh
    shell, so an unpinned lookup resolves to a different workspace and reports a live job
    as unknown; the miss is **not** evidence the job died. **Always recheck exactly once**
    with `--cwd` pinned, whether or not the missing call already carried it: two causes
    produce the identical message and only one is settled by adding the flag. The other is
    transient. In companion v1.0.2 `saveState` writes `state.json` with a plain
    `fs.writeFileSync` (truncate-then-write, no temp-and-rename) while `loadState` wraps
    `JSON.parse` in a bare `catch` returning the **default** state, whose `jobs` list is
    empty. A read landing inside that write window yields a well-formed `No job found`
    rather than an error, and the background worker is rewriting that file precisely while
    the status call runs. So an identical repeat need not return an identical answer. Once
    a pinned recheck has also missed, return `unavailable` saying the job could not be
    located. Never re-dispatch (the original may still be running) and never record it as
    exhausted budget.
  - **Fast transient failure** — retried **once**, and only when all three hold:
    `.job.status` is `failed` (never `cancelled`), the job died in **under 60 seconds**
    by its own timestamps, and the error names a known-retryable cause such as an upstream
    capacity rejection (`Selected model is at capacity`) or a transient dispatch fault.
    The threshold is a number rather than a judgment because the observed cases are far
    apart (a capacity rejection at ~10s against a genuine timeout at 10m19s), and an
    undefined "quickly" is how a one-retry budget erodes. A retry then costs seconds and
    these clear on their own. **`cancelled` is never retried**: the companion emits it only
    for explicit cancellation, so a retry would restart work someone deliberately stopped.
    Nor is a late failure: `.waitTimedOut` false only means the job became terminal before
    the inner deadline, which a failure at 8:59 also satisfies while having burned the
    whole window. An unrecognised error is not assumed retryable either.
  - **Wait elapsed, job still alive** (`.waitTimedOut` true with `.job.status` still
    `queued`/`running`) — not the end of the lane. The job is dispatched in the background
    and outlives the Bash call waiting on it, so it needs more **time**, not another
    attempt, and the protocol gives it exactly that: **one continuation wait** on the
    *same* job id in a fresh call. That is not a retry; nothing is re-dispatched and no
    work is duplicated. A second `.waitTimedOut` is then genuinely exhausted budget, and
    the lane returns `unavailable` with the job id so the result can be fetched later.
    Measured on a real run: the lane timed out at the full 540000 ms while the job was
    demonstrably mid-work, the job was **still** `running` long after the review was
    abandoned, and its result stayed retrievable, so reporting exhausted budget there
    discarded a required lane, and the whole review with it, over a job that had merely
    not finished.
  - **Exhausted budget** — a second `.waitTimedOut`, or no parseable JSON at all because
    the outer timeout killed the call (a dead broker). No retry, no further wait.

  The ceiling is not tunable: the wait runs inside a Bash call, and that tool silently
  kills any foreground command at 600000 ms. Exceeding 10 minutes requires polling across
  several calls, which is exactly what the continuation wait above does; a live job gets
  two waits, roughly eighteen minutes, without a longer single call. The inner wait is
  therefore 540000 ms, deliberately **below** the outer cap: were the two equal, Bash could
  kill the command before it printed its JSON, leaving no `.waitTimedOut` to classify on.
  That is why an unclassifiable kill counts as exhausted-budget rather than a fast failure;
  guessing wrong there costs another full window for nothing.
- Codex is required, so a lane that is still unavailable after its retry makes the run
  report `incomplete`; re-run rather than interpreting a partial result.
- CodeRabbit slow runs: check the newest file in `~/.coderabbit/logs/` (429/queue lines
  mean cloud-side queueing) and confirm `which -a coderabbit` resolves to the
  brew-managed binary; a stale `~/.local/bin` copy shadows it.
- **A sandboxed CodeRabbit run hangs instead of failing.** `~/.coderabbit` is outside the
  sandbox write allowlist, so the CLI cannot create its log or review store; it stalls at
  `connecting_to_review_service` until the timebox kills it. The lane therefore runs the
  `coderabbit` command with sandbox bypass, and only that command.

  **Why bypass rather than an allowlist entry.** Adding `~/.coderabbit` to the sandbox
  write allowlist is the narrower grant and was considered first. It was rejected because
  this skill is checked into the repo and has to work on a contributor's machine as
  written: an allowlist entry lives in each person's local settings, so anyone who has not
  made the same edit gets the silent ten-minute hang above rather than a usable lane. The
  bypass is portable and self-documenting at the call site. Its cost is a permission
  prompt on step 2 of every round, which is accepted. Adding the allowlist entry locally
  is still worthwhile if you run this often (it does not conflict with the bypass), but
  the skill must not depend on it.

  **Diagnose it by absence:** a stall at `connecting` *with no new file in
  `~/.coderabbit/logs/`* is sandbox denial. A process killed mid-run still flushes a
  partial log, so zero bytes means it never created one. A real cloud problem leaves a log
  with 429/queue lines. Do not read this stall as an outage or as contention with another
  session: an unsandboxed run succeeding while a sandboxed one hangs looks exactly like
  contention and is not.
- **Persisted-store fallback.** If a run still fails, the lane checks
  `~/.coderabbit/reviews/*/*/reviews/*/git.json`, whichever session produced the record.
  Acceptance is `head` plus **the pinned change itself**, never `baseCommitId`. Measured:
  the CLI writes `baseCommitId` from the base it resolves locally in that working
  directory, not from the `--base-commit` the lane passes. In one real store the same head
  carried two different values (a main tip, and a commit not on main at all), while three
  other valid records carried the stale local `main` rather than the base Phase 1 fetched
  from the canonical repo. Gating on equality discarded good reviews whenever the local ref
  lagged, which is the common case for a fork PR. `baseCommitId` is now reported in
  `statusNote` and never branched on.

  What identifies the review instead is the **blob OIDs the record already stores**.
  Acceptance is layered, cheapest first, and only the last step is identity:

  1. Same paths, from `git diff --numstat -z` over the **three-dot** range Phase 1 saved.
     Two-dot would compare the base tip to the head and pull in everything that landed on
     the base after branching; on a real pair, 13 files against the valid record's 59.
     `-z` matters independently: a plain `split()` shreds a path containing a space into
     separate members and would reject every record for such a PR.
  2. Same per-file line counts, against the record's `linesAdded` / `linesRemoved`.
  3. The committed-only `lanes` shape.
  4. The **file modes and blob OIDs** the record's own patch states, equal to what
     `git diff --raw --no-abbrev` reports for the pinned range.

  Steps 1 and 2 are a prefilter, never a result: two different patches collide on a
  numstat trivially. A `beta`→`BETA` edit and a `gamma`→`GAMMA` edit in the same file both
  report one added and one removed, and a review of this head against another base can
  leave exactly such a record.

  Step 4 compares OIDs rather than the stored patch text, which was the obvious move and
  is wrong. Bare `git diff` output moves with the reader's gitconfig (measured: the text
  differs under `diff.noprefix` and `diff.context`, and fails outright under
  `diff.external`), so a contributor with ordinary settings gets a permanent mismatch and
  this fallback silently never fires. That is the same works-on-my-machine class the
  sandbox decision rejected an allowlist for. Pinning flags (`--no-ext-diff`, `--no-color`,
  `-U3`, `--src-prefix`) fixes *our* side only; if the CLI produced the record under that
  same config, hardening makes the mismatch worse. Blob OIDs are content hashes: stable on
  both sides, and exact content identity rather than a rendering of it. Both git calls
  still pass `--no-ext-diff --no-color`, since the `--raw` call needs them.

  **Modes are checked before OIDs, because OIDs alone are not identity.** With a `chmod +x`
  in one commit and an edit in the next, a stored review of the *edit alone* carries the
  same path, counts, `lanes` and **both** blob OIDs as the pinned range covering both
  changes (verified). Only the modes differ: pinned `100644 → 100755` against the record's
  `100755 → 100755`. Git states modes in one of four shapes (`old mode`/`new mode`,
  `new file mode`, `deleted file mode`, or the trailing mode on the `index` line), and the
  trailing mode is absent exactly when the `old`/`new` pair is present, so the cases do not
  overlap. A record that states no mode at all cannot prove identity and is skipped. This
  repo ships executable scripts under `scripts/` and `k8s-tests/`, so a `chmod` landing in
  a range is not hypothetical here.

  An entry with **no** `index` line is not automatically rejected. Git omits that line
  exactly when the blob is unchanged: for a `chmod +x` the mode lines carry the
  information instead, and the mode check above has already matched them. Such an entry is
  accepted when the pinned diff agrees the blob is unchanged (`src == dst`), and rejected
  as `no-index-line` otherwise. Rejecting on sight would have disabled this fallback for
  any change that merely marks a script executable. A rename or copy in the pinned range is
  rejected outright as `rename-unverifiable`, by an explicit status check rather than
  incidentally: the record is keyed on the post-rename path and carries no source path, so
  an edit-only review of that path is indistinguishable from one that covered the rename
  (measured: a pinned `R077 old.txt -> new.txt` and a stored `M new.txt` share modes, blobs
  and line counts). The scope prefilter usually rejects such a record first, since the CLI
  scopes its stored patch and counts to the post-rename path.

  Every entry must additionally prove it was **committed-only**. `lanes` is a boolean map
  (`{"committed": true, "uncommitted": false}`), so the check tests the values and requires
  that exact shape. Merely rejecting a truthy `uncommitted` was not enough: a record from
  an older CLI that omits `lanes`, or carries a non-dict, would pass as clean and
  contribute comments on code outside the pinned head. Absent proof of clean, skip.

  The pinned diff itself is computed **fail-closed**: an unreachable base exits non-zero
  with empty output, and treating that as an empty result would reject every record and
  then report a clean "nothing matched", so the scan aborts with an `ERROR` line instead.
  Several records can also pass every check at once (a re-run after a transient failure
  produces a twin), so candidates are ordered by mtime and exactly one `MATCH` is emitted:
  newest wins, since a re-run supersedes what it replaced.

## Phase 3: CI status for the pinned commit

**`pr` mode only.** In `local` mode there is no PR and no CI run for the commit; say so
in one line and move on. Do not push the branch to trigger CI, and do not run tests
locally: the no-execution rule covers `make unit-tests` and `make test` exactly as it
covers everything else.

Do **not** compute coverage locally and do **not** parse a CI coverage comment. CI
enforces its own thresholds and reports against its own baseline; a locally computed
number is not comparable and is not worth the run.

`gh pr checks` reports the PR's **current** head, not `HEAD_SHA`, and the head can move
during a long review. Confirm it first:

```bash
# Separate `if`, not `A && B || C`: gh pr checks exits nonzero for pending (8) and
# failing checks, so chaining would report "head moved" whenever CI is simply red or
# still running.
if [ "$(gh pr view <n> --repo NVIDIA/nodewright --json headRefOid -q .headRefOid)" = "<HEAD_SHA>" ]; then
  gh pr checks <n> --repo NVIDIA/nodewright   # 0 = green, 8 = still running, other = failing
else
  echo "head moved during review — no CI status for the reviewed commit"
fi
```

If the head moved, omit the CI line rather than reporting another commit's result.
Otherwise report one line: passing, failing (name them), or still running. This repo runs
a `Merge Gate` workflow plus per-component CI (`operator-ci`, `agent-ci`, `lint-ci`,
`codeql`, and others), so "failing" should name the check, not just the count.

Do **not** add `--required`: before an aggregate gate job exists it prints "no required
checks reported" and exits 1, so an ordinary in-progress run looks like an error. Plain
`gh pr checks` exits 8 while running and 0 when green, and handles `skipped`/`neutral`
correctly, unlike a raw check-runs query which counts them as failures and returns only
the first page.

## Phase 4: Consensus report

Build from the workflow's return value plus the CI status line from Phase 3:

```markdown
## Cross-Review Summary for <PR #number | branch <name>>

**Reviewers:** Claude Code, Codex, CodeRabbit + Integration Analysis
**Head commit:** <sha> | **Consensus reached:** Yes/No
**Review depth:** <two passes per lane | ONE pass per lane: no round-1 candidates, so the
cross-review round and its independent re-review did not run — from `crossRoundRan`>
**Change-list items analysed:** <from `changeListSize`; if 0, say plainly that the
integration lane verified nothing>
**CI for this commit:** <passing | failing: check names | still running | n/a (no PR)>
<note if CodeRabbit was unavailable — it is the only best-effort lane>
<in local mode, note any uncommitted files from Phase 1: they were NOT reviewed>

### Confirmed Issues (met consensus rule; survived adversarial verification)

| # | File | Line | Severity | Description | Confirmed By |
|---|------|------|----------|-------------|--------------|

### Integration Findings (cross-cutting impact)

| # | Changed File | Consumer File | Severity | Description | Confirmed By |
|---|--------------|---------------|----------|-------------|--------------|

### Unresolved (no settled disposition)

| # | File | Line | Severity | Description | Why unresolved |
|---|------|------|----------|-------------|----------------|

<from the workflow's `unresolved` array: findings that reached consensus but did not
survive adversarial verification, plus findings no reviewer cast a valid vote on; omit
the section if empty>

### Contested Issues (no 2-of-3 disposition)

Split reviewers, a lone dissent, or a finding raised during the cross-review round and
therefore never presented for evaluation.

**Carry the `adjudication` field into the table.** The two states need different things
from the reader and the counts are not comparable: `evaluated` was presented, voted on,
and did not reach 2-of-3, so it needs a tie broken; `raised-late` was never presented to
anyone, so its only position is its reporter's and it just needs reading. Collapsing them
is what makes a row of late findings read as a row of disagreements.

| # | File | Line | Severity | Description | Adjudication | For | Against | Reasoning |
|---|------|------|----------|-------------|--------------|-----|---------|-----------|

<`Adjudication` is `evaluated` or `raised-late`, verbatim from the workflow's field of
that name; the workflow's `why` explains each in one line>

### Dismissed Findings

<finding, who flagged it, why dismissed (incl. "failed adversarial verification: ...")>

### Open Questions

<unverifiable findings + reviewers' open questions>

### Residual Risk

<from the workflow's residualRisk array: reviewer-flagged risks that are not
findings; omit the section if empty>

### Positive Observations

<noteworthy good patterns>
```

## Phase 5: Output

**Default: do NOT post.** Present the full report in chat and stop. Do not ask
whether to post. In `local` mode there is nothing to post to, so this phase does not
apply at all.

**When asked to post, post a DRAFT review, not a comment.** A review created through the
API with no `event` field stays `PENDING`: GitHub shows it only to the account that
created it, in the PR's Files-changed tab, where it can be edited comment by comment and
then submitted or discarded. Nothing is public until a human presses Submit. That is the
right default for a machine-generated review, and it is why this phase does not use
`gh pr comment`, which publishes immediately and cannot be recalled.

Findings already carry `path` and `line`, so anchor them as inline comments rather than
flattening them into one blob. Split them first:

- **Inline-able**: the finding's `path:line` falls on a line the PR diff actually touches.
- **Everything else**: goes in the review body. GitHub rejects the whole request with 422
  if any single comment names a line outside the diff, and this skill deliberately reads
  full files at the pinned commit, so findings outside the diff are normal, not an error.
  One bad coordinate loses the entire review, so when in doubt put it in the body.

Build the payload with the Write tool and post it with `--input`. Never interpolate a
finding into a shell argument: findings quote PR content, and a backtick or `$(...)` in
one would be executed by the shell before `gh` ever runs. JSON in a file avoids that
entirely.

```json
{
  "commit_id": "<HEAD_SHA>",
  "body": "<the filtered summary — see the content rules below>",
  "comments": [
    {"path": "operator/internal/controller/foo.go", "line": 412, "side": "RIGHT",
     "body": "<one finding, stated plainly>"}
  ]
}
```

```bash
# No "event" key in the payload: that is what makes it a PENDING (draft) review.
gh api repos/NVIDIA/nodewright/pulls/<n>/reviews --method POST --input "<payload-file>"
```

`<payload-file>` is the exact path you passed to Write. A Write-tool call cannot export a
shell variable, so substitute the literal path here.

Then tell the user it is waiting as a draft on the Files-changed tab, and stop. Do not
submit it for them. Submitting is a one-click action in the UI, and the whole point of the
draft is that a human reads it first.

Three failure modes worth naming, because each returns a bare 422:

- **One pending review per user per PR.** A second POST while one is still pending fails.
  Say so plainly rather than retrying; the earlier draft has to be submitted or discarded
  in the UI first.
- **`commit_id` must be the reviewed commit.** Pass `HEAD_SHA`, not the PR's current head.
  If Phase 3 found the head had moved, say the review anchors to a commit that is no
  longer the tip, or skip posting.
- **A line outside the diff** rejects the entire request, not just that comment.

Content rules, unchanged from a posted comment:

- Post **issues only**: Confirmed Issues (without the "Confirmed By" column),
  confirmed Integration Findings, Contested Issues, Unresolved, Open Questions.
- **No reviewer-agent attribution and no severity-label prefixes** in posted
  content. State each finding and its evidence plainly.
- Never post Dismissed Findings or Positive Observations.
- State the review depth in the body when `crossRoundRan` is false, and say the
  integration lane verified nothing when `changeListSize` is 0. A reader of the PR cannot
  see the workflow's return value, so an unqualified review reads as a full one.

If the user explicitly asks to submit rather than draft, add `"event": "COMMENT"` to the
payload. Never use `"REQUEST_CHANGES"` or `"APPROVE"`: this skill's output is evidence for
a human, not an approval decision.

## Rules

- Never post to the PR without an explicit user request, and when asked, post a PENDING
  draft review (no `event` field) so a human submits it. Never submit or approve.
- The consensus rule, the required-lane contract, the repo-context block, and the
  single-cross-review-round structure live in `scripts/workflow.mjs`; keep it and this
  doc in sync.
- **This skill never executes the reviewed commit's code.** No builds, tests, coverage,
  generators, `make` targets, package managers, or repository scripts. In particular:
  suspected `make manifests generate` drift is reported as a finding against the diff, not
  settled by running the generator. If a claim can only be settled by running something,
  it is an open question.
- Confirmed integration findings identifying broken consumers escalate to at least
  **medium** severity (done in-script).
- Severity scale: critical (must fix) > major (should fix) > medium > minor.
- Keep the report concise: actionable findings, not noise.
- Never set `dangerouslyDisableSandbox` for reviewer or companion commands; they run
  fine sandboxed. **Exactly two exceptions**, both kept in sync with the protocols in
  `scripts/workflow.mjs`, and both scoped to a single command that performs no Git
  operation, no working-copy mutation, and no GitHub write. (They do *read* files;
  CodeRabbit necessarily reads the detached worktree it was pointed at. The rule bars
  bypassing calls that **act on** the working copy, not calls that read a path):
  - **Codex companion** — it writes its job log under `~/.claude/plugins/data`, which is
    sandbox-denied. If dispatch fails on that write, bypass for that call only.
  - **CodeRabbit review** — `~/.coderabbit` is outside the write allowlist, so a
    sandboxed CLI cannot create its log or review store and hangs at
    `connecting_to_review_service` until the timebox kills it. Bypass **step 2 only** of
    the three-step CodeRabbit protocol, which is a lone `coderabbit review` command;
    worktree setup and cleanup live in steps 1 and 3 and stay sandboxed.

  Anything else stays sandboxed. In particular, never bypass a call that also performs
  `git` operations; that is why the CodeRabbit protocol is split into three calls rather
  than the single invocation it used to be.

  Note that in this repo `.claude/skills/` is itself on the sandbox write deny-list, so
  *editing this skill* needs a bypass even though *running* it does not. That is a
  property of the local sandbox profile, not of the skill.
- **The tool shell is zsh, and every prompt that hands out a shell command says so.**
  `scripts/workflow.mjs` defines a single `SHELL_CONTRACT` constant, interpolated in
  **exactly one place**: `NO_EXECUTION`, which every prompt builder composes exactly
  once. So every assembled prompt carries it exactly once. Do not add a second
  interpolation: `NO_EXECUTION` already embeds `PINNED_READS`, so putting it in
  `PINNED_READS` or `CODEX_LEAN` as well silently doubles it in every lane. That is how
  the first attempt got it wrong, and no block-level check can see it; the duplication
  only appears once the prompts are assembled. It covers the three differences that bite
  silently: `shopt` and other bash builtins are simply absent, an unmatched glob is a hard
  error that aborts the command list rather than passing through literally, and `status`
  is readonly.
- **Clean up before finishing:** `rm -f <the DIFFPATH echoed in Phase 1>` and delete the
  scoped refs captured in Phase 1 (`git -C "<repo-path>" update-ref -d "$BASEREF"`, and
  `"$PRREF"` in `pr` mode; use the exact names echoed there, not a guess). Confirm no
  `${TMPDIR:-/tmp}/cr-rabbit.*` worktree path remains in `git worktree list`. Write the
  fallback out, since setup creates the worktree under `${TMPDIR:-/tmp}` and with `TMPDIR`
  unset a bare `$TMPDIR/cr-rabbit.*` names `/cr-rabbit.*` while the leak sits in `/tmp`.
  The CodeRabbit lane cleans its own in step 3, but verify, since a lane killed between
  steps 2 and 3 leaves one behind. Step 1 of the next run reaps `cr-rabbit.*` directories
  older than 120 minutes, which bounds the leak but does not clear it now. Do not compare
  total worktree counts: this repo keeps its own worktrees under `.claude/worktrees/` and
  concurrent sessions change the total legitimately.
