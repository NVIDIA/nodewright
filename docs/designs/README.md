# Architecture decision records

An architecture decision record (ADR) records one decision that shapes NodeWright: what was decided, the context that forced it, and the alternatives that were turned down. It is written before the code, reviewed as a pull request, and kept after merge as the record of why the project works the way it does.

ADRs live in this directory, numbered: `NNN-short-slug.md`, titled `# ADR-NNN: Title`.

## Index

| ADR                                                     | Title                       | Status   |
| ------------------------------------------------------- | --------------------------- | -------- |
| [ADR-001](001-uninstall-enhancement.md)                 | Uninstall enhancement       | Accepted |
| [ADR-002](002-webhook-bootstrap-lease.md)               | Webhook bootstrap lease     | Accepted |
| [ADR-003](003-package-execution-as-jobs.md)             | Package execution as Jobs   | Accepted |
| [ADR-004](004-non-interruptible-workloads.md)           | Non-interruptible workloads | Proposed |

An ADR pull request adds its row, and a pull request that changes an ADR's status updates it.

ADR-001 to ADR-004 were written as design documents before NodeWright used ADRs and were numbered in the order they were first merged. When ADRs were introduced they were condensed to decision records in the format below, and each Status section links the original design in full.

## When to write one

Write an ADR before starting work that:

- moves a responsibility between components (operator, agent, CLI, chart), or adds a component or Go module;
- changes lifecycle semantics: the stage graph, interrupts, ordering, or what a Status, State or Stage means;
- adds or changes a public contract in a way that is hard to reverse: a CRD field with new semantics, an on-host path or file format, an agent or CLI interface other tools call;
- introduces a pattern that later changes are expected to follow.

You don't need one for a bug fix that restores intended behavior, a field or flag that follows an existing pattern, a refactor with no behavior change, or a docs change. When unsure, ask on the issue; a maintainer can ask for an ADR during triage or review.

## Numbering

Take the next free number when you open the pull request. If another ADR merges first with the same number, renumber yours before merging. Numbers are never reused, including for rejected ADRs.

## Status

The Status section names one of:

| Status                 | Meaning                                                                                |
| ---------------------- | -------------------------------------------------------------------------------------- |
| Proposed               | under review; an ADR can merge as Proposed when maintainers want it recorded first     |
| Accepted               | maintainers agreed; set in the ADR's own pull request or a later one                   |
| Rejected               | maintainers decided against it; merged only when the record is worth keeping           |
| Superseded by ADR-NNN  | a later ADR replaced the decision; the later ADR says "Supersedes ADR-NNN"             |

An ADR is accepted the way any change is, under [Decision-Making](../../GOVERNANCE.md#decision-making). One that alters a public contract needs an explicit approval from a maintainer who did not write it.

Accepted does not mean implemented. Implementation follows in its own issues and pull requests, which link the ADR.

## After an ADR merges

A Proposed ADR can be revised in later pull requests until it is accepted or rejected. An accepted ADR records what was decided at the time, so it is not rewritten as the code moves on. The only later edits to it are:

- a status change to Superseded, made in the pull request that adds the superseding ADR;
- additions to a table the ADR explicitly asks later changes to extend;
- typo and broken link fixes.

When a change departs from an accepted ADR, leave the ADR as written and say in the pull request description what the ADR decided, what changes and why. A departure that reverses the decision itself gets a new ADR that supersedes the old one.

Current behavior is documented in `docs/architecture/`, `docs/user-guide/` and `docs/operations/`, not in ADRs. The pull request that implements an ADR updates those pages, as any behavior change does.

## Format

Keep it short: an ADR records the decision, not the design. State each decision in a sentence or two. Flag and field tables, file formats, step-by-step behavior and test plans belong in the implementing pull request and the component's docs; keep in the ADR only the detail that is itself decided and hard to reverse.

Start from this outline. Drop a section that has nothing to say rather than filling it, and add a section when the decision needs one (Migration, Phases).

```markdown
# ADR-NNN: Title

## Status

**Proposed**, YYYY-MM-DD. Answers #NNN.

## Decision summary

Two or three paragraphs a reader can stop after: the decision, its scope, and what it does not change.

## Context

The facts that force a decision: current behavior on `main`, the problem, and who is affected. Name the code symbols involved so a reviewer can check them.

## Decision

What NodeWright will do, as a short list of decisions, each precise enough to review an implementation against.

## Non-goals

What this decision deliberately leaves out, and where that goes instead.

## Alternatives

Each option that was considered and turned down, and why.

## Consequences

What becomes easier, what becomes harder, what the project now has to maintain, and the follow-up work this creates.

## Open questions

Questions left for implementation or a later ADR. An ADR can be accepted with open questions only if none of them can reverse the decision.

## References

Issues, pull requests, and other designs.
```

Write it for a reader who knows NodeWright but not the discussion that led to it. Link code by repository path, and issues by number.
