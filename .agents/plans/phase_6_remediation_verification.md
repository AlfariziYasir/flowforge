# Phase AJ–AL (Phase 6 Remediation) — Verification Review

Review of the execution of [phase_6_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_6_review_remediation_plan.md).

**Verdict: 🟡 CONDITIONAL — 0 critical, 0 high, 1 medium, 0 low** (AA-1)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` empty · `go test ./... -race -count=1` clean · `FLOWFORGE_INTEGRATION=1` suite green (Postgres/Redis both healthy).

> [!NOTE]
> **Findings and remediation are combined in one document.** One medium item doesn't warrant a separate pair, consistent with `phase_4_remediation_verification.md`'s precedent.

---

## Scorecard

| Finding | Result |
|---|---|
| **Z-1** — wrong status code (200 vs 202) on Trigger/Cancel/Retry | ✅ Fixed. `httpx.Accepted` added; all three switched; the other 6 routes correctly stayed `httpx.OK` |
| **Z-2** — trigger body field names don't match the spec | ✅ Fixed. `triggerRunRequest` now decodes `input`/`triggerSource`; verified live that the spec's own example body reaches the usecase intact |
| **Z-3** — `ListRuns` never checks the parent workflow | ✅ Fixed. `uc.workflows.GetWorkflow` called first; `handleError` maps `workflow.ErrWorkflowNotFound` → 404 |
| **Z-4** — step responses use `id` instead of `stepRunId`, omit `workflowNodeId` | ✅ Fixed for those two fields — see **AA-1** for a residual gap in the same area |
| **Z-5** — RetryRun response missing `originalRunId` | ✅ Fixed |
| **Z-6** — enqueue failures unlogged | ✅ Fixed. `ExecutionConfig.Logger` added, defaults to `slog.Default()`, `cmd/api/main.go` passes the process logger |
| **Z-7** — oversized body not distinguished from malformed JSON on Retry/Analyze | ✅ Fixed via a shared `decodeJSON` helper (better than the plan's "3x duplication" fallback option) |
| **Z-8** — RetryRun read idempotency key from the body | ✅ Fixed. Now reads the header, matching TriggerRun; body field removed |

### Mutation verification — every gate the plan set, re-run independently

| Mutation | Expected | Result |
|---|---|---|
| Revert `TriggerRun` to `httpx.OK` | `TestTriggerCancelRetry_Return202` fails | ✅ **Fails** |
| Revert `triggerRunRequest`'s JSON tags to `inputContext`/`triggerType` | `TestTriggerRun_AcceptsDocumentedRequestShape` fails | ✅ **Fails** |
| Remove the `workflows.GetWorkflow` parent check from `ListRuns` | the Z-3 usecase test fails | ✅ **Fails** (`TestExecutionUseCase_ListRunsParentCheck`) |
| Revert `newStepView` to a bare `"id"` key | `TestHandler_StepResponseShape` fails | ✅ **Fails**, both subtests |

All four ★ mutation gates the remediation plan specified bite exactly as claimed.

### Behavior probe

POSTing the spec's own example body against the live handler:

```
{"input":{"leadEmail":"user@example.com","leadName":"Jane Doe"},"triggerSource":"manual"}
→ 202 Accepted, usecase receives TriggerType="manual", InputContext contains "leadEmail"
```

Both Z-1 and Z-2 confirmed closed together, exactly as the plan required.

---

## 🟡 Medium

### AA-1: `GetStep`'s payload fields still don't match the documented names

[handler.go:268-281](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go#L268-L281) · [api-3.md:686-706](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/docs/api-3.md#L686-L706)

`api-3.md` §10.2.2 documents `GetStep`'s response with `inputPayload`, `outputPayload`, `errorPayload`. The shared `newStepView` — introduced by this round specifically to fix the step-shape mismatch (Z-4) — uses `"input"`, `"output"`, `"error"` instead:

```go
func newStepView(s *domain.StepRun) map[string]any {
	return map[string]any{
		"stepRunId": s.ID, "workflowNodeId": s.WorkflowNodeID, "nodeKey": s.NodeKey,
		"status": s.Status, "attemptCount": s.AttemptCount,
		"input": s.InputPayload, "output": s.OutputPayload, "error": s.ErrorPayload,
		"startedAt": s.StartedAt, "finishedAt": s.FinishedAt,
	}
}
```

Confirmed by mutation: `TestHandler_StepResponseShape` only asserts `stepRunId`/`workflowNodeId`/absence of a bare `id` — it never checks the payload field names, so this slipped through untested.

**Root cause, stated plainly: this is a defect in my own remediation plan.** The code sample I supplied for Z-4 used `"input"/"output"/"error"` (copied from `ListSteps`'s pre-existing, spec-compliant-for-that-endpoint naming) and the executor implemented it verbatim without cross-checking `GetStep`'s *own* documented shape, which is different from `ListSteps`'s. Two endpoints share the fix; only one of their two documented shapes actually has payload fields, and I gave both the same names.

Separately, `api-3.md` §10.2.1 doesn't list payload fields in `ListSteps`'s documented shape at all — the current `input`/`output`/`error` inclusion there is extraneous relative to the spec's example. Not scored as part of AA-1 (extra fields are typically harmless, and a list endpoint returning payload summaries isn't unreasonable), but worth a decision: either trim `ListSteps`'s payloads to match the leaner documented shape, or treat the documented shape as a floor rather than a ceiling and keep them. Not blocking.

---

## Remediation

### Phase AM — Close the Field-Name Gap (AA-1)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler.go)

Split the previously-shared view back into two, since the two endpoints' documented shapes are genuinely different — `GetStep` has payloads, `ListSteps` (per spec) does not. Keep them next to each other so the divergence stays visible and intentional:

```go
// newStepSummaryView renders ListSteps's documented shape (api-3.md §10.2.1) — no payloads.
func newStepSummaryView(s *domain.StepRun) map[string]any {
	return map[string]any{
		"stepRunId": s.ID, "workflowNodeId": s.WorkflowNodeID, "nodeKey": s.NodeKey,
		"status": s.Status, "attemptCount": s.AttemptCount,
		"startedAt": s.StartedAt, "finishedAt": s.FinishedAt,
	}
}

