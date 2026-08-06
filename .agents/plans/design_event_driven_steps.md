# Design Note — Event-Driven Steps (Message Queue & gRPC)

**Status:** Design note · **Created:** 2026-08-04
**Context:** Outcome of the discussion while revising the Phase 4 plan. No code has been written for this yet.

---

## 1. Decisions

| # | Decision |
|---|---|
| **E-1** | "Event node" means three different things. This note covers **B — waiting for an event mid-workflow**. |
| **E-2** | A run must be able to "sleep". A worker **must never** hold a goroutine while waiting for an event. |
| **E-3** | Correlating an event to a run uses a tenant-scoped *wait token* with `UNIQUE (tenant_id, correlation_key)`. |
| **E-4** | **Outbox** guarantees the message is published; **Inbox** guarantees it is not processed twice. Both must share a transaction with the business state change. |
| **E-5** | The "orphan event" trap **cannot** be solved at the broker layer. It is an application concern. |
| **E-6** | Phase 4 prepares only **two small things**: the `waiting` status and its readiness rule. Everything else belongs to later phases. |

> [!NOTE]
> **Placement is settled** (decision 2026-08-04, this product will be used for a real case):
> **case C → Phase 5** · **case A → Phase 6** · **case B → Phase 7 (new phase)**.
> `backlog.md` has been updated; the old phases 7–12 shifted to 8–13. See §7.
>
> Case B sits deliberately **before** Phase 12 (*Testing and Reliability*) — placed after it, the part most in need of reliability testing would never meet the phase designed to test it.

---

## 2. Three Meanings of "Event Node"

Worth separating, because the costs differ enormously.

| | Meaning | Example | Cost |
|---|---|---|---|
| **A** | Event as a **trigger** — the workflow *starts* when a message arrives | Message on Kafka → workflow runs | 🟢 Light |
| **B** | Event as a **waiting node** — the workflow *pauses* for a message | Create order → **wait for payment** → send email | 🔴 Heavy |
| **C** | Event as a **publisher** — the workflow *emits* a message | Processing done → publish to Kafka | 🟢 Light |

**A is already half-built.** The schema carries the concept:

```sql
-- workflow_runs
trigger_type VARCHAR(50) NOT NULL DEFAULT 'manual'
    CHECK (trigger_type IN ('manual', 'webhook', 'cron'))
```

Adding `'queue'` / `'grpc'` needs one migration plus a listener whose only job is to **create a new run**. The engine does not change at all.

**C mirrors the existing `HTTP` node** — same shape, different destination. Node type `EVENT_PUBLISH` plus an executor in Phase 5.

**The rest of this note covers B.**

---

## 3. Core Principle: A Run Must Be Able to Sleep

Forbidden:

```go
// WRONG — one waiting workflow means one dead worker
msg := <-queue.Subscribe(topic)
```

If the event arrives three hours later, a goroutine is pinned for three hours. 100 waiting workflows exhausts the pool.

Correct: the node releases its run to the database and the worker moves on. The run is woken again when the event arrives.

### The flow

**Stage 1 — a worker executes an `EVENT_WAIT` node:**

1. Derive `correlation_key` from scope, e.g. `{{trigger.orderId}}` → `"ORD-123"`
2. Persist the wait token **and** the outbox message — **one transaction** (§5)
3. Set `step_runs.status = 'waiting'`, `workflow_runs.status = 'waiting'`
4. Worker finishes and releases the run

**Stage 2 — the event arrives (3 seconds or 3 hours later):**

A separate listener (queue consumer / gRPC handler):

1. Receive the message, read its `correlation_key`
2. **One transaction**: record to inbox → write `output_payload` → step becomes `succeeded` → token `consumed_at`
3. Re-enqueue the run → any worker continues it

**Stage 3 — the event never arrives:**

A sweeper (cron, every minute): find tokens past `expires_at` → mark the step `failed` → wake the run onto its error path.

---

## 4. Required Schema

