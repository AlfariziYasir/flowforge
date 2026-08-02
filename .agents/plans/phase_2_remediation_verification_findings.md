# Phase 2 Remediation — Verification Review Findings

Post-execution review of the fixes applied for [phase_2_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_review_findings.md) per [phase_2_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_review_remediation_plan.md).

**Verdict: 🔴 REJECTED — NEEDS REVISION** (2 critical, 2 high, 4 medium, 7 low)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./...` all packages **ok**.

> [!WARNING]
> The suite is green because **none of the 16 regression tests named in the remediation plan were written** (see R-8). Both critical defects below were reproduced locally against the real `AuthMiddleware` and `jwtService`; neither is detected by any existing test.

---

## Remediation Scorecard

| Original finding | Result | Note |
|---|---|---|
| B-1 Composite FK `SET NULL` | ✅ | Also fixed `fk_workflows_current_version`, which the original review missed |
| B-2 Redis fail-open | ⚠️ Partial | Prod exit ✅, noop `GetSession` fail-closed ✅ — but see **R-5** |
| B-3 Blacklist TTL | ✅ | `time.Until(claims.ExpiresAt.Time)`, error propagated to 500 |
| B-4 Unpersisted session ID | ✅ | `sid == uuid.Nil` → `ErrUnauthorized` |
| B-5 Deactivation TTL/errors | ✅ | `refreshExpiry` injected; `revokeUserAccess` helper propagates |
| B-6 Same-second revocation | 🔴 **Regressed** | See **R-2** |
| B-7 Login enumeration | ✅ | Uniform 401 — but see **R-11** |
| B-8 `ErrUserAlreadyExists` | ⚠️ Partial | Create ✅, update handler ❌ — see **R-6** |
| B-9 Non-atomic rotation | ✅ | Revoke-then-issue, error propagated |
| N-1 `SCAN` listing | ⚠️ Partial | Index ✅, ordering ❌ — see **R-7**, **R-4** |
| N-2 Sliding sessions | ✅ | `ExpiresAt` synced + 30d absolute cap |
| N-3 `IssuedAt` nil deref | ⚠️ Partial | `usecase.go` ✅, `middleware.go` ❌ — see **R-3** |
| N-4 `X-Forwarded-For` | ✅ | Gated behind `TRUST_PROXY_HEADERS`, default false |
| N-5 Role guard on reads | ✅ | `RequireRole("admin", "editor")` on list |
| N-6 Role change revocation | ✅ | `roleChanged` tracked, no-op changes excluded |
| N-7 JWT check hoisted | ⚠️ Partial | Hoisted ✅ — but introduced **R-1** |
| N-8 `init()` bcrypt | ✅ | `sync.OnceValue`; fallback hash verified valid (60 chars, cost 12, 240 ms) |
| N-9 `structToMap` empty key | ✅ | |
| N-10 Table-driven tests | ❌ | Not done — see **R-10** |
| N-11 `user_handler_test.go` | ✅ | Created, 5 endpoints covered |
| N-12 `gofmt` + Makefile | ✅ | `fmt`/`fmt-check` targets added, `test` gates on `fmt-check` |

---

## 🔴 Critical

### R-1: Production boots with the public default JWT secret

[main.go:156](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L156) · [config.go:32](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/config/config.go#L32)

Hoisting the JWT validation (N-7) dropped the **unset** check that used to guard it:

```go
// DELETED in the refactor
jwtSecret := os.Getenv("JWT_SECRET")
if jwtSecret == "" {
    if cfg.Environment == "production" || cfg.Environment == "staging" {
        log.Error("JWT_SECRET environment variable must be set in production/staging")
        os.Exit(1)
    }
    jwtSecret = "flowforge-dev-secret-change-in-prod-12345"
}
```

`config.Load()` now supplies that literal as the default, and `main.go` retains only the length check:

```go
if len(cfg.JWTSecret) < 32 && isProductionLike(cfg.Environment) { os.Exit(1) }
```

```
default JWT_SECRET len = 41 ; passes 'len < 32' guard? true
```

41 ≥ 32, so a production deploy that omits `JWT_SECRET` starts **silently** on a secret committed to this repository. Anyone who can read the source can forge an access token for any tenant with `role: "admin"`. Every downstream control — composite-key tenant isolation, the session store, the JTI blacklist — sits behind a signature that no longer authenticates anything.

**Fix:** distinguish "unset" from "set to a short value". Leave `Config.JWTSecret` empty when the env var is absent and apply the dev default only outside production/staging, or reject the known dev literal explicitly in the `isProductionLike` branch.

---

### R-2: Logout-all locks the user out of logging back in

[session_store.go:177](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L177) · [session_store.go:205](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L205)

B-6 was fixed by switching the revocation marker to `UnixMilli` **and** changing `<` to `<=`. But JWT `iat` is truncated to whole seconds — `jwt.TimePrecision` defaults to `1s`, and `jwt.NewNumericDate` truncates in memory *before* signing:

```
jwt.TimePrecision = 1s
in-memory iat  = 12:00:00        (from a 12:00:00.700 input)
serialized iat = 1785672000
revokedBefore  = 1785672000500   (time.Now() at 12:00:00.500)
NEW token issued AFTER revocation judged revoked? true
```

A revocation stamped mid-second therefore marks as revoked every token issued *later* in that same second. Reproduced end-to-end against the real middleware:

```
brand-new access token -> status 401,
  body {"error":"Unauthorized","message":"user token has been revoked"}
