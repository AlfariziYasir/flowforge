# Implementation Plan — Phase 2 Remediation Verification Fixes

Remediates all 15 findings in [phase_2_remediation_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_remediation_verification_findings.md) — 2 critical (R-1, R-2), 2 high (R-3, R-4), 4 medium (R-5 … R-8), 7 low (R-9 … R-15).

These are defects **introduced or left behind by** the Phase 2 remediation execution, not new product work. Phase E is a security hotfix and should ship on its own.

---

## User Review Required

> [!IMPORTANT]
> - **R-2 (decided — millisecond `iat`)**: `jwt.TimePrecision` will be set to `time.Millisecond`. Serialized `iat`/`exp`/`nbf` become fractional numerics. RFC 7519 permits this and only this service consumes the tokens, but any external verifier would need to tolerate it. Verified working: a token issued 2 ms after a revocation is accepted, while an older token is still revoked.
> - **R-2 (global mutation)**: `jwt.TimePrecision` is a package-level variable in `jwt/v5`. It will be set once from an `init()` in `internal/auth/jwt.go` — **not** from `NewJWTService`, which would data-race under `t.Parallel()` and `-race`.
> - **R-5 (decided — fail at login)**: `noopSessionStore.CreateSession` will return an error so `Login` fails loudly when Redis is absent. `ENV=development` still boots and `/health` still reports; developers just cannot authenticate without Redis.
> - **R-1 (dev default retained)**: `Config.JWTSecret` will be empty when `JWT_SECRET` is unset, with the dev fallback applied in `main.go` for non-production only. If you would rather drop the hardcoded dev secret entirely and require `JWT_SECRET` in every environment, say so — it is a one-line change to this plan.
> - **R-4 (backfill vs. flush)**: this plan implements a `SCAN` fallback that backfills the index when it is missing, so no operational step is required. If Redis can simply be flushed at release time, the fallback can be dropped — confirm which you prefer.

---

## Phase E — Security Hotfix (R-1, R-2, R-3)

Ship these three together, ahead of everything else.

#### [MODIFY] [config.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/config/config.go)

- **R-1** — change `JWTSecret` to `getEnv("JWT_SECRET", "")` so an unset variable is distinguishable from a short one. Do **not** carry the dev literal here.

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

- **R-1** — restore the unset check alongside the length check, immediately after `config.Load()` at [:156](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L156):
  ```go
  if cfg.JWTSecret == "" {
      if isProductionLike(cfg.Environment) {
          log.Error("JWT_SECRET must be set in production/staging")
          os.Exit(1)
      }
      cfg.JWTSecret = devJWTSecret          // package const, non-production only
  }
  if len(cfg.JWTSecret) < 32 && isProductionLike(cfg.Environment) {
      log.Error("JWT_SECRET must be at least 32 characters long in production/staging")
      os.Exit(1)
  }
  ```
- Belt-and-braces: also `os.Exit(1)` in production/staging when `cfg.JWTSecret == devJWTSecret`, so an operator who copies the dev value into the environment is still caught.

#### [MODIFY] [jwt.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/jwt.go)

- **R-2** — add a package `init()` setting `jwt.TimePrecision = time.Millisecond` so `iat` carries the precision that `IsUserRevoked` assumes. Place it beside the existing `TokenTypeAccess`/`TokenTypeRefresh` constants with a comment pointing at [session_store.go:205](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L205) explaining the coupling — this is a non-obvious cross-file invariant and the next reader will otherwise "clean it up".
- Leave [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go) unchanged: `UnixMilli` + `<=` become correct once `iat` is millisecond-precise.

#### [MODIFY] [middleware.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go)

- **R-3** — guard `claims.IssuedAt` before [:78](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L78), mirroring [usecase.go:165-167](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L165-L167):
  ```go
  if claims.IssuedAt == nil {
      respondJSONError(w, http.StatusUnauthorized, "Unauthorized", "invalid or expired token")
      return
  }
  ```

### Test Strategy — Phase E
- `TestConfig_JWTSecretUnsetIsEmpty` — `JWT_SECRET` unset yields `""`, not the dev literal.
- `TestMain_ProductionRejectsDefaultSecret` — table-driven over {unset, dev literal, 20-char, valid 40-char} × {development, production}; assert which combinations are fatal. Extract the validation into a testable `validateJWTSecret(cfg) error` rather than testing `os.Exit`.
- `TestSessionStore_IsUserRevoked_FreshTokenSurvivesSameSecondRevocation` — **the R-2 regression test.** Stamp a revocation, issue a token immediately after via the real `jwtService`, drive it through `AuthMiddleware.Authenticate`, assert **200**.
- `TestSessionStore_IsUserRevoked_InFlightTokenStillRevoked` — the B-6 case must stay closed: a token issued *before* the revocation is rejected.
- `TestAuthMiddleware_RejectsTokenWithoutIssuedAt` — hand-build claims with `IssuedAt` omitted, sign with the service secret, assert 401 **and no panic**.
- Run the whole package with `-race` — the `init()` placement for `TimePrecision` must not race under parallel tests.