```sql
-- (1) New statuses
--     step_runs.status         → add 'waiting'
--     workflow_runs.status     → add 'waiting'
--     workflow_nodes.node_type → add 'EVENT_WAIT', 'EVENT_PUBLISH'
--     NOTE: all three carry CHECK constraints, so each needs a migration.

-- (2) Correlating an event to the waiting run
CREATE TABLE step_wait_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_run_id UUID NOT NULL,
    step_run_id     UUID NOT NULL,
    correlation_key VARCHAR(255) NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_wait_token UNIQUE (tenant_id, correlation_key),
    CONSTRAINT fk_wait_token_tenant_run FOREIGN KEY (tenant_id, workflow_run_id)
        REFERENCES workflow_runs(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_wait_tokens_expiry ON step_wait_tokens (expires_at) WHERE consumed_at IS NULL;

-- (3) Outbox
CREATE TABLE outbox_messages (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    topic         VARCHAR(255) NOT NULL,
    payload       JSONB NOT NULL,
    published_at  TIMESTAMPTZ,
    attempt_count INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_outbox_pending ON outbox_messages (created_at) WHERE published_at IS NULL;

-- (4) Inbox
CREATE TABLE inbox_messages (
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    message_id   VARCHAR(255) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, message_id)
);
```

Every table is **tenant-scoped**, consistent with the rest of the system. `UNIQUE (tenant_id, correlation_key)` guarantees a key is held by at most one waiting step.

> [!NOTE]
> `outbox_messages` and `inbox_messages` grow without bound. They need a cleanup job — `idempotency_keys` already sets the TTL precedent.

---

## 5. Inbox & Outbox — Traps #1 and #2

### Trap #2: the reply arrives too early → **Outbox**

Storing the token first and publishing afterwards is **not enough**, because this still leaks:

```
1. Token committed  ✅
2. Worker crashes   💥
3. Message never sent → run waits until timeout
```

The outbox closes it because both land in one transaction:

```go
uow.ExecuteInTx(ctx, func(txCtx context.Context) error {
    if err := waitTokenRepo.Create(txCtx, token); err != nil { return err }
    return outboxRepo.Enqueue(txCtx, msg)
})
```

A separate relay reads the outbox and publishes. The result is **atomic**, and the message cannot possibly be sent before its token exists — the relay only sees committed data.

> The existing `UnitOfWork` fits this exactly. Phase 3 already set the precedent: `auditRepo.Record` is written inside the publish transaction, for the identical reason.

### Trap #1: the message arrives twice → **Inbox**

> [!IMPORTANT]
> **Hard requirement: the inbox insert must share a transaction with the business state change.** Split them and the race merely moves:
>
> record inbox (commit) → crash → the step never becomes `succeeded` → redelivery is treated as a duplicate → the run hangs forever.

```go
uow.ExecuteInTx(ctx, func(txCtx context.Context) error {
    if err := inboxRepo.Record(txCtx, messageID); err != nil {
        return err                       // unique violation = duplicate, stop quietly
    }
    if err := stepRepo.MarkSucceeded(txCtx, stepRunID, payload); err != nil { return err }
    return waitTokenRepo.Consume(txCtx, correlationKey)
})
```

### The price

| | Consequence |
|---|---|
| Latency | A polling relay adds 100 ms–1 s. Reducible with Postgres `LISTEN/NOTIFY` |
| Tables | 2 new tables plus a cleanup job |
| Ordering | The outbox does not preserve inter-message order unless the relay is single-consumer per partition |

At this scale a **simple polling relay is sufficient** — do not reach for Debezium/CDC.

---

## 6. Trap #3: Orphan Events — No Infrastructure Solution

A message arrives, but no token matches it.

### Why the broker cannot help

| Kind of confirmation | Available? |
|---|---|
| Publish ack ("the broker received it") | ✅ Nearly every broker |
| Consumer ack ("the consumer processed it") | ✅ But sent to the **broker**, not to the publisher |
| End-to-end ("the right recipient handled it") | ❌ **No broker provides this** |

```
Producer → [Broker] → our listener → look up token → NOT FOUND  ← #3 happens here
              ▲
              └── the broker's guarantee stops here
```

When #3 occurs the **broker has worked perfectly** — the message was delivered and the listener received it. The problem is on our side.

