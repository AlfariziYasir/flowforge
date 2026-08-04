# Phase W–Y (SQL Injection Hotfix) — Verification Review Findings

Review of the execution of [phase_3_response_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_response_verification_plan.md).

**Verdict: 🟡 CONDITIONAL — 0 critical, 0 high, 2 medium, 2 low** (S-1 … S-4)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` **all packages ok**.

> [!NOTE]
> **The critical injection is closed, verified by probe rather than by reading — and the fix went further than the plan asked.** The plan specified key validation on `Search` and `Exclude`; the execution also applied it to `Filters`. All three reject a hostile identifier:
>
> ```
> Search key  → invalid search column  "name FROM workflows WHERE 1=1 UNION SELECT password_hash FROM users --"
> Exclude key → invalid exclude column "…"
> Filters key → invalid filter column  "…"
> ```
>
> The tree is also green again on both gates that were red last round.

---

## Remediation Scorecard

| Finding | Result |
|---|---|
| R-1 SQL injection | ✅ Closed at both layers — `Search` is a `string` through the use case and filter; the repository owns the `"name"` literal; `Paginate` validates all three key maps. **Probe-verified.** |
| R-2(a) Failing route test | ✅ Real token minted with `sessionID = uuid.Nil` so the noop session store's fail-closed branch is skipped; passes |
| R-2(b) `gofmt` | ✅ Clean |
| R-3 Restore T-35 coverage | ❌ Not done — see **S-1** |
| R-4 Cover `Search`/`Exclude` | ⚠️ Half — hostile-key rejection covered by `TestBaseRepository_PaginateSanitization`; the positive paths and `escapeLike` are not — see **S-1** |
| R-5 base64 `filter` blob | ✅ Removed; ordinary `page`/`pageSize`/`status`/`search`/`sortBy`/`sortOrder` parsed field by field |
| R-6 T-41 tautology | ❌ Not done — see **S-3** |
| R-7 Backlog (9 tests) | ❌ Zero landed — see **S-4** |

Residual injection surface swept: every remaining map key in a column position (`repository.go:144-163`) flows through `validColumnPattern`; `ORDER BY` still flows through the regex-guarded `sanitizeOrderBy`. No client-controlled identifier reaches SQL.

---

## 🟡 Medium

### S-1: The deleted list-query coverage was not restored, and `escapeLike` remains untested

[repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go) still holds four builder tests — the same four as before. Nothing replaced T-35.

`TestBaseRepository_PaginateSanitization` covers the *rejection* path well, but the properties T-35 existed to assert are still unverified:

| Property | Status |
|---|---|
| `tenant_id` always present in the list query | untested |
| `ILIKE` injected only when `Search != ""` | untested — the empty-guard added this round has no test |
| `%`, `_`, `\` escaped in the search term | **untested** |
| `ORDER BY` only ever an allowlisted column | untested at the repo layer |
| `archived` excluded by default | untested |

```
$ grep -rn escapeLike --include=*.go internal
internal/platform/postgres/repository.go          ← definition only, no test file
```

`escapeLike` is the function standing between a user's search string and a `LIKE` pattern. A user searching for `100%` currently produces `%100\%%`; nothing proves that, and nothing would catch a regression that dropped the backslash escape and let `%` turn into a wildcard. The plan's R-4 bullet named it explicitly.

This is the second consecutive round in which the list-query SQL has no shape assertion — and it was the absence of exactly that assertion that let R-1 through.

### S-2: `handleError` returns `err.Error()` to the client on 10 of 11 branches

[handler.go — `handleError`](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

```go
case errors.Is(err, ErrVersionConflict):
    httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowVersionConflict, err.Error())   // ← wrapped chain
…
default:
    httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "internal server error")  // ← correct
```

Phase 3 plan §3.7 is explicit: *"`Fail` never receives `err.Error()` — the message is a fixed human string; the code carries the meaning (`api-1.md:266`)."* §3.11 repeats it: *"never leak `err.Error()`."* Only the `default` branch complies.

The sentinels reach `handleError` wrapped, so the emitted message carries internal context — `"clone graph for new draft: …"`, `"revoke user access on update: …"`, node keys, version numbers. Database text does not reach these branches (`execOptimistic` wraps driver errors into a non-sentinel that falls to `default`), so this is an information-disclosure and contract issue rather than a driver leak — but the whole point of the rule is that the boundary should not depend on that analysis holding as the code grows.

T-25 — *"force a repo failure; assert the body contains neither the underlying error text nor `pgx`/`SQL`"* — is the test for this, and it is still in the backlog (**S-4**).

---

## 🟢 Low

### S-3: T-41 still tests the standard library, not this route table

[main_test.go — `TestNewRouter_PublishVsRollbackRouteDisambiguation`](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go)

The 401 is fixed — a real token is minted and passed as a Bearer header, with `sessionID = uuid.Nil` to skip the noop store's fail-closed session lookup. That is a neat solution.

But line 21 still constructs `mux := http.NewServeMux()` and registers two local dummy handlers. `NewRouter` is never called. The test therefore asserts that Go's `ServeMux` prefers a literal segment over a wildcard — a property of the standard library. If someone deleted the `/versions/publish` route from `NewRouter` tomorrow, this test would still pass.

The plan asked for this twice: *"drive it through the real `NewRouter`, not the handler directly."*

### S-4: None of the nine backlog tests landed

T-12 (tenant isolation), T-19 (cancelled ctx), T-23 (2 MiB → 413), T-25 (no error-text leakage — now doubly relevant, see **S-2**), T-31 (`GetVersion` on a draft), T-33 (audit action strings), T-38 (`ReplaceGraph` order), T-39 / T-40 (envelope shape, `items: []` not `null`).

T-23 is worth singling out: the `*http.MaxBytesError` branch has now been present and untested for three rounds.

---

## Remediation

See [phase_3_hotfix_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_hotfix_verification_plan.md). Nothing here blocks merge; S-1 is the one worth doing before the branch lands, because it is the coverage whose absence caused the critical.
