# Phase 2 Code Review — Findings

Full review of the `feat/phase-2-identity-authentication-and-tenant-safety` working tree against `.agents/prompts/reviewer.md`, covering Clean Architecture boundaries, tenant isolation, concurrency safety, and TDD quality gates.

**Verdict: 🔴 REJECTED — NEEDS REVISION** (9 blocking, 12 non-blocking)

Baseline at review time: `go build ./...` clean · `go vet ./...` clean · `go test ./...` all packages **ok** · `gofmt -l ./cmd ./internal` reports **4 files**.

---

## Quality Gates

| Gate | Status | Note |
|---|---|---|
| Plan in `.agents/plans/` | ✅ | 17 plan docs present |
| Action history logged | ✅ | `.agents/memory/action_history.md` current |
| Sentinel errors via `errors.New` | ✅ | `internal/domain/errors.go`, `internal/auth/repository.go`, `internal/tenant/repository.go` |
| UseCase free of `net/http` | ✅ | Verified across `auth.authUseCase`, `auth.userUseCase`, `tenant.tenantUseCase` |
| Engine purity | ➖ | `internal/engine/` is empty — N/A this phase |
| Tenant isolation (DB + Redis) | ✅ | Composite PK/FK schema; Redis keys `session:{tenant_id}:{user_id}:{session_id}` |
| Atomic claims | ➖ | No task claiming in this diff |
| SSRF validator | ➖ | No HTTP step execution in this diff |
| Goroutine safety / `context.Context` | ✅ | Single server goroutine; `ctx` threaded through every layer |
| Table-driven tests | ❌ | **N-10** — 1 of 13 test files uses a table loop |
| `%w` wrapping, no `panic()` | ✅ | Only generated mocks and the UoW re-panic idiom |
| `gofmt` clean | ❌ | **N-12** — 4 files unformatted |

**What holds up well.** The Clean Architecture refactor is sound. The UseCase layer carries zero transport dependencies, sentinel errors are consistently `errors.New`, error wrapping uses `%w` throughout, and every DB query and Redis key is tenant-scoped. The dummy-bcrypt timing defense in `Login`, the `http.MaxBytesReader` body caps, and the composite-key migration hardening are all real improvements.

**What blocks approval.** The revocation subsystem. Logout, logout-all-devices, and user deactivation each have at least one path where a token that should be dead stays alive — and one migration constraint errors at runtime the first time a user row is deleted.

---

## 🔴 Blocking Findings

### B-1: Composite FK `ON DELETE SET NULL` nulls a `NOT NULL` column

[migrations/000001_init_schema.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000001_init_schema.up.sql)

```sql
CONSTRAINT fk_audit_logs_tenant_actor FOREIGN KEY (tenant_id, actor_user_id)
    REFERENCES users(tenant_id, id) ON DELETE SET NULL
```

Postgres `ON DELETE SET NULL` without a column list nulls **all** referencing columns — including `tenant_id UUID NOT NULL`. Deleting any user row, or cascading a tenant delete, raises:

```
ERROR: null value in column "tenant_id" of relation "audit_logs"
       violates not-null constraint
```

and aborts the whole transaction. `fk_workflow_versions_tenant_creator` on `workflow_versions.created_by` has the identical defect.

> [!WARNING]
> This is not latent. `tenants` cascades to `users`, so the **first tenant deletion** trips it. `BaseRepository.DeleteByPK` also reaches it directly.

**Fix:** column-scoped `SET NULL` (PG 15+):
```sql
CONSTRAINT fk_audit_logs_tenant_actor FOREIGN KEY (tenant_id, actor_user_id)
    REFERENCES users(tenant_id, id) ON DELETE SET NULL (actor_user_id)
```

---

### B-2: Revocation fails **open** when Redis is unavailable

[main.go:194-202](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L194-L202) · [session_store.go:272-278](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L272-L278)

```go
// cmd/api/main.go — Redis down at startup logs a Warn, then:
} else {
    blacklist = auth.NewNoopTokenBlacklist()
    sessionStore = auth.NewNoopSessionStore()
}
```

