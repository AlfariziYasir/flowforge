# Phase AB (Close-Out) — Verification Review & Remaining Work

Review of the execution of [phase_3_coverage_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_coverage_verification_plan.md).

**Verdict: 🟡 CONDITIONAL — 0 critical, 0 high, 1 medium, 2 low** (V-1 … V-3)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` **all packages ok**.

> [!NOTE]
> **Findings and remediation are combined in one document.** One medium and two low items did not warrant a separate pair — consistent with `phase_2_final_polish.md`. All other `.agents/` conventions unchanged.

---

## All five findings fixed — and both mutation checks pass

| Finding | Result |
|---|---|
| U-1 `Paginate` positive paths | ✅ `BuildPaginateSQL` extracted and exported; `TestBuildPaginateSQL` asserts empty-search → no `ILIKE`, populated search → `name ILIKE $1` with `%my\_user%` as a **bound argument**, `Exclude` → `status <> $1` |
| U-2 T-38 `ReplaceGraph` order | ✅ `TestReplaceGraph_StatementExecutionOrder` with a recording `DBTX` |
| U-3 Tautological audit test | ✅ Removed, replaced by a comment pointing at the `MatchedBy` expectations |
| U-4 History overstatement | ✅ Corrected in place; the DEFERRED note is intact |
| U-5 `error.details.details` | ✅ Now `map[string]any{"reason": err.Error()}` |

**Mutation verification — the gate this plan set, and the reason to trust these tests:**

```
MUTATION 1: delete the Search loop from buildPaginateSQL
  → TestBuildPaginateSQL FAILS
    expected ["%my\\_user%"], actual <nil>

MUTATION 2: swap delete-nodes before delete-edges in ReplaceGraph
  → TestReplaceGraph_StatementExecutionOrder FAILS
    "DELETE FROM workflow_nodes …" does not contain "DELETE FROM workflow_edges"
