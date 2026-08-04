# Implementation Plan — Phase 3 Response Shapes & Test Backlog

Remediates the 4 findings in [phase_3_remediation_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_remediation_verification_findings.md) — 2 medium (Q-1, Q-2), 2 low (Q-3, Q-4).

The correctness work is done and mutation-verified. What remains is the response contract and the test backlog. **Q-1 should land before any client integrates**, because it makes the working optimistic locking unusable in practice.

---

## User Review Required

> [!IMPORTANT]
> - **Q-2 (response DTOs)**: this plan introduces explicit response types in `internal/workflow/dto.go` and maps every handler through them. That changes the JSON payload of five endpoints — dropping `tenantId`, `graphSnapshot` and `metadata` from responses where `api-2.md` does not list them. If any consumer already depends on the current fuller payloads, say so and this narrows to just the versions-list (the one with a real size cost) plus Q-1.
> - **Q-4 scope**: the eleven backlog tests are ~150 lines of table-driven work. If you would rather ship and take them as a follow-up, the two worth doing regardless are **T-41** (route disambiguation — newly load-bearing this round) and **T-23** (the `MaxBytesError` path added this round is entirely untested).

---

## Phase U — Response Contract (Q-1, Q-2)

#### [MODIFY] [dto.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/dto.go)

- **Q-1** — add the result type the previous plan called for, carrying the post-bump `row_version`:
  ```go
  type SaveDraftResult struct {
      WorkflowID    uuid.UUID `json:"workflowId"`
      VersionID     uuid.UUID `json:"versionId"`
      VersionNumber int       `json:"versionNumber"`
      Status        string    `json:"status"`
      RowVersion    int       `json:"rowVersion"`   // bumped value — clients chain on this
      UpdatedAt     time.Time `json:"updatedAt"`
  }
  ```
- **Q-2** — add response types for the four endpoints that currently leak the domain struct. Keep them in `dto.go` next to the commands so the request/response pair reads together:
  ```go
  type WorkflowSummary struct { … }  // GET /workflows list item: id,name,description,status,currentVersionNumber,updatedAt
  type WorkflowDetail  struct { … }  // GET /workflows/{id}: + currentVersionId,rowVersion,createdAt
  type WorkflowUpdated struct { … }  // PATCH: id,name,description,rowVersion,updatedAt
  type VersionSummary  struct { … }  // GET .../versions item: id,workflowId,versionNumber,status,publishedAt,createdAt
  ```
  Give each a `From…(*domain.X)` constructor so the mapping lives in one place and a new domain field cannot silently join the wire format.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go)

- **Q-1** — change `SaveDraft` to return `(*SaveDraftResult, error)`. `TouchRowVersion` already knows the expected version, so the bumped value is `cmd.RowVersion + 1`; return that rather than re-reading the row. Update the `WorkflowUseCase` interface and run `make mocks`.

  > [!WARNING]
  > `cmd.RowVersion + 1` is correct **only** because `TouchRowVersion` uses `row_version = row_version + 1` with an equality predicate on the expected value — so a success proves the stored value was `cmd.RowVersion` and is now one greater. If that SQL ever changes to a non-monotonic update, this arithmetic silently lies. Add a comment at the return site tying the two together.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

- **Q-2** — route lines 107, 190, 273, 388 and 416 through the new response types. `GetVersion` ([:416](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go#L416)) keeps `graphSnapshot` — `api-2.md` lists it for the single-version read; it is only the *list* that must not carry it.
- Leave `Create` as-is or convert it to `WorkflowDetail` + `tenantId` for consistency; either is fine, but do not leave two idioms in the same file.

### Test Strategy — Phase U
- ★ `SaveDraft: response carries the bumped rowVersion` — save with `rowVersion: 1`, assert the result reports **2**. Red today (the field does not exist).
- ★ `SaveDraft: two sequential saves succeed when chaining rowVersion` — save at 1, take the returned value, save again. This is the user-visible bug; it must fail before the fix.
- `GET .../versions` response contains no `graphSnapshot` key — decode into `map[string]any` and assert absence, not just the typed shape.
- `GET /workflows/{id}` response contains no `tenantId`.

---

## Phase V — Test Backlog (Q-3, Q-4)

#### [MODIFY] [user_handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler_test.go)

- **Q-3** — reshape assertions to decode `httpx.Envelope` and check `error.code` / `data.*`, matching `middleware_test.go` and `handler_test.go`. Delete no test. Five endpoints, ~12 subtests. This closes the last uncovered response surface in `internal/auth`.

#### [MODIFY] workflow tests — Q-4

Priority order — the first two exercise code added in the last round and currently untested:

1. **T-41** `handler_test.go` or `main_test.go` — drive `POST /api/v1/workflows/{id}/versions/publish` through the real `NewRouter` and assert it reaches `Publish`, not `Rollback`. The `/versions/publish` vs `/versions/{versionId}/rollback` pairing is new this round; literal-beats-wildcard is now load-bearing and unproven.
2. **T-23** `handler_test.go` — a 2 MiB body → **413**. The `*http.MaxBytesError` branch added in Phase S has no coverage.
3. **T-12** `usecase_test.go` — tenant B's workflow fetched as tenant A → `ErrWorkflowNotFound`, never 403, never a leak of existence.
4. **T-33** `usecase_test.go` — all six `ActionWorkflow*` constants recorded, with publish's `Record` inside the tx. Assert the exact strings so a rename breaks a test rather than a dashboard.
5. **T-31** `usecase_test.go` — `GetVersion` on a draft assembles from `LoadGraph`, not the empty `graph_snapshot`.
6. **T-38** `repository_sql_test.go` — `ReplaceGraph` statement order via a recording `DBTX`.
7. **T-19** `usecase_test.go` — cancelled `ctx` → no writes.
8. **T-25, T-39, T-40** `handler_test.go` — no error-text leakage; envelope shape on success; empty list → `items: []` not `null` (needs `[]*domain.Workflow{}`, not `nil`, to prove the marshalling).

### Test Strategy — Phase V
`make ci`. For T-40 in particular, assert on the raw JSON bytes — a typed decode cannot distinguish `[]` from `null`.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **U — Response contract** | Q-1, Q-2 | 2 ★ cases red → green; no `graphSnapshot` in the versions list; no `tenantId` in workflow responses |
| V — Test backlog | Q-3, Q-4 | `make ci` clean; 12 cases present |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test -race -count=1`), then append an execution entry to `.agents/memory/action_history.md`. **Runtime figures from `-race` only.**

**Corrected boundary check** (the previous plan's version produced a false positive — `repository.go` and `audit.go` legitimately import `postgres`):

```
grep -l platform/postgres internal/workflow/*.go   # must NOT list usecase.go
go list -deps ./internal/engine | grep -v '^flowforge/internal/domain$' | grep flowforge
```

### Live smoke test — still the outstanding gate

Unchanged from the previous plan and still never run. Item 2 is now the interesting one: it previously would have shown *both* draft saves succeeding, and should now show a 409.

1. Two concurrent `POST .../versions/publish` → one 200, one 409, no `uq_workflow_versions_workflow_ver` violation.
2. Two concurrent `PUT .../draft` with the same `rowVersion` → one 200, one **409**.
3. `ReplaceGraph` leaves no orphan `workflow_edges`.
4. Stale `rowVersion` on `PATCH` → 409, not 404.
5. A published `workflow_versions` row is never subsequently UPDATEd.
6. **New:** save a draft, take the `rowVersion` from the response, save again without an intervening `GET` → 200 (this is Q-1).
