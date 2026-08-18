# Fix — `execution_logs` Has No Writer

**Planner:** Claude (Architect) · **Executor:** Executor agent
**Source:** found while planning Phase 8 (real-time monitoring), confirmed as a standalone, pre-existing gap — not new to Phase 8.
**Scope:** small, standalone, executable independently of Phase 8.

---

## 0. Verification

```
$ grep -rn "\.Append(" internal/ --include=*.go | grep -v _test.go
(zero results)
```

`LogRepository.Append` (`internal/execution/repository.go`) is fully implemented and fully unit-tested from Phase 5 — the bug is not there. It is simply **never called**. Confirmed the whole chain is otherwise wired correctly, so this is a pure omission, not a deeper design problem:

- `coordinator.go:71` — `Coordinator` already holds a `logs LogRepository` field.
- `cmd/worker/main.go:141-142` — `NewCoordinator(execRepo, execRepo, execRepo, verRepo, execs, ...)`: the third `execRepo` **is** the `LogRepository` argument, already non-nil in production. **No wiring change needed anywhere** — this is purely additive code inside `coordinator.go`.
- Consequence confirmed: `execution_logs` is permanently empty, so Phase 6's `GET /api/v1/workflow-runs/{runId}/logs` (built, reviewed, approved) silently returns an empty list forever. Not a defect in that endpoint — it has never had anything to read.

---

## 1. Decision

**Log at the same 7 points `coordinator.go` already calls `c.cfg.Logger.Info`/`Warn`**, using the same message text, so nothing new has to be decided about *what* is worth logging — that judgment was already made when those `slog` calls were written; this fix just persists it. `execution_logs` is the durable, tenant/run/step-queryable form of exactly those events; `slog` output is the operational form. They should never drift apart in wording.

Same best-effort discipline as `Metrics` (`if c.cfg.Metrics != nil { ... }`, no error path): **`logs.Append` failing must never fail a step or run transition.** A dropped log line is recoverable; a run failing because `execution_logs` had a transient insert error is not acceptable. Log the append failure itself via `c.cfg.Logger.Warn`, nothing more.

---

## 2. The 7 call sites (`internal/execution/coordinator.go`)

| Line (current) | Transition | Level | Message | `StepRunID` |
|---|---|---|---|---|
| `:376` | step → `waiting` | info | `"run parked on wait token"` | `&step.ID` |
| `:401` | step → `succeeded` | info | `"step succeeded"` | `&step.ID` |
| `:432` | step → `failed` (attempts exhausted) | error | `"step failed"` | `&step.ID` |
| `:451-452` | step → `retrying` | warn | `"step scheduled for retry"` | `&step.ID` |
| `:502-503` | run → `failed` (stuck, liveness check) | warn | `"run stuck: pending steps with no active path, marked failed"` | `nil` (run-level) |
| `:514` (success path) | run → `succeeded` | info | `"run succeeded"` | `nil` |
| `:514` (failure path) | run → `failed` (normal, not stuck) | error | `"run failed"` | `nil` |

The last two share one call site (`finishRun`'s terminal `UpdateRunStatus`) but need different level/message depending on `status` — same as the existing `c.cfg.Metrics.RunsFinished.WithLabelValues(..., status)` right below it, which already branches the same way.

`Context` (the JSONB field) carries whatever's most useful per site: for step failure, the same `errPayload` already built for `UpdateStepResult`; for retry, `attempt`/`backoff`; for others, `nil`/`{}`. Reuse existing local variables — nothing new needs constructing.

**Helper, not seven inline blocks.** A small private method keeps this from bloating `runStepWithRetry`/`finishRun`:

```go
// appendLog persists a durable log line best-effort — a failure here is logged
// and swallowed, never propagated. Matches c.cfg.Metrics's existing discipline:
// observability must never affect execution.
func (c *Coordinator) appendLog(ctx context.Context, tenantID, runID uuid.UUID, stepRunID *uuid.UUID, level, message string, ctxData any) {
    payload, _ := json.Marshal(ctxData) // nil ctxData marshals to "null"; fine, Context accepts it
    entry := &domain.ExecutionLog{
        TenantID:      tenantID,
        WorkflowRunID: runID,
        StepRunID:     stepRunID,
        Level:         level,
        Message:       message,
        Context:       payload,
    }
    if err := c.logs.Append(ctx, entry); err != nil {
        c.cfg.Logger.Warn("append execution log failed", slog.String("message", message), slog.Any("error", err))
    }
}
```

Each of the 7 sites gets one line, placed immediately after its existing `c.cfg.Logger.Info/Warn(...)` call — same place, same information, now also durable.

---

## 3. TDD Specification

testify · `-race` · extends the existing `coordinator_test.go` fakes. **Confirmed**: `fakeLogs` already exists (`coordinator_test.go:278-280`) but only counts calls (`calls int`) — it does not capture what was logged. Extend it to record entries (`entries []*domain.ExecutionLog`) so the table-driven assertions in this fix can check `Level`/`Message`/`StepRunID`, not just that *a* call happened.

- Each of the 7 transitions appends exactly one `ExecutionLog` with the right `Level`/`Message`/`StepRunID` (`nil` for run-level, set for step-level) — table-driven over the 7 rows in §2.
- A `fakeLogs.Append` returning an error on every call does **not** change any existing assertion in the suite — the coordinator's step/run transitions still succeed identically (mirrors Phase 8 plan's D-4 discipline for `Events`, applied here first since this fix lands before Phase 8).
- Integration (`FLOWFORGE_INTEGRATION=1`): a real run driven end-to-end through the coordinator against live Postgres, then `ListLogs` (Phase 6's existing use case) returns a non-empty, correctly-ordered result — this is the test that actually proves the original bug is fixed, not just that `Append` was called.

---

## 4. Execution Order

1. `coordinator.go`: add `appendLog` helper + 7 call sites.
2. Extend `coordinator_test.go`'s fakes/tests per §3.
3. `internal/execution/repository_test.go` (or wherever Phase 6's `ListLogs` integration test lives): add the end-to-end "run produces readable logs" case.
4. `make ci` and `FLOWFORGE_INTEGRATION=1 make test-integration`, both green.
5. Append an entry to `.agents/memory/action_history.md`.

---

## 5. Definition of Done

- [ ] All 7 transitions produce a durable `execution_logs` row with correct `level`/`message`/`step_run_id`.
- [ ] `logs.Append` failure never fails a step or run transition — proven by test, not just by code inspection.
- [ ] A real run's logs are readable through Phase 6's `GET /api/v1/workflow-runs/{runId}/logs` after this fix — proven end-to-end, closing the loop on the actual user-facing symptom.
- [ ] `make ci` green, `-race` clean.

No `Carried Forward` section — this fix has no scope boundary beyond itself; Phase 8's SSE work reuses these same 7 call sites next, per that plan's own step 3/5.
