# Implementation Plan — List-Query Coverage & Error Boundary

Remediates findings S-1 through S-4 from [.agents/plans/phase_3_hotfix_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_hotfix_verification_plan.md).

## Phase Z — List-Query Coverage (S-1)

- [ ] **Step 1: Add `TestEscapeLike` & `Paginate` positive path tests**:
  - In [postgres/repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go): Add table-driven `TestEscapeLike` testing `%`, `_`, `\`, and plain strings. Add tests for `Paginate` (`Search`, `Exclude`, `Filters`).
- [ ] **Step 2: Add List SQL builder / query structure test (T-35)**:
  - In [repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go): Add `TestBuildListWorkflowsSQL` asserting `tenant_id` predicate, `ILIKE` escaping, `status <> 'archived'` default, and `ORDER BY` clause.

---

## Phase AA — Error Boundary & Backlog (S-2, S-3, S-4)

- [ ] **Step 1: Clean error messages in `handleError` (S-2)**:
  - In [internal/workflow/handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go): Replace `err.Error()` with actionable human messages on sentinel error branches. Use `httpx.FailWithDetails` for `ErrInvalidDAG` and `ErrCycleDetected`.
- [ ] **Step 2: Wire `TestNewRouter` directly through `NewRouter` (S-3)**:
  - In [cmd/api/main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go): Test route disambiguation directly against `NewRouter(..., wfHandler)`.
- [ ] **Step 3: Complete Workflow Test Backlog (S-4)**:
  - Add T-25 (no error leakage), T-23 (413 body size), T-12 (tenant isolation), T-33 (audit constants), T-31 (draft graph assembly), T-38 (`ReplaceGraph` order), T-19 (cancelled ctx), T-39/T-40 (envelope shape & `"items": []`).

---

## Verification Plan

### Automated Verification
- `gofmt -l ./cmd ./internal` (must be empty)
- `go test ./... -race -count=1` (100% PASS)
- `make ci`
