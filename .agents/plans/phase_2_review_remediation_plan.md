# Implementation Plan — Phase 2 Code Review Remediation

This plan remediates all 21 findings identified in [phase_2_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_review_findings.md) — 9 blocking (B-1 … B-9) and 12 non-blocking (N-1 … N-12) — covering schema integrity, token revocation correctness, transport error mapping, and code quality.

Work is ordered so the highest-severity security defects land first. Each phase is independently shippable and ends green on `go build ./... && go vet ./... && go test ./internal/... -race -count=1`.

---

## User Review Required

> [!IMPORTANT]
> - **B-1 (Migration amended in place)**: `000001_init_schema.up.sql` is the initial schema and, per `.agents/memory/action_history.md`, has not been released to any shared environment. This plan **edits it in place** rather than adding `000002`. If any environment has already applied it, say so — the fix becomes a forward migration that drops and recreates both constraints.
> - **B-1 (PostgreSQL 15 floor)**: Column-scoped `ON DELETE SET NULL (col)` requires **PG 15+**. This plan adopts it. On older servers the fallback is `ON DELETE NO ACTION` plus application-level nulling before delete.
> - **B-2 (Redis becomes a hard dependency)**: In `production`/`staging` the API will `os.Exit(1)` when Redis is unreachable, instead of degrading to no-op revocation. Local development is unaffected — `ENV=development` still boots without Redis. This is a deliberate availability-for-security trade.
> - **B-6 (Revocation key format change)**: `user:revoked_before:{user_id}` switches from Unix seconds to `UnixMilli`. Existing keys become unparseable and are treated as "no revocation" until rewritten. Since the TTL is bounded by `JWTRefreshExpiry`, the window self-heals; alternatively use a new key prefix to avoid ambiguity entirely.
> - **N-5 (Role gating on user reads)**: This plan gates `GET /api/v1/users` (list) behind `RequireRole("admin", "editor")` while leaving `GET /api/v1/users/{userId}` open to any authenticated tenant member. Confirm this matches the intended permission model.

---

## Phase A — Schema Integrity (B-1)

#### [MODIFY] [000001_init_schema.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql)

- `fk_audit_logs_tenant_actor` on `audit_logs`: change `ON DELETE SET NULL` → `ON DELETE SET NULL (actor_user_id)`.
- `fk_workflow_versions_tenant_creator` on `workflow_versions`: change `ON DELETE SET NULL` → `ON DELETE SET NULL (created_by)`.
- Audit every other composite FK in the file for the same pattern. The `ON DELETE CASCADE` constraints (`fk_workflow_versions_tenant_workflow`, `fk_workflow_nodes_tenant_version`, `fk_step_runs_tenant_run`, `fk_step_runs_tenant_node`, `fk_execution_logs_tenant_run`, `fk_execution_logs_tenant_step`, `fk_idempotency_keys_tenant_wf`, `fk_idempotency_keys_tenant_run`) are **correct as written** — `CASCADE` deletes the row rather than nulling columns, so `NOT NULL` is never violated. Only the two `SET NULL` constraints need changing.
- Add a comment above each amended constraint noting the PG 15+ requirement.

### Test Strategy — Phase A
- Apply the migration against a local PG 15+ instance, insert a tenant + user + `audit_logs` row referencing that user, then `DELETE FROM users WHERE id = ...`. Assert the delete succeeds and `audit_logs.actor_user_id IS NULL` while `tenant_id` is preserved.
- Repeat the cascade case: `DELETE FROM tenants WHERE id = ...` must succeed and remove all dependent rows without a not-null violation.

---

## Phase B — Revocation Correctness (B-2, B-3, B-4, B-5, B-6, B-9, N-7)

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

- **N-7 first** — hoist the `JWT_SECRET` length validation out of the `if dbPool != nil` block (currently [:189-192](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L189-L192)) to immediately after `config.Load()`, so it runs regardless of database availability.
- **B-2** — add a matching startup guard for Redis. When `redis.NewClient` fails and `cfg.Environment` is `production` or `staging`, `log.Error` + `os.Exit(1)` instead of falling through to the no-op stores. Keep the `Warn`-and-continue path for `development`/`test`.
- Introduce a small `isProductionLike(env string) bool` helper rather than repeating the `env == "production" || env == "staging"` literal in two places.
- Pass `cfg.JWTRefreshExpiry` into `auth.NewUserUseCase` (see the constructor change below).

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)

