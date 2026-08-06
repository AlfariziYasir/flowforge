# Execution Record — Phase 6 Review Remediation (Z-1 … Z-8)

Plan: [phase_6_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_6_review_remediation_plan.md). **Status: DONE — both suites green, all mutations verified to fail.**

## Phase AJ — Trigger Endpoint Contract (Z-1, Z-2)
- **Z-1**: new `httpx.Accepted` helper (202); `TriggerRun`, `CancelRun`, `RetryRun` now return 202. `GetRun`/`ListRuns`/`ListSteps`/`GetStep`/`ListLogs`/`AnalyzeRun` stay 200.
- **Z-2**: `triggerRunRequest` wire shape aligned to `api-3.md` §10.1.1 — `triggerSource` → `TriggerType`, `input` → `InputContext`. A non-`manual` `triggerSource` (when supplied) is 422 `VALIDATION_ERROR`.
- ★ RED tests: `TestTriggerRun_AcceptsDocumentedRequestShape` (spec body reaches the usecase intact) and `TestTriggerCancelRetry_Return202`.
- **Mutations verified**: revert TriggerRun to 200 → 202 test fails; revert wire tags → shape test fails.

## Phase AK — List/Get Contract Fixes (Z-3, Z-4, Z-8)
- **Z-3**: `ListRuns` parent-checks the workflow first (`uc.workflows.GetWorkflow`); `handleError` maps `workflow.ErrWorkflowNotFound` → 404 `WORKFLOW_NOT_FOUND`.
- **Z-4**: shared `newStepView` (used by both `ListSteps` and `GetStep`) exposing `stepRunId` + `workflowNodeId`; `domain.StepRun`'s `json:"-"` tag left intact — fixed at the view boundary.
- **Z-8**: `RetryRun` reads `Idempotency-Key` from the header; the body `idempotencyKey` field was removed.
- **Mutations verified**: remove the parent check → usecase Z-3 test fails; revert to bare `id` → step-shape test fails.

## Phase AL — Low-Severity Cleanup (Z-5, Z-6, Z-7)
- **Z-5**: `RetryRun` success response is `{runId, originalRunId, status}` per the spec.
- **Z-6**: `ExecutionConfig.Logger` (defaults to `slog.Default()` in the constructor); enqueue failures in `CreateRun`/`RetryRun` are now logged; `cmd/api/main.go` passes the process logger.
- **Z-7**: extracted a shared `decodeJSON` helper (oversized → 413, malformed → 400) used by `TriggerRun`, `RetryRun`, and `AnalyzeRun` — the plan's offered judgment-call alternative to triple-duplicating the `MaxBytesError` branch (hence 1 `MaxBytesError` occurrence, not 3).

## Verification
- `make ci` exit 0 · `FLOWFORGE_INTEGRATION=1 make test-integration` exit 0.
- Guarantee tests green: `TestExecutionHandler_ErrorDispatch`, `TestExecutionHandler_Routes`, `TestExecutionUseCase_E2EIdempotentTriggerAndRetry` (idempotent create + retry lineage).
- New tests: AJ (2 ★), AK (3: workflow-404 mapping, step shape, retry idempotency header), AL (retry response shape, oversized-body 413 on retry/analysis).
- API smoke: documented trigger body reaches the authenticated route correctly (401 before auth, as expected).

## Notes for Phase 7
- Every remaining endpoint's request/response JSON should be diffed against the spec's literal example blocks — Z-2 and Z-4 both stemmed from inventing field names from prose instead of checking the spec JSON.
- `retryRunRequest.idempotencyKey` (body) is now silently ignored — worth a deprecation note if anything already integrates against the old shape.
- The unused `idempotency_keys` table needs an explicit decision before Phase 7 adds another idempotent endpoint.
