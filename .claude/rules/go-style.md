---
summary: "Wrap errors with %w, plumb ctx, logr in the operator, named constants, comments only for the unusual"
paths:
  - "**/*.go"
  - "**/go.mod"
---
# Go conventions

These apply to every Go module in the repository. The operator, agent, tools and test rules add to them.

## Errors

- Use stdlib `errors` and `fmt.Errorf("…: %w", err)`, not a `pkg/errors` sentinel-code wrapper. Wrap with `%w` so `errors.Is` and `errors.As` keep working.
- Never return a bare `err` from a function that does more than one thing. Add context: `fmt.Errorf("reconciling node %s: %w", node.Name, err)`.
- Don't ignore `Close()` on writable handles; `errcheck` is on. Check the error from `os.Create`, `os.OpenFile(..., O_RDWR|O_WRONLY)` and the like:

  ```go
  closeErr := f.Close()
  if err == nil { err = closeErr }
  ```

- `client.IgnoreNotFound(err)` and the return of `controllerutil.SetControllerReference` are idiomatic and need no wrapping.

## Context and concurrency

- Plumb the caller's `ctx` through. `context.Background()` is fine only in package `init`, in long-running goroutines owned by `main`, and in test setup.
- Any HTTP or other external I/O needs a bounded context: `ctx, cancel := context.WithTimeout(ctx, …); defer cancel()`.
- Never use `http.DefaultClient`, which has no timeout. Build a client with `Timeout:` set, or use the REST client controller-runtime wires up.
- Check `ctx.Done()` in long-running loops so cancellation propagates.
- For parallel fan-out, prefer `golang.org/x/sync/errgroup` over a hand-rolled `sync.WaitGroup`.

## Logging

- Operator code (`operator/internal/`, `operator/api/` webhooks, `operator/cmd/manager/`) uses controller-runtime's `logr.Logger`, from `log.FromContext(ctx)` or the reconciler's `Logger` field, with key/value pairs. Don't introduce `slog` or stdlib `log` there:

  ```go
  log := log.FromContext(ctx)
  log.Info("reconciling nodewright", "name", nw.Name, "generation", nw.Generation)
  log.Error(err, "failed to cordon node", "node", node.Name)
  ```

- The agent uses `log/slog` instead (see `.claude/rules/agent.md`).
- No `fmt.Println` / `fmt.Printf` logging in non-test operator or agent code.

## Constants

Extract repeated literals (timeouts, annotation keys, label values, image names) into named constants in the nearest appropriate file. Operator annotation and finalizer names already live in `operator/internal/controller/annotations.go` and `operator/internal/controller/nodewright_controller.go`.

## Comments

Default to no comments. Well-named identifiers, small functions and idiomatic control flow should tell the story. Add a comment when, and only when, the code does something odd, surprising, or against a pattern a reader would otherwise assume. That comment must say both:

- **What** is unusual, so the reader knows this is not the pattern to copy.
- **Why** it is done this way: the hidden constraint, invariant, ordering dependency, known bug, webhook race, performance reason or upstream quirk.

Keep it tight to the code it sits on: a sentence or two about the one noteworthy thing in that function or block. Don't name other functions, files, call sites or tests ("called from X", "mirrors Y", "see the check in Z"). A change over there then silently makes the comment wrong over here, and keeping it right means hunting down comments all over the tree. State the constraint itself instead; the reader can find the callers.

```go
// Names other code, so it goes stale when that code changes:
// Trim the prefix because parseTaints keeps it and the CLI compares bare keys.

// States the constraint itself:
// Keys arrive with the domain prefix; callers compare bare keys.
```

Worth writing:

```go
// Patch instead of Update here: the node's status may have been mutated
// by the kubelet between our Get and Update, and a full Update would
// stomp the kubelet's changes.

// Skip the finalizer wait on the DeploymentPolicy: cluster-scoped CRs
// have no owner references back to the operator deployment, so if the
// operator is being uninstalled first, nothing will ever release it.
```

Not worth writing:

- Narration that restates the code, such as `// increment i` next to `i++`.
- A paragraph where a sentence would do, or a doc comment that walks through the implementation instead of saying what the function guarantees.
- Cross-references such as `// keep in sync with buildPod in pod.go` or `// used by the CLI's reset command`.
- PR, issue or fix references without substance, such as `// added for PR #123`. Put the PR description in the PR and the reason in the comment.
- TODOs without a name, a date or a concrete trigger for revisiting them.

If a long comment is needed to explain a clever block, consider whether a rename, an extraction, or not being clever would remove the need for it.

## Dependencies and vendoring

`operator/` and `agent/` vendor their dependencies, and their make targets pass `-mod=vendor`. `go get` is fine; follow it with `go mod vendor`.

Mocks are generated by mockery. After editing an interface that has a mock (for example in `operator/internal/dal/`), run `make generate-mocks` in that module; hand-edited mocks drift.

## Choosing between approaches

Prefer, in order:

1. **Testability**: can it be covered by envtest or unit tests without a real cluster?
2. **Readability**: will the next person on pager duty understand it at 3am?
3. **Consistency**: does it match neighboring code?
4. **Simplicity**: is it the smallest solution that works?
5. **Reversibility**: can it be backed out without a migration?