// newStepDetailView renders GetStep's documented shape (api-3.md §10.2.2) — includes payloads.
func newStepDetailView(s *domain.StepRun) map[string]any {
	return map[string]any{
		"stepRunId": s.ID, "workflowNodeId": s.WorkflowNodeID, "nodeKey": s.NodeKey,
		"status": s.Status, "attemptCount": s.AttemptCount,
		"inputPayload": s.InputPayload, "outputPayload": s.OutputPayload, "errorPayload": s.ErrorPayload,
		"startedAt": s.StartedAt, "finishedAt": s.FinishedAt,
	}
}
```

`ListSteps` calls `newStepSummaryView`; `GetStep` calls `newStepDetailView`. This is a judgment call between two options — the alternative is keeping one shared view with all fields (simpler, but keeps emitting payloads from a list endpoint the spec doesn't ask to). Splitting is recommended: it matches the spec exactly for both endpoints and stops a future payload-size concern on `ListSteps` before it exists, rather than after a run with megabyte-sized HTTP step bodies makes it one.

#### [MODIFY] [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/handler_test.go)

Extend `TestHandler_StepResponseShape`'s `GetStep` subtest to assert `inputPayload`/`outputPayload`/`errorPayload` keys are present (with `input`/`output`/`error` absent), and its `ListSteps` subtest to assert no payload keys are present at all.

### Verification

```
make ci

# mutation: revert newStepDetailView's payload keys to "input"/"output"/"error"
go test ./internal/execution/ -run TestHandler_StepResponseShape -v
# MUST fail. Restore afterwards.

grep -n "inputPayload\|outputPayload\|errorPayload" internal/execution/handler.go
# present in GetStep's view, absent from ListSteps's
```

---

## Notes for Phase 7

- Carried forward: every remaining endpoint's request/response JSON should be diffed against the spec's literal example blocks, not the plan's prose paraphrase — this round's own remediation is the second time in two rounds that a field-name mismatch shipped from a plan's *sample code* rather than the implementation deviating from a correct plan. The habit gap is upstream of execution: whoever writes the fix's example code needs to check the spec too, not just whoever implements it.
- The `idempotency_keys` table's continued disuse still needs an explicit decision before Phase 7 (carried from the prior round, unchanged).
