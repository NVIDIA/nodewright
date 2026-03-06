# Strict Order Test

Tests both `sequencing: node` (default, per-node ordering) and `sequencing: all` (opt-in, global ordering).

## Skyhook Layout

```
Priority 1:  zzz          sequencing: node (default)
Priority 2:  gate         sequencing: all  (global sync point)
Priority 3:  aa, b (paused), c, d (disabled)
```

## Timeline

A taint on `kind-worker2` blocks it from running pods initially. Time flows left to right. Both nodes run in parallel after the taint is removed.

```
                  assert 3       assert 3b  rm taint    assert 5    unpause b  assert 7
                  ▼               ▼          ▼           ▼           ▼          ▼

kind-worker  zzz ■■■■■■■■■■■  gate ■■■■■■■  ···· waiting (gate holds) ····  b ■■■■ c ■■■■ ✓
                                    ╲
kind-worker2 ░░░░░░ blocked ░░░░░░░  zzz ■■■■■■■■■  gate ■■■■■  b ■■■■ c ■■■■ ✓
                                     ▲
                               gate on worker + zzz on worker2
                               run CONCURRENTLY here
                               (per-node ordering in action)

■ = running/complete    ░ = blocked (taint)    · = waiting (gate sequencing:all)
```

**Observed pod execution order** (from `kubectl get pods -w`):

```
 worker:  zzz apply → config → interrupt → post-interrupt
 worker:  gate apply ──┐                                     per-node: worker starts
 worker2: zzz apply  ──┘ concurrent after taint removed       gate while worker2 on zzz
 worker2: zzz config ──┐
 worker:  gate config ──┘ concurrent
 worker2: zzz interrupt → post-interrupt
 worker2: gate apply → config                                 worker waits (sequencing:all)
 worker:  b apply ──┐
 worker2: b apply ──┘ BOTH start b at same time               gate released!
 (b and c complete on both nodes)
```

**What each assertion checks:**

- **Phase 3**: worker completed zzz, worker2 still blocked (taint). Proves `sequencing: node` — worker moved ahead.
- **Phase 3b**: worker past gate, b paused, worker2 still blocked. Proves `sequencing: all` — gate holds worker from priority 3.
- **Phase 5**: both nodes completed zzz + gate. b still paused. **Stable state** — no race. Proves gate released once both cleared it.
- **Phase 7**: everything complete. b before c (alphabetical). d disabled/skipped.

## What This Proves

| Phase | Behavior | How |
|-------|----------|-----|
| 3 | **Per-node ordering** | worker moves from prio 1 → 2 while worker2 is stuck on prio 1 |
| 3b | **`sequencing: all` blocks** | worker finished gate but can't start prio 3 until worker2 catches up |
| 5 | **Gate releases** | both nodes past gate, b still paused = stable assertion point |
| 6-7 | **Alphabetical ordering** | b processes before c at priority 3 |
| 7 | **Disabled skip** | d is skipped on both nodes |

## Files

| File | Phase | Purpose |
|------|-------|---------|
| `chainsaw-test.yaml` | — | Main test, 7 phases |
| `skyhook.yaml` | 2 | Skyhook definitions (zzz, gate, b, c, d) |
| `skyhook-pause-update.yaml` | 6 | Unpause b |
| `skyhook-disable-update.yaml` | — | Disable state patches (not used in current flow) |
| `assert-node1-priority1-complete-node2-blocked.yaml` | 3 | worker past zzz, worker2 blocked |
| `assert-gate-blocks-worker.yaml` | 3b | worker past gate, b paused, worker2 still blocked |
| `assert-gate-released-b-paused.yaml` | 5 | Both past gate, b still paused (stable) |
| `assert-both-nodes-complete.yaml` | 7 | Everything complete |
| `assert-concurrent-different-priorities.yaml` | — | Kept, not referenced by current test |
| `assert-multiple-skyhooks-in-progress.yaml` | — | Kept, not referenced by current test |
