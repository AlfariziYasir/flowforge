# Implementation Plan — Phase 12 Review Remediation

Closes the 2 findings in [phase_12_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_12_review_findings.md) — 1 high (AE-1), 1 medium (AE-2).

**AE-1 and AE-2 are independent** — different files, different mechanisms. Fix in either order; AE-1 first since it's the higher-severity, single-file fix.

---

## Phase AT — Fix Rate Limiter Wrapping Order (AE-1)

### Step 1 — test first, it must be RED

#### [NEW] test in [cmd/api/main_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main_test.go)

- ★ **`TestNewRouterWithLimiter_RateLimitAppliesToAuthenticatedRoute`** — construct `NewRouterWithLimiter` with a real `AuthMiddleware`, a real signed JWT, and a fake `RedisClient` (matching `ratelimit_test.go`'s `fakeRedis` shape — copy or share it) that records every key it's called with. Hit a `generalLimit`-wrapped route (e.g. `GET /api/v1/users/me`, needs only `authHandler`) with a valid bearer token and assert the fake Redis's `Incr` was called with a key containing the tenant ID. This is the exact test that would have caught AE-1 — it goes through the real router construction, not a hand-assembled context like `ratelimit_test.go`'s existing tests. Red today: confirmed live in this review — zero `Incr` calls across 5 requests.

  > Reuse pattern: the review's own probe (deleted after use) constructed this exact scenario — `NewRouterWithLimiter(nil, authHandler, nil, nil, nil, middleware, nil, limiter, false)` with a `authmocks.MockAuthUseCase` stubbing `GetMe`, hitting `GET /api/v1/users/me`.

### Step 2 — the fix

#### [MODIFY] [cmd/api/main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go)

Every route currently wrapped `generalLimit(authMiddleware.Authenticate(...))` or `triggerRunLimit(authMiddleware.Authenticate(...))` needs the two swapped — `Authenticate` must be outermost so it populates the context before the rate limiter's `keyFunc` runs:

```go
// Before (wrong — rate limit runs before auth populates context):
mux.Handle("POST /api/v1/workflows/{workflowId}/runs",
    triggerRunLimit(authMiddleware.Authenticate(auth.RequireRole("admin", "editor")(http.HandlerFunc(executionHandler.TriggerRun)))))

// After (correct — auth runs first, rate limiter sees the tenant):
mux.Handle("POST /api/v1/workflows/{workflowId}/runs",
    authMiddleware.Authenticate(triggerRunLimit(auth.RequireRole("admin", "editor")(http.HandlerFunc(executionHandler.TriggerRun)))))
```

This applies to **every** `generalLimit(authMiddleware.Authenticate(...))` and `triggerRunLimit(authMiddleware.Authenticate(...))` occurrence in `NewRouterWithLimiter` — every workflow, execution, and user route currently wired that way. `loginLimit` is unaffected (correctly `IPKeyFunc`-based, doesn't need auth context) and should not be touched.

Whether `RequireRole` sits inside or outside the rate limiter doesn't matter functionally (both need the tenant from `Authenticate`, and rate limiting an unauthorized-role request before or after the 403 check is a minor policy choice, not a correctness issue) — keep it inside (closest to the handler) to match the plan's original phrasing ("wraps `RequireRole(...)`, not the other way").

### Verification — Phase AT

```
go test ./cmd/api/ -run TestNewRouterWithLimiter_RateLimitAppliesToAuthenticatedRoute -v
# green

# mutation: revert one route's ordering back to generalLimit(authMiddleware.Authenticate(...))
# the new test MUST fail

# manual re-verification of the review's own probe, for extra confidence:
# 5 authenticated requests to a generalLimit route with a 2/min-capped fake limiter
# must show request 3+ returning 429, not all 200
```

Guarantee that must not regress: `TestLimiter_Middleware` (the existing, correctly-scoped unit test for `ratelimit.Middleware` in isolation) stays green unchanged — this fix is purely about composition order in `main.go`, not the middleware's own logic.

---

## Phase AU — Fix `user.created` Audit Attribution (AE-2)

### Step 1 — test first, it must be RED

#### [MODIFY] [user_usecase_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase_test.go)

- ★ **`TestCreateUser_AuditRecordsActualActor`** — construct `CreateUserCommand` with an explicit `ActorID` set to a *different* UUID than the user being created (mirroring how `TestUpdateUser`-style tests in this file presumably already assert `ActorID` for the update path — check the existing pattern and match it). Assert the recorded `domain.AuditEntry.ActorUserID` equals the command's `ActorID`, not the new user's own `ID`. Red today: `ActorUserID` is always `&usr.ID` because `CreateUserCommand` has no `ActorID` field for the test to even set — this test can't compile until Step 2 lands, which is itself proof of the gap (same pattern as the Phase 8 remediation's `TestExecutionUseCase_PublishEvent_RecordsMetric`).

### Step 2 — the fix

#### [MODIFY] [user_usecase.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go)

Add the missing field, matching `UpdateUserCommand`'s existing shape exactly:

```go
type CreateUserCommand struct {
	ActorID  uuid.UUID `json:"-"`
	TenantID uuid.UUID `json:"tenantId"`
	Email    string    `json:"email"`
	Password string    `json:"password"`
	Role     string    `json:"role"`
}
```

`CreateUser`'s audit call uses it, with the same nil-fallback discipline `UpdateUser` already has (defensive, in case a future caller doesn't set it — though the handler fix in Step 2 below means the production path always will):

