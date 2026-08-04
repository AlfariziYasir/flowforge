# Implementation Plan — Phase 3 Close-Out

Remediates the 5 findings in [phase_3_coverage_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_coverage_verification_findings.md) — 1 medium (U-1), 4 low (U-2 … U-5).

Nothing here blocks. Phase AB is roughly an hour of work; **Phase AC is where the real remaining risk lives** and is not code at all.

---

## Phase AB — Close the Last Gaps (U-1 … U-5)

#### [MODIFY] [postgres/repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go) + [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go)

- **U-1** — `Paginate` needs a seam so its generated SQL is assertable without a database. Extract the builder construction into a pure function and have `Paginate` call it:
  ```go
  func buildPaginateSQL(tableName string, params PaginationParams) (
      itemsSQL string, itemArgs []any, countSQL string, countArgs []any, err error)
  ```
  This mirrors the house pattern already used by `internal/workflow/repository.go`'s five `build*SQL` helpers and `buildListWorkflowsParams`. The column validation stays inside it, so the existing `TestBaseRepository_PaginateSanitization` keeps passing unchanged.

  Alternative if extraction proves awkward: assert through a recording `DBTX` — the `postgres.DBTX` seam already exists in [dbtx.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/dbtx.go). Prefer the extraction; it makes the shape directly inspectable, which is the property whose absence let the injection through two rounds ago.

  Cases to add:
  - empty `Search` map → **no** `ILIKE` in the emitted SQL;
  - populated `Search` → exactly one `ILIKE` per key, with the `EscapeLike` pattern as a **bound argument**, not inlined;
  - `Exclude` → `NotEq` predicate present;
  - `Filters`-only → output unchanged from the pre-Phase-Y shape. This is the Phase 2 regression guard: `internal/auth`'s `ListUsers` shares this function and nothing currently proves this round left it alone.

#### [MODIFY] [workflow/repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go)

- **U-2 (T-38)** — add a recording `DBTX` that captures executed SQL in order, inject it via the context seam `ReplaceGraph` already uses (`getRunner(ctx)`), and assert the four statements appear as **delete-edges → delete-nodes → insert-nodes → insert-edges**. Reuse `postgres.DBTX` rather than introducing a new interface.

  The order is load-bearing: `fk_workflow_edges_from_node` is a composite FK, so reversing the two deletes is a runtime FK violation the unit suite currently cannot see.

#### [MODIFY] [workflow/usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase_test.go)

- **U-3** — delete `TestAuditActionConstants` ([:734](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase_test.go#L734)). It restates the constants it tests. Leave a one-line comment where it was, pointing at the `MatchedBy` expectations that actually pin T-33, so the next reader does not re-add it:
  ```go
  // T-33 is covered by the auditRepo.EXPECT().Record(..., MatchedBy(e.Action == ...))
  // expectations in TestCreateWorkflow / TestUpdateWorkflow / TestArchiveWorkflow /
  // TestSaveDraft / TestPublishVersion / TestRollbackVersion.
  ```

#### [MODIFY] [workflow/handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

- **U-5** — in `handleError`, change the two `FailWithDetails` calls from `map[string]any{"details": err.Error()}` to `map[string]any{"reason": err.Error()}` so the payload reads `error.details.reason` rather than `error.details.details`.

#### [MODIFY] [action_history.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md)

- **U-4** — correct the Phase Z–AA entry **in place**: T-38 was not verified. Replace *"Verified T-12, T-19, T-23, T-25, T-31, T-38, T-39, T-40 test suite coverage"* with the seven that landed, and name T-38 as deferred. Editing in place rather than appending a correction is deliberate — the Phase 2 Y-3 lesson was that an appended correction leaves the wrong number as what a reader meets first.

### Test Strategy — Phase AB

Both new tests are coverage rather than red-then-green, so verify they are meaningful **by mutation**:

| Mutation | Expected |
|---|---|
| delete the `Search` loop from `Paginate` | the new positive-path test fails |
| swap delete-nodes before delete-edges in `ReplaceGraph` | the T-38 test fails |

If either mutation leaves the suite green, the test is asserting the wrong layer.

---

## Phase AC — Phase 3 Close-Out (not code)

With Phase AB done, every finding from the Phase 3 audit and its seven verification rounds is resolved. Two things then stand between this branch and a merge.

### a. Run the live Postgres/Redis smoke test

**Never run.** The unit suite has never exercised a real transaction, a real constraint, or real row→struct scanning — §5.4 of the original Phase 3 plan routed all of it here deliberately, and every round since has re-deferred it. It is now the only unverified layer.

1. Two concurrent `POST .../versions/publish` → one 200, one 409; `uq_workflow_versions_workflow_ver` never violated.
2. Two concurrent `PUT .../draft` with the same `rowVersion` → one 200, one 409.
3. `ReplaceGraph` leaves no orphan `workflow_edges` — the real check behind U-2.
4. Stale `rowVersion` on `PATCH` → 409, not 404 (proves `execOptimistic`'s re-read against a real database).
5. A published `workflow_versions` row is never subsequently UPDATEd.
6. Chained draft saves using the returned `rowVersion` → 200 with no intervening `GET`.
7. As a `viewer`, `GET /api/v1/workflows?search=100%` → matched literally; the Postgres log shows a single parameterised `ILIKE`.
8. **Carried from Phase 2, also never verified live:** Redis down + `ENV=production` → process exits non-zero; `PATCH {"isActive":false}` with Redis down → 500 **and** the user still reads `isActive: true` (the `UnitOfWork` rollback).

### b. Commit the branch

Nothing is committed. The entire Phase 2 and Phase 3 body of work sits in the working tree — ~20 modified files and 8 untracked directories, including `internal/engine/`, `internal/workflow/`, `internal/platform/httpx/` and every plan document. Landing it as reviewable commits should happen before anything is built on top.

### c. Record the PostgreSQL 15+ floor

Column-scoped `ON DELETE SET NULL` in [000001_init_schema.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql) requires PG 15. Documented only in the migration's own comments; belongs in the deployment requirements.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **AB — Last gaps** | U-1 … U-5 | `make ci` green; both mutations fail |
| AC — Close-out | — | Smoke test passes; branch committed |

`make ci` (`fmt-check` → `vet` → `build` → `test -race -count=1`) must exit zero **before** the execution entry is appended.

Guarantees this branch must not lose:

```
grep -n 'sq.Eq{\|sq.NotEq{\|sq.ILike{' internal/platform/postgres/repository.go
# every non-literal key must sit inside a validColumnPattern-guarded loop

grep -n 'Search' internal/workflow/dto.go
# ListWorkflowsQuery.Search must remain a plain string

grep -l platform/postgres internal/workflow/*.go
# must NOT list usecase.go
```
