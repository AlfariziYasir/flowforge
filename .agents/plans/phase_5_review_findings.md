# Phase 5 (Worker Runtime) — Review Findings

Review of the execution of [phase_5_worker_runtime.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_5_worker_runtime.md).

**Verdict: 🔴 REJECTED — 2 high, 2 medium** (Y-1 … Y-4)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l` empty · `go test ./... -race -count=1` **15 packages ok** · integration suite green against a live Postgres.

> [!CAUTION]
> **Y-1 and Y-2 both undermine the phase's headline criterion** — *"a run cannot be executed twice by concurrent workers."* The lease that was built to prevent it is acquired and then never checked again, and an orphaned step turns `HandleRun` into a hot spin loop against the database.

---

## What Was Done Right — and it is a lot

**Step 0 genuinely happened.** After eleven rounds of deferral, this phase actually ran against real infrastructure:

```
flowforge-postgres   Up 44 minutes (healthy)
flowforge-redis      Up 42 minutes (healthy)

FLOWFORGE_INTEGRATION=1 go test ./internal/execution/
  TestRunClaim_ExactlyOneWinner              PASS (0.75s)   ← 16 workers × 50 iterations
  TestStepClaim_NoDoubleClaimNoStarvation    PASS (0.99s)
  TestReaper_ReclaimsExpiredLease            PASS (0.03s)
  TestRunClaim_DuplicateDeliveryIsNoOp       PASS (0.02s)
  TestStatusConstants_AcceptedByCheckConstraints PASS
  TestCoordinator_E2EExecution               PASS
  TestQueue_EndToEndRunExecution             PASS (1.51s)
```

Without `FLOWFORGE_INTEGRATION=1` these correctly **skip**, so the hardening from the earlier round is working as designed.

| Item | Result |
|---|---|
| **B-1** — SSRF design | ✅ Denylist after DNS resolution, IP pinned to the validated address, redirects re-validated, allowlist ignored when `productionLike`. Design is right; coverage has gaps — see **Y-4** |
| **B-2** — metrics cardinality | ✅ Counters carry `tenant_id`; `StepDuration`/`RunAge` histograms deliberately do not, with the reasoning in the file header |
| **D-1/D-5** — migration `000003` | ✅ `claimed_by`, `lease_expires_at`, `EVENT_PUBLISH`, plus a **partial index** for the reaper that the plan never asked for |
| `canceled` spelling | ✅ One `l`, matching `migration:119` |
| Engine purity | ✅ `internal/execution` never imports `internal/auth`; purity test untouched and passing |
| `CanTransition` gating | ✅ Used in 5 places in the coordinator |
| Liveness check | ✅ `finishRun` marks a stuck run failed rather than hanging — the Phase 4 hand-off note was acted on |
| Goroutine ownership | ✅ `errgroup.SetLimit`, and the heartbeat's LIFO `defer stopHB()` / `defer hbWG.Wait()` ordering is correct and commented |

---

## 🔴 High

### Y-1: The run lease is acquired but never enforced

[repository.go:196-210](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository.go#L196-L210) · [coordinator.go:152-160](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/coordinator.go#L152-L160)

`ClaimRun` correctly records `claimed_by = $WorkerID`. Nothing afterwards ever checks it.

```go
func (r *executionRepository) ExtendLease(ctx, tenantID, runID, lease) error {
    psql.Update("workflow_runs").
        Set("lease_expires_at", sq.Expr("NOW() + ?::interval", lease.String())).
        Where(sq.Eq{"tenant_id": tenantID, "id": runID, "status": domain.RunStatusRunning})
        //     ↑ no claimed_by predicate
}
```

And `Tick` re-reads the run but only asks whether it is still `running`:

```go
if run.Status != domain.RunStatusRunning {
    return false, nil
}
// ← never checks run.ClaimedBy == c.cfg.WorkerID
```

`WorkerID` exists on the coordinator config and is passed to `ClaimRun`. The identity is available — it is simply never used again.

**The failure sequence**, all of it reachable with a 60-second network partition (the default `RUN_LEASE_DURATION`):

1. Worker A claims run R. Heartbeat starts.
2. A loses the database for longer than the lease. `ExtendLease` logs a warning and **continues** — the heartbeat has no notion of having lost the lease.
3. The reaper sets R back to `pending`, clears `claimed_by`, re-enqueues.
4. Worker B claims R. `claimed_by = 'worker-B'`, status `running`.
5. A's connectivity returns. Its next `ExtendLease` **succeeds** — it now extends *B's* lease, because there is no owner predicate.
6. A's next `Tick` sees `status == running` and carries on.

Both workers now advance R, and A is actively keeping B's lease alive. This is the classic missing-fencing-token problem: a lease that is acquired but not validated on use is not a lease.

`SKIP LOCKED` on step claims stops them grabbing the *same* step simultaneously, so this is not immediately catastrophic — but both workers call `MarkStepsReady`, `MarkStepsSkipped`, and `finishRun`, and the run's step set is mutated from two places with interleaved scope rebuilds.

---

### Y-2: An orphaned `running` step makes `HandleRun` spin at full speed

[coordinator.go:112-127](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/coordinator.go#L112-L127) · [coordinator.go `finishRun`](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/coordinator.go)

```go
for {
    if err := ctx.Err(); err != nil { return err }
    more, err := c.Tick(ctx, tenantID, runID)
    if err != nil { ...; return nil }
    if !more { return nil }
}                                   // ← no sleep, no backoff
```

`finishRun` returns `more = true` whenever any step is `running`, `ready`, `waiting`, or `retrying`:

```go
if anyActive {
    return true, nil // a concurrent worker is still advancing this run
}
```

A step stuck in `running` with nobody executing it therefore produces: `Tick` → `executeWave` claims nothing (no `ready` rows) → `didWork = false` → `finishRun` → `anyActive` → `more = true` → **loop immediately**.

Each iteration issues `GetRun` + `loadGraph` + `ListStepRuns` + `ClaimReadySteps` + `ListStepRuns` again. That is a tight loop at 100% CPU hammering Postgres with five queries per revolution, for as long as the worker lives.

**How the orphan arises: Y-3.**

---

## 🟡 Medium

### Y-3: The reaper reclaims runs but orphans their `running` steps

[repository.go `ReclaimExpiredLeases`](file:///home/mohyasiralfarizi/Golang/flowforge/internal/execution/repository.go)

```sql
UPDATE workflow_runs
   SET status = 'pending', claimed_by = NULL, lease_expires_at = NULL
 WHERE status = 'running' AND lease_expires_at < NOW()
