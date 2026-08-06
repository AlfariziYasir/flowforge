# Implementation Plan — Phase 7: Event-Driven Steps (**v2 — multi-protocol**)

**Planner:** Claude (Architect) · **Executor:** Executor agent
**Source docs:** `backlog.md` §Phase 7 · [design_event_driven_steps.md](design_event_driven_steps.md) (case B) · `AGENTS.md`
**Predecessor:** Phase 6 executed and reviewed **APPROVED** — `internal/execution` and `internal/platform/{ai,redact,audit,queue,safehttp,metrics}` all in tree, `make ci` green.

> **v2 changelog.** v1 scoped ingress to HTTP webhook only. The user confirmed a specific external system requires **both gRPC and a message broker (NATS/JetStream), in both directions** (it calls FlowForge, and FlowForge calls it). v2 keeps v1's core mechanism (wait tokens, coordinator changes, sweeper — all protocol-agnostic) unchanged and adds gRPC + NATS as ingress **and** egress adapters, all converging on the same ports v1 already established. HTTP webhook is kept as the baseline/manual-test path, not dropped.

---

## 0. Verification Log (v2 additions on top of v1's, which still hold)

| Claim | Result |
|---|---|
| `EVENT_PUBLISH` currently delivers to a real external destination | ❌ **It does not — it is fully inert today.** `queue.Client.Publish` (`asynq.go:73-86`) enqueues an Asynq task `"workflow:event:"+eventType`, but **nothing consumes it** — `NewServer` (`asynq.go:92`) registers a handler only for the run-wake task type. Confirmed by grep: zero handler registration for `"workflow:event:"` anywhere. The doc comment at `asynq.go:74` already says *"Phase 7 introduces the listener that consumes it"* — this was a known, intentional stub. |
| `google.golang.org/grpc` available | ✅ `v1.83.0` (latest stable) |
| `github.com/nats-io/nats.go` available | ✅ `v1.52.0` (JetStream support included) |
| Existing process shape | `cmd/api` (HTTP surface) + `cmd/worker` (background execution) — exactly two. No precedent for a third. |
| A gRPC server can share a process with an existing `http.Server` | ✅ standard Go pattern — a second `net.Listener` on a second port, same binary, same DB pool, same use-case instances. No framework conflict. |

---

## 1. Decisions

| # | Decision | Rationale |
|---|---|---|
| **D-1** | **Three ingress adapters**, all calling the same `execution.HandleEvent(ctx, tenantID, correlationKey, payload) (resolved bool, err error)` port unchanged from v1: HTTP webhook (baseline), gRPC server, NATS/JetStream consumer. | `HandleEvent` was already designed as the single business-logic entry point in v1, precisely so additional transports are adapters, not redesigns. This is that design being exercised for real. |
| **D-2** | **Two egress adapters** complete `EVENT_PUBLISH`'s stub: a gRPC client and a NATS publisher, selected **per node** via a new `transport` config field. The existing internal-queue behavior stays as the default when `transport` is unset — nothing that (theoretically) depends on today's stub behavior breaks, though nothing currently does since it's unconsumed. | `EVENT_PUBLISH`'s config already carries `eventType`/`payload`/`correlationKey`; adding `transport`+`target` is additive, not a breaking change to the node's shape. |
| **D-3** | **No third process.** The gRPC server is a second listener inside `cmd/api` (shares `execUC`, `dbPool`, config already built there). The NATS consumer is a second background loop inside `cmd/worker` (shares the shape of `reaperLoop` — long-running, ticker/subscription-driven, not request-driven). | The project has run on exactly two processes since Phase 1. A third (`cmd/grpc`) would mean a new Dockerfile stage, a new `docker-compose` service, new health checks, new deploy surface — for two listeners that fit naturally into the processes that already have everything they need. Flagged as a deliberate simplification, not an oversight. |
| **D-4** | **The exact wire contract (proto messages, NATS subject naming, JetStream stream config) is provisional**, built generic and documented, pending the real external system's actual integration spec. | The user confirmed a specific system needs this, but its exact message schema, auth expectations, and RPC/subject naming weren't provided — and shouldn't be guessed. §4.4/4.5 below define a sensible, working generic contract now; **adapting field names/auth details to match the real system once its spec is available is a small, isolated change to `internal/platform/eventbus/` only** — it does not reopen the wait-token mechanism, the coordinator, or the migration. |
| *(D-1…D-6 from v1, unchanged)* | HTTP webhook baseline, interpolated correlation key, HMAC secret, per-tenant cap, `MaxWaitDuration`, sweeper on the existing reaper loop. | See v1 rationale, still holds — none of it is protocol-specific. |