---

## Phase F — Redis Correctness (R-4, R-5, R-7, R-14)

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)

- **R-4** — in `RevokeAllUserSessions` and `ListUserSessions`, when `SMEMBERS` returns empty, fall back to the previous `SCAN` over `session:{tenant}:{user}:*` and **backfill** the discovered session IDs into the index via `SADD` before proceeding. Gate it on an empty index so the fallback costs nothing in steady state, and `log`/comment it as removable once no pre-upgrade sessions can remain (one `JWTRefreshExpiry` after rollout).
- **R-7** — sort the `SMEMBERS` result (`sort.Strings`) before slicing in `ListUserSessions` at [:221](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L221). Session IDs are UUID strings, so lexical order is stable and arbitrary — which is all pagination needs.
- **R-5** — `noopSessionStore.CreateSession` returns an error (e.g. `errors.New("session store unavailable: redis is required for authentication")`) so `Login` fails at [usecase.go:138-140](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L138-L140) instead of issuing tokens the middleware will reject. Update the doc comment on `NewNoopSessionStore` accordingly.
- **R-14** — propagate the `Expire` error at [:97](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L97) rather than discarding it, so an index key is never left without a TTL.

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)

- **R-5** — confirm `AuthHandler.Login` maps the new `create user session` failure to **500**, not 401. It currently falls through to the generic 500 branch at [:65](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L65), which is correct; add a test rather than a code change.

### Test Strategy — Phase F
- `TestNoopSessionStore_CreateSessionFailsClosed` — `CreateSession` errors; `authUseCase.Login` wired with the noop store returns an error and **no** token pair.
- `TestSessionStore_RevokeAll_BackfillsLegacySessions` — seed session keys with **no** index entry (the pre-upgrade state), call `RevokeAllUserSessions`, assert every legacy key is deleted.
- `TestSessionStore_List_StablePagination` — 25 sessions at `pageSize=10`; every session appears exactly once across pages and page contents are identical across repeated calls.
- `TestSessionStore_RevokeAll_ClearsIndex` and `TestSessionStore_ListPrunesExpiredIndexEntries` — carried over from the prior plan, still unwritten.

---

## Phase G — Error Mapping & Atomicity (R-6, R-9, R-11, R-12, R-13, R-15)

#### [MODIFY] [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go)

- **R-6** — add the missing `ErrUserAlreadyExists` → **409** branch to `UpdateUser` at [:180-191](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go#L180-L191), matching `CreateUser` at [:53-56](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go#L53-L56). The repository already produces it.

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **R-9** — wrap the `userRepo.UpdateUser` + `revokeUserAccess` pair in the existing `UnitOfWork` ([unit_of_work.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/unit_of_work.go)) so a revocation failure rolls the deactivation back. Redis is not transactional, so order the work DB-first and roll back the DB when Redis fails — the reverse leaves a user revoked but still active, which is the safer failure but a confusing one.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)

- **R-11** — the use case has no logger today. Add a `*slog.Logger` field to `authUseCase` (defaulting to `slog.Default()`) and log the inactive-account rejection at `Info` before returning `ErrUnauthorized` at [:116-119](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L116-L119). `log/slog` is a stdlib logging concern, not a transport dependency — it does not breach the Clean Architecture gate that bans `net/http`.
- **R-12** — derive the rotation TTL at [:230](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L230) from `time.Until(claims.ExpiresAt.Time)` with a `> 0` guard, matching `Logout`. Extract the shared shape into a small `revokeTokenUntilExpiry(ctx, claims) error` helper used by both.
- **R-13** — in `Logout` at [:260-267](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L260-L267), treat a nil `ExpiresAt` as a malformed token: skip the blacklist but still revoke the session, and log it. Do not report success for a token that could not be revoked.

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go)

