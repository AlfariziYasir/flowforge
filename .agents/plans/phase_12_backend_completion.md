# Implementation Plan — Backend Completion (Phase 12 + Carried-Forward Debt)

**Planner:** Claude (Architect) · **Executor:** Executor agent
**Source docs:** `backlog.md` §Phase 12 · `api-4.md` §12 (Security) · every prior phase plan's "Carried Forward" section
**Predecessor:** Phase 8 executed, reviewed, remediated, re-verified clean. Backend feature work (Phases 1–8) is functionally complete; this plan closes what was deliberately deferred along the way, before Phase 9 (frontend) starts depending on a moving API surface.

---

## 0. Verification Log — every carried-forward item re-checked against the current tree, not carried by memory

| # | Item | First flagged | Current status |
|---|---|---|---|
| 1 | Rate limiting | Phase 5 note, `api-4.md` §12.11 | ❌ confirmed absent — zero matches for `RateLimit`/`rate.limit` anywhere in `internal/`, `cmd/` |
| 2 | CORS | `api-4.md` §12.10 | ❌ confirmed absent — zero matches for `CORS`/`Access-Control` |
| 3 | HTTP security headers | `api-4.md` §12.9 | ❌ confirmed absent — zero matches for any of the 4 named headers |
| 4 | Audit logging gap in `internal/auth` | `api-4.md` §12.12 | ❌ **confirmed and newly precise**: `api-4.md` names `login`, `user created`, `user updated` as required audited actions. `grep` for `AuditRepository`/`auditRepo`/`domain.AuditEntry` in `internal/auth/*.go` returns **zero results** — none of the three are ever audited, despite the identical infrastructure already working in `internal/workflow` (`ActionWorkflow*`, 6 call sites) and `internal/execution` (`ActionRun*`, 3 call sites, confirmed live at `usecase.go:176,271,344`). |
| 5 | `/metrics` HTTP endpoint | Phase 8 remediation notes | ❌ confirmed absent in both processes — `reg`/`m := metrics.New(reg)` exist in `cmd/api` and `cmd/worker`, but no `promhttp` import, no `/metrics` route anywhere. **`cmd/worker` runs no HTTP server at all today** — confirmed, zero `http.Server`/`ListenAndServe` in `cmd/worker/main.go`. |
| 6 | `idempotency_keys` table | Phase 6 decision (D-3, reuse the `workflow_runs.idempotency_key` column instead) | Still unused — this was a **deliberate** decision, not an oversight. Closing it means removing dead schema, not building a feature. |
| 7 | V-1 — `ContextWithTx`/`TxFromContext` typed to `pgx.Tx` | Flagged Phase 3, repeated through Phase 5 | ❌ confirmed still `pgx.Tx`, never widened to `postgres.DBTX` — carried forward across 3 phases and never done. Estimated ~20 minutes each time it was flagged. |
| 8 | Case A — trigger via queue/gRPC | Assigned Phase 6 in `backlog.md`/design note, missed, re-flagged in Phase 7 as "cheaper now" | ❌ confirmed still absent — `trigger_type` CHECK is still `('manual','webhook','cron')`, no `TriggerTypeQueue`/`Grpc` anywhere. |
| 9 | `step.waiting`/`workflow.run.waiting` SSE event names | Phase 8 v2 | Confirmed still an open spec gap — `api-4.md` §11.6 predates Phase 7's `waiting` status. |
| 10 | Phase 12's own scope: smoke tests | `backlog.md` §Phase 12 | ❌ **confirmed genuinely missing** — `cmd/api` has a `main_test.go` (4 tests, all routing/JWT-validation, zero `HealthChecker` coverage); `cmd/worker` **has no test file at all**. Everything else in Phase 12's scope (unit tests for engine, repository tests, integration tests, E2E workflow test, race-focused tests) is **already satisfied** by the TDD discipline carried through every prior phase — confirmed via existing `*_integration_test.go` files (`coordinator_integration_test.go`, `usecase_integration_test.go`, `queue_integration_test.go`, `grpc_e2e_integration_test.go`) and universal `-race` usage. Phase 12's real remaining scope is narrow. |

**Already resolved, dropped from this plan** (verified, not assumed): "nothing is committed" (multiple commits exist) · "integration testing deferred" (extensively exercised against live Postgres/Redis/NATS since Phase 5) · **PostgreSQL 15+ floor** (found actually documented — `README.md:8`, commit `739bbdb` — earlier carry-forward notes calling this "still open" were themselves stale).

---

## 1. Decisions

