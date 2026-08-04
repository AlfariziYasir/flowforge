# Phase 3 — Workflow Domain Foundations: Review Findings

Review of the execution of [phase_3_workflow_domain_foundations.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_workflow_domain_foundations.md) against `.agents/prompts/reviewer.md`.

**Verdict: 🔴 REJECTED — NEEDS REVISION** (2 high, 4 medium, 5 low)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok**.

---

## Quality Gates

| Gate | Status | Note |
|---|---|---|
| Plan in `.agents/plans/` | ✅ | |
| Action history logged | ✅ | |
| Sentinel errors via `errors.New` | ✅ | `internal/workflow/errors.go`, 9 sentinels |
| UseCase free of `net/http` | ✅ | …but **not** free of infrastructure — see **P-3** |
| **Engine purity** | ✅ | `internal/engine` imports only stdlib + `domain`; enforced by `purity_test.go` |
| Tenant isolation | ✅ | Every query in `repository.go` filters `tenant_id` — verified including `MarkPublished` and all four `ReplaceGraph` statements |
| Atomic claims | ➖ | N/A this phase |
| SSRF validator | ➖ | Phase 5 |
| Goroutine safety / ctx | ✅ | `ctx` first param throughout; no `context.Background()` in request paths |
| Table-driven / named subtests | ✅ | ~48 named subtests across the new packages |
| `%w` wrapping, no `panic()` | ✅ | Sole `panic` is the deliberate nil-`TxRunner` guard |
| `gofmt` clean | ✅ | |

**What is genuinely good.** `internal/engine/dag.go` is the strongest code in the phase — Kahn's algorithm with a `container/heap` min-heap ready-set gives real determinism, self-loops are named before traversal, and every error is built on the two `domain.*` sentinels so `errors.Is` survives to the handler. `domain.FromPersisted` canonicalises correctly, which is what makes the publish checksum meaningful. `execOptimistic` resolves the `RowsAffected() == 0` ambiguity exactly as specified — re-read, then 404 vs 409. `httpx` matches the `api-1.md` envelope.

**What blocks approval.** Optimistic locking is specified everywhere and enforced almost nowhere. `SaveDraft` ignores `RowVersion` outright; publish and rollback perform substantial writes before their only version check fires. The tests that appear to cover this exercise the handler's error *mapping* with a mocked use case, so they pass regardless.

---

## 🔴 High

### P-1: `SaveDraft` ignores `RowVersion` — the draft graph has no optimistic locking

[usecase.go:248-293](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L248-L293)

`SaveDraftCommand.RowVersion` is declared, the handler validates it is `>= 1`, and the use case then **never reads it**. There is no comparison against `wf.RowVersion`, no `row_version` predicate on the write, and no bump afterwards:

```go
wf, err := uc.wfRepo.FindByID(ctx, cmd.TenantID, cmd.WorkflowID)   // RowVersion loaded…
if wf.Status == domain.WorkflowStatusArchived { … }                // …only Status is read
draftVer, err := uc.verRepo.FindDraftVersion(ctx, …)
…
uc.verRepo.ReplaceGraph(txCtx, cmd.TenantID, draftVer.ID, nodes, edges)   // unconditional
…
return draftVer, nil                                               // row_version unchanged
```

Two editors opening the same workflow both `PUT /draft`; the second silently destroys the first's graph. No conflict is ever raised, and the returned `rowVersion` never advances, so a client cannot detect staleness on its next call either.

This is the phase's core artifact — the draft graph — and it is the one mutable thing in the design.

> [!WARNING]
> **Nothing catches this.** The `returns 409 Conflict on stale row_version` subtest in [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler_test.go) mocks `PublishVersion` returning `ErrVersionConflict` and asserts the HTTP mapping. It tests the handler's switch statement, not the locking. No use-case test covers stale-`RowVersion` for any operation.

**Fix:** compare `wf.RowVersion != cmd.RowVersion` → `ErrVersionConflict` before any write, and bump the workflow's `row_version` inside the transaction so the returned value is usable.