- **R-15** — delete `postgresUserRepository.DeleteUser` at [:147-157](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/repository.go#L147-L157). Deletion is soft (`DeleteUser` in the use case deactivates), so a hard-delete path that nothing calls is dead weight. Re-add it to the interface only when a hard-delete feature exists.

### Test Strategy — Phase G
- `TestUserHandler_UpdateUser_DuplicateEmailReturns409` — the R-6 regression test.
- `TestUserUseCase_Deactivate_RollsBackOnRevocationFailure` — a `SessionStore` error leaves `is_active` unchanged in the repository.
- `TestAuthUseCase_Login_LogsInactiveReason` — capture via `slog` test handler; assert the response is still an indistinguishable 401.
- `TestAuthUseCase_Logout_MalformedTokenDoesNotReportSuccess` — nil `ExpiresAt` claims.
- `TestAuthUseCase_Refresh_RotationTTLMatchesTokenLifetime` — the R-12 assertion.

---

## Phase H — Test Debt (R-8, R-10)

The 16 cases from the prior plan plus the 14 above are the deliverable here. Write them **before** or alongside Phases E–G so each fix lands red-then-green — R-8 exists precisely because the previous execution shipped fixes without them.

#### [MODIFY] Test suite conversion

- **R-10** — convert the 11 non-table test files in `internal/auth` and `internal/tenant` to table-driven form, following [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go). Highest value first: `usecase_test.go` (5 funcs), `handler_test.go` (5), `user_handler_test.go` (5), `user_usecase_test.go` (3), `jwt_test.go` (3).
- **R-8** — the full backlog, by home file:
  - `session_store_test.go` — `NoopSessionStore_FailsClosed`, `CreateSessionFailsClosed`, `IsUserRevoked_SameInstant`, `FreshTokenSurvivesSameSecondRevocation`, `InFlightTokenStillRevoked`, `IndexPagination`, `RevokeAll_ClearsIndex`, `RevokeAll_BackfillsLegacySessions`, `ListPrunesExpiredIndexEntries`, `List_StablePagination`
  - `usecase_test.go` — `Logout_BlacklistTTLCoversTokenLifetime`, `Refresh_RejectsTokenWithoutSessionID`, `Refresh_RevokesBeforeIssuing`, `Refresh_RotationTTLMatchesTokenLifetime`, `Logout_MalformedTokenDoesNotReportSuccess`, `Login_LogsInactiveReason`
  - `user_usecase_test.go` — `Deactivate_PropagatesRevocationError`, `Deactivate_UsesConfiguredExpiry`, `Deactivate_RollsBackOnRevocationFailure`, `RoleChange_RevokesSessions`
  - `middleware_test.go` — `RejectsTokenWithoutIssuedAt`
  - `handler_test.go` — `Login_UniformFailureResponse`
  - `user_handler_test.go` — `CreateUser_DuplicateEmailReturns409`, `UpdateUser_DuplicateEmailReturns409`
  - `repository_test.go` (platform) — `IsUniqueViolation`
  - `main_test.go` — `UserListRequiresElevatedRole`, `ProductionRejectsDefaultSecret`

#### [MODIFY] [Makefile](file:///home/mohyasiralfarizi/Golang/flowforge/Makefile)

- Add a `ci` target chaining `fmt-check`, `vet`, `build`, and `test` so a single command reproduces the release gate.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **E — Security hotfix** | R-1, R-2, R-3 | Ship alone. Prod boot without `JWT_SECRET` exits non-zero; logout-all → immediate re-login → 200; nil-`iat` token → 401, no panic |
| F — Redis correctness | R-4, R-5, R-7, R-14 | Legacy sessions revoked; stable pagination; login fails loudly without Redis |
| G — Mapping & atomicity | R-6, R-9, R-11, R-12, R-13, R-15 | `PATCH` duplicate email → 409; revocation failure rolls back |
| H — Test debt | R-8, R-10 | 30 named cases present and passing |

After each phase: `make fmt-check && make vet && make build && go test ./... -race -count=1`, then append an execution entry to `.agents/memory/action_history.md` per `.agents/AGENTS.md` §2.

End-to-end smoke test once Phases E–G land, against live Postgres + Redis:
1. Start with `ENV=production` and no `JWT_SECRET` → process exits non-zero (**R-1**).
2. Set a 40-char `JWT_SECRET`, restart, `POST /api/v1/auth/login` → 200.
3. `POST /api/v1/auth/logout-all`, then **immediately** `POST /api/v1/auth/login` again and call `GET /api/v1/users/me` with the new token → **200** (**R-2** — this is the regression; it currently returns 401).
4. Create a session, restart the API to simulate a pre-index deployment state, `POST /api/v1/auth/logout-all`, confirm the legacy session key is gone from Redis (**R-4**).
5. `PATCH /api/v1/users/{id}` setting an email that already exists in the tenant → **409** (**R-6**).
6. Stop Redis, restart with `ENV=development`, `POST /api/v1/auth/login` → **500** with a clear message, not a 200 followed by 401s (**R-5**).