- **B-2** — `noopSessionStore.GetSession` returns `nil, ErrSessionNotFound` instead of synthesizing a session. `noopSessionStore.IsUserRevoked` may keep returning `false, nil` (a user with no session cannot authenticate anyway once `GetSession` denies), but document the fail-closed intent in a comment on the type.
- **B-6** — `SetUserRevokedBefore` writes `strconv.FormatInt(revokedAt.UnixMilli(), 10)`; `IsUserRevoked` parses it and compares `issuedAt.UnixMilli() <= revokedUnixMilli`. Keeping `<=` alongside millisecond precision closes both the same-second and same-millisecond edges.
- Leave `CreateSession`/`RevokeSession` key derivation untouched — tenant scoping is already correct.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)

- **B-3** — replace the `24*time.Hour` literal in `Logout` ([:238](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L238)) with a TTL derived from the token:
  ```go
  ttl := time.Until(claims.ExpiresAt.Time)
  if ttl > 0 {
      if err := u.blacklist.Revoke(ctx, claims.JTI(), ttl); err != nil {
          return fmt.Errorf("revoke token on logout: %w", err)
      }
  }
  ```
  Note this changes `Logout` from always returning `nil` to propagating errors — update [handler.go:107](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L107) to map a failure to 500 rather than discarding it with `_ =`.
- **B-4** — in `Refresh`, remove the `sessionID = uuid.New()` fallback at [:192-194](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L192-L194). A `sid`-less refresh token is a pre-migration artifact: return `ErrUnauthorized` and force re-login. (Persisting a synthetic session is the alternative, but it silently converts an unverifiable token into a trusted session — rejecting is the safer default.)
- **B-9** — reorder `Refresh` to revoke-then-issue: move the `u.blacklist.Revoke` call from [:213-215](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L213-L215) to *before* `GenerateTokenPair`, and propagate its error with `fmt.Errorf("revoke rotated refresh token: %w", err)`. This closes the concurrent-double-refresh race: the second caller's `IsRevoked` check now sees the revocation.
- **N-2** — set `sess.ExpiresAt = time.Now().Add(u.refreshExpiry)` alongside `sess.LastActiveAt` at [:190](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L190) so the persisted metadata matches the Redis TTL. Add an absolute cap: if `time.Since(sess.CreatedAt) > maxSessionLifetime` (suggest 30d, or a new config value), refuse to extend and return `ErrUnauthorized`.
- **N-3** — guard `claims.IssuedAt` before dereferencing at [:173](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L173); treat a missing `iat` as `ErrUnauthorized`.
- **N-8** — replace `init()` at [:18-24](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L18-L24) with `sync.OnceValue`, checking the hash error and falling back to a fixed valid bcrypt string so the timing defense holds even on failure.

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **B-5** — add `refreshExpiry time.Duration` to the `userUseCase` struct and to `NewUserUseCase(userRepo, passSvc, sessionStore, refreshExpiry)`, defaulting to `7*24*time.Hour` when `<= 0` (mirroring `NewAuthUseCase` at [usecase.go:68-70](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L68-L70)).
- Replace both `7*24*time.Hour` literals ([:191](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L191), [:217](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L217)) with `u.refreshExpiry`.
- Propagate both revocation errors instead of `_ =`, wrapping with `%w`. Extract the repeated pair into a private helper — it now appears in `UpdateUser`, `DeleteUser`, and (after N-6) the role-change path:
  ```go
  func (u *userUseCase) revokeUserAccess(ctx context.Context, usr *domain.User) error
  ```
  Model the error handling on `authUseCase.LogoutAllDevices` at [usecase.go:248-262](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L248-L262), which already does this correctly.

#### [REGENERATE] Mocks

