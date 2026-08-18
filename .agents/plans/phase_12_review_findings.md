# Phase 12 (Backend Completion) — Review Findings

Review of the execution of [phase_12_backend_completion.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_12_backend_completion.md).

**Verdict: 🔴 REJECTED — 1 high, 1 medium** (AE-1, AE-2)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` empty · `go test ./... -race -count=1` clean · `FLOWFORGE_INTEGRATION=1 go test ./... -race -count=1` clean · `make ci` exit 0.

> [!CAUTION]
> **AE-1 defeats the entire rate-limiting workstream (E-1/E-2) for every authenticated route.** The unit tests for `ratelimit.Middleware` pass because they construct the auth context manually before invoking the middleware directly — they never exercise the actual composition order used in `cmd/api/main.go`, which is backwards. Confirmed live: 5 requests to a 120/min-limited route, zero Redis `INCR` calls, all 200s.

---

## What Was Done Right — and it is substantial

- **CORS and security-headers wiring is correct**, including the one place order genuinely doesn't matter (they wrap the whole router, outside all per-route auth) — unlike the rate limiter, these don't depend on auth context, so their outermost placement is fine, not a mirror of AE-1's bug.
- **The rate limiter's own logic (`Limiter.Allow`) is correct and well-tested** — fail-open on Redis error is explicitly tested and passes; cross-instance shared quota is proven live against real Redis. The bug is entirely in how the middleware is composed into the router, not in the limiter itself.
- **`UpdateUser`'s audit attribution is correct** — `cmd.ActorID` is used for `ActorUserID`, with a sensible self-service fallback. This makes AE-2 more clearly an inconsistency than a "the pattern was never understood" problem — the correct version exists one function away.
- **Both migrations (`000007`, `000008`) verified live, both directions**, inside a rolled-back transaction against the real database — `000007`'s `.down.sql` recreates the exact original `idempotency_keys` schema; `000008`'s `.down.sql` correctly narrows the CHECK constraint back.
- **V-1 is done exactly as scoped** — `ContextWithTx`/`TxFromContext` speak `DBTX`, confirmed directly in `dbtx.go`.
- **`/metrics` is live in both processes**, correctly gated on `reg != nil` in `cmd/api` and running as an isolated minimal server in `cmd/worker` with its own graceful `Shutdown`.
- **SSE `step.waiting`/`workflow.run.waiting` are wired**, closing the open item from Phase 8.
- **Smoke tests land exactly where the plan specified** — `cmd/worker` went from zero test files to real coverage; `HealthChecker`'s four states are table-driven tested.

---

## 🔴 High

### AE-1: Rate limiting is silently inert on every authenticated route — the middleware wraps outside `Authenticate`, so `TenantKeyFunc` never sees a tenant

[cmd/api/main.go:169-217](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/api/main.go#L169-L217)

The plan's own §2.1 was explicit: `keyFunc` for authenticated routes "applied **after** `Authenticate`, so it wraps `RequireRole(...)`, not the other way — the tenant must be known first." The actual composition does the opposite:

```go
mux.Handle("POST /api/v1/workflows/{workflowId}/runs",
    triggerRunLimit(authMiddleware.Authenticate(auth.RequireRole("admin", "editor")(http.HandlerFunc(executionHandler.TriggerRun)))))
