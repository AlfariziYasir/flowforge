# Implementation Plan — Phase 3 Review Remediation

Remediates the 11 findings in [phase_3_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_review_findings.md) — 2 high (P-1, P-2), 4 medium (P-3 … P-6), 5 low (P-7 … P-11).

Phase R is a correctness fix and should land alone. Phases S and T are contract and hygiene.

> [!IMPORTANT]
> **Write the P-1/P-2 tests first and watch them fail.** Every locking test that exists today mocks the use case and asserts the handler's error mapping, which is why P-1 shipped with a green suite. A test that passes before the fix is not a regression test — Phase 2 lost three rounds to exactly this.

---

## User Review Required

> [!IMPORTANT]
> - **P-5 (route contract)**: this plan restores the `api-2.md` shapes — `PATCH` for update, `/versions/publish`, `/versions/{versionId}/rollback`, `DELETE` → 204 empty. That is a **breaking change to four routes**. If a client already targets the current shapes, say so and this narrows to just the 204 and the RBAC alignment.
> - **P-6 (audit action names)**: renaming to `workflow.published` etc. is free now and a data migration later. This plan renames. Confirm no `audit_logs` rows exist in any environment.
> - **P-5 (RBAC)**: the plan restores `admin, editor` on writes and adds explicit `admin, editor, viewer` on reads. Current code is `admin`-only on delete/publish/rollback — if that tightening was deliberate, say so and only the read routes change.

---

## Phase R — Optimistic Locking (P-1, P-2, P-3)

### Step 1 — tests first, red

#### [MODIFY] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase_test.go)

Add to the existing `TestSaveDraft` / `TestPublishVersion` / `TestRollbackVersion` functions, following the file's named-`t.Run` style.

- ★ `SaveDraft: rejects a stale rowVersion` — workflow at `RowVersion: 7`, command sends `3`; assert `ErrVersionConflict` **and** `verRepo.AssertNotCalled(t, "ReplaceGraph")`. The negative assertion is the finding.
- ★ `SaveDraft: bumps rowVersion on success` — the returned version reflects an advanced `row_version`, so a client can chain calls.
- ★ `PublishVersion: rejects a stale rowVersion before any write` — assert `MarkPublished`, `SetCurrentVersion`, `CreateVersion` and `ReplaceGraph` are all **never called**. Red today: `MarkPublished` fires first.
- ★ `RollbackVersion: rejects a stale rowVersion before any write` — same, against `CreateVersion`.
- `PublishVersion: on an archived workflow → ErrWorkflowArchived` (T-30).
- `SaveDraft: on a published version → ErrVersionImmutable` (T-16) — construct the case where `FindDraftVersion` returns a non-draft row, proving the guard is explicit rather than implied by the query.

### Step 2 — the fix

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go)

- **P-2** — insert immediately after `FindByIDForUpdate` in both `PublishVersion` ([:325](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L325)) and `RollbackVersion` ([:428](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L428)):
  ```go
  if wf.RowVersion != cmd.RowVersion {
      return ErrVersionConflict
  }
  ```
  Keep the `SetCurrentVersion` predicate — it is the real serialization guard; this is the cheap pre-check that stops the method doing work it will discard. Order the archived check after it, matching plan §3.10 steps 2→3.
- **P-1** — give `SaveDraft` a transaction boundary and a version predicate. It currently reads the workflow *outside* the tx and never locks it:
  1. Move `FindByID` inside `ExecuteInTx` and switch it to `FindByIDForUpdate`.
  2. `wf.RowVersion != cmd.RowVersion` → `ErrVersionConflict`.
  3. After `ReplaceGraph`, bump the workflow's `row_version` so concurrent editors collide. `UpdateMetadata` already carries the predicate and the `+1`; either reuse it with unchanged name/description or add a narrow `TouchRowVersion(ctx, tenantID, id, expected)` to `WorkflowRepository`. Prefer the latter — reusing `UpdateMetadata` to mean "touch" is the kind of overload that misleads later.
  4. Return the bumped `rowVersion` in `SaveDraftResult` so the client can chain edits (the handler's `data` shape in plan §3.7 already promises `rowVersion`).
- **P-10** — default `Metadata` to `[]byte("{}")` when the source is empty, in both the cloned draft ([:376](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L376)) and `newPublishedVer`. `metadata` is `NOT NULL`.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go) — P-3

- Declare the consumer-side interface the plan specified and drop the `internal/platform/postgres` import:
  ```go
  // TxRunner is satisfied structurally by postgres.UnitOfWork. Declared here so the
  // application layer does not depend on an infrastructure package.
  type TxRunner interface {
      ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
  }
  ```
  Change `NewWorkflowUseCase`'s last parameter to `TxRunner`. `cmd/api/main.go` needs no change — `*postgres.pgxUnitOfWork` already satisfies it. Mirror [auth/user_usecase.go:61-63](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L61-L63).
- Keep the nil-panic guard; it is the Phase 2 Y-1 contract and correctly applied.

### Test Strategy — Phase R
The four ★ cases must be **red** before the Step 2 edits and green after. If one passes immediately, the mock expectation is wrong — most likely it asserts a call happened rather than that it did not.

---

## Phase S — API Contract (P-4, P-5, P-6)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