| # | Decision | Rationale |
|---|---|---|
| **E-1** | Rate limiting is **Redis-backed** (fixed-window counter via `INCR`+`EXPIRE`), not in-memory. | The project is already designed for multi-instance `cmd/api` (Phase 8's `ClientManager` is the precedent) — an in-memory limiter would let each instance enforce its own separate quota, defeating the point. Redis is already a hard dependency. |
| **E-2** | Exact limits from `api-4.md` §12.11: auth routes 10/min, `POST .../runs` (trigger) 30/min, everything else read-only 120/min — **keyed by tenant ID where authenticated, by IP for the pre-auth `/auth/login` route** (no tenant is known yet at that point). | Matches the spec literally rather than inventing different numbers; IP-keying for login is the only route where tenant isn't yet resolvable. |
| **E-3** | CORS origin allowlist is config-driven (`CORS_ALLOWED_ORIGINS`, comma-separated, default empty = same-origin only), credentials disabled unless `CORS_ALLOW_CREDENTIALS=true` is explicitly set. | Matches §12.10 literally ("must be configurable," "disabled by default unless explicitly required"). An empty default means this ships inert until the frontend's actual origin is known — deliberately, since guessing it now would need revisiting anyway once Phase 9 exists. |
| **E-4** | Security headers are a single small middleware wrapping the whole `mux`, not per-route. | All 4 headers (`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Content-Security-Policy`) are response-shape concerns with no route-specific variation needed at this project's stage. |
| **E-5** | Auth audit logging reuses the exact `domain.AuditRepository` + action-constant pattern already proven in `internal/workflow`/`internal/execution` — **no new abstraction**. `Login`'s audit call is **not** wrapped in a transaction (matches `AnalyzeRun`'s precedent — no preceding DB write needs atomicity with it); `CreateUser`/`UpdateUser` **are** wrapped, since `UserUseCase` already has a `txRunner` and other mutations in that file already use it. | Consistency over cleverness — this is the third package to add audit logging, and it should look exactly like the first two. |
| **E-6** | `/metrics` is exposed in **both** processes. `cmd/worker` gets a new, minimal `http.Server` (new config `METRICS_PORT`, default `9091`) whose only route is `/metrics` — not a general-purpose HTTP surface for the worker. | The worker's own run/step/retry counters (`RunsCreated`, `Steps`, `StepDuration`, etc.) are the more operationally important half of this project's metrics and currently have no exposition path at all. |
| **E-7** | `idempotency_keys` table is **dropped** via a new migration, not just left alone. | Consistent with how every migration in this project has behaved under the "no production data yet" rule — dead schema left in place invites a future reader to wonder whether it's actually used (as this planning pass's own confusion in Phase 6 demonstrated) or to write to it thinking it's live. Removing it is the more honest state. |
| **E-8** | V-1 (`ContextWithTx`/`TxFromContext` → `DBTX`) is done exactly as originally scoped — a type widening, nothing more. | Three phases of "still open, ~20 minutes" is long enough; closing it before Phase 9 removes one more thing a frontend-adjacent reader has to discover is stale. |
| **E-9** | Case A folds into this plan as originally recommended in Phase 7 — reusing the gRPC/NATS ingress plumbing already built, new handler only. | Matches Phase 7's own carried-forward recommendation; doing it now (rather than a dedicated phase) is cheaper, per that note. |
| **E-10** | `step.waiting`/`workflow.run.waiting` — **extend** the event vocabulary rather than leave `waiting` silent on SSE. | Cheap (two more constants + two more `Events.Publish` call sites, both already-touched files from Phase 8), and leaving a real state silently unreported on the "live" stream is a worse asymmetry than adding two names. |

---

## 2. Architecture

### 2.1 `internal/platform/ratelimit/` — new package (E-1, E-2)

```go
// Limiter enforces a fixed-window request quota per key, backed by Redis.
type Limiter struct{ rdb *redis.Client }
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
```
`Allow` does `INCR key; if result == 1 { EXPIRE key, window }; return result <= limit`. Middleware factory:
```go
func Middleware(l *Limiter, limit int, window time.Duration, keyFunc func(*http.Request) string) func(http.Handler) http.Handler
```
`keyFunc` for authenticated routes reads `auth.AuthUserFromContext(r.Context()).TenantID` (applied **after** `Authenticate`, so it wraps `RequireRole(...)`, not the other way — the tenant must be known first); for `/auth/login`, `keyFunc` reads the client IP (respecting `cfg.TrustProxyHeaders`, the same flag `AuthHandler` already uses for X-Forwarded-For). A Redis error **fails open** (allows the request, logs a warning) — matching this project's established "observability must never break execution" discipline (D-4 from Phase 8, `Metrics`'s nil-safety from Phase 5) applied to a new kind of side channel.