`SessionStore` is unchanged in signature, but `UserUseCase` construction changes. Run `make mocks` (mockery, config at [.mockery.yaml](file:///home/mohyasiralfarizi/Golang/flowforge/.mockery.yaml)) and update call sites in `usecase_test.go`, `user_usecase_test.go`, and `handler_test.go`.

### Test Strategy — Phase B
- `TestNoopSessionStore_FailsClosed` — noop `GetSession` returns `ErrSessionNotFound`; assert `AuthMiddleware.Authenticate` yields 401 when wired with it.
- `TestAuthUseCase_Logout_BlacklistTTLCoversTokenLifetime` — assert the TTL passed to `Revoke` is within a tolerance of `time.Until(claims.ExpiresAt)`, not a 24h literal.
- `TestAuthUseCase_Refresh_RejectsTokenWithoutSessionID` — `sid == uuid.Nil` returns `ErrUnauthorized` and never calls `GenerateTokenPair`.
- `TestAuthUseCase_Refresh_RevokesBeforeIssuing` — use mockery ordering to assert `Revoke` precedes `GenerateTokenPair`; a `Revoke` error aborts the refresh.
- `TestSessionStore_IsUserRevoked_SameInstant` — a token issued at the exact revocation timestamp is reported revoked.
- `TestUserUseCase_Deactivate_PropagatesRevocationError` — a `SessionStore` error surfaces from `UpdateUser`/`DeleteUser` rather than being swallowed.
- `TestUserUseCase_Deactivate_UsesConfiguredExpiry` — assert the TTL argument equals the injected `refreshExpiry`.

---

## Phase C — Transport & Error Mapping (B-7, B-8, N-3, N-4, N-5, N-6)

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go) + [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)

- **B-7** — `Login` returns `ErrUnauthorized` for the inactive-account case at [:114-117](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L114-L117), keeping the dummy-hash comparison. Remove the `ErrUserInactive` branch from `AuthHandler.Login` at [handler.go:57-60](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L57-L60) so every login failure is a uniform 401 with an identical body.
- Emit a server-side log at the use-case boundary recording the real reason (inactive vs. bad credentials) so operators retain the signal the client no longer gets.
- **Leave `Refresh` alone** — its `ErrUserInactive` → 403 at [handler.go:88-91](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L88-L91) is correct; that caller already proved identity.
- **N-4** — gate `X-Forwarded-For` parsing at [:45-52](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L45-L52) behind a `TrustedProxy` config flag (new `TRUST_PROXY_HEADERS` env var, default `false`). When untrusted, use `r.RemoteAddr` only.

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go)

- **B-8** — add an exported helper that classifies pgx driver errors:
  ```go
  // IsUniqueViolation reports whether err is a Postgres unique-constraint
  // violation (SQLSTATE 23505), and returns the violated constraint name.
  func IsUniqueViolation(err error) (string, bool)
  ```
  implemented with `errors.As(err, &pgErr)` on `*pgconn.PgError`. `github.com/jackc/pgx/v5/pgconn` is a subpackage of the existing `pgx/v5` dependency — **no `go.mod` change required**.
