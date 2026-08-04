# Implementation Plan — Phase 3 Response Shapes & Test Backlog

Remediates findings Q-1 through Q-4 per [.agents/plans/phase_3_remediation_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_remediation_verification_plan.md).

## Phase U — Response Contract (Q-1, Q-2)

- [ ] **Step 1: Define DTOs in `internal/workflow/dto.go`**:
  - Add `SaveDraftResult` (carrying `RowVersion: cmd.RowVersion + 1`).
  - Add `WorkflowSummary`, `WorkflowDetail`, `WorkflowUpdatedResponse`, `VersionSummary` DTOs and converter constructors.
- [ ] **Step 2: Update `SaveDraft` in `internal/workflow/usecase.go`**:
  - Return `(*SaveDraftResult, error)` instead of `(*domain.WorkflowVersion, error)`.
  - Calculate `RowVersion: cmd.RowVersion + 1` with comment linking to `TouchRowVersion` SQL.
  - Update `WorkflowUseCase` interface and regenerate mocks via `mockery`.
- [ ] **Step 3: Update `internal/workflow/handler.go`**:
  - Map handlers through DTO constructors (`Get` -> `WorkflowDetail`, `List` -> `[]WorkflowSummary`, `Update` -> `WorkflowUpdatedResponse`, `SaveDraft` -> `SaveDraftResult`, `ListVersions` -> `[]VersionSummary`, `Create` -> `WorkflowDetail` + `VersionSummary`).
- [ ] **Step 4: Phase U Tests**:
  - Write test: `SaveDraft` returns bumped `rowVersion` (e.g. 1 -> 2).
  - Write test: Chaining sequential draft saves (save at 1, take returned 2, save again at 2).
  - Write test: `GET /workflows` response contains no `tenantId`.
  - Write test: `GET /versions` response contains no `graphSnapshot`.

---

## Phase V — Test Backlog (Q-3, Q-4)

- [ ] **Step 1: Auth Test Envelope Reshaping (Q-3)**:
  - Reshape assertions in `internal/auth/user_handler_test.go` to unmarshal `httpx.Envelope` and assert `success`, `data`, and `error.code`.
- [ ] **Step 2: Workflow Route Disambiguation & Test Backlog (Q-4)**:
  - Add T-41 test in `cmd/api/main_test.go` verifying `POST /api/v1/workflows/{id}/versions/publish` reaches Publish, not Rollback.
  - Assert raw JSON bytes for T-40 (`"items":[]`).
- [ ] **Step 3: Verification**:
  - Run `make ci` (`gofmt`, `go vet`, `go build`, `go test -race -count=1`).

---

## Verification Plan

### Automated Verification
- `go test ./internal/workflow/... -race -count=1`
- `go test ./internal/auth/... -race -count=1`
- `go test ./cmd/api/... -race -count=1`
- `make ci`