```go
// internal/auth/session_store.go — the noop store SYNTHESIZES a valid session
func (n *noopSessionStore) GetSession(ctx context.Context, tenantID, userID, sessionID uuid.UUID) (*UserSession, error) {
    return &UserSession{SessionID: sessionID, UserID: userID, TenantID: tenantID}, nil
}

func (n *noopSessionStore) IsUserRevoked(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error) {
    return false, nil
}
```

If Redis is unreachable at startup the API serves traffic anyway, and in that mode `Logout`, `LogoutAllDevices`, and account deactivation are **all silent no-ops**. `GetSession` accepts any session ID the middleware hands it. Nothing after the startup `Warn` surfaces the degraded state.

A no-op store is a fine test double. It must not be the production fallback.

**Fix:** require Redis in `production`/`staging` and `os.Exit(1)` when unreachable, mirroring the existing `JWT_SECRET` guard. Additionally make the noop `GetSession` return `ErrSessionNotFound` so any residual noop path fails closed. Contrast with the middleware, which already fails closed correctly on Redis *errors* at [middleware.go:78-86](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L78-L86) — the gap is only the startup fallback.

---

### B-3: Blacklist TTL is shorter than the token it revokes

[usecase.go:236-243](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L236-L243)

```go
if err == nil {
    if claims.JTI() != "" {
        _ = u.blacklist.Revoke(ctx, claims.JTI(), 24*time.Hour)   // ← literal
    }
```

