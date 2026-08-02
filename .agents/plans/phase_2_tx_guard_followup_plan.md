# Implementation Plan — Tx Guard Follow-up

Remediates the 3 low findings in [phase_2_tx_guard_followup_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_followup_findings.md) — Y-1, Y-2, Y-3.

**None of these block merge.** The transaction boundary is correctly wired, compile-guarded, and test-pinned. This is a single short phase closing the last three gaps, and it is the natural stopping point for the review chain that began with the Phase 2 audit.

---

## User Review Required

> [!IMPORTANT]
> - **Y-2 (extraction vs. record)**: this plan takes the cheaper option — record the decision to skip `buildAuthComponents` rather than perform the extraction, since the residual risk now requires deliberately passing the wrong runner rather than forgetting one. Say if you would rather have the extraction and the wiring assertion.
> - **Y-3 (dummy-hash cost)**: this plan makes `getDummyBcryptHash` injectable to recover ~15 s of test time. That touches the login timing-attack defense, so it is deliberately structured to keep production on cost 12 with no behavioural change. Skip this half if you would rather not touch that code path for a test-speed win.

---

## Phase Q — Close the Gaps (Y-1, Y-2, Y-3)

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **Y-1** — delete the `txRunner == nil` branch at [:92-94](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L92-L94). A nil runner then panics at first use, which surfaces immediately in any test or first request, instead of silently disabling atomicity forever. Document the requirement on the constructor:
  ```go
  // NewUserUseCaseWithTx builds a UserUseCase bound to a transaction boundary.
  // txRunner must not be nil — pass NewPassthroughTxRunner() to opt out explicitly.
  ```
- Leave the `sessionStore` and `refreshExpiry` nil/zero defaults alone; those are benign conveniences, not silently-disabled guarantees.

#### [MODIFY] [handler_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler_test.go)

- **Y-3** — change [:31](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler_test.go#L31) from `auth.NewPasswordService()` to `auth.NewPasswordServiceWithCost(bcrypt.MinCost)`. This is the single call site the X-5 sweep missed; it accounts for ~25 s of the 70 s `-race` runtime.
- Leave [password_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/password_test.go) on the production cost — that is the one place the cost is the thing under test.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)

- **Y-3** — make the dummy hash injectable so `TestAuthUseCase_Login`'s five failure-path subtests stop paying a cost-12 compare each (~15 s). Add a `dummyHash string` field to `authUseCase`, defaulted from the existing `getDummyBcryptHash()` in `NewAuthUseCaseWithLogger` when empty, and replace the three `getDummyBcryptHash()` call sites at [:133](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L133), [:142](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L142), [:149](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L149) with `u.dummyHash`.

  > [!WARNING]
  > Production behaviour must not change: `NewAuthUseCase` and `NewAuthUseCaseWithLogger` keep resolving the real cost-12 hash. Only tests supply a cheaper one, and only via a constructor that names what it is doing. The invariant to preserve is that the dummy compare costs the *same* as a real compare — so a test that lowers the dummy cost must also lower the `PasswordService` cost, or the timing defense is no longer being exercised faithfully. Add a comment saying exactly that.

  If that coupling feels too subtle to be worth 15 s, skip this bullet and keep only the `handler_test.go` change — the honest gain is then ~25 s rather than ~40 s.

#### [MODIFY] [action_history.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md)

- **Y-2** — append to the Phase O–P execution entry that the `buildAuthComponents` extraction was **deliberately skipped**, with the reason: the compile-time guard covers omission, and the residual case (deliberately passing `NewPassthroughTxRunner()` in production) is a conscious act rather than an oversight.
- **Y-3** — correct the recorded speedup. Replace *"from 105s down to 4.9s (a 20x speedup)"* with the like-for-like figures: **`-race` 105 s → 70 s**; non-race ~4.4 s → 4.7 s. The 4.9 s figure is the non-race run and is not comparable to the 105 s `-race` baseline.

### Test Strategy — Phase Q
- `go build ./...` must still fail if the production call site drops its runner (re-confirm the X-2 guard survived the Y-1 change).
- Add `NewUserUseCaseWithTx panics on a nil runner` only if the codebase has a convention for panic tests; otherwise the compile-time guard plus the constructor doc is sufficient and a panic test is noise.
- `make ci` and record the actual `-race` runtime for `internal/auth` in the execution entry — measured, not inferred.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| Q — Close the gaps | Y-1, Y-2, Y-3 | `make ci` clean; `internal/auth` `-race` runtime materially below 70 s |

1. `make ci` — clean.
2. Re-run the X-2 guard check: swap [main.go:240](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L240) to the deleted `NewUserUseCase` → build **fails**; restore.
3. `go test ./internal/auth/ -race -count=1` and compare against the 70 s baseline.

---

## After This Phase

With Y-1 … Y-3 closed, every finding from the original Phase 2 audit and the five verification rounds that followed is resolved. The remaining pre-merge item is the **live-Postgres/Redis smoke test**, which no unit test can substitute for — it is the only end-to-end confirmation that the rollback actually happens:

1. Point `REDIS_URL` at a dead port, `PATCH /api/v1/users/{id}` with `{"isActive": false}` → 500; restore Redis, `GET /api/v1/users/{id}` → **`isActive: true`**.
2. `POST /api/v1/auth/logout-all`, then re-login and call `GET /api/v1/users/me` 100× → **100 × 200**, no intermittent 401.
3. With `MONITOR` attached: three `POST /api/v1/auth/logout-all` calls → **one** `SCAN` total.
4. Start with `ENV=production` and no `JWT_SECRET` → process exits non-zero.

Also still outstanding from earlier rounds and worth resolving before merge: `.agents/prompts/` is staged but the branch has no commit yet, and `migrations/000001_init_schema.up.sql` carries a **PostgreSQL 15+** floor (column-scoped `ON DELETE SET NULL`) that should be recorded in the deployment requirements.