```

`Login` never consults `IsUserRevoked`, so the login itself returns `200` with a valid-looking token pair; the failure only appears on the next request and persists until the user retries in a later second (~500 ms average dead window).

Triggered by `LogoutAllDevices`, by deactivation, and — since N-6 wired role changes into `revokeUserAccess` — by every role change. "Log out everywhere, then sign back in" is the canonical flow this breaks.

> [!NOTE]
> The pre-remediation code (seconds + `<`) handled re-login correctly and missed only the sub-second in-flight case. This change traded a narrow hole for a broad one.

**Fix:** second-granularity `iat` cannot distinguish the two cases, so the precision must come from the token. Set `jwt.TimePrecision = time.Millisecond` and keep `UnixMilli` + `<=`. Verified working:

```
revokedAt   = 1785654147188 ms
parsed iat  = 1785654147190 ms
new token judged revoked?                  false   ← lockout fixed
older token judged revoked by later revoke? true   ← B-6 still closed
```

---

## 🟠 High

### R-3: Middleware panics on a token with no `iat`

[middleware.go:78](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L78)

N-3 was applied to [usecase.go:165](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L165) but not to the middleware, which still dereferences `claims.IssuedAt.Time` unguarded:

```go
revokedUser, err := m.sessionStore.IsUserRevoked(r.Context(), claims.UserID(), claims.IssuedAt.Time)
```

```
REPRO CONFIRMED: middleware panicked:
  runtime error: invalid memory address or nil pointer dereference
