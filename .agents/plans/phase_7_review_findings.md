# Phase 7 (Event-Driven Steps, v2 Multi-Protocol) — Review Findings

Review of the execution of [phase_7_event_driven_steps.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_7_event_driven_steps.md).

**Verdict: 🔴 REJECTED — 2 high, 1 medium** (AB-1, AB-2, AB-3)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l ./cmd ./internal` empty · `go test ./... -race -count=1` clean (all packages, including the three new ones: `webhookauth`, `eventbus`, `eventbus/eventspb`).

> [!CAUTION]
> **AB-1 and AB-2 both undermine the phase's actual deliverable.** The plan exists to make `EVENT_PUBLISH`/`EVENT_WAIT` a real, reliable mechanism instead of an inert stub. AB-1 means the NATS transport — one of the two new protocols this phase's user explicitly asked for — dies permanently the first time it sees a bad message, with no self-healing. AB-2 means the wait-token mechanism, once used, permanently blocks reuse of its own correlation key within a tenant — silently breaking Phase 6's retry feature for exactly the workflows this phase was built to support. Both were proven live, not inferred from reading.

---

## What Was Done Right — and it is substantial

- **`webhookauth.VerifyHMAC` is correct and shared.** Constant-time (`hmac.Equal`, not `==`), hex-decodes the signature, rejects empty secret/sig, and is genuinely the only place signature comparison logic exists — HTTP, gRPC, and NATS all call through it.
- **`HandleEvent`'s dedup is correctly race-safe.** `FindTokenByCorrelationKey`'s pre-check is an optimization; the real guarantee is `ConsumeTokenIfUnconsumed`'s atomic `UPDATE ... WHERE consumed_at IS NULL` inside a transaction, with `MarkWaitingStepSucceeded`'s `ErrStepNotFound` correctly treated as "already moved on, not an orphan" rather than an error.
- **The coordinator's `waiting` transitions are properly gated.** Both `engine.CanTransition` (step level) and the new `canTransitionRun` (run level) are checked before every write; no goroutine is held for the wait duration — `runStepWithRetry` returns immediately after parking, exactly as designed.
- **The three ingress adapters are genuinely symmetric.** `TestGRPC_ServerAndClientRoundTrip` and the NATS/HTTP paths all converge on the same `HandleEvent` port with identical semantics — no transport-specific business logic leaked past the adapter boundary.
- **`Router.Publish`'s default is preserved.** `transport==""` still calls `Internal` unchanged — a pre-Phase-7 `EVENT_PUBLISH` node compiles and runs identically.
- **The NATS/gRPC goroutines are correctly owned during normal operation** — `natsWG`/`WaitGroup` discipline in `cmd/worker` mirrors `reaperLoop`'s pattern from Phase 5, and the gRPC listener shuts down on the same signal handler as the HTTP server.
- **NATS is treated as optional infrastructure at startup**, not a hard dependency — a `worker` process without NATS reachable still runs the rest of its job (internal queue, HTTP/gRPC transports unaffected). This is the right instinct; see AB-3 for where its execution falls short.

---

## 🔴 High

### AB-1: A single bad NATS message permanently kills the entire subscriber

[nats.go:65-98](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventbus/nats.go#L65-L98)

```go
for _, m := range msgs {
    if err := handleNATSMessage(ctx, m, secret, handler); err != nil {
        // Do not ack: JetStream redelivers. The errgroup/loop keeps going.
        // We log via the caller; the message stays in the stream.
        return err
    }
    _ = m.Ack()
}
```

The comment says "the errgroup/loop keeps going." The code does the opposite: `return err` exits `Subscribe` **entirely** — not just the current message, the whole function — on *any* failure from `handleNATSMessage`: invalid `Tenant-Id` header, a signature that fails `VerifyHMAC`, malformed JSON, or any transient error from `HandleEvent`/`RecordOrphanEvent`.

`cmd/worker/main.go` wraps the call in a plain goroutine with no restart logic:

```go
go func() {
    defer natsWG.Done()
    if err := eventbus.Subscribe(natsCtx, nc, "flowforge.events.>", "flowforge-worker", secretGetter, eventService); err != nil {
        log.Error("nats subscriber exited", slog.Any("error", err))
    }
}()
```

One log line, then silence — the subscriber goroutine is dead for the rest of the process's life. Confirmed live:

```
1. Publish a message with an invalid signature (no valid tenant secret needed to trigger this).
2. Publish a second, correctly-signed, valid message for a real wait token.
→ Subscribe() returned/exited with err="nats: invalid signature for tenant ..."
→ the second, valid message was NEVER processed — the token stayed unconsumed.
```

**Any actor with network access to the NATS server can permanently disable event-driven step resolution for every tenant on that worker**, with a single malformed or unsigned message and zero authentication required to do so (the signature check happens *inside* the function that kills the loop). Recovery requires noticing the log line and restarting the worker process.

### AB-2: Wait tokens are never deleted — a correlation key becomes permanently unusable within a tenant after first use, breaking Phase 6 retries

[migrations/000005_event_driven_steps.up.sql:27](file:///home/mohyasiralfarizi/Golang/flowforge/migrations/000005_event_driven_steps.up.sql#L27) · [repository.go:784-799](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository.go#L784-L799) · [repository.go:873-901](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository.go#L873-L901)

```sql
CONSTRAINT uq_wait_token UNIQUE (tenant_id, correlation_key)
```

This is a full (non-partial) unique constraint — it applies to consumed and expired tokens exactly as much as active ones, and **nothing in this codebase ever deletes a row from `step_wait_tokens`**. Once a correlation key has been used once for a tenant — consumed successfully, or expired and failed — no future `EVENT_WAIT` step, in any run, can ever create a new token with that same key again. `CreateToken` just wraps the DB error generically; there is no retry-with-suffix, no cleanup job, no partial-index scoping to unconsumed tokens.

Confirmed live with the exact scenario Phase 6 exists to support:

```
1. Run A: EVENT_WAIT with correlationKey "ORDER-RETRY-1" — token created, then consumed (event arrived, step succeeded).
2. Run A later fails at a downstream step; the operator calls POST .../retry (Phase 6).
3. The retried run B replays the same graph with the same input — its EVENT_WAIT step
   recomputes the SAME correlation key "ORDER-RETRY-1" (interpolated from the same input,
   exactly as Phase 6's RetryRun documents preserving InputContext for this reason).
4. Run B's EVENT_WAIT step calls CreateToken → 
   "insert wait token: ERROR: duplicate key value violates unique constraint uq_wait_token"
```

Correlation keys are, by design, deterministic business identifiers (`"order-{{input.orderId}}"`, `"payment-{{input.paymentRef}}"`) — the exact property that makes them useful for external systems to address. That same property guarantees a retry of any run that got as far as creating its `EVENT_WAIT` token will **permanently and deterministically fail** on retry, forever, for that specific run — not a transient failure, not something `MaxAttempts` retries past, since every attempt hits the same unique-constraint violation. This silently defeats Phase 6's retry feature for the exact workflow shape (event-driven, multi-step) Phase 7 was built to unblock.

**A second, independent symptom of the same root cause — the sweeper can starve behind its own unprocessed backlog:**

```go
func (r *executionRepository) FindExpiredTokens(ctx context.Context, limit int) ([]domain.StepWaitToken, error) {
    ...
    Where("consumed_at IS NULL").Where(sq.Lt{"expires_at": sq.Expr("NOW()")}).
    OrderBy("expires_at").
    Limit(uint64(limit)).  // default 100
```

`SweepExpiredWaitTokens` calls `MarkWaitingStepFailed`, which correctly no-ops (`ErrStepNotFound`, handled via `continue`) once a token's step is already `failed` from a prior sweep — but the **token row itself is never marked consumed and never deleted**. So `FindExpiredTokens` returns the exact same already-handled tokens on every subsequent sweep tick, forever, ordered oldest-first. Once more than `limit` (100) expired-and-never-cleared tokens accumulate across the system's lifetime, `ORDER BY expires_at LIMIT 100` can **never advance past them** — any newer token that expires after that point is permanently unreachable by the sweeper, because the same 100 stale rows always sort first.

---

## 🟡 Medium

### AB-3: The NATS service's healthcheck is broken, and nothing waits for it — a real startup race that can silently and permanently disable NATS for a worker's lifetime

[docker-compose.yml:36-49](file:///home/mohyasiralfarizi/Golang/flowforge/docker-compose.yml#L36-L49) · [docker-compose.yml:117-135](file:///home/mohyasiralfarizi/Golang/flowforge/docker-compose.yml#L117-L135) · [cmd/worker/main.go:104-131](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/worker/main.go#L104-L131)

```yaml
nats:
  command: ["-js"]
  ports: ["4222:4222"]
  healthcheck:
    test: ["CMD-SHELL", "wget -qO- http://localhost:8222/healthz || exit 1"]
```

`command: ["-js"]` enables JetStream but never enables the HTTP monitoring endpoint (`-m 8222` or `--http_port`), and port 8222 isn't published either. Confirmed live: the container has been running for 35+ minutes with **311 consecutive failing health checks** (`wget: can't connect to remote host: Connection refused`) — the NATS server itself is healthy and accepting client connections on 4222 the whole time; only the healthcheck's target port was never started.

This compounds with two more facts, both confirmed by reading `cmd/worker/main.go` and `docker-compose.yml` directly:

1. `worker`'s `depends_on` lists `postgres`, `redis`, `migrate` — **not `nats`**, at any condition.
2. `nc, natsErr := nats.Connect(cfg.NATSURL)` is called with **no options** — no `nats.RetryOnFailedConnect()`, no `nats.MaxReconnects()`. The `nats.go` client performs exactly one synchronous dial attempt at call time by default; auto-reconnect only ever activates *after* an initial successful connection, never for the first attempt.

Put together: on a cold `docker-compose up`, there is no ordering guarantee between `nats` finishing its JetStream storage initialization and `worker` reaching its single, non-retrying `Connect` call. If `worker` wins that race, NATS ingress and egress are silently disabled for the rest of that process's life — one `Warn` log line, no crash, no retry, and the broken healthcheck means even *adding* `depends_on: nats: condition: service_healthy` today would just hang `worker` forever rather than fix the race.

---

## Notes, not findings

- **The gRPC auth interceptor signs over `proto.Marshal(req)`** — the request re-serialized *after* being unmarshaled from the wire, not the original bytes the client sent. `google.golang.org/protobuf`'s own documentation explicitly disclaims marshal-output stability as an API guarantee; using it as the basis for a cryptographic signature is a known fragile pattern. It works today (`TestGRPC_ServerAndClientRoundTrip` passes) because `DeliverEventRequest` is a flat two-field message with no maps or repeated fields, where this implementation's output happens to be stable in practice. Worth hardening before the contract grows a map field or the protobuf runtime version changes — not scored as a finding since nothing demonstrates it broken today.
- **The executor recorded this phase's work in a new `.agents/memory/action-log.md`** instead of appending to the established `.agents/memory/action_history.md` every prior phase in this project has used. Not a code defect, but it splits the project's single execution history across two files going forward unless corrected.
- **D-4's placeholder wire contract is exactly as provisional as the plan says it is** — nothing here evaluates it against a real external system's spec, since none was provided. Not in scope for this review.

---

## Remediation

See [phase_7_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_7_review_remediation_plan.md). AB-1 and AB-2 are independent of each other and of AB-3 — none share a root cause — and can be fixed in any order.
