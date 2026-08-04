# Phase L–N (Tx Wiring) — Verification Review Findings

Post-execution review of the fixes applied for [phase_2_tx_wiring_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_wiring_verification_findings.md) per [phase_2_tx_wiring_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_wiring_verification_plan.md).

**Verdict: 🟡 CONDITIONAL — 1 medium, 4 low** (X-1 … X-5)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok**.

> [!NOTE]
> **This is the cleanest round so far.** Seven of the eight findings are fully fixed, including both high-severity ones on the functional side. The transaction is genuinely wired now, the backfill sentinel works, and the entire W-8 test backlog landed — including the two hardest cases (`Refresh: revokes the old JTI before issuing new tokens` with real call-order assertions, and `ListUserSessions: prunes expired index entries`).
>
> One thing did not get done: **the test that was supposed to prevent W-1 from recurring was not rewritten.** The bug is fixed; the guard against it is not.

---

## Remediation Scorecard

| Finding | Result | Note |
|---|---|---|
| W-1 `TxRunner` not wired | ✅ | [main.go:222](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L222) builds the UoW, [:240](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L240) passes it |
| W-2 Vacuous rollback test | ❌ | Unchanged, byte for byte — see **X-1** |
| W-3 Variadic constructors | ⚠️ Half | Named constructors added ✅; compile-time guard did not materialise — see **X-2** |
| W-4 Revoke-all rescan | ✅ | Sentinel restored; measured **1 SCAN** for 3 revoke-alls (was 3) |
| W-5 Dead error branch | ✅ | Removed |
| W-6 Empty-token logout | ✅ | 401 retained as decided |
| W-7 `go mod tidy` | ✅ | `miniredis` now a direct require |
| W-8 Test backlog | ✅ | All 6 cases landed, plus 2 new Redis cases |
| Housekeeping | ✅ | `.agents/prompts/` staged (`A` in `git status`) |

---

## 🟡 Medium

### X-1: The W-2 rollback test was never rewritten — nothing pins the transaction boundary

[user_usecase_test.go:195-225](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go#L195-L225)

The subtest is identical to the version flagged last round:

```go
userRepo.EXPECT().UpdateUser(mock.Anything, mock.Anything).Return(nil)   // asserts the write HAPPENED
...
uc := auth.NewUserUseCase(userRepo, passSvc, sessionStore, 7*24*time.Hour)   // ← noopTxRunner
...
_, err := uc.UpdateUser(ctx, cmd)
is.Error(err)                                                            // ← still the only assertion
```

None of the plan's Phase L test work was done:

```
$ grep -rn "NewUserUseCaseWithTx\|auth.TxRunner\|recordingTxRunner" --include=*_test.go .
(no matches — the only ExecuteInTx hits are postgres/unit_of_work_test.go's own tests)
```

There is no `recordingTxRunner`, no `deactivation runs inside a transaction` case, and no test anywhere constructs the use case with a real `TxRunner`.

**Consequence:** W-1 is fixed in `main.go`, but nothing holds it fixed. Reverting that one line to `auth.NewUserUseCase(...)` compiles cleanly and passes the entire suite:

```
NewUserUseCase(4 args) still compiles; txRunner is noop = true
=> a revert of cmd/api/main.go to NewUserUseCase would compile and pass every test
```

This is the same pattern that produced W-1: the plan's ordering — *"Phase L fixes the test first, watches it go red, then fixes the wiring"* — was inverted. The wiring landed; the guard did not.

---

## 🟢 Low

### X-2: The compile-time guard W-3 was meant to buy did not materialise

[user_usecase.go:81-83](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L81-L83)

```go
func NewUserUseCase(userRepo UserRepository, passSvc PasswordService, sessionStore SessionStore, refreshExpiry time.Duration) UserUseCase {
    return NewUserUseCaseWithTx(userRepo, passSvc, sessionStore, refreshExpiry, &noopTxRunner{})
}
```

The named-constructor split landed and reads well, but `NewUserUseCase` kept the **identical four-argument signature**, so omitting the unit of work is still silent. The plan's stated payoff — *"confirm `go build ./...` fails until `main.go` is updated — that compile error is the durable guard W-3 buys"* — never happened, because nothing forced the call site to change.

Structurally this is the other half of **X-1**: there is now neither a compile-time nor a test-time guard on the transaction boundary. Fixing X-1 is sufficient; this note explains why the constructor split alone is not.

### X-3: The B-9 ordering assertion uses a proxy operation

[usecase_test.go:297-341](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase_test.go#L297-L341)

The case is named `revokes the old JTI before issuing new tokens` but orders `Revoke` against `CreateNewSession`, not against `GenerateTokenPair`:

```go
is.Equal("RevokeOldJTI", callOrder[0], "expected old JTI to be revoked BEFORE creating new session")
is.Equal("CreateNewSession", callOrder[1])
```

`jwtSvc` is a real `NewJWTService`, not a mock, so token generation cannot be recorded. The assertion holds transitively today because `GenerateTokenPair` sits after `CreateSession` — but if someone moves token generation above the session update, B-9 reopens while this test stays green. Using `mocks.NewMockJWTService` for this one case would assert the actual invariant.

### X-4: `Refresh` was reordered beyond the plan's scope

[usecase.go:69-79 (within `Refresh`)](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)

`revokeTokenUntilExpiry` moved **above** the `sess.LastActiveAt` / `CreateSession` block to satisfy the new ordering test. The resulting sequence — validate → lifetime cap → load user → revoke old JTI → update session → issue pair — is coherent and fails safe: if the session update or token generation fails, the old refresh token is already dead and the caller must re-authenticate.

No defect, but it is a behavioural change that was not in the plan and is not recorded in `action_history.md`. Worth a comment in `Refresh` stating that revocation must precede issuance, so the order is not "tidied" back later.

### X-5: Test suite runtime is compounding

`internal/auth` under `-race`: **58s → 89s → 105s** across the last three rounds. The cause is bcrypt at cost 12 (~240 ms per hash) multiplied across a growing subtest count, amplified by the race detector. Not a defect, but `make ci` is approaching two minutes for one package.

Making the bcrypt cost injectable — `NewPasswordServiceWithCost(cost int)` alongside the existing `NewPasswordService()`, using `bcrypt.MinCost` in tests — would cut most of it. The password-strength tests would keep the production cost.

### Note on X-1's accepted cost

The plan's callout on **W-4** predicted that `RevokeAllUserSessions` would leave a sentinel-only index key per revoked user for up to `defaultTTL` (7 days). That is confirmed working as designed — flagging only so the retained-key behaviour is a known, accepted cost rather than a surprise during a Redis capacity review.

---

## Remediation

See [phase_2_tx_guard_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_verification_plan.md).