---

### P-2: Publish and rollback write before their only version check

[usecase.go:321-419](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L321-L419) · [usecase.go:424-530](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L424-L530)

Plan §3.10 step 2 is explicit: *"`wf.RowVersion != cmd.RowVersion` → `ErrVersionConflict`. **Before any write.**"* Neither method does it. `wf` is loaded via `FindByIDForUpdate` and only `wf.Status` is ever read.

Actual publish order:

```
 5:  FindByIDForUpdate      ← RowVersion fetched, never compared
36:  MarkPublished          ← WRITE: draft flipped to published, checksum + snapshot stored
40:  SetCurrentVersion      ← the FIRST and ONLY row_version predicate
```

Rollback is worse — `CreateVersion` (consuming `maxNum+1`) and two `ReplaceGraph` calls all precede `SetCurrentVersion`.

Today the data stays consistent **only because the whole body runs in one `ExecuteInTx` and the transaction rolls back**. That is a single point of failure for correctness: the plan's ordering was defence in depth precisely so a passthrough runner, a future refactor that splits the transaction, or a partial-commit path cannot leave a draft marked `published` with no current-version pointer — an unrecoverable state for that workflow.

It also holds a `FOR UPDATE` lock across a graph load, DAG validation, JSON marshal, SHA-256, and several writes before discovering the caller was stale.

**Fix:** insert the `RowVersion` comparison immediately after `FindByIDForUpdate` in both methods, and keep the `SetCurrentVersion` predicate as the belt-and-braces check.

---

## 🟡 Medium

### P-3: The use case imports `internal/platform/postgres`

[usecase.go:16](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L16) · [usecase.go:509](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go)

```go
import "flowforge/internal/platform/postgres"
…
func NewWorkflowUseCase(wfRepo WorkflowRepository, verRepo VersionRepository,
    auditRepo AuditRepository, txRunner postgres.UnitOfWork) WorkflowUseCase
```

Plan §3.10 specified a consumer-side interface for exactly this reason — *"`postgres.UnitOfWork` satisfies it structurally… Deliberately NOT imported."* `internal/auth` gets this right with its own `TxRunner` ([user_usecase.go:61-63](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L61-L63)); `internal/workflow` does not. The application layer now depends on an infrastructure package, which is the boundary `.agents/prompts/reviewer.md` §2 exists to protect.

The declared `TxRunner` interface the plan called for does not appear anywhere in the package.

### P-4: No request-body size limit on any workflow route

```
$ grep -c MaxBytesReader internal/workflow/handler.go
0
```

Plan §3.11: *"`http.MaxBytesReader(w, r.Body, 1<<20)` on every body route."* `internal/auth` applies it to all four of its body routes. `POST /workflows`, `PUT /draft`, `POST /publish` and `POST /rollback` accept unbounded bodies — and `/draft` in particular decodes an arbitrarily large node/edge array straight into memory.

### P-5: Routes, methods and status codes deviate from `api-2.md`

