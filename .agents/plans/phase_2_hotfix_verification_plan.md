# Implementation Plan — Phase 2 Hotfix Verification Fixes

Remediates the 9 findings in [phase_2_hotfix_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_hotfix_verification_findings.md) — 3 medium (V-1, V-2, V-3), 5 low (V-4 … V-7, V-9), 1 process note (V-8, recommended closed as satisfied).

No critical defects remain. R-1 and R-3 are fully closed and pinned by tests; R-2 is one character short. Test coverage improved materially this round (110 named subtests, ~16 of 30 planned cases landed), so the remaining risk is narrow and specific: **`redisSessionStore` gained ~90 lines of index/backfill/pagination logic with no test coverage at all, and all three medium findings live in that code or in the timing predicate next to it.**

> [!IMPORTANT]
> **Phase K (tests) is sequenced first, and its ★ cases must be written red before the Phase I fixes land.** V-1, V-2, and V-3 each have a Phase K test that fails against the current tree. Writing them first is what closes the loop that the previous rounds left open.

---

## User Review Required

> [!IMPORTANT]
> - **V-1 (`<=` → `<`)**: one-character change to [session_store.go:259](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L259). Verified correct in both directions — the fresh token is accepted and the in-flight token is still revoked. No token-format or key-format change.
> - **V-2 (migration sentinel)**: this plan adds a `__migrated__` sentinel member to the index set so the legacy `SCAN` runs at most once per user. It is deliberately a set member rather than a second key so it shares the index TTL and needs no extra cleanup. It must be filtered out everywhere the set is read — flagged below at each call site.
> - **V-2 (removal date)**: the whole backfill path is dead code once no pre-upgrade session can survive — one `JWTRefreshExpiry` (default 7 days) after rollout. This plan adds a dated `// REMOVE AFTER` comment rather than a config flag. Confirm the rollout date to put in it.
> - **V-3 (rollback semantics)**: wrapping deactivation in `UnitOfWork` means a Redis failure rolls back the Postgres write, leaving the user **active**. The alternative — commit the DB and surface a partial failure — leaves them deactivated but still holding live sessions. This plan chooses rollback, so the operator's retry is meaningful. Say if you would rather fail forward.
> - **V-4 (logout status)**: this plan maps both malformed-token cases to **401**. If clients depend on logout being unconditionally 200, say so and it becomes a silent-success-with-log instead.
> - **V-9 (Redis test double)**: the untested index/backfill/pagination paths need a fake Redis. This plan adds **`github.com/alicebob/miniredis/v2`** as a test-only dependency — it implements `SCAN`/`SADD`/`SMEMBERS`/`SREM`/TTL faithfully, which a hand-rolled `Cmdable` spy would not. Confirm you are willing to take the new `go.mod` entry; the alternative is a spy that verifies call sequences but not Redis semantics.
> - **V-8 (reviewer gate)**: recommends amending `.agents/prompts/reviewer.md` §4 to accept named subtests, and closing N-10/R-10 rather than carrying it. Skip Phase J's second half if you would rather keep the literal table-loop requirement.

---

## Phase K — Regression Tests (V-9), written first

Write these **before** Phases I–J. The starred cases must fail against the current tree; if one passes immediately, the test is wrong.

#### [ADD] test dependency

- `go get github.com/alicebob/miniredis/v2` — test-only. Every case below except the two timing ones needs real `SCAN`/set/TTL semantics. Existing subtests in [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go) cover only the noop store, `NewRedisSessionStore(nil)`, and key formatting; nothing exercises `redisSessionStore` against a live command surface.

#### [MODIFY] [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go)

Follow the existing named-`t.Run` style in this file — do not introduce a different convention.

