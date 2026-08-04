# Phase R–T (Phase 3 Remediation) — Verification Review Findings

Review of the execution of [phase_3_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_review_remediation_plan.md).

**Verdict: 🟡 CONDITIONAL — 0 critical, 0 high, 2 medium, 2 low** (Q-1 … Q-4)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok**.

> [!NOTE]
> **Both high findings are genuinely fixed, and — for the first time in this review chain — I confirmed it by mutation rather than by reading.** Deleting the `RowVersion` check from `SaveDraft` fails `TestSaveDraft`; deleting it from `PublishVersion` fails `TestPublishVersion`. The new regression tests are real guards, not assertions that pass either way.

---

## Remediation Scorecard

| Finding | Result | Evidence |
|---|---|---|
| P-1 `SaveDraft` no locking | ✅ | `FindByIDForUpdate` inside the tx, `RowVersion` compared at [:267](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L267), `TouchRowVersion` at [:294](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L294) — **mutation-verified** |
| P-2 Publish/rollback write first | ✅ | Checks at [:350](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L350) and [:463](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L463), before any write — **mutation-verified** |
| P-3 Infrastructure import | ✅ | `usecase.go` declares `TxRunner` and no longer imports `internal/platform/postgres` |
| P-4 No body size limit | ✅ | 6 `MaxBytesReader` calls, with `*http.MaxBytesError` mapped explicitly |
| P-5 Route/method/status | ✅ | `PATCH`, `/versions/publish`, `/versions/{versionId}/rollback`, `DELETE` → `NoContent`, RBAC restored on all 10 routes |
| P-6 Audit action names | ✅ | Six `ActionWorkflow*` constants in `audit.go`, no inline literals |
| P-7 `respondJSON` shim | ✅ | Removed entirely; call sites use `httpx` directly |
| P-8 Invented error codes | ✅ | `CodeNotFound` / `CodeConflict` promoted to `httpx` constants |
| P-9 Auth retrofit tests | ⚠️ Half | `middleware_test.go` reshaped (6 envelope assertions); `user_handler_test.go` **still 0** — see **Q-3** |
| P-10 Nil `Metadata` | ✅ | Defaulted to `{}` |
| P-11 Test backlog | ⚠️ Partial | T-16, T-18, T-24, T-30 landed; 11 cases still open — see **Q-4** |

**Mutation results** — the check that matters:

```
MUTATION 1: delete SaveDraft RowVersion check      → TestSaveDraft      FAIL ✓
MUTATION 2: delete PublishVersion RowVersion check → TestPublishVersion FAIL ✓
```

Both fail through mockery's unexpected-call detection (`FindDraftVersion` has no expectation once execution proceeds past the guard). That is a legitimate and fairly strong form of the negative assertion the plan asked for.

> [!NOTE]
> **My own plan's boundary command was wrong.** `go list -deps ./internal/workflow | grep platform/postgres` still matches — but that is `repository.go` and `audit.go`, which *are* the infrastructure adapters and *should* import it. The correct check is per-file: `grep -l platform/postgres internal/workflow/*.go` must not list `usecase.go`. It does not. P-3 is fixed; the verification step in the previous plan was the defect.

---

## 🟡 Medium

### Q-1: `SaveDraft`'s response carries no `rowVersion`, so clients cannot chain edits

