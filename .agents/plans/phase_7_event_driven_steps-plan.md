# Implementation Plan — Phase 7: Event-Driven Steps (**v2 — multi-protocol**)

**Planner:** Claude (Architect) · **Executor:** Executor agent
**Source docs:** `backlog.md` §Phase 7 · [design_event_driven_steps.md](design_event_driven_steps.md) (case B) · `AGENTS.md`
**Predecessor:** Phase 6 executed and reviewed **APPROVED** — `internal/execution` and `internal/platform/{ai,redact,audit,queue,safehttp,metrics}` all in tree, `make ci` green.

> **v2 changelog.** v1 scoped ingress to HTTP webhook only. The user confirmed a specific external system requires **both gRPC and a message broker (NATS/JetStream), in both directions** (it calls FlowForge, and FlowForge calls it). v2 keeps v1's core mechanism (wait tokens, coordinator changes, sweeper — all protocol-agnostic) unchanged and adds gRPC + NATS as ingress **and** egress adapters, all converging on the same ports v1 already established. HTTP webhook is kept as the baseline/manual-test path, not dropped.

---

## 0. Verification Log (v2 additions on top of v1's, which still hold)

| Claim | Result |
|---|---|
| `EVENT_PUBLISH` currently delivers to a real external destination | ❌ **It does not — it is fully inert today.** `queue.Client.Publish` enqueues an Asynq task `"workflow:event:"+eventType`, but nothing consumes it. |
| `google.golang.org/grpc` available | ✅ `v1.83.0` |
| `github.com/nats-io/nats.go` available | ✅ `v1.52.0` |
| Process shape | `cmd/api` + `cmd/worker` |

---

## 1. Architectural Decisions

| # | Decision | Rationale |
|---|---|---|
| **D-1** | **Three Ingress Adapters** (HTTP webhook, gRPC server, NATS consumer) all converging on `execution.HandleEvent`. | Single business entry point ensures zero duplication of wait-token logic. |
| **D-2** | **Multi-Protocol Egress Router** for `EVENT_PUBLISH` (`internal`, `grpc`, `nats`). | Extensible event emission per step configuration. |
| **D-3** | **Process Co-location**: gRPC listener in `cmd/api`, NATS consumer in `cmd/worker`. | Avoids process sprawl while maintaining clear concurrency lifecycle. |
| **D-4** | **Centralized Auth & Verification**: `webhookauth.VerifyHMAC` shared across all transports. | Guarantees timing-safe HMAC checks. |

---

## 2. Multi-Protocol Architecture & Ports

1. **Domain & Sentinels** (`internal/domain`): `RunStatusWaiting`, `StepWaitToken`, `OrphanEvent`, `ErrTokenNotFound`, `ErrTokenExpired`, `ErrTokenAlreadyConsumed`.
2. **Ingress Layer** (`internal/execution`, `internal/platform/webhookauth`, `internal/platform/eventbus`):
   - HTTP: `POST /api/v1/tenants/{tenantId}/events`
   - gRPC: `DeliverEvent` RPC service with `x-signature` interceptor.
   - NATS: Durable JetStream pull subscriber validating headers and calling `HandleEvent`.
3. **Egress Router Layer** (`internal/platform/eventbus/router.go`):
   - Internal Asynq Queue, gRPC Client, NATS Publisher.
