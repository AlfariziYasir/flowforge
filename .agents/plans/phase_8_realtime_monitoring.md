# Implementation Plan — Phase 8: Real-Time Monitoring (**v2**)

**Planner:** Claude (Architect) · **Executor:** Executor agent
**Source docs:** `backlog.md` §Phase 8 · `api-4.md` §11 (SSE) · `AGENTS.md`
**Predecessor:** Phase 7 executed, reviewed, remediated, re-verified clean. The `execution_logs` writer gap (found while planning v1 of this document) is **fixed, reviewed, and independently re-verified live** — see [fix_execution_log_writer.md](fix_execution_log_writer.md).

> **v2 changelog.** v1's biggest open item was "the exact call site(s) where `LogRepository.Append` is invoked... not found in this planning pass." That's no longer true — the log-writer fix landed and gave Phase 8 something better than a plan to locate 7 sites: **7 sites that already exist, with a working example (`appendLog`) sitting right next to each one.** v2 re-verifies every line reference against the current tree (they shifted after the fix's insertions), and adds one thing v1 missed: `AuthMiddleware.Authenticate` is header-only today with no query-param fallback path, and it also checks token blacklist + session revocation — D-1's "accept a query-param token" needs a real design for *how*, not just a statement that it happens, or it risks either duplicating security-critical logic or silently dropping it.

---

## 0. Verification Log (v2 — fresh, against the current tree)

| Claim | Result |
|---|---|
| The 7 status-transition call sites in `coordinator.go` | ✅ **all 7 already have `appendLog` calls** (lines 377, 403, 435, 456, 511, 530, 533 — `coordinator.go` is now 647 lines, up from 595 in v1's pass). Phase 8 adds one `Events.Publish` line beside each `appendLog` line — same sites, same information, third form (after DB row and slog line) of the same event. |
| `CoordinatorConfig`'s current shape | ✅ re-read in full — unchanged from v1's pass in the fields that matter: still carries `Publisher executor.EventPublisher` (a **different, unrelated** concern — Phase 5/7's `EVENT_PUBLISH` node delivery) and `Metrics *metrics.Metrics` (optional, nil-safe telemetry). D-3's naming decision (a distinct `Events` field, not reusing `Publisher`) still holds and is now more clearly justified having seen the field survive a full extra phase unchanged. |
| `AuthMiddleware.Authenticate`'s exact behavior | ✅ read in full (`internal/auth/middleware.go`) — reads `Authorization` header only (`r.Header.Get("Authorization")`, hard `401` if absent), then validates via `jwtService.ValidateAccessToken`, **then** checks `blacklist.IsRevoked(claims.JTI())` **and** `sessionStore.IsUserRevoked(claims.UserID(), claims.IssuedAt.Time)`. **No query-param path exists.** Token extraction and token validation are currently one inlined block, not two separable steps. |
| Route collision risk between Phase 7 and Phase 8 | ✅ checked — Phase 7 already registered `POST /api/v1/tenants/{tenantId}/events` (**inbound webhook**: external system → FlowForge, resolves wait tokens) and `POST /api/v1/tenants/{tenantId}/webhook-secret/rotate`. Phase 8's endpoint is `GET /api/v1/events` (**outbound stream**: FlowForge → browser, per `api-4.md` §11.2, no `tenantId` in the path — tenant comes from the JWT). Different HTTP methods and different path shapes, so Go's `ServeMux` never confuses them — but the names are close enough that a reader (or an executor skimming quickly) could. Calling this out explicitly so it isn't. |
| `ListLogsFilter`/`ListLogsQuery` exact field names | ✅ re-confirmed (`repository.go:132-138`, `dto.go:57-64`) — used verbatim in §2.3's example, not guessed. |
| `metrics.Metrics`'s existing labeling discipline | ✅ re-confirmed — counters carry `tenant_id`, histograms deliberately do not (Phase 5's B-2 cardinality decision). Any new SSE metric must follow the same split. |

---

## 1. Decisions (D-1…D-7 from v1, D-1 now fully specified; nothing else changed)

| # | Decision | Status |
|---|---|---|
| **D-1** | SSE auth accepts the token via `Authorization` header **or** `?token=` query param — **now fully specified, see §2.2**: `AuthMiddleware` gets a small refactor separating "where is the token" from "is the token valid," so blacklist/session-revocation checks are shared, not duplicated or skipped for the query-param path. | Refined in v2 |
| **D-2** | Redis channel: `events:tenant:{tenantId}`. | Unchanged |
| **D-3** | New `Events eventstream.Publisher` field, distinct from `Publisher executor.EventPublisher`. | Unchanged, reconfirmed still the right call |
| **D-4** | Publishing is best-effort, never blocks or fails a transition (same discipline as `Metrics`, and as the log-writer fix's `appendLog`). | Unchanged |
| **D-5** | `ClientManager` subscribes to a tenant's Redis channel only while ≥1 local client is connected (reference-counted). | Unchanged |
| **D-6** | No persisted replay buffer; `Last-Event-ID` support is best-effort. | Unchanged |
| **D-7** | Event names follow `api-4.md` §11.6's literal spelling (`completed`, `cancelled`), translated from `domain.RunStatus*`/`engine.StepStatus*` via a small mapping table — not unified with the DB's spelling. | Unchanged |
| **D-8** *(new)* | Two new Prometheus metrics, following the existing counter-gets-`tenant_id`/histogram-doesn't split: `SSEConnections *prometheus.GaugeVec` (labels: `tenant_id`) and `EventsPublished *prometheus.CounterVec` (labels: `tenant_id`, `event_type`). | New in v2 |

---

## 2. Architecture

### 2.1 `internal/domain/event.go` — new, pure (unchanged from v1)

```go
type Event struct {
    Type      string          `json:"-"`
    ID        string          `json:"-"`
    TenantID  uuid.UUID       `json:"-"`
    RunID     *uuid.UUID      `json:"runId,omitempty"`
    StepID    *uuid.UUID      `json:"stepId,omitempty"`
    Status    string          `json:"status,omitempty"`
    Timestamp time.Time       `json:"timestamp"`
    Extra     json.RawMessage `json:"-"`
}

const (
    EventRunCreated, EventRunQueued, EventRunStarted             = "workflow.run.created", "workflow.run.queued", "workflow.run.started"
    EventRunCompleted, EventRunFailed                            = "workflow.run.completed", "workflow.run.failed"
    EventRunCancelRequested, EventRunCancelled                   = "workflow.run.cancelRequested", "workflow.run.cancelled"
    EventRunRetryRequested                                       = "workflow.run.retryRequested"
    EventStepStarted, EventStepCompleted, EventStepFailed        = "step.started", "step.completed", "step.failed"
    EventStepRetrying, EventAnalysisCompleted, EventHeartbeat    = "step.retrying", "workflow.analysis.completed", "heartbeat"
)
```

### 2.2 `AuthMiddleware` — the D-1 refactor, specified precisely

Extract token *location* from token *validation* in `internal/auth/middleware.go`, so both paths share every check:

```go
// extractBearerToken reads the credential from either the Authorization header
// or, when absent, a "token" query parameter — needed because browsers' native
// EventSource API cannot set custom request headers, so SSE has no other way
// to authenticate. Every other route continues to use the header only; this
// fallback is consulted solely by the SSE route (§2.4).
func extractBearerToken(r *http.Request, allowQueryParam bool) (string, bool) {
    if authHeader := r.Header.Get("Authorization"); authHeader != "" {
        parts := strings.SplitN(authHeader, " ", 2)
        if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
            return parts[1], true
        }
        return "", false
    }
    if allowQueryParam {
        if t := r.URL.Query().Get("token"); t != "" {
            return t, true
        }
    }
    return "", false
}
```

`Authenticate` (existing, header-only) becomes `AuthenticateWithTokenSource(allowQueryParam bool)`, keeping `Authenticate` as a thin wrapper (`AuthenticateWithTokenSource(false)`) so **every existing route's behavior is byte-identical** — this is a refactor, not a behavior change, for every route except the new one. The blacklist and session-revocation checks (`m.blacklist.IsRevoked`, `m.sessionStore.IsUserRevoked`) move into the shared body untouched — the query-param path gets exactly the same revocation checking the header path always has. A query-string token is never logged: confirm the existing access-log middleware (if any) doesn't log full URLs with query strings verbatim; if it does, redact `token=` the same way `internal/platform/redact` already redacts JSON keys.

### 2.3 `internal/platform/eventstream/` — new package (unchanged from v1)

- `Publisher` interface + `redisPublisher` (`PUBLISH events:tenant:{tenantId} <json>`).
- `ClientManager` — `Register(tenantID) (connID, <-chan domain.Event, unregister func())`, reference-counted Redis subscription per D-5.

### 2.4 Coordinator wiring — `internal/execution/coordinator.go`

`CoordinatorConfig` gains `Events eventstream.Publisher` (nil-safe). One `Events.Publish` line beside each of the 7 **already-located** `appendLog` calls:

| Line (current) | `appendLog` message | Paired `Events.Publish` type |
|---|---|---|
| `:377` | "run parked on wait token" | *(no direct `api-4.md` §11.6 event for "waiting" — see §5 open item)* |
| `:403` | "step succeeded" | `EventStepCompleted` |
| `:435` | "step failed" | `EventStepFailed` |
| `:456` | "step scheduled for retry" | `EventStepRetrying` |
| `:511` | "run stuck... marked failed" | `EventRunFailed` |
| `:530` | "run succeeded" | `EventRunCompleted` |
| `:533` | "run failed" | `EventRunFailed` |

`step.started` (§11.6) and `workflow.run.started` have no existing `appendLog` counterpart — they fire earlier than any of these 7 sites (step claim, run claim), so they're separate additions, not reuses of the log-writer's sites: `workflow.run.started` at the successful `ClaimRun` in `cmd/worker/main.go`'s task handler; `step.started` at `ClaimReadySteps`'s success in `executeWave` (`coordinator.go`, before `runStepWithRetry` is even called). `workflow.run.created`/`.queued` belong to `internal/execution/usecase.go`'s `CreateRun`/`RetryRun`, outside the coordinator entirely (same as v1's finding).

Every `Events.Publish` call: best-effort (D-4), and increments `EventsPublished` (D-8) regardless of success/failure of the publish itself (the metric measures "an event was generated," not "delivery succeeded" — Redis Pub/Sub has no delivery confirmation to measure anyway).

### 2.5 `internal/execution/handler.go` — `GET /api/v1/events`

```go
func (h *ExecutionHandler) StreamEvents(w http.ResponseWriter, r *http.Request) {
    // auth already ran via AuthenticateWithTokenSource(true) — see §2.2, §2.6
    authUser, _ := auth.AuthUserFromContext(r.Context())
    flusher, ok := w.(http.Flusher)
    if !ok { httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "streaming unsupported"); return }
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    connID, events, unregister := h.clients.Register(authUser.TenantID)
    defer unregister()
    if h.metrics != nil { h.metrics.SSEConnections.WithLabelValues(authUser.TenantID.String()).Inc() }
    defer func() { if h.metrics != nil { h.metrics.SSEConnections.WithLabelValues(authUser.TenantID.String()).Dec() } }()

    heartbeat := time.NewTicker(30 * time.Second)
    defer heartbeat.Stop()
    for {
        select {
        case <-r.Context().Done():
            return
        case ev := <-events:
            writeSSE(w, ev); flusher.Flush()
        case <-heartbeat.C:
            writeSSE(w, domain.Event{Type: domain.EventHeartbeat}); flusher.Flush()
        }
    }
}
```

### 2.6 `cmd/api/main.go` wiring — route registration, disambiguated from Phase 7's

```go
mux.Handle("GET /api/v1/events", authMiddleware.AuthenticateWithTokenSource(true)(http.HandlerFunc(executionHandler.StreamEvents)))
```

Placed near, but clearly distinct from, the existing (Phase 7) block:
```go
mux.HandleFunc("POST /api/v1/tenants/{tenantId}/events", executionHandler.IngestEvent) // Phase 7 — inbound webhook, unrelated
```
No `RequireRole` wrapper — any authenticated role may watch, matching every other read-only route.

Construction: `eventstream.NewRedisPublisher(rClient)` + `eventstream.NewClientManager(rClient)` inside the existing `if rClient != nil` block in **both** `cmd/api` (for `ClientManager`, subscribing/relaying to browsers) and `cmd/worker` (for the `Publisher`, passed into `CoordinatorConfig.Events`) — they never share memory, only the Redis channel (D-2), which is the entire reason Pub/Sub was chosen over an in-process channel.

### 2.7 `internal/platform/metrics/metrics.go` — D-8

```go
SSEConnections  *prometheus.GaugeVec   // labels: tenant_id
EventsPublished *prometheus.CounterVec // labels: tenant_id, event_type
```

---

## 3. Boundary Checklist

- [ ] `domain/event.go` stays pure — no `internal/platform` imports.
- [ ] `Events.Publish` failure never fails a step/run transition (D-4) — same test shape as the log-writer fix's §3, second verse.
- [ ] Events never cross tenants (`ClientManager` test, unchanged from v1).
- [ ] SSE disconnect never calls into `execUC` — the handler's only action on `ctx.Done()` is `return`.
- [ ] `AuthenticateWithTokenSource(false)` (every existing route) is **behaviorally identical** to the old `Authenticate` — a regression suite run against Phase 2–7's existing auth tests must pass unchanged after the refactor, not just the new SSE-specific tests.
- [ ] `ClientManager`'s Redis subscription is reference-counted (D-5), proven under `-race` with concurrent register/unregister.
- [ ] The query-param token is never written to any access log verbatim.

---

## 4. TDD Specification

Adds to v1's §4 (still valid): domain event marshaling, `ClientManager` cross-tenant isolation and ref-counted subscribe, coordinator publish-call-site table, SSE handler stream/heartbeat/disconnect tests, cross-process integration test.

**New in v2, from §2.2's refactor:**
- `extractBearerToken`: header present → used, query param ignored even if also present; header absent + `allowQueryParam=false` → not found (existing route behavior); header absent + `allowQueryParam=true` + query param present → used.
- **Full existing `AuthMiddleware` test suite re-run, not just the new cases** — the refactor's whole point is zero behavior change for every route except one; the regression suite is the proof, not a read of the diff.
- A blacklisted/revoked token via the query-param path is rejected identically to one via the header — this is the test that proves D-1 didn't quietly drop a security check to add a convenience one.

---

## 5. Execution Order

1. `internal/domain/event.go`.
2. `internal/auth/middleware.go`: `extractBearerToken` + `AuthenticateWithTokenSource`, `Authenticate` becomes a wrapper. **Full existing auth suite must stay green before proceeding** — this is a refactor of shared, security-critical code and earns its own checkpoint.
3. `internal/platform/eventstream/` — `Publisher`, `ClientManager`, tests.
4. `internal/platform/metrics/metrics.go` — `SSEConnections`, `EventsPublished` (D-8).
5. `internal/execution/coordinator.go` — `CoordinatorConfig.Events`, 7 reused sites + 2 new ones (`step.started`, `workflow.run.started` — see §2.4's note on where the latter actually lives).
6. `internal/execution/usecase.go` — `workflow.run.created`/`.queued` in `CreateRun`/`RetryRun`.
7. `internal/execution/handler.go` — `StreamEvents`.
8. `cmd/api/main.go` + `cmd/worker/main.go` wiring (§2.6).
9. `make ci` and `FLOWFORGE_INTEGRATION=1 make test-integration`, both green.
10. Append a Phase 8 entry to `.agents/memory/action_history.md`.

---

## 6. Definition of Done

- [ ] `GET /api/v1/events` streams tenant-scoped SSE per `api-4.md` §11.5.
- [ ] All 8 run events + 4 step events (§11.6) emitted from correct, verified call sites.
- [ ] Heartbeat every 30s.
- [ ] Events never cross tenants — proven by test.
- [ ] Redis outage degrades to "no live updates," never breaks execution (D-4).
- [ ] Multi-instance routing proven by the cross-process integration test.
- [ ] **The `AuthMiddleware` refactor introduces zero behavior change for every pre-existing route** — proven by the full existing auth suite passing unchanged, not asserted.
- [ ] `make ci` green, `-race` clean.

---

## 7. Carried Forward

1. **`step.waiting` has no `api-4.md` §11.6 event name.** The spec's event list predates Phase 7's `waiting` status. Recommend either extending the spec with a `step.waiting`/`workflow.run.waiting` pair (cheap, consistent) or explicitly deciding "waiting" is silent on the SSE stream (the REST poll endpoints from Phase 6 still show it) — flagged, not silently resolved either way.
2. D-6's no-replay-buffer boundary — unchanged from v1.
3. Rate limiting, CORS, HTTP security headers — still not built, still carried forward.
4. `idempotency_keys` (generic table) — still unused schema debt.
