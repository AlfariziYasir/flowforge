# Phase 4 (Workflow Engine Core) — Review Findings

Review of the execution of [phase_4_workflow_engine_core.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_workflow_engine_core.md) v3.

**Verdict: 🔴 REJECTED — 1 high, 1 medium, 2 low** (W-1 … W-4)

Baseline: `go build ./...` clean · `go vet ./...` clean · `gofmt -l` empty · `go test ./... -race -count=1` **all packages ok**.

> [!CAUTION]
> **W-1: the if/else-then-join pattern deadlocks permanently.** This is the most common shape a conditional workflow takes. The join node is never `ready` and never `skipped` — the run hangs with no error. Reproduced with a runnable test.

---

## What Was Done Correctly

The execution is careful and mostly on target. Verified by running, not by reading:

| Item | Result |
|---|---|
| **D-1 / B-5** — `branch` column | ✅ Both call sites covered: `ReplaceGraph` insert list (`repository.go:477`) **and** `LoadGraph` select list (`:512`) |
| **B-6** — snapshot normalisation | ✅ `Graph.Normalize()` present; `FromPersisted` maps `""` → `"default"`; the handler calls it after decoding |
| **D-3** — `expr` sandbox | ✅ `expr.MaxNodes(1000)`, no fake timeout. `ctx.Err()` checked before compile and before run — exactly as v3 specified after the B-1/B-2 corrections |
| **B-4** — `ExprEnv` | ✅ Lowercase keys, `Steps` rendered as nested maps, nil maps guarded |
| **D-6 (narrowed)** — migration | ✅ `000002` carries only `branch` + `step_runs.status`. `node_type` and `trigger_type` are **not** widened — exactly as v3 narrowed it |
| **`.down.sql`** | ✅ Present, clean reversal, the "no production data" assumption stated in a comment |
| **D-4 / D-5** — statuses | ✅ `StepStatusWaiting` present; `CanTransition` correct: `running → waiting` legal, `waiting → {succeeded,failed,skipped}`, `waiting` not terminal |
| **D-7** — timeout | ✅ `TimeoutPolicy.EffectiveTimeout` with a 15-minute `MaxStepTimeout` |
| **Test coverage** | ✅ Nearly all of P-1…P-22 landed — 68 functions/subtests across `engine` + `domain` |

**P-22b verified manually** — I injected `net/http` into `scope.go`, and the guard bit:

```
purity_test.go:63: scope.go imports "net/http", which breaks the engine purity contract
FAIL
```

---

## 🔴 High

### W-1: The if/else-then-join pattern deadlocks permanently

[readiness.go:71-136](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L71-L136)

The most common shape a conditional workflow takes:

```
A(CONDITION) --true--> B --default--> D
             --false--> C --default--> D
```

Stepped through:

```
after A succeeds(true)    → ready=[b]  skipped=[c]     ✅ correct
after B succeeds, C skip  → ready=[]   skipped=[]      ❌ D vanishes
```

Node `d` is **never ready and never skipped**. Because `c` is already `skipped` — a terminal status — nothing will ever change the state again. The run hangs forever, with no error and no symptom.

**The cause** is in how predecessors are classified. A `skipped` predecessor sets `allPredecessorsSucceeded = false` ([:90](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L90)) but its edge is not treated as dead, so:

- [:128](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L128) `allInboundEdgesDead` → `false` (B's edge is live) → **not** skipped
- [:134](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L134) `allPredecessorsSucceeded` → `false` (C was skipped) → **not** ready

The node falls between the two branches and goes nowhere.

The underlying problem: the code uses **two mutually inconsistent flags** (`allPredecessorsSucceeded` and `allInboundEdgesDead`) to answer a single question. A `skipped` predecessor must make its edge **dead**, not **blocking**.

> [!NOTE]
> **Why the tests missed it.** Coverage is genuinely complete — `TestReadiness_ConditionBranching` (P-18) exists and passes. But it stops one step short: it only checks that the `false` branch is skipped, and never advances to the join node beyond it. This gap is in the **plan**, not the execution — P-18 asked for exactly that much and no more.

---

## 🟡 Medium

### W-2: A `CONDITION` with no `output["result"]` silently skips both branches

[readiness.go:154-165](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L154-L165)

```
CONDITION succeeded without "result" → ready=[] skipped=[b c] err=<nil>
```

`conditionResult` returns `(false, false)` when `result` is absent. Because `ok == false`, `branchTaken` stays `false` for **both** edges, so both are treated as dead and the entire branch is skipped — **with no error**.

But a `CONDITION` node in status `succeeded` that produced no `result` is an **executor bug**, not a valid state. The effect is that a whole branch of the workflow disappears silently, with nothing an operator could see.

This is the same class as "trap #3" in the event-driven design: technically nothing errors, but the outcome is wrong.

---

## 🟢 Low

### W-3: The doc comment contradicts the implementation

[readiness.go:13-14](file:///home/mohyasiralfarizi/Golang/flowforge/internal/engine/readiness.go#L13-L14)

> *"A node is ready when it is pending, every predecessor has succeeded, and the edge branch connecting it to **each** predecessor was taken."*

The implementation does not do this — it requires only **at least one** live edge. For an ordinary join (all edges `default`) the two are equivalent, so the difference is invisible; it only surfaces on branching, which is precisely the W-1 case.

Once W-1 is fixed the semantics need stating precisely: **OR-join over dead branches, AND-join over readiness.** A node runs when there is a completed live path and no path still in motion.

### W-4: `graph_snapshot` is never read back

```
grep -rn "GraphSnapshot" internal | grep -iE "unmarshal|decode"
→ (empty)
```

`GetVersion` always assembles the graph from `LoadGraph`, including for `published` versions. The snapshot is written and never read.

As a result B-6 (normalising on snapshot decode) guards a path that does not exist — harmless, but it delivers nothing yet. More importantly: **the checksum and snapshot that P-2 protects guard a value nothing ever reads back.** If the `workflow_nodes` rows of a published version were altered, `GetVersion` would return a graph differing from its snapshot and nobody would know.

This is inherited from Phase 3 (finding G-9 asked for published versions to read the snapshot), not introduced by Phase 4. Recorded so it does not get lost.

---

## Remediation

See [phase_4_review_remediation_plan.md](file:///home/mohyasiralfarizi/Golang/flowforge/.agents/plans/phase_4_review_remediation_plan.md). **W-1 blocks Phase 5** — the worker will be built directly on `CalculateReadyNodes`, and a run stuck this way offers no symptom to diagnose.