### 2.2 `internal/platform/httpmw/` — new package, CORS + security headers (E-3, E-4)

```go
func CORS(allowedOrigins []string, allowCredentials bool) func(http.Handler) http.Handler
func SecurityHeaders() func(http.Handler) http.Handler
```
`SecurityHeaders` sets `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`, `Content-Security-Policy: default-src 'none'` (this is a JSON API — no script/style/img sources need whitelisting; tightest reasonable default, revisited if a frontend build ever needs to serve HTML from the same origin, which Phase 9's plan should confirm or deny explicitly).

### 2.3 `internal/auth` — audit logging (E-5)

`NewAuthUseCaseWithLogger` gains `auditRepo domain.AuditRepository`; `Login`'s success path gets one `auditRepo.Record(ctx, domain.AuditEntry{Action: ActionUserLoggedIn, EntityType: "user", Metadata: {ipAddress, userAgent}})` call (new `internal/auth/actions.go`, mirroring `internal/workflow/actions.go`'s exact shape). `NewUserUseCaseWithTx` gains the same param; `CreateUser`/`UpdateUser` wrap their existing DB write and the new audit call in the `txRunner.ExecuteInTx` they already have available.

### 2.4 `internal/platform/audit/audit.go` — V-1's neighbor gets its own fix (E-8)

