# Implementation Plan — List-Query Coverage & Error Boundary

Remediates the 4 findings in [phase_3_hotfix_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_hotfix_verification_findings.md) — 2 medium (S-1, S-2), 2 low (S-3, S-4).

The security work is done and probe-verified; the tree is green. **S-1 is the one item worth doing before this branch lands** — it is the coverage whose absence let the critical through, and it has now been skipped twice.

---

## User Review Required

> [!IMPORTANT]
> - **S-2 (error messages)**: this plan replaces `err.Error()` with fixed human strings on all ten sentinel branches, per Phase 3 plan §3.7. Clients currently receive the wrapped internal text (`"clone graph for new draft: …"`); after this they receive a stable phrase and rely on `error.code` for meaning — which is what `api-1.md` intends. If any consumer parses `error.message`, say so; the alternative is to keep the detail but move it into `error.details` behind a debug flag.
> - **S-4 scope**: nine tests, ~200 lines. If they stay deferred again, please say so explicitly and record it in `action_history.md` — three silent deferrals in a row is how T-35 got lost.

---

## Phase Z — List-Query Coverage (S-1)

The properties below are exactly the ones T-35 asserted before it was deleted. How they are asserted matters less than that they are.

#### [MODIFY] [postgres/repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go)

- ★ `escapeLike` — table-driven: `100%` → `%100\%%`; `a_b` → `%a\_b%`; `back\slash` → `%back\\slash%`; `plain` → `%plain%`. This is the function standing between a user's search string and a `LIKE` pattern, and it currently has no test at all. Escape order matters — `\` must be escaped **first**, or the escapes introduced for `%` and `_` get double-escaped; a test locks that in.
- `Paginate` positive paths, complementing the existing `TestBaseRepository_PaginateSanitization`:
  - `Search` with an empty map emits no `ILIKE` predicate.
  - `Search` with a value emits exactly one `ILIKE` per key, with the escaped pattern as a bound argument.
  - `Exclude` emits `NotEq`.
  - A `Filters`-only call is byte-identical to what it produced before this round's changes — `Paginate` is shared with `internal/auth`'s `ListUsers`, and nothing currently proves Phase 2's path is unaffected.

#### [MODIFY] [repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go)

- ★ Restore T-35's assertions against `List`. `buildListWorkflowsSQL` is gone, so either:
  - **(a)** reinstate a pure builder that `List` delegates to — the shape the original design chose so this test could exist without a database; or
  - **(b)** assert through a recording `DBTX` that captures the SQL `Paginate` executes.

  Prefer **(a)**. It is a smaller change than it looks, and it restores the property that the list query's shape is directly inspectable — which is the thing that would have caught R-1.

  Assert: `tenant_id` always present; `ILIKE` only when `Search != ""`; the search term escaped; `ORDER BY` only ever an allowlisted column; `status <> 'archived'` present by default and absent when `Status == "archived"`.

### Test Strategy — Phase Z
Both ★ cases are new coverage rather than red-then-green — there is no bug to reproduce, only an untested guarantee. Verify they are meaningful by mutation: drop the `\\` replacement from `escapeLike` and confirm the table fails; remove the `ExcludeStatus` default in `List` and confirm the archived assertion fails.

---

## Phase AA — Error Boundary & Backlog (S-2, S-3, S-4)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

- **S-2** — replace `err.Error()` with a fixed string on each of the ten sentinel branches in `handleError`. The code already carries the meaning:
  ```go
  case errors.Is(err, ErrVersionConflict):
      httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowVersionConflict,
          "workflow was modified by another request; refetch and retry")
  ```
  Keep the messages actionable — a stable phrase that tells the caller what to do, not a restatement of the code. `domain.ErrInvalidDAG` and `domain.ErrCycleDetected` are the two worth a thought: their wrapped text names the offending node keys, which is genuinely useful to a client building a graph. Put that in `error.details` via `httpx.FailWithDetails` rather than dropping it — `details` exists for exactly this.

#### [MODIFY] [main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go)

- **S-3** — rewrite `TestNewRouter_PublishVsRollbackRouteDisambiguation` to call `NewRouter` with a real `WorkflowHandler` backed by a mock use case, and assert `PublishVersion` was invoked and `RollbackVersion` was not. The token-minting approach already in the test is correct and should be kept verbatim; only the mux construction changes. As written, deleting the `/versions/publish` route from `NewRouter` would leave this test green.

#### [MODIFY] workflow tests — S-4

Priority order; the first two exercise branches that have been untested for three rounds:

1. **T-25** no error-text leakage — force a repo failure and assert the body contains neither the wrapped message nor `pgx`/`SQL`. This is S-2's regression test; write it alongside the fix.
2. **T-23** 2 MiB body → 413.
3. **T-12** tenant isolation — tenant B's workflow fetched as tenant A → `ErrWorkflowNotFound`.
4. **T-33** audit — all six `ActionWorkflow*` constants, publish's `Record` inside the tx.
5. **T-31** `GetVersion` on a draft assembles from `LoadGraph`.
6. **T-38** `ReplaceGraph` statement order.
7. **T-19** cancelled `ctx` → no writes.
8. **T-39 / T-40** envelope shape; `items: []` not `null` — assert raw JSON bytes, since a typed decode cannot distinguish the two.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **Z — List coverage** | S-1 | `escapeLike` table green; list-query shape asserted; both mutations fail |
| AA — Boundary & backlog | S-2, S-3, S-4 | `make ci` clean; T-25 passes; route test fails if `NewRouter` loses the route |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test -race -count=1`). Append the execution entry **only after `make ci` exits zero**.

Injection re-check — the guarantee this branch must not lose:

```
grep -n 'sq.Eq{\|sq.NotEq{\|sq.ILike{' internal/platform/postgres/repository.go
# every non-literal key must be inside a validColumnPattern-guarded loop
grep -rn 'map\[string\]string' internal/workflow/dto.go
# ListWorkflowsQuery.Search must remain a plain string
```

### Live smoke test — the last outstanding gate

Still never run, and now the only thing standing between this branch and a merge. Nothing in the unit suite exercises a real transaction, a real constraint, or real row→struct scanning.

1. Two concurrent `POST .../versions/publish` → one 200, one 409, no unique-constraint violation.
2. Two concurrent `PUT .../draft` with the same `rowVersion` → one 200, one 409.
3. `ReplaceGraph` leaves no orphan `workflow_edges`.
4. Stale `rowVersion` on `PATCH` → 409, not 404.
5. A published `workflow_versions` row is never subsequently UPDATEd.
6. Chained draft saves using the returned `rowVersion` → 200 without an intervening `GET`.
7. As a `viewer`, `GET /api/v1/workflows?search=…` with a hostile term → the term is escaped and matched literally; the Postgres log shows a single parameterised `ILIKE`.

Also still outstanding from earlier phases: the **PostgreSQL 15+ floor** (column-scoped `ON DELETE SET NULL`) is recorded only in migration comments, and **nothing on this branch is committed** — the entire Phase 2 and Phase 3 body of work is still in the working tree.
