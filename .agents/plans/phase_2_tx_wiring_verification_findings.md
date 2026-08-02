# Phase K–J Hotfix — Verification Review Findings

Post-execution review of the fixes applied for [phase_2_hotfix_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_hotfix_verification_findings.md) per [phase_2_hotfix_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_hotfix_verification_plan.md).

**Verdict: 🟠 NEEDS REVISION — 2 high, 2 medium, 4 low** (W-1 … W-8)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok**.

> [!NOTE]
> **V-1, V-2 (list path), V-4, V-5, V-6, V-7 and V-8 are all correctly fixed**, and the Redis test suite went from zero coverage to eight real miniredis-backed cases. The comparison operator is now `<`, the migration sentinel works, `NewRedisSessionStore` takes the configured TTL and is wired, the reviewer gate is amended, and `.agents/prompts/reviewer.md` §4 now reads "named subtests or Table-Driven Test patterns".

> [!WARNING]
> **V-3 did not actually ship.** The `TxRunner` plumbing was built correctly but is never passed at the one call site that matters, so deactivation is still non-transactional in production — and the regression test written to catch exactly this cannot fail. See **W-1** and **W-2**.

---

## Remediation Scorecard

| Finding | Result | Note |
|---|---|---|
| V-1 `<=` → `<` | ✅ | [session_store.go:296](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L296); both timing tests present and passing |
| V-2 Backfill sentinel | ⚠️ Half | `ListUserSessions` scans once ✅; `RevokeAllUserSessions` rescans every call — see **W-4** |
| V-3 Atomic deactivate+revoke | ❌ | Built but not wired — see **W-1**, **W-2** |
| V-4 Logout status | ✅ | Both malformed cases → `ErrUnauthorized` → 401; see **W-5**, **W-6** for residue |
| V-5 Backfill errors + TTL | ✅ | `SAdd`/`Expire` propagate; `defaultTTL` threaded from `cfg.JWTRefreshExpiry` |
| V-6 Dead sort | ✅ | Removed |
| V-7 Validator side effect | ✅ | Split into `applyJWTSecretDefault` + `validateJWTSecret` |
| V-8 Reviewer gate | ✅ | §4 amended to accept named subtests |
| V-9 Test debt | ⚠️ ~11/17 | Real progress; 6 cases missing and 1 vacuous — see **W-2**, **W-8** |

---

## 🟠 High

### W-1: `TxRunner` is never wired — V-3 is a no-op in production

[main.go:239](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L239) · [user_usecase.go:79-97](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L79-L97)

The `TxRunner` interface, the `noopTxRunner` fallback, and both `ExecuteInTx` wrappers in `UpdateUser`/`DeleteUser` are all implemented correctly. But the production call site passes four arguments:

```go
userUC := auth.NewUserUseCase(userRepo, passSvc, sessionStore, cfg.JWTRefreshExpiry)
```

`txRunner` is variadic, so this compiles, `len(txRunner) == 0`, and `runner` falls back to `&noopTxRunner{}` — whose `ExecuteInTx` is `return fn(ctx)`. No transaction is ever opened. `postgres.NewUnitOfWork` is never called anywhere outside its own test.

Reproduced against the exact production construction:

```
err = revoke user access on update: set user revoked before timestamp: redis down
userRepo.UpdateUser invoked 1 time(s) and NOT rolled back
usr.IsActive in memory = false

with explicit TxRunner: ExecuteInTx used = true    ← the mechanism works; it is just not passed
```

A Redis failure during deactivation still commits the Postgres write and returns 500 — the exact R-9/V-3 state, unchanged since it was first raised three rounds ago.

**Fix:** construct `postgres.NewUnitOfWork(dbPool)` in `main.go` and pass it. See **W-3** for why the signature made this easy to miss.

---

### W-2: The V-3 regression test cannot fail

