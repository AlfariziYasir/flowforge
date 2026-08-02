# Implementation Plan — Transaction Wiring & Backfill Fixes

Remediates the 8 findings in [phase_2_tx_wiring_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_wiring_verification_findings.md) — 2 high (W-1, W-2), 2 medium (W-3, W-4), 4 low (W-5 … W-8).

This is a small round. The substance is one missing constructor argument, one vacuous test, and one sentinel that gets deleted immediately after it is written. The rest is cleanup.

> [!IMPORTANT]
> **W-2 is the root cause of W-1, and fixing it is the point of this round.** V-3 was implemented correctly and still did not ship, because the test written to catch that fact was constructed so it could never fail. Phase L fixes the test first, watches it go red, then fixes the wiring. If the corrected test does not fail against the current tree, stop — it is still wrong.

---

## User Review Required

> [!IMPORTANT]
> - **W-1 / W-3 (constructor shape)**: this plan converts `NewUserUseCase` and `NewRedisSessionStore` from variadic optionals to the codebase's existing explicit-second-constructor pattern (`NewUserUseCaseWithTx`, `NewRedisSessionStoreWithTTL`), matching `NewAuthUseCaseWithLogger` / `NewAuthMiddlewareWithSessionStore`. This is a **breaking signature change** for the base constructors' optional arguments — every call site must be revisited, which is the property that would have caught W-1. Say if you would rather keep variadics and add a lint/wiring test instead.
> - **W-4 (sentinel restore)**: `RevokeAllUserSessions` will re-`SADD` the sentinel after deleting the session keys, leaving the index in a "migrated, empty" state. This means the index key outlives the sessions by up to `defaultTTL`. That is intended — it is what stops the rescan — but it is a small amount of Redis retained per revoked user.
> - **W-6 (empty-token logout)**: `POST /api/v1/auth/logout` with no `Authorization` header currently returns **401**. This plan keeps that. If any client treats logout as fire-and-forget and does not tolerate 401, say so and it reverts to 200-with-log for the empty case only, leaving malformed tokens on 401.
> - **Housekeeping**: `.agents/prompts/` is still untracked. This plan includes committing it; skip that step if it is deliberately local.

---

## Phase L — Transaction Wiring (W-1, W-2, W-3)

Test first. The corrected W-2 test must be **red** before the W-1 wiring lands.

#### [MODIFY] [user_usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go)