```

Reachable only with a token signed by our own secret — which **R-1** makes materially easier to obtain against a misconfigured deployment.

**Fix:** mirror the `usecase.go` guard; treat a missing `iat` as `ErrUnauthorized` → 401.

---

### R-4: Pre-existing Redis sessions are invisible to the new index

[session_store.go:140-166](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L140-L166) · [session_store.go:208-231](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L208-L231)

`RevokeAllUserSessions` now derives its key list purely from `SMEMBERS session:index:{tenant}:{user}`. Sessions written by the previous `SCAN`-based implementation carry no index entry, so immediately after deploying this change **logout-all silently fails to revoke every session created before the upgrade** while still returning `200 OK`. `ListUserSessions` has the same blind spot.

This is the same class of deploy-window defect as B-4, which this remediation was meant to close.

**Fix:** a one-time `SCAN` fallback when the index key is absent, backfilling the index as it goes — or an explicit `session:*` flush as a documented release step.

---

## 🟡 Medium

### R-5: Development without Redis is silently broken

[session_store.go:294-300](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L294-L300) · [main.go:176-180](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L176-L180)

Failing `noopSessionStore.GetSession` closed is correct, but `CreateSession` still returns `nil`. With `ENV=development` and Redis down, `Login` succeeds and issues a token carrying a `sid`; the middleware then resolves that `sid` to `ErrSessionNotFound` and **every** authenticated request 401s. The only signal is one startup `Warn`.

**Fix:** make the noop `CreateSession` return an error so the failure surfaces at login with a clear message, rather than handing out tokens that are dead on arrival.

### R-6: B-8 is only half-wired for updates

[user_handler.go:180-191](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go#L180-L191) · [repository.go:139-141](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go#L139-L141)

`postgresUserRepository.UpdateUser` correctly maps `domain.ErrConflict` → `ErrUserAlreadyExists`, but `UserHandler.UpdateUser` has no `ErrUserAlreadyExists` branch — only `CreateUser` does. `PATCH /api/v1/users/{userId}` with a colliding email still returns **500**, which was exactly the B-8 defect.

### R-7: Session pagination is still unstable

[session_store.go:221](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L221)

The plan called for sorting by session ID before slicing; there is no `sort` in the file. `SMEMBERS` carries no ordering guarantee, so paging through `ListUserSessions` can still duplicate and skip sessions. The O(keyspace) half of N-1 is fixed; the correctness half is not.

### R-8: None of the planned regression tests were written

All 16 cases named in the remediation plan's Test Strategy sections are absent — verified by name across the suite:

```
NoopSessionStore_FailsClosed          => MISSING
Logout_BlacklistTTL                   => MISSING
Refresh_RejectsTokenWithoutSessionID  => MISSING
Refresh_RevokesBeforeIssuing          => MISSING
IsUserRevoked_SameInstant             => MISSING
Deactivate_PropagatesRevocationError  => MISSING
Deactivate_UsesConfiguredExpiry       => MISSING
Login_UniformFailureResponse          => MISSING
IsUniqueViolation                     => MISSING
DuplicateEmailReturns409              => MISSING
RejectsTokenWithoutIssuedAt           => MISSING
RoleChange_RevokesSessions            => MISSING
UserListRequiresElevatedRole          => MISSING
IndexPagination                       => MISSING
RevokeAll_ClearsIndex                 => MISSING
PrunesExpired                         => MISSING
```

`IsUserRevoked_SameInstant` and `RejectsTokenWithoutIssuedAt` would each have caught a defect above. The suite is green because it does not exercise the changed behavior.

---

## 🟢 Low

### R-9: `revokeUserAccess` failure leaves a committed write behind

[user_usecase.go:205-213](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L205-L213) · [user_usecase.go:233-239](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L233-L239)

`userRepo.UpdateUser` has already committed when revocation fails, so the caller gets 500 with the user deactivated in Postgres but sessions still live. Error propagation is now correct (B-5); the atomicity is not. The existing `UnitOfWork` in `internal/platform/postgres` is the natural fit.

### R-10: N-10 not done

Still 1 table loop across the 11 `internal/auth` + `internal/tenant` test files; the new [user_handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler_test.go) is 5 one-scenario functions.

### R-11: B-7's operator signal was dropped, not relocated

[usecase.go:116-119](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L116-L119) returns `ErrUnauthorized` for inactive accounts with no server-side log. The plan called for logging the real reason at the use-case boundary; the inactive-vs-bad-password distinction is now lost to operators as well as clients.

### R-12: Inconsistent blacklist TTLs

[usecase.go:230](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L230) uses `u.refreshExpiry` while `Logout` correctly uses `time.Until(claims.ExpiresAt.Time)`. Safe (over-approximates) but inconsistent with the B-3 fix.

### R-13: `Logout` silently skips revocation when `ExpiresAt` is nil

[usecase.go:260-267](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L260-L267) returns success having revoked nothing. Same nil-claim class as R-3.

### R-14: `CreateSession` discards the `Expire` error

[session_store.go:97](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L97) — a failed `EXPIRE` leaves the index key with no TTL, leaking it permanently.

### R-15: `postgresUserRepository.DeleteUser` is still dead code

[repository.go:147-157](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go#L147-L157) remains off the `UserRepository` interface with no caller. The prior plan asked for a decision either way.

---

## Remediation

See [phase_2_remediation_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_remediation_verification_plan.md).