```

Both new tests fail when the behaviour they cover is removed. That is the standard this chain took ten rounds to establish, and it is met here.

---

## 🟡 Medium

### V-1: `ContextWithTx` / `TxFromContext` were widened from `pgx.Tx` to `DBTX`

[dbtx.go:20-27](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/dbtx.go#L20-L27) — unplanned; the plan asked only to *reuse* `postgres.DBTX` for the recorder.

```diff
-func ContextWithTx(ctx context.Context, tx pgx.Tx) context.Context
-func TxFromContext(ctx context.Context) (pgx.Tx, bool)
+func ContextWithTx(ctx context.Context, tx DBTX) context.Context
+func TxFromContext(ctx context.Context) (DBTX, bool)
```

This changes what the context key *means*. `UnitOfWork.ExecuteInTxOptions` uses it as its nested-transaction guard:

```go
if _, ok := TxFromContext(ctx); ok {
    return fn(ctx)          // "already inside a transaction — join it"
}
tx, err := u.pool.BeginTx(ctx, opts)
```

The question that check asks used to be *"is there a transaction here?"* — the `pgx.Tx` type made that true by construction. It now asks *"is there a database handle here?"*, which is a strictly weaker property. Any non-transactional `DBTX` placed in the context makes `ExecuteInTx` **skip `BeginTx` entirely and run the body with no transaction and no rollback**, silently.

The new test already does exactly that:

```go
// internal/workflow/repository_sql_test.go:164
ctx := postgres.ContextWithTx(context.Background(), recorder)
```

Production is safe **today** — `ContextWithTx` is called from only one non-test site, `ExecuteInTxOptions:43`, always with a real `pgx.Tx`. So this is a lost compile-time guarantee rather than a live defect. But it is the same failure class the Phase 2 W-1/X-1 rounds spent three iterations closing at the constructor layer, reopened one layer below it, and introduced to make a test easier to write.

**Fix:** restore `pgx.Tx` on both functions and give the recorder its own injection path — either construct `postgresVersionRepository` with the recorder directly, or add a test-only `contextWithDBTX` key that `UnitOfWork` does not consult.

---

## 🟢 Low

### V-2: Phase 4 planning began while Phase AC is undone

`.agents/plans/phase_4_workflow_engine_core.md` now exists and proposes adding `github.com/expr-lang/expr` to `go.mod`. Meanwhile every Phase AC item is still open:

- **The live smoke test has never run.** Ten review rounds have verified logic through mocks; nothing has exercised a real transaction, a real constraint, or a real row scan. Two checks are carried from Phase 2 — including whether the `UnitOfWork` rollback actually rolls back, which **V-1 now touches**.
- **Nothing is committed.** 13 modified files and 8 untracked directories, including all of `internal/engine/`, `internal/workflow/`, `internal/platform/httpx/`.
- **The PostgreSQL 15+ floor** is still recorded only in migration comments.

Phase AC(b) said commits "should happen before anything is built on top." Phase 4 will build directly on `internal/engine/`.

### V-3: The history entry misdescribes the U-4 fix

> **U-4 Fix (Corrected Action History in Place)**: Updated prior log entries to reflect T-38 verification.

The prior entry needed no correction on that point — it correctly recorded T-38 as **deferred at that time**, and T-38 was only written in this round. The line reads as though the historical record was rewritten to claim verification. The actual record is intact (the `DEFERRED` note is still present and accurate for its date); only the description of the action is wrong.

---

## Remediation

### Phase AD — Restore the transaction type (V-1, V-3)

#### [MODIFY] [dbtx.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/dbtx.go)

- Revert `ContextWithTx` and `TxFromContext` to `pgx.Tx`. Add a comment on `TxFromContext` stating the invariant plainly: *the value in this key is always a live transaction; `UnitOfWork` relies on it to decide whether to open one.*
- `GetDBTX` continues to work unchanged — `pgx.Tx` satisfies `DBTX`.

#### [MODIFY] [repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go)

- Give `TestReplaceGraph_StatementExecutionOrder` a different seam. Simplest option: add an unexported constructor `newVersionRepositoryWithDBTX(db postgres.DBTX)` used only by tests, so the recorder never enters the transaction context. If that feels heavier than it is worth, a test-only context key in the `postgres` package works equally well — the requirement is only that `UnitOfWork` cannot mistake it for a transaction.
- Re-run **Mutation 2** afterwards; the test must still fail when the delete order is swapped.

#### [MODIFY] [action_history.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md)

- **V-3** — reword the U-4 line to what actually happened: the Phase Z–AA entry was corrected to name T-38 as deferred, and T-38 was subsequently implemented in Phase AB.

### Phase AC — unchanged, and now the only thing left

Carried verbatim from [phase_3_coverage_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_coverage_verification_plan.md) §AC. With V-1 fixed, Phase 3 is code-complete:

1. **Run the live Postgres/Redis smoke test** — the eight checks in §AC(a). Item 8 (the `UnitOfWork` rollback with Redis down) is the one V-1 makes newly worth confirming.
2. **Commit the branch** before Phase 4 starts.
3. **Record the PostgreSQL 15+ floor** in the deployment requirements.

---

## Verification

1. `make ci` — exit zero.
2. Both mutations must still fail:
   - delete the `Search` loop from `buildPaginateSQL` → `TestBuildPaginateSQL` fails;
   - swap delete-nodes before delete-edges → `TestReplaceGraph_StatementExecutionOrder` fails.
3. Transaction-type guarantee restored:
   ```
   grep -n 'func ContextWithTx\|func TxFromContext' internal/platform/postgres/dbtx.go
   # both must name pgx.Tx, not DBTX
   grep -rn 'ContextWithTx' --include=*.go . | grep -v dbtx.go
   # unit_of_work.go should be the only caller
   ```
4. Injection guarantees intact (unchanged from the previous round):
   ```
   grep -n 'sq.Eq{\|sq.NotEq{\|sq.ILike{' internal/platform/postgres/repository.go
   grep -n 'Search' internal/workflow/dto.go
   grep -l platform/postgres internal/workflow/*.go   # must not list usecase.go
   ```
