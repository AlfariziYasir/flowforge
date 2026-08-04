# Implementation Plan — SQL Injection Hotfix & Green Tree

Remediates the 7 findings in [phase_3_response_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_3_response_verification_findings.md) — 1 critical (R-1), 1 high (R-2), 3 medium (R-3 … R-5), 2 low (R-6, R-7).

**Phase W is a security hotfix. Ship it alone, before anything else.** The three items the previous plan actually asked for (Q-1, Q-2, Q-3) all landed correctly; every finding here comes from unplanned work that accompanied them.

> [!CAUTION]
> The current tree is exploitable by any authenticated `viewer` and does not compile a green `make ci`. Treat this as a stop-the-line fix, not a queued item.

---

## User Review Required

> [!IMPORTANT]
> - **R-5 (the `filter` blob)**: this plan reverts list filtering to ordinary query parameters per `api-2.md` (`page`, `pageSize`, `status`, `search`, `sortBy`, `sortOrder`). That is a **breaking change** to `GET /api/v1/workflows`. If the base64 `filter` scheme was a deliberate product decision, say so — it can be kept, but then `ListWorkflowsQuery` must be split so that only a whitelisted subset is unmarshalled from client JSON, and `Search` must still become a plain string (R-1 is not negotiable either way).
> - **R-4 (platform `Search`/`Exclude`)**: this plan keeps `Exclude` (it is safe — keys come from the repository) and removes `Search` from `PaginationParams`, returning the `ILIKE` construction to the workflow repository where the original design put it. If you would rather keep `Search` in the shared layer, it needs a key allowlist parameter and its own tests; say which.

---

## Phase W — Security Hotfix (R-1, R-2)

### Step 1 — the injection

#### [MODIFY] [dto.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/dto.go)

- **R-1** — `ListWorkflowsQuery.Search` reverts to `string`, matching Phase 3 plan §3.5 (`Search string // ILIKE over name`). A use-case query must not carry column identifiers; that is a repository concern.

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository.go)

- **R-1** — `ListWorkflowsFilter.Search` reverts to `string`. `List` constructs the column mapping itself, so the identifier is a compile-time constant owned by the repository:
  ```go
  if f.Search != "" {
      params.Search = map[string]string{"name": f.Search}   // "name" is OURS, never the client's
  }
  ```
  Note the empty-string guard — the current code passes the map through unconditionally, which also produces a pointless `name ILIKE '%%'` on every unfiltered list.

#### [MODIFY] [postgres/repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go)

- **R-1 (defence in depth)** — even with the caller fixed, `Paginate` must not accept arbitrary identifiers. Validate every `Search` and `Exclude` key against the same pattern `sanitizeOrderBy` uses, and return an error rather than silently dropping:
  ```go
  var validColumnPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
  …
  for k, v := range params.Search {
      if !validColumnPattern.MatchString(k) {
          return nil, 0, fmt.Errorf("invalid search column %q", k)
      }
      …
  }
  ```
  A single caller mistake should not become a data breach. This is the layer that was missing.
- **R-2(b)** — `gofmt -w` the file: align the `Search` field and restore the trailing newline.

### Step 2 — the red test

#### [MODIFY] [main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go)

- **R-2(a)** — `TestNewRouter_PublishVsRollbackRouteDisambiguation` fails because it supplies the principal via `ContextWithAuthUser` while `AuthMiddleware.Authenticate` reads the `Authorization` header. Fix by minting a real token:
  ```go
  pair, err := jwtSvc.GenerateTokenPair(userID, tenantID, sessionID, "admin@flowforge.local", "admin")
  req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
  ```
  `NewAuthMiddleware` wires the noop session store, whose `GetSession` fails closed — so either use `NewAuthMiddlewareWithSessionStore` with a stub that returns a session, or generate the token with `sessionID = uuid.Nil` so the middleware's `claims.SessionID != uuid.Nil` branch is skipped.
- **R-6** — while here, drive the assertion through the real `NewRouter` rather than a locally built mux. Registering two dummy handlers on a fresh `ServeMux` tests the standard library, not this route table.

