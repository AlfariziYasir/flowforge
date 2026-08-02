# Implementation Plan — Transaction Boundary Guard

Remediates the 5 findings in [phase_2_tx_guard_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_verification_findings.md) — 1 medium (X-1), 4 low (X-2 … X-5).

The functional work is done. **X-1 is the only item that matters**: the transaction wiring is correct but unguarded, so a one-line revert of `main.go` would pass the whole suite. Phase O is small and should be finished before this branch merges. Phase P is optional cleanup that can wait.

> [!IMPORTANT]
> This is the third round in which a fix landed without the test meant to hold it. The durable fix is not another test entry on a checklist — it is **making the unguarded state impossible to compile** (X-2), so the guarantee does not depend on remembering. Phase O does both: the compile-time guard first, the test second.

---

## User Review Required

> [!IMPORTANT]
> - **X-1/X-2 (removing the 4-argument constructor)**: the strongest fix is to delete `NewUserUseCase` and keep only `NewUserUseCaseWithTx`, so every caller must pass a `TxRunner` — including tests, which pass an explicit pass-through. That touches ~6 test call sites. The weaker alternative keeps both constructors and relies on the new test alone. This plan takes the strong option; say if the test-only version is preferred.
> - **X-5 (bcrypt cost)**: adding `NewPasswordServiceWithCost` changes no production behaviour (`NewPasswordService` keeps cost 12) but touches the password service's public surface. Skip if you would rather live with the ~105 s package runtime.

---

## Phase O — Close the Guard (X-1, X-2, X-3)

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

- **X-2** — delete `NewUserUseCase` ([:81-83](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L81-L83)) and keep only `NewUserUseCaseWithTx`. Export the pass-through runner so callers that genuinely have no transaction can opt in explicitly:
  ```go
  // NewPassthroughTxRunner returns a TxRunner that executes fn with no transaction
  // boundary. Production callers owning a database pool must pass postgres.UnitOfWork
  // instead — see cmd/api/main.go.
  func NewPassthroughTxRunner() TxRunner { return &noopTxRunner{} }
  ```
  After this, omitting the unit of work is a **compile error**, not a silent downgrade. That is the guarantee X-1 and W-1 both needed and neither got.
- Rename `noopTxRunner` → `passthroughTxRunner`. "Noop" is misleading: it runs the function, it just does not open a transaction.

#### [MODIFY] [user_usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go)

- **X-1** — add the recording runner the previous plan specified:
  ```go
  type recordingTxRunner struct {
      entered  bool
      innerErr error
  }
  func (r *recordingTxRunner) ExecuteInTx(ctx context.Context, fn func(context.Context) error) error {
      r.entered = true
      r.innerErr = fn(ctx)
      return r.innerErr   // a real UnitOfWork rolls back here
  }
  ```
- **X-1** — rewrite `deactivation rolls back when revocation fails` ([:195-225](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go#L195-L225)) to construct with `NewUserUseCaseWithTx(..., runner)` and assert `runner.entered` **and** `runner.innerErr != nil` — the failure reached the boundary where rollback occurs. Delete the bare `is.Error(err)`.
- **X-1** — add `deactivation runs inside a transaction` and `deletion runs inside a transaction`: happy paths asserting `runner.entered` is true. These are what fail if someone reverts to a pass-through runner in production wiring.
- Update the other ~5 call sites in this file to `NewUserUseCaseWithTx(..., auth.NewPassthroughTxRunner())`.

#### [MODIFY] [main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go)

- **X-1** — the strongest possible guard is at the wiring layer. Extract the `dbPool != nil` construction block from `main` into a testable `buildAuthComponents(cfg, dbPool, rClient) (…, error)` and assert the returned `UserUseCase` was built with a real `postgres.UnitOfWork`, not a pass-through. If extraction is more churn than it is worth, the Phase O tests above are sufficient — note the decision in `action_history.md` either way.

#### [MODIFY] [usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go)

- **X-3** — change `Refresh: revokes the old JTI before issuing new tokens` ([:297](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go#L297)) to use `mocks.NewMockJWTService` so `GenerateTokenPair` can be recorded, and assert `RevokeOldJTI` precedes `GenerateTokenPair` — the invariant B-9 is actually about. Keep `CreateNewSession` in the recorded order as a third entry if useful, but stop treating it as the proxy for issuance.

### Test Strategy — Phase O
1. Make the `user_usecase.go` change first and run `go build ./...` — it **must fail** at every call site that omits a runner. That compile break is the deliverable; if the build stays green, the constructor was not actually removed.
2. Fix the call sites, then run `go test ./internal/... -race -count=1`.
3. Verify the guard works: temporarily change [main.go:240](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L240) to pass `auth.NewPassthroughTxRunner()` and confirm the build still passes but the wiring test fails. Revert.

---

## Phase P — Optional Cleanup (X-4, X-5)

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)

- **X-4** — add a comment above `revokeTokenUntilExpiry` inside `Refresh` recording the invariant: *revocation of the old JTI must precede session update and token issuance (B-9); reordering reopens the concurrent-double-refresh race.* The reorder itself is correct and fails safe — this only stops it being undone.

#### [MODIFY] [password.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/password.go)

- **X-5** — add `NewPasswordServiceWithCost(cost int) PasswordService` alongside the existing `NewPasswordService()`, which keeps `defaultBcryptCost = 12`. Switch test call sites that do not assert hashing strength to `bcrypt.MinCost`. `internal/auth` currently runs ~105 s under `-race`, up from 58 s two rounds ago, almost entirely bcrypt.
- Leave [password_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/password_test.go) on the production cost — it is the one place the cost matters.

### Test Strategy — Phase P
`make ci` and compare `internal/auth` wall-clock before and after. Confirm `password_test.go` still exercises cost 12.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| **O — Guard** | X-1, X-2, X-3 | `go build` breaks until every call site passes a runner; reverting `main.go` fails a test |
| P — Cleanup (optional) | X-4, X-5 | `make ci` clean; `internal/auth` runtime materially lower |

After each phase: `make ci`, then append an execution entry to `.agents/memory/action_history.md` per `.agents/AGENTS.md` §2.

Verification for Phase O is mechanical rather than a smoke test — the point is that the unguarded state stops compiling:

1. Remove the `uow` argument at [main.go:240](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L240) → `go build ./...` **fails**. (Today it succeeds, which is X-1.)
2. Restore it, swap `uow` for `auth.NewPassthroughTxRunner()` → build succeeds, `deactivation runs inside a transaction` **fails**.
3. Restore → `make ci` clean.

The live-Postgres/Redis smoke test from the previous plan still applies and should be run once before merge, since it is the only end-to-end confirmation that the rollback actually happens:

- Point `REDIS_URL` at a dead port, `PATCH /api/v1/users/{id}` with `{"isActive": false}` → 500; restore Redis, `GET /api/v1/users/{id}` → **`isActive: true`**.
