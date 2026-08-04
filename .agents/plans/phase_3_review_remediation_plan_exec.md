# Implementation Plan — Phase 3 Review Remediation

Remediating 11 audit findings in Phase 3 per [.agents/plans/phase_3_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_review_remediation_plan.md).

## Phase R — Optimistic Locking & Infrastructure Decoupling (P-1, P-2, P-3, P-10)

- [ ] **Step 1: Write RED tests for optimistic locking & version guards** in `internal/workflow/usecase_test.go`:
  - `SaveDraft`: rejects stale `rowVersion` (assert `verRepo.AssertNotCalled(t, "ReplaceGraph")`)
  - `SaveDraft`: advances `row_version` on success
  - `SaveDraft`: on published version returns `ErrVersionImmutable`
  - `PublishVersion`: rejects stale `rowVersion` before any write (assert `MarkPublished`, `SetCurrentVersion`, `CreateVersion`, `ReplaceGraph` never called)
  - `PublishVersion`: on archived workflow returns `ErrWorkflowArchived`
  - `RollbackVersion`: rejects stale `rowVersion` before any write (assert `CreateVersion` never called)
- [ ] **Step 2: Implement locking fixes in `internal/workflow/usecase.go`**:
  - P-1: Move `FindByID` inside `ExecuteInTx` in `SaveDraft`, switch to `FindByIDForUpdate`, check `wf.RowVersion != cmd.RowVersion`, and touch/update `row_version` via repository (`TouchRowVersion`).
  - P-2: Insert `wf.RowVersion != cmd.RowVersion` check immediately after `FindByIDForUpdate` in `PublishVersion` and `RollbackVersion`.
  - P-10: Default `Metadata` to `json.RawMessage("{}")` when nil/empty in cloned draft and rollback version.
- [ ] **Step 3: Infrastructure Decoupling (P-3)**:
  - Declare `TxRunner` interface in `usecase.go`:
    ```go
    type TxRunner interface {
        ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
    }
    ```
  - Update `NewWorkflowUseCase` parameter to `TxRunner` and remove `internal/platform/postgres` import from `usecase.go`.
- [ ] **Step 4: Verify Phase R**:
  - Run `go list -deps ./internal/workflow | grep platform/postgres` (must return no match).
  - Verify all Phase R unit tests turn GREEN.

---

## Phase S — API Contract & REST Alignment (P-4, P-5, P-6)

- [ ] **Step 1: MaxBytesReader & Error 413 (P-4)**:
  - Add `http.MaxBytesReader(w, r.Body, 1<<20)` to `Create`, `Update`, `SaveDraft`, `Publish`, `Rollback`, `Archive` in `internal/workflow/handler.go`.
  - Handle `http.MaxBytesError` returning HTTP 413.
- [ ] **Step 2: REST Route & Response Contract Alignment (P-5)**:
  - `DELETE /api/v1/workflows/{workflowId}` -> returns 204 No Content (`httpx.NoContent(w)`).
  - `POST /api/v1/workflows/{workflowId}/versions/{versionId}/rollback` -> `versionId` extracted from URL path (`r.PathValue("versionId")`), body has `rowVersion`.
  - Update route registrations in `cmd/api/main.go` per plan §3.12:
    - `PATCH /api/v1/workflows/{workflowId}` (admin, editor)
    - `DELETE /api/v1/workflows/{workflowId}` (admin, editor)
    - `POST /api/v1/workflows/{workflowId}/versions/publish` (admin, editor)
    - `POST /api/v1/workflows/{workflowId}/versions/{versionId}/rollback` (admin, editor)
    - Read endpoints: `GET` /workflows, /workflows/{id}, /workflows/{id}/versions, /workflows/{id}/versions/{versionId} (admin, editor, viewer)
- [ ] **Step 3: Audit Action Names (P-6)**:
  - Define constants in `internal/workflow/audit.go` (`workflow.created`, `workflow.updated`, `workflow.archived`, `workflow.draft_saved`, `workflow.published`, `workflow.rolled_back`).
  - Use constants in `usecase.go`.

---

## Phase T — Test Debt & Auth Retrofit Cleanup (P-7, P-8, P-9, P-11)

- [ ] **Step 1: Auth Handler Cleanup (P-7) & HTTPX Error Codes (P-8)**:
  - Remove `respondJSON` shim in `internal/auth/handler.go`, using direct `httpx.OK` / `httpx.Created`.
  - Add `CodeNotFound = "NOT_FOUND"` and `CodeConflict = "CONFLICT"` constants to `internal/platform/httpx/httpx.go`.
- [ ] **Step 2: Auth Test Reshaping (P-9)**:
  - Reshape assertions in `internal/auth/middleware_test.go` and `internal/auth/user_handler_test.go` to assert on `httpx.Envelope` response body format.
- [ ] **Step 3: Workflow Test Backlog Completion (P-11)**:
  - `usecase_test.go`: T-12 (tenant isolation), T-18 (checksum stability & change sensitivity), T-19 (context cancellation), T-31 (`GetVersion` on draft graph assembly), T-33 (all audit actions).
  - `repository_sql_test.go`: T-38 (`ReplaceGraph` SQL statement execution order).
  - `handler_test.go`: T-25 (no raw DB/pgx leak on internal error), T-39 (envelope metadata formatting), T-40 (empty list renders `items: []`).
- [ ] **Step 4: Comprehensive Verification**:
  - Run `make ci` (`fmt-check`, `vet`, `build`, `test -race -count=1`).

---

## Verification Plan

### Automated Verification
- `go test ./internal/workflow/... -race -count=1`
- `go test ./internal/auth/... -race -count=1`
- `go test ./cmd/api/... -race -count=1`
- `go list -deps ./internal/workflow | grep platform/postgres` (Expect empty)
- `make ci`