- ★ `IsUserRevoked: freshly issued token survives a same-instant revocation` — **the V-1 test.** `SetUserRevokedBefore(time.Now())`, then *immediately* `GenerateTokenPair` via the real `jwtService` (the absence of a sleep is the point), drive the token through `AuthMiddleware.Authenticate`, assert **200**. Fails today with `401 user token has been revoked`.
- `IsUserRevoked: in-flight token issued before revocation is rejected` — issue, sleep 2 ms, revoke, assert **401**. Must stay green through the V-1 fix; this is the B-6 guard.
- ★ `backfill: legacy SCAN runs at most once per user` — **the V-2 test.** miniredis with an empty index plus unrelated keyspace noise; call `ListUserSessions` three times and assert the `SCAN` fires only on the first. Count via `miniredis.Miniredis` command hooks or a thin `Cmdable` wrapper that tallies `Scan` calls.
- `backfill: recovers pre-upgrade sessions with no index entry` — seed session keys directly, no index; assert `RevokeAllUserSessions` deletes every legacy key. R-4 has no coverage today.
- `ListUserSessions: pagination is stable across repeated calls` — 25 sessions at `pageSize=10`; each appears exactly once across pages and repeated calls return identical pages. R-7 has no coverage today.
- `RevokeAllUserSessions: clears the index key`, `ListUserSessions: prunes expired index entries`.

#### [MODIFY] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go)

Add to the existing `TestAuthUseCase_Logout` / `_Refresh` / `_Login` functions as new subtests.

- ★ `Logout: malformed tokens do not report success` — **the V-4 test.** Cases {garbage string, valid token with nil `exp`}; both must produce the same error class. Today the first returns `nil` (200) and the second returns `ErrInvalidToken` (500).
- `Logout: blacklist TTL covers the token's remaining lifetime` — the TTL passed to `Revoke` tracks `time.Until(claims.ExpiresAt)`, not a literal.
- `Refresh: rotation TTL matches token lifetime`, `Refresh: rejects a token with no session ID`, `Refresh: revokes the old JTI before issuing` (assert ordering via mockery call sequencing — the existing "refreshes token pair and revokes old refresh token" subtest asserts both happen but not the order, which is the whole point of B-9).
- `Login: logs the inactive-account reason` — inject a capturing `slog` handler via `NewAuthUseCaseWithLogger`; assert the record fires **and** the returned error is `ErrUnauthorized`, indistinguishable from a bad password.

#### [MODIFY] [user_usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go)

- ★ `deactivation rolls back when revocation fails` — **the V-3 test.** `SessionStore` returns an error; assert the repository never observes a committed deactivation. Fails today.
- `deactivation propagates the revocation error`, `deactivation uses the configured refresh expiry` (assert the TTL argument), `role change revokes sessions` and does **not** revoke on a no-op role change.

### Test Strategy — Phase K
`go test ./internal/... -race -count=1`. The four ★ cases must be red before Phase I, green after. Confirm every other new case passes on the current tree — a new case that fails unexpectedly is a finding this review missed; one that passes when it should fail is a test bug.

R-3's guard is already pinned by `returns 401 Unauthorized without panic when token lacks IssuedAt claim` in [middleware_test.go:177](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware_test.go#L177) — no new case needed there.

---

## Phase I — Correctness (V-1, V-2, V-3, V-4, V-5)

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)

- **V-1** — change `<=` to `<` at [:259](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L259):
  ```go
  return issuedAt.UnixMilli() < revokedUnixMilli, nil
  ```
  Add a comment stating the invariant: *revoke tokens issued strictly before the revocation instant; `iat` precision is set to milliseconds by the `init()` in `jwt.go`.*
- **V-2** — add a `sessionIndexMigratedMember = "__migrated__"` constant. In `backfillIndexIfEmpty`: return early when the sentinel is present; `SADD` it after a successful backfill **and** when the `SCAN` finds nothing, so users with genuinely zero sessions also stop scanning. Filter the sentinel out of `sids` in both `RevokeAllUserSessions` and `ListUserSessions` before the `uuid.Parse` loops — the existing `uuid.Parse` failure path would otherwise push it onto `expiredSIDs` and `SREM` it away on the first list, defeating the sentinel.
- **V-2** — add `// REMOVE AFTER <rollout date + JWTRefreshExpiry>: legacy pre-index session backfill` above `backfillIndexIfEmpty` and `scanUserSessionKeys`.
- **V-5** — propagate the `SAdd` and `Expire` errors in the backfill, matching `CreateSession` at [:96-101](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L96-L101). Thread the configured TTL in rather than the `7*24*time.Hour` literal — `redisSessionStore` has no expiry field today, so add one set from `cfg.JWTRefreshExpiry` via `NewRedisSessionStore`, defaulting to 7 days when non-positive (same shape as `NewAuthUseCase`).
- **V-6** — drop the dead `sort.Strings(sids)` at [:284](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L284).