Two features that sound relevant but still do not help:

- **RabbitMQ `mandatory` + `basic.return`** — returns a message that cannot be routed to any queue
- **NATS `ErrNoResponders`** — immediate error when there is no subscriber

Both answer *"there is no **subscriber**"*, not *"there is no **run** waiting"*. Our listener is alive and receiving, so neither ever fires.

### The actual solution, in priority order

**1. Tolerate early arrival.** The most frequent and least dangerous case: the reply lands a fraction of a second before the token commits.

```
token not found → DO NOT ack → let the broker redeliver (with backoff)
→ retry 2–3 times → still missing after ~30s → then dead-letter
```

**2. Wait timeout > the producer's retry window.** Easy to overlook: if `expires_at` is 5 minutes but the payment service retries for 10, a legitimate reply arrives after the token has expired — orphaned by our own doing.

**3. Dead-letter; never drop silently.** Store the full context: `correlation_key`, payload, arrival time, reason. This is usually what exposes a correlation-key bug.

**4. Metrics + alerting.** `orphan_events_total` must sit near zero. This is the only way #3 is ever detected — technically nothing "errors".

### For this stack (Asynq/Redis)

- Asynq ships retry and *archive* (dead-letter) — steps 1 and 3 are largely available
- Redis `XADD` returns a write confirmation — sufficient for the outbox relay
- What must be built here: steps 2 and 4

---

## 7. Phase Allocation

| Part | Phase | Size |
|---|---|---|
| CHECK constraint widening (`waiting`, node types, trigger types) | **Phase 4** (migration `000002`) | ~6 lines of SQL |
| `waiting` status in the state machine | **Phase 4** | ~10 lines |
| Readiness: a `waiting` predecessor keeps the successor `pending` | **Phase 4** | ~5 lines |
| Node type `EVENT_PUBLISH` + executor (case C) | **Phase 5** | Medium |
| Trigger `queue`/`grpc` + listener (case A) | **Phase 6** | Medium |
| Wait tokens + inbox + outbox + relay + sweeper (case B) | **Phase 7** (new) | Large |

### Placement principle: separate migrations from logic

> **Widen the constraint now, create the tables later.**

| | When | Why |
|---|---|---|
| **CHECK widening** (`step_runs.status`, `workflow_nodes.node_type`, `workflow_runs.trigger_type`) | **Phase 4**, though unused until Phases 5–7 | Widening a CHECK on an empty table is one line. Once production data exists it becomes an `ALTER TABLE` that locks and revalidates |
| **New tables** (`step_wait_tokens`, `outbox_messages`, `inbox_messages`) | Phase 7 | Purely additive — creating a table is cheap at any time and touches nothing existing |

> [!IMPORTANT]
> **Phase 4's executor narrowed this** (decision D-6 in `phase_4_workflow_engine_core.md` v3), and the reasoning was sound: no migration has ever been applied to any database, so there is no table to lock. Against that, a CHECK admitting `EVENT_PUBLISH` while no code can produce it stops being a guard in the phases between. Migration `000002` therefore carries **only** `workflow_edges.branch` and `step_runs.status`; `node_type` and `trigger_type` widening moves to the phases that introduce those values.

> [!WARNING]
> **A `.down.sql` for a CHECK widening is not trivial.** Narrowing a CHECK back **fails** if any row already holds a new value. The down migration must delete or rewrite those rows first, or be declared irreversible with the reason stated. Write it alongside the up-migration, not later.

---

## 8. Open Questions

To answer before case B is built:

1. **Source of `correlation_key`** — interpolated from the node config (e.g. `{{trigger.orderId}}`), or a generated UUID carried in the payload? Interpolation integrates more naturally with external systems but is more prone to typos.
2. **Which broker** — Asynq/Redis already covers the internal queue, but integrating with external systems may need Kafka/RabbitMQ/NATS. This determines the relay's shape.
3. **gRPC: server or client?** Receiving events over gRPC means running a gRPC server — a new component alongside `cmd/api` and `cmd/worker`.
4. **A cap on waiting runs per tenant**, so one tenant cannot fill the token table and starve the others.