---

## 2. What Stays Exactly as v1 Designed It (protocol-agnostic core)

Unchanged from v1 — re-verify against the actual code at execution time, don't re-derive:

- Migration `000005`: `waiting` on `workflow_runs.status`, `EVENT_WAIT` on `workflow_nodes.node_type`, `step_wait_tokens`, `orphan_events`, `tenants.webhook_secret`.
- `domain.RunStatusWaiting`; `StepWaitToken`, `OrphanEvent` structs.
- `coordinator.go`: `canTransitionRun` gains `running→waiting`/`waiting→pending`; `runStepWithRetry` gains the `Output.Waiting` branch, moving **both** step and run to `waiting` (§4.3 of v1 — the "why both" reasoning about `ReclaimExpiredLeases` churn still applies verbatim).
- `executor/event_wait.go`: interpolate `correlationKey`, enforce per-tenant cap, create the token, return `Output{Waiting: true}`.
- Sweeper: expired-token timeout (step→failed, run→pending, reused error path) and orphan-event retry-then-dead-letter, both riding the existing `reaperLoop` ticker.
- No outbox, no generic inbox table — the reasoning in v1 §2 (token's own `consumed_at` as the dedup record; wake-side reliability via the existing `ReclaimStalePendingRuns`) is unaffected by which transport delivered the event.

**What changes: `HandleEvent`'s callers (multiplied to three) and `EventPublisher`'s implementations (multiplied to three, one of which — internal queue — already exists and stays default).**

---

## 3. Architecture — the multi-protocol layer

### 3.1 `internal/platform/webhookauth/` — new, shared by all three ingress adapters

```go
// VerifyHMAC checks sig against HMAC-SHA256(secret, body) in constant time.
func VerifyHMAC(secret string, body []byte, sig string) bool
```
One function, used by the HTTP handler (signature header), the gRPC interceptor (signature in metadata), and the NATS consumer (signature in message headers) — so the security-critical comparison logic exists in exactly one place, tested once, not reimplemented per transport.

### 3.2 Ingress — HTTP webhook (unchanged from v1)
`POST /api/v1/tenants/{tenantId}/events`, `X-Signature` header, `webhookauth.VerifyHMAC`, delegates to `execUC.HandleEvent`. Always `200` regardless of match/no-match/duplicate (v1 §4.4's reasoning: never invite sender-side retry guessing).

### 3.3 Ingress — gRPC server, inside `cmd/api`

New `proto/events.proto` (generic contract — see D-4):
```protobuf
syntax = "proto3";
package flowforge.events.v1;
option go_package = "flowforge/internal/platform/eventbus/eventspb";

service EventListener {
  rpc DeliverEvent(DeliverEventRequest) returns (DeliverEventResponse);
}

message DeliverEventRequest {
  string correlation_key = 1;
  bytes payload_json = 2;
}

message DeliverEventResponse {
  bool accepted = 1; // mirrors the HTTP path: always true, matching/no-match is internal
}
```
Auth via a unary interceptor reading `x-tenant-id` + `x-signature` from gRPC metadata, calling the same `webhookauth.VerifyHMAC`. Codegen: `protoc`/`buf` — new `Makefile` target `proto-gen`, output checked into `internal/platform/eventbus/eventspb/` (generated code committed, matching how `mocks/` are already committed rather than generated in CI).

`cmd/api/main.go` gains a second listener: `grpcServer := grpc.NewServer(grpc.UnaryInterceptor(authInterceptor)); eventspb.RegisterEventListenerServer(grpcServer, &eventListenerServer{execUC: execUC})`, served on a new configurable port (`GRPC_PORT`, default `9090`) via its own `net.Listener` in a goroutine, shut down alongside the HTTP server on the same signal handler.

### 3.4 Ingress — NATS/JetStream consumer, inside `cmd/worker`

New `internal/platform/eventbus/nats.go`:
```go
// Subscribe starts a durable JetStream pull consumer on subject and calls
// handle for each message, acking only after handle returns nil — so a crash
// between receipt and HandleEvent's commit redelivers rather than loses the
// event. Combined with HandleEvent's own idempotent token-consume, a redelivery
// after a successful-but-unacked message is a safe no-op, not a duplicate side effect.
func Subscribe(ctx context.Context, nc *nats.Conn, subject, durableName string, handle func(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) error) error
```
Message headers carry `Tenant-Id` and `Signature` (NATS supports headers natively), verified via the same `webhookauth.VerifyHMAC`. Subject convention: `flowforge.events.{tenantId}` (one durable consumer, tenant read from the header rather than the subject, keeping subject cardinality flat) — **placeholder pending the real system's actual subject naming, per D-4**.

`docker-compose.yml` gains a `nats` service (`nats:2-alpine`, `-js` flag for JetStream) and a healthcheck, mirroring the existing `postgres`/`redis` service shape. `cmd/worker/main.go` provisions the stream idempotently on startup (`js.AddStream` with `ErrStreamNameAlreadyInUse` tolerated) — no separate migration mechanism needed; NATS streams don't carry the same forward/backward migration concern SQL schemas do.

### 3.5 Egress — completing `EVENT_PUBLISH`

`executor/event_publish.go`'s config gains two fields:
```go
type eventPublishConfig struct {
    EventType      string `json:"eventType"`
    Payload        string `json:"payload"`
    CorrelationKey string `json:"correlationKey"`
    Transport      string `json:"transport"` // "" (internal queue, current behavior) | "grpc" | "nats"
    Target         string `json:"target"`    // grpc: "host:port"; nats: subject; ignored when transport == ""
}
```
New `internal/platform/eventbus/router.go`:
```go
// Router dispatches to the transport a node's config selects, defaulting to
// the existing internal-queue publisher when Transport is unset — v1's
// EVENT_PUBLISH behavior is preserved exactly for any node that doesn't opt in.
type Router struct {
    Internal executor.EventPublisher // today's queue.Client, unchanged
    GRPC     executor.EventPublisher // new
    NATS     executor.EventPublisher // new
}
func (r Router) Publish(ctx context.Context, eventType, target, transport string, payload []byte) error
```
`GRPCPublisher.Publish` dials `target` (short-lived connection per call, or a small connection-pool keyed by target if profiling shows dial overhead matters — start simple), calls the same `DeliverEvent` RPC the ingress side implements — symmetric by construction, so two FlowForge-shaped endpoints interoperate for free; a genuinely third-party gRPC service will need its contract matched here once known (D-4). `NATSPublisher.Publish` does `js.Publish(target, payload, nats.MsgId(...))` for JetStream's own dedup-on-republish as a bonus (free idempotency at the transport layer, on top of `HandleEvent`'s own).

### 3.6 Config additions
```
GRPC_PORT              default "9090"
NATS_URL               default "nats://localhost:4222"
NATS_STREAM            default "FLOWFORGE_EVENTS"
```

---

## 4. Boundary Checklist (adds to v1's, all still hold)

- [ ] `webhookauth.VerifyHMAC` is the **only** place signature comparison logic exists — no transport reimplements it.
- [ ] All three ingress adapters call `HandleEvent` with identical semantics — no transport-specific business logic leaks past the adapter boundary (the port stays transport-blind, matching every other use-case boundary in this codebase since Phase 2).
- [ ] `Router.Publish`'s default (`transport == ""`) is byte-identical to today's `EVENT_PUBLISH` behavior — a node written before Phase 7 continues to compile and run unchanged.
- [ ] NATS message handling acks only after `HandleEvent` succeeds — never ack-then-process.
- [ ] gRPC and NATS credentials/targets are never logged verbatim (reuse `internal/platform/redact` conventions where payloads are logged).
- [ ] No new goroutine is unowned — the gRPC listener and NATS subscription both shut down on the existing signal handlers in `cmd/api`/`cmd/worker`, mirroring `reaperLoop`'s `WaitGroup` discipline (Phase 5).

---

## 5. TDD Specification (adds to v1's §6, unchanged there)

- **`webhookauth_test.go`**: valid signature accepted; wrong secret rejected; tampered body rejected; constant-time comparison (no early-exit timing leak — use `hmac.Equal`, not `==`, and assert the function uses it).
- **gRPC interceptor**: missing/invalid `x-signature` metadata → `Unauthenticated` before `DeliverEvent` is invoked (mirrors v1's HTTP-path assertion that `ResolveToken` is never reached on bad auth).
- **gRPC server integration** (`bufconn`, in-process — no real network needed for the test): a valid `DeliverEvent` call resolves a matching token identically to the HTTP path resolving the same correlation key — same usecase, same outcome, different adapter, proven side by side.
- **NATS consumer** (integration, real NATS via `docker compose`, `FLOWFORGE_INTEGRATION=1`): a published message resolves the matching token; redelivery of the same message (simulated by not acking, or by JetStream's own redelivery on a short ack-wait) is a safe no-op, not a duplicate side effect.
- **`Router.Publish`**: `transport==""` calls `Internal` (regression-proves v1's stub behavior is preserved); `transport=="grpc"` calls `GRPC` with the configured `target`; `transport=="nats"` calls `NATS`; an unknown transport value is a step-level error (existing failure path), not a silent internal-queue fallback — silently falling back would hide a config typo pointing at a real integration.
- **End-to-end** (integration): `EVENT_PUBLISH` with `transport: grpc` targeting the new gRPC server's own address resolves a real `EVENT_WAIT` elsewhere in the same graph — proves the two halves (egress client, ingress server) are wire-compatible with each other by construction, even before the real external system's exact contract is known.

---

## 6. Execution Order

1. **Part A — core mechanism** (protocol-agnostic): migration `000005`, `domain` additions, `coordinator.go` diff, `event_wait.go` executor, sweeper extensions. **Fully testable and mergeable on its own** — nothing here depends on gRPC or NATS.
2. **Part B — HTTP webhook** (baseline ingress): `webhookauth` package, `POST /api/v1/tenants/{tenantId}/events`, rotate-secret endpoint. Proves the end-to-end wait→resolve flow works before adding two more transports on top.
3. **Part C — gRPC**: `.proto` + codegen target, server (ingress, in `cmd/api`) + client (`GRPCPublisher`, egress), interceptor reusing `webhookauth`.
4. **Part D — NATS/JetStream**: `docker-compose` service, subscriber (ingress, in `cmd/worker`) + publisher (`NATSPublisher`, egress), stream provisioning.
5. **Part E — `Router`**: wires `Internal`/`GRPC`/`NATS` behind `EVENT_PUBLISH`'s `transport` config field; `cmd/api`/`cmd/worker` wiring updated to construct all three and pass the `Router` in place of the bare `queueClient` currently passed as `Publisher`.
6. `make ci` and `FLOWFORGE_INTEGRATION=1 make test-integration` (now also exercising a real NATS container — `docker-compose.yml` update needed before this step, not after).
7. Append a Phase 7 entry to `.agents/memory/action_history.md`, explicitly noting D-4's placeholder-contract status so it isn't mistaken for a finished integration.

**Parts B/C/D are independent of each other after Part A lands** — buildable and reviewable in any order, or in parallel by different executors, since none of them touch the same files.

---

## 7. Carried Forward

1. **Case A (trigger via queue/gRPC)** — v1's §8 finding still stands: assigned to Phase 6, never built. **Now cheaper than v1 estimated**: Part C's gRPC server and Part D's NATS consumer are exactly the ingress plumbing a queue/gRPC *trigger* would also need (auth interceptor, message parsing) — only the handler differs (create a new run vs. resolve a wait token). Recommend folding it in as a thin addition to Parts C/D rather than a separate phase.
2. **D-4's placeholder contract** is the single biggest source of rework risk in this plan. The moment the real external system's actual proto/NATS spec is available, `internal/platform/eventbus/` is the only place that needs to change — flagged prominently so that's a five-minute confirmation, not a rediscovery.
3. Rate limiting, CORS, HTTP security headers, `idempotency_keys` table, PostgreSQL 15+ floor documentation — unchanged from v1, still not this phase's job.