```

`step_runs` is never touched. A step that was mid-execution when its worker died stays `running` forever — no worker will ever claim it (claims target `status = 'ready'`), and `engine.CalculateReadyNodes` treats `running` as *blocking*, so nothing downstream unblocks either.

The plan's §6 stated the intended behaviour explicitly:

> *"a step recorded `running` at reclaim time is retried, and `attempt_count` makes the retry visible"*

That retry is not implemented. The consequence is Y-2's spin, and a run that can never reach a terminal state on its own.

### Y-4: SSRF denylist misses ranges that matter in cloud environments

[ssrf.go:96-99](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/safehttp/ssrf.go#L96-L99)

`validateIP` covers loopback, `IsPrivate()` (RFC 1918 + RFC 4193), link-local unicast, multicast, and unspecified. Probed against a wider set:

```
127.0.0.1         loopback                     BLOCKED ✓
169.254.169.254   cloud metadata               BLOCKED ✓
10.0.0.1          RFC1918                      BLOCKED ✓
192.168.1.1       RFC1918                      BLOCKED ✓
172.16.0.1        RFC1918                      BLOCKED ✓
::1               IPv6 loopback                BLOCKED ✓
fc00::1           IPv6 ULA                     BLOCKED ✓
0.0.0.0           unspecified                  BLOCKED ✓
::ffff:127.0.0.1  IPv4-mapped loopback         BLOCKED ✓
─────────────────────────────────────────────────────────
100.64.0.1        CGNAT (RFC 6598)             ALLOWED ✗
0.0.0.1           0.0.0.0/8 non-zero           ALLOWED ✗
192.0.0.1         IETF protocol assignments    ALLOWED ✗
198.18.0.1        benchmarking (RFC 2544)      ALLOWED ✗
64:ff9b::7f00:1   NAT64 embedding 127.0.0.1    ALLOWED ✗
8.8.8.8           public                       ALLOWED ✓
```

**All nine addresses the plan's Q-13 named are blocked** — the spec was met. The gap is that Q-13 was too narrow.

`100.64.0.0/10` is the one that matters: AWS EKS pod networking, GCP, and several managed-Kubernetes providers place internal service endpoints there. For a product heading to real use, that range is reachable-and-sensitive in exactly the way RFC 1918 is.

The others are lower risk — `0.0.0.0/8` is usually unroutable, `192.0.0.0/24` and `198.18.0.0/15` are rarely live, and NAT64 requires a NAT64 gateway on the path — but they are all one line each to close.

---

## Notes, not findings

- **`resolveAndPin` and `ValidateHostname` disagree on multi-address hosts.** `ValidateHostname` fails closed if *any* resolved address is non-public; `resolveAndPin` skips bad addresses and dials the first good one. Both are individually safe — `resolveAndPin` only ever dials a validated IP — but the struct's doc comment states the stricter policy, so a reader will believe the wrong one. Worth aligning the comment or the code.
- **Y-1's blast radius is limited by `SKIP LOCKED`**, which is why this is high rather than critical: two workers cannot claim the same step at the same instant. The exposure is interleaved run-level mutation, not simultaneous double side-effects.

---

## Remediation

See [phase_5_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_5_review_remediation_plan.md). Y-1, Y-2 and Y-3 are one connected story — the lease, the reaper, and the loop — and should be fixed together.