### Test Strategy — Phase W
- ★ `Paginate rejects a non-identifier search column` — pass `map[string]string{"name FROM users --": "x"}` directly to `Paginate` and assert an error. Red today; this is the R-1 regression test and it belongs in `internal/platform/postgres/repository_test.go` because that is the layer that must hold.
- ★ `ListWorkflows: a client cannot choose the search column` — decode a handler request whose JSON carries a hostile `search` value and assert the generated predicate targets `name`. Once `Search` is a `string` this becomes structurally impossible, which is the point.
- `make ci` green — both the `gofmt` and the failing-test gates.

---

## Phase X — Restore Deleted Coverage (R-3, R-4)

#### [MODIFY] [repository_sql_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/repository_sql_test.go)

- **R-3** — restore T-35's coverage. `buildListWorkflowsSQL` no longer exists, so assert the same properties against whatever `List` now produces: `tenant_id` always present; `ILIKE` only when `Search != ""`; `%`, `_`, `\` escaped; `ORDER BY` only ever an allowlisted column; `archived` excluded by default.
  If asserting through `Paginate` is awkward, reinstate a pure builder for the list query — the original design chose that shape precisely so this test could exist without a database.

#### [MODIFY] [postgres/repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go)

- **R-4** — cover the new `Search` and `Exclude` behaviour that arrived untested: predicates appear only when the map is non-empty; `escapeLike` handles `%`, `_`, `\`; `Exclude` emits `NotEq`; and the new key validation rejects non-identifiers.
- `Paginate` is shared with `internal/auth`'s `ListUsers` — add one case proving the existing `Filters`-only path is unchanged, so this round's additions cannot regress Phase 2.

---

## Phase Y — Backlog & Contract (R-5, R-7)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/workflow/handler.go)

- **R-5** — replace the base64 `filter` blob with ordinary query parameters, parsed field by field with explicit validation, per `api-2.md` and Phase 3 plan §3.5. Parsing each parameter individually is what keeps a new struct field from silently becoming an attacker-controlled input — the property whose absence produced R-1.

#### [MODIFY] workflow tests — R-7

Priority order:

1. **T-12** tenant isolation — tenant B's workflow fetched as tenant A → `ErrWorkflowNotFound`. Directly adjacent to R-1's failure mode.
2. **T-23** 2 MiB body → 413. The `*http.MaxBytesError` branch is still untested two rounds after it was added.
3. **T-33** audit — all six `ActionWorkflow*` constants, publish's `Record` inside the tx.
4. **T-31** `GetVersion` on a draft assembles from `LoadGraph`.
5. **T-38** `ReplaceGraph` statement order.
6. **T-19** cancelled `ctx` → no writes.
7. **T-25, T-39, T-40** no error-text leakage; envelope shape; `items: []` not `null` (assert raw JSON bytes — a typed decode cannot tell `[]` from `null`).

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **W — Hotfix** | R-1, R-2, R-6 | 2 ★ cases red → green; `make ci` **green** |
| X — Restore coverage | R-3, R-4 | T-35-equivalent present; `Paginate` additions covered |
| Y — Contract & backlog | R-5, R-7 | Query params per `api-2.md`; 9 cases present |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test -race -count=1`). **Do not append an execution entry claiming success until `make ci` exits zero** — this round's entry was written against a tree that fails both `fmt-check` and `cmd/api`.

Post-Phase-W verification, run verbatim:

```
gofmt -l ./cmd ./internal                         # expect: empty
go test ./... -race -count=1                      # expect: all ok
grep -rn 'sq.ILike{\|sq.NotEq{\|sq.Eq{' internal/platform/postgres/repository.go
# every map key in the results must be a literal or a validated identifier
```

### Live smoke test — now with a security case

Still never run. Item 7 is new and should be checked the moment Phase W lands:

1. Two concurrent `POST .../versions/publish` → one 200, one 409, no unique-constraint violation.
2. Two concurrent `PUT .../draft` with the same `rowVersion` → one 200, one 409.
3. `ReplaceGraph` leaves no orphan `workflow_edges`.
4. Stale `rowVersion` on `PATCH` → 409, not 404.
5. A published `workflow_versions` row is never subsequently UPDATEd.
6. Chained draft saves using the returned `rowVersion` → 200 without an intervening `GET`.
7. **New:** as a `viewer`, `GET /api/v1/workflows` carrying a hostile search column → **400**, and the Postgres log shows no statement containing the injected fragment.
