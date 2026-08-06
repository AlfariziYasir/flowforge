# Implementation Plan — Phase 6 Review Remediation

Closes the 8 findings in [phase_6_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_6_review_findings.md) — 2 high (Z-1, Z-2), 3 medium (Z-3, Z-4, Z-8), 3 low (Z-5, Z-6, Z-7).

**Z-1 and Z-2 both live in `TriggerRun` and should land in the same change** — fixing the status code without fixing the field names (or vice versa) still leaves the endpoint broken against the documented contract.

---

## Phase AJ — Trigger Endpoint Contract (Z-1, Z-2)

### Step 1 — tests first, they must be RED

#### [MODIFY] [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler_test.go)

- ★ **`TestTriggerRun_AcceptsDocumentedRequestShape`** — POST the exact body from `api-3.md`'s own example (`{"input": {"leadEmail": "user@example.com", "leadName": "Jane Doe"}, "triggerSource": "manual"}`) against a capturing stub; assert the usecase receives `InputContext` containing `leadEmail` and `TriggerType == "manual"`. Needs the stub's `CreateRun` to capture its `cmd` argument — add a `lastCreateCmd execution.CreateRunCommand` field to `stubUseCase` and set it in `CreateRun`, mirroring `lastTenant`/`lastRunID`'s existing pattern. Red today: both come back empty/zero.

- ★ **`TestTriggerCancelRetry_Return202`** — table over the three routes (`TriggerRun`, `CancelRun`, `RetryRun`); assert `http.StatusAccepted`. Red today: all three are 200.

### Step 2 — the fix

#### [MODIFY] [httpx.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/httpx/httpx.go)

Add the missing helper next to `Created`:

```go
func Accepted(w http.ResponseWriter, data any) {
    writeJSON(w, http.StatusAccepted, Envelope{
        Success: true,
        Data:    data,
        Meta:    nil,
        Error:   nil,
    })
}
```

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go)

- **Z-1** — swap `httpx.OK` → `httpx.Accepted` at the three success sites: `TriggerRun` (line 82), `CancelRun` (line 171), `RetryRun` (line 205). `GetRun`, `ListRuns`, `ListSteps`, `GetStep`, `ListLogs`, `AnalyzeRun` stay `httpx.OK` — they return a resource or a computed result synchronously, not an accepted-for-later-processing acknowledgment.

- **Z-2** — align `triggerRunRequest` with `api-3.md` §10.1.1:

  ```go
  type triggerRunRequest struct {
      TriggerSource string          `json:"triggerSource"`
      Input         json.RawMessage `json:"input"`
  }
  ```

  Update the two read sites accordingly (`req.TriggerSource` → `CreateRunCommand.TriggerType`, `req.Input` → `CreateRunCommand.InputContext`). Internal field names (`CreateRunCommand.TriggerType`/`InputContext`, `domain.WorkflowRun.TriggerType`) are usecase/domain vocabulary and stay as-is — only the **wire** struct's JSON tags need to match the documented contract. Validate `triggerSource`, when present, is `"manual"` per the spec's validation rule ("must be `manual` for MVP if explicitly supplied"); anything else is `422 VALIDATION_ERROR` via the existing `httpx.CodeValidationError` pattern used elsewhere in the codebase (grep `internal/workflow/handler.go` for the established shape).

### Verification — Phase AJ

```
go test ./internal/execution/ -run 'TestTriggerRun_AcceptsDocumentedRequestShape|TestTriggerCancelRetry_Return202' -v
# both green

# mutation: revert httpx.Accepted back to httpx.OK on TriggerRun only
# TestTriggerCancelRetry_Return202 MUST fail

# mutation: revert triggerRunRequest's json tags to "inputContext"/"triggerType"
# TestTriggerRun_AcceptsDocumentedRequestShape MUST fail
```

---

## Phase AK — List/Get Contract Fixes (Z-3, Z-4, Z-8)

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go)

- **Z-3** — `ListRuns` parent-checks the workflow first, matching `ListVersions`'s shape:

  ```go
  func (uc *executionUseCase) ListRuns(ctx context.Context, q ListRunsQuery) (*PaginatedRuns, error) {
      if _, err := uc.workflows.GetWorkflow(ctx, q.TenantID, q.WorkflowID); err != nil {
          return nil, err
      }
      ...
  }
  ```

  `WorkflowReader.GetWorkflow` already returns a not-found sentinel the handler's `handleError` must learn to map — see below.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go)

- **Z-3** — `handleError` needs a case for whatever `workflow.GetWorkflow`'s not-found sentinel is (check `internal/workflow`'s existing `ErrWorkflowNotFound`, likely already exported and usable via `errors.Is`) mapping to `404 httpx.CodeWorkflowNotFound`, matching `api-3.md`'s documented error for this endpoint. Add a test case to `TestExecutionHandler_ErrorDispatch`'s table.