[user_usecase_test.go:195-225](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go#L195-L225)

```go
uc := auth.NewUserUseCase(userRepo, passSvc, sessionStore, 7*24*time.Hour)  // ← noopTxRunner
...
_, err := uc.UpdateUser(ctx, cmd)
is.Error(err)                                                              // ← the only assertion
```

The subtest is named `deactivation rolls back when revocation fails` but asserts nothing about rollback. Three problems compound:

1. It constructs the use case **without** a `TxRunner`, so it exercises the pass-through path — the same path production uses (**W-1**), which performs no rollback.
2. `is.Error(err)` was already satisfied *before* Phase I: the pre-existing code returned `fmt.Errorf("revoke user access on update: %w", err)`. The test passes identically against the unfixed tree.
3. `userRepo.EXPECT().UpdateUser(...).Return(nil)` asserts the write **happened** — the opposite of the rollback the name claims.

The plan was explicit: *"The four ★ cases must be red before Phase I, green after… one that passes when it should fail is a test bug."* This case can never be red, which is why W-1 shipped green.

**Fix:** inject a `TxRunner` spy that records whether the callback returned an error, and assert the transaction was entered and the error propagated. A true rollback assertion needs the integration-tagged Postgres path in [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go).

---

## 🟡 Medium

### W-3: Variadic optional dependencies hide missing wiring

[user_usecase.go:79](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L79) · [session_store.go:57](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L57)

```go
func NewUserUseCase(..., refreshExpiry time.Duration, txRunner ...TxRunner) UserUseCase
func NewRedisSessionStore(client redisclient.Cmdable, defaultTTL ...time.Duration) SessionStore
```

Both new dependencies were added as variadic parameters, so omitting them is not a compile error. `NewRedisSessionStore` *was* wired ([main.go:228](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L228) passes `cfg.JWTRefreshExpiry`); `NewUserUseCase` was not — and nothing flagged it. **This is the mechanism by which W-1 happened.**

The codebase already has an established pattern for optional dependencies that does not have this property — a second named constructor: `NewAuthUseCaseWithLogger`, `NewAuthMiddlewareWithSessionStore`, `NewAuthHandlerWithTrustProxy`. Those force the call site to state its intent, and a signature change breaks the build until every caller is revisited.

### W-4: `RevokeAllUserSessions` rescans the keyspace on every call

[session_store.go:229-256](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L229-L256)

`backfillIndexIfEmpty` performs `SCAN` → `SADD __migrated__` → `EXPIRE`, and then `RevokeAllUserSessions` deletes the index key at [:247](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L247) — **including the sentinel it just wrote**. The next call finds an empty index and scans again.

Measured with a `Scan`-counting `Cmdable` wrapper over miniredis:

```
after RevokeAllUserSessions #1 -> cumulative SCAN calls = 1
after RevokeAllUserSessions #2 -> cumulative SCAN calls = 2
after RevokeAllUserSessions #3 -> cumulative SCAN calls = 3   ← W-4

after 3 ListUserSessions       -> cumulative SCAN calls = 1   ← V-2 fixed here
```

The `ListUserSessions` half of V-2 is genuinely fixed; the revoke path is not. Every `POST /api/v1/auth/logout-all`, every deactivation, and every role change pays a full-keyspace scan.

**Fix:** after the `Del`, re-`SADD` the sentinel with `s.defaultTTL` so the index is left in the "migrated, empty" state rather than absent. The backfill itself must stay — revoking pre-upgrade sessions is R-4's purpose.

---

## 🟢 Low

### W-5: Dead error branch in `AuthHandler.Logout`

[handler.go:107](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L107)

```go
if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrInvalidToken) {
```

`authUseCase.Logout` no longer returns `ErrInvalidToken` anywhere — the nil-`ExpiresAt` path now returns `ErrUnauthorized`. The second clause is unreachable.

### W-6: `Logout` with no token now returns 401

[usecase.go:281-284](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L281-L284)

```go
if tokenStr == "" {
    return ErrUnauthorized
}
```

V-4 asked for malformed tokens to stop reporting success. An **absent** token was a third case not in scope, and it changed too: `POST /api/v1/auth/logout` with no `Authorization` header now returns 401 instead of 200. Defensible, but it is a client-visible contract change on an unauthenticated route — worth confirming rather than discovering.

### W-7: `go mod tidy` was not run

[go.mod](file:///home/mohyasiralfarizi/Golang/flowforge/go.mod)

```
github.com/alicebob/miniredis/v2 v2.38.0 // indirect
github.com/stretchr/objx v0.5.2 // indirect
github.com/yuin/gopher-lua v1.1.1 // indirect
```

`miniredis` is a direct test dependency, not indirect. The block placement and comments are what `go get` leaves behind; `go mod tidy` would move it into the direct require block.

### W-8: Six planned test cases still missing

Landed this round (all real, all passing): the two V-1 timing cases, `backfill: legacy SCAN runs at most once per user`, `backfill: recovers pre-upgrade sessions with no index entry`, `ListUserSessions: pagination is stable across repeated calls`, `RevokeAllUserSessions: clears the index key`, `returns ErrUnauthorized for malformed or unparseable token string`, `triggers revocation on role change`.

Still absent:

| Missing case | Pins |
|---|---|
| `ListUserSessions: prunes expired index entries` | index lifecycle |
| `Logout: blacklist TTL covers the token's remaining lifetime` | B-3 |
| `Refresh: rotation TTL matches token lifetime` | R-12 |
| `Refresh: rejects a token with no session ID` | B-4 |
| `Refresh: revokes the old JTI before issuing` (ordering) | B-9 |
| `Login: logs the inactive-account reason` | R-11 |

Note also that **`RevokeAllUserSessions` has no scan-count test** — which is why W-4 shipped despite the sibling `ListUserSessions` case being written correctly.

### Housekeeping

`.agents/prompts/` remains untracked (`?? .agents/prompts/`). The reviewer gate the last three reviews ran against is still not versioned with the code.

---

## Remediation

See [phase_2_tx_wiring_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_tx_wiring_verification_plan.md).
