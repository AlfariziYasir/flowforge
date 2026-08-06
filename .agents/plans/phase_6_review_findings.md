# Phase 6 (Remaining Steps 5–11) — Review Findings

Review of the execution of [phase_6_remaining_steps.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_6_remaining_steps.md).

**Verdict: 🔴 REJECTED — 2 high, 3 medium, 3 low** (Z-1 … Z-8)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` empty · `go test ./... -race -count=1` clean · `FLOWFORGE_INTEGRATION=1` suite green (Postgres/Redis both healthy).

> [!CAUTION]
> **Z-1 and Z-2 both undermine the execution API's primary write path.** Triggering a run through the API exactly as `api-3.md` §10.1.1 documents it silently discards the caller's input and always returns 200 instead of the documented 202 — no error, no 422, just data loss and a wrong status code. Neither is caught by the test suite because the tests exercise the implementation's own field names and never assert the trigger/cancel/retry success status code.

---

## What Was Done Right — and it is substantial

- **All integration coverage genuinely exercised.** `ListRuns` filtering/pagination, `ReclaimStalePendingRuns` stale-vs-fresh, `FindByIdempotencyKey` after a real `ON CONFLICT`, `CloneStepRunsForRetry`'s timing-preserving clone, and a real E2E usecase smoke (duplicate idempotency key, retry lineage) — all ran against live Postgres, not mocks.
- **`runColumns` shared projection** (repository.go:149-154) is the right fix for the exact class of bug the plan warned about ("three Scan sites can't misalign") — `CreateRun`, `GetRun`, `ClaimRun`, `FindByIdempotencyKey`, and `ListRuns` all read the same slice.
- **Idempotent `CreateRun` is correctly wired to a real partial unique index.** `ON CONFLICT (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING` matches `uq_workflow_runs_tenant_idempotency` (already present since migration `000001`) exactly — arbiter inference succeeds, verified live.
- **Enqueue-after-commit is real**, not just commented: `CreateRun`/`RetryRun` call `uc.queue.EnqueueRun` strictly after `ExecuteInTx` returns, never inside the closure — confirmed by reading both usecase methods end to end.
- **`CloneStepRunsForRetry` correctly preserves timing** on reused succeeded steps (`orig.StartedAt`/`orig.FinishedAt`), closing the gap the plan's own Phase-agent critique flagged.
- **The `WorkflowReader` correction was real and necessary** — `workflow.WorkflowRepository` genuinely does not have `GetWorkflow` (it has `FindByID`); reusing the already-built `wfUC` instead is correct and adds no new instance.
- **`ExecuteInTx` failure correctly leaves the run un-enqueued** — verified by `TestExecutionUseCase_CreateRun/a_mid-transaction_failure_leaves_the_run_un-enqueued`.
- **Redaction genuinely runs before the AI provider is reached** — `analysisUserPrompt` calls `redact.RedactPayload` on every step and log field before the prompt is built, and `TestExecutionUseCase_AnalyzeRun/planted_secret_never_reaches_the_provider` proves it via `fakeAIProvider.lastUserPrompt`.
- **RBAC matches the plan's table exactly** for all 9 routes, verified directly in `cmd/api/main.go:166-174`.

---

## 🔴 High

### Z-1: TriggerRun, CancelRun, and RetryRun all return 200, not the documented 202

[handler.go:82](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L82) · [handler.go:171](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L171) · [handler.go:205](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L205) · [httpx.go:94-101](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/httpx/httpx.go#L94-L101)

The plan's own Step 8 table specifies 202 for `TriggerRun`, `CancelRun`, and `RetryRun`; `api-3.md` §10.1.1/10.1.4/10.1.5 document the same. `httpx.go` only defines `OK` (200), `Created` (201), and `NoContent` (204) — **no `Accepted` helper was ever added**, and all three handlers call `httpx.OK`.

Confirmed live:

```
POST /api/v1/workflows/{id}/runs  →  status=200   (expected 202)
```

Not caught because no test asserts the success-path status code for any of the three — `TestExecutionHandler_Validation` only checks the 400/401 error paths, and `TestExecutionHandler_Routes` only checks that routes are registered (`assert.NotEqual(t, http.StatusNotFound, ...)`), not what they return on success.

Async accept-and-queue semantics exist specifically so a client can distinguish "your request is durably recorded and will run" from "here is the finished resource" — collapsing both to 200 is a real, if quiet, contract break for any client written against the documented API.

### Z-2: TriggerRun's request body field names don't match the documented contract — client input is silently discarded

[handler.go:25-28](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L25-L28) · [api-3.md:52-62](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/docs/api-3.md#L52-L62)

`api-3.md` §10.1.1 documents the request body as:

```json
{ "input": {...}, "triggerSource": "manual" }
```

`triggerRunRequest` decodes `TriggerType string \`json:"triggerType"\`` and `InputContext json.RawMessage \`json:"inputContext"\``. Go's `json.Decoder` silently ignores unmatched keys — it does not error.

Confirmed live: POSTing the **exact body from the spec's own example** —

```json
{"input":{"leadEmail":"a@b.com"},"triggerSource":"manual"}
```

— reaches `CreateRunCommand` as `TriggerType=""` and `InputContext=` (empty). The usecase's own defaulting (`triggerType = "manual"`, `input = {}}`) then silently masks the loss: the request returns 200 (also wrong, see Z-1) with a run that looks entirely normal, but the caller's `leadEmail` never reached the run. Any workflow whose steps interpolate `{{input.leadEmail}}` executes against an empty object instead — a silent, hard-to-diagnose production failure, not a crash.

Not caught because `handler_test.go` never posts a trigger body containing `"input"` or `"triggerSource"` — every test that exercises `TriggerRun`'s success path (there is exactly one, `TestProbe_...` written for this review; the shipped suite has none) uses the implementation's own field names or omits the body checks entirely.

---

## 🟡 Medium

### Z-3: `ListRuns` never checks the parent workflow exists

[usecase.go:296-312](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go#L296-L312)

`api-3.md` §10.1.2 documents `404 WORKFLOW_NOT_FOUND` as an error response for `GET /api/v1/workflows/{workflowId}/runs`. `ListRuns` goes straight to `uc.runs.ListRuns(...)` with no `uc.workflows.GetWorkflow` call first — unlike `internal/workflow.ListVersions`, which the plan itself cites as the shape to follow for parent-child list endpoints (it does call `wfRepo.FindByID` first).

Confirmed live: constructing the usecase with a `WorkflowReader` that always errors and calling `ListRuns` with a random `workflowId` still returns `200 {items: [], totalItems: 0}, err: nil` — the workflow reader is never consulted for this endpoint. A caller cannot distinguish "this workflow has no runs yet" from "this workflow does not exist" or "this workflow belongs to someone else."

### Z-4: Step JSON responses don't match the documented shape

[handler.go:227-238](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L227-L238) · [execution.go:54](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/execution.go#L54) · [api-3.md:621-706](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/docs/api-3.md#L621-L706)

`api-3.md` §10.2.1/10.2.2 document both `ListSteps` and `GetStep` responses with `"stepRunId"` and `"workflowNodeId"` fields. In this codebase:

- `ListSteps`'s hand-built view map uses `"id"` (handler.go:228), not `"stepRunId"`, and never includes `workflowNodeId` at all.
- `GetStep` marshals `*domain.StepRun` directly, whose struct tag is `WorkflowNodeID uuid.UUID \`json:"-"\`` — confirmed live, the field never appears in the JSON regardless of value. Its `ID` field is tagged `json:"id"`, not `stepRunId`.

`WorkflowNodeID`'s `json:"-"` tag was reasonable when `domain.StepRun` had no HTTP-facing consumer (Phase 5). This phase is the first to marshal the struct directly to a client, and nobody re-checked the tag against `api-3.md`'s documented shape before doing so.

### Z-8: RetryRun reads `Idempotency-Key` from the body; TriggerRun reads it from the header

[handler.go:30-33](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L30-L33) · [handler.go:65-68](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L65-L68) · [api-3.md:491-501](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/docs/api-3.md#L491-L501)

`TriggerRun` correctly reads `r.Header.Get("Idempotency-Key")`. `RetryRun` instead decodes `idempotencyKey` out of the **JSON body** (`retryRunRequest.IdempotencyKey`). `api-3.md` §10.1.5 documents the same `Headers: Idempotency-Key` convention for retry as for trigger. A client sending the header as documented (and no body field, since the spec's example body is only `{"retryFailedStepsOnly": false}`) gets no idempotency protection on retry at all — `cmd.IdempotencyKey` stays `nil`, so every retry request creates a fresh run even when the client intended a de-duplicated retry.

---

## 🟢 Low

### Z-5: RetryRun's response shape doesn't match `api-3.md`'s documented fields

[handler.go:205](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L205) · [api-3.md:549-562](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/docs/api-3.md#L549-L562)

The spec's success response is `{runId, originalRunId, status}`. `RetryRun` returns the full `newRunView` (which includes `retriedFromRunId`, not `originalRunId`, plus several fields the spec doesn't mention). Extra fields are harmless; the missing `originalRunId` key means a client following the documented contract can't read the retry's lineage from this response at all — it would have to fall back to `retriedFromRunId`, an undocumented name.

### Z-6: Enqueue failures on `CreateRun`/`RetryRun` are entirely unlogged

[usecase.go:173-179](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go#L173-L179) · [usecase.go:252-256](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/usecase.go#L252-L256)

Both call sites swallow `uc.queue.EnqueueRun`'s error with a comment ("logged, not fatal") but `executionUseCase` carries no logger — there is nothing to log with. Contrast with the worker's `reaperLoop`, which logs every reclaim/re-enqueue outcome explicitly. An operator has zero visibility into a Redis blip on the trigger path; the run silently sits `pending` for up to 30s (the `ReclaimStalePendingRuns` threshold) with no signal anything went wrong, and no signal even after it recovers.

### Z-7: RetryRun and AnalyzeRun don't distinguish an oversized body from malformed JSON

[handler.go:188-192](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L188-L192) · [handler.go:313-317](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L313-L317)

`TriggerRun` checks `errors.As(err, &maxBytesErr)` and returns 413 for a body over 1MB. `RetryRun` and `AnalyzeRun` wrap their bodies with the same `http.MaxBytesReader` but their decode-error branches return a flat 400 regardless of cause — a legitimately oversized retry/analysis request looks identical to a malformed one.

---

## Notes, not findings

- **The generic `idempotency_keys` table (migration `000001`) is unused by this feature.** `CreateRun`/`RetryRun` dedupe via a column + partial unique index on `workflow_runs` directly, not via the separate `idempotency_keys` table `api-3.md`'s "Database Operations" sections list ("Insert into `idempotency_keys` when used"). Both approaches are individually sound and the column approach is arguably simpler; this predates Phase 6 (the table has been unused since Phase 2/3) and isn't something this round introduced, so it's not scored as a finding — but it means the spec's documented "Database Operations" bullet for `idempotency_keys` inserts is never true for any endpoint in this codebase.
- **`platform/audit` has no test file.** It's a near-verbatim reuse of the already-tested `postgres.BaseRepository[T].Create`, so the risk is low, but it's worth a smoke test given it's now shared infrastructure between `internal/workflow` and `internal/execution`.

---

## Remediation

See [phase_6_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_6_review_remediation_plan.md). Z-1 and Z-2 both sit on the trigger endpoint and should land together; Z-3/Z-4/Z-8 are independent contract fixes.
