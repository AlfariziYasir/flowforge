# Implementation Plan — Phase 3 Close-Out (Phase AB)

Remediates findings U-1 through U-5 from [.agents/plans/phase_3_coverage_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_coverage_verification_plan.md).

## Phase AB — Close the Last Gaps (U-1 … U-5)

- [ ] **Step 1: Extract `buildPaginateQueryBuilders` in `internal/platform/postgres/repository.go` (U-1)**:
  - Extract pure SQL builder function `buildPaginateQueryBuilders(tableName string, params PaginationParams)` returning item and count SQL builders + argument lists.
  - In [postgres/repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go): Add unit tests asserting SQL outputs for empty search (no ILIKE), populated search (bound ILIKE argument), exclude (NotEq), and filters-only query.
- [ ] **Step 2: Add T-38 Statement Execution Order Test for `ReplaceGraph` (U-2)**:
  - In [workflow/repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go): Test `ReplaceGraph` using a recording `postgres.DBTX` runner in context. Assert statements execute in FK-safe order: `DELETE FROM workflow_edges` -> `DELETE FROM workflow_nodes` -> `INSERT INTO workflow_nodes` -> `INSERT INTO workflow_edges`.
- [ ] **Step 3: Remove Redundant `TestAuditActionConstants` (U-3)**:
  - In [workflow/usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase_test.go): Remove `TestAuditActionConstants` and replace with explanatory comment linking T-33 to `MatchedBy` expectations.
- [ ] **Step 4: Align Error Details Key (`reason`) in `handleError` (U-5)**:
  - In [workflow/handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go): Change details map key from `"details"` to `"reason"` (`map[string]any{"reason": err.Error()}`). Update corresponding handler tests.
- [ ] **Step 5: Correct Action History (U-4)**:
  - Update [.agents/memory/action_history.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md) to record T-38 verification.

---

## Verification Plan

### Automated Tests
- `gofmt -l ./cmd ./internal` (must be empty)
- `go test ./... -race -count=1` (100% PASS)
- `make ci`