`internal/platform/postgres/dbtx.go`: `ContextWithTx(ctx, tx pgx.Tx)` → `ContextWithTx(ctx, tx postgres.DBTX)`; `TxFromContext(ctx) (pgx.Tx, bool)` → `(DBTX, bool)`. `GetDBTX` already returns `DBTX` — this closes the last place `pgx.Tx` leaked out narrower than necessary. Every call site (all inside `postgres.UnitOfWork`'s own `ExecuteInTx`) needs no change beyond the type — `pgx.Tx` already satisfies `DBTX`'s `Exec`/`Query`/`QueryRow` trio.

### 2.5 `/metrics` — both processes (E-6)

`cmd/api/main.go`: `mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))` — no auth (Prometheus scrapers don't carry tenant JWTs; if this needs protecting later, network-level restriction is the standard answer, not application auth).

`cmd/worker/main.go`: new minimal server —
```go
metricsMux := http.NewServeMux()
metricsMux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
metricsSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.MetricsPort), Handler: metricsMux}
go func() { _ = metricsSrv.ListenAndServe() }()
```
Shut down alongside the worker's existing signal handler, same `WaitGroup` discipline as `reaperLoop`.

### 2.6 Migration `000007_drop_idempotency_keys_table` (E-7)

```sql
DROP TABLE IF EXISTS idempotency_keys;
```
`.down.sql` recreates it verbatim from `000001`'s original definition — a genuine reversal, not a no-op, since this is the one migration in this plan that removes something rather than adding.

### 2.7 Case A — queue/gRPC trigger (E-9)

New migration `000008_trigger_type_queue_grpc`: widen `workflow_runs.trigger_type` CHECK to add `'queue'`, `'grpc'`. New handler on the **already-registered** gRPC service and NATS subject from Phase 7 (`internal/platform/eventbus/`) — a second RPC method / message type that calls `execUC.CreateRun` (Phase 6, already exists) instead of `execUC.HandleEvent` (Phase 7). Auth: the same `webhookauth`/HMAC interceptor already wired for both transports.

### 2.8 SSE event vocabulary (E-10)

`internal/domain/event.go`: `EventStepWaiting = "step.waiting"`, `EventRunWaiting = "workflow.run.waiting"`. `internal/execution/coordinator.go`: the wait-branch site (already identified in Phase 8's plan, `coordinator.go:377`'s `appendLog` neighbor) gets its `Events.Publish` call using these two — the one site Phase 8 v2's own table (§2.4) explicitly left blank pending this decision.

### 2.9 Smoke tests (E-10... continuing the numbering would collide; this is Phase 12's own scope, not a lettered decision)

`cmd/api/main_test.go`: `TestHealthChecker_AllUp`, `TestHealthChecker_DBDown`, `TestHealthChecker_RedisDown`, `TestHealthChecker_BothDown` — table-driven over `HealthChecker`'s existing `Pinger` interface (already designed for exactly this — fake pingers, no real DB/Redis needed for these). New `cmd/worker/main_test.go`: `TestHostname_NeverEmpty` (the existing `hostname()` helper's fallback path), `TestIsProductionLike` (already exists as a bare function, currently untested), a construction smoke test proving `CoordinatorConfig` built from a `config.Config` with sane defaults doesn't panic.

---

## 3. Boundary Checklist

- [ ] Rate limiter fails open on Redis error (E-1) — proven by a test with an unreachable Redis, asserting the request still succeeds.
- [ ] CORS never reflects `Access-Control-Allow-Origin: *` when credentials are enabled (a real, common misconfiguration this checklist item exists specifically to catch).
- [ ] Security headers present on every response, including error responses (4xx/5xx) — not just success paths.
- [ ] New auth audit calls follow the exact `EntityType` discipline from the Phase 6 audit-relocation fix (explicit, never defaulted).
- [ ] `/metrics` exposes no per-tenant cardinality explosion — re-confirms Phase 5's B-2 decision is still honored (histograms un-labeled by tenant) now that the endpoint actually serves scrapeable data instead of sitting inert.
- [ ] `idempotency_keys` drop migration's `.down.sql` is a genuine reversal, tested both directions.
- [ ] Case A's new run-creation path enforces the same tenant/auth checks as the HTTP trigger route — not a shortcut around `RequireRole`.

---

## 4. TDD Specification (high-level; full table-driven detail follows this project's established per-package convention)

- `ratelimit`: allow under limit, deny over limit, window reset after expiry, fail-open on Redis error, IP-vs-tenant keying.
- `httpmw`: CORS allows a listed origin, denies an unlisted one, credentials header only when configured; security headers present on both 200 and error responses.
- `internal/auth`: login success audits with correct `EntityType`/`Action`; `CreateUser`/`UpdateUser` audit inside the existing transaction (a `recordingTxRunner`-style assertion, matching every prior audit-adjacent test in this project).
- `cmd/api`, `cmd/worker`: the smoke tests from §2.9.
- Integration (`FLOWFORGE_INTEGRATION=1`): `/metrics` in both processes returns real Prometheus text format after some activity; rate limiter's Redis-backed counting survives across two separate `*Limiter` instances (simulating two `cmd/api` replicas) sharing one Redis key.

---

## 5. Execution Order

Ten independent workstreams; sequence chosen to front-load the riskiest/shared-code item (V-1) and end with the cheapest/most isolated ones.

1. **V-1** (`dbtx.go` type widening) — touches shared infrastructure every other package depends on; land and verify `make ci` first, alone.
2. **Migration `000007`** (drop `idempotency_keys`) + **`000008`** (trigger_type widening) — both migrations, both directions tested against live Postgres.
3. `internal/platform/httpmw/` (CORS + security headers) + wiring.
4. `internal/platform/ratelimit/` + wiring.
5. `internal/auth` audit logging (E-5).
6. `/metrics` in both processes (E-6).
7. SSE `waiting` event names (E-10, §2.8).
8. Case A queue/gRPC trigger (E-9, §2.7) — largest single item, done after the smaller ones are settled.
9. Smoke tests (§2.9) — can run any time after step 1, placed last only because it depends on nothing and blocks nothing.
10. `make ci` and `FLOWFORGE_INTEGRATION=1 make test-integration`, both green; append to `.agents/memory/action_history.md`.

---

## 6. Definition of Done

- [ ] Every item in §0's table is either closed or has an explicit, deliberate reason it remains open (D-4's eventbus placeholder contract is the one item in this project genuinely blocked on external information — carried forward, not closeable by this plan).
- [ ] `login`/`user created`/`user updated` produce audit rows, closing `api-4.md` §12.12's gap.
- [ ] `/metrics` serves real data in both processes.
- [ ] Rate limiting and CORS match `api-4.md` §12.10/§12.11 exactly, both configurable, both fail safely.
- [ ] `idempotency_keys` no longer exists; `ContextWithTx`/`TxFromContext` speak `DBTX`.
- [ ] Case A closes the last item from Phase 6's original scope.
- [ ] `cmd/api` and `cmd/worker` both have real smoke-test coverage.
- [ ] `make ci` green, `-race` clean, full integration suite green against live Postgres/Redis/NATS.

## 7. Carried Forward (genuinely irreducible — not this plan's job)

1. **D-4's eventbus placeholder contract** (Phase 7) — still pending the real external system's actual proto/NATS spec. Cannot be closed without information only that system's owners have.
2. **Frontend-dependent CORS origin value** — the allowlist mechanism ships in this plan; the actual origin string is Phase 9's to supply.
