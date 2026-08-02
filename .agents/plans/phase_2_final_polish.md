# Phase Q — Verification Review & Final Polish

Post-execution review of [phase_2_tx_guard_followup_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_guard_followup_plan.md).

**Verdict: ✅ APPROVED — 0 critical, 0 high, 0 medium, 3 low** (Z-1 … Z-3)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok** · `internal/auth` **33 s** under `-race` (was 105 s three rounds ago).

> [!NOTE]
> **This combines findings and remediation in one document**, deviating from the two-file convention used by the previous rounds. All three items are low-severity cleanup measured in minutes; a separate findings/plan pair would be more ceremony than content. Everything else about the `.agents/` convention is unchanged.

---

## All Y findings fixed

| Finding | Result |
|---|---|
| Y-1 nil `TxRunner` downgrade | ✅ Branch deleted, contract documented on the constructor |
| Y-2 Wiring decision unrecorded | ✅ Skip recorded with reasoning in `action_history.md` |
| Y-3a `handler_test.go` on cost 12 | ✅ Now `NewPasswordServiceWithCost(bcrypt.MinCost)` |
| Y-3b Dummy hash not injectable | ✅ **Better than specified** — see below |
| Y-3c Baseline figure wrong | ⚠️ Corrected by appending, not replacing — see **Z-1** |

**The X-2 compile guard survived the Y-1 change** — re-verified:

```
cmd/api/main.go:240:18: undefined: auth.NewUserUseCase
```

**Y-3b was implemented better than the plan proposed.** The plan called for a separately injected `dummyHash` plus a comment warning test authors to keep the dummy cost and the `PasswordService` cost in sync. The execution instead derives the dummy hash *from `passSvc` itself* at construction ([usecase.go:99-109](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L99-L109)), which makes timing equivalence **structural rather than advisory** — the two costs cannot drift apart, because there is only one cost. That is a genuine improvement on the plan and worth keeping.

**Regression sweep — every prior round's key invariant re-verified against the current tree:**

```
V-1/R-2: fresh token survives same-instant revocation   PASS
B-6:     in-flight token still revoked                  PASS
R-3:     nil iat does not panic                         PASS
W-4:     revoke-all scans at most once                  PASS
B-2:     noop store fails closed                        PASS
```

**Runtime**, measured like-for-like under `-race`:

```
TestPasswordService_ComparePassword  20.53s   ← production cost 12, correct: cost IS the test
TestPasswordService_HashPassword      5.70s   ← production cost 12, correct
TestAuthUseCase_Logout                4.95s
TestAuthUseCase_Login                 0.26s   ← was 15.17s
TestAuthHandler_Login                 0.17s   ← was 25.35s
```

26 s of the remaining 33 s is the two password tests, where the cost is the thing under test. There is no further meaningful reduction available without weakening what those tests verify.

---

## 🟢 Low — remaining polish

### Z-1: The superseded speedup figure was appended to, not replaced

[action_history.md:341](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md#L341) still reads:

> Switched test file call sites to `bcrypt.MinCost`, reducing `internal/auth` test execution time from 105s down to 4.9s (a 20x speedup).

The correction was added as a new bullet in the *following* entry rather than replacing this line, so a reader scanning the Phase O–P entry meets the wrong number first and may never reach the correction. The plan said *"Replace"*.

**Fix:** edit line 341 in place to `reducing internal/auth -race runtime from 105s to 70s`, and leave the Phase Q entry's `105s → 70s → 30s` progression as the running total. (Measured now: **33 s**; the recorded 30 s is within run-to-run noise, but using the measured figure is better.)

### Z-2: `getDummyBcryptHash` is now unreachable in practice

[usecase.go:20](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L20) · [usecase.go:107-109](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L107-L109)

```go
if dummyHash == "" {
    dummyHash = getDummyBcryptHash()
}
```

This fallback fires only when `passSvc` is nil or `HashPassword` errors:

- **`passSvc == nil`** — `Login` nil-panics on the very next use (`u.passSvc.ComparePassword(u.dummyHash, password)`), so the fallback protects nothing.
- **`HashPassword` errors** — only possible on a length violation, and the literal is a fixed 35 characters. If it somehow did error, the fallback would substitute a **cost-12** hash while `passSvc` runs at a lower cost, breaking timing equivalence in the opposite direction — dummy compares becoming *more* expensive than real ones, which is its own (weaker) oracle.

**Fix:** reject a nil `passSvc` in `NewAuthUseCaseWithLogger` the same way Y-1 handled a nil `TxRunner`, then delete both the fallback and the `getDummyBcryptHash` `sync.OnceValue`. That removes a dead cost-12 code path and makes the precondition explicit.

### Z-3: Dummy-hash generation moved from process-once to per-instance

[usecase.go:99-106](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L99-L106)

`NewAuthUseCaseWithLogger` now performs a bcrypt hash on every call — ~240 ms at production cost 12 — where the old `sync.OnceValue` did it once per process. `cmd/api/main.go` constructs exactly one instance at startup, so today this is a one-time cost and not a regression.

**No fix required.** Recording it because the property is invisible at the call site: if the use case ever becomes per-request or per-tenant, this turns into 240 ms of CPU per construction. A one-line comment on the constructor noting "performs a bcrypt hash; construct once per process" would prevent that discovery happening in production.

---

## Remediation

Single phase, all optional, none blocking merge.

#### [MODIFY] [action_history.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/memory/action_history.md)

- **Z-1** — replace the "105s down to 4.9s (a 20x speedup)" text at line 341 in place; use the measured **33 s** in the Phase Q running total.

#### [MODIFY] [usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go)

- **Z-2** — treat a nil `passSvc` as a programming error in `NewAuthUseCaseWithLogger` (panic, matching the Y-1 nil-`TxRunner` treatment), then delete the `dummyHash == ""` fallback and the now-unused `getDummyBcryptHash` variable and its `sync` import if nothing else needs it.
- **Z-3** — add to the existing `NOTE:` comment above the dummy-hash block: *"performs a bcrypt hash at cost `passSvc`'s — construct once per process, not per request."*

### Verification
- `make ci` clean.
- Re-run the X-2 guard: swap [main.go:240](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L240) to `NewUserUseCase` → build **fails**; restore.
- `go test ./internal/auth/ -race -count=1` — expect ~33 s, unchanged (Z-2 removes a path that never executes).

---

## Chain complete — remaining pre-merge items

Every finding from the original Phase 2 audit and the six verification rounds that followed is now resolved: **21 → 15 → 9 → 8 → 5 → 3 → 0** blocking or medium issues. What remains is not code:

1. **Live Postgres/Redis smoke test.** No unit test substitutes for it — it is the only end-to-end confirmation that the Redis-failure rollback actually happens:
   - `REDIS_URL` at a dead port, `PATCH /api/v1/users/{id}` `{"isActive": false}` → 500; restore Redis, `GET /api/v1/users/{id}` → **`isActive: true`**.
   - `POST /api/v1/auth/logout-all`, re-login, `GET /api/v1/users/me` ×100 → **100 × 200**.
   - `MONITOR` attached: three `logout-all` calls → **one** `SCAN`.
   - `ENV=production` with no `JWT_SECRET` → process exits non-zero.
2. **PostgreSQL 15+ floor.** `migrations/000001_init_schema.up.sql` uses column-scoped `ON DELETE SET NULL`, which requires PG 15. This is not recorded anywhere outside the migration's own comments and belongs in the deployment requirements.
3. **Nothing is committed.** The branch still has all of Phase 2 in the working tree — `.agents/prompts/` is staged, everything else is unstaged or untracked. Landing this as reviewable commits is the last step.