[main.go:148-157](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L148-L157) · [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

| Spec / plan §3.12 | Implemented |
|---|---|
| `PATCH /workflows/{id}` | **`PUT`** |
| `POST /workflows/{id}/versions/publish` | `POST /workflows/{id}/publish` |
| `POST /workflows/{id}/versions/{versionId}/rollback` | `POST /workflows/{id}/rollback`, `versionId` **in the body** |
| `DELETE` → 204, empty body | **200 + workflow JSON** ([handler Archive](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)) |
| write routes → `admin`, `editor` | `DELETE`/`publish`/`rollback` → **`admin` only** |
| read routes → `admin`, `editor`, `viewer` | **no `RequireRole` wrapper at all** |

The 204 and the `PATCH`/`PUT` swap are client-visible contract breaks. Moving `versionId` out of the path also drops the natural 400-on-malformed-UUID that `uuid.Parse(r.PathValue(...))` would give.

`Archive` additionally reads `rowVersion` from the body *or* a query parameter and swallows the JSON decode error ([`if err == nil`](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)); the `r.ContentLength > 0` guard also skips the body entirely for chunked requests.

Plan T-41 — a test proving `/versions/publish` does not land in the rollback handler — is moot under this scheme, and no route test replaced it.

### P-6: Audit action names do not match the specification

[usecase.go:98, 198, 231, 282, 399, 514](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go)

| Plan §3.8 | Implemented |
|---|---|
| `workflow.created` | `create` |
| `workflow.updated` | `update` |
| `workflow.archived` | `archive` |
| `workflow.draft_saved` | `draft_save` |
| `workflow.published` | `publish` |
| `workflow.rolled_back` | `rollback` |

Audit `action` values are a durable, queried contract — renaming them after rows exist means a migration. Placement is correct: publish's and rollback's `Record` calls are inside the transaction as required.

---

## 🟢 Low

### P-7: `respondJSON` silently discards its status argument

[handler.go:202-208](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L202-L208)

```go
func respondJSON(w http.ResponseWriter, status int, data any) {
    if status == http.StatusCreated { httpx.Created(w, data); return }
    httpx.OK(w, data)   // every other status collapses to 200
}
```

All current auth callers pass 200 or 201, so no live defect — but the signature promises something it does not honour, and the next `respondJSON(w, http.StatusAccepted, …)` will silently return 200.

### P-8: Auth emits error codes outside the documented set

[handler.go:215-217](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L215-L217)

```go
case http.StatusNotFound:  code = "NOT_FOUND"
case http.StatusConflict:  code = "CONFLICT"
```

Neither appears in `api-1.md` §7.2, and both are bare string literals — plan §3.7: *"declare them as constants and use no string literals at call sites."* `httpx` has no matching constant, which is the signal that these codes were invented at the call site.

### P-9: The auth retrofit is only half-tested

`git status` shows `internal/auth/handler_test.go` and `cmd/api/main_test.go` updated, but **`middleware_test.go` and `user_handler_test.go` untouched**. Since `respondJSONError` moved to the `httpx` envelope, every error body those two files produce changed shape — yet their assertions did not, which means they never asserted on body shape. Plan T-43 required reshaping assertions to `data.*` / `error.code`; the whole `user_handler` surface and every middleware rejection path now has no envelope coverage.

### P-10: The cloned draft can carry a nil `Metadata`

[usecase.go:376](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L376)

```go
Metadata: draftVer.Metadata,
```

`workflow_versions.metadata` is `NOT NULL DEFAULT '{}'`. If the source row's `Metadata` is nil, the insert fails at runtime. Plan G-14 is explicit: *"Phase 3 never populates it — always write `[]byte("{}")`."* Rollback has the same pattern at `newPublishedVer.Metadata`.

### P-11: Roughly a third of the specified tests are absent

Present and real: T-1…T-9b (engine, including the T-8 determinism loop), T-26 (purity), T-27…T-29 (graph), T-34…T-37 (SQL builders), T-42 (httpx), T-20 (nil runner panics), T-10 partially, T-32 partially.

Missing:

| Test | Covers |
|---|---|
| T-12 | tenant isolation — workflow of tenant B fetched as tenant A |
| T-16 | `SaveDraft` against a published version → `ErrVersionImmutable` |
| T-18 | checksum stability across shuffled input |
| T-19 | cancelled `ctx` → no writes |
| T-30 | publish on an archived workflow |
| T-31 | `GetVersion` on a draft assembles from nodes/edges |
| T-33 | audit entries for all five actions |
| T-38 | `ReplaceGraph` statement order |
| T-22, T-23, T-24, T-25 | RBAC 403, 1 MiB → 413, `DELETE` 204 empty, no error leakage |
| T-39, T-40, T-41 | envelope on handler responses, `items: []`, route disambiguation |
| T-43 | auth retrofit assertions |

The gap is concentrated in exactly the places the high findings live: no use-case test asserts optimistic-locking behaviour for any operation.

---

## Remediation

See [phase_3_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_review_remediation_plan.md).
