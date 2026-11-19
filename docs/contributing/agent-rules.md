# Agent rules

The coding-agent guide is split in two:

- `.claude/CLAUDE.md` (also reachable as the root `AGENTS.md`) holds the rules for every change, plus an index of the area rules. It must stay under 16 KiB so Codex, which reads at most 32 KiB of `AGENTS.md` by default, sees all of it.
- `.claude/rules/<name>.md` holds the rules for one area of the tree, one file per area.

## How agents find the area rules

- **Claude Code** loads a rule when it reads or edits a file matching the rule's `paths:` frontmatter, but only for files inside the directory the session started in. A session started in `operator/` that edits `chart/` gets no chart rule, so for files outside the start directory it follows the index in `.claude/CLAUDE.md` like any other agent.
- **Codex, Cursor, Copilot and others** read the index in `AGENTS.md` and open the matching rule file before editing.

## Adding or changing a rule

1. Create or edit `.claude/rules/<name>.md`. It starts with frontmatter whose values are double-quoted:

   ```markdown
   ---
   summary: "One line naming the obligation, shown in the index"
   paths:
     - "operator/cmd/cli/**"
   ---
   # Title
   ```

   An unquoted glob that starts with `*` is invalid YAML, and Claude Code then ignores `paths:` and loads the rule in every session.
2. Keep it a regular file. Claude Code skips a path-scoped rule that is a symlink to a file outside the session's working directory, so a symlink into `docs/` stops working for anyone who starts a session in `operator/`.
3. Name other files as repository-root paths in backticks, so the text reads the same whether an agent loaded it automatically or opened it from the index.
4. Run `make agent-rules-index` to regenerate the index in `.claude/CLAUDE.md`, then `make agent-rules-check`, which CI also runs.

Keep a rule in `.claude/rules/` only if it applies to part of the tree. A rule for every change belongs in `.claude/CLAUDE.md`.