Refresh tokens live `cfg.JWTRefreshExpiry` (default **7 days**, [config.go:33](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/config/config.go#L33)). The blacklist entry for a logged-out refresh token expires after 24 hours, after which the JTI is no longer listed and the token is accepted again for its remaining ~6 days.

A revocation record must outlive the credential it revokes.

**Fix:** derive the TTL from the token — `time.Until(claims.ExpiresAt.Time)` — rather than any literal.

---

### B-4: `Refresh` mints a session ID it never persists

[usecase.go:181-194](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L181-L194)

```go
sessionID := claims.SessionID
if sessionID != uuid.Nil {
    sess, err := u.sessionStore.GetSession(ctx, claims.TenantID, claims.UserID(), sessionID)
    // ... validated and refreshed
} else {
    sessionID = uuid.New()      // ← invented, never written to Redis
}
```

When `claims.SessionID == uuid.Nil` — every refresh token issued before `sid` was introduced, i.e. all in-flight tokens during a deploy — `Refresh` invents a session ID, embeds it in the new token pair, and never calls `CreateSession`. The middleware then looks that session up at [middleware.go:88-98](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L88-L98), gets `ErrSessionNotFound`, and returns 401.

The refresh returns `200 OK` with tokens that are dead on arrival.

**Fix:** either persist a session in this branch, or reject `sid`-less tokens with `ErrUnauthorized` and force re-login.

---

### B-5: Deactivation revocation uses a hardcoded TTL and swallows its errors

[user_usecase.go:190-193](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L190-L193) · [user_usecase.go:217-218](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L217-L218)

```go
if deactivating {
    _ = u.sessionStore.SetUserRevokedBefore(ctx, usr.ID, time.Now(), 7*24*time.Hour)
    _ = u.sessionStore.RevokeAllUserSessions(ctx, usr.TenantID, usr.ID)
}
```

Two defects in three lines.

1. **Hardcoded TTL.** `7*24*time.Hour` ignores `cfg.JWTRefreshExpiry`. A deployment configured with a longer refresh expiry gets a revocation marker that expires *before* the tokens it suppresses — the deactivated user's session resurrects.
2. **Discarded errors.** A Redis failure leaves the user fully active in every live session while `UpdateUser`/`DeleteUser` return `200 OK`. The operator has no signal that the deactivation did not take.

`LogoutAllDevices` gets both right at [usecase.go:248-262](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L248-L262) — it uses `u.refreshExpiry` and propagates with `fmt.Errorf(...: %w)`. This path should match it.

**Fix:** inject `refreshExpiry` into `userUseCase` and propagate both errors.

---

### B-6: Revocation comparison loses the current second

[session_store.go:188-193](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L188-L193)

```go
revokedUnix, err := strconv.ParseInt(val, 10, 64)
// ...
return issuedAt.Unix() < revokedUnix, nil
```

Both sides are second-granularity. A token issued in the **same second** as the logout-all compares equal, not less, and survives revocation.

That is precisely the "log out everywhere while a request is in flight" race the mechanism exists to close: a token minted at `T.900s` against a revocation stamped `T.100s` is treated as pre-dating the revocation.

**Fix:** use `<=`, or store the marker with `UnixMilli` for real sub-second ordering.

---

### B-7: Login discloses account existence via status code

[usecase.go:114-121](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L114-L121) · [handler.go:56-66](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L56-L66)

```go
// usecase.go
if !usr.IsActive {
    _ = u.passSvc.ComparePassword(dummyBcryptHash, password)
    return nil, ErrUserInactive          // ← distinct from ErrUnauthorized
}
```
```go
// handler.go
if errors.Is(err, ErrUserInactive) {
    respondJSONError(w, http.StatusForbidden, ...)   // 403
}
if errors.Is(err, ErrUnauthorized) {
    respondJSONError(w, http.StatusUnauthorized, ...) // 401
}
```

The dummy-bcrypt timing defense at [usecase.go:99](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L99), [:108](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L108), and [:115](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L115) is well built — and then undone one layer up. On an unauthenticated endpoint, 403-vs-401 is a reliable account-enumeration oracle over `(tenantSlug, email)`, with no password knowledge required.

**Fix:** return 401 for every login failure; log the inactive reason server-side. `Refresh`'s `ErrUserInactive` → 403 at [handler.go:88-91](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L88-L91) is fine and should stay — that caller already holds a valid token.

---

### B-8: `ErrUserAlreadyExists` has no producer

[user_handler.go:52-56](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go#L52-L56) · [repository.go:169-194](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L169-L194)

```go
// user_handler.go maps it to 409...
if errors.Is(err, ErrUserAlreadyExists) {
    respondJSONError(w, http.StatusConflict, "Conflict", "user email already exists in tenant")
```

…but nothing returns it. `postgresUserRepository.CreateUser` → `BaseRepository.Create` wraps the raw pgx error and nothing anywhere inspects SQLSTATE:

```
$ grep -rn "ErrUserAlreadyExists" --include=*.go .
internal/auth/user_handler.go:53:  if errors.Is(err, ErrUserAlreadyExists) {   ← only consumer
internal/auth/repository.go:19:    ErrUserAlreadyExists = errors.New(...)      ← only declaration

$ grep -rn "23505\|pgconn.PgError\|UniqueViolation" --include=*.go internal
(no matches)
```

Creating a user with an email already taken in the tenant violates `uq_users_tenant_email` and returns **500 Internal Error**, not 409. The same gap makes `UpdateUser` with a colliding email a 500.

**Fix:** map `pgconn.PgError.Code == "23505"` to `domain.ErrConflict` in the platform layer; `internal/auth/repository.go` translates the `uq_users_tenant_email` constraint to `ErrUserAlreadyExists`.

---

### B-9: Refresh rotation is non-atomic and its revoke is discarded

[usecase.go:208-215](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L208-L215)

```go
pair, err := u.jwtSvc.GenerateTokenPair(...)   // ← new pair issued FIRST
if err != nil {
    return nil, fmt.Errorf("generate tokens on refresh: %w", err)
}

if claims.JTI() != "" {
    _ = u.blacklist.Revoke(ctx, claims.JTI(), u.refreshExpiry)   // ← then revoked, error dropped
}
```

The old refresh JTI is blacklisted *after* the replacement is minted, with the error discarded. Two consequences:

- **Race.** Two concurrent refreshes with the same token both pass the `IsRevoked` check at [usecase.go:163-171](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L163-L171) before either revokes, and both receive valid pairs.
- **Silent failure.** If Redis errors, the old token is never invalidated at all, yet the caller gets `200 OK` — a stolen refresh token remains replayable for its full lifetime.

**Fix:** revoke-then-issue, and treat a failed revoke as a failed refresh.

---

## 🟡 Non-Blocking Findings

### N-1: `SCAN`-based session listing is O(keyspace) with unstable pagination

[session_store.go:130-145](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L130-L145) · [session_store.go:208-240](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L208-L240)

Both `RevokeAllUserSessions` and `ListUserSessions` walk the entire Redis keyspace on every call. Worse, `SCAN` guarantees no stable ordering between invocations, so paging through `ListUserSessions` can duplicate and skip sessions while `total` shifts between pages.

**Fix:** maintain a per-user index `session:index:{tenant_id}:{user_id}` as a Redis `SET`, written in `CreateSession` and pruned in `RevokeSession`; read it with `SMEMBERS`/`SSCAN`.

### N-2: Sliding sessions never truly expire

[usecase.go:190-191](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L190-L191)

```go
sess.LastActiveAt = time.Now()
_ = u.sessionStore.CreateSession(ctx, sess, u.refreshExpiry)   // resets TTL, leaves ExpiresAt stale
```

The Redis TTL resets on each refresh but `sess.ExpiresAt` keeps its original value, so `ListSessions` reports live sessions as long-expired. There is also no absolute cap — a session can be extended indefinitely.

### N-3: `claims.IssuedAt` nil dereference

[middleware.go:78](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L78) · [usecase.go:173](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L173)

Both dereference `claims.IssuedAt.Time`, a `*jwt.NumericDate` that `jwt/v5` does not require to be present. Only reachable with a token signed by our own secret, so the risk is low — but a nil guard is one line and the alternative is a panic inside a request handler.

### N-4: `X-Forwarded-For` trusted unconditionally

[handler.go:45-52](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L45-L52)

The header is recorded as the session IP with no trusted-proxy check, making the session audit trail trivially spoofable by any client.

### N-5: No role guard on user read routes

[main.go:139-140](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L139-L140)

`GET /api/v1/users` and `GET /api/v1/users/{userId}` are authenticate-only, so a `viewer` can enumerate every account and role in the tenant. The write routes are correctly gated with `RequireRole("admin")`.

### N-6: Role change does not revoke sessions

[user_usecase.go:167-173](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L167-L173)

Demoting `admin` → `viewer` leaves the old role embedded in live access tokens until they expire (default 15m). Deactivation revokes; a privilege downgrade should too.

### N-7: JWT-secret check is unreachable without a database

[main.go:185-192](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L185-L192)

The `len(jwtSecret) < 32` production guard sits inside `if dbPool != nil`. A production boot with Postgres down skips the validation entirely — and serves no auth routes at all, only `/health`.

### N-8: `init()` does bcrypt work at package load and discards the error

[usecase.go:18-24](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L18-L24)

```go
func init() {
    passSvc := NewPasswordService()
    h, _ := passSvc.HashPassword("flowforge-dummy-password-never-used")   // ← error dropped
    dummyBcryptHash = h
}
```

A cost-12 hash runs on import (~250ms, paid by every test binary). On failure `dummyBcryptHash` is `""` and `ComparePassword` returns instantly — defeating the exact timing defense it exists to provide. Use `sync.OnceValue` and check the error.

### N-9: `structToMap` silently produces an empty column name

[repository.go:286-298](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L286-L298)

A field with neither a `db` nor a `json` tag falls through to `setMap[""] = value`, producing a malformed `INSERT` at runtime. All current domain structs are fully tagged, so this is latent — skip untagged fields rather than relying on that.

### N-10: Tests are not table-driven

Only [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go) uses a table loop; the other 12 files are one-scenario-per-function. `.agents/prompts/reviewer.md` §4 requires table-driven patterns.

### N-11: `user_handler.go` has no test file

[user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go) — 5 endpoints, ~230 lines, zero coverage. Every other handler in the package is tested.

### N-12: `gofmt` violations

`gofmt -l ./cmd ./internal` reports:
- `internal/platform/config/config.go`
- `internal/platform/postgres/repository.go` (misaligned `Filters` field at [:102](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L102), `sq.Eq{k:v}` spacing at [:126-127](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository.go#L126-L127))
- `internal/auth/handler_test.go`
- `internal/auth/usecase_test.go`

The `Makefile` has `vet` and `test` targets but no `fmt`.

---

## Remediation

See [phase_2_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_review_remediation_plan.md) for the phased execution plan covering all 21 findings.