```

In Go's `f(g(h))` composition, `f`'s wrapper code runs *before* it calls `g`. Here `triggerRunLimit` is the outermost function — its code (which calls `ratelimit.TenantKeyFunc`, which reads `auth.AuthUserFromContext(r.Context())`) runs **before** `authMiddleware.Authenticate` has had any chance to populate that context. Every request arrives at `TenantKeyFunc` with no auth user in context, `TenantKeyFunc` returns `""`, and `Middleware`'s own `if key == "" { next.ServeHTTP(w, r); return }` fallback skips rate limiting entirely — for every single tenant-keyed route in the application, not just this one (`generalLimit` has the identical bug, applied to every workflow/user/execution route).

Confirmed live: constructed the real `NewRouterWithLimiter` with a real `AuthMiddleware`, a valid signed JWT, and a fake Redis client instrumented to record every `INCR` call. Sent 5 authenticated requests to a route wrapped in `generalLimit` (120/min):

```
statuses across 5 requests: [200 200 200 200 200]
fake redis counts map: map[]
```

Zero `INCR` calls. The Redis-backed quota this entire workstream exists to enforce (`api-4.md` §12.11) is never consulted for a single authenticated request.

**Only the login route is unaffected** — `loginLimit` uses `IPKeyFunc`, which reads the client IP from the request itself, not from auth context, so it doesn't need `Authenticate` to have run first. That route's wrapping order happens to be irrelevant to this bug, which is presumably why the discrepancy wasn't noticed: the one route exercised most naturally (login, pre-auth) works, and the pattern was copy-pasted to the others without re-deriving why it worked there.

**Why the existing tests didn't catch it**: `TestLimiter_Middleware` in `ratelimit_test.go` constructs the auth context manually (`ctx := auth.ContextWithAuthUser(...)`) and calls `ratelimit.Middleware(...)` directly on a dummy handler — it never goes through `authMiddleware.Authenticate`, so it can't observe an ordering bug between the two. `cmd/api/main_test.go` has no test that exercises `NewRouterWithLimiter` with a limiter at all.

### AE-2: `user.created` audit entries always attribute the action to the newly-created user, never the admin who created them

[user_usecase.go:21-26](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L21-L26) · [user_usecase.go:169](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_usecase.go#L169) · [user_handler.go:27-52](file:///home/mohyasiralfarizi/Golang/flowforge/internal/auth/user_handler.go#L27-L52)

```go
type CreateUserCommand struct {
    TenantID uuid.UUID `json:"tenantId"`
    Email    string    `json:"email"`
    Password string    `json:"password"`
    Role     string    `json:"role"`
}
```

No `ActorID` field — unlike `UpdateUserCommand`, which has one (`ActorID uuid.UUID \`json:"-"\``) and uses it correctly:

```go
// UpdateUser (correct)
actorID := cmd.ActorID
if actorID == uuid.Nil {
    actorID = usr.ID
}
...ActorUserID: &actorID...

// CreateUser (wrong)
...ActorUserID: &usr.ID...   // usr is the NEW user just created — never the caller
```

`user_handler.go`'s `CreateUser` has `authUser` (the authenticated admin performing the request, required by the route's `RequireRole("admin")` wrapper) available right there via `AuthUserFromContext`, but `CreateUserCommand` never carries it through to the usecase — the information is silently dropped at the handler boundary. Every `user.created` audit row will therefore forever claim the new user created their own account, which is nonsensical (`CreateUser` is exclusively an admin-only action per the route's RBAC) and defeats the accountability purpose `api-4.md` §12.12 exists for: "who did this" is unrecoverable from the audit log for every user creation, ever.

---

## Notes, not findings

- **Case A's `DeliverEvent`/NATS trigger overload uses payload-shape sniffing, not an explicit discriminator.** Both the gRPC and NATS ingress paths decide "is this an event-delivery or a run-trigger?" by attempting to `json.Unmarshal` the payload and checking whether a `workflowId` field came back non-empty. This is consistent across both transports (good), and the existing HMAC auth boundary still applies to both cases identically (also good) — but it's a magic-field multiplexing scheme over one wire shape rather than an explicit `oneof`/discriminator field, and if an `EVENT_WAIT` payload's business data ever legitimately contains a `workflowId` key, it would be silently misrouted to "trigger a new run" instead of resolving the wait token. No current payload shape in this codebase causes that collision, and no test proves the ambiguity is handled one way or the other — flagged as a design fragility worth hardening (an explicit `action` or `kind` field) before a real external system's payload shape is locked in, not as a demonstrated live bug.
- **`trigger_type` self-reported by the caller isn't clamped.** `DeliverEvent`'s trigger path defaults `TriggerType` to `"grpc"` only when the caller's `triggerType` field is empty — if a caller explicitly sends `"triggerType": "manual"`, that's what lands in `workflow_runs.trigger_type`, which is misleading for anything reading that column to distinguish ingress paths later. Low impact (it's an informational/audit-adjacent field, not an authorization boundary) — not scored as a finding, but worth a one-line fix (force the value server-side) if this area is touched again.

---

## Remediation

See [phase_12_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_12_review_remediation_plan.md). AE-1 and AE-2 are independent — different files, different mechanisms.