> [!WARNING]
> `NewRedisSessionStore` gaining a TTL parameter changes its signature. Update the call site in [main.go:203](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L203) and regenerate mocks (`make mocks`) — `SessionStore` the interface is unchanged, so only the constructor call moves.

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **V-3** — wrap the `userRepo.UpdateUser` + `revokeUserAccess` pair in `UpdateUser` ([:205-213](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L205-L213)) and `DeleteUser` ([:233-239](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L233-L239)) in `postgres.UnitOfWork.ExecuteInTx`, so a Redis revocation failure rolls the Postgres write back. Inject the `UnitOfWork` through `NewUserUseCase` as an interface owned by the auth package (not a concrete `*postgres.UnitOfWork`) to keep the use-case layer free of infrastructure types:
  ```go
  type TxRunner interface {
      ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
  }
  ```
  Default to a pass-through implementation when nil so existing tests and the noop path keep working.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go) + [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)

- **V-4** — make `Logout` return `ErrUnauthorized` for both malformed-token cases: the both-validations-failed path at [:290-292](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L290-L292) currently returns `nil`, and the nil-`ExpiresAt` path returns `ErrInvalidToken`. Keep the best-effort session revocation in the nil-`exp` branch and keep logging both.
- Map `ErrUnauthorized` → **401** in `AuthHandler.Logout` at [:106-109](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L106-L109), leaving genuine store failures on 500.

### Test Strategy — Phase I
The three ★ Phase K cases flip green. Re-run the full suite with `-race`; `TestSessionStore_IsUserRevoked_InFlightTokenStillRevoked` must stay green — that is the check that V-1 did not reopen B-6.

---

## Phase J — Hygiene (V-7, V-8)

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

- **V-7** — split the side effect out of the validator:
  ```go
  func applyJWTSecretDefault(cfg *config.Config)   // dev fallback assignment
  func validateJWTSecret(cfg *config.Config) error // pure check, no mutation
  ```
  Call both from `main`. Keep `TestValidateJWTSecret` pointed at the pure function and add a separate case for the default-application step.

#### [MODIFY] [reviewer.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/prompts/reviewer.md)

- **V-8** — amend §4 from "Do tests use Table-Driven Test patterns?" to "Do tests use named subtests or table-driven patterns?". The repo has 110 named `t.Run` subtests applied consistently; that satisfies the gate's intent. Close N-10/R-10 as satisfied rather than carrying it a fourth round — rewriting working subtests into `for ... range` loops is churn with no correctness payoff.
- Note: `.agents/prompts/` is still untracked (`git status` shows `?? .agents/prompts/`). Commit it so the gate the reviews run against is versioned alongside the code.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **K — Tests first** | V-9 | 4 ★ cases **red**, all others green |
| I — Correctness | V-1, V-2, V-3, V-4, V-5, V-6 | 4 ★ cases green; in-flight-revocation test still green |
| J — Hygiene | V-7, V-8 | `make ci` clean |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test`), then append an execution entry to `.agents/memory/action_history.md` per `.agents/AGENTS.md` §2.

End-to-end smoke test once I and J land, against live Postgres + Redis:
1. `POST /api/v1/auth/logout-all`, then re-login and call `GET /api/v1/users/me` in a tight loop 100× — expect **100 × 200**, no intermittent 401 (**V-1**).
2. With `MONITOR` attached to Redis: `GET /api/v1/auth/sessions` three times for a user with no sessions — expect a `SCAN` on the first call only (**V-2**).
3. Point `REDIS_URL` at a dead port, `PATCH /api/v1/users/{id}` with `{"isActive": false}` → 500, then `GET /api/v1/users/{id}` → still `isActive: true` (**V-3**).
4. `POST /api/v1/auth/logout` with `Authorization: Bearer garbage` → **401**, not 200 (**V-4**).
