# Phase 6 — Remaining Steps (Detailed Execution Plan)

Continuation of [phase_6_workflow_execution_api.md](phase_6_workflow_execution_api.md) (the approved architecture) and the plan-mode file it was saved from. Steps 1–4 are done and verified:

1. ✅ Migration `000004` (`retried_from_run_id`) — applied, verified, both directions tested against the running Postgres.
2. ✅ Audit relocated: `internal/domain/audit.go` + `internal/platform/audit/audit.go`; `internal/workflow`'s 6 call sites updated; `make ci` green.
3. ✅ `internal/platform/redact/` — `RedactPayload`, 8 tests, all green.
4. ✅ `internal/platform/ai/` — `Provider` interface + DeepSeek client, 7 tests including a real timeout race, all green.

This document details steps 5–11, the remaining work, with one correction to the original plan found while re-checking the code just now.

---

## Correction: `internal/execution` does not use mockery

The original plan's step 6 said "update `.mockery.yaml` and regenerate mocks" for the execution package, by analogy with `internal/workflow`. **That analogy is wrong.** Checked directly: `internal/execution/coordinator_test.go` and `usecase_test.go` use **hand-written fakes** (`fakeRunRepo`, `fakeStepRepo`, `fakeWorkflowReader`, `fakeEnqueuer`, `fakeGraph`, `fakeLogs`) — there is no `.mockery.yaml` entry for this package and no `internal/execution/mocks` directory. This is Phase 5's own established convention for this specific package, distinct from `internal/auth`/`internal/workflow`'s mockery+testify-EXPECT style.

**Correction: extend the existing fakes, do not introduce mockery here.** Step 6 is retitled below.

---

## Step 5 (in progress) — `internal/execution/repository.go`

### 5a. Thread `retried_from_run_id` through every full-column-list query
Three places enumerate `workflow_runs` columns explicitly and all three need the new column added, in the same position, or `Scan` will misalign:
- `CreateRun` — `Columns(...)`/`Values(...)` (repository.go:121-127)
- `GetRun` — `Select(...)` + `Scan(...)` (repository.go:139-155)
- `ClaimRun` — raw SQL `SELECT`/`RETURNING` + `Scan(...)` (repository.go:166-179)

### 5b. `CreateRun` becomes idempotent — **signature change**
Current: `CreateRun(ctx, run *domain.WorkflowRun) error`. New: `CreateRun(ctx, run *domain.WorkflowRun) (created bool, err error)`.

```go
query, args, err := psql.Insert("workflow_runs").
    Columns(..., "retried_from_run_id", ...).
    Values(..., run.RetriedFromRunID, ...).
    Suffix("ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING").
    ToSql()
...
tag, err := r.getRunner(ctx).Exec(ctx, query, args...)
...
return tag.RowsAffected() > 0, nil
```
`created=false, err=nil` means a genuine idempotency-key conflict — the caller (usecase) must not create step rows and must not enqueue.