```go
actorID := cmd.ActorID
if actorID == uuid.Nil {
	actorID = usr.ID
}
...
if err := u.audit.Record(txCtx, domain.AuditEntry{
	TenantID:    usr.TenantID,
	ActorUserID: &actorID,
	Action:      ActionUserCreated,
	EntityType:  "user",
	EntityID:    &usr.ID,
	Metadata:    meta,
}); err != nil {
```

#### [MODIFY] [user_handler.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go)

`CreateUser`'s handler already has `authUser` in scope (used for `TenantID`) — thread `authUser.ID` through the same way:

```go
cmd := CreateUserCommand{
	ActorID:  authUser.ID,
	TenantID: authUser.TenantID,
	Email:    req.Email,
	Password: req.Password,
	Role:     req.Role,
}
```

### Verification — Phase AU

```
go test ./internal/auth/... -race -count=1 -run TestCreateUser_AuditRecordsActualActor -v

# mutation: revert the audit call back to ActorUserID: &usr.ID
# the new test MUST fail
```

Guarantee that must not regress: the existing `UpdateUser` audit test(s) stay green unchanged — this fix only touches `CreateUser`'s path.

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| AT — Rate limiter ordering | AE-1 | ★ test red → green; mutation fails; login route untouched |
| AU — Audit actor attribution | AE-2 | ★ test red → green; mutation fails; `UpdateUser` audit tests unchanged |

After each phase: `make ci` **and** `FLOWFORGE_INTEGRATION=1 make test-integration`, both green, before appending to `.agents/memory/action_history.md`.

Guarantees that must not be lost:

```
FLOWFORGE_INTEGRATION=1 go test ./internal/platform/ratelimit/ -run TestLimiter_MultiInstanceIntegration -race -count=1
# cross-instance shared quota — untouched by this fix, must stay green

go test ./internal/auth/... -race -count=1
# the full existing auth suite, including UpdateUser's correct audit attribution
```

---

## Notes for Phase 13

- **Case A's payload-shape sniffing** (noted, not scored, in the findings doc) is worth hardening with an explicit discriminator field before a real external system's exact payload shape is finalized — the risk is currently theoretical, not demonstrated.
- **`DeliverEvent`'s self-reported `triggerType` isn't clamped server-side** — low impact, worth a one-line fix (force `"grpc"`/`"queue"` regardless of caller input) whenever this file is next touched.
- D-4's placeholder eventbus contract and the frontend-dependent CORS origin value remain carried forward, unchanged from Phase 12's own plan.