- Have `BaseRepository.Create` and `BaseRepository.Update` return `domain.ErrConflict` (already declared at [errors.go:16](file:///home/mohyasiralfarizi/Golang/flowforge/internal/domain/errors.go#L16)) wrapped with the constraint name, so the platform layer stays domain-agnostic.
- **N-9** — in `structToMap` at [:286-298](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L286-L298), `continue` when the resolved `dbTag` is empty instead of writing `setMap[""]`.

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go)

- **B-8** — in `CreateUser` and `UpdateUser`, translate a `domain.ErrConflict` on the `uq_users_tenant_email` constraint into `ErrUserAlreadyExists`, giving [user_handler.go:53](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go#L53) a real producer and turning duplicate-email into 409.
- **N-6** — also map the conflict in `UpdateUser` so an email collision on update returns 409 rather than 500.
- Note: `postgresUserRepository.DeleteUser` at [:147-157](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go#L147-L157) is not on the `UserRepository` interface and has no caller — either add it to the interface or delete it.

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **N-6** — in `UpdateUser` at [:167-173](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L167-L173), track whether `Role` actually changed and, if so, call the `revokeUserAccess` helper introduced in Phase B so a privilege downgrade takes effect immediately rather than at access-token expiry.

#### [MODIFY] [middleware.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go)

- **N-3** — nil-guard `claims.IssuedAt` before [:78](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L78); a token without `iat` is rejected as unauthorized rather than panicking inside the handler.

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

- **N-5** — wrap `GET /api/v1/users` at [:139](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L139) in `auth.RequireRole("admin", "editor")`. Leave `GET /api/v1/users/{userId}` at [:140](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L140) authenticate-only (see the User Review callout).

### Test Strategy — Phase C
- `TestAuthHandler_Login_UniformFailureResponse` — table-driven over {unknown tenant, unknown email, wrong password, inactive account}; assert all four produce byte-identical 401 bodies.
- `TestIsUniqueViolation` — table-driven over a synthetic `*pgconn.PgError{Code: "23505", ConstraintName: ...}`, a non-unique pg error, and a plain `errors.New`.
- `TestUserHandler_CreateUser_DuplicateEmailReturns409` — mock `UserUseCase` returning `ErrUserAlreadyExists`; assert 409.
- `TestAuthMiddleware_RejectsTokenWithoutIssuedAt` — hand-craft claims with a nil `IssuedAt`; assert 401 and no panic.
- `TestUserUseCase_RoleChange_RevokesSessions` — assert `SetUserRevokedBefore` + `RevokeAllUserSessions` fire on `admin` → `viewer`, and do **not** fire when the role is unchanged.
- `TestNewRouter_UserListRequiresElevatedRole` — a `viewer` token gets 403 on `GET /api/v1/users`.

---

## Phase D — Quality & Hygiene (N-1, N-10, N-11, N-12)

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)

- **N-1** — introduce a per-user index key `session:index:{tenant_id}:{user_id}` held as a Redis `SET`:
  - `CreateSession` — `SADD` the session ID and `EXPIRE` the index to `ttl` in the same pipeline as the session `SET`.
  - `RevokeSession` — `SREM` the session ID alongside the `DEL`.
  - `RevokeAllUserSessions` — `SMEMBERS` the index, `DEL` each session key, then `DEL` the index. Replaces the `SCAN` loop at [:130-145](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L130-L145).
  - `ListUserSessions` — `SMEMBERS`, sort by session ID for stable ordering, then slice for pagination and `MGET` the page. Replaces the `SCAN` loop at [:208-240](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L208-L240) and fixes the unstable-pagination defect.
  - Prune index entries whose session key has expired (a `MGET` miss) so the count stays accurate.

#### [MODIFY] [Makefile](file:///home/mohyasiralfarizi/Golang/flowforge/Makefile)

- **N-12** — add `fmt` (`gofmt -w ./cmd ./internal`) and `fmt-check` (`test -z "$$(gofmt -l ./cmd ./internal)"`) targets; add both to `.PHONY`. Wire `fmt-check` ahead of `test` so formatting regressions fail fast.
- Run `gofmt -w` over the 4 offending files: `internal/platform/config/config.go`, `internal/platform/postgres/repository.go`, `internal/auth/handler_test.go`, `internal/auth/usecase_test.go`.

#### [CREATE] [user_handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler_test.go)

- **N-11** — cover all five endpoints of [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go) using the existing `mocks.NewMockUserUseCase` expecter pattern already established in `handler_test.go`. Per endpoint: happy path, missing auth context (401), malformed `userId` (400), and the mapped domain errors (`ErrUserAlreadyExists` → 409, `ErrUserNotFound` → 404, `ErrCannotDeleteSelf`/`ErrCannotDeactivateSelf` → 403, unknown → 500).

#### [MODIFY] Test suite conversion

- **N-10** — convert the 12 non-table test files to table-driven form, following the shape already used in [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go). Prioritize the files with the most scenarios: `usecase_test.go` (5 funcs), `handler_test.go` (5), `user_usecase_test.go` (3), `jwt_test.go` (3). Each case gets a `name` field and runs under `t.Run(tt.name, ...)`.

### Test Strategy — Phase D
- `TestSessionStore_IndexPagination` — create 25 sessions, page through at `pageSize=10`, assert every session appears exactly once across pages and `total` is stable between calls (the defect the `SCAN` implementation cannot satisfy).
- `TestSessionStore_RevokeAll_ClearsIndex` — after `RevokeAllUserSessions`, both the session keys and the index key are gone.
- `TestSessionStore_ListPrunesExpiredIndexEntries` — an index entry whose session key expired is not counted in `total`.
- Full-suite gate: `make fmt-check && make vet && go test ./... -race -count=1`.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| A — Schema | B-1 | Migration applies; user + tenant deletion succeed on PG 15+ |
| B — Revocation | B-2 … B-6, B-9, N-2, N-3, N-7, N-8 | `go test ./internal/auth/... -race`; manual: logout invalidates for the token's full lifetime |
| C — Transport | B-7, B-8, N-4, N-5, N-6, N-9 | `go test ./... -race`; manual: duplicate email → 409, all login failures → identical 401 |
| D — Quality | N-1, N-10, N-11, N-12 | `make fmt-check && make vet && go test ./... -race -count=1` |

After each phase:
1. `go build ./...` — clean.
2. `go vet ./...` — clean.
3. `go test ./... -race -count=1` — all packages ok.
4. `gofmt -l ./cmd ./internal` — empty (from Phase D onward).
5. Append an execution entry to `.agents/memory/action_history.md` per `.agents/AGENTS.md` §2.

End-to-end smoke test once all phases land, against a live Postgres + Redis:
1. `POST /api/v1/auth/login` → capture the token pair.
2. `GET /api/v1/users/me` with the access token → 200.
3. `POST /api/v1/auth/logout`, then `POST /api/v1/auth/refresh` with the same refresh token → 401 (verifies B-3, B-9).
4. Re-login, `POST /api/v1/auth/logout-all`, then immediately reuse the access token → 401 (verifies B-6's same-instant edge).
5. As an admin, `PATCH /api/v1/users/{id}` with `{"isActive": false}` on a second account, then use that account's access token → 401 (verifies B-5).
6. `POST /api/v1/users` twice with the same email → 201 then 409 (verifies B-8).
7. Stop Redis and restart the API with `ENV=production` → process exits non-zero (verifies B-2).
