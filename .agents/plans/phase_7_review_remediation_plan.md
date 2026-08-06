# Implementation Plan — Phase 7 Review Remediation

Closes the 3 findings in [phase_7_review_findings.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_7_review_findings.md) — 2 high (AB-1, AB-2), 1 medium (AB-3).

**AB-1, AB-2, and AB-3 are independent** — different files, different mechanisms, no shared root cause. Fix in any order; listed here in severity order.

---

## Phase AN — NATS Subscriber Resilience (AB-1)

### Step 1 — test first, it must be RED

#### [MODIFY] [eventbus_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventbus/eventbus_test.go) or a new `nats_test.go` in the same package

- ★ **`TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing`** — using a fake `EventHandler` (or the real NATS integration harness, matching `nats_integration_test.go`'s existing pattern): publish one message that fails `handleNATSMessage` (bad signature, or a fake handler that errors), then a second, valid message. Assert the second message is still processed — the handler's `HandleEvent` is called for it, or (integration form) the matching wait token is consumed. Red today: `Subscribe` returns after the first message and the second is never fetched.

### Step 2 — the fix

#### [MODIFY] [nats.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/eventbus/nats.go)

Per-message failure must never stop the loop — only a fetch-level or context-level failure should. Log and continue on a per-message error; do not ack (so JetStream redelivers per the existing, correct comment), but keep pulling:

```go
for {
    if err := ctx.Err(); err != nil {
        return nil
    }
    msgs, err := sub.Fetch(10, nats.MaxWait(2*time.Second))
    if err != nil {
        if err == nats.ErrTimeout {
            continue
        }
        return fmt.Errorf("nats subscribe: fetch: %w", err)
    }
    for _, m := range msgs {
        if err := handleNATSMessage(ctx, m, secret, handler); err != nil {
            log.Error("nats message handling failed, leaving unacked for redelivery",
                slog.String("subject", m.Subject), slog.Any("error", err))
            continue // NOT return — one bad message must not stop the subscriber
        }
        _ = m.Ack()
    }
}
```

This needs a `*slog.Logger` threaded into `Subscribe` (it currently has none — the caller only logs `Subscribe`'s own terminal return value). Add a `log *slog.Logger` parameter; `cmd/worker/main.go` passes the process logger it already has.

A `Fetch`-level or `ctx`-level error (NATS connection dropped, subscription itself failed) is a different class of problem than "one message was garbage" — that case legitimately should end the loop and let the caller decide whether to reconnect/retry, so that branch's existing `return` behavior is correct and unchanged.

> [!NOTE]
> Consider also wrapping the goroutine in `cmd/worker/main.go` with a bounded restart loop (e.g. reconnect with backoff if `Subscribe` does return an error) so a genuine connection loss self-heals rather than requiring a manual restart — this is a smaller, separate hardening on top of the per-message fix above, worth doing in the same phase since it's the same resilience concern one level up.

### Verification — Phase AN

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ ./internal/platform/eventbus/... -run 'TestSubscribe_OneBadMessageDoesNotStopSubsequentProcessing|TestNATS_EventResolvesWaitToken' -v
# both green

# mutation: revert the per-message `continue` back to `return err`
# the new test MUST fail
```

---

## Phase AO — Wait Token Lifecycle (AB-2)

The unique constraint's purpose — preventing two *simultaneously active* tokens from colliding on the same correlation key — is correct and should stay. What's missing is any notion that a token's useful life ends at consumption or expiry-handling.

### Step 1 — tests first, they must be RED

#### [MODIFY] [repository_test.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository_test.go) *(integration)*

- ★ **`TestCreateToken_ReusesCorrelationKeyAfterConsumption`** — create + consume a token for key K; create a second token for the same tenant+K. Must succeed. Red today: unique-constraint violation, exactly as this review's probe demonstrated.
- ★ **`TestSweeper_AdvancesPastAlreadyHandledExpiredTokens`** — seed >100 already-expired, already-`MarkWaitingStepFailed`'d tokens (their steps already `failed`, not `waiting`), plus one more recently-expired token whose step is still genuinely `waiting`. Run `SweepExpiredWaitTokens` twice (simulating two ticks). Assert the genuinely-waiting token's step gets marked failed and its run woken — i.e., the sweeper made progress past the backlog. Red today: `FindExpiredTokens`'s `LIMIT 100 ORDER BY expires_at` never returns the newer row while ≥100 older, already-handled rows exist.

### Step 2 — the fix

#### [MODIFY] [migrations/000006_wait_token_cleanup.up.sql](file:///home/mohyasiralfarizi/Golang/flowforge/migrations) *(new)*

Change the constraint to a **partial** unique index scoped to active (unconsumed, unexpired-in-the-relevant-sense) tokens only — a consumed or already-swept token should not occupy the slot:

```sql
ALTER TABLE step_wait_tokens DROP CONSTRAINT uq_wait_token;
CREATE UNIQUE INDEX uq_wait_token_active ON step_wait_tokens (tenant_id, correlation_key)
    WHERE consumed_at IS NULL;
```

This alone fixes the "consumed tokens block reuse" half of AB-2 (a consumed token no longer occupies the unique slot). It does **not** by itself fix the "expired-and-failed" half — those rows still have `consumed_at IS NULL` (they were never consumed, they expired) and would still collide under a `WHERE consumed_at IS NULL` partial index. Two decisions, pick one per row-class:

- **(a) Mark expired-and-handled tokens consumed too**, semantically "this token's lifecycle is over," via a new `handled_at` column separate from `consumed_at` (so "resolved by a real event" and "expired away" stay distinguishable for observability), with the partial index changed to `WHERE consumed_at IS NULL AND handled_at IS NULL`. **Recommended** — keeps the audit trail (you can still tell an expired token from a successfully-consumed one) while freeing the correlation key.
- **(b) Delete expired-and-handled token rows outright** after the sweeper processes them. Simpler, but destroys the "this correlation key was tried and timed out" history `orphan_events`-style debugging would want.

Recommend (a). Add the column in the same migration:

```sql
ALTER TABLE step_wait_tokens ADD COLUMN handled_at TIMESTAMPTZ;
DROP INDEX uq_wait_token_active;
CREATE UNIQUE INDEX uq_wait_token_active ON step_wait_tokens (tenant_id, correlation_key)
    WHERE consumed_at IS NULL AND handled_at IS NULL;
```

#### [MODIFY] [repository.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository.go)

- `FindExpiredTokens`'s WHERE clause gains `AND handled_at IS NULL` alongside the existing `consumed_at IS NULL`, so a token the sweeper already dealt with never resurfaces.
- The sweeper's per-token loop (`SweepExpiredWaitTokens` in [sweeper.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/sweeper.go)) sets `handled_at = NOW()` on the token row in the same statement/transaction as `MarkWaitingStepFailed` — new repository method `MarkTokenHandled(ctx, tokenID) error`, called right after (or merged into) the existing `MarkWaitingStepFailed` call. Must happen even on the `ErrStepNotFound` branch (a token whose step already moved on is exactly as "done" as one this sweep just failed) — otherwise the same starvation bug reappears for tokens that raced with something else.

#### [MODIFY] [event_service.go](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/event_service.go)

`ConsumeTokenIfUnconsumed`'s existing UPDATE should also set `handled_at = NOW()` (a consumed token is handled by definition) so both retirement paths converge on the same column — check `repository.go`'s `ConsumeTokenIfUnconsumed` (line 837) and add it there rather than as a second write.

### TDD — additions beyond the two ★ cases

- `TestConsumeTokenIfUnconsumed_SetsHandledAt` — direct repository assertion.
- `TestSweeper_MarksHandledOnErrStepNotFound` — a token whose step already left `waiting` (raced with a real event) still gets `handled_at` set, not left to resurface forever.

### Verification — Phase AO

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/... -race -count=1

# mutation: revert the partial index back to the plain UNIQUE(tenant_id, correlation_key)
# TestCreateToken_ReusesCorrelationKeyAfterConsumption MUST fail

# mutation: remove the handled_at write from the sweeper's failure path
# TestSweeper_AdvancesPastAlreadyHandledExpiredTokens MUST fail

grep -n "uq_wait_token_active" migrations/000006_wait_token_cleanup.up.sql
```

Guarantee that must not be lost: `CountActiveTokens`'s per-tenant cap query (`WHERE consumed_at IS NULL`) needs the same `AND handled_at IS NULL` addition — otherwise expired-and-handled tokens would keep counting against a tenant's cap forever, a smaller version of the same starvation bug applied to `MaxPerTenant` instead of the sweeper's `LIMIT`.

---

## Phase AP — NATS Startup Reliability (AB-3)

#### [MODIFY] [docker-compose.yml](file:///home/mohyasiralfarizi/Golang/flowforge/docker-compose.yml)

- Enable the monitoring endpoint the healthcheck already expects, and publish it:

  ```yaml
  nats:
    command: ["-js", "-m", "8222"]
    ports:
      - "4222:4222"
      - "8222:8222"
  ```

- Add `worker`'s missing dependency, now that the healthcheck can actually pass:

  ```yaml
  worker:
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
      nats:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
  ```

#### [MODIFY] [cmd/worker/main.go](file:///home/mohyasiralfarizi/Golang/flowforge/cmd/worker/main.go)

Even with compose-level ordering fixed, a bare `nats.Connect` is still a single-attempt call — any other deployment target (Kubernetes, bare process restart order) has no such guarantee. Add retry options so the client itself is resilient to NATS not being up yet, independent of orchestration:

```go
nc, natsErr := nats.Connect(cfg.NATSURL,
    nats.RetryOnFailedConnect(true),
    nats.MaxReconnects(-1), // unlimited — keep trying for the life of the process
    nats.ReconnectWait(2*time.Second),
)
```

With `RetryOnFailedConnect(true)`, `nats.Connect` itself blocks/retries according to `MaxReconnects`/`ReconnectWait` rather than failing on the first attempt — the existing `if natsErr != nil { log.Warn(...) }` fallback path becomes reachable only after retries are genuinely exhausted (or immediately, if `MaxReconnects` is finite — with `-1` it effectively never gives up, matching "NATS is optional but should self-heal" better than "NATS is optional and gives up instantly").

### Verification — Phase AP

```
docker compose up -d nats
sleep 10
docker inspect flowforge-nats --format '{{.State.Health.Status}}'
# must print "healthy", not "unhealthy"

docker compose up -d worker
docker compose logs worker | grep -i nats
# must show a successful connection, not "nats unavailable, NATS ingress disabled"
```

---

## Execution Order & Verification

| Phase | Findings | Gate |
|---|---|---|
| AN — NATS subscriber resilience | AB-1 | ★ test red → green; mutation fails |
| AO — Wait token lifecycle | AB-2 | 2 ★ tests red → green; both mutations fail; `CountActiveTokens` guarantee re-checked |
| AP — NATS startup reliability | AB-3 | Healthcheck passes live; worker connects on a cold `docker compose up` |

After each phase: `make ci` **and** `FLOWFORGE_INTEGRATION=1 make test-integration` (now genuinely exercising NATS — confirm `docker compose ps nats` shows `healthy` before running it, per Phase AP), both green, **before** the execution entry is appended to `.agents/memory/action_history.md` — **not** `.agents/memory/action-log.md**` (see Notes below).

Guarantees that must not be lost:

```
FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestNATS_EventResolvesWaitToken -count=1
# the phase's own happy-path proof must stay green throughout

FLOWFORGE_INTEGRATION=1 go test ./internal/execution/ -run TestRunClaim_ExactlyOneWinner -count=1
# Phase 5's headline guarantee, untouched by any of this
```

---

## Notes for Phase 8

- **Consolidate `.agents/memory/action-log.md` into `.agents/memory/action_history.md`.** This phase's execution was recorded in a new file instead of the established one every prior phase used. Move its content into `action_history.md` (as a dated entry, matching the existing format) and delete `action-log.md`, so the project keeps a single execution history rather than two.
- **The gRPC signing-over-re-marshaled-bytes fragility** (noted, not scored, in the findings doc) is worth hardening before `DeliverEventRequest` grows any field type protobuf doesn't guarantee stable re-encoding for (maps, in particular). A cheap fix: sign over `payload_json` and `correlation_key` concatenated with a fixed separator, computed directly from the Go struct fields rather than `proto.Marshal`'s output — sidesteps the determinism question entirely.
- D-4's placeholder contract status is unchanged and still the largest source of future rework, exactly as the original plan flagged.
