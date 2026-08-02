# Phase O–P (Tx Guard) — Verification Review Findings

Post-execution review of the fixes applied for [phase_2_tx_guard_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_verification_findings.md) per [phase_2_tx_guard_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_verification_plan.md).

**Verdict: ✅ APPROVED WITH NOTES — 0 critical, 0 high, 0 medium, 3 low** (Y-1 … Y-3)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok**.

> [!NOTE]
> **All five findings from the previous round are fixed, and this is the first round where the tests written to hold a fix actually hold it.** The compile-time guard is real, verified mechanically. The rewritten rollback test asserts `runner.entered` and `runner.innerErr` instead of a bare `is.Error`. The B-9 ordering test now uses `authmocks.NewMockJWTService` and asserts a three-element call order ending in `GenerateTokenPair` — the actual invariant, not a proxy.

---

## Remediation Scorecard

| Finding | Result | Note |
|---|---|---|
| X-1 Unguarded tx boundary | ✅ | `recordingTxRunner` + 3 real assertions; rollback test no longer vacuous |
| X-2 No compile-time guard | ✅ | Verified — see below |
| X-3 Proxy ordering assertion | ✅ | Mock `JWTService`; asserts `RevokeOldJTI` → `CreateNewSession` → `GenerateTokenPair` |
| X-4 Undocumented reorder | ✅ | `INVARIANT (B-9)` comment added above `revokeTokenUntilExpiry` |
| X-5 Compounding runtime | ⚠️ Mostly | `NewPasswordServiceWithCost` added; one call site missed — see **Y-3** |

**X-2 verified mechanically.** Reverting the production call site to the deleted constructor now fails the build:

```
$ sed -i 's|NewUserUseCaseWithTx(..., uow)|NewUserUseCase(...)|' cmd/api/main.go && go build ./...
cmd/api/main.go:240:18: undefined: auth.NewUserUseCase
```

That is the durable guarantee the last three rounds were missing.

---

## 🟢 Low

### Y-1: `NewUserUseCaseWithTx(..., nil)` still silently downgrades

[user_usecase.go:92-94](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L92-L94)

```go
if txRunner == nil {
    txRunner = &passthroughTxRunner{}
}
```

The compile-time guard catches *omitting* a runner. It does not catch passing an explicit `nil`, which compiles and silently runs every deactivation outside a transaction — the same failure mode X-2 was closing, through a narrower door.

The sibling nil-defaults on the same constructor (`sessionStore`, `refreshExpiry`) are benign conveniences; this one silently disables an atomicity guarantee. Dropping the branch is better: a nil `TxRunner` then panics at first use, which is loud and immediate rather than silent and permanent.

### Y-2: Production wiring is still not pinned by a test

Verified: removing the `uow := postgres.NewUnitOfWork(dbPool)` line and passing `auth.NewPassthroughTxRunner()` at [main.go:240](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L240) **compiles and passes the entire suite**:

```
--- build exit: 0
ok  flowforge/cmd/api        0.007s
ok  flowforge/internal/auth  4.685s
   … all packages ok
```

The plan marked the `buildAuthComponents` extraction optional and asked for the decision to be recorded in `action_history.md` either way; the execution entry does not mention it. Not a defect — the residual risk requires someone to deliberately pass the wrong runner rather than forget one, which is materially safer than where this started. Worth either doing the extraction or recording the skip so it stops resurfacing each review.

### Y-3: X-5 missed one call site, and the recorded speedup is measured against the wrong baseline

[handler_test.go:31](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler_test.go#L31) still constructs `auth.NewPasswordService()` at production cost 12. Per-test timings under `-race`:

```
--- PASS: TestAuthHandler_Login              (25.35s)   ← handler_test.go:31, missed
--- PASS: TestPasswordService_ComparePassword (20.32s)  ← correct, cost matters here
--- PASS: TestAuthUseCase_Login              (15.17s)   ← getDummyBcryptHash, see below
--- PASS: TestPasswordService_HashPassword    (4.91s)   ← correct
--- PASS: TestRedisSessionStore_Miniredis     (0.06s)
```

Two separate causes:

1. **`handler_test.go` was missed** in the sweep — switching it to `NewPasswordServiceWithCost(bcrypt.MinCost)` removes ~25 s.
2. **`getDummyBcryptHash`** ([usecase.go:20](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L20)) is a package-level `sync.OnceValue` at cost 12, so every login-failure subtest pays a full-cost compare that no injected `PasswordService` can lower. That is correct for production — it *is* the timing defense — but it means `TestAuthUseCase_Login`'s 15 s is not reachable without making the dummy hash injectable.

**Baseline correction:** the execution entry records *"reducing `internal/auth` test execution time from 105s down to 4.9s (a 20x speedup)"*. That compares the old `-race` time against the new **non**-race time. Like for like:

| | before | after |
|---|---|---|
| `go test -race` (what `make ci` runs) | 105 s | **70 s** |
| `go test` (no race) | ~4.4 s | 4.7 s |

A real ~1.5× improvement on the CI path, not 20×. Worth correcting so the next round does not budget against a figure that was never achieved.

---

## Remediation

See [phase_2_tx_guard_followup_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_followup_plan.md). None of these block merge.
