# Phase U–V (Response Contract & Test Backlog) — Verification Review Findings

Review of the execution of [phase_3_remediation_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_remediation_verification_plan.md).

**Verdict: 🔴 REJECTED — 1 critical, 1 high, 3 medium, 2 low** (R-1 … R-7)

Baseline: `go build ./...` clean · `go vet ./...` clean · **`gofmt -l` FAILS** · **`go test ./... -race` FAILS**.

> [!CAUTION]
> **R-1 is a SQL injection reachable by any authenticated user, including `viewer`.** It was introduced by an unplanned refactor in this round — list filtering moved to a client-supplied base64 JSON blob whose `Search` field is a `map[string]string`, and squirrel places map **keys** verbatim into SQL. The same refactor deleted `buildListWorkflowsSQL` and its test **T-35**, which existed specifically to assert this class of property. Do not deploy this tree.

---

## Remediation Scorecard

| Finding | Result |
|---|---|
| Q-1 `SaveDraft` rowVersion | ✅ `SaveDraftResult` added; both ★ tests present, including `two sequential saves succeed when chaining rowVersion` |
| Q-2 Response DTOs | ✅ `WorkflowSummary` / `WorkflowDetail` / `WorkflowUpdatedResponse` / `VersionSummary` + `New…` constructors; handlers routed through them |
| Q-3 `user_handler_test.go` | ✅ 10 envelope assertions — the last uncovered response surface in `internal/auth` is closed |
| Q-4 Test backlog | ❌ T-41 written but **failing**; 9 of the remaining cases absent |

The three planned items landed. The damage came from work that was **not** in the plan.

---

## 🔴 Critical

### R-1: SQL injection via client-controlled `Search` map keys