- **Z-4** — fix both step response shapes to match `api-3.md` §10.2.1/10.2.2:

  ```go
  // ListSteps's view map:
  views = append(views, map[string]any{
      "stepRunId":      s.ID,
      "workflowNodeId": s.WorkflowNodeID,
      "nodeKey":        s.NodeKey,
      "status":         s.Status,
      "attemptCount":   s.AttemptCount,
      "input":          s.InputPayload,
      "output":         s.OutputPayload,
      "error":          s.ErrorPayload,
      "startedAt":      s.StartedAt,
      "finishedAt":     s.FinishedAt,
  })
  ```

  `GetStep` currently marshals `*domain.StepRun` directly — the cleanest fix is a small `stepView` type in `handler.go` (mirroring `newRunView`'s pattern) used by **both** `ListSteps` and `GetStep`, so the two endpoints can't drift again:

  ```go
  func newStepView(s *domain.StepRun) map[string]any {
      return map[string]any{
          "stepRunId": s.ID, "workflowNodeId": s.WorkflowNodeID, "nodeKey": s.NodeKey,
          "status": s.Status, "attemptCount": s.AttemptCount,
          "inputPayload": s.InputPayload, "outputPayload": s.OutputPayload, "errorPayload": s.ErrorPayload,
          "startedAt": s.StartedAt, "finishedAt": s.FinishedAt,
      }
  }
  ```

  Do **not** change `domain.StepRun`'s own `json:"-"` tag on `WorkflowNodeID` — that struct is also used internally (coordinator, repository scan targets) where the extra field is unwanted noise; fix it at the handler's view boundary, the same layering `newRunView` already establishes for runs.

- **Z-8** — `RetryRun` reads the idempotency key from the header, matching `TriggerRun`:

  ```go
  var idemKey *string
  if v := r.Header.Get("Idempotency-Key"); v != "" {
      idemKey = &v
  }
  ```

  Drop `IdempotencyKey` from `retryRunRequest` — it was never documented as a body field.

### TDD — `internal/execution/handler_test.go` additions

- **Z-3**: `ListRuns` for a nonexistent `workflowId` → 404 `WORKFLOW_NOT_FOUND`, not 200. Needs `stubUseCase`'s `ListRuns` to actually propagate a configurable error (check the current stub — if it always returns success, add an `listRunsErr` field).
- **Z-4**: assert both `ListSteps` and `GetStep` responses contain `stepRunId` and `workflowNodeId` keys with the right values; assert neither contains a bare `id` key.
- **Z-8**: `RetryRun` with `Idempotency-Key` header set and no body field reaches the usecase with a non-nil `IdempotencyKey`; a body-only `idempotencyKey` (no header) is now ignored — assert the usecase receives `nil`.

### Verification — Phase AK

```
go test ./internal/execution/... -race -count=1

# mutation: remove the workflows.GetWorkflow call from ListRuns
# the new Z-3 test MUST fail

# mutation: revert newStepView to use "id" instead of "stepRunId"
# the new Z-4 test MUST fail
```

---

## Phase AL — Low-Severity Cleanup (Z-5, Z-6, Z-7)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go)

- **Z-5** — `RetryRun`'s success response includes `originalRunId`: `map[string]any{"runId": run.ID, "originalRunId": run.RetriedFromRunID, "status": run.Status}` per `api-3.md`'s minimal documented shape, rather than the full `newRunView`. (Optional: keep the extra fields alongside `originalRunId` if the team prefers a richer response — the missing key is the actual defect, not the extra ones.)

- **Z-7** — `RetryRun` and `AnalyzeRun` gain the same `errors.As(err, &maxBytesErr)` → 413 branch `TriggerRun` already has. Worth extracting a small `decodeJSONBody[T any](w, r) (T, error)` helper at this point, since it would now be duplicated three times identically — judgment call, not required.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go), [NewExecutionUseCase constructor](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go#L69)

- **Z-6** — add a `Logger *slog.Logger` field to `ExecutionConfig` (defaulting to `slog.Default()` in the constructor, matching `CoordinatorConfig`'s existing pattern from Phase 5). Log the swallowed enqueue error in both `CreateRun` and `RetryRun`:

  ```go
  if err := uc.queue.EnqueueRun(cmd.TenantID, run.ID); err != nil {
      uc.cfg.Logger.Warn("enqueue failed after create, relying on stale-pending reclaim",
          slog.String("runID", run.ID.String()), slog.Any("error", err))
  }
  ```

  Update `cmd/api/main.go`'s `execution.ExecutionConfig{...}` construction to pass the process logger.

### Verification — Phase AL

```
make ci
grep -n "originalRunId" internal/execution/handler.go   # present
grep -n "MaxBytesError" internal/execution/handler.go    # 3 occurrences (Trigger, Retry, Analyze)
```

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **AJ — Trigger contract** | Z-1, Z-2 | 2 ★ tests red → green; both mutations fail |
| AK — List/get contract | Z-3, Z-4, Z-8 | New tests green; both mutations fail |
| AL — Low-severity cleanup | Z-5, Z-6, Z-7 | `make ci` green |

After each phase: `make ci` **and** `FLOWFORGE_INTEGRATION=1 make test-integration`, both green, **before** the execution entry is appended to `.agents/memory/action_history.md`.

Guarantees that must not be lost:

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestExecutionUseCase_E2EIdempotentTriggerAndRetry -count=1
# idempotent create + retry lineage — the phase's headline behavior

go test ./internal/execution/... -run 'TestExecutionHandler_ErrorDispatch|TestExecutionHandler_Routes' -v
# RBAC and error-dispatch table stay intact after the handler edits
```

---

## Notes for Phase 7

- **Every other endpoint's request/response shape should get the same spec cross-check this round skipped** — Z-2 and Z-4 both happened because the field names were invented from the plan's prose description rather than checked against `api-3.md`'s literal JSON examples. Worth a standing habit: when a plan step says "mirrors X's shape," diff the actual JSON against the spec's example block before calling it done, not just against the plan's own summary.
- Z-8's fix removes `retryRunRequest.IdempotencyKey` — if any client integration test or fixture posts it in the body, it will now be silently ignored rather than erroring (JSON decode still succeeds on an unknown-but-typed-away field). Worth a deprecation note if this ships to anyone already integrating against the old shape.
- The `idempotency_keys` table's continued disuse (noted, not scored, in the findings doc) should get an explicit decision recorded somewhere before Phase 7 adds another idempotent endpoint and reintroduces the same "which mechanism" question.