New method for the conflict branch:
```go
FindByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.WorkflowRun, error)
```
Plain `SELECT ... WHERE tenant_id = $1 AND idempotency_key = $2`, same column list as `GetRun`, `ErrRunNotFound` on no rows (shouldn't happen right after a conflict, but the sentinel is the honest response if it somehow does).

### 5c. `ListRuns`
```go
type ListRunsFilter struct {
    TenantID                       uuid.UUID
    WorkflowID                     uuid.UUID // required — this list is always workflow-scoped per api-3.md 10.1.2
    Status, TriggerType            string    // "" = no predicate
    CreatedAtFrom, CreatedAtTo     *time.Time
    Page, PageSize                 int
}

ListRuns(ctx context.Context, f ListRunsFilter) ([]*domain.WorkflowRun, int64, error)
```
`ORDER BY created_at DESC` (api-3.md: "Sorting should default to newest first" — no `sortBy` param on this endpoint, unlike workflow list). Count query mirrors the item query's `WHERE` exactly, same pattern as `internal/workflow`'s `List`.

### 5d. `ReclaimStalePendingRuns` — no UPDATE needed, only a SELECT
```go
ReclaimStalePendingRuns(ctx context.Context, olderThan time.Duration) ([]ReclaimedRun, error)
```
```sql
SELECT id, tenant_id FROM workflow_runs
 WHERE status = 'pending' AND claimed_by IS NULL AND created_at < NOW() - $1::interval
```
Reasoning worth stating in the doc comment: unlike a reclaimed lease, a never-enqueued `pending` run has nothing to reset — it's already sitting in the correct pre-execution state. Re-running this every reaper tick (15s) and re-enqueuing a run that's still stuck is harmless (duplicate delivery is already a documented no-op, matching `Q-7`'s existing behavior) and self-limiting: the moment a worker claims it, `status` flips to `running` and it drops out of this query on the next tick.

### 5e. `StepRunRepository.GetStepRun`
```go
GetStepRun(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error)
```
Same column list/scan as `ListStepRuns`, single row, `WHERE tenant_id=$1 AND workflow_run_id=$2 AND id=$3`. New sentinel `ErrStepNotFound` next to `ErrRunNotFound` (repository.go:22).

### 5f. `StepRunRepository.CloneStepRunsForRetry`
```go
CloneStepRunsForRetry(ctx context.Context, tenantID, newRunID, originalRunID uuid.UUID, nodes []domain.WorkflowNode, onlyFailed bool) error
```
Loads the original run's steps via the existing `ListStepRuns` (reused, not reimplemented), indexed by `node_key`. For each node in `nodes`: if `onlyFailed && orig.Status == domain.StepStatusSucceeded`, insert a fresh row (new ID, new run ID) as `succeeded`, copying `input_payload`, `output_payload`, `attempt_count`, **`started_at`, and `finished_at`** — the two timestamps were the gap the Plan-agent critique caught; a retried run whose reused steps show no timing for work it didn't redo is a real, visible defect in the run detail view. Every other node gets a plain fresh `pending` row, byte-identical to what `CreateStepRuns` already produces.

### 5g. `LogReader` — new interface, same file, next to `LogRepository`
```go
type ListLogsFilter struct {
    TenantID, WorkflowRunID uuid.UUID
    Level, StepRunID        string // StepRunID as string so "" means unfiltered without an extra bool
    Page, PageSize          int
}

type LogReader interface {
    ListLogs(ctx context.Context, f ListLogsFilter) ([]*domain.ExecutionLog, int64, error)
}
```
Implemented by the same `executionRepository` struct. `LogRepository.Append` is untouched — this is purely additive, read-only, API-side.

**Files touched:** `internal/execution/repository.go` only. **Verify:** `go build ./...` after this step — it will fail loudly everywhere `CreateRun`'s old single-return signature is assumed (usecase.go, coordinator_test.go's `fakeRunRepo`), which is expected and picked up in step 7.

---

## Step 6 (retitled) — Extend the existing hand-written fakes

No `.mockery.yaml` changes. In `internal/execution/coordinator_test.go` (where the shared fakes live):

- `fakeRunRepo.CreateRun` → update to the new `(bool, error)` signature (return `(true, nil)` unconditionally unless a test needs otherwise — matches its current unconditional `nil`).
- `fakeRunRepo` gains `ListRuns`, `ReclaimStalePendingRuns`, `FindByIdempotencyKey` — each configurable via a field on the struct (e.g. `listRunsResult`, `idempotencyHit *domain.WorkflowRun`), following the existing `fakeRunRepo`/`fakeStepRepo` style of plain fields read by the method, not a mocking framework.
- `fakeStepRepo` gains `GetStepRun`, `CloneStepRunsForRetry`.
- New `fakeLogReader` (`ListLogs`).
- New `fakeAuditRepo` (`Record`) — records calls in a slice so tests can assert on action/entity type, mirroring `fakeEnqueuer`'s `calls int` counter pattern but capturing the entries themselves since `AnalyzeRun`'s tests need to inspect *what* was audited.
- New `recordingTxRunner` (`ExecuteInTx`) — copy `internal/workflow/usecase_test.go`'s version verbatim (`called bool`, runs `fn(ctx)` inline); this package doesn't have one today because it never needed a transaction boundary before.
- New `fakeAIProvider` (`ai.Provider`) — configurable `response string`/`err error`, plus a `lastSystemPrompt`/`lastUserPrompt` capture so redaction tests can assert a planted secret never reached it.

**Files touched:** `internal/execution/coordinator_test.go` (fake definitions only — no behavioral change to the coordinator itself, confirmed `CreateRun` is called nowhere in `coordinator.go`, only from `usecase.go:109`, so this signature change is fully isolated to the use-case layer).

---

## Step 7 — `internal/execution/usecase.go`

### New/changed sentinels
- `repository.go`: add `ErrStepNotFound` next to `ErrRunNotFound`.
- `usecase.go`: add `ErrRunAlreadyRunning`, `ErrRunAlreadyCompleted`, `ErrAIInvalidResponse`, `ErrAIGenerationFailed` next to the existing `ErrNoPublishedVersion`/`ErrRunNotRunning`/`ErrRunIllegalCancel`.

### Constructor
```go
func NewExecutionUseCase(
    workflows WorkflowReader, runs RunRepository, steps StepRunRepository, logs LogReader,
    graph GraphLoader, queue RunEnqueuer, audit domain.AuditRepository, txRunner TxRunner,
    aiProvider ai.Provider, cfg ExecutionConfig,
) ExecutionUseCase
```
`TxRunner` is a new consumer interface in this file, identical shape to `workflow.TxRunner`. `ExecutionConfig` groups the AI knobs (`AIMaxRetries int`, `AIRequestTimeout time.Duration`) — mirrors `execution.CoordinatorConfig`'s existing pattern of grouping trailing numeric config into one struct rather than growing the positional list further.

**Confirmed zero production call sites today** — only `usecase_test.go` constructs this. Safe to change freely.

### `CreateRun` (rewritten body)
```go
var run *domain.WorkflowRun
err := uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
    // existing validation (tenant/workflow required, published version, load graph) unchanged, stays outside the tx — no writes yet
    created, err := uc.runs.CreateRun(txCtx, candidate)
    if err != nil { return err }
    if !created {
        existing, err := uc.runs.FindByIdempotencyKey(txCtx, cmd.TenantID, *cmd.IdempotencyKey)
        if err != nil { return err }
        run = existing
        return nil // no step creation, no audit for a duplicate — nothing new happened
    }
    if err := uc.steps.CreateStepRuns(txCtx, cmd.TenantID, candidate.ID, nodes); err != nil { return err }
    if err := uc.audit.Record(txCtx, domain.AuditEntry{..., Action: "workflow.run.created", EntityType: "workflow_run"}); err != nil { return err }
    run = candidate
    return nil
})
if err != nil { return nil, err }
if /* was a genuine new create */ {
    if err := uc.queue.EnqueueRun(cmd.TenantID, run.ID); err != nil {
        // logged, not fatal to the request — ReclaimStalePendingRuns recovers it
    }
}
return run, nil
```
The "was a genuine new create" check needs a signal out of the closure (a local `bool` set alongside `run`), since the closure's `error` alone can't distinguish "duplicate, no-op" from "created". Enqueue happens **after** `ExecuteInTx` returns successfully — never inside the closure — so a transaction that rolls back never leaves an orphaned queue message.

### `RetryRun`
```go
func (uc *executionUseCase) RetryRun(ctx context.Context, cmd RetryRunCommand) (*domain.WorkflowRun, error)
```
1. `original, err := uc.runs.GetRun(ctx, cmd.TenantID, cmd.RunID)` → `ErrRunNotFound` propagates.
2. Eligibility switch: `succeeded` → `ErrRunAlreadyCompleted`; `pending`/`running` → `ErrRunAlreadyRunning`; `failed`/`canceled`/`timed_out` → proceed.
3. `nodes, _, err := uc.graph.LoadGraph(ctx, cmd.TenantID, original.WorkflowVersionID)` — **the original run's version, not the workflow's current one.**
4. Inside one `ExecuteInTx`: idempotent-create the new run (`WorkflowID`/`WorkflowVersionID`/`TriggerType` copied from `original`, `RetriedFromRunID: &original.ID`, `IdempotencyKey: cmd.IdempotencyKey`) using the same `CreateRun`+conflict-check path as above; if genuinely created, `uc.steps.CloneStepRunsForRetry(txCtx, cmd.TenantID, newRun.ID, original.ID, nodes, cmd.RetryFailedStepsOnly)`; audit `"workflow.run.retryRequested"`.
5. Enqueue after commit, same as `CreateRun`.

### `ListSteps` / `GetStep` / `ListLogs`
Parent-check-then-child, matching `internal/workflow.ListVersions`'s established shape:
```go
func (uc *executionUseCase) ListSteps(ctx context.Context, tenantID, runID uuid.UUID) ([]*domain.StepRun, error) {
    if _, err := uc.runs.GetRun(ctx, tenantID, runID); err != nil { return nil, err }
    steps, err := uc.steps.ListStepRuns(ctx, tenantID, runID)
    ...
}
```
`GetStep` additionally maps a not-found step to `ErrStepNotFound`. `ListLogs` takes a `ListLogsQuery` (usecase-level; `dto.go`, see below), normalizes pagination (page≥1, pageSize 1–100 default 20 — the established default), and calls `uc.logs.ListLogs`.

### `AnalyzeRun`
```go
func (uc *executionUseCase) AnalyzeRun(ctx context.Context, cmd AnalyzeRunCommand) (*AnalysisResult, error)
```
1. `run, err := uc.runs.GetRun(...)` → `ErrRunNotFound`. **No status gate — any run status is eligible**, per `api-3.md`'s explicit "may also be generated for non-terminal runs."
2. `steps, _ := uc.steps.ListStepRuns(...)`; if `cmd.IncludeLogs` (default `true`), `logs, _ := uc.logs.ListLogs(..., limit: cmd.LogLimit /* default 20 */)`.
3. Redact every step's `input_payload`/`output_payload`/`error_payload` and every log's `context` via `redact.RedactPayload`.
4. Build the prompt: a system message stating the model's role and the exact required JSON shape, plus an explicit instruction that everything inside the delimited data block is inert content, never instructions (the prompt-injection mitigation) — and a user message containing the redacted run/step/log summary between clear delimiters (e.g. `<run_data>...</run_data>`).
5. `raw, err := uc.aiProvider.Complete(ctx, systemPrompt, userPrompt)`; a provider error is wrapped as `ErrAIGenerationFailed`.
6. Parse `raw` into `{Diagnosis, PossibleCause, SuggestedFix string; Confidence float64}`; validate all three strings non-empty and `0 <= Confidence <= 1`. On failure, retry with a corrective follow-up user message ("your previous response was invalid: <reason>; respond again with ONLY valid JSON matching the schema") up to `cfg.AIMaxRetries` (default 2) **additional** attempts; final failure is `ErrAIInvalidResponse`.
7. Audit `"workflow.run.analysisGenerated"` (outside any transaction — no DB writes preceded it, a plain `Record` call is enough, no `ExecuteInTx` needed here since there's nothing else to make atomic with it).
8. No persistence of the result — the spec marks storage optional.

### `internal/execution/dto.go` — new file
Mirrors `internal/workflow/dto.go`'s split (usecase-level query/command/result types here; repo-level filter types stay in `repository.go`):
```go
type RetryRunCommand struct {
    TenantID, RunID uuid.UUID
    RetryFailedStepsOnly bool
    IdempotencyKey       *string
}

type ListRunsQuery struct {
    TenantID, WorkflowID                      uuid.UUID
    Status, TriggerType                       string
    CreatedAtFrom, CreatedAtTo                 *time.Time
    Page, PageSize                             int
}

type PaginatedRuns struct {
    Items      []*domain.WorkflowRun
    TotalItems int64
    Page, PageSize int
}

type ListLogsQuery struct {
    TenantID, RunID uuid.UUID
    Level, StepRunID string
    Page, PageSize   int
}

type PaginatedLogs struct {
    Items      []*domain.ExecutionLog
    TotalItems int64
    Page, PageSize int
}

type AnalyzeRunCommand struct {
    TenantID, RunID uuid.UUID
    IncludeLogs     bool // default true, set by the handler
    LogLimit        int  // default 20, set by the handler
}

type AnalysisResult struct {
    RunID                                      uuid.UUID
    Diagnosis, PossibleCause, SuggestedFix      string
    Confidence                                  float64
}

type ExecutionConfig struct {
    AIMaxRetries     int
    AIRequestTimeout time.Duration
}
```

### TDD — `internal/execution/usecase_test.go` additions
- Idempotent `CreateRun`: duplicate key → `FindByIdempotencyKey`'s result returned, `CreateStepRuns`/`Record`/`EnqueueRun` **not called** (assert via the fakes' call counters).
- `CreateRun` transaction: `recordingTxRunner.called == true`; a mid-tx failure (fake `CreateStepRuns` errors) leaves `EnqueueRun` uncalled.
- `RetryRun` eligibility table: `succeeded`→`ErrRunAlreadyCompleted`, `pending`/`running`→`ErrRunAlreadyRunning`, `failed`/`canceled`/`timed_out`→proceeds.
- `RetryRun` reuses `original.WorkflowVersionID`, not the workflow's current version (fake `WorkflowReader` returns a *different* current version to make this observable).
- `RetryRun` with `RetryFailedStepsOnly=true`: assert `CloneStepRunsForRetry` was called with `onlyFailed=true` and the right `originalRunID`.
- `ListSteps`/`GetStep`/`ListLogs`: parent-not-found propagates before the child fake is ever called; tenant isolation via a run owned by a different tenant.
- `AnalyzeRun`: `fakeAIProvider.lastUserPrompt` never contains a secret planted in a step's `output_payload`; malformed-JSON response retried then `ErrAIInvalidResponse` after `AIMaxRetries` is exhausted; confidence `1.5` takes the same path; provider error → `ErrAIGenerationFailed`; any run status (not just `failed`) is accepted.
- `ReclaimStalePendingRuns`-driven scenarios belong to the repository's integration tests (step 5), not here — this package's usecase tests are pure/mocked.

**Files touched:** `internal/execution/usecase.go`, `internal/execution/dto.go` (new), `internal/execution/usecase_test.go`, `internal/execution/repository.go` (the two new sentinels).

---

## Step 8 — `internal/execution/handler.go` + `httpx` codes

### `internal/platform/httpx/httpx.go` — new constants
`CodeRunNotFound = "RUN_NOT_FOUND"`, `CodeRunAlreadyRunning = "RUN_ALREADY_RUNNING"`, `CodeRunAlreadyCompleted = "RUN_ALREADY_COMPLETED"`, `CodeStepNotFound = "STEP_NOT_FOUND"`, `CodeAIInvalidResponse = "AI_INVALID_RESPONSE"`, `CodeAIGenerationFailed = "AI_GENERATION_FAILED"`.

### `internal/execution/handler.go` — new file
Mirrors `internal/workflow/handler.go` exactly (`MaxBytesReader` on bodies, `uuid.Parse` + 400 on path params, one `handleError` dispatcher):

| Handler | Method/Path | Success |
|---|---|---|
| `TriggerRun` | `POST /api/v1/workflows/{workflowId}/runs` | 202 |
| `ListRuns` | `GET /api/v1/workflows/{workflowId}/runs` | 200 |
| `GetRun` | `GET /api/v1/workflow-runs/{runId}` | 200 |
| `CancelRun` | `POST /api/v1/workflow-runs/{runId}/cancel` | 202 |
| `RetryRun` | `POST /api/v1/workflow-runs/{runId}/retry` | 202 |
| `ListSteps` | `GET /api/v1/workflow-runs/{runId}/steps` | 200 |
| `GetStep` | `GET /api/v1/workflow-runs/{runId}/steps/{stepRunId}` | 200 |
| `ListLogs` | `GET /api/v1/workflow-runs/{runId}/logs` | 200 |
| `AnalyzeRun` | `POST /api/v1/workflow-runs/{runId}/analysis` | 200 |

`handleError` dispatch table (one `switch`/`errors.Is` chain, matching `workflow.WorkflowHandler.handleError`'s shape):

| Error | Status | Code |
|---|---|---|
| `ErrRunNotFound` | 404 | `RUN_NOT_FOUND` |
| `ErrStepNotFound` | 404 | `STEP_NOT_FOUND` |
| `ErrRunAlreadyRunning` | 409 | `RUN_ALREADY_RUNNING` |
| `ErrRunAlreadyCompleted` | 409 | `RUN_ALREADY_COMPLETED` |
| `ErrRunIllegalCancel` | 409 | `RUN_ALREADY_COMPLETED` |
| `ErrNoPublishedVersion` | 409 | `WORKFLOW_VERSION_CONFLICT` (existing code, no new one invented) |
| `ErrAIInvalidResponse` | 409 | `AI_INVALID_RESPONSE` |
| `ErrAIGenerationFailed` | 503 | `AI_GENERATION_FAILED` |
| default | 500 | `INTERNAL_SERVER_ERROR` |

### TDD — `internal/execution/handler_test.go` — new file
Table-driven per the dispatch table above; RBAC (`AnalyzeRun` allows `viewer`, everything else follows read=`admin,editor,viewer`/write=`admin,editor`); the two path namespaces route correctly (`POST .../runs` under `/workflows/{id}` vs everything else under `/workflow-runs/{id}`, no `workflowId` in scope for the latter); tenant isolation on every read; malformed body/UUID → 400.

**Files touched:** `internal/platform/httpx/httpx.go`, `internal/execution/handler.go` (new), `internal/execution/handler_test.go` (new).

---

## Step 9 — Wiring

### `cmd/api/main.go`
Inside the existing `if dbPool != nil` block, after `uow`/`rClient` are already built:
```go
execRepo := execution.NewExecutionRepository(dbPool)
queueClient := queue.NewClient(rClient)          // same rClient already built for auth — no second connection
auditRepo2 := audit.NewAuditRepository(dbPool)   // reuse the audit package now shared with workflow
aiProvider := ai.NewDeepSeekProvider(ai.Config{
    APIKey: cfg.AIProviderAPIKey, Model: cfg.AIModel, BaseURL: cfg.AIBaseURL, RequestTimeout: cfg.AIRequestTimeout,
})
execUC := execution.NewExecutionUseCase(
    wfRepo /* already built, satisfies WorkflowReader */, execRepo, execRepo, execRepo,
    execRepo /* GraphLoader — check: does workflow.VersionRepository already satisfy this, or does execution need workflow's verRepo? See note below */,
    queueClient, auditRepo2, uow, aiProvider,
    execution.ExecutionConfig{AIMaxRetries: cfg.AIMaxRetries, AIRequestTimeout: cfg.AIRequestTimeout},
)
executionHandler := execution.NewExecutionHandler(execUC)
```
**Note to resolve during implementation, not before:** `GraphLoader.LoadGraph` is currently satisfied by `workflow.NewVersionRepository` in the worker's wiring (confirm by reading `cmd/worker/main.go`'s existing construction) — the API process's `verRepo` (already built for `workflowHandler`) should satisfy the same interface structurally; if so, this is a zero-cost reuse, not a new repository instance.

`NewRouter` gains an `executionHandler *execution.ExecutionHandler` parameter and the 9 routes, `if executionHandler != nil && authMiddleware != nil { ... }`.

### `internal/platform/config/config.go`
```go
AIProviderAPIKey  string        // getEnv("AI_PROVIDER_API_KEY", "")
AIModel           string        // getEnv("AI_MODEL", "deepseek-chat")
AIBaseURL         string        // getEnv("AI_BASE_URL", "https://api.deepseek.com")
AIRequestTimeout  time.Duration // getEnvDuration("AI_REQUEST_TIMEOUT", 30*time.Second)
AIMaxRetries      int           // getEnvInt("AI_MAX_RETRIES", 2)
```

### `cmd/worker/main.go`
`reaperLoop` gains a second reclaim call alongside the existing one:
```go
if stale, err := runs.ReclaimStalePendingRuns(ctx, 30*time.Second); err == nil {
    for _, r := range stale {
        _ = qc.EnqueueRun(r.TenantID, r.RunID)
    }
}
```
Same ticker, same loop — no new goroutine.

**Files touched:** `cmd/api/main.go`, `cmd/worker/main.go`, `internal/platform/config/config.go`.

---

## Step 10 — Verification

1. `make ci` (`fmt-check`, `vet`, `build`, `test -race -count=1`) green.
2. `FLOWFORGE_INTEGRATION=1 make test-integration` green against the already-running Postgres/Redis (confirmed reachable, migration `000004` already applied and confirmed reversible).
3. New integration coverage in `internal/execution/repository_test.go`: `ListRuns` filtering/pagination; `ReclaimStalePendingRuns` picks up a genuinely stale row and ignores a fresh one; `FindByIdempotencyKey` after a real `ON CONFLICT` no-op; `CloneStepRunsForRetry` produces the right mix of seeded-`succeeded`/fresh-`pending` rows against real `step_runs`.
4. Manual smoke (documented, not necessarily run live given no real DeepSeek key is configured in this environment): trigger twice with the same `Idempotency-Key` → one row; retry a failed run with `retryFailedStepsOnly: true` → only the failed step re-executes; analysis on a run with a planted secret in step output → response never echoes it (checkable even against `fakeAIProvider` in unit tests, and structurally guaranteed by redaction running before the real provider is ever reached).

## Step 11 — Records
Append a Phase 6 completion entry to `.agents/memory/action_history.md` (what shipped, the mockery correction, the two fixed hazards — enqueue ordering and the audit layering smell — and anything carried forward). This plan file and the architecture plan both stay in `.agents/plans/`.