[handler.go `List`](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go) · [usecase.go:153](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/usecase.go#L153) · [repository.go:212](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository.go#L212) · [postgres/repository.go:148-151](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L148-L151)

The chain, end to end:

```go
// 1. handler — the ENTIRE query object is unmarshalled from client base64 JSON
encodedQuery := r.URL.Query().Get("filter")
jsonBytes, _ := base64.URLEncoding.DecodeString(encodedQuery)
json.Unmarshal(jsonBytes, &q)          // q.Search is map[string]string, attacker-controlled
q.TenantID = authUser.TenantID         // only TenantID is overwritten

// 2. use case — SortBy is allowlisted; Search keys are NOT validated at all
filter := ListWorkflowsFilter{ …, Search: q.Search, … }

// 3. platform — the map KEY becomes the column identifier
for k, v := range params.Search {
    itemBuilder = itemBuilder.Where(sq.ILike{k: escapeLike(strings.TrimSpace(v))})
}
```

`escapeLike` sanitises the **value**. Squirrel parameterises the **value**. Neither touches the key. Demonstrated against squirrel v1.5.4:

```
input key:  name FROM workflows WHERE 1=1 UNION SELECT password_hash,1,1,1,1,1,1,1,1,1 FROM users --

SQL : SELECT * FROM workflows WHERE tenant_id = $1 AND name FROM workflows WHERE 1=1
      UNION SELECT password_hash,1,1,1,1,1,1,1,1,1 FROM users -- ILIKE $2
ARGS: [T %x%]
```

The trailing `--` comments out the rest of the statement, so the injected fragment also **discards the `tenant_id` predicate**. This defeats the tenant isolation that every prior round of this review verified, and it can read `users.password_hash` across all tenants.

Reachable by `GET /api/v1/workflows` — gated `admin, editor, viewer`, so the **lowest-privilege role** suffices. No body, no special headers; one query parameter.

**Fix:** the repository must own the column name. `ListWorkflowsQuery.Search` reverts to `string` (the original plan §3.5: *"`Search string // ILIKE over name`"*), and the repository builds `map[string]string{"name": term}` itself. Any map-keyed predicate that survives must validate keys against an allowlist inside `Paginate`.

---

## 🟠 High

### R-2: The tree is red — `make ci` fails on two counts

**(a) A test fails.**

```
--- FAIL: TestNewRouter_PublishVsRollbackRouteDisambiguation
    main_test.go:158: expected: 200 / actual: 401
    main_test.go:159: literal /versions/publish must route to Publish handler
    main_test.go:168: expected: 200 / actual: 401
```

The test injects the principal with `auth.ContextWithAuthUser(...)` and attaches it via `WithContext`, but `AuthMiddleware.Authenticate` reads the **`Authorization` header** — context injection is what the middleware *produces*, not what it consumes. With no header it returns 401 before routing is ever exercised. The test cannot pass as written.

**(b) `gofmt` fails.**

```
$ gofmt -l ./cmd ./internal
internal/platform/postgres/repository.go
```

```diff
-	Search map[string]string
+	Search   map[string]string
-}
\ No newline at end of file
+}
```

Misaligned struct field and a missing trailing newline. `make test` depends on `fmt-check`, so **`make ci` never completed successfully** — the plan's stated gate for both phases.

---

## 🟡 Medium

### R-3: `buildListWorkflowsSQL` and test T-35 were deleted

`repository_sql_test.go` went from five builder tests to four; `buildListWorkflowsSQL` no longer exists. `List` now delegates to `BaseRepository.Paginate`.

T-35 was specified in the Phase 3 plan §5.4 to assert exactly:

> always includes `tenant_id`; injects `ILIKE` only when `Search != ""`; escapes `%`, `_`, `\`; `ORDER BY` only ever contains an allowlisted column; excludes `archived` by default.

Every one of those properties is now either unverified or, in the case of the first, **actively violated by R-1**. The test that would have caught the injection was removed by the same change that introduced it. Its sibling T-34 was called *"the highest-value assertion in the phase"*; T-35 was in that class.

Ordering also regressed: `List` now passes `OrderBy` into `Paginate`, which runs it through `sanitizeOrderBy` — the function the Phase 3 plan §3.9 explicitly said not to lean on because *"it silently swallows bad input into `created_at DESC`"*. The use case's `SortBy` allowlist still defends this, but the repo-level guarantee is gone and nothing tests the pair.

### R-4: Unplanned change to shared Phase 1/2 platform code, with no tests

[postgres/repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go) gained `PaginationParams.Search`, `PaginationParams.Exclude`, two predicate loops in `Paginate`, and a new `escapeLike` helper. None of it was in the plan — Phase U named `dto.go`, `usecase.go` and `handler.go`; Phase V was tests only.

`Paginate` is shared with `internal/auth`'s `ListUsers`. `internal/platform/postgres/repository_test.go` has **zero** references to `Search` or `Exclude`. A change with that blast radius, made outside the plan, with no coverage, is how R-1 reached the tree unnoticed.

### R-5: List filtering was redesigned to a base64 JSON blob

`GET /api/v1/workflows?filter=<base64(JSON)>` replaces the ordinary query parameters (`page`, `pageSize`, `status`, `search`, `sortBy`, `sortOrder`) that `api-2.md` and Phase 3 plan §3.5 specify. Consequences beyond the spec deviation:

- The **entire** `ListWorkflowsQuery` becomes client-controlled in one `json.Unmarshal`, which is the mechanism behind R-1. Adding a field to that struct now silently adds an attacker-controlled input.
- Two decode attempts (`URLEncoding` then `StdEncoding`) with the error from the first discarded.
- Filters are no longer visible in logs, proxies, or cache keys.

---

## 🟢 Low

### R-6: T-41 does not test what it claims

Even with the 401 fixed, the test constructs its own `http.NewServeMux()` and registers two dummy handlers. It never calls `NewRouter`. It would then assert Go's stdlib literal-beats-wildcard precedence — a property of the standard library, not of this application's route table. The plan said *"drive it through the real `NewRouter`, not the handler directly."*

`TestNewRouter_WorkflowRoutesWiring` immediately above it does call `NewRouter`, but only asserts a 404 when every dependency is nil.

### R-7: Nine backlog tests still absent

Landed: T-41 (broken, see R-2/R-6). Still missing: **T-12** (tenant isolation), **T-19** (cancelled ctx), **T-23** (2 MiB → 413), **T-25** (no error-text leakage), **T-31** (`GetVersion` on a draft), **T-33** (audit action strings), **T-38** (`ReplaceGraph` order), **T-39** / **T-40** (envelope shape, `items: []` not `null`).

T-12 is worth singling out: it asserts tenant isolation at the use-case boundary, and R-1 is a tenant-isolation break.

---

## Remediation

See [phase_3_response_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_response_verification_plan.md). **R-1 and R-2 must land before anything else.**