- **W-2** — rewrite `deactivation rolls back when revocation fails` ([:195-225](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go#L195-L225)) so it actually pins the behaviour. Add a recording `TxRunner` to the test file:
  ```go
  type recordingTxRunner struct {
      entered  bool
      innerErr error
  }
  func (r *recordingTxRunner) ExecuteInTx(ctx context.Context, fn func(context.Context) error) error {
      r.entered = true
      r.innerErr = fn(ctx)
      return r.innerErr   // a real UnitOfWork would roll back here
  }
  ```
  Assert `runner.entered` is true and `runner.innerErr` is non-nil — i.e. the failure reached the transaction boundary where a rollback would occur. Drop the `is.Error(err)`-only assertion.
- ★ **W-1 regression guard** — add `deactivation runs inside a transaction` constructing the use case **exactly as `main.go` does** and asserting the runner was entered. This is the case that must be red today: with `NewUserUseCase(repo, passSvc, store, ttl)` the noop runner is silently substituted.
  > The cleanest form of this assertion is to make the noop fallback observable — see the `NewUserUseCaseWithTx` change below, after which the 4-argument constructor no longer exists and the wiring omission becomes a compile error instead of a test.

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **W-3** — replace the variadic optional at [:79](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L79) with the house pattern:
  ```go
  // NewUserUseCase builds a UserUseCase with no transaction boundary.
  // Callers that own a database pool should use NewUserUseCaseWithTx.
  func NewUserUseCase(userRepo UserRepository, passSvc PasswordService,
      sessionStore SessionStore, refreshExpiry time.Duration) UserUseCase

  func NewUserUseCaseWithTx(userRepo UserRepository, passSvc PasswordService,
      sessionStore SessionStore, refreshExpiry time.Duration, txRunner TxRunner) UserUseCase
  ```
  Keep `noopTxRunner` as the base constructor's fallback for tests, but make the production path name what it is.

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)

- **W-3** — same treatment for [:57](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L57): `NewRedisSessionStore(client)` plus `NewRedisSessionStoreWithTTL(client, ttl)`. This one is already wired correctly; the change is to stop the shape from being copied again.

#### [MODIFY] [main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

- **W-1** — construct the unit of work inside the `dbPool != nil` block and pass it:
  ```go
  uow := postgres.NewUnitOfWork(dbPool)
  userUC := auth.NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, cfg.JWTRefreshExpiry, uow)
  ```
  `postgres.UnitOfWork.ExecuteInTx` already matches `auth.TxRunner` structurally, so it satisfies the interface with no adapter.
- Update the `NewRedisSessionStore` call at [:228](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L228) to `NewRedisSessionStoreWithTTL`.

> [!WARNING]
> `revokeUserAccess` performs Redis writes **inside** the Postgres transaction. A Redis failure now correctly rolls the DB back, but a Redis success followed by a Postgres **commit** failure leaves sessions revoked while the user stays active. That is the safer of the two partial failures — the user is logged out but can sign back in — and it is the unavoidable cost of mixing a non-transactional store into a transaction. Add a comment at the `ExecuteInTx` call site recording the trade so it is not "fixed" later into the dangerous direction.

### Test Strategy — Phase L
`go test ./internal/... -race -count=1`. The rewritten rollback case and the new transaction-boundary case must be red before the `main.go` change and green after. After the `NewUserUseCaseWithTx` split, confirm `go build ./...` fails until `main.go` is updated — that compile error is the durable guard W-3 buys.

---

## Phase M — Backfill Sentinel (W-4)

#### [MODIFY] [session_store.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go)

- **W-4** — in `RevokeAllUserSessions` ([:229-256](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L229-L256)), after the `Del` succeeds, restore the sentinel so the index is left "migrated and empty" rather than absent:
  ```go
  if err := s.client.SAdd(ctx, indexKey, sessionIndexMigratedMember).Err(); err != nil {
      return fmt.Errorf("restore migration sentinel in redis: %w", err)
  }
  if err := s.client.Expire(ctx, indexKey, s.defaultTTL).Err(); err != nil {
      return fmt.Errorf("set migration sentinel ttl in redis: %w", err)
  }
  ```
  Alternatively drop `indexKey` from the `keys` slice and `SREM` only the session IDs — equivalent, and avoids a delete-then-recreate. Either is fine; pick one and note why in a comment.
- Keep the backfill itself. Revoking pre-upgrade sessions is R-4's purpose and must survive.

### Test Strategy — Phase M
- ★ `RevokeAllUserSessions: legacy SCAN runs at most once per user` — mirror the existing `ListUserSessions` scan-count case using a `Scan`-counting `redisclient.Cmdable` wrapper over miniredis. Three consecutive `RevokeAllUserSessions` calls must produce exactly one `SCAN`. Red today:
  ```
  after RevokeAllUserSessions #1 -> cumulative SCAN calls = 1
  after RevokeAllUserSessions #2 -> cumulative SCAN calls = 2
  after RevokeAllUserSessions #3 -> cumulative SCAN calls = 3
  ```
- `RevokeAllUserSessions: clears session keys but leaves the migration sentinel` — after revoking, session keys are gone and a subsequent `ListUserSessions` returns empty **without** scanning.
- The existing `RevokeAllUserSessions: clears the index key` case asserts the opposite of the new behaviour — update it to assert the session keys are cleared and the index holds only the sentinel.

---

## Phase N — Cleanup (W-5, W-6, W-7, W-8)

#### [MODIFY] [handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go)

- **W-5** — drop the unreachable `errors.Is(err, ErrInvalidToken)` clause at [:107](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L107).

#### [MODIFY] [go.mod](file:///home/mohyasiralfarizi/Golang/flowforge/go.mod)

- **W-7** — run `go mod tidy` so `miniredis` moves to the direct require block and `gopher-lua` / `objx` settle as its indirects.

#### [MODIFY] tests — the remaining W-8 backlog

Follow the existing named-`t.Run` style; add to the current test functions rather than new top-level ones.

- [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go) — `ListUserSessions: prunes expired index entries` (miniredis `FastForward` past a session TTL, assert the stale SID is `SREM`ed and excluded from `total`).
- [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go) —
  - `Logout: blacklist TTL covers the token's remaining lifetime`
  - `Refresh: rotation TTL matches token lifetime`
  - `Refresh: rejects a token with no session ID`
  - `Refresh: revokes the old JTI before issuing` — assert ordering, not just occurrence; the existing `refreshes token pair and revokes old refresh token` case checks both happen, which is not what B-9 is about
  - `Login: logs the inactive-account reason` — capturing `slog` handler via `NewAuthUseCaseWithLogger`; assert the record fires and the error is still `ErrUnauthorized`

#### [COMMIT] `.agents/prompts/`

- Still untracked. `git add .agents/prompts/` so the reviewer gate is versioned with the code it gates.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **L — Tx wiring** | W-1, W-2, W-3 | Rewritten rollback test red → green; `go build` fails until `main.go` passes the UoW |
| M — Backfill sentinel | W-4 | 3 revoke-alls produce exactly 1 `SCAN` |
| N — Cleanup | W-5, W-6, W-7, W-8 | `make ci` clean; 6 backlog cases present |

After each phase: `make ci` (`fmt-check` → `vet` → `build` → `test`), then append an execution entry to `.agents/memory/action_history.md` per `.agents/AGENTS.md` §2.

End-to-end smoke test once L and M land, against live Postgres + Redis:
1. Point `REDIS_URL` at a dead port, `PATCH /api/v1/users/{id}` with `{"isActive": false}` → 500; then restore Redis and `GET /api/v1/users/{id}` → **`isActive: true`** (**W-1** — the write was rolled back; today it reads `false`).
2. With `MONITOR` attached: `POST /api/v1/auth/logout-all` three times for the same user — expect **one** `SCAN` total, not three (**W-4**).
3. `POST /api/v1/auth/logout` with no `Authorization` header → **401** (**W-6**, confirming the intended contract).
