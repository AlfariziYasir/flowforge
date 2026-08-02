# Phase 2 Hotfix (Phases E–H) — Verification Review Findings

Post-execution review of the fixes applied for [phase_2_remediation_verification_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_remediation_verification_findings.md) per [phase_2_remediation_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_remediation_verification_plan.md).

**Verdict: 🟡 CONDITIONAL — no critical defects remain; 3 medium, 5 low, 1 process note** (V-1 … V-9)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` **empty** · `go test ./... -race -count=1` all packages **ok**.

> [!NOTE]
> **Both criticals from the previous round are resolved.** R-1 (production booting on the committed dev secret) is fully fixed and now has a table-driven test. R-3 (middleware nil-`iat` panic) is fixed and verified — a token with no `iat` now returns 401 with no panic. R-2 is ~99% fixed: the millisecond-precision change shrank the lockout window from ~500 ms to ~1 ms, but a one-character comparison bug keeps a narrow edge open (**V-1**).

---

## Remediation Scorecard

| Finding | Result | Note |
|---|---|---|
| R-1 Default JWT secret in prod | ✅ | `validateJWTSecret` rejects unset, short, **and** the dev literal; covered by `TestValidateJWTSecret` |
| R-2 Revocation lockout | ⚠️ ~99% | `jwt.TimePrecision = time.Millisecond` correct; `<=` still wrong — see **V-1** |
| R-3 Middleware nil-`iat` panic | ✅ | Guard added at [middleware.go:75-78](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/middleware.go#L75-L78); verified 401, no panic |
| R-4 Legacy session backfill | ⚠️ Works, but | Correct behaviour; unbounded SCAN cost — see **V-2** |
| R-5 Dev fails at login | ✅ | Noop `CreateSession` errors |
| R-6 409 on update | ✅ | Branch added **and** tested |
| R-7 Stable pagination | ✅ | `sort.Slice` on `activeSessions` — see **V-6** for the dead first sort |
| R-8 Regression tests | ⚠️ ~16/30 | Good coverage, one concentrated gap — see **V-9** |
| R-9 Atomic deactivate+revoke | ❌ | Not done — see **V-3** |
| R-10 Table-driven conversion | ✅* | Satisfied via named `t.Run` subtests — see **V-8** |
| R-11 Inactive-login log | ✅ | `slog.Logger` injected, `Info` on rejection |
| R-12 Rotation TTL | ✅ | `revokeTokenUntilExpiry` helper shared by `Logout` and `Refresh` |
| R-13 Nil `ExpiresAt` on logout | ⚠️ Partial | No longer reports success, but status mapping is wrong — see **V-4** |
| R-14 `Expire` error | ✅ | Propagated in `CreateSession` — but not in the backfill, see **V-5** |
| R-15 Dead `DeleteUser` | ✅ | Removed |

---

## 🟡 Medium

### V-1: `IsUserRevoked` uses `<=` where `<` is correct

[session_store.go:259](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L259)

```go
return issuedAt.UnixMilli() <= revokedUnixMilli, nil
```

The millisecond-precision half of the R-2 fix is correct and is doing its job. What remains is the comparison operator: `<=` marks a token issued in the **same millisecond** as the revocation as revoked, which is precisely the fresh-login case the fix exists to protect. The semantically correct predicate is "issued *strictly before* the revocation instant".

Measured both directions against the real `jwtService`:

```
fresh token, same ms as revocation? true
  with <= : revoked=true   (want false)   ← the bug
  with <  : revoked=false  (want false)

in-flight token issued before revocation
  with <= : revoked=true   (want true)
  with <  : revoked=true   (want true)    ← B-6 stays closed