- **P-4** — `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)` as the first statement of `Create`, `Update`, `SaveDraft`, `Publish`, `Rollback`, and `Archive`. Map the resulting decode failure to **413**, not 400 (plan T-23).
- **P-5** — `Archive` returns `httpx.NoContent(w)`; delete the `httpx.OK(w, wf)`. `ArchiveWorkflow` can keep returning the workflow for the audit metadata; the handler simply discards it.
- **P-5** — `Rollback` takes `versionId` from `r.PathValue("versionId")` via `uuid.Parse`, not the body. The body keeps only `rowVersion`.
- **P-5** — in `Archive`, drop the query-parameter fallback and the swallowed decode error; require the body and return 400 `INVALID_REQUEST_BODY` on a malformed one. `r.ContentLength > 0` is unreliable for chunked requests.

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

- **P-5** — restore the plan §3.12 route table:
  ```
  PATCH  /api/v1/workflows/{workflowId}                              admin, editor
  DELETE /api/v1/workflows/{workflowId}                              admin, editor
  POST   /api/v1/workflows/{workflowId}/versions/publish             admin, editor
  POST   /api/v1/workflows/{workflowId}/versions/{versionId}/rollback admin, editor
  GET    …(all four read routes)                                     admin, editor, viewer
  ```
  Go 1.26's `ServeMux` prefers the literal `/versions/publish` over `/versions/{versionId}`, so registration order is irrelevant — but T-41 must prove it.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go)

- **P-6** — rename the six audit actions to `workflow.created`, `workflow.updated`, `workflow.archived`, `workflow.draft_saved`, `workflow.published`, `workflow.rolled_back`. Declare them as constants in `audit.go` rather than inline literals, so the next writer cannot drift.

### Test Strategy — Phase S
- T-24 `DELETE` → 204 with `rec.Body.Len() == 0`.
- T-41 `POST /workflows/{id}/versions/publish` reaches `Publish`, not `Rollback` — drive it through the real `NewRouter`, not the handler directly.
- T-22 viewer `POST /workflows` → 403; viewer `GET /workflows` → 200.
- T-23 a 2 MiB body → 413.
- T-33 asserts the exact action strings, so a future rename breaks a test rather than a dashboard.

---

## Phase T — Test Debt & Auth Retrofit (P-7 … P-9, P-11)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)

- **P-7** — make `respondJSON` honour its argument, or delete the parameter. Preferred: replace the two call shapes with direct `httpx.OK` / `httpx.Created` at the call sites and remove the shim entirely — it now adds nothing but a chance to be wrong.
- **P-8** — add `CodeNotFound = "NOT_FOUND"` and `CodeConflict = "CONFLICT"` to `httpx` **only if** `api-1.md` §7.2 sanctions them; otherwise map 404/409 in auth to the existing domain-specific codes. Either way, no string literals at the call site.

#### [MODIFY] auth tests — P-9

- **T-43** — reshape assertions in [middleware_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware_test.go) and [user_handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler_test.go) to decode `httpx.Envelope` and assert `error.code` / `data.*`, matching what `handler_test.go` already does. **Delete no test** — reshape only. Both files currently pass because they never asserted on body shape, which is the gap.

#### [MODIFY] workflow tests — P-11

Remaining backlog, by home file:

- `usecase_test.go` — T-12 (tenant isolation → `ErrWorkflowNotFound`, never 403), T-18 (checksum stability: same graph shuffled → identical checksum; one config byte changed → different), T-19 (cancelled ctx → no writes), T-31 (`GetVersion` on a draft assembles from `LoadGraph`, not the empty `graph_snapshot`), T-33 (audit for all five actions, publish's inside the tx).
- `repository_sql_test.go` — T-38 (`ReplaceGraph` statement order: delete-edges → delete-nodes → insert-nodes → insert-edges, via a recording `DBTX`).
- `handler_test.go` — T-25 (force a repo failure; assert the body contains neither the underlying error text nor `pgx`/`SQL`), T-39 (every success `{success:true,data,meta:null,error:null}`), T-40 (empty list serialises `items: []`, not `null`).

### Test Strategy — Phase T
`make ci`. T-40 in particular needs a genuinely empty result set — `[]*domain.Workflow{}`, not `nil` — to prove the `httpx.List` marshalling.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **R — Locking** | P-1, P-2, P-3, P-10 | 4 ★ cases red → green; `internal/workflow` no longer imports `internal/platform/postgres` |
| S — Contract | P-4, P-5, P-6 | Routes match `api-2.md`; `DELETE` → 204 empty; 2 MiB body → 413 |
| T — Test debt | P-7, P-8, P-9, P-11 | `make ci` clean; backlog present |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test -race -count=1`), then append an execution entry to `.agents/memory/action_history.md` per `.agents/AGENTS.md` §2. **Any runtime figure must come from `-race`** — plan §6's reporting rule.

Boundary re-check after Phase R:

```
go list -deps ./internal/workflow | grep platform/postgres   # expect: no match
go list -deps ./internal/engine   | grep -Ev '^(flowforge/internal/domain|[a-z/]+)$'
```

### Live smoke test — still outstanding

Carried from Phase 2 and now materially more important, because §5.4 deliberately routes scanning, real constraints and true transaction semantics here:

1. Two concurrent `POST .../versions/publish` on one workflow → one succeeds, one returns **409**, and `uq_workflow_versions_workflow_ver` is never violated.
2. Two concurrent `PUT .../draft` with the same `rowVersion` → one succeeds, one **409** (this is P-1; today both succeed).
3. `ReplaceGraph` leaves no orphan `workflow_edges` rows.
4. A stale `rowVersion` on `PATCH` returns 409 against a real database, not 404.
5. Publish, then confirm the published `workflow_versions` row is never subsequently UPDATEd.