[usecase.go:253](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L253) · [handler.go:273](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go#L273)

The locking now works — and that is exactly what exposes the gap. `SaveDraft` still returns `*domain.WorkflowVersion`:

```go
SaveDraft(ctx context.Context, cmd SaveDraftCommand) (*domain.WorkflowVersion, error)
…
httpx.OK(w, draftVer)
```

`domain.WorkflowVersion` has no `RowVersion` field — `row_version` lives on `workflows`, not `workflow_versions`. So the sequence is:

1. Client `GET /workflows/{id}` → `rowVersion: 1`.
2. Client `PUT /draft` with `rowVersion: 1` → 200. Server bumps `row_version` to **2**. Response contains no `rowVersion`.
3. Client edits again, sends `rowVersion: 1` — the only value it has → **409**.

Every draft save now requires an intervening `GET /workflows/{id}`. Plan §3.7 anticipated this and specified the response shape as `{workflowId, versionId, versionNumber, status, rowVersion, updatedAt}`; the remediation plan's Phase R step 4 called for a `SaveDraftResult` carrying the bumped value. Neither landed.

The optimistic locking is correct. It is just not *usable* as shipped.

### Q-2: Handlers marshal domain structs directly instead of the specified response subsets

[handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go) — lines 107, 190, 273, 388, 416

Only `Create` builds an explicit response map. Every other endpoint passes the raw domain struct or use-case result to `httpx.OK`. Plan §3.7 (G-5) was explicit: *"Handlers must not marshal domain structs directly where `api-2.md` specifies a subset."*

Consequences by endpoint:

| Endpoint | Specified `data` | Actual |
|---|---|---|
| `GET /workflows/{id}` | 9 named fields | full `domain.Workflow`, including `tenantId` |
| `PATCH /workflows/{id}` | `{id, name, description, rowVersion, updatedAt}` | full `domain.Workflow` |
| `GET .../versions` | `{id, workflowId, versionNumber, status, publishedAt, createdAt}` per item | full `[]*domain.WorkflowVersion` — **including `graphSnapshot` and `metadata` for every version** |
| `PUT .../draft` | 6 fields incl. `rowVersion` | full `domain.WorkflowVersion` (see **Q-1**) |

The versions-list case is the one with real weight: a workflow with 50 published versions returns 50 full graph snapshots on a call the spec defines as a summary list.

`tenantId` leaking into responses is not a privilege issue — the caller is authenticated into that tenant — but it is off-spec and it is the kind of field that becomes load-bearing for a client once shipped.

---

## 🟢 Low

### Q-3: `user_handler_test.go` still has no envelope assertions

```
$ grep -c "httpx.Envelope|env.Error.Code|env.Data" …
internal/auth/middleware_test.go:   6      ← reshaped ✓
internal/auth/user_handler_test.go: 0      ← untouched
```

`internal/auth/user_handler.go` **was** modified this round, so all five of its endpoints now emit the `httpx` envelope — but its test file still asserts only status codes. T-43 named both files; one got done.

This is the third round in which `user_handler_test.go` has been listed and skipped. It is the last uncovered response surface in `internal/auth`.

### Q-4: Eleven backlog tests remain open

Landed this round: T-16 (`on a published version returns ErrVersionImmutable`), T-18 (`shuffled nodes and edges produce identical checksum`), T-24 (`returns 204 No Content on successful archive`), T-30 (`on an archived workflow returns ErrWorkflowArchived`), plus the four ★ locking cases.

Still absent:

| Test | Covers |
|---|---|
| T-12 | tenant isolation — tenant B's workflow fetched as tenant A → `ErrWorkflowNotFound` |
| T-19 | cancelled `ctx` → no writes |
| T-31 | `GetVersion` on a draft assembles from `LoadGraph`, not the empty `graph_snapshot` |
| T-33 | audit entries for all six actions, publish's inside the tx |
| T-38 | `ReplaceGraph` statement order |
| T-22 | RBAC — viewer `POST /workflows` → 403 |
| T-23 | 2 MiB body → 413 (the `MaxBytesError` path added this round is untested) |
| T-25 | forced repo failure leaks neither error text nor `pgx`/`SQL` |
| T-39, T-40 | envelope on handler responses; empty list → `items: []` not `null` |
| T-41 | `POST .../versions/publish` reaches `Publish`, not `Rollback` |

T-41 matters more now than when it was written: the route table gained `/versions/publish` alongside `/versions/{versionId}/rollback` this round, so the literal-beats-wildcard behaviour is now actually load-bearing and nothing exercises it.

---

## Remediation

See [phase_3_remediation_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_remediation_verification_plan.md). Q-1 is worth fixing before the API is consumed; the rest is cleanup.
