# Implementation Plan — SQL Injection Hotfix & Green Tree

Remediates findings R-1 through R-7 from [.agents/plans/phase_3_response_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_response_verification_plan.md).

## User Review Required

> [!IMPORTANT]
> - **R-1 (SQL Injection Hotfix)**: Reverts `ListWorkflowsQuery.Search` back to `string` (searching `name` via `ILIKE`). Adds column identifier regex validation (`^[a-zA-Z_][a-zA-Z0-9_]*$`) in `internal/platform/postgres/repository.go` for all `Search` and `Exclude` keys in `Paginate`.
> - **R-5 (Query Params Alignment)**: Restores `GET /api/v1/workflows` query parameter filtering (`page`, `pageSize`, `status`, `search`, `sortBy`, `sortOrder`) per `api-2.md` and Phase 3 spec.

---

## Phase W — Security Hotfix (R-1, R-2, R-6)

- [ ] **Step 1: Revert `Search` to `string` in DTO & Workflow Repository (R-1)**:
  - In [dto.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/dto.go): Change `ListWorkflowsQuery.Search` to `string`.
  - In [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository.go): Change `ListWorkflowsFilter.Search` to `string`. In `List`, if `f.Search != ""`, map `Search: map[string]string{"name": f.Search}` (hardcoded column identifier owned by repository).
- [ ] **Step 2: Add Column Sanitization in Postgres `Paginate` (R-1)**:
  - In [internal/platform/postgres/repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go): Validate all `Search` and `Exclude` keys against regex `^[a-zA-Z_][a-zA-Z0-9_]*$`. Return error if invalid.
  - Run `gofmt -w internal/platform/postgres/repository.go` (R-2b).
- [ ] **Step 3: Fix `cmd/api/main_test.go` Auth & Router Wiring (R-2a, R-6)**:
  - Update `TestNewRouter_PublishVsRollbackRouteDisambiguation` in [cmd/api/main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go) to generate a valid JWT token header for authentication.
- [ ] **Step 4: Regression Tests for Phase W**:
  - Add test in `internal/platform/postgres/repository_test.go` verifying `Paginate` rejects non-identifier search keys (e.g. `map[string]string{"name FROM users --": "x"}`).

---

## Phase X — Restore Deleted Coverage (R-3, R-4)

- [ ] **Step 1: Restore T-35 Coverage in `internal/workflow/repository_sql_test.go` (R-3)**:
  - Add test asserting list query SQL construction properties (tenant isolation, ILIKE escaping, default exclude archived).
- [ ] **Step 2: Add `Search` & `Exclude` Unit Tests in Postgres Platform (R-4)**:
  - In `internal/platform/postgres/repository_test.go`, test `Search` (ILIKE) and `Exclude` (NotEq) predicate generation.

---

## Phase Y — Backlog & Contract (R-5, R-7)

- [ ] **Step 1: Revert Handler `List` Query Parameter Parsing (R-5)**:
  - In `internal/workflow/handler.go`, parse `status`, `search`, `sortBy`, `sortOrder`, `page`, `pageSize` from query params per `api-2.md`.
- [ ] **Step 2: Complete Test Backlog (R-7)**:
  - Add T-12 (tenant isolation returns `ErrWorkflowNotFound`), T-23 (2 MiB body → 413), T-33 (all six `ActionWorkflow*` constants), T-31 (`GetVersion` on draft graph assembly), T-38 (`ReplaceGraph` statement order), T-19 (cancelled ctx → no writes), T-25, T-39, T-40 (`items: []`).

---

## Verification Plan

### Automated Tests
- `gofmt -l ./cmd ./internal` (must be empty)
- `go test ./... -race -count=1` (must pass 100%)
- `make ci`