```

`<` is correct in both directions; `<=` is wrong in one. Reproduced end-to-end through `AuthMiddleware.Authenticate`: a token generated immediately after `SetUserRevokedBefore` returns

```
401 {"error":"Unauthorized","message":"user token has been revoked"}
```

**Impact is now low** — a real logout-all → re-login round trip includes an HTTP hop and a ~240 ms bcrypt compare, so same-millisecond collisions will be rare in production. But the window is real, reachable, and the fix is one character.

---

### V-2: Legacy-session backfill runs a full keyspace `SCAN` on every empty index

[session_store.go:163-192](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L163-L192) · called from [:200](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L200) and [:275](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L275)

```go
sids, err := s.client.SMembers(ctx, indexKey).Result()
...
if len(sids) == 0 {
    sessionKeys, err := s.scanUserSessionKeys(ctx, tenantID, userID)   // O(keyspace)
```

The plan specified "gate it on an empty index so the fallback costs nothing in steady state." In practice the gate is inverted: **an empty index *is* the steady state** for

- any user who has never logged in,
- any user whose sessions have all expired,
- **every user immediately after `LogoutAllDevices`** — which deletes the index key at [:211](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L211).

Both `RevokeAllUserSessions` and `ListUserSessions` route through it, so `GET /api/v1/auth/sessions` triggers a full-keyspace `SCAN` on demand for any authenticated user with no sessions. This re-introduces exactly the O(keyspace) cost that N-1 removed, and there is no marker distinguishing "index missing because pre-upgrade" from "index missing because empty", so the fallback never retires.

**Fix:** write a per-user sentinel (e.g. `SADD` a `__migrated__` member, or a separate `session:migrated:{tenant}:{user}` key with the same TTL) on first backfill and skip the `SCAN` when present. Add a comment with a concrete removal date — one `JWTRefreshExpiry` past rollout.

---

### V-3: R-9 was not implemented — deactivation is still non-atomic

[user_usecase.go:205-213](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L205-L213) · [user_usecase.go:233-239](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L233-L239)

```
$ grep -rn "UnitOfWork\|unit_of_work" internal/auth/ | grep -v _test
NONE
```

`userRepo.UpdateUser` still commits before `revokeUserAccess` runs, so a Redis failure returns 500 to the caller with the user already deactivated in Postgres and every session still live — the exact state R-9 was raised to prevent. The existing [unit_of_work.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/unit_of_work.go) is untouched by this package.

---

## 🟢 Low

### V-4: `Logout` status mapping is inconsistent across malformed tokens

[usecase.go:290-300](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/usecase.go#L290-L300) · [handler.go:106-109](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/handler.go#L106-L109)

Two kinds of malformed input now produce opposite outcomes:

| Input | Use case returns | HTTP |
|---|---|---|
| Token fails both validations (garbage/expired) | `nil` | **200 success** |
| Token valid but missing `exp` | `ErrInvalidToken` | **500 Internal Error** |

R-13 correctly stopped reporting success for the second case, but 500 is the wrong class — this is client-supplied input, not a server fault. And the first case still reports success for a token that was never revoked. Both should be **401**, or both should be a deliberate silent success; the current split is neither.

### V-5: Backfill discards errors and hardcodes its TTL

[session_store.go:186-187](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L186-L187)

```go
_ = s.client.SAdd(ctx, indexKey, backfillSIDs...).Err()
_ = s.client.Expire(ctx, indexKey, 7*24*time.Hour).Err()
```

Both errors are dropped two functions below the `CreateSession` where R-14 was applied, and the TTL is the same hardcoded `7*24*time.Hour` literal that B-5 removed elsewhere — it ignores `JWTRefreshExpiry`. If the configured refresh expiry is longer, the backfilled index expires before the sessions it indexes and the `SCAN` fires again (compounding **V-2**).

### V-6: Dead sort in `ListUserSessions`

[session_store.go:284](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L284)

`sort.Strings(sids)` is superseded by the `sort.Slice(activeSessions, ...)` at [:320](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store.go#L320) after filtering. Harmless, but it reads as if ordering depends on it.

### V-7: `validateJWTSecret` mutates its argument

[main.go:145-156](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L145-L156)

```go
func validateJWTSecret(cfg *config.Config) error {
    if cfg.JWTSecret == "" {
        ...
        cfg.JWTSecret = devJWTSecret     // ← side effect hidden by the name
    }
```

A `validate*` function that assigns a default is a name/behaviour mismatch. The extraction for testability was the right call (and `TestValidateJWTSecret` is good); the default assignment belongs in a separately named step.

### V-8: R-10 — close as satisfied, not as debt

The repo has settled on named `t.Run` subtests (**110** across the suite) rather than literal `for _, tt := range` table loops. That is the same intent the reviewer gate is after — named, isolated, enumerable cases — and it is applied consistently. Counting `for ... range` loops undercounts it badly.

**Recommendation:** close N-10/R-10 as satisfied and amend `.agents/prompts/reviewer.md` §4 to read "named subtests or table-driven" so future reviews stop re-raising it. Mechanically rewriting 110 working subtests into table loops would be churn with no correctness payoff.

### V-9: R-8 — good progress, one concentrated gap

Roughly **16 of 30** planned cases landed, and the ones that landed are real. Verified present:

`ValidateJWTSecret` (table-driven, 4 environments) · `UserListRequiresElevatedRole` (admin/viewer/unauthenticated) · `IsUniqueViolation` · `CreateSession fails closed` · `GetSession fails closed` · `401 without panic when token lacks IssuedAt claim` · `uniform failure response for all login failure reasons` · `409 Conflict when updated user email already exists` · `409 Conflict when user email already exists` · deactivation and deletion revocation triggers · self-deactivate/self-delete guards.

The remaining gap is **not evenly spread — it is almost entirely the new Redis index code and the timing-sensitive revocation semantics**:

| Missing case | Would have caught |
|---|---|
| `FreshTokenSurvivesSameInstantRevocation` | **V-1** |
| `InFlightTokenStillRevoked` | V-1 regression guard |
| `BackfillRunsAtMostOnce` | **V-2** |
| `BackfillsLegacySessions` | R-4 correctness (untested entirely) |
| `List_StablePagination` | R-7 (untested entirely) |
| `RevokeAll_ClearsIndex`, `ListPrunesExpiredIndexEntries` | index lifecycle |
| `Deactivate_PropagatesRevocationError` | V-3 |
| `MalformedTokenDoesNotReportSuccess` | **V-4** |
| `Logout_BlacklistTTLCoversTokenLifetime`, `RotationTTLMatchesTokenLifetime` | R-3/R-12 TTL assertions |
| `LogsInactiveReason`, `Refresh_RejectsTokenWithoutSessionID` | R-11, B-4 |

`redisSessionStore` gained `backfillIndexIfEmpty`, `scanUserSessionKeys`, sentinel-free index management and sorted pagination in this round — roughly 90 lines of new Redis logic — and [session_store_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/session_store_test.go) still only covers the noop store, constructor validation, and key formatting. Every defect in this review sits in either that untested code or the untested timing predicate.

Practical blocker: those paths need a real or faked Redis. `miniredis` is not currently a dependency (`go get github.com/alicebob/miniredis/v2`), and a `redisclient.Cmdable` spy is the alternative.

---

## Remediation

See [phase_2_hotfix_verification_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_2_hotfix_verification_plan.md).
